package ffuf

import (
	"fmt"
	"math"
	"net/url"
	"strings"
)

// HighRatePolicyReport records the complete opt-in ceiling-test policy and its
// sizing decision. It is runtime evidence, not a persisted user setting.
type HighRatePolicyReport struct {
	Enabled                  bool    `json:"enabled"`
	Acknowledged             bool    `json:"acknowledged"`
	TargetRPS                int     `json:"target_rps"`
	TargetRPSSource          string  `json:"target_rps_source"`
	LatencyMS                float64 `json:"latency_ms"`
	LatencySource            string  `json:"latency_source"`
	Headroom                 float64 `json:"headroom"`
	DesiredWorkers           int     `json:"desired_workers"`
	ConnectionMode           string  `json:"connection_mode"`
	ConnectionCapacity       int     `json:"connection_capacity"`
	ConnectionCapacitySource string  `json:"connection_capacity_source"`
	StreamsPerConnection     int     `json:"streams_per_connection"`
	StreamsSource            string  `json:"streams_source"`
	StreamCapacity           int     `json:"stream_capacity"`
	HTTP3MaxWorkers          int     `json:"http3_max_workers"`
	HTTP3MaxWorkersSource    string  `json:"http3_max_workers_source"`
	HardWorkerLimit          int     `json:"hard_worker_limit"`
	EffectiveWorkers         int     `json:"effective_workers"`
	ExecutionWorkers         int     `json:"execution_workers"`
	ExpandedInputs           int     `json:"expanded_inputs"`
	RecommendedMinimumInputs uint64  `json:"recommended_minimum_inputs"`
	InputDurationHeadroom    bool    `json:"input_duration_headroom"`
	Provisioned              bool    `json:"provisioned"`
	Protocol                 string  `json:"protocol"`
	Method                   string  `json:"method"`
	ResponseOptimization     string  `json:"response_optimization"`
	Rate                     int64   `json:"rate"`
	Delay                    string  `json:"delay"`
	Noninteractive           bool    `json:"noninteractive"`
	TelemetryPath            string  `json:"telemetry_path"`
	DowngradeFatal           bool    `json:"downgrade_fatal"`
}

// HighRatePolicyRequested reports whether any CLI-only high-rate policy option
// was supplied. It lets process modes such as the web console reject the policy
// before they bypass normal scan configuration.
func HighRatePolicyRequested(opts *ConfigOptions) bool {
	if opts == nil {
		return false
	}
	return opts.HTTP.Http3HighRate || opts.HTTP.Http3HighRateAcknowledge ||
		(opts.HTTP.Http3TargetRPS != 0 && opts.HTTP.Http3TargetRPS != DefaultHTTP3TargetRPS) || opts.HTTP.Http3LatencyMS != 0 ||
		opts.Provenance.Http3HighRate || opts.Provenance.Http3HighRateAcknowledge ||
		opts.Provenance.Http3TargetRPS || opts.Provenance.Http3LatencyMS
}

func validateHighRateOptions(opts *ConfigOptions, conf *Config) []error {
	if opts == nil || conf == nil {
		return nil
	}
	if !opts.HTTP.Http3HighRate {
		var errs []error
		if opts.HTTP.Http3HighRateAcknowledge || opts.Provenance.Http3HighRateAcknowledge {
			errs = append(errs, fmt.Errorf("-http3-high-rate-acknowledge requires -http3-high-rate"))
		}
		if opts.Provenance.Http3TargetRPS || (opts.HTTP.Http3TargetRPS != 0 && opts.HTTP.Http3TargetRPS != DefaultHTTP3TargetRPS) {
			errs = append(errs, fmt.Errorf("-http3-target-rps requires -http3-high-rate"))
		}
		if opts.Provenance.Http3LatencyMS || opts.HTTP.Http3LatencyMS != 0 {
			errs = append(errs, fmt.Errorf("-http3-latency-ms requires -http3-high-rate"))
		}
		return errs
	}

	var errs []error
	add := func(condition bool, message string) {
		if condition {
			errs = append(errs, fmt.Errorf("%s", message))
		}
	}
	add(!opts.HTTP.Http3HighRateAcknowledge, "-http3-high-rate requires -http3-high-rate-acknowledge")
	add(strings.TrimSpace(opts.Output.RunTelemetry) == "", "-http3-high-rate requires -run-telemetry <path>")
	add(opts.HTTP.Http3TargetRPS <= 0, "-http3-target-rps must be greater than 0")
	add(opts.HTTP.Http3LatencyMS < 0 || math.IsNaN(opts.HTTP.Http3LatencyMS) || math.IsInf(opts.HTTP.Http3LatencyMS, 0), "-http3-latency-ms must be a finite positive value")
	add(opts.Provenance.Http3LatencyMS && opts.HTTP.Http3LatencyMS <= 0, "-http3-latency-ms must be a finite positive value")
	add(conf.ThreadsExplicit, "-http3-high-rate rejects explicit -t; omit it to enable runner-derived workers")
	add(opts.HTTP.Http1, "-http3-high-rate rejects HTTP/1.1 selection")
	add(opts.HTTP.NoHttp3, "-http3-high-rate rejects -no-http3")
	add(opts.HTTP.Http2, "-http3-high-rate rejects HTTP/2 selection")
	add(opts.HTTP.Http3Fingerprint, "-http3-high-rate rejects the standalone -http3-fingerprint action")
	add(opts.HTTP.NoResponseOptimization, "-http3-high-rate requires automatic HEAD and Range qualification")
	add(opts.HTTP.IgnoreBody, "-http3-high-rate rejects -ignore-body because ceiling responses must reach a normal terminal outcome")
	add(opts.General.AutoCalibration, "-http3-high-rate rejects -ac")
	add(opts.General.AutoCalibrationPerHost, "-http3-high-rate rejects -ach")
	add(len(opts.General.AutoCalibrationStrings) != 0, "-http3-high-rate rejects -acc autocalibration strings")
	add(len(opts.General.AutoCalibrationStrategies) != 1 || opts.General.AutoCalibrationStrategies[0] != "basic", "-http3-high-rate rejects -acs autocalibration strategies")
	add(opts.HTTP.Raw, "-http3-high-rate rejects -raw")
	add(opts.Input.Request != "", "-http3-high-rate rejects a raw request file")
	add(!strings.EqualFold(conf.Method, "GET") || conf.Data != "", "-http3-high-rate requires a bodyless GET")
	add(opts.HTTP.FollowRedirects, "-http3-high-rate rejects redirects")
	add(conf.ProxyURL != "", "-http3-high-rate rejects a proxy")
	add(conf.ReplayProxyURL != "", "-http3-high-rate rejects replay")
	add(opts.HTTP.Recursion || opts.HTTP.RecursionDepth != 0 || opts.HTTP.RecursionStrategy != "default", "-http3-high-rate rejects recursion")
	add(len(opts.HTTP.Preflights) != 0, "-http3-high-rate rejects any preflight")
	add(len(opts.HTTP.Postflights) != 0, "-http3-high-rate rejects any postflight because it adds a request per input")
	add(opts.Output.OutputFile != "", "-http3-high-rate rejects -o")
	add(opts.Output.OutputDirectory != "", "-http3-high-rate rejects -od")
	add(opts.Output.AuditLog != "", "-http3-high-rate rejects audit logging")
	add(opts.General.StopOnAll, "-http3-high-rate rejects -sa")
	add(opts.General.StopOnErrors, "-http3-high-rate rejects -se")
	add(opts.General.StopOn403, "-http3-high-rate rejects -sf")
	add(opts.General.MaxTime != 0, "-http3-high-rate rejects nonzero -maxtime")
	add(opts.General.MaxTimeJob != 0, "-http3-high-rate rejects nonzero -maxtime-job")
	add(opts.General.Rate != 0, "-http3-high-rate rejects nonzero -rate")
	add(strings.TrimSpace(opts.General.Delay) != "", "-http3-high-rate rejects nonzero -p delay")
	add(opts.Output.QuicQlogDir != "", "-http3-high-rate rejects -quic-qlog-dir")
	add(opts.Output.ProfileCPU != "" || opts.Output.ProfileHeap != "" || opts.Output.ProfileMutex != "" || opts.Output.ProfileBlock != "", "-http3-high-rate rejects runtime profiling")
	add(opts.General.ScraperFile != "" || (opts.Provenance.Scrapers && strings.TrimSpace(opts.General.Scrapers) != ""), "-http3-high-rate rejects active scrapers")
	add(opts.Input.DirSearchCompat, "-http3-high-rate rejects dirsearch compatibility mode")
	add(opts.Input.InputMode != "clusterbomb", "-http3-high-rate requires the single-wordlist input mode")
	add(len(opts.Input.Encoders) != 0, "-http3-high-rate rejects input encoders")
	add(len(conf.InputProviders) != 1 || len(opts.Input.Wordlists) != 1 || len(opts.Input.Inputcommands) != 0, "-http3-high-rate requires one wordlist input")
	if len(conf.InputProviders) == 1 {
		provider := conf.InputProviders[0]
		add(provider.Name != "wordlist", "-http3-high-rate requires one wordlist input")
		add(provider.Encoders != "", "-http3-high-rate rejects input encoders")
		add(provider.Value == "-", "-http3-high-rate requires a finite wordlist so expanded inputs can be calculated")
	}

	add(opts.Matcher.Lines != "" || opts.Matcher.Regexp != "" || opts.Matcher.Time != "" || opts.Matcher.Words != "", "-http3-high-rate rejects a body-dependent matcher")
	add(opts.Filter.Lines != "" || opts.Filter.Regexp != "" || opts.Filter.Time != "" || opts.Filter.Words != "", "-http3-high-rate rejects a body-dependent filter")
	add(strings.TrimSpace(opts.Filter.Status) == "" && strings.TrimSpace(opts.Filter.Size) == "", "-http3-high-rate requires a known baseline status or size filter (using logical size); -s alone is insufficient")

	parsed, parseErr := url.Parse(conf.Url)
	if parseErr != nil || parsed.Scheme != "https" || parsed.Host == "" {
		errs = append(errs, fmt.Errorf("-http3-high-rate requires an absolute HTTPS URL with one URL fuzz keyword"))
	} else if len(conf.InputProviders) == 1 {
		keyword := conf.InputProviders[0].Keyword
		add(keyword == "" || !strings.Contains(parsed.RequestURI(), keyword), "-http3-high-rate requires one URL fuzz keyword in the path or query")
		add(strings.Contains(parsed.Host, keyword) || strings.Contains(conf.SNI, keyword), "-http3-high-rate requires a same-origin URL fuzz keyword and rejects host fuzzing")
		for name, value := range conf.Headers {
			if strings.Contains(name, keyword) || strings.Contains(value, keyword) {
				errs = append(errs, fmt.Errorf("-http3-high-rate rejects header fuzzing"))
				break
			}
		}
	} else if len(opts.Input.Wordlists) == 1 {
		keyword := "FUZZ"
		if parts := strings.SplitN(opts.Input.Wordlists[0], ":", 2); len(parts) == 2 {
			keyword = parts[1]
		}
		add(keyword == "" || !strings.Contains(parsed.RequestURI(), keyword), "-http3-high-rate requires one URL fuzz keyword in the path or query")
	}
	for name, value := range conf.Headers {
		if strings.EqualFold(name, "Range") && strings.TrimSpace(value) != "" {
			errs = append(errs, fmt.Errorf("-http3-high-rate rejects a user Range header"))
		}
		if strings.EqualFold(name, "Accept-Encoding") {
			encoding := strings.TrimSpace(value)
			if encoding != "" && !strings.EqualFold(encoding, "identity") {
				errs = append(errs, fmt.Errorf("-http3-high-rate requires identity or absent Accept-Encoding"))
			}
		}
	}
	return errs
}

func applyHighRatePreset(opts *ConfigOptions, conf *Config) {
	if opts == nil || conf == nil || !opts.HTTP.Http3HighRate {
		return
	}
	conf.Http3 = true
	conf.Noninteractive = true
	if !opts.Provenance.Scrapers {
		// The ordinary default enables the built-in scraper groups. Ceiling mode
		// has no body consumer, so omission means no active scraper.
		conf.Scrapers = ""
	}
	connectionMode := "adaptive"
	connectionCapacity := conf.Http3MaxConnections
	connectionSource := "default"
	if conf.Http3Connections > 0 {
		connectionMode = "fixed"
		connectionCapacity = conf.Http3Connections
		connectionSource = "explicit"
	} else if opts.Provenance.Http3MaxConnections || opts.HTTP.Http3MaxConnections != 64 {
		connectionSource = "explicit"
	}
	targetSource := "default"
	if opts.Provenance.Http3TargetRPS || opts.HTTP.Http3TargetRPS != DefaultHTTP3TargetRPS {
		targetSource = "explicit"
	}
	streamsSource := "default"
	if opts.Provenance.Http3Streams || opts.HTTP.Http3Streams != 256 {
		streamsSource = "explicit"
	}
	latencySource := "measured_preflight"
	if opts.Provenance.Http3LatencyMS || opts.HTTP.Http3LatencyMS != 0 {
		latencySource = "explicit"
	}
	maxWorkersSource := "default"
	if opts.Provenance.Http3MaxWorkers || opts.HTTP.Http3MaxWorkers != MaxHTTP3Workers {
		maxWorkersSource = "explicit"
	}
	conf.HighRatePolicy = &HighRatePolicyReport{
		Enabled:                  true,
		Acknowledged:             opts.HTTP.Http3HighRateAcknowledge,
		TargetRPS:                opts.HTTP.Http3TargetRPS,
		TargetRPSSource:          targetSource,
		LatencyMS:                opts.HTTP.Http3LatencyMS,
		LatencySource:            latencySource,
		Headroom:                 1.25,
		ConnectionMode:           connectionMode,
		ConnectionCapacity:       connectionCapacity,
		ConnectionCapacitySource: connectionSource,
		StreamsPerConnection:     conf.Http3Streams,
		StreamsSource:            streamsSource,
		HTTP3MaxWorkers:          conf.Http3MaxWorkers,
		HTTP3MaxWorkersSource:    maxWorkersSource,
		HardWorkerLimit:          MaxHTTP3Workers,
		Protocol:                 "http3",
		Method:                   "GET",
		ResponseOptimization:     "automatic_head_or_range",
		Rate:                     0,
		Delay:                    "none",
		Noninteractive:           true,
		TelemetryPath:            conf.RunTelemetry,
		DowngradeFatal:           true,
	}
}

// FinalizeHighRatePolicy applies the documented latency/headroom formula after
// protocol and request-strategy qualification have completed. expandedInputs is
// the input provider's post-extension total.
func FinalizeHighRatePolicy(conf *Config, expandedInputs int) error {
	if conf == nil || conf.HighRatePolicy == nil || !conf.HighRatePolicy.Enabled {
		return nil
	}
	report := conf.HighRatePolicy
	if selected := strings.ToLower(strings.TrimSpace(conf.ProtocolPreflight.SelectedProtocol)); selected != "" && selected != "http3" {
		return fmt.Errorf("-http3-high-rate requires HTTP/3; protocol qualification selected %s", selected)
	}
	report.Headroom = 1.25
	report.HardWorkerLimit = MaxHTTP3Workers
	report.HTTP3MaxWorkers = conf.Http3MaxWorkers
	report.StreamsPerConnection = conf.Http3Streams
	report.ExpandedInputs = expandedInputs
	if report.TargetRPS <= 0 || uint64(report.TargetRPS) > ^uint64(0)/36 {
		return fmt.Errorf("-http3-high-rate target_rps=%d cannot be represented safely", report.TargetRPS)
	}
	report.RecommendedMinimumInputs = uint64(report.TargetRPS) * 36
	report.InputDurationHeadroom = expandedInputs >= 0 && uint64(expandedInputs) >= report.RecommendedMinimumInputs

	if conf.Http3Connections > 0 {
		report.ConnectionMode = "fixed"
		report.ConnectionCapacity = conf.Http3Connections
	} else {
		report.ConnectionMode = "adaptive"
		report.ConnectionCapacity = conf.Http3MaxConnections
	}
	streamCapacity64 := int64(report.ConnectionCapacity) * int64(report.StreamsPerConnection)
	if streamCapacity64 <= 0 || streamCapacity64 > int64(^uint(0)>>1) {
		return fmt.Errorf("-http3-high-rate invalid connection capacity: connections=%d streams=%d", report.ConnectionCapacity, report.StreamsPerConnection)
	}
	report.StreamCapacity = int(streamCapacity64)

	latencyMS := report.LatencyMS
	if report.LatencySource == "measured_preflight" {
		full := conf.ProtocolPreflight.RequestStrategy.FullGET
		if !full.Successful || !validPositiveFloat(full.MeanLatencyMS) {
			return fmt.Errorf("-http3-high-rate could not size workers: successful full-GET preflight latency is unavailable")
		}
		latencyMS = full.MeanLatencyMS
		selectedName := strings.ToUpper(conf.ProtocolPreflight.RequestStrategy.Selected)
		var selected PreflightProbeReport
		switch selectedName {
		case "HEAD":
			selected = conf.ProtocolPreflight.RequestStrategy.HEAD
		case "RANGE":
			selected = conf.ProtocolPreflight.RequestStrategy.Range
		case "GET", "":
			selected = full
		default:
			return fmt.Errorf("-http3-high-rate could not size workers: unknown selected strategy %q", selectedName)
		}
		if !selected.Successful || !validPositiveFloat(selected.MeanLatencyMS) {
			return fmt.Errorf("-http3-high-rate could not size workers: successful %s preflight latency is unavailable", selectedName)
		}
		if selected.MeanLatencyMS > latencyMS {
			latencyMS = selected.MeanLatencyMS
		}
	}
	if !validPositiveFloat(latencyMS) {
		return fmt.Errorf("-http3-high-rate requires a finite positive sizing latency")
	}
	report.LatencyMS = latencyMS

	desiredFloat := math.Ceil(float64(report.TargetRPS) * (latencyMS / 1000) * report.Headroom)
	if !validPositiveFloat(desiredFloat) || desiredFloat > float64(^uint(0)>>1) {
		return fmt.Errorf("-http3-high-rate desired worker calculation overflowed for target_rps=%d latency_ms=%.6f", report.TargetRPS, latencyMS)
	}
	report.DesiredWorkers = int(desiredFloat)
	report.EffectiveWorkers = minHighRateInt(report.DesiredWorkers, report.StreamCapacity, report.HTTP3MaxWorkers, report.HardWorkerLimit)
	if report.EffectiveWorkers < report.DesiredWorkers {
		report.Provisioned = false
		return fmt.Errorf("-http3-high-rate capacity is insufficient: desired_workers=%d effective_workers=%d connection_mode=%s connection_capacity=%d streams=%d stream_capacity=%d http3_max_workers=%d hard_worker_limit=%d",
			report.DesiredWorkers, report.EffectiveWorkers, report.ConnectionMode, report.ConnectionCapacity,
			report.StreamsPerConnection, report.StreamCapacity, report.HTTP3MaxWorkers, report.HardWorkerLimit)
	}
	report.Provisioned = true
	executionWorkers := conf.Threads
	if report.ConnectionMode == "fixed" {
		executionWorkers = minHighRateInt(report.StreamCapacity, report.HTTP3MaxWorkers, report.HardWorkerLimit)
	} else if executionWorkers <= 0 {
		executionWorkers = report.StreamCapacity
		if executionWorkers > report.HTTP3MaxWorkers {
			executionWorkers = report.HTTP3MaxWorkers
		}
	}
	if expandedInputs >= 0 && executionWorkers > expandedInputs {
		executionWorkers = expandedInputs
	}
	conf.Threads = executionWorkers
	report.ExecutionWorkers = executionWorkers
	return nil
}

func validPositiveFloat(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func minHighRateInt(values ...int) int {
	if len(values) == 0 {
		return 0
	}
	minimum := values[0]
	for _, value := range values[1:] {
		if value < minimum {
			minimum = value
		}
	}
	return minimum
}
