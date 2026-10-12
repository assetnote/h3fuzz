package runner

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/assetnote/h3fuzz/harness/quicfault"
	"github.com/assetnote/h3fuzz/pkg/ffuf"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

func TestOwnedHTTP3TransportRetriesReplayableRejectedBody(t *testing.T) {
	origin, transport := newOwnedRecoveryTestTransport(t)
	request, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		origin.URL(quicfault.PathRejectOnce),
		bytes.NewReader([]byte("replay me")),
	)
	if err != nil {
		t.Fatal(err)
	}
	if request.GetBody == nil {
		t.Fatal("replayable request unexpectedly lacks GetBody")
	}
	connection, err := recoveryRoundTripConnection(transport, request)
	if err != nil {
		t.Fatalf("replayable rejected request: %v", err)
	}
	if connection != 2 || origin.ConnectionCount() != 2 {
		t.Fatalf("replayable retry used connection %d of %d, want B=2", connection, origin.ConnectionCount())
	}
}

func TestOwnedHTTP3TransportRetriesRejectedRequestAfterUnusableGeneration(t *testing.T) {
	origin, transport := newOwnedRecoveryTestTransport(t)
	first, err := recoveryRoundTripConnection(transport, recoveryRequest(t, origin.URL(quicfault.PathOK)))
	if err != nil {
		t.Fatalf("prime generation: %v", err)
	}
	generation := transport.current.Load()
	if err := origin.CloseConnection(first, 0xface); err != nil {
		t.Fatalf("terminate generation A: %v", err)
	}
	waitForRecoveryCondition(t, time.Second, "client generation A to terminate", func() bool {
		return generation.conn.Context().Err() != nil
	})
	request, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		origin.URL(quicfault.PathRejectOnce),
		bytes.NewReader([]byte("replay after reconnect")),
	)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := recoveryRoundTripConnection(transport, request)
	if err != nil {
		t.Fatalf("rejected request after unusable generation: %v", err)
	}
	if connection != 3 || origin.ConnectionCount() != 3 {
		t.Fatalf("combined recovery used connection %d of %d, want C=3", connection, origin.ConnectionCount())
	}
}

func TestOwnedHTTP3TransportHealthyHeadKeepsOneLifetimeWrapper(t *testing.T) {
	origin, transport := newOwnedRecoveryTestTransport(t)
	request, err := http.NewRequestWithContext(context.Background(), http.MethodHead, origin.URL(quicfault.PathOK), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatalf("healthy HEAD request: %v", err)
	}
	if _, ok := response.Body.(*ownedHTTP3ResponseBody); !ok {
		t.Fatalf("healthy HEAD response body = %T, want one owned lifetime wrapper", response.Body)
	}
	generation := transport.current.Load()
	if generation == nil || generation.inFlight.Load() != 1 {
		t.Fatalf("healthy HEAD generation after headers = %+v, want current with one live stream body", generation)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if inFlight := generation.inFlight.Load(); inFlight != 0 {
		t.Fatalf("healthy HEAD close left generation in-flight=%d, want 0", inFlight)
	}
}

func TestHTTP3PoolFusesGenerationAndCompletionBodyTracking(t *testing.T) {
	origin, err := quicfault.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := origin.Close(ctx); err != nil {
			t.Errorf("close body-fusion origin: %v", err)
		}
	})
	target, err := url.Parse(origin.URL("/"))
	if err != nil {
		t.Fatal(err)
	}
	probe := recoveryRequest(t, origin.URL(quicfault.PathOK))
	pool, err := newHTTP3Pool(&ffuf.Config{
		Http3Connections:    1,
		Http3MaxConnections: 1,
		Http3Streams:        8,
		Http3ProbeTimeout:   3,
		Threads:             1,
		ThreadsExplicit:     true,
	}, target, probe)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	if active, err := pool.start(context.Background()); err != nil || active != 1 {
		t.Fatalf("start one-slot body-fusion pool: active=%d error=%v", active, err)
	}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodHead, origin.URL(quicfault.PathOK), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := pool.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := response.Body.(*ownedHTTP3ResponseBody); !ok {
		t.Fatalf("pool response body = %T, want fused owned wrapper instead of trackedHTTP3Body", response.Body)
	}
	if got := pool.slots[0].inFlight.Load(); got != 1 {
		t.Fatalf("pool slot in-flight before body close = %d, want 1", got)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if got := pool.slots[0].inFlight.Load(); got != 0 {
		t.Fatalf("pool slot in-flight after body close = %d, want 0", got)
	}
	if got := pool.completed.Load(); got != 1 {
		t.Fatalf("fused body completion count = %d, want 1", got)
	}
}

func TestOwnedHTTP3TransportSurfacesNonReplayableRejectedBody(t *testing.T) {
	origin, transport := newOwnedRecoveryTestTransport(t)
	request, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		origin.URL(quicfault.PathRejectOnce),
		io.NopCloser(strings.NewReader("send once")),
	)
	if err != nil {
		t.Fatal(err)
	}
	if request.GetBody != nil {
		t.Fatal("non-replayable request unexpectedly has GetBody")
	}
	_, err = recoveryRoundTripConnection(transport, request)
	var h3Error *http3.Error
	if !errors.As(err, &h3Error) || h3Error.ErrorCode != http3.ErrCodeRequestRejected {
		t.Fatalf("non-replayable error = %v, want H3_REQUEST_REJECTED", err)
	}
	connection, err := recoveryRoundTripConnection(transport, recoveryRequest(t, origin.URL(quicfault.PathOK)))
	if err != nil {
		t.Fatalf("request after non-replayable rejection: %v", err)
	}
	if connection != 2 || origin.ConnectionCount() != 2 {
		t.Fatalf("post-rejection request used connection %d of %d, want B=2", connection, origin.ConnectionCount())
	}
}

func TestOwnedHTTP3TransportCallerCancellationKeepsHealthyGeneration(t *testing.T) {
	origin, transport := newOwnedRecoveryTestTransport(t)
	first, err := recoveryRoundTripConnection(transport, recoveryRequest(t, origin.URL(quicfault.PathOK)))
	if err != nil {
		t.Fatalf("prime generation: %v", err)
	}
	requestContext, cancelRequest := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, origin.URL(quicfault.PathWait), nil)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		response, requestErr := transport.RoundTrip(request)
		if response != nil {
			_ = response.Body.Close()
		}
		result <- requestErr
	}()
	waitContext, cancelWait := context.WithTimeout(context.Background(), time.Second)
	defer cancelWait()
	blockedOn, err := origin.WaitBlocked(waitContext)
	if err != nil {
		t.Fatalf("wait for cancellable request: %v", err)
	}
	if blockedOn != first {
		t.Fatalf("cancellable request used generation %d, want %d", blockedOn, first)
	}
	cancelRequest()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled request error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled request did not return")
	}
	second, err := recoveryRoundTripConnection(transport, recoveryRequest(t, origin.URL(quicfault.PathOK)))
	if err != nil {
		t.Fatalf("request after cancellation: %v", err)
	}
	if second != first || origin.ConnectionCount() != 1 {
		t.Fatalf("request after cancellation used generation %d of %d, want original %d", second, origin.ConnectionCount(), first)
	}
}

func TestOwnedHTTP3TransportCallerDeadlineKeepsHealthyGeneration(t *testing.T) {
	origin, transport := newOwnedRecoveryTestTransport(t)
	first, err := recoveryRoundTripConnection(transport, recoveryRequest(t, origin.URL(quicfault.PathOK)))
	if err != nil {
		t.Fatalf("prime generation: %v", err)
	}
	requestContext, cancelRequest := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancelRequest()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, origin.URL(quicfault.PathWait), nil)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		response, requestErr := transport.RoundTrip(request)
		if response != nil {
			_ = response.Body.Close()
		}
		result <- requestErr
	}()
	waitContext, cancelWait := context.WithTimeout(context.Background(), time.Second)
	defer cancelWait()
	blockedOn, err := origin.WaitBlocked(waitContext)
	if err != nil {
		t.Fatalf("wait for deadline request: %v", err)
	}
	if blockedOn != first {
		t.Fatalf("deadline request used generation %d, want %d", blockedOn, first)
	}
	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("deadline request error = %v, want context.DeadlineExceeded", err)
		}
	case <-time.After(time.Second):
		t.Fatal("deadline request did not return")
	}
	second, err := recoveryRoundTripConnection(transport, recoveryRequest(t, origin.URL(quicfault.PathOK)))
	if err != nil {
		t.Fatalf("request after deadline: %v", err)
	}
	if second != first || origin.ConnectionCount() != 1 {
		t.Fatalf("request after deadline used generation %d of %d, want original %d", second, origin.ConnectionCount(), first)
	}
}

func TestOwnedHTTP3TransportStreamResetReplacesGenerationWithoutRetiringSlot(t *testing.T) {
	origin, transport := newOwnedRecoveryTestTransport(t)
	first, err := recoveryRoundTripConnection(transport, recoveryRequest(t, origin.URL(quicfault.PathOK)))
	if err != nil {
		t.Fatalf("prime generation: %v", err)
	}
	_, err = recoveryRoundTripConnection(transport, recoveryRequest(t, origin.URL(quicfault.PathStreamReset)))
	var h3Error *http3.Error
	if !errors.As(err, &h3Error) || h3Error.ErrorCode != http3.ErrCodeRequestCanceled {
		t.Fatalf("stream-reset error = %v, want H3_REQUEST_CANCELLED", err)
	}
	second, err := recoveryRoundTripConnection(transport, recoveryRequest(t, origin.URL(quicfault.PathOK)))
	if err != nil {
		t.Fatalf("request after stream reset: %v", err)
	}
	if second != 2 || origin.ConnectionCount() != 2 {
		t.Fatalf("request after stream reset used generation %d of %d, want B=2", second, origin.ConnectionCount())
	}
	waitForRecoveryCondition(t, time.Second, "reset generation A to close", func() bool {
		return !origin.ConnectionAlive(first)
	})
}

func TestOwnedHTTP3TransportSecondAttemptErrorDoesNotChurnAgain(t *testing.T) {
	origin, transport := newOwnedRecoveryTestTransport(t)
	_, err := recoveryRoundTripConnection(transport, recoveryRequest(t, origin.URL(quicfault.PathRejectTwice)))
	var h3Error *http3.Error
	if !errors.As(err, &h3Error) || h3Error.ErrorCode != http3.ErrCodeRequestRejected {
		t.Fatalf("second-attempt error = %v, want H3_REQUEST_REJECTED", err)
	}
	if got := origin.ConnectionCount(); got != 2 {
		t.Fatalf("second-attempt failure created %d generations, want 2", got)
	}
	connection, err := recoveryRoundTripConnection(transport, recoveryRequest(t, origin.URL(quicfault.PathOK)))
	if err != nil {
		t.Fatalf("request after second-attempt error: %v", err)
	}
	if connection != 2 || origin.ConnectionCount() != 2 {
		t.Fatalf("post-error request used generation %d of %d, want retained B=2", connection, origin.ConnectionCount())
	}
}

func TestOwnedHTTP3TransportRetiredGenerationHasBoundedDrain(t *testing.T) {
	origin, transport := newOwnedRecoveryTestTransport(t)
	transport.retireTimeout = 25 * time.Millisecond
	first, err := recoveryRoundTripConnection(transport, recoveryRequest(t, origin.URL(quicfault.PathOK)))
	if err != nil {
		t.Fatalf("prime bounded-drain generation A: %v", err)
	}
	heldResult := make(chan recoveryRoundTripResult, 1)
	go func() {
		connection, requestErr := recoveryRoundTripConnection(transport, recoveryRequestNoTest(origin.URL(quicfault.PathHoldReject)))
		heldResult <- recoveryRoundTripResult{connection: connection, err: requestErr}
	}()
	waitContext, cancelWait := context.WithTimeout(context.Background(), time.Second)
	heldOn, err := origin.WaitHeld(waitContext)
	cancelWait()
	if err != nil {
		t.Fatalf("wait for bounded-drain request: %v", err)
	}
	if heldOn != first {
		t.Fatalf("bounded-drain request used generation %d, want A=%d", heldOn, first)
	}
	second, err := recoveryRoundTripConnection(transport, recoveryRequest(t, origin.URL(quicfault.PathRejectOnce)))
	if err != nil {
		t.Fatalf("create bounded-drain replacement: %v", err)
	}
	waitForRecoveryCondition(t, time.Second, "retired generation A bounded drain", func() bool {
		return !origin.ConnectionAlive(first)
	})
	if !origin.ConnectionAlive(second) {
		t.Fatalf("bounded drain of A also closed current generation B=%d", second)
	}
	origin.ReleaseHeld()
	select {
	case result := <-heldResult:
		// A forced bounded drain may surface the one stream that exceeded the
		// grace period. If quic-go classifies it as safely replayable, it must
		// complete on B; otherwise the surfaced error is the accounted outcome.
		if result.err == nil && result.connection != second {
			t.Fatalf("held old-generation request recovered on %d, want B=%d", result.connection, second)
		}
	case <-time.After(time.Second):
		t.Fatal("held old-generation request did not finish after bounded close")
	}
	if got := origin.ConnectionCount(); got != 2 {
		t.Fatalf("bounded drain created %d generations, want A plus B", got)
	}
}

func TestOwnedHTTP3TransportUnusableOrFatalGenerationCreatesOneReplacement(t *testing.T) {
	origin, transport := newOwnedRecoveryTestTransport(t)
	first, err := recoveryRoundTripConnection(transport, recoveryRequest(t, origin.URL(quicfault.PathOK)))
	if err != nil {
		t.Fatalf("prime generation: %v", err)
	}
	if err := origin.CloseConnection(first, 0xface); err != nil {
		t.Fatalf("terminate generation A: %v", err)
	}
	waitForRecoveryCondition(t, time.Second, "server generation A to terminate", func() bool {
		return !origin.ConnectionAlive(first)
	})
	connection, firstErr := recoveryRoundTripConnection(transport, recoveryRequest(t, origin.URL(quicfault.PathOK)))
	if firstErr != nil {
		connection, err = recoveryRoundTripConnection(transport, recoveryRequest(t, origin.URL(quicfault.PathOK)))
		if err != nil {
			t.Fatalf("later request after surfaced fatal error: %v", err)
		}
	}
	if connection != 2 || origin.ConnectionCount() != 2 {
		t.Fatalf("terminal A recovered on generation %d of %d, want one replacement B", connection, origin.ConnectionCount())
	}
}

func TestOwnedHTTP3TransportDialFailureSurfacesWithoutSameRequestRetry(t *testing.T) {
	origin, transport := newOwnedRecoveryTestTransport(t)
	originalDial := transport.dialEarly
	injected := errors.New("injected dial failure")
	var dialCalls atomic.Int64
	transport.dialEarly = func(ctx context.Context, quicTransport *quic.Transport, addr net.Addr, tlsConfig *tls.Config, config *quic.Config) (*quic.Conn, error) {
		if dialCalls.Add(1) == 1 {
			return nil, injected
		}
		return originalDial(ctx, quicTransport, addr, tlsConfig, config)
	}
	_, err := recoveryRoundTripConnection(transport, recoveryRequest(t, origin.URL(quicfault.PathOK)))
	if !errors.Is(err, injected) {
		t.Fatalf("first dial error = %v, want injected failure", err)
	}
	if dialCalls.Load() != 1 || origin.ConnectionCount() != 0 {
		t.Fatalf("failed logical request made %d dials and %d server connections, want 1 and 0", dialCalls.Load(), origin.ConnectionCount())
	}
	connection, err := recoveryRoundTripConnection(transport, recoveryRequest(t, origin.URL(quicfault.PathOK)))
	if err != nil {
		t.Fatalf("later request after dial failure: %v", err)
	}
	if connection != 1 || dialCalls.Load() != 2 || origin.ConnectionCount() != 1 {
		t.Fatalf("later request connection=%d dials=%d server_connections=%d, want 1,2,1", connection, dialCalls.Load(), origin.ConnectionCount())
	}
}

func TestOwnedHTTP3TransportsShareReplacementDialLimit(t *testing.T) {
	const attempts = 12
	const dialLimit = 3
	clock := &http3ReconnectFakeClock{current: time.Unix(1_700_000_000, 0)}
	gate := newHTTP3ReconnectGate(10, &http3ReconnectGateOptions{
		now:    clock.now,
		wait:   clock.waitRespectingWake,
		random: func() float64 { return 0.5 },
	})
	injected := errors.New("injected total-origin dial failure")
	entered := make(chan struct{}, attempts)
	release := make(chan struct{}, attempts)
	defer close(release)
	results := make(chan error, attempts)
	var active atomic.Int64
	var maximum atomic.Int64
	for range attempts {
		transport := newOwnedHTTP3Transport(
			"127.0.0.1:443",
			&tls.Config{InsecureSkipVerify: true}, // #nosec G402 -- no real handshake occurs.
			http3QUICConfig(time.Second),
			gate,
		)
		transport.everConnected.Store(true)
		transport.dialEarly = func(context.Context, *quic.Transport, net.Addr, *tls.Config, *quic.Config) (*quic.Conn, error) {
			current := active.Add(1)
			for observed := maximum.Load(); current > observed && !maximum.CompareAndSwap(observed, current); observed = maximum.Load() {
			}
			entered <- struct{}{}
			<-release
			active.Add(-1)
			return nil, injected
		}
		t.Cleanup(func() { _ = transport.Close() })
		request := recoveryRequest(t, "https://127.0.0.1:443/")
		go func() {
			_, err := transport.RoundTrip(request)
			results <- err
		}()
	}
	enteredCount := 0
	for range dialLimit {
		select {
		case <-entered:
			enteredCount++
		case <-time.After(time.Second):
			t.Fatal("three replacement dials did not reach the injected dial seam")
		}
	}
	select {
	case <-entered:
		t.Fatal("fourth replacement dial crossed the shared desired-10 limit of three")
	case <-time.After(20 * time.Millisecond):
	}
	for enteredCount < attempts {
		for range dialLimit {
			release <- struct{}{}
		}
		nextWave := minInt(dialLimit, attempts-enteredCount)
		for range nextWave {
			select {
			case <-entered:
				enteredCount++
			case <-time.After(time.Second):
				t.Fatalf("replacement wave stopped after %d of %d failed attempts", enteredCount, attempts)
			}
		}
	}
	for range dialLimit {
		release <- struct{}{}
	}
	for range attempts {
		select {
		case err := <-results:
			if !errors.Is(err, injected) {
				t.Fatalf("replacement result = %v, want injected origin failure", err)
			}
		case <-time.After(time.Second):
			t.Fatal("replacement dial did not return")
		}
	}
	if got := maximum.Load(); got != dialLimit {
		t.Fatalf("maximum concurrent replacement dials = %d, want exactly %d", got, dialLimit)
	}
	if snapshot := gate.snapshot(); snapshot.InFlight != 0 || snapshot.ConsecutiveFailures != attempts {
		t.Fatalf("shared replacement gate after outage = %+v, want 0 active and %d failures", snapshot, attempts)
	}
}

func TestOwnedHTTP3TransportCloseInterruptsReconnectWaitBeforeDialTrace(t *testing.T) {
	waitEntered := make(chan struct{}, 1)
	gate := newHTTP3ReconnectGate(1, &http3ReconnectGateOptions{
		wait: func(ctx context.Context, delay time.Duration, wake <-chan struct{}) error {
			select {
			case waitEntered <- struct{}{}:
			default:
			}
			return waitHTTP3Reconnect(ctx, delay, wake)
		},
	})
	occupied, err := gate.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.finish(true)
	transport := newOwnedHTTP3Transport(
		"127.0.0.1:443",
		&tls.Config{InsecureSkipVerify: true}, // #nosec G402 -- the request cannot reach a handshake.
		http3QUICConfig(time.Second),
		gate,
	)
	transport.everConnected.Store(true)
	var connectStarts atomic.Int64
	var handshakeStarts atomic.Int64
	trace := &httptrace.ClientTrace{
		ConnectStart: func(string, string) { connectStarts.Add(1) },
		TLSHandshakeStart: func() {
			handshakeStarts.Add(1)
		},
	}
	request := recoveryRequest(t, "https://127.0.0.1:443/")
	request = request.WithContext(httptrace.WithClientTrace(request.Context(), trace))
	requestResult := make(chan error, 1)
	go func() {
		_, requestErr := transport.RoundTrip(request)
		requestResult <- requestErr
	}()
	select {
	case <-waitEntered:
	case <-time.After(time.Second):
		t.Fatal("replacement request did not block on the reconnect gate")
	}
	closeResult := make(chan error, 1)
	go func() { closeResult <- transport.Close() }()
	select {
	case err := <-closeResult:
		if err != nil {
			t.Fatalf("close waiting replacement transport: %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("transport close did not interrupt reconnect-gate wait")
	}
	select {
	case err := <-requestResult:
		if !errors.Is(err, http3.ErrTransportClosed) {
			t.Fatalf("interrupted reconnect error = %v, want http3.ErrTransportClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("replacement request did not return after transport close")
	}
	if got := connectStarts.Load(); got != 0 {
		t.Fatalf("gate wait emitted %d connect-start callbacks, want 0", got)
	}
	if got := handshakeStarts.Load(); got != 0 {
		t.Fatalf("gate wait emitted %d TLS-handshake-start callbacks, want 0", got)
	}
}

func TestOwnedHTTP3TransportMeasuredRecoveryGate(t *testing.T) {
	if os.Getenv("H3FUZZ_RUN_LONG_RECOVERY") != "1" {
		t.Skip("set H3FUZZ_RUN_LONG_RECOVERY=1 to run the 30-second PERF-103 measurement gate")
	}
	const loadWorkers = 16
	const concurrentProbes = 8
	const baselineDuration = 5 * time.Second
	const postFaultDuration = 30 * time.Second
	const sampleInterval = 100 * time.Millisecond
	const probeTimeout = 3 * time.Second
	recoveryDeadline := maxDuration(10*time.Second, 3*probeTimeout)

	origin, transport := newOwnedRecoveryTestTransport(t)
	first, err := recoveryRoundTripConnection(transport, recoveryRequest(t, origin.URL(quicfault.PathOK)))
	if err != nil {
		t.Fatalf("prime measured generation A: %v", err)
	}

	loadContext, cancelLoad := context.WithCancel(context.Background())
	var completed atomic.Uint64
	var surfacedErrors atomic.Uint64
	var loadWG sync.WaitGroup
	for range loadWorkers {
		request, err := http.NewRequestWithContext(loadContext, http.MethodHead, origin.URL(quicfault.PathOK), nil)
		if err != nil {
			t.Fatal(err)
		}
		loadWG.Add(1)
		go func() {
			defer loadWG.Done()
			for {
				_, requestErr := recoveryRoundTripConnection(transport, request)
				if requestErr == nil {
					completed.Add(1)
					continue
				}
				if loadContext.Err() != nil {
					return
				}
				surfacedErrors.Add(1)
			}
		}()
	}
	loadJoined := false
	defer func() {
		cancelLoad()
		if !loadJoined {
			loadWG.Wait()
		}
	}()
	time.Sleep(time.Second)
	baselineStarted := time.Now()
	baselineCompleted := completed.Load()
	time.Sleep(baselineDuration)
	baselineElapsed := time.Since(baselineStarted)
	baselineResponses := completed.Load() - baselineCompleted
	baselineRate := float64(baselineResponses) / baselineElapsed.Seconds()
	if baselineResponses == 0 {
		t.Fatal("measured recovery baseline completed no responses")
	}

	heldResult := make(chan recoveryRoundTripResult, 1)
	go func() {
		connection, requestErr := recoveryRoundTripConnection(transport, recoveryRequestNoTest(origin.URL(quicfault.PathHoldReject)))
		heldResult <- recoveryRoundTripResult{connection: connection, err: requestErr}
	}()
	waitContext, cancelWait := context.WithTimeout(context.Background(), time.Second)
	heldOn, err := origin.WaitHeld(waitContext)
	cancelWait()
	if err != nil {
		t.Fatalf("wait for measured held generation: %v", err)
	}
	if heldOn != first {
		t.Fatalf("measured held request used generation %d, want A=%d", heldOn, first)
	}

	faultAt := time.Now()
	faultCompleted := completed.Load()
	faultErrors := surfacedErrors.Load()
	second, err := recoveryRoundTripConnection(transport, recoveryRequest(t, origin.URL(quicfault.PathRejectOnce)))
	if err != nil {
		t.Fatalf("measured replacement handshake: %v", err)
	}
	if second == first {
		t.Fatalf("fault stayed on generation %d instead of handshaking a replacement", second)
	}

	probeResults := make(chan recoveryRoundTripResult, concurrentProbes)
	for range concurrentProbes {
		request := recoveryRequest(t, origin.URL(quicfault.PathOK))
		go func() {
			connection, requestErr := recoveryRoundTripConnection(transport, request)
			probeResults <- recoveryRoundTripResult{connection: connection, err: requestErr}
		}()
	}
	for range concurrentProbes {
		select {
		case result := <-probeResults:
			if result.err != nil || result.connection != second {
				t.Fatalf("concurrent recovery probe connection=%d error=%v, want generation B=%d", result.connection, result.err, second)
			}
		case <-time.After(recoveryDeadline):
			t.Fatal("concurrent recovery probe missed the recovery deadline")
		}
	}
	origin.ReleaseHeld()
	select {
	case result := <-heldResult:
		if result.err != nil || result.connection != second {
			t.Fatalf("late generation A request connection=%d error=%v, want B=%d", result.connection, result.err, second)
		}
	case <-time.After(recoveryDeadline):
		t.Fatal("late generation A request missed the recovery deadline")
	}

	type rateSample struct {
		at        time.Time
		completed uint64
	}
	samples := []rateSample{{at: faultAt, completed: faultCompleted}}
	ticker := time.NewTicker(sampleInterval)
	defer ticker.Stop()
	deadlineAt := faultAt.Add(recoveryDeadline)
	var recoveredAt time.Time
	var recoveryRate float64
	for recoveredAt.IsZero() && time.Now().Before(deadlineAt) {
		<-ticker.C
		now := time.Now()
		samples = append(samples, rateSample{at: now, completed: completed.Load()})
		windowStart := -1
		for index := len(samples) - 2; index >= 0; index-- {
			if now.Sub(samples[index].at) >= time.Second {
				windowStart = index
				break
			}
		}
		if windowStart < 0 {
			continue
		}
		elapsed := now.Sub(samples[windowStart].at)
		recoveryRate = float64(samples[len(samples)-1].completed-samples[windowStart].completed) / elapsed.Seconds()
		if recoveryRate >= 0.9*baselineRate {
			recoveredAt = now
		}
	}
	if recoveredAt.IsZero() {
		t.Fatalf("usable capacity did not recover to 90%% of %.1f RPS within %s; latest one-second rate %.1f RPS", baselineRate, recoveryDeadline, recoveryRate)
	}

	if remaining := time.Until(faultAt.Add(postFaultDuration)); remaining > 0 {
		timer := time.NewTimer(remaining)
		<-timer.C
	}
	postFaultCompleted := completed.Load() - faultCompleted
	postFaultErrors := surfacedErrors.Load() - faultErrors
	postFaultOutcomes := postFaultCompleted + postFaultErrors
	errorRate := 0.0
	if postFaultOutcomes > 0 {
		errorRate = float64(postFaultErrors) / float64(postFaultOutcomes)
	}
	if errorRate >= 0.001 {
		t.Fatalf("30-second surfaced error rate = %.6f (%d/%d), want <0.001", errorRate, postFaultErrors, postFaultOutcomes)
	}
	if got := origin.ConnectionCount(); got != 2 {
		t.Fatalf("measured fault created %d generations, want A plus one B", got)
	}

	cancelLoad()
	loadWG.Wait()
	loadJoined = true
	if err := transport.Close(); err != nil {
		t.Fatalf("close measured owned transport: %v", err)
	}
	waitForRecoveryCondition(t, time.Second, "measured generations to drain", func() bool {
		return !origin.ConnectionAlive(first) && !origin.ConnectionAlive(second)
	})
	t.Logf(
		"PERF-103 measured gate: baseline=%.1f_rps baseline_responses=%d recovery=%.3fs recovery_rate=%.1f_rps deadline=%s post_fault_completed=%d surfaced_errors=%d error_rate=%.6f generations=%d",
		baselineRate,
		baselineResponses,
		recoveredAt.Sub(faultAt).Seconds(),
		recoveryRate,
		recoveryDeadline,
		postFaultCompleted,
		postFaultErrors,
		errorRate,
		origin.ConnectionCount(),
	)
}

func TestHTTP3SlotLateOldGenerationErrorKeepsReplacementAndDrains(t *testing.T) {
	origin, err := quicfault.Start()
	if err != nil {
		t.Fatalf("start fault origin: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := origin.Close(ctx); err != nil {
			t.Errorf("close fault origin: %v", err)
		}
	})

	target, err := url.Parse(origin.URL("/"))
	if err != nil {
		t.Fatal(err)
	}
	probe, err := http.NewRequest(http.MethodHead, origin.URL(quicfault.PathOK), nil)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := newHTTP3Pool(&ffuf.Config{
		RunTelemetry:      "recovery-test.json",
		QuicTelemetry:     true,
		Http3ProbeTimeout: 3,
	}, target, probe)
	if err != nil {
		t.Fatalf("new HTTP/3 pool: %v", err)
	}
	slotDiagnostics := newQUICSlotDiagnostics(0)
	transport := pool.newSlotTransport(slotDiagnostics)
	t.Cleanup(func() { _ = transport.Close() })

	first, err := recoveryRoundTripConnection(transport, recoveryRequest(t, origin.URL(quicfault.PathOK)))
	if err != nil {
		t.Fatalf("prime generation A: %v", err)
	}
	heldRequest := recoveryRequest(t, origin.URL(quicfault.PathHoldReject))
	heldResult := make(chan recoveryRoundTripResult, 1)
	go func() {
		id, requestErr := recoveryRoundTripConnection(transport, heldRequest)
		heldResult <- recoveryRoundTripResult{connection: id, err: requestErr}
	}()
	waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
	defer waitCancel()
	heldOn, err := origin.WaitHeld(waitCtx)
	if err != nil {
		t.Fatalf("wait for held generation A request: %v", err)
	}
	if heldOn != first {
		t.Fatalf("held request used generation %d, want A=%d", heldOn, first)
	}

	second, err := recoveryRoundTripConnection(transport, recoveryRequest(t, origin.URL(quicfault.PathRejectOnce)))
	if err != nil {
		t.Fatalf("initial request rejection did not recover: %v", err)
	}
	if second != 2 {
		t.Fatalf("initial rejection recovered on generation %d, want B=2", second)
	}
	origin.ReleaseHeld()
	var late recoveryRoundTripResult
	select {
	case late = <-heldResult:
	case <-time.After(2 * time.Second):
		t.Fatal("late generation A request did not finish")
	}
	if late.err != nil {
		t.Fatalf("late generation A rejection did not recover: %v", late.err)
	}
	if late.connection != second {
		t.Fatalf("late A rejection used generation %d, want healthy B=%d", late.connection, second)
	}
	if got := origin.ConnectionCount(); got != 2 {
		t.Fatalf("connection generations = %d, want A plus one replacement B", got)
	}

	if err := transport.Close(); err != nil {
		t.Fatalf("close owned transport: %v", err)
	}
	waitForRecoveryCondition(t, time.Second, "all server-side generations to close", func() bool {
		return !origin.ConnectionAlive(1) && !origin.ConnectionAlive(2)
	})
	if err := pool.diagnostics.waitForDrain(time.Second); err != nil {
		t.Fatalf("diagnostic generation drain: %v", err)
	}
	snapshot := pool.diagnostics.snapshot([]*quicSlotDiagnostics{slotDiagnostics})
	if snapshot.trackedGenerations != 0 || snapshot.quic.GenerationsStarted != 2 {
		t.Fatalf("generation telemetry after drain = %+v, tracked=%d", snapshot.quic, snapshot.trackedGenerations)
	}
}

type recoveryRoundTripper interface {
	RoundTrip(*http.Request) (*http.Response, error)
}

type recoveryRoundTripResult struct {
	connection uint64
	err        error
}

func recoveryRequest(t *testing.T, target string) *http.Request {
	t.Helper()
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func recoveryRequestNoTest(target string) *http.Request {
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
	if err != nil {
		panic(err)
	}
	return request
}

func recoveryRoundTripConnection(transport recoveryRoundTripper, request *http.Request) (uint64, error) {
	response, err := transport.RoundTrip(request)
	if err != nil {
		return 0, err
	}
	_, readErr := io.Copy(io.Discard, response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		return 0, errors.Join(readErr, closeErr)
	}
	return strconv.ParseUint(response.Header.Get(quicfault.ConnectionHeader), 10, 64)
}

func waitForRecoveryCondition(t *testing.T, timeout time.Duration, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", description)
		}
		time.Sleep(time.Millisecond)
	}
}

func newOwnedRecoveryTestTransport(t *testing.T) (*quicfault.Origin, *ownedHTTP3Transport) {
	t.Helper()
	origin, err := quicfault.Start()
	if err != nil {
		t.Fatalf("start fault origin: %v", err)
	}
	target, err := url.Parse(origin.URL("/"))
	if err != nil {
		t.Fatal(err)
	}
	tlsConfig, err := http3TLSConfig(&ffuf.Config{}, target)
	if err != nil {
		t.Fatal(err)
	}
	transport := newOwnedHTTP3Transport(target.Host, tlsConfig, http3QUICConfig(3*time.Second), newHTTP3ReconnectGate(1, nil))
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
