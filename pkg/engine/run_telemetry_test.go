package engine

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

type retryTelemetryRunner struct {
	errors []error
	calls  atomic.Uint64
}

func (r *retryTelemetryRunner) Prepare(input map[string][]byte, base *ffuf.Request) (ffuf.Request, error) {
	req := ffuf.CopyRequest(base)
	req.Input = input
	return req, nil
}

func (r *retryTelemetryRunner) Execute(req *ffuf.Request) (ffuf.Response, error) {
	call := int(r.calls.Add(1) - 1)
	if call < len(r.errors) && r.errors[call] != nil {
		return ffuf.Response{}, r.errors[call]
	}
	return ffuf.Response{Request: req, StatusCode: 204}, nil
}

func (r *retryTelemetryRunner) Dump(*ffuf.Request) ([]byte, error) { return nil, nil }
func (r *retryTelemetryRunner) RunTelemetry() ffuf.RunTelemetry    { return ffuf.RunTelemetry{} }

type roleAwareRetryRunner struct {
	calls atomic.Uint64
}

func (r *roleAwareRetryRunner) Prepare(input map[string][]byte, base *ffuf.Request) (ffuf.Request, error) {
	req := ffuf.CopyRequest(base)
	req.Input = input
	return req, nil
}

func (r *roleAwareRetryRunner) Execute(*ffuf.Request) (ffuf.Response, error) {
	return ffuf.Response{}, errors.New("uninstrumented Execute called")
}

func (r *roleAwareRetryRunner) ExecuteWithRunTelemetry(req *ffuf.Request, role ffuf.RunTransportAttemptRole, recorder ffuf.RunTransportAttemptRecorder) (ffuf.Response, error) {
	call := r.calls.Add(1)
	recorder.RecordRunTransportAttempt(ffuf.RunTransportAttempt{
		Role: role, Method: "HEAD", Optimized: true, SurfacedError: true,
	})
	if call == 1 {
		return ffuf.Response{}, errors.New("initial outer attempt failed")
	}
	recorder.RecordRunTransportAttempt(ffuf.RunTransportAttempt{
		Role:   ffuf.RunTransportRoleEngineRetryOptimizationFallback,
		Method: "GET", Completed: true,
	})
	return ffuf.Response{Request: req, StatusCode: 204}, nil
}

func (r *roleAwareRetryRunner) Dump(*ffuf.Request) ([]byte, error) { return nil, nil }
func (r *roleAwareRetryRunner) RunTelemetry() ffuf.RunTelemetry {
	return ffuf.RunTelemetry{Drain: ffuf.RunDrainTelemetry{RunnerClosed: true}}
}

type telemetryRunner struct {
	report ffuf.RunTelemetry
}

func TestMainRequestSpanExcludesPreflightAndShutdown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.json")
	job := NewJob(&ffuf.Config{RunTelemetry: path})
	job.Runner = telemetryRunner{report: ffuf.RunTelemetry{
		Drain: ffuf.RunDrainTelemetry{RunnerClosed: true},
	}}
	var monotonicNS atomic.Int64
	job.telemetryClock = monotonicNS.Load

	// Artificial preflight elapsed before the first outer main Execute call.
	monotonicNS.Store(10_000)
	job.beginMainRequest()
	monotonicNS.Store(10_750)
	job.recordMainTerminalTimestamp()

	// Artificial shutdown elapsed after the last terminal callback.
	monotonicNS.Store(99_000)
	if err := job.writeRunTelemetry(true); err != nil {
		t.Fatalf("writeRunTelemetry: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read telemetry: %v", err)
	}
	var got ffuf.RunTelemetry
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode telemetry: %v", err)
	}
	if got.Responses.MainRequestSpanNS == nil || *got.Responses.MainRequestSpanNS != 750 {
		t.Fatalf("main request span = %v, want 750ns", got.Responses.MainRequestSpanNS)
	}
}

func TestEmptyRunHasNoMainRequestSpan(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.json")
	job := NewJob(&ffuf.Config{RunTelemetry: path})
	job.Runner = telemetryRunner{report: ffuf.RunTelemetry{
		Drain: ffuf.RunDrainTelemetry{RunnerClosed: true},
	}}
	if err := job.writeRunTelemetry(true); err != nil {
		t.Fatalf("writeRunTelemetry: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read telemetry: %v", err)
	}
	var got ffuf.RunTelemetry
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode telemetry: %v", err)
	}
	if got.Responses.MainRequestSpanNS != nil {
		t.Fatalf("empty run span = %d, want null", *got.Responses.MainRequestSpanNS)
	}
}

func TestOneInputRunHasPositiveMainRequestSpanWithoutSleeping(t *testing.T) {
	job := NewJob(&ffuf.Config{
		Context:        context.Background(),
		RunTelemetry:   filepath.Join(t.TempDir(), "run.json"),
		MatcherManager: &fakeMatcherManager{},
	})
	job.Output = NewNullOutput()
	job.Runner = &retryTelemetryRunner{}
	var monotonicNS atomic.Int64
	job.telemetryClock = func() int64 { return monotonicNS.Add(250) }
	job.initializeLogicalTelemetry(1)
	id := job.scheduleLogicalInput()

	job.runLogicalTask(
		jobContext{basereq: ffuf.Request{Method: "GET", Url: "https://example.test/FUZZ"}},
		map[string][]byte{"FUZZ": []byte("one")},
		1,
		id,
	)

	span := job.mainRequestSpanNS()
	if span == nil || *span != 250 {
		t.Fatalf("one-input main span = %v, want 250ns", span)
	}
}

func TestLogicalLedgerRejectsBalancedLostAndDuplicateTerminal(t *testing.T) {
	expected := uint64(2)
	ledger := newLogicalInputLedger(&expected)
	first := ledger.assignAndSchedule()
	second := ledger.assignAndSchedule()
	ledger.recordTerminal(first, logicalInputSuccess)
	ledger.recordTerminal(first, logicalInputError) // duplicate balances the lost second ID

	got := ledger.snapshot()
	if got.AssignedIDs != 2 || got.ScheduledTransitions != 2 {
		t.Fatalf("assignment telemetry = %+v", got)
	}
	if got.SuccessTerminalStates != 1 || got.ErrorTerminalStates != 0 || got.PendingScheduledStates != 1 {
		t.Fatalf("terminal telemetry = %+v", got)
	}
	if got.DuplicateTerminalTransitions != 1 || logicalLedgerComplete(got) {
		t.Fatalf("ledger should reject lost+duplicate balance: %+v", got)
	}
	if len(got.AnomalySampleIDs) != 2 || got.AnomalySampleIDs[0] != first || got.AnomalySampleIDs[1] != second {
		t.Fatalf("anomaly samples = %v, want [%d %d]", got.AnomalySampleIDs, first, second)
	}
}

func TestRunTelemetryRejectsProducerThatStopsBeforeExpectedCount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.json")
	job := NewJob(&ffuf.Config{RunTelemetry: path})
	job.Runner = telemetryRunner{report: ffuf.RunTelemetry{
		Drain: ffuf.RunDrainTelemetry{RunnerClosed: true},
	}}
	expected := uint64(3)
	job.telemetryLedger = newLogicalInputLedger(&expected)
	for range 2 {
		id := job.telemetryLedger.assignAndSchedule()
		job.telemetryLedger.recordTerminal(id, logicalInputSuccess)
	}
	atomic.StoreInt64(&job.telemetryScheduled, 2)
	atomic.StoreInt64(&job.telemetrySuccessful, 2)
	atomic.StoreInt64(&job.telemetryTerminalOutcomes, 2)

	if err := job.writeRunTelemetry(true); err != nil {
		t.Fatalf("writeRunTelemetry: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read telemetry: %v", err)
	}
	var got ffuf.RunTelemetry
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode telemetry: %v", err)
	}
	if got.LogicalLedger.ExpectedExpandedInputs == nil || *got.LogicalLedger.ExpectedExpandedInputs != 3 {
		t.Fatalf("expected count = %v, want 3", got.LogicalLedger.ExpectedExpandedInputs)
	}
	if got.LogicalLedger.AssignedIDs != 2 || got.LogicalLedger.IDGaps != 1 {
		t.Fatalf("logical ledger = %+v", got.LogicalLedger)
	}
	if got.Drain.Complete {
		t.Fatalf("short producer must make drain ineligible: %+v", got.Drain)
	}
}

func TestLogicalLedgerClassifiesScheduleAndTerminalAnomalies(t *testing.T) {
	expected := uint64(2)
	ledger := newLogicalInputLedger(&expected)
	first := ledger.assignAndSchedule()
	ledger.recordSchedule(first)
	ledger.recordTerminal(2, logicalInputSuccess)
	ledger.recordTerminal(3, logicalInputError)

	got := ledger.snapshot()
	if got.DuplicateScheduleTransitions != 1 || got.InvalidTransitions != 1 || got.OutOfRangeIDs != 1 {
		t.Fatalf("anomaly classification = %+v", got)
	}
	if len(got.AnomalySampleIDs) != 3 {
		t.Fatalf("anomaly samples = %v, want three IDs", got.AnomalySampleIDs)
	}
}

func TestLogicalLedgerConcurrentChunkGrowthAndTransitions(t *testing.T) {
	const inputCount = 10_003
	expected := uint64(inputCount)
	ledger := newLogicalInputLedger(&expected)
	var wg sync.WaitGroup
	var work atomic.Uint64
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				if work.Add(1) > inputCount {
					return
				}
				id := ledger.assignAndSchedule()
				ledger.recordTerminal(id, logicalInputSuccess)
			}
		}()
	}
	wg.Wait()

	got := ledger.snapshot()
	if got.AssignedIDs != inputCount || got.ScheduledTransitions != inputCount || got.SuccessTerminalStates != inputCount || !logicalLedgerComplete(got) {
		t.Fatalf("concurrent ledger = %+v", got)
	}
}

func TestLogicalLedgerCancellationReconcilesAssignedIDsAndReportsUnassignedGap(t *testing.T) {
	const (
		expectedCount = 10_003
		cancelAfter   = 4_097
	)
	expected := uint64(expectedCount)
	ledger := newLogicalInputLedger(&expected)
	ctx, cancel := context.WithCancel(context.Background())
	tasks := make(chan uint64, 64)
	reachedCancellationPoint := make(chan struct{})
	resumeProducer := make(chan struct{})

	var workers sync.WaitGroup
	for range 16 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for id := range tasks {
				ledger.recordTerminal(id, logicalInputSuccess)
			}
		}()
	}

	producerDone := make(chan struct{})
	go func() {
		defer close(producerDone)
		defer close(tasks)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			id := ledger.assignAndSchedule()
			tasks <- id
			if id == cancelAfter {
				close(reachedCancellationPoint)
				<-resumeProducer
			}
		}
	}()

	<-reachedCancellationPoint
	cancel()
	close(resumeProducer)
	<-producerDone
	workers.Wait()

	got := ledger.snapshot()
	if got.AssignedIDs != cancelAfter || got.ScheduledTransitions != cancelAfter || got.SuccessTerminalStates != cancelAfter {
		t.Fatalf("cancelled ledger transitions = %+v", got)
	}
	if got.PendingScheduledStates != 0 || got.IDGaps != expectedCount-cancelAfter || logicalLedgerComplete(got) {
		t.Fatalf("cancelled ledger reconciliation = %+v", got)
	}
}

func TestLogicalLedgerBoundsAnomalySamplesButKeepsExactCounts(t *testing.T) {
	expected := uint64(250)
	ledger := newLogicalInputLedger(&expected)
	for id := uint64(1); id <= 150; id++ {
		ledger.recordTerminal(id, logicalInputSuccess)
	}
	for id := uint64(251); id <= 300; id++ {
		ledger.recordTerminal(id, logicalInputError)
	}

	got := ledger.snapshot()
	if got.InvalidTransitions != 150 || got.OutOfRangeIDs != 50 {
		t.Fatalf("exact anomaly counts = %+v", got)
	}
	if len(got.AnomalySampleIDs) != logicalLedgerSampleLimit {
		t.Fatalf("anomaly sample count = %d, want %d", len(got.AnomalySampleIDs), logicalLedgerSampleLimit)
	}
}

func TestApplicationRetryRecordsOneTerminalLogicalOutcome(t *testing.T) {
	job := NewJob(&ffuf.Config{
		Context:        context.Background(),
		RunTelemetry:   filepath.Join(t.TempDir(), "run.json"),
		MatcherManager: &fakeMatcherManager{},
	})
	job.Output = NewNullOutput()
	runner := &retryTelemetryRunner{errors: []error{errors.New("first attempt failed"), nil}}
	job.Runner = runner
	job.initializeLogicalTelemetry(1)
	id := job.scheduleLogicalInput()

	job.runLogicalTask(jobContext{basereq: ffuf.Request{Method: "GET", Url: "https://example.test/FUZZ"}}, map[string][]byte{"FUZZ": []byte("one")}, 1, id)

	if runner.calls.Load() != 2 {
		t.Fatalf("runner calls = %d, want 2", runner.calls.Load())
	}
	if got := atomic.LoadInt64(&job.telemetryRetries); got != 1 {
		t.Fatalf("application retries = %d, want 1", got)
	}
	if got := atomic.LoadInt64(&job.telemetryTerminalOutcomes); got != 1 {
		t.Fatalf("terminal outcomes = %d, want 1", got)
	}
	if got := atomic.LoadInt64(&job.telemetrySuccessful); got != 1 {
		t.Fatalf("successful logical responses = %d, want 1", got)
	}
	if got := atomic.LoadInt64(&job.telemetryTerminalErrors); got != 0 {
		t.Fatalf("terminal logical errors = %d, want 0", got)
	}
	if got := job.telemetryLedger.snapshot(); !logicalLedgerComplete(got) || got.SuccessTerminalStates != 1 {
		t.Fatalf("logical ledger = %+v", got)
	}
}

func TestMixedSuccessAndFailureAccountForEveryScheduledInput(t *testing.T) {
	job := NewJob(&ffuf.Config{
		Context:        context.Background(),
		RunTelemetry:   filepath.Join(t.TempDir(), "run.json"),
		MatcherManager: &fakeMatcherManager{},
	})
	job.Output = NewNullOutput()
	runner := &retryTelemetryRunner{errors: []error{nil, errors.New("attempt failed"), errors.New("retry failed")}}
	job.Runner = runner
	job.initializeLogicalTelemetry(2)
	ctx := jobContext{basereq: ffuf.Request{Method: "GET", Url: "https://example.test/FUZZ"}}
	for position, word := range []string{"ok", "failed"} {
		id := job.scheduleLogicalInput()
		job.runLogicalTask(ctx, map[string][]byte{"FUZZ": []byte(word)}, position+1, id)
	}

	if scheduled := atomic.LoadInt64(&job.telemetryScheduled); scheduled != 2 {
		t.Fatalf("scheduled = %d, want 2", scheduled)
	}
	if successful := atomic.LoadInt64(&job.telemetrySuccessful); successful != 1 {
		t.Fatalf("successful = %d, want 1", successful)
	}
	if outcomes := atomic.LoadInt64(&job.telemetryTerminalOutcomes); outcomes != 2 {
		t.Fatalf("terminal outcomes = %d, want 2", outcomes)
	}
	if terminalErrors := atomic.LoadInt64(&job.telemetryTerminalErrors); terminalErrors != 1 {
		t.Fatalf("terminal errors = %d, want 1", terminalErrors)
	}
	ledger := job.telemetryLedger.snapshot()
	if !logicalLedgerComplete(ledger) || ledger.SuccessTerminalStates != 1 || ledger.ErrorTerminalStates != 1 {
		t.Fatalf("logical ledger = %+v", ledger)
	}
}

func TestMainTimingConcurrentFirstStartAndOutOfOrderTerminals(t *testing.T) {
	job := NewJob(&ffuf.Config{RunTelemetry: filepath.Join(t.TempDir(), "run.json")})
	job.Runner = telemetryRunner{}
	var clockCalls atomic.Int64
	job.telemetryClock = func() int64 { return clockCalls.Add(1) * 100 }

	var wg sync.WaitGroup
	for range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			job.beginMainRequest()
		}()
	}
	wg.Wait()
	if calls := clockCalls.Load(); calls != 1 {
		t.Fatalf("first-start clock calls = %d, want 1", calls)
	}
	if started := atomic.LoadInt64(&job.telemetryMainStartNS); started != 100 {
		t.Fatalf("main start = %d, want 100", started)
	}

	for _, terminal := range []int64{250, 700, 400, 1_100, 900, 300} {
		terminal := terminal
		wg.Add(1)
		go func() {
			defer wg.Done()
			job.recordMainTerminalAt(terminal)
		}()
	}
	wg.Wait()
	span := job.mainRequestSpanNS()
	if span == nil || *span != 1_000 {
		t.Fatalf("main span = %v, want 1000ns", span)
	}
}

func TestMainTransportAttemptsHaveExclusiveRolesAndMethods(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.json")
	job := NewJob(&ffuf.Config{
		Context:        context.Background(),
		RunTelemetry:   path,
		MatcherManager: &fakeMatcherManager{},
	})
	job.Output = NewNullOutput()
	job.Runner = &roleAwareRetryRunner{}
	job.initializeLogicalTelemetry(1)
	id := job.scheduleLogicalInput()
	job.runLogicalTask(jobContext{basereq: ffuf.Request{Method: "GET", Url: "https://example.test/FUZZ"}}, map[string][]byte{"FUZZ": []byte("one")}, 1, id)

	if err := job.writeRunTelemetry(true); err != nil {
		t.Fatalf("writeRunTelemetry: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read telemetry: %v", err)
	}
	var got ffuf.RunTelemetry
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode telemetry: %v", err)
	}
	transport := got.Transport
	if transport.ApplicationAttempts != 3 || transport.CompletedAttempts != 1 || transport.SurfacedAttemptErrors != 2 || transport.ApplicationRetries != 1 {
		t.Fatalf("main transport totals = %+v", transport)
	}
	if transport.OptimizedAttempts != 2 || transport.FullGETRecoveries != 1 {
		t.Fatalf("optimization totals = %+v", transport)
	}
	if transport.AttemptRoles.Initial != 1 || transport.AttemptRoles.EngineRetry != 1 || transport.AttemptRoles.EngineRetryOptimizationFallback != 1 || transport.AttemptRoles.OptimizationFallback != 0 {
		t.Fatalf("attempt roles = %+v", transport.AttemptRoles)
	}
	if transport.Methods.HEAD != 2 || transport.Methods.GET != 1 || transport.Methods.Other != 0 {
		t.Fatalf("attempt methods = %+v", transport.Methods)
	}
}

func TestPausedOrRateLimitedRunIsMarkedIneligible(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.json")
	job := NewJob(&ffuf.Config{RunTelemetry: path, Rate: 100})
	job.Output = NewNullOutput()
	job.Runner = telemetryRunner{report: ffuf.RunTelemetry{
		Drain: ffuf.RunDrainTelemetry{RunnerClosed: true},
	}}
	job.initializeLogicalTelemetry(0)
	job.setRunning(true)
	job.Pause()
	job.Resume()
	job.setRunning(false)

	if err := job.writeRunTelemetry(true); err != nil {
		t.Fatalf("writeRunTelemetry: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read telemetry: %v", err)
	}
	var got ffuf.RunTelemetry
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode telemetry: %v", err)
	}
	if got.Eligibility.Eligible {
		t.Fatalf("eligibility = %+v, want ineligible", got.Eligibility)
	}
	if !got.Eligibility.PauseObserved || !got.Eligibility.RateLimited {
		t.Fatalf("eligibility diagnostics = %+v", got.Eligibility)
	}
}

func (r telemetryRunner) Prepare(map[string][]byte, *ffuf.Request) (ffuf.Request, error) {
	return ffuf.Request{}, nil
}
func (r telemetryRunner) Execute(*ffuf.Request) (ffuf.Response, error) {
	return ffuf.Response{}, nil
}
func (r telemetryRunner) Dump(*ffuf.Request) ([]byte, error) { return nil, nil }
func (r telemetryRunner) RunTelemetry() ffuf.RunTelemetry    { return r.report }

func TestWriteRunTelemetryAccountsForResponsesAndDrain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.json")
	job := NewJob(&ffuf.Config{RunTelemetry: path})
	job.Runner = telemetryRunner{report: ffuf.RunTelemetry{
		SchemaVersion: 2,
		Protocol: ffuf.RunProtocolTelemetry{
			Selected:      "http3",
			HTTP3Selected: true,
		},
		Drain: ffuf.RunDrainTelemetry{RunnerClosed: true},
	}}
	atomic.StoreInt64(&job.telemetryScheduled, 10)
	atomic.StoreInt64(&job.telemetrySuccessful, 8)
	atomic.StoreInt64(&job.telemetryTerminalOutcomes, 9)
	atomic.StoreInt64(&job.telemetryTerminalErrors, 1)
	atomic.StoreInt64(&job.telemetryRetries, 2)

	if err := job.writeRunTelemetry(true); err != nil {
		t.Fatalf("writeRunTelemetry: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read telemetry: %v", err)
	}
	var got ffuf.RunTelemetry
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode telemetry: %v", err)
	}
	if got.Responses.ScheduledLogicalInputs != 10 ||
		got.Responses.SuccessfulLogicalResponses != 8 ||
		got.Responses.TerminalLogicalOutcomes != 9 ||
		got.Responses.TerminalLogicalErrors != 1 {
		t.Fatalf("response telemetry = %+v", got.Responses)
	}
	if got.Transport.ApplicationRetries != 2 {
		t.Fatalf("transport telemetry = %+v", got.Transport)
	}
	if !got.Drain.WorkersDrained || got.Drain.Complete {
		t.Fatalf("drain telemetry = %+v", got.Drain)
	}
}

func TestWriteRunTelemetryMergesCapacityProtocolAndFinalWorkerDrain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.json")
	job := NewJob(&ffuf.Config{RunTelemetry: path})
	job.Runner = telemetryRunner{report: ffuf.RunTelemetry{
		Workers: ffuf.RunWorkerCapacityTelemetry{
			StaleAcknowledgements:            3,
			LateAfterTimeoutAcknowledgements: 2,
			RepublishedProposals:             4,
			TimedOutProposals:                1,
			SupersededProposals:              5,
			MissingListenerProposals:         1,
		},
		Drain: ffuf.RunDrainTelemetry{RunnerClosed: true},
	}}
	job.recordWorkerSupervisorSnapshot(workerSupervisorSnapshot{
		ExecutionGeneration:       9,
		ProposalEpoch:             13,
		AcknowledgementGeneration: 9,
		AcknowledgementEpoch:      13,
		StaleProposals:            2,
		ProposedWorkers:           5_120,
		AcknowledgedLiveWorkers:   5_000,
		DesiredWorkers:            5_000,
		LiveWorkers:               0,
		RetiringWorkers:           0,
		PeakWorkers:               5_000,
		RemainingInputCap:         0,
		State:                     workerSupervisorStopped,
	})
	job.BeginRunTransportAttempt()
	job.BeginRunTransportAttempt()
	job.EndRunTransportAttempt()
	job.EndRunTransportAttempt()
	expected := uint64(0)
	job.telemetryLedger = newLogicalInputLedger(&expected)

	if err := job.writeRunTelemetry(true); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got ffuf.RunTelemetry
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	workers := got.Workers
	if workers.ExecutionGeneration != 9 || workers.ProposalEpoch != 13 ||
		workers.AcknowledgementGeneration != 9 || workers.AcknowledgementEpoch != 13 ||
		workers.StaleProposals != 2 || workers.StaleAcknowledgements != 3 ||
		workers.LateAfterTimeoutAcknowledgements != 2 || workers.RepublishedProposals != 4 ||
		workers.TimedOutProposals != 1 || workers.SupersededProposals != 5 ||
		workers.MissingListenerProposals != 1 || workers.ProposedWorkers != 5_120 ||
		workers.AcknowledgedLiveWorkers != 5_000 || workers.DesiredWorkers != 5_000 ||
		workers.LiveWorkers != 0 || workers.RetiringWorkers != 0 || workers.PeakWorkers != 5_000 ||
		workers.PeakMainInFlight != 2 || workers.RemainingInputCap != 0 ||
		workers.SupervisorState != string(workerSupervisorStopped) {
		t.Fatalf("worker capacity telemetry = %+v", workers)
	}
	if !got.Drain.Complete {
		t.Fatalf("final drain telemetry = %+v", got.Drain)
	}
}

func TestWriteRunTelemetryRequiresAllQUICGenerationsToDrain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.json")
	job := NewJob(&ffuf.Config{RunTelemetry: path})
	job.Runner = telemetryRunner{report: ffuf.RunTelemetry{
		Drain: ffuf.RunDrainTelemetry{
			RunnerClosed:                 true,
			TrackedConnectionGenerations: 1,
		},
	}}
	expected := uint64(0)
	job.telemetryLedger = newLogicalInputLedger(&expected)

	if err := job.writeRunTelemetry(true); err != nil {
		t.Fatalf("writeRunTelemetry: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got ffuf.RunTelemetry
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Drain.Complete {
		t.Fatalf("drain marked complete with a live QUIC generation: %+v", got.Drain)
	}
}

func TestWriteRunTelemetryMarksFullQlogRunsIneligible(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.json")
	job := NewJob(&ffuf.Config{
		RunTelemetry: path,
		QuicQlogDir:  t.TempDir(),
	})
	job.Runner = telemetryRunner{report: ffuf.RunTelemetry{
		Drain: ffuf.RunDrainTelemetry{RunnerClosed: true},
	}}
	expected := uint64(0)
	job.telemetryLedger = newLogicalInputLedger(&expected)

	if err := job.writeRunTelemetry(true); err != nil {
		t.Fatalf("writeRunTelemetry: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got ffuf.RunTelemetry
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Eligibility.Eligible || len(got.Eligibility.IneligibleReasons) != 1 ||
		got.Eligibility.IneligibleReasons[0] != "quic_qlog_enabled" {
		t.Fatalf("qlog eligibility = %+v", got.Eligibility)
	}
}

func TestProfileFlagsMarkRunTelemetryDiagnosticAndIneligible(t *testing.T) {
	for _, profileFlag := range []string{
		"profile-cpu",
		"profile-heap",
		"profile-mutex",
		"profile-block",
	} {
		t.Run(profileFlag, func(t *testing.T) {
			dir := t.TempDir()
			telemetryPath := filepath.Join(dir, "run.json")
			opts := ffuf.NewConfigOptions()
			fs := flag.NewFlagSet("h3fuzz", flag.ContinueOnError)
			ffuf.RegisterFlags(fs, opts)
			if err := fs.Parse([]string{
				"-u", "https://example.test/FUZZ",
				"-w", filepath.Join(dir, "words.txt"),
				"-run-telemetry", telemetryPath,
				"-" + profileFlag, filepath.Join(dir, profileFlag+".pprof"),
			}); err != nil {
				t.Fatalf("parse CLI flags: %v", err)
			}

			conf, err := ffuf.ConfigFromOptions(opts, context.Background(), func() {})
			if err != nil {
				t.Fatalf("ConfigFromOptions: %v", err)
			}
			job := NewJob(conf)
			job.Runner = telemetryRunner{report: ffuf.RunTelemetry{
				Drain: ffuf.RunDrainTelemetry{RunnerClosed: true},
			}}
			if err := job.writeRunTelemetry(true); err != nil {
				t.Fatalf("writeRunTelemetry: %v", err)
			}

			data, err := os.ReadFile(telemetryPath)
			if err != nil {
				t.Fatal(err)
			}
			var got ffuf.RunTelemetry
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			if got.EligibleMode != "diagnostic" {
				t.Fatalf("eligible_mode = %q, want diagnostic", got.EligibleMode)
			}
			if got.Eligibility.Eligible ||
				!slices.Contains(got.Eligibility.IneligibleReasons, "runtime_profiling_enabled") {
				t.Fatalf("profile eligibility = %+v", got.Eligibility)
			}
		})
	}
}
