package runner

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

type observedHTTPVersions struct {
	http1 atomic.Int64
	http2 atomic.Int64
}

func (o *observedHTTPVersions) handler(w http.ResponseWriter, r *http.Request) {
	switch r.ProtoMajor {
	case 1:
		o.http1.Add(1)
	case 2:
		o.http2.Add(1)
	}
	w.WriteHeader(http.StatusNoContent)
}

func startProtocolTLSServer(t *testing.T, enableHTTP2 bool) (*httptest.Server, *observedHTTPVersions) {
	t.Helper()
	observed := &observedHTTPVersions{}
	server := httptest.NewUnstartedServer(http.HandlerFunc(observed.handler))
	server.EnableHTTP2 = enableHTTP2
	server.StartTLS()
	t.Cleanup(server.Close)
	return server, observed
}

func newProtocolSelectionRunner(t *testing.T, target string, configure func(*ffuf.Config)) (*SimpleRunner, *ffuf.Config, ffuf.Request) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	conf := ffuf.NewConfig(ctx, cancel)
	conf.Url = target + "/FUZZ"
	conf.Method = http.MethodGet
	conf.Timeout = 2
	conf.Threads = 1
	conf.Http3ProbeTimeout = 1
	conf.Http3MaxConnections = 1
	conf.Http3Streams = 16
	conf.NoResponseOptimization = true
	if configure != nil {
		configure(&conf)
	}

	runner := NewSimpleRunner(&conf, false).(*SimpleRunner)
	t.Cleanup(func() { _ = runner.Close() })
	return runner, &conf, ffuf.BaseRequest(&conf)
}

func executeProtocolSelectionRequest(t *testing.T, runner *SimpleRunner, request ffuf.Request, target string) {
	t.Helper()
	request.Url = target + "/selected"
	response, err := runner.Execute(&request)
	if err != nil {
		t.Fatalf("execute selected transport: %v", err)
	}
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusNoContent)
	}
}

func TestAutomaticProtocolSelectionFallsBackFromHTTP3ToHTTP2(t *testing.T) {
	server, observed := startProtocolTLSServer(t, true)
	runner, conf, request := newProtocolSelectionRunner(t, server.URL, nil)

	if err := runner.Initialize(&request); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if conf.Http3Selected {
		t.Fatal("HTTP/3 was selected even though the local server has no QUIC listener")
	}
	if !conf.ProtocolPreflight.HTTP3.NegotiationProbe.Attempted {
		t.Fatal("automatic mode did not attempt HTTP/3 before selecting HTTP/2")
	}
	if !conf.Http2Selected {
		t.Fatalf("HTTP/2 was not selected during initialization; preflight = %+v", conf.ProtocolPreflight)
	}
	if conf.ProtocolPreflight.SelectedProtocol != "http2" {
		t.Fatalf("preflight selected protocol = %q, want http2", conf.ProtocolPreflight.SelectedProtocol)
	}

	executeProtocolSelectionRequest(t, runner, request, server.URL)
	if observed.http2.Load() == 0 {
		t.Fatal("local server observed no HTTP/2 request")
	}
	if observed.http1.Load() != 0 {
		t.Fatalf("local server observed %d HTTP/1 requests in automatic HTTP/2 mode", observed.http1.Load())
	}
}

func TestAutomaticProtocolSelectionFallsBackFromHTTP3AndHTTP2ToHTTP1(t *testing.T) {
	server, observed := startProtocolTLSServer(t, false)
	runner, conf, request := newProtocolSelectionRunner(t, server.URL, nil)

	if err := runner.Initialize(&request); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if conf.Http3Selected {
		t.Fatal("HTTP/3 was selected even though the local server has no QUIC listener")
	}
	if !conf.ProtocolPreflight.HTTP3.NegotiationProbe.Attempted {
		t.Fatal("automatic mode did not attempt HTTP/3 before selecting HTTP/1")
	}
	if conf.Http2Selected {
		t.Fatal("HTTP/2 was selected even though the local server only supports HTTP/1")
	}
	if conf.ProtocolPreflight.SelectedProtocol != "http1" {
		t.Fatalf("preflight selected protocol = %q, want http1", conf.ProtocolPreflight.SelectedProtocol)
	}

	executeProtocolSelectionRequest(t, runner, request, server.URL)
	if observed.http1.Load() == 0 {
		t.Fatal("local server observed no HTTP/1 request")
	}
	if observed.http2.Load() != 0 {
		t.Fatalf("local server observed %d HTTP/2 requests against an HTTP/1-only endpoint", observed.http2.Load())
	}
}

func TestForcedHTTP1DoesNotNegotiateHTTP2WithCapableServer(t *testing.T) {
	server, observed := startProtocolTLSServer(t, true)
	runner, conf, request := newProtocolSelectionRunner(t, server.URL, func(conf *ffuf.Config) {
		conf.Http1 = true
	})

	if err := runner.Initialize(&request); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if conf.Http3Selected || conf.Http2Selected {
		t.Fatalf("forced HTTP/1 selected a newer protocol: http3=%v http2=%v", conf.Http3Selected, conf.Http2Selected)
	}
	if conf.ProtocolPreflight.HTTP3.NegotiationProbe.Attempted {
		t.Fatal("forced HTTP/1 unexpectedly attempted HTTP/3")
	}
	if conf.ProtocolPreflight.SelectedProtocol != "http1" {
		t.Fatalf("preflight selected protocol = %q, want http1", conf.ProtocolPreflight.SelectedProtocol)
	}
	if runner.tcpTransport.ForceAttemptHTTP2 {
		t.Fatal("forced HTTP/1 transport still has ForceAttemptHTTP2 enabled")
	}
	if runner.tcpTransport.TLSNextProto == nil || len(runner.tcpTransport.TLSNextProto) != 0 {
		t.Fatalf("forced HTTP/1 TLSNextProto = %#v, want a non-nil empty map", runner.tcpTransport.TLSNextProto)
	}

	executeProtocolSelectionRequest(t, runner, request, server.URL)
	if observed.http1.Load() == 0 {
		t.Fatal("HTTP/2-capable server observed no forced HTTP/1 request")
	}
	if observed.http2.Load() != 0 {
		t.Fatalf("HTTP/2-capable server observed %d HTTP/2 requests while HTTP/1 was forced", observed.http2.Load())
	}
}

func TestTCPProtocolModesSkipHTTP3AndAllowHTTP1Fallback(t *testing.T) {
	modes := []struct {
		name      string
		configure func(*ffuf.Config)
	}{
		{
			name: "http2 preference",
			configure: func(conf *ffuf.Config) {
				conf.Http2 = true
			},
		},
		{
			name: "HTTP3 disabled",
			configure: func(conf *ffuf.Config) {
				conf.NoHttp3 = true
			},
		},
	}
	targets := []struct {
		name        string
		enableHTTP2 bool
		wantHTTP2   bool
	}{
		{name: "HTTP2 capable", enableHTTP2: true, wantHTTP2: true},
		{name: "HTTP1 only", enableHTTP2: false, wantHTTP2: false},
	}

	for _, mode := range modes {
		t.Run(mode.name, func(t *testing.T) {
			for _, target := range targets {
				t.Run(target.name, func(t *testing.T) {
					server, observed := startProtocolTLSServer(t, target.enableHTTP2)
					runner, conf, request := newProtocolSelectionRunner(t, server.URL, mode.configure)

					if err := runner.Initialize(&request); err != nil {
						t.Fatalf("initialize: %v", err)
					}
					if conf.ProtocolPreflight.HTTP3.NegotiationProbe.Attempted {
						t.Fatal("TCP protocol mode unexpectedly attempted HTTP/3")
					}
					if conf.Http3Selected {
						t.Fatal("TCP protocol mode unexpectedly selected HTTP/3")
					}
					if conf.Http2Selected != target.wantHTTP2 {
						t.Fatalf("HTTP/2 selected = %v, want %v", conf.Http2Selected, target.wantHTTP2)
					}
					wantProtocol := "http1"
					if target.wantHTTP2 {
						wantProtocol = "http2"
					}
					if conf.ProtocolPreflight.SelectedProtocol != wantProtocol {
						t.Fatalf("preflight selected protocol = %q, want %s", conf.ProtocolPreflight.SelectedProtocol, wantProtocol)
					}

					executeProtocolSelectionRequest(t, runner, request, server.URL)
					if target.wantHTTP2 && (observed.http2.Load() == 0 || observed.http1.Load() != 0) {
						t.Fatalf("observed HTTP versions: http1=%d http2=%d", observed.http1.Load(), observed.http2.Load())
					}
					if !target.wantHTTP2 && (observed.http1.Load() == 0 || observed.http2.Load() != 0) {
						t.Fatalf("observed HTTP versions: http1=%d http2=%d", observed.http1.Load(), observed.http2.Load())
					}
				})
			}
		})
	}
}
