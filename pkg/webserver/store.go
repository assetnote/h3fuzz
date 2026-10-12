package webserver

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
	"modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")

const terminalResultSQLFunction = "h3fuzz_result_terminal"

func init() {
	sqlite.MustRegisterDeterministicScalarFunction(terminalResultSQLFunction, 6,
		func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			if len(args) != 6 {
				return nil, fmt.Errorf("%s expects 6 arguments", terminalResultSQLFunction)
			}
			url, ok := args[0].(string)
			if !ok {
				return nil, fmt.Errorf("%s URL argument is %T", terminalResultSQLFunction, args[0])
			}
			values := make([]int64, 5)
			for index := range values {
				value, valueOK := args[index+1].(int64)
				if !valueOK {
					return nil, fmt.Errorf("%s numeric argument %d is %T", terminalResultSQLFunction, index+1, args[index+1])
				}
				values[index] = value
			}
			return formatResultTerminalFields(url, values[0], values[1], values[2], values[3], values[4]), nil
		})
}

func formatResultTerminalFields(url string, status, size, words, lines, durationNS int64) string {
	return fmt.Sprintf("%s [Status: %d, Size: %d, Words: %d, Lines: %d, Duration: %s]",
		url, status, size, words, lines, time.Duration(durationNS))
}

func formatResultTerminal(result StoredResult) string {
	return formatResultTerminalFields(result.URL, result.StatusCode, result.ContentLength,
		result.ContentWords, result.ContentLines, result.DurationNS)
}

type Store struct {
	db *sql.DB

	resultIndexMu    sync.Mutex
	resultIndexUsers int
}

var deferredResultIndexes = []struct {
	name string
	sql  string
}{
	{name: "results_session_status_cursor", sql: `CREATE INDEX IF NOT EXISTS results_session_status_cursor
		ON results(session_id,status,id)`},
	{name: "results_session_url_cursor", sql: `CREATE INDEX IF NOT EXISTS results_session_url_cursor
		ON results(session_id,url,id DESC)`},
	{name: "results_explorer_metrics", sql: `CREATE INDEX IF NOT EXISTS results_explorer_metrics
		ON results(session_id,status,content_type,length,words,lines,id)`},
	{name: "results_tree_scope", sql: `CREATE INDEX IF NOT EXISTS results_tree_scope
		ON results(session_id,tree_origin,tree_path_key,id)`},
}

func OpenStore(path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("database path is required")
	}
	if path != ":memory:" {
		absolutePath, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("resolve web database path: %w", err)
		}
		path = absolutePath
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return nil, fmt.Errorf("create web database: %w", err)
		}
		if err := file.Close(); err != nil {
			return nil, err
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return nil, fmt.Errorf("secure web database permissions: %w", err)
		}
	}
	memoryID, err := newID()
	if err != nil {
		return nil, fmt.Errorf("create isolated memory database name: %w", err)
	}
	dsn := "file:webserver-" + memoryID + "?mode=memory&cache=shared&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_txlock=immediate"
	if path != ":memory:" {
		dsn = (&url.URL{Scheme: "file", Path: path}).String()
		dsn += "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_txlock=immediate"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	if path != ":memory:" {
		if err := os.Chmod(path, 0o600); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("secure web database permissions: %w", err)
		}
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// beginResultIngest removes secondary browsing indexes while one or more scans
// are producing findings. Maintaining those global B-trees for every response
// makes retained historical sessions throttle an otherwise fast current scan.
// The authoritative table and its session cursor remain indexed, so REST reads
// stay correct while ingestion is active; only uncommon sorts and tree queries
// may use a temporary SQLite sort until the indexes are restored.
func (s *Store) beginResultIngest(ctx context.Context) error {
	s.resultIndexMu.Lock()
	defer s.resultIndexMu.Unlock()
	if s.resultIndexUsers > 0 {
		s.resultIndexUsers++
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	for _, index := range deferredResultIndexes {
		if _, err := tx.ExecContext(ctx, `DROP INDEX IF EXISTS `+index.name); err != nil {
			return fmt.Errorf("suspend result index %s: %w", index.name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("suspend result indexes: %w", err)
	}
	s.resultIndexUsers = 1
	return nil
}

// endResultIngest restores secondary indexes after the final concurrent scan
// has drained its persistence queue. Building each index once over the finished
// data is substantially cheaper than random B-tree maintenance in the request
// hot path and keeps later browsing fully indexed.
func (s *Store) endResultIngest(ctx context.Context) error {
	s.resultIndexMu.Lock()
	defer s.resultIndexMu.Unlock()
	if s.resultIndexUsers <= 0 {
		return fmt.Errorf("result index ingest reservation is not active")
	}
	s.resultIndexUsers--
	if s.resultIndexUsers > 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	for _, index := range deferredResultIndexes {
		if _, err := tx.ExecContext(ctx, index.sql); err != nil {
			return fmt.Errorf("restore result index %s: %w", index.name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("restore result indexes: %w", err)
	}
	return nil
}

func (s *Store) migrate(ctx context.Context) error {
	statements := []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA synchronous=NORMAL`,
		`PRAGMA foreign_keys=ON`,
		`PRAGMA busy_timeout=5000`,
		`CREATE TABLE IF NOT EXISTS sessions (
            id TEXT PRIMARY KEY,
            name TEXT NOT NULL,
            target TEXT NOT NULL,
            wordlist TEXT NOT NULL,
            profile TEXT NOT NULL,
            status TEXT NOT NULL,
            options_json BLOB NOT NULL,
            result_filters_json BLOB NOT NULL DEFAULT '{}',
            explicit_flags_json BLOB NOT NULL DEFAULT '[]',
            progress_json BLOB NOT NULL,
            result_count INTEGER NOT NULL DEFAULT 0,
            sequence INTEGER NOT NULL DEFAULT 0,
			terminal_sequence INTEGER NOT NULL DEFAULT 0,
            error TEXT NOT NULL DEFAULT '',
            created_at INTEGER NOT NULL,
            updated_at INTEGER NOT NULL,
            started_at INTEGER,
			completed_at INTEGER,
			runtime_transport TEXT NOT NULL DEFAULT '',
			runtime_transport_reason TEXT NOT NULL DEFAULT '',
			response_optimization_strategy TEXT NOT NULL DEFAULT '',
			response_optimization_reason TEXT NOT NULL DEFAULT '',
			preflight_json BLOB NOT NULL DEFAULT '{}',
			error_diagnostics_json BLOB NOT NULL DEFAULT '[]'
        )`,
		`CREATE TABLE IF NOT EXISTS results (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
            input_json BLOB NOT NULL,
            position INTEGER NOT NULL,
            status INTEGER NOT NULL,
            length INTEGER NOT NULL,
            words INTEGER NOT NULL,
            lines INTEGER NOT NULL,
            content_type TEXT NOT NULL,
            redirect_location TEXT NOT NULL,
            url TEXT NOT NULL,
            duration_ns INTEGER NOT NULL,
            scraper_json BLOB NOT NULL,
            host TEXT NOT NULL,
			response_fingerprint TEXT NOT NULL DEFAULT '',
			tree_origin TEXT NOT NULL DEFAULT '',
			tree_path_key TEXT NOT NULL DEFAULT '',
			tree_suffix TEXT NOT NULL DEFAULT '',
			tree_trailing_slash INTEGER NOT NULL DEFAULT 0,
			terminal_sequence INTEGER NOT NULL DEFAULT 0,
			terminal_at INTEGER NOT NULL DEFAULT 0,
            created_at INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS results_session_cursor ON results(session_id, id)`,
		`CREATE INDEX IF NOT EXISTS results_session_status_cursor ON results(session_id, status, id)`,
		`CREATE TABLE IF NOT EXISTS terminal_log (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
            at INTEGER NOT NULL,
            level TEXT NOT NULL,
			message TEXT NOT NULL,
			terminal_sequence INTEGER NOT NULL DEFAULT 0
        )`,
		`CREATE INDEX IF NOT EXISTS terminal_session_cursor ON terminal_log(session_id, id)`,
		`CREATE TABLE IF NOT EXISTS terminal_result_ranges (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
			first_result_id INTEGER NOT NULL,
			last_result_id INTEGER NOT NULL,
			first_terminal_sequence INTEGER NOT NULL,
			last_terminal_sequence INTEGER NOT NULL,
			CHECK(first_result_id <= last_result_id),
			CHECK(first_terminal_sequence <= last_terminal_sequence),
			CHECK(last_result_id-first_result_id = last_terminal_sequence-first_terminal_sequence)
		)`,
		`CREATE INDEX IF NOT EXISTS terminal_result_ranges_sequence
			ON terminal_result_ranges(session_id,first_terminal_sequence,last_terminal_sequence)`,
		`CREATE TABLE IF NOT EXISTS settings (
            id INTEGER PRIMARY KEY CHECK (id = 1),
            value_json BLOB NOT NULL,
            updated_at INTEGER NOT NULL
        )`,
		`CREATE TABLE IF NOT EXISTS web_auth (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			password TEXT NOT NULL,
			created_at INTEGER NOT NULL
		)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize web database: %w", err)
		}
	}
	if err := s.ensureColumn(ctx, "sessions", "explicit_flags_json", "BLOB NOT NULL DEFAULT '[]'"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "sessions", "result_filters_json", "BLOB NOT NULL DEFAULT '{}'"); err != nil {
		return err
	}
	for column, declaration := range map[string]string{
		"runtime_transport":              "TEXT NOT NULL DEFAULT ''",
		"runtime_transport_reason":       "TEXT NOT NULL DEFAULT ''",
		"response_optimization_strategy": "TEXT NOT NULL DEFAULT ''",
		"response_optimization_reason":   "TEXT NOT NULL DEFAULT ''",
		"preflight_json":                 "BLOB NOT NULL DEFAULT '{}'",
		"error_diagnostics_json":         "BLOB NOT NULL DEFAULT '[]'",
		"terminal_sequence":              "INTEGER NOT NULL DEFAULT 0",
	} {
		if err := s.ensureColumn(ctx, "sessions", column, declaration); err != nil {
			return err
		}
	}
	// Directory-tree metadata is denormalized onto each result once, when the
	// result is persisted. This keeps every tree expansion from reparsing and
	// unmarshalling an entire large scan. Existing databases are backfilled
	// below before the covering prefix index is created.
	for column, declaration := range map[string]string{
		"tree_origin":         "TEXT NOT NULL DEFAULT ''",
		"tree_path_key":       "TEXT NOT NULL DEFAULT ''",
		"tree_suffix":         "TEXT NOT NULL DEFAULT ''",
		"tree_trailing_slash": "INTEGER NOT NULL DEFAULT 0",
		"terminal_sequence":   "INTEGER NOT NULL DEFAULT 0",
		"terminal_at":         "INTEGER NOT NULL DEFAULT 0",
	} {
		if err := s.ensureColumn(ctx, "results", column, declaration); err != nil {
			return err
		}
	}
	if err := s.migrateProductFeatures(ctx); err != nil {
		return err
	}
	if err := s.migrateTerminalEvents(ctx); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "results", "response_fingerprint", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.backfillResultTreeMetadata(ctx); err != nil {
		return fmt.Errorf("backfill result tree metadata: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS results_tree_scope
		ON results(session_id,tree_origin,tree_path_key,id)`); err != nil {
		return fmt.Errorf("initialize result tree index: %w", err)
	}
	// A process restart cannot resume an in-memory engine. Preserve the tab and
	// results, but make the interrupted terminal state explicit.
	now := time.Now().UTC().UnixNano()
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET status = ?, error = ?, updated_at = ?, completed_at = ?
		WHERE status IN (?, ?, ?, ?)`, StatusFailed, "h3fuzz web server restarted while the scan was active", now, now,
		StatusStarting, StatusRunning, StatusPaused, StatusStopping)
	return err
}

func (s *Store) ensureColumn(ctx context.Context, table, column, declaration string) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid int
		var name, kind string
		var notNull, primaryKey int
		var defaultValue interface{}
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return err
		}
		if name == column {
			found = true
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if found {
		return nil
	}
	_, err = s.db.ExecContext(ctx, `ALTER TABLE `+table+` ADD COLUMN `+column+` `+declaration)
	return err
}

// migrateTerminalEvents introduces one persisted order shared by lifecycle
// lines and result-backed terminal lines. Legacy rows deliberately keep a zero
// terminal_sequence and continue to use their physical row ID. Rewriting a
// large historical terminal_log here would create a correspondingly large WAL
// during startup, so only the small sessions table is advanced to the highest
// legacy cursor. Results written by the first result-backed terminal schema
// keep using their partial sequence index. Current writers use compact
// terminal_result_ranges instead, avoiding one B-tree insertion per finding.
func (s *Store) migrateTerminalEvents(ctx context.Context) error {
	if err := s.ensureColumn(ctx, "terminal_log", "terminal_sequence", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return fmt.Errorf("add terminal event sequence: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE sessions
		SET terminal_sequence=MAX(terminal_sequence, COALESCE((
			SELECT MAX(id) FROM terminal_log WHERE terminal_log.session_id=sessions.id
		), 0))`); err != nil {
		return fmt.Errorf("initialize terminal event sequence: %w", err)
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS terminal_result_ranges (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
			first_result_id INTEGER NOT NULL,
			last_result_id INTEGER NOT NULL,
			first_terminal_sequence INTEGER NOT NULL,
			last_terminal_sequence INTEGER NOT NULL,
			CHECK(first_result_id <= last_result_id),
			CHECK(first_terminal_sequence <= last_terminal_sequence),
			CHECK(last_result_id-first_result_id = last_terminal_sequence-first_terminal_sequence)
		)`,
		`CREATE INDEX IF NOT EXISTS terminal_result_ranges_sequence
			ON terminal_result_ranges(session_id,first_terminal_sequence,last_terminal_sequence)`,
		`CREATE INDEX IF NOT EXISTS results_terminal_sequence
			ON results(session_id,terminal_sequence) WHERE terminal_sequence>0`,
		`CREATE INDEX IF NOT EXISTS terminal_new_sequence
			ON terminal_log(session_id,terminal_sequence) WHERE terminal_sequence>0`,
		`CREATE INDEX IF NOT EXISTS terminal_new_level_sequence
			ON terminal_log(session_id,level,terminal_sequence) WHERE terminal_sequence>0`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize terminal event index: %w", err)
		}
	}
	return nil
}

func (s *Store) CreateSession(ctx context.Context, session *Session) error {
	options, err := json.Marshal(session.Options)
	if err != nil {
		return err
	}
	resultFilters, err := json.Marshal(session.ResultFilters)
	if err != nil {
		return err
	}
	progress, err := json.Marshal(session.Progress)
	if err != nil {
		return err
	}
	explicit, err := json.Marshal(session.ExplicitFlags)
	if err != nil {
		return err
	}
	preflight, err := json.Marshal(session.PreflightReport)
	if err != nil {
		return err
	}
	diagnostics, err := json.Marshal(session.ErrorDiagnostics)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO sessions
		(id,name,target,wordlist,profile,status,options_json,result_filters_json,explicit_flags_json,progress_json,result_count,sequence,error,created_at,updated_at,
		runtime_transport,runtime_transport_reason,response_optimization_strategy,response_optimization_reason,preflight_json,error_diagnostics_json)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, session.ID, session.Name, session.Target, session.Wordlist,
		session.Profile, session.Status, options, resultFilters, explicit, progress, session.ResultCount, session.Sequence,
		session.Error, session.CreatedAt.UnixNano(), session.UpdatedAt.UnixNano(), session.RuntimeTransport,
		session.RuntimeTransportReason, session.ResponseOptimizationStrategy, session.ResponseOptimizationReason, preflight, diagnostics)
	return err
}

func (s *Store) ListSessions(ctx context.Context) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,target,wordlist,profile,status,options_json,result_filters_json,explicit_flags_json,progress_json,
		result_count,sequence,error,created_at,updated_at,started_at,completed_at,runtime_transport,runtime_transport_reason,
		response_optimization_strategy,response_optimization_reason,preflight_json,error_diagnostics_json FROM sessions ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sessions []Session
	for rows.Next() {
		session, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	if sessions == nil {
		sessions = []Session{}
	}
	if err := s.enrichSessionOrganizations(ctx, sessions); err != nil {
		return nil, err
	}
	return sessions, rows.Err()
}

func (s *Store) GetSession(ctx context.Context, id string) (Session, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id,name,target,wordlist,profile,status,options_json,result_filters_json,explicit_flags_json,progress_json,
		result_count,sequence,error,created_at,updated_at,started_at,completed_at,runtime_transport,runtime_transport_reason,
		response_optimization_strategy,response_optimization_reason,preflight_json,error_diagnostics_json FROM sessions WHERE id=?`, id)
	session, err := scanSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err == nil {
		err = s.enrichSessionOrganization(ctx, &session)
	}
	return session, err
}

type scanner interface{ Scan(...interface{}) error }

func scanSession(row scanner) (Session, error) {
	var session Session
	var options, resultFilters, explicit, progress, preflight, diagnostics []byte
	var created, updated int64
	var started, completed sql.NullInt64
	if err := row.Scan(&session.ID, &session.Name, &session.Target, &session.Wordlist, &session.Profile,
		&session.Status, &options, &resultFilters, &explicit, &progress, &session.ResultCount, &session.Sequence, &session.Error,
		&created, &updated, &started, &completed, &session.RuntimeTransport, &session.RuntimeTransportReason,
		&session.ResponseOptimizationStrategy, &session.ResponseOptimizationReason, &preflight, &diagnostics); err != nil {
		return Session{}, err
	}
	if err := json.Unmarshal(options, &session.Options); err != nil {
		return Session{}, err
	}
	if err := json.Unmarshal(resultFilters, &session.ResultFilters); err != nil {
		return Session{}, err
	}
	if err := json.Unmarshal(explicit, &session.ExplicitFlags); err != nil {
		return Session{}, err
	}
	if err := unmarshalStoredProgress(progress, &session.Progress); err != nil {
		return Session{}, err
	}
	if err := json.Unmarshal(preflight, &session.PreflightReport); err != nil {
		return Session{}, err
	}
	if session.PreflightReport.State == "" {
		session.PreflightReport.State = PreflightNotRun
	}
	if err := json.Unmarshal(diagnostics, &session.ErrorDiagnostics); err != nil {
		return Session{}, err
	}
	if session.ErrorDiagnostics == nil {
		session.ErrorDiagnostics = []ErrorDiagnostic{}
	}
	session.CreatedAt = time.Unix(0, created).UTC()
	session.UpdatedAt = time.Unix(0, updated).UTC()
	if started.Valid {
		t := time.Unix(0, started.Int64).UTC()
		session.StartedAt = &t
	}
	if completed.Valid {
		t := time.Unix(0, completed.Int64).UTC()
		session.CompletedAt = &t
	}
	return session, nil
}

// unmarshalStoredProgress preserves useful progress for databases created by
// versions that predate completed_count. Scheduled inputs are not completions,
// so prefer the drain-aware response and terminal-error counters when those
// fields were persisted. Very old records without response_count can only fall
// back to their scheduled count, which matches the progress semantics they had
// when written.
func unmarshalStoredProgress(data []byte, progress *Progress) error {
	if err := json.Unmarshal(data, progress); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if _, present := fields["request_count"]; !present {
		progress.RequestCount = progress.ReqCount
	}
	if _, present := fields["completed_count"]; present {
		return nil
	}

	scheduled := progress.RequestCount
	if scheduled == 0 {
		scheduled = progress.ReqCount
	}
	if _, present := fields["response_count"]; !present {
		progress.CompletedCount = scheduled
		return nil
	}
	completed := progress.ResponseCount + progress.ErrorCount
	if completed < 0 {
		completed = 0
	}
	if scheduled > 0 && completed > scheduled {
		completed = scheduled
	}
	progress.CompletedCount = completed
	return nil
}

func (s *Store) UpdateSessionConfig(ctx context.Context, session Session) error {
	options, err := json.Marshal(session.Options)
	if err != nil {
		return err
	}
	explicit, err := json.Marshal(session.ExplicitFlags)
	if err != nil {
		return err
	}
	preflight, _ := json.Marshal(PreflightReport{State: PreflightNotRun})
	result, err := s.db.ExecContext(ctx, `UPDATE sessions SET name=?,target=?,wordlist=?,profile=?,options_json=?,explicit_flags_json=?,
		preflight_json=?,runtime_transport='',runtime_transport_reason='',response_optimization_strategy='',response_optimization_reason='',updated_at=?
		WHERE id=? AND status=?`, session.Name, session.Target, session.Wordlist, session.Profile, options, explicit,
		preflight, time.Now().UTC().UnixNano(), session.ID, StatusIdle)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return fmt.Errorf("session can only be edited while idle")
	}
	return nil
}

// UpdateSessionFilters persists the complete ffuf filter configuration and the
// post-start subset used for retroactive result visibility. The visible result
// count is recomputed in the same transaction, so session snapshots, result
// pages, and exports all switch to the new filter set together.
func (s *Store) UpdateSessionFilters(ctx context.Context, id string, options ffuf.ConfigOptions, resultOptions ffuf.FilterOptions, explicitFlags []string) error {
	optionsJSON, err := json.Marshal(options)
	if err != nil {
		return err
	}
	resultOptionsJSON, err := json.Marshal(resultOptions)
	if err != nil {
		return err
	}
	explicitJSON, err := json.Marshal(explicitFlags)
	if err != nil {
		return err
	}
	resultFilter, err := newStoredResultFilter(resultOptions)
	if err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	updatedAt := time.Now().UTC().UnixNano()
	result, err := tx.ExecContext(ctx, `UPDATE sessions SET options_json=?,result_filters_json=?,explicit_flags_json=?,updated_at=? WHERE id=?`,
		optionsJSON, resultOptionsJSON, explicitJSON, updatedAt, id)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return ErrNotFound
	}
	// Live additions are metric-only, so the recount does not need to decode
	// input/scraper JSON or load URLs. Keeping this scan narrow matters when a
	// broad match policy has retained hundreds of thousands of findings.
	rows, err := tx.QueryContext(ctx, `SELECT status,length,words,lines,duration_ns FROM results WHERE session_id=?`, id)
	if err != nil {
		return err
	}
	visible := int64(0)
	for rows.Next() {
		var stored StoredResult
		if scanErr := rows.Scan(&stored.StatusCode, &stored.ContentLength, &stored.ContentWords,
			&stored.ContentLines, &stored.DurationNS); scanErr != nil {
			rows.Close()
			return scanErr
		}
		keep, filterErr := resultFilter.keep(stored)
		if filterErr != nil {
			rows.Close()
			return filterErr
		}
		if keep {
			visible++
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET result_count=? WHERE id=?`, visible, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) UpdateSessionStatus(ctx context.Context, id string, status SessionStatus, errText string, started, completed *time.Time) error {
	var startedNS, completedNS interface{}
	if started != nil {
		startedNS = started.UnixNano()
	}
	if completed != nil {
		completedNS = completed.UnixNano()
	}
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET status=?,error=?,updated_at=?,
        started_at=COALESCE(?,started_at),completed_at=COALESCE(?,completed_at) WHERE id=?`,
		status, errText, time.Now().UTC().UnixNano(), startedNS, completedNS, id)
	return err
}

// UpdateSessionRuntime persists the effective choices made by transport and
// response-optimization probing. These values describe what the run used, while
// Session.Options continues to describe what the operator requested.
func (s *Store) UpdateSessionRuntime(ctx context.Context, id, transport, transportReason, optimizationStrategy, optimizationReason string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE sessions SET runtime_transport=?,runtime_transport_reason=?,
		response_optimization_strategy=?,response_optimization_reason=?,updated_at=? WHERE id=?`, transport,
		transportReason, optimizationStrategy, optimizationReason, time.Now().UTC().UnixNano(), id)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) UpdateSessionPreflight(ctx context.Context, id string, report PreflightReport) error {
	encoded, err := json.Marshal(report)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE sessions SET preflight_json=?,updated_at=? WHERE id=?`,
		encoded, time.Now().UTC().UnixNano(), id)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) TransitionSessionStatus(ctx context.Context, id string, from, to SessionStatus, errText string, started *time.Time) error {
	var startedNS interface{}
	if started != nil {
		startedNS = started.UnixNano()
	}
	result, err := s.db.ExecContext(ctx, `UPDATE sessions SET status=?,error=?,updated_at=?,started_at=COALESCE(?,started_at)
        WHERE id=? AND status=?`, to, errText, time.Now().UTC().UnixNano(), startedNS, id, from)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return fmt.Errorf("session status changed; expected %s", from)
	}
	return nil
}

func (s *Store) UpdateProgress(ctx context.Context, id string, progress interface{}, sequence int64) error {
	return s.UpdateProgressTelemetry(ctx, id, progress, nil, sequence)
}

func (s *Store) UpdateProgressTelemetry(ctx context.Context, id string, progress interface{}, diagnostics []ErrorDiagnostic, sequence int64) error {
	b, err := json.Marshal(progress)
	if err != nil {
		return err
	}
	if diagnostics == nil {
		_, err = s.db.ExecContext(ctx, `UPDATE sessions SET progress_json=?,sequence=?,updated_at=? WHERE id=?`,
			b, sequence, time.Now().UTC().UnixNano(), id)
		return err
	}
	diagnosticsJSON, err := json.Marshal(diagnostics)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE sessions SET progress_json=?,error_diagnostics_json=?,sequence=?,updated_at=? WHERE id=?`,
		b, diagnosticsJSON, sequence, time.Now().UTC().UnixNano(), id)
	return err
}

func (s *Store) SetSequence(ctx context.Context, id string, sequence int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET sequence=?,updated_at=? WHERE id=?`, sequence, time.Now().UTC().UnixNano(), id)
	return err
}

func (s *Store) DeleteSession(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) InsertResultBatch(ctx context.Context, sessionID string, results []StoredResult, logs []TerminalEntry) ([]StoredResult, []TerminalEntry, error) {
	if len(results) == 0 && len(logs) == 0 {
		return results, logs, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	var resultFiltersJSON []byte
	var terminalSequence int64
	if err := tx.QueryRowContext(ctx, `SELECT result_filters_json,terminal_sequence FROM sessions WHERE id=?`, sessionID).
		Scan(&resultFiltersJSON, &terminalSequence); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, err
	}
	var resultFilterOptions ffuf.FilterOptions
	if err := json.Unmarshal(resultFiltersJSON, &resultFilterOptions); err != nil {
		return nil, nil, err
	}
	resultFilter, err := newStoredResultFilter(resultFilterOptions)
	if err != nil {
		return nil, nil, err
	}
	initialTerminalSequence := terminalSequence
	storedLogs := append([]TerminalEntry(nil), logs...)
	terminalSequence, terminalEvents := assignTerminalEvents(sessionID, terminalSequence, results, storedLogs)
	// Keep assigned IDs separate until commit so a failed transaction cannot
	// leak rolled-back cursors through the caller's slice. An ID-only sidecar is
	// substantially smaller than copying every result on this hot path.
	assignedIDs := make([]int64, len(results))
	var resultStatement *sql.Stmt
	if len(results) > 0 {
		resultStatement, err = tx.PrepareContext(ctx, `INSERT INTO results
			(session_id,input_json,position,status,length,words,lines,content_type,redirect_location,url,duration_ns,scraper_json,host,response_fingerprint,created_at,
			tree_origin,tree_path_key,tree_suffix,tree_trailing_slash)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
		if err != nil {
			return nil, nil, err
		}
		defer resultStatement.Close()
	}
	visibleResults := make([]StoredResult, 0, len(results))
	for i := range results {
		input, marshalErr := json.Marshal(results[i].Input)
		if marshalErr != nil {
			return nil, nil, fmt.Errorf("marshal result input: %w", marshalErr)
		}
		scraper, marshalErr := json.Marshal(results[i].ScraperData)
		if marshalErr != nil {
			return nil, nil, fmt.Errorf("marshal result scraper data: %w", marshalErr)
		}
		origin, pathKey, suffix, trailingSlash := resultTreeMetadata(results[i].URL, results[i].Host)
		inserted, insertErr := resultStatement.ExecContext(ctx, sessionID, input, results[i].Position, results[i].StatusCode,
			results[i].ContentLength, results[i].ContentWords, results[i].ContentLines, results[i].ContentType,
			results[i].RedirectLocation, results[i].URL, results[i].DurationNS, scraper, results[i].Host,
			results[i].ResponseFingerprint, results[i].CreatedAt.UnixNano(),
			origin, pathKey, suffix, boolInt(trailingSlash))
		if insertErr != nil {
			return nil, nil, insertErr
		}
		assignedIDs[i], insertErr = inserted.LastInsertId()
		if insertErr != nil {
			return nil, nil, insertErr
		}
		keep, filterErr := resultFilter.keep(results[i])
		if filterErr != nil {
			return nil, nil, filterErr
		}
		if keep {
			visible := results[i]
			visible.ID = assignedIDs[i]
			visibleResults = append(visibleResults, visible)
		}
	}
	if err := insertTerminalResultRanges(ctx, tx, sessionID, results, assignedIDs); err != nil {
		return nil, nil, err
	}
	var logStatement *sql.Stmt
	if len(storedLogs) > 0 {
		logStatement, err = tx.PrepareContext(ctx, `INSERT INTO terminal_log(session_id,at,level,message,terminal_sequence) VALUES(?,?,?,?,?)`)
		if err != nil {
			return nil, nil, err
		}
		defer logStatement.Close()
	}
	for i := range storedLogs {
		entry := &storedLogs[i]
		if _, err := logStatement.ExecContext(ctx, sessionID, entry.At.UnixNano(), entry.Level, entry.Message, entry.ID); err != nil {
			return nil, nil, err
		}
	}
	if len(visibleResults) > 0 || terminalSequence != initialTerminalSequence {
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET result_count=result_count+?,terminal_sequence=?,updated_at=? WHERE id=?`,
			len(visibleResults), terminalSequence, time.Now().UTC().UnixNano(), sessionID); err != nil {
			return nil, nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	for i := range results {
		results[i].ID = assignedIDs[i]
	}
	for i := range logs {
		logs[i].ID = storedLogs[i].ID
		logs[i].SessionID = storedLogs[i].SessionID
	}
	return visibleResults, terminalEvents, nil
}

// insertTerminalResultRanges stores the cursor mapping for result-backed
// terminal lines as contiguous ranges. A normal result-only batch becomes one
// small row instead of one additional index entry per result. Timestamp
// reordering or interleaved lifecycle messages split the mapping only where
// the result-ID and terminal-sequence runs stop being contiguous.
func insertTerminalResultRanges(ctx context.Context, tx *sql.Tx, sessionID string, results []StoredResult, resultIDs []int64) error {
	if len(results) == 0 {
		return nil
	}
	statement, err := tx.PrepareContext(ctx, `INSERT INTO terminal_result_ranges
		(session_id,first_result_id,last_result_id,first_terminal_sequence,last_terminal_sequence)
		VALUES(?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer statement.Close()

	firstID := resultIDs[0]
	lastID := firstID
	firstSequence := results[0].terminalSequence
	lastSequence := firstSequence
	flush := func() error {
		_, err := statement.ExecContext(ctx, sessionID, firstID, lastID, firstSequence, lastSequence)
		return err
	}
	for index := 1; index < len(results); index++ {
		result := results[index]
		resultID := resultIDs[index]
		if resultID == lastID+1 && result.terminalSequence == lastSequence+1 {
			lastID = resultID
			lastSequence = result.terminalSequence
			continue
		}
		if err := flush(); err != nil {
			return err
		}
		firstID, lastID = resultID, resultID
		firstSequence, lastSequence = result.terminalSequence, result.terminalSequence
	}
	return flush()
}

// assignTerminalEvents gives every terminal-visible item one crash-persistent
// per-session cursor. Result lines are represented by the authoritative result
// row itself; only lifecycle and caller-provided custom lines remain in
// terminal_log. Each queue's order is authoritative: concurrent workers can
// capture timestamps immediately before contending on the output mutex, so a
// global timestamp sort would reorder result IDs and fragment the compact
// range mapping. A stable merge preserves both queues and uses timestamps only
// to decide which queue contributes the next event.
func assignTerminalEvents(sessionID string, sequence int64, results []StoredResult, logs []TerminalEntry) (int64, []TerminalEntry) {
	events := make([]TerminalEntry, 0, len(results)+len(logs))
	resultIndex, logIndex := 0, 0
	for resultIndex < len(results) || logIndex < len(logs) {
		useResult := resultIndex < len(results) &&
			(logIndex >= len(logs) || !logs[logIndex].At.Before(results[resultIndex].CreatedAt))
		sequence++
		if useResult {
			result := &results[resultIndex]
			result.terminalSequence = sequence
			result.terminalAt = result.CreatedAt
			events = append(events, TerminalEntry{ID: sequence, SessionID: sessionID,
				At: result.CreatedAt, Level: "result", Message: formatResultTerminal(*result)})
			resultIndex++
			continue
		}
		entry := &logs[logIndex]
		entry.ID = sequence
		entry.SessionID = sessionID
		events = append(events, *entry)
		logIndex++
	}
	return sequence, events
}

func scanStoredResult(row scanner, sessionID string) (StoredResult, error) {
	var result StoredResult
	var input, scraper []byte
	var created int64
	result.SessionID = sessionID
	if err := row.Scan(&result.ID, &input, &result.Position, &result.StatusCode, &result.ContentLength,
		&result.ContentWords, &result.ContentLines, &result.ContentType, &result.RedirectLocation,
		&result.URL, &result.DurationNS, &scraper, &result.Host, &result.ResponseFingerprint, &created); err != nil {
		return StoredResult{}, err
	}
	_ = json.Unmarshal(input, &result.Input)
	_ = json.Unmarshal(scraper, &result.ScraperData)
	result.CreatedAt = time.Unix(0, created).UTC()
	return result, nil
}

// terminalEventsSQL projects one ordered terminal stream from four storage
// representations: range-backed results, results written by the transitional
// per-row sequence schema, untouched legacy terminal rows, and new lifecycle
// rows. The split legacy/new terminal branches are
// intentional. They let SQLite use the historical (session_id,id) index for a
// large existing database instead of scanning COALESCE(terminal_sequence,id).
const terminalEventsSQL = `
	SELECT ranges.first_terminal_sequence+(results.id-ranges.first_result_id) AS event_id,
		results.created_at AS at,'result' AS level,
		h3fuzz_result_terminal(results.url,results.status,results.length,results.words,results.lines,results.duration_ns) AS message
	FROM terminal_result_ranges AS ranges
	JOIN results ON results.id BETWEEN ranges.first_result_id AND ranges.last_result_id
		AND results.session_id=ranges.session_id
	WHERE ranges.session_id=?
	UNION ALL
	SELECT terminal_sequence AS event_id,terminal_at AS at,'result' AS level,
		h3fuzz_result_terminal(url,status,length,words,lines,duration_ns) AS message
	FROM results WHERE session_id=? AND terminal_sequence>0
	UNION ALL
	SELECT id AS event_id,at,level,message
	FROM terminal_log WHERE session_id=? AND terminal_sequence=0
	UNION ALL
	SELECT terminal_sequence AS event_id,at,level,message
	FROM terminal_log WHERE session_id=? AND terminal_sequence>0`

func terminalEventQueryArgs(sessionID string) []any {
	return []any{sessionID, sessionID, sessionID, sessionID}
}

func scanTerminalEntry(row scanner, sessionID string) (TerminalEntry, error) {
	var entry TerminalEntry
	var at int64
	entry.SessionID = sessionID
	if err := row.Scan(&entry.ID, &at, &entry.Level, &entry.Message); err != nil {
		return TerminalEntry{}, err
	}
	entry.At = time.Unix(0, at).UTC()
	return entry, nil
}

func (s *Store) resultFilter(ctx context.Context, sessionID string) (*storedResultFilter, error) {
	session, err := s.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	return newStoredResultFilter(session.ResultFilters)
}

type ResultQuery struct {
	AfterID  int64
	BeforeID *int64
	Limit    int
	Latest   bool
	Search   string
	Statuses []int64
}

func resultMatchesSearch(result StoredResult, needle string) bool {
	if needle == "" {
		return true
	}
	if strings.Contains(strings.ToLower(result.URL), needle) ||
		strings.Contains(strings.ToLower(result.ContentType), needle) {
		return true
	}
	for key, value := range result.Input {
		if strings.Contains(strings.ToLower(key), needle) || strings.Contains(strings.ToLower(string(value)), needle) {
			return true
		}
	}
	return false
}

func resultHasStatus(statuses map[int64]struct{}, status int64) bool {
	if len(statuses) == 0 {
		return true
	}
	_, ok := statuses[status]
	return ok
}

func statusSet(values []int64) map[int64]struct{} {
	statuses := make(map[int64]struct{}, len(values))
	for _, status := range values {
		statuses[status] = struct{}{}
	}
	return statuses
}

func initialResultsPage(query ResultQuery) ResultsPage {
	page := ResultsPage{
		Results:      []StoredResult{},
		NextCursor:   query.AfterID,
		StatusCounts: map[string]int64{},
	}
	if query.BeforeID != nil {
		page.NextCursor = *query.BeforeID
	}
	return page
}

func finishResultsPage(page ResultsPage, backward bool, latest bool) ResultsPage {
	if backward {
		for left, right := 0, len(page.Results)-1; left < right; left, right = left+1, right-1 {
			page.Results[left], page.Results[right] = page.Results[right], page.Results[left]
		}
		if len(page.Results) > 0 {
			page.NextCursor = page.Results[0].ID
		}
	} else if len(page.Results) > 0 {
		page.NextCursor = page.Results[len(page.Results)-1].ID
	}
	page.WindowTruncated = latest && page.HasMore
	return page
}

func (s *Store) queryResultsIndexed(ctx context.Context, sessionID string, query ResultQuery,
	statuses map[int64]struct{}) (ResultsPage, error) {
	page := initialResultsPage(query)
	facetRows, err := s.db.QueryContext(ctx,
		`SELECT status, COUNT(*) FROM results WHERE session_id=? GROUP BY status`, sessionID)
	if err != nil {
		return ResultsPage{}, err
	}
	for facetRows.Next() {
		var status, count int64
		if err := facetRows.Scan(&status, &count); err != nil {
			_ = facetRows.Close()
			return ResultsPage{}, err
		}
		page.StatusCounts[strconv.FormatInt(status, 10)] = count
		if resultHasStatus(statuses, status) {
			page.Total += count
		}
	}
	if err := facetRows.Err(); err != nil {
		_ = facetRows.Close()
		return ResultsPage{}, err
	}
	if err := facetRows.Close(); err != nil {
		return ResultsPage{}, err
	}

	where := []string{"session_id=?"}
	args := []any{sessionID}
	if len(statuses) > 0 {
		placeholders := make([]string, 0, len(statuses))
		for status := range statuses {
			placeholders = append(placeholders, "?")
			args = append(args, status)
		}
		where = append(where, "status IN ("+strings.Join(placeholders, ",")+")")
	}
	backward := query.Latest || query.BeforeID != nil
	order := "ASC"
	if backward {
		order = "DESC"
		if query.BeforeID != nil && *query.BeforeID > 0 {
			where = append(where, "id<?")
			args = append(args, *query.BeforeID)
		}
	} else {
		where = append(where, "id>?")
		args = append(args, query.AfterID)
	}
	args = append(args, query.Limit+1)
	rows, err := s.db.QueryContext(ctx, `SELECT id,input_json,position,status,length,words,lines,content_type,
		redirect_location,url,duration_ns,scraper_json,host,response_fingerprint,created_at FROM results WHERE `+
		strings.Join(where, " AND ")+" ORDER BY id "+order+" LIMIT ?", args...)
	if err != nil {
		return ResultsPage{}, err
	}
	defer rows.Close()
	for rows.Next() {
		result, err := scanStoredResult(rows, sessionID)
		if err != nil {
			return ResultsPage{}, err
		}
		page.Results = append(page.Results, result)
	}
	if err := rows.Err(); err != nil {
		return ResultsPage{}, err
	}
	if len(page.Results) > query.Limit {
		page.Results = page.Results[:query.Limit]
		page.HasMore = true
	}
	return finishResultsPage(page, backward, query.Latest), nil
}

// QueryResults applies the session's persisted live filters first, then the
// transient UI search and status filters, and only then selects a page. Status
// facets intentionally ignore the transient status selection so options do not
// disappear merely because they are not present in the current page.
func (s *Store) QueryResults(ctx context.Context, sessionID string, query ResultQuery) (ResultsPage, error) {
	if query.Limit <= 0 {
		query.Limit = 200
	}
	resultFilter, err := s.resultFilter(ctx, sessionID)
	if err != nil {
		return ResultsPage{}, err
	}
	needle := strings.ToLower(strings.TrimSpace(query.Search))
	statuses := statusSet(query.Statuses)
	if len(resultFilter.providers) == 0 && needle == "" {
		return s.queryResultsIndexed(ctx, sessionID, query, statuses)
	}
	order := "ASC"
	backward := query.Latest || query.BeforeID != nil
	if backward {
		order = "DESC"
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,input_json,position,status,length,words,lines,content_type,
		redirect_location,url,duration_ns,scraper_json,host,response_fingerprint,created_at FROM results
		WHERE session_id=? ORDER BY id `+order, sessionID)
	if err != nil {
		return ResultsPage{}, err
	}
	defer rows.Close()

	page := initialResultsPage(query)
	for rows.Next() {
		result, err := scanStoredResult(rows, sessionID)
		if err != nil {
			return ResultsPage{}, err
		}
		keep, err := resultFilter.keep(result)
		if err != nil {
			return ResultsPage{}, err
		}
		if !keep || !resultMatchesSearch(result, needle) {
			continue
		}
		page.StatusCounts[strconv.FormatInt(result.StatusCode, 10)]++
		if !resultHasStatus(statuses, result.StatusCode) {
			continue
		}
		page.Total++

		eligible := result.ID > query.AfterID
		if query.Latest {
			eligible = true
		} else if query.BeforeID != nil {
			eligible = *query.BeforeID == 0 || result.ID < *query.BeforeID
		}
		if !eligible {
			continue
		}
		if len(page.Results) < query.Limit {
			page.Results = append(page.Results, result)
		} else {
			page.HasMore = true
		}
	}
	if err := rows.Err(); err != nil {
		return ResultsPage{}, err
	}
	return finishResultsPage(page, backward, query.Latest), nil
}

func (s *Store) Results(ctx context.Context, sessionID string, afterID int64, limit int) (ResultsPage, error) {
	if limit <= 0 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}
	resultFilter, err := s.resultFilter(ctx, sessionID)
	if err != nil {
		return ResultsPage{}, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,input_json,position,status,length,words,lines,content_type,
		redirect_location,url,duration_ns,scraper_json,host,response_fingerprint,created_at FROM results
		WHERE session_id=? AND id>? ORDER BY id`, sessionID, afterID)
	if err != nil {
		return ResultsPage{}, err
	}
	defer rows.Close()
	page := ResultsPage{Results: []StoredResult{}, NextCursor: afterID}
	for rows.Next() {
		result, err := scanStoredResult(rows, sessionID)
		if err != nil {
			return ResultsPage{}, err
		}
		keep, err := resultFilter.keep(result)
		if err != nil {
			return ResultsPage{}, err
		}
		if !keep {
			continue
		}
		if len(page.Results) == limit {
			page.HasMore = true
			break
		}
		page.Results = append(page.Results, result)
		page.NextCursor = result.ID
	}
	return page, rows.Err()
}

func (s *Store) LatestResults(ctx context.Context, sessionID string, limit int) (ResultsPage, error) {
	if limit <= 0 {
		limit = 1000
	}
	if limit > 25000 {
		limit = 25000
	}
	resultFilter, err := s.resultFilter(ctx, sessionID)
	if err != nil {
		return ResultsPage{}, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,input_json,position,status,length,words,lines,content_type,
		redirect_location,url,duration_ns,scraper_json,host,response_fingerprint,created_at FROM results
		WHERE session_id=? ORDER BY id DESC`, sessionID)
	if err != nil {
		return ResultsPage{}, err
	}
	defer rows.Close()
	descending := make([]StoredResult, 0, limit)
	truncated := false
	for rows.Next() {
		result, err := scanStoredResult(rows, sessionID)
		if err != nil {
			return ResultsPage{}, err
		}
		keep, err := resultFilter.keep(result)
		if err != nil {
			return ResultsPage{}, err
		}
		if !keep {
			continue
		}
		if len(descending) == limit {
			truncated = true
			break
		}
		descending = append(descending, result)
	}
	if err := rows.Err(); err != nil {
		return ResultsPage{}, err
	}
	for left, right := 0, len(descending)-1; left < right; left, right = left+1, right-1 {
		descending[left], descending[right] = descending[right], descending[left]
	}
	page := ResultsPage{Results: descending, WindowTruncated: truncated}
	if len(descending) > 0 {
		page.NextCursor = descending[len(descending)-1].ID
	}
	return page, nil
}

func (s *Store) AllResults(ctx context.Context, sessionID string) ([]StoredResult, error) {
	var all []StoredResult
	var cursor int64
	for {
		page, err := s.Results(ctx, sessionID, cursor, 1000)
		if err != nil {
			return nil, err
		}
		all = append(all, page.Results...)
		cursor = page.NextCursor
		if !page.HasMore {
			break
		}
	}
	if all == nil {
		all = []StoredResult{}
	}
	return all, nil
}

func (s *Store) ForEachResult(ctx context.Context, sessionID string, fn func(StoredResult) error) error {
	resultFilter, err := s.resultFilter(ctx, sessionID)
	if err != nil {
		return err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,input_json,position,status,length,words,lines,content_type,
		redirect_location,url,duration_ns,scraper_json,host,response_fingerprint,created_at FROM results
        WHERE session_id=? ORDER BY id`, sessionID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		result, err := scanStoredResult(rows, sessionID)
		if err != nil {
			return err
		}
		keep, err := resultFilter.keep(result)
		if err != nil {
			return err
		}
		if !keep {
			continue
		}
		if err := fn(result); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (s *Store) ForEachTerminalEntry(ctx context.Context, sessionID string, fn func(TerminalEntry) error) error {
	rows, err := s.db.QueryContext(ctx, `SELECT event_id,at,level,message FROM (`+terminalEventsSQL+`) ORDER BY event_id`,
		terminalEventQueryArgs(sessionID)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		entry, err := scanTerminalEntry(rows, sessionID)
		if err != nil {
			return err
		}
		if err := fn(entry); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (s *Store) TerminalEntries(ctx context.Context, sessionID string) ([]TerminalEntry, error) {
	page, err := s.Terminal(ctx, sessionID, 0, 1000)
	if err != nil {
		return nil, err
	}
	entries := page.Entries
	for page.HasMore {
		page, err = s.Terminal(ctx, sessionID, page.NextCursor, 1000)
		if err != nil {
			return nil, err
		}
		entries = append(entries, page.Entries...)
	}
	return entries, nil
}

func (s *Store) Terminal(ctx context.Context, sessionID string, afterID int64, limit int) (TerminalPage, error) {
	if limit <= 0 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}
	args := append(terminalEventQueryArgs(sessionID), afterID, limit+1)
	rows, err := s.db.QueryContext(ctx, `SELECT event_id,at,level,message FROM (`+terminalEventsSQL+`)
		WHERE event_id>? ORDER BY event_id LIMIT ?`, args...)
	if err != nil {
		return TerminalPage{}, err
	}
	defer rows.Close()
	page := TerminalPage{Entries: []TerminalEntry{}, NextCursor: afterID}
	for rows.Next() {
		entry, err := scanTerminalEntry(rows, sessionID)
		if err != nil {
			return TerminalPage{}, err
		}
		if len(page.Entries) == limit {
			page.HasMore = true
			break
		}
		page.Entries = append(page.Entries, entry)
		page.NextCursor = entry.ID
	}
	return page, rows.Err()
}

func (s *Store) GetSettings(ctx context.Context) (Settings, error) {
	var raw []byte
	var updated int64
	err := s.db.QueryRowContext(ctx, `SELECT value_json,updated_at FROM settings WHERE id=1`).Scan(&raw, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		settings := DefaultSettings()
		if err := s.PutSettings(ctx, settings); err != nil {
			return Settings{}, err
		}
		return settings, nil
	}
	if err != nil {
		return Settings{}, err
	}
	var settings Settings
	if err := json.Unmarshal(raw, &settings); err != nil {
		return Settings{}, err
	}
	settings.UpdatedAt = time.Unix(0, updated).UTC()
	settingsChanged := false
	generatedLegacyThreadDefault := false
	if settings.Defaults.Options.HTTP.Http3MaxWorkers <= 0 &&
		!containsString(settings.Defaults.ExplicitFlags, "http3-max-workers") {
		settings.Defaults.Options.HTTP.Http3MaxWorkers = ffuf.MaxHTTP3Workers
		settingsChanged = true
	}
	if settings.SchemaVersion < 1 {
		if settings.Defaults.Options.Input.InputMode == "clusterbomb" && !containsString(settings.Defaults.ExplicitFlags, "mode") {
			settings.Defaults.Options.Input.InputMode = "pitchfork"
		}
		settings.SchemaVersion = 1
		settingsChanged = true
	}
	if settings.SchemaVersion < 2 {
		// Version 1 inherited ffuf's 40-thread process default without marking
		// -t explicit. Only migrate that untouched shape; a custom thread value or
		// explicit t is operator-owned and must survive unchanged.
		if settings.Defaults.Options.General.Threads == 40 && !containsString(settings.Defaults.ExplicitFlags, "t") {
			settings.Defaults.Options.General.Threads = 1000
			settings.Defaults.ExplicitFlags = uniqueStrings(append(settings.Defaults.ExplicitFlags, "t"))
			generatedLegacyThreadDefault = true
		}
		settings.SchemaVersion = 2
		settingsChanged = true
	}
	if settings.SchemaVersion < 3 {
		// HTTP/3 worker bounds were absent from older JSON. Backfill the value
		// without marking it explicit; automatic sizing is provenance-driven.
		// The following schema migration distinguishes the generated historical
		// thread default from operator-owned concurrency choices.
		if settings.Defaults.Options.HTTP.Http3MaxWorkers <= 0 {
			settings.Defaults.Options.HTTP.Http3MaxWorkers = ffuf.MaxHTTP3Workers
		}
		settings.SchemaVersion = 3
		settingsChanged = true
	}
	if settings.SchemaVersion < 4 {
		// Schema v2 wrote a fixed 1,000-thread value into the otherwise untouched
		// bundled web defaults. That predates adaptive HTTP/3 worker supervision:
		// retaining the generated -t now pins the run below the 8x256 initial
		// transport capacity and prevents later 16/32-connection growth. Migrate
		// only the exact generated baseline. Custom values and provenance-marked
		// explicit choices remain operator-owned.
		if generatedLegacyThreadDefault || isLegacyFixedWebConcurrencyDefault(settings.Defaults) {
			settings.Defaults.Options.General.Threads = ffuf.NewConfigOptions().General.Threads
			settings.Defaults.ExplicitFlags = stringsWithout(settings.Defaults.ExplicitFlags, "t")
			settings.Defaults.Options.Provenance.Threads = false
		}

		// Very old settings JSON may not contain all HTTP/3 capacity fields. Zero
		// is valid only for the fixed-connection selector, where it means adaptive;
		// the remaining omitted values inherit the current measured defaults.
		fresh := ffuf.NewConfigOptions()
		if settings.Defaults.Options.HTTP.Http3MaxConnections <= 0 &&
			!containsString(settings.Defaults.ExplicitFlags, "http3-max-connections") {
			settings.Defaults.Options.HTTP.Http3MaxConnections = fresh.HTTP.Http3MaxConnections
		}
		if settings.Defaults.Options.HTTP.Http3Streams <= 0 &&
			!containsString(settings.Defaults.ExplicitFlags, "http3-streams") {
			settings.Defaults.Options.HTTP.Http3Streams = fresh.HTTP.Http3Streams
		}
		if settings.Defaults.Options.HTTP.Http3ProbeTimeout <= 0 &&
			!containsString(settings.Defaults.ExplicitFlags, "http3-probe-timeout") {
			settings.Defaults.Options.HTTP.Http3ProbeTimeout = fresh.HTTP.Http3ProbeTimeout
		}
		if settings.Defaults.Options.General.Scrapers == fresh.General.Scrapers &&
			!containsString(settings.Defaults.ExplicitFlags, "scrapers") &&
			!settings.Defaults.Options.Provenance.Scrapers {
			settings.Defaults.Options.General.Scrapers = ""
		}
		settings.SchemaVersion = 4
		settingsChanged = true
	}
	if settings.SchemaVersion < 5 {
		// Schema v4 released the historical fixed web default by restoring
		// ffuf's 40-thread CLI seed. That kept HTTP/3 adaptive, but also made
		// ordinary HTTP scans inherit a concurrency level far below the web
		// console's established 1,000-worker baseline. The v4-generated shape
		// is identifiable by the CLI seed with no explicit thread provenance.
		// Preserve every custom value and every explicit -t choice.
		cliThreadDefault := ffuf.NewConfigOptions().General.Threads
		if settings.Defaults.Options.General.Threads == cliThreadDefault &&
			!containsString(settings.Defaults.ExplicitFlags, "t") &&
			!settings.Defaults.Options.Provenance.Threads {
			settings.Defaults.Options.General.Threads = defaultWebThreadCount
		}
		settings.SchemaVersion = currentSettingsSchemaVersion
		settingsChanged = true
	}
	syncWebOptionProvenance(&settings.Defaults.Options, settings.Defaults.ExplicitFlags)
	if settingsChanged {
		if err := s.PutSettings(ctx, settings); err != nil {
			return Settings{}, err
		}
	}
	return settings, nil
}

func (s *Store) PutSettings(ctx context.Context, settings Settings) error {
	if settings.SchemaVersion < currentSettingsSchemaVersion {
		settings.SchemaVersion = currentSettingsSchemaVersion
	}
	if settings.Defaults.Options.HTTP.Http3MaxWorkers <= 0 &&
		!containsString(settings.Defaults.ExplicitFlags, "http3-max-workers") {
		settings.Defaults.Options.HTTP.Http3MaxWorkers = ffuf.MaxHTTP3Workers
	}
	settings.Defaults.ExplicitFlags = uniqueStrings(settings.Defaults.ExplicitFlags)
	syncWebOptionProvenance(&settings.Defaults.Options, settings.Defaults.ExplicitFlags)
	settings.UpdatedAt = time.Now().UTC()
	raw, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO settings(id,value_json,updated_at) VALUES(1,?,?)
        ON CONFLICT(id) DO UPDATE SET value_json=excluded.value_json,updated_at=excluded.updated_at`,
		raw, settings.UpdatedAt.UnixNano())
	return err
}

func isLegacyFixedWebConcurrencyDefault(defaults Defaults) bool {
	if defaults.Wordlist != "final.txt" || defaults.Profile != profiles[0].ID ||
		defaults.Options.General.Threads != 1000 || defaults.Options.Provenance.Threads {
		return false
	}
	wantFlags := []string{"D", "e", "mc", "fc", "H", "t"}
	if len(uniqueStrings(defaults.ExplicitFlags)) != len(wantFlags) {
		return false
	}
	for _, flag := range wantFlags {
		if !containsString(defaults.ExplicitFlags, flag) {
			return false
		}
	}
	return defaults.Options.HTTP.Http3Connections == 0 &&
		(defaults.Options.HTTP.Http3MaxConnections == 0 || defaults.Options.HTTP.Http3MaxConnections == 64) &&
		(defaults.Options.HTTP.Http3Streams == 0 || defaults.Options.HTTP.Http3Streams == 256) &&
		defaults.Options.General.Rate == 0 && defaults.Options.General.Delay == ""
}

func stringsWithout(values []string, unwanted string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != unwanted {
			out = append(out, value)
		}
	}
	return out
}
