package webserver

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "WebSocket upgrade requires GET")
		return
	}
	// The library's default origin policy accepts same-host origins and rejects
	// cross-origin browser upgrades. Basic Auth has already run in middleware.
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	defer func() {
		_ = conn.CloseNow()
	}()
	conn.SetReadLimit(64 << 10)

	client := s.hub.add()
	defer s.hub.remove(client)
	sessions, err := s.store.ListSessions(r.Context())
	if err != nil {
		_ = conn.Close(websocket.StatusInternalError, "could not load session snapshot")
		return
	}
	if err := writeWSEvent(conn, Event{Type: "snapshot", Sequence: 0,
		Data: map[string]interface{}{"sessions": sessions}, At: time.Now().UTC()}); err != nil {
		return
	}

	readContext, cancelRead := context.WithCancel(context.Background())
	defer cancelRead()
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		for {
			if _, _, err := conn.Read(readContext); err != nil {
				return
			}
		}
	}()

	ping := time.NewTicker(20 * time.Second)
	resync := time.NewTicker(time.Second)
	defer ping.Stop()
	defer resync.Stop()
	for {
		select {
		case <-client.done:
			return
		case <-closed:
			return
		case message, ok := <-client.send:
			if !ok {
				return
			}
			payload, encodeErr := message.encode()
			if encodeErr != nil {
				continue
			}
			if writeWSPayload(conn, payload) != nil {
				return
			}
		case <-resync.C:
			if client.needsResync.Swap(false) {
				event := Event{Type: "resync.required", Sequence: 0, Data: map[string]string{
					"reason": "client fell behind; refresh sessions and results with REST"}, At: time.Now().UTC()}
				if writeWSEvent(conn, event) != nil {
					return
				}
			}
		case <-ping.C:
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			err := conn.Ping(ctx)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

func writeWSEvent(conn *websocket.Conn, event Event) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return writeWSPayload(conn, payload)
}

func writeWSPayload(conn *websocket.Conn, payload []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return conn.Write(ctx, websocket.MessageText, payload)
}
