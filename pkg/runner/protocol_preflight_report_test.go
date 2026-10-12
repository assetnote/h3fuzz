package runner

import (
	"testing"
	"time"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

func TestAdvertisesHTTP3ParsesAltSvcProtocolTokens(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{`h3=":443"; ma=86400`, true},
		{`h2=":443", h3-29=":443"; ma=60`, true},
		{`h2=":443"`, false},
		{"", false},
	} {
		if got := advertisesHTTP3(tc.value); got != tc.want {
			t.Errorf("advertisesHTTP3(%q) = %v, want %v", tc.value, got, tc.want)
		}
	}
}

func TestFinalizePreflightReportRecordsDowngradeAndEffectiveConcurrency(t *testing.T) {
	conf := &ffuf.Config{
		Threads: 40, Http2: true, Http2Selected: true, Http3Reason: "QUIC handshake timed out",
		ResponseOptimizationStrategy: "HEAD", ResponseOptimizationReason: "HEAD was reliable",
	}
	runner := &SimpleRunner{config: conf}
	report := ffuf.ProtocolPreflightReport{
		SchemaVersion: 1,
		RequestStrategy: ffuf.RequestStrategyPreflightReport{
			Selected: "HEAD", Reason: "HEAD was reliable",
		},
	}
	started := time.Now().Add(-25 * time.Millisecond)
	runner.finalizePreflightReport(&report, started)

	if report.SelectedProtocol != "http2" || report.DowngradeReason != conf.Http3Reason {
		t.Fatalf("protocol decision = %+v", report)
	}
	if report.EffectiveConcurrency.Workers != 40 || report.EffectiveConcurrency.Connections != 1 ||
		report.EffectiveConcurrency.MaximumConcurrentStreams != 40 {
		t.Fatalf("effective concurrency = %+v", report.EffectiveConcurrency)
	}
	if report.Timings.TotalMillis <= 0 || conf.ProtocolPreflight.SelectedProtocol != "http2" {
		t.Fatalf("report was not completed/published: %+v", report)
	}
}

func TestMeanProbeLatencyMSPreservesSubMillisecondQualificationSamples(t *testing.T) {
	if got := meanProbeLatencyMS(750*time.Microsecond, 3); got != 0.25 {
		t.Fatalf("meanProbeLatencyMS = %v, want 0.25", got)
	}
	if got := meanProbeLatencyMS(time.Second, 0); got != 0 {
		t.Fatalf("zero-sample mean = %v, want 0", got)
	}
}
