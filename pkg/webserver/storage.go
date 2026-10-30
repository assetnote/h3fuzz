package webserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const storageWarningThresholdBytes int64 = 1 << 30 // 1 GiB
const legacyEstimatedBytesPerResult int64 = 900

var errStorageScansActive = errors.New("database compaction is unavailable while a scan is active")
var errStorageMaintenance = errors.New("database maintenance is in progress")
var errStorageCheckpointBusy = errors.New("SQLite WAL checkpoint is busy")
var errStorageInsufficientSpace = errors.New("insufficient free disk space for SQLite compaction")

// StorageStats describes the durable web console store. Raw response bodies
// are deliberately not persisted; result metadata, optional scraper output,
// terminal history, scan configuration, and indexes account for the usage.
type StorageStats struct {
	DatabaseBytes           int64                 `json:"database_bytes"`
	WALBytes                int64                 `json:"wal_bytes"`
	TotalBytes              int64                 `json:"total_bytes"`
	ReclaimableBytes        int64                 `json:"reclaimable_bytes"`
	WarningThresholdBytes   int64                 `json:"warning_threshold_bytes"`
	Warning                 bool                  `json:"warning"`
	ResponseBodiesStored    bool                  `json:"response_bodies_stored"`
	CompactionAvailable     bool                  `json:"compaction_available"`
	CompactionBlockedReason string                `json:"compaction_blocked_reason,omitempty"`
	ActiveScanCount         int64                 `json:"active_scan_count"`
	Sessions                []StorageSessionUsage `json:"sessions"`
	AvailableDiskBytes      int64                 `json:"available_disk_bytes"`
	CompactionRequiredBytes int64                 `json:"compaction_required_bytes"`
	MaintenanceRequired     bool                  `json:"maintenance_required"`
	MaintenanceReason       string                `json:"maintenance_reason,omitempty"`
}

// StorageSessionUsage attributes the dominant result and index payload to one
// session so users can remove the scans that will reclaim the most space.
// EstimatedBytes excludes shared schema pages and is intentionally approximate.
type StorageSessionUsage struct {
	SessionID      string `json:"session_id"`
	Name           string `json:"name"`
	Status         string `json:"status"`
	ResultCount    int64  `json:"result_count"`
	EstimatedBytes int64  `json:"estimated_bytes"`
}

func (s *Store) StorageStats(ctx context.Context) (StorageStats, error) {
	stats := StorageStats{WarningThresholdBytes: storageWarningThresholdBytes, CompactionAvailable: true, AvailableDiskBytes: -1}
	var pageSize, freePages int64
	if err := s.db.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&pageSize); err != nil {
		return stats, fmt.Errorf("read SQLite page size: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `PRAGMA freelist_count`).Scan(&freePages); err != nil {
		return stats, fmt.Errorf("read SQLite free-page count: %w", err)
	}
	stats.ReclaimableBytes = pageSize * freePages

	path, err := s.mainDatabasePath(ctx)
	if err != nil {
		return stats, err
	}
	if path != "" {
		stats.DatabaseBytes, err = storageFileSize(path)
		if err != nil {
			return stats, fmt.Errorf("read SQLite database size: %w", err)
		}
		stats.WALBytes, err = storageFileSize(path + "-wal")
		if err != nil {
			return stats, fmt.Errorf("read SQLite WAL size: %w", err)
		}
		stats.AvailableDiskBytes, err = availableDiskBytes(filepath.Dir(path))
		if err != nil {
			return stats, fmt.Errorf("read available database disk space: %w", err)
		}
	}
	stats.CompactionRequiredBytes = stats.DatabaseBytes - stats.ReclaimableBytes + (64 << 20)
	if stats.CompactionRequiredBytes < 0 {
		stats.CompactionRequiredBytes = 0
	}
	stats.TotalBytes = stats.DatabaseBytes + stats.WALBytes
	stats.Warning = stats.TotalBytes >= stats.WarningThresholdBytes
	stats.ActiveScanCount, err = s.activeScanCount(ctx)
	if err != nil {
		return stats, err
	}
	if stats.ActiveScanCount > 0 {
		stats.CompactionAvailable = false
		stats.CompactionBlockedReason = "active_scans"
	}
	treeReady, err := s.storageMigrationComplete(ctx, resultTreeMetadataMigration)
	if err != nil {
		return stats, fmt.Errorf("read result tree migration state: %w", err)
	}
	summariesReady, err := s.storageMigrationComplete(ctx, resultSummaryMigration)
	if err != nil {
		return stats, fmt.Errorf("read result summary migration state: %w", err)
	}
	if !treeReady {
		stats.MaintenanceRequired = true
		stats.MaintenanceReason = "legacy_tree_metadata"
	} else if !summariesReady {
		stats.MaintenanceRequired = true
		stats.MaintenanceReason = "legacy_result_summaries"
	}
	rows, err := s.db.QueryContext(ctx, `SELECT sessions.id,sessions.name,sessions.status,
		CASE WHEN result_summaries.session_id IS NULL
			THEN MAX(sessions.result_count,COALESCE(result_ranges.total,0)) ELSE result_summaries.total END,
		CASE WHEN result_summaries.session_id IS NULL
			THEN MAX(sessions.result_count,COALESCE(result_ranges.total,0))*? ELSE result_summaries.estimated_bytes END
		FROM sessions LEFT JOIN result_summaries ON result_summaries.session_id=sessions.id
		LEFT JOIN (SELECT session_id,SUM(last_result_id-first_result_id+1) AS total
			FROM terminal_result_ranges GROUP BY session_id) AS result_ranges ON result_ranges.session_id=sessions.id
		ORDER BY 5 DESC,sessions.updated_at DESC`, legacyEstimatedBytesPerResult)
	if err != nil {
		return stats, fmt.Errorf("read per-session storage usage: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var usage StorageSessionUsage
		if err := rows.Scan(&usage.SessionID, &usage.Name, &usage.Status, &usage.ResultCount, &usage.EstimatedBytes); err != nil {
			return stats, fmt.Errorf("scan per-session storage usage: %w", err)
		}
		stats.Sessions = append(stats.Sessions, usage)
	}
	if err := rows.Err(); err != nil {
		return stats, fmt.Errorf("read per-session storage usage: %w", err)
	}
	return stats, nil
}

func (s *Store) activeScanCount(ctx context.Context) (int64, error) {
	var count int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions
		WHERE status IN (?, ?, ?, ?)`, StatusStarting, StatusRunning, StatusPaused, StatusStopping).Scan(&count); err != nil {
		return 0, fmt.Errorf("check active scans before compaction: %w", err)
	}
	return count, nil
}

func (s *Store) mainDatabasePath(ctx context.Context) (string, error) {
	rows, err := s.db.QueryContext(ctx, `PRAGMA database_list`)
	if err != nil {
		return "", fmt.Errorf("read SQLite database path: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var sequence int
		var name, path string
		if err := rows.Scan(&sequence, &name, &path); err != nil {
			return "", fmt.Errorf("read SQLite database path: %w", err)
		}
		if name == "main" {
			return path, nil
		}
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("read SQLite database path: %w", err)
	}
	return "", nil
}

func storageFileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

type sqliteMaintenanceConn interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// checkpointWAL validates the result row returned by SQLite. PRAGMA
// wal_checkpoint does not necessarily return an SQL error when a reader blocks
// a checkpoint; it reports that condition in the first result column instead.
// Discarding that row would let the API claim compaction succeeded while a
// large WAL still occupied disk.
func checkpointWAL(ctx context.Context, conn sqliteMaintenanceConn, stage string) error {
	var busy, logFrames, checkpointedFrames int64
	if err := conn.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).
		Scan(&busy, &logFrames, &checkpointedFrames); err != nil {
		return fmt.Errorf("checkpoint SQLite WAL %s compaction: %w", stage, err)
	}
	if busy != 0 || logFrames != checkpointedFrames {
		return fmt.Errorf("%w: %s compaction (busy=%d, log_frames=%d, checkpointed_frames=%d)",
			errStorageCheckpointBusy, stage, busy, logFrames, checkpointedFrames)
	}
	return nil
}

// compactResultPayloads upgrades legacy rows in bounded transactions. New
// writes already use these compact encodings; this path lets an explicit
// maintenance operation recover the same space from an existing large DB.
func compactResultPayloads(ctx context.Context, conn sqliteMaintenanceConn) error {
	var maxID int64
	if err := conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),0) FROM results`).Scan(&maxID); err != nil {
		return fmt.Errorf("inspect result payloads before compaction: %w", err)
	}
	const chunkSize int64 = 50_000
	for after := int64(0); after < maxID; after += chunkSize {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `UPDATE results SET
			response_fingerprint=h3fuzz_compact_fingerprint(response_fingerprint),
			input_json=h3fuzz_compact_input(input_json)
			WHERE id>? AND id<=? AND (
				(typeof(response_fingerprint)='text' AND length(response_fingerprint)=71
					AND substr(response_fingerprint,1,7)='sha256:')
				OR instr(CAST(input_json AS TEXT),'"FFUFHASH"')>0
			)`, after, after+chunkSize); err != nil {
			return fmt.Errorf("compact legacy result payloads: %w", err)
		}
	}
	return nil
}

func (s *Store) Compact(ctx context.Context) error {
	active, err := s.activeScanCount(ctx)
	if err != nil {
		return err
	}
	if active > 0 {
		return errStorageScansActive
	}
	stats, err := s.StorageStats(ctx)
	if err != nil {
		return err
	}
	if stats.AvailableDiskBytes >= 0 && stats.AvailableDiskBytes < stats.CompactionRequiredBytes {
		return fmt.Errorf("%w: need approximately %d bytes, have %d bytes",
			errStorageInsufficientSpace, stats.CompactionRequiredBytes, stats.AvailableDiskBytes)
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("reserve SQLite maintenance connection: %w", err)
	}
	defer conn.Close()
	if err := checkpointWAL(ctx, conn, "before"); err != nil {
		return err
	}
	if err := compactResultPayloads(ctx, conn); err != nil {
		return err
	}
	if err := s.completeResultTreeMigration(ctx); err != nil {
		return err
	}
	if err := s.backfillResultSummaries(ctx); err != nil {
		return fmt.Errorf("backfill legacy result summaries during compaction: %w", err)
	}
	if err := s.completeResultSummaryMigration(ctx); err != nil {
		return err
	}
	// Existing databases can only change from NONE to INCREMENTAL when VACUUM
	// rewrites the file. New databases already have this mode from migration.
	if _, err := conn.ExecContext(ctx, `PRAGMA auto_vacuum=INCREMENTAL`); err != nil {
		return fmt.Errorf("enable incremental SQLite vacuum: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `VACUUM`); err != nil {
		return fmt.Errorf("compact SQLite database: %w", err)
	}
	// VACUUM itself can populate the WAL. A second validated truncate is what
	// makes a successful response describe the final on-disk state.
	if err := checkpointWAL(ctx, conn, "after"); err != nil {
		return err
	}
	return nil
}

// reserveWebOperation keeps database-backed API handlers out of SQLite
// maintenance. It also lets maintenance drain readers that could pin a WAL
// snapshot and make a checkpoint or VACUUM fail.
// The double-check avoids a race between observing maintenance=false and
// acquiring the shared gate while a compaction is being reserved.
func (m *Manager) reserveWebOperation() (func(), error) {
	m.mu.Lock()
	if m.shuttingDown {
		m.mu.Unlock()
		return nil, fmt.Errorf("web session manager is shutting down")
	}
	if m.maintenance || m.maintenancePending {
		m.mu.Unlock()
		return nil, errStorageMaintenance
	}
	m.mu.Unlock()

	m.maintenanceGate.RLock()
	m.mu.Lock()
	if m.shuttingDown {
		m.mu.Unlock()
		m.maintenanceGate.RUnlock()
		return nil, fmt.Errorf("web session manager is shutting down")
	}
	if m.maintenance || m.maintenancePending {
		m.mu.Unlock()
		m.maintenanceGate.RUnlock()
		return nil, errStorageMaintenance
	}
	m.mu.Unlock()
	return m.maintenanceGate.RUnlock, nil
}

// compactStorage reserves the scan lifecycle while VACUUM runs. The store's
// persisted-state check remains a second line of defence for interrupted or
// externally manipulated databases.
func (m *Manager) compactStorage(ctx context.Context) error {
	m.mu.Lock()
	if m.shuttingDown {
		m.mu.Unlock()
		return fmt.Errorf("web session manager is shutting down")
	}
	if m.maintenance || m.maintenancePending {
		m.mu.Unlock()
		return errStorageMaintenance
	}
	if len(m.active) != 0 || len(m.starting) != 0 {
		m.mu.Unlock()
		return errStorageScansActive
	}
	// Mark the reservation before waiting for older database handlers. New web
	// operations fail promptly, while handlers that already hold the shared gate
	// are allowed to finish normally.
	m.maintenancePending = true
	m.mu.Unlock()
	m.maintenanceGate.Lock()
	maintenanceStarted := false
	defer func() {
		m.mu.Lock()
		if maintenanceStarted {
			m.maintenance = false
		}
		m.maintenancePending = false
		m.mu.Unlock()
		m.maintenanceGate.Unlock()
	}()
	// An operation that already held the shared gate may have started a
	// scan before maintenance was reserved. Drain it, then re-check the runtime
	// maps before touching SQLite.
	m.mu.Lock()
	active := len(m.active) != 0 || len(m.starting) != 0
	shuttingDown := m.shuttingDown
	m.mu.Unlock()
	if shuttingDown {
		return fmt.Errorf("web session manager is shutting down")
	}
	if active {
		return errStorageScansActive
	}
	m.mu.Lock()
	m.maintenancePending = false
	m.maintenance = true
	maintenanceStarted = true
	m.mu.Unlock()
	return m.store.Compact(ctx)
}

// protectStorageMaintenance serializes every database-backed API request with
// compaction. Static assets, immutable metadata, health, and the long-lived
// websocket are exempt; the compaction endpoint owns the exclusive reservation.
func (s *Server) protectStorageMaintenance(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		exempt := !strings.HasPrefix(r.URL.Path, "/api/") ||
			r.URL.Path == "/api/v1/health" || r.URL.Path == "/api/v1/profiles" ||
			r.URL.Path == "/api/v1/ws" ||
			(r.Method == http.MethodPost && r.URL.Path == "/api/v1/storage/compact")
		if exempt {
			next.ServeHTTP(w, r)
			return
		}
		release, err := s.manager.reserveWebOperation()
		if err != nil {
			if errors.Is(err, errStorageMaintenance) {
				writeAPIError(w, http.StatusConflict, "maintenance_in_progress", err.Error())
				return
			}
			writeAPIError(w, http.StatusServiceUnavailable, "manager_unavailable", err.Error())
			return
		}
		defer release()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleStorageStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.StorageStats(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "storage_stats_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (s *Server) handleCompactStorage(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Confirm bool `json:"confirm"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	if !request.Confirm {
		writeAPIError(w, http.StatusBadRequest, "compact_confirmation_required", "database compaction requires explicit confirmation")
		return
	}
	if err := s.manager.compactStorage(r.Context()); err != nil {
		if errors.Is(err, errStorageScansActive) {
			writeAPIError(w, http.StatusConflict, "scans_active", err.Error())
			return
		}
		if errors.Is(err, errStorageMaintenance) {
			writeAPIError(w, http.StatusConflict, "maintenance_in_progress", err.Error())
			return
		}
		if errors.Is(err, errStorageCheckpointBusy) {
			writeAPIError(w, http.StatusConflict, "storage_checkpoint_busy", err.Error())
			return
		}
		if errors.Is(err, errStorageInsufficientSpace) {
			writeAPIError(w, http.StatusInsufficientStorage, "storage_insufficient_space", err.Error())
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "storage_compact_failed", err.Error())
		return
	}
	s.handleStorageStats(w, r)
}
