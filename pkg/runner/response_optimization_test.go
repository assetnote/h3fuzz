package runner

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
	ffilter "github.com/assetnote/h3fuzz/pkg/filter"
)

type optimizationServerBehavior struct {
	headStatus      int
	headLengthDelta int
	rangeMode       string
	emptyHit        bool
	requireHeaders  bool
}

type optimizationRequestLog struct {
	mu      sync.Mutex
	paths   map[string]int
	invalid []string
}

func (l *optimizationRequestLog) record(r *http.Request, requireHeaders bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.paths == nil {
		l.paths = make(map[string]int)
	}
	l.paths[r.URL.Path]++
	if !requireHeaders {
		return
	}
	if got := r.Header.Get("Authorization"); got != "Bearer probe-secret" {
		l.invalid = append(l.invalid, "authorization="+got)
	}
	if got := r.Header.Get("X-Probe"); got != "preserved" {
		l.invalid = append(l.invalid, "x-probe="+got)
	}
	if got := r.Header.Get("Accept-Encoding"); got != "identity" {
		l.invalid = append(l.invalid, "accept-encoding="+got)
	}
}

func (l *optimizationRequestLog) snapshot() (map[string]int, []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	paths := make(map[string]int, len(l.paths))
	for path, count := range l.paths {
		paths[path] = count
	}
	return paths, append([]string(nil), l.invalid...)
}

func newOptimizationServer(t *testing.T, behavior optimizationServerBehavior) (*httptest.Server, *optimizationRequestLog) {
	t.Helper()
	log := &optimizationRequestLog{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.record(r, behavior.requireHeaders)
		hit := r.URL.Path == "/"
		body := []byte("missing")
		status := http.StatusNotFound
		if hit {
			status = http.StatusOK
			body = []byte("hello")
			if behavior.emptyHit {
				body = nil
			}
		}

		w.Header().Set("Content-Type", "text/plain")
		if r.Method == http.MethodHead {
			if behavior.headStatus != 0 {
				w.Header().Set("Content-Length", "0")
				w.WriteHeader(behavior.headStatus)
				return
			}
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)+behavior.headLengthDelta))
			w.WriteHeader(status)
			return
		}

		if r.Header.Get("Range") == "bytes=-1" && hit {
			switch behavior.rangeMode {
			case "good":
				if len(body) == 0 {
					w.Header().Set("Content-Range", "bytes */0")
					w.Header().Set("Content-Length", "0")
					w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
					return
				}
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", len(body)-1, len(body)-1, len(body)))
				w.Header().Set("Content-Length", "1")
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write(body[len(body)-1:])
				return
			case "lying":
				w.Header().Set("Content-Range", "bytes 4-4/999")
				w.Header().Set("Content-Length", "1")
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write([]byte("o"))
				return
			case "neither":
				w.Header().Set("Content-Length", "0")
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			case "ignored", "":
				// Fall through to the complete 200 response.
			}
		}

		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	return server, log
}

func optimizationConfigAndRequest(serverURL string) (*ffuf.Config, *ffuf.Request) {
	conf := &ffuf.Config{
		InputProviders: []ffuf.InputProviderConfig{{Name: "wordlist", Keyword: "FUZZ"}},
	}
	req := &ffuf.Request{
		Method: http.MethodGet,
		Url:    serverURL + "/FUZZ",
		Headers: map[string]string{
			"Authorization": "Bearer probe-secret",
			"X-Probe":       "preserved",
		},
	}
	return conf, req
}

func TestDetectResponseOptimizationSelectsHEADForNamedWordlistKeyword(t *testing.T) {
	server, _ := newOptimizationServer(t, optimizationServerBehavior{})
	wordlist := t.TempDir() + "/words.txt"
	if err := os.WriteFile(wordlist, []byte("admin\n"), 0o600); err != nil {
		t.Fatalf("write wordlist: %v", err)
	}

	opts := ffuf.NewConfigOptions()
	opts.HTTP.URL = server.URL + "/PATH"
	opts.HTTP.Method = http.MethodGet
	opts.Input.Wordlists = []string{wordlist + ":PATH"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conf, err := ffuf.ConfigFromOptions(opts, ctx, cancel)
	if err != nil {
		t.Fatalf("ConfigFromOptions: %v", err)
	}
	if len(conf.InputProviders) != 1 || conf.InputProviders[0].Keyword != "PATH" {
		t.Fatalf("configured input providers = %+v, want named PATH provider", conf.InputProviders)
	}

	baseReq := ffuf.BaseRequest(conf)
	report := detectResponseOptimization(ctx, optimizationClient(server), conf, &baseReq)
	if report.mode != responseOptimizationHEAD {
		t.Fatalf("mode = %s, want HEAD for named wordlist keyword; reason: %s", report.mode, report.reason)
	}
	if report.probes != 6 {
		t.Fatalf("probes = %d, want 6", report.probes)
	}
}

func TestResponseOptimizationKeywordsUseNamedCommandProviderFromConfig(t *testing.T) {
	opts := ffuf.NewConfigOptions()
	opts.HTTP.URL = "https://example.test/PATH"
	opts.HTTP.Method = http.MethodGet
	opts.Input.Inputcommands = []string{"printf payload:PATH"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conf, err := ffuf.ConfigFromOptions(opts, ctx, cancel)
	if err != nil {
		t.Fatalf("ConfigFromOptions: %v", err)
	}

	keywords := responseOptimizationKeywords(conf)
	if len(keywords) != 1 || keywords[0] != "PATH" {
		t.Fatalf("optimization keywords = %v, want [PATH] from the named command provider", keywords)
	}
}

func optimizationClient(server *httptest.Server) *http.Client {
	client := server.Client()
	client.Timeout = 2 * time.Second
	return client
}

func TestDetectResponseOptimizationSelectsHEADAndPreservesProbeHeaders(t *testing.T) {
	server, requestLog := newOptimizationServer(t, optimizationServerBehavior{requireHeaders: true})
	conf, baseReq := optimizationConfigAndRequest(server.URL)

	report := detectResponseOptimization(context.Background(), optimizationClient(server), conf, baseReq)
	if report.mode != responseOptimizationHEAD {
		t.Fatalf("mode = %s, want HEAD; reason: %s", report.mode, report.reason)
	}
	if report.probes != 6 {
		t.Fatalf("probes = %d, want 6", report.probes)
	}
	if report.reason == "" {
		t.Fatal("expected an explicit selection reason")
	}
	if !report.headAttempted || !report.headReliable || report.headProbes != 3 || report.rangeAttempted {
		t.Fatalf("unexpected HEAD/Range decision evidence: %+v", report)
	}
	if report.rangeReason == "" || report.baselineProbes != 3 {
		t.Fatalf("incomplete probe evidence: %+v", report)
	}

	paths, invalid := requestLog.snapshot()
	if len(invalid) != 0 {
		t.Fatalf("probe did not preserve headers: %v", invalid)
	}
	if paths["/"] != 2 {
		t.Fatalf("likely-existing path was requested %d times, want 2", paths["/"])
	}
	if len(paths) != 3 {
		t.Fatalf("distinct paths = %d (%v), want likely-existing plus two misses", len(paths), paths)
	}
	for path, count := range paths {
		if strings.Contains(path, "FUZZ") {
			t.Fatalf("probe path retained fuzz keyword: %q", path)
		}
		if count != 2 {
			t.Fatalf("path %q count = %d, want GET+HEAD", path, count)
		}
	}

	req, err := http.NewRequest(http.MethodGet, server.URL+"/asset", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !report.apply(req) {
		t.Fatal("HEAD optimization was not applied")
	}
	if req.Method != http.MethodHead || req.Header.Get("Accept-Encoding") != "identity" {
		t.Fatalf("applied request = method %s, encoding %q", req.Method, req.Header.Get("Accept-Encoding"))
	}
}

func TestDetectResponseOptimizationRequiresOrdinaryHEADHit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Length", "7")
		w.WriteHeader(http.StatusNotFound)
		if r.Method != http.MethodHead {
			_, _ = w.Write([]byte("missing"))
		}
	}))
	defer server.Close()
	conf, baseReq := optimizationConfigAndRequest(server.URL)

	report := detectResponseOptimization(context.Background(), optimizationClient(server), conf, baseReq)
	if report.mode != responseOptimizationGET {
		t.Fatalf("mode = %s, want GET when all baseline paths are 404; reason: %s", report.mode, report.reason)
	}
	if !strings.Contains(report.headReason, "ordinary 2xx hit") {
		t.Fatalf("HEAD reason = %q, want missing ordinary hit explanation", report.headReason)
	}
}

func TestHEADRuntimeStatusMustHaveBeenProvenByBaseline(t *testing.T) {
	server, _ := newOptimizationServer(t, optimizationServerBehavior{})
	conf, baseReq := optimizationConfigAndRequest(server.URL)
	report := detectResponseOptimization(context.Background(), optimizationClient(server), conf, baseReq)
	if report.mode != responseOptimizationHEAD {
		t.Fatalf("mode = %s, want HEAD; reason: %s", report.mode, report.reason)
	}

	known := report.logicalMetadata(http.StatusOK, http.Header{"Content-Length": []string{"5"}}, 0)
	if !known.trustworthy {
		t.Fatalf("proven 200 status was rejected: %+v", known)
	}
	unseen := report.logicalMetadata(http.StatusMethodNotAllowed, http.Header{"Content-Length": []string{"0"}}, 0)
	if unseen.trustworthy {
		t.Fatalf("unseen route-specific HEAD 405 was trusted: %+v", unseen)
	}
}

func TestDetectResponseOptimizationRejectsLyingHEADThenSelectsRange(t *testing.T) {
	server, _ := newOptimizationServer(t, optimizationServerBehavior{
		headLengthDelta: 1,
		rangeMode:       "good",
	})
	conf, baseReq := optimizationConfigAndRequest(server.URL)

	report := detectResponseOptimization(context.Background(), optimizationClient(server), conf, baseReq)
	if report.mode != responseOptimizationRange {
		t.Fatalf("mode = %s, want RANGE; reason: %s", report.mode, report.reason)
	}
	if report.probes != 9 {
		t.Fatalf("probes = %d, want 9", report.probes)
	}
	if !strings.Contains(report.headReason, "length") {
		t.Fatalf("HEAD reason %q does not explain the lying length", report.headReason)
	}
}

func TestRangeRuntimeErrorStatusMustHaveBeenProvenByBaseline(t *testing.T) {
	server, _ := newOptimizationServer(t, optimizationServerBehavior{
		headLengthDelta: 1,
		rangeMode:       "good",
	})
	conf, baseReq := optimizationConfigAndRequest(server.URL)
	report := detectResponseOptimization(context.Background(), optimizationClient(server), conf, baseReq)
	if report.mode != responseOptimizationRange {
		t.Fatalf("mode = %s, want RANGE; reason: %s", report.mode, report.reason)
	}

	knownMiss := report.logicalMetadata(http.StatusNotFound, http.Header{"Content-Length": []string{"7"}}, 7)
	if !knownMiss.trustworthy {
		t.Fatalf("proven 404 miss status was rejected: %+v", knownMiss)
	}
	unseen := report.logicalMetadata(http.StatusMethodNotAllowed, http.Header{"Content-Length": []string{"0"}}, 0)
	if unseen.trustworthy {
		t.Fatalf("unseen route-specific Range 405 was trusted: %+v", unseen)
	}
}

func TestDetectResponseOptimizationHandles405HEADWithRange(t *testing.T) {
	server, _ := newOptimizationServer(t, optimizationServerBehavior{
		headStatus: http.StatusMethodNotAllowed,
		rangeMode:  "good",
	})
	conf, baseReq := optimizationConfigAndRequest(server.URL)

	report := detectResponseOptimization(context.Background(), optimizationClient(server), conf, baseReq)
	if report.mode != responseOptimizationRange {
		t.Fatalf("mode = %s, want RANGE; reason: %s", report.mode, report.reason)
	}
	if !strings.Contains(report.headReason, "405") {
		t.Fatalf("HEAD reason %q does not report 405", report.headReason)
	}

	req, err := http.NewRequest(http.MethodGet, server.URL+"/asset", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !report.apply(req) || req.Header.Get("Range") != "bytes=-1" {
		t.Fatalf("Range optimization was not applied: %#v", req.Header)
	}
	metadata := report.logicalMetadata(http.StatusPartialContent, http.Header{
		"Content-Range":  []string{"bytes 4-4/5"},
		"Content-Length": []string{"1"},
	}, 1)
	if !metadata.trustworthy || !metadata.normalized || metadata.statusCode != http.StatusOK || metadata.contentLength != 5 {
		t.Fatalf("logical metadata = %+v, want trustworthy normalized 200/5", metadata)
	}
}

func TestDetectResponseOptimizationRejectsIgnoredRange(t *testing.T) {
	server, _ := newOptimizationServer(t, optimizationServerBehavior{
		headStatus: http.StatusMethodNotAllowed,
		rangeMode:  "ignored",
	})
	conf, baseReq := optimizationConfigAndRequest(server.URL)

	report := detectResponseOptimization(context.Background(), optimizationClient(server), conf, baseReq)
	if report.mode != responseOptimizationGET {
		t.Fatalf("mode = %s, want GET; reason: %s", report.mode, report.reason)
	}
	if !strings.Contains(report.rangeReason, "instead of 206") {
		t.Fatalf("Range reason %q does not explain ignored Range", report.rangeReason)
	}
}

func TestDetectResponseOptimizationRejectsLyingRange(t *testing.T) {
	server, _ := newOptimizationServer(t, optimizationServerBehavior{
		headStatus: http.StatusMethodNotAllowed,
		rangeMode:  "lying",
	})
	conf, baseReq := optimizationConfigAndRequest(server.URL)

	report := detectResponseOptimization(context.Background(), optimizationClient(server), conf, baseReq)
	if report.mode != responseOptimizationGET {
		t.Fatalf("mode = %s, want GET; reason: %s", report.mode, report.reason)
	}
	if !strings.Contains(report.rangeReason, "did not match GET length") {
		t.Fatalf("Range reason %q does not explain the false total", report.rangeReason)
	}
}

func TestDetectResponseOptimizationRejectsNeitherImplementation(t *testing.T) {
	server, _ := newOptimizationServer(t, optimizationServerBehavior{
		headStatus: http.StatusMethodNotAllowed,
		rangeMode:  "neither",
	})
	conf, baseReq := optimizationConfigAndRequest(server.URL)

	report := detectResponseOptimization(context.Background(), optimizationClient(server), conf, baseReq)
	if report.mode != responseOptimizationGET {
		t.Fatalf("mode = %s, want GET; reason: %s", report.mode, report.reason)
	}
	if report.probes != 9 || report.headReason == "" || report.rangeReason == "" || report.reason == "" {
		t.Fatalf("incomplete report: %+v", report)
	}
}

func TestDetectResponseOptimizationHandlesEmptyEntity(t *testing.T) {
	server, _ := newOptimizationServer(t, optimizationServerBehavior{
		rangeMode: "good",
		emptyHit:  true,
	})
	conf, baseReq := optimizationConfigAndRequest(server.URL)

	report := detectResponseOptimization(context.Background(), optimizationClient(server), conf, baseReq)
	if report.mode != responseOptimizationHEAD {
		t.Fatalf("mode = %s, want HEAD for an empty entity; reason: %s", report.mode, report.reason)
	}
	metadata := report.logicalMetadata(http.StatusOK, http.Header{"Content-Length": []string{"0"}}, 0)
	if !metadata.trustworthy || metadata.contentLength != 0 {
		t.Fatalf("HEAD empty metadata = %+v", metadata)
	}

	zeroRange := normalizeResponseMetadata(
		responseOptimizationRange,
		http.StatusRequestedRangeNotSatisfiable,
		http.Header{"Content-Range": []string{"bytes */0"}},
		0,
	)
	if !zeroRange.trustworthy || !zeroRange.normalized || zeroRange.statusCode != http.StatusOK || zeroRange.contentLength != 0 {
		t.Fatalf("zero Range metadata = %+v, want trustworthy normalized 200/0", zeroRange)
	}
}

func TestDetectResponseOptimizationRequiresNonEmptyRangeHit(t *testing.T) {
	server, _ := newOptimizationServer(t, optimizationServerBehavior{
		headStatus: http.StatusMethodNotAllowed,
		rangeMode:  "good",
		emptyHit:   true,
	})
	conf, baseReq := optimizationConfigAndRequest(server.URL)

	report := detectResponseOptimization(context.Background(), optimizationClient(server), conf, baseReq)
	if report.mode != responseOptimizationGET {
		t.Fatalf("mode = %s, want GET without a one-byte 206 hit; reason: %s", report.mode, report.reason)
	}
	if !strings.Contains(report.rangeReason, "no non-empty") {
		t.Fatalf("Range reason = %q", report.rangeReason)
	}
}

func TestResponseOptimizationApplyNeverOverridesExplicitRequest(t *testing.T) {
	report := responseOptimizationReport{mode: responseOptimizationRange}

	post, err := http.NewRequest(http.MethodPost, "https://example.test/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.apply(post) || post.Method != http.MethodPost || post.Header.Get("Range") != "" {
		t.Fatalf("automatic optimization changed explicit method: %#v", post)
	}

	ranged, err := http.NewRequest(http.MethodGet, "https://example.test/", nil)
	if err != nil {
		t.Fatal(err)
	}
	ranged.Header.Set("Range", "bytes=0-7")
	if report.apply(ranged) || ranged.Header.Get("Range") != "bytes=0-7" {
		t.Fatalf("automatic optimization overwrote user Range: %#v", ranged.Header)
	}

	body, err := http.NewRequest(http.MethodGet, "https://example.test/", strings.NewReader("body"))
	if err != nil {
		t.Fatal(err)
	}
	if report.apply(body) {
		t.Fatal("automatic optimization changed a bodyful GET")
	}
}

type rejectingOptimizationClient struct {
	calls int
}

func (c *rejectingOptimizationClient) Do(*http.Request) (*http.Response, error) {
	c.calls++
	return nil, fmt.Errorf("network should not be reached")
}

func TestDetectResponseOptimizationForcesGETForUnsafeConfigurations(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ffuf.Config, *ffuf.Request)
		want   string
	}{
		{
			name: "disabled",
			mutate: func(conf *ffuf.Config, _ *ffuf.Request) {
				conf.NoResponseOptimization = true
			},
			want: "disabled",
		},
		{
			name: "custom method",
			mutate: func(_ *ffuf.Config, req *ffuf.Request) {
				req.Method = http.MethodPost
			},
			want: "GET discovery",
		},
		{
			name: "body",
			mutate: func(_ *ffuf.Config, req *ffuf.Request) {
				req.Data = []byte("payload")
			},
			want: "bodyless",
		},
		{
			name: "user range",
			mutate: func(_ *ffuf.Config, req *ffuf.Request) {
				req.Headers["rAnGe"] = "bytes=0-10"
			},
			want: "user-supplied Range",
		},
		{
			name: "autocalibration",
			mutate: func(conf *ffuf.Config, _ *ffuf.Request) {
				conf.AutoCalibration = true
			},
			want: "autocalibration",
		},
		{
			name: "body scraper",
			mutate: func(conf *ffuf.Config, _ *ffuf.Request) {
				conf.ScraperNeedsResponseBody = true
			},
			want: "scraper",
		},
		{
			name: "raw URL semantics",
			mutate: func(conf *ffuf.Config, _ *ffuf.Request) {
				conf.Raw = true
			},
			want: "raw URL",
		},
		{
			name: "audit",
			mutate: func(conf *ffuf.Config, _ *ffuf.Request) {
				conf.AuditLog = "audit.json"
			},
			want: "artifacts",
		},
		{
			name: "output directory",
			mutate: func(conf *ffuf.Config, _ *ffuf.Request) {
				conf.OutputDirectory = "responses"
			},
			want: "artifacts",
		},
		{
			name: "output file",
			mutate: func(conf *ffuf.Config, _ *ffuf.Request) {
				conf.OutputFile = "results.json"
			},
			want: "artifacts",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			conf, req := optimizationConfigAndRequest("https://example.test")
			test.mutate(conf, req)
			client := &rejectingOptimizationClient{}
			report := detectResponseOptimization(context.Background(), client, conf, req)
			if report.mode != responseOptimizationGET || report.probes != 0 || client.calls != 0 {
				t.Fatalf("unexpected probe for unsafe configuration: report=%+v calls=%d", report, client.calls)
			}
			if !strings.Contains(report.reason, test.want) {
				t.Fatalf("reason %q does not contain %q", report.reason, test.want)
			}
		})
	}
}

func TestDetectResponseOptimizationRejectsDistinctHeaderAndHostKeywords(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ffuf.Request)
		want   string
	}{
		{
			name: "header value",
			mutate: func(req *ffuf.Request) {
				req.Headers["X-Tenant"] = "TENANT"
			},
			want: "headers",
		},
		{
			name: "header name",
			mutate: func(req *ffuf.Request) {
				req.Headers["X-TENANT"] = "fixed"
			},
			want: "headers",
		},
		{
			name: "host override",
			mutate: func(req *ffuf.Request) {
				req.Host = "TENANT.example.test"
			},
			want: "host discovery",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			conf, req := optimizationConfigAndRequest("https://example.test")
			conf.InputProviders = []ffuf.InputProviderConfig{
				{Name: "wordlist", Keyword: "PATH"},
				{Name: "command", Keyword: "TENANT"},
			}
			// ConfigFromOptions historically records the command text here for a
			// named command provider. It must not hide the provider's real keyword.
			conf.CommandKeywords = []string{"printf tenant-value"}
			req.Url = "https://example.test/PATH"
			test.mutate(req)
			client := &rejectingOptimizationClient{}

			report := detectResponseOptimization(context.Background(), client, conf, req)
			if report.mode != responseOptimizationGET || report.probes != 0 || client.calls != 0 {
				t.Fatalf("distinct header/host keyword was probed: report=%+v calls=%d", report, client.calls)
			}
			if !strings.Contains(report.reason, test.want) {
				t.Fatalf("reason %q does not contain %q", report.reason, test.want)
			}
		})
	}
}

func TestDetectResponseOptimizationForcesGETForBodyMatchersAndFilters(t *testing.T) {
	for _, providerType := range []string{"matcher", "filter"} {
		for _, name := range []string{"regexp", "word", "line", "time"} {
			t.Run(providerType+"/"+name, func(t *testing.T) {
				conf, req := optimizationConfigAndRequest("https://example.test")
				manager := ffilter.NewMatcherManager()
				value := "1"
				if name == "regexp" {
					value = "secret"
				} else if name == "time" {
					value = ">1"
				}
				var err error
				if providerType == "matcher" {
					err = manager.AddMatcher(name, value)
				} else {
					err = manager.AddFilter(name, value, false)
				}
				if err != nil {
					t.Fatal(err)
				}
				conf.MatcherManager = manager
				client := &rejectingOptimizationClient{}

				report := detectResponseOptimization(context.Background(), client, conf, req)
				if report.mode != responseOptimizationGET || report.probes != 0 || client.calls != 0 {
					t.Fatalf("body-dependent %s was probed: report=%+v calls=%d", providerType, report, client.calls)
				}
				if !strings.Contains(report.reason, name) {
					t.Fatalf("reason %q does not name %s", report.reason, name)
				}
			})
		}
	}
}

func TestDetectResponseOptimizationForcesGETForPerHostTimeFilter(t *testing.T) {
	conf, req := optimizationConfigAndRequest("https://example.test")
	manager := ffilter.NewMatcherManager()
	if err := manager.AddPerDomainFilter("example.test", "time", ">1"); err != nil {
		t.Fatal(err)
	}
	conf.MatcherManager = manager
	client := &rejectingOptimizationClient{}

	report := detectResponseOptimization(context.Background(), client, conf, req)
	if report.mode != responseOptimizationGET || report.probes != 0 || client.calls != 0 {
		t.Fatalf("per-host response-time filter was probed: report=%+v calls=%d", report, client.calls)
	}
	if !strings.Contains(report.reason, "time") {
		t.Fatalf("reason %q does not name time filter", report.reason)
	}
}

func TestNormalizeResponseMetadataRejectsMalformedRange(t *testing.T) {
	metadata := normalizeResponseMetadata(
		responseOptimizationRange,
		http.StatusPartialContent,
		http.Header{"Content-Range": []string{"bytes 3-3/5"}},
		1,
	)
	if metadata.trustworthy || metadata.normalized {
		t.Fatalf("malformed final-byte response was trusted: %+v", metadata)
	}

	metadata = normalizeResponseMetadata(
		responseOptimizationRange,
		http.StatusRequestedRangeNotSatisfiable,
		http.Header{"Content-Range": []string{"bytes */7"}},
		0,
	)
	if metadata.trustworthy || metadata.normalized {
		t.Fatalf("non-empty 416 response was trusted: %+v", metadata)
	}
}
