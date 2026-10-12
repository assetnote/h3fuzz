package ffuf

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

func TestCLIVisitPreservesExplicitDefaultWorkerValues(t *testing.T) {
	opts := validHTTP3Options()
	flags := flag.NewFlagSet("ffuf", flag.ContinueOnError)
	RegisterFlags(flags, opts)
	if err := flags.Parse([]string{"-t", "40", "-http3-max-workers", "65536"}); err != nil {
		t.Fatal(err)
	}
	flags.Visit(func(value *flag.Flag) { MarkExplicitOptions(opts, value.Name) })
	conf, err := ConfigFromOptions(opts, context.Background(), func() {})
	if err != nil {
		t.Fatal(err)
	}
	if !conf.ThreadsExplicit || !conf.Http3MaxWorkersExplicit {
		t.Fatalf("CLI explicit-default provenance lost: %+v", opts.Provenance)
	}
}

func TestAdaptiveWorkerOptionDefaultsAndExplicitProvenanceRoundTrip(t *testing.T) {
	opts := validHTTP3Options()
	if opts.HTTP.Http3MaxWorkers != MaxHTTP3Workers {
		t.Fatalf("default http3 max workers = %d, want %d", opts.HTTP.Http3MaxWorkers, MaxHTTP3Workers)
	}
	MarkExplicitOptions(opts, "t", "http3-max-workers")
	conf, err := ConfigFromOptions(opts, context.Background(), func() {})
	if err != nil {
		t.Fatalf("ConfigFromOptions: %v", err)
	}
	if conf.Threads != defaultThreadCount || !conf.ThreadsExplicit {
		t.Fatalf("explicit default threads mapped as (%d, %t)", conf.Threads, conf.ThreadsExplicit)
	}
	if conf.Http3MaxWorkers != MaxHTTP3Workers || !conf.Http3MaxWorkersExplicit {
		t.Fatalf("explicit default max workers mapped as (%d, %t)", conf.Http3MaxWorkers, conf.Http3MaxWorkersExplicit)
	}

	encoded, err := json.Marshal(opts)
	if err != nil {
		t.Fatal(err)
	}
	var restored ConfigOptions
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := ConfigFromOptions(&restored, context.Background(), func() {})
	if err != nil {
		t.Fatal(err)
	}
	if !rebuilt.ThreadsExplicit || !rebuilt.Http3MaxWorkersExplicit {
		t.Fatalf("history round trip lost provenance: %+v", restored.Provenance)
	}
}

func TestReadConfigPreservesExplicitDefaultWorkerValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ffufrc")
	data := []byte("[general]\nthreads = 40\n[http]\nhttp3maxworkers = 65536\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	opts, err := ReadConfig(path)
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}
	if !opts.Provenance.Threads || !opts.Provenance.Http3MaxWorkers {
		t.Fatalf("config-file provenance = %+v", opts.Provenance)
	}
}

func TestAdaptiveWorkerOptionValidation(t *testing.T) {
	for _, value := range []int{0, MaxHTTP3Workers + 1} {
		opts := validHTTP3Options()
		opts.HTTP.Http3MaxWorkers = value
		MarkExplicitOptions(opts, "http3-max-workers")
		if _, err := ConfigFromOptions(opts, context.Background(), func() {}); err == nil {
			t.Fatalf("http3 max workers %d unexpectedly accepted", value)
		}
	}
}

func TestLegacyHistoryWithoutAdaptiveWorkerBoundUsesDefault(t *testing.T) {
	opts := validHTTP3Options()
	opts.HTTP.Http3MaxWorkers = 0
	conf, err := ConfigFromOptions(opts, context.Background(), func() {})
	if err != nil {
		t.Fatal(err)
	}
	if conf.Http3MaxWorkers != MaxHTTP3Workers || conf.Options.HTTP.Http3MaxWorkers != MaxHTTP3Workers || conf.Http3MaxWorkersExplicit {
		t.Fatalf("legacy worker bound = config %d retained %d explicit %t", conf.Http3MaxWorkers, conf.Options.HTTP.Http3MaxWorkers, conf.Http3MaxWorkersExplicit)
	}
}
