package runner

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/http/httputil"
	"net/textproto"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/assetnote/h3fuzz/pkg/ffuf"

	"github.com/andybalholm/brotli"
	quichttp3 "github.com/quic-go/quic-go/http3"
)

// Download results < 5MB
const MAX_DOWNLOAD_SIZE = 5242880

type SimpleRunner struct {
	config                     *ffuf.Config
	client                     *http.Client
	tcpTransport               *http.Transport
	h3Pool                     *http3Pool
	deadlines                  *sharedDeadlineManager
	preparedHeaderRoot         context.Context
	preparedDeadlines          *sharedDeadlineManager
	lanes                      *lanePool
	optimization               responseOptimizationReport
	replay                     bool
	initialize                 sync.Once
	initializeErr              error
	closeOnce                  sync.Once
	closed                     atomic.Bool
	tcpProtocolObserved        bool
	closeErrMu                 sync.RWMutex
	closeErr                   string
	telemetryOn                bool
	optimizedAttempts          atomic.Uint64
	optimizedSuccesses         atomic.Uint64
	optimizedFallbackTransport atomic.Uint64
	optimizedFallbackSemantic  atomic.Uint64
	// autoPins maps a -preflight-var-auto *ffuf.VarExtract to the source its
	// bare key first resolved to (see extractByName).
	autoPins sync.Map
}

type requestHeaderSnapshot struct {
	values  []string
	present bool
}

type requestFallbackState struct {
	method              string
	body                io.ReadCloser
	getBody             func() (io.ReadCloser, error)
	contentLength       int64
	acceptEncoding      requestHeaderSnapshot
	rangeHeader         requestHeaderSnapshot
	contentLengthHeader requestHeaderSnapshot
}

func captureRequestFallbackState(req *http.Request) requestFallbackState {
	return requestFallbackState{
		method:              req.Method,
		body:                req.Body,
		getBody:             req.GetBody,
		contentLength:       req.ContentLength,
		acceptEncoding:      captureRequestHeader(req.Header, "Accept-Encoding"),
		rangeHeader:         captureRequestHeader(req.Header, "Range"),
		contentLengthHeader: captureRequestHeader(req.Header, "Content-Length"),
	}
}

func captureRequestHeader(header http.Header, name string) requestHeaderSnapshot {
	values, present := header[textproto.CanonicalMIMEHeaderKey(name)]
	return requestHeaderSnapshot{values: values, present: present}
}

func restoreRequestHeader(header http.Header, name string, snapshot requestHeaderSnapshot) {
	name = textproto.CanonicalMIMEHeaderKey(name)
	if !snapshot.present {
		header.Del(name)
		return
	}
	header[name] = append([]string(nil), snapshot.values...)
}

func (s requestFallbackState) clone(req *http.Request, ctx context.Context) *http.Request {
	full := req.Clone(ctx)
	full.Method = s.method
	full.Body = s.body
	full.GetBody = s.getBody
	full.ContentLength = s.contentLength
	restoreRequestHeader(full.Header, "Accept-Encoding", s.acceptEncoding)
	restoreRequestHeader(full.Header, "Range", s.rangeHeader)
	restoreRequestHeader(full.Header, "Content-Length", s.contentLengthHeader)
	return full
}

// preflightLane holds the variables extracted by a per-thread preflight chain.
// A lane is checked out of the pool for the duration of one Execute call, so its
// map is only ever touched by a single goroutine at a time.
type preflightLane struct {
	vars        map[string]string
	initialized bool
}

// lanePool hands out preflightLanes for per-thread preflight mode. ffuf runs a
// fresh goroutine per request (bounded to -t concurrent), so there is no
// persistent worker to pin per-thread state to; instead a request borrows a lane
// for the length of its Execute call and returns it. Lanes are reused, so a
// per-thread preflight chain runs once per lane and its vars are amortized across
// every request that later borrows that lane. The free list grows on demand, so a
// checkout never blocks and cannot deadlock.
type lanePool struct {
	mu   sync.Mutex
	free []*preflightLane
}

func (p *lanePool) get() *preflightLane {
	p.mu.Lock()
	defer p.mu.Unlock()
	if n := len(p.free); n > 0 {
		l := p.free[n-1]
		p.free = p.free[:n-1]
		return l
	}
	return &preflightLane{}
}

func (p *lanePool) put(l *preflightLane) {
	p.mu.Lock()
	p.free = append(p.free, l)
	p.mu.Unlock()
}

func NewSimpleRunner(conf *ffuf.Config, replay bool) ffuf.RunnerProvider {
	var simplerunner SimpleRunner
	proxyURL := http.ProxyFromEnvironment
	customProxy := ""

	if replay {
		customProxy = conf.ReplayProxyURL
	} else {
		customProxy = conf.ProxyURL
	}
	if len(customProxy) > 0 {
		pu, err := url.Parse(customProxy)
		if err == nil {
			proxyURL = http.ProxyURL(pu)
		}
	}
	cert := []tls.Certificate{}

	if conf.ClientCert != "" && conf.ClientKey != "" {
		tmp, _ := tls.LoadX509KeyPair(conf.ClientCert, conf.ClientKey)
		cert = []tls.Certificate{tmp}
	}

	simplerunner.config = conf
	simplerunner.replay = replay
	simplerunner.lanes = &lanePool{}
	simplerunner.telemetryOn = conf.RunTelemetry != "" && !replay
	// Keep the historical pool size for smaller scans, but do not silently
	// serialize workers above it. MaxConnsPerHost is a hard admission limit in
	// net/http, so leaving it fixed at 500 makes a -t 1000 scan run in two
	// synchronized waves. The idle limits track the same capacity to avoid
	// reconnect churn between bursts without pre-allocating any connections.
	connectionCapacity := 500
	if conf.Threads > connectionCapacity {
		connectionCapacity = conf.Threads
	}
	globalIdleCapacity := 1000
	if connectionCapacity > globalIdleCapacity {
		globalIdleCapacity = connectionCapacity
	}
	tcpTransport := &http.Transport{
		// Automatic transport selection prefers HTTP/2 after HTTP/3 is ruled out.
		// net/http falls back to HTTP/1.1 when ALPN negotiation doesn't select h2.
		ForceAttemptHTTP2: !conf.Http1,
		Proxy:             proxyURL,
		// Response headers are not covered by MAX_DOWNLOAD_SIZE, and Go's default
		// ceiling is 10 MB. A hostile target could spend that on every response,
		// and the headers are retained per response and copied into resp.Raw, the
		// scraper's source string and the audit log. 1 MB is far above anything a
		// real server sends (nginx and Apache cap a header line at 8 KB).
		MaxResponseHeaderBytes: 1 << 20,
		MaxIdleConns:           globalIdleCapacity,
		MaxIdleConnsPerHost:    connectionCapacity,
		MaxConnsPerHost:        connectionCapacity,
		DialContext: (&net.Dialer{
			Timeout: time.Duration(time.Duration(conf.Timeout) * time.Second),
		}).DialContext,
		TLSHandshakeTimeout: time.Duration(time.Duration(conf.Timeout) * time.Second),
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
			MinVersion:         tls.VersionTLS10,
			Renegotiation:      tls.RenegotiateOnceAsClient,
			ServerName:         conf.SNI,
			Certificates:       cert,
		},
	}
	if conf.Http1 {
		// A non-nil TLSNextProto map suppresses net/http's automatic HTTP/2
		// registration. ForceAttemptHTTP2=false alone isn't a sufficiently strong
		// force-HTTP/1 contract when transport initialization behavior changes.
		tcpTransport.TLSNextProto = make(map[string]func(string, *tls.Conn) http.RoundTripper)
	}
	simplerunner.tcpTransport = tcpTransport
	simplerunner.client = &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       time.Duration(time.Duration(conf.Timeout) * time.Second),
		Transport:     tcpTransport,
	}

	if conf.FollowRedirects {
		simplerunner.client.CheckRedirect = nil
	}
	return &simplerunner
}

// Initialize performs bounded protocol and response-shape probes before workers
// start. Keeping this out of NewSimpleRunner lets callers construct a runner for
// request dumping/history without causing network traffic.
func (r *SimpleRunner) Initialize(baseReq *ffuf.Request) error {
	r.initialize.Do(func() {
		r.initializeErr = r.initializeForTarget(baseReq)
	})
	return r.initializeErr
}

func (r *SimpleRunner) initializeForTarget(baseReq *ffuf.Request) error {
	if r.replay {
		return nil
	}
	if baseReq == nil {
		return errors.New("cannot initialize runner without a base request")
	}
	preflightStarted := time.Now()
	preflight := ffuf.ProtocolPreflightReport{
		SchemaVersion: 2,
		RanAt:         preflightStarted.UTC(),
	}
	defer func() {
		r.finalizePreflightReport(&preflight, preflightStarted)
	}()

	target, targetErr := http3ProbeURL(r.config, "")
	if targetErr != nil && r.config.Http3 {
		return targetErr
	}
	shouldProbeHTTP3 := targetErr == nil && target.Scheme == "https" && !r.config.Http1 && !r.config.NoHttp3 && !r.config.Http2
	if targetErr == nil && target.Scheme != "https" {
		r.config.Http3Reason = "automatic HTTP/3 requires an https target"
	} else if r.config.Http1 {
		r.config.Http3Reason = "HTTP/1.1 explicitly selected"
	} else if r.config.NoHttp3 {
		r.config.Http3Reason = "automatic HTTP/3 detection disabled"
	} else if r.config.Http2 && !r.config.Http3 {
		r.config.Http3Reason = "automatic HTTP/3 skipped because HTTP/2 was explicitly selected"
	}
	if shouldProbeHTTP3 {
		proxyProbe, err := http.NewRequestWithContext(configContext(r.config), http.MethodHead, target.String(), nil)
		if err != nil {
			return fmt.Errorf("build HTTP/3 proxy probe: %w", err)
		}
		proxyURL, proxyErr := r.tcpTransport.Proxy(proxyProbe)
		if proxyErr != nil {
			if r.config.Http3 {
				return fmt.Errorf("resolve proxy for forced HTTP/3: %w", proxyErr)
			}
			r.config.Http3Reason = "automatic HTTP/3 skipped because proxy resolution failed: " + proxyErr.Error()
			shouldProbeHTTP3 = false
		} else if proxyURL != nil {
			r.config.Http3Reason = "automatic HTTP/3 skipped because the target uses a proxy"
			shouldProbeHTTP3 = false
			if r.config.Http3 {
				return errors.New("forced HTTP/3 would bypass the configured or environment proxy")
			}
		}
	}

	if shouldProbeHTTP3 {
		advertisementStarted := time.Now()
		advertised, advertisement, reason, advertisementErr := r.probeHTTP3Advertisement(target)
		preflight.HTTP3.Advertised = advertised
		preflight.HTTP3.Advertisement = advertisement
		preflight.HTTP3.AdvertisementProbe = ffuf.PreflightProbeReport{
			Attempted: true, Successful: advertisementErr == nil, Reliable: advertised,
			Reason: reason, Requests: 1, ElapsedMillis: time.Since(advertisementStarted).Milliseconds(),
		}
		preflight.Timings.AdvertisementMillis = preflight.HTTP3.AdvertisementProbe.ElapsedMillis

		// Automatic mode must prove concurrent stream admission, not merely one
		// reusable request, because that is the capability the high-rate pool needs.
		fingerprintStarted := time.Now()
		fingerprint, err := FingerprintHTTP3(r.config, true)
		preflight.HTTP3.Fingerprint = fingerprint
		preflight.HTTP3.Negotiated = fingerprint.Supported
		preflight.HTTP3.NegotiationProbe = ffuf.PreflightProbeReport{
			Attempted: true, Successful: fingerprint.Supported, Reliable: fingerprint.HighRateReady,
			Reason: fingerprint.Reason, Requests: 2 + fingerprint.ConcurrentStreamsAttempted,
			ElapsedMillis: time.Since(fingerprintStarted).Milliseconds(),
		}
		preflight.Timings.HTTP3Millis = preflight.HTTP3.NegotiationProbe.ElapsedMillis
		if err != nil {
			preflight.HTTP3.NegotiationProbe.Reason = err.Error()
			if r.config.Http3 {
				return fmt.Errorf("forced HTTP/3 fingerprint: %w", err)
			}
			r.config.Http3Reason = "automatic HTTP/3 fingerprint failed: " + err.Error()
		} else if !fingerprint.HighRateReady {
			r.config.Http3Reason = fingerprint.Reason
			if r.config.Http3 {
				return fmt.Errorf("target is not ready for forced HTTP/3: %s", fingerprint.Reason)
			}
		} else {
			probe, probeErr := newHTTP3WarmupRequest(r.config, target)
			if probeErr != nil {
				return probeErr
			}
			pool, poolErr := newHTTP3Pool(r.config, target, probe)
			if poolErr == nil {
				var active int
				active, poolErr = pool.start(configContext(r.config))
				if poolErr == nil {
					r.h3Pool = pool
					r.client.Transport = &originRoutingTransport{
						target:   target,
						primary:  pool,
						fallback: r.tcpTransport,
					}
					r.config.Http3Selected = true
					r.deadlines = newSharedDeadlineManager(
						configContext(r.config),
						time.Duration(r.config.Timeout)*time.Second,
					)
					r.client.Timeout = 0
					r.config.Http3ConnectionsActive = active
					r.config.Http3Reason = fmt.Sprintf("negotiated %s over %s", fingerprint.ALPN, fingerprint.QUICVersion)
					if !r.config.ThreadsExplicit {
						r.config.Threads = pool.suggestedWorkers()
					}
				}
			}
			if poolErr != nil {
				if pool != nil {
					_ = pool.Close()
				}
				r.config.Http3Reason = "HTTP/3 pool warmup failed: " + poolErr.Error()
				if r.config.Http3 {
					return fmt.Errorf("forced HTTP/3 pool warmup: %w", poolErr)
				}
			}
		}
	} else if r.config.Http3 {
		if targetErr != nil {
			return targetErr
		}
		if target.Scheme != "https" {
			return errors.New("forced HTTP/3 requires an https target")
		}
		if r.config.Http2 {
			return errors.New("forced HTTP/3 cannot be combined with HTTP/2 mode")
		}
		if r.config.NoHttp3 {
			return errors.New("forced HTTP/3 cannot be combined with -no-http3")
		}
	}

	// The TLS/TCP transport offers HTTP/2 unless HTTP/1.1 was explicitly forced.
	// Record the protocol actually negotiated so reports and the web UI never
	// confuse an HTTP/1.1 fallback with HTTP/2. The Alt-Svc probe already provides
	// this evidence in ordinary automatic HTTPS mode; an explicit bounded probe
	// covers HTTPS proxy, -http2, and -no-http3 paths. Plain HTTP is classified
	// directly because the standard transport does not attempt h2c.
	if !r.config.Http3Selected && r.config.Http1 {
		r.config.Http2Selected = false
		r.tcpProtocolObserved = true
	} else if !r.config.Http3Selected && targetErr == nil && target.Scheme != "https" {
		// The standard transport does not attempt h2c. Plain HTTP therefore has
		// no ALPN decision to probe and is deterministically HTTP/1.1. Avoid an
		// otherwise user-visible extra request before every plain-HTTP scan.
		r.config.Http2Selected = false
		r.tcpProtocolObserved = true
	} else if !r.config.Http3Selected && !r.tcpProtocolObserved && targetErr == nil {
		if err := r.probeTCPProtocol(target); err != nil {
			if r.config.Http3Reason == "" {
				r.config.Http3Reason = "TCP protocol probe failed: " + err.Error()
			}
		}
	}
	if r.config.Http2 {
		if r.config.Http2Selected {
			r.config.Http3Reason = "HTTP/2 explicitly selected and negotiated"
		} else if r.tcpProtocolObserved {
			r.config.Http3Reason = "HTTP/2 was requested but the target negotiated HTTP/1.1"
		}
	}

	probeTimeout := time.Duration(r.config.Http3ProbeTimeout*3) * time.Second
	if probeTimeout <= 0 {
		probeTimeout = 9 * time.Second
	}
	probeContext, cancel := context.WithTimeout(configContext(r.config), probeTimeout)
	defer cancel()
	r.optimization = detectResponseOptimization(probeContext, r.client, r.config, baseReq)
	r.config.ResponseOptimizationStrategy = r.optimization.mode.String()
	r.config.ResponseOptimizationReason = r.optimization.reason
	r.configurePreparedRequestHeaders()
	preflight.RequestStrategy = ffuf.RequestStrategyPreflightReport{
		Selected:      r.optimization.mode.String(),
		Reason:        r.optimization.reason,
		ProbeRequests: r.optimization.probes,
		FullGET: ffuf.PreflightProbeReport{
			Attempted: r.optimization.baselineProbes > 0, Successful: r.optimization.baselineReliable,
			Reliable: r.optimization.baselineReliable, Reason: "full-GET qualification baseline",
			Requests: r.optimization.baselineProbes, ElapsedMillis: r.optimization.baselineElapsed.Milliseconds(),
			MeanLatencyMS: meanProbeLatencyMS(r.optimization.baselineElapsed, r.optimization.baselineProbes),
		},
		HEAD: ffuf.PreflightProbeReport{
			Attempted: r.optimization.headAttempted, Successful: r.optimization.headReliable,
			Reliable: r.optimization.headReliable, Reason: r.optimization.headReason,
			Requests: r.optimization.headProbes, ElapsedMillis: r.optimization.headElapsed.Milliseconds(),
			MeanLatencyMS: meanProbeLatencyMS(r.optimization.headElapsed, r.optimization.headProbes),
		},
		Range: ffuf.PreflightProbeReport{
			Attempted: r.optimization.rangeAttempted, Successful: r.optimization.rangeReliable,
			Reliable: r.optimization.rangeReliable, Reason: r.optimization.rangeReason,
			Requests: r.optimization.rangeProbes, ElapsedMillis: r.optimization.rangeElapsed.Milliseconds(),
			MeanLatencyMS: meanProbeLatencyMS(r.optimization.rangeElapsed, r.optimization.rangeProbes),
		},
	}
	preflight.Timings.BaselineMillis = r.optimization.baselineElapsed.Milliseconds()
	preflight.Timings.HEADMillis = r.optimization.headElapsed.Milliseconds()
	preflight.Timings.RangeMillis = r.optimization.rangeElapsed.Milliseconds()
	return nil
}

// configurePreparedRequestHeaders opts h3fuzz into quic-go's default-off
// prepared writer only after both transport and semantic qualification have
// succeeded. It is deliberately independent of the CLI-only HighRatePolicy.
func (r *SimpleRunner) configurePreparedRequestHeaders() {
	if r == nil || r.config == nil || r.replay || !r.config.Http3Selected || r.h3Pool == nil ||
		r.optimization.mode != responseOptimizationHEAD || !r.optimization.headReliable {
		return
	}
	root := configContext(r.config)
	if r.telemetryOn {
		root = quichttp3.WithPreparedRequestHeadersTelemetry(root)
	} else {
		root = quichttp3.WithPreparedRequestHeaders(root)
	}
	r.preparedHeaderRoot = root
	r.preparedDeadlines = newSharedDeadlineManager(root, time.Duration(r.config.Timeout)*time.Second)
}

func meanProbeLatencyMS(elapsed time.Duration, requests int) float64 {
	if elapsed <= 0 || requests <= 0 {
		return 0
	}
	return float64(elapsed) / float64(time.Millisecond) / float64(requests)
}

func (r *SimpleRunner) probeHTTP3Advertisement(target *url.URL) (bool, string, string, error) {
	timeout := time.Duration(r.config.Http3ProbeTimeout) * time.Second
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	ctx, cancel := context.WithTimeout(configContext(r.config), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, target.String(), nil)
	if err != nil {
		return false, "", "could not build Alt-Svc probe: " + err.Error(), err
	}
	copyHTTP3Headers(req, r.config.Headers)
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", fmt.Sprintf("h3fuzz v%s", ffuf.Version()))
	}
	probeClient := *r.client
	probeClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := probeClient.Do(req)
	if err != nil {
		return false, "", "Alt-Svc probe failed: " + err.Error(), err
	}
	r.recordTCPProtocol(resp)
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, fingerprintBodyLimit+1))
	advertisement := strings.Join(resp.Header.Values("Alt-Svc"), ", ")
	advertised := advertisesHTTP3(advertisement)
	if advertised {
		return true, advertisement, "server advertised HTTP/3 with Alt-Svc", nil
	}
	if advertisement == "" {
		return false, "", "response did not include Alt-Svc", nil
	}
	return false, advertisement, "Alt-Svc did not advertise an h3 protocol", nil
}

func (r *SimpleRunner) probeTCPProtocol(target *url.URL) error {
	if target == nil {
		return errors.New("target URL is nil")
	}
	timeout := time.Duration(r.config.Http3ProbeTimeout) * time.Second
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	ctx, cancel := context.WithTimeout(configContext(r.config), timeout)
	defer cancel()
	// Use a one-byte GET rather than HEAD here. This fallback probe is reached
	// when the Alt-Svc HEAD probe was skipped or failed, and some otherwise valid
	// HTTP/2 targets reject HEAD. Range and identity encoding bound the response
	// while still allowing ALPN negotiation to be observed.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return fmt.Errorf("build TCP protocol probe: %w", err)
	}
	copyHTTP3Headers(req, r.config.Headers)
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Range", "bytes=0-0")
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", fmt.Sprintf("h3fuzz v%s", ffuf.Version()))
	}
	probeClient := *r.client
	probeClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := probeClient.Do(req)
	if err != nil {
		return err
	}
	r.recordTCPProtocol(resp)
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, fingerprintBodyLimit+1))
	return nil
}

func (r *SimpleRunner) recordTCPProtocol(resp *http.Response) {
	if r == nil || r.config == nil || resp == nil {
		return
	}
	r.tcpProtocolObserved = true
	r.config.Http2Selected = resp.ProtoMajor == 2
}

func advertisesHTTP3(value string) bool {
	for _, alternative := range strings.Split(value, ",") {
		protocol, _, ok := strings.Cut(strings.TrimSpace(alternative), "=")
		if ok && strings.HasPrefix(strings.ToLower(strings.TrimSpace(protocol)), "h3") {
			return true
		}
	}
	return false
}

func (r *SimpleRunner) finalizePreflightReport(report *ffuf.ProtocolPreflightReport, started time.Time) {
	if report == nil || r == nil || r.config == nil {
		return
	}
	selected := "http1"
	connections := r.config.Threads
	streamsPerConnection := 1
	maximumStreams := r.config.Threads
	if r.config.Http2Selected {
		selected = "http2"
		connections = 1
		streamsPerConnection = r.config.Threads
	}
	if r.config.Http3Selected {
		selected = "http3"
		connections = r.config.Http3ConnectionsActive
		streamsPerConnection = r.config.Http3Streams
		if streamsPerConnection <= 0 {
			streamsPerConnection = 256
		}
		maximumStreams = connections * streamsPerConnection
		if r.config.Threads > 0 && maximumStreams > r.config.Threads {
			maximumStreams = r.config.Threads
		}
	}
	if connections < 0 {
		connections = 0
	}
	if maximumStreams < 0 {
		maximumStreams = 0
	}
	report.SelectedProtocol = selected
	if selected != "http3" {
		report.DowngradeReason = r.config.Http3Reason
	}
	if !report.HTTP3.AdvertisementProbe.Attempted {
		report.HTTP3.AdvertisementProbe.Reason = r.config.Http3Reason
	}
	if !report.HTTP3.NegotiationProbe.Attempted {
		report.HTTP3.NegotiationProbe.Reason = r.config.Http3Reason
	} else if report.HTTP3.NegotiationProbe.Reason == "" {
		report.HTTP3.NegotiationProbe.Reason = r.config.Http3Reason
	}
	if report.RequestStrategy.Selected == "" {
		report.RequestStrategy.Selected = r.config.ResponseOptimizationStrategy
		if report.RequestStrategy.Selected == "" {
			report.RequestStrategy.Selected = http.MethodGet
		}
		report.RequestStrategy.Reason = r.config.ResponseOptimizationReason
	}
	if !report.RequestStrategy.HEAD.Attempted && report.RequestStrategy.HEAD.Reason == "" {
		report.RequestStrategy.HEAD.Reason = report.RequestStrategy.Reason
	}
	if !report.RequestStrategy.Range.Attempted && report.RequestStrategy.Range.Reason == "" {
		report.RequestStrategy.Range.Reason = report.RequestStrategy.Reason
	}
	report.EffectiveConcurrency = ffuf.EffectiveConcurrencyReport{
		Workers: r.config.Threads, Connections: connections,
		StreamsPerConnection: streamsPerConnection, MaximumConcurrentStreams: maximumStreams,
	}
	report.CompletedAt = time.Now().UTC()
	report.Timings.TotalMillis = time.Since(started).Milliseconds()
	r.config.ProtocolPreflight = *report
}

func newHTTP3WarmupRequest(conf *ffuf.Config, target *url.URL) (*http.Request, error) {
	req, err := http.NewRequestWithContext(configContext(conf), http.MethodHead, target.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build HTTP/3 warmup request: %w", err)
	}
	copyHTTP3Headers(req, conf.Headers)
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", fmt.Sprintf("h3fuzz v%s", ffuf.Version()))
	}
	return req, nil
}

type originRoutingTransport struct {
	target   *url.URL
	primary  http.RoundTripper
	fallback http.RoundTripper
}

func (t *originRoutingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req != nil && req.URL != nil && t.target != nil && ffuf.UrlEqual(req.URL, t.target) {
		return t.primary.RoundTrip(req)
	}
	return t.fallback.RoundTrip(req)
}

func (r *SimpleRunner) Close() error {
	var closeErr error
	r.closeOnce.Do(func() {
		if r.preparedDeadlines != nil {
			r.preparedDeadlines.close()
		}
		if r.deadlines != nil {
			r.deadlines.close()
		}
		if r.h3Pool != nil {
			closeErr = r.h3Pool.Close()
		}
		if r.tcpTransport != nil {
			r.tcpTransport.CloseIdleConnections()
		}
		if closeErr != nil {
			r.closeErrMu.Lock()
			r.closeErr = closeErr.Error()
			r.closeErrMu.Unlock()
		}
		r.closed.Store(true)
	})
	return closeErr
}

// requestContext returns a timeout context for a single high-rate request.
// HTTP/3 requests share one standard-library deadline timer per 50 ms bucket,
// rather than creating and stopping a timer for every request. Before HTTP/3 is
// selected, net/http's Client.Timeout remains responsible for request bounds.
func (r *SimpleRunner) requestContext() context.Context {
	if r != nil && r.deadlines != nil {
		return r.deadlines.context()
	}
	if r != nil {
		return configContext(r.config)
	}
	return context.Background()
}

// preparedRequestContext returns a context derived from the separately marked
// root. requestContext deliberately remains generic so recovery GETs never
// inherit the prepared-header marker.
func (r *SimpleRunner) preparedRequestContext() context.Context {
	if r != nil && r.preparedDeadlines != nil {
		return r.preparedDeadlines.context()
	}
	if r != nil && r.preparedHeaderRoot != nil {
		return r.preparedHeaderRoot
	}
	return r.requestContext()
}

// RunTelemetry returns an atomic snapshot suitable for the post-drain report.
// It is intentionally not consulted by Execute, keeping telemetry reads out of
// the request hot path.
func (r *SimpleRunner) RunTelemetry() ffuf.RunTelemetry {
	protocol := "http1.1"
	if r.config.Http2Selected {
		protocol = "http2"
	}
	if r.config.Http3Selected {
		protocol = "http3"
	}

	rangeHeader := ""
	for name, value := range r.config.Headers {
		if strings.EqualFold(name, "Range") {
			rangeHeader = value
			break
		}
	}
	effectiveStrategy := strings.ToUpper(r.config.Method)
	if strings.EqualFold(r.config.ResponseOptimizationStrategy, "HEAD") ||
		strings.EqualFold(r.config.ResponseOptimizationStrategy, "RANGE") {
		effectiveStrategy = strings.ToUpper(r.config.ResponseOptimizationStrategy)
	}
	if rangeHeader != "" {
		effectiveStrategy = "RANGE"
	}
	internalUnavailableReason := "http3_not_selected"
	if r.config.Http3Selected {
		internalUnavailableReason = "quic_lifecycle_diagnostics_disabled"
	}
	var runID *string
	if r.config.HarnessRunID != "" {
		value := r.config.HarnessRunID
		runID = &value
	}
	report := ffuf.RunTelemetry{
		SchemaVersion: 2,
		RunID:         runID,
		Protocol: ffuf.RunProtocolTelemetry{
			Selected:                protocol,
			SelectionReason:         r.config.Http3Reason,
			HTTP3Selected:           r.config.Http3Selected,
			HTTP3InitialConnections: r.config.Http3ConnectionsActive,
		},
		RequestStrategy: ffuf.RunRequestStrategyTelemetry{
			Method:                       r.config.Method,
			Range:                        rangeHeader,
			AutomaticOptimizationEnabled: !r.config.NoResponseOptimization,
			SelectedOptimization:         effectiveStrategy,
			SelectionReason:              r.config.ResponseOptimizationReason,
			OptimizedAttempts:            r.optimizedAttempts.Load(),
			OptimizedSuccesses:           r.optimizedSuccesses.Load(),
			FullGETFallbacks:             r.optimizedFallbackTransport.Load() + r.optimizedFallbackSemantic.Load(),
			FallbackTransportErrors:      r.optimizedFallbackTransport.Load(),
			FallbackUntrustworthy:        r.optimizedFallbackSemantic.Load(),
		},
		Transport: ffuf.RunTransportTelemetry{
			InternalAttemptsUnavailableReason: internalUnavailableReason,
			InternalRetriesUnavailableReason:  internalUnavailableReason,
		},
		QUIC: unavailableQUICDiagnostics(internalUnavailableReason),
		Drain: ffuf.RunDrainTelemetry{
			RunnerClosed: r.closed.Load(),
		},
	}
	report.Transport.PreparedRequestHeaders = preparedRequestHeadersRunTelemetry(r.preparedHeaderRoot)
	if r.config.HighRatePolicy != nil {
		policy := *r.config.HighRatePolicy
		report.HighRatePolicy = &policy
	}
	r.closeErrMu.RLock()
	report.Drain.CloseError = r.closeErr
	r.closeErrMu.RUnlock()
	if r.h3Pool != nil {
		pool := r.h3Pool.telemetrySnapshot()
		report.Protocol.HTTP3FinalConnections = pool.activeConnections
		report.Protocol.HTTP3OpenedConnections = pool.openedConnections
		report.Transport.CompletedResponses = pool.completed
		report.Transport.Errors = pool.errors
		report.Transport.InFlight = pool.inFlight
		report.Drain.LimiterInUse = pool.limiterInUse
		report.Drain.LimiterWaiters = pool.limiterWaiters
		report.Drain.RunnerClosed = pool.closed && r.closed.Load()
		if pool.capacity != nil {
			report.Workers = ffuf.RunWorkerCapacityTelemetry{
				ExecutionGeneration:              pool.capacity.ExecutionGeneration,
				ProposalEpoch:                    pool.capacity.ProposalEpoch,
				AcknowledgementGeneration:        pool.capacity.AcknowledgementGeneration,
				AcknowledgementEpoch:             pool.capacity.AcknowledgementEpoch,
				StaleAcknowledgements:            pool.capacity.StaleAcknowledgements,
				LateAfterTimeoutAcknowledgements: pool.capacity.LateAfterTimeoutAcknowledgements,
				RepublishedProposals:             pool.capacity.RepublishedProposals,
				TimedOutProposals:                pool.capacity.TimedOutProposals,
				SupersededProposals:              pool.capacity.SupersededProposals,
				MissingListenerProposals:         pool.capacity.MissingListenerProposals,
				ProposedWorkers:                  pool.capacity.ProposedWorkers,
				AcknowledgedLiveWorkers:          pool.capacity.AcknowledgedLiveWorkers,
				DesiredWorkers:                   pool.capacity.DesiredWorkers,
			}
		}
		if pool.diagnostics != nil {
			report.QUIC = pool.diagnostics.quic
			report.Transport.InternalAttempts = pool.diagnostics.internalAttempts
			report.Transport.InternalRetries = pool.diagnostics.internalRetries
			report.Transport.InternalAttemptsUnavailableReason = ""
			report.Transport.InternalRetriesUnavailableReason = ""
			report.Transport.ObservedPreHandlerApplicationFailures = pool.diagnostics.observedPreHandlerFailures
			report.Drain.TrackedConnectionGenerations = pool.diagnostics.trackedGenerations
		}
	}
	return report
}

func preparedRequestHeadersRunTelemetry(root context.Context) ffuf.RunPreparedRequestHeadersTelemetry {
	if root == nil {
		return ffuf.RunPreparedRequestHeadersTelemetry{}
	}
	report := ffuf.RunPreparedRequestHeadersTelemetry{Enabled: true}
	prepared, ok := quichttp3.SnapshotPreparedRequestHeaders(root)
	if !ok {
		return report
	}
	return mapPreparedRequestHeadersRunTelemetry(prepared)
}

func mapPreparedRequestHeadersRunTelemetry(prepared quichttp3.PreparedRequestHeadersStats) ffuf.RunPreparedRequestHeadersTelemetry {
	fallbacks := prepared.FallbackQLog + prepared.FallbackTrace + prepared.FallbackMethod +
		prepared.FallbackBody + prepared.FallbackCompression + prepared.FallbackURL +
		prepared.FallbackHeaders + prepared.FallbackQPACK
	return ffuf.RunPreparedRequestHeadersTelemetry{
		Enabled:           true,
		AccountingEnabled: true,
		Attempts:          prepared.Attempts,
		Hits:              prepared.Hits,
		Fallbacks:         fallbacks,
		FallbackReasons: ffuf.RunPreparedRequestHeaderFallbackTelemetry{
			QLog:        prepared.FallbackQLog,
			Trace:       prepared.FallbackTrace,
			Method:      prepared.FallbackMethod,
			Body:        prepared.FallbackBody,
			Compression: prepared.FallbackCompression,
			URL:         prepared.FallbackURL,
			Headers:     prepared.FallbackHeaders,
			QPACK:       prepared.FallbackQPACK,
		},
	}
}

func (r *SimpleRunner) Prepare(input map[string][]byte, basereq *ffuf.Request) (ffuf.Request, error) {
	req := ffuf.CopyRequest(basereq)

	for keyword, inputitem := range input {
		req.Method = strings.ReplaceAll(req.Method, keyword, string(inputitem))
		headers := make(map[string]string, len(req.Headers))
		for h, v := range req.Headers {
			var CanonicalHeader string = textproto.CanonicalMIMEHeaderKey(strings.ReplaceAll(h, keyword, string(inputitem)))
			headers[CanonicalHeader] = strings.ReplaceAll(v, keyword, string(inputitem))
		}
		req.Headers = headers
		req.Url = strings.ReplaceAll(req.Url, keyword, string(inputitem))
		req.Data = []byte(strings.ReplaceAll(string(req.Data), keyword, string(inputitem)))
	}

	req.Input = input
	return req, nil
}

func (r *SimpleRunner) Execute(req *ffuf.Request) (ffuf.Response, error) {
	return r.execute(req, "", nil)
}

// ExecuteWithRunTelemetry instruments only the main workload transport
// attempts selected by the engine. Startup probes, warmup, qualification,
// preflights, and postflights continue through uninstrumented paths.
func (r *SimpleRunner) ExecuteWithRunTelemetry(req *ffuf.Request, role ffuf.RunTransportAttemptRole, recorder ffuf.RunTransportAttemptRecorder) (ffuf.Response, error) {
	return r.execute(req, role, recorder)
}

func recordRunTransportAttempt(recorder ffuf.RunTransportAttemptRecorder, role ffuf.RunTransportAttemptRole, method string, optimized, completed, surfacedError bool) {
	if recorder == nil {
		return
	}
	recorder.RecordRunTransportAttempt(ffuf.RunTransportAttempt{
		Role:          role,
		Method:        method,
		Optimized:     optimized,
		Completed:     completed,
		SurfacedError: surfacedError,
	})
}

func executeRunTransportAttempt(client *http.Client, req *http.Request, recorder ffuf.RunTransportAttemptRecorder, timing *exactRequestTiming) (*http.Response, error, time.Time, exactRequestTimingSnapshot) {
	if lifecycle, ok := recorder.(ffuf.RunTransportAttemptLifecycleRecorder); ok {
		lifecycle.BeginRunTransportAttempt()
		defer lifecycle.EndRunTransportAttempt()
	}
	start := time.Now()
	if timing != nil {
		timing.beginAt(start)
	}
	resp, err := client.Do(req)
	var snapshot exactRequestTimingSnapshot
	if timing != nil {
		snapshot = timing.freeze()
	}
	return resp, err, start, snapshot
}

type exactRequestTiming struct {
	mu sync.Mutex

	pendingStart time.Time
	pending      bool

	completedStart    time.Time
	completedDuration time.Duration
	completed         bool
	frozen            bool
}

type exactRequestTimingSnapshot struct {
	start         time.Time
	firstByteTime time.Duration
	firstByteSeen bool
}

func newExactRequestTiming() (*exactRequestTiming, *httptrace.ClientTrace) {
	timing := &exactRequestTiming{}
	trace := &httptrace.ClientTrace{
		WroteRequest:         timing.wroteRequest,
		GotFirstResponseByte: timing.gotFirstResponseByte,
	}
	return timing, trace
}

func (t *exactRequestTiming) beginAt(start time.Time) {
	t.mu.Lock()
	t.pendingStart = start
	t.pending = true
	t.completedStart = time.Time{}
	t.completedDuration = 0
	t.completed = false
	t.frozen = false
	t.mu.Unlock()
}

func (t *exactRequestTiming) wroteRequest(httptrace.WroteRequestInfo) {
	now := time.Now()
	t.mu.Lock()
	if !t.frozen {
		// A ClientTrace follows redirects with its request context. Each write opens
		// a new candidate interval; the matching first byte commits that hop.
		t.pendingStart = now
		t.pending = true
	}
	t.mu.Unlock()
}

func (t *exactRequestTiming) gotFirstResponseByte() {
	t.mu.Lock()
	if !t.frozen && t.pending {
		t.completedStart = t.pendingStart
		t.completedDuration = time.Since(t.pendingStart)
		t.completed = true
		t.pending = false
	}
	t.mu.Unlock()
}

func (t *exactRequestTiming) snapshot() exactRequestTimingSnapshot {
	t.mu.Lock()
	snapshot := t.snapshotLocked()
	t.mu.Unlock()
	return snapshot
}

func (t *exactRequestTiming) freeze() exactRequestTimingSnapshot {
	t.mu.Lock()
	t.frozen = true
	snapshot := t.snapshotLocked()
	t.mu.Unlock()
	return snapshot
}

func (t *exactRequestTiming) snapshotLocked() exactRequestTimingSnapshot {
	if t.completed {
		return exactRequestTimingSnapshot{
			start:         t.completedStart,
			firstByteTime: t.completedDuration,
			firstByteSeen: true,
		}
	}
	return exactRequestTimingSnapshot{start: t.pendingStart}
}

func requestAttemptContext(base context.Context, trace *httptrace.ClientTrace, applicationAttempt bool) context.Context {
	if applicationAttempt {
		base = withQUICApplicationAttempt(base)
	}
	if trace != nil {
		base = httptrace.WithClientTrace(base, trace)
	}
	return base
}

func optimizationFallbackRole(role ffuf.RunTransportAttemptRole) ffuf.RunTransportAttemptRole {
	if role == "" {
		return ""
	}
	if role == ffuf.RunTransportRoleEngineRetry {
		return ffuf.RunTransportRoleEngineRetryOptimizationFallback
	}
	return ffuf.RunTransportRoleOptimizationFallback
}

func (r *SimpleRunner) markHarnessRequest(req *http.Request, role ffuf.RunTransportAttemptRole) {
	if r.config.HarnessRunID == "" || role == "" {
		return
	}
	req.Header.Set(ffuf.HarnessRunHeader, r.config.HarnessRunID)
	req.Header.Set(ffuf.HarnessRoleHeader, string(role))
}

func rejectHarnessMarkerCollision(header http.Header) error {
	if _, present := header[ffuf.HarnessRunHeader]; present {
		return fmt.Errorf("request contains reserved harness header %s", ffuf.HarnessRunHeader)
	}
	if _, present := header[ffuf.HarnessRoleHeader]; present {
		return fmt.Errorf("request contains reserved harness header %s", ffuf.HarnessRoleHeader)
	}
	return nil
}

func (r *SimpleRunner) execute(req *ffuf.Request, role ffuf.RunTransportAttemptRole, recorder ffuf.RunTransportAttemptRecorder) (resp ffuf.Response, err error) {
	var finalAttemptPending bool
	var finalAttemptMethod string
	var finalAttemptOptimized bool
	var finalAttemptRole ffuf.RunTransportAttemptRole
	var terminalAttemptErrorRecorded bool
	defer func() {
		if finalAttemptPending {
			recordRunTransportAttempt(recorder, finalAttemptRole, finalAttemptMethod, finalAttemptOptimized, err == nil, err != nil)
		}
	}()
	// Avoid URL parsing on the normal hot path. We only need the original host if
	// a preflight may inject captured values into the request.
	targetHost := ""
	if len(r.config.Preflights) > 0 {
		// Pin the target host: a value captured from an (untrusted) preflight
		// response must not change which host this authenticated request is sent to.
		targetHost = hostOf(req.Url)
	}
	// Run the preflight chain first: it may inject extracted variables into this
	// request's URL, headers and body before it is built.
	appliedVars, releaseLane, pferr := r.runPreflights(req)
	defer releaseLane()
	// Postflight runs only when the main request produced a response (err == nil).
	// Deferred so it also covers the oversized / -ignore-body early returns, but is
	// skipped when the request itself errors.
	defer func() {
		if err == nil {
			r.runPostflights(appliedVars)
		}
	}()
	if pferr != nil {
		return ffuf.Response{}, pferr
	}
	if len(appliedVars) > 0 {
		if h := hostOf(req.Url); h != targetHost {
			return ffuf.Response{}, fmt.Errorf("preflight variable changed the request host from %q to %q; refusing to send (a captured value must not alter the target host)", targetHost, h)
		}
	}

	var httpreq *http.Request
	var rawreq []byte
	var data io.Reader
	if len(req.Data) > 0 {
		data = bytes.NewReader(req.Data)
	}

	var start time.Time
	var firstByteTime time.Duration
	var timing *exactRequestTiming
	var trace *httptrace.ClientTrace
	// Generic GETs and telemetry-bearing runs retain ffuf's exact
	// write-to-first-byte semantics. With telemetry disabled, an eligible automatic
	// HEAD or Range request cannot coexist with a response-time matcher or filter,
	// so it can measure the smaller RoundTrip-to-headers interval and avoid
	// allocating a ClientTrace on every request. Defensive per-request failures to
	// apply the optimization install the exact trace below.
	if r.optimization.mode == responseOptimizationGET || r.telemetryOn {
		timing, trace = newExactRequestTiming()
	}

	// Attach the application marker and client trace before constructing the
	// request. Calling Request.WithContext afterwards makes a full shallow copy;
	// NewRequestWithContext can retain the composed context directly.
	applicationAttempt := recorder != nil && r.h3Pool != nil
	baseRequestCtx := r.requestContext()
	if r.preparedHeaderRoot != nil {
		baseRequestCtx = r.preparedRequestContext()
	}
	requestCtx := requestAttemptContext(baseRequestCtx, trace, applicationAttempt)
	httpreq, err = http.NewRequestWithContext(requestCtx, req.Method, req.Url, data)

	if err != nil {
		return ffuf.Response{}, err
	}

	// set default User-Agent header if not present
	if _, ok := req.Headers["User-Agent"]; !ok {
		req.Headers["User-Agent"] = fmt.Sprintf("%s v%s", "h3fuzz", ffuf.Version())
	}

	// Handle Go http.Request special cases
	if _, ok := req.Headers["Host"]; ok {
		httpreq.Host = req.Headers["Host"]
	}

	req.Host = httpreq.Host

	if r.config.Raw {
		httpreq.URL.Opaque = req.Url
	}

	for k, v := range req.Headers {
		httpreq.Header.Set(k, v)
	}
	if r.config.HarnessRunID != "" && role != "" {
		if err := rejectHarnessMarkerCollision(httpreq.Header); err != nil {
			return ffuf.Response{}, err
		}
		r.markHarnessRequest(httpreq, role)
	}
	var fallbackState requestFallbackState
	if r.optimization.mode != responseOptimizationGET {
		fallbackState = captureRequestFallbackState(httpreq)
	}
	optimized := r.optimization.apply(httpreq)
	if !optimized && timing == nil {
		// The immutable run strategy was optimized, but this individual request no
		// longer has a safe HEAD / Range shape. Preserve generic TTFB semantics.
		timing, trace = newExactRequestTiming()
		httpreq = httpreq.WithContext(httptrace.WithClientTrace(httpreq.Context(), trace))
	}
	if optimized && r.telemetryOn {
		r.optimizedAttempts.Add(1)
	}

	if len(r.config.OutputDirectory) > 0 || len(r.config.AuditLog) > 0 {
		rawreq, _ = httputil.DumpRequestOut(httpreq, true)
		req.Raw = string(rawreq)
	}

	// The helper captures start immediately before Client.Do. Exact timing hooks
	// refine it to WroteRequest when the RoundTripper supports that callback.
	var httpresp *http.Response
	var timingSnapshot exactRequestTimingSnapshot
	httpresp, err, start, timingSnapshot = executeRunTransportAttempt(r.client, httpreq, recorder, timing)
	if timing != nil {
		start = timingSnapshot.start
		firstByteTime = timingSnapshot.firstByteTime
	} else {
		// RoundTrip returns after response headers are available. This deliberately
		// differs from write-to-first-byte timing and is confined to a
		// telemetry-disabled optimized shape that eligibility has proven has no
		// response-time matcher or filter.
		firstByteTime = time.Since(start)
	}
	if err != nil && optimized {
		recordRunTransportAttempt(recorder, role, httpreq.Method, true, false, true)
		// A peer or intermediary may reject HEAD or Range on an individual stream
		// even after the baseline accepted it. Retry the untouched request once as
		// a full GET so an optimization-specific transport error does not become a
		// false-negative fuzz result.
		optimized = false
		if r.telemetryOn {
			r.optimizedFallbackTransport.Add(1)
		}
		// Use attempt-local state. httptrace callbacks from the failed optimized
		// attempt may legally arrive after RoundTrip returns and must not corrupt
		// the fallback GET's interval.
		timing, trace = newExactRequestTiming()
		fullRequest := fallbackState.clone(httpreq, requestAttemptContext(r.requestContext(), trace, applicationAttempt))
		r.markHarnessRequest(fullRequest, optimizationFallbackRole(role))
		httpresp, err, _, timingSnapshot = executeRunTransportAttempt(r.client, fullRequest, recorder, timing)
		start = timingSnapshot.start
		firstByteTime = timingSnapshot.firstByteTime
		if err != nil {
			recordRunTransportAttempt(recorder, optimizationFallbackRole(role), fullRequest.Method, false, false, true)
			terminalAttemptErrorRecorded = true
		} else {
			finalAttemptPending = true
			finalAttemptMethod = fullRequest.Method
			finalAttemptRole = optimizationFallbackRole(role)
		}
	}
	if err != nil {
		if !terminalAttemptErrorRecorded {
			// A non-optimized first attempt has no separate recovery role.
			recordRunTransportAttempt(recorder, role, httpreq.Method, false, false, true)
		}
		return ffuf.Response{}, err
	}
	if optimized {
		logical, fallbackReason := r.validateAndCloseOptimizedResponse(httpresp)
		if fallbackReason != optimizedFallbackNone {
			// Close before the fallback GET so this worker releases its HTTP/3
			// stream admission slot. Deferring this close can deadlock a saturated
			// pool when every worker simultaneously falls back.
			optimized = false
			recordRunTransportAttempt(
				recorder,
				role,
				httpreq.Method,
				true,
				fallbackReason != optimizedFallbackTransport,
				fallbackReason == optimizedFallbackTransport,
			)
			if r.telemetryOn {
				switch fallbackReason {
				case optimizedFallbackTransport:
					r.optimizedFallbackTransport.Add(1)
				default:
					r.optimizedFallbackSemantic.Add(1)
				}
			}
			// Keep the fallback interval isolated from any delayed callbacks on
			// the optimized attempt's trace.
			timing, trace = newExactRequestTiming()
			fullRequest := fallbackState.clone(httpreq, requestAttemptContext(r.requestContext(), trace, applicationAttempt))
			r.markHarnessRequest(fullRequest, optimizationFallbackRole(role))
			httpresp, err, _, timingSnapshot = executeRunTransportAttempt(r.client, fullRequest, recorder, timing)
			start = timingSnapshot.start
			firstByteTime = timingSnapshot.firstByteTime
			if err != nil {
				recordRunTransportAttempt(recorder, optimizationFallbackRole(role), fullRequest.Method, false, false, true)
				return ffuf.Response{}, err
			}
			finalAttemptPending = true
			finalAttemptMethod = fullRequest.Method
			finalAttemptRole = optimizationFallbackRole(role)
		} else {
			// Body-dependent matchers, scrapers and output artifacts are ruled out
			// before selecting HEAD or Range. Construct matcher-facing metadata here
			// instead of replaying the validation bytes through the generic body path.
			req.Timestamp = start
			resp = ffuf.NewResponse(httpresp, req)
			resp.StatusCode = int64(logical.statusCode)
			resp.ContentLength = logical.contentLength
			resp.Data = nil
			resp.ContentWords = 0
			resp.ContentLines = 0
			resp.Duration = firstByteTime
			resp.Timestamp = start.Add(firstByteTime)
			if r.config.IgnoreBody {
				resp.Cancelled = true
			}
			if r.telemetryOn {
				r.optimizedSuccesses.Add(1)
			}
			finalAttemptPending = true
			finalAttemptMethod = httpreq.Method
			finalAttemptOptimized = true
			finalAttemptRole = role
			return resp, nil
		}
	}
	if !finalAttemptPending {
		finalAttemptPending = true
		finalAttemptMethod = httpreq.Method
		finalAttemptOptimized = optimized
		finalAttemptRole = role
	}

	req.Timestamp = start

	resp = ffuf.NewResponse(httpresp, req)
	defer httpresp.Body.Close()

	// Record the server-declared length first, so it is set before any early return
	// below and the size filters still observe it.
	if size, serr := strconv.Atoi(httpresp.Header.Get("Content-Length")); serr == nil {
		resp.ContentLength = int64(size)
		if !optimized && size > MAX_DOWNLOAD_SIZE {
			resp.Cancelled = true
			return resp, nil
		}
	}

	// -ignore-body has to hold regardless of framing. Keeping this check inside the
	// Content-Length branch above meant it was silently skipped for chunked
	// responses, which carry no Content-Length.
	if r.config.IgnoreBody && !optimized {
		resp.Cancelled = true
		return resp, nil
	}

	// Bound the body at the source, before anything reads it. The Content-Length
	// guard above cannot fire when the header is absent, which is the case for
	// chunked responses and for any response the transport transparently decoded,
	// and httputil.DumpResponse below drains the entire body with no bound of its
	// own.
	//
	// This does not replace the LimitReader further down, and which stream each of
	// them bounds depends on the path. Where the transport decoded the body itself,
	// both bound the same decompressed stream. Where Content-Encoding survived and
	// we decode manually, this one bounds the compressed bytes and that one bounds
	// what they expand to, so that case needs both.
	httpresp.Body = struct {
		io.Reader
		io.Closer
	}{io.LimitReader(httpresp.Body, int64(MAX_DOWNLOAD_SIZE)+1), httpresp.Body}

	if len(r.config.OutputDirectory) > 0 || len(r.config.AuditLog) > 0 {
		rawresp, _ := httputil.DumpResponse(httpresp, true)
		resp.Request.Raw = string(rawreq)
		resp.Raw = string(rawresp)
	}
	var bodyReader io.Reader = httpresp.Body
	switch httpresp.Header.Get("Content-Encoding") {
	case "gzip":
		if zr, zerr := gzip.NewReader(httpresp.Body); zerr == nil {
			bodyReader = zr
		} // else: fall back to the raw (undecoded) body
	case "br":
		bodyReader = brotli.NewReader(httpresp.Body)
	case "deflate":
		bodyReader = flate.NewReader(httpresp.Body)
	}

	// Bound the DECOMPRESSED read. The Content-Length guard above is not
	// sufficient on its own: a malicious server can send a small compressed body
	// (or a chunked response with no Content-Length, or one the transport
	// transparently decoded and stripped the headers from) that expands to
	// gigabytes. LimitReader caps the bytes we ever pull from the possibly-
	// decompressing reader, so a decompression bomb only materialises
	// MAX_DOWNLOAD_SIZE+1 bytes in memory instead of OOM-killing the process.
	limited := io.LimitReader(bodyReader, int64(MAX_DOWNLOAD_SIZE)+1)
	respbody, readErr := io.ReadAll(limited)
	if readErr != nil {
		return ffuf.Response{}, readErr
	}
	if len(respbody) > MAX_DOWNLOAD_SIZE {
		// The snapshot taken above holds a body truncated at the cap, which would
		// be written out as a well-formed response that merely looks complete.
		// Drop it rather than store a misleading artifact.
		resp.Raw = ""
		resp.Cancelled = true
		return resp, nil
	}

	resp.ContentLength = int64(len(respbody))
	resp.Data = respbody
	// Count words and lines without materializing a []string.
	resp.ContentWords = int64(bytes.Count(resp.Data, []byte(" ")) + 1)
	resp.ContentLines = int64(bytes.Count(resp.Data, []byte("\n")) + 1)
	resp.Duration = firstByteTime
	resp.Timestamp = start.Add(firstByteTime)

	return resp, nil
}

// validateAndCloseOptimizedResponse consumes only the bytes needed to prove the
// selected wire optimization still matches its pre-run contract. It always
// closes the body before returning so a caller can safely issue a full-GET
// fallback without retaining an HTTP/3 stream-admission slot.
type optimizedFallbackReason uint8

const (
	optimizedFallbackNone optimizedFallbackReason = iota
	optimizedFallbackTransport
	optimizedFallbackSemantic
)

func (r *SimpleRunner) validateAndCloseOptimizedResponse(httpresp *http.Response) (logicalResponseMetadata, optimizedFallbackReason) {
	if httpresp.Body == nil {
		httpresp.Body = http.NoBody
	}
	if r.optimization.mode == responseOptimizationHEAD && httpresp.Header.Get("Content-Length") == "" && httpresp.ContentLength >= 0 {
		// net/http may expose HEAD's declared length only on Response.ContentLength.
		httpresp.Header.Set("Content-Length", strconv.FormatInt(httpresp.ContentLength, 10))
	}

	assumedBytes := int64(0)
	readLimit := int64(1)
	maxBodyBytes := int64(0)
	switch r.optimization.mode {
	case responseOptimizationHEAD:
		// HEAD must be bodyless. One byte is sufficient to disprove that.
	case responseOptimizationRange:
		switch httpresp.StatusCode {
		case http.StatusPartialContent:
			assumedBytes = 1
			readLimit = 2 // one valid byte plus one byte to detect an oversized 206
			maxBodyBytes = 1
		case http.StatusRequestedRangeNotSatisfiable:
			// bytes */0 must be bodyless. One byte is sufficient to disprove that.
		default:
			// Status trust can be decided before touching a miss/error body. The
			// baseline caps such bodies at 4 KiB, so read one extra byte to detect
			// a route whose runtime representation no longer matches that bound.
			precheck := r.optimization.logicalMetadata(httpresp.StatusCode, httpresp.Header, 0)
			if !precheck.trustworthy {
				_ = httpresp.Body.Close()
				return logicalResponseMetadata{}, optimizedFallbackSemantic
			}
			readLimit = responseOptimizationMaxMissBody + 1
			maxBodyBytes = responseOptimizationMaxMissBody
		}
	default:
		_ = httpresp.Body.Close()
		return logicalResponseMetadata{}, optimizedFallbackSemantic
	}

	// Reject malformed headers or an unseen status without reading a body whose
	// contents cannot make the response trustworthy.
	precheck := r.optimization.logicalMetadata(httpresp.StatusCode, httpresp.Header, assumedBytes)
	if !precheck.trustworthy {
		_ = httpresp.Body.Close()
		return logicalResponseMetadata{}, optimizedFallbackSemantic
	}

	bodyBytes, readErr := io.CopyN(io.Discard, httpresp.Body, readLimit)
	closeErr := httpresp.Body.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return logicalResponseMetadata{}, optimizedFallbackTransport
	}
	if closeErr != nil {
		return logicalResponseMetadata{}, optimizedFallbackTransport
	}
	if bodyBytes > maxBodyBytes {
		return logicalResponseMetadata{}, optimizedFallbackSemantic
	}
	logical := r.optimization.logicalMetadata(httpresp.StatusCode, httpresp.Header, bodyBytes)
	if !logical.trustworthy {
		return logicalResponseMetadata{}, optimizedFallbackSemantic
	}
	return logical, optimizedFallbackNone
}

// parsePreflightRequest reads a Burp-style raw HTTP request file and builds an
// *http.Request. Main-config headers are inherited (so auth carries over) and any
// already-known vars are substituted into the request before it is built.
func (r *SimpleRunner) parsePreflightRequest(filename string, vars map[string]string) (*http.Request, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("preflight: could not open %q: %s", filename, err)
	}
	defer f.Close()
	reader := bufio.NewReader(f)

	// First line: METHOD path HTTP/version
	firstLine, err := reader.ReadString('\n')
	if err != nil && firstLine == "" {
		return nil, fmt.Errorf("preflight: could not read request line from %q: %s", filename, err)
	}
	parts := strings.SplitN(strings.TrimSpace(firstLine), " ", 3)
	if len(parts) < 2 {
		return nil, fmt.Errorf("preflight: malformed request line in %q", filename)
	}
	method, path := parts[0], parts[1]

	// Headers, seeded with the main config headers so auth etc. is inherited.
	headers := make(map[string]string)
	for k, v := range r.config.Headers {
		headers[k] = v
	}
	var host string
	for {
		line, rerr := reader.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break // blank line: end of the header section
		}
		// Parse the line BEFORE honoring rerr, so a final header with no trailing
		// newline (as many editors save) is not dropped.
		if p := strings.SplitN(line, ":", 2); len(p) == 2 {
			k, v := strings.TrimSpace(p[0]), strings.TrimSpace(p[1])
			switch {
			case strings.EqualFold(k, "content-length"): // let net/http compute it
			case strings.EqualFold(k, "host"):
				host = v // set via Request.Host, not a header
			default:
				headers[textproto.CanonicalMIMEHeaderKey(k)] = v
			}
		}
		if rerr != nil {
			break
		}
	}

	body, _ := io.ReadAll(reader)
	body = bytes.TrimRight(body, "\r\n") // trim trailing newline editors add

	// Record where the operator's own file says this request goes, before any
	// captured value is substituted in.
	authoredScheme, authoredHost := r.flightAuthority(path, host)

	// Substitute known variables into method, path, headers, host and body, in a
	// single deterministic pass.
	if len(vars) > 0 {
		rep := varsReplacer(vars)
		method = rep.Replace(method)
		path = rep.Replace(path)
		body = []byte(rep.Replace(string(body)))
		host = rep.Replace(host)
		for h, v := range headers {
			headers[h] = rep.Replace(v)
		}
	}

	// Captured values come from the scanned target, and flight requests carry the
	// main configuration's headers, so letting a captured value pick the
	// destination hands the target somewhere to send the operator's credentials.
	// An operator-authored destination is untouched by this: it only fires when
	// substitution is what moved the request, which is why comparing against the
	// pre-substitution authority is the check rather than pinning to the target
	// host. Discovery-driven flows that genuinely need the target to name the next
	// host opt in with -preflight-anyhost.
	if len(vars) > 0 && !r.config.PreflightAnyHost {
		gotScheme, gotHost := r.flightAuthority(path, host)
		if gotScheme != authoredScheme || gotHost != authoredHost {
			return nil, fmt.Errorf("preflight %q: a captured variable changed the request destination from %s://%s to %s://%s; captured values come from the scanned target, so this is refused. Pass -preflight-anyhost if the target is meant to choose the destination", filename, authoredScheme, authoredHost, gotScheme, gotHost)
		}
	}

	// Build the full URL, deriving scheme/host from the main target when the raw
	// request uses a relative path.
	var rawURL string
	if strings.HasPrefix(path, "http") {
		rawURL = path
	} else {
		scheme := "https"
		if u, uerr := url.Parse(r.config.Url); uerr == nil && u.Scheme != "" {
			scheme = u.Scheme
			if host == "" {
				host = u.Host
			}
		}
		// A relative-path preflight whose host was derived from the target template
		// during host/vhost fuzzing would carry an unsubstituted keyword; fail loudly
		// rather than send the request to a literal "FUZZ.example.com".
		if kw, ok := r.hostHasKeyword(host); ok {
			return nil, fmt.Errorf("preflight %q: derived target host %q still contains fuzz keyword %q; give the preflight file an absolute URL or a Host header", filename, host, kw)
		}
		rawURL = scheme + "://" + host + path
	}

	httpreq, err := http.NewRequestWithContext(r.requestContext(), method, rawURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("preflight: could not build request from %q: %s", filename, err)
	}
	if host != "" {
		httpreq.Host = host
	}
	if _, ok := headers["User-Agent"]; !ok {
		headers["User-Agent"] = fmt.Sprintf("%s v%s", "h3fuzz", ffuf.Version())
	}
	for k, v := range headers {
		httpreq.Header.Set(k, v)
	}
	return httpreq, nil
}

// runPreflightChain executes an ordered chain of requests, accumulating extracted
// variables. inheritVars seeds the map (e.g. with vars from an earlier step). The
// result is returned; no shared state is touched. On error it honours
// -preflight-error: "ignore" returns the vars gathered so far, "abort" errors.
func (r *SimpleRunner) runPreflightChain(chain []ffuf.PreflightConfig, inheritVars map[string]string) (map[string]string, error) {
	vars := make(map[string]string, len(inheritVars))
	for k, v := range inheritVars {
		vars[k] = v
	}
	ignore := r.config.PreflightError == "ignore"

	for _, pf := range chain {
		httpreq, err := r.parsePreflightRequest(pf.RequestFile, vars)
		if err != nil {
			if ignore {
				log.Printf("preflight ignored error building request from %q: %s", pf.RequestFile, err)
				return vars, nil
			}
			return nil, err
		}
		// Count this request against the shared rate limiter so preflight/postflight
		// traffic honors -rate/-p rather than firing unmetered.
		if r.config.RateLimitFunc != nil {
			r.config.RateLimitFunc()
		}
		resp, err := r.client.Do(httpreq)
		if err != nil {
			if ignore {
				log.Printf("preflight ignored error executing %q: %s", pf.RequestFile, err)
				return vars, nil
			}
			return nil, fmt.Errorf("preflight: request %q failed: %w", pf.RequestFile, err)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, int64(MAX_DOWNLOAD_SIZE)+1))
		resp.Body.Close()
		if err != nil {
			if ignore {
				log.Printf("preflight ignored error reading response from %q: %s", pf.RequestFile, err)
				return vars, nil
			}
			return nil, fmt.Errorf("preflight: reading response body from %q failed: %w", pf.RequestFile, err)
		}

		fr := &flightResponse{body: body, header: resp.Header}
		for j := range pf.Vars {
			// A pointer into the config's slice: extractByName keys its auto pins on it.
			ve := &pf.Vars[j]
			var val string
			if ve.Source != "" {
				v, xerr := r.extractByName(ve, fr)
				if xerr != nil {
					if ignore {
						log.Printf("preflight: var %s not extracted from %q: %s", ve.Name, pf.RequestFile, xerr)
						continue
					}
					return nil, fmt.Errorf("preflight: var %s not extracted from response of %q: %s", ve.Name, pf.RequestFile, xerr)
				}
				val = v
			} else {
				// Reuse the regex compiled once at config time; fall back to compiling
				// here only when a VarExtract was built outside ConfigFromOptions (tests).
				re := ve.Compiled
				if re == nil {
					var cerr error
					re, cerr = regexp.Compile(ve.Regex)
					if cerr != nil {
						if ignore {
							log.Printf("preflight ignored invalid regex %q for var %s: %s", ve.Regex, ve.Name, cerr)
							continue
						}
						return nil, fmt.Errorf("preflight: invalid regex %q for var %s: %s", ve.Regex, ve.Name, cerr)
					}
				}
				// The body is searched first, so a regex that matched before headers
				// were searched still captures the same text. Only a regex that finds
				// nothing in the body gets to look at the headers.
				matches := re.FindSubmatch(body)
				if len(matches) < 2 {
					matches = re.FindSubmatch(fr.headerBlock())
				}
				if len(matches) < 2 {
					if ignore {
						log.Printf("preflight: regex %q did not match var %s in the body or headers of the response from %q", ve.Regex, ve.Name, pf.RequestFile)
						continue
					}
					return nil, fmt.Errorf("preflight: regex %q did not capture var %s from the body or headers of the response of %q", ve.Regex, ve.Name, pf.RequestFile)
				}
				val = string(matches[1])
			}
			// The captured value comes from the scanned target (untrusted). Reject
			// control characters: they can't legally go into a URL/header and would
			// otherwise corrupt or fail the outgoing request (defense in depth).
			if strings.ContainsAny(val, "\r\n\x00") {
				if ignore {
					log.Printf("preflight: var %s from %q contains a control character, skipping", ve.Name, pf.RequestFile)
					continue
				}
				return nil, fmt.Errorf("preflight: var %s captured from %q contains a control character; refusing to use it", ve.Name, pf.RequestFile)
			}
			vars[ve.Name] = val
			log.Printf("preflight: extracted %s from %q", ve.Name, pf.RequestFile)
		}
	}
	return vars, nil
}

// varsReplacer builds one deterministic replacer for the extracted vars. Names
// are applied longest-first (then lexicographically) so a shorter name that is a
// prefix of a longer one can't win, and strings.Replacer does a single
// simultaneous pass, so a replaced value is never rescanned. That makes
// substitution independent of map iteration order and free of value-contains-name
// cascades.
func varsReplacer(vars map[string]string) *strings.Replacer {
	names := make([]string, 0, len(vars))
	for k := range vars {
		names = append(names, k)
	}
	sort.Slice(names, func(i, j int) bool {
		if len(names[i]) != len(names[j]) {
			return len(names[i]) > len(names[j])
		}
		return names[i] < names[j]
	})
	pairs := make([]string, 0, len(vars)*2)
	for _, n := range names {
		pairs = append(pairs, n, vars[n])
	}
	return strings.NewReplacer(pairs...)
}

// hostOf returns the authority (host[:port]) of a URL, or "" if it cannot be parsed.
// flightAuthority reports the scheme and host a flight request would be sent to,
// given the request-line path and Host header from its file. It mirrors the URL
// construction below, so the two stay in step.
func (r *SimpleRunner) flightAuthority(path, host string) (scheme, authority string) {
	if strings.HasPrefix(path, "http") {
		u, err := url.Parse(path)
		if err != nil {
			return "", ""
		}
		return u.Scheme, u.Host
	}
	scheme = "https"
	if u, uerr := url.Parse(r.config.Url); uerr == nil && u.Scheme != "" {
		scheme = u.Scheme
		if host == "" {
			host = u.Host
		}
	}
	return scheme, host
}

func hostOf(rawurl string) string {
	u, err := url.Parse(rawurl)
	if err != nil {
		return ""
	}
	return u.Host
}

// hostHasKeyword reports whether host still contains an input fuzz keyword, which
// means it was derived from an unsubstituted target template (e.g. vhost fuzzing
// with a relative-path preflight).
func (r *SimpleRunner) hostHasKeyword(host string) (string, bool) {
	for _, ip := range r.config.InputProviders {
		if ip.Keyword != "" && strings.Contains(host, ip.Keyword) {
			return ip.Keyword, true
		}
	}
	return "", false
}

// applyVars substitutes extracted variable keywords into a single request's URL,
// headers and body. It mutates only the request passed in.
func (r *SimpleRunner) applyVars(req *ffuf.Request, vars map[string]string) {
	if len(vars) == 0 {
		return
	}
	rep := varsReplacer(vars)
	req.Url = rep.Replace(req.Url)
	req.Data = []byte(rep.Replace(string(req.Data)))
	for h, v := range req.Headers {
		req.Headers[h] = rep.Replace(v)
	}
}

// runPreflights resolves the variables for this request per -preflight-mode
// (fresh per request, or once per lane and amortized in per-thread mode) and
// applies them. It returns the applied vars so postflight can chain off them, and
// a cleanup func the caller must defer (it returns a borrowed lane, if any).
func (r *SimpleRunner) runPreflights(req *ffuf.Request) (applied map[string]string, cleanup func(), err error) {
	cleanup = func() {}
	if len(r.config.Preflights) == 0 {
		return nil, cleanup, nil
	}
	if r.config.PreflightMode == "per-thread" {
		lane := r.lanes.get()
		cleanup = func() { r.lanes.put(lane) }
		if !lane.initialized {
			v, ferr := r.runPreflightChain(r.config.Preflights, nil)
			if ferr != nil {
				cleanup()
				return nil, func() {}, ferr
			}
			lane.vars = v
			lane.initialized = true
		}
		applied = lane.vars
	} else {
		v, ferr := r.runPreflightChain(r.config.Preflights, nil)
		if ferr != nil {
			return nil, cleanup, ferr
		}
		applied = v
	}
	if len(applied) > 0 {
		r.applyVars(req, applied)
	}
	return applied, cleanup, nil
}

// runPostflights executes the postflight chain after the main request, seeded
// with the vars applied to it so chained extractions work. Postflight failure is
// non-fatal: the main result is always kept, the error is only logged.
func (r *SimpleRunner) runPostflights(seedVars map[string]string) {
	if len(r.config.Postflights) == 0 {
		return
	}
	if _, ferr := r.runPreflightChain(r.config.Postflights, seedVars); ferr != nil {
		log.Printf("postflight error (result still recorded): %s", ferr)
	}
}

func (r *SimpleRunner) Dump(req *ffuf.Request) ([]byte, error) {
	var httpreq *http.Request
	var err error
	data := bytes.NewReader(req.Data)
	httpreq, err = http.NewRequestWithContext(r.config.Context, req.Method, req.Url, data)
	if err != nil {
		return []byte{}, err
	}

	// set default User-Agent header if not present
	if _, ok := req.Headers["User-Agent"]; !ok {
		req.Headers["User-Agent"] = fmt.Sprintf("%s v%s", "h3fuzz", ffuf.Version())
	}

	// Handle Go http.Request special cases
	if _, ok := req.Headers["Host"]; ok {
		httpreq.Host = req.Headers["Host"]
	}

	req.Host = httpreq.Host
	for k, v := range req.Headers {
		httpreq.Header.Set(k, v)
	}
	return httputil.DumpRequestOut(httpreq, true)
}
