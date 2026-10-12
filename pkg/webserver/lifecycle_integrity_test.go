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

	"github.com/assetnote/h3fuzz/pkg/engine"
	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

func TestCreateWithAutoStartReturnsPersistedFailureAsAuthoritativeSession(t *testing.T) {
	server := testServer(t)
	wordlist := filepath.Join(t.TempDir(), "words.txt")
	if err := os.WriteFile(wordlist, []byte("admin\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	options := ffuf.NewConfigOptions()
	options.HTTP.URL = "https://example.test/FUZZ"
	options.HTTP.NoHttp3 = true
	options.HTTP.NoResponseOptimization = true
	options.Input.InputMode = "pitchfork"
	options.Input.Wordlists = []string{wordlist}
	server.manager.buildJob = func(*ffuf.Config) (*engine.Job, error) {
		return nil, errors.New("deliberate auto-start failure")
	}
	payload, err := json.Marshal(CreateSessionRequest{
		Target: options.HTTP.URL, Wordlist: wordlist, Profile: "dir", Options: options,
		ExplicitFlags: []string{"w", "no-http3", "no-response-optimization"}, AutoStart: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authenticatedRequest(http.MethodPost, "/api/v1/sessions", bytes.NewReader(payload)))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("auto-start create = %d %s, want 201 with the persisted session", recorder.Code, recorder.Body.String())
	}
	var created Session
	if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Status != StatusFailed || !strings.Contains(created.Error, "deliberate auto-start failure") {
		t.Fatalf("create response is not the authoritative failure: %+v", created)
	}
	persisted, err := server.store.GetSession(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != created.Status || persisted.Error != created.Error || persisted.Sequence != created.Sequence {
		t.Fatalf("response %+v does not match persisted session %+v", created, persisted)
	}
	sessions, err := server.store.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("auto-start failure persisted %d sessions, want exactly one", len(sessions))
	}
}

func TestCloneWithAutoStartFailurePreservesProvenanceAndReturnsClone(t *testing.T) {
	server := testServer(t)
	wordlist := filepath.Join(t.TempDir(), "clone-words.txt")
	if err := os.WriteFile(wordlist, []byte("admin\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	options := ffuf.NewConfigOptions()
	options.HTTP.URL = "https://example.test/FUZZ"
	options.HTTP.NoHttp3 = true
	options.HTTP.NoResponseOptimization = true
	options.Input.InputMode = "pitchfork"
	options.Input.Wordlists = []string{wordlist}
	source, err := server.manager.Create(context.Background(), CreateSessionRequest{
		Name: "source", Target: options.HTTP.URL, Wordlist: wordlist, Profile: "dir", Options: options,
		ExplicitFlags: []string{"w", "no-http3", "no-response-optimization"},
	})
	if err != nil {
		t.Fatal(err)
	}
	provenanceAtStart := ""
	server.manager.buildJob = func(*ffuf.Config) (*engine.Job, error) {
		sessions, listErr := server.store.ListSessions(context.Background())
		if listErr != nil {
			return nil, listErr
		}
		for _, candidate := range sessions {
			if candidate.ID != source.ID {
				provenanceAtStart = candidate.ClonedFromID
			}
		}
		return nil, errors.New("deliberate clone start failure")
	}

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authenticatedRequest(http.MethodPost,
		"/api/v1/sessions/"+source.ID+"/clone", strings.NewReader(`{"auto_start":true}`)))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("auto-start clone = %d %s, want 201 with the persisted clone", recorder.Code, recorder.Body.String())
	}
	var clone Session
	if err := json.Unmarshal(recorder.Body.Bytes(), &clone); err != nil {
		t.Fatal(err)
	}
	if clone.ID == "" || clone.ID == source.ID || clone.Status != StatusFailed ||
		!strings.Contains(clone.Error, "deliberate clone start failure") || clone.ClonedFromID != source.ID {
		t.Fatalf("clone response lost failure state or provenance: %+v", clone)
	}
	if provenanceAtStart != source.ID {
		t.Fatalf("clone provenance at auto-start = %q, want %q before runtime initialization", provenanceAtStart, source.ID)
	}
	persisted, err := server.store.GetSession(context.Background(), clone.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ClonedFromID != source.ID || persisted.Status != StatusFailed || persisted.Error != clone.Error {
		t.Fatalf("persisted clone is not authoritative: %+v", persisted)
	}
}

func TestLiveFilterEngineFailureRollsBackPersistedFilterWithoutLosingResults(t *testing.T) {
	server := testServer(t)
	session := testSession("live-filter-engine-failure")
	session.Status = StatusRunning
	if err := server.store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	if _, _, err := server.store.InsertResultBatch(context.Background(), session.ID, []StoredResult{
		{URL: "https://example.test/hidden-if-applied", StatusCode: 403},
		{URL: "https://example.test/visible", StatusCode: 200},
	}, nil); err != nil {
		t.Fatal(err)
	}
	// Inject a rejected engine batch at the active-run boundary. Production
	// jobs use engine.Job.AddFilters through the same seam.
	server.manager.mu.Lock()
	server.manager.active[session.ID] = &activeRun{addJobFilters: func(map[string]string) error {
		return errors.New("injected matcher update failure")
	}}
	server.manager.mu.Unlock()

	_, err := server.manager.AddFilters(context.Background(), session.ID, AddSessionFiltersRequest{Status: "403"})
	if err == nil || !strings.Contains(err.Error(), "injected matcher update failure") {
		t.Fatalf("AddFilters error = %v, want injected engine failure", err)
	}
	persisted, err := server.store.GetSession(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Options.Filter.Status != "" || persisted.ResultFilters.Status != "" {
		t.Fatalf("failed live filter remained persisted: options=%q result_filters=%q",
			persisted.Options.Filter.Status, persisted.ResultFilters.Status)
	}
	if persisted.ResultCount != 2 {
		t.Fatalf("failed live filter changed visible result count to %d, want 2", persisted.ResultCount)
	}
	page, err := server.store.Results(context.Background(), session.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Results) != 2 {
		t.Fatalf("failed live filter lost stored results: %#v", page.Results)
	}
	state := server.manager.sequenceFor(session.ID)
	filtered, ok := server.manager.filterResultEvent(state.resultFilter, map[string]interface{}{
		"results": page.Results, "next_cursor": page.Results[len(page.Results)-1].ID,
	})
	if !ok || len(filtered.(map[string]interface{})["results"].([]StoredResult)) != 2 {
		t.Fatalf("failed live filter remained in the result-event cache: %#v", filtered)
	}
}

func TestPausePersistenceFailureRestoresRunningRuntimeState(t *testing.T) {
	server := testServer(t)
	session := testSession("pause-persistence-failure")
	session.Status = StatusRunning
	if err := server.store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	runtimePaused := false
	resumeCalls := 0
	run := &activeRun{
		job:      &engine.Job{},
		pauseJob: func() { runtimePaused = true },
		resumeJob: func() {
			resumeCalls++
			runtimePaused = false
		},
	}
	server.manager.mu.Lock()
	server.manager.active[session.ID] = run
	server.manager.mu.Unlock()
	if _, err := server.store.db.Exec(`CREATE TRIGGER reject_pause BEFORE UPDATE OF status ON sessions
		WHEN NEW.status = 'paused' BEGIN SELECT RAISE(ABORT, 'injected pause persistence failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := server.manager.Pause(context.Background(), session.ID); err == nil ||
		!strings.Contains(err.Error(), "injected pause persistence failure") {
		t.Fatalf("Pause error = %v, want injected persistence failure", err)
	}
	persisted, err := server.store.GetSession(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != StatusRunning {
		t.Fatalf("persisted status = %q, want running", persisted.Status)
	}
	if runtimePaused || run.paused.Load() || resumeCalls != 1 {
		t.Fatalf("runtime was not restored after persistence failed: runtime_paused=%v manager_paused=%v resume_calls=%d",
			runtimePaused, run.paused.Load(), resumeCalls)
	}
	if _, err := server.store.db.Exec(`DROP TRIGGER reject_pause`); err != nil {
		t.Fatal(err)
	}
}

func TestResumePersistenceFailureRestoresPausedRuntimeState(t *testing.T) {
	server := testServer(t)
	session := testSession("resume-persistence-failure")
	session.Status = StatusPaused
	if err := server.store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	runtimePaused := true
	pauseCalls := 0
	run := &activeRun{
		job: &engine.Job{},
		pauseJob: func() {
			pauseCalls++
			runtimePaused = true
		},
		resumeJob: func() { runtimePaused = false },
	}
	run.paused.Store(true)
	server.manager.mu.Lock()
	server.manager.active[session.ID] = run
	server.manager.mu.Unlock()
	if _, err := server.store.db.Exec(`CREATE TRIGGER reject_resume BEFORE UPDATE OF status ON sessions
		WHEN NEW.status = 'running' BEGIN SELECT RAISE(ABORT, 'injected resume persistence failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := server.manager.Resume(context.Background(), session.ID); err == nil ||
		!strings.Contains(err.Error(), "injected resume persistence failure") {
		t.Fatalf("Resume error = %v, want injected persistence failure", err)
	}
	persisted, err := server.store.GetSession(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != StatusPaused {
		t.Fatalf("persisted status = %q, want paused", persisted.Status)
	}
	if !runtimePaused || !run.paused.Load() || pauseCalls != 1 {
		t.Fatalf("runtime was not restored after persistence failed: runtime_paused=%v manager_paused=%v pause_calls=%d",
			runtimePaused, run.paused.Load(), pauseCalls)
	}
}

func TestResumeStartsAFreshCompletionWatchdogWindow(t *testing.T) {
	server := testServer(t)
	session := testSession("resume-watchdog-window")
	session.Status = StatusPaused
	if err := server.store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	run := &activeRun{
		job:                  &engine.Job{},
		pauseJob:             func() {},
		resumeJob:            func() {},
		lastCompletedCount:   0,
		lastCompletionChange: time.Now().Add(-time.Hour),
		stallAfter:           time.Second,
		stallWarned:          true,
	}
	run.paused.Store(true)
	server.manager.mu.Lock()
	server.manager.active[session.ID] = run
	server.manager.mu.Unlock()

	if err := server.manager.Resume(context.Background(), session.ID); err != nil {
		t.Fatal(err)
	}
	if run.paused.Load() {
		t.Fatal("manager remained paused after a successful resume")
	}
	if reason, stalled, warn, recovered := run.observeProgress(0, webOutputStats{}, time.Now()); reason != "" || stalled != 0 || warn || recovered {
		t.Fatalf("resumed watchdog inherited paused inactivity: reason=%q stalled=%d warn=%v recovered=%v",
			reason, stalled, warn, recovered)
	}
}
