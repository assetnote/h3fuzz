package runner

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http/httptrace"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/quic-go/qlog"
	"github.com/quic-go/quic-go/qlogwriter"
)

func TestQUICDiagnosticsSyntheticEventsAreExactAndBounded(t *testing.T) {
	diagnostics, err := newQUICDiagnostics(true, "")
	if err != nil {
		t.Fatal(err)
	}
	slot := newQUICSlotDiagnostics(3)
	trace := diagnostics.tracerForSlot(slot, nil)(context.Background(), true, quic.ConnectionIDFromBytes([]byte{1}))
	if trace == nil {
		t.Fatal("enabled diagnostics returned a nil trace")
	}
	recorder := trace.AddProducer()
	recorder.RecordEvent(qlog.StartedConnection{})

	clientTrace := diagnostics.lifecycleClientTrace(slot)
	clientTrace.ConnectStart("udp", "127.0.0.1:443")
	clientTrace.TLSHandshakeDone(tls.ConnectionState{}, nil)
	clientTrace.ConnectDone("udp", "127.0.0.1:443", nil)

	const goroutines = 16
	const eventsPerGoroutine = 200
	var wg sync.WaitGroup
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range eventsPerGoroutine {
				recorder.RecordEvent(qlog.PacketSent{})
				recorder.RecordEvent(qlog.PacketLost{})
				recorder.RecordEvent(qlog.PTOCountUpdated{PTOCount: 1})
			}
		}()
	}
	wg.Wait()
	recorder.RecordEvent(qlog.MetricsUpdated{
		LatestRTT:        7 * time.Millisecond,
		SmoothedRTT:      5 * time.Millisecond,
		MinRTT:           3 * time.Millisecond,
		CongestionWindow: 64_000,
		BytesInFlight:    12_000,
	})
	recorder.RecordEvent(qlog.ConnectionClosed{})
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}

	snapshot := diagnostics.snapshot([]*quicSlotDiagnostics{slot})
	wantEvents := uint64(goroutines * eventsPerGoroutine)
	if snapshot.quic.PacketsSent == nil || *snapshot.quic.PacketsSent != wantEvents ||
		snapshot.quic.PacketsLost == nil || *snapshot.quic.PacketsLost != wantEvents ||
		snapshot.quic.PTOCountChanges == nil || *snapshot.quic.PTOCountChanges != wantEvents {
		t.Fatalf("packet aggregates = %+v, want %d each", snapshot.quic, wantEvents)
	}
	if snapshot.quic.LatestRTTNS == nil || *snapshot.quic.LatestRTTNS != uint64(7*time.Millisecond) ||
		snapshot.quic.SmoothedRTTNS == nil || *snapshot.quic.SmoothedRTTNS != uint64(5*time.Millisecond) ||
		snapshot.quic.MinimumRTTNS == nil || *snapshot.quic.MinimumRTTNS != uint64(3*time.Millisecond) ||
		snapshot.quic.CongestionWindowBytes == nil || *snapshot.quic.CongestionWindowBytes != 64_000 ||
		snapshot.quic.BytesInFlight == nil || *snapshot.quic.BytesInFlight != 12_000 {
		t.Fatalf("recovery aggregates = %+v", snapshot.quic)
	}
	if snapshot.quic.GenerationsStarted != 1 || snapshot.quic.ConnectionsOpened != 1 ||
		snapshot.quic.ConnectionsClosed != 1 || snapshot.quic.HandshakeSuccesses != 1 ||
		snapshot.quic.DialAttempts != 1 || snapshot.quic.DialSuccesses != 1 || snapshot.quic.DialFailures != 0 ||
		snapshot.trackedGenerations != 0 {
		t.Fatalf("lifecycle aggregates = %+v, tracked=%d", snapshot.quic, snapshot.trackedGenerations)
	}
	if len(snapshot.quic.Slots) != 1 || snapshot.quic.Slots[0].SlotID != 3 ||
		snapshot.quic.Slots[0].LatestGeneration != 1 || snapshot.quic.Slots[0].DialAttempts != 1 ||
		snapshot.quic.Slots[0].DialSuccesses != 1 || snapshot.quic.Slots[0].DialFailures != 0 {
		t.Fatalf("bounded slot telemetry = %+v", snapshot.quic.Slots)
	}
}

func TestQUICDiagnosticsLateOldGenerationCloseDoesNotCloseReplacement(t *testing.T) {
	diagnostics, err := newQUICDiagnostics(false, "")
	if err != nil {
		t.Fatal(err)
	}
	slot := newQUICSlotDiagnostics(5)
	factory := diagnostics.tracerForSlot(slot, nil)
	oldRecorder := factory(context.Background(), true, quic.ConnectionIDFromBytes([]byte{1})).AddProducer()
	newRecorder := factory(context.Background(), true, quic.ConnectionIDFromBytes([]byte{2})).AddProducer()

	oldRecorder.RecordEvent(qlog.ConnectionClosed{})
	if err := oldRecorder.Close(); err != nil {
		t.Fatal(err)
	}
	snapshot := diagnostics.snapshot([]*quicSlotDiagnostics{slot})
	if snapshot.trackedGenerations != 1 || snapshot.quic.Slots[0].LatestGeneration != 2 ||
		snapshot.quic.Slots[0].LiveGenerations != 1 {
		t.Fatalf("late close affected replacement: quic=%+v tracked=%d", snapshot.quic, snapshot.trackedGenerations)
	}

	newRecorder.RecordEvent(qlog.ConnectionClosed{})
	_ = newRecorder.Close()
	if got := diagnostics.snapshot([]*quicSlotDiagnostics{slot}).trackedGenerations; got != 0 {
		t.Fatalf("tracked generations after both close = %d, want 0", got)
	}
}

func TestQUICDiagnosticsAttributesInternalAttemptsWithoutCallingThemApplicationRetries(t *testing.T) {
	diagnostics, err := newQUICDiagnostics(false, "")
	if err != nil {
		t.Fatal(err)
	}
	slot := newQUICSlotDiagnostics(1)
	trace := diagnostics.tracerForSlot(slot, nil)(context.Background(), true, quic.ConnectionIDFromBytes([]byte{1}))
	recorder := trace.AddProducer()

	observation, clientTrace := diagnostics.newAttemptTrace(slot)
	clientTrace.GetConn("example.test:443")
	clientTrace.GotConn(httptrace.GotConnInfo{})
	// quic-go only internally retries a successfully written request when the
	// first stream was rejected with H3_REQUEST_REJECTED.
	clientTrace.WroteRequest(httptrace.WroteRequestInfo{})
	clientTrace.GetConn("example.test:443")
	clientTrace.GotConn(httptrace.GotConnInfo{})
	clientTrace.WroteRequest(httptrace.WroteRequestInfo{})
	observation.finish(nil)

	rejected, rejectedTrace := diagnostics.newAttemptTrace(slot)
	rejectedTrace.GetConn("example.test:443")
	rejectedTrace.GotConn(httptrace.GotConnInfo{})
	rejectedTrace.WroteRequest(httptrace.WroteRequestInfo{})
	rejected.finish(&http3.Error{ErrorCode: http3.ErrCodeRequestRejected})

	snapshot := diagnostics.snapshot([]*quicSlotDiagnostics{slot})
	if snapshot.internalAttempts == nil || *snapshot.internalAttempts != 3 ||
		snapshot.internalRetries == nil || *snapshot.internalRetries != 1 {
		t.Fatalf("internal attempt ledger = attempts %v retries %v", snapshot.internalAttempts, snapshot.internalRetries)
	}
	if snapshot.observedPreHandlerFailures != 1 ||
		snapshot.quic.InternallyRecoveredApplicationAttempts != 1 ||
		snapshot.quic.RequestRejections != 2 || snapshot.quic.FinalApplicationErrors != 1 {
		t.Fatalf("attempt attribution = %+v, pre-handler=%d", snapshot.quic, snapshot.observedPreHandlerFailures)
	}
	if got := snapshot.quic.Slots[0].LatestAttributedGeneration; got != 1 {
		t.Fatalf("latest attributed generation = %d, want 1", got)
	}

	recorder.RecordEvent(qlog.ConnectionClosed{})
	_ = recorder.Close()
}

func TestQUICDiagnosticsDisabledAggregateFieldsAreUnavailableNotZero(t *testing.T) {
	diagnostics, err := newQUICDiagnostics(false, "")
	if err != nil {
		t.Fatal(err)
	}
	snapshot := diagnostics.snapshot(nil)
	if snapshot.quic.PacketsSent != nil || snapshot.quic.LatestRTTNS != nil {
		t.Fatalf("disabled aggregates reported observed zero: %+v", snapshot.quic)
	}
	if snapshot.quic.Unavailable["packets_sent"] != "quic_telemetry_disabled" ||
		snapshot.quic.Unavailable["latest_rtt_ns"] != "quic_telemetry_disabled" {
		t.Fatalf("disabled aggregate reasons = %+v", snapshot.quic.Unavailable)
	}
}

func TestQUICDiagnosticsClassifiesFinalErrorsExclusively(t *testing.T) {
	diagnostics, err := newQUICDiagnostics(false, "")
	if err != nil {
		t.Fatal(err)
	}
	slot := newQUICSlotDiagnostics(0)

	canceled, canceledTrace := diagnostics.newAttemptTrace(slot)
	canceledTrace.WroteRequest(httptrace.WroteRequestInfo{})
	canceled.finish(errors.Join(context.Canceled, &quic.StreamError{ErrorCode: 1}))

	deadline, _ := diagnostics.newAttemptTrace(slot)
	deadline.finish(context.DeadlineExceeded)

	stream, streamTrace := diagnostics.newAttemptTrace(slot)
	streamTrace.WroteRequest(httptrace.WroteRequestInfo{})
	stream.finish(&quic.StreamError{ErrorCode: 2})

	connection, connectionTrace := diagnostics.newAttemptTrace(slot)
	connectionTrace.WroteRequest(httptrace.WroteRequestInfo{})
	connection.finish(&quic.ApplicationError{ErrorCode: 3})

	snapshot := diagnostics.snapshot([]*quicSlotDiagnostics{slot})
	if snapshot.quic.FinalApplicationErrors != 4 || snapshot.quic.CallerCancellations != 1 ||
		snapshot.quic.DeadlineCancellations != 1 || snapshot.quic.RequestStreamErrors != 1 ||
		snapshot.quic.ConnectionErrors != 1 || snapshot.observedPreHandlerFailures != 1 {
		t.Fatalf("exclusive error attribution = %+v, pre-handler=%d", snapshot.quic, snapshot.observedPreHandlerFailures)
	}
}

func TestQUICDiagnosticsFullQlogUsesBoundedOwnerOnlyFiles(t *testing.T) {
	diagnostics, err := newQUICDiagnostics(false, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	slot := newQUICSlotDiagnostics(2)
	recorder := diagnostics.tracerForSlot(slot, nil)(
		context.Background(), true, quic.ConnectionIDFromBytes([]byte{0xaa}),
	).AddProducer()
	recorder.RecordEvent(qlog.StartedConnection{})
	recorder.RecordEvent(qlog.ConnectionClosed{})
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(diagnostics.qlogDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("qlog file count = %d, want 1", len(entries))
	}
	info, err := entries[0].Info()
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("qlog permissions = %o, want 600", info.Mode().Perm())
	}
	data, err := os.ReadFile(diagnostics.qlogDir + string(os.PathSeparator) + entries[0].Name())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "transport:connection_started") {
		t.Fatalf("qlog omitted connection event: %q", data)
	}
	if !strings.Contains(string(data), "urn:ietf:params:qlog:events:http3-12") {
		t.Fatalf("qlog omitted HTTP/3 event schema: %q", data)
	}
	snapshot := diagnostics.snapshot([]*quicSlotDiagnostics{slot})
	if !snapshot.quic.QLogEnabled || snapshot.quic.QLogFilesCreated != 1 || snapshot.quic.QLogCreateErrors != 0 {
		t.Fatalf("qlog telemetry = %+v", snapshot.quic)
	}
}

func TestQUICDiagnosticsComposeAnExistingTracer(t *testing.T) {
	diagnostics, err := newQUICDiagnostics(false, "")
	if err != nil {
		t.Fatal(err)
	}
	existing := &countingQUICTrace{}
	slot := newQUICSlotDiagnostics(4)
	trace := diagnostics.tracerForSlot(slot, func(context.Context, bool, quic.ConnectionID) qlogwriter.Trace {
		return existing
	})(context.Background(), true, quic.ConnectionIDFromBytes([]byte{1}))
	if !trace.SupportsSchemas(countingQUICSchema) {
		t.Fatal("composed trace hid the existing tracer's schema")
	}
	recorder := trace.AddProducer()
	recorder.RecordEvent(qlog.StartedConnection{})
	recorder.RecordEvent(qlog.ConnectionClosed{})
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	if existing.events.Load() != 2 || existing.closes.Load() != 1 {
		t.Fatalf("existing tracer saw events=%d closes=%d, want 2 and 1", existing.events.Load(), existing.closes.Load())
	}
}

const countingQUICSchema = "urn:h3fuzz:test"

type countingQUICTrace struct {
	events atomic.Uint64
	closes atomic.Uint64
}

func (t *countingQUICTrace) SupportsSchemas(schema string) bool {
	return schema == countingQUICSchema
}

func (t *countingQUICTrace) AddProducer() qlogwriter.Recorder {
	return &countingQUICRecorder{trace: t}
}

type countingQUICRecorder struct {
	trace *countingQUICTrace
}

func (r *countingQUICRecorder) RecordEvent(qlogwriter.Event) {
	r.trace.events.Add(1)
}

func (r *countingQUICRecorder) Close() error {
	r.trace.closes.Add(1)
	return nil
}
