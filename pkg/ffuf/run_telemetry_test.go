package ffuf

import (
	"context"
	"flag"
	"testing"
)

func TestRunTelemetryOptionFlagAndMapping(t *testing.T) {
	opts := NewConfigOptions()
	fs := flag.NewFlagSet("ffuf", flag.ContinueOnError)
	RegisterFlags(fs, opts)
	if err := fs.Parse([]string{"-run-telemetry", "/tmp/run.json"}); err != nil {
		t.Fatalf("parse run telemetry flag: %v", err)
	}
	if opts.Output.RunTelemetry != "/tmp/run.json" {
		t.Fatalf("option path = %q, want /tmp/run.json", opts.Output.RunTelemetry)
	}
	opts.HTTP.URL = "https://example.org/FUZZ"
	opts.Input.Wordlists = []string{"/tmp/wl.txt"}
	conf, err := ConfigFromOptions(opts, context.Background(), func() {})
	if err != nil {
		t.Fatalf("ConfigFromOptions: %v", err)
	}
	if conf.RunTelemetry != "/tmp/run.json" {
		t.Fatalf("config path = %q, want /tmp/run.json", conf.RunTelemetry)
	}
}
