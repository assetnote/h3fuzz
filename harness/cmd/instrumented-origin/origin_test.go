package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

const testRunID = "11111111-2222-4333-8444-555555555555"

func TestMarkedBenchmarkRequestHasExactCorrelationDimensions(t *testing.T) {
	t.Parallel()

	origin, err := NewOrigin(OriginConfig{
		CountersEnabled:              true,
		OriginInstanceID:             "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
		StartedAt:                    time.Date(2026, 10, 11, 1, 2, 3, 0, time.UTC),
		BuildSHA256:                  repeatHex("a"),
		ConfigSHA256:                 repeatHex("b"),
		ConfiguredMaxIncomingStreams: 256,
		EffectiveMaxIncomingStreams:  256,
	})
	if err != nil {
		t.Fatalf("NewOrigin: %v", err)
	}

	req := httptest.NewRequest(http.MethodHead, "https://origin.test/bench/word", nil)
	req.Proto = "HTTP/3.0"
	req.ProtoMajor = 3
	req.ProtoMinor = 0
	req.Header.Set(harnessRunHeader, testRunID)
	req.Header.Set(harnessRoleHeader, string(roleEngineRetry))
	recorder := httptest.NewRecorder()

	origin.Handler().ServeHTTP(recorder, req)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
	snapshot := origin.Snapshot()
	if len(snapshot.Counters) != 1 {
		t.Fatalf("counter rows = %d, want 1: %+v", len(snapshot.Counters), snapshot.Counters)
	}
	got := snapshot.Counters[0]
	if got.RunID == nil || *got.RunID != testRunID {
		t.Fatalf("run_id = %v, want %q", got.RunID, testRunID)
	}
	if got.Phase != phaseMain || got.Route != routeBench || got.Protocol != protocolH3 || got.Method != methodHEAD {
		t.Fatalf("dimensions = phase:%q route:%q protocol:%q method:%q", got.Phase, got.Route, got.Protocol, got.Method)
	}
	if got.Role == nil || *got.Role != string(roleEngineRetry) {
		t.Fatalf("role = %v, want %q", got.Role, roleEngineRetry)
	}
	if got.HandlerRequests != 1 {
		t.Fatalf("handler_requests = %d, want 1", got.HandlerRequests)
	}
	if got.H3Attempts != nil || got.H3RequestRejections != nil {
		t.Fatalf("unsupported transport counters must be null: %+v", got)
	}
}

func TestUnmarkedProbesCannotInflateMainWorkload(t *testing.T) {
	t.Parallel()

	origin := newTestOrigin(t, true)
	requests := []struct {
		path   string
		runID  string
		role   string
		status int
	}{
		{path: "/healthz", status: http.StatusOK},
		{path: "/bench/fingerprint", status: http.StatusNoContent},
		{path: "/bench/warmup", runID: testRunID, role: "not-a-role", status: http.StatusNoContent},
		{path: "/not-a-workload", runID: testRunID, role: string(roleInitial), status: http.StatusNotFound},
	}
	for _, item := range requests {
		req := httptest.NewRequest(http.MethodGet, "https://origin.test"+item.path, nil)
		req.Proto = "HTTP/3.0"
		req.ProtoMajor = 3
		if item.runID != "" {
			req.Header.Set(harnessRunHeader, item.runID)
		}
		if item.role != "" {
			req.Header.Set(harnessRoleHeader, item.role)
		}
		recorder := httptest.NewRecorder()
		origin.Handler().ServeHTTP(recorder, req)
		if recorder.Code != item.status {
			t.Fatalf("%s status = %d, want %d", item.path, recorder.Code, item.status)
		}
	}

	var unmarked uint64
	for _, row := range origin.Snapshot().Counters {
		if row.Phase != phaseUnmarked || row.RunID != nil || row.Role != nil {
			t.Fatalf("probe escaped unmarked ledger: %+v", row)
		}
		unmarked += row.HandlerRequests
	}
	if unmarked != uint64(len(requests)) {
		t.Fatalf("unmarked handler requests = %d, want %d", unmarked, len(requests))
	}
}

func TestCounterResetStartsANewStableEpoch(t *testing.T) {
	t.Parallel()

	origin := newTestOrigin(t, true)
	req := httptest.NewRequest(http.MethodGet, "https://origin.test/bench/one", nil)
	req.ProtoMajor = 3
	origin.Handler().ServeHTTP(httptest.NewRecorder(), req)
	before := origin.Snapshot()
	if before.Origin.CounterEpoch != 1 || len(before.Counters) != 1 {
		t.Fatalf("before reset = epoch %d, counters %+v", before.Origin.CounterEpoch, before.Counters)
	}

	if epoch := origin.ResetCounters(); epoch != 2 {
		t.Fatalf("reset epoch = %d, want 2", epoch)
	}
	after := origin.Snapshot()
	if after.Origin.OriginInstanceID != before.Origin.OriginInstanceID {
		t.Fatalf("reset changed origin instance: before %q after %q", before.Origin.OriginInstanceID, after.Origin.OriginInstanceID)
	}
	if after.Origin.CounterEpoch != 2 || len(after.Counters) != 0 {
		t.Fatalf("after reset = epoch %d, counters %+v", after.Origin.CounterEpoch, after.Counters)
	}

	req = httptest.NewRequest(http.MethodGet, "https://origin.test/bench/two", nil)
	req.ProtoMajor = 3
	origin.Handler().ServeHTTP(httptest.NewRecorder(), req)
	repopulated := origin.Snapshot()
	if repopulated.Origin.CounterEpoch != 2 || len(repopulated.Counters) != 1 || repopulated.Counters[0].HandlerRequests != 1 {
		t.Fatalf("repopulated snapshot = %+v", repopulated)
	}
}

func TestAdminSnapshotReturnsVersionedJSON(t *testing.T) {
	t.Parallel()

	origin := newTestOrigin(t, true)
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/admin/v1/snapshot", nil)
	recorder := httptest.NewRecorder()
	origin.AdminHandler().ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	var snapshot Snapshot
	if err := json.Unmarshal(recorder.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if snapshot.SchemaVersion != 1 || !validUUID(snapshot.SnapshotID) {
		t.Fatalf("snapshot identity = version %d ID %q", snapshot.SchemaVersion, snapshot.SnapshotID)
	}
	if snapshot.Origin.ConfiguredMaxIncomingStreams != 256 || snapshot.Origin.EffectiveMaxIncomingStreams != 256 {
		t.Fatalf("stream metadata = %+v", snapshot.Origin)
	}
}

func TestAdminResetAdvancesEpochWithoutChangingInstance(t *testing.T) {
	t.Parallel()

	origin := newTestOrigin(t, true)
	before := origin.Snapshot()
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/admin/v1/reset", nil)
	recorder := httptest.NewRecorder()
	origin.AdminHandler().ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var result struct {
		SchemaVersion    int    `json:"schema_version"`
		OriginInstanceID string `json:"origin_instance_id"`
		CounterEpoch     uint64 `json:"counter_epoch"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode reset result: %v", err)
	}
	if result.SchemaVersion != 1 || result.OriginInstanceID != before.Origin.OriginInstanceID || result.CounterEpoch != 2 {
		t.Fatalf("reset result = %+v", result)
	}
	if got := origin.Snapshot().Origin.CounterEpoch; got != 2 {
		t.Fatalf("snapshot epoch = %d, want 2", got)
	}
}

func TestAdminMetadataReportsConfiguredAndEffectiveStreamLimit(t *testing.T) {
	t.Parallel()

	origin := newTestOrigin(t, true)
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/admin/v1", nil)
	recorder := httptest.NewRecorder()
	origin.AdminHandler().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	var metadata struct {
		SchemaVersion int            `json:"schema_version"`
		Origin        OriginMetadata `json:"origin"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &metadata); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	if metadata.SchemaVersion != 1 || metadata.Origin.ConfiguredMaxIncomingStreams != 256 || metadata.Origin.EffectiveMaxIncomingStreams != 256 {
		t.Fatalf("metadata = %+v", metadata)
	}
}

func TestQUICConfigurationEnforcesReportedPeerStreamLimit(t *testing.T) {
	t.Parallel()

	origin := newTestOrigin(t, true)
	quicConfig := origin.QUICConfig()
	if quicConfig.MaxIncomingStreams != 256 {
		t.Fatalf("MaxIncomingStreams = %d, want 256", quicConfig.MaxIncomingStreams)
	}
	quicConfig.MaxIncomingStreams = 1
	if got := origin.QUICConfig().MaxIncomingStreams; got != 256 {
		t.Fatalf("caller mutation changed enforced limit to %d", got)
	}
	if got := origin.Snapshot().Origin.EffectiveMaxIncomingStreams; got != 256 {
		t.Fatalf("reported effective limit = %d, want 256", got)
	}
}

func TestNewOriginRejectsEffectiveStreamLimitAboveConfiguredValue(t *testing.T) {
	t.Parallel()

	_, err := NewOrigin(OriginConfig{
		CountersEnabled:              true,
		OriginInstanceID:             "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
		StartedAt:                    time.Date(2026, 10, 11, 1, 2, 3, 0, time.UTC),
		BuildSHA256:                  repeatHex("a"),
		ConfigSHA256:                 repeatHex("b"),
		ConfiguredMaxIncomingStreams: 64,
		EffectiveMaxIncomingStreams:  256,
		FilesRoot:                    ".",
	})
	if err == nil {
		t.Fatal("NewOrigin accepted an effective stream limit above the configured value")
	}
}

func TestBenchmarkRoutesMatchHarnessWorkloads(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "known.txt"), []byte("known\n"), 0o600); err != nil {
		t.Fatalf("write known fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "known-large.bin"), []byte("large-body"), 0o600); err != nil {
		t.Fatalf("write large fixture: %v", err)
	}
	origin := newTestOriginWithConfig(t, OriginConfig{CountersEnabled: true, FilesRoot: root})

	cases := []struct {
		path   string
		status int
		body   string
	}{
		{path: "/bench/value", status: http.StatusNoContent},
		{path: "/files/known.txt", status: http.StatusOK, body: "known\n"},
		{path: "/files/missing.txt", status: http.StatusNotFound, body: "404 page not found\n"},
		{path: "/large/anything", status: http.StatusOK, body: "large-body"},
	}
	for _, item := range cases {
		req := httptest.NewRequest(http.MethodGet, "https://origin.test"+item.path, nil)
		recorder := httptest.NewRecorder()
		origin.Handler().ServeHTTP(recorder, req)
		response := recorder.Result()
		body, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil {
			t.Fatalf("read %s response: %v", item.path, err)
		}
		if response.StatusCode != item.status || string(body) != item.body {
			t.Fatalf("%s = status %d body %q, want status %d body %q", item.path, response.StatusCode, body, item.status, item.body)
		}
	}
}

func TestSyntheticPreHandlerRejectionStaysOutOfHandlerLedger(t *testing.T) {
	t.Parallel()

	origin := newTestOrigin(t, true)
	err := origin.RecordH3TransportAttempt(H3TransportAttempt{
		RunID:    testRunID,
		Route:    routeBench,
		Method:   methodGET,
		Role:     string(roleInitial),
		Rejected: true,
	})
	if err != nil {
		t.Fatalf("RecordH3TransportAttempt: %v", err)
	}
	snapshot := origin.Snapshot()
	if len(snapshot.Counters) != 1 {
		t.Fatalf("counter rows = %d, want 1: %+v", len(snapshot.Counters), snapshot.Counters)
	}
	row := snapshot.Counters[0]
	if row.HandlerRequests != 0 {
		t.Fatalf("handler_requests = %d, want 0", row.HandlerRequests)
	}
	if row.H3Attempts == nil || *row.H3Attempts != 1 {
		t.Fatalf("h3_attempts = %v, want 1", row.H3Attempts)
	}
	if row.H3RequestRejections == nil || *row.H3RequestRejections != 1 {
		t.Fatalf("h3_request_rejections = %v, want 1", row.H3RequestRejections)
	}
}

func TestConcurrentHandlerIncrementsAreExact(t *testing.T) {
	t.Parallel()

	origin := newTestOrigin(t, true)
	const requestCount = 10_000
	start := make(chan struct{})
	var workers sync.WaitGroup
	workers.Add(requestCount)
	for range requestCount {
		go func() {
			defer workers.Done()
			<-start
			req := httptest.NewRequest(http.MethodGet, "https://origin.test/bench/concurrent", nil)
			req.ProtoMajor = 3
			req.Header.Set(harnessRunHeader, testRunID)
			req.Header.Set(harnessRoleHeader, string(roleInitial))
			origin.Handler().ServeHTTP(httptest.NewRecorder(), req)
		}()
	}
	close(start)
	workers.Wait()

	rows := origin.Snapshot().Counters
	if len(rows) != 1 || rows[0].HandlerRequests != requestCount {
		t.Fatalf("concurrent counters = %+v, want one row with %d requests", rows, requestCount)
	}
}

func TestCounterDisabledPathServesIdenticalResponseWithoutRows(t *testing.T) {
	t.Parallel()

	enabled := newTestOrigin(t, true)
	disabled := newTestOrigin(t, false)
	serve := func(origin *Origin) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "https://origin.test/bench/control", nil)
		req.Proto = "HTTP/3.0"
		req.ProtoMajor = 3
		req.Header.Set(harnessRunHeader, testRunID)
		req.Header.Set(harnessRoleHeader, string(roleInitial))
		recorder := httptest.NewRecorder()
		origin.Handler().ServeHTTP(recorder, req)
		return recorder
	}
	enabledResponse := serve(enabled)
	disabledResponse := serve(disabled)
	if enabledResponse.Code != disabledResponse.Code || enabledResponse.Body.String() != disabledResponse.Body.String() {
		t.Fatalf("enabled response = (%d, %q), disabled response = (%d, %q)", enabledResponse.Code, enabledResponse.Body.String(), disabledResponse.Code, disabledResponse.Body.String())
	}
	if err := disabled.RecordH3TransportAttempt(H3TransportAttempt{
		RunID: testRunID, Route: routeBench, Method: methodGET, Role: string(roleInitial),
	}); err != nil {
		t.Fatalf("counter-disabled transport hook: %v", err)
	}
	if rows := disabled.Snapshot().Counters; len(rows) != 0 {
		t.Fatalf("counter-disabled rows = %+v, want none", rows)
	}
}

func TestOptimizationFallbackCreatesTwoDistinctMarkedRows(t *testing.T) {
	t.Parallel()

	origin := newTestOrigin(t, true)
	attempts := []struct {
		method string
		role   requestRole
	}{
		{method: http.MethodHead, role: roleInitial},
		{method: http.MethodGet, role: roleOptimizationFallback},
	}
	for _, attempt := range attempts {
		req := httptest.NewRequest(attempt.method, "https://origin.test/bench/recovery", nil)
		req.ProtoMajor = 3
		req.Header.Set(harnessRunHeader, testRunID)
		req.Header.Set(harnessRoleHeader, string(attempt.role))
		origin.Handler().ServeHTTP(httptest.NewRecorder(), req)
	}

	rows := origin.Snapshot().Counters
	if len(rows) != 2 {
		t.Fatalf("counter rows = %+v, want two", rows)
	}
	got := map[string]uint64{}
	for _, row := range rows {
		if row.Role == nil {
			t.Fatalf("marked row has null role: %+v", row)
		}
		got[row.Method+":"+*row.Role] += row.HandlerRequests
	}
	if got[methodHEAD+":"+string(roleInitial)] != 1 || got[methodGET+":"+string(roleOptimizationFallback)] != 1 {
		t.Fatalf("attempt rows = %+v", got)
	}
}

func TestMarkedCounterSteadyStateDoesNotAllocate(t *testing.T) {
	origin := newTestOrigin(t, true)
	req := httptest.NewRequest(http.MethodGet, "https://origin.test/bench/allocation", nil)
	req.ProtoMajor = 3
	req.Header.Set(harnessRunHeader, testRunID)
	req.Header.Set(harnessRoleHeader, string(roleInitial))
	writer := &discardResponseWriter{header: make(http.Header)}
	origin.Handler().ServeHTTP(writer, req) // Create the counter row and header keys.

	if allocations := testing.AllocsPerRun(1_000, func() {
		origin.Handler().ServeHTTP(writer, req)
	}); allocations != 0 {
		t.Fatalf("steady-state allocations per marked request = %.2f, want 0", allocations)
	}
}

type discardResponseWriter struct {
	header http.Header
}

func (w *discardResponseWriter) Header() http.Header              { return w.header }
func (*discardResponseWriter) Write(contents []byte) (int, error) { return len(contents), nil }
func (*discardResponseWriter) WriteHeader(int)                    {}

func BenchmarkOriginBenchRoute(b *testing.B) {
	for _, countersEnabled := range []bool{false, true} {
		name := "counters_disabled"
		if countersEnabled {
			name = "counters_enabled"
		}
		b.Run(name, func(b *testing.B) {
			origin := newBenchmarkOrigin(b, countersEnabled)
			req := httptest.NewRequest(http.MethodGet, "https://origin.test/bench/benchmark", nil)
			req.ProtoMajor = 3
			req.Header.Set(harnessRunHeader, testRunID)
			req.Header.Set(harnessRoleHeader, string(roleInitial))
			writer := &discardResponseWriter{header: make(http.Header)}
			origin.Handler().ServeHTTP(writer, req)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				origin.Handler().ServeHTTP(writer, req)
			}
		})
	}
}

func newBenchmarkOrigin(b *testing.B, countersEnabled bool) *Origin {
	b.Helper()
	origin, err := NewOrigin(OriginConfig{
		CountersEnabled:              countersEnabled,
		OriginInstanceID:             "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
		StartedAt:                    time.Unix(1, 0),
		BuildSHA256:                  repeatHex("a"),
		ConfigSHA256:                 repeatHex("b"),
		ConfiguredMaxIncomingStreams: 256,
		EffectiveMaxIncomingStreams:  256,
		FilesRoot:                    ".",
	})
	if err != nil {
		b.Fatalf("NewOrigin: %v", err)
	}
	return origin
}

func newTestOrigin(t *testing.T, countersEnabled bool) *Origin {
	t.Helper()
	return newTestOriginWithConfig(t, OriginConfig{CountersEnabled: countersEnabled})
}

func newTestOriginWithConfig(t *testing.T, config OriginConfig) *Origin {
	t.Helper()
	config.OriginInstanceID = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	config.StartedAt = time.Date(2026, 10, 11, 1, 2, 3, 0, time.UTC)
	config.BuildSHA256 = repeatHex("a")
	config.ConfigSHA256 = repeatHex("b")
	config.ConfiguredMaxIncomingStreams = 256
	config.EffectiveMaxIncomingStreams = 256
	origin, err := NewOrigin(config)
	if err != nil {
		t.Fatalf("NewOrigin: %v", err)
	}
	return origin
}

func repeatHex(digit string) string {
	value := ""
	for range 64 {
		value += digit
	}
	return value
}
