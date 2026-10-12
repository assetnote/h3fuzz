package ffuf

import (
	"encoding/json"
	"testing"
)

func TestPreparedRequestHeadersTelemetryJSONSchema(t *testing.T) {
	report := RunTelemetry{SchemaVersion: 2}
	report.Transport.PreparedRequestHeaders = RunPreparedRequestHeadersTelemetry{
		Enabled:           true,
		AccountingEnabled: true,
		Attempts:          47,
		Hits:              11,
		Fallbacks:         36,
		FallbackReasons: RunPreparedRequestHeaderFallbackTelemetry{
			QLog:        1,
			Trace:       2,
			Method:      3,
			Body:        4,
			Compression: 5,
			URL:         6,
			Headers:     7,
			QPACK:       8,
		},
	}

	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	transport, ok := document["transport"].(map[string]any)
	if !ok {
		t.Fatalf("transport JSON = %#v", document["transport"])
	}
	prepared, ok := transport["prepared_request_headers"].(map[string]any)
	if !ok {
		t.Fatalf("prepared_request_headers JSON = %#v", transport["prepared_request_headers"])
	}
	reasons, ok := prepared["fallback_reasons"].(map[string]any)
	if !ok {
		t.Fatalf("fallback_reasons JSON = %#v", prepared["fallback_reasons"])
	}
	if got := reasons["compression"]; got != float64(5) {
		t.Fatalf("compression fallback JSON = %#v, want 5", got)
	}
	if got := prepared["attempts"]; got != float64(47) {
		t.Fatalf("attempts JSON = %#v, want 47", got)
	}
	if got := document["schema_version"]; got != float64(2) {
		t.Fatalf("schema version = %#v, want additive schema v2", got)
	}

	var roundTrip RunTelemetry
	if err := json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatal(err)
	}
	got := roundTrip.Transport.PreparedRequestHeaders
	if !got.Enabled || !got.AccountingEnabled || got.Attempts != 47 || got.Hits != 11 ||
		got.Fallbacks != 36 || got.FallbackReasons.Compression != 5 {
		t.Fatalf("prepared telemetry JSON round-trip = %+v", got)
	}
}
