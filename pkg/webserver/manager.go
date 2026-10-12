package webserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/assetnote/h3fuzz/pkg/assembly"
	"github.com/assetnote/h3fuzz/pkg/engine"
	"github.com/assetnote/h3fuzz/pkg/ffuf"
	"github.com/assetnote/h3fuzz/pkg/filter"
)

type Manager struct {
	store         *Store
	hub           *Hub
	allowInputCmd bool
	buildJob      func(*ffuf.Config) (*engine.Job, error)

	mu                 sync.Mutex
	active             map[string]*activeRun
	starting           map[string]chan struct{}
	shuttingDown       bool
	maintenance        bool
	maintenancePending bool
	// maintenanceGate drains mutating HTTP handlers before SQLite maintenance
	// starts, then prevents new mutations from racing VACUUM. The manager mutex
	// still protects the lifecycle flags and active-run maps.
	maintenanceGate sync.RWMutex
	seqMu           sync.Mutex
	seqByID         map[string]*sequenceState
}

type sequenceState struct {
	mu           sync.Mutex
	filterMu     sync.Mutex
	value        int64
	resultFilter *storedResultFilter
}

type activeRun struct {
	job                  *engine.Job
	pauseJob             func()
	resumeJob            func()
	addJobFilters        func(map[string]string) error
	output               *webOutput
	done                 chan struct{}
	running              chan struct{}
	stopRequested        atomic.Bool
	paused               atomic.Bool
	stateMu              sync.Mutex
	finished             bool
	progressMu           sync.Mutex
	lastCompletedCount   int
	lastCompletionChange time.Time
	stallAfter           time.Duration
	stallWarned          bool
	throughput           throughputTracker
	resultIndexReserved  bool
}

func (r *activeRun) pauseRuntime() {
	if r.pauseJob != nil {
		r.pauseJob()
		return
	}
	r.job.Pause()
}

func (r *activeRun) resumeRuntime() {
	if r.resumeJob != nil {
		r.resumeJob()
		return
	}
	r.job.Resume()
}

func (r *activeRun) addFiltersRuntime(filters map[string]string) error {
	if r.addJobFilters != nil {
		return r.addJobFilters(filters)
	}
	return r.job.AddFilters(filters)
}

// ErrMissingFuzzKeyword is returned when none of the configured input-provider
// keywords occur in the effective request. ConfigFromOptions supports FUZZ in
// the URL, method, headers or body, raw request files, and named wordlist
// keywords, so this check does not narrow ffuf's supported request shapes.
var ErrMissingFuzzKeyword = errors.New("no active fuzz keyword configured")

const zeroWorkError = "scan generated zero requests; check for a missing fuzz keyword or empty wordlist: add FUZZ to the target URL, a request header, or request body, and select a non-empty wordlist"

func NewManager(store *Store, hub *Hub, allowInputCmd bool) (*Manager, error) {
	m := &Manager{store: store, hub: hub, allowInputCmd: allowInputCmd, buildJob: assembly.BuildJob,
		active: make(map[string]*activeRun), starting: make(map[string]chan struct{}),
		seqByID: make(map[string]*sequenceState)}
	sessions, err := store.ListSessions(context.Background())
	if err != nil {
		return nil, err
	}
	for _, session := range sessions {
		resultFilter, filterErr := newStoredResultFilter(session.ResultFilters)
		if filterErr != nil {
			return nil, fmt.Errorf("load result filters for session %s: %w", session.ID, filterErr)
		}
		m.seqByID[session.ID] = &sequenceState{value: session.Sequence, resultFilter: resultFilter}
	}
	return m, nil
}

func (m *Manager) sequenceFor(sessionID string) *sequenceState {
	m.seqMu.Lock()
	seq := m.seqByID[sessionID]
	if seq == nil {
		seq = &sequenceState{}
		m.seqByID[sessionID] = seq
	}
	m.seqMu.Unlock()
	return seq
}

func (m *Manager) emit(sessionID, eventType string, data interface{}) int64 {
	state := m.sequenceFor(sessionID)
	state.mu.Lock()
	defer state.mu.Unlock()
	if eventType == "result" {
		filtered, ok := m.filterResultEvent(state.resultFilter, data)
		if !ok {
			return 0
		}
		data = filtered
	}
	sequence := state.value + 1
	if err := m.store.SetSequence(context.Background(), sessionID, sequence); err != nil {
		return 0
	}
	state.value = sequence
	m.hub.Broadcast(Event{Type: eventType, SessionID: sessionID, Sequence: sequence, Data: data, At: time.Now().UTC()})
	return sequence
}

// filterResultEvent closes the small gap between a result transaction and its
// later WebSocket broadcast. Holding the session sequence lock means a filter
// update is observed in one of two safe orders: either this result is emitted
// first and session.updated follows (prompting a UI reset), or the new filter is
// visible here and the stale result is suppressed.
func (m *Manager) filterResultEvent(resultFilter *storedResultFilter, data interface{}) (interface{}, bool) {
	payload, ok := data.(map[string]interface{})
	if !ok {
		return data, true
	}
	results, ok := payload["results"].([]StoredResult)
	if !ok {
		return data, true
	}
	if resultFilter == nil || len(resultFilter.providers) == 0 {
		return data, true
	}
	visible := make([]StoredResult, 0, len(results))
	for _, result := range results {
		keep, filterErr := resultFilter.keep(result)
		if filterErr != nil {
			return nil, false
		}
		if keep {
			visible = append(visible, result)
		}
	}
	if len(visible) == 0 {
		return nil, false
	}
	filtered := make(map[string]interface{}, len(payload))
	for key, value := range payload {
		filtered[key] = value
	}
	filtered["results"] = visible
	filtered["next_cursor"] = visible[len(visible)-1].ID
	return filtered, true
}

func (m *Manager) Create(ctx context.Context, request CreateSessionRequest) (Session, error) {
	settings, err := m.store.GetSettings(ctx)
	if err != nil {
		return Session{}, err
	}
	session, err := normalizeCreate(request, settings)
	if err != nil {
		return Session{}, err
	}
	if !m.allowInputCmd && len(session.Options.Input.Inputcommands) > 0 {
		return Session{}, fmt.Errorf("input-cmd is disabled in web mode; restart with -web-allow-input-cmd to enable it")
	}
	if err := validateWebFuzzOptions(ctx, &session.Options); err != nil {
		return Session{}, err
	}
	session.ID, err = newID()
	if err != nil {
		return Session{}, err
	}
	now := time.Now().UTC()
	session.CreatedAt, session.UpdatedAt = now, now
	if err := m.store.CreateSession(ctx, &session); err != nil {
		return Session{}, err
	}
	m.emitCurrent(ctx, session.ID, "session.created")
	// Return the persisted representation that was broadcast, including its
	// sequence. A sequence-zero response could otherwise overwrite the newer
	// session.created snapshot in a browser when HTTP and WebSocket delivery
	// cross in flight.
	session, err = m.store.GetSession(ctx, session.ID)
	if err != nil {
		return Session{}, err
	}
	if request.AutoStart {
		return m.startCreatedSession(ctx, session.ID)
	}
	return session, nil
}

// startCreatedSession treats creation and auto-start as one API operation. The
// session already exists by the time Start performs transport initialization,
// so returning only a start error would invite clients to retry POST and create
// a duplicate. Instead, make any pre-running failure explicit on that persisted
// session and return its authoritative snapshot.
func (m *Manager) startCreatedSession(ctx context.Context, id string) (Session, error) {
	startErr := m.Start(ctx, id)
	if startErr != nil {
		// Start records failures that happen after the starting transition. Early
		// failures, such as an invalid runtime configuration, still leave the
		// newly created session idle and need an explicit terminal explanation.
		persisted, loadErr := m.store.GetSession(context.Background(), id)
		if loadErr != nil {
			return Session{}, fmt.Errorf("auto-start failed (%v) and the persisted session could not be loaded: %w", startErr, loadErr)
		}
		if persisted.Status == StatusIdle || persisted.Status == StatusStarting ||
			(persisted.Status == StatusFailed && persisted.Error == "") {
			now := time.Now().UTC()
			if persistErr := m.store.UpdateSessionStatus(context.Background(), id, StatusFailed, startErr.Error(), nil, &now); persistErr != nil {
				return persisted, fmt.Errorf("auto-start failed (%v) and its persisted failure state could not be recorded: %w", startErr, persistErr)
			}
			m.emitCurrent(context.Background(), id, "session.updated")
		}
	}
	return m.store.GetSession(context.Background(), id)
}

func (m *Manager) Update(ctx context.Context, id string, patch PatchSessionRequest) (Session, error) {
	session, err := m.store.GetSession(ctx, id)
	if err != nil {
		return Session{}, err
	}
	if session.Status != StatusIdle {
		return Session{}, fmt.Errorf("session can only be edited while idle")
	}
	request := CreateSessionRequest{Name: session.Name, Target: session.Target, Wordlist: session.Wordlist,
		Profile: session.Profile, Options: &session.Options, ExplicitFlags: session.ExplicitFlags}
	if patch.Name != nil {
		request.Name = *patch.Name
	}
	if patch.Target != nil {
		request.Target = *patch.Target
	}
	if patch.Wordlist != nil {
		request.Wordlist = *patch.Wordlist
	}
	if patch.Profile != nil {
		request.Profile = *patch.Profile
		request.ExplicitFlags = uniqueStrings(append(request.ExplicitFlags, "D", "e", "mc", "fc", "H"))
		if patch.Options == nil {
			options := cloneOptions(session.Options)
			if profile, ok := profileByID(*patch.Profile); ok {
				applyProfile(&options, profile)
			}
			request.Options = &options
		}
	}
	if patch.Options != nil {
		request.Options = patch.Options
	}
	if patch.ExplicitFlags != nil {
		request.ExplicitFlags = *patch.ExplicitFlags
	}
	settings, err := m.store.GetSettings(ctx)
	if err != nil {
		return Session{}, err
	}
	updated, err := normalizeCreate(request, settings)
	if err != nil {
		return Session{}, err
	}
	if !m.allowInputCmd && len(updated.Options.Input.Inputcommands) > 0 {
		return Session{}, fmt.Errorf("input-cmd is disabled in web mode; restart with -web-allow-input-cmd to enable it")
	}
	if err := validateWebFuzzOptions(ctx, &updated.Options); err != nil {
		return Session{}, err
	}
	updated.ID, updated.CreatedAt, updated.UpdatedAt = session.ID, session.CreatedAt, time.Now().UTC()
	updated.Sequence, updated.ResultCount = session.Sequence, session.ResultCount
	if err := m.store.UpdateSessionConfig(ctx, updated); err != nil {
		return Session{}, err
	}
	m.emitCurrent(ctx, id, "session.updated")
	return m.store.GetSession(ctx, id)
}

func (m *Manager) Start(ctx context.Context, id string) error {
	m.mu.Lock()
	if m.shuttingDown {
		m.mu.Unlock()
		return fmt.Errorf("web session manager is shutting down")
	}
	if m.maintenance {
		m.mu.Unlock()
		return errStorageMaintenance
	}
	if _, exists := m.active[id]; exists {
		m.mu.Unlock()
		return fmt.Errorf("session is already active")
	}
	if _, exists := m.starting[id]; exists {
		m.mu.Unlock()
		return fmt.Errorf("session is already starting")
	}
	m.starting[id] = make(chan struct{})
	m.mu.Unlock()
	startReserved := true
	defer func() {
		if startReserved {
			m.finishStarting(id)
		}
	}()
	// Serialize the idle-config snapshot with additive filter edits. Without
	// this lock Start could build a job from the old options while AddFilters
	// persisted a newer configuration after observing the session as idle.
	filterState := m.sequenceFor(id)
	filterState.filterMu.Lock()
	defer filterState.filterMu.Unlock()

	session, err := m.store.GetSession(ctx, id)
	if err != nil {
		return err
	}
	if err := rejectWebProfilingOptions(&session.Options); err != nil {
		return err
	}
	if err := rejectWebProfilingFlags(session.ExplicitFlags); err != nil {
		return err
	}
	if session.Status != StatusIdle {
		return fmt.Errorf("only an idle session can be started")
	}
	if !m.allowInputCmd && len(session.Options.Input.Inputcommands) > 0 {
		return fmt.Errorf("input-cmd is disabled in web mode; restart with -web-allow-input-cmd to enable it")
	}
	runContext, cancel := context.WithCancel(context.Background())
	ffuf.MarkExplicitOptions(&session.Options, session.ExplicitFlags...)
	conf, err := ffuf.ConfigFromOptions(&session.Options, runContext, cancel)
	if err != nil {
		cancel()
		return err
	}
	if err := validateActiveFuzzInputs(conf); err != nil {
		cancel()
		return err
	}
	now := time.Now().UTC()
	if err := m.store.TransitionSessionStatus(ctx, id, StatusIdle, StatusStarting, "", &now); err != nil {
		cancel()
		return err
	}
	if err := m.store.UpdateSessionPreflight(ctx, id, PreflightReport{State: PreflightRunning, RanAt: &now}); err != nil {
		cancel()
		m.failStart(id, err)
		return err
	}
	m.emitCurrent(ctx, id, "session.updated")

	conf.Noninteractive = true
	conf.Cancel = cancel
	addStatusMatcher, threadsExplicit := webExecutionPolicy(session.ExplicitFlags)
	conf.ThreadsExplicit = threadsExplicit
	conf.MatcherManager, err = filter.FromConfig(&session.Options, addStatusMatcher)
	if err != nil {
		cancel()
		m.failStart(id, err)
		return err
	}
	job, buildErr := m.buildJob(conf)
	transport := effectiveRuntimeTransport(conf)
	if err := m.store.UpdateSessionRuntime(context.Background(), id, transport, conf.Http3Reason,
		conf.ResponseOptimizationStrategy, conf.ResponseOptimizationReason); err != nil {
		if job != nil {
			job.Stop()
		}
		cancel()
		m.failStart(id, err)
		return err
	}
	preflightState, preflightError := PreflightComplete, ""
	if buildErr != nil {
		preflightState, preflightError = PreflightFailed, buildErr.Error()
	}
	if err := m.store.UpdateSessionPreflight(context.Background(), id,
		preflightReportFromFFUF(conf.ProtocolPreflight, preflightState, preflightError)); err != nil {
		if job != nil {
			job.Stop()
		}
		cancel()
		m.failStart(id, err)
		return err
	}
	m.emitCurrent(context.Background(), id, "session.updated")
	if buildErr != nil {
		cancel()
		m.failStart(id, buildErr)
		return buildErr
	}
	if err := m.store.beginResultIngest(ctx); err != nil {
		job.Stop()
		cancel()
		m.failStart(id, err)
		return fmt.Errorf("prepare result persistence: %w", err)
	}

	var run *activeRun
	var output *webOutput
	output = newWebOutput(id, m.store,
		func(eventType string, data interface{}) { m.emit(id, eventType, data) },
		func(progress ffuf.Progress) { m.onProgress(id, progress, output.Stats()) })
	job.Output = output
	job.SetExternallyManaged(true)
	stallAfter := time.Duration(conf.Timeout)*time.Second + time.Second
	if stallAfter < 5*time.Second {
		stallAfter = 5 * time.Second
	}
	run = &activeRun{job: job, output: output, done: make(chan struct{}), running: make(chan struct{}),
		pauseJob: job.Pause, resumeJob: job.Resume, addJobFilters: job.AddFilters,
		lastCompletionChange: time.Now(), stallAfter: stallAfter, resultIndexReserved: true}
	output.fatal = func(error) { job.Stop() }
	output.Info(runtimeTransportLine(conf))
	output.Info(runtimeOptimizationLine(conf))

	m.mu.Lock()
	if m.shuttingDown {
		m.mu.Unlock()
		job.Stop()
		if job.AuditLogger != nil {
			job.AuditLogger.Close()
		}
		_ = output.Finalize()
		_ = m.store.endResultIngest(context.Background())
		err := fmt.Errorf("web session manager is shutting down")
		m.failStart(id, err)
		return err
	}
	if _, exists := m.active[id]; exists {
		m.mu.Unlock()
		job.Stop()
		if job.AuditLogger != nil {
			job.AuditLogger.Close()
		}
		_ = output.Finalize()
		_ = m.store.endResultIngest(context.Background())
		return fmt.Errorf("session is already active")
	}
	m.active[id] = run
	done := m.starting[id]
	delete(m.starting, id)
	close(done)
	startReserved = false
	m.mu.Unlock()

	go m.run(id, run)
	// Start is an accepted lifecycle transition, not merely goroutine
	// scheduling. Wait until the running state has been persisted and queued to
	// WebSocket subscribers so the action/create response cannot carry an older
	// starting snapshot that regresses the UI when the two transports cross.
	<-run.running
	return nil
}

// RerunPreflight repeats the exact bounded runner initialization used by Start
// without scheduling fuzz inputs. It is valid for any inactive session and
// persists both the in-progress and final report so REST and WebSocket clients
// observe one authoritative state transition.
func (m *Manager) RerunPreflight(ctx context.Context, id string) (Session, error) {
	m.mu.Lock()
	if m.shuttingDown {
		m.mu.Unlock()
		return Session{}, fmt.Errorf("web session manager is shutting down")
	}
	if m.maintenance {
		m.mu.Unlock()
		return Session{}, errStorageMaintenance
	}
	if _, exists := m.active[id]; exists {
		m.mu.Unlock()
		return Session{}, fmt.Errorf("preflight cannot run while the session is active")
	}
	if _, exists := m.starting[id]; exists {
		m.mu.Unlock()
		return Session{}, fmt.Errorf("session preparation is already in progress")
	}
	m.starting[id] = make(chan struct{})
	m.mu.Unlock()
	defer m.finishStarting(id)

	session, err := m.store.GetSession(ctx, id)
	if err != nil {
		return Session{}, err
	}
	if err := rejectWebProfilingOptions(&session.Options); err != nil {
		return Session{}, err
	}
	if err := rejectWebProfilingFlags(session.ExplicitFlags); err != nil {
		return Session{}, err
	}
	ranAt := time.Now().UTC()
	runningReport := PreflightReport{State: PreflightRunning, RanAt: &ranAt}
	if err := m.store.UpdateSessionPreflight(ctx, id, runningReport); err != nil {
		return Session{}, err
	}
	m.emitCurrent(ctx, id, "session.updated")

	runContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	ffuf.MarkExplicitOptions(&session.Options, session.ExplicitFlags...)
	conf, err := ffuf.ConfigFromOptions(&session.Options, runContext, cancel)
	if err == nil {
		err = validateActiveFuzzInputs(conf)
	}
	if err == nil {
		conf.Noninteractive = true
		conf.Cancel = cancel
		addStatusMatcher, threadsExplicit := webExecutionPolicy(session.ExplicitFlags)
		conf.ThreadsExplicit = threadsExplicit
		conf.MatcherManager, err = filter.FromConfig(&session.Options, addStatusMatcher)
	}
	var job *engine.Job
	if err == nil {
		job, err = m.buildJob(conf)
	}
	if job != nil {
		job.Stop()
		if job.AuditLogger != nil {
			job.AuditLogger.Close()
		}
	}
	state, errText := PreflightComplete, ""
	if err != nil {
		state, errText = PreflightFailed, err.Error()
	}
	var rawReport ffuf.ProtocolPreflightReport
	if conf != nil {
		rawReport = conf.ProtocolPreflight
	}
	report := preflightReportFromFFUF(rawReport, state, errText)
	if report.RanAt == nil {
		report.RanAt = &ranAt
	}
	if err := m.store.UpdateSessionPreflight(context.Background(), id, report); err != nil {
		return Session{}, err
	}
	if conf != nil {
		_ = m.store.UpdateSessionRuntime(context.Background(), id, effectiveRuntimeTransport(conf), conf.Http3Reason,
			conf.ResponseOptimizationStrategy, conf.ResponseOptimizationReason)
	}
	m.emitCurrent(context.Background(), id, "session.updated")
	updated, getErr := m.store.GetSession(context.Background(), id)
	if getErr != nil {
		return Session{}, getErr
	}
	// Probe/capability failures are report data, not an action failure. Returning
	// the failed report with 202 lets clients render its evidence immediately;
	// only lifecycle or persistence errors reject the action itself.
	return updated, nil
}

func webExecutionPolicy(explicitFlags []string) (addStatusMatcher, threadsExplicit bool) {
	explicit := make(map[string]bool, len(explicitFlags))
	for _, name := range explicitFlags {
		explicit[name] = true
	}
	matcherSet := explicit["mc"] || explicit["ms"] || explicit["ml"] || explicit["mw"] || explicit["mr"] || explicit["mt"]
	return explicit["mc"] || !matcherSet, explicit["t"]
}

func missingFuzzKeywordError() error {
	return fmt.Errorf("%w; add FUZZ to the target URL, a request header, or request body, or ensure each custom wordlist keyword is present in the configured request", ErrMissingFuzzKeyword)
}

func validateActiveFuzzInputs(conf *ffuf.Config) error {
	if conf == nil || len(conf.InputProviders) == 0 {
		return missingFuzzKeywordError()
	}
	return nil
}

func validateWebFuzzOptions(ctx context.Context, opts *ffuf.ConfigOptions) error {
	parseContext, cancel := context.WithCancel(ctx)
	defer cancel()
	conf, err := ffuf.ConfigFromOptions(opts, parseContext, cancel)
	if err != nil {
		return err
	}
	return validateActiveFuzzInputs(conf)
}

func runtimeTransportLine(conf *ffuf.Config) string {
	transport := "HTTP/1.1"
	switch effectiveRuntimeTransport(conf) {
	case RuntimeTransportHTTP3:
		transport = "HTTP/3"
	case RuntimeTransportHTTP2:
		transport = "HTTP/2"
	}
	if conf.Http3Reason != "" {
		return fmt.Sprintf("transport selected: %s (%s)", transport, conf.Http3Reason)
	}
	return "transport selected: " + transport
}

func effectiveRuntimeTransport(conf *ffuf.Config) string {
	if conf.Http3Selected {
		return RuntimeTransportHTTP3
	}
	if conf.Http2Selected {
		return RuntimeTransportHTTP2
	}
	return RuntimeTransportHTTP1
}

func runtimeOptimizationLine(conf *ffuf.Config) string {
	strategy := conf.ResponseOptimizationStrategy
	if strategy == "" {
		strategy = "GET"
	}
	if conf.ResponseOptimizationReason != "" {
		return fmt.Sprintf("response optimization: %s (%s)", strategy, conf.ResponseOptimizationReason)
	}
	return "response optimization: " + strategy
}

func (m *Manager) failStart(id string, startErr error) {
	now := time.Now().UTC()
	_ = m.store.UpdateSessionStatus(context.Background(), id, StatusFailed, startErr.Error(), nil, &now)
	m.emitCurrent(context.Background(), id, "session.updated")
}

func (m *Manager) run(id string, run *activeRun) {
	defer close(run.done)
	_ = m.store.UpdateSessionStatus(context.Background(), id, StatusRunning, "", nil, nil)
	m.emitCurrent(context.Background(), id, "session.updated")
	close(run.running)
	run.job.Start()
	run.stateMu.Lock()
	run.finished = true
	run.stateMu.Unlock()
	snapshot := run.job.Snapshot()
	m.onProgress(id, snapshot.Progress, run.output.Stats())
	if run.job.AuditLogger != nil {
		run.job.AuditLogger.Close()
	}

	status := StatusCompleted
	errText := ""
	if run.stopRequested.Load() {
		status = StatusStopped
	} else if snapshot.Error != "" {
		status, errText = StatusFailed, snapshot.Error
	} else if snapshot.Progress.ReqCount == 0 && !run.output.SawRequests() {
		status, errText = StatusFailed, zeroWorkError
	}
	if err := run.output.Finalize(); err != nil {
		status, errText = StatusFailed, err.Error()
	}
	if run.resultIndexReserved {
		if err := m.store.endResultIngest(context.Background()); err != nil {
			m.appendTerminalEntry(id, "warning", "result browsing indexes could not be restored: "+err.Error())
		}
		run.resultIndexReserved = false
	}
	m.appendTerminal(id, status, errText, snapshot.Progress.ReqCount)
	now := time.Now().UTC()
	_ = m.store.UpdateSessionStatus(context.Background(), id, status, errText, nil, &now)
	m.emitCurrent(context.Background(), id, "session.updated")
	m.mu.Lock()
	delete(m.active, id)
	m.mu.Unlock()
}

func (m *Manager) appendTerminal(id string, status SessionStatus, errText string, requestCount int) {
	level := "info"
	message := fmt.Sprintf("scan completed: %d requests", requestCount)
	switch status {
	case StatusStopped:
		level, message = "warning", fmt.Sprintf("scan stopped after %d requests", requestCount)
	case StatusFailed:
		level, message = "error", "scan failed: "+errText
	}
	m.appendTerminalEntry(id, level, message)
}

func (m *Manager) appendTerminalEntry(id, level, message string) {
	_, entries, err := m.store.InsertResultBatch(context.Background(), id, nil, []TerminalEntry{{
		At: time.Now().UTC(), Level: level, Message: message,
	}})
	if err != nil || len(entries) == 0 {
		return
	}
	m.emit(id, "terminal", map[string]interface{}{
		"entries": entries, "next_cursor": entries[len(entries)-1].ID,
	})
}

func (m *Manager) onProgress(id string, raw ffuf.Progress, outputStats ...webOutputStats) {
	progress := progressFromFFUF(raw)
	now := time.Now()
	stats := webOutputStats{}
	if len(outputStats) > 0 {
		stats = outputStats[0]
		progress.ResultsQueued = stats.ResultsQueued
		progress.PersistenceBackpressure = stats.PersistenceBackpressure
		progress.LastBatchDurationMS = stats.LastBatchDurationMS
		progress.ErrorDiagnostics = stats.ErrorDiagnostics
	}
	m.mu.Lock()
	run := m.active[id]
	m.mu.Unlock()
	if run != nil {
		run.throughput.enrich(&progress, now)
	} else {
		tracker := throughputTracker{}
		tracker.enrich(&progress, now)
	}
	warn, recovered := false, false
	if run != nil && run.job.Snapshot().Running && !run.paused.Load() && !run.stopRequested.Load() {
		progress.StallReason, progress.StalledForMS, warn, recovered = run.observeProgress(raw.CompletedCount, stats, now)
	}
	state := m.sequenceFor(id)
	state.mu.Lock()
	defer state.mu.Unlock()
	sequence := state.value + 1
	if err := m.store.UpdateProgressTelemetry(context.Background(), id, progress, stats.ErrorDiagnostics, sequence); err != nil {
		return
	}
	state.value = sequence
	m.hub.Broadcast(Event{Type: "progress", SessionID: id, Sequence: sequence, Data: progress, At: time.Now().UTC()})
	if run != nil && warn {
		run.output.Warning(stallWarningMessage(progress.StallReason,
			time.Duration(progress.StalledForMS)*time.Millisecond))
	} else if run != nil && recovered {
		run.output.Info("request completion resumed after the inactivity window")
	}
}

func stallWarningMessage(reason string, duration time.Duration) string {
	if reason == "result persistence backpressure" {
		return fmt.Sprintf("%s for %s; SQLite is persisting matched results more slowly than they are produced; narrow matchers/filters or use faster local storage",
			reason, duration)
	}
	return fmt.Sprintf("%s for %s; target or intermediary may be throttling or holding responses; consider setting a lower rate or concurrency",
		reason, duration)
}

func (run *activeRun) observeProgress(completedCount int, stats webOutputStats, now time.Time) (reason string, stalledMS int64, warn, recovered bool) {
	run.progressMu.Lock()
	defer run.progressMu.Unlock()
	if run.lastCompletionChange.IsZero() {
		run.lastCompletionChange = now
		run.lastCompletedCount = completedCount
		return "", 0, false, false
	}
	if completedCount != run.lastCompletedCount {
		recovered = run.stallWarned
		run.lastCompletedCount = completedCount
		run.lastCompletionChange = now
		run.stallWarned = false
		return "", 0, false, recovered
	}
	stalledFor := now.Sub(run.lastCompletionChange)
	if stalledFor < run.stallAfter {
		return "", 0, false, false
	}
	if stats.PersistenceBackpressure {
		reason = "result persistence backpressure"
	} else {
		reason = "no completed requests while result persistence has capacity"
	}
	warn = !run.stallWarned
	run.stallWarned = true
	return reason, stalledFor.Milliseconds(), warn, false
}

func (run *activeRun) resetProgressWatchdog(completedCount int, now time.Time) {
	run.progressMu.Lock()
	run.lastCompletedCount = completedCount
	run.lastCompletionChange = now
	run.stallWarned = false
	run.progressMu.Unlock()
}

func (m *Manager) emitCurrent(ctx context.Context, id, eventType string) {
	state := m.sequenceFor(id)
	state.mu.Lock()
	defer state.mu.Unlock()
	if session, err := m.store.GetSession(ctx, id); err == nil {
		sequence := state.value + 1
		session.Sequence = sequence
		if err := m.store.SetSequence(ctx, id, sequence); err != nil {
			return
		}
		state.value = sequence
		m.hub.Broadcast(Event{Type: eventType, SessionID: id, Sequence: sequence, Data: session, At: time.Now().UTC()})
	}
}

func (m *Manager) Pause(ctx context.Context, id string) error {
	run, err := m.activeRun(id)
	if err != nil {
		return err
	}
	run.stateMu.Lock()
	defer run.stateMu.Unlock()
	if run.finished {
		return fmt.Errorf("session is no longer active")
	}
	session, err := m.store.GetSession(ctx, id)
	if err != nil {
		return err
	}
	if session.Status != StatusRunning {
		return fmt.Errorf("only a running session can be paused")
	}
	run.pauseRuntime()
	run.paused.Store(true)
	if err := m.store.UpdateSessionStatus(ctx, id, StatusPaused, "", nil, nil); err != nil {
		// The runtime transition happened first so no persisted paused snapshot
		// can race ahead of the pause gate. If persistence rejects it, restore
		// the runtime to the still-authoritative running state before returning.
		run.resumeRuntime()
		run.paused.Store(false)
		return err
	}
	m.emitCurrent(ctx, id, "session.updated")
	return nil
}

func (m *Manager) Resume(ctx context.Context, id string) error {
	run, err := m.activeRun(id)
	if err != nil {
		return err
	}
	run.stateMu.Lock()
	defer run.stateMu.Unlock()
	if run.finished {
		return fmt.Errorf("session is no longer active")
	}
	session, err := m.store.GetSession(ctx, id)
	if err != nil {
		return err
	}
	if session.Status != StatusPaused {
		return fmt.Errorf("only a paused session can be resumed")
	}
	run.resumeRuntime()
	if err := m.store.UpdateSessionStatus(ctx, id, StatusRunning, "", nil, nil); err != nil {
		// Resume is also reversible while stateMu excludes competing lifecycle
		// actions. Re-close the gate so runtime and the persisted paused state do
		// not diverge on a database failure.
		run.pauseRuntime()
		run.paused.Store(true)
		return err
	}
	// A pause is intentional inactivity, not a transport stall. Keep the manager
	// pause flag set until persistence succeeds so progress callbacks cannot
	// diagnose the paused interval, then start a fresh completion window from the
	// runtime's current terminal-work count.
	run.resetProgressWatchdog(run.job.Snapshot().Progress.CompletedCount, time.Now())
	run.paused.Store(false)
	m.emitCurrent(ctx, id, "session.updated")
	return nil
}

func (m *Manager) Stop(ctx context.Context, id string) error {
	_, err := m.StopSnapshot(ctx, id)
	return err
}

// StopSnapshot persists and broadcasts the intermediate stopping state before
// canceling transport work. Returning the captured snapshot lets the REST
// action acknowledge the operator immediately without racing the final stopped
// transition performed by run.
func (m *Manager) StopSnapshot(ctx context.Context, id string) (Session, error) {
	run, err := m.activeRun(id)
	if err != nil {
		return Session{}, err
	}
	run.stateMu.Lock()
	defer run.stateMu.Unlock()
	if run.finished {
		return Session{}, fmt.Errorf("session is no longer active")
	}
	run.stopRequested.Store(true)
	if err := m.store.UpdateSessionStatus(ctx, id, StatusStopping, "", nil, nil); err != nil {
		go run.job.Stop()
		return Session{}, err
	}
	m.emitCurrent(ctx, id, "session.updated")
	session, err := m.store.GetSession(ctx, id)
	go run.job.Stop()
	if err != nil {
		return Session{}, err
	}
	return session, nil
}

func (m *Manager) activeRun(id string) (*activeRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	run := m.active[id]
	if run == nil {
		return nil, fmt.Errorf("session is not active")
	}
	return run, nil
}

func (m *Manager) finishStarting(id string) {
	m.mu.Lock()
	if done, exists := m.starting[id]; exists {
		delete(m.starting, id)
		close(done)
	}
	m.mu.Unlock()
}

func (m *Manager) beginShutdown() {
	m.mu.Lock()
	m.shuttingDown = true
	m.mu.Unlock()
}

func (m *Manager) Delete(ctx context.Context, id string) error {
	m.mu.Lock()
	_, active := m.active[id]
	_, starting := m.starting[id]
	m.mu.Unlock()
	if active || starting {
		return fmt.Errorf("stop the session before deleting it")
	}
	state := m.sequenceFor(id)
	state.mu.Lock()
	sequence := state.value + 1
	if err := m.store.DeleteSession(ctx, id); err != nil {
		state.mu.Unlock()
		return err
	}
	state.value = sequence
	m.hub.Broadcast(Event{Type: "session.deleted", SessionID: id, Sequence: sequence,
		Data: map[string]string{"id": id}, At: time.Now().UTC()})
	state.mu.Unlock()
	m.seqMu.Lock()
	delete(m.seqByID, id)
	m.seqMu.Unlock()
	return nil
}

func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	m.shuttingDown = true
	runs := make([]*activeRun, 0, len(m.active))
	for _, run := range m.active {
		runs = append(runs, run)
	}
	starts := make([]<-chan struct{}, 0, len(m.starting))
	for _, done := range m.starting {
		starts = append(starts, done)
	}
	m.mu.Unlock()
	for _, run := range runs {
		run.stateMu.Lock()
		if !run.finished {
			run.stopRequested.Store(true)
			run.job.Stop()
		}
		run.stateMu.Unlock()
	}
	for _, done := range starts {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	for _, run := range runs {
		select {
		case <-run.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func newID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	return fmt.Sprintf("%s-%s-%s-%s-%s", hex.EncodeToString(raw[0:4]), hex.EncodeToString(raw[4:6]),
		hex.EncodeToString(raw[6:8]), hex.EncodeToString(raw[8:10]), hex.EncodeToString(raw[10:16])), nil
}

func isNotFound(err error) bool { return errors.Is(err, ErrNotFound) }
