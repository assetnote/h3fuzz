package runner

import (
	"context"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

func TestHTTP3PoolQUICDiagnosticsActivationBoundaries(t *testing.T) {
	target, err := url.Parse("https://example.test/")
	if err != nil {
		t.Fatal(err)
	}
	probe, err := http.NewRequest(http.MethodHead, target.String(), nil)
	if err != nil {
		t.Fatal(err)
	}

	disabled, err := newHTTP3Pool(&ffuf.Config{}, target, probe)
	if err != nil {
		t.Fatal(err)
	}
	defer disabled.Close()
	if disabled.diagnostics != nil {
		t.Fatal("ordinary HTTP/3 pool allocated diagnostics")
	}
	disabledTransport := disabled.newSlotTransport(nil)
	if disabledTransport.QUICConfig.Tracer != nil {
		t.Fatal("ordinary HTTP/3 pool installed a QUIC tracer")
	}

	enabled, err := newHTTP3Pool(&ffuf.Config{RunTelemetry: "run.json"}, target, probe)
	if err != nil {
		t.Fatal(err)
	}
	defer enabled.Close()
	if enabled.diagnostics == nil || enabled.diagnostics.aggregate {
		t.Fatalf("certification diagnostics = %+v, want lifecycle-only", enabled.diagnostics)
	}
	enabledTransport := enabled.newSlotTransport(newQUICSlotDiagnostics(0))
	if enabledTransport.QUICConfig.Tracer == nil {
		t.Fatal("certification HTTP/3 pool did not install lifecycle tracer")
	}
	if !enabledTransport.QUICConfig.EnableDatagrams || !enabledTransport.EnableDatagrams {
		t.Fatal("PERF-006 changed the current QUIC / HTTP/3 datagram defaults before the B0 decision")
	}

	aggregate, err := newHTTP3Pool(&ffuf.Config{RunTelemetry: "run.json", QuicTelemetry: true}, target, probe)
	if err != nil {
		t.Fatal(err)
	}
	defer aggregate.Close()
	if aggregate.diagnostics == nil || !aggregate.diagnostics.aggregate {
		t.Fatal("-quic-telemetry did not enable bounded aggregates")
	}
}

func TestHTTP3PoolQUICAttemptLedgerExcludesNonMainRequests(t *testing.T) {
	diagnostics, err := newQUICDiagnostics(false, "")
	if err != nil {
		t.Fatal(err)
	}
	slotDiagnostics := newQUICSlotDiagnostics(0)
	pool := &http3Pool{diagnostics: diagnostics}
	slot := &http3PoolSlot{diagnostics: slotDiagnostics}

	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.test/", nil)
	if err != nil {
		t.Fatal(err)
	}
	background, observation := pool.instrumentQUICRequest(slot, request)
	if observation != nil {
		t.Fatal("background/preflight request received main-attempt observation")
	}
	backgroundTrace := httptrace.ContextClientTrace(background.Context())
	if backgroundTrace.GetConn != nil {
		backgroundTrace.GetConn("example.test:443")
	}
	backgroundTrace.ConnectStart("udp", "127.0.0.1:443")
	if got := diagnostics.snapshot([]*quicSlotDiagnostics{slotDiagnostics}); got.internalAttempts == nil || *got.internalAttempts != 0 || got.quic.DialAttempts != 1 {
		t.Fatalf("background request attribution = %+v", got)
	}

	mainRequest := markQUICApplicationAttempt(request)
	main, observation := pool.instrumentQUICRequest(slot, mainRequest)
	if observation == nil {
		t.Fatal("main request did not receive attempt observation")
	}
	mainTrace := httptrace.ContextClientTrace(main.Context())
	mainTrace.GetConn("example.test:443")
	mainTrace.WroteRequest(httptrace.WroteRequestInfo{})
	observation.finish(nil)
	if got := diagnostics.snapshot([]*quicSlotDiagnostics{slotDiagnostics}); got.internalAttempts == nil || *got.internalAttempts != 1 || got.quic.DialAttempts != 1 {
		t.Fatalf("main request attribution = %+v", got)
	}
}

func TestRequestAttemptContextComposesApplicationMarkerAndTrace(t *testing.T) {
	trace := &httptrace.ClientTrace{}
	for _, test := range []struct {
		name   string
		marked bool
	}{
		{name: "main application attempt", marked: true},
		{name: "background request", marked: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := requestAttemptContext(context.Background(), trace, test.marked)
			req, err := http.NewRequestWithContext(ctx, http.MethodHead, "https://example.test/", nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := isQUICApplicationAttempt(req); got != test.marked {
				t.Fatalf("application marker = %t, want %t", got, test.marked)
			}
			if got := httptrace.ContextClientTrace(req.Context()); got != trace {
				t.Fatalf("client trace = %p, want %p", got, trace)
			}
		})
	}
}

func TestHTTP3PoolRejectsInvalidQlogDirectoryBeforeDialing(t *testing.T) {
	target, err := url.Parse("https://example.test/")
	if err != nil {
		t.Fatal(err)
	}
	probe, err := http.NewRequest(http.MethodHead, target.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(path, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newHTTP3Pool(&ffuf.Config{RunTelemetry: "run.json", QuicQlogDir: path}, target, probe); err == nil {
		t.Fatal("newHTTP3Pool accepted a qlog file as a directory")
	}
}
