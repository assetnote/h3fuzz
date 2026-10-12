package runner

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptrace"
	"testing"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/quic-go/qlog"
)

type attemptRecorder struct {
	attempts []ffuf.RunTransportAttempt
	begins   int
	ends     int
	current  int
	peak     int
}

func (r *attemptRecorder) RecordRunTransportAttempt(attempt ffuf.RunTransportAttempt) {
	r.attempts = append(r.attempts, attempt)
}

func (r *attemptRecorder) BeginRunTransportAttempt() {
	r.begins++
	r.current++
	if r.current > r.peak {
		r.peak = r.current
	}
}

func (r *attemptRecorder) EndRunTransportAttempt() {
	r.ends++
	r.current--
}

func TestSimpleRunnerRunTelemetrySnapshotsHTTP3AndStrategy(t *testing.T) {
	conf := &ffuf.Config{
		Method:                       http.MethodGet,
		Headers:                      map[string]string{"Range": "bytes=-1"},
		NoResponseOptimization:       true,
		Http3Selected:                true,
		Http3ConnectionsActive:       2,
		Http3Reason:                  "negotiated h3 over v1",
		ResponseOptimizationStrategy: http.MethodGet,
		ResponseOptimizationReason:   "disabled by configuration",
	}
	slots := []*http3PoolSlot{{}, {}}
	pool := &http3Pool{
		slots:   slots,
		active:  2,
		limiter: newResizableLimiter(8),
		closed:  make(chan struct{}),
	}
	pool.published.Store(&http3ActiveSlots{slots: slots})
	pool.completed.Store(25)
	close(pool.closed)

	runner := &SimpleRunner{config: conf, h3Pool: pool}
	runner.closed.Store(true)
	report := runner.RunTelemetry()
	if report.SchemaVersion != 2 {
		t.Fatalf("schema version = %d, want 2", report.SchemaVersion)
	}
	if report.Protocol.Selected != "http3" || !report.Protocol.HTTP3Selected {
		t.Fatalf("protocol telemetry = %+v", report.Protocol)
	}
	if report.Protocol.HTTP3InitialConnections != 2 || report.Protocol.HTTP3FinalConnections != 2 || report.Protocol.HTTP3OpenedConnections != 2 {
		t.Fatalf("connection telemetry = %+v", report.Protocol)
	}
	if report.RequestStrategy.SelectedOptimization != "RANGE" || report.RequestStrategy.Range != "bytes=-1" || report.RequestStrategy.AutomaticOptimizationEnabled {
		t.Fatalf("strategy telemetry = %+v", report.RequestStrategy)
	}
	if report.Transport.CompletedResponses != 25 || report.Transport.Errors != 0 || report.Transport.InFlight != 0 {
		t.Fatalf("transport telemetry = %+v", report.Transport)
	}
	if !report.Drain.RunnerClosed || report.Drain.LimiterInUse != 0 || report.Drain.LimiterWaiters != 0 {
		t.Fatalf("drain telemetry = %+v", report.Drain)
	}
}

func TestSimpleRunnerRunTelemetryIncludesHighRatePolicyProvenance(t *testing.T) {
	conf := ffuf.NewConfig(context.Background(), func() {})
	conf.HighRatePolicy = &ffuf.HighRatePolicyReport{
		Enabled: true, Acknowledged: true, TargetRPS: 100_000,
		TargetRPSSource: "default", LatencyMS: 2.5, LatencySource: "measured_preflight",
		DesiredWorkers: 313, EffectiveWorkers: 313, ExecutionWorkers: 512, Provisioned: true,
	}
	runner := NewSimpleRunner(&conf, false).(*SimpleRunner)
	report := runner.RunTelemetry()
	if report.HighRatePolicy == nil || report.HighRatePolicy.LatencySource != "measured_preflight" ||
		report.HighRatePolicy.DesiredWorkers != 313 || !report.HighRatePolicy.Provisioned {
		t.Fatalf("high-rate telemetry = %+v", report.HighRatePolicy)
	}
	report.HighRatePolicy.DesiredWorkers = 1
	if conf.HighRatePolicy.DesiredWorkers != 313 {
		t.Fatal("telemetry snapshot aliases mutable high-rate policy state")
	}
}

func TestSimpleRunnerRunTelemetryMergesQUICLifecycleAndAttemptAttribution(t *testing.T) {
	diagnostics, err := newQUICDiagnostics(false, "")
	if err != nil {
		t.Fatal(err)
	}
	slotDiagnostics := newQUICSlotDiagnostics(7)
	recorder := diagnostics.tracerForSlot(slotDiagnostics, nil)(
		context.Background(), true, quic.ConnectionIDFromBytes([]byte{1}),
	).AddProducer()
	recorder.RecordEvent(qlog.StartedConnection{})

	observation, trace := diagnostics.newAttemptTrace(slotDiagnostics)
	trace.GetConn("example.test:443")
	trace.GotConn(httptrace.GotConnInfo{})
	trace.WroteRequest(httptrace.WroteRequestInfo{})
	observation.finish(&http3.Error{ErrorCode: http3.ErrCodeRequestRejected})

	pool := &http3Pool{
		diagnostics: diagnostics,
		slots:       []*http3PoolSlot{{diagnostics: slotDiagnostics}},
		closed:      make(chan struct{}),
	}
	pool.published.Store(&http3ActiveSlots{slots: pool.slots})
	runner := &SimpleRunner{config: &ffuf.Config{Http3Selected: true}, h3Pool: pool}
	report := runner.RunTelemetry()
	if !report.QUIC.LifecycleEnabled || report.QUIC.AggregateEnabled ||
		report.QUIC.GenerationsStarted != 1 || report.QUIC.ConnectionsOpened != 1 {
		t.Fatalf("QUIC telemetry = %+v", report.QUIC)
	}
	if report.Transport.InternalAttempts == nil || *report.Transport.InternalAttempts != 1 ||
		report.Transport.InternalRetries == nil || *report.Transport.InternalRetries != 0 ||
		report.Transport.ObservedPreHandlerApplicationFailures != 1 {
		t.Fatalf("transport attribution = %+v", report.Transport)
	}
	if report.Drain.TrackedConnectionGenerations != 1 {
		t.Fatalf("drain telemetry = %+v", report.Drain)
	}

	recorder.RecordEvent(qlog.ConnectionClosed{})
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	if got := runner.RunTelemetry().Drain.TrackedConnectionGenerations; got != 0 {
		t.Fatalf("tracked generations after close = %d, want 0", got)
	}
}

func TestSimpleRunnerRunTelemetryMarksInternalQUICSignalsUnavailable(t *testing.T) {
	runner := &SimpleRunner{config: &ffuf.Config{}}
	report := runner.RunTelemetry()
	if report.Transport.InternalAttempts != nil || report.Transport.InternalRetries != nil {
		t.Fatalf("unobserved internal metrics = %+v", report.Transport)
	}
	if report.Transport.InternalAttemptsUnavailableReason != "http3_not_selected" ||
		report.Transport.InternalRetriesUnavailableReason != "http3_not_selected" {
		t.Fatalf("internal metric reasons = %+v", report.Transport)
	}
	if report.QUIC.Unavailable["packets_sent"] != "http3_not_selected" ||
		report.QUIC.Unavailable["latest_rtt_ns"] != "http3_not_selected" {
		t.Fatalf("QUIC unavailable reasons = %+v", report.QUIC.Unavailable)
	}
}

func TestExecuteWithRunTelemetryClassifiesOptimizationFallbackByOuterAttempt(t *testing.T) {
	runner, _, req := optimizedExecuteRunner(t, responseOptimizationHEAD)
	runner.client.Transport = testRoundTripFunc(func(httpReq *http.Request) (*http.Response, error) {
		if httpReq.Method == http.MethodHead {
			return nil, errors.New("optimized attempt failed")
		}
		return optimizedHTTPResponse(httpReq, http.StatusNoContent, http.Header{}, http.NoBody, 0), nil
	})

	initial := &attemptRecorder{}
	if _, err := runner.ExecuteWithRunTelemetry(&req, ffuf.RunTransportRoleInitial, initial); err != nil {
		t.Fatalf("initial ExecuteWithRunTelemetry: %v", err)
	}
	assertAttemptPair(t, initial.attempts, ffuf.RunTransportRoleInitial, ffuf.RunTransportRoleOptimizationFallback)
	if initial.begins != 2 || initial.ends != 2 || initial.peak != 1 || initial.current != 0 {
		t.Fatalf("initial transport lifecycle = begins %d ends %d peak %d current %d", initial.begins, initial.ends, initial.peak, initial.current)
	}

	retry := &attemptRecorder{}
	if _, err := runner.ExecuteWithRunTelemetry(&req, ffuf.RunTransportRoleEngineRetry, retry); err != nil {
		t.Fatalf("retry ExecuteWithRunTelemetry: %v", err)
	}
	assertAttemptPair(t, retry.attempts, ffuf.RunTransportRoleEngineRetry, ffuf.RunTransportRoleEngineRetryOptimizationFallback)
	if retry.begins != 2 || retry.ends != 2 || retry.peak != 1 || retry.current != 0 {
		t.Fatalf("retry transport lifecycle = begins %d ends %d peak %d current %d", retry.begins, retry.ends, retry.peak, retry.current)
	}
}

func assertAttemptPair(t *testing.T, attempts []ffuf.RunTransportAttempt, firstRole, fallbackRole ffuf.RunTransportAttemptRole) {
	t.Helper()
	if len(attempts) != 2 {
		t.Fatalf("attempt count = %d, want 2: %+v", len(attempts), attempts)
	}
	if attempts[0].Role != firstRole || attempts[0].Method != http.MethodHead || !attempts[0].Optimized || !attempts[0].SurfacedError {
		t.Fatalf("first attempt = %+v", attempts[0])
	}
	if attempts[1].Role != fallbackRole || attempts[1].Method != http.MethodGet || attempts[1].Optimized || !attempts[1].Completed || attempts[1].SurfacedError {
		t.Fatalf("fallback attempt = %+v", attempts[1])
	}
}
