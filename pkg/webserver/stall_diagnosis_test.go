package webserver

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/assetnote/h3fuzz/pkg/engine"
	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

// countingInput is deliberately cheap: the diagnostic below is intended to
// expose scheduling/backpressure boundaries rather than exercise wordlist I/O.
type countingInput struct {
	mu       sync.Mutex
	position int
	total    int
}

func (*countingInput) ActivateKeywords([]string)                  {}
func (*countingInput) AddProvider(ffuf.InputProviderConfig) error { return nil }
func (*countingInput) Keywords() []string                         { return []string{"FUZZ"} }
func (i *countingInput) Next() bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.position >= i.total {
		return false
	}
	i.position++
	return true
}
func (i *countingInput) Position() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.position
}
func (i *countingInput) SetPosition(position int) {
	i.mu.Lock()
	i.position = position
	i.mu.Unlock()
}
func (i *countingInput) Reset() { i.SetPosition(0) }
func (i *countingInput) Value() map[string][]byte {
	i.mu.Lock()
	position := i.position
	i.mu.Unlock()
	return map[string][]byte{"FUZZ": []byte(fmt.Sprintf("item-%d", position))}
}
func (i *countingInput) Total() int { return i.total }

type immediateMatchRunner struct{}

func (immediateMatchRunner) Prepare(input map[string][]byte, base *ffuf.Request) (ffuf.Request, error) {
	req := ffuf.CopyRequest(base)
	req.Input = input
	return req, nil
}
func (immediateMatchRunner) Execute(req *ffuf.Request) (ffuf.Response, error) {
	return ffuf.Response{StatusCode: 200, ContentLength: 1, Request: req}, nil
}
func (immediateMatchRunner) Dump(*ffuf.Request) ([]byte, error) { return nil, nil }

type matchEverything struct{}

func (matchEverything) SetCalibrated(bool)                                     {}
func (matchEverything) SetCalibratedForHost(string, bool)                      {}
func (matchEverything) AddFilter(string, string, bool) error                   { return nil }
func (matchEverything) AddFilters(map[string]string) error                     { return nil }
func (matchEverything) AddPerDomainFilter(string, string, string) error        { return nil }
func (matchEverything) RemoveFilter(string)                                    {}
func (matchEverything) AddMatcher(string, string) error                        { return nil }
func (matchEverything) GetFilters() map[string]ffuf.FilterProvider             { return nil }
func (matchEverything) GetMatchers() map[string]ffuf.FilterProvider            { return nil }
func (matchEverything) FiltersForDomain(string) map[string]ffuf.FilterProvider { return nil }
func (matchEverything) CalibratedForDomain(string) bool                        { return true }
func (matchEverything) Calibrated() bool                                       { return true }
func (matchEverything) Matches(*ffuf.Response, bool, string, string) bool      { return true }

type measuredBatchStore struct {
	store      *Store
	mu         sync.Mutex
	batches    int
	results    int
	logs       int
	maxLatency time.Duration
	errors     []error
}

func (s *measuredBatchStore) InsertResultBatch(ctx context.Context, sessionID string, results []StoredResult, logs []TerminalEntry) ([]StoredResult, []TerminalEntry, error) {
	started := time.Now()
	persisted, persistedLogs, err := s.store.InsertResultBatch(ctx, sessionID, results, logs)
	elapsed := time.Since(started)
	s.mu.Lock()
	s.batches++
	s.results += len(persisted)
	s.logs += len(persistedLogs)
	if elapsed > s.maxLatency {
		s.maxLatency = elapsed
	}
	if err != nil {
		s.errors = append(s.errors, err)
	}
	s.mu.Unlock()
	return persisted, persistedLogs, err
}

func (s *measuredBatchStore) snapshot() (batches, results, logs int, maxLatency time.Duration, errors []error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.batches, s.results, s.logs, s.maxLatency, append([]error(nil), s.errors...)
}

// This is a regression/diagnostic for a deceptively transport-looking stall.
// If persistence stops draining, webOutput's bounded result queue blocks every
// worker. The dispatcher can then advance only through the worker pool and its
// task buffer, leaving the Job running with zero errors and a stable request
// count close to maxPendingResults + 2*Threads. That is the same shape as an
// HTTP/3 admission deadlock, so keep the boundary explicit and observable.
func TestWebPersistenceBackpressureProducesBoundedRunningPlateau(t *testing.T) {
	releaseStore := make(chan struct{})
	store := &fakeBatchStore{block: releaseStore}
	output := newWebOutput("session", store, nil, nil)

	const workers = 64
	total := maxPendingResults + 4*workers + 1000
	ctx, cancel := context.WithCancel(context.Background())
	conf := ffuf.NewConfig(ctx, cancel)
	conf.Noninteractive = true
	conf.Quiet = true
	conf.Method = "GET"
	conf.Url = "https://example.test/FUZZ"
	conf.Headers = map[string]string{}
	conf.Threads = workers
	conf.ProgressFrequency = 10
	conf.MatcherManager = matchEverything{}

	job := engine.NewJob(&conf)
	job.Input = &countingInput{total: total}
	job.Runner = immediateMatchRunner{}
	job.Output = output
	job.SetExternallyManaged(true)

	done := make(chan struct{})
	go func() {
		job.Start()
		close(done)
	}()

	// Once all workers and the task buffer are blocked behind persistence, the
	// dispatcher has counted one additional request before its blocking send.
	wantPlateau := maxPendingResults + 2*workers + 1
	deadline := time.Now().Add(10 * time.Second)
	for job.Snapshot().Progress.ReqCount < wantPlateau && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	first := job.Snapshot()
	if first.Progress.ReqCount != wantPlateau {
		close(releaseStore)
		job.Stop()
		<-done
		t.Fatalf("request plateau = %d, want %d", first.Progress.ReqCount, wantPlateau)
	}
	if !first.Running || first.Progress.ErrorCount != 0 {
		close(releaseStore)
		job.Stop()
		<-done
		t.Fatalf("unexpected plateau lifecycle: %+v", first)
	}
	stats := output.Stats()
	if !stats.PersistenceBackpressure || stats.ResultsQueued != maxPendingResults {
		close(releaseStore)
		job.Stop()
		<-done
		t.Fatalf("plateau was not identified as persistence backpressure: %+v", stats)
	}

	time.Sleep(150 * time.Millisecond)
	second := job.Snapshot()
	if second.Progress.ReqCount != first.Progress.ReqCount {
		close(releaseStore)
		job.Stop()
		<-done
		t.Fatalf("request count did not remain stalled: first=%d second=%d", first.Progress.ReqCount, second.Progress.ReqCount)
	}

	close(releaseStore)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		job.Stop()
		t.Fatal("job did not resume after persistence recovered")
	}
	if got := job.Snapshot().Progress.ReqCount; got != total {
		t.Fatalf("completed request count = %d, want %d", got, total)
	}
	if stats := output.Stats(); stats.PersistenceBackpressure || stats.ResultsQueued != 0 {
		t.Fatalf("persistence metrics did not recover: %+v", stats)
	}
}

func TestSQLiteWriterDrainsMoreThanBackpressureWindow(t *testing.T) {
	store, err := OpenStore(t.TempDir() + "/stall-diagnosis.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	session := securityTestSession("sqlite-drain")
	if err := store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}

	output := newWebOutput(session.ID, store, nil, nil)
	done := make(chan error, 1)
	go func() {
		for position := 0; position < maxPendingResults+resultBatchSize; position++ {
			output.Result(responseAt(position))
		}
		done <- output.Finalize()
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	// The race detector instruments every URL parse and SQLite bind in this
	// 33k-result burst. Keep this as a deadlock guard, not a wall-clock
	// performance assertion; production throughput is covered by the dedicated
	// benchmarks and the non-race suite completes this path in about a second.
	case <-time.After(90 * time.Second):
		t.Fatal("SQLite writer did not drain a result burst larger than the bounded queue")
	}
	stored, err := store.GetSession(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(maxPendingResults + resultBatchSize); stored.ResultCount != want {
		t.Fatalf("stored result count = %d, want %d", stored.ResultCount, want)
	}
}

func TestSQLiteWriterDrainsAutomaticHTTP3WorkerBurst(t *testing.T) {
	store, err := OpenStore(t.TempDir() + "/h3-worker-burst.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	session := securityTestSession("h3-worker-burst")
	if err := store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	hub := NewHub()
	manager, err := NewManager(store, hub, false)
	if err != nil {
		t.Fatal(err)
	}
	client := hub.add()
	defer hub.remove(client)

	measured := &measuredBatchStore{store: store}
	output := newWebOutput(session.ID, measured,
		func(eventType string, data interface{}) { manager.emit(session.ID, eventType, data) },
		func(progress ffuf.Progress) { manager.onProgress(session.ID, progress) })

	// Automatic HTTP/3 starts with eight connections and 256 streams each.
	// Use that exact worker geometry and enough all-match responses to cross the
	// result queue's backpressure boundary.
	const workers = defaultAdaptiveHTTP3ConnectionsForDiagnostic * 256
	total := maxPendingResults + 2*workers + resultBatchSize
	ctx, cancel := context.WithCancel(context.Background())
	conf := ffuf.NewConfig(ctx, cancel)
	conf.Noninteractive = true
	conf.Quiet = true
	conf.Method = "GET"
	conf.Url = "https://example.test/FUZZ"
	conf.Headers = map[string]string{}
	conf.Threads = workers
	conf.ProgressFrequency = 10
	conf.MatcherManager = matchEverything{}
	job := engine.NewJob(&conf)
	job.Input = &countingInput{total: total}
	job.Runner = immediateMatchRunner{}
	job.Output = output
	job.SetExternallyManaged(true)

	var maxPending int
	var firstCapAt time.Duration
	var persistedAtFirstCap int
	started := time.Now()
	monitorDone := make(chan struct{})
	monitorExited := make(chan struct{})
	go func() {
		defer close(monitorExited)
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				output.mu.Lock()
				pending := output.pendingResultsLocked()
				output.mu.Unlock()
				if pending > maxPending {
					maxPending = pending
				}
				if pending == maxPendingResults && firstCapAt == 0 {
					firstCapAt = time.Since(started)
					_, persistedAtFirstCap, _, _, _ = measured.snapshot()
				}
			case <-monitorDone:
				return
			}
		}
	}()
	job.Start()
	close(monitorDone)
	<-monitorExited
	elapsed := time.Since(started)

	batches, resultCount, logCount, maxLatency, batchErrors := measured.snapshot()
	if len(batchErrors) != 0 {
		t.Fatalf("batch persistence errors: %v", batchErrors)
	}
	if resultCount != total {
		t.Fatalf("persisted results = %d, want %d", resultCount, total)
	}
	if got := job.Snapshot().Progress.ReqCount; got != total {
		t.Fatalf("completed request count = %d, want %d", got, total)
	}
	if maxPending > maxPendingResults {
		t.Fatalf("pending results exceeded bound: %d", maxPending)
	}
	t.Logf("workers=%d results=%d elapsed=%s max_pending=%d first_cap_at=%s persisted_at_first_cap=%d batches=%d logs=%d max_batch_latency=%s websocket_resync=%t",
		workers, total, elapsed, maxPending, firstCapAt, persistedAtFirstCap, batches, logCount, maxLatency, client.needsResync.Load())
}

const defaultAdaptiveHTTP3ConnectionsForDiagnostic = 8
