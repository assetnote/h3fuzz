package ffuf

import (
	"context"
	"encoding/json"
	"flag"
	"reflect"
	"strings"
	"testing"
)

func validHTTP3Options() *ConfigOptions {
	opts := NewConfigOptions()
	opts.HTTP.URL = "https://example.org/FUZZ"
	opts.Input.Wordlists = []string{"/tmp/wl.txt"}
	return opts
}

func TestHTTP3OptionDefaultsAndMapping(t *testing.T) {
	opts := validHTTP3Options()
	if opts.HTTP.Http3Connections != 0 || opts.HTTP.Http3MaxConnections != 64 ||
		opts.HTTP.Http3MaxWorkers != MaxHTTP3Workers || opts.HTTP.Http3Streams != 256 || opts.HTTP.Http3ProbeTimeout != 3 {
		t.Fatalf("unexpected HTTP/3 defaults: %+v", opts.HTTP)
	}

	conf, err := ConfigFromOptions(opts, context.Background(), func() {})
	if err != nil {
		t.Fatalf("ConfigFromOptions: %v", err)
	}
	if conf.Http1 || conf.Http3 || conf.NoHttp3 || conf.Http3Fingerprint || conf.NoResponseOptimization {
		t.Fatalf("HTTP/3 boolean defaults were not preserved: %+v", conf)
	}
	if conf.Http3Connections != 0 || conf.Http3MaxConnections != 64 ||
		conf.Http3MaxWorkers != MaxHTTP3Workers || conf.Http3Streams != 256 || conf.Http3ProbeTimeout != 3 {
		t.Fatalf("HTTP/3 numeric defaults were not mapped: %+v", conf)
	}
}

func TestHTTP1OptionMapping(t *testing.T) {
	opts := validHTTP3Options()
	opts.HTTP.Http1 = true

	conf, err := ConfigFromOptions(opts, context.Background(), func() {})
	if err != nil {
		t.Fatalf("ConfigFromOptions: %v", err)
	}
	if !conf.Http1 {
		t.Fatalf("HTTP/1 option was not mapped into Config: %+v", conf)
	}
}

func TestHTTP3OptionMapping(t *testing.T) {
	opts := validHTTP3Options()
	opts.HTTP.Http3 = true
	opts.HTTP.Http3Fingerprint = true
	opts.HTTP.NoResponseOptimization = true
	opts.HTTP.Http3Connections = 8
	opts.HTTP.Http3MaxConnections = 16
	opts.HTTP.Http3MaxWorkers = 5_000
	opts.HTTP.Http3Streams = 400
	opts.HTTP.Http3ProbeTimeout = 6

	conf, err := ConfigFromOptions(opts, context.Background(), func() {})
	if err != nil {
		t.Fatalf("ConfigFromOptions: %v", err)
	}
	if !conf.Http3 || !conf.Http3Fingerprint || !conf.NoResponseOptimization ||
		conf.Http3Connections != 8 || conf.Http3MaxConnections != 16 || conf.Http3MaxWorkers != 5_000 ||
		conf.Http3Streams != 400 || conf.Http3ProbeTimeout != 6 {
		t.Fatalf("HTTP/3 options were not mapped into Config: %+v", conf)
	}

	disabled := validHTTP3Options()
	disabled.HTTP.NoHttp3 = true
	conf, err = ConfigFromOptions(disabled, context.Background(), func() {})
	if err != nil {
		t.Fatalf("ConfigFromOptions with -no-http3: %v", err)
	}
	if !conf.NoHttp3 {
		t.Fatalf("NoHttp3 was not mapped into Config: %+v", conf)
	}
}

func TestHTTP3FlagsAreReflectedIntoOptions(t *testing.T) {
	opts := NewConfigOptions()
	fs := flag.NewFlagSet("ffuf", flag.ContinueOnError)
	RegisterFlags(fs, opts)
	err := fs.Parse([]string{
		"-http3", "-http3-fingerprint", "-no-response-optimization",
		"-http3-connections", "4", "-http3-max-connections", "12",
		"-http3-max-workers", "5000",
		"-http3-streams", "512", "-http3-probe-timeout", "7",
	})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !opts.HTTP.Http3 || !opts.HTTP.Http3Fingerprint || !opts.HTTP.NoResponseOptimization {
		t.Fatalf("HTTP/3 boolean flags were not parsed: %+v", opts.HTTP)
	}
	if opts.HTTP.Http3Connections != 4 || opts.HTTP.Http3MaxConnections != 12 || opts.HTTP.Http3MaxWorkers != 5_000 ||
		opts.HTTP.Http3Streams != 512 || opts.HTTP.Http3ProbeTimeout != 7 {
		t.Fatalf("HTTP/3 numeric flags were not parsed: %+v", opts.HTTP)
	}

	disabled := NewConfigOptions()
	fs = flag.NewFlagSet("ffuf", flag.ContinueOnError)
	RegisterFlags(fs, disabled)
	if err := fs.Parse([]string{"-no-http3"}); err != nil {
		t.Fatalf("Parse -no-http3: %v", err)
	}
	if !disabled.HTTP.NoHttp3 {
		t.Fatalf("-no-http3 was not reflected into options: %+v", disabled.HTTP)
	}
}

func TestHTTP3OptionsJSONRoundTrip(t *testing.T) {
	for _, mode := range []struct {
		name string
		set  func(*HTTPOptions)
	}{
		{"force", func(http *HTTPOptions) { http.Http3 = true }},
		{"disable", func(http *HTTPOptions) { http.NoHttp3 = true }},
	} {
		t.Run(mode.name, func(t *testing.T) {
			opts := NewConfigOptions()
			mode.set(&opts.HTTP)
			opts.HTTP.Http3Fingerprint = true
			opts.HTTP.NoResponseOptimization = true
			opts.HTTP.Http3Connections = 7
			opts.HTTP.Http3MaxConnections = 11
			opts.HTTP.Http3MaxWorkers = 4_321
			opts.HTTP.Http3Streams = 333
			opts.HTTP.Http3ProbeTimeout = 9

			data, err := json.Marshal(opts)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			var got ConfigOptions
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if got.HTTP.Http3HighRate || got.HTTP.Http3HighRateAcknowledge || got.HTTP.Http3TargetRPS != 0 || got.HTTP.Http3LatencyMS != 0 {
				t.Fatalf("CLI-only high-rate policy leaked into history JSON: %+v", got.HTTP)
			}
			// The policy fields are intentionally not persisted. Restore their
			// in-memory defaults before comparing the ordinary HTTP options.
			got.HTTP.Http3TargetRPS = opts.HTTP.Http3TargetRPS
			if !reflect.DeepEqual(got.HTTP, opts.HTTP) {
				t.Fatalf("HTTP/3 history-compatible JSON round trip lost values: got %+v, want %+v", got.HTTP, opts.HTTP)
			}
		})
	}
}

func TestHTTP3FingerprintAllowsMissingInput(t *testing.T) {
	opts := NewConfigOptions()
	opts.HTTP.URL = "https://example.org/"
	opts.HTTP.Http3Fingerprint = true

	conf, err := ConfigFromOptions(opts, context.Background(), func() {})
	if err != nil {
		t.Fatalf("fingerprint-only configuration should not require input: %v", err)
	}
	if len(conf.InputProviders) != 0 {
		t.Fatalf("fingerprint-only configuration unexpectedly created input providers: %+v", conf.InputProviders)
	}

	opts.HTTP.Http3Fingerprint = false
	_, err = ConfigFromOptions(opts, context.Background(), func() {})
	if err == nil || !strings.Contains(err.Error(), "Either -w or --input-cmd flag is required") {
		t.Fatalf("normal configuration without input error = %v", err)
	}
}

func TestHTTP3OptionValidation(t *testing.T) {
	tests := []struct {
		name string
		set  func(*HTTPOptions)
		want string
	}{
		{"force conflicts with disabled", func(o *HTTPOptions) { o.Http3, o.NoHttp3 = true, true }, "-http3 and -no-http3"},
		{"force conflicts with HTTP2", func(o *HTTPOptions) { o.Http3, o.Http2 = true, true }, "-http3 and -http2"},
		{"HTTP1 conflicts with HTTP2", func(o *HTTPOptions) { o.Http1, o.Http2 = true, true }, "-http1 and -http2"},
		{"HTTP1 conflicts with HTTP3", func(o *HTTPOptions) { o.Http1, o.Http3 = true, true }, "-http1 and -http3"},
		{"HTTP1 conflicts with no HTTP3", func(o *HTTPOptions) { o.Http1, o.NoHttp3 = true, true }, "-http1 and -no-http3"},
		{"negative fixed connections", func(o *HTTPOptions) { o.Http3Connections = -1 }, "-http3-connections must be"},
		{"zero max connections", func(o *HTTPOptions) { o.Http3MaxConnections = 0 }, "-http3-max-connections must be"},
		{"max workers above safety cap", func(o *HTTPOptions) { o.Http3MaxWorkers = MaxHTTP3Workers + 1 }, "-http3-max-workers cannot exceed"},
		{"zero streams", func(o *HTTPOptions) { o.Http3Streams = 0 }, "-http3-streams must be"},
		{"zero probe timeout", func(o *HTTPOptions) { o.Http3ProbeTimeout = 0 }, "-http3-probe-timeout must be"},
		{"fixed exceeds max", func(o *HTTPOptions) { o.Http3Connections, o.Http3MaxConnections = 5, 4 }, "-http3-connections cannot exceed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := validHTTP3Options()
			tt.set(&opts.HTTP)
			_, err := ConfigFromOptions(opts, context.Background(), func() {})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ConfigFromOptions error = %v, want substring %q", err, tt.want)
			}
		})
	}
}
