package webserver

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestResultTerminalHistoryUsesAuthoritativeResultRow(t *testing.T) {
	store, err := OpenStore(t.TempDir() + "/terminal-results.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	session := testSession("terminal-result-storage")
	if err := store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	result := StoredResult{
		URL: "https://example.test/admin", StatusCode: 200,
		ContentLength: 17, ContentWords: 3, ContentLines: 2,
		DurationNS: int64(1500 * time.Microsecond), CreatedAt: now,
	}
	resultLine := fmt.Sprintf("%s [Status: %d, Size: %d, Words: %d, Lines: %d, Duration: %s]",
		result.URL, result.StatusCode, result.ContentLength, result.ContentWords,
		result.ContentLines, time.Duration(result.DurationNS))
	lifecycleLogs := []TerminalEntry{
		{At: now.Add(-time.Millisecond), Level: "info", Message: "scan started"},
		{At: now.Add(time.Millisecond), Level: "info", Message: "scan completed"},
	}

	_, persistedLogs, err := store.InsertResultBatch(context.Background(), session.ID, []StoredResult{result}, lifecycleLogs)
	if err != nil {
		t.Fatal(err)
	}
	if len(persistedLogs) != 3 || persistedLogs[0].ID <= 0 ||
		persistedLogs[1].ID != persistedLogs[0].ID+1 || persistedLogs[2].ID != persistedLogs[1].ID+1 {
		t.Fatalf("terminal event IDs are not one ordered sequence: %#v", persistedLogs)
	}

	page, err := store.Terminal(context.Background(), session.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 3 {
		t.Fatalf("terminal history entries = %d, want 3: %#v", len(page.Entries), page.Entries)
	}
	want := []TerminalEntry{lifecycleLogs[0], {At: now, Level: "result", Message: resultLine}, lifecycleLogs[1]}
	for index := range want {
		if page.Entries[index].ID != persistedLogs[index].ID ||
			page.Entries[index].Level != want[index].Level ||
			page.Entries[index].Message != want[index].Message ||
			!page.Entries[index].At.Equal(want[index].At) {
			t.Fatalf("terminal entry %d = %#v, want %#v with ID %d", index,
				page.Entries[index], want[index], persistedLogs[index].ID)
		}
	}

	var physicalResultRows int
	if err := store.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM terminal_log WHERE session_id=? AND level='result'`, session.ID).
		Scan(&physicalResultRows); err != nil {
		t.Fatal(err)
	}
	if physicalResultRows != 0 {
		t.Fatalf("terminal_log contains %d duplicated result rows, want 0", physicalResultRows)
	}
}

func TestWebOutputBannerDoesNotShiftResultHistoryAcrossBatches(t *testing.T) {
	store, err := OpenStore(t.TempDir() + "/banner-offset.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	session := testSession("banner-offset")
	if err := store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}

	output := newWebOutput(session.ID, store, nil, nil)
	output.Banner()
	const resultCount = resultBatchSize + 37
	for index := 0; index < resultCount; index++ {
		response := responseAt(index)
		response.Request.Url = fmt.Sprintf("https://example.test/item-%04d", index)
		output.Result(response)
	}
	if err := output.Finalize(); err != nil {
		t.Fatal(err)
	}

	entries, err := store.TerminalEntries(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != resultCount+1 || entries[0].Level != "info" || entries[0].Message != "scan started" {
		t.Fatalf("terminal history has shifted around banner: count=%d first=%#v", len(entries), entries[0])
	}
	for index, entry := range entries {
		if entry.ID != int64(index+1) {
			t.Fatalf("terminal cursor %d = %d, want %d", index, entry.ID, index+1)
		}
	}
	var duplicated int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM terminal_log WHERE session_id=? AND level='result'`, session.ID).Scan(&duplicated); err != nil {
		t.Fatal(err)
	}
	if duplicated != 0 {
		t.Fatalf("cross-batch run persisted %d duplicate result terminal rows", duplicated)
	}
}

func TestConcurrentWebResultsHaveOneStableTerminalEventEach(t *testing.T) {
	store, err := OpenStore(t.TempDir() + "/concurrent-terminal.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	session := testSession("concurrent-terminal")
	if err := store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}

	output := newWebOutput(session.ID, store, nil, nil)
	const resultCount = 2500
	var workers sync.WaitGroup
	workers.Add(resultCount)
	for index := 0; index < resultCount; index++ {
		go func(position int) {
			defer workers.Done()
			response := responseAt(position)
			response.Request.Url = fmt.Sprintf("https://example.test/concurrent-%04d", position)
			output.Result(response)
		}(index)
	}
	workers.Wait()
	if err := output.Finalize(); err != nil {
		t.Fatal(err)
	}
	entries, err := store.TerminalEntries(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != resultCount {
		t.Fatalf("concurrent terminal entries = %d, want %d", len(entries), resultCount)
	}
	for index, entry := range entries {
		if entry.ID != int64(index+1) || entry.Level != "result" {
			t.Fatalf("concurrent terminal entry %d = %#v", index, entry)
		}
	}
	var duplicated int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM terminal_log WHERE session_id=?`, session.ID).Scan(&duplicated); err != nil {
		t.Fatal(err)
	}
	if duplicated != 0 {
		t.Fatalf("concurrent run persisted %d terminal_log rows, want 0", duplicated)
	}
	var ranges int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM terminal_result_ranges WHERE session_id=?`, session.ID).Scan(&ranges); err != nil {
		t.Fatal(err)
	}
	if ranges > 3 {
		t.Fatalf("concurrent run fragmented its compact terminal mapping into %d ranges", ranges)
	}
}

func TestTerminalRangeMappingPreservesResultQueueOrder(t *testing.T) {
	store, err := OpenStore(t.TempDir() + "/terminal-range-order.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	session := testSession("terminal-range-order")
	if err := store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	results := []StoredResult{
		{URL: "https://example.test/first", StatusCode: 200, CreatedAt: now.Add(2 * time.Millisecond)},
		{URL: "https://example.test/second", StatusCode: 200, CreatedAt: now.Add(time.Millisecond)},
		{URL: "https://example.test/third", StatusCode: 200, CreatedAt: now.Add(3 * time.Millisecond)},
	}
	logs := []TerminalEntry{
		{At: now.Add(-time.Millisecond), Level: "info", Message: "scan started"},
		{At: now.Add(2500 * time.Microsecond), Level: "info", Message: "checkpoint"},
	}
	if _, _, err := store.InsertResultBatch(context.Background(), session.ID, results, logs); err != nil {
		t.Fatal(err)
	}

	entries, err := store.TerminalEntries(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantMessages := []string{
		"scan started", formatResultTerminal(results[0]), formatResultTerminal(results[1]),
		"checkpoint", formatResultTerminal(results[2]),
	}
	if len(entries) != len(wantMessages) {
		t.Fatalf("terminal entries = %d, want %d: %#v", len(entries), len(wantMessages), entries)
	}
	for index, want := range wantMessages {
		if entries[index].ID != int64(index+1) || entries[index].Message != want {
			t.Fatalf("terminal entry %d = %#v, want ID %d and message %q", index, entries[index], index+1, want)
		}
	}
	var ranges int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM terminal_result_ranges WHERE session_id=?`, session.ID).Scan(&ranges); err != nil {
		t.Fatal(err)
	}
	if ranges != 2 {
		t.Fatalf("terminal result ranges = %d, want 2 around one lifecycle event", ranges)
	}
}

func TestSearchTerminalFindsSynthesizedResultLinesAndFacets(t *testing.T) {
	store, err := OpenStore(t.TempDir() + "/terminal-search.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	session := testSession("terminal-result-search")
	if err := store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	result := StoredResult{URL: "https://example.test/search-me", StatusCode: 302,
		ContentLength: 17, ContentWords: 3, ContentLines: 2,
		DurationNS: int64(1500 * time.Microsecond), CreatedAt: now}
	logs := []TerminalEntry{{At: now.Add(time.Millisecond), Level: "info", Message: "scan completed"}}
	if _, _, err := store.InsertResultBatch(context.Background(), session.ID, []StoredResult{result}, logs); err != nil {
		t.Fatal(err)
	}

	page, err := store.SearchTerminal(context.Background(), session.ID, TerminalQuery{
		Search: "Duration: 1.5ms", Latest: true, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Entries) != 1 || page.Entries[0].Level != "result" ||
		page.Entries[0].Message != formatResultTerminal(result) {
		t.Fatalf("result terminal search = %#v", page)
	}
	if page.Levels["result"] != 1 || page.Levels["info"] != 1 {
		t.Fatalf("terminal level facets = %#v, want result=1 info=1", page.Levels)
	}
}

func TestTerminalSearchLatestIndexedMergesResultsAndLifecycleLogs(t *testing.T) {
	store, err := OpenStore(t.TempDir() + "/terminal-latest-mixed.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	session := testSession("terminal-latest-mixed")
	if err := store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	results := []StoredResult{
		{URL: "https://example.test/one", StatusCode: 200, CreatedAt: now},
		{URL: "https://example.test/two", StatusCode: 204, CreatedAt: now.Add(time.Millisecond)},
	}
	logs := []TerminalEntry{
		{At: now.Add(-time.Millisecond), Level: "info", Message: "scan started"},
		{At: now.Add(2 * time.Millisecond), Level: "warning", Message: "scan warning"},
	}
	if _, _, err := store.InsertResultBatch(context.Background(), session.ID, results, logs); err != nil {
		t.Fatal(err)
	}

	page, err := store.SearchTerminal(context.Background(), session.ID, TerminalQuery{Latest: true, Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 4 || len(page.Entries) != 3 || !page.HasOlder || page.SnapshotID != 4 ||
		page.Levels["result"] != 2 || page.Levels["info"] != 1 || page.Levels["warning"] != 1 {
		t.Fatalf("mixed latest terminal page = %#v", page)
	}
	if page.Entries[0].ID != 2 || page.Entries[0].Message != formatResultTerminal(results[0]) ||
		page.Entries[1].ID != 3 || page.Entries[1].Message != formatResultTerminal(results[1]) ||
		page.Entries[2].ID != 4 || page.Entries[2].Message != "scan warning" {
		t.Fatalf("mixed latest terminal ordering = %#v", page.Entries)
	}

	warnings, err := store.SearchTerminal(context.Background(), session.ID, TerminalQuery{
		Latest: true, Levels: []string{"warning"}, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if warnings.Total != 1 || len(warnings.Entries) != 1 || warnings.Entries[0].Message != "scan warning" ||
		warnings.Levels["result"] != 2 {
		t.Fatalf("filtered latest terminal page = %#v", warnings)
	}

	before, snapshot := page.NextBeforeID, page.SnapshotID
	older, err := store.SearchTerminal(context.Background(), session.ID, TerminalQuery{
		Latest: true, BeforeID: &before, SnapshotID: &snapshot, Limit: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(older.Entries) != 1 || older.Entries[0].ID != 1 || older.Entries[0].Message != "scan started" || older.HasOlder {
		t.Fatalf("mixed older terminal page = %#v", older)
	}
}

func TestTerminalEventMigrationKeepsLegacyRowsAndAppendsWithoutDuplication(t *testing.T) {
	databasePath := t.TempDir() + "/legacy-terminal.db"
	store, err := OpenStore(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	session := testSession("legacy-terminal")
	if err := store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	legacyAt := time.Now().UTC().Add(-time.Second)
	if _, err := store.db.ExecContext(context.Background(),
		`INSERT INTO terminal_log(session_id,at,level,message,terminal_sequence) VALUES(?,?,?,?,0)`,
		session.ID, legacyAt.UnixNano(), "result", "legacy result line"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	// Recreate the pre-terminal-sequence schema shape without copying or
	// rewriting user data. Opening it through Store is the migration boundary.
	raw, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	legacyDDL := []string{
		`DROP INDEX IF EXISTS results_terminal_sequence`,
		`DROP INDEX IF EXISTS terminal_new_sequence`,
		`DROP INDEX IF EXISTS terminal_new_level_sequence`,
		`ALTER TABLE results DROP COLUMN terminal_sequence`,
		`ALTER TABLE results DROP COLUMN terminal_at`,
		`ALTER TABLE terminal_log DROP COLUMN terminal_sequence`,
		`ALTER TABLE sessions DROP COLUMN terminal_sequence`,
	}
	for _, statement := range legacyDDL {
		if _, err := raw.ExecContext(context.Background(), statement); err != nil {
			raw.Close()
			t.Fatalf("prepare legacy schema with %q: %v", statement, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = OpenStore(databasePath)
	if err != nil {
		t.Fatalf("migrate legacy terminal database: %v", err)
	}
	defer store.Close()
	now := time.Now().UTC()
	result := StoredResult{URL: "https://example.test/new", StatusCode: 200, DurationNS: int64(time.Millisecond), CreatedAt: now}
	if _, _, err := store.InsertResultBatch(context.Background(), session.ID, []StoredResult{result}, nil); err != nil {
		t.Fatal(err)
	}

	page, err := store.Terminal(context.Background(), session.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 2 || page.Entries[0].ID != 1 || page.Entries[0].Message != "legacy result line" ||
		page.Entries[1].ID != 2 || page.Entries[1].Message != formatResultTerminal(result) {
		t.Fatalf("migrated terminal history = %#v", page)
	}
}

func BenchmarkSearchTerminalLatest100KRangeBacked(b *testing.B) {
	store, err := OpenStore(b.TempDir() + "/terminal-latest-benchmark.db")
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	session := testSession("terminal-latest-benchmark")
	if err := store.CreateSession(context.Background(), &session); err != nil {
		b.Fatal(err)
	}

	const resultCount = 100_000
	now := time.Now().UTC()
	for offset := 0; offset < resultCount; offset += resultBatchSize {
		results := make([]StoredResult, resultBatchSize)
		for index := range results {
			position := offset + index
			results[index] = StoredResult{
				URL:        fmt.Sprintf("https://example.test/item-%06d", position),
				StatusCode: 200, ContentLength: 17, ContentWords: 3, ContentLines: 2,
				DurationNS: int64(time.Millisecond), CreatedAt: now.Add(time.Duration(position)),
			}
		}
		if _, _, err := store.InsertResultBatch(context.Background(), session.ID, results, nil); err != nil {
			b.Fatal(err)
		}
	}
	var ranges int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM terminal_result_ranges WHERE session_id=?`, session.ID).Scan(&ranges); err != nil {
		b.Fatal(err)
	}
	if ranges != resultCount/resultBatchSize {
		b.Fatalf("terminal result ranges = %d, want %d", ranges, resultCount/resultBatchSize)
	}

	b.ResetTimer()
	for range b.N {
		page, err := store.SearchTerminal(context.Background(), session.ID, TerminalQuery{Latest: true, Limit: 500})
		if err != nil {
			b.Fatal(err)
		}
		if page.Total != resultCount || len(page.Entries) != 500 {
			b.Fatalf("latest terminal page total=%d entries=%d", page.Total, len(page.Entries))
		}
		if page.Entries[0].ID != resultCount-499 || page.Entries[len(page.Entries)-1].ID != resultCount {
			b.Fatalf("latest terminal cursors = %d..%d", page.Entries[0].ID, page.Entries[len(page.Entries)-1].ID)
		}
	}
}
