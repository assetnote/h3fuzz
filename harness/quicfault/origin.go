// Package quicfault provides a deterministic, connection-aware HTTP/3 origin
// for transport recovery tests. It is deliberately test-oriented: callers can
// reject one request stream, hold an older-generation request until a newer
// connection is usable, and inspect which server-side QUIC connections remain
// alive after client drain.
package quicfault

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

const (
	ConnectionHeader = "X-H3fuzz-Fault-Connection"

	PathOK          = "/ok"
	PathRejectOnce  = "/reject-once"
	PathRejectTwice = "/reject-twice"
	PathHoldReject  = "/hold-reject"
	PathStreamReset = "/stream-reset"
	PathWait        = "/wait"
)

type connectionContextKey struct{}

type connectionState struct {
	id   uint64
	conn *quic.Conn
}

// Origin owns a real UDP listener and HTTP/3 server. Its controls are safe for
// concurrent use and intentionally expose only deterministic fault operations.
type Origin struct {
	listener *quic.EarlyListener
	server   *http3.Server
	serveErr chan error

	nextConnection atomic.Uint64
	rejectOnce     atomic.Bool
	rejectTwice    atomic.Uint64
	heldConnection atomic.Uint64
	heldStarted    chan uint64
	releaseHeld    chan struct{}
	releaseOnce    sync.Once
	waitStarted    chan uint64

	mu          sync.RWMutex
	connections map[uint64]*connectionState
	closeOnce   sync.Once
}

// Start starts a loopback HTTP/3 origin with a generated test certificate.
func Start() (*Origin, error) {
	certificate, err := testCertificate()
	if err != nil {
		return nil, err
	}
	packetConn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen UDP fault origin: %w", err)
	}
	tlsConfig := http3.ConfigureTLSConfig(&tls.Config{
		Certificates: []tls.Certificate{certificate},
		MinVersion:   tls.VersionTLS13,
	})
	listener, err := quic.ListenEarly(packetConn, tlsConfig, &quic.Config{
		MaxIdleTimeout:  30 * time.Second,
		KeepAlivePeriod: time.Second,
	})
	if err != nil {
		_ = packetConn.Close()
		return nil, fmt.Errorf("listen QUIC fault origin: %w", err)
	}
	origin := &Origin{
		listener:    listener,
		serveErr:    make(chan error, 1),
		heldStarted: make(chan uint64, 1),
		releaseHeld: make(chan struct{}),
		waitStarted: make(chan uint64, 1),
		connections: make(map[uint64]*connectionState),
	}
	origin.server = &http3.Server{
		Handler: origin,
		ConnContext: func(ctx context.Context, conn *quic.Conn) context.Context {
			id := origin.nextConnection.Add(1)
			state := &connectionState{id: id, conn: conn}
			origin.mu.Lock()
			origin.connections[id] = state
			origin.mu.Unlock()
			return context.WithValue(ctx, connectionContextKey{}, state)
		},
	}
	go func() {
		origin.serveErr <- origin.server.ServeListener(listener)
	}()
	return origin, nil
}

// WaitBlocked waits until a PathWait handler is waiting for caller
// cancellation or deadline expiry.
func (o *Origin) WaitBlocked(ctx context.Context) (uint64, error) {
	select {
	case id := <-o.waitStarted:
		return id, nil
	case <-ctx.Done():
		return 0, context.Cause(ctx)
	}
}

// URL returns an https URL rooted at the fault origin.
func (o *Origin) URL(path string) string {
	return "https://" + o.listener.Addr().String() + path
}

// ClientTLSConfig returns a test-only client configuration for the generated
// certificate.
func (o *Origin) ClientTLSConfig() *tls.Config {
	return &tls.Config{ // #nosec G402 -- the origin uses an ephemeral test certificate.
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS13,
		NextProtos:         []string{http3.NextProtoH3},
	}
}

// WaitHeld waits until PathHoldReject is blocked on its original connection.
func (o *Origin) WaitHeld(ctx context.Context) (uint64, error) {
	select {
	case id := <-o.heldStarted:
		return id, nil
	case <-ctx.Done():
		return 0, context.Cause(ctx)
	}
}

// ReleaseHeld makes the blocked old-generation request fail with
// H3_REQUEST_REJECTED. Its transparent retry will succeed on a newer
// connection.
func (o *Origin) ReleaseHeld() {
	o.releaseOnce.Do(func() { close(o.releaseHeld) })
}

// ConnectionCount returns how many QUIC connections reached ConnContext.
func (o *Origin) ConnectionCount() uint64 {
	return o.nextConnection.Load()
}

// ConnectionAlive reports whether the named server-side QUIC connection is
// still open.
func (o *Origin) ConnectionAlive(id uint64) bool {
	o.mu.RLock()
	state := o.connections[id]
	o.mu.RUnlock()
	if state == nil {
		return false
	}
	select {
	case <-state.conn.Context().Done():
		return false
	default:
		return true
	}
}

// CloseConnection terminates one named server-side connection.
func (o *Origin) CloseConnection(id uint64, code quic.ApplicationErrorCode) error {
	o.mu.RLock()
	state := o.connections[id]
	o.mu.RUnlock()
	if state == nil {
		return fmt.Errorf("unknown QUIC connection %d", id)
	}
	return state.conn.CloseWithError(code, "fault injection")
}

// Close stops accepting and closes every server-side connection. It is safe
// to call repeatedly.
func (o *Origin) Close(ctx context.Context) error {
	var closeErr error
	o.closeOnce.Do(func() {
		o.ReleaseHeld()
		serverErr := o.server.Close()
		listenerErr := o.listener.Close()
		select {
		case err := <-o.serveErr:
			if !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, quic.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
				serverErr = errors.Join(serverErr, err)
			}
		case <-ctx.Done():
			serverErr = errors.Join(serverErr, context.Cause(ctx))
		}
		closeErr = errors.Join(serverErr, listenerErr)
	})
	return closeErr
}

func (o *Origin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	state, _ := r.Context().Value(connectionContextKey{}).(*connectionState)
	if state == nil {
		http.Error(w, "missing QUIC connection context", http.StatusInternalServerError)
		return
	}
	w.Header().Set(ConnectionHeader, strconv.FormatUint(state.id, 10))
	switch r.URL.Path {
	case PathOK:
		w.WriteHeader(http.StatusNoContent)
	case PathRejectOnce:
		if o.rejectOnce.CompareAndSwap(false, true) {
			rejectRequest(w, http3.ErrCodeRequestRejected)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case PathRejectTwice:
		if o.rejectTwice.Add(1) <= 2 {
			rejectRequest(w, http3.ErrCodeRequestRejected)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case PathHoldReject:
		original := o.heldConnection.Load()
		if original == 0 && o.heldConnection.CompareAndSwap(0, state.id) {
			o.heldStarted <- state.id
			<-o.releaseHeld
			rejectRequest(w, http3.ErrCodeRequestRejected)
			return
		}
		if original == 0 {
			original = o.heldConnection.Load()
		}
		if original == state.id {
			<-o.releaseHeld
			rejectRequest(w, http3.ErrCodeRequestRejected)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case PathStreamReset:
		rejectRequest(w, http3.ErrCodeRequestCanceled)
	case PathWait:
		o.waitStarted <- state.id
		<-r.Context().Done()
	default:
		http.NotFound(w, r)
	}
}

func rejectRequest(w http.ResponseWriter, code http3.ErrCode) {
	streamer, ok := w.(http3.HTTPStreamer)
	if !ok {
		panic("HTTP/3 response writer does not implement HTTPStreamer")
	}
	stream := streamer.HTTPStream()
	// Queue the response reset before stopping request-body delivery. HTTPStream
	// flushes response headers when it takes ownership; resetting the write side
	// first prevents a scheduling race where the client can observe those
	// headers as a successful response and only discover the rejection while
	// reading the body.
	stream.CancelWrite(quic.StreamErrorCode(code))
	stream.CancelRead(quic.StreamErrorCode(code))
}

func testCertificate() (tls.Certificate, error) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate test key: %w", err)
	}
	now := time.Now()
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("create test certificate: %w", err)
	}
	return tls.Certificate{
		Certificate: [][]byte{certificateDER},
		PrivateKey:  privateKey,
		Leaf:        &template,
	}, nil
}
