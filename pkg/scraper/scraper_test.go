package scraper

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

// TestFromDir_UsesDirnameNotGlobal proves FromDir enumerates the directory it is
// given rather than the package-global ffuf.SCRAPERDIR. Before the fix it read the
// global for the directory listing while opening the entries relative to dirname,
// so a valid dirname combined with a missing global still errored.
func TestFromDir_UsesDirnameNotGlobal(t *testing.T) {
	orig := ffuf.SCRAPERDIR
	defer func() { ffuf.SCRAPERDIR = orig }()

	valid := t.TempDir() // exists, empty
	ffuf.SCRAPERDIR = filepath.Join(t.TempDir(), "does-not-exist")

	_, errs := FromDir(valid, "all")
	if errs.ErrorOrNil() != nil {
		t.Errorf("FromDir should read its dirname argument (a valid dir), not the global (missing): %v", errs.ErrorOrNil())
	}
}

func TestNeedsResponseBodyMatchesExecuteTargets(t *testing.T) {
	tests := []struct {
		name   string
		target string
		want   bool
	}{
		{name: "headers", target: "headers", want: false},
		{name: "body", target: "body", want: true},
		{name: "both", target: "both", want: true},
		{name: "omitted defaults to both", target: "", want: true},
		{name: "unknown defaults to both", target: "future-target", want: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scraper := &Scraper{Rules: []*ScraperRule{{Target: test.target}}}
			if got := scraper.NeedsResponseBody(); got != test.want {
				t.Fatalf("NeedsResponseBody() = %t, want %t for target %q", got, test.want, test.target)
			}
		})
	}
}

func TestNeedsResponseBodyIncludesInstalledAndCustomRules(t *testing.T) {
	t.Run("active installed default", func(t *testing.T) {
		dir := t.TempDir()
		writeScraperGroup(t, filepath.Join(dir, "default.json"), ScraperGroup{
			Name:   "default-body",
			Active: true,
			Rules: []*ScraperRule{{
				Name:   "installed-body",
				Rule:   "secret",
				Target: "body",
				Type:   "regexp",
			}},
		})

		loaded, errs := FromDir(dir, "all")
		if err := errs.ErrorOrNil(); err != nil {
			t.Fatalf("FromDir: %v", err)
		}
		if !loaded.NeedsResponseBody() {
			t.Fatal("active installed body rule was not reported as requiring the response body")
		}
	})

	t.Run("custom file appended to header defaults", func(t *testing.T) {
		scraper := &Scraper{Rules: []*ScraperRule{{Target: "headers"}}}
		if scraper.NeedsResponseBody() {
			t.Fatal("header-only rules unexpectedly require the response body")
		}

		path := filepath.Join(t.TempDir(), "custom.json")
		writeScraperGroup(t, path, ScraperGroup{
			Name: "custom-body",
			Rules: []*ScraperRule{{
				Name:   "custom-body",
				Rule:   "secret",
				Target: "both",
				Type:   "regexp",
			}},
		})
		if err := scraper.AppendFromFile(path); err != nil {
			t.Fatalf("AppendFromFile: %v", err)
		}
		if !scraper.NeedsResponseBody() {
			t.Fatal("custom both-target rule was not reported as requiring the response body")
		}
	})
}

func writeScraperGroup(t *testing.T, path string, group ScraperGroup) {
	t.Helper()
	data, err := json.Marshal(group)
	if err != nil {
		t.Fatalf("marshal scraper group: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write scraper group: %v", err)
	}
}
