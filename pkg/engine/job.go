package engine

import (
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

// Job ties together Config, Runner, Input and Output
type Job struct {
	// 64-bit atomic counters. These MUST stay first in the struct so they are
	// 8-byte aligned on 32-bit architectures, which sync/atomic requires. Access
	// only through the helper methods below, never directly.
	counter                      int64
	completedCounter             int64
	responseCounter              int64
	errorCounter                 int64
	spuriousErrorCounter         int64
	count403                     int64
	count429                     int64
	telemetryScheduled           int64
	telemetryRetries             int64
	telemetrySuccessful          int64
	telemetryTerminalOutcomes    int64
	telemetryTerminalErrors      int64
	telemetryMainStartNS         int64
	telemetryMaxTerminalNS       int64
	telemetryApplicationAttempts int64
	telemetryCompletedAttempts   int64
	telemetrySurfacedErrors      int64
	telemetryOptimizedAttempts   int64
	telemetryFullGETRecoveries   int64
	telemetryRoleInitial         int64
	telemetryRoleFallback        int64
	telemetryRoleRetry           int64
	telemetryRoleRetryFallback   int64
	telemetryMethodGET           int64
	telemetryMethodHEAD          int64
	telemetryMethodOther         int64
	telemetryCurrentMainInFlight int64
	telemetryPeakMainInFlight    int64
	executionGeneration          uint64

	// 32-bit atomic flags (0 = false, 1 = true). Access only through the helpers.
	running                int32
	runningJob             int32
	skipQueue              int32
	stopped                int32
	telemetryPauseObserved int32
	// externallyManaged is set by embedders such as the web console. Managed
	// jobs do not install process-wide signal handlers and must honor the pause
	// gate even when terminal interaction is disabled with Noninteractive.
	externallyManaged int32

	AuditLogger  ffuf.AuditLogger
	Config       *ffuf.Config
	Input        ffuf.InputProvider
	Runner       ffuf.RunnerProvider
	ReplayRunner ffuf.RunnerProvider
	Scraper      ffuf.Scraper
	Output       ffuf.OutputProvider
	Jobhash      string
	Total        int
	Rate         *RateThrottle

	queue     *jobQueue
	recursion *recursionManager

	// autocalibrationEnabled is an immutable construction-time snapshot. The
	// response hot path uses it before parsing the request URL; CalibrateIfNeeded
	// still checks Config for callers outside that path.
	autocalibrationEnabled bool
	autocalibrationHost    func(ffuf.Request) string

	startTime    time.Time
	startTimeJob time.Time
	errorMsg     string
	paused       bool // guarded by pauseStateMutex

	timeMutex          sync.Mutex   // guards startTime and startTimeJob
	errMutex           sync.Mutex   // guards errorMsg
	inputMutex         sync.Mutex   // serializes main-loop input iteration vs interactive restart Reset
	calibMutex         sync.Mutex   // serializes autocalibration
	pauseStateMutex    sync.Mutex   // makes the pause-flag flip and the pauseMutex Lock/Unlock one atomic step
	pauseMutex         sync.RWMutex // pause gate: workers RLock as a speed bump, Pause takes the write Lock
	stopOnce           sync.Once    // makes cancellation and transport teardown idempotent
	telemetryStartOnce sync.Once
	telemetryClock     func() int64
	telemetryLedger    *logicalInputLedger
	responseLatency    *responseLatencyHistogram
	workerTelemetryMu  sync.Mutex
	workerTelemetry    ffuf.RunWorkerCapacityTelemetry
}

type QueueJob struct {
	Url   string
	depth int
	req   ffuf.Request
}

// JobSnapshot is an immutable, race-safe view of a Job's lifecycle and
// progress. It is intended for embedders that need to render status while the
// job is running without reaching into Job's mutable internals.
type JobSnapshot struct {
	Running      bool          `json:"running"`
	RunningJob   bool          `json:"running_job"`
	Paused       bool          `json:"paused"`
	Progress     ffuf.Progress `json:"progress"`
	Error        string        `json:"error,omitempty"`
	StartedAt    time.Time     `json:"started_at"`
	StartedAtJob time.Time     `json:"started_at_job"`
}

// jobContext carries the per-queue-job values a worker needs, passed BY VALUE so
// no worker reads mutable Job/Config state during execution. basereq is the
// immutable base request for the job; depth is its recursion depth. This is what
// makes the recursion-depth and base-request reads correct by construction rather
// than by drain timing.
type jobContext struct {
	basereq ffuf.Request
	depth   int
}

// requestDiagnosticOutput is an optional extension used by embedded outputs.
// It keeps per-request diagnostics out of the stable OutputProvider interface,
// so existing CLI and third-party outputs remain source compatible.
type requestDiagnosticOutput interface {
	RecordRequestError(error)
	RecordResponseStatus(int64)
}

func NewJob(conf *ffuf.Config) *Job {
	var j Job
	j.Config = conf
	j.queue = newJobQueue()
	j.Rate = NewRateThrottle(conf)
	j.autocalibrationEnabled = conf.AutoCalibration
	j.autocalibrationHost = ffuf.HostURLFromRequest
	if conf.RunTelemetry != "" {
		j.responseLatency = newResponseLatencyHistogram()
	}
	// Let the runner meter preflight/postflight requests against the same rate
	// limiter as the main dispatch loop, so -rate/-p bound total outgoing volume
	// rather than only the fuzzing requests.
	conf.RateLimitFunc = j.Rate.Wait
	return &j
}

// The per-run counters and run-state flags are mutated by the execution loop and
// the worker goroutines while the progress goroutine and the interrupt handler
// read them concurrently. Counters use sync/atomic (int64), flags use atomic
// int32, so there is no mutex to forget and no lock-ordering to get wrong. The
// fields are private: the only way to touch them is through these helpers.

func (j *Job) incCounter()      { atomic.AddInt64(&j.counter, 1) }
func (j *Job) setCounter(n int) { atomic.StoreInt64(&j.counter, int64(n)) }
func (j *Job) getCounter() int  { return int(atomic.LoadInt64(&j.counter)) }

func (j *Job) incCompletedCounter() { atomic.AddInt64(&j.completedCounter, 1) }
func (j *Job) getCompletedCounter() int {
	return int(atomic.LoadInt64(&j.completedCounter))
}

func (j *Job) incResponseCounter() { atomic.AddInt64(&j.responseCounter, 1) }
func (j *Job) getResponseCounter() int {
	return int(atomic.LoadInt64(&j.responseCounter))
}

func (j *Job) getErrorCounter() int { return int(atomic.LoadInt64(&j.errorCounter)) }
func (j *Job) getSpuriousErrorCounter() int {
	return int(atomic.LoadInt64(&j.spuriousErrorCounter))
}
func (j *Job) getCount403() int { return int(atomic.LoadInt64(&j.count403)) }
func (j *Job) getCount429() int { return int(atomic.LoadInt64(&j.count429)) }

func boolToInt32(b bool) int32 {
	if b {
		return 1
	}
	return 0
}

func (j *Job) setRunning(v bool)    { atomic.StoreInt32(&j.running, boolToInt32(v)) }
func (j *Job) isRunning() bool      { return atomic.LoadInt32(&j.running) == 1 }
func (j *Job) setRunningJob(v bool) { atomic.StoreInt32(&j.runningJob, boolToInt32(v)) }
func (j *Job) isRunningJob() bool   { return atomic.LoadInt32(&j.runningJob) == 1 }
func (j *Job) setSkipQueue(v bool)  { atomic.StoreInt32(&j.skipQueue, boolToInt32(v)) }
func (j *Job) isSkipQueue() bool    { return atomic.LoadInt32(&j.skipQueue) == 1 }
func (j *Job) isStopped() bool      { return atomic.LoadInt32(&j.stopped) == 1 }

// SetExternallyManaged configures lifecycle ownership for an embedded Job.
// Call it before Start. Managed jobs leave process signal handling to their
// host and retain Pause/Resume support when Config.Noninteractive is true.
func (j *Job) SetExternallyManaged(managed bool) {
	atomic.StoreInt32(&j.externallyManaged, boolToInt32(managed))
}

func (j *Job) isExternallyManaged() bool {
	return atomic.LoadInt32(&j.externallyManaged) == 1
}

// Snapshot returns a point-in-time view of lifecycle state and progress. All
// mutable fields are read through the same atomics and locks used by the engine.
func (j *Job) Snapshot() JobSnapshot {
	j.pauseStateMutex.Lock()
	paused := j.paused
	j.pauseStateMutex.Unlock()

	j.timeMutex.Lock()
	startedAt := j.startTime
	startedAtJob := j.startTimeJob
	j.timeMutex.Unlock()

	reqTotal := 0
	if j.Input != nil {
		reqTotal = j.Input.Total()
	}
	queuePos, queueTotal := 0, 0
	if j.queue != nil {
		queuePos = j.queue.position()
		queueTotal = j.queue.total()
	}
	reqSec := int64(0)
	if j.Rate != nil {
		reqSec = j.Rate.CurrentRate()
	}

	return JobSnapshot{
		Running:      j.isRunning(),
		RunningJob:   j.isRunningJob(),
		Paused:       paused,
		Error:        j.getError(),
		StartedAt:    startedAt,
		StartedAtJob: startedAtJob,
		Progress: ffuf.Progress{
			StartedAt:      startedAtJob,
			ReqCount:       j.getCounter(),
			CompletedCount: j.getCompletedCounter(),
			ResponseCount:  j.getResponseCounter(),
			ReqTotal:       reqTotal,
			ReqSec:         reqSec,
			QueuePos:       queuePos,
			QueueTotal:     queueTotal,
			ErrorCount:     j.getErrorCounter(),
		},
	}
}

func (j *Job) setError(s string) { j.errMutex.Lock(); j.errorMsg = s; j.errMutex.Unlock() }
func (j *Job) getError() string  { j.errMutex.Lock(); defer j.errMutex.Unlock(); return j.errorMsg }

func (j *Job) setStartTime(t time.Time) { j.timeMutex.Lock(); j.startTime = t; j.timeMutex.Unlock() }
func (j *Job) getStartTime() time.Time {
	j.timeMutex.Lock()
	defer j.timeMutex.Unlock()
	return j.startTime
}
func (j *Job) setStartTimeJob(t time.Time) {
	j.timeMutex.Lock()
	j.startTimeJob = t
	j.timeMutex.Unlock()
}
func (j *Job) getStartTimeJob() time.Time {
	j.timeMutex.Lock()
	defer j.timeMutex.Unlock()
	return j.startTimeJob
}

// incError increments the error counter
func (j *Job) incError() {
	atomic.AddInt64(&j.errorCounter, 1)
	atomic.AddInt64(&j.spuriousErrorCounter, 1)
}

// inc403 increments the 403 response counter
func (j *Job) inc403() { atomic.AddInt64(&j.count403, 1) }

// inc429 increments the 429 response counter
func (j *Job) inc429() { atomic.AddInt64(&j.count429, 1) }

func (j *Job) telemetryEnabled() bool {
	return j.Config != nil && j.Config.RunTelemetry != ""
}

func (j *Job) incTelemetryRetry() {
	if j.telemetryEnabled() {
		atomic.AddInt64(&j.telemetryRetries, 1)
	}
}

// resetSpuriousErrors resets the spurious error counter
func (j *Job) resetSpuriousErrors() { atomic.StoreInt64(&j.spuriousErrorCounter, 0) }

// DeleteQueueItem deletes a recursion job from the queue by its index in the slice
func (j *Job) DeleteQueueItem(index int) {
	j.queue.removeAt(index)
}

// QueuedJobs returns the slice of queued recursive jobs
func (j *Job) QueuedJobs() []QueueJob {
	return j.queue.remaining()
}

// Start the execution of the Job
func (j *Job) Start() {
	if j.isStopped() {
		return
	}
	workersDrained := false
	if j.getStartTime().IsZero() {
		j.setStartTime(time.Now())
	}
	// Output is wired by the assembly before Start, so the recursion manager can
	// capture it here.
	j.recursion = newRecursionManager(j.Config, j.queue, j.Output)

	basereq := ffuf.BaseRequest(j.Config)

	if j.Config.InputMode == "sniper" {
		// process multiple payload locations and create a queue job for each location
		reqs := ffuf.SniperRequests(&basereq, j.Config.InputProviders[0].Template)
		for _, r := range reqs {
			j.queue.push(QueueJob{Url: j.Config.Url, depth: 0, req: r})
		}
		j.Total = j.Input.Total() * len(reqs)
	} else {
		// Add the default job to job queue
		j.queue.push(QueueJob{Url: j.Config.Url, depth: 0, req: ffuf.BaseRequest(j.Config)})
		j.Total = j.Input.Total()
	}
	j.initializeLogicalTelemetry(j.Total)

	defer func() {
		j.Stop()
		if err := j.writeRunTelemetry(workersDrained); err != nil && j.Output != nil {
			j.Output.Error(fmt.Sprintf("Could not write run telemetry: %s\n", err))
		}
	}()

	j.setRunning(true)
	// Stop may have raced the setup above. Never let Start resurrect a job whose
	// context and transports have already been torn down.
	if j.isStopped() {
		j.setRunning(false)
		return
	}
	j.setRunningJob(true)
	//Show banner if not running in silent mode
	if !j.Config.Quiet {
		j.Output.Banner()
	}
	// Monitor for SIGTERM and do cleanup properly (writing the output files etc)
	stopInterruptMonitor := j.interruptMonitor()
	defer stopInterruptMonitor()
	for j.jobsInQueue() {
		ctx := j.prepareQueueJob()
		j.Reset(true)
		j.setRunningJob(true)
		j.startExecution(ctx)
	}

	err := j.Output.Finalize()
	if err != nil {
		j.Output.Error(err.Error())
	}
	workersDrained = true
}

// Reset resets the counters and wordlist position for a job
func (j *Job) Reset(cycle bool) {
	// inputMutex serializes this against the main loop's Input iteration, so an
	// interactive "restart" that calls Reset while workers are live cannot race
	// the input provider's internal position.
	j.inputMutex.Lock()
	j.Input.Reset()
	j.inputMutex.Unlock()
	j.setCounter(0)
	atomic.StoreInt64(&j.completedCounter, 0)
	atomic.StoreInt64(&j.responseCounter, 0)
	j.setSkipQueue(false)
	j.setStartTimeJob(time.Now())
	if cycle {
		j.Output.Cycle()
	} else {
		j.Output.Reset()
	}
}

func (j *Job) jobsInQueue() bool {
	return j.queue.hasNext()
}

func (j *Job) prepareQueueJob() jobContext {
	job, _ := j.queue.advance()
	// Config.Url is set here on the MAIN goroutine only, so WriteHistoryEntry below
	// (FFUFHASH) and the queued-job banner serialize the current target. Workers
	// never read it; they use the immutable jobContext returned from here.
	j.Config.Url = job.Url

	//Find all keywords present in new queued job
	kws := j.Input.Keywords()
	found_kws := make([]string, 0)
	for _, k := range kws {
		if ffuf.RequestContainsKeyword(job.req, k) {
			found_kws = append(found_kws, k)
		}
	}
	//And activate / disable inputproviders as needed
	j.Input.ActivateKeywords(found_kws)
	j.Jobhash, _ = WriteHistoryEntry(j.Config)
	return jobContext{basereq: job.req, depth: job.depth}
}

// SkipQueue allows to skip the current job and advance to the next queued recursion job
func (j *Job) SkipQueue() {
	j.setSkipQueue(true)
}

func (j *Job) sleepIfNeeded() {
	if !j.Config.Delay.HasDelay {
		return
	}
	var sleepDuration time.Duration
	if j.Config.Delay.IsRange {
		sTime := j.Config.Delay.Min + rand.Float64()*(j.Config.Delay.Max-j.Config.Delay.Min)
		sleepDuration = time.Duration(sTime * 1000)
	} else {
		sleepDuration = time.Duration(j.Config.Delay.Min * 1000)
	}
	sleepDuration = sleepDuration * time.Millisecond
	// makes the sleep cancellable by context
	select {
	case <-j.Config.Context.Done(): // cancelled
	case <-time.After(sleepDuration): // sleep
	}
}

// Pause pauses the job process. The pause gate is a sync.RWMutex: while Pause
// holds the write lock, every worker blocks at its pauseCheckpoint (an RLock).
//
// pauseStateMutex makes the paused-flag flip and the pauseMutex Lock/Unlock a
// single atomic step, so there is exactly one Lock per Unlock and no goroutine
// ever unlocks an unlocked mutex (a fatal panic), however Pause, Resume, and the
// SIGINT handler interleave.
//
// Pausing is refused once the job is stopping (isRunning is false). Together with
// Stop() calling Resume(), that guarantees the gate always ends OPEN after a Stop
// regardless of ordering, so a Pause racing a Stop can never strand a worker at
// the checkpoint with no Resume left to wake it.
func (j *Job) Pause() {
	j.pauseStateMutex.Lock()
	defer j.pauseStateMutex.Unlock()
	if !j.isRunning() {
		return
	}
	if !j.paused {
		j.paused = true
		if j.telemetryEnabled() {
			atomic.StoreInt32(&j.telemetryPauseObserved, 1)
		}
		j.pauseMutex.Lock()
		j.Output.Info("------ PAUSING ------")
	}
}

// Resume resumes the job process
func (j *Job) Resume() {
	j.pauseStateMutex.Lock()
	defer j.pauseStateMutex.Unlock()
	if j.paused {
		j.paused = false
		j.pauseMutex.Unlock()
		j.Output.Info("------ RESUMING -----")
	}
}

// pauseCheckpoint blocks while the job is paused and returns immediately
// otherwise. Acquiring and releasing the read lock is a cheap speed bump when no
// pause is in effect.
func (j *Job) pauseCheckpoint() {
	if j.Config != nil && j.Config.Noninteractive && !j.isExternallyManaged() {
		return
	}
	j.pauseMutex.RLock()
	//nolint:staticcheck // intentional: RLock/RUnlock is a pause speed bump, not a critical section
	j.pauseMutex.RUnlock()
}

func (j *Job) startExecution(ctx jobContext) {
	var background sync.WaitGroup
	background.Add(1)
	go j.runBackgroundTasks(&background)

	// Print the base URL when starting a new recursion or sniper queue job
	if j.queue.position() > 1 {
		if j.Config.InputMode == "sniper" {
			j.Output.Info(fmt.Sprintf("Starting queued sniper job (%d of %d) on target: %s", j.queue.position(), j.queue.total(), j.Config.Url))
		} else {
			j.Output.Info(fmt.Sprintf("Starting queued job on target: %s", j.Config.Url))
		}
	}

	// Persistent workers avoid allocating a goroutine and stack for every
	// wordlist entry. At HTTP/3 rates that per-request churn becomes a CPU/GC
	// ceiling long before the network is saturated.
	workerCount, maxWorkers := executionWorkerBounds(j.Config)
	queueDepth := workerCount
	if queueDepth > 4096 {
		queueDepth = 4096
	}
	tasks := make(chan executionTask, queueDepth)
	generation := atomic.AddUint64(&j.executionGeneration, 1)
	subscription := capacitySubscriptionForExecution(j.Runner, j.Config.ThreadsExplicit, generation)
	supervisor := newWorkerSupervisor(workerSupervisorConfig{
		Generation:     generation,
		InitialWorkers: workerCount,
		Remaining:      j.Input.Total(),
		MaxWorkers:     maxWorkers,
		Tasks:          tasks,
		Subscription:   subscription,
		Run: func(task executionTask) {
			j.pauseCheckpoint()
			j.runLogicalTask(ctx, task.input, task.position, task.logicalID)
			j.sleepIfNeeded()
			j.Rate.Tick()
		},
	})
	supervisor.start()

	injectHash := requestContainsKeyword(&ctx.basereq, "FFUFHASH")
	for {
		// Advancing the input cursor is done under inputMutex so an interactive
		// restart (which calls Input.Reset) cannot race it.
		var hasNext bool
		if j.Config.Noninteractive {
			hasNext = j.Input.Next()
		} else {
			j.inputMutex.Lock()
			hasNext = j.Input.Next()
			j.inputMutex.Unlock()
		}
		if !hasNext || j.isSkipQueue() {
			break
		}

		// Check if we should stop the process
		j.CheckStop()

		if !j.isRunning() {
			defer j.Output.Warning(j.getError())
			break
		}
		j.pauseCheckpoint()
		// Ratelimiter is a no-op in unlimited mode and paces configured rates.
		j.Rate.Wait()

		var nextInput map[string][]byte
		var nextPosition int
		if j.Config.Noninteractive {
			nextInput = j.Input.Value()
			nextPosition = j.Input.Position()
		} else {
			j.inputMutex.Lock()
			nextInput = j.Input.Value()
			nextPosition = j.Input.Position()
			j.inputMutex.Unlock()
		}
		if injectHash {
			nextInput["FFUFHASH"] = j.ffufHash(nextPosition)
		}

		j.incCounter()
		logicalID := j.scheduleLogicalInput()
		tasks <- executionTask{input: nextInput, position: nextPosition, logicalID: logicalID}
		if !j.isRunningJob() {
			defer j.Output.Warning(j.getError())
			// break, not return: fall through to wg.Wait() so the in-flight
			// workers finish before Start() advances to the next queue job, which
			// rewrites Config.Url / currentDepth / keyword-active state that those
			// workers still read (the -maxtime-job drain race).
			break
		}
	}
	// Stop advisor updates before closing the producer. wait joins the supervisor
	// (the sole WaitGroup.Add owner) before it waits for worker drain.
	supervisor.beginDrain()
	close(tasks)
	supervisor.wait()
	j.recordWorkerSupervisorSnapshot(supervisor.snapshot())
	background.Wait()
	j.updateProgress()
}

func (j *Job) interruptMonitor() func() {
	if j.isExternallyManaged() {
		return func() {}
	}
	sigChan := make(chan os.Signal, 2)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-sigChan:
				j.setError("Caught keyboard interrupt (Ctrl-C)\n")
				// Stop reopens the pause gate itself, so a worker blocked at a
				// checkpoint wakes and observes the stop.
				j.Stop()
			case <-done:
				return
			}
		}
	}()
	var cleanupOnce sync.Once
	return func() {
		cleanupOnce.Do(func() {
			signal.Stop(sigChan)
			close(done)
		})
	}
}

func (j *Job) runBackgroundTasks(wg *sync.WaitGroup) {
	defer wg.Done()
	totalProgress := j.Input.Total()
	for j.getCounter() <= totalProgress && !j.isSkipQueue() {
		j.pauseCheckpoint()
		if !j.isRunning() {
			break
		}
		j.updateProgress()
		// Scheduling all inputs does not mean their HTTP work has completed. Keep
		// publishing progress while the worker pool drains so consumers never see
		// an artificial zero-rate/stale-response interval at the end of a burst.
		if j.getCompletedCounter() >= totalProgress {
			return
		}
		if !j.isRunningJob() {
			return
		}
		time.Sleep(time.Millisecond * time.Duration(j.Config.ProgressFrequency))
	}
}

func (j *Job) updateProgress() {
	prog := ffuf.Progress{
		StartedAt:      j.getStartTimeJob(),
		ReqCount:       j.getCounter(),
		CompletedCount: j.getCompletedCounter(),
		ResponseCount:  j.getResponseCounter(),
		ReqTotal:       j.Input.Total(),
		ReqSec:         j.Rate.CurrentRate(),
		QueuePos:       j.queue.position(),
		QueueTotal:     j.queue.total(),
		ErrorCount:     j.getErrorCounter(),
	}
	j.Output.Progress(prog)
}

// isMatch delegates the match/filter decision to the MatcherManager seam, adapting
// the Config fields it needs. The logic itself now lives in filter.MatcherManager.
func (j *Job) isMatch(resp ffuf.Response) bool {
	return j.Config.MatcherManager.Matches(&resp, j.Config.AutoCalibrationPerHost, j.Config.MatcherMode, j.Config.FilterMode)
}

// AddFilters atomically appends metric filters to a running job. Live filter
// changes are restricted to OR mode because appending a new predicate in AND
// mode can make previously hidden responses visible, and those responses were
// never retained by the output pipeline. In OR mode additions are monotonic:
// they affect future match decisions and can safely be replayed over persisted
// findings without requiring a scan restart.
func (j *Job) AddFilters(options map[string]string) error {
	if j == nil || j.Config == nil || j.Config.MatcherManager == nil {
		return fmt.Errorf("job matcher manager is not initialized")
	}
	mode := strings.ToLower(strings.TrimSpace(j.Config.FilterMode))
	if mode != "" && mode != "or" {
		return fmt.Errorf("filters can only be added to a running scan when filter mode is or")
	}
	return j.Config.MatcherManager.AddFilters(options)
}

// ResultPassesFilters applies a filter set to the metrics retained for an
// already-observed result. It mirrors ffuf's OR/AND filter semantics without
// inventing response content that the high-throughput web output deliberately
// does not store. Regexp filtering is therefore unavailable post-start.
func ResultPassesFilters(result ffuf.Result, filters map[string]ffuf.FilterProvider, mode string) (bool, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = "or"
	}
	if mode != "or" && mode != "and" {
		return false, fmt.Errorf("invalid filter mode %q", mode)
	}
	if _, exists := filters["regexp"]; exists {
		return false, fmt.Errorf("regexp filters cannot be applied to stored results because response bodies are not retained")
	}

	response := &ffuf.Response{
		StatusCode:    result.StatusCode,
		ContentLength: result.ContentLength,
		ContentWords:  result.ContentWords,
		ContentLines:  result.ContentLines,
		Duration:      result.Duration,
		Request: &ffuf.Request{
			Input: result.Input,
			Url:   result.Url,
			Host:  result.Host,
		},
	}
	for _, provider := range filters {
		filtered, err := provider.Filter(response)
		if err != nil {
			return false, err
		}
		if mode == "or" && filtered {
			return false, nil
		}
		if mode == "and" && !filtered {
			return true, nil
		}
	}
	if mode == "and" && len(filters) > 0 {
		return false, nil
	}
	return true, nil
}

func (j *Job) ffufHash(pos int) []byte {
	hashstring := ""
	r := []rune(j.Jobhash)
	if len(r) > 5 {
		hashstring = string(r[:5])
	}
	hashstring += fmt.Sprintf("%x", pos)
	return []byte(hashstring)
}

func requestContainsKeyword(req *ffuf.Request, keyword string) bool {
	if req == nil {
		return false
	}
	if strings.Contains(req.Method, keyword) || strings.Contains(req.Url, keyword) || strings.Contains(string(req.Data), keyword) {
		return true
	}
	for name, value := range req.Headers {
		if strings.Contains(name, keyword) || strings.Contains(value, keyword) {
			return true
		}
	}
	return false
}

func (j *Job) runLogicalTask(ctx jobContext, input map[string][]byte, position int, logicalID uint64) {
	success, duration := j.runTask(ctx, input, position, false)
	j.incCompletedCounter()
	if success && j.responseLatency != nil {
		j.responseLatency.record(logicalID, duration)
	}
	j.completeLogicalInput(logicalID, success)
}

func (j *Job) runTask(ctx jobContext, input map[string][]byte, position int, retried bool) (bool, time.Duration) {
	basereq := ctx.basereq
	req, err := j.Runner.Prepare(input, &basereq)
	req.Timestamp = time.Now()

	req.Position = position
	if err != nil {
		j.Output.Error(fmt.Sprintf("Encountered an error while preparing request: %s\n", err))
		if observer, ok := j.Output.(requestDiagnosticOutput); ok {
			observer.RecordRequestError(err)
		}
		j.incError()
		log.Printf("%s", err)
		return false, 0
	}

	j.beginMainRequest()
	resp, err := j.executeMainRequest(&req, retried)
	if err != nil {
		req.Error = err.Error()
	}

	// Audit the request after sending to the runner so we get any changes
	if j.AuditLogger != nil {
		e := j.AuditLogger.Write(&req)
		if e != nil {
			j.Output.Error(fmt.Sprintf("Encountered error while writing request audit log: %s\n", e))
		}
	}

	if err != nil {
		if !retried {
			// Retry once. The timeout messaging below runs only on the final
			// failure, so a request that recovers on retry does not also print a
			// spurious timeout notice.
			j.incTelemetryRetry()
			return j.runTask(ctx, input, position, true)
		}
		j.incError()
		if observer, ok := j.Output.(requestDiagnosticOutput); ok {
			observer.RecordRequestError(err)
		}
		log.Printf("%s", err)
		if os.IsTimeout(err) {
			for name := range j.Config.MatcherManager.GetMatchers() {
				if name == "time" {
					inputmsg := ""
					for k, v := range input {
						inputmsg = inputmsg + fmt.Sprintf("%s : %s  // ", k, v)
					}
					j.Output.Info("Timeout while 'time' matcher is active: " + inputmsg)
					return false, 0
				}
			}
			for name := range j.Config.MatcherManager.GetFilters() {
				if name == "time" {
					inputmsg := ""
					for k, v := range input {
						inputmsg = inputmsg + fmt.Sprintf("%s : %s  // ", k, v)
					}
					j.Output.Info("Timeout while 'time' filter is active: " + inputmsg)
					return false, 0
				}
			}
		}
		return false, 0
	}
	j.incResponseCounter()
	if observer, ok := j.Output.(requestDiagnosticOutput); ok {
		observer.RecordResponseStatus(resp.StatusCode)
	}

	// audit the response after the error handling
	if j.AuditLogger != nil {
		err = j.AuditLogger.Write(&resp)
		if err != nil {
			j.Output.Error(fmt.Sprintf("Encountered error while writing response audit log: %s\n", err))
		}
	}

	if j.getSpuriousErrorCounter() > 0 {
		j.resetSpuriousErrors()
	}
	if j.Config.StopOn403 || j.Config.StopOnAll {
		// Increment Forbidden counter if we encountered one
		if resp.StatusCode == 403 {
			j.inc403()
		}
	}
	if j.Config.StopOnAll {
		// increment 429 counter if the response code is 429
		if resp.StatusCode == 429 {
			j.inc429()
		}
	}
	j.pauseCheckpoint()

	// Handle autocalibration after the actual request so req.Host is populated.
	// The disabled guard precedes URL parsing and calibration locking.
	j.calibrateResponse(req, input)

	matched := j.isMatch(resp)
	// Handle scraper actions
	if j.Scraper != nil {
		for _, sres := range j.Scraper.Execute(&resp, matched) {
			resp.SetScraperData(sres.Name, sres.Results)
			j.handleScraperResult(&resp, sres)
		}
	}

	if matched {
		// Re-send request through replay-proxy if needed
		if j.ReplayRunner != nil {
			replayreq, err := j.ReplayRunner.Prepare(input, &basereq)
			replayreq.Position = position
			if err != nil {
				j.Output.Error(fmt.Sprintf("Encountered an error while preparing replayproxy request: %s\n", err))
				j.incError()
				log.Printf("%s", err)
			} else {
				_, _ = j.ReplayRunner.Execute(&replayreq)
			}
		}
		j.ensureResultHash(&resp)
		j.Output.Result(resp)

		// Refresh the progress indicator as we printed something out
		j.updateProgress()
		if j.Config.Recursion && j.Config.RecursionStrategy == "greedy" {
			j.recursion.handleGreedy(ctx, resp)
		}
	} else {
		if len(resp.ScraperData) > 0 {
			// print the result anyway, as scraper found something
			j.ensureResultHash(&resp)
			j.Output.Result(resp)
		}
	}

	if j.Config.Recursion && j.Config.RecursionStrategy == "default" && len(resp.GetRedirectLocation(false)) > 0 {
		j.recursion.handleDefault(ctx, resp)
	}
	return true, resp.Duration
}

func (j *Job) ensureResultHash(resp *ffuf.Response) {
	if resp == nil || resp.Request == nil {
		return
	}
	if resp.Request.Input == nil {
		resp.Request.Input = make(map[string][]byte)
	}
	if _, exists := resp.Request.Input["FFUFHASH"]; !exists {
		resp.Request.Input["FFUFHASH"] = j.ffufHash(resp.Request.Position)
	}
}

func (j *Job) handleScraperResult(resp *ffuf.Response, sres ffuf.ScraperResult) {
	for _, a := range sres.Action {
		switch a {
		case "output":
			resp.SetScraperData(sres.Name, sres.Results)
		}
	}
}

// CheckStop stops the job if stopping conditions are met
func (j *Job) CheckStop() {
	counter := j.getCounter()
	if counter > 50 {
		// We have enough samples
		if j.Config.StopOn403 || j.Config.StopOnAll {
			if float64(j.getCount403())/float64(counter) > 0.95 {
				// Over 95% of requests are 403
				j.setError("Getting an unusual amount of 403 responses, exiting.")
				j.Stop()
			}
		}
		if j.Config.StopOnErrors || j.Config.StopOnAll {
			if j.getSpuriousErrorCounter() > j.Config.Threads*2 {
				// Most of the requests are erroring
				j.setError("Receiving spurious errors, exiting.")
				j.Stop()
			}

		}
		if j.Config.StopOnAll && (float64(j.getCount429())/float64(counter) > 0.2) {
			// Over 20% of responses are 429
			j.setError("Getting an unusual amount of 429 responses, exiting.")
			j.Stop()
		}
	}

	// Check for runtime of entire process
	if j.Config.MaxTime > 0 {
		dur := time.Since(j.getStartTime())
		runningSecs := int(dur / time.Second)
		if runningSecs >= j.Config.MaxTime {
			j.setError("Maximum running time for entire process reached, exiting.")
			j.Stop()
		}
	}

	// Check for runtime of current job
	if j.Config.MaxTimeJob > 0 {
		dur := time.Since(j.getStartTimeJob())
		runningSecs := int(dur / time.Second)
		if runningSecs >= j.Config.MaxTimeJob {
			j.setError("Maximum running time for this job reached, continuing with next job if one exists.")
			j.Next()

		}
	}
}

// Stop the execution of the Job
func (j *Job) Stop() {
	j.stopOnce.Do(func() {
		atomic.StoreInt32(&j.stopped, 1)
		j.setRunning(false)
		if j.Config != nil && j.Config.Cancel != nil {
			j.Config.Cancel()
		}
		if closer, ok := j.Runner.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
		if closer, ok := j.ReplayRunner.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
		// Reopen the pause gate so no worker is left blocked at a checkpoint after a
		// stop. Resume is a no-op when the job is not paused, and Pause refuses to
		// close the gate once isRunning is false, so the gate always ends open.
		j.Resume()
	})
}

// Stop current, resume to next
func (j *Job) Next() {
	j.setRunningJob(false)
}
