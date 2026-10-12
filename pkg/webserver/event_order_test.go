package webserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
	"github.com/coder/websocket"
)

type observedSessionEvent struct {
	Type     string
	Sequence int64
	Session  Session
}

func TestCreateAndStartResponsesDoNotPrecedeRunningWebSocketState(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer target.Close()
	wordlist := filepath.Join(t.TempDir(), "event-order-words.txt")
	if err := os.WriteFile(wordlist, []byte("admin\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	application, server := newSecurityTestServer(t, false)
	connection, _, err := websocket.Dial(context.Background(),
		"ws"+strings.TrimPrefix(server.URL, "http")+"/api/v1/ws",
		&websocket.DialOptions{HTTPHeader: websocketHeaders(server.URL)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = connection.CloseNow()
	}()
	readContext, cancelRead := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelRead()
	if _, _, err := connection.Read(readContext); err != nil { // initial snapshot
		t.Fatal(err)
	}

	requestFor := func(name string, autoStart bool) []byte {
		opts := ffuf.NewConfigOptions()
		opts.HTTP.URL = target.URL + "/FUZZ"
		opts.HTTP.NoHttp3 = true
		opts.HTTP.NoResponseOptimization = true
		opts.HTTP.Timeout = 30
		opts.Input.InputMode = "pitchfork"
		opts.Input.Wordlists = []string{wordlist}
		opts.Input.DirSearchCompat = false
		opts.General.Threads = 1
		body, marshalErr := json.Marshal(CreateSessionRequest{
			Name: name, Target: opts.HTTP.URL, Wordlist: wordlist, Profile: "dir", Options: opts,
			ExplicitFlags: []string{"w", "t", "no-http3", "no-response-optimization", "timeout"},
			AutoStart:     autoStart,
		})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		return body
	}
	post := func(path string, body []byte, wantStatus int) Session {
		request := securityAuthenticatedRequest(t, http.MethodPost, server.URL+path, body)
		request.Header.Set("Content-Type", "application/json")
		response, requestErr := http.DefaultClient.Do(request)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		defer response.Body.Close()
		if response.StatusCode != wantStatus {
			t.Fatalf("POST %s status = %d, want %d", path, response.StatusCode, wantStatus)
		}
		var session Session
		if err := json.NewDecoder(response.Body).Decode(&session); err != nil {
			t.Fatal(err)
		}
		return session
	}
	readThroughRunning := func(id string) []observedSessionEvent {
		var observed []observedSessionEvent
		for {
			_, payload, readErr := connection.Read(readContext)
			if readErr != nil {
				t.Fatalf("read WebSocket events for %s: %v", id, readErr)
			}
			var event Event
			if err := json.Unmarshal(payload, &event); err != nil {
				t.Fatal(err)
			}
			if event.SessionID != id || (event.Type != "session.created" && event.Type != "session.updated") {
				continue
			}
			encoded, err := json.Marshal(event.Data)
			if err != nil {
				t.Fatal(err)
			}
			var session Session
			if err := json.Unmarshal(encoded, &session); err != nil {
				t.Fatal(err)
			}
			observed = append(observed, observedSessionEvent{Type: event.Type, Sequence: event.Sequence, Session: session})
			if session.Status == StatusRunning {
				return observed
			}
		}
	}
	assertOrdering := func(response Session, events []observedSessionEvent, wantCreated bool) {
		t.Helper()
		if len(events) < 3 {
			t.Fatalf("session %s emitted only %#v", response.ID, events)
		}
		var created, initialStarting, runtime, running *observedSessionEvent
		for index := range events {
			event := &events[index]
			if index > 0 && event.Sequence <= events[index-1].Sequence {
				t.Fatalf("session %s event sequences are not increasing: %#v", response.ID, events)
			}
			switch {
			case event.Type == "session.created":
				created = event
			case event.Session.Status == StatusStarting && event.Session.RuntimeTransport == "":
				initialStarting = event
			case event.Session.Status == StatusStarting && event.Session.RuntimeTransport != "":
				runtime = event
			case event.Session.Status == StatusRunning:
				running = event
			}
		}
		if wantCreated && created == nil {
			t.Fatalf("session %s did not emit session.created: %#v", response.ID, events)
		}
		if initialStarting == nil || runtime == nil || running == nil {
			t.Fatalf("session %s lifecycle events incomplete: %#v", response.ID, events)
		}
		if !(initialStarting.Sequence < runtime.Sequence && runtime.Sequence < running.Sequence) {
			t.Fatalf("session %s lifecycle order is invalid: %#v", response.ID, events)
		}
		if response.Sequence < running.Sequence || response.Status == StatusStarting || response.Status == StatusIdle {
			t.Fatalf("POST response regressed session %s behind running event: response status=%s seq=%d, running seq=%d",
				response.ID, response.Status, response.Sequence, running.Sequence)
		}
		t.Logf("session %s response=%s#%d events=%v", response.Name, response.Status,
			response.Sequence, summarizeSessionEvents(events))
	}

	manualCreated := post("/api/v1/sessions", requestFor("manual", false), http.StatusCreated)
	manualCreateEvents := readSessionEvents(t, connection, readContext, manualCreated.ID, StatusIdle)
	if len(manualCreateEvents) != 1 || manualCreateEvents[0].Type != "session.created" ||
		manualCreated.Sequence < manualCreateEvents[0].Sequence {
		t.Fatalf("manual create response seq=%d did not include created event %#v", manualCreated.Sequence, manualCreateEvents)
	}
	t.Logf("session manual-create response=%s#%d events=%v", manualCreated.Status,
		manualCreated.Sequence, summarizeSessionEvents(manualCreateEvents))
	manualStarted := post("/api/v1/sessions/"+manualCreated.ID+"/start", nil, http.StatusAccepted)
	assertOrdering(manualStarted, readThroughRunning(manualCreated.ID), false)

	automatic := post("/api/v1/sessions", requestFor("automatic", true), http.StatusCreated)
	assertOrdering(automatic, readThroughRunning(automatic.ID), true)

	for _, id := range []string{manualCreated.ID, automatic.ID} {
		_, _ = application.manager.StopSnapshot(context.Background(), id)
	}
}

func summarizeSessionEvents(events []observedSessionEvent) []string {
	summary := make([]string, 0, len(events))
	for _, event := range events {
		transport := event.Session.RuntimeTransport
		if transport == "" {
			transport = "unselected"
		}
		summary = append(summary, fmt.Sprintf("%s:%s:%s#%d", event.Type, event.Session.Status, transport, event.Sequence))
	}
	return summary
}

func readSessionEvents(t *testing.T, connection *websocket.Conn, ctx context.Context, id string, through SessionStatus) []observedSessionEvent {
	t.Helper()
	var observed []observedSessionEvent
	for {
		_, payload, err := connection.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var event Event
		if err := json.Unmarshal(payload, &event); err != nil {
			t.Fatal(err)
		}
		if event.SessionID != id || (event.Type != "session.created" && event.Type != "session.updated") {
			continue
		}
		encoded, err := json.Marshal(event.Data)
		if err != nil {
			t.Fatal(err)
		}
		var session Session
		if err := json.Unmarshal(encoded, &session); err != nil {
			t.Fatal(err)
		}
		observed = append(observed, observedSessionEvent{Type: event.Type, Sequence: event.Sequence, Session: session})
		if session.Status == through {
			return observed
		}
	}
}
