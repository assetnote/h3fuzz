package webserver

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

func TestWebRejectsHighRatePolicyOptionsAndImportedFlags(t *testing.T) {
	for _, name := range []string{
		"http3-high-rate", "http3-high-rate-acknowledge", "http3-target-rps", "http3-latency-ms",
	} {
		t.Run(name, func(t *testing.T) {
			if err := rejectWebProfilingFlags([]string{name}); err == nil || !strings.Contains(err.Error(), "command-line only") {
				t.Fatalf("imported flag rejection = %v", err)
			}
			if _, err := normalizeCreate(CreateSessionRequest{
				Target: "https://example.test/FUZZ", ExplicitFlags: []string{name},
			}, DefaultSettings()); err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("session creation imported flag rejection = %v", err)
			}
		})
	}

	opts := ffuf.NewConfigOptions()
	opts.HTTP.Http3HighRate = true
	opts.HTTP.Http3HighRateAcknowledge = true
	opts.HTTP.Http3TargetRPS = 125_000
	opts.HTTP.Http3LatencyMS = 2.5
	if err := sanitizeWebOptions(opts); err == nil || !strings.Contains(err.Error(), "high-rate") {
		t.Fatalf("stored high-rate options rejection = %v", err)
	}
	settings := DefaultSettings()
	settings.Defaults.Options = *opts
	if _, err := normalizeCreate(CreateSessionRequest{Target: "https://example.test/FUZZ"}, settings); err == nil || !strings.Contains(err.Error(), "high-rate") {
		t.Fatalf("stored default high-rate rejection = %v", err)
	}

	payload, err := json.Marshal(opts)
	if err != nil {
		t.Fatal(err)
	}
	var restored ffuf.ConfigOptions
	if err := json.Unmarshal(payload, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.HTTP.Http3HighRate || restored.HTTP.Http3HighRateAcknowledge || restored.HTTP.Http3LatencyMS != 0 {
		t.Fatalf("CLI-only high-rate values survived persisted JSON: %+v", restored.HTTP)
	}
}

func TestWebRawJSONRejectsHighRateFields(t *testing.T) {
	for _, field := range []string{"http3_high_rate", "http3_high_rate_acknowledge", "http3_target_rps", "http3_latency_ms"} {
		t.Run(field, func(t *testing.T) {
			req := httptest.NewRequest("PUT", "/api/v1/settings", bytes.NewBufferString(`{"http":{"`+field+`":true}}`))
			response := httptest.NewRecorder()
			var target ffuf.ConfigOptions
			if decodeJSON(response, req, &target) {
				t.Fatalf("raw CLI-only field %q was accepted", field)
			}
			if response.Code != 400 {
				t.Fatalf("raw CLI-only field %q status = %d, want 400", field, response.Code)
			}
		})
	}
}

func TestWebFlagRegistryOmitsHighRatePolicyButKeepsMaxWorkers(t *testing.T) {
	flags := ffuf.DescribeFlags(ffuf.NewConfigOptions())
	foundMaxWorkers := false
	for _, metadata := range flags {
		switch metadata.Name {
		case "http3-high-rate", "http3-high-rate-acknowledge", "http3-target-rps", "http3-latency-ms":
			t.Fatalf("CLI-only flag leaked into web registry: %+v", metadata)
		case "http3-max-workers":
			foundMaxWorkers = metadata.Available && metadata.JSONPath == "http.http3_max_workers"
		}
	}
	if !foundMaxWorkers {
		t.Fatal("general -http3-max-workers option missing from web registry")
	}
}

func TestWebLegacyOptionsWithoutCLIOnlyTargetDefaultRemainValid(t *testing.T) {
	opts := ffuf.NewConfigOptions()
	opts.HTTP.Http3TargetRPS = 0 // JSON/session records omit the CLI-only field.
	if err := sanitizeWebOptions(opts); err != nil {
		t.Fatalf("ordinary legacy web options rejected: %v", err)
	}
}
