package webserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

func testSession(id string) Session {
	now := time.Now().UTC()
	return Session{ID: id, Name: "example", Target: "https://example.test/FUZZ", Wordlist: "final.txt",
		Profile: "aspm", Status: StatusIdle, Options: *ffuf.NewConfigOptions(),
		ExplicitFlags: []string{"mc"}, CreatedAt: now, UpdatedAt: now}
}

func TestStorePersistsPagesAndCascades(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	session := testSession("session-1")
	if err := store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	results := []StoredResult{
		{Input: map[string][]byte{"FUZZ": []byte("one")}, URL: "https://example.test/one", StatusCode: 200, CreatedAt: time.Now().UTC()},
		{Input: map[string][]byte{"FUZZ": []byte("two")}, URL: "https://example.test/two", StatusCode: 204, CreatedAt: time.Now().UTC()},
	}
	logs := []TerminalEntry{{At: time.Now().UTC(), Level: "info", Message: "started"}}
	persisted, persistedLogs, err := store.InsertResultBatch(context.Background(), session.ID, results, logs)
	if err != nil {
		t.Fatal(err)
	}
	if persisted[0].ID == 0 || persistedLogs[0].ID == 0 {
		t.Fatal("persistent cursors were not assigned")
	}
	page, err := store.Results(context.Background(), session.ID, 0, 1)
	if err != nil || len(page.Results) != 1 || !page.HasMore {
		t.Fatalf("unexpected first page: %#v, %v", page, err)
	}
	page, err = store.Results(context.Background(), session.ID, page.NextCursor, 10)
	if err != nil || len(page.Results) != 1 || page.HasMore {
		t.Fatalf("unexpected second page: %#v, %v", page, err)
	}
	terminal, err := store.Terminal(context.Background(), session.ID, 0, 10)
	if err != nil || len(terminal.Entries) != 3 ||
		terminal.Entries[0].Message != formatResultTerminal(results[0]) ||
		terminal.Entries[1].Message != formatResultTerminal(results[1]) ||
		terminal.Entries[2].Message != "started" {
		t.Fatalf("unexpected terminal page: %#v, %v", terminal, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("database permissions = %v, err=%v", info.Mode().Perm(), err)
	}
	store, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	loaded, err := store.GetSession(context.Background(), session.ID)
	if err != nil || loaded.ResultCount != 2 || len(loaded.ExplicitFlags) != 1 {
		t.Fatalf("persisted session = %#v, err=%v", loaded, err)
	}
	if err := store.DeleteSession(context.Background(), session.ID); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"results", "terminal_log", "terminal_result_ranges"} {
		var count int
		if err := store.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s rows after cascade = %d, err=%v", table, count, err)
		}
	}
}

func TestStoreBatchPreservesIDsOrderAndJSON(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "batch-order.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	session := testSession("batch-order")
	if err := store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.InsertResultBatch(context.Background(), session.ID, nil, []TerminalEntry{{
		At: time.Now().UTC(), Level: "info", Message: "seed",
	}}); err != nil {
		t.Fatal(err)
	}

	const count = 503
	results := make([]StoredResult, count)
	logs := make([]TerminalEntry, count)
	now := time.Now().UTC()
	for index := range results {
		results[index] = StoredResult{
			Input: map[string][]byte{
				"FUZZ":   []byte(fmt.Sprintf("item-%03d", index)),
				"binary": {0, byte(index), 0xff},
			},
			Position:    index,
			StatusCode:  int64(200 + index%5),
			URL:         fmt.Sprintf("https://example.test/item-%03d", index),
			ScraperData: map[string][]string{"links": {fmt.Sprintf("/%d", index), "/shared"}},
			CreatedAt:   now.Add(time.Duration(index) * time.Nanosecond),
		}
		logs[index] = TerminalEntry{At: now.Add(time.Duration(index) * time.Nanosecond),
			Level: "result", Message: fmt.Sprintf("line-%03d", index)}
	}

	persisted, persistedLogs, err := store.InsertResultBatch(context.Background(), session.ID, results, logs)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < count; index++ {
		if got, want := persisted[index].ID, int64(index+1); got != want {
			t.Fatalf("result %d ID=%d, want %d", index, got, want)
		}
		if got, want := persistedLogs[index].ID, int64(index+2); got != want {
			t.Fatalf("terminal entry %d ID=%d, want %d", index, got, want)
		}
		if persistedLogs[index].SessionID != session.ID {
			t.Fatalf("terminal entry %d session=%q, want %q", index, persistedLogs[index].SessionID, session.ID)
		}
	}

	page, err := store.Results(context.Background(), session.ID, 0, count)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Results) != count {
		t.Fatalf("loaded %d results, want %d", len(page.Results), count)
	}
	for index := range page.Results {
		if page.Results[index].ID != persisted[index].ID || page.Results[index].Position != index {
			t.Fatalf("loaded result %d order changed: %#v", index, page.Results[index])
		}
		if !reflect.DeepEqual(page.Results[index].Input, results[index].Input) {
			t.Fatalf("loaded result %d input JSON=%#v, want %#v", index, page.Results[index].Input, results[index].Input)
		}
		if !reflect.DeepEqual(page.Results[index].ScraperData, results[index].ScraperData) {
			t.Fatalf("loaded result %d scraper JSON=%#v, want %#v", index, page.Results[index].ScraperData, results[index].ScraperData)
		}
	}
}

func TestStoreBackfillsCompletedCountForLegacyProgress(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "legacy-progress.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	session := testSession("legacy-progress")
	if err := store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	legacy := []byte(`{"req_count":2001,"response_count":70,"error_count":3,"req_total":3000}`)
	if _, err := store.db.ExecContext(context.Background(),
		`UPDATE sessions SET progress_json=? WHERE id=?`, legacy, session.ID); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.GetSession(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Progress.CompletedCount != 73 {
		t.Fatalf("legacy completed count = %d, want response + terminal errors = 73", loaded.Progress.CompletedCount)
	}
	if loaded.Progress.RequestCount != 2_001 {
		t.Fatalf("legacy request count = %d, want req_count fallback 2,001", loaded.Progress.RequestCount)
	}
}

func TestMemoryStoresAreIsolated(t *testing.T) {
	one, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer one.Close()
	two, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer two.Close()
	session := testSession("only-in-one")
	if err := one.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	if _, err := two.GetSession(context.Background(), session.ID); err != ErrNotFound {
		t.Fatalf("second memory store unexpectedly shared data: %v", err)
	}
}

func TestLatestResultsReturnsAscendingWindow(t *testing.T) {
	store, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	session := testSession("latest")
	if err := store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	results := make([]StoredResult, 5)
	for i := range results {
		results[i] = StoredResult{Position: i, URL: "https://example.test", CreatedAt: time.Now().UTC()}
	}
	if _, _, err := store.InsertResultBatch(context.Background(), session.ID, results, nil); err != nil {
		t.Fatal(err)
	}
	page, err := store.LatestResults(context.Background(), session.ID, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Results) != 3 || !page.WindowTruncated || page.Results[0].Position != 2 || page.Results[2].Position != 4 {
		t.Fatalf("unexpected latest window: %#v", page)
	}
}

func TestWebDefaultsUsePitchforkForOneWordlist(t *testing.T) {
	settings := DefaultSettings()
	if settings.Defaults.Options.Input.InputMode != "pitchfork" {
		t.Fatalf("web input mode = %q", settings.Defaults.Options.Input.InputMode)
	}
	if settings.SchemaVersion != currentSettingsSchemaVersion || settings.Defaults.Options.General.Threads != 1000 ||
		containsString(settings.Defaults.ExplicitFlags, "t") ||
		settings.Defaults.Options.Provenance.Threads ||
		settings.Defaults.Options.HTTP.Http3MaxWorkers != ffuf.MaxHTTP3Workers {
		t.Fatalf("web concurrency defaults = schema %d, threads %d, explicit %v",
			settings.SchemaVersion, settings.Defaults.Options.General.Threads, settings.Defaults.ExplicitFlags)
	}
	httpOptions := settings.Defaults.Options.HTTP
	if httpOptions.Http3 || httpOptions.NoHttp3 || httpOptions.NoResponseOptimization ||
		httpOptions.Http3Connections != 0 || httpOptions.Http3MaxConnections != 64 ||
		httpOptions.Http3Streams != 256 || httpOptions.Http3ProbeTimeout != 3 ||
		settings.Defaults.Options.General.Rate != 0 || settings.Defaults.Options.General.Delay != "" ||
		settings.Defaults.Options.General.Scrapers != "" {
		t.Fatalf("web HTTP/3 defaults do not match adaptive measured path: %+v", settings.Defaults.Options)
	}
	session, err := normalizeCreate(CreateSessionRequest{
		Target: "https://example.test/FUZZ", Profile: "php", Wordlist: "words.txt",
	}, settings)
	if err != nil {
		t.Fatal(err)
	}
	if session.Options.Input.InputMode != "pitchfork" || len(session.Options.Input.Wordlists) != 1 || session.Options.Input.Wordlists[0] != "words.txt" {
		t.Fatalf("one-wordlist defaults were not preserved: mode=%q wordlists=%v",
			session.Options.Input.InputMode, session.Options.Input.Wordlists)
	}
}

func TestSettingsBackfillOnlyUntouchedClusterbombMode(t *testing.T) {
	store, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	legacy := DefaultSettings()
	legacy.SchemaVersion = 0
	legacy.Defaults.Options.Input.InputMode = "clusterbomb"
	legacy.Defaults.Options.General.Threads = 40
	legacy.Defaults.Options.General.Scrapers = "all"
	legacy.Defaults.ExplicitFlags = []string{"D", "e"}
	raw, _ := json.Marshal(legacy)
	if _, err := store.db.Exec(`INSERT INTO settings(id,value_json,updated_at) VALUES(1,?,?)`, raw, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.GetSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SchemaVersion != currentSettingsSchemaVersion || loaded.Defaults.Options.Input.InputMode != "pitchfork" ||
		loaded.Defaults.Options.General.Threads != defaultWebThreadCount || containsString(loaded.Defaults.ExplicitFlags, "t") ||
		loaded.Defaults.Options.Provenance.Threads {
		t.Fatalf("legacy settings were not backfilled: %#v", loaded)
	}
}

func TestSettingsMigrationUpdatesLegacyAutomaticCLIThreadDefault(t *testing.T) {
	store, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	settings := DefaultSettings()
	settings.SchemaVersion = 2
	settings.Defaults.Options.General.Threads = 40
	settings.Defaults.Options.HTTP.Http3MaxWorkers = 0
	settings.Defaults.ExplicitFlags = []string{"D", "e", "mc", "fc", "H"}
	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO settings(id,value_json,updated_at) VALUES(1,?,?)`, raw, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.GetSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Defaults.Options.General.Threads != defaultWebThreadCount || containsString(loaded.Defaults.ExplicitFlags, "t") {
		t.Fatalf("schema-v2 automatic web default was not updated: threads=%d explicit=%v",
			loaded.Defaults.Options.General.Threads, loaded.Defaults.ExplicitFlags)
	}
	if loaded.SchemaVersion != currentSettingsSchemaVersion || loaded.Defaults.Options.HTTP.Http3MaxWorkers != ffuf.MaxHTTP3Workers {
		t.Fatalf("current worker bound backfill = schema %d max %d", loaded.SchemaVersion, loaded.Defaults.Options.HTTP.Http3MaxWorkers)
	}
}

func TestSettingsMigrationReleasesHistoricalFixedWorkerDefault(t *testing.T) {
	store, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	legacy := DefaultSettings()
	legacy.SchemaVersion = 2
	legacy.Defaults.Options.General.Threads = 1000
	legacy.Defaults.Options.General.Scrapers = "all"
	legacy.Defaults.Options.HTTP.Http3MaxWorkers = 0
	legacy.Defaults.Options.Provenance = ffuf.ConfigOptionProvenance{}
	legacy.Defaults.ExplicitFlags = []string{"D", "e", "mc", "fc", "H", "t"}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO settings(id,value_json,updated_at) VALUES(1,?,?)`, raw, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.GetSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SchemaVersion != currentSettingsSchemaVersion || loaded.Defaults.Options.General.Threads != defaultWebThreadCount ||
		containsString(loaded.Defaults.ExplicitFlags, "t") || loaded.Defaults.Options.Provenance.Threads {
		t.Fatalf("historical fixed worker default survived migration: %+v", loaded.Defaults)
	}
	if loaded.Defaults.Options.HTTP.Http3MaxWorkers != ffuf.MaxHTTP3Workers ||
		loaded.Defaults.Options.HTTP.Http3MaxConnections != 64 || loaded.Defaults.Options.HTTP.Http3Streams != 256 ||
		loaded.Defaults.Options.HTTP.Http3ProbeTimeout != 3 || loaded.Defaults.Options.General.Scrapers != "" {
		t.Fatalf("HTTP/3 capacity defaults were not backfilled: %+v", loaded.Defaults.Options.HTTP)
	}
}

func TestSettingsMigrationRestoresGeneratedV4WebConcurrencyDefault(t *testing.T) {
	store, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	legacy := DefaultSettings()
	legacy.SchemaVersion = 4
	legacy.Defaults.Options.General.Threads = 40
	legacy.Defaults.Options.Provenance.Threads = false
	legacy.Defaults.ExplicitFlags = stringsWithout(legacy.Defaults.ExplicitFlags, "t")
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO settings(id,value_json,updated_at) VALUES(1,?,?)`, raw, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.GetSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SchemaVersion != currentSettingsSchemaVersion ||
		loaded.Defaults.Options.General.Threads != defaultWebThreadCount ||
		containsString(loaded.Defaults.ExplicitFlags, "t") ||
		loaded.Defaults.Options.Provenance.Threads {
		t.Fatalf("generated schema-v4 concurrency default was not restored: %+v", loaded.Defaults)
	}
}

func TestSettingsMigrationPreservesCustomAutomaticConcurrency(t *testing.T) {
	store, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	legacy := DefaultSettings()
	legacy.SchemaVersion = 4
	legacy.Defaults.Options.General.Threads = 777
	legacy.Defaults.Options.Provenance.Threads = false
	legacy.Defaults.ExplicitFlags = stringsWithout(legacy.Defaults.ExplicitFlags, "t")
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO settings(id,value_json,updated_at) VALUES(1,?,?)`, raw, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.GetSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Defaults.Options.General.Threads != 777 ||
		containsString(loaded.Defaults.ExplicitFlags, "t") ||
		loaded.Defaults.Options.Provenance.Threads {
		t.Fatalf("custom automatic concurrency was not preserved: %+v", loaded.Defaults)
	}
}

func TestSettingsMigrationPreservesDeliberateConcurrency(t *testing.T) {
	tests := []struct {
		name       string
		threads    int
		provenance bool
	}{
		{name: "custom worker count", threads: 777},
		{name: "explicit CLI-default count", threads: 40, provenance: true},
		{name: "explicit legacy-shaped count", threads: 1000, provenance: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, err := OpenStore(":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()

			settings := DefaultSettings()
			settings.SchemaVersion = 3
			settings.Defaults.Options.General.Threads = test.threads
			settings.Defaults.Options.Provenance.Threads = test.provenance
			settings.Defaults.ExplicitFlags = []string{"D", "e", "mc", "fc", "H", "t"}
			raw, err := json.Marshal(settings)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.Exec(`INSERT INTO settings(id,value_json,updated_at) VALUES(1,?,?)`, raw, time.Now().UnixNano()); err != nil {
				t.Fatal(err)
			}

			loaded, err := store.GetSettings(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Defaults.Options.General.Threads != test.threads ||
				!containsString(loaded.Defaults.ExplicitFlags, "t") || !loaded.Defaults.Options.Provenance.Threads {
				t.Fatalf("deliberate concurrency was not preserved: %+v", loaded.Defaults)
			}
		})
	}
}

func TestSettingsMigrationPreservesExplicitScrapers(t *testing.T) {
	store, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	settings := DefaultSettings()
	settings.SchemaVersion = 3
	settings.Defaults.Options.General.Scrapers = "all"
	settings.Defaults.ExplicitFlags = append(settings.Defaults.ExplicitFlags, "scrapers")
	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO settings(id,value_json,updated_at) VALUES(1,?,?)`, raw, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.GetSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Defaults.Options.General.Scrapers != "all" || !loaded.Defaults.Options.Provenance.Scrapers ||
		!containsString(loaded.Defaults.ExplicitFlags, "scrapers") {
		t.Fatalf("explicit scraper choice was not preserved: %+v", loaded.Defaults)
	}
}

func TestNormalizeCreateCompilesMeasuredAutomaticHTTP3Defaults(t *testing.T) {
	settings := DefaultSettings()
	session, err := normalizeCreate(CreateSessionRequest{
		Target: "https://example.test/FUZZ",
	}, settings)
	if err != nil {
		t.Fatal(err)
	}
	ffuf.MarkExplicitOptions(&session.Options, session.ExplicitFlags...)
	conf, err := ffuf.ConfigFromOptions(&session.Options, context.Background(), func() {})
	if err != nil {
		t.Fatal(err)
	}
	_, threadsExplicit := webExecutionPolicy(session.ExplicitFlags)
	conf.ThreadsExplicit = threadsExplicit
	if containsString(session.ExplicitFlags, "t") || session.Options.Provenance.Threads {
		t.Fatalf("automatic web default gained explicit thread provenance: flags=%v provenance=%+v",
			session.ExplicitFlags, session.Options.Provenance)
	}

	if conf.Http3 || conf.NoHttp3 {
		t.Fatalf("default web scan must retain automatic protocol selection: http3=%t no-http3=%t", conf.Http3, conf.NoHttp3)
	}
	if conf.NoResponseOptimization || conf.Method != "GET" || conf.Data != "" {
		t.Fatalf("default web scan left the qualified bodyless path: optimization-disabled=%t method=%q body=%q",
			conf.NoResponseOptimization, conf.Method, conf.Data)
	}
	if conf.ThreadsExplicit || conf.Threads != defaultWebThreadCount || conf.Http3Connections != 0 ||
		conf.Http3MaxConnections != 64 || conf.Http3Streams != 256 ||
		conf.Http3MaxWorkers != ffuf.MaxHTTP3Workers || conf.Rate != 0 || conf.Delay.HasDelay ||
		conf.AutoCalibration || conf.Raw || conf.IgnoreBody || conf.Http2 || conf.Scrapers != "" ||
		len(conf.Preflights) != 0 || len(conf.Postflights) != 0 ||
		session.Options.Matcher.Lines != "" || session.Options.Matcher.Regexp != "" ||
		session.Options.Matcher.Time != "" || session.Options.Matcher.Words != "" ||
		session.Options.Filter.Lines != "" || session.Options.Filter.Regexp != "" ||
		session.Options.Filter.Time != "" || session.Options.Filter.Words != "" {
		t.Fatalf("default web scan does not compile to adaptive HTTP/3 capacity: %+v", conf)
	}
}

func TestNormalizeCreateUsesWebConcurrencyBaselineForRegularHTTP(t *testing.T) {
	settings := DefaultSettings()
	session, err := normalizeCreate(CreateSessionRequest{
		Target: "http://example.test/FUZZ",
	}, settings)
	if err != nil {
		t.Fatal(err)
	}
	ffuf.MarkExplicitOptions(&session.Options, session.ExplicitFlags...)
	conf, err := ffuf.ConfigFromOptions(&session.Options, context.Background(), func() {})
	if err != nil {
		t.Fatal(err)
	}
	_, conf.ThreadsExplicit = webExecutionPolicy(session.ExplicitFlags)

	if conf.Threads != defaultWebThreadCount || conf.ThreadsExplicit ||
		containsString(session.ExplicitFlags, "t") || session.Options.Provenance.Threads {
		t.Fatalf("regular HTTP web concurrency = threads %d explicit %t flags=%v provenance=%+v",
			conf.Threads, conf.ThreadsExplicit, session.ExplicitFlags, session.Options.Provenance)
	}
}

func TestStorePersistsRuntimeTransportAndOptimization(t *testing.T) {
	store, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	session := testSession("runtime-selection")
	if err := store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateSessionRuntime(context.Background(), session.ID, RuntimeTransportHTTP3,
		"negotiated h3 over v1", "HEAD", "reliable empty-body baseline"); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.GetSession(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.RuntimeTransport != RuntimeTransportHTTP3 || loaded.RuntimeTransportReason != "negotiated h3 over v1" ||
		loaded.ResponseOptimizationStrategy != "HEAD" || loaded.ResponseOptimizationReason != "reliable empty-body baseline" {
		t.Fatalf("runtime selection did not round trip: %#v", loaded)
	}
	listed, err := store.ListSessions(context.Background())
	if err != nil || len(listed) != 1 || listed[0].RuntimeTransport != RuntimeTransportHTTP3 {
		t.Fatalf("listed runtime selection = %#v, err=%v", listed, err)
	}
	if err := store.UpdateSessionRuntime(context.Background(), "missing", RuntimeTransportHTTP1, "fallback", "GET", "safe default"); err != ErrNotFound {
		t.Fatalf("missing-session runtime update = %v, want ErrNotFound", err)
	}
}

func TestStoreMigratesLegacySessionsWithoutRuntimeColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`CREATE TABLE sessions (
		id TEXT PRIMARY KEY, name TEXT NOT NULL, target TEXT NOT NULL, wordlist TEXT NOT NULL,
		profile TEXT NOT NULL, status TEXT NOT NULL, options_json BLOB NOT NULL,
		explicit_flags_json BLOB NOT NULL DEFAULT '[]', progress_json BLOB NOT NULL,
		result_count INTEGER NOT NULL DEFAULT 0, sequence INTEGER NOT NULL DEFAULT 0,
		error TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
		started_at INTEGER, completed_at INTEGER
	)`)
	if err != nil {
		t.Fatal(err)
	}
	options, _ := json.Marshal(ffuf.NewConfigOptions())
	progress, _ := json.Marshal(Progress{})
	now := time.Now().UTC().UnixNano()
	_, err = legacy.Exec(`INSERT INTO sessions
		(id,name,target,wordlist,profile,status,options_json,explicit_flags_json,progress_json,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`, "legacy-runtime", "legacy", "https://example.test/FUZZ", "final.txt", "dir",
		StatusIdle, options, []byte(`[]`), progress, now, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	loaded, err := store.GetSession(context.Background(), "legacy-runtime")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.RuntimeTransport != "" || loaded.RuntimeTransportReason != "" ||
		loaded.ResponseOptimizationStrategy != "" || loaded.ResponseOptimizationReason != "" {
		t.Fatalf("legacy runtime fields were not backfilled empty: %#v", loaded)
	}
	if loaded.ResultFilters != (ffuf.FilterOptions{}) {
		t.Fatalf("legacy result filters were not backfilled empty: %#v", loaded.ResultFilters)
	}
	for _, column := range []string{"result_filters_json", "runtime_transport", "runtime_transport_reason", "response_optimization_strategy", "response_optimization_reason"} {
		var count int
		if err := store.db.QueryRow(`SELECT count(*) FROM pragma_table_info('sessions') WHERE name=?`, column).Scan(&count); err != nil || count != 1 {
			t.Fatalf("migrated column %s count=%d err=%v", column, count, err)
		}
	}
	if err := store.UpdateSessionRuntime(context.Background(), loaded.ID, RuntimeTransportHTTP2,
		"explicit HTTP/2", "GET", "body matcher requires GET"); err != nil {
		t.Fatal(err)
	}
	loaded, err = store.GetSession(context.Background(), loaded.ID)
	if err != nil || loaded.RuntimeTransport != RuntimeTransportHTTP2 || loaded.ResponseOptimizationStrategy != "GET" {
		t.Fatalf("migrated runtime update = %#v, err=%v", loaded, err)
	}
}

func BenchmarkStoreInsertResultBatch1000(b *testing.B) {
	store, err := OpenStore(filepath.Join(b.TempDir(), "batch.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	session := testSession("batch-benchmark")
	if err := store.CreateSession(context.Background(), &session); err != nil {
		b.Fatal(err)
	}
	results := make([]StoredResult, resultBatchSize)
	for index := range results {
		results[index] = StoredResult{
			Input: map[string][]byte{"FUZZ": []byte("admin")}, Position: index,
			StatusCode: 403, URL: "https://example.test/admin", CreatedAt: time.Now().UTC(),
		}
	}
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		batch := append([]StoredResult(nil), results...)
		if _, _, err := store.InsertResultBatch(context.Background(), session.ID, batch, nil); err != nil {
			b.Fatal(err)
		}
	}
}
