package webserver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

func waitForSessionStatus(t *testing.T, store *Store, id string, timeout time.Duration, wanted ...SessionStatus) Session {
	t.Helper()
	allowed := make(map[SessionStatus]bool, len(wanted))
	for _, status := range wanted {
		allowed[status] = true
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		session, err := store.GetSession(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if allowed[session.Status] {
			return session
		}
		if session.Status == StatusFailed {
			t.Fatalf("session %s failed: %s", id, session.Error)
		}
		time.Sleep(5 * time.Millisecond)
	}
	session, _ := store.GetSession(context.Background(), id)
	t.Fatalf("session %s status = %s, want one of %v", id, session.Status, wanted)
	return Session{}
}

func TestConcurrentSessionsPauseIndependentlyAndPersistIsolatedResults(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Millisecond)
		if strings.HasSuffix(r.URL.Path, "/hit") {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("found"))
			return
		}
		http.NotFound(w, r)
	}))
	defer target.Close()

	wordlist := filepath.Join(t.TempDir(), "concurrent.txt")
	var words strings.Builder
	words.WriteString("hit\n")
	for index := 0; index < 300; index++ {
		fmt.Fprintf(&words, "missing-%03d\n", index)
	}
	if err := os.WriteFile(wordlist, []byte(words.String()), 0o600); err != nil {
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

	newRequest := func(prefix string) CreateSessionRequest {
		opts := ffuf.NewConfigOptions()
		opts.General.Threads = 2
		opts.HTTP.URL = target.URL + "/" + prefix + "/FUZZ"
		opts.Input.Wordlists = []string{wordlist}
		opts.Matcher.Status = "all"
		opts.Filter.Status = "404"
		return CreateSessionRequest{
			Name: prefix, Target: opts.HTTP.URL, Wordlist: wordlist, Profile: "dir", Options: opts,
			ExplicitFlags: []string{"t", "mc", "fc"}, AutoStart: true,
		}
	}
	first, err := manager.Create(context.Background(), newRequest("first"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Create(context.Background(), newRequest("second"))
	if err != nil {
		t.Fatal(err)
	}
	waitForSessionStatus(t, store, first.ID, 3*time.Second, StatusRunning)
	waitForSessionStatus(t, store, second.ID, 3*time.Second, StatusRunning)
	if err := manager.Pause(context.Background(), first.ID); err != nil {
		t.Fatal(err)
	}
	waitForSessionStatus(t, store, first.ID, time.Second, StatusPaused)
	waitForSessionStatus(t, store, second.ID, 5*time.Second, StatusCompleted)
	if paused, err := store.GetSession(context.Background(), first.ID); err != nil || paused.Status != StatusPaused {
		t.Fatalf("first session did not remain paused while second completed: status=%s err=%v", paused.Status, err)
	}
	if err := manager.Resume(context.Background(), first.ID); err != nil {
		t.Fatal(err)
	}
	waitForSessionStatus(t, store, first.ID, 5*time.Second, StatusCompleted)

	for _, session := range []Session{first, second} {
		results, err := store.AllResults(context.Background(), session.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(results) != 1 {
			t.Fatalf("session %s persisted %d results, want 1", session.Name, len(results))
		}
		wantPath := "/" + session.Name + "/hit"
		if !strings.Contains(results[0].URL, wantPath) {
			t.Fatalf("session %s result URL = %q, want path %q", session.Name, results[0].URL, wantPath)
		}
	}
	assertDeferredResultIndexes(t, store, true)
}

// TestDockerHarnessConcurrentHTTP3Sessions is an opt-in integration test for
// the Docker harness's UDP-only listener. Success on that listener proves the
// managed jobs used HTTP/3 rather than silently falling back to TCP. Keep it
// opt-in so the ordinary unit suite does not require Docker.
func TestDockerHarnessConcurrentHTTP3Sessions(t *testing.T) {
	if os.Getenv("H3FUZZ_DOCKER_HARNESS") == "" {
		t.Skip("set H3FUZZ_DOCKER_HARNESS=1 with harness/compose.yaml running")
	}
	targetBase := os.Getenv("H3FUZZ_HARNESS_URL")
	if targetBase == "" {
		targetBase = "https://localhost:8446"
	}
	words := []string{
		"admin", "admin.php", "api", "cgi-bin", "dashboard", "empty.txt", "health", "images",
		"known-large.bin", "login", "one-byte.txt", "runtime-fallback.txt",
		"definitely-missing-a", "definitely-missing-b",
	}
	wordlist := filepath.Join(t.TempDir(), "docker-h3-words.txt")
	if err := os.WriteFile(wordlist, []byte(strings.Join(words, "\n")+"\n"), 0o600); err != nil {
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
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = manager.Shutdown(ctx)
	}()

	create := func(name string) Session {
		opts := ffuf.NewConfigOptions()
		opts.General.Threads = 32
		opts.HTTP.URL = strings.TrimRight(targetBase, "/") + "/files/FUZZ"
		opts.Input.Wordlists = []string{wordlist}
		opts.Input.DirSearchCompat = false
		opts.Input.Extensions = ""
		opts.Matcher.Status = "all"
		opts.Filter.Status = "404"
		session, createErr := manager.Create(context.Background(), CreateSessionRequest{
			Name: name, Target: opts.HTTP.URL, Wordlist: wordlist, Profile: "dir", Options: opts,
			ExplicitFlags: []string{"w", "t", "mc", "fc"},
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		return session
	}
	first, second := create("docker-h3-first"), create("docker-h3-second")
	start := make(chan struct{})
	startErrors := make(chan error, 2)
	for _, id := range []string{first.ID, second.ID} {
		go func(sessionID string) {
			<-start
			startErrors <- manager.Start(context.Background(), sessionID)
		}(id)
	}
	close(start)
	for range 2 {
		if err := <-startErrors; err != nil {
			t.Fatal(err)
		}
	}
	for _, session := range []Session{first, second} {
		waitForSessionStatus(t, store, session.ID, 15*time.Second, StatusCompleted)
		results, err := store.AllResults(context.Background(), session.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(results) != 12 {
			t.Fatalf("session %s found %d paths over the UDP-only H3 listener, want 12", session.Name, len(results))
		}
	}
}
