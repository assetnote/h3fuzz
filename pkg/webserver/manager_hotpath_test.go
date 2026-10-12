package webserver

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

type marshalCounter struct {
	calls *atomic.Int64
}

func (value marshalCounter) MarshalJSON() ([]byte, error) {
	value.calls.Add(1)
	return []byte(`{"ok":true}`), nil
}

type blockingJSON struct {
	release <-chan struct{}
}

func (value blockingJSON) MarshalJSON() ([]byte, error) {
	<-value.release
	return []byte(`{"ok":true}`), nil
}

func TestHubBroadcastDoesNotEncodeWithoutClients(t *testing.T) {
	hub := NewHub()
	var calls atomic.Int64

	hub.Broadcast(Event{Type: "result", Data: marshalCounter{calls: &calls}})

	if got := calls.Load(); got != 0 {
		t.Fatalf("JSON encodes with no WebSocket clients = %d, want 0", got)
	}
}

func TestHubBroadcastDoesNotWaitForJSONEncoding(t *testing.T) {
	hub := NewHub()
	client := hub.add()
	defer hub.remove(client)
	release := make(chan struct{})
	returned := make(chan struct{})
	go func() {
		hub.Broadcast(Event{Type: "result", Data: blockingJSON{release: release}})
		close(returned)
	}()

	select {
	case <-returned:
		close(release)
	case <-time.After(100 * time.Millisecond):
		close(release)
		<-returned
		t.Fatal("Broadcast waited for JSON encoding on the persistence writer path")
	}
}

func TestResultEventFilteringDoesNotReadSQLitePerBatch(t *testing.T) {
	store, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	session := testSession("cached-result-filter")
	session.ResultFilters.Status = "403"
	if err := store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(store, NewHub(), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	state := manager.sequenceFor(session.ID)
	filtered, ok := manager.filterResultEvent(state.resultFilter, map[string]interface{}{
		"results": []StoredResult{
			{ID: 1, URL: "https://example.test/hidden", StatusCode: 403},
			{ID: 2, URL: "https://example.test/visible", StatusCode: 200},
		},
		"next_cursor": int64(2),
	})
	if !ok {
		t.Fatal("cached result filter suppressed a batch containing a visible result")
	}
	results := filtered.(map[string]interface{})["results"].([]StoredResult)
	if len(results) != 1 || results[0].ID != 2 {
		t.Fatalf("cached result filter returned %#v, want only result 2", results)
	}
}

func TestResultEventWithoutRetroactiveFiltersReusesPersistedBatch(t *testing.T) {
	results := []StoredResult{
		{ID: 1, URL: "https://example.test/one", StatusCode: 200},
		{ID: 2, URL: "https://example.test/two", StatusCode: 200},
	}
	payload := map[string]interface{}{"results": results, "next_cursor": int64(2)}
	resultFilter, err := newStoredResultFilter(testSession("unfiltered").ResultFilters)
	if err != nil {
		t.Fatal(err)
	}

	filtered, ok := (&Manager{}).filterResultEvent(resultFilter, payload)
	if !ok {
		t.Fatal("unfiltered batch was suppressed")
	}
	visible := filtered.(map[string]interface{})["results"].([]StoredResult)
	if &visible[0] != &results[0] {
		t.Fatal("unfiltered result batch was copied")
	}
}

func TestAddFiltersRefreshesCachedResultEventFilter(t *testing.T) {
	store, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	session := testSession("refresh-result-filter")
	session.Status = StatusCompleted
	if err := store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(store, NewHub(), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AddFilters(context.Background(), session.ID, AddSessionFiltersRequest{Status: "403"}); err != nil {
		t.Fatal(err)
	}

	state := manager.sequenceFor(session.ID)
	filtered, ok := manager.filterResultEvent(state.resultFilter, map[string]interface{}{
		"results": []StoredResult{
			{ID: 1, URL: "https://example.test/hidden", StatusCode: 403},
			{ID: 2, URL: "https://example.test/visible", StatusCode: 200},
		},
		"next_cursor": int64(2),
	})
	if !ok {
		t.Fatal("updated cached filter suppressed the complete result batch")
	}
	results := filtered.(map[string]interface{})["results"].([]StoredResult)
	if len(results) != 1 || results[0].ID != 2 {
		t.Fatalf("updated cached filter returned %#v, want only result 2", results)
	}
}

func BenchmarkManagerEmitResultBatch1000(b *testing.B) {
	for _, clients := range []int{0, 1} {
		b.Run(fmt.Sprintf("clients=%d", clients), func(b *testing.B) {
			store, err := OpenStore(":memory:")
			if err != nil {
				b.Fatal(err)
			}
			defer store.Close()
			session := testSession("emit-hot-path")
			if err := store.CreateSession(context.Background(), &session); err != nil {
				b.Fatal(err)
			}
			hub := NewHub()
			manager, err := NewManager(store, hub, false)
			if err != nil {
				b.Fatal(err)
			}
			var client *wsClient
			if clients > 0 {
				client = hub.add()
				defer hub.remove(client)
				go func() {
					for {
						select {
						case <-client.send:
						case <-client.done:
							return
						}
					}
				}()
			}
			results := make([]StoredResult, 1000)
			for index := range results {
				results[index] = StoredResult{
					ID: int64(index + 1), URL: fmt.Sprintf("https://example.test/path/%d", index),
					StatusCode: 200, ContentLength: 42, ContentWords: 7, ContentLines: 2,
					ContentType: "text/html", CreatedAt: time.Unix(1, int64(index)).UTC(),
				}
			}
			payload := map[string]interface{}{"results": results, "next_cursor": int64(len(results))}
			b.ReportAllocs()
			b.ResetTimer()
			for index := 0; index < b.N; index++ {
				if sequence := manager.emit(session.ID, "result", payload); sequence == 0 {
					b.Fatal("emit failed")
				}
			}
			b.StopTimer()
			if clients > 0 {
				// Ensure the benchmark's event payload is valid JSON independently
				// from the asynchronous reader above.
				if _, err := json.Marshal(payload); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
