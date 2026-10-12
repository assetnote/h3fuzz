package ffuf

import (
	"context"
	"flag"
	"strings"
	"testing"
)

func TestQUICDiagnosticFlagsMapToRuntimeConfig(t *testing.T) {
	opts := validHTTP3Options()
	fs := flag.NewFlagSet("ffuf", flag.ContinueOnError)
	RegisterFlags(fs, opts)
	if err := fs.Parse([]string{
		"-run-telemetry", "/tmp/run.json",
		"-quic-telemetry",
		"-quic-qlog-dir", "/tmp/qlogs",
	}); err != nil {
		t.Fatalf("parse QUIC diagnostic flags: %v", err)
	}

	conf, err := ConfigFromOptions(opts, context.Background(), func() {})
	if err != nil {
		t.Fatalf("ConfigFromOptions: %v", err)
	}
	if !conf.QuicTelemetry || conf.QuicQlogDir != "/tmp/qlogs" {
		t.Fatalf("QUIC diagnostics were not mapped: %+v", conf)
	}
}

func TestQUICDiagnosticsRequireStructuredRunTelemetry(t *testing.T) {
	for _, test := range []struct {
		name string
		set  func(*ConfigOptions)
		want string
	}{
		{
			name: "bounded aggregates",
			set:  func(opts *ConfigOptions) { opts.Output.QuicTelemetry = true },
			want: "-quic-telemetry requires -run-telemetry",
		},
		{
			name: "full qlog",
			set:  func(opts *ConfigOptions) { opts.Output.QuicQlogDir = "/tmp/qlogs" },
			want: "-quic-qlog-dir requires -run-telemetry",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			opts := validHTTP3Options()
			test.set(opts)
			_, err := ConfigFromOptions(opts, context.Background(), func() {})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ConfigFromOptions error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestQUICDiagnosticsDefaultOff(t *testing.T) {
	opts := NewConfigOptions()
	if opts.Output.QuicTelemetry || opts.Output.QuicQlogDir != "" {
		t.Fatalf("QUIC diagnostics defaulted on: %+v", opts.Output)
	}
}
