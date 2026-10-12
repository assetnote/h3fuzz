package runner

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

const (
	fingerprintBurstSize = 16
	fingerprintBodyLimit = 1 << 20
)

// FingerprintHTTP3 performs a direct QUIC probe. It intentionally does not
// trust Alt-Svc: a target is classified as supported only after negotiating
// ALPN h3, receiving the peer SETTINGS, and completing HTTP requests on the
// same connection. Network/handshake failures are capability results, while
// malformed local configuration is returned as an error.
func FingerprintHTTP3(conf *ffuf.Config, detailed bool) (report ffuf.HTTP3Fingerprint, configErr error) {
	started := time.Now()
	report.Target = conf.Url
	defer func() { report.ElapsedMillis = time.Since(started).Milliseconds() }()

	probeURL, err := http3ProbeURL(conf, "")
	if err != nil {
		return report, err
	}
	if probeURL.Scheme != "https" {
		report.Reason = "HTTP/3 requires an https target"
		return report, nil
	}
	if conf.ProxyURL != "" {
		report.Reason = "HTTP/3 direct probing is disabled when an upstream proxy is configured"
		return report, nil
	}
	proxyURL, proxyErr := http.ProxyFromEnvironment(&http.Request{
		Method: http.MethodHead,
		URL:    probeURL,
	})
	if proxyErr != nil {
		return report, fmt.Errorf("resolve environment proxy for HTTP/3 fingerprint: %w", proxyErr)
	}
	if proxyURL != nil {
		report.Reason = "HTTP/3 direct probing is disabled when an environment proxy applies to the target"
		return report, nil
	}

	tlsConf, err := http3TLSConfig(conf, probeURL)
	if err != nil {
		return report, err
	}
	timeout := time.Duration(conf.Http3ProbeTimeout) * time.Second
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	parent := conf.Context
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	quicConf := http3QUICConfig(timeout)
	address := probeURL.Host
	if probeURL.Port() == "" {
		address = net.JoinHostPort(probeURL.Hostname(), "443")
	}
	conn, err := quic.DialAddr(ctx, address, tlsConf, quicConf)
	if err != nil {
		report.Reason = "direct QUIC handshake failed: " + err.Error()
		return report, nil
	}
	defer func() {
		_ = conn.CloseWithError(0, "fingerprint complete")
	}()

	transport := &http3.Transport{
		TLSClientConfig:        tlsConf,
		QUICConfig:             quicConf,
		EnableDatagrams:        true,
		DisableCompression:     true,
		MaxResponseHeaderBytes: 1 << 20,
	}
	clientConn := transport.NewClientConn(conn)

	select {
	case <-clientConn.ReceivedSettings():
	case <-ctx.Done():
		report.Reason = "timed out waiting for the server HTTP/3 SETTINGS: " + ctx.Err().Error()
		return report, nil
	}

	state := conn.ConnectionState()
	report.ALPN = state.TLS.NegotiatedProtocol
	report.QUICVersion = state.Version.String()
	report.QUICDatagrams = state.SupportsDatagrams.Remote
	report.RemoteAddr = conn.RemoteAddr().String()
	settings := clientConn.Settings()
	report.HTTP3Datagrams = settings != nil && settings.EnableDatagrams
	if report.ALPN != http3.NextProtoH3 {
		report.Reason = fmt.Sprintf("unexpected ALPN %q", report.ALPN)
		return report, nil
	}

	first, err := fingerprintRoundTrip(ctx, clientConn, conf, probeURL)
	if err != nil {
		report.Reason = "HTTP/3 request failed after handshake: " + err.Error()
		return report, nil
	}
	report.StatusCode = first.StatusCode
	report.HTTPVersion = first.Proto
	report.Supported = first.ProtoMajor == 3
	if !report.Supported {
		report.Reason = fmt.Sprintf("response used %s after h3 negotiation", first.Proto)
		return report, nil
	}

	if _, err = fingerprintRoundTrip(ctx, clientConn, conf, probeURL); err != nil {
		report.Reason = "second request on the negotiated connection failed: " + err.Error()
		return report, nil
	}
	report.ConnectionReused = true

	attempted := 1
	if detailed {
		attempted = fingerprintBurstSize
	}
	report.ConcurrentStreamsAttempted = attempted
	var completed int
	var wg sync.WaitGroup
	wg.Add(attempted)
	for i := 0; i < attempted; i++ {
		go func() {
			defer wg.Done()
			if _, burstErr := fingerprintRoundTrip(ctx, clientConn, conf, probeURL); burstErr == nil {
				// A mutex is cheaper than making the public report field atomic and this
				// path runs only during the explicit fingerprint burst.
				fingerprintCompletedMu.Lock()
				completed++
				fingerprintCompletedMu.Unlock()
			}
		}()
	}
	wg.Wait()
	report.ConcurrentStreamsCompleted = completed
	report.HighRateReady = report.Supported && report.ConnectionReused && completed == attempted
	if !report.HighRateReady {
		report.Reason = fmt.Sprintf("only %d of %d concurrent HTTP/3 streams completed", completed, attempted)
	}
	return report, nil
}

// The fingerprint operation is rare, so a package mutex avoids a per-goroutine
// allocation solely for incrementing a local counter. Separate fingerprint calls
// are also serialized at this tiny critical section only, not across network I/O.
var fingerprintCompletedMu sync.Mutex

func fingerprintRoundTrip(ctx context.Context, conn *http3.ClientConn, conf *ffuf.Config, target *url.URL) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, target.String(), nil)
	if err != nil {
		return nil, err
	}
	copyHTTP3Headers(req, conf.Headers)
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", fmt.Sprintf("h3fuzz v%s", ffuf.Version()))
	}
	resp, err := conn.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	n, readErr := io.Copy(io.Discard, io.LimitReader(resp.Body, fingerprintBodyLimit+1))
	if readErr != nil {
		return nil, readErr
	}
	if n > fingerprintBodyLimit {
		return nil, fmt.Errorf("HEAD probe returned more than %d response-body bytes", fingerprintBodyLimit)
	}
	return resp, nil
}

func http3ProbeURL(conf *ffuf.Config, replacement string) (*url.URL, error) {
	raw := conf.Url
	keywordSet := map[string]struct{}{"FUZZ": {}}
	for _, provider := range conf.InputProviders {
		if provider.Keyword != "" {
			keywordSet[provider.Keyword] = struct{}{}
		}
	}
	keywords := make([]string, 0, len(keywordSet))
	for keyword := range keywordSet {
		keywords = append(keywords, keyword)
	}
	sort.Slice(keywords, func(i, j int) bool {
		if len(keywords[i]) == len(keywords[j]) {
			return keywords[i] < keywords[j]
		}
		return len(keywords[i]) > len(keywords[j])
	})
	for _, keyword := range keywords {
		raw = strings.ReplaceAll(raw, keyword, replacement)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid target URL for HTTP/3 probe: %w", err)
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("HTTP/3 probe target has no hostname: %q", raw)
	}
	return u, nil
}

func http3TLSConfig(conf *ffuf.Config, target *url.URL) (*tls.Config, error) {
	serverName := conf.SNI
	if serverName == "" {
		serverName = target.Hostname()
	}
	certificates := []tls.Certificate{}
	if conf.ClientCert != "" || conf.ClientKey != "" {
		if conf.ClientCert == "" || conf.ClientKey == "" {
			return nil, fmt.Errorf("both client certificate and key are required for HTTP/3")
		}
		cert, err := tls.LoadX509KeyPair(conf.ClientCert, conf.ClientKey)
		if err != nil {
			return nil, fmt.Errorf("load HTTP/3 client certificate: %w", err)
		}
		certificates = append(certificates, cert)
	}
	return &tls.Config{
		Certificates:       certificates,
		InsecureSkipVerify: true, // matches ffuf's existing transport semantics
		MinVersion:         tls.VersionTLS13,
		NextProtos:         []string{http3.NextProtoH3},
		ServerName:         serverName,
	}, nil
}

func http3QUICConfig(timeout time.Duration) *quic.Config {
	handshakeIdle := timeout / 2
	if handshakeIdle < 250*time.Millisecond {
		handshakeIdle = 250 * time.Millisecond
	}
	return &quic.Config{
		EnableDatagrams:      true,
		HandshakeIdleTimeout: handshakeIdle,
		MaxIdleTimeout:       30 * time.Second,
		KeepAlivePeriod:      10 * time.Second,
	}
}

func copyHTTP3Headers(req *http.Request, headers map[string]string) {
	connectionTokens := make(map[string]struct{})
	for key, value := range headers {
		if strings.EqualFold(key, "Connection") {
			for _, token := range strings.Split(value, ",") {
				connectionTokens[strings.ToLower(strings.TrimSpace(token))] = struct{}{}
			}
		}
	}
	for key, value := range headers {
		lower := strings.ToLower(key)
		if _, namedByConnection := connectionTokens[lower]; namedByConnection {
			continue
		}
		switch lower {
		case "connection", "keep-alive", "proxy-connection", "transfer-encoding", "upgrade":
			continue
		case "host":
			req.Host = value
		default:
			req.Header.Set(key, value)
		}
	}
}
