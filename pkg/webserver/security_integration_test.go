package webserver

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
	"github.com/coder/websocket"
)

func securityTestSession(id string) Session {
	now := time.Now().UTC()
	return Session{
		ID:        id,
		Name:      id,
		Target:    "https://example.test/FUZZ",
		Wordlist:  "final.txt",
		Profile:   "dir",
		Status:    StatusIdle,
		Options:   *ffuf.NewConfigOptions(),
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func TestSecurityMemoryStoresAreIsolated(t *testing.T) {
	first, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	session := securityTestSession("only-in-first")
	if err := first.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	sessions, err := second.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Fatalf("second in-memory store saw %d sessions from first store", len(sessions))
	}
}

func TestSQLiteSafetyPragmasApplyToEveryPooledConnection(t *testing.T) {
	store, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	connections := make([]*sql.Conn, 0, 4)
	for i := 0; i < 4; i++ {
		conn, err := store.db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		connections = append(connections, conn)
	}
	defer func() {
		for _, conn := range connections {
			_ = conn.Close()
		}
	}()

	for index, conn := range connections {
		var foreignKeys, busyTimeout int
		if err := conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
			t.Fatalf("connection %d foreign_keys: %v", index, err)
		}
		if err := conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
			t.Fatalf("connection %d busy_timeout: %v", index, err)
		}
		if foreignKeys != 1 {
			t.Errorf("connection %d foreign_keys = %d, want 1", index, foreignKeys)
		}
		if busyTimeout < 5_000 {
			t.Errorf("connection %d busy_timeout = %d, want at least 5000", index, busyTimeout)
		}
	}
}

func TestPersistentStoreFilesArePrivate(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "console.db")
	store, err := OpenStore(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	session := securityTestSession("private-files")
	if err := store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}

	files, err := filepath.Glob(databasePath + "*")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("SQLite did not create a database file")
	}
	for _, name := range files {
		info, err := os.Stat(name)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().IsRegular() && info.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s permissions are %04o, want no group or other access", filepath.Base(name), info.Mode().Perm())
		}
	}
}

func TestListenAddressRequiresExplicitRemoteOptIn(t *testing.T) {
	for _, address := range []string{"127.0.0.1:8787", "localhost:8787", "[::1]:8787"} {
		if err := validateListenAddress(address, false); err != nil {
			t.Errorf("loopback address %q rejected: %v", address, err)
		}
	}
	for _, address := range []string{":8787", "0.0.0.0:8787", "[::]:8787", "192.0.2.10:8787"} {
		if err := validateListenAddress(address, false); err == nil {
			t.Errorf("non-loopback address %q accepted without opt-in", address)
		}
		if err := validateListenAddress(address, true); err != nil {
			t.Errorf("non-loopback address %q rejected with opt-in: %v", address, err)
		}
	}
	if err := validateListenAddress("127.0.0.1", true); err == nil {
		t.Fatal("address without a port was accepted")
	}
}

func newSecurityTestServer(t *testing.T, allowInputCmd bool) (*Server, *httptest.Server) {
	t.Helper()
	server, err := New(Config{
		ListenAddress: "127.0.0.1:0",
		DatabasePath:  ":memory:",
		Username:      "operator",
		Password:      "correct-horse-battery-staple",
		AllowInputCmd: allowInputCmd,
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(func() {
		httpServer.Close()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.manager.Shutdown(ctx)
		_ = server.store.Close()
	})
	return server, httpServer
}

func securityAuthenticatedRequest(t *testing.T, method, target string, body []byte) *http.Request {
	t.Helper()
	request, err := http.NewRequest(method, target, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.SetBasicAuth("operator", "correct-horse-battery-staple")
	return request
}

func TestHTTPBasicAuthenticationProtectsUIAndAPI(t *testing.T) {
	_, server := newSecurityTestServer(t, false)

	response, err := http.Get(server.URL + "/api/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", response.StatusCode)
	}
	if !strings.HasPrefix(response.Header.Get("WWW-Authenticate"), "Basic ") {
		t.Fatalf("missing Basic challenge: %q", response.Header.Get("WWW-Authenticate"))
	}
	if response.Header.Get("Content-Security-Policy") == "" || response.Header.Get("X-Frame-Options") != "DENY" {
		t.Fatal("security headers were not applied to authentication failure")
	}

	request := securityAuthenticatedRequest(t, http.MethodGet, server.URL+"/api/v1/health", nil)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("authenticated status = %d, want 200", response.StatusCode)
	}
	if response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("API Cache-Control = %q, want no-store", response.Header.Get("Cache-Control"))
	}
}

func TestCrossOriginStateChangesAreRejected(t *testing.T) {
	_, server := newSecurityTestServer(t, false)
	for name, mutate := range map[string]func(*http.Request){
		"foreign origin": func(request *http.Request) { request.Header.Set("Origin", "https://attacker.example") },
		"null origin":    func(request *http.Request) { request.Header.Set("Origin", "null") },
		"fetch metadata": func(request *http.Request) { request.Header.Set("Sec-Fetch-Site", "cross-site") },
	} {
		t.Run(name, func(t *testing.T) {
			request := securityAuthenticatedRequest(t, http.MethodPut, server.URL+"/api/v1/settings", []byte(`{}`))
			request.Header.Set("Content-Type", "application/json")
			mutate(request)
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", response.StatusCode)
			}
		})
	}

	request := securityAuthenticatedRequest(t, http.MethodPut, server.URL+"/api/v1/settings", []byte(`{}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", server.URL)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusForbidden {
		t.Fatal("same-origin state change was rejected")
	}
}

func websocketHeaders(origin string) http.Header {
	header := make(http.Header)
	header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("operator:correct-horse-battery-staple")))
	if origin != "" {
		header.Set("Origin", origin)
	}
	return header
}

func TestWebSocketHandshakeEnforcesAuthenticationAndOrigin(t *testing.T) {
	application, server := newSecurityTestServer(t, false)
	websocketURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/v1/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	connection, response, err := websocket.Dial(ctx, websocketURL, nil)
	if connection != nil {
		_ = connection.CloseNow()
	}
	if err == nil || response == nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated handshake: response=%v err=%v", response, err)
	}
	_ = response.Body.Close()

	connection, response, err = websocket.Dial(ctx, websocketURL, &websocket.DialOptions{
		HTTPHeader: websocketHeaders("https://attacker.example"),
	})
	if connection != nil {
		_ = connection.CloseNow()
	}
	if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin handshake: response=%v err=%v", response, err)
	}
	_ = response.Body.Close()

	connection, response, err = websocket.Dial(ctx, websocketURL, &websocket.DialOptions{
		HTTPHeader: websocketHeaders(server.URL),
	})
	if err != nil {
		if response != nil {
			_ = response.Body.Close()
		}
		t.Fatalf("same-origin handshake: %v", err)
	}
	_, payload, err := connection.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var event Event
	if err := json.Unmarshal(payload, &event); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if event.Type != "snapshot" {
		t.Fatalf("first event type = %q, want snapshot", event.Type)
	}
	if err := connection.Close(websocket.StatusNormalClosure, "done"); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(time.Second)
	for application.hub.clientCount() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := application.hub.clientCount(); got != 0 {
		t.Fatalf("WebSocket client leaked after close: %d clients", got)
	}
}

func TestMalformedWebSocketRequestIsRejected(t *testing.T) {
	_, server := newSecurityTestServer(t, false)
	request := securityAuthenticatedRequest(t, http.MethodGet, server.URL+"/api/v1/ws", nil)
	request.Header.Set("Origin", server.URL)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode < 400 || response.StatusCode >= 500 {
		t.Fatalf("ordinary GET status = %d, want a 4xx WebSocket handshake error", response.StatusCode)
	}
}

func TestWebSocketHubBoundsSlowClientsAndRequestsResync(t *testing.T) {
	hub := NewHub()
	client := hub.add()
	for sequence := int64(1); sequence <= 100; sequence++ {
		hub.Broadcast(Event{Type: "progress", SessionID: "slow", Sequence: sequence})
	}
	if got, want := len(client.send), cap(client.send); got != want {
		t.Fatalf("queued messages = %d, want bounded capacity %d", got, want)
	}
	if !client.needsResync.Load() {
		t.Fatal("slow WebSocket client was not marked for REST resynchronization")
	}
	hub.remove(client)
	if got := hub.clientCount(); got != 0 {
		t.Fatalf("removed client remains registered: %d", got)
	}
}

func TestConcurrentSessionEventsRemainMonotonicAndPersisted(t *testing.T) {
	store, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	session := securityTestSession("ordered-events")
	if err := store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	hub := NewHub()
	client := hub.add()
	defer hub.remove(client)
	manager, err := NewManager(store, hub, false)
	if err != nil {
		t.Fatal(err)
	}

	const eventCount = 50
	var wg sync.WaitGroup
	for index := 0; index < eventCount; index++ {
		wg.Add(1)
		go func(value int) {
			defer wg.Done()
			manager.emit(session.ID, "progress", map[string]int{"value": value})
		}(index)
	}
	wg.Wait()

	for want := int64(1); want <= eventCount; want++ {
		select {
		case message := <-client.send:
			payload, err := message.encode()
			if err != nil {
				t.Fatal(err)
			}
			var event Event
			if err := json.Unmarshal(payload, &event); err != nil {
				t.Fatal(err)
			}
			if event.Sequence != want {
				t.Fatalf("event sequence = %d, want %d", event.Sequence, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for event %d", want)
		}
	}
	persisted, err := store.GetSession(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Sequence != eventCount {
		t.Fatalf("persisted sequence = %d, want %d", persisted.Sequence, eventCount)
	}
}

func TestWebProgressHotPathIsAllocationFreeAndPublishesLatestOnly(t *testing.T) {
	store, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var calls atomic.Int64
	var lastCount atomic.Int64
	output := newWebOutput("progress", store, nil, func(progress ffuf.Progress) {
		lastCount.Store(int64(progress.ReqCount))
		calls.Add(1)
	})
	progress := ffuf.Progress{StartedAt: time.Now(), ReqTotal: 10_000}
	allocations := testing.AllocsPerRun(1_000, func() {
		output.Progress(progress)
	})
	if allocations != 0 {
		t.Fatalf("Progress allocated %.2f objects per update, want 0", allocations)
	}
	for count := 0; count < 10_000; count++ {
		progress.ReqCount = count
		output.Progress(progress)
	}
	if err := output.Finalize(); err != nil {
		t.Fatal(err)
	}
	if got := lastCount.Load(); got != 9_999 {
		t.Fatalf("published ReqCount = %d, want latest 9999", got)
	}
	if got := calls.Load(); got > 3 {
		t.Fatalf("10,000 progress updates caused %d publications, want coalesced output", got)
	}
}

func TestWebSessionSanitizesProcessOwnedOutputPaths(t *testing.T) {
	store, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	hub := NewHub()
	manager, err := NewManager(store, hub, false)
	if err != nil {
		t.Fatal(err)
	}
	opts := ffuf.NewConfigOptions()
	opts.Output.AuditLog = filepath.Join(t.TempDir(), "should-not-exist.audit")
	opts.Output.DebugLog = filepath.Join(t.TempDir(), "should-not-exist.debug")
	opts.Output.OutputDirectory = filepath.Join(t.TempDir(), "should-not-exist")
	opts.Output.OutputFile = filepath.Join(t.TempDir(), "should-not-exist.json")
	opts.Output.RunTelemetry = filepath.Join(t.TempDir(), "should-not-exist.telemetry")

	session, err := manager.Create(context.Background(), CreateSessionRequest{
		Target:  "https://example.test/FUZZ",
		Profile: "dir",
		Options: opts,
	})
	if err != nil {
		t.Fatal(err)
	}
	if session.Options.Output.AuditLog != "" || session.Options.Output.DebugLog != "" ||
		session.Options.Output.OutputDirectory != "" || session.Options.Output.OutputFile != "" ||
		session.Options.Output.RunTelemetry != "" {
		t.Fatalf("web-owned output paths survived normalization: %+v", session.Options.Output)
	}
}

func TestInputCommandIsRejectedByDefaultWithoutExecution(t *testing.T) {
	_, server := newSecurityTestServer(t, false)
	marker := filepath.Join(t.TempDir(), "command-ran")
	opts := ffuf.NewConfigOptions()
	opts.Input.Inputcommands = []string{"touch " + marker}
	opts.Input.InputNum = 1
	payload, err := json.Marshal(CreateSessionRequest{
		Target:    "https://example.test/FUZZ",
		Profile:   "dir",
		Options:   opts,
		AutoStart: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := securityAuthenticatedRequest(t, http.MethodPost, server.URL+"/api/v1/sessions", payload)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		t.Fatalf("input-cmd request unexpectedly succeeded with status %d", response.StatusCode)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("disabled input command affected filesystem: %v", err)
	}
}

func TestSettingsCannotPersistUnavailableSessionFlags(t *testing.T) {
	application, server := newSecurityTestServer(t, false)
	settings := DefaultSettings()
	settings.Defaults.Options.General.ConfigFile = filepath.Join(t.TempDir(), "process.ffufrc")
	settings.Defaults.Options.HTTP.Http3Fingerprint = true
	settings.Defaults.Options.Output.AuditLog = filepath.Join(t.TempDir(), "audit.log")
	settings.Defaults.Options.Output.OutputFile = filepath.Join(t.TempDir(), "results.json")
	payload, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	request := securityAuthenticatedRequest(t, http.MethodPut, server.URL+"/api/v1/settings", payload)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()

	persisted, err := application.store.GetSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Defaults.Options.General.ConfigFile != "" || persisted.Defaults.Options.HTTP.Http3Fingerprint ||
		persisted.Defaults.Options.Output.AuditLog != "" || persisted.Defaults.Options.Output.OutputFile != "" {
		t.Fatalf("unavailable flags were persisted in defaults: %+v", persisted.Defaults.Options)
	}
}

func TestOptionsEndpointMarksProcessOwnedFlagsUnavailable(t *testing.T) {
	_, server := newSecurityTestServer(t, false)
	request := securityAuthenticatedRequest(t, http.MethodGet, server.URL+"/api/v1/options", nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	var document struct {
		Flags []ffuf.FlagMetadata `json:"flags"`
	}
	if err := json.NewDecoder(response.Body).Decode(&document); err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]ffuf.FlagMetadata, len(document.Flags))
	for _, metadata := range document.Flags {
		byName[metadata.Name] = metadata
		if !metadata.Available && metadata.UnavailableReason == "" {
			t.Errorf("unavailable flag %q has no reason", metadata.Name)
		}
	}
	for _, name := range []string{
		"config", "http3-fingerprint", "input-cmd", "o", "od", "of", "or",
		"audit-log", "debug-log", "run-telemetry", "noninteractive", "c", "json", "s", "v",
	} {
		metadata, ok := byName[name]
		if !ok {
			t.Errorf("flag %q missing from metadata", name)
			continue
		}
		if metadata.Available {
			t.Errorf("process-owned flag %q is advertised as available", name)
		}
	}
}

func TestOversizedJSONBodyReturnsRequestEntityTooLarge(t *testing.T) {
	_, server := newSecurityTestServer(t, false)
	body := []byte(strings.Repeat(" ", maxRequestBody+1) + `{}`)
	request := securityAuthenticatedRequest(t, http.MethodPost, server.URL+"/api/v1/sessions", body)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", response.StatusCode)
	}
}

func TestPersistedResultsExportAsJSONPathsAndTerminalOutput(t *testing.T) {
	application, server := newSecurityTestServer(t, false)
	session := securityTestSession("export-session")
	if err := application.store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	_, _, err := application.store.InsertResultBatch(context.Background(), session.ID, []StoredResult{{
		SessionID: session.ID, URL: "https://example.test/admin?source=fuzz", StatusCode: 200,
		ContentLength: 12, ContentWords: 2, ContentLines: 1, CreatedAt: time.Now().UTC(),
	}}, []TerminalEntry{{At: time.Now().UTC(), Level: "info", Message: "scan started"}})
	if err != nil {
		t.Fatal(err)
	}

	for format, contains := range map[string][]string{
		"json":     {`"id":"export-session"`, `"url":"https://example.test/admin?source=fuzz"`},
		"text":     {"/admin?source=fuzz"},
		"terminal": {"scan started", "Status: 200", "https://example.test/admin?source=fuzz"},
	} {
		t.Run(format, func(t *testing.T) {
			request := securityAuthenticatedRequest(t, http.MethodGet,
				server.URL+"/api/v1/sessions/"+session.ID+"/export?format="+format, nil)
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", response.StatusCode)
			}
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			for _, fragment := range contains {
				if !bytes.Contains(body, []byte(fragment)) {
					t.Errorf("%s export does not contain %q: %s", format, fragment, body)
				}
			}
			if disposition := response.Header.Get("Content-Disposition"); !strings.Contains(disposition, "attachment;") {
				t.Errorf("Content-Disposition = %q", disposition)
			}
		})
	}
}
