package ffuf

import (
	"strings"
	"testing"
)

func TestFinalizeHighRatePolicyUsesExplicitLatencyWithCeilingAndHeadroom(t *testing.T) {
	conf := &Config{
		Threads:             40,
		Http3Connections:    2,
		Http3MaxConnections: 64,
		Http3Streams:        128,
		Http3MaxWorkers:     300,
		HighRatePolicy: &HighRatePolicyReport{
			Enabled: true, TargetRPS: 100_000, LatencyMS: 2.001,
			LatencySource: "explicit", Headroom: 1.25,
			ConnectionMode: "fixed", ConnectionCapacity: 2,
			StreamsPerConnection: 128, HTTP3MaxWorkers: 300,
			HardWorkerLimit: MaxHTTP3Workers,
		},
	}

	if err := FinalizeHighRatePolicy(conf, 4_000_000); err != nil {
		t.Fatalf("FinalizeHighRatePolicy: %v", err)
	}
	report := conf.HighRatePolicy
	if report.DesiredWorkers != 251 || report.StreamCapacity != 256 ||
		report.EffectiveWorkers != 251 || report.ExecutionWorkers != 256 || !report.Provisioned {
		t.Fatalf("unexpected sizing report: %+v", report)
	}
	if !report.InputDurationHeadroom || report.RecommendedMinimumInputs != 3_600_000 {
		t.Fatalf("unexpected input-duration report: %+v", report)
	}
}

func TestFinalizeHighRatePolicyUsesLargestSuccessfulPreflightMean(t *testing.T) {
	conf := &Config{
		Threads: 512, Http3Connections: 0, Http3MaxConnections: 4,
		Http3Streams: 128, Http3MaxWorkers: 1_000,
		HighRatePolicy: &HighRatePolicyReport{
			Enabled: true, TargetRPS: 100_000, LatencySource: "measured_preflight", Headroom: 1.25,
			ConnectionMode: "adaptive", ConnectionCapacity: 4,
			StreamsPerConnection: 128, HTTP3MaxWorkers: 1_000, HardWorkerLimit: MaxHTTP3Workers,
		},
		ProtocolPreflight: ProtocolPreflightReport{RequestStrategy: RequestStrategyPreflightReport{
			Selected: "HEAD",
			FullGET:  PreflightProbeReport{Attempted: true, Successful: true, MeanLatencyMS: 1.2},
			HEAD:     PreflightProbeReport{Attempted: true, Successful: true, MeanLatencyMS: 2.5},
		}},
	}

	if err := FinalizeHighRatePolicy(conf, 4_000_000); err != nil {
		t.Fatalf("FinalizeHighRatePolicy: %v", err)
	}
	if conf.HighRatePolicy.LatencyMS != 2.5 || conf.HighRatePolicy.DesiredWorkers != 313 {
		t.Fatalf("measured sizing = %+v, want latency 2.5ms and 313 workers", conf.HighRatePolicy)
	}
}

func TestFinalizeHighRatePolicyExplainsInsufficientBounds(t *testing.T) {
	conf := &Config{
		Threads: 64, Http3Connections: 1, Http3MaxConnections: 64,
		Http3Streams: 64, Http3MaxWorkers: MaxHTTP3Workers,
		HighRatePolicy: &HighRatePolicyReport{
			Enabled: true, TargetRPS: 100_000, LatencyMS: 1, LatencySource: "explicit", Headroom: 1.25,
			ConnectionMode: "fixed", ConnectionCapacity: 1,
			StreamsPerConnection: 64, HTTP3MaxWorkers: MaxHTTP3Workers, HardWorkerLimit: MaxHTTP3Workers,
		},
	}

	err := FinalizeHighRatePolicy(conf, 4_000_000)
	if err == nil || !strings.Contains(err.Error(), "desired_workers=125") ||
		!strings.Contains(err.Error(), "connection_capacity=1") || !strings.Contains(err.Error(), "stream_capacity=64") {
		t.Fatalf("insufficient-capacity error = %v", err)
	}
	if conf.HighRatePolicy.Provisioned {
		t.Fatalf("failed sizing was incorrectly marked provisioned: %+v", conf.HighRatePolicy)
	}
}

func TestFinalizeHighRatePolicyCapsExecutionByRemainingInputsAndOnlyWarnsDuration(t *testing.T) {
	conf := &Config{
		Threads: 2_048, Http3Connections: 8, Http3MaxConnections: 64,
		Http3Streams: 256, Http3MaxWorkers: MaxHTTP3Workers,
		HighRatePolicy: &HighRatePolicyReport{
			Enabled: true, TargetRPS: 100_000, LatencyMS: 1, LatencySource: "explicit", Headroom: 1.25,
			ConnectionMode: "fixed", ConnectionCapacity: 8,
			StreamsPerConnection: 256, HTTP3MaxWorkers: MaxHTTP3Workers, HardWorkerLimit: MaxHTTP3Workers,
		},
	}
	if err := FinalizeHighRatePolicy(conf, 100); err != nil {
		t.Fatalf("short input should warn, not fail: %v", err)
	}
	if conf.Threads != 100 || conf.HighRatePolicy.ExecutionWorkers != 100 || conf.HighRatePolicy.InputDurationHeadroom {
		t.Fatalf("remaining-input cap/report = threads %d policy %+v", conf.Threads, conf.HighRatePolicy)
	}
}

func TestFinalizeHighRatePolicyEnforcesHardWorkerMaximum(t *testing.T) {
	base := func(latency float64) *Config {
		return &Config{
			Threads: MaxHTTP3Workers, Http3Connections: 0, Http3MaxConnections: 64,
			Http3Streams: 4096, Http3MaxWorkers: MaxHTTP3Workers,
			HighRatePolicy: &HighRatePolicyReport{
				Enabled: true, TargetRPS: 100_000, LatencyMS: latency, LatencySource: "explicit", Headroom: 1.25,
				ConnectionMode: "adaptive", ConnectionCapacity: 64,
				StreamsPerConnection: 4096, HTTP3MaxWorkers: MaxHTTP3Workers, HardWorkerLimit: MaxHTTP3Workers,
			},
		}
	}
	if err := FinalizeHighRatePolicy(base(524.288), 4_000_000); err != nil {
		t.Fatalf("exact hard maximum rejected: %v", err)
	}
	if err := FinalizeHighRatePolicy(base(524.296), 4_000_000); err == nil || !strings.Contains(err.Error(), "hard_worker_limit=65536") {
		t.Fatalf("above-hard-maximum error = %v", err)
	}
}

func TestFinalizeHighRatePolicyRequiresMeasuredQualificationLatency(t *testing.T) {
	conf := &Config{
		Threads: 256, Http3Connections: 1, Http3MaxConnections: 64,
		Http3Streams: 256, Http3MaxWorkers: MaxHTTP3Workers,
		HighRatePolicy: &HighRatePolicyReport{
			Enabled: true, TargetRPS: 100_000, LatencySource: "measured_preflight", Headroom: 1.25,
			ConnectionMode: "fixed", ConnectionCapacity: 1,
			StreamsPerConnection: 256, HTTP3MaxWorkers: MaxHTTP3Workers, HardWorkerLimit: MaxHTTP3Workers,
		},
	}
	if err := FinalizeHighRatePolicy(conf, 4_000_000); err == nil || !strings.Contains(err.Error(), "full-GET preflight latency") {
		t.Fatalf("missing measured latency error = %v", err)
	}
}
