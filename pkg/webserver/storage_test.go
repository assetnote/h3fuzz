package webserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStorageStatsEndpointReportsOnDiskUsageWithoutClaimingResponseBodies(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "web.db")
	server, err := New(Config{DatabasePath: databasePath, Password: "secret", PlainHTTP: true})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	t.Cleanup(func() { _ = server.store.Close() })

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authenticatedRequest(http.MethodGet, "/api/v1/storage", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	var stats StorageStats
	if err := json.Unmarshal(recorder.Body.Bytes(), &stats); err != nil {
		t.Fatalf("decode storage stats: %v", err)
	}
	info, err := os.Stat(databasePath)
	if err != nil {
		t.Fatalf("stat database: %v", err)
	}
	if stats.DatabaseBytes != info.Size() {
		t.Fatalf("database_bytes = %d, want %d", stats.DatabaseBytes, info.Size())
	}
	if stats.TotalBytes != stats.DatabaseBytes+stats.WALBytes {
		t.Fatalf("total_bytes = %d, database + WAL = %d", stats.TotalBytes, stats.DatabaseBytes+stats.WALBytes)
	}
	if stats.WarningThresholdBytes <= 0 {
		t.Fatalf("warning_threshold_bytes = %d, want positive threshold", stats.WarningThresholdBytes)
	}
	if stats.ResponseBodiesStored {
		t.Fatal("storage endpoint incorrectly reports that response bodies are stored")
	}
	if stats.Warning {
		t.Fatalf("new database unexpectedly exceeds warning threshold: %+v", stats)
	}
	if !stats.CompactionAvailable || stats.ActiveScanCount != 0 || stats.CompactionBlockedReason != "" {
		t.Fatalf("idle compaction eligibility = available:%t active:%d reason:%q",
			stats.CompactionAvailable, stats.ActiveScanCount, stats.CompactionBlockedReason)
	}
	if stats.MaintenanceRequired || stats.MaintenanceReason != "" {
		t.Fatalf("new database unexpectedly requires maintenance: %+v", stats)
	}
}

func TestStorageCompactRequiresConfirmationAndRejectsActiveScans(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "web.db")
	server, err := New(Config{DatabasePath: databasePath, Password: "secret", PlainHTTP: true})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	t.Cleanup(func() { _ = server.store.Close() })

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authenticatedRequest(http.MethodPost, "/api/v1/storage/compact", bytes.NewBufferString(`{"confirm":false}`)))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("unconfirmed status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	session, err := server.manager.Create(t.Context(), CreateSessionRequest{
		Target:   "https://target.example/FUZZ",
		Wordlist: filepath.Join("..", "..", "final.txt"),
		Profile:  "dir",
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	now := time.Now().UTC()
	if err := server.store.UpdateSessionStatus(t.Context(), session.ID, StatusRunning, "", &now, nil); err != nil {
		t.Fatalf("mark session running: %v", err)
	}
	stats, err := server.store.StorageStats(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if stats.CompactionAvailable || stats.ActiveScanCount != 1 || stats.CompactionBlockedReason != "active_scans" {
		t.Fatalf("active compaction eligibility = available:%t active:%d reason:%q",
			stats.CompactionAvailable, stats.ActiveScanCount, stats.CompactionBlockedReason)
	}

	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authenticatedRequest(http.MethodPost, "/api/v1/storage/compact", bytes.NewBufferString(`{"confirm":true}`)))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("active status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	completed := time.Now().UTC()
	if err := server.store.UpdateSessionStatus(t.Context(), session.ID, StatusCompleted, "", &now, &completed); err != nil {
		t.Fatalf("mark session completed: %v", err)
	}
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authenticatedRequest(http.MethodPost, "/api/v1/storage/compact", bytes.NewBufferString(`{"confirm":true}`)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("compact status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if _, err := server.store.GetSession(t.Context(), session.ID); err != nil {
		t.Fatalf("compaction must not delete sessions: %v", err)
	}
}

func TestReopenReconcilesInterruptedScanForCompaction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	session := testSession("interrupted-scan")
	session.Status = StatusRunning
	if err := store.CreateSession(t.Context(), &session); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	persisted, err := store.GetSession(t.Context(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != StatusFailed {
		t.Fatalf("interrupted status = %q, want failed", persisted.Status)
	}
	stats, err := store.StorageStats(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !stats.CompactionAvailable || stats.ActiveScanCount != 0 {
		t.Fatalf("reconciled compaction eligibility = available:%t active:%d", stats.CompactionAvailable, stats.ActiveScanCount)
	}
}

func TestCheckpointWALReportsBusyResultInsteadOfFalseSuccess(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "web.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	first := testSession("checkpoint-reader")
	if err := store.CreateSession(t.Context(), &first); err != nil {
		t.Fatal(err)
	}
	reader, err := store.db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	// The production DSN uses immediate write transactions. Start this fixture
	// explicitly as a deferred read transaction so it pins a WAL snapshot
	// without preventing the newer writer commit needed by the checkpoint test.
	if _, err := reader.ExecContext(t.Context(), `BEGIN DEFERRED`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = reader.ExecContext(context.Background(), `ROLLBACK`)
	}()
	var count int
	if err := reader.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM sessions`).Scan(&count); err != nil {
		t.Fatal(err)
	}

	// Commit a newer WAL frame while the read transaction retains its older
	// snapshot, making TRUNCATE report busy in its result row.
	second := testSession("checkpoint-writer")
	if err := store.CreateSession(t.Context(), &second); err != nil {
		t.Fatal(err)
	}
	checkpointConn, err := store.db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer checkpointConn.Close()
	if _, err := checkpointConn.ExecContext(t.Context(), `PRAGMA busy_timeout=25`); err != nil {
		t.Fatal(err)
	}
	if err := checkpointWAL(t.Context(), checkpointConn, "during test"); !errors.Is(err, errStorageCheckpointBusy) {
		t.Fatalf("checkpoint error = %v, want %v", err, errStorageCheckpointBusy)
	}
	if _, err := reader.ExecContext(t.Context(), `ROLLBACK`); err != nil {
		t.Fatal(err)
	}
	if err := checkpointWAL(t.Context(), checkpointConn, "after reader"); err != nil {
		t.Fatalf("checkpoint after reader release: %v", err)
	}
}

func TestStorageCompactReclaimsDeletedPagesAndTruncatesFinalWAL(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "web.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	session := testSession("storage-reclaim")
	if err := store.CreateSession(t.Context(), &session); err != nil {
		t.Fatal(err)
	}
	results := make([]StoredResult, 1500)
	payload := []byte(strings.Repeat("x", 768))
	now := time.Now().UTC()
	for index := range results {
		results[index] = StoredResult{
			Input:      map[string][]byte{"FUZZ": payload},
			Position:   index,
			StatusCode: http.StatusOK,
			URL:        "https://storage.example/" + strings.Repeat("p", 96),
			Host:       "storage.example",
			CreatedAt:  now,
		}
	}
	if _, _, err := store.InsertResultBatch(t.Context(), session.ID, results, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteSession(t.Context(), session.ID); err != nil {
		t.Fatal(err)
	}
	before, err := store.StorageStats(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Compact(t.Context()); err != nil {
		t.Fatal(err)
	}
	after, err := store.StorageStats(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if after.WALBytes != 0 {
		t.Fatalf("final WAL size = %d, want 0 after successful compaction", after.WALBytes)
	}
	if after.ReclaimableBytes != 0 {
		t.Fatalf("reclaimable bytes = %d, want 0 after VACUUM", after.ReclaimableBytes)
	}
	if after.TotalBytes >= before.TotalBytes {
		t.Fatalf("compaction did not reduce on-disk storage: before=%+v after=%+v", before, after)
	}
}

func TestStorageMaintenanceDrainsExistingMutationAndRejectsNewWrites(t *testing.T) {
	server, err := New(Config{DatabasePath: filepath.Join(t.TempDir(), "web.db"), Password: "secret", PlainHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.store.Close() })

	release, err := server.manager.reserveWebOperation()
	if err != nil {
		t.Fatal(err)
	}
	compactDone := make(chan error, 1)
	go func() { compactDone <- server.manager.compactStorage(context.Background()) }()

	deadline := time.Now().Add(time.Second)
	for {
		server.manager.mu.Lock()
		pending := server.manager.maintenancePending
		server.manager.mu.Unlock()
		if pending {
			break
		}
		if time.Now().After(deadline) {
			release()
			t.Fatal("compaction did not reserve maintenance")
		}
		time.Sleep(time.Millisecond)
	}

	// Even read-only DB telemetry is rejected while maintenance is waiting so it
	// cannot acquire a snapshot between the drain and VACUUM.
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authenticatedRequest(http.MethodGet, "/api/v1/storage", nil))
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "maintenance_in_progress") {
		t.Fatalf("storage telemetry during maintenance = %d, body=%s", recorder.Code, recorder.Body.String())
	}

	// A new mutating API call is rejected instead of racing or waiting behind a
	// potentially long multi-gigabyte VACUUM.
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authenticatedRequest(http.MethodPost, "/api/v1/sessions", bytes.NewBufferString(`{}`)))
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "maintenance_in_progress") {
		t.Fatalf("mutation during maintenance = %d, body=%s", recorder.Code, recorder.Body.String())
	}

	select {
	case err := <-compactDone:
		release()
		t.Fatalf("compaction completed before the existing mutation drained: %v", err)
	default:
	}
	release()
	select {
	case err := <-compactDone:
		if err != nil {
			t.Fatalf("compaction after mutation drained: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("compaction did not finish after the existing mutation drained")
	}

	releaseAfter, err := server.manager.reserveWebOperation()
	if err != nil {
		t.Fatalf("mutation guard remained locked after compaction: %v", err)
	}
	releaseAfter()
}

func TestStorageMaintenanceDrainsExistingExportReaderAndRejectsNewReads(t *testing.T) {
	server, err := New(Config{DatabasePath: filepath.Join(t.TempDir(), "web.db"), Password: "secret", PlainHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.store.Close() })

	readerStarted := make(chan struct{})
	releaseReader := make(chan struct{})
	readerDone := make(chan struct{})
	guarded := server.protectStorageMaintenance(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(readerStarted)
		<-releaseReader
		w.WriteHeader(http.StatusOK)
	}))
	go func() {
		defer close(readerDone)
		guarded.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet,
			"/api/v1/sessions/session/export?format=json", nil))
	}()
	<-readerStarted

	compactDone := make(chan error, 1)
	go func() { compactDone <- server.manager.compactStorage(context.Background()) }()
	deadline := time.Now().Add(time.Second)
	for {
		server.manager.mu.Lock()
		pending := server.manager.maintenancePending
		server.manager.mu.Unlock()
		if pending {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("compaction did not wait for the active export reader")
		}
		time.Sleep(time.Millisecond)
	}

	recorder := httptest.NewRecorder()
	guarded.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("read during maintenance = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	select {
	case err := <-compactDone:
		t.Fatalf("compaction completed before export reader drained: %v", err)
	default:
	}
	close(releaseReader)
	<-readerDone
	select {
	case err := <-compactDone:
		if err != nil {
			t.Fatalf("compaction after export reader drained: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("compaction did not finish after export reader drained")
	}
}

func TestWebSocketSnapshotIsRejectedDuringStorageMaintenance(t *testing.T) {
	server, err := New(Config{DatabasePath: filepath.Join(t.TempDir(), "web.db"), Password: "secret", PlainHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.store.Close() })
	server.manager.mu.Lock()
	server.manager.maintenance = true
	server.manager.mu.Unlock()
	t.Cleanup(func() {
		server.manager.mu.Lock()
		server.manager.maintenance = false
		server.manager.mu.Unlock()
	})
	recorder := httptest.NewRecorder()
	server.handleWebSocket(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/ws", nil))
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "maintenance_in_progress") {
		t.Fatalf("websocket during maintenance = %d, body=%s", recorder.Code, recorder.Body.String())
	}
}
