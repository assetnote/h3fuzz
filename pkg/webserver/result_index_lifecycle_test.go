package webserver

import (
	"context"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

func resultIndexNames(t *testing.T, store *Store) []string {
	t.Helper()
	rows, err := store.db.QueryContext(context.Background(), `PRAGMA index_list(results)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var sequence, unique, partial int
		var name, origin string
		if err := rows.Scan(&sequence, &name, &unique, &origin, &partial); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	return names
}

func assertPersistentResultIndexes(t *testing.T, store *Store) {
	t.Helper()
	names := make(map[string]bool)
	for _, name := range resultIndexNames(t, store) {
		names[name] = true
	}
	for _, index := range persistentResultIndexes {
		if !names[index.name] {
			t.Fatalf("persistent index %q is missing (all indexes: %v)",
				index.name, resultIndexNames(t, store))
		}
	}
	if !names["results_session_cursor"] {
		t.Fatalf("authoritative session cursor was removed (all indexes: %v)", resultIndexNames(t, store))
	}
}

func TestPersistentResultIndexesStayOnlineDuringConcurrentIngest(t *testing.T) {
	store, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	assertPersistentResultIndexes(t, store)
	if err := store.beginResultIngest(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertPersistentResultIndexes(t, store)
	if err := store.beginResultIngest(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := store.endResultIngest(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertPersistentResultIndexes(t, store)
	if err := store.endResultIngest(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertPersistentResultIndexes(t, store)
	if err := store.endResultIngest(context.Background()); err == nil {
		t.Fatal("unbalanced ingest release succeeded")
	}
}

func TestOpenStoreRepairsMissingPersistentResultIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "interrupted.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DROP INDEX results_session_status_cursor`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	assertPersistentResultIndexes(t, store)
}

func TestResultIngestReleaseDoesNotWaitForConcurrentReader(t *testing.T) {
	store, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	session := testSession("index-reader")
	if err := store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	if err := store.beginResultIngest(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.InsertResultBatch(context.Background(), session.ID, []StoredResult{{
		URL: "https://example.test/hit", StatusCode: 200, CreatedAt: time.Now().UTC(),
	}}, nil); err != nil {
		t.Fatal(err)
	}

	rows, err := store.db.QueryContext(context.Background(), `SELECT id,url FROM results WHERE session_id=?`, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !rows.Next() {
		rows.Close()
		t.Fatal("reader did not observe the persisted result")
	}
	var id int64
	var resultURL string
	if err := rows.Scan(&id, &resultURL); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() {
		time.Sleep(25 * time.Millisecond)
		_ = rows.Close()
		close(released)
	}()

	if err := store.endResultIngest(context.Background()); err != nil {
		<-released
		t.Fatalf("restore indexes while a short-lived reader held the results table: %v", err)
	}
	<-released
	assertPersistentResultIndexes(t, store)
	if id == 0 || resultURL == "" {
		t.Fatalf("unexpected reader values id=%d url=%q", id, resultURL)
	}
}

func TestPersistentResultIndexDefinitionsAreUnique(t *testing.T) {
	seen := make(map[string]struct{}, len(persistentResultIndexes))
	for _, index := range persistentResultIndexes {
		if index.name == "" || index.sql == "" {
			t.Fatalf("empty persistent index definition: %#v", index)
		}
		if _, duplicate := seen[index.name]; duplicate {
			t.Fatalf("duplicate persistent index definition %q", index.name)
		}
		seen[index.name] = struct{}{}
	}
}
