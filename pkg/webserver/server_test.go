package webserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
	"github.com/coder/websocket"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	assets := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<!doctype html><title>h3fuzz</title>")},
		"app.js":     &fstest.MapFile{Data: []byte("console.log('ok')")},
	}
	server, err := New(Config{ListenAddress: "127.0.0.1:8787", DatabasePath: filepath.Join(t.TempDir(), "web.db"),
		Username: "h3fuzz", Password: "secret", Assets: fs.FS(assets)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.store.Close() })
	return server
}

func authenticatedRequest(method, target string, body io.Reader) *http.Request {
	request := httptest.NewRequest(method, target, body)
	request.SetBasicAuth("h3fuzz", "secret")
	return request
}

func TestServerRequiresAuthenticationAndServesSPAWithoutRedirect(t *testing.T) {
	server := testServer(t)
	unauthorized := httptest.NewRecorder()
	server.Handler().ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil))
	if unauthorized.Code != http.StatusUnauthorized || unauthorized.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("unauthorized response = %d, headers=%v", unauthorized.Code, unauthorized.Header())
	}
	for _, route := range []string{"/", "/sessions/client-route"} {
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, authenticatedRequest(http.MethodGet, route, nil))
		if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "h3fuzz") {
			t.Fatalf("SPA route %s = %d %q", route, recorder.Code, recorder.Body.String())
		}
		if location := recorder.Header().Get("Location"); location != "" {
			t.Fatalf("SPA route %s unexpectedly redirected to %q", route, location)
		}
		if cacheControl := recorder.Header().Get("Cache-Control"); cacheControl != "no-store" {
			t.Fatalf("SPA route %s Cache-Control = %q, want no-store", route, cacheControl)
		}
	}
}

func TestServerSessionAPIShapesAndPolicies(t *testing.T) {
	server := testServer(t)
	wordlist := filepath.Join(t.TempDir(), "custom.txt")
	if err := os.WriteFile(wordlist, []byte("admin\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(CreateSessionRequest{
		Target: "https://example.test/FUZZ", Profile: "php", Wordlist: wordlist,
	})
	if err != nil {
		t.Fatal(err)
	}
	requestBody := string(payload)
	recorder := httptest.NewRecorder()
	request := authenticatedRequest(http.MethodPost, "/api/v1/sessions", strings.NewReader(requestBody))
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", recorder.Code, recorder.Body.String())
	}
	var created Session
	if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Target != "https://example.test/FUZZ" {
		t.Fatalf("target was silently rewritten: %q", created.Target)
	}
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authenticatedRequest(http.MethodGet, "/api/v1/sessions", nil))
	var sessions []Session
	if err := json.Unmarshal(recorder.Body.Bytes(), &sessions); err != nil || len(sessions) != 1 {
		t.Fatalf("sessions response = %q, err=%v", recorder.Body.String(), err)
	}

	crossSite := authenticatedRequest(http.MethodPost, "/api/v1/sessions", strings.NewReader(requestBody))
	crossSite.Header.Set("Origin", "https://attacker.invalid")
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, crossSite)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("cross-origin create = %d", recorder.Code)
	}

	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authenticatedRequest(http.MethodGet, "/api/v1/does-not-exist", nil))
	if recorder.Code != http.StatusNotFound || !strings.Contains(recorder.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("unknown API = %d %s", recorder.Code, recorder.Header().Get("Content-Type"))
	}
}

func TestInspectWordlistReportsCommentAndMarkerCardinality(t *testing.T) {
	server := testServer(t)
	wordlist := filepath.Join(t.TempDir(), "words.txt")
	content := "admin\n# ignored\n # marker ignored outside DirSearch %EXT%\nindex.%EXT%\napi # trailing comment\n"
	if err := os.WriteFile(wordlist, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]string{"path": wordlist})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authenticatedRequest(
		http.MethodPost,
		"/api/v1/wordlists/inspect",
		bytes.NewReader(payload),
	))
	if recorder.Code != http.StatusOK {
		t.Fatalf("inspect = %d %s", recorder.Code, recorder.Body.String())
	}
	var inspection WordlistInspection
	if err := json.Unmarshal(recorder.Body.Bytes(), &inspection); err != nil {
		t.Fatal(err)
	}
	if inspection.Entries != 5 || inspection.ExtensionMarkers != 2 || inspection.CommentEntries != 2 || inspection.CommentExtensionMarkers != 1 {
		t.Fatalf("unexpected inspection: %+v", inspection)
	}
}

func TestCreateSessionWithoutFuzzKeywordReturnsBadRequest(t *testing.T) {
	server := testServer(t)
	wordlist := filepath.Join(t.TempDir(), "words.txt")
	if err := os.WriteFile(wordlist, []byte("admin\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(CreateSessionRequest{
		Target: "https://example.test/no-keyword", Profile: "dir", Wordlist: wordlist,
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authenticatedRequest(http.MethodPost, "/api/v1/sessions", bytes.NewReader(payload)))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("create without FUZZ = %d %s, want 400", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(strings.ToLower(recorder.Body.String()), "fuzz keyword") {
		t.Fatalf("missing keyword response is not actionable: %s", recorder.Body.String())
	}
}

func TestStopActionReturnsStoppingAndWebSocketThenEmitsStopped(t *testing.T) {
	requestStarted := make(chan struct{})
	var startedOnce sync.Once
	target := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		startedOnce.Do(func() { close(requestStarted) })
		<-r.Context().Done()
	}))
	defer target.Close()
	wordlist := filepath.Join(t.TempDir(), "stop-words.txt")
	if err := os.WriteFile(wordlist, []byte("one\ntwo\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	application, httpServer := newSecurityTestServer(t, false)
	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/api/v1/ws"
	headers := http.Header{}
	headers.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("operator:correct-horse-battery-staple")))
	headers.Set("Origin", httpServer.URL)
	readContext, cancelRead := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelRead()
	connection, _, err := websocket.Dial(readContext, wsURL, &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = connection.CloseNow()
	}()
	if _, _, err := connection.Read(readContext); err != nil { // snapshot
		t.Fatal(err)
	}

	opts := ffuf.NewConfigOptions()
	opts.HTTP.URL = target.URL + "/FUZZ"
	opts.HTTP.NoHttp3 = true
	opts.HTTP.NoResponseOptimization = true
	opts.HTTP.Timeout = 30
	opts.Input.InputMode = "pitchfork"
	opts.Input.Wordlists = []string{wordlist}
	opts.General.Threads = 1
	session, err := application.manager.Create(context.Background(), CreateSessionRequest{
		Target: opts.HTTP.URL, Wordlist: wordlist, Profile: "dir", Options: opts,
		ExplicitFlags: []string{"w", "t", "no-http3", "no-response-optimization", "timeout"}, AutoStart: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-requestStarted:
	case <-readContext.Done():
		t.Fatal("scan request did not start")
	}

	request := securityAuthenticatedRequest(t, http.MethodPost,
		httpServer.URL+"/api/v1/sessions/"+session.ID+"/stop", nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("stop status = %d", response.StatusCode)
	}
	var acknowledged Session
	if err := json.NewDecoder(response.Body).Decode(&acknowledged); err != nil {
		t.Fatal(err)
	}
	if acknowledged.Status != StatusStopping {
		t.Fatalf("stop action session status = %q, want stopping", acknowledged.Status)
	}
	waitForSessionStatus(t, application.store, session.ID, 5*time.Second, StatusStopped)

	statuses := make([]SessionStatus, 0, 2)
	for len(statuses) < 2 {
		_, payload, err := connection.Read(readContext)
		if err != nil {
			t.Fatalf("read session status events: %v", err)
		}
		var event struct {
			Type      string          `json:"type"`
			SessionID string          `json:"session_id"`
			Data      json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(payload, &event); err != nil || event.Type != "session.updated" || event.SessionID != session.ID {
			continue
		}
		var updated Session
		if err := json.Unmarshal(event.Data, &updated); err != nil {
			t.Fatal(err)
		}
		if updated.Status == StatusStopping || updated.Status == StatusStopped {
			statuses = append(statuses, updated.Status)
		}
	}
	if statuses[0] != StatusStopping || statuses[1] != StatusStopped {
		t.Fatalf("WebSocket stop status order = %v, want [stopping stopped]", statuses)
	}
}

func TestOptionsDescribeEveryFlagAndDisableProcessActions(t *testing.T) {
	server := testServer(t)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authenticatedRequest(http.MethodGet, "/api/v1/options", nil))
	if recorder.Code != http.StatusOK {
		t.Fatal(recorder.Body.String())
	}
	var response struct {
		Defaults ffuf.ConfigOptions  `json:"defaults"`
		Flags    []ffuf.FlagMetadata `json:"flags"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]ffuf.FlagMetadata)
	for _, flag := range response.Flags {
		byName[flag.Name] = flag
	}
	for _, name := range []string{"preflight", "preflight-var", "postflight", "postflight-var", "web", "web-listen", "web-cert", "web-key", "web-plain-http", "http3-fingerprint", "input-cmd", "b"} {
		if _, ok := byName[name]; !ok {
			t.Errorf("metadata omitted -%s", name)
		}
	}
	for _, name := range []string{"web", "web-listen", "web-cert", "web-key", "web-plain-http", "http3-fingerprint", "input-cmd"} {
		if byName[name].Available || byName[name].UnavailableReason == "" {
			t.Errorf("process/policy flag -%s was not explained as unavailable: %#v", name, byName[name])
		}
	}
	if byName["b"].JSONPath != "http.cookies" {
		t.Fatalf("cookie metadata path = %q", byName["b"].JSONPath)
	}
	if got := strings.Join(byName["preflight-mode"].Choices, ","); got != "per-request,per-thread" {
		t.Fatalf("preflight mode choices = %q", got)
	}
}

func TestSettingsSanitizeProcessOwnedPaths(t *testing.T) {
	server := testServer(t)
	settings, err := server.store.GetSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	settings.Defaults.Options.Output.AuditLog = "/tmp/should-not-be-created"
	settings.Defaults.Options.Output.OutputDirectory = "/tmp/responses"
	payload, _ := json.Marshal(settings)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authenticatedRequest(http.MethodPut, "/api/v1/settings", bytes.NewReader(payload)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("settings update = %d %s", recorder.Code, recorder.Body.String())
	}
	var saved Settings
	if err := json.Unmarshal(recorder.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Defaults.Options.Output.AuditLog != "" || saved.Defaults.Options.Output.OutputDirectory != "" {
		t.Fatalf("process-owned paths survived sanitation: %#v", saved.Defaults.Options.Output)
	}
}

func TestExportPreviewIsBoundedAndReportsTruncation(t *testing.T) {
	server := testServer(t)
	session := testSession("preview-session")
	if err := server.store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	results := make([]StoredResult, 3)
	logs := make([]TerminalEntry, 3)
	for index := range results {
		results[index] = StoredResult{URL: fmt.Sprintf("https://example.test/path-%d", index+1),
			StatusCode: 200, CreatedAt: time.Now().UTC()}
		logs[index] = TerminalEntry{At: time.Now().UTC(), Level: "info", Message: fmt.Sprintf("line-%d", index+1)}
	}
	if _, _, err := server.store.InsertResultBatch(context.Background(), session.ID, results, logs); err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"json", "text", "terminal"} {
		t.Run(format, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			path := "/api/v1/sessions/" + session.ID + "/export?preview=true&limit=2&format=" + format
			server.Handler().ServeHTTP(recorder, authenticatedRequest(http.MethodGet, path, nil))
			if recorder.Code != http.StatusOK {
				t.Fatalf("preview status = %d: %s", recorder.Code, recorder.Body.String())
			}
			if recorder.Header().Get("X-H3fuzz-Preview-Truncated") != "true" ||
				recorder.Header().Get("X-H3fuzz-Preview-Limit") != "2" {
				t.Fatalf("preview headers = %v", recorder.Header())
			}
			if disposition := recorder.Header().Get("Content-Disposition"); disposition != "" {
				t.Fatalf("preview unexpectedly forced a download: %q", disposition)
			}
			body := recorder.Body.String()
			switch format {
			case "json":
				var preview struct {
					Results   []StoredResult `json:"results"`
					Truncated bool           `json:"truncated"`
				}
				if err := json.Unmarshal(recorder.Body.Bytes(), &preview); err != nil || len(preview.Results) != 2 || !preview.Truncated {
					t.Fatalf("JSON preview = %s, err=%v", body, err)
				}
			case "text":
				if !strings.Contains(body, "/path-1") || !strings.Contains(body, "/path-2") ||
					strings.Contains(body, "/path-3") || !strings.Contains(body, "preview truncated") {
					t.Fatalf("text preview was not bounded: %s", body)
				}
			case "terminal":
				if !strings.Contains(body, "line-1") || !strings.Contains(body, "line-2") ||
					strings.Contains(body, "line-3") || !strings.Contains(body, "preview truncated") {
					t.Fatalf("terminal preview was not bounded: %s", body)
				}
			}
		})
	}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authenticatedRequest(http.MethodGet,
		"/api/v1/sessions/"+session.ID+"/export?preview=true&limit=1001", nil))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("oversized preview limit status = %d", recorder.Code)
	}
}

func TestTerminalExportDoesNotDuplicateResultRenderLogs(t *testing.T) {
	server := testServer(t)
	session := testSession("terminal-export-dedup")
	if err := server.store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	resultURL := "https://example.test/only-once"
	results := []StoredResult{{URL: resultURL, StatusCode: 200, CreatedAt: time.Now().UTC()}}
	logs := []TerminalEntry{
		{At: time.Now().UTC(), Level: "info", Message: "scan started"},
		{At: time.Now().UTC(), Level: "result", Message: resultURL + " [Status: 200]"},
		{At: time.Now().UTC(), Level: "info", Message: "scan completed: 1 requests"},
	}
	if _, _, err := server.store.InsertResultBatch(context.Background(), session.ID, results, logs); err != nil {
		t.Fatal(err)
	}

	for _, suffix := range []string{"?format=terminal", "?format=terminal&preview=true&limit=3"} {
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, authenticatedRequest(http.MethodGet,
			"/api/v1/sessions/"+session.ID+"/export"+suffix, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("terminal export %q status = %d: %s", suffix, recorder.Code, recorder.Body.String())
		}
		body := recorder.Body.String()
		if count := strings.Count(body, resultURL); count != 1 {
			t.Fatalf("terminal export %q rendered result URL %d times:\n%s", suffix, count, body)
		}
		if strings.Contains(body, "[RESULT]") {
			t.Fatalf("terminal export %q included rendering-only result log:\n%s", suffix, body)
		}
	}
}

func TestAddSessionFiltersPersistsAndRetroactivelyFiltersResults(t *testing.T) {
	server := testServer(t)
	session := testSession("live-filter-retroactive")
	session.Status = StatusCompleted
	if err := server.store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	results := []StoredResult{
		{URL: "https://example.test/forbidden", StatusCode: 403, ContentWords: 10, CreatedAt: time.Now().UTC()},
		{URL: "https://example.test/common", StatusCode: 200, ContentWords: 20, CreatedAt: time.Now().UTC()},
		{URL: "https://example.test/visible", StatusCode: 200, ContentWords: 30, CreatedAt: time.Now().UTC()},
	}
	if _, _, err := server.store.InsertResultBatch(context.Background(), session.ID, results, nil); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authenticatedRequest(http.MethodPatch,
		"/api/v1/sessions/"+session.ID+"/filters", strings.NewReader(`{"status":"403","words":"20"}`)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("add filters = %d: %s", recorder.Code, recorder.Body.String())
	}
	var updated Session
	if err := json.Unmarshal(recorder.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Options.Filter.Status != "403" || updated.Options.Filter.Words != "20" {
		t.Fatalf("saved filters = %#v", updated.Options.Filter)
	}
	if updated.ResultCount != 1 {
		t.Fatalf("filtered result count = %d, want 1", updated.ResultCount)
	}

	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authenticatedRequest(http.MethodGet,
		"/api/v1/sessions/"+session.ID+"/results?limit=10", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("results = %d: %s", recorder.Code, recorder.Body.String())
	}
	var page ResultsPage
	if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Results) != 1 || page.Results[0].URL != "https://example.test/visible" {
		t.Fatalf("filtered results = %#v", page.Results)
	}

	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authenticatedRequest(http.MethodGet,
		"/api/v1/sessions/"+session.ID+"/export?format=text", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("text export = %d: %s", recorder.Code, recorder.Body.String())
	}
	if body := recorder.Body.String(); !strings.Contains(body, "/visible") ||
		strings.Contains(body, "/forbidden") || strings.Contains(body, "/common") {
		t.Fatalf("filtered export = %q", body)
	}

	reloaded, err := server.store.GetSession(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Options.Filter.Status != "403" || reloaded.Options.Filter.Words != "20" || reloaded.ResultCount != 1 {
		t.Fatalf("persisted filtered session = %#v", reloaded)
	}
}

func TestWebSocketRequiresBasicAuthAndSameOrigin(t *testing.T) {
	server := testServer(t)
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/api/v1/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, response, err := websocket.Dial(ctx, wsURL, nil)
	if err == nil || response == nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated websocket err=%v response=%v", err, response)
	}

	headers := http.Header{}
	headers.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("h3fuzz:secret")))
	headers.Set("Origin", "https://attacker.invalid")
	_, response, err = websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: headers})
	if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin websocket err=%v response=%v", err, response)
	}

	headers.Set("Origin", httpServer.URL)
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = conn.CloseNow()
	}()
	_, data, err := conn.Read(ctx)
	if err != nil || !strings.Contains(string(data), `"type":"snapshot"`) {
		t.Fatalf("snapshot = %q, err=%v", data, err)
	}
}
