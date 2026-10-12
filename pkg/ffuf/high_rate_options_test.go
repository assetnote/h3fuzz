package ffuf

import (
	"context"
	"flag"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHighRateCLIFlagsParseAndRemainOptIn(t *testing.T) {
	ordinary := NewConfigOptions()
	if ordinary.HTTP.Http3HighRate || ordinary.HTTP.Http3HighRateAcknowledge ||
		ordinary.HTTP.Http3TargetRPS != 100_000 || ordinary.HTTP.Http3LatencyMS != 0 {
		t.Fatalf("ordinary defaults unexpectedly enable or alter high-rate policy: %+v", ordinary.HTTP)
	}

	opts := NewConfigOptions()
	flags := flag.NewFlagSet("ffuf", flag.ContinueOnError)
	RegisterFlags(flags, opts)
	if err := flags.Parse([]string{
		"-http3-high-rate",
		"-http3-high-rate-acknowledge",
		"-http3-target-rps", "125000",
		"-http3-latency-ms", "2.75",
	}); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	flags.Visit(func(value *flag.Flag) { MarkExplicitOptions(opts, value.Name) })

	if !opts.HTTP.Http3HighRate || !opts.HTTP.Http3HighRateAcknowledge ||
		opts.HTTP.Http3TargetRPS != 125_000 || opts.HTTP.Http3LatencyMS != 2.75 {
		t.Fatalf("high-rate flags were not parsed: %+v", opts.HTTP)
	}
	if !opts.Provenance.Http3HighRate || !opts.Provenance.Http3HighRateAcknowledge ||
		!opts.Provenance.Http3TargetRPS || !opts.Provenance.Http3LatencyMS {
		t.Fatalf("high-rate explicit-value provenance was not recorded: %+v", opts.Provenance)
	}
}

func TestConfigFilesRejectCLIOnlyHighRatePolicyKeys(t *testing.T) {
	for _, key := range []string{"http3-high-rate", "http3-high-rate-acknowledge", "http3-target-rps", "http3-latency-ms"} {
		t.Run(key, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ffufrc")
			contents := "[http]\n" + key + " = true\n"
			if key == "http3-target-rps" {
				contents = "[http]\n" + key + " = 100000\n"
			} else if key == "http3-latency-ms" {
				contents = "[http]\n" + key + " = 1.25\n"
			}
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := ReadConfig(path)
			if err == nil || !strings.Contains(err.Error(), "command-line only") {
				t.Fatalf("ReadConfig(%s) error = %v", key, err)
			}
		})
	}
}

func TestConfigFileMarksHighRateRelevantOrdinaryOptionProvenance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ffufrc")
	contents := "[http]\nhttp3streams = 256\n[general]\nscrapers = 'all'\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	opts, err := ReadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !opts.Provenance.Http3Streams || !opts.Provenance.Scrapers {
		t.Fatalf("high-rate-relevant config provenance = %+v", opts.Provenance)
	}
}

func validHighRateOptions() *ConfigOptions {
	opts := validHTTP3Options()
	opts.HTTP.Http3HighRate = true
	opts.HTTP.Http3HighRateAcknowledge = true
	opts.Output.RunTelemetry = "/tmp/high-rate-run.json"
	opts.Filter.Status = "204"
	return opts
}

func TestHighRateRequiresExplicitAcknowledgementBeforeRuntime(t *testing.T) {
	opts := validHighRateOptions()
	opts.HTTP.Http3HighRateAcknowledge = false

	_, err := ConfigFromOptions(opts, context.Background(), func() {})
	if err == nil || !strings.Contains(err.Error(), "-http3-high-rate-acknowledge") {
		t.Fatalf("ConfigFromOptions error = %v, want acknowledgement requirement", err)
	}
}

func TestHighRatePresetForcesOnlyItsDocumentedSafeDefaults(t *testing.T) {
	opts := validHighRateOptions()

	conf, err := ConfigFromOptions(opts, context.Background(), func() {})
	if err != nil {
		t.Fatalf("ConfigFromOptions: %v", err)
	}
	if !conf.Http3 || !conf.Noninteractive || conf.Rate != 0 || conf.Delay.HasDelay {
		t.Fatalf("forced high-rate defaults were not applied: %+v", conf)
	}
	if conf.Method != "GET" || conf.NoResponseOptimization {
		t.Fatalf("eligible request strategy was not preserved: method=%q disabled=%t", conf.Method, conf.NoResponseOptimization)
	}
	if conf.Scrapers != "" {
		t.Fatalf("omitted scraper selection should be disabled by the preset, got %q", conf.Scrapers)
	}
	if conf.HighRatePolicy == nil || !conf.HighRatePolicy.Enabled ||
		conf.HighRatePolicy.TargetRPS != 100_000 || conf.HighRatePolicy.StreamsPerConnection != 256 {
		t.Fatalf("high-rate runtime policy was not retained: %+v", conf.HighRatePolicy)
	}
}

func TestHighRatePolicyRetainsExplicitCapacityProvenance(t *testing.T) {
	opts := validHighRateOptions()
	opts.HTTP.Http3MaxConnections = 12
	opts.HTTP.Http3Streams = 128
	opts.HTTP.Http3MaxWorkers = 4_096
	opts.HTTP.Http3TargetRPS = 125_000
	opts.HTTP.Http3LatencyMS = 2.5
	MarkExplicitOptions(opts, "http3-max-connections", "http3-streams", "http3-max-workers", "http3-target-rps", "http3-latency-ms")

	conf, err := ConfigFromOptions(opts, context.Background(), func() {})
	if err != nil {
		t.Fatal(err)
	}
	report := conf.HighRatePolicy
	if report.ConnectionCapacity != 12 || report.ConnectionCapacitySource != "explicit" ||
		report.StreamsPerConnection != 128 || report.StreamsSource != "explicit" ||
		report.HTTP3MaxWorkers != 4_096 || report.HTTP3MaxWorkersSource != "explicit" ||
		report.TargetRPS != 125_000 || report.TargetRPSSource != "explicit" ||
		report.LatencyMS != 2.5 || report.LatencySource != "explicit" {
		t.Fatalf("explicit high-rate provenance = %+v", report)
	}
}

func TestHighRateRequiresStructuredTelemetryBeforePreflight(t *testing.T) {
	opts := validHighRateOptions()
	opts.Output.RunTelemetry = ""

	_, err := ConfigFromOptions(opts, context.Background(), func() {})
	if err == nil || !strings.Contains(err.Error(), "-run-telemetry") {
		t.Fatalf("ConfigFromOptions error = %v, want telemetry requirement", err)
	}
}

func TestHighRatePolicyFlagDependenciesAndNumericValidation(t *testing.T) {
	tests := []struct {
		name string
		set  func(*ConfigOptions)
		want string
	}{
		{"acknowledgement without preset", func(o *ConfigOptions) { o.HTTP.Http3HighRate = false; o.HTTP.Http3HighRateAcknowledge = true }, "requires -http3-high-rate"},
		{"target without preset", func(o *ConfigOptions) { o.HTTP.Http3HighRate = false; MarkExplicitOptions(o, "http3-target-rps") }, "requires -http3-high-rate"},
		{"latency without preset", func(o *ConfigOptions) { o.HTTP.Http3HighRate = false; o.HTTP.Http3LatencyMS = 1 }, "requires -http3-high-rate"},
		{"zero target", func(o *ConfigOptions) { o.HTTP.Http3TargetRPS = 0 }, "-http3-target-rps must be greater"},
		{"zero explicit latency", func(o *ConfigOptions) { MarkExplicitOptions(o, "http3-latency-ms") }, "finite positive"},
		{"negative latency", func(o *ConfigOptions) { o.HTTP.Http3LatencyMS = -1 }, "finite positive"},
		{"NaN latency", func(o *ConfigOptions) { o.HTTP.Http3LatencyMS = math.NaN() }, "finite positive"},
		{"infinite latency", func(o *ConfigOptions) { o.HTTP.Http3LatencyMS = math.Inf(1) }, "finite positive"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := validHighRateOptions()
			tt.set(opts)
			_, err := ConfigFromOptions(opts, context.Background(), func() {})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ConfigFromOptions error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestHighRateRejectsIneligibleFeaturesBeforePreflight(t *testing.T) {
	tests := []struct {
		name string
		set  func(*ConfigOptions)
		want string
	}{
		{"explicit threads", func(o *ConfigOptions) { MarkExplicitOptions(o, "t") }, "rejects explicit -t"},
		{"autocalibration", func(o *ConfigOptions) { o.General.AutoCalibration = true }, "-ac"},
		{"per-host autocalibration", func(o *ConfigOptions) { o.General.AutoCalibrationPerHost = true }, "-ach"},
		{"autocalibration string", func(o *ConfigOptions) { o.General.AutoCalibrationStrings = []string{"x"} }, "-acc"},
		{"autocalibration strategy", func(o *ConfigOptions) { o.General.AutoCalibrationStrategies = []string{"advanced"} }, "-acs"},
		{"raw mode", func(o *ConfigOptions) { o.HTTP.Raw = true }, "-raw"},
		{"request body", func(o *ConfigOptions) { o.HTTP.Data = "x" }, "bodyless GET"},
		{"non-GET method", func(o *ConfigOptions) { o.HTTP.Method = "POST" }, "bodyless GET"},
		{"user Range", func(o *ConfigOptions) { o.HTTP.Headers = []string{"Range: bytes=-1"} }, "user Range"},
		{"compressed representation", func(o *ConfigOptions) { o.HTTP.Headers = []string{"Accept-Encoding: gzip"} }, "Accept-Encoding"},
		{"header fuzzing", func(o *ConfigOptions) { o.HTTP.Headers = []string{"X-Tenant: FUZZ"} }, "header fuzzing"},
		{"host fuzzing", func(o *ConfigOptions) { o.HTTP.URL = "https://FUZZ.example/bench" }, "same-origin URL"},
		{"missing URL keyword", func(o *ConfigOptions) { o.HTTP.URL = "https://example.org/bench" }, "one URL fuzz keyword"},
		{"multiple input providers", func(o *ConfigOptions) {
			o.Input.Wordlists = append(o.Input.Wordlists, "/tmp/two.txt:OTHER")
			o.HTTP.URL += "/OTHER"
		}, "one wordlist input"},
		{"encoded input", func(o *ConfigOptions) { o.Input.Encoders = []string{"FUZZ:urlencode"} }, "encoders"},
		{"command input", func(o *ConfigOptions) { o.Input.Wordlists = nil; o.Input.Inputcommands = []string{"printf x:FUZZ"} }, "one wordlist input"},
		{"raw request", func(o *ConfigOptions) { o.Input.Request = "/missing/request" }, "raw request"},
		{"response optimization disabled", func(o *ConfigOptions) { o.HTTP.NoResponseOptimization = true }, "automatic HEAD and Range"},
		{"ignore body", func(o *ConfigOptions) { o.HTTP.IgnoreBody = true }, "-ignore-body"},
		{"redirects", func(o *ConfigOptions) { o.HTTP.FollowRedirects = true }, "redirects"},
		{"proxy", func(o *ConfigOptions) { o.HTTP.ProxyURL = "http://127.0.0.1:8080" }, "proxy"},
		{"replay", func(o *ConfigOptions) { o.HTTP.ReplayProxyURL = "http://127.0.0.1:8080" }, "replay"},
		{"recursion", func(o *ConfigOptions) { o.HTTP.Recursion = true }, "recursion"},
		{"preflight", func(o *ConfigOptions) { o.HTTP.Preflights = []PreflightConfig{{RequestFile: "/missing/pre"}} }, "preflight"},
		{"postflight", func(o *ConfigOptions) { o.HTTP.Postflights = []PreflightConfig{{RequestFile: "/missing/post"}} }, "postflight"},
		{"output file", func(o *ConfigOptions) { o.Output.OutputFile = "/tmp/out.json" }, "-o"},
		{"output directory", func(o *ConfigOptions) { o.Output.OutputDirectory = "/tmp/out" }, "-od"},
		{"audit log", func(o *ConfigOptions) { o.Output.AuditLog = "/tmp/audit" }, "audit logging"},
		{"stop all", func(o *ConfigOptions) { o.General.StopOnAll = true }, "-sa"},
		{"stop errors", func(o *ConfigOptions) { o.General.StopOnErrors = true }, "-se"},
		{"stop forbidden", func(o *ConfigOptions) { o.General.StopOn403 = true }, "-sf"},
		{"max time", func(o *ConfigOptions) { o.General.MaxTime = 1 }, "-maxtime"},
		{"max job time", func(o *ConfigOptions) { o.General.MaxTimeJob = 1 }, "-maxtime-job"},
		{"rate", func(o *ConfigOptions) { o.General.Rate = 1 }, "nonzero -rate"},
		{"delay", func(o *ConfigOptions) { o.General.Delay = "0.01" }, "nonzero -p"},
		{"line matcher", func(o *ConfigOptions) { o.Matcher.Lines = "1" }, "body-dependent matcher"},
		{"regexp matcher", func(o *ConfigOptions) { o.Matcher.Regexp = "x" }, "body-dependent matcher"},
		{"time matcher", func(o *ConfigOptions) { o.Matcher.Time = ">1" }, "body-dependent matcher"},
		{"word matcher", func(o *ConfigOptions) { o.Matcher.Words = "1" }, "body-dependent matcher"},
		{"line filter", func(o *ConfigOptions) { o.Filter.Lines = "1" }, "body-dependent filter"},
		{"regexp filter", func(o *ConfigOptions) { o.Filter.Regexp = "x" }, "body-dependent filter"},
		{"time filter", func(o *ConfigOptions) { o.Filter.Time = ">1" }, "body-dependent filter"},
		{"word filter", func(o *ConfigOptions) { o.Filter.Words = "1" }, "body-dependent filter"},
		{"no baseline filter", func(o *ConfigOptions) { o.Filter.Status, o.Filter.Size = "", "" }, "status or size filter"},
		{"explicit scrapers", func(o *ConfigOptions) { o.General.Scrapers = "all"; MarkExplicitOptions(o, "scrapers") }, "active scrapers"},
		{"custom scraper", func(o *ConfigOptions) { o.General.ScraperFile = "/tmp/scraper.json" }, "active scrapers"},
		{"dirsearch compatibility", func(o *ConfigOptions) { o.Input.DirSearchCompat = true }, "dirsearch"},
		{"pitchfork mode", func(o *ConfigOptions) { o.Input.InputMode = "pitchfork" }, "single-wordlist input mode"},
		{"stdin wordlist", func(o *ConfigOptions) { o.Input.Wordlists = []string{"-"} }, "finite wordlist"},
		{"HTTP1", func(o *ConfigOptions) { o.HTTP.Http1 = true }, "HTTP/1.1"},
		{"HTTP2", func(o *ConfigOptions) { o.HTTP.Http2 = true }, "HTTP/2"},
		{"HTTP3 disabled", func(o *ConfigOptions) { o.HTTP.NoHttp3 = true }, "-no-http3"},
		{"fingerprint action", func(o *ConfigOptions) { o.HTTP.Http3Fingerprint = true }, "-http3-fingerprint"},
		{"qlog", func(o *ConfigOptions) { o.Output.QuicQlogDir = "/tmp/qlog" }, "-quic-qlog-dir"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := validHighRateOptions()
			tt.set(opts)
			_, err := ConfigFromOptions(opts, context.Background(), func() {})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ConfigFromOptions error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestHighRateAllowsStatusAndLogicalSizeMatcherFilterCombinations(t *testing.T) {
	for _, configure := range []func(*ConfigOptions){
		func(o *ConfigOptions) { o.Filter.Status = "204" },
		func(o *ConfigOptions) { o.Filter.Status = ""; o.Filter.Size = "0" },
		func(o *ConfigOptions) { o.Filter.Status = "204"; o.Matcher.Status = "all" },
		func(o *ConfigOptions) { o.Filter.Size = "0"; o.Matcher.Size = "0" },
		func(o *ConfigOptions) {
			o.HTTP.Headers = []string{"X-Ceiling-Route: fixed", "Accept-Encoding: identity"}
		},
	} {
		opts := validHighRateOptions()
		configure(opts)
		if _, err := ConfigFromOptions(opts, context.Background(), func() {}); err != nil {
			t.Fatalf("compatible high-rate matcher/filter combination rejected: %v", err)
		}
	}
}
