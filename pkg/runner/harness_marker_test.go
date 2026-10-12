package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

const testHarnessRunID = "11111111-2222-4333-8444-555555555555"

func TestExecuteWithRunTelemetryMarksMainWorkloadRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conf := ffuf.NewConfig(ctx, cancel)
	conf.Method = http.MethodGet
	conf.HarnessRunID = testHarnessRunID
	runner := NewSimpleRunner(&conf, false).(*SimpleRunner)

	var runID, role string
	runner.client.Transport = testRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		runID = req.Header.Get(ffuf.HarnessRunHeader)
		role = req.Header.Get(ffuf.HarnessRoleHeader)
		return &http.Response{
			Status:        "200 OK",
			StatusCode:    http.StatusOK,
			Header:        make(http.Header),
			Body:          io.NopCloser(strings.NewReader("ok")),
			ContentLength: 2,
			Request:       req,
		}, nil
	})
	req := ffuf.NewRequest(&conf)
	req.Method = http.MethodGet
	req.Url = "https://example.test/bench/word"

	if _, err := runner.ExecuteWithRunTelemetry(&req, ffuf.RunTransportRoleInitial, nil); err != nil {
		t.Fatalf("ExecuteWithRunTelemetry: %v", err)
	}
	if runID != testHarnessRunID || role != string(ffuf.RunTransportRoleInitial) {
		t.Fatalf("marker headers = run %q role %q", runID, role)
	}
}

func TestHarnessMarkerDistinguishesOptimizedFallbackRoles(t *testing.T) {
	tests := []struct {
		name         string
		outerRole    ffuf.RunTransportAttemptRole
		fallbackRole ffuf.RunTransportAttemptRole
	}{
		{name: "initial", outerRole: ffuf.RunTransportRoleInitial, fallbackRole: ffuf.RunTransportRoleOptimizationFallback},
		{name: "engine retry", outerRole: ffuf.RunTransportRoleEngineRetry, fallbackRole: ffuf.RunTransportRoleEngineRetryOptimizationFallback},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner, _, req := optimizedExecuteRunner(t, responseOptimizationHEAD)
			runner.config.HarnessRunID = testHarnessRunID
			type observedMarker struct {
				method string
				runID  string
				role   string
			}
			var seen []observedMarker
			runner.client.Transport = testRoundTripFunc(func(httpReq *http.Request) (*http.Response, error) {
				seen = append(seen, observedMarker{
					method: httpReq.Method,
					runID:  httpReq.Header.Get(ffuf.HarnessRunHeader),
					role:   httpReq.Header.Get(ffuf.HarnessRoleHeader),
				})
				if len(seen) == 1 {
					return nil, errors.New("optimized attempt failed")
				}
				return optimizedHTTPResponse(httpReq, http.StatusNoContent, http.Header{}, http.NoBody, 0), nil
			})

			if _, err := runner.ExecuteWithRunTelemetry(&req, test.outerRole, nil); err != nil {
				t.Fatalf("ExecuteWithRunTelemetry: %v", err)
			}
			if len(seen) != 2 {
				t.Fatalf("requests = %+v, want optimized attempt plus fallback", seen)
			}
			if seen[0] != (observedMarker{method: http.MethodHead, runID: testHarnessRunID, role: string(test.outerRole)}) {
				t.Fatalf("optimized marker = %+v", seen[0])
			}
			if seen[1] != (observedMarker{method: http.MethodGet, runID: testHarnessRunID, role: string(test.fallbackRole)}) {
				t.Fatalf("fallback marker = %+v", seen[1])
			}
		})
	}
}

func TestQUICApplicationMarkerSurvivesOptimizedFallbacks(t *testing.T) {
	for _, role := range []ffuf.RunTransportAttemptRole{
		ffuf.RunTransportRoleInitial,
		ffuf.RunTransportRoleEngineRetry,
	} {
		for _, fallback := range []string{"transport", "semantic"} {
			name := string(role) + "/" + fallback
			t.Run(name, func(t *testing.T) {
				runner, _, req := optimizedExecuteRunner(t, responseOptimizationHEAD)
				runner.config.HarnessRunID = testHarnessRunID
				runner.h3Pool = &http3Pool{}
				type observation struct {
					method string
					role   string
					marked bool
					traced bool
				}
				var seen []observation
				runner.client.Transport = testRoundTripFunc(func(httpReq *http.Request) (*http.Response, error) {
					seen = append(seen, observation{
						method: httpReq.Method,
						role:   httpReq.Header.Get(ffuf.HarnessRoleHeader),
						marked: isQUICApplicationAttempt(httpReq),
						traced: httptrace.ContextClientTrace(httpReq.Context()) != nil,
					})
					if len(seen) == 1 {
						if fallback == "transport" {
							return nil, errors.New("optimized attempt failed")
						}
						return optimizedHTTPResponse(
							httpReq,
							http.StatusOK,
							http.Header{"Content-Length": []string{"10"}},
							io.NopCloser(strings.NewReader("unexpected")),
							10,
						), nil
					}
					return optimizedHTTPResponse(httpReq, http.StatusNoContent, http.Header{}, http.NoBody, 0), nil
				})

				if _, err := runner.ExecuteWithRunTelemetry(&req, role, &attemptRecorder{}); err != nil {
					t.Fatalf("ExecuteWithRunTelemetry: %v", err)
				}
				wantFallbackRole := optimizationFallbackRole(role)
				want := []observation{
					// Run telemetry keeps the exact timing trace so its latency
					// histogram remains homogeneous write-to-first-byte evidence.
					{method: http.MethodHead, role: string(role), marked: true, traced: true},
					{method: http.MethodGet, role: string(wantFallbackRole), marked: true, traced: true},
				}
				if len(seen) != len(want) {
					t.Fatalf("requests = %+v, want %+v", seen, want)
				}
				for i := range want {
					if seen[i] != want[i] {
						t.Fatalf("request %d = %+v, want %+v", i, seen[i], want[i])
					}
				}
			})
		}
	}
}

func TestHarnessMarkerMarksSemanticGETRecovery(t *testing.T) {
	runner, _, req := optimizedExecuteRunner(t, responseOptimizationHEAD)
	runner.config.HarnessRunID = testHarnessRunID

	type observedMarker struct {
		method string
		runID  string
		role   string
		marked bool
	}
	var seen []observedMarker
	runner.client.Transport = testRoundTripFunc(func(httpReq *http.Request) (*http.Response, error) {
		seen = append(seen, observedMarker{
			method: httpReq.Method,
			runID:  httpReq.Header.Get(ffuf.HarnessRunHeader),
			role:   httpReq.Header.Get(ffuf.HarnessRoleHeader),
			marked: isQUICApplicationAttempt(httpReq),
		})
		if len(seen) == 1 {
			return optimizedHTTPResponse(
				httpReq,
				http.StatusOK,
				http.Header{"Content-Length": []string{"10"}},
				io.NopCloser(strings.NewReader("unexpected")),
				10,
			), nil
		}
		return optimizedHTTPResponse(httpReq, http.StatusNoContent, http.Header{}, http.NoBody, 0), nil
	})

	if _, err := runner.ExecuteWithRunTelemetry(&req, ffuf.RunTransportRoleInitial, nil); err != nil {
		t.Fatalf("ExecuteWithRunTelemetry: %v", err)
	}
	want := []observedMarker{
		{method: http.MethodHead, runID: testHarnessRunID, role: string(ffuf.RunTransportRoleInitial)},
		{method: http.MethodGet, runID: testHarnessRunID, role: string(ffuf.RunTransportRoleOptimizationFallback)},
	}
	if len(seen) != len(want) {
		t.Fatalf("requests = %+v, want %+v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("request %d marker = %+v, want %+v", i, seen[i], want[i])
		}
	}
}

func TestHarnessMarkerExcludedFromUninstrumentedOptimizedFallback(t *testing.T) {
	runner, _, req := optimizedExecuteRunner(t, responseOptimizationHEAD)
	runner.config.HarnessRunID = testHarnessRunID
	runner.replay = true
	runner.h3Pool = &http3Pool{}

	type observedMarker struct {
		method string
		runID  string
		role   string
		marked bool
	}
	var seen []observedMarker
	runner.client.Transport = testRoundTripFunc(func(httpReq *http.Request) (*http.Response, error) {
		seen = append(seen, observedMarker{
			method: httpReq.Method,
			runID:  httpReq.Header.Get(ffuf.HarnessRunHeader),
			role:   httpReq.Header.Get(ffuf.HarnessRoleHeader),
			marked: isQUICApplicationAttempt(httpReq),
		})
		if len(seen) == 1 {
			return nil, errors.New("optimized replay attempt failed")
		}
		return optimizedHTTPResponse(httpReq, http.StatusNoContent, http.Header{}, http.NoBody, 0), nil
	})

	if _, err := runner.Execute(&req); err != nil {
		t.Fatalf("Execute replay: %v", err)
	}
	if len(seen) != 2 {
		t.Fatalf("requests = %+v, want optimized attempt plus fallback", seen)
	}
	for i, marker := range seen {
		if marker.runID != "" || marker.role != "" || marker.marked {
			t.Fatalf("replay request %d marker = %+v, want unmarked", i, marker)
		}
	}
}

func TestHarnessMarkerExcludedFromPreflightAndPostflight(t *testing.T) {
	type requestMarker struct {
		path  string
		runID string
		role  string
	}
	seen := make(chan requestMarker, 3)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		seen <- requestMarker{
			path:  req.URL.Path,
			runID: req.Header.Get(ffuf.HarnessRunHeader),
			role:  req.Header.Get(ffuf.HarnessRoleHeader),
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	host := srv.Listener.Addr().String()
	before := writeTempRequest(t, fmt.Sprintf("GET /before HTTP/1.1\nHost: %s\n\n", host))
	after := writeTempRequest(t, fmt.Sprintf("GET /after HTTP/1.1\nHost: %s\n\n", host))
	conf := newTestConfig(srv.URL + "/main")
	conf.HarnessRunID = testHarnessRunID
	conf.Preflights = []ffuf.PreflightConfig{{RequestFile: before}}
	conf.Postflights = []ffuf.PreflightConfig{{RequestFile: after}}
	runner := newTestRunner(conf)
	runner.optimization = responseOptimizationReport{mode: responseOptimizationGET}
	req := ffuf.Request{Method: http.MethodGet, Url: srv.URL + "/main", Headers: map[string]string{}}

	if _, err := runner.ExecuteWithRunTelemetry(&req, ffuf.RunTransportRoleInitial, nil); err != nil {
		t.Fatalf("ExecuteWithRunTelemetry: %v", err)
	}
	want := []requestMarker{
		{path: "/before"},
		{path: "/main", runID: testHarnessRunID, role: string(ffuf.RunTransportRoleInitial)},
		{path: "/after"},
	}
	for i, expected := range want {
		got := <-seen
		if got != expected {
			t.Fatalf("request %d marker = %+v, want %+v", i, got, expected)
		}
	}
}

func TestHarnessMarkerExcludedFromStartupProbesAndWarmup(t *testing.T) {
	type requestMarker struct {
		runID string
		role  string
	}
	var (
		mu   sync.Mutex
		seen []requestMarker
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		seen = append(seen, requestMarker{
			runID: req.Header.Get(ffuf.HarnessRunHeader),
			role:  req.Header.Get(ffuf.HarnessRoleHeader),
		})
		mu.Unlock()
		w.Header().Set("Content-Length", "2")
		if req.Method != http.MethodHead {
			_, _ = io.WriteString(w, "ok")
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conf := ffuf.NewConfig(ctx, cancel)
	conf.Url = srv.URL + "/bench/FUZZ"
	conf.Method = http.MethodGet
	conf.NoHttp3 = true
	conf.HarnessRunID = testHarnessRunID
	runner := NewSimpleRunner(&conf, false).(*SimpleRunner)
	baseReq := ffuf.NewRequest(&conf)
	baseReq.Method = http.MethodGet
	baseReq.Url = conf.Url

	if err := runner.Initialize(&baseReq); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	mu.Lock()
	probeMarkers := append([]requestMarker(nil), seen...)
	mu.Unlock()
	if len(probeMarkers) == 0 {
		t.Fatal("Initialize sent no qualification probes")
	}
	for i, marker := range probeMarkers {
		if marker.runID != "" || marker.role != "" {
			t.Fatalf("startup probe %d marker = %+v, want unmarked", i, marker)
		}
	}

	target, err := url.Parse(srv.URL + "/bench/warmup")
	if err != nil {
		t.Fatal(err)
	}
	warmup, err := newHTTP3WarmupRequest(&conf, target)
	if err != nil {
		t.Fatalf("newHTTP3WarmupRequest: %v", err)
	}
	if runID, role := warmup.Header.Get(ffuf.HarnessRunHeader), warmup.Header.Get(ffuf.HarnessRoleHeader); runID != "" || role != "" {
		t.Fatalf("warmup marker = run %q role %q, want unmarked", runID, role)
	}

	runner.optimization = responseOptimizationReport{mode: responseOptimizationGET}
	mainReq := baseReq
	mainReq.Url = srv.URL + "/bench/word"
	if _, err := runner.ExecuteWithRunTelemetry(&mainReq, ffuf.RunTransportRoleInitial, nil); err != nil {
		t.Fatalf("ExecuteWithRunTelemetry: %v", err)
	}
	mu.Lock()
	mainMarker := seen[len(seen)-1]
	mu.Unlock()
	if mainMarker != (requestMarker{runID: testHarnessRunID, role: string(ffuf.RunTransportRoleInitial)}) {
		t.Fatalf("main marker = %+v", mainMarker)
	}
}

func TestHarnessMarkerRejectsRequestLevelHeaderCollision(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conf := ffuf.NewConfig(ctx, cancel)
	conf.Method = http.MethodGet
	conf.HarnessRunID = testHarnessRunID
	runner := NewSimpleRunner(&conf, false).(*SimpleRunner)
	transportCalled := false
	runner.client.Transport = testRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		transportCalled = true
		return optimizedHTTPResponse(req, http.StatusNoContent, http.Header{}, http.NoBody, 0), nil
	})
	req := ffuf.NewRequest(&conf)
	req.Method = http.MethodGet
	req.Url = "https://example.test/bench/collision"
	req.Headers[ffuf.HarnessRoleHeader] = "forged"

	_, err := runner.ExecuteWithRunTelemetry(&req, ffuf.RunTransportRoleInitial, nil)
	if err == nil || !strings.Contains(err.Error(), "reserved harness header") {
		t.Fatalf("ExecuteWithRunTelemetry error = %v, want collision rejection", err)
	}
	if transportCalled {
		t.Fatal("colliding request reached the transport")
	}
}

func TestRunTelemetryCarriesHarnessRunID(t *testing.T) {
	runner := &SimpleRunner{config: &ffuf.Config{
		Method:       http.MethodGet,
		Headers:      map[string]string{},
		HarnessRunID: testHarnessRunID,
	}}

	report := runner.RunTelemetry()
	if report.RunID == nil || *report.RunID != testHarnessRunID {
		t.Fatalf("telemetry run_id = %v, want %q", report.RunID, testHarnessRunID)
	}
}
