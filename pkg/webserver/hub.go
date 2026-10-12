package webserver

import (
	"encoding/json"
	"sync"
	"sync/atomic"
)

// Hub never waits for a browser. A slow client is marked stale and receives a
// resync.required marker after it drains enough data; REST remains authoritative.
type Hub struct {
	mu      sync.RWMutex
	clients map[*wsClient]struct{}
	closed  bool
}

type wsClient struct {
	send        chan *wsMessage
	done        chan struct{}
	needsResync atomic.Bool
	closeOnce   sync.Once
}

// wsMessage defers JSON work to the WebSocket writer. Multiple clients share
// the encoded bytes, while result persistence only performs a bounded channel
// send and can immediately continue draining its SQLite queue.
type wsMessage struct {
	event   Event
	once    sync.Once
	payload []byte
	err     error
}

func (message *wsMessage) encode() ([]byte, error) {
	message.once.Do(func() {
		message.payload, message.err = json.Marshal(message.event)
	})
	return message.payload, message.err
}

func NewHub() *Hub { return &Hub{clients: make(map[*wsClient]struct{})} }

func (h *Hub) add() *wsClient {
	client := &wsClient{send: make(chan *wsMessage, 64), done: make(chan struct{})}
	h.mu.Lock()
	if h.closed {
		client.closeOnce.Do(func() { close(client.done) })
	} else {
		h.clients[client] = struct{}{}
	}
	h.mu.Unlock()
	return client
}

func (h *Hub) remove(client *wsClient) {
	h.mu.Lock()
	if _, ok := h.clients[client]; ok {
		delete(h.clients, client)
		client.closeOnce.Do(func() { close(client.done) })
	}
	h.mu.Unlock()
}

func (h *Hub) Close() {
	h.mu.Lock()
	h.closed = true
	for client := range h.clients {
		client.closeOnce.Do(func() { close(client.done) })
	}
	h.mu.Unlock()
}

func (h *Hub) Broadcast(event Event) {
	h.mu.RLock()
	hasClients := !h.closed && len(h.clients) > 0
	h.mu.RUnlock()
	if !hasClients {
		return
	}
	message := &wsMessage{event: event}
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.closed {
		return
	}
	for client := range h.clients {
		select {
		case client.send <- message:
		default:
			client.needsResync.Store(true)
		}
	}
}

func (h *Hub) clientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}
