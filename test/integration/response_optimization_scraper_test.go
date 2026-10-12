package integration

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/assetnote/h3fuzz/pkg/assembly"
	"github.com/assetnote/h3fuzz/pkg/ffuf"
	"github.com/assetnote/h3fuzz/pkg/filter"
)

func TestBodyScrapersForceGETAndPreserveResults(t *testing.T) {
	tests := []struct {
		name   string
		target string
		custom bool
	}{
		{name: "custom body scraper", target: "body", custom: true},
		{name: "installed both scraper", target: "both", custom: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var methodsMu sync.Mutex
			methods := make([]string, 0, 1)
			const responseBody = "prefix BODY_SECRET suffix"
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				methodsMu.Lock()
				methods = append(methods, r.Method)
				methodsMu.Unlock()

				w.Header().Set("Content-Type", "text/plain")
				w.Header().Set("Content-Length", "25")
				w.WriteHeader(http.StatusOK)
				if r.Method != http.MethodHead {
					_, _ = io.WriteString(w, responseBody)
				}
			}))
			defer target.Close()

			scraperPath := filepath.Join(t.TempDir(), "body-scraper.json")
			if !test.custom {
				scraperPath = filepath.Join(ffuf.SCRAPERDIR, "integration-body-scraper.json")
				defer os.Remove(scraperPath)
			}
			writeBodyScraperFile(t, scraperPath, test.target)

			opts := ffuf.NewConfigOptions()
			opts.HTTP.URL = target.URL + "/FUZZ"
			opts.HTTP.Method = http.MethodGet
			opts.HTTP.NoHttp3 = true
			opts.Input.Wordlists = []string{writeWordlist(t, []string{"asset"})}
			opts.General.Quiet = true
			opts.General.Threads = 1
			if test.custom {
				opts.General.ScraperFile = scraperPath
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			conf, err := ffuf.ConfigFromOptions(opts, ctx, cancel)
			if err != nil {
				t.Fatalf("ConfigFromOptions: %v", err)
			}
			conf.MatcherManager = filter.NewMatcherManager()
			mustMatch(t, conf.MatcherManager, "status", "all")

			job, err := assembly.BuildJob(conf)
			if err != nil {
				t.Fatalf("BuildJob: %v", err)
			}
			if !conf.ScraperNeedsResponseBody {
				t.Fatal("assembled config did not record the active body scraper")
			}
			if conf.ResponseOptimizationStrategy != "GET" {
				t.Fatalf("response optimization strategy = %q, want GET", conf.ResponseOptimizationStrategy)
			}
			if !strings.Contains(conf.ResponseOptimizationReason, "scraper") {
				t.Fatalf("response optimization reason = %q, want scraper body dependency", conf.ResponseOptimizationReason)
			}

			out := &capture{}
			job.Output = out
			job.Start()

			if got := out.scraperResults("body-secret"); len(got) != 1 || got[0] != "BODY_SECRET" {
				t.Fatalf("body scraper results = %q, want [BODY_SECRET]", got)
			}
			methodsMu.Lock()
			gotMethods := append([]string(nil), methods...)
			methodsMu.Unlock()
			if len(gotMethods) != 1 || gotMethods[0] != http.MethodGet {
				t.Fatalf("target methods = %v, want one GET and no optimization probes", gotMethods)
			}
		})
	}
}

func writeBodyScraperFile(t *testing.T, path, target string) {
	t.Helper()
	document := map[string]interface{}{
		"groupname": "body-integration",
		"active":    true,
		"rules": []map[string]interface{}{
			{
				"name":        "body-secret",
				"rule":        "BODY_SECRET",
				"target":      target,
				"type":        "regexp",
				"onlymatched": false,
				"action":      []string{},
			},
		},
	}
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("marshal scraper file: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write scraper file: %v", err)
	}
}

func (c *capture) scraperResults(name string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var results []string
	for _, response := range c.responses {
		results = append(results, response.ScraperData[name]...)
	}
	return results
}
