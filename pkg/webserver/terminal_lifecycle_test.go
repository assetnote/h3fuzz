package webserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
	"github.com/coder/websocket"
)

func TestRealSessionPersistsTerminalLifecycleAndEmitsCursorShape(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("found"))
	}))
	defer target.Close()
	wordlist := filepath.Join(t.TempDir(), "terminal-words.txt")
	if err := os.WriteFile(wordlist, []byte("admin\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	application, server := newSecurityTestServer(t, false)
	wsURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	wsURL.Scheme = "ws"
	wsURL.Path = "/api/v1/ws"
	headers := http.Header{}
	headers.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("operator:correct-horse-battery-staple")))
	headers.Set("Origin", server.URL)
	connection, _, err := websocket.Dial(context.Background(), wsURL.String(), &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = connection.CloseNow()
	}()
	readContext, cancelRead := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelRead()
	if _, _, err := connection.Read(readContext); err != nil { // initial snapshot
		t.Fatal(err)
	}

	options := ffuf.NewConfigOptions()
	options.HTTP.URL = target.URL + "/FUZZ"
	options.Input.Wordlists = []string{wordlist}
	options.Input.DirSearchCompat = false
	options.Input.Extensions = ""
	options.Matcher.Status = "all"
	session, err := application.manager.Create(context.Background(), CreateSessionRequest{
		Name: "terminal-contract", Target: options.HTTP.URL, Wordlist: wordlist, Profile: "dir",
		Options: options, ExplicitFlags: []string{"w", "mc"}, AutoStart: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForSessionStatus(t, application.store, session.ID, 5*time.Second, StatusCompleted)

	type wireEvent struct {
		Type      string          `json:"type"`
		SessionID string          `json:"session_id"`
		Data      json.RawMessage `json:"data"`
	}
	type terminalPayload struct {
		Entries    []TerminalEntry `json:"entries"`
		NextCursor int64           `json:"next_cursor"`
	}
	var wsPayload terminalPayload
	var lifecycleEntry TerminalEntry
	for {
		_, payload, err := connection.Read(readContext)
		if err != nil {
			t.Fatalf("read terminal WebSocket event: %v", err)
		}
		var event wireEvent
		if err := json.Unmarshal(payload, &event); err != nil {
			t.Fatal(err)
		}
		if event.Type != "terminal" || event.SessionID != session.ID {
			continue
		}
		if err := json.Unmarshal(event.Data, &wsPayload); err != nil {
			t.Fatalf("decode terminal event payload: %v", err)
		}
		if len(wsPayload.Entries) == 0 || wsPayload.Entries[0].ID <= 0 {
			t.Fatalf("terminal WebSocket event has no persisted numeric entry ID: %#v", wsPayload)
		}
		if wsPayload.NextCursor != wsPayload.Entries[len(wsPayload.Entries)-1].ID {
			t.Fatalf("terminal WebSocket cursor = %d, last ID = %d", wsPayload.NextCursor,
				wsPayload.Entries[len(wsPayload.Entries)-1].ID)
		}
		for _, entry := range wsPayload.Entries {
			if entry.SessionID == session.ID && strings.Contains(entry.Message, "scan started") {
				lifecycleEntry = entry
				break
			}
		}
		if lifecycleEntry.ID > 0 {
			break
		}
	}

	request := securityAuthenticatedRequest(t, http.MethodGet,
		server.URL+"/api/v1/sessions/"+session.ID+"/terminal?after_id=0&limit=10", nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("terminal REST status = %d", response.StatusCode)
	}
	var restPayload TerminalPage
	if err := json.NewDecoder(response.Body).Decode(&restPayload); err != nil {
		t.Fatal(err)
	}
	foundLifecycle, foundResult, foundCompletion := false, false, false
	for _, entry := range restPayload.Entries {
		if entry.ID == lifecycleEntry.ID && entry.Message == lifecycleEntry.Message {
			foundLifecycle = true
		}
		if entry.Level == "result" && strings.Contains(entry.Message, "[Status: 200") {
			foundResult = true
		}
		if strings.Contains(entry.Message, "scan completed:") {
			foundCompletion = true
		}
	}
	if !foundLifecycle || !foundResult || !foundCompletion || restPayload.NextCursor < wsPayload.NextCursor {
		t.Fatalf("REST terminal page %#v does not match WS payload %#v", restPayload, wsPayload)
	}
}
