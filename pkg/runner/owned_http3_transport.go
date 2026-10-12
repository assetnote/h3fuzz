package runner

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

// ownedHTTP3Transport is a one-authority transport with explicit QUIC
// generation ownership. quic-go v0.63's Transport removes cached connections
// by hostname, so a late error from retired generation A can remove healthy B
// and leave both generations outside Transport.Close. Keeping the current
// generation here makes retirement compare-and-swap on identity instead.
type ownedHTTP3Transport struct {
	*http3.Transport

	authority     string
	tlsConfig     *tls.Config
	quicConfig    *quic.Config
	resolveAddrs  func(context.Context, string) ([]*net.UDPAddr, error)
	dialEarly     func(context.Context, *quic.Transport, net.Addr, *tls.Config, *quic.Config) (*quic.Conn, error)
	reconnect     *http3ReconnectGate
	retireTimeout time.Duration

	current       atomic.Pointer[ownedHTTP3Generation]
	closed        atomic.Bool
	everConnected atomic.Bool
	dialMu        sync.Mutex
	closeContext  context.Context
	cancelClose   context.CancelCauseFunc

	lifecycleMu   sync.Mutex
	packetConn    *net.UDPConn
	quicTransport *quic.Transport
	generations   map[*ownedHTTP3Generation]struct{}

	closeOnce sync.Once
	closeErr  error
}

type ownedHTTP3Generation struct {
	conn   *quic.Conn
	client *http3.ClientConn

	accepting atomic.Bool
	inFlight  atomic.Int64
	closeOnce sync.Once
}

func newOwnedHTTP3Transport(authority string, tlsConfig *tls.Config, quicConfig *quic.Config, reconnect *http3ReconnectGate) *ownedHTTP3Transport {
	closeContext, cancelClose := context.WithCancelCause(context.Background())
	codec := &http3.Transport{
		TLSClientConfig:        tlsConfig,
		QUICConfig:             quicConfig,
		EnableDatagrams:        true,
		DisableCompression:     true,
		MaxResponseHeaderBytes: 1 << 20,
	}
	return &ownedHTTP3Transport{
		Transport:     codec,
		authority:     authority,
		tlsConfig:     tlsConfig,
		quicConfig:    quicConfig,
		reconnect:     reconnect,
		retireTimeout: ownedHTTP3RetireTimeout(quicConfig),
		closeContext:  closeContext,
		cancelClose:   cancelClose,
		resolveAddrs:  resolveOwnedHTTP3Addrs,
		dialEarly: func(ctx context.Context, transport *quic.Transport, addr net.Addr, tlsConfig *tls.Config, config *quic.Config) (*quic.Conn, error) {
			return transport.DialEarly(ctx, addr, tlsConfig, config)
		},
		generations: make(map[*ownedHTTP3Generation]struct{}),
	}
}

func (t *ownedHTTP3Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	response, err := t.roundTrip(req)
	if err != nil && req.Body != nil {
		_ = req.Body.Close()
	}
	return response, err
}

func (t *ownedHTTP3Transport) roundTrip(req *http.Request) (*http.Response, error) {
	var retryBudget ownedHTTP3RetryBudget
	firstAttempt := true
	for {
		generation, err := t.acquireGeneration(req)
		if err != nil {
			return nil, err
		}
		response, err := generation.client.RoundTrip(req)
		if err == nil {
			return t.trackResponse(generation, response), nil
		}
		if req.Context().Err() != nil {
			t.releaseGeneration(generation)
			return nil, err
		}

		retryRequest, retryReason, retryErr := ownedHTTP3RetryRequest(err, req)
		retry := retryErr == nil && retryBudget.take(retryReason)
		// Match the pinned Transport contract for the first non-context error.
		// A later error retires its exact generation only when it qualifies for
		// the other independently bounded safe retry. Exhausted or unsafe
		// errors stay on the current generation so repeated failures cannot
		// churn connections without bound.
		if firstAttempt || retry {
			t.retireGeneration(generation)
		}
		t.releaseGeneration(generation)
		if !retry {
			if retryErr != nil {
				return nil, retryErr
			}
			return nil, err
		}

		req = retryRequest
		firstAttempt = false
	}
}

func (t *ownedHTTP3Transport) acquireGeneration(req *http.Request) (*ownedHTTP3Generation, error) {
	trace := httptrace.ContextClientTrace(req.Context())
	if trace != nil && trace.GetConn != nil {
		trace.GetConn(t.authority)
	}
	for {
		if t.closed.Load() {
			return nil, http3.ErrTransportClosed
		}
		generation := t.current.Load()
		if generation == nil {
			return t.dialGeneration(req.Context(), trace)
		}
		if t.reserveGeneration(generation) {
			ownedTraceGotConn(trace, generation.conn, true)
			return generation, nil
		}
	}
}

func (t *ownedHTTP3Transport) dialGeneration(ctx context.Context, trace *httptrace.ClientTrace) (*ownedHTTP3Generation, error) {
	t.dialMu.Lock()
	defer t.dialMu.Unlock()
	if t.closed.Load() {
		return nil, http3.ErrTransportClosed
	}
	if generation := t.current.Load(); generation != nil {
		if t.reserveGeneration(generation) {
			ownedTraceGotConn(trace, generation.conn, true)
			return generation, nil
		}
	}

	dialContext, cancelDial := context.WithCancelCause(ctx)
	stopCloseCancellation := func() bool { return true }
	if t.closeContext != nil {
		stopCloseCancellation = context.AfterFunc(t.closeContext, func() {
			cancelDial(http3.ErrTransportClosed)
		})
	}
	defer func() {
		stopCloseCancellation()
		cancelDial(context.Canceled)
	}()

	var reconnectPermit *http3ReconnectPermit
	reconnectSucceeded := false
	if t.reconnect != nil && t.everConnected.Load() {
		var err error
		reconnectPermit, err = t.reconnect.acquire(dialContext)
		if err != nil {
			return nil, err
		}
	}
	if reconnectPermit != nil {
		defer func() { reconnectPermit.finish(reconnectSucceeded) }()
	}

	remotes, err := t.resolveAddrs(dialContext, t.authority)
	if err != nil {
		return nil, err
	}
	quicTransport, err := t.ensureQUICTransport()
	if err != nil {
		return nil, err
	}
	conn, dialErr := t.dialResolvedCandidates(dialContext, trace, quicTransport, remotes)
	if dialErr != nil {
		return nil, dialErr
	}
	if t.closed.Load() {
		_ = conn.CloseWithError(quic.ApplicationErrorCode(http3.ErrCodeNoError), "transport closed")
		return nil, http3.ErrTransportClosed
	}
	generation := &ownedHTTP3Generation{conn: conn}
	generation.client = t.Transport.NewClientConn(conn)
	generation.accepting.Store(true)
	generation.inFlight.Store(1)
	t.lifecycleMu.Lock()
	t.generations[generation] = struct{}{}
	t.lifecycleMu.Unlock()
	t.current.Store(generation)
	t.everConnected.Store(true)
	reconnectSucceeded = true
	ownedTraceGotConn(trace, conn, false)
	return generation, nil
}

func (t *ownedHTTP3Transport) dialResolvedCandidates(ctx context.Context, trace *httptrace.ClientTrace, quicTransport *quic.Transport, remotes []*net.UDPAddr) (*quic.Conn, error) {
	dialErrors := make([]error, 0, len(remotes))
	for index, remote := range remotes {
		if cause := context.Cause(ctx); cause != nil {
			return nil, cause
		}
		network, address := "udp", remote.String()
		if trace != nil && trace.ConnectStart != nil {
			trace.ConnectStart(network, address)
		}
		if trace != nil && trace.TLSHandshakeStart != nil {
			trace.TLSHandshakeStart()
		}
		attemptCtx := ctx
		cancelAttempt := func() {}
		remainingCandidates := len(remotes) - index
		if remainingCandidates > 1 {
			if deadline, hasDeadline := ctx.Deadline(); hasDeadline {
				// HandshakeIdleTimeout only measures periods without network
				// activity, so it cannot guarantee that a later DNS candidate is
				// attempted. Give each non-final candidate an explicit share of
				// the remaining outer deadline; the final candidate retains the
				// caller's context and all remaining time.
				budget := time.Until(deadline) / time.Duration(remainingCandidates)
				attemptCtx, cancelAttempt = context.WithTimeout(ctx, maxDuration(0, budget))
			}
		}
		conn, dialErr := t.dialEarly(attemptCtx, quicTransport, remote, t.tlsConfig.Clone(), t.quicConfig.Clone())
		cancelAttempt()
		var state tls.ConnectionState
		if conn != nil {
			state = conn.ConnectionState().TLS
		}
		if trace != nil && trace.TLSHandshakeDone != nil {
			trace.TLSHandshakeDone(state, dialErr)
		}
		if trace != nil && trace.ConnectDone != nil {
			trace.ConnectDone(network, address, dialErr)
		}
		if cause := context.Cause(ctx); cause != nil {
			if conn != nil {
				_ = conn.CloseWithError(quic.ApplicationErrorCode(http3.ErrCodeNoError), "dial context canceled")
			}
			return nil, cause
		}
		if dialErr == nil {
			return conn, nil
		}
		dialErrors = append(dialErrors, fmt.Errorf("dial %s: %w", address, dialErr))
	}
	if len(dialErrors) == 0 {
		return nil, fmt.Errorf("no IP address found for %s", t.authority)
	}
	return nil, errors.Join(dialErrors...)
}

func (t *ownedHTTP3Transport) reserveGeneration(generation *ownedHTTP3Generation) bool {
	if generation == nil || !generation.accepting.Load() {
		return false
	}
	generation.inFlight.Add(1)
	if generation.accepting.Load() && t.current.Load() == generation {
		return true
	}
	// Retirement won after this request loaded an old published pointer.
	// Roll the reservation back; it must never reach ClientConn.RoundTrip.
	t.releaseGeneration(generation)
	return false
}

func (t *ownedHTTP3Transport) ensureQUICTransport() (*quic.Transport, error) {
	t.lifecycleMu.Lock()
	defer t.lifecycleMu.Unlock()
	if t.quicTransport != nil {
		return t.quicTransport, nil
	}
	packetConn, err := net.ListenUDP("udp", nil)
	if err != nil {
		return nil, err
	}
	t.packetConn = packetConn
	t.quicTransport = &quic.Transport{Conn: packetConn}
	return t.quicTransport, nil
}

func (t *ownedHTTP3Transport) retireGeneration(generation *ownedHTTP3Generation) {
	if t.current.Load() != generation {
		return
	}
	if !generation.accepting.CompareAndSwap(true, false) {
		return
	}
	if !t.current.CompareAndSwap(generation, nil) {
		return
	}
	if generation.inFlight.Load() == 0 {
		t.closeGeneration(generation)
		return
	}
	if t.retireTimeout > 0 {
		time.AfterFunc(t.retireTimeout, func() {
			t.closeGeneration(generation)
		})
	}
}

func (t *ownedHTTP3Transport) releaseGeneration(generation *ownedHTTP3Generation) {
	remaining := generation.inFlight.Add(-1)
	if remaining < 0 {
		panic("owned HTTP/3 generation in-flight count became negative")
	}
	if remaining == 0 && !generation.accepting.Load() {
		t.closeGeneration(generation)
	}
}

func (t *ownedHTTP3Transport) closeGeneration(generation *ownedHTTP3Generation) {
	generation.closeOnce.Do(func() {
		_ = generation.client.CloseWithError(quic.ApplicationErrorCode(http3.ErrCodeNoError), "generation retired")
		t.lifecycleMu.Lock()
		delete(t.generations, generation)
		t.lifecycleMu.Unlock()
	})
}

func (t *ownedHTTP3Transport) trackResponse(generation *ownedHTTP3Generation, response *http.Response) *http.Response {
	if response == nil || response.Body == nil || response.Body == http.NoBody {
		t.releaseGeneration(generation)
		return response
	}
	response.Body = &ownedHTTP3ResponseBody{
		ReadCloser: response.Body,
		owner:      t,
		generation: generation,
	}
	return response
}

func (t *ownedHTTP3Transport) Close() error {
	t.closeOnce.Do(func() {
		t.closed.Store(true)
		if t.cancelClose != nil {
			t.cancelClose(http3.ErrTransportClosed)
		}
		t.dialMu.Lock()
		defer t.dialMu.Unlock()
		if current := t.current.Swap(nil); current != nil {
			current.accepting.Store(false)
		}
		t.lifecycleMu.Lock()
		generations := make([]*ownedHTTP3Generation, 0, len(t.generations))
		for generation := range t.generations {
			generation.accepting.Store(false)
			generations = append(generations, generation)
		}
		quicTransport := t.quicTransport
		packetConn := t.packetConn
		t.lifecycleMu.Unlock()
		for _, generation := range generations {
			t.closeGeneration(generation)
		}
		if quicTransport != nil {
			t.closeErr = errors.Join(t.closeErr, quicTransport.Close())
		}
		if packetConn != nil {
			t.closeErr = errors.Join(t.closeErr, packetConn.Close())
		}
	})
	return t.closeErr
}

type ownedHTTP3ResponseBody struct {
	io.ReadCloser
	owner      *ownedHTTP3Transport
	generation *ownedHTTP3Generation
	finished   atomic.Bool

	// The pool attaches these before exposing the response. Keeping both
	// lifetimes in this wrapper avoids another per-response allocation.
	slot *http3PoolSlot
}

func (b *ownedHTTP3ResponseBody) Read(dst []byte) (int, error) {
	n, err := b.ReadCloser.Read(dst)
	if err == io.EOF {
		b.finish(true)
	} else if err != nil {
		b.finish(false)
	}
	return n, err
}

func (b *ownedHTTP3ResponseBody) Close() error {
	err := b.ReadCloser.Close()
	b.finish(err == nil)
	return err
}

func (b *ownedHTTP3ResponseBody) finish(success bool) {
	if b.finished.CompareAndSwap(false, true) {
		b.owner.releaseGeneration(b.generation)
		if b.slot != nil {
			finishHTTP3PoolResponse(b.slot, b.slot.pool.limiter, b.slot.pool, success)
		}
	}
}

type ownedHTTP3RetryReason uint8

const (
	ownedHTTP3RetryNone ownedHTTP3RetryReason = iota
	ownedHTTP3RetryConnectionUnusable
	ownedHTTP3RetryRequestRejected
)

type ownedHTTP3RetryBudget struct {
	connectionUnusable bool
	requestRejected    bool
}

// take allows each protocol guarantee to trigger at most one replacement.
// A connection-unusable error means no request stream was opened, while
// H3_REQUEST_REJECTED means the server explicitly rejected processing. Keeping
// separate one-shot budgets covers either ordering without permitting an
// unbounded reconnect loop.
func (b *ownedHTTP3RetryBudget) take(reason ownedHTTP3RetryReason) bool {
	switch reason {
	case ownedHTTP3RetryConnectionUnusable:
		if b.connectionUnusable {
			return false
		}
		b.connectionUnusable = true
		return true
	case ownedHTTP3RetryRequestRejected:
		if b.requestRejected {
			return false
		}
		b.requestRejected = true
		return true
	default:
		return false
	}
}

func ownedHTTP3RetryRequest(err error, req *http.Request) (*http.Request, ownedHTTP3RetryReason, error) {
	if isOwnedHTTP3ConnUnusable(err) {
		return req, ownedHTTP3RetryConnectionUnusable, nil
	}
	var h3Error *http3.Error
	if !errors.As(err, &h3Error) || h3Error.ErrorCode != http3.ErrCodeRequestRejected {
		return nil, ownedHTTP3RetryNone, err
	}
	if req.Body == nil || req.Body == http.NoBody {
		return req, ownedHTTP3RetryRequestRejected, nil
	}
	if req.GetBody == nil {
		return nil, ownedHTTP3RetryNone, fmt.Errorf("http3: Transport: cannot retry err [%w] after Request.Body was written; define Request.GetBody to avoid this error", err)
	}
	body, bodyErr := req.GetBody()
	if bodyErr != nil {
		return nil, ownedHTTP3RetryNone, bodyErr
	}
	requestCopy := *req
	requestCopy.Body = body
	return &requestCopy, ownedHTTP3RetryRequestRejected, nil
}

func ownedHTTP3RetireTimeout(config *quic.Config) time.Duration {
	timeout := 10 * time.Second
	if config != nil && config.HandshakeIdleTimeout > 0 {
		// http3QUICConfig uses half of the effective probe timeout for the
		// handshake deadline. PERF-103's retirement bound is max(10s, 3*probe).
		timeout = maxDuration(timeout, 6*config.HandshakeIdleTimeout)
	}
	return timeout
}

// errConnUnusable is intentionally unexported by quic-go. The exact pinned
// v0.63 contract treats it as safe because no request stream was opened. Match
// its package and type name rather than an error string that an unrelated
// error could reproduce.
func isOwnedHTTP3ConnUnusable(err error) bool {
	for current := err; current != nil; current = errors.Unwrap(current) {
		typeOf := reflect.TypeOf(current)
		if typeOf.Kind() == reflect.Pointer {
			typeOf = typeOf.Elem()
		}
		if typeOf.PkgPath() == "github.com/quic-go/quic-go/http3" && typeOf.Name() == "errConnUnusable" {
			return true
		}
	}
	return false
}

func resolveOwnedHTTP3Addrs(ctx context.Context, authority string) ([]*net.UDPAddr, error) {
	host, port, err := net.SplitHostPort(authority)
	if err != nil {
		host = authority
		port = "443"
		// url.URL.Host preserves brackets around an IPv6 literal even when the
		// default port is omitted. Remove exactly that syntactic pair before
		// asking the resolver for the literal address.
		if len(host) >= 2 && host[0] == '[' && host[len(host)-1] == ']' {
			host = host[1 : len(host)-1]
		}
	}
	portNumber, err := net.LookupPort("udp", port)
	if err != nil {
		return nil, err
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("no IP address found for %s", host)
	}
	return ownedHTTP3UDPAddrs(addresses, portNumber), nil
}

func ownedHTTP3UDPAddrs(addresses []net.IPAddr, portNumber int) []*net.UDPAddr {
	resolved := make([]*net.UDPAddr, 0, len(addresses))
	seen := make(map[string]struct{}, len(addresses))
	// Match quic-go's HTTP/3 hostname resolver by preferring IPv4 while
	// retaining every candidate as a fallback. In particular, this keeps the
	// capability probe and the owned high-throughput pool on the same address
	// when a hostname such as localhost resolves to both families.
	for _, ipv4 := range []bool{true, false} {
		for _, address := range addresses {
			if (address.IP.To4() != nil) != ipv4 {
				continue
			}
			udpAddr := &net.UDPAddr{IP: address.IP, Port: portNumber, Zone: address.Zone}
			key := udpAddr.String()
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			resolved = append(resolved, udpAddr)
		}
	}
	return resolved
}

type ownedHTTP3TraceConn struct {
	conn *quic.Conn
}

func (c *ownedHTTP3TraceConn) Read([]byte) (int, error)    { panic("connection operation prohibited") }
func (c *ownedHTTP3TraceConn) Write([]byte) (int, error)   { panic("connection operation prohibited") }
func (c *ownedHTTP3TraceConn) Close() error                { panic("connection operation prohibited") }
func (c *ownedHTTP3TraceConn) LocalAddr() net.Addr         { return c.conn.LocalAddr() }
func (c *ownedHTTP3TraceConn) RemoteAddr() net.Addr        { return c.conn.RemoteAddr() }
func (c *ownedHTTP3TraceConn) SetDeadline(time.Time) error { panic("connection operation prohibited") }
func (c *ownedHTTP3TraceConn) SetReadDeadline(time.Time) error {
	panic("connection operation prohibited")
}
func (c *ownedHTTP3TraceConn) SetWriteDeadline(time.Time) error {
	panic("connection operation prohibited")
}

func ownedTraceGotConn(trace *httptrace.ClientTrace, conn *quic.Conn, reused bool) {
	if trace != nil && trace.GotConn != nil {
		trace.GotConn(httptrace.GotConnInfo{Conn: &ownedHTTP3TraceConn{conn: conn}, Reused: reused})
	}
}
