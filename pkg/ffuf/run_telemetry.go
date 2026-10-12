package ffuf

// RunTelemetry is an opt-in, machine-readable account of a completed run. It
// deliberately keeps logical fuzz inputs separate from lower-level HTTP/3
// transport attempts, which can also include bounded startup probes and retries.
type RunTelemetry struct {
	SchemaVersion   int                         `json:"schema_version"`
	RunID           *string                     `json:"run_id"`
	EligibleMode    string                      `json:"eligible_mode"`
	Protocol        RunProtocolTelemetry        `json:"protocol"`
	RequestStrategy RunRequestStrategyTelemetry `json:"request_strategy"`
	Responses       RunResponseTelemetry        `json:"responses"`
	Workers         RunWorkerCapacityTelemetry  `json:"workers"`
	LogicalLedger   RunLogicalLedgerTelemetry   `json:"logical_ledger"`
	Transport       RunTransportTelemetry       `json:"transport"`
	QUIC            RunQUICDiagnosticsTelemetry `json:"quic"`
	Eligibility     RunEligibilityTelemetry     `json:"eligibility"`
	Drain           RunDrainTelemetry           `json:"drain"`
	HighRatePolicy  *HighRatePolicyReport       `json:"high_rate_policy,omitempty"`
}

// RunWorkerCapacityTelemetry reconciles the pool's proposal state with the
// engine supervisor's actual worker lifecycle.
type RunWorkerCapacityTelemetry struct {
	ExecutionGeneration              uint64 `json:"execution_generation"`
	ProposalEpoch                    uint64 `json:"proposal_epoch"`
	AcknowledgementGeneration        uint64 `json:"acknowledgement_generation"`
	AcknowledgementEpoch             uint64 `json:"acknowledgement_epoch"`
	StaleProposals                   uint64 `json:"stale_proposals"`
	StaleAcknowledgements            uint64 `json:"stale_acknowledgements"`
	LateAfterTimeoutAcknowledgements uint64 `json:"late_after_timeout_acknowledgements"`
	RepublishedProposals             uint64 `json:"republished_proposals"`
	TimedOutProposals                uint64 `json:"timed_out_proposals"`
	SupersededProposals              uint64 `json:"superseded_proposals"`
	MissingListenerProposals         uint64 `json:"missing_listener_proposals"`
	ProposedWorkers                  int    `json:"proposed_workers"`
	AcknowledgedLiveWorkers          int    `json:"acknowledged_live_workers"`
	DesiredWorkers                   int    `json:"desired_workers"`
	LiveWorkers                      int    `json:"live_workers"`
	RetiringWorkers                  int    `json:"retiring_workers"`
	PeakWorkers                      int    `json:"peak_workers"`
	PeakMainInFlight                 int64  `json:"peak_main_in_flight"`
	RemainingInputCap                int    `json:"remaining_input_cap"`
	SupervisorState                  string `json:"supervisor_state"`
}

type RunProtocolTelemetry struct {
	Selected                string `json:"selected"`
	SelectionReason         string `json:"selection_reason,omitempty"`
	HTTP3Selected           bool   `json:"http3_selected"`
	HTTP3InitialConnections int    `json:"http3_initial_connections,omitempty"`
	HTTP3FinalConnections   int    `json:"http3_final_connections,omitempty"`
	HTTP3OpenedConnections  int    `json:"http3_opened_connections,omitempty"`
}

type RunRequestStrategyTelemetry struct {
	Method                       string `json:"method"`
	Range                        string `json:"range,omitempty"`
	AutomaticOptimizationEnabled bool   `json:"automatic_optimization_enabled"`
	SelectedOptimization         string `json:"selected_optimization"`
	SelectionReason              string `json:"selection_reason,omitempty"`
	OptimizedAttempts            uint64 `json:"optimized_attempts"`
	OptimizedSuccesses           uint64 `json:"optimized_successes"`
	FullGETFallbacks             uint64 `json:"full_get_fallbacks"`
	FallbackTransportErrors      uint64 `json:"fallback_transport_errors"`
	FallbackUntrustworthy        uint64 `json:"fallback_untrustworthy_responses"`
}

type RunResponseTelemetry struct {
	MainRequestSpanNS          *uint64                      `json:"main_request_span_ns"`
	ScheduledLogicalInputs     uint64                       `json:"scheduled_logical_inputs"`
	TerminalLogicalOutcomes    uint64                       `json:"terminal_logical_outcomes"`
	SuccessfulLogicalResponses uint64                       `json:"successful_logical_responses"`
	TerminalLogicalErrors      uint64                       `json:"terminal_logical_errors"`
	Latency                    *RunResponseLatencyTelemetry `json:"latency,omitempty"`

	// Schema-v1 fields remain decodable so in-process readers can consume old
	// artifacts. Version-2 writers leave them absent.
	Scheduled          uint64 `json:"scheduled,omitempty"`
	CompletedResponses uint64 `json:"completed_responses,omitempty"`
	Errors             uint64 `json:"errors,omitempty"`
	Retries            uint64 `json:"retries,omitempty"`
}

// RunResponseLatencyTelemetry is a bounded final-attempt time-to-first-byte
// summary for successful main logical responses. P99UpperBoundNS is the upper
// edge of the histogram bucket containing the nearest-rank p99 sample, so
// consumers can use it as a conservative bound without mistaking QUIC RTT for
// request latency.
type RunResponseLatencyTelemetry struct {
	SchemaVersion      int    `json:"schema_version"`
	Source             string `json:"source"`
	Scope              string `json:"scope"`
	Clock              string `json:"clock"`
	Unit               string `json:"unit"`
	Method             string `json:"method"`
	SampleCount        uint64 `json:"sample_count"`
	P99UpperBoundNS    uint64 `json:"p99_upper_bound_ns"`
	MaximumTrackableNS uint64 `json:"maximum_trackable_ns"`
	OverflowCount      uint64 `json:"overflow_count"`
}

type RunLogicalLedgerTelemetry struct {
	ExpectedExpandedInputs       *uint64  `json:"expected_expanded_inputs"`
	AssignedIDs                  uint64   `json:"assigned_ids"`
	ScheduledTransitions         uint64   `json:"scheduled_transitions"`
	SuccessTerminalStates        uint64   `json:"success_terminal_states"`
	ErrorTerminalStates          uint64   `json:"error_terminal_states"`
	PendingScheduledStates       uint64   `json:"pending_scheduled_states"`
	IDGaps                       uint64   `json:"id_gaps"`
	DuplicateScheduleTransitions uint64   `json:"duplicate_schedule_transitions"`
	DuplicateTerminalTransitions uint64   `json:"duplicate_terminal_transitions"`
	InvalidTransitions           uint64   `json:"invalid_transitions"`
	OutOfRangeIDs                uint64   `json:"out_of_range_ids"`
	AnomalySampleIDs             []uint64 `json:"anomaly_sample_ids"`
}

type RunTransportTelemetry struct {
	CompletedResponses uint64 `json:"completed_responses"`
	Errors             uint64 `json:"errors"`
	InFlight           int64  `json:"in_flight"`

	ApplicationAttempts                   uint64                             `json:"application_attempts"`
	CompletedAttempts                     uint64                             `json:"completed_attempts"`
	SurfacedAttemptErrors                 uint64                             `json:"surfaced_attempt_errors"`
	ApplicationRetries                    uint64                             `json:"application_retries"`
	ObservedPreHandlerApplicationFailures uint64                             `json:"observed_pre_handler_application_failures"`
	InternalAttempts                      *uint64                            `json:"internal_attempts"`
	InternalRetries                       *uint64                            `json:"internal_retries"`
	InternalAttemptsUnavailableReason     string                             `json:"internal_attempts_unavailable_reason,omitempty"`
	InternalRetriesUnavailableReason      string                             `json:"internal_retries_unavailable_reason,omitempty"`
	OptimizedAttempts                     uint64                             `json:"optimized_attempts"`
	FullGETRecoveries                     uint64                             `json:"full_get_recoveries"`
	AttemptRoles                          RunTransportRoleTelemetry          `json:"attempt_roles"`
	Methods                               RunTransportMethodTelemetry        `json:"methods"`
	PreparedRequestHeaders                RunPreparedRequestHeadersTelemetry `json:"prepared_request_headers"`
}

// RunPreparedRequestHeadersTelemetry accounts for quic-go's explicitly
// enabled prepared request-header writer. Fallback reasons are mutually
// exclusive. Attempts must equal Hits+Fallbacks, and Fallbacks must equal the
// sum of FallbackReasons.
type RunPreparedRequestHeadersTelemetry struct {
	Enabled           bool                                      `json:"enabled"`
	AccountingEnabled bool                                      `json:"accounting_enabled"`
	Attempts          uint64                                    `json:"attempts"`
	Hits              uint64                                    `json:"hits"`
	Fallbacks         uint64                                    `json:"fallbacks"`
	FallbackReasons   RunPreparedRequestHeaderFallbackTelemetry `json:"fallback_reasons"`
}

type RunPreparedRequestHeaderFallbackTelemetry struct {
	QLog        uint64 `json:"qlog"`
	Trace       uint64 `json:"trace"`
	Method      uint64 `json:"method"`
	Body        uint64 `json:"body"`
	Compression uint64 `json:"compression"`
	URL         uint64 `json:"url"`
	Headers     uint64 `json:"headers"`
	QPACK       uint64 `json:"qpack"`
}

type RunTransportAttemptRole string

const (
	RunTransportRoleInitial                         RunTransportAttemptRole = "initial"
	RunTransportRoleOptimizationFallback            RunTransportAttemptRole = "optimization_fallback"
	RunTransportRoleEngineRetry                     RunTransportAttemptRole = "engine_retry"
	RunTransportRoleEngineRetryOptimizationFallback RunTransportAttemptRole = "engine_retry_optimization_fallback"
)

type RunTransportAttempt struct {
	Role          RunTransportAttemptRole
	Method        string
	Optimized     bool
	Completed     bool
	SurfacedError bool
}

type RunTransportAttemptRecorder interface {
	RecordRunTransportAttempt(RunTransportAttempt)
}

// RunTransportAttemptLifecycleRecorder is an optional, narrower hot-path hook
// used to measure actual simultaneous main-work transport calls. Preflight,
// postflight, fingerprint, qualification, and warmup traffic never receives it.
type RunTransportAttemptLifecycleRecorder interface {
	BeginRunTransportAttempt()
	EndRunTransportAttempt()
}

type RunTransportRoleTelemetry struct {
	Initial                         uint64 `json:"initial"`
	OptimizationFallback            uint64 `json:"optimization_fallback"`
	EngineRetry                     uint64 `json:"engine_retry"`
	EngineRetryOptimizationFallback uint64 `json:"engine_retry_optimization_fallback"`
}

type RunTransportMethodTelemetry struct {
	GET   uint64 `json:"GET"`
	HEAD  uint64 `json:"HEAD"`
	Other uint64 `json:"other"`
}

// RunQUICDiagnosticsTelemetry contains bounded, opt-in QUIC transport
// observations. Packet and recovery metrics are pointers because a missing
// dependency signal is different from an observed zero.
type RunQUICDiagnosticsTelemetry struct {
	LifecycleEnabled bool `json:"lifecycle_enabled"`
	AggregateEnabled bool `json:"aggregate_enabled"`
	QLogEnabled      bool `json:"qlog_enabled"`

	GenerationsStarted uint64 `json:"generations_started"`
	ConnectionsOpened  uint64 `json:"connections_opened"`
	ConnectionsClosed  uint64 `json:"connections_closed"`
	DialAttempts       uint64 `json:"dial_attempts"`
	DialSuccesses      uint64 `json:"dial_successes"`
	DialFailures       uint64 `json:"dial_failures"`
	HandshakeSuccesses uint64 `json:"handshake_successes"`
	HandshakeFailures  uint64 `json:"handshake_failures"`

	InternallyRecoveredApplicationAttempts uint64 `json:"internally_recovered_application_attempts"`
	FinalApplicationErrors                 uint64 `json:"final_application_errors"`
	CallerCancellations                    uint64 `json:"caller_cancellations"`
	DeadlineCancellations                  uint64 `json:"deadline_cancellations"`
	RequestRejections                      uint64 `json:"request_rejections"`
	RequestStreamErrors                    uint64 `json:"request_stream_errors"`
	ConnectionErrors                       uint64 `json:"connection_errors"`

	PacketsSent           *uint64 `json:"packets_sent"`
	PacketsLost           *uint64 `json:"packets_lost"`
	PTOCountChanges       *uint64 `json:"pto_count_changes"`
	LatestRTTNS           *uint64 `json:"latest_rtt_ns"`
	SmoothedRTTNS         *uint64 `json:"smoothed_rtt_ns"`
	MinimumRTTNS          *uint64 `json:"minimum_rtt_ns"`
	CongestionWindowBytes *uint64 `json:"congestion_window_bytes"`
	BytesInFlight         *uint64 `json:"bytes_in_flight"`

	QLogFilesCreated uint64                 `json:"qlog_files_created"`
	QLogCreateErrors uint64                 `json:"qlog_create_errors"`
	Unavailable      map[string]string      `json:"unavailable"`
	Slots            []RunQUICSlotTelemetry `json:"slots"`
}

// RunQUICSlotTelemetry is one bounded row per configured pool slot. It records
// monotonic generation identity without retaining an event history.
type RunQUICSlotTelemetry struct {
	SlotID                     int    `json:"slot_id"`
	GenerationsStarted         uint64 `json:"generations_started"`
	LatestGeneration           uint64 `json:"latest_generation"`
	LatestAttributedGeneration uint64 `json:"latest_attributed_generation"`
	LiveGenerations            uint64 `json:"live_generations"`
	DialAttempts               uint64 `json:"dial_attempts"`
	DialSuccesses              uint64 `json:"dial_successes"`
	DialFailures               uint64 `json:"dial_failures"`
	ConnectionsOpened          uint64 `json:"connections_opened"`
	ConnectionsClosed          uint64 `json:"connections_closed"`
	HandshakeSuccesses         uint64 `json:"handshake_successes"`
	HandshakeFailures          uint64 `json:"handshake_failures"`
	InternalAttempts           uint64 `json:"internal_attempts"`
	InternalRetries            uint64 `json:"internal_retries"`
}

type RunEligibilityTelemetry struct {
	Eligible          bool     `json:"eligible"`
	PauseObserved     bool     `json:"pause_observed"`
	RequestDelay      bool     `json:"request_delay"`
	RateLimited       bool     `json:"rate_limited"`
	IneligibleReasons []string `json:"ineligible_reasons"`
}

type RunDrainTelemetry struct {
	WorkersDrained               bool   `json:"workers_drained"`
	RunnerClosed                 bool   `json:"runner_closed"`
	CloseError                   string `json:"close_error,omitempty"`
	LimiterInUse                 int64  `json:"limiter_in_use"`
	LimiterWaiters               int64  `json:"limiter_waiters"`
	TrackedConnectionGenerations uint64 `json:"tracked_connection_generations"`
	Complete                     bool   `json:"complete"`
}
