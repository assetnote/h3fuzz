package webserver

import (
	"strings"
	"testing"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

func TestWebSessionsRejectHarnessRunIDOption(t *testing.T) {
	opts := ffuf.NewConfigOptions()
	opts.General.HarnessRunID = "11111111-2222-4333-8444-555555555555"

	err := sanitizeWebOptions(opts)
	if err == nil || !strings.Contains(err.Error(), "harness markers are command-line only") {
		t.Fatalf("sanitizeWebOptions error = %v", err)
	}
}

func TestWebSessionsRejectImportedHarnessRunIDFlag(t *testing.T) {
	err := rejectWebProfilingFlags([]string{"harness-run-id"})
	if err == nil || !strings.Contains(err.Error(), "command-line only") {
		t.Fatalf("rejectWebProfilingFlags error = %v", err)
	}
}
