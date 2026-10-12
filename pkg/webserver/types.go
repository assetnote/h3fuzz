package webserver

import (
	"encoding/json"
	"time"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

type SessionStatus string

const (
	StatusIdle      SessionStatus = "idle"
	StatusStarting  SessionStatus = "starting"
	StatusRunning   SessionStatus = "running"
	StatusPaused    SessionStatus = "paused"
	StatusStopping  SessionStatus = "stopping"
	StatusCompleted SessionStatus = "completed"
	StatusStopped   SessionStatus = "stopped"
	StatusFailed    SessionStatus = "failed"
)

const (
	RuntimeTransportHTTP1 = "http1"
	RuntimeTransportHTTP2 = "http2"
	RuntimeTransportHTTP3 = "http3"
)

type Session struct {
	ID       string             `json:"id"`
	Name     string             `json:"name"`
	Target   string             `json:"target"`
	Wordlist string             `json:"wordlist"`
	Profile  string             `json:"profile"`
	Status   SessionStatus      `json:"status"`
	Options  ffuf.ConfigOptions `json:"options"`
	// ResultFilters contains only filters added after execution began. Options
	// keeps the complete effective ffuf configuration, while this subset lets
	// stored findings be filtered retroactively without reapplying filters the
	// engine had already enforced before persistence.
	ResultFilters ffuf.FilterOptions `json:"-"`
	ExplicitFlags []string           `json:"explicit_flags"`
	Progress      Progress           `json:"progress"`
	ResultCount   int64              `json:"result_count"`
	Sequence      int64              `json:"sequence"`
	Error         string             `json:"error,omitempty"`
	CreatedAt     time.Time          `json:"created_at"`
	UpdatedAt     time.Time          `json:"updated_at"`
	StartedAt     *time.Time         `json:"started_at,omitempty"`
	CompletedAt   *time.Time         `json:"completed_at,omitempty"`
	// Runtime fields report the transport and response-minimisation strategy
	// actually selected after probing. They are separate from Options, which
	// preserves the operator's requested configuration.
	RuntimeTransport             string            `json:"runtime_transport,omitempty"`
	RuntimeTransportReason       string            `json:"runtime_transport_reason,omitempty"`
	ResponseOptimizationStrategy string            `json:"response_optimization_strategy,omitempty"`
	ResponseOptimizationReason   string            `json:"response_optimization_reason,omitempty"`
	PreflightReport              PreflightReport   `json:"preflight_report"`
	ErrorDiagnostics             []ErrorDiagnostic `json:"error_diagnostics"`
	Pinned                       bool              `json:"pinned"`
	Archived                     bool              `json:"archived"`
	GroupName                    string            `json:"group_name,omitempty"`
	ClonedFromID                 string            `json:"cloned_from_id,omitempty"`
}

type Progress struct {
	StartedAt               time.Time         `json:"started_at"`
	ReqCount                int               `json:"req_count"`
	RequestCount            int               `json:"request_count"`
	CompletedCount          int               `json:"completed_count"`
	ResponseCount           int               `json:"response_count"`
	ReqTotal                int               `json:"req_total"`
	ReqSec                  int64             `json:"req_sec"`
	CurrentRate             float64           `json:"current_rate"`
	Average30s              float64           `json:"average_30s"`
	OverallAverage          float64           `json:"overall_average"`
	PeakRate                float64           `json:"peak_rate"`
	ETASeconds              *int64            `json:"eta_seconds"`
	QueuePos                int               `json:"queue_pos"`
	QueueTotal              int               `json:"queue_total"`
	ErrorCount              int               `json:"error_count"`
	ResultsQueued           int               `json:"results_queued"`
	PersistenceBackpressure bool              `json:"persistence_backpressure"`
	LastBatchDurationMS     int64             `json:"last_batch_duration_ms"`
	ErrorDiagnostics        []ErrorDiagnostic `json:"error_diagnostics,omitempty"`
	StallReason             string            `json:"stall_reason,omitempty"`
	StalledForMS            int64             `json:"stalled_for_ms,omitempty"`
}

func progressFromFFUF(p ffuf.Progress) Progress {
	return Progress{StartedAt: p.StartedAt, ReqCount: p.ReqCount, RequestCount: p.ReqCount,
		CompletedCount: p.CompletedCount, ResponseCount: p.ResponseCount, ReqTotal: p.ReqTotal, ReqSec: p.ReqSec,
		QueuePos: p.QueuePos, QueueTotal: p.QueueTotal, ErrorCount: p.ErrorCount}
}

const (
	PreflightNotRun   = "not_run"
	PreflightRunning  = "running"
	PreflightComplete = "complete"
	PreflightFailed   = "failed"
)

type PreflightCheck struct {
	Attempted bool   `json:"attempted"`
	Reliable  bool   `json:"reliable"`
	Reason    string `json:"reason,omitempty"`
	Requests  int    `json:"requests"`
	ElapsedMS int64  `json:"elapsed_ms"`
}

type PreflightTimings struct {
	Advertisement int64 `json:"advertisement"`
	HTTP3         int64 `json:"http3"`
	Baseline      int64 `json:"baseline"`
	HEAD          int64 `json:"head"`
	Range         int64 `json:"range"`
	Total         int64 `json:"total"`
}

// PreflightReport is deliberately shaped for the web API rather than exposing
// runner internals. The flattened decision fields are stable UI contracts while
// Fingerprint keeps detailed QUIC evidence available in the report drawer.
type PreflightReport struct {
	State                string                `json:"state"`
	RanAt                *time.Time            `json:"ran_at,omitempty"`
	CompletedAt          *time.Time            `json:"completed_at,omitempty"`
	HTTP3Advertised      bool                  `json:"http3_advertised"`
	HTTP3Negotiated      bool                  `json:"http3_negotiated"`
	HTTP3Advertisement   string                `json:"http3_advertisement,omitempty"`
	AdvertisementCheck   PreflightCheck        `json:"advertisement_check"`
	NegotiationCheck     PreflightCheck        `json:"negotiation_check"`
	Fingerprint          ffuf.HTTP3Fingerprint `json:"fingerprint"`
	HEADCheck            PreflightCheck        `json:"head_check"`
	RangeCheck           PreflightCheck        `json:"range_check"`
	SelectedProtocol     string                `json:"selected_protocol"`
	SelectedStrategy     string                `json:"selected_strategy"`
	StrategyReason       string                `json:"strategy_reason,omitempty"`
	EffectiveWorkers     int                   `json:"effective_workers"`
	EffectiveConnections int                   `json:"effective_connections"`
	EffectiveStreams     int                   `json:"effective_streams"`
	StreamsPerConnection int                   `json:"streams_per_connection"`
	DowngradeReason      string                `json:"downgrade_reason,omitempty"`
	TimingsMS            PreflightTimings      `json:"timings_ms"`
	Error                string                `json:"error,omitempty"`
}

type ErrorDiagnostic struct {
	Category   string   `json:"category"`
	Count      int64    `json:"count"`
	Samples    []string `json:"samples"`
	Suggestion string   `json:"suggestion"`
}

type StoredResult struct {
	ID               int64               `json:"id"`
	SessionID        string              `json:"session_id"`
	Input            map[string][]byte   `json:"input"`
	Position         int                 `json:"position"`
	StatusCode       int64               `json:"status"`
	ContentLength    int64               `json:"length"`
	ContentWords     int64               `json:"words"`
	ContentLines     int64               `json:"lines"`
	ContentType      string              `json:"content_type"`
	RedirectLocation string              `json:"redirect_location"`
	URL              string              `json:"url"`
	DurationNS       int64               `json:"duration_ns"`
	ScraperData      map[string][]string `json:"scraper,omitempty"`
	Host             string              `json:"host"`
	// ResponseFingerprint is a SHA-256 digest of the response bytes that were
	// actually captured by the runner. It is intentionally empty for bodyless
	// responses. Explorer duplicate grouping must never infer equality from
	// status, size, word, and line counts alone.
	ResponseFingerprint string    `json:"response_fingerprint,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
	terminalSequence    int64
	terminalAt          time.Time
}

type ResultsPage struct {
	Results         []StoredResult   `json:"results"`
	NextCursor      int64            `json:"next_cursor"`
	HasMore         bool             `json:"has_more"`
	WindowTruncated bool             `json:"window_truncated,omitempty"`
	Total           int64            `json:"total"`
	StatusCounts    map[string]int64 `json:"status_counts"`
}

type Defaults struct {
	Wordlist      string             `json:"wordlist"`
	Profile       string             `json:"profile"`
	Options       ffuf.ConfigOptions `json:"options"`
	ExplicitFlags []string           `json:"explicit_flags"`
}

type Settings struct {
	SchemaVersion int       `json:"schema_version"`
	Defaults      Defaults  `json:"defaults"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type CreateSessionRequest struct {
	Name          string              `json:"name"`
	Target        string              `json:"target"`
	Wordlist      string              `json:"wordlist"`
	Profile       string              `json:"profile"`
	Options       *ffuf.ConfigOptions `json:"options,omitempty"`
	ExplicitFlags []string            `json:"explicit_flags,omitempty"`
	AutoStart     bool                `json:"auto_start"`
}

type PatchSessionRequest struct {
	Name          *string             `json:"name,omitempty"`
	Target        *string             `json:"target,omitempty"`
	Wordlist      *string             `json:"wordlist,omitempty"`
	Profile       *string             `json:"profile,omitempty"`
	Options       *ffuf.ConfigOptions `json:"options,omitempty"`
	ExplicitFlags *[]string           `json:"explicit_flags,omitempty"`
}

// AddSessionFiltersRequest adds result filters to a session. Empty fields are
// no-ops, which makes retries idempotent. Once a scan has started filters are
// deliberately additive: responses rejected by the engine were never retained
// and therefore cannot be restored by a later removal.
type AddSessionFiltersRequest struct {
	Mode   string `json:"mode,omitempty"`
	Lines  string `json:"lines,omitempty"`
	Regexp string `json:"regexp,omitempty"`
	Size   string `json:"size,omitempty"`
	Status string `json:"status,omitempty"`
	Time   string `json:"time,omitempty"`
	Words  string `json:"words,omitempty"`
}

type Event struct {
	Type      string      `json:"type"`
	SessionID string      `json:"session_id,omitempty"`
	Sequence  int64       `json:"sequence"`
	Data      interface{} `json:"data,omitempty"`
	At        time.Time   `json:"at"`
}

type APIError struct {
	Error APIErrorBody `json:"error"`
}

type APIErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type TerminalEntry struct {
	ID        int64     `json:"id"`
	SessionID string    `json:"session_id"`
	At        time.Time `json:"at"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`
}

type TerminalPage struct {
	Entries    []TerminalEntry `json:"entries"`
	NextCursor int64           `json:"next_cursor"`
	HasMore    bool            `json:"has_more"`
}

func cloneOptions(in ffuf.ConfigOptions) ffuf.ConfigOptions {
	var out ffuf.ConfigOptions
	b, _ := json.Marshal(in)
	_ = json.Unmarshal(b, &out)
	return out
}
