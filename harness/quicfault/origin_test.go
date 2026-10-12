package quicfault

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

func TestRequestRejectedRetryContract(t *testing.T) {
	t.Run("bodyless", func(t *testing.T) {
		origin, transport := newTestOriginAndTransport(t)
		id, err := roundTripConnection(transport, newRequest(t, http.MethodGet, origin.URL(PathRejectOnce), nil))
		if err != nil {
			t.Fatalf("bodyless rejected request: %v", err)
		}
		if id != 2 || origin.ConnectionCount() != 2 {
			t.Fatalf("bodyless retry used connection %d with %d total, want connection 2 of 2", id, origin.ConnectionCount())
		}
	})

	t.Run("replayable body", func(t *testing.T) {
		origin, transport := newTestOriginAndTransport(t)
		request := newRequest(t, http.MethodPost, origin.URL(PathRejectOnce), bytes.NewReader([]byte("replay me")))
		if request.GetBody == nil {
			t.Fatal("bytes.Reader request unexpectedly lacks GetBody")
		}
		id, err := roundTripConnection(transport, request)
		if err != nil {
			t.Fatalf("replayable rejected request: %v", err)
		}
		if id != 2 || origin.ConnectionCount() != 2 {
			t.Fatalf("replayable retry used connection %d with %d total, want connection 2 of 2", id, origin.ConnectionCount())
		}
	})

	t.Run("non-replayable body", func(t *testing.T) {
		origin, transport := newTestOriginAndTransport(t)
		request := newRequest(t, http.MethodPost, origin.URL(PathRejectOnce), io.NopCloser(strings.NewReader("send once")))
		if request.GetBody != nil {
			t.Fatal("non-replayable request unexpectedly has GetBody")
		}
		_, err := roundTripConnection(transport, request)
		var h3Error *http3.Error
		if !errors.As(err, &h3Error) || h3Error.ErrorCode != http3.ErrCodeRequestRejected {
			t.Fatalf("non-replayable error = %v, want H3_REQUEST_REJECTED", err)
		}
		if origin.ConnectionCount() != 1 {
			t.Fatalf("non-replayable request dialed %d connections, want 1", origin.ConnectionCount())
		}
		id, err := roundTripConnection(transport, newRequest(t, http.MethodGet, origin.URL(PathOK), nil))
		if err != nil {
			t.Fatalf("request after non-replayable rejection: %v", err)
		}
		if id != 2 {
			t.Fatalf("request after non-replayable rejection used connection %d, want replacement 2", id)
		}
	})
}

func TestSecondAttemptErrorLeavesSecondGenerationCached(t *testing.T) {
	origin, transport := newTestOriginAndTransport(t)
	_, err := roundTripConnection(transport, newRequest(t, http.MethodGet, origin.URL(PathRejectTwice), nil))
	var h3Error *http3.Error
	if !errors.As(err, &h3Error) || h3Error.ErrorCode != http3.ErrCodeRequestRejected {
		t.Fatalf("second-attempt error = %v, want H3_REQUEST_REJECTED", err)
	}
	if origin.ConnectionCount() != 2 {
		t.Fatalf("second-attempt failure dialed %d connections, want 2", origin.ConnectionCount())
	}
	id, err := roundTripConnection(transport, newRequest(t, http.MethodGet, origin.URL(PathOK), nil))
	if err != nil {
		t.Fatalf("request after second-attempt error: %v", err)
	}
	if id != 2 || origin.ConnectionCount() != 2 {
		t.Fatalf("post-error request used connection %d of %d, want cached generation 2", id, origin.ConnectionCount())
	}
}

func TestTerminalCachedConnectionCreatesAtMostOneReplacement(t *testing.T) {
	origin, transport := newTestOriginAndTransport(t)
	first, err := roundTripConnection(transport, newRequest(t, http.MethodGet, origin.URL(PathOK), nil))
	if err != nil {
		t.Fatalf("prime connection: %v", err)
	}
	if err := origin.CloseConnection(first, 0xface); err != nil {
		t.Fatalf("close cached connection: %v", err)
	}
	waitFor(t, time.Second, func() bool { return !origin.ConnectionAlive(first) }, "cached connection to close")

	second, firstPostCloseErr := roundTripConnection(transport, newRequest(t, http.MethodGet, origin.URL(PathOK), nil))
	if firstPostCloseErr != nil {
		// If the peer close reaches ClientConn after a request stream was
		// opened, that application-visible error is returned and removes the
		// cache entry. A later request performs the redial. If it reaches the
		// connection first, quic-go wraps the open failure as errConnUnusable
		// and transparently performs this same one replacement.
		second, err = roundTripConnection(transport, newRequest(t, http.MethodGet, origin.URL(PathOK), nil))
		if err != nil {
			t.Fatalf("later request after connection-fatal error: %v", err)
		}
	}
	if second != 2 || origin.ConnectionCount() != 2 {
		t.Fatalf("terminal cached connection recovered on %d of %d, want one replacement", second, origin.ConnectionCount())
	}
}

func TestCallerCancellationDoesNotReplaceHealthyConnection(t *testing.T) {
	origin, transport := newTestOriginAndTransport(t)
	first, err := roundTripConnection(transport, newRequest(t, http.MethodGet, origin.URL(PathOK), nil))
	if err != nil {
		t.Fatalf("prime connection: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	request := newRequest(t, http.MethodGet, origin.URL(PathWait), nil).WithContext(ctx)
	result := make(chan error, 1)
	go func() {
		_, err := roundTripConnection(transport, request)
		result <- err
	}()
	waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
	defer waitCancel()
	blockedOn, err := origin.WaitBlocked(waitCtx)
	if err != nil {
		t.Fatalf("wait for blocked request: %v", err)
	}
	if blockedOn != first {
		t.Fatalf("blocked request used connection %d, want %d", blockedOn, first)
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled request error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled request did not return")
	}
	second, err := roundTripConnection(transport, newRequest(t, http.MethodGet, origin.URL(PathOK), nil))
	if err != nil {
		t.Fatalf("request after caller cancellation: %v", err)
	}
	if second != first || origin.ConnectionCount() != 1 {
		t.Fatalf("request after cancellation used connection %d with %d total, want original %d", second, origin.ConnectionCount(), first)
	}
}

func TestLateOldGenerationErrorEvictsReplacementAndLeaksHistoricalConnections(t *testing.T) {
	origin, transport := newTestOriginAndTransport(t)
	first, err := roundTripConnection(transport, newRequest(t, http.MethodGet, origin.URL(PathOK), nil))
	if err != nil {
		t.Fatalf("prime generation A: %v", err)
	}

	heldResult := make(chan struct {
		connection uint64
		err        error
	}, 1)
	go func() {
		id, requestErr := roundTripConnection(transport, newRequest(t, http.MethodGet, origin.URL(PathHoldReject), nil))
		heldResult <- struct {
			connection uint64
			err        error
		}{connection: id, err: requestErr}
	}()
	waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
	defer waitCancel()
	heldOn, err := origin.WaitHeld(waitCtx)
	if err != nil {
		t.Fatalf("wait for old-generation request: %v", err)
	}
	if heldOn != first {
		t.Fatalf("held request used generation %d, want A=%d", heldOn, first)
	}

	second, err := roundTripConnection(transport, newRequest(t, http.MethodGet, origin.URL(PathRejectOnce), nil))
	if err != nil {
		t.Fatalf("initial rejected request did not recover: %v", err)
	}
	if second != 2 {
		t.Fatalf("initial rejected request recovered on generation %d, want B=2", second)
	}
	origin.ReleaseHeld()
	var late struct {
		connection uint64
		err        error
	}
	select {
	case late = <-heldResult:
	case <-time.After(2 * time.Second):
		t.Fatal("late old-generation request did not finish")
	}
	if late.err != nil {
		t.Fatalf("late old-generation rejection did not recover: %v", late.err)
	}
	if late.connection != 3 || origin.ConnectionCount() != 3 {
		t.Fatalf("late rejection recovered on generation %d with %d total, want C=3 (evidence that A evicted B)", late.connection, origin.ConnectionCount())
	}

	if err := transport.Close(); err != nil {
		t.Fatalf("close transport: %v", err)
	}
	waitFor(t, time.Second, func() bool { return !origin.ConnectionAlive(3) }, "current generation C to close")
	if !origin.ConnectionAlive(1) || !origin.ConnectionAlive(2) {
		t.Fatalf("historical connection liveness after Transport.Close: A=%t B=%t, want both alive to retain the stock v0.63 orphan evidence", origin.ConnectionAlive(1), origin.ConnectionAlive(2))
	}
	t.Logf("retained stock quic-go v0.63 evidence: generations=%d, current_closed=true, orphan_A=%t, orphan_B=%t", origin.ConnectionCount(), origin.ConnectionAlive(1), origin.ConnectionAlive(2))
}

func newTestOriginAndTransport(t *testing.T) (*Origin, *http3.Transport) {
	t.Helper()
	origin, err := Start()
	if err != nil {
		t.Fatalf("start fault origin: %v", err)
	}
	transport := &http3.Transport{
		TLSClientConfig: origin.ClientTLSConfig(),
		QUICConfig: &quic.Config{
			MaxIdleTimeout:  30 * time.Second,
			KeepAlivePeriod: time.Second,
		},
	}
	t.Cleanup(func() {
		_ = transport.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := origin.Close(ctx); err != nil {
			t.Errorf("close fault origin: %v", err)
		}
	})
	return origin, transport
}

func newRequest(t *testing.T, method, target string, body io.Reader) *http.Request {
	t.Helper()
	request, err := http.NewRequestWithContext(context.Background(), method, target, body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	return request
}

func roundTripConnection(transport *http3.Transport, request *http.Request) (uint64, error) {
	response, err := transport.RoundTrip(request)
	if err != nil {
		return 0, err
	}
	_, readErr := io.Copy(io.Discard, response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		return 0, errors.Join(readErr, closeErr)
	}
	id, parseErr := strconv.ParseUint(response.Header.Get(ConnectionHeader), 10, 64)
	if parseErr != nil {
		return 0, parseErr
	}
	return id, nil
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool, description string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", description)
		}
		time.Sleep(time.Millisecond)
	}
}
