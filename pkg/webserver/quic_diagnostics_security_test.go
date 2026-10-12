package webserver

import (
	"strings"
	"testing"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

func TestWebSessionsRejectQUICDiagnosticOptions(t *testing.T) {
	for _, test := range []struct {
		name string
		set  func(*ffuf.ConfigOptions)
	}{
		{"bounded telemetry", func(opts *ffuf.ConfigOptions) { opts.Output.QuicTelemetry = true }},
		{"full qlog", func(opts *ffuf.ConfigOptions) { opts.Output.QuicQlogDir = t.TempDir() }},
	} {
		t.Run(test.name, func(t *testing.T) {
			opts := ffuf.NewConfigOptions()
			test.set(opts)
			err := sanitizeWebOptions(opts)
			if err == nil || !strings.Contains(err.Error(), "QUIC diagnostics are command-line only") {
				t.Fatalf("sanitizeWebOptions error = %v", err)
			}
		})
	}
}

func TestWebSessionsRejectImportedQUICDiagnosticFlags(t *testing.T) {
	for _, name := range []string{"quic-telemetry", "quic-qlog-dir"} {
		err := rejectWebProfilingFlags([]string{name})
		if err == nil || !strings.Contains(err.Error(), "command-line only") {
			t.Fatalf("flag %q error = %v", name, err)
		}
	}
}
