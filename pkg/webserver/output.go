package webserver

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

func capturedResponseFingerprint(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	digest := sha256.Sum256(data)
	return fmt.Sprintf("sha256:%x", digest)
}

const (
	resultBatchSize   = 1000
	terminalBatchSize = 1000
	maxPendingResults = 32768
)

// webOutput keeps database and WebSocket work out of Job.runTask. Result only
// copies the compact metadata into an in-memory queue and wakes the batch writer.
type webOutput struct {
	sessionID string
	store     resultBatchStore
	emit      func(string, interface{})
	progress  func(ffuf.Progress)

	mu                 sync.Mutex
	space              *sync.Cond
	pending            []StoredResult
	pendingHead        int
	logs               []TerminalEntry
	logsHead           int
	current            []ffuf.Result
	diagnostics        map[string]*ErrorDiagnostic
	err                error
	wake               chan struct{}
	stop               chan struct{}
	done               chan struct{}
	once               sync.Once
	fatal              func(error)
	fatalOnce          sync.Once
	progressSet        atomic.Bool
	progressStarted    atomic.Int64
	progressReqCount   atomic.Int64
	progressCompleted  atomic.Int64
	progressResponses  atomic.Int64
	progressReqTotal   atomic.Int64
	progressReqSec     atomic.Int64
	progressQueuePos   atomic.Int64
	progressQueueTotal atomic.Int64
	progressErrorCount atomic.Int64
	sawRequests        atomic.Bool
	pendingCount       atomic.Int64
	blockedProducers   atomic.Int64
	lastBatchDuration  atomic.Int64
	backpressureLogged atomic.Bool
}

type webOutputStats struct {
	ResultsQueued           int
	PersistenceBackpressure bool
	LastBatchDurationMS     int64
	ErrorDiagnostics        []ErrorDiagnostic
}

type resultBatchStore interface {
	InsertResultBatch(context.Context, string, []StoredResult, []TerminalEntry) ([]StoredResult, []TerminalEntry, error)
}

func newWebOutput(sessionID string, store resultBatchStore, emit func(string, interface{}), progress func(ffuf.Progress)) *webOutput {
	o := &webOutput{sessionID: sessionID, store: store, emit: emit, progress: progress,
		wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{}),
		pending: make([]StoredResult, 0, resultBatchSize), diagnostics: make(map[string]*ErrorDiagnostic)}
	o.space = sync.NewCond(&o.mu)
	go o.writeLoop()
	return o
}

// Banner is the first lifecycle callback made by Job.Start. Persist a concise
// marker so an otherwise clean scan still has useful terminal history instead
// of leaving both the terminal REST endpoint and export empty.
func (o *webOutput) Banner() { o.Info("scan started") }

func (o *webOutput) Finalize() error {
	o.once.Do(func() { close(o.stop) })
	<-o.done
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.err
}

func (o *webOutput) Progress(progress ffuf.Progress) {
	if progress.ReqCount > 0 {
		o.sawRequests.Store(true)
	}
	o.progressStarted.Store(progress.StartedAt.UnixNano())
	o.progressReqCount.Store(int64(progress.ReqCount))
	o.progressCompleted.Store(int64(progress.CompletedCount))
	o.progressResponses.Store(int64(progress.ResponseCount))
	o.progressReqTotal.Store(int64(progress.ReqTotal))
	o.progressReqSec.Store(progress.ReqSec)
	o.progressQueuePos.Store(int64(progress.QueuePos))
	o.progressQueueTotal.Store(int64(progress.QueueTotal))
	o.progressErrorCount.Store(int64(progress.ErrorCount))
	o.progressSet.Store(true)
}

func (o *webOutput) SawRequests() bool { return o.sawRequests.Load() }

func (o *webOutput) Stats() webOutputStats {
	o.mu.Lock()
	diagnostics := cloneDiagnostics(o.diagnostics)
	o.mu.Unlock()
	return webOutputStats{
		ResultsQueued:           int(o.pendingCount.Load()),
		PersistenceBackpressure: o.blockedProducers.Load() > 0,
		LastBatchDurationMS:     o.lastBatchDuration.Load(),
		ErrorDiagnostics:        diagnostics,
	}
}

func (o *webOutput) publishProgress() {
	if o.progress == nil || !o.progressSet.Load() {
		return
	}
	o.progress(ffuf.Progress{
		StartedAt: time.Unix(0, o.progressStarted.Load()),
		ReqCount:  int(o.progressReqCount.Load()), CompletedCount: int(o.progressCompleted.Load()),
		ResponseCount: int(o.progressResponses.Load()),
		ReqTotal:      int(o.progressReqTotal.Load()),
		ReqSec:        o.progressReqSec.Load(), QueuePos: int(o.progressQueuePos.Load()),
		QueueTotal: int(o.progressQueueTotal.Load()), ErrorCount: int(o.progressErrorCount.Load()),
	})
}

func (o *webOutput) RecordRequestError(err error) {
	if err == nil {
		return
	}
	o.mu.Lock()
	addDiagnostic(o.diagnostics, classifyRequestError(err), err.Error())
	o.mu.Unlock()
}

func (o *webOutput) RecordResponseStatus(status int64) {
	if status != 429 {
		return
	}
	o.mu.Lock()
	addDiagnostic(o.diagnostics, "rate_limiting", "HTTP 429 Too Many Requests")
	o.mu.Unlock()
}

func (o *webOutput) Info(message string)    { o.log("info", message) }
func (o *webOutput) Error(message string)   { o.log("error", message) }
func (o *webOutput) Warning(message string) { o.log("warning", message) }
func (o *webOutput) Raw(message string)     { o.log("raw", message) }

func (o *webOutput) log(level, message string) {
	entry := TerminalEntry{At: time.Now().UTC(), Level: level, Message: message}
	o.mu.Lock()
	o.logs = append(o.logs, entry)
	o.mu.Unlock()
	o.signal()
}

func (o *webOutput) Result(resp ffuf.Response) {
	input := make(map[string][]byte, len(resp.Request.Input))
	for key, value := range resp.Request.Input {
		input[key] = append([]byte(nil), value...)
	}
	result := StoredResult{SessionID: o.sessionID, Input: input, Position: resp.Request.Position,
		StatusCode: resp.StatusCode, ContentLength: resp.ContentLength, ContentWords: resp.ContentWords,
		ContentLines: resp.ContentLines, ContentType: resp.ContentType,
		RedirectLocation: resp.GetRedirectLocation(false), URL: resp.Request.Url,
		DurationNS: resp.Duration.Nanoseconds(), ScraperData: resp.ScraperData, Host: resp.Request.Host,
		ResponseFingerprint: capturedResponseFingerprint(resp.Data),
		CreatedAt:           time.Now().UTC()}
	o.enqueueResult(result)
}

func (o *webOutput) enqueueResult(result StoredResult) {
	o.sawRequests.Store(true)
	o.mu.Lock()
	for o.pendingResultsLocked() >= maxPendingResults && o.err == nil {
		if o.backpressureLogged.CompareAndSwap(false, true) {
			message := fmt.Sprintf("result persistence queue reached %d entries; request workers are waiting for SQLite so findings remain lossless", maxPendingResults)
			o.logs = append(o.logs, TerminalEntry{At: time.Now().UTC(), Level: "warning", Message: message})
			addDiagnostic(o.diagnostics, "persistence_backpressure", message)
		}
		o.blockedProducers.Add(1)
		o.space.Wait()
		o.blockedProducers.Add(-1)
	}
	if o.err != nil {
		o.mu.Unlock()
		return
	}
	o.pending = append(o.pending, result)
	pending := o.pendingResultsLocked()
	o.pendingCount.Store(int64(pending))
	shouldWake := pending >= resultBatchSize
	o.mu.Unlock()
	if shouldWake {
		o.signal()
	}
}

func (o *webOutput) PrintResult(result ffuf.Result) {
	stored := StoredResult{SessionID: o.sessionID, Input: result.Input, Position: result.Position,
		StatusCode: result.StatusCode, ContentLength: result.ContentLength, ContentWords: result.ContentWords,
		ContentLines: result.ContentLines, ContentType: result.ContentType,
		RedirectLocation: result.RedirectLocation, URL: result.Url, DurationNS: result.Duration.Nanoseconds(),
		ScraperData: result.ScraperData, Host: result.Host, CreatedAt: time.Now().UTC()}
	o.enqueueResult(stored)
}

func (o *webOutput) SaveFile(string, string) error {
	return fmt.Errorf("web sessions are exported through the REST API")
}

func (o *webOutput) GetCurrentResults() []ffuf.Result {
	o.mu.Lock()
	defer o.mu.Unlock()
	result := make([]ffuf.Result, len(o.current))
	copy(result, o.current)
	return result
}

func (o *webOutput) SetCurrentResults(results []ffuf.Result) {
	o.mu.Lock()
	o.current = append(o.current[:0], results...)
	o.mu.Unlock()
}

func (o *webOutput) FilterCurrentResults(keep func(ffuf.Result) bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	filtered := o.current[:0]
	for _, result := range o.current {
		if keep(result) {
			filtered = append(filtered, result)
		}
	}
	o.current = filtered
}

func (o *webOutput) SetPaused(bool)      {}
func (o *webOutput) PendingResults() int { return 0 }
func (o *webOutput) Reset()              {}
func (o *webOutput) Cycle()              {}

func (o *webOutput) signal() {
	select {
	case o.wake <- struct{}{}:
	default:
	}
}

func (o *webOutput) peekBatch() ([]StoredResult, []TerminalEntry) {
	o.mu.Lock()
	defer o.mu.Unlock()
	pending := o.pending[o.pendingHead:]
	count := len(pending)
	if count > resultBatchSize {
		count = resultBatchSize
	}
	results := append([]StoredResult(nil), pending[:count]...)
	logsPending := o.logs[o.logsHead:]
	logCount := len(logsPending)
	if logCount > terminalBatchSize {
		logCount = terminalBatchSize
	}
	logs := append([]TerminalEntry(nil), logsPending[:logCount]...)
	return results, logs
}

func (o *webOutput) commitBatch(resultCount, logCount int) {
	o.mu.Lock()
	o.pending, o.pendingHead = advanceQueue(o.pending, o.pendingHead, resultCount)
	pending := o.pendingResultsLocked()
	o.pendingCount.Store(int64(pending))
	if pending < maxPendingResults/2 && o.backpressureLogged.CompareAndSwap(true, false) {
		o.logs = append(o.logs, TerminalEntry{At: time.Now().UTC(), Level: "info",
			Message: "result persistence queue recovered; request workers resumed"})
	}
	o.logs, o.logsHead = advanceQueue(o.logs, o.logsHead, logCount)
	o.space.Broadcast()
	o.mu.Unlock()
}

func (o *webOutput) pendingResultsLocked() int { return len(o.pending) - o.pendingHead }

func (o *webOutput) pendingLogsLocked() int { return len(o.logs) - o.logsHead }

// advanceQueue removes a committed prefix in O(batch size). The consumed head
// is compacted only after enough batches have accumulated to otherwise retain
// roughly a second full persistence queue in memory. This keeps the common
// full-queue commit path from copying ~32k entries under the producer mutex for
// every 1k-row SQLite transaction.
func advanceQueue[T any](items []T, head, count int) ([]T, int) {
	if count < 0 || count > len(items)-head {
		panic("web output queue commit exceeds pending entries")
	}
	clear(items[head : head+count])
	head += count
	if head == len(items) {
		return items[:0], 0
	}
	if head >= maxPendingResults && head*2 >= len(items) {
		remaining := copy(items, items[head:])
		clear(items[remaining:])
		return items[:remaining], 0
	}
	return items, head
}

func (o *webOutput) hasPending() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.pendingResultsLocked() > 0 || o.pendingLogsLocked() > 0
}

func (o *webOutput) setError(err error) {
	o.mu.Lock()
	if o.err == nil {
		o.err = err
	}
	o.space.Broadcast()
	o.mu.Unlock()
	if o.fatal != nil {
		o.fatalOnce.Do(func() { o.fatal(err) })
	}
}

func (o *webOutput) flush() {
	for batches := 0; batches < 4; batches++ {
		results, logs := o.peekBatch()
		if len(results) == 0 && len(logs) == 0 {
			return
		}
		var persisted []StoredResult
		var persistedLogs []TerminalEntry
		var err error
		batchStarted := time.Now()
		for attempt := 0; attempt < 3; attempt++ {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			persisted, persistedLogs, err = o.store.InsertResultBatch(ctx, o.sessionID, results, logs)
			cancel()
			if err == nil {
				break
			}
			time.Sleep(time.Duration(attempt+1) * 25 * time.Millisecond)
		}
		if err != nil {
			o.setError(fmt.Errorf("persist result batch: %w", err))
			return
		}
		o.lastBatchDuration.Store(time.Since(batchStarted).Milliseconds())
		o.commitBatch(len(results), len(logs))
		if len(persisted) > 0 && o.emit != nil {
			o.emit("result", map[string]interface{}{
				"results": persisted, "next_cursor": persisted[len(persisted)-1].ID,
			})
		}
		if len(persistedLogs) > 0 && o.emit != nil {
			o.emit("terminal", map[string]interface{}{"entries": persistedLogs,
				"next_cursor": persistedLogs[len(persistedLogs)-1].ID})
		}
	}
	if o.hasPending() {
		o.signal()
	}
}

func (o *webOutput) writeLoop() {
	defer close(o.done)
	flushTicker := time.NewTicker(50 * time.Millisecond)
	progressTicker := time.NewTicker(200 * time.Millisecond)
	defer flushTicker.Stop()
	defer progressTicker.Stop()
	for {
		select {
		case <-o.wake:
			o.flush()
		case <-flushTicker.C:
			o.flush()
		case <-progressTicker.C:
			o.publishProgress()
		case <-o.stop:
			for o.hasPending() {
				o.flush()
				if o.err != nil {
					return
				}
			}
			o.publishProgress()
			return
		}
	}
}
