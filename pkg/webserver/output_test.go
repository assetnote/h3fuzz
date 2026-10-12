package webserver

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

type fakeBatchStore struct {
	mu      sync.Mutex
	count   int
	logs    int
	maxLogs int
	calls   int
	err     error
	block   <-chan struct{}
}

func (s *fakeBatchStore) InsertResultBatch(_ context.Context, sessionID string, results []StoredResult, logs []TerminalEntry) ([]StoredResult, []TerminalEntry, error) {
	if s.block != nil {
		<-s.block
	}
	if s.err != nil {
		return nil, nil, s.err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.logs += len(logs)
	if len(logs) > s.maxLogs {
		s.maxLogs = len(logs)
	}
	for i := range results {
		s.count++
		results[i].ID = int64(s.count)
		results[i].SessionID = sessionID
	}
	return results, logs, nil
}

func TestMatchedResultDoesNotQueueDuplicateTerminalLog(t *testing.T) {
	store := &fakeBatchStore{}
	output := newWebOutput("session", store, nil, nil)
	output.Result(responseAt(0))
	output.mu.Lock()
	queuedLogs := output.pendingLogsLocked()
	output.mu.Unlock()
	if queuedLogs != 0 {
		t.Fatalf("matched result queued %d duplicate terminal_log rows, want 0", queuedLogs)
	}
	if err := output.Finalize(); err != nil {
		t.Fatal(err)
	}
}

func responseAt(position int) ffuf.Response {
	return ffuf.Response{StatusCode: 200, ContentLength: 1, Request: &ffuf.Request{
		Position: position, Url: "https://example.test/x", Input: map[string][]byte{"FUZZ": []byte("x")}}}
}

func TestCapturedResponseFingerprintRequiresBytesAndIsContentStable(t *testing.T) {
	if got := capturedResponseFingerprint(nil); got != "" {
		t.Fatalf("bodyless response fingerprint = %q, want empty", got)
	}
	left := capturedResponseFingerprint([]byte("same response"))
	right := capturedResponseFingerprint([]byte("same response"))
	other := capturedResponseFingerprint([]byte("different response"))
	if left == "" || left != right || left == other {
		t.Fatalf("fingerprints are not exact and stable: left=%q right=%q other=%q", left, right, other)
	}
}

func TestWebOutputPersistsHighVolumeInBatches(t *testing.T) {
	store := &fakeBatchStore{}
	output := newWebOutput("session", store, nil, nil)
	for i := 0; i < 10000; i++ {
		output.Result(responseAt(i))
	}
	if err := output.Finalize(); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.count != 10000 {
		t.Fatalf("persisted %d of 10000 results", store.count)
	}
	if store.logs != 0 {
		t.Fatalf("persisted %d duplicate result terminal rows, want 0", store.logs)
	}
	if store.maxLogs > terminalBatchSize {
		t.Fatalf("terminal batch reached %d entries, want at most %d", store.maxLogs, terminalBatchSize)
	}
}

func waitForPersistenceBackpressure(t *testing.T, output *webOutput) webOutputStats {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		stats := output.Stats()
		if stats.PersistenceBackpressure {
			return stats
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("persistence queue did not apply backpressure within timeout: %+v", output.Stats())
	return webOutputStats{}
}

func TestWebOutputRetainsFailedBatchAndSignalsFatal(t *testing.T) {
	store := &fakeBatchStore{err: errors.New("database unavailable")}
	output := newWebOutput("session", store, nil, nil)
	fatal := make(chan struct{}, 1)
	output.fatal = func(error) { fatal <- struct{}{} }
	output.Result(responseAt(1))
	if err := output.Finalize(); err == nil {
		t.Fatal("expected persistence failure")
	}
	output.mu.Lock()
	pending := output.pendingResultsLocked()
	output.mu.Unlock()
	if pending != 1 {
		t.Fatalf("failed batch disappeared: pending=%d", pending)
	}
	select {
	case <-fatal:
	default:
		t.Fatal("fatal persistence failure was not reported")
	}
}

func TestWebOutputBoundsBackpressure(t *testing.T) {
	release := make(chan struct{})
	store := &fakeBatchStore{block: release}
	output := newWebOutput("session", store, nil, nil)
	done := make(chan struct{})
	go func() {
		for i := 0; i < maxPendingResults+resultBatchSize+1; i++ {
			output.Result(responseAt(i))
		}
		close(done)
	}()
	waitForPersistenceBackpressure(t, output)
	output.mu.Lock()
	pending := output.pendingResultsLocked()
	output.mu.Unlock()
	if pending > maxPendingResults {
		t.Fatalf("pending queue exceeded bound: %d", pending)
	}
	select {
	case <-done:
		t.Fatal("producer did not apply bounded backpressure")
	default:
	}
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("producer did not resume after persistence recovered")
	}
	if err := output.Finalize(); err != nil {
		t.Fatal(err)
	}
}

func TestWebOutputPrintResultUsesBoundedBackpressure(t *testing.T) {
	release := make(chan struct{})
	store := &fakeBatchStore{block: release}
	output := newWebOutput("session", store, nil, nil)
	done := make(chan struct{})
	go func() {
		for i := 0; i < maxPendingResults+resultBatchSize+1; i++ {
			output.PrintResult(ffuf.Result{Position: i, Url: "https://example.test/x", StatusCode: 200})
		}
		close(done)
	}()
	if stats := waitForPersistenceBackpressure(t, output); stats.ResultsQueued > maxPendingResults {
		t.Fatalf("PrintResult bypassed bounded queue: %+v", stats)
	}
	select {
	case <-done:
		t.Fatal("PrintResult producer did not apply backpressure")
	default:
	}
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("PrintResult producer did not resume")
	}
	if err := output.Finalize(); err != nil {
		t.Fatal(err)
	}
}

func TestCommitBatchDoesNotMoveQueuedTail(t *testing.T) {
	output := &webOutput{
		pending: make([]StoredResult, maxPendingResults, maxPendingResults*2),
		logs:    make([]TerminalEntry, maxPendingResults, maxPendingResults*2),
	}
	output.space = sync.NewCond(&output.mu)
	for index := 0; index < maxPendingResults; index++ {
		output.pending[index].Position = index
		output.logs[index].Message = fmt.Sprintf("line-%d", index)
	}
	output.pendingCount.Store(maxPendingResults)

	output.commitBatch(resultBatchSize, terminalBatchSize)

	// Committing a batch must advance through the queue instead of copying the
	// entire queued tail while request workers are waiting on the same mutex.
	if got := output.pending[resultBatchSize].Position; got != resultBatchSize {
		t.Fatalf("queued result tail was moved during commit: position=%d, want %d", got, resultBatchSize)
	}
	if got := output.logs[terminalBatchSize].Message; got != "line-1000" {
		t.Fatalf("queued terminal tail was moved during commit: message=%q", got)
	}
	results, logs := output.peekBatch()
	if got := len(results); got != resultBatchSize {
		t.Fatalf("next result batch size=%d, want %d", got, resultBatchSize)
	}
	if got := results[0].Position; got != resultBatchSize {
		t.Fatalf("next result position=%d, want %d", got, resultBatchSize)
	}
	if got := logs[0].Message; got != "line-1000" {
		t.Fatalf("next terminal line=%q, want line-1000", got)
	}
	if got, want := output.Stats().ResultsQueued, maxPendingResults-resultBatchSize; got != want {
		t.Fatalf("pending count=%d, want %d", got, want)
	}
}

func TestCommitBatchPeriodicCompactionPreservesOrderAndBound(t *testing.T) {
	output := &webOutput{
		pending: make([]StoredResult, maxPendingResults, maxPendingResults*2),
		logs:    make([]TerminalEntry, maxPendingResults, maxPendingResults*2),
	}
	output.space = sync.NewCond(&output.mu)
	for index := 0; index < maxPendingResults; index++ {
		output.pending[index].Position = index
		output.logs[index].Message = fmt.Sprintf("line-%d", index)
	}
	output.pendingCount.Store(maxPendingResults)
	nextPosition := maxPendingResults

	for batch := 0; batch < 100; batch++ {
		results, logs := output.peekBatch()
		wantFirst := batch * resultBatchSize
		if results[0].Position != wantFirst || logs[0].Message != fmt.Sprintf("line-%d", wantFirst) {
			t.Fatalf("batch %d starts with result=%d log=%q, want position %d", batch,
				results[0].Position, logs[0].Message, wantFirst)
		}
		output.commitBatch(len(results), len(logs))

		output.mu.Lock()
		for index := 0; index < resultBatchSize; index++ {
			position := nextPosition + index
			output.pending = append(output.pending, StoredResult{Position: position})
			output.logs = append(output.logs, TerminalEntry{Message: fmt.Sprintf("line-%d", position)})
		}
		nextPosition += resultBatchSize
		output.pendingCount.Store(int64(output.pendingResultsLocked()))
		output.mu.Unlock()
	}

	output.mu.Lock()
	defer output.mu.Unlock()
	if got := output.pendingResultsLocked(); got != maxPendingResults {
		t.Fatalf("active result queue=%d, want %d", got, maxPendingResults)
	}
	if got := output.pendingLogsLocked(); got != maxPendingResults {
		t.Fatalf("active terminal queue=%d, want %d", got, maxPendingResults)
	}
	if len(output.pending) > maxPendingResults*2 || len(output.logs) > maxPendingResults*2 {
		t.Fatalf("queue backing storage was not compacted: results=%d logs=%d", len(output.pending), len(output.logs))
	}
}

func TestWebOutputProgressIsLatestOnly(t *testing.T) {
	store := &fakeBatchStore{}
	progressed := make(chan ffuf.Progress, 2)
	output := newWebOutput("session", store, nil, func(p ffuf.Progress) { progressed <- p })
	for i := 0; i < 10000; i++ {
		output.Progress(ffuf.Progress{ReqCount: i, CompletedCount: i / 2, ReqTotal: 10000})
	}
	if err := output.Finalize(); err != nil {
		t.Fatal(err)
	}
	var last ffuf.Progress
	for len(progressed) > 0 {
		last = <-progressed
	}
	if last.ReqCount != 9999 {
		t.Fatalf("final progress = %d", last.ReqCount)
	}
	if last.CompletedCount != 4999 {
		t.Fatalf("final completed count = %d, want 4999", last.CompletedCount)
	}
}

func BenchmarkWebOutputProgress(b *testing.B) {
	output := &webOutput{}
	p := ffuf.Progress{StartedAt: time.Now(), ReqTotal: b.N}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		p.ReqCount = i
		output.Progress(p)
	}
}

func BenchmarkWebOutputCommitBatchAtBackpressureLimit(b *testing.B) {
	output := &webOutput{
		pending: make([]StoredResult, maxPendingResults, maxPendingResults*2),
		logs:    make([]TerminalEntry, maxPendingResults, maxPendingResults*2),
	}
	output.space = sync.NewCond(&output.mu)
	results := make([]StoredResult, resultBatchSize)
	logs := make([]TerminalEntry, terminalBatchSize)
	output.pendingCount.Store(maxPendingResults)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		output.commitBatch(resultBatchSize, terminalBatchSize)
		output.mu.Lock()
		output.pending = append(output.pending, results...)
		output.logs = append(output.logs, logs...)
		output.pendingCount.Store(maxPendingResults)
		output.mu.Unlock()
	}
}
