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

func addFiltersRequest(t *testing.T, server *Server, sessionID, body string) (*httptest.ResponseRecorder, Session) {
	t.Helper()
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authenticatedRequest(http.MethodPatch,
		"/api/v1/sessions/"+sessionID+"/filters", strings.NewReader(body)))
	var session Session
	if recorder.Code == http.StatusOK {
		if err := json.Unmarshal(recorder.Body.Bytes(), &session); err != nil {
			t.Fatal(err)
		}
	}
	return recorder, session
}

func TestAddSessionFiltersRejectsInvalidBatchWithoutPartialChanges(t *testing.T) {
	server := testServer(t)
	session := testSession("invalid-live-filter")
	session.Status = StatusCompleted
	if err := server.store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	if _, _, err := server.store.InsertResultBatch(context.Background(), session.ID, []StoredResult{{
		URL: "https://example.test/original", StatusCode: 403, ContentWords: 7, CreatedAt: time.Now().UTC(),
	}}, nil); err != nil {
		t.Fatal(err)
	}

	recorder, _ := addFiltersRequest(t, server, session.ID, "{\"status\":\"403\",\"words\":\"not-a-range\"}")
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid filter status = %d: %s", recorder.Code, recorder.Body.String())
	}
	unchanged, err := server.store.GetSession(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Options.Filter.Status != "" || unchanged.Options.Filter.Words != "" ||
		unchanged.ResultFilters.Status != "" || unchanged.ResultFilters.Words != "" ||
		unchanged.ResultCount != 1 {
		t.Fatalf("invalid batch partially changed session: %#v", unchanged)
	}
}

func TestAddSessionFiltersIsIdempotent(t *testing.T) {
	server := testServer(t)
	session := testSession("idempotent-live-filter")
	session.Status = StatusCompleted
	if err := server.store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	if _, _, err := server.store.InsertResultBatch(context.Background(), session.ID, []StoredResult{
		{URL: "https://example.test/hidden", StatusCode: 403, CreatedAt: time.Now().UTC()},
		{URL: "https://example.test/visible", StatusCode: 200, CreatedAt: time.Now().UTC()},
	}, nil); err != nil {
		t.Fatal(err)
	}

	firstRecorder, first := addFiltersRequest(t, server, session.ID, "{\"status\":\"403\"}")
	if firstRecorder.Code != http.StatusOK {
		t.Fatalf("first addition = %d: %s", firstRecorder.Code, firstRecorder.Body.String())
	}
	secondRecorder, second := addFiltersRequest(t, server, session.ID, "{\"status\":\"403\"}")
	if secondRecorder.Code != http.StatusOK {
		t.Fatalf("retry = %d: %s", secondRecorder.Code, secondRecorder.Body.String())
	}
	if second.Options.Filter.Status != "403" {
		t.Fatalf("retry duplicated filter: options=%q", second.Options.Filter.Status)
	}
	if strings.Contains(secondRecorder.Body.String(), "result_filters") {
		t.Fatalf("API exposed internal result-filter bookkeeping: %s", secondRecorder.Body.String())
	}
	persisted, err := server.store.GetSession(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ResultFilters.Status != "403" {
		t.Fatalf("persisted retroactive filter = %q", persisted.ResultFilters.Status)
	}
	if second.Sequence != first.Sequence {
		t.Fatalf("idempotent retry advanced sequence from %d to %d", first.Sequence, second.Sequence)
	}
	if second.ResultCount != 1 {
		t.Fatalf("idempotent visible count = %d, want 1", second.ResultCount)
	}
}

func TestAddSessionFiltersRejectsNonRetroactiveChangesAfterStart(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		mutate func(*Session)
	}{
		{name: "regexp", body: "{\"regexp\":\"secret\"}"},
		{name: "mode change", body: "{\"mode\":\"and\",\"words\":\"7\"}"},
		{name: "existing and mode", body: "{\"words\":\"7\"}", mutate: func(session *Session) {
			session.Options.Filter.Mode = "and"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := testServer(t)
			session := testSession("conflicting-live-filter")
			session.Status = StatusCompleted
			if test.mutate != nil {
				test.mutate(&session)
			}
			if err := server.store.CreateSession(context.Background(), &session); err != nil {
				t.Fatal(err)
			}
			recorder, _ := addFiltersRequest(t, server, session.ID, test.body)
			if recorder.Code != http.StatusConflict {
				t.Fatalf("conflicting addition = %d: %s", recorder.Code, recorder.Body.String())
			}
			unchanged, err := server.store.GetSession(context.Background(), session.ID)
			if err != nil {
				t.Fatal(err)
			}
			if unchanged.ResultFilters != session.ResultFilters {
				t.Fatalf("conflicting addition changed result filters: %#v", unchanged.ResultFilters)
			}
		})
	}
}

func TestAddRegexpFilterWhileIdlePersistsAsEngineConfigurationOnly(t *testing.T) {
	server := testServer(t)
	session := testSession("idle-regexp-filter")
	if err := server.store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	recorder, updated := addFiltersRequest(t, server, session.ID, "{\"regexp\":\"secret|token\"}")
	if recorder.Code != http.StatusOK {
		t.Fatalf("idle regexp addition = %d: %s", recorder.Code, recorder.Body.String())
	}
	if updated.Options.Filter.Regexp != "secret|token" {
		t.Fatalf("idle regexp = %q", updated.Options.Filter.Regexp)
	}
	if updated.ResultFilters.Regexp != "" {
		t.Fatalf("idle regexp was incorrectly marked retroactive: %#v", updated.ResultFilters)
	}
	persisted, err := server.store.GetSession(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ResultFilters.Regexp != "" || persisted.Options.Filter.Regexp != "secret|token" {
		t.Fatalf("idle regexp persistence = options %q, retroactive %q",
			persisted.Options.Filter.Regexp, persisted.ResultFilters.Regexp)
	}
}

func TestResultBatchAfterFilterSaveKeepsRawRowsButCountsAndReturnsVisibleOnly(t *testing.T) {
	server := testServer(t)
	session := testSession("filtered-result-batch")
	session.Status = StatusCompleted
	if err := server.store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	recorder, _ := addFiltersRequest(t, server, session.ID, "{\"status\":\"403\"}")
	if recorder.Code != http.StatusOK {
		t.Fatalf("add status filter = %d: %s", recorder.Code, recorder.Body.String())
	}
	visible, _, err := server.store.InsertResultBatch(context.Background(), session.ID, []StoredResult{
		{URL: "https://example.test/late-hidden", StatusCode: 403, CreatedAt: time.Now().UTC()},
		{URL: "https://example.test/late-visible", StatusCode: 200, CreatedAt: time.Now().UTC()},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(visible) != 1 || visible[0].URL != "https://example.test/late-visible" {
		t.Fatalf("visible committed result event payload = %#v", visible)
	}
	updated, err := server.store.GetSession(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ResultCount != 1 {
		t.Fatalf("visible result count = %d, want 1", updated.ResultCount)
	}
	var rawCount int
	if err := server.store.db.QueryRow("SELECT count(*) FROM results WHERE session_id=?", session.ID).Scan(&rawCount); err != nil {
		t.Fatal(err)
	}
	if rawCount != 2 {
		t.Fatalf("raw retained result count = %d, want 2", rawCount)
	}
	page, err := server.store.Results(context.Background(), session.ID, 0, 10)
	if err != nil || len(page.Results) != 1 || page.Results[0].URL != "https://example.test/late-visible" {
		t.Fatalf("filtered result page = %#v, err=%v", page, err)
	}
}

func TestConcurrentSessionFilterAdditionsDoNotLoseUpdates(t *testing.T) {
	server := testServer(t)
	session := testSession("concurrent-live-filters")
	session.Status = StatusCompleted
	if err := server.store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errors := make(chan error, 2)
	var workers sync.WaitGroup
	for _, request := range []AddSessionFiltersRequest{{Status: "403"}, {Words: "7"}} {
		request := request
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			_, err := server.manager.AddFilters(context.Background(), session.ID, request)
			errors <- err
		}()
	}
	close(start)
	workers.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	updated, err := server.store.GetSession(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Options.Filter.Status != "403" || updated.Options.Filter.Words != "7" ||
		updated.ResultFilters.Status != "403" || updated.ResultFilters.Words != "7" {
		t.Fatalf("concurrent additions lost an update: options=%#v result_filters=%#v",
			updated.Options.Filter, updated.ResultFilters)
	}
	if !containsString(updated.ExplicitFlags, "fc") || !containsString(updated.ExplicitFlags, "fw") {
		t.Fatalf("concurrent additions lost explicit flags: %v", updated.ExplicitFlags)
	}
}

func TestStoredMetricFilterFamilyIsRetroactive(t *testing.T) {
	tests := []struct {
		name    string
		request AddSessionFiltersRequest
		hidden  StoredResult
		visible StoredResult
	}{
		{name: "status", request: AddSessionFiltersRequest{Status: "403"},
			hidden: StoredResult{StatusCode: 403}, visible: StoredResult{StatusCode: 200}},
		{name: "size", request: AddSessionFiltersRequest{Size: "100"},
			hidden: StoredResult{ContentLength: 100}, visible: StoredResult{ContentLength: 101}},
		{name: "words", request: AddSessionFiltersRequest{Words: "7"},
			hidden: StoredResult{ContentWords: 7}, visible: StoredResult{ContentWords: 8}},
		{name: "lines", request: AddSessionFiltersRequest{Lines: "5"},
			hidden: StoredResult{ContentLines: 5}, visible: StoredResult{ContentLines: 6}},
		{name: "time", request: AddSessionFiltersRequest{Time: ">50"},
			hidden:  StoredResult{DurationNS: int64(51 * time.Millisecond)},
			visible: StoredResult{DurationNS: int64(50 * time.Millisecond)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := testServer(t)
			session := testSession("metric-" + test.name)
			session.Status = StatusCompleted
			if err := server.store.CreateSession(context.Background(), &session); err != nil {
				t.Fatal(err)
			}
			test.hidden.URL = "https://example.test/hidden"
			test.hidden.CreatedAt = time.Now().UTC()
			test.visible.URL = "https://example.test/visible"
			test.visible.CreatedAt = time.Now().UTC()
			if _, _, err := server.store.InsertResultBatch(context.Background(), session.ID,
				[]StoredResult{test.hidden, test.visible}, nil); err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(test.request)
			if err != nil {
				t.Fatal(err)
			}
			recorder, updated := addFiltersRequest(t, server, session.ID, string(body))
			if recorder.Code != http.StatusOK {
				t.Fatalf("add metric filter = %d: %s", recorder.Code, recorder.Body.String())
			}
			if updated.ResultCount != 1 {
				t.Fatalf("filtered count = %d, want 1", updated.ResultCount)
			}
			page, err := server.store.Results(context.Background(), session.ID, 0, 10)
			if err != nil || len(page.Results) != 1 || page.Results[0].URL != "https://example.test/visible" {
				t.Fatalf("filtered %s page = %#v, err=%v", test.name, page, err)
			}
		})
	}
}

func TestFilteredResultPaginationAndExportsSkipHiddenRows(t *testing.T) {
	server := testServer(t)
	session := testSession("filtered-pagination")
	session.Status = StatusCompleted
	if err := server.store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	recorder, _ := addFiltersRequest(t, server, session.ID, "{\"status\":\"403\"}")
	if recorder.Code != http.StatusOK {
		t.Fatalf("add status filter = %d: %s", recorder.Code, recorder.Body.String())
	}
	statuses := []int64{403, 403, 200, 403, 200, 403, 200}
	results := make([]StoredResult, len(statuses))
	for index, status := range statuses {
		results[index] = StoredResult{
			URL:        "https://example.test/item-" + string(rune('1'+index)),
			StatusCode: status,
			CreatedAt:  time.Now().UTC(),
		}
	}
	if _, _, err := server.store.InsertResultBatch(context.Background(), session.ID, results, nil); err != nil {
		t.Fatal(err)
	}
	first, err := server.store.Results(context.Background(), session.ID, 0, 2)
	if err != nil || len(first.Results) != 2 || !first.HasMore ||
		first.Results[0].URL != "https://example.test/item-3" || first.Results[1].URL != "https://example.test/item-5" {
		t.Fatalf("first filtered page = %#v, err=%v", first, err)
	}
	second, err := server.store.Results(context.Background(), session.ID, first.NextCursor, 2)
	if err != nil || len(second.Results) != 1 || second.HasMore || second.Results[0].URL != "https://example.test/item-7" {
		t.Fatalf("second filtered page = %#v, err=%v", second, err)
	}
	latest, err := server.store.LatestResults(context.Background(), session.ID, 2)
	if err != nil || len(latest.Results) != 2 || !latest.WindowTruncated ||
		latest.Results[0].URL != "https://example.test/item-5" || latest.Results[1].URL != "https://example.test/item-7" {
		t.Fatalf("latest filtered window = %#v, err=%v", latest, err)
	}
	for _, format := range []string{"json", "text", "terminal"} {
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, authenticatedRequest(http.MethodGet,
			"/api/v1/sessions/"+session.ID+"/export?format="+format, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s export = %d: %s", format, recorder.Code, recorder.Body.String())
		}
		body := recorder.Body.String()
		if strings.Contains(body, "item-1") || strings.Contains(body, "item-2") || strings.Contains(body, "item-4") || strings.Contains(body, "item-6") {
			t.Fatalf("%s export included filtered rows: %s", format, body)
		}
		for _, visible := range []string{"item-3", "item-5", "item-7"} {
			if !strings.Contains(body, visible) {
				t.Fatalf("%s export omitted %s: %s", format, visible, body)
			}
		}
	}
}

func TestFilterPatchRacingFinalResultBatchLeavesConsistentVisibleState(t *testing.T) {
	server := testServer(t)
	session := testSession("filter-final-batch-race")
	session.Status = StatusCompleted
	if err := server.store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	results := make([]StoredResult, 1000)
	for index := range results {
		results[index] = StoredResult{
			URL:        "https://example.test/final-batch",
			StatusCode: 403,
			CreatedAt:  time.Now().UTC(),
		}
	}
	start := make(chan struct{})
	type workerResult struct {
		name string
		err  error
	}
	resultsCh := make(chan workerResult, 2)
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		<-start
		_, _, err := server.store.InsertResultBatch(context.Background(), session.ID, results, nil)
		resultsCh <- workerResult{name: "insert final result batch", err: err}
	}()
	go func() {
		defer workers.Done()
		<-start
		_, err := server.manager.AddFilters(context.Background(), session.ID, AddSessionFiltersRequest{Status: "403"})
		resultsCh <- workerResult{name: "persist filter patch", err: err}
	}()
	close(start)
	workers.Wait()
	close(resultsCh)
	for result := range resultsCh {
		if result.err != nil {
			t.Fatalf("%s: %v", result.name, result.err)
		}
	}

	updated, err := server.store.GetSession(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ResultCount != 0 {
		t.Fatalf("visible result count after race = %d, want 0", updated.ResultCount)
	}
	page, err := server.store.Results(context.Background(), session.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Results) != 0 || page.HasMore {
		t.Fatalf("filtered page after race = %#v", page)
	}
	var rawCount int
	if err := server.store.db.QueryRow("SELECT count(*) FROM results WHERE session_id=?", session.ID).Scan(&rawCount); err != nil {
		t.Fatal(err)
	}
	if rawCount != len(results) {
		t.Fatalf("raw rows after race = %d, want %d", rawCount, len(results))
	}
}

func TestWordFilterAddedToRunningSessionFiltersHistoricAndFutureResults(t *testing.T) {
	releaseSecond := make(chan struct{})
	secondStarted := make(chan struct{})
	var signalSecond sync.Once
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/two") {
			signalSecond.Do(func() { close(secondStarted) })
			<-releaseSecond
		}
		_, _ = w.Write([]byte("alpha beta"))
	}))
	defer target.Close()
	wordlist := filepath.Join(t.TempDir(), "live-filter-words.txt")
	if err := os.WriteFile(wordlist, []byte("one\ntwo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := testServer(t)
	options := ffuf.NewConfigOptions()
	options.HTTP.URL = target.URL + "/FUZZ"
	options.HTTP.NoHttp3 = true
	options.HTTP.NoResponseOptimization = true
	options.HTTP.Timeout = 5
	options.Input.InputMode = "pitchfork"
	options.Input.Wordlists = []string{wordlist}
	options.General.Threads = 1
	options.Matcher.Status = "all"
	session, err := server.manager.Create(context.Background(), CreateSessionRequest{
		Target: options.HTTP.URL, Wordlist: wordlist, Profile: "dir", Options: options,
		ExplicitFlags: []string{"w", "t", "mc", "no-http3", "no-response-optimization", "timeout"},
		AutoStart:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-secondStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("second request did not start")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var rawCount int
		if err := server.store.db.QueryRow("SELECT count(*) FROM results WHERE session_id=?", session.ID).Scan(&rawCount); err != nil {
			t.Fatal(err)
		}
		if rawCount == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("first result was not persisted before filter addition; raw count=%d", rawCount)
		}
		time.Sleep(10 * time.Millisecond)
	}
	recorder, updated := addFiltersRequest(t, server, session.ID, "{\"words\":\"2\"}")
	if recorder.Code != http.StatusOK {
		t.Fatalf("add live word filter = %d: %s", recorder.Code, recorder.Body.String())
	}
	if updated.Options.Filter.Words != "2" || updated.ResultCount != 0 {
		t.Fatalf("live word filter response = words %q count %d", updated.Options.Filter.Words, updated.ResultCount)
	}
	close(releaseSecond)
	waitForSessionStatus(t, server.store, session.ID, 5*time.Second, StatusCompleted)

	page, err := server.store.Results(context.Background(), session.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Results) != 0 {
		t.Fatalf("word-filtered results = %#v", page.Results)
	}
	var rawCount int
	if err := server.store.db.QueryRow("SELECT count(*) FROM results WHERE session_id=?", session.ID).Scan(&rawCount); err != nil {
		t.Fatal(err)
	}
	if rawCount != 1 {
		t.Fatalf("future word-filtered response was persisted; raw count=%d, want historic row only", rawCount)
	}
}
