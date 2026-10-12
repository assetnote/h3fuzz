package webserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

func TestSanitizeWebOptionsRejectsProfilingPaths(t *testing.T) {
	tests := []struct {
		name string
		set  func(*ffuf.ConfigOptions)
	}{
		{"cpu", func(opts *ffuf.ConfigOptions) { opts.Output.ProfileCPU = "cpu.pprof" }},
		{"heap", func(opts *ffuf.ConfigOptions) { opts.Output.ProfileHeap = "heap.pprof" }},
		{"mutex", func(opts *ffuf.ConfigOptions) { opts.Output.ProfileMutex = "mutex.pprof" }},
		{"block", func(opts *ffuf.ConfigOptions) { opts.Output.ProfileBlock = "block.pprof" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			opts := ffuf.NewConfigOptions()
			test.set(opts)
			err := sanitizeWebOptions(opts)
			if err == nil || !strings.Contains(err.Error(), "profiling") {
				t.Fatalf("sanitizeWebOptions error = %v, want profiling rejection", err)
			}
		})
	}
}

func TestNormalizeCreateRejectsProfilingExplicitFlags(t *testing.T) {
	for _, name := range []string{"profile-cpu", "profile-heap", "profile-mutex", "profile-block"} {
		t.Run(name, func(t *testing.T) {
			_, err := normalizeCreate(CreateSessionRequest{
				Target: "https://example.test/FUZZ", ExplicitFlags: []string{name},
			}, DefaultSettings())
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("normalizeCreate error = %v, want rejection naming -%s", err, name)
			}
		})
	}
}

func TestNormalizeCreateRejectsProfilingPathBeforeOptionClone(t *testing.T) {
	opts := ffuf.NewConfigOptions()
	opts.Output.ProfileHeap = "/tmp/web-heap.pprof"
	_, err := normalizeCreate(CreateSessionRequest{
		Target: "https://example.test/FUZZ", Options: opts,
	}, DefaultSettings())
	if err == nil || !strings.Contains(err.Error(), "profiling") {
		t.Fatalf("normalizeCreate error = %v, want profiling rejection", err)
	}
}

func TestNormalizeCreateRejectsStoredDefaultProfilingPath(t *testing.T) {
	settings := DefaultSettings()
	settings.Defaults.Options.Output.ProfileMutex = "/tmp/stored-mutex.pprof"
	_, err := normalizeCreate(CreateSessionRequest{Target: "https://example.test/FUZZ"}, settings)
	if err == nil || !strings.Contains(err.Error(), "profiling") {
		t.Fatalf("normalizeCreate error = %v, want stored profiling rejection", err)
	}
}

func TestSettingsRejectProfilingExplicitFlags(t *testing.T) {
	server := testServer(t)
	settings := DefaultSettings()
	settings.Defaults.ExplicitFlags = append(settings.Defaults.ExplicitFlags, "profile-cpu")
	payload, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authenticatedRequest(http.MethodPut, "/api/v1/settings", bytes.NewReader(payload)))
	if recorder.Code != http.StatusUnprocessableEntity || !strings.Contains(recorder.Body.String(), "profile-cpu") {
		t.Fatalf("settings response = %d %s, want profiling rejection", recorder.Code, recorder.Body.String())
	}
}

func TestCreateSessionRejectsSubmittedProfilingPath(t *testing.T) {
	server := testServer(t)
	body := `{"target":"https://example.test/FUZZ","options":{"output":{"profile_cpu":"/tmp/server.pprof"}}}`
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authenticatedRequest(http.MethodPost, "/api/v1/sessions", strings.NewReader(body)))
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "profile_cpu") {
		t.Fatalf("create response = %d %s, want unknown profiling path rejection", recorder.Code, recorder.Body.String())
	}
}

func TestStoredSessionProfilingFlagIsRejectedBeforeStart(t *testing.T) {
	server := testServer(t)
	wordlist := filepath.Join(t.TempDir(), "words.txt")
	if err := os.WriteFile(wordlist, []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	session, err := server.manager.Create(context.Background(), CreateSessionRequest{
		Target: "https://example.test/FUZZ", Wordlist: wordlist,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.store.db.ExecContext(context.Background(),
		`UPDATE sessions SET explicit_flags_json=? WHERE id=?`, `["profile-block"]`, session.ID); err != nil {
		t.Fatal(err)
	}

	err = server.manager.Start(context.Background(), session.ID)
	if err == nil || !strings.Contains(err.Error(), "profile-block") {
		t.Fatalf("Start error = %v, want stored profiling flag rejection", err)
	}
}
