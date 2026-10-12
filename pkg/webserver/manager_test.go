package webserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

func TestWebExecutionPolicyMatchesCLI(t *testing.T) {
	tests := []struct {
		name    string
		flags   []string
		status  bool
		threads bool
	}{
		{name: "no matcher uses default status", status: true},
		{name: "explicit status", flags: []string{"mc"}, status: true},
		{name: "only size matcher", flags: []string{"ms"}, status: false},
		{name: "explicit default thread count", flags: []string{"t"}, status: true, threads: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status, threads := webExecutionPolicy(test.flags)
			if status != test.status || threads != test.threads {
				t.Fatalf("got status=%v threads=%v", status, threads)
			}
		})
	}
}

func TestWebSessionPreservesAutomaticAndExplicitWorkerProvenance(t *testing.T) {
	wordlist := filepath.Join(t.TempDir(), "words.txt")
	if err := os.WriteFile(wordlist, []byte("admin\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name            string
		threads         int
		explicit        []string
		wantFixed       bool
		maxWorkers      int
		wantMaxExplicit bool
	}{
		{name: "automatic web default omitted t", threads: defaultWebThreadCount, explicit: []string{"w"}, maxWorkers: ffuf.MaxHTTP3Workers},
		{name: "explicit default t40", threads: 40, explicit: []string{"w", "t"}, wantFixed: true, maxWorkers: ffuf.MaxHTTP3Workers},
		{name: "explicit t1000 and default max", threads: 1000, explicit: []string{"w", "t", "http3-max-workers"}, wantFixed: true, maxWorkers: ffuf.MaxHTTP3Workers, wantMaxExplicit: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			opts := ffuf.NewConfigOptions()
			opts.HTTP.URL = "https://example.test/FUZZ"
			opts.HTTP.Http3MaxWorkers = test.maxWorkers
			opts.Input.Wordlists = []string{wordlist}
			opts.General.Threads = test.threads
			session, err := normalizeCreate(CreateSessionRequest{
				Target: opts.HTTP.URL, Wordlist: wordlist, Profile: "dir", Options: opts, ExplicitFlags: test.explicit,
			}, DefaultSettings())
			if err != nil {
				t.Fatal(err)
			}
			ffuf.MarkExplicitOptions(&session.Options, session.ExplicitFlags...)
			conf, err := ffuf.ConfigFromOptions(&session.Options, context.Background(), func() {})
			if err != nil {
				t.Fatal(err)
			}
			_, conf.ThreadsExplicit = webExecutionPolicy(session.ExplicitFlags)
			if conf.Threads != test.threads || conf.ThreadsExplicit != test.wantFixed ||
				conf.Http3MaxWorkers != test.maxWorkers || conf.Http3MaxWorkersExplicit != test.wantMaxExplicit {
				t.Fatalf("web worker provenance = threads %d/%t max %d/%t flags %v", conf.Threads, conf.ThreadsExplicit, conf.Http3MaxWorkers, conf.Http3MaxWorkersExplicit, session.ExplicitFlags)
			}
		})
	}
}

func TestConcurrentEventsAreObservedInSequence(t *testing.T) {
	store, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	session := testSession("sequences")
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
	const count = 50
	var wg sync.WaitGroup
	wg.Add(count)
	for i := 0; i < count; i++ {
		go func(value int) {
			defer wg.Done()
			manager.emit(session.ID, "test", value)
		}(i)
	}
	wg.Wait()
	for expected := int64(1); expected <= count; expected++ {
		message := <-client.send
		payload, err := message.encode()
		if err != nil {
			t.Fatal(err)
		}
		var event Event
		if err := json.Unmarshal(payload, &event); err != nil {
			t.Fatal(err)
		}
		if event.Sequence != expected {
			t.Fatalf("event sequence = %d, want %d", event.Sequence, expected)
		}
	}
	loaded, err := store.GetSession(context.Background(), session.ID)
	if err != nil || loaded.Sequence != count {
		t.Fatalf("persisted sequence = %d, err=%v", loaded.Sequence, err)
	}
}

func TestManagerRejectsSessionWithoutActiveFuzzKeyword(t *testing.T) {
	store, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager, err := NewManager(store, NewHub(), false)
	if err != nil {
		t.Fatal(err)
	}
	wordlist := filepath.Join(t.TempDir(), "words.txt")
	if err := os.WriteFile(wordlist, []byte("admin\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := ffuf.NewConfigOptions()
	opts.Input.Wordlists = []string{wordlist}
	opts.HTTP.URL = "https://example.test/no-keyword"

	_, err = manager.Create(context.Background(), CreateSessionRequest{
		Target: opts.HTTP.URL, Wordlist: wordlist, Profile: "dir", Options: opts,
		ExplicitFlags: []string{"w"},
	})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "fuzz keyword") {
		t.Fatalf("Create error = %v, want an actionable missing fuzz keyword error", err)
	}
	sessions, listErr := store.ListSessions(context.Background())
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(sessions) != 0 {
		t.Fatalf("invalid session was persisted: %+v", sessions)
	}
}

func TestManagerAllowsConfiguredCustomKeywordInHeader(t *testing.T) {
	store, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager, err := NewManager(store, NewHub(), false)
	if err != nil {
		t.Fatal(err)
	}
	wordlist := filepath.Join(t.TempDir(), "words.txt")
	if err := os.WriteFile(wordlist, []byte("admin\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := ffuf.NewConfigOptions()
	opts.Input.Wordlists = []string{wordlist + ":PATH"}
	opts.HTTP.Headers = []string{"X-Discovery: PATH"}
	opts.HTTP.URL = "https://example.test/no-url-keyword"

	session, err := manager.Create(context.Background(), CreateSessionRequest{
		Target: opts.HTTP.URL, Wordlist: wordlist, Profile: "dir", Options: opts,
		ExplicitFlags: []string{"w", "H"},
	})
	if err != nil {
		t.Fatalf("custom keyword session was rejected: %v", err)
	}
	if session.Status != StatusIdle {
		t.Fatalf("custom keyword session status = %q, want idle", session.Status)
	}
}

func TestManagerMarksZeroWorkRunFailed(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer target.Close()
	wordlist := filepath.Join(t.TempDir(), "empty.txt")
	if err := os.WriteFile(wordlist, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager, err := NewManager(store, NewHub(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = manager.Shutdown(ctx)
	}()
	opts := ffuf.NewConfigOptions()
	opts.Input.InputMode = "pitchfork"
	opts.Input.Wordlists = []string{wordlist}
	opts.HTTP.URL = target.URL + "/FUZZ"
	opts.HTTP.NoHttp3 = true
	opts.HTTP.NoResponseOptimization = true
	opts.General.Threads = 1

	session, err := manager.Create(context.Background(), CreateSessionRequest{
		Target: opts.HTTP.URL, Wordlist: wordlist, Profile: "dir", Options: opts,
		ExplicitFlags: []string{"w", "t", "no-http3", "no-response-optimization"}, AutoStart: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		session, err = store.GetSession(context.Background(), session.ID)
		if err != nil {
			t.Fatal(err)
		}
		if session.Status == StatusCompleted || session.Status == StatusFailed {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if session.Status != StatusFailed {
		t.Fatalf("zero-work session status = %q, want failed", session.Status)
	}
	wantParts := []string{"zero requests", "fuzz keyword", "empty wordlist"}
	for _, part := range wantParts {
		if !strings.Contains(strings.ToLower(session.Error), part) {
			t.Errorf("zero-work error %q does not mention %q", session.Error, part)
		}
	}
}

func TestManagerStartRejectsPersistedSessionWithoutKeywordBeforeStarting(t *testing.T) {
	wordlist := filepath.Join(t.TempDir(), "words.txt")
	if err := os.WriteFile(wordlist, []byte("admin\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	session := testSession("legacy-no-keyword")
	session.Target = "https://example.test/no-keyword"
	session.Wordlist = wordlist
	session.Options = *ffuf.NewConfigOptions()
	session.Options.HTTP.URL = session.Target
	session.Options.Input.InputMode = "pitchfork"
	session.Options.Input.Wordlists = []string{wordlist}
	if err := store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(store, NewHub(), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background(), session.ID); err == nil ||
		!strings.Contains(strings.ToLower(err.Error()), "fuzz keyword") {
		t.Fatalf("Start error = %v, want missing fuzz keyword", err)
	}
	loaded, err := store.GetSession(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != StatusIdle || loaded.StartedAt != nil {
		t.Fatalf("invalid persisted session transitioned before validation: status=%q started_at=%v", loaded.Status, loaded.StartedAt)
	}
}

func TestManagerPersistsActualHTTP1FallbackAfterAutomaticHTTP3Probe(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer target.Close()
	wordlist := filepath.Join(t.TempDir(), "words.txt")
	if err := os.WriteFile(wordlist, []byte("admin\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager, err := NewManager(store, NewHub(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = manager.Shutdown(ctx)
	}()
	opts := ffuf.NewConfigOptions()
	opts.HTTP.URL = target.URL + "/FUZZ"
	opts.HTTP.Http3ProbeTimeout = 1
	opts.HTTP.NoResponseOptimization = true
	opts.Input.InputMode = "pitchfork"
	opts.Input.Wordlists = []string{wordlist}
	opts.General.Threads = 1
	session, err := manager.Create(context.Background(), CreateSessionRequest{
		Target: opts.HTTP.URL, Wordlist: wordlist, Profile: "dir", Options: opts,
		ExplicitFlags: []string{"w", "t", "http3-probe-timeout", "no-response-optimization"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background(), session.ID); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.GetSession(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.RuntimeTransport != RuntimeTransportHTTP1 {
		t.Fatalf("runtime transport = %q, want actual HTTP/1 fallback", loaded.RuntimeTransport)
	}
	if loaded.RuntimeTransportReason == "" {
		t.Fatal("automatic HTTP/3 downgrade reason was not persisted")
	}
	if loaded.ResponseOptimizationStrategy == "" || loaded.ResponseOptimizationReason == "" {
		t.Fatalf("response optimization decision was not persisted: strategy=%q reason=%q",
			loaded.ResponseOptimizationStrategy, loaded.ResponseOptimizationReason)
	}
}

func TestProgressWatchdogDistinguishesRemoteInactivityFromPersistenceBackpressure(t *testing.T) {
	started := time.Now()
	remote := &activeRun{lastCompletedCount: 100, lastCompletionChange: started, stallAfter: time.Second}
	reason, stalled, warn, _ := remote.observeProgress(100, webOutputStats{}, started.Add(2*time.Second))
	if !warn || stalled != 2000 || !strings.Contains(reason, "persistence has capacity") {
		t.Fatalf("remote inactivity diagnosis = reason %q stalled=%d warn=%v", reason, stalled, warn)
	}
	if _, _, warnAgain, _ := remote.observeProgress(100, webOutputStats{}, started.Add(3*time.Second)); warnAgain {
		t.Fatal("watchdog emitted duplicate warning for one inactivity episode")
	}
	if _, _, _, recovered := remote.observeProgress(101, webOutputStats{}, started.Add(4*time.Second)); !recovered {
		t.Fatal("watchdog did not report completion recovery")
	}

	persistence := &activeRun{lastCompletedCount: 200, lastCompletionChange: started, stallAfter: time.Second}
	reason, _, warn, _ = persistence.observeProgress(200, webOutputStats{PersistenceBackpressure: true}, started.Add(2*time.Second))
	if !warn || reason != "result persistence backpressure" {
		t.Fatalf("persistence diagnosis = reason %q warn=%v", reason, warn)
	}
	if message := stallWarningMessage(reason, 2*time.Second); !strings.Contains(message, "SQLite") || strings.Contains(message, "target or intermediary") {
		t.Fatalf("persistence warning blamed the remote target: %q", message)
	}
	if message := stallWarningMessage("no completed requests while result persistence has capacity", 2*time.Second); !strings.Contains(message, "target or intermediary") || strings.Contains(message, "SQLite") || strings.Contains(message, "HTTP/3") {
		t.Fatalf("remote warning did not describe remote throttling: %q", message)
	}
}
