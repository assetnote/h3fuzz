package runner

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	http3qlog "github.com/quic-go/quic-go/http3/qlog"
	"github.com/quic-go/quic-go/qlog"
	"github.com/quic-go/quic-go/qlogwriter"
)

const quicMetricUnavailableDisabled = "quic_telemetry_disabled"

// quicDiagnostics is deliberately an aggregate recorder. It never retains a
// per-packet or per-attempt event history; the only cardinality that grows with
// configuration is the single row held by each HTTP/3 pool slot.
type quicDiagnostics struct {
	aggregate bool
	qlogDir   string
	drainWake chan struct{}

	generationsStarted atomic.Uint64
	trackedGenerations atomic.Uint64
	connectionsOpened  atomic.Uint64
	connectionsClosed  atomic.Uint64
	dialAttempts       atomic.Uint64
	dialSuccesses      atomic.Uint64
	dialFailures       atomic.Uint64
	handshakeSuccesses atomic.Uint64
	handshakeFailures  atomic.Uint64

	internalAttempts atomic.Uint64
	internalRetries  atomic.Uint64
	preHandlerErrors atomic.Uint64

	internallyRecovered atomic.Uint64
	finalErrors         atomic.Uint64
	callerCancellations atomic.Uint64
	deadlineErrors      atomic.Uint64
	requestRejections   atomic.Uint64
	requestStreamErrors atomic.Uint64
	connectionErrors    atomic.Uint64

	packetsSent      atomic.Uint64
	packetsLost      atomic.Uint64
	ptoCountChanges  atomic.Uint64
	latestRTT        observedUint64
	smoothedRTT      observedUint64
	minimumRTT       observedUint64
	congestionWindow observedUint64
	bytesInFlight    observedUint64

	qlogFilesCreated atomic.Uint64
	qlogCreateErrors atomic.Uint64
	qlogSequence     atomic.Uint64
}

type observedUint64 struct {
	value    atomic.Uint64
	observed atomic.Bool
}

func (v *observedUint64) store(value uint64) {
	v.value.Store(value)
	v.observed.Store(true)
}

func (v *observedUint64) snapshot() *uint64 {
	if !v.observed.Load() {
		return nil
	}
	value := v.value.Load()
	return &value
}

type quicSlotDiagnostics struct {
	id atomic.Int64

	nextGeneration             atomic.Uint64
	current                    atomic.Pointer[quicGenerationDiagnostics]
	latestAttributedGeneration atomic.Uint64
	liveGenerations            atomic.Uint64
	dialAttempts               atomic.Uint64
	dialSuccesses              atomic.Uint64
	dialFailures               atomic.Uint64
	connectionsOpened          atomic.Uint64
	connectionsClosed          atomic.Uint64
	handshakeSuccesses         atomic.Uint64
	handshakeFailures          atomic.Uint64
	internalAttempts           atomic.Uint64
	internalRetries            atomic.Uint64
}

func newQUICSlotDiagnostics(slotID int) *quicSlotDiagnostics {
	slot := &quicSlotDiagnostics{}
	slot.id.Store(int64(slotID))
	return slot
}

type quicGenerationDiagnostics struct {
	owner      *quicDiagnostics
	slot       *quicSlotDiagnostics
	generation uint64

	producers atomic.Int64
	opened    atomic.Bool
	closed    atomic.Bool
	// 0 is unknown, 1 is success, and 2 is failure.
	handshake atomic.Uint32
}

type quicDiagnosticsSnapshot struct {
	quic                       ffuf.RunQUICDiagnosticsTelemetry
	trackedGenerations         uint64
	internalAttempts           *uint64
	internalRetries            *uint64
	observedPreHandlerFailures uint64
}

func newQUICDiagnostics(aggregate bool, qlogDir string) (*quicDiagnostics, error) {
	if qlogDir != "" {
		if err := os.MkdirAll(qlogDir, 0o700); err != nil {
			return nil, fmt.Errorf("create QUIC qlog directory: %w", err)
		}
		info, err := os.Stat(qlogDir)
		if err != nil {
			return nil, fmt.Errorf("inspect QUIC qlog directory: %w", err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("QUIC qlog path %q is not a directory", qlogDir)
		}
	}
	return &quicDiagnostics{aggregate: aggregate, qlogDir: qlogDir, drainWake: make(chan struct{}, 1)}, nil
}

type quicTracerFactory func(context.Context, bool, quic.ConnectionID) qlogwriter.Trace

// tracerForSlot composes diagnostics with an existing quic-go tracer. It does
// not replace dialing, and therefore leaves transport ownership and pooling to
// quic-go's supported public APIs.
func (d *quicDiagnostics) tracerForSlot(slot *quicSlotDiagnostics, existing quicTracerFactory) quicTracerFactory {
	return func(ctx context.Context, isClient bool, connectionID quic.ConnectionID) qlogwriter.Trace {
		generationID := slot.nextGeneration.Add(1)
		generation := &quicGenerationDiagnostics{
			owner:      d,
			slot:       slot,
			generation: generationID,
		}
		slot.current.Store(generation)
		slot.liveGenerations.Add(1)
		d.generationsStarted.Add(1)
		d.trackedGenerations.Add(1)

		traces := []qlogwriter.Trace{&quicAggregateTrace{generation: generation}}
		if existing != nil {
			if trace := existing(ctx, isClient, connectionID); trace != nil {
				traces = append(traces, trace)
			}
		}
		if trace := d.newQlogTrace(slot, generationID, isClient, connectionID); trace != nil {
			traces = append(traces, trace)
		}
		return multiplexQUICTrace(traces)
	}
}

func (d *quicDiagnostics) newQlogTrace(slot *quicSlotDiagnostics, generation uint64, isClient bool, connectionID quic.ConnectionID) qlogwriter.Trace {
	if d.qlogDir == "" {
		return nil
	}
	sequence := d.qlogSequence.Add(1)
	name := fmt.Sprintf("h3fuzz-quic-slot-%d-generation-%d-%d-%s.sqlog", slot.id.Load(), generation, sequence, connectionID.String())
	file, err := os.OpenFile(filepath.Join(d.qlogDir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		d.qlogCreateErrors.Add(1)
		return nil
	}
	trace := qlogwriter.NewConnectionFileSeq(file, isClient, connectionID, []string{qlog.EventSchema, http3qlog.EventSchema})
	d.qlogFilesCreated.Add(1)
	go trace.Run()
	return trace
}

func (d *quicDiagnostics) lifecycleClientTrace(slot *quicSlotDiagnostics) *httptrace.ClientTrace {
	trace := &httptrace.ClientTrace{}
	d.addLifecycleHooks(trace, slot)
	return trace
}

func (d *quicDiagnostics) addLifecycleHooks(trace *httptrace.ClientTrace, slot *quicSlotDiagnostics) {
	trace.ConnectStart = func(_, _ string) {
		d.dialAttempts.Add(1)
		slot.dialAttempts.Add(1)
	}
	trace.ConnectDone = func(_, _ string, err error) {
		if err == nil {
			d.dialSuccesses.Add(1)
			slot.dialSuccesses.Add(1)
			return
		}
		d.dialFailures.Add(1)
		slot.dialFailures.Add(1)
	}
	trace.TLSHandshakeDone = func(_ tls.ConnectionState, err error) {
		generation := slot.current.Load()
		if generation == nil {
			return
		}
		generation.recordHandshake(err)
	}
}

type quicAttemptObservation struct {
	owner *quicDiagnostics
	slot  *quicSlotDiagnostics

	attempts         atomic.Uint64
	successfulWrites atomic.Uint64
	wroteRequest     atomic.Bool
	finished         atomic.Bool
}

func (d *quicDiagnostics) newAttemptTrace(slot *quicSlotDiagnostics) (*quicAttemptObservation, *httptrace.ClientTrace) {
	observation := &quicAttemptObservation{owner: d, slot: slot}
	trace := &httptrace.ClientTrace{
		GetConn: func(_ string) {
			attempt := observation.attempts.Add(1)
			d.internalAttempts.Add(1)
			slot.internalAttempts.Add(1)
			if attempt > 1 {
				d.internalRetries.Add(1)
				slot.internalRetries.Add(1)
				// The pinned quic-go transport retries only two internal error
				// classes. A retry after WroteRequest(nil) proves that the prior
				// stream was H3_REQUEST_REJECTED; an unusable connection fails
				// before that callback. If an asynchronous body callback arrives
				// later, leave the cause unattributed rather than guessing.
				if observation.successfulWrites.Load() >= attempt-1 {
					d.requestRejections.Add(1)
				}
			}
		},
		GotConn: func(_ httptrace.GotConnInfo) {
			if generation := slot.current.Load(); generation != nil {
				slot.latestAttributedGeneration.Store(generation.generation)
			}
		},
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err == nil {
				observation.successfulWrites.Add(1)
				observation.wroteRequest.Store(true)
			}
		},
	}
	d.addLifecycleHooks(trace, slot)
	return observation, trace
}

func (d *quicDiagnostics) recordUnattributedApplicationFailure(err error) {
	observation := &quicAttemptObservation{owner: d}
	observation.finish(err)
}

func (o *quicAttemptObservation) finish(err error) {
	if !o.finished.CompareAndSwap(false, true) {
		return
	}
	if err == nil {
		if o.attempts.Load() > 1 {
			o.owner.internallyRecovered.Add(1)
		}
		return
	}

	o.owner.finalErrors.Add(1)
	if errors.Is(err, context.DeadlineExceeded) {
		o.owner.deadlineErrors.Add(1)
		if !o.wroteRequest.Load() {
			o.owner.preHandlerErrors.Add(1)
		}
		return
	}
	if errors.Is(err, context.Canceled) {
		o.owner.callerCancellations.Add(1)
		if !o.wroteRequest.Load() {
			o.owner.preHandlerErrors.Add(1)
		}
		return
	}

	var h3Error *http3.Error
	if errors.As(err, &h3Error) {
		if h3Error.ErrorCode == http3.ErrCodeRequestRejected {
			o.owner.requestRejections.Add(1)
			o.owner.preHandlerErrors.Add(1)
			return
		}
		o.owner.requestStreamErrors.Add(1)
		if !o.wroteRequest.Load() {
			o.owner.preHandlerErrors.Add(1)
		}
		return
	}

	var streamError *quic.StreamError
	if errors.As(err, &streamError) {
		o.owner.requestStreamErrors.Add(1)
	} else if isQUICConnectionError(err) {
		o.owner.connectionErrors.Add(1)
	}
	if !o.wroteRequest.Load() {
		o.owner.preHandlerErrors.Add(1)
	}
}

func isQUICConnectionError(err error) bool {
	var transportError *quic.TransportError
	var applicationError *quic.ApplicationError
	var statelessResetError *quic.StatelessResetError
	var handshakeTimeoutError *quic.HandshakeTimeoutError
	var idleTimeoutError *quic.IdleTimeoutError
	var versionNegotiationError *quic.VersionNegotiationError
	return errors.As(err, &transportError) ||
		errors.As(err, &applicationError) ||
		errors.As(err, &statelessResetError) ||
		errors.As(err, &handshakeTimeoutError) ||
		errors.As(err, &idleTimeoutError) ||
		errors.As(err, &versionNegotiationError)
}

func (d *quicDiagnostics) snapshot(slots []*quicSlotDiagnostics) quicDiagnosticsSnapshot {
	quicTelemetry := ffuf.RunQUICDiagnosticsTelemetry{
		LifecycleEnabled: true,
		AggregateEnabled: d.aggregate,
		QLogEnabled:      d.qlogDir != "",

		GenerationsStarted: d.generationsStarted.Load(),
		ConnectionsOpened:  d.connectionsOpened.Load(),
		ConnectionsClosed:  d.connectionsClosed.Load(),
		DialAttempts:       d.dialAttempts.Load(),
		DialSuccesses:      d.dialSuccesses.Load(),
		DialFailures:       d.dialFailures.Load(),
		HandshakeSuccesses: d.handshakeSuccesses.Load(),
		HandshakeFailures:  d.handshakeFailures.Load(),

		InternallyRecoveredApplicationAttempts: d.internallyRecovered.Load(),
		FinalApplicationErrors:                 d.finalErrors.Load(),
		CallerCancellations:                    d.callerCancellations.Load(),
		DeadlineCancellations:                  d.deadlineErrors.Load(),
		RequestRejections:                      d.requestRejections.Load(),
		RequestStreamErrors:                    d.requestStreamErrors.Load(),
		ConnectionErrors:                       d.connectionErrors.Load(),

		QLogFilesCreated: d.qlogFilesCreated.Load(),
		QLogCreateErrors: d.qlogCreateErrors.Load(),
		Unavailable:      make(map[string]string),
		Slots:            make([]ffuf.RunQUICSlotTelemetry, 0, len(slots)),
	}
	if d.aggregate {
		quicTelemetry.PacketsSent = uint64Pointer(d.packetsSent.Load())
		quicTelemetry.PacketsLost = uint64Pointer(d.packetsLost.Load())
		quicTelemetry.PTOCountChanges = uint64Pointer(d.ptoCountChanges.Load())
		quicTelemetry.LatestRTTNS = d.latestRTT.snapshot()
		quicTelemetry.SmoothedRTTNS = d.smoothedRTT.snapshot()
		quicTelemetry.MinimumRTTNS = d.minimumRTT.snapshot()
		quicTelemetry.CongestionWindowBytes = d.congestionWindow.snapshot()
		quicTelemetry.BytesInFlight = d.bytesInFlight.snapshot()
		markUnobservedQUICMetrics(&quicTelemetry)
	} else {
		for _, metric := range quicAggregateMetricNames {
			quicTelemetry.Unavailable[metric] = quicMetricUnavailableDisabled
		}
	}
	for _, slot := range slots {
		if slot == nil {
			continue
		}
		quicTelemetry.Slots = append(quicTelemetry.Slots, ffuf.RunQUICSlotTelemetry{
			SlotID:                     int(slot.id.Load()),
			GenerationsStarted:         slot.nextGeneration.Load(),
			LatestGeneration:           slot.nextGeneration.Load(),
			LatestAttributedGeneration: slot.latestAttributedGeneration.Load(),
			LiveGenerations:            slot.liveGenerations.Load(),
			DialAttempts:               slot.dialAttempts.Load(),
			DialSuccesses:              slot.dialSuccesses.Load(),
			DialFailures:               slot.dialFailures.Load(),
			ConnectionsOpened:          slot.connectionsOpened.Load(),
			ConnectionsClosed:          slot.connectionsClosed.Load(),
			HandshakeSuccesses:         slot.handshakeSuccesses.Load(),
			HandshakeFailures:          slot.handshakeFailures.Load(),
			InternalAttempts:           slot.internalAttempts.Load(),
			InternalRetries:            slot.internalRetries.Load(),
		})
	}
	internalAttempts := d.internalAttempts.Load()
	internalRetries := d.internalRetries.Load()
	return quicDiagnosticsSnapshot{
		quic:                       quicTelemetry,
		trackedGenerations:         d.trackedGenerations.Load(),
		internalAttempts:           &internalAttempts,
		internalRetries:            &internalRetries,
		observedPreHandlerFailures: d.preHandlerErrors.Load(),
	}
}

func (d *quicDiagnostics) waitForDrain(timeout time.Duration) error {
	if d.trackedGenerations.Load() == 0 {
		return nil
	}
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for d.trackedGenerations.Load() != 0 {
		select {
		case <-d.drainWake:
		case <-timer.C:
			return fmt.Errorf("timed out draining %d tracked QUIC connection generations", d.trackedGenerations.Load())
		}
	}
	return nil
}

func uint64Pointer(value uint64) *uint64 { return &value }

var quicAggregateMetricNames = []string{
	"packets_sent",
	"packets_lost",
	"pto_count_changes",
	"latest_rtt_ns",
	"smoothed_rtt_ns",
	"minimum_rtt_ns",
	"congestion_window_bytes",
	"bytes_in_flight",
}

func unavailableQUICDiagnostics(reason string) ffuf.RunQUICDiagnosticsTelemetry {
	telemetry := ffuf.RunQUICDiagnosticsTelemetry{
		Unavailable: make(map[string]string, len(quicAggregateMetricNames)),
		Slots:       []ffuf.RunQUICSlotTelemetry{},
	}
	for _, metric := range quicAggregateMetricNames {
		telemetry.Unavailable[metric] = reason
	}
	return telemetry
}

func markUnobservedQUICMetrics(telemetry *ffuf.RunQUICDiagnosticsTelemetry) {
	metrics := []struct {
		name  string
		value *uint64
	}{
		{"latest_rtt_ns", telemetry.LatestRTTNS},
		{"smoothed_rtt_ns", telemetry.SmoothedRTTNS},
		{"minimum_rtt_ns", telemetry.MinimumRTTNS},
		{"congestion_window_bytes", telemetry.CongestionWindowBytes},
		{"bytes_in_flight", telemetry.BytesInFlight},
	}
	for _, metric := range metrics {
		if metric.value == nil {
			telemetry.Unavailable[metric.name] = "not_observed"
		}
	}
}

type quicAggregateTrace struct {
	generation *quicGenerationDiagnostics
}

func (t *quicAggregateTrace) SupportsSchemas(schema string) bool {
	return schema == qlog.EventSchema
}

func (t *quicAggregateTrace) AddProducer() qlogwriter.Recorder {
	t.generation.producers.Add(1)
	return &quicAggregateRecorder{generation: t.generation}
}

type quicAggregateRecorder struct {
	generation *quicGenerationDiagnostics
	closed     atomic.Bool
}

func (r *quicAggregateRecorder) RecordEvent(event qlogwriter.Event) {
	switch event := event.(type) {
	case qlog.StartedConnection, *qlog.StartedConnection:
		r.generation.recordOpened()
	case qlog.ConnectionClosed, *qlog.ConnectionClosed:
		r.generation.recordClosed()
	case qlog.PacketSent, *qlog.PacketSent:
		if r.generation.owner.aggregate {
			r.generation.owner.packetsSent.Add(1)
		}
	case qlog.PacketLost, *qlog.PacketLost:
		if r.generation.owner.aggregate {
			r.generation.owner.packetsLost.Add(1)
		}
	case qlog.PTOCountUpdated, *qlog.PTOCountUpdated:
		if r.generation.owner.aggregate {
			r.generation.owner.ptoCountChanges.Add(1)
		}
	case qlog.MetricsUpdated:
		if r.generation.owner.aggregate {
			r.generation.recordMetrics(event)
		}
	case *qlog.MetricsUpdated:
		if r.generation.owner.aggregate {
			r.generation.recordMetrics(*event)
		}
	}
}

func (r *quicAggregateRecorder) Close() error {
	if !r.closed.CompareAndSwap(false, true) {
		return nil
	}
	if r.generation.producers.Add(-1) == 0 {
		r.generation.recordClosed()
	}
	return nil
}

func (g *quicGenerationDiagnostics) recordOpened() {
	if !g.opened.CompareAndSwap(false, true) {
		return
	}
	g.owner.connectionsOpened.Add(1)
	g.slot.connectionsOpened.Add(1)
}

func (g *quicGenerationDiagnostics) recordHandshake(err error) {
	state := uint32(1)
	if err != nil {
		state = 2
	}
	if !g.handshake.CompareAndSwap(0, state) {
		return
	}
	if err == nil {
		g.owner.handshakeSuccesses.Add(1)
		g.slot.handshakeSuccesses.Add(1)
		return
	}
	g.owner.handshakeFailures.Add(1)
	g.slot.handshakeFailures.Add(1)
}

func (g *quicGenerationDiagnostics) recordClosed() {
	if !g.closed.CompareAndSwap(false, true) {
		return
	}
	// A generation that closes without a successful TLS callback is a failed
	// handshake. This also covers connection setup failures where httptrace
	// never emits TLSHandshakeDone.
	if g.handshake.CompareAndSwap(0, 2) {
		g.owner.handshakeFailures.Add(1)
		g.slot.handshakeFailures.Add(1)
	}
	g.owner.connectionsClosed.Add(1)
	g.slot.connectionsClosed.Add(1)
	g.owner.trackedGenerations.Add(^uint64(0))
	g.slot.liveGenerations.Add(^uint64(0))
	select {
	case g.owner.drainWake <- struct{}{}:
	default:
	}
}

func (g *quicGenerationDiagnostics) recordMetrics(event qlog.MetricsUpdated) {
	g.owner.latestRTT.store(uint64(event.LatestRTT))
	g.owner.smoothedRTT.store(uint64(event.SmoothedRTT))
	g.owner.minimumRTT.store(uint64(event.MinRTT))
	g.owner.congestionWindow.store(uint64(event.CongestionWindow))
	g.owner.bytesInFlight.store(uint64(event.BytesInFlight))
}

type multiplexQUICTrace []qlogwriter.Trace

func (t multiplexQUICTrace) SupportsSchemas(schema string) bool {
	for _, trace := range t {
		if trace != nil && trace.SupportsSchemas(schema) {
			return true
		}
	}
	return false
}

func (t multiplexQUICTrace) AddProducer() qlogwriter.Recorder {
	recorders := make(multiplexQUICRecorder, 0, len(t))
	for _, trace := range t {
		if trace == nil {
			continue
		}
		if recorder := trace.AddProducer(); recorder != nil {
			recorders = append(recorders, recorder)
		}
	}
	return recorders
}

type multiplexQUICRecorder []qlogwriter.Recorder

func (r multiplexQUICRecorder) RecordEvent(event qlogwriter.Event) {
	for _, recorder := range r {
		recorder.RecordEvent(event)
	}
}

func (r multiplexQUICRecorder) Close() error {
	var result error
	for _, recorder := range r {
		result = errors.Join(result, recorder.Close())
	}
	return result
}

var _ qlogwriter.Trace = (*quicAggregateTrace)(nil)
var _ qlogwriter.Trace = multiplexQUICTrace(nil)
var _ qlogwriter.Recorder = (*quicAggregateRecorder)(nil)
var _ qlogwriter.Recorder = multiplexQUICRecorder(nil)
