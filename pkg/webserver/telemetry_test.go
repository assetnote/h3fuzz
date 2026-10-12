package webserver

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/assetnote/h3fuzz/pkg/engine"
	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

func TestProgressFromFFUFExposesCompletedLogicalWork(t *testing.T) {
	progress := progressFromFFUF(ffuf.Progress{
		ReqCount: 2_001, CompletedCount: 73, ResponseCount: 70, ErrorCount: 3, ReqTotal: 3_000,
	})
	if progress.RequestCount != 2_001 || progress.CompletedCount != 73 {
		t.Fatalf("progress counters = %+v, want 2,001 scheduled and 73 completed", progress)
	}
	payload, err := json.Marshal(progress)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if got := decoded["completed_count"]; got != float64(73) {
		t.Fatalf("completed_count JSON = %#v, want 73", got)
	}
}

func TestThroughputTrackerReportsRollingOverallPeakAndETA(t *testing.T) {
	started := time.Unix(1_700_000_000, 0).UTC()
	tracker := throughputTracker{}
	initial := Progress{StartedAt: started, ReqTotal: 200}
	tracker.enrich(&initial, started)

	progress := Progress{StartedAt: started, ReqCount: 200, RequestCount: 200, CompletedCount: 100,
		ResponseCount: 90, ErrorCount: 10, ReqTotal: 200, ReqSec: 7}
	tracker.enrich(&progress, started.Add(10*time.Second))

	if progress.CurrentRate != 7 {
		t.Errorf("current rate = %v, want authoritative engine rate 7", progress.CurrentRate)
	}
	for name, got := range map[string]float64{
		"30-second": progress.Average30s, "overall": progress.OverallAverage,
	} {
		if math.Abs(got-10) > 0.001 {
			t.Errorf("%s rate = %v, want 10", name, got)
		}
	}
	if progress.PeakRate != 7 {
		t.Errorf("peak rate = %v, want 7", progress.PeakRate)
	}
	if progress.ETASeconds == nil || *progress.ETASeconds != 10 {
		t.Fatalf("ETA = %v, want 10 seconds", progress.ETASeconds)
	}
	if progress.RequestCount != 200 || progress.CompletedCount != 100 || progress.ResponseCount != 90 || progress.ErrorCount != 10 {
		t.Fatalf("separate totals were changed: %+v", progress)
	}

	progress.CompletedCount = 120
	progress.ReqSec = 20
	tracker.enrich(&progress, started.Add(11*time.Second))
	if progress.CurrentRate != 20 || progress.PeakRate != 20 {
		t.Fatalf("current/peak rates did not retain the engine rate: %+v", progress)
	}
}

func TestThroughputTrackerIgnoresScheduledWorkUntilItCompletes(t *testing.T) {
	started := time.Unix(1_700_000_000, 0).UTC()
	tracker := throughputTracker{}
	initial := Progress{StartedAt: started, ReqTotal: 3_000}
	tracker.enrich(&initial, started)

	queued := Progress{
		StartedAt: started, ReqCount: 2_001, RequestCount: 2_001,
		CompletedCount: 0, ReqTotal: 3_000, ReqSec: 0,
	}
	tracker.enrich(&queued, started.Add(time.Second))
	if queued.CurrentRate != 0 || queued.Average30s != 0 || queued.OverallAverage != 0 || queued.PeakRate != 0 {
		t.Fatalf("2,001 scheduled and zero completed produced false throughput: %+v", queued)
	}
	if queued.ETASeconds != nil {
		t.Fatalf("ETA = %v with zero completed throughput, want unknown", queued.ETASeconds)
	}

	completed := queued
	completed.CompletedCount = 1_000
	completed.ResponseCount = 990
	completed.ErrorCount = 10
	completed.ReqSec = 1_000
	tracker.enrich(&completed, started.Add(2*time.Second))
	if completed.CurrentRate != 1_000 || completed.Average30s <= 0 || completed.OverallAverage <= 0 || completed.PeakRate != 1_000 {
		t.Fatalf("completed work did not advance throughput: %+v", completed)
	}
}

func TestThroughputTrackerPreservesAMeasuredZeroThirtySecondRate(t *testing.T) {
	started := time.Unix(1_700_000_000, 0).UTC()
	tracker := throughputTracker{}
	initial := Progress{StartedAt: started, CompletedCount: 0, ReqTotal: 200}
	tracker.enrich(&initial, started)

	burst := Progress{StartedAt: started, CompletedCount: 100, ReqTotal: 200, ReqSec: 10}
	tracker.enrich(&burst, started.Add(10*time.Second))
	quiet := Progress{StartedAt: started, CompletedCount: 100, ReqTotal: 200, ReqSec: 0}
	// Leave a gap longer than the tracker's retention window. The last sample
	// before the window must remain as an anchor proving that no work completed.
	tracker.enrich(&quiet, started.Add(45*time.Second))

	if quiet.OverallAverage <= 0 {
		t.Fatalf("overall average = %v, want the lifetime burst represented", quiet.OverallAverage)
	}
	if quiet.Average30s != 0 {
		t.Fatalf("30-second average = %v, want measured zero after a quiet window", quiet.Average30s)
	}
	if quiet.ETASeconds != nil {
		t.Fatalf("ETA = %v, want unknown while the measured window has zero throughput", quiet.ETASeconds)
	}
}

func TestErrorDiagnosticsClassifyAndRetainRepresentativeSamples(t *testing.T) {
	cases := []struct {
		err      error
		category string
	}{
		{&net.DNSError{Err: "no such host", Name: "missing.example"}, "dns"},
		{errors.New("remote error: tls: bad certificate"), "tls"},
		{errors.New("QUIC handshake failed while dialing UDP"), "quic_handshake"},
		{context.DeadlineExceeded, "timeout"},
		{errors.New("read: connection reset by peer"), "connection_reset"},
	}
	diagnostics := make(map[string]*ErrorDiagnostic)
	for _, tc := range cases {
		if got := classifyRequestError(tc.err); got != tc.category {
			t.Errorf("classify %q = %q, want %q", tc.err, got, tc.category)
		}
		addDiagnostic(diagnostics, tc.category, tc.err.Error())
	}
	addDiagnostic(diagnostics, "rate_limiting", "HTTP 429 Too Many Requests")
	addDiagnostic(diagnostics, "persistence_backpressure", "SQLite queue full")

	got := cloneDiagnostics(diagnostics)
	if len(got) != 7 {
		t.Fatalf("diagnostic categories = %#v", got)
	}
	for _, diagnostic := range got {
		if diagnostic.Count != 1 || len(diagnostic.Samples) != 1 || diagnostic.Suggestion == "" {
			t.Errorf("incomplete diagnostic: %#v", diagnostic)
		}
	}
}

func TestStorePersistsPreflightAndErrorDiagnostics(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "web.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	session := testSession("telemetry-persistence")
	if err := store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	report := PreflightReport{State: PreflightComplete, RanAt: &now, HTTP3Advertised: true,
		HTTP3Negotiated: true, SelectedProtocol: "http3", SelectedStrategy: "HEAD",
		EffectiveConnections: 10, EffectiveStreams: 2560,
		HEADCheck: PreflightCheck{Attempted: true, Reliable: true, Requests: 3}}
	if err := store.UpdateSessionPreflight(context.Background(), session.ID, report); err != nil {
		t.Fatal(err)
	}
	progress := Progress{RequestCount: 12, ResponseCount: 10, ErrorCount: 2, CurrentRate: 3.5}
	diagnostics := []ErrorDiagnostic{{Category: "timeout", Count: 2,
		Samples: []string{"context deadline exceeded"}, Suggestion: diagnosticSuggestion("timeout")}}
	if err := store.UpdateProgressTelemetry(context.Background(), session.ID, progress, diagnostics, 7); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.GetSession(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PreflightReport.SelectedProtocol != "http3" || loaded.PreflightReport.EffectiveStreams != 2560 {
		t.Fatalf("preflight report was not persisted: %+v", loaded.PreflightReport)
	}
	if loaded.Progress.ResponseCount != 10 || len(loaded.ErrorDiagnostics) != 1 || loaded.ErrorDiagnostics[0].Count != 2 {
		t.Fatalf("telemetry was not persisted: progress=%+v errors=%+v", loaded.Progress, loaded.ErrorDiagnostics)
	}
}

func TestPreflightActionPersistsAndReturnsCompleteReport(t *testing.T) {
	server := testServer(t)
	wordlist := filepath.Join(t.TempDir(), "words.txt")
	if err := os.WriteFile(wordlist, []byte("admin\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	session, err := server.manager.Create(context.Background(), CreateSessionRequest{
		Target: "https://example.test/FUZZ", Wordlist: wordlist, Profile: "dir",
	})
	if err != nil {
		t.Fatal(err)
	}
	server.manager.buildJob = func(conf *ffuf.Config) (*engine.Job, error) {
		now := time.Now().UTC()
		conf.Http3Reason = "UDP/443 unavailable"
		conf.ResponseOptimizationStrategy = "HEAD"
		conf.ResponseOptimizationReason = "HEAD metadata was reliable"
		conf.ProtocolPreflight = ffuf.ProtocolPreflightReport{
			SchemaVersion: 1, RanAt: now, CompletedAt: now.Add(time.Millisecond),
			SelectedProtocol: "http2", DowngradeReason: conf.Http3Reason,
			HTTP3: ffuf.HTTP3PreflightReport{Advertised: true},
			RequestStrategy: ffuf.RequestStrategyPreflightReport{Selected: "HEAD", Reason: conf.ResponseOptimizationReason,
				HEAD: ffuf.PreflightProbeReport{Attempted: true, Successful: true, Reliable: true, Requests: 3}},
			EffectiveConcurrency: ffuf.EffectiveConcurrencyReport{Workers: 40, Connections: 1,
				StreamsPerConnection: 40, MaximumConcurrentStreams: 40},
		}
		return engine.NewJob(conf), nil
	}

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authenticatedRequest(http.MethodPost,
		"/api/v1/sessions/"+session.ID+"/preflight", nil))
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("preflight action = %d %s", recorder.Code, recorder.Body.String())
	}
	loaded, err := server.store.GetSession(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PreflightReport.State != PreflightComplete || !loaded.PreflightReport.HTTP3Advertised ||
		loaded.PreflightReport.SelectedStrategy != "HEAD" || loaded.PreflightReport.EffectiveStreams != 40 {
		t.Fatalf("unexpected preflight report: %+v", loaded.PreflightReport)
	}

	server.manager.buildJob = func(conf *ffuf.Config) (*engine.Job, error) {
		return engine.NewJob(conf), errors.New("forced HTTP/3 handshake failed")
	}
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authenticatedRequest(http.MethodPost,
		"/api/v1/sessions/"+session.ID+"/preflight", nil))
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("failed capability report action = %d %s", recorder.Code, recorder.Body.String())
	}
	loaded, err = server.store.GetSession(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PreflightReport.State != PreflightFailed || loaded.PreflightReport.Error == "" {
		t.Fatalf("probe failure was not returned as report data: %+v", loaded.PreflightReport)
	}
}
