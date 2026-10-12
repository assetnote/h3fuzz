package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

type runTelemetryProvider interface {
	RunTelemetry() ffuf.RunTelemetry
}

type runTelemetryExecutor interface {
	ExecuteWithRunTelemetry(*ffuf.Request, ffuf.RunTransportAttemptRole, ffuf.RunTransportAttemptRecorder) (ffuf.Response, error)
}

type logicalInputState uint8

const (
	logicalInputUnseen logicalInputState = iota
	logicalInputScheduled
	logicalInputSuccess
	logicalInputError

	logicalLedgerWordsPerChunk = 64
	logicalLedgerIDsPerWord    = 32
	logicalLedgerIDsPerChunk   = logicalLedgerWordsPerChunk * logicalLedgerIDsPerWord
	logicalLedgerSampleLimit   = 100
)

type logicalLedgerChunk struct {
	words [logicalLedgerWordsPerChunk]atomic.Uint64
}

type logicalLedgerChunkTable struct {
	chunks []*logicalLedgerChunk
}

// logicalInputLedger stores two bits per logical input. Chunk-table snapshots
// are immutable, so terminal transitions do not contend with producer growth.
type logicalInputLedger struct {
	expected *uint64

	assigned              atomic.Uint64
	scheduledTransitions  atomic.Uint64
	successTerminalStates atomic.Uint64
	errorTerminalStates   atomic.Uint64
	duplicateSchedules    atomic.Uint64
	duplicateTerminals    atomic.Uint64
	invalidTransitions    atomic.Uint64
	outOfRangeIDs         atomic.Uint64
	chunks                atomic.Pointer[logicalLedgerChunkTable]
	growMu                sync.Mutex
	anomalyMu             sync.Mutex
	anomalySamples        []uint64
}

func newLogicalInputLedger(expected *uint64) *logicalInputLedger {
	ledger := &logicalInputLedger{}
	if expected != nil {
		value := *expected
		ledger.expected = &value
	}
	ledger.chunks.Store(&logicalLedgerChunkTable{})
	return ledger
}

func (l *logicalInputLedger) inExpectedRange(id uint64) bool {
	return id > 0 && (l.expected == nil || id <= *l.expected)
}

func (l *logicalInputLedger) recordAnomaly(id uint64) {
	l.anomalyMu.Lock()
	defer l.anomalyMu.Unlock()
	for _, existing := range l.anomalySamples {
		if existing == id {
			return
		}
	}
	if len(l.anomalySamples) < logicalLedgerSampleLimit {
		l.anomalySamples = append(l.anomalySamples, id)
	}
}

func (l *logicalInputLedger) chunkFor(id uint64, create bool) *logicalLedgerChunk {
	chunkIndex := int((id - 1) / logicalLedgerIDsPerChunk)
	table := l.chunks.Load()
	if table != nil && chunkIndex < len(table.chunks) {
		return table.chunks[chunkIndex]
	}
	if !create {
		return nil
	}

	l.growMu.Lock()
	defer l.growMu.Unlock()
	table = l.chunks.Load()
	if chunkIndex < len(table.chunks) {
		return table.chunks[chunkIndex]
	}
	chunks := make([]*logicalLedgerChunk, len(table.chunks), chunkIndex+1)
	copy(chunks, table.chunks)
	for len(chunks) <= chunkIndex {
		chunks = append(chunks, &logicalLedgerChunk{})
	}
	l.chunks.Store(&logicalLedgerChunkTable{chunks: chunks})
	return chunks[chunkIndex]
}

func ledgerWordPosition(id uint64) (int, uint) {
	withinChunk := (id - 1) % logicalLedgerIDsPerChunk
	return int(withinChunk / logicalLedgerIDsPerWord), uint((withinChunk % logicalLedgerIDsPerWord) * 2)
}

func (l *logicalInputLedger) transition(id uint64, from, to logicalInputState) logicalInputState {
	chunk := l.chunkFor(id, true)
	wordIndex, shift := ledgerWordPosition(id)
	mask := uint64(3) << shift
	word := &chunk.words[wordIndex]
	for {
		old := word.Load()
		state := logicalInputState((old & mask) >> shift)
		if state != from {
			return state
		}
		next := (old &^ mask) | uint64(to)<<shift
		if word.CompareAndSwap(old, next) {
			return from
		}
	}
}

func (l *logicalInputLedger) state(id uint64) logicalInputState {
	chunk := l.chunkFor(id, false)
	if chunk == nil {
		return logicalInputUnseen
	}
	wordIndex, shift := ledgerWordPosition(id)
	return logicalInputState((chunk.words[wordIndex].Load() >> shift) & 3)
}

func (l *logicalInputLedger) assignAndSchedule() uint64 {
	id := l.assigned.Add(1)
	l.recordSchedule(id)
	return id
}

func (l *logicalInputLedger) recordSchedule(id uint64) {
	if !l.inExpectedRange(id) {
		l.outOfRangeIDs.Add(1)
		l.recordAnomaly(id)
		return
	}
	if id > l.assigned.Load() {
		l.invalidTransitions.Add(1)
		l.recordAnomaly(id)
		return
	}
	if prior := l.transition(id, logicalInputUnseen, logicalInputScheduled); prior != logicalInputUnseen {
		l.duplicateSchedules.Add(1)
		l.recordAnomaly(id)
		return
	}
	l.scheduledTransitions.Add(1)
}

func (l *logicalInputLedger) recordTerminal(id uint64, terminal logicalInputState) {
	if terminal != logicalInputSuccess && terminal != logicalInputError {
		l.invalidTransitions.Add(1)
		l.recordAnomaly(id)
		return
	}
	if !l.inExpectedRange(id) {
		l.outOfRangeIDs.Add(1)
		l.recordAnomaly(id)
		return
	}
	if id > l.assigned.Load() {
		l.invalidTransitions.Add(1)
		l.recordAnomaly(id)
		return
	}
	prior := l.transition(id, logicalInputScheduled, terminal)
	switch prior {
	case logicalInputScheduled:
		if terminal == logicalInputSuccess {
			l.successTerminalStates.Add(1)
		} else {
			l.errorTerminalStates.Add(1)
		}
	case logicalInputSuccess, logicalInputError:
		l.duplicateTerminals.Add(1)
		l.recordAnomaly(id)
	default:
		l.invalidTransitions.Add(1)
		l.recordAnomaly(id)
	}
}

func appendAnomalySample(samples []uint64, id uint64) []uint64 {
	for _, existing := range samples {
		if existing == id {
			return samples
		}
	}
	if len(samples) < logicalLedgerSampleLimit {
		return append(samples, id)
	}
	return samples
}

func (l *logicalInputLedger) snapshot() ffuf.RunLogicalLedgerTelemetry {
	assigned := l.assigned.Load()
	limit := assigned
	if l.expected != nil && limit > *l.expected {
		limit = *l.expected
	}
	l.anomalyMu.Lock()
	samples := append([]uint64(nil), l.anomalySamples...)
	l.anomalyMu.Unlock()
	var pending, gaps uint64
	for id := uint64(1); id <= limit; id++ {
		switch l.state(id) {
		case logicalInputUnseen:
			gaps++
			samples = appendAnomalySample(samples, id)
		case logicalInputScheduled:
			pending++
			samples = appendAnomalySample(samples, id)
		}
	}
	if l.expected != nil && assigned < *l.expected {
		gaps += *l.expected - assigned
		for id := assigned + 1; id <= *l.expected && len(samples) < logicalLedgerSampleLimit; id++ {
			samples = appendAnomalySample(samples, id)
		}
	}

	return ffuf.RunLogicalLedgerTelemetry{
		ExpectedExpandedInputs:       l.expected,
		AssignedIDs:                  assigned,
		ScheduledTransitions:         l.scheduledTransitions.Load(),
		SuccessTerminalStates:        l.successTerminalStates.Load(),
		ErrorTerminalStates:          l.errorTerminalStates.Load(),
		PendingScheduledStates:       pending,
		IDGaps:                       gaps,
		DuplicateScheduleTransitions: l.duplicateSchedules.Load(),
		DuplicateTerminalTransitions: l.duplicateTerminals.Load(),
		InvalidTransitions:           l.invalidTransitions.Load(),
		OutOfRangeIDs:                l.outOfRangeIDs.Load(),
		AnomalySampleIDs:             samples,
	}
}

func logicalLedgerComplete(report ffuf.RunLogicalLedgerTelemetry) bool {
	if report.ExpectedExpandedInputs != nil && report.AssignedIDs != *report.ExpectedExpandedInputs {
		return false
	}
	return report.AssignedIDs == report.ScheduledTransitions &&
		report.ScheduledTransitions == report.SuccessTerminalStates+report.ErrorTerminalStates &&
		report.PendingScheduledStates == 0 &&
		report.IDGaps == 0 &&
		report.DuplicateScheduleTransitions == 0 &&
		report.DuplicateTerminalTransitions == 0 &&
		report.InvalidTransitions == 0 &&
		report.OutOfRangeIDs == 0
}

func (j *Job) initializeLogicalTelemetry(expected int) {
	if !j.telemetryEnabled() {
		return
	}
	if expected < 0 {
		j.telemetryLedger = newLogicalInputLedger(nil)
		return
	}
	count := uint64(expected)
	j.telemetryLedger = newLogicalInputLedger(&count)
}

func (j *Job) scheduleLogicalInput() uint64 {
	if !j.telemetryEnabled() {
		return 0
	}
	atomic.AddInt64(&j.telemetryScheduled, 1)
	if j.telemetryLedger == nil {
		return 0
	}
	return j.telemetryLedger.assignAndSchedule()
}

func (j *Job) completeLogicalInput(id uint64, success bool) {
	if !j.telemetryEnabled() {
		return
	}
	atomic.AddInt64(&j.telemetryTerminalOutcomes, 1)
	terminal := logicalInputError
	if success {
		terminal = logicalInputSuccess
		atomic.AddInt64(&j.telemetrySuccessful, 1)
	} else {
		atomic.AddInt64(&j.telemetryTerminalErrors, 1)
	}
	if j.telemetryLedger != nil {
		j.telemetryLedger.recordTerminal(id, terminal)
	}
	j.recordMainTerminalTimestamp()
}

var runTelemetryMonotonicEpoch = time.Now()

func defaultRunTelemetryClock() int64 {
	// time.Since uses the monotonic component embedded in the process-local
	// epoch. Adding one reserves zero as the "not recorded" sentinel.
	return time.Since(runTelemetryMonotonicEpoch).Nanoseconds() + 1
}

func (j *Job) runTelemetryNow() int64 {
	if j.telemetryClock != nil {
		return j.telemetryClock()
	}
	return defaultRunTelemetryClock()
}

// beginMainRequest records the boundary immediately before the first outer
// main Runner.Execute call. sync.Once makes the first caller authoritative when
// workers begin concurrently.
func (j *Job) beginMainRequest() {
	if !j.telemetryEnabled() {
		return
	}
	j.telemetryStartOnce.Do(func() {
		atomic.StoreInt64(&j.telemetryMainStartNS, j.runTelemetryNow())
	})
}

func (j *Job) executeMainRequest(req *ffuf.Request, retried bool) (ffuf.Response, error) {
	if !j.telemetryEnabled() {
		return j.Runner.Execute(req)
	}
	if executor, ok := j.Runner.(runTelemetryExecutor); ok {
		role := ffuf.RunTransportRoleInitial
		if retried {
			role = ffuf.RunTransportRoleEngineRetry
		}
		return executor.ExecuteWithRunTelemetry(req, role, j)
	}
	// Third-party runners remain source-compatible. Their main-attempt counters
	// are unavailable rather than inferred from logical outcomes.
	return j.Runner.Execute(req)
}

func (j *Job) BeginRunTransportAttempt() {
	if !j.telemetryEnabled() {
		return
	}
	inFlight := atomic.AddInt64(&j.telemetryCurrentMainInFlight, 1)
	recordTelemetryMaximum(&j.telemetryPeakMainInFlight, inFlight)
}

func (j *Job) EndRunTransportAttempt() {
	if !j.telemetryEnabled() {
		return
	}
	atomic.AddInt64(&j.telemetryCurrentMainInFlight, -1)
}

func recordTelemetryMaximum(target *int64, candidate int64) {
	for {
		current := atomic.LoadInt64(target)
		if candidate <= current || atomic.CompareAndSwapInt64(target, current, candidate) {
			return
		}
	}
}

func (j *Job) recordWorkerSupervisorSnapshot(snapshot workerSupervisorSnapshot) {
	j.workerTelemetryMu.Lock()
	defer j.workerTelemetryMu.Unlock()
	telemetry := &j.workerTelemetry
	telemetry.ExecutionGeneration = snapshot.ExecutionGeneration
	telemetry.ProposalEpoch = snapshot.ProposalEpoch
	telemetry.AcknowledgementGeneration = snapshot.AcknowledgementGeneration
	telemetry.AcknowledgementEpoch = snapshot.AcknowledgementEpoch
	telemetry.StaleProposals += snapshot.StaleProposals
	telemetry.ProposedWorkers = snapshot.ProposedWorkers
	telemetry.AcknowledgedLiveWorkers = snapshot.AcknowledgedLiveWorkers
	telemetry.DesiredWorkers = snapshot.DesiredWorkers
	telemetry.LiveWorkers = snapshot.LiveWorkers
	telemetry.RetiringWorkers = snapshot.RetiringWorkers
	if snapshot.PeakWorkers > telemetry.PeakWorkers {
		telemetry.PeakWorkers = snapshot.PeakWorkers
	}
	telemetry.RemainingInputCap = snapshot.RemainingInputCap
	telemetry.SupervisorState = string(snapshot.State)
}

func (j *Job) workerTelemetrySnapshot() ffuf.RunWorkerCapacityTelemetry {
	j.workerTelemetryMu.Lock()
	defer j.workerTelemetryMu.Unlock()
	return j.workerTelemetry
}

func (j *Job) RecordRunTransportAttempt(attempt ffuf.RunTransportAttempt) {
	if !j.telemetryEnabled() {
		return
	}
	atomic.AddInt64(&j.telemetryApplicationAttempts, 1)
	if attempt.Completed {
		atomic.AddInt64(&j.telemetryCompletedAttempts, 1)
	}
	if attempt.SurfacedError {
		atomic.AddInt64(&j.telemetrySurfacedErrors, 1)
	}
	if attempt.Optimized {
		atomic.AddInt64(&j.telemetryOptimizedAttempts, 1)
	}
	switch attempt.Role {
	case ffuf.RunTransportRoleInitial:
		atomic.AddInt64(&j.telemetryRoleInitial, 1)
	case ffuf.RunTransportRoleOptimizationFallback:
		atomic.AddInt64(&j.telemetryRoleFallback, 1)
		atomic.AddInt64(&j.telemetryFullGETRecoveries, 1)
	case ffuf.RunTransportRoleEngineRetry:
		atomic.AddInt64(&j.telemetryRoleRetry, 1)
	case ffuf.RunTransportRoleEngineRetryOptimizationFallback:
		atomic.AddInt64(&j.telemetryRoleRetryFallback, 1)
		atomic.AddInt64(&j.telemetryFullGETRecoveries, 1)
	}
	switch {
	case strings.EqualFold(attempt.Method, "GET"):
		atomic.AddInt64(&j.telemetryMethodGET, 1)
	case strings.EqualFold(attempt.Method, "HEAD"):
		atomic.AddInt64(&j.telemetryMethodHEAD, 1)
	default:
		atomic.AddInt64(&j.telemetryMethodOther, 1)
	}
}

// recordMainTerminalTimestamp keeps the maximum terminal time because inputs
// complete out of scheduling order.
func (j *Job) recordMainTerminalTimestamp() {
	if !j.telemetryEnabled() {
		return
	}
	j.recordMainTerminalAt(j.runTelemetryNow())
}

func (j *Job) recordMainTerminalAt(now int64) {
	for {
		current := atomic.LoadInt64(&j.telemetryMaxTerminalNS)
		if now <= current || atomic.CompareAndSwapInt64(&j.telemetryMaxTerminalNS, current, now) {
			return
		}
	}
}

func (j *Job) mainRequestSpanNS() *uint64 {
	started := atomic.LoadInt64(&j.telemetryMainStartNS)
	terminal := atomic.LoadInt64(&j.telemetryMaxTerminalNS)
	if started <= 0 || terminal <= started {
		return nil
	}
	span := uint64(terminal - started)
	return &span
}

func (j *Job) writeRunTelemetry(workersDrained bool) error {
	if !j.telemetryEnabled() {
		return nil
	}
	provider, ok := j.Runner.(runTelemetryProvider)
	if !ok {
		return fmt.Errorf("runner does not expose run telemetry")
	}
	report := provider.RunTelemetry()
	report.SchemaVersion = 2
	profilingEnabled := j.Config != nil && j.Config.ProfilingEnabled
	if profilingEnabled {
		report.EligibleMode = "diagnostic"
	} else if report.EligibleMode == "" {
		report.EligibleMode = "ordinary"
	}
	scheduled := uint64(atomic.LoadInt64(&j.telemetryScheduled))
	successful := uint64(atomic.LoadInt64(&j.telemetrySuccessful))
	terminalOutcomes := uint64(atomic.LoadInt64(&j.telemetryTerminalOutcomes))
	terminalErrors := uint64(atomic.LoadInt64(&j.telemetryTerminalErrors))
	report.Responses = ffuf.RunResponseTelemetry{
		MainRequestSpanNS:          j.mainRequestSpanNS(),
		ScheduledLogicalInputs:     scheduled,
		SuccessfulLogicalResponses: successful,
		TerminalLogicalOutcomes:    terminalOutcomes,
		TerminalLogicalErrors:      terminalErrors,
	}
	if j.responseLatency != nil {
		latency := j.responseLatency.snapshot()
		report.Responses.Latency = &latency
	}
	report.Transport.ApplicationRetries = uint64(atomic.LoadInt64(&j.telemetryRetries))
	report.Transport.ApplicationAttempts = uint64(atomic.LoadInt64(&j.telemetryApplicationAttempts))
	report.Transport.CompletedAttempts = uint64(atomic.LoadInt64(&j.telemetryCompletedAttempts))
	report.Transport.SurfacedAttemptErrors = uint64(atomic.LoadInt64(&j.telemetrySurfacedErrors))
	report.Transport.OptimizedAttempts = uint64(atomic.LoadInt64(&j.telemetryOptimizedAttempts))
	report.Transport.FullGETRecoveries = uint64(atomic.LoadInt64(&j.telemetryFullGETRecoveries))
	report.Transport.AttemptRoles = ffuf.RunTransportRoleTelemetry{
		Initial:                         uint64(atomic.LoadInt64(&j.telemetryRoleInitial)),
		OptimizationFallback:            uint64(atomic.LoadInt64(&j.telemetryRoleFallback)),
		EngineRetry:                     uint64(atomic.LoadInt64(&j.telemetryRoleRetry)),
		EngineRetryOptimizationFallback: uint64(atomic.LoadInt64(&j.telemetryRoleRetryFallback)),
	}
	report.Transport.Methods = ffuf.RunTransportMethodTelemetry{
		GET:   uint64(atomic.LoadInt64(&j.telemetryMethodGET)),
		HEAD:  uint64(atomic.LoadInt64(&j.telemetryMethodHEAD)),
		Other: uint64(atomic.LoadInt64(&j.telemetryMethodOther)),
	}
	workers := j.workerTelemetrySnapshot()
	report.Workers.ExecutionGeneration = workers.ExecutionGeneration
	report.Workers.ProposalEpoch = workers.ProposalEpoch
	report.Workers.AcknowledgementGeneration = workers.AcknowledgementGeneration
	report.Workers.AcknowledgementEpoch = workers.AcknowledgementEpoch
	report.Workers.StaleProposals += workers.StaleProposals
	report.Workers.ProposedWorkers = workers.ProposedWorkers
	report.Workers.AcknowledgedLiveWorkers = workers.AcknowledgedLiveWorkers
	report.Workers.DesiredWorkers = workers.DesiredWorkers
	report.Workers.LiveWorkers = workers.LiveWorkers
	report.Workers.RetiringWorkers = workers.RetiringWorkers
	report.Workers.PeakWorkers = workers.PeakWorkers
	report.Workers.PeakMainInFlight = atomic.LoadInt64(&j.telemetryPeakMainInFlight)
	report.Workers.RemainingInputCap = workers.RemainingInputCap
	report.Workers.SupervisorState = workers.SupervisorState
	pauseObserved := atomic.LoadInt32(&j.telemetryPauseObserved) == 1
	requestDelay := j.Config != nil && j.Config.Delay.HasDelay
	rateLimited := j.Config != nil && j.Config.Rate > 0
	reasons := make([]string, 0, 5)
	if pauseObserved {
		reasons = append(reasons, "pause_observed")
	}
	if requestDelay {
		reasons = append(reasons, "request_delay_configured")
	}
	if rateLimited {
		reasons = append(reasons, "rate_limit_configured")
	}
	if j.Config != nil && j.Config.QuicQlogDir != "" {
		reasons = append(reasons, "quic_qlog_enabled")
	}
	if profilingEnabled {
		reasons = append(reasons, "runtime_profiling_enabled")
	}
	report.Eligibility = ffuf.RunEligibilityTelemetry{
		Eligible:          len(reasons) == 0,
		PauseObserved:     pauseObserved,
		RequestDelay:      requestDelay,
		RateLimited:       rateLimited,
		IneligibleReasons: reasons,
	}
	ledgerComplete := false
	if j.telemetryLedger != nil {
		report.LogicalLedger = j.telemetryLedger.snapshot()
		ledgerComplete = logicalLedgerComplete(report.LogicalLedger)
	}
	report.Drain.WorkersDrained = workersDrained
	report.Drain.Complete = workersDrained &&
		report.Drain.RunnerClosed &&
		report.Drain.CloseError == "" &&
		report.Transport.InFlight == 0 &&
		report.Drain.LimiterInUse == 0 &&
		report.Drain.LimiterWaiters == 0 &&
		report.Drain.TrackedConnectionGenerations == 0 &&
		report.Workers.LiveWorkers == 0 &&
		report.Workers.RetiringWorkers == 0 &&
		report.Responses.TerminalLogicalOutcomes == report.Responses.ScheduledLogicalInputs &&
		report.Responses.SuccessfulLogicalResponses+report.Responses.TerminalLogicalErrors == report.Responses.TerminalLogicalOutcomes &&
		ledgerComplete

	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("encode telemetry: %w", err)
	}
	encoded = append(encoded, '\n')
	path := j.Config.RunTelemetry
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create telemetry file: %w", err)
	}
	tempName := temp.Name()
	keep := false
	defer func() {
		_ = temp.Close()
		if !keep {
			_ = os.Remove(tempName)
		}
	}()
	if _, err = temp.Write(encoded); err != nil {
		return fmt.Errorf("write telemetry: %w", err)
	}
	if err = temp.Sync(); err != nil {
		return fmt.Errorf("sync telemetry: %w", err)
	}
	if err = temp.Close(); err != nil {
		return fmt.Errorf("close telemetry: %w", err)
	}
	if err = os.Rename(tempName, path); err != nil {
		return fmt.Errorf("publish telemetry: %w", err)
	}
	keep = true
	return nil
}
