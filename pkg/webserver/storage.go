package webserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
)

const storageWarningThresholdBytes int64 = 1 << 30 // 1 GiB

var errStorageScansActive = errors.New("database compaction is unavailable while a scan is active")
var errStorageMaintenance = errors.New("database maintenance is in progress")
var errStorageCheckpointBusy = errors.New("SQLite WAL checkpoint is busy")

// StorageStats describes the durable web console store. Raw response bodies
// are deliberately not persisted; result metadata, optional scraper output,
// terminal history, scan configuration, and indexes account for the usage.
type StorageStats struct {
	DatabaseBytes         int64 `json:"database_bytes"`
	WALBytes              int64 `json:"wal_bytes"`
	TotalBytes            int64 `json:"total_bytes"`
	ReclaimableBytes      int64 `json:"reclaimable_bytes"`
	WarningThresholdBytes int64 `json:"warning_threshold_bytes"`
	Warning               bool  `json:"warning"`
	ResponseBodiesStored  bool  `json:"response_bodies_stored"`
}

func (s *Store) StorageStats(ctx context.Context) (StorageStats, error) {
	stats := StorageStats{WarningThresholdBytes: storageWarningThresholdBytes}
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
	}
	stats.TotalBytes = stats.DatabaseBytes + stats.WALBytes
	stats.Warning = stats.TotalBytes >= stats.WarningThresholdBytes
	return stats, nil
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

func (s *Store) Compact(ctx context.Context) error {
	var active bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM sessions WHERE status IN (?, ?, ?, ?) LIMIT 1
	)`, StatusStarting, StatusRunning, StatusPaused, StatusStopping).Scan(&active); err != nil {
		return fmt.Errorf("check active scans before compaction: %w", err)
	}
	if active {
		return errStorageScansActive
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("reserve SQLite maintenance connection: %w", err)
	}
	defer conn.Close()
	if err := checkpointWAL(ctx, conn, "before"); err != nil {
		return err
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

// reserveWebMutation keeps mutating API handlers out of SQLite maintenance.
// The double-check avoids a race between observing maintenance=false and
// acquiring the shared gate while a compaction is being reserved.
func (m *Manager) reserveWebMutation() (func(), error) {
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
	// Mark the reservation before waiting for older mutating handlers. New web
	// mutations fail promptly, while handlers that already hold the shared gate
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
	// A mutating request that already held the shared gate may have started a
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

// protectStorageMaintenance serializes every mutating API request with
// compaction. Read-only APIs, including storage statistics, remain available.
// The compaction endpoint owns the exclusive reservation itself.
func (s *Server) protectStorageMaintenance(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		safeMethod := r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions
		if safeMethod || !strings.HasPrefix(r.URL.Path, "/api/") ||
			(r.Method == http.MethodPost && r.URL.Path == "/api/v1/storage/compact") {
			next.ServeHTTP(w, r)
			return
		}
		release, err := s.manager.reserveWebMutation()
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
		writeAPIError(w, http.StatusInternalServerError, "storage_compact_failed", err.Error())
		return
	}
	s.handleStorageStats(w, r)
}
