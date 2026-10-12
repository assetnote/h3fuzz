package ffuf

import "time"

// HTTP3Fingerprint is the machine-readable result produced by
// -http3-fingerprint and by the automatic protocol probe. A successful QUIC
// handshake alone is not enough: Supported is set only after ALPN h3 was
// negotiated and a complete HTTP response was received.
type HTTP3Fingerprint struct {
	Target                     string `json:"target"`
	Supported                  bool   `json:"supported"`
	HighRateReady              bool   `json:"high_rate_ready"`
	Reason                     string `json:"reason,omitempty"`
	ALPN                       string `json:"alpn,omitempty"`
	QUICVersion                string `json:"quic_version,omitempty"`
	HTTPVersion                string `json:"http_version,omitempty"`
	StatusCode                 int    `json:"status_code,omitempty"`
	ConnectionReused           bool   `json:"connection_reused"`
	ConcurrentStreamsCompleted int    `json:"concurrent_streams_completed"`
	ConcurrentStreamsAttempted int    `json:"concurrent_streams_attempted"`
	QUICDatagrams              bool   `json:"quic_datagrams"`
	HTTP3Datagrams             bool   `json:"http3_datagrams"`
	RemoteAddr                 string `json:"remote_addr,omitempty"`
	ElapsedMillis              int64  `json:"elapsed_ms"`
}

// ResponseOptimizationReport describes the response-minimisation decision made
// before a scan. Strategy is one of "get", "head", "range", or "user-head".
// ProbeRequests are deliberately accounted separately from fuzzing inputs.
type ResponseOptimizationReport struct {
	Strategy      string `json:"strategy"`
	Reason        string `json:"reason"`
	ProbeRequests int    `json:"probe_requests"`
}

// PreflightProbeReport records one bounded capability check. Attempted is kept
// separate from Successful so callers can distinguish a negative result from a
// check that was unnecessary or disabled by configuration.
type PreflightProbeReport struct {
	Attempted     bool    `json:"attempted"`
	Successful    bool    `json:"successful"`
	Reliable      bool    `json:"reliable,omitempty"`
	Reason        string  `json:"reason,omitempty"`
	Requests      int     `json:"requests"`
	ElapsedMillis int64   `json:"elapsed_ms"`
	MeanLatencyMS float64 `json:"mean_latency_ms,omitempty"`
}

// HTTP3PreflightReport retains both discovery and direct negotiation evidence.
// Servers are often HTTP/3 capable without advertising Alt-Svc (or advertise it
// while UDP is blocked), so the two facts must not be collapsed into one flag.
type HTTP3PreflightReport struct {
	Advertised         bool                 `json:"advertised"`
	Advertisement      string               `json:"advertisement,omitempty"`
	AdvertisementProbe PreflightProbeReport `json:"advertisement_probe"`
	Negotiated         bool                 `json:"negotiated"`
	NegotiationProbe   PreflightProbeReport `json:"negotiation_probe"`
	Fingerprint        HTTP3Fingerprint     `json:"fingerprint"`
}

type RequestStrategyPreflightReport struct {
	Selected      string               `json:"selected"`
	Reason        string               `json:"reason"`
	ProbeRequests int                  `json:"probe_requests"`
	FullGET       PreflightProbeReport `json:"full_get"`
	HEAD          PreflightProbeReport `json:"head"`
	Range         PreflightProbeReport `json:"range"`
}

type EffectiveConcurrencyReport struct {
	Workers                  int `json:"workers"`
	Connections              int `json:"connections"`
	StreamsPerConnection     int `json:"streams_per_connection"`
	MaximumConcurrentStreams int `json:"maximum_concurrent_streams"`
}

type ProtocolPreflightTimings struct {
	AdvertisementMillis int64 `json:"advertisement_ms"`
	HTTP3Millis         int64 `json:"http3_ms"`
	BaselineMillis      int64 `json:"baseline_ms"`
	HEADMillis          int64 `json:"head_ms"`
	RangeMillis         int64 `json:"range_ms"`
	TotalMillis         int64 `json:"total_ms"`
}

// ProtocolPreflightReport is the complete, immutable decision record produced
// before request workers start. It is runtime-only configuration state and can
// be safely persisted by embedders such as the web console.
type ProtocolPreflightReport struct {
	SchemaVersion        int                            `json:"schema_version"`
	RanAt                time.Time                      `json:"ran_at"`
	CompletedAt          time.Time                      `json:"completed_at"`
	SelectedProtocol     string                         `json:"selected_protocol"`
	DowngradeReason      string                         `json:"downgrade_reason,omitempty"`
	HTTP3                HTTP3PreflightReport           `json:"http3"`
	RequestStrategy      RequestStrategyPreflightReport `json:"request_strategy"`
	EffectiveConcurrency EffectiveConcurrencyReport     `json:"effective_concurrency"`
	Timings              ProtocolPreflightTimings       `json:"timings"`
}
