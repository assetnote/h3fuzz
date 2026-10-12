package ffuf

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml"
)

const testHarnessRunID = "11111111-2222-4333-8444-555555555555"

func TestHarnessRunIDFlagMapsToRuntimeConfig(t *testing.T) {
	opts := validHTTP3Options()
	fs := flag.NewFlagSet("ffuf", flag.ContinueOnError)
	RegisterFlags(fs, opts)
	if err := fs.Parse([]string{"-harness-run-id", testHarnessRunID}); err != nil {
		t.Fatalf("parse harness run ID: %v", err)
	}

	conf, err := ConfigFromOptions(opts, context.Background(), func() {})
	if err != nil {
		t.Fatalf("ConfigFromOptions: %v", err)
	}
	if conf.HarnessRunID != testHarnessRunID {
		t.Fatalf("HarnessRunID = %q, want %q", conf.HarnessRunID, testHarnessRunID)
	}
}

func TestReadConfigRejectsHarnessRunIDBypasses(t *testing.T) {
	for _, body := range []string{
		"harness-run-id = \"" + testHarnessRunID + "\"\n",
		"[general]\nharness_run_id = \"" + testHarnessRunID + "\"\n",
	} {
		path := filepath.Join(t.TempDir(), "harness.toml")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := ReadConfig(path)
		if err == nil || !strings.Contains(err.Error(), "-harness-run-id") {
			t.Fatalf("ReadConfig error = %v, want command-line-only harness flag rejection", err)
		}
	}
}

func TestHarnessRunIDRejectsMalformedUUID(t *testing.T) {
	opts := validHTTP3Options()
	opts.General.HarnessRunID = "not-a-uuid"

	_, err := ConfigFromOptions(opts, context.Background(), func() {})
	if err == nil || !strings.Contains(err.Error(), "-harness-run-id must be a UUID") {
		t.Fatalf("ConfigFromOptions error = %v, want malformed harness UUID rejection", err)
	}
}

func TestHarnessMarkerHeadersCannotBeSuppliedAsUserHeaders(t *testing.T) {
	for _, name := range []string{"X-H3fuzz-Harness-Run", "x-h3fuzz-harness-role"} {
		t.Run(name, func(t *testing.T) {
			opts := validHTTP3Options()
			opts.HTTP.Headers = []string{name + ": forged"}

			_, err := ConfigFromOptions(opts, context.Background(), func() {})
			if err == nil || !strings.Contains(err.Error(), "reserved harness header") {
				t.Fatalf("ConfigFromOptions error = %v, want reserved harness header rejection", err)
			}
		})
	}
}

func TestHarnessRunIDIsOffByDefaultAndNeverSerialized(t *testing.T) {
	opts := NewConfigOptions()
	if opts.General.HarnessRunID != "" {
		t.Fatalf("default HarnessRunID = %q, want empty", opts.General.HarnessRunID)
	}
	opts.General.HarnessRunID = testHarnessRunID

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conf := NewConfig(ctx, cancel)
	conf.HarnessRunID = testHarnessRunID

	for name, value := range map[string]any{"options": opts, "runtime config": conf} {
		jsonData, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal %s as JSON: %v", name, err)
		}
		if strings.Contains(string(jsonData), testHarnessRunID) {
			t.Fatalf("%s leaked harness run ID into JSON: %s", name, jsonData)
		}
	}
	tomlData, err := toml.Marshal(opts)
	if err != nil {
		t.Fatalf("marshal options as TOML: %v", err)
	}
	if strings.Contains(string(tomlData), testHarnessRunID) {
		t.Fatalf("options leaked harness run ID into TOML: %s", tomlData)
	}
}
