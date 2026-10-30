package webserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResponseFingerprintUsesCompactBlobAndRoundTrips(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "web.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	session := testSession("fingerprint-encoding")
	if err := store.CreateSession(t.Context(), &session); err != nil {
		t.Fatal(err)
	}
	fingerprint := "sha256:" + strings.Repeat("ab", 32)
	if _, _, err := store.InsertResultBatch(t.Context(), session.ID, []StoredResult{{
		URL: "https://example.test/hit", Host: "example.test", StatusCode: http.StatusOK,
		ResponseFingerprint: fingerprint, CreatedAt: time.Now().UTC(),
	}}, nil); err != nil {
		t.Fatal(err)
	}
	var storageType string
	var length int
	if err := store.db.QueryRow(`SELECT typeof(response_fingerprint),length(response_fingerprint) FROM results`).Scan(&storageType, &length); err != nil {
		t.Fatal(err)
	}
	if storageType != "blob" || length != 33 {
		t.Fatalf("fingerprint storage = %s/%d bytes, want blob/33", storageType, length)
	}
	results, err := store.AllResults(t.Context(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].ResponseFingerprint != fingerprint {
		t.Fatalf("decoded fingerprint = %#v, want %q", results, fingerprint)
	}
}

func TestCompactUpgradesLegacyPayloadsAndBackfillsSessionUsage(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "web.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	session := testSession("legacy-storage")
	if err := store.CreateSession(t.Context(), &session); err != nil {
		t.Fatal(err)
	}
	fingerprint := "sha256:" + strings.Repeat("cd", 32)
	if _, _, err := store.InsertResultBatch(t.Context(), session.ID, []StoredResult{{
		Input: map[string][]byte{"FUZZ": []byte("admin"), "FFUFHASH": []byte("internal")},
		URL:   "https://example.test/admin", Host: "example.test", StatusCode: http.StatusOK,
		ContentType: "text/html", ContentLength: 42, ContentWords: 3, ContentLines: 2,
		ResponseFingerprint: fingerprint, CreatedAt: time.Now().UTC(),
	}}, nil); err != nil {
		t.Fatal(err)
	}
	legacyInput, _ := json.Marshal(map[string][]byte{"FUZZ": []byte("admin"), "FFUFHASH": []byte("internal")})
	if _, err := store.db.Exec(`UPDATE results SET response_fingerprint=?,input_json=?`, fingerprint, legacyInput); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DELETE FROM result_summaries`); err != nil {
		t.Fatal(err)
	}
	// A retroactive filter can reduce the visible session count to zero even
	// though the scan still owns raw rows. Range metadata supplies an exact,
	// non-scanning fallback for cleanup ranking.
	if _, err := store.db.Exec(`UPDATE sessions SET result_count=0 WHERE id=?`, session.ID); err != nil {
		t.Fatal(err)
	}
	legacyStats, err := store.StorageStats(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(legacyStats.Sessions) != 1 || legacyStats.Sessions[0].ResultCount != 1 ||
		legacyStats.Sessions[0].EstimatedBytes != legacyEstimatedBytesPerResult {
		t.Fatalf("legacy non-scanning storage fallback = %#v", legacyStats.Sessions)
	}
	if err := store.Compact(t.Context()); err != nil {
		t.Fatal(err)
	}
	var storageType string
	var length int
	var storedInput []byte
	if err := store.db.QueryRow(`SELECT typeof(response_fingerprint),length(response_fingerprint),input_json FROM results`).
		Scan(&storageType, &length, &storedInput); err != nil {
		t.Fatal(err)
	}
	if storageType != "blob" || length != 33 {
		t.Fatalf("compacted fingerprint = %s/%d bytes, want blob/33", storageType, length)
	}
	var input map[string][]byte
	if err := json.Unmarshal(storedInput, &input); err != nil {
		t.Fatal(err)
	}
	if _, exists := input["FFUFHASH"]; exists || string(input["FUZZ"]) != "admin" {
		t.Fatalf("compacted input = %#v", input)
	}
	stats, err := store.StorageStats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(stats.Sessions) != 1 || stats.Sessions[0].ResultCount != 1 || stats.Sessions[0].EstimatedBytes <= 0 {
		t.Fatalf("session storage usage = %#v", stats.Sessions)
	}
	var dimensionCount int64
	if err := store.db.QueryRow(`SELECT count FROM result_dimension_summaries
		WHERE session_id=? AND status=200 AND content_type='text/html'`, session.ID).Scan(&dimensionCount); err != nil {
		t.Fatal(err)
	}
	if dimensionCount != 1 {
		t.Fatalf("backfilled dimension count = %d, want 1", dimensionCount)
	}
	results, err := store.AllResults(t.Context(), session.ID)
	if err != nil || len(results) != 1 || results[0].ResponseFingerprint != fingerprint {
		t.Fatalf("decoded compacted result = %#v, err=%v", results, err)
	}
}

func TestNewDatabaseUsesIncrementalAutoVacuumAndOmitsLegacyMetricsIndex(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "web.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	var mode int
	if err := store.db.QueryRow(`PRAGMA auto_vacuum`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != 2 {
		t.Fatalf("auto_vacuum = %d, want INCREMENTAL (2)", mode)
	}
	var legacyIndex int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='results_explorer_metrics'`).Scan(&legacyIndex); err != nil {
		t.Fatal(err)
	}
	if legacyIndex != 0 {
		t.Fatal("legacy wide result metrics index was recreated")
	}
}

func TestReopenDefersLegacyTreeBackfillUntilExplicitCompaction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	session := testSession("legacy-tree")
	if err := store.CreateSession(t.Context(), &session); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.InsertResultBatch(t.Context(), session.ID, []StoredResult{{
		URL: "https://example.test/dir/item", Host: "example.test", StatusCode: 200, CreatedAt: time.Now().UTC(),
	}}, nil); err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`DROP INDEX results_tree_scope`,
		`DELETE FROM storage_migrations WHERE name='result_tree_metadata_v1'`,
		`UPDATE results SET tree_origin='',tree_path_key='',tree_suffix='',tree_trailing_slash=0`,
		`CREATE TRIGGER reject_startup_tree_backfill BEFORE UPDATE OF tree_origin ON results
			BEGIN SELECT RAISE(ABORT,'tree backfill must not run during startup'); END`,
	}
	for _, statement := range statements {
		if _, err := store.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = OpenStore(path)
	if err != nil {
		t.Fatalf("reopen performed an unbounded tree backfill: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	var origin string
	if err := store.db.QueryRow(`SELECT tree_origin FROM results LIMIT 1`).Scan(&origin); err != nil {
		t.Fatal(err)
	}
	if origin != "" {
		t.Fatalf("startup rewrote legacy tree origin %q", origin)
	}
	var indexExists bool
	if err := store.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master
		WHERE type='index' AND name='results_tree_scope')`).Scan(&indexExists); err != nil {
		t.Fatal(err)
	}
	if indexExists {
		t.Fatal("startup built the deferred tree index over legacy rows")
	}
	pendingStats, err := store.StorageStats(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !pendingStats.MaintenanceRequired || pendingStats.MaintenanceReason != "legacy_tree_metadata" {
		t.Fatalf("legacy maintenance state = required:%t reason:%q",
			pendingStats.MaintenanceRequired, pendingStats.MaintenanceReason)
	}
	if _, err := store.ExploreResultTree(t.Context(), session.ID, ResultExplorerQuery{Limit: 10}, resultTreeScope{}); !errors.Is(err, errResultTreeMigrationPending) {
		t.Fatalf("tree query before maintenance error = %v, want migration pending", err)
	}
	if _, err := store.db.Exec(`DROP TRIGGER reject_startup_tree_backfill`); err != nil {
		t.Fatal(err)
	}
	if err := store.Compact(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT tree_origin FROM results LIMIT 1`).Scan(&origin); err != nil {
		t.Fatal(err)
	}
	if origin != "https://example.test" {
		t.Fatalf("compacted tree origin = %q", origin)
	}
	if err := store.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master
		WHERE type='index' AND name='results_tree_scope')`).Scan(&indexExists); err != nil {
		t.Fatal(err)
	}
	if !indexExists {
		t.Fatal("explicit compaction did not create the tree index")
	}
	completedStats, err := store.StorageStats(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if completedStats.MaintenanceRequired || completedStats.MaintenanceReason != "" {
		t.Fatalf("maintenance remained pending after compaction: %+v", completedStats)
	}
	page, err := store.ExploreResultTree(t.Context(), session.ID, ResultExplorerQuery{Limit: 10}, resultTreeScope{})
	if err != nil || len(page.Nodes) != 1 || page.Nodes[0].Name != "https://example.test" {
		t.Fatalf("tree after maintenance = %#v, err=%v", page, err)
	}
}

func TestLegacyExplorerIndexRemainsUntilSummaryCompaction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	session := testSession("legacy-summary")
	if err := store.CreateSession(t.Context(), &session); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.InsertResultBatch(t.Context(), session.ID, []StoredResult{{
		URL: "https://example.test/hit", StatusCode: 200, ContentType: "text/html",
		ContentLength: 42, ContentWords: 3, ContentLines: 2, CreatedAt: time.Now().UTC(),
	}}, nil); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DELETE FROM result_summaries`,
		`DELETE FROM result_status_counts`,
		`DELETE FROM result_content_type_counts`,
		`DELETE FROM result_dimension_summaries`,
		`DELETE FROM storage_migrations WHERE name='result_summaries_v1'`,
		`CREATE INDEX results_explorer_metrics ON results(session_id,status,content_type,length,words,lines,id)`,
	} {
		if _, err := store.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	var indexExists bool
	if err := store.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master
		WHERE type='index' AND name='results_explorer_metrics')`).Scan(&indexExists); err != nil {
		t.Fatal(err)
	}
	if !indexExists {
		t.Fatal("startup removed the only covering index before legacy summaries existed")
	}
	page, err := store.ExploreResults(t.Context(), session.ID, ResultExplorerQuery{Limit: 10})
	if err != nil || page.Total != 1 || page.Facets.Status["200"] != 1 {
		t.Fatalf("legacy explorer before compaction = %#v, err=%v", page, err)
	}
	if err := store.Compact(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master
		WHERE type='index' AND name='results_explorer_metrics')`).Scan(&indexExists); err != nil {
		t.Fatal(err)
	}
	if indexExists {
		t.Fatal("explicit compaction retained obsolete metrics index")
	}
}

func TestLegacyTreeEndpointRequestsExplicitMaintenance(t *testing.T) {
	server, err := New(Config{DatabasePath: filepath.Join(t.TempDir(), "web.db"), Password: "secret", PlainHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.store.Close() })
	session := testSession("legacy-tree-endpoint")
	if err := server.store.CreateSession(t.Context(), &session); err != nil {
		t.Fatal(err)
	}
	if _, _, err := server.store.InsertResultBatch(t.Context(), session.ID, []StoredResult{{
		URL: "https://example.test/item", Host: "example.test", StatusCode: 200, CreatedAt: time.Now().UTC(),
	}}, nil); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DROP INDEX results_tree_scope`,
		`DELETE FROM storage_migrations WHERE name='result_tree_metadata_v1'`,
		`UPDATE results SET tree_origin='',tree_path_key='',tree_suffix=''`,
	} {
		if _, err := server.store.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authenticatedRequest(http.MethodGet,
		"/api/v1/sessions/"+session.ID+"/results/tree", nil))
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "storage_maintenance_required") {
		t.Fatalf("legacy tree endpoint = %d, body=%s", recorder.Code, recorder.Body.String())
	}
}
