package webserver

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/assetnote/h3fuzz/pkg/engine"
	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

func newStartRaceManager(t *testing.T) (*Manager, *Store, Session) {
	t.Helper()
	store, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager, err := NewManager(store, NewHub(), false)
	if err != nil {
		t.Fatal(err)
	}
	wordlist := filepath.Join(t.TempDir(), "words.txt")
	if err := os.WriteFile(wordlist, []byte("hit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	options := ffuf.NewConfigOptions()
	options.HTTP.URL = "https://example.test/FUZZ"
	options.Input.Wordlists = []string{wordlist}
	session, err := manager.Create(context.Background(), CreateSessionRequest{
		Target: options.HTTP.URL, Wordlist: wordlist, Profile: "dir", Options: options,
	})
	if err != nil {
		t.Fatal(err)
	}
	return manager, store, session
}

func TestStartingSessionCannotBeDeletedDuringJobBuild(t *testing.T) {
	manager, store, session := newStartRaceManager(t)
	buildEntered := make(chan struct{})
	releaseBuild := make(chan struct{})
	manager.buildJob = func(*ffuf.Config) (*engine.Job, error) {
		close(buildEntered)
		<-releaseBuild
		return nil, errors.New("deliberate build failure")
	}
	startDone := make(chan error, 1)
	go func() { startDone <- manager.Start(context.Background(), session.ID) }()

	select {
	case <-buildEntered:
	case <-time.After(time.Second):
		t.Fatal("Start did not enter the job builder")
	}
	if err := manager.Delete(context.Background(), session.ID); err == nil {
		t.Fatal("Delete removed a session while its job was being built")
	}
	if _, err := store.GetSession(context.Background(), session.ID); err != nil {
		t.Fatalf("starting session disappeared from persistence: %v", err)
	}

	close(releaseBuild)
	if err := <-startDone; err == nil || !strings.Contains(err.Error(), "deliberate build failure") {
		t.Fatalf("Start error = %v, want deliberate build failure", err)
	}
	if err := manager.Delete(context.Background(), session.ID); err != nil {
		t.Fatalf("Delete after failed build = %v", err)
	}
}

func TestShutdownWaitsForStartingBuildAndPreventsActivation(t *testing.T) {
	manager, store, session := newStartRaceManager(t)
	buildEntered := make(chan struct{})
	releaseBuild := make(chan struct{})
	manager.buildJob = func(config *ffuf.Config) (*engine.Job, error) {
		close(buildEntered)
		<-releaseBuild
		return engine.NewJob(config), nil
	}
	startDone := make(chan error, 1)
	go func() { startDone <- manager.Start(context.Background(), session.ID) }()
	select {
	case <-buildEntered:
	case <-time.After(time.Second):
		t.Fatal("Start did not enter the job builder")
	}

	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- manager.Shutdown(context.Background()) }()
	select {
	case err := <-shutdownDone:
		t.Fatalf("Shutdown returned before starting build resolved: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(releaseBuild)
	if err := <-startDone; err == nil || !strings.Contains(err.Error(), "shutting down") {
		t.Fatalf("Start error = %v, want shutdown rejection", err)
	}
	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("Shutdown = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Shutdown did not finish after starting build resolved")
	}
	manager.mu.Lock()
	active, starting := len(manager.active), len(manager.starting)
	manager.mu.Unlock()
	if active != 0 || starting != 0 {
		t.Fatalf("manager retained active=%d starting=%d runs after shutdown", active, starting)
	}
	assertDeferredResultIndexes(t, store, true)
}
