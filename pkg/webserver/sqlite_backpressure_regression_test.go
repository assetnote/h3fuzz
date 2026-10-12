package webserver

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// TestSQLiteWriterSustainsMatchedResultsAfterRepeatedSessions covers the real
// web persistence seam: every matched response is queued once as authoritative
// result metadata, with terminal cursors stored as compact ranges, then drained
// into a file-backed WAL database. A scan producing
// results at the lower end of h3fuzz's established 40-50k RPS range must not
// stall request workers behind the bounded SQLite queue merely because earlier
// scans are still retained in the same database.
func TestSQLiteWriterSustainsMatchedResultsAfterRepeatedSessions(t *testing.T) {
	if raceDetectorEnabled {
		t.Skip("wall-clock persistence throughput is not meaningful under the race detector")
	}
	store, err := OpenStore(t.TempDir() + "/repeated-scans.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// Retain several earlier all-match scans so the regression exercises index
	// growth and WAL reuse instead of measuring only a pristine database.
	for run := 0; run < 3; run++ {
		session := testSession(fmt.Sprintf("prior-scan-%d", run))
		if err := store.CreateSession(context.Background(), &session); err != nil {
			t.Fatal(err)
		}
		if err := store.beginResultIngest(context.Background()); err != nil {
			t.Fatal(err)
		}
		output := newWebOutput(session.ID, store, nil, nil)
		for position := 0; position < 15_000; position++ {
			output.Result(responseAt(position))
		}
		if err := output.Finalize(); err != nil {
			t.Fatalf("finalize prior scan %d: %v", run, err)
		}
		if err := store.endResultIngest(context.Background()); err != nil {
			t.Fatalf("restore indexes after prior scan %d: %v", run, err)
		}
	}

	session := testSession("rate-controlled-scan")
	if err := store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	if err := store.beginResultIngest(context.Background()); err != nil {
		t.Fatal(err)
	}
	output := newWebOutput(session.ID, store, nil, nil)

	var sawBackpressure atomic.Bool
	monitorDone := make(chan struct{})
	monitorExited := make(chan struct{})
	go func() {
		defer close(monitorExited)
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if output.Stats().PersistenceBackpressure {
					sawBackpressure.Store(true)
				}
			case <-monitorDone:
				return
			}
		}
	}()

	const (
		targetResultsPerSecond = 50_000
		resultsPerBurst        = 500
		// Long enough that the bounded queue cannot hide a writer that only
		// handles a short burst near the target rate.
		resultCount = 250_000
	)
	started := time.Now()
	for position := 0; position < resultCount; position++ {
		if position%resultsPerBurst == 0 {
			due := started.Add(time.Duration(position) * time.Second / targetResultsPerSecond)
			if delay := time.Until(due); delay > 0 {
				time.Sleep(delay)
			}
		}
		output.Result(responseAt(position))
	}
	producerElapsed := time.Since(started)
	if err := output.Finalize(); err != nil {
		t.Fatal(err)
	}
	if err := store.endResultIngest(context.Background()); err != nil {
		t.Fatal(err)
	}
	close(monitorDone)
	<-monitorExited

	stored, err := store.GetSession(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ResultCount != resultCount {
		t.Fatalf("stored result count = %d, want %d", stored.ResultCount, resultCount)
	}
	if sawBackpressure.Load() {
		t.Fatalf("SQLite persistence applied backpressure while ingesting %d matched results at %d results/s after repeated scans (producer elapsed %s)",
			resultCount, targetResultsPerSecond, producerElapsed)
	}
}

func BenchmarkStoreInsertResultAndTerminalBatch1000(b *testing.B) {
	store, err := OpenStore(b.TempDir() + "/result-terminal-batch.db")
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	session := testSession("result-terminal-batch-benchmark")
	if err := store.CreateSession(context.Background(), &session); err != nil {
		b.Fatal(err)
	}

	results := make([]StoredResult, resultBatchSize)
	logs := make([]TerminalEntry, resultBatchSize)
	now := time.Now().UTC()
	for index := range results {
		url := fmt.Sprintf("https://example.test/batch/item-%06d", index)
		results[index] = StoredResult{
			Input:    map[string][]byte{"FUZZ": []byte(fmt.Sprintf("item-%06d", index))},
			Position: index, StatusCode: 200, URL: url, CreatedAt: now,
		}
		logs[index] = TerminalEntry{At: now, Level: "result",
			Message: fmt.Sprintf("%s [Status: 200, Size: 1, Words: 0, Lines: 0, Duration: 1ms]", url)}
	}

	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		resultBatch := append([]StoredResult(nil), results...)
		logBatch := append([]TerminalEntry(nil), logs...)
		if _, _, err := store.InsertResultBatch(context.Background(), session.ID, resultBatch, logBatch); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStoreInsertSynthesizedResultBatch1000(b *testing.B) {
	store, err := OpenStore(b.TempDir() + "/synthesized-result-batch.db")
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	session := testSession("synthesized-result-batch-benchmark")
	if err := store.CreateSession(context.Background(), &session); err != nil {
		b.Fatal(err)
	}
	results := make([]StoredResult, resultBatchSize)
	now := time.Now().UTC()
	for index := range results {
		results[index] = StoredResult{
			Input:    map[string][]byte{"FUZZ": []byte(fmt.Sprintf("item-%06d", index))},
			Position: index, StatusCode: 200,
			URL: fmt.Sprintf("https://example.test/batch/item-%06d", index), CreatedAt: now,
		}
	}
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		resultBatch := append([]StoredResult(nil), results...)
		if _, _, err := store.InsertResultBatch(context.Background(), session.ID, resultBatch, nil); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStoreInsertResultBatchIndexProfiles(b *testing.B) {
	profiles := []struct {
		name string
		drop []string
	}{
		{name: "all_indexes"},
		{name: "without_explorer", drop: []string{"results_explorer_metrics"}},
		{name: "without_tree", drop: []string{"results_tree_scope"}},
		{name: "without_url", drop: []string{"results_session_url_cursor"}},
		{name: "without_status", drop: []string{"results_session_status_cursor"}},
		{name: "without_cursor", drop: []string{"results_session_cursor"}},
		{name: "cursor_only", drop: []string{"results_explorer_metrics", "results_tree_scope", "results_session_url_cursor", "results_session_status_cursor"}},
	}
	for _, profile := range profiles {
		b.Run(profile.name, func(b *testing.B) {
			store, err := OpenStore(b.TempDir() + "/index-profile.db")
			if err != nil {
				b.Fatal(err)
			}
			defer store.Close()
			for _, index := range profile.drop {
				if _, err := store.db.Exec(`DROP INDEX ` + index); err != nil {
					b.Fatal(err)
				}
			}
			session := testSession("index-profile")
			if err := store.CreateSession(context.Background(), &session); err != nil {
				b.Fatal(err)
			}
			results := make([]StoredResult, resultBatchSize)
			now := time.Now().UTC()
			for index := range results {
				results[index] = StoredResult{
					Input:    map[string][]byte{"FUZZ": []byte(fmt.Sprintf("item-%06d", index))},
					Position: index, StatusCode: 200,
					URL: fmt.Sprintf("https://example.test/batch/item-%06d", index), CreatedAt: now,
				}
			}
			b.ResetTimer()
			for iteration := 0; iteration < b.N; iteration++ {
				batch := append([]StoredResult(nil), results...)
				if _, _, err := store.InsertResultBatch(context.Background(), session.ID, batch, nil); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
