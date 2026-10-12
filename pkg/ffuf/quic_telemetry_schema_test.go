package ffuf

import (
	"encoding/json"
	"testing"
)

func TestQUICRunTelemetrySerializesUnavailableMetricsAsNull(t *testing.T) {
	report := RunTelemetry{
		SchemaVersion: 2,
		Transport: RunTransportTelemetry{
			ObservedPreHandlerApplicationFailures: 3,
			InternalAttemptsUnavailableReason:     "http3_not_selected",
			InternalRetriesUnavailableReason:      "http3_not_selected",
		},
		QUIC: RunQUICDiagnosticsTelemetry{
			LifecycleEnabled: false,
			Unavailable: map[string]string{
				"packets_sent":  "quic_telemetry_disabled",
				"latest_rtt_ns": "quic_telemetry_disabled",
			},
		},
		Drain: RunDrainTelemetry{TrackedConnectionGenerations: 2},
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	transport := got["transport"].(map[string]any)
	if transport["internal_attempts"] != nil || transport["internal_retries"] != nil {
		t.Fatalf("unavailable internal metrics must be null: %s", encoded)
	}
	if transport["observed_pre_handler_application_failures"] != float64(3) {
		t.Fatalf("pre-handler failures missing: %s", encoded)
	}
	quic := got["quic"].(map[string]any)
	if quic["packets_sent"] != nil || quic["latest_rtt_ns"] != nil {
		t.Fatalf("unavailable QUIC metrics must be null: %s", encoded)
	}
	drain := got["drain"].(map[string]any)
	if drain["tracked_connection_generations"] != float64(2) {
		t.Fatalf("tracked generations missing: %s", encoded)
	}
}

func TestQUICRunTelemetryKeepsBoundedSlotGenerationIdentity(t *testing.T) {
	latest := uint64(4)
	report := RunQUICDiagnosticsTelemetry{
		LifecycleEnabled: true,
		Slots: []RunQUICSlotTelemetry{{
			SlotID:                     7,
			GenerationsStarted:         4,
			LatestGeneration:           latest,
			LatestAttributedGeneration: latest,
			LiveGenerations:            1,
		}},
	}
	if len(report.Slots) != 1 || report.Slots[0].LatestGeneration != 4 {
		t.Fatalf("slot generation identity = %+v", report.Slots)
	}
}
