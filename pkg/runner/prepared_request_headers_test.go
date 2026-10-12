package runner

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
	quichttp3 "github.com/quic-go/quic-go/http3"
)

func TestConfigurePreparedRequestHeadersQualificationGates(t *testing.T) {
	disabledPolicy := &ffuf.HighRatePolicyReport{Enabled: false, Acknowledged: false}
	unacknowledgedPolicy := &ffuf.HighRatePolicyReport{Enabled: true, Acknowledged: false}
	tests := []struct {
		name       string
		selected   bool
		pool       bool
		mode       responseOptimizationMode
		reliable   bool
		replay     bool
		telemetry  bool
		policy     *ffuf.HighRatePolicyReport
		wantActive bool
	}{
		{name: "eligible default policy", selected: true, pool: true, mode: responseOptimizationHEAD, reliable: true, wantActive: true},
		{name: "eligible disabled policy", selected: true, pool: true, mode: responseOptimizationHEAD, reliable: true, policy: disabledPolicy, wantActive: true},
		{name: "eligible unacknowledged policy", selected: true, pool: true, mode: responseOptimizationHEAD, reliable: true, telemetry: true, policy: unacknowledgedPolicy, wantActive: true},
		{name: "HTTP3 not selected", pool: true, mode: responseOptimizationHEAD, reliable: true},
		{name: "HTTP3 pool absent", selected: true, mode: responseOptimizationHEAD, reliable: true},
		{name: "GET optimization", selected: true, pool: true, mode: responseOptimizationGET, reliable: true},
		{name: "Range optimization", selected: true, pool: true, mode: responseOptimizationRange, reliable: true},
		{name: "HEAD not reliable", selected: true, pool: true, mode: responseOptimizationHEAD},
		{name: "replay runner", selected: true, pool: true, mode: responseOptimizationHEAD, reliable: true, replay: true, telemetry: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			conf := ffuf.NewConfig(ctx, cancel)
			conf.Http3Selected = test.selected
			conf.HighRatePolicy = test.policy
			conf.Method = http.MethodGet
			conf.Timeout = 60
			if test.telemetry {
				conf.RunTelemetry = "test-only"
			}
			runner := NewSimpleRunner(&conf, test.replay).(*SimpleRunner)
			if test.pool {
				runner.h3Pool = newA42TestHTTP3Pool(&conf)
			}
			runner.optimization = responseOptimizationReport{mode: test.mode, headReliable: test.reliable}
			runner.configurePreparedRequestHeaders()
			t.Cleanup(func() {
				if err := runner.Close(); err != nil {
					t.Errorf("Close: %v", err)
				}
			})

			if got := runner.preparedHeaderRoot != nil; got != test.wantActive {
				t.Fatalf("prepared root active = %t, want %t", got, test.wantActive)
			}
			if got := runner.preparedDeadlines != nil; got != test.wantActive {
				t.Fatalf("prepared deadline manager active = %t, want %t", got, test.wantActive)
			}
			if !test.wantActive {
				return
			}
			if !a42ContextHasPreparedMarker(runner.preparedHeaderRoot) {
				t.Fatal("qualified prepared root does not retain the fork marker")
			}
			_, accounting := quichttp3.SnapshotPreparedRequestHeaders(runner.preparedHeaderRoot)
			if accounting != test.telemetry {
				t.Fatalf("prepared accounting enabled = %t, want %t", accounting, test.telemetry)
			}
			got := runner.RunTelemetry().Transport.PreparedRequestHeaders
			if !got.Enabled || got.AccountingEnabled != test.telemetry {
				t.Fatalf("run telemetry prepared state = %+v", got)
			}
		})
	}

	t.Run("nil receiver", func(t *testing.T) {
		var runner *SimpleRunner
		runner.configurePreparedRequestHeaders()
	})
	t.Run("nil config", func(t *testing.T) {
		(&SimpleRunner{}).configurePreparedRequestHeaders()
	})
}

func TestPreparedRequestContextRetainsMarkerWithoutHotPathAllocations(t *testing.T) {
	for _, telemetry := range []bool{false, true} {
		name := "marker-only"
		if telemetry {
			name = "telemetry"
		}
		t.Run(name, func(t *testing.T) {
			runner, _ := newA42PreparedRunner(t, telemetry, 60)
			runner.preparedDeadlines.quantum = time.Second

			prepared := runner.preparedRequestContext()
			if !a42ContextHasPreparedMarker(prepared) {
				t.Fatal("prepared deadline context lost the fork marker")
			}
			_, accounting := quichttp3.SnapshotPreparedRequestHeaders(prepared)
			if accounting != telemetry {
				t.Fatalf("derived accounting enabled = %t, want %t", accounting, telemetry)
			}

			generic := runner.requestContext()
			if a42ContextHasPreparedMarker(generic) {
				t.Fatal("generic deadline context unexpectedly carries the prepared marker")
			}
			if _, ok := quichttp3.SnapshotPreparedRequestHeaders(generic); ok {
				t.Fatal("generic deadline context unexpectedly carries prepared accounting")
			}

			allocs := testing.AllocsPerRun(1000, func() {
				_ = runner.preparedRequestContext()
			})
			if allocs != 0 {
				t.Fatalf("prepared deadline fast-path allocations = %.2f, want 0", allocs)
			}
		})
	}
}

func TestPreparedRequestHeadersFallbacksUseGenericContext(t *testing.T) {
	tests := []struct {
		name      string
		firstCall func(*SimpleRunner, *http.Request) (*http.Response, error)
	}{
		{
			name: "transport error",
			firstCall: func(_ *SimpleRunner, _ *http.Request) (*http.Response, error) {
				return nil, errors.New("optimized stream rejected")
			},
		},
		{
			name: "untrustworthy response",
			firstCall: func(runner *SimpleRunner, request *http.Request) (*http.Response, error) {
				runner.optimization.validateRuntimeStatuses = true
				runner.optimization.trustedRuntimeStatuses[http.StatusOK] = true
				return a42HTTPResponse(request, http.StatusMethodNotAllowed, "", 0), nil
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner, conf := newA42PreparedRunner(t, true, 60)
			type observation struct {
				method     string
				prepared   bool
				accounting bool
			}
			var observations []observation
			runner.client.Transport = a42RoundTripFunc(func(request *http.Request) (*http.Response, error) {
				_, accounting := quichttp3.SnapshotPreparedRequestHeaders(request.Context())
				observations = append(observations, observation{
					method:     request.Method,
					prepared:   a42ContextHasPreparedMarker(request.Context()),
					accounting: accounting,
				})
				if len(observations) == 1 {
					return test.firstCall(runner, request)
				}
				return a42HTTPResponse(request, http.StatusOK, "fallback", int64(len("fallback"))), nil
			})

			request := ffuf.NewRequest(conf)
			request.Method = http.MethodGet
			request.Url = "https://example.test/asset"
			response, err := runner.Execute(&request)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if string(response.Data) != "fallback" {
				t.Fatalf("fallback response body = %q", response.Data)
			}
			if len(observations) != 2 {
				t.Fatalf("transport observations = %d, want optimized request plus fallback", len(observations))
			}
			if got := observations[0]; got.method != http.MethodHead || !got.prepared || !got.accounting {
				t.Fatalf("initial request = %+v, want instrumented prepared HEAD", got)
			}
			if got := observations[1]; got.method != http.MethodGet || got.prepared || got.accounting {
				t.Fatalf("fallback request = %+v, want generic GET", got)
			}
		})
	}
}

func TestMapPreparedRequestHeadersRunTelemetry(t *testing.T) {
	stats := quichttp3.PreparedRequestHeadersStats{
		Attempts:            47,
		Hits:                11,
		FallbackQLog:        1,
		FallbackTrace:       2,
		FallbackMethod:      3,
		FallbackBody:        4,
		FallbackCompression: 5,
		FallbackURL:         6,
		FallbackHeaders:     7,
		FallbackQPACK:       8,
	}
	wantReasons := ffuf.RunPreparedRequestHeaderFallbackTelemetry{
		QLog:        1,
		Trace:       2,
		Method:      3,
		Body:        4,
		Compression: 5,
		URL:         6,
		Headers:     7,
		QPACK:       8,
	}
	got := mapPreparedRequestHeadersRunTelemetry(stats)
	if !got.Enabled || !got.AccountingEnabled || got.Attempts != 47 || got.Hits != 11 || got.Fallbacks != 36 {
		t.Fatalf("mapped prepared telemetry = %+v", got)
	}
	if !reflect.DeepEqual(got.FallbackReasons, wantReasons) {
		t.Fatalf("fallback reasons = %+v, want %+v", got.FallbackReasons, wantReasons)
	}
	if got.Attempts != got.Hits+got.Fallbacks {
		t.Fatalf("attempt reconciliation failed: %d != %d + %d", got.Attempts, got.Hits, got.Fallbacks)
	}
	reasonSum := got.FallbackReasons.QLog + got.FallbackReasons.Trace + got.FallbackReasons.Method +
		got.FallbackReasons.Body + got.FallbackReasons.Compression + got.FallbackReasons.URL +
		got.FallbackReasons.Headers + got.FallbackReasons.QPACK
	if got.Fallbacks != reasonSum {
		t.Fatalf("fallback reconciliation failed: %d != %d", got.Fallbacks, reasonSum)
	}

	var inactiveContext context.Context
	inactive := preparedRequestHeadersRunTelemetry(inactiveContext)
	if inactive.Enabled || inactive.AccountingEnabled {
		t.Fatalf("inactive prepared telemetry = %+v", inactive)
	}
	markerOnly := preparedRequestHeadersRunTelemetry(quichttp3.WithPreparedRequestHeaders(context.Background()))
	if !markerOnly.Enabled || markerOnly.AccountingEnabled || markerOnly.Attempts != 0 {
		t.Fatalf("marker-only prepared telemetry = %+v", markerOnly)
	}
	instrumented := preparedRequestHeadersRunTelemetry(quichttp3.WithPreparedRequestHeadersTelemetry(context.Background()))
	if !instrumented.Enabled || !instrumented.AccountingEnabled || instrumented.Attempts != 0 {
		t.Fatalf("instrumented prepared telemetry = %+v", instrumented)
	}
}

func TestSimpleRunnerCloseCancelsBothDeadlineManagersIdempotently(t *testing.T) {
	runner, _ := newA42PreparedRunner(t, true, 60)
	generic := runner.requestContext()
	prepared := runner.preparedRequestContext()
	poolClosed := runner.h3Pool.closed

	if err := runner.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := runner.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	for name, ctx := range map[string]context.Context{"generic": generic, "prepared": prepared} {
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Fatalf("%s deadline context error = %v, want context.Canceled", name, ctx.Err())
		}
	}
	select {
	case <-poolClosed:
	default:
		t.Fatal("HTTP/3 pool was not closed")
	}
	if !runner.closed.Load() {
		t.Fatal("runner closed flag was not set")
	}
	if report := runner.RunTelemetry(); !report.Drain.RunnerClosed {
		t.Fatalf("drain telemetry after Close = %+v", report.Drain)
	}
}

func newA42PreparedRunner(t testing.TB, telemetry bool, timeoutSeconds int) (*SimpleRunner, *ffuf.Config) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	conf := ffuf.NewConfig(ctx, cancel)
	conf.Method = http.MethodGet
	conf.Http3Selected = true
	conf.Timeout = timeoutSeconds
	if telemetry {
		conf.RunTelemetry = "test-only"
	}
	runner := NewSimpleRunner(&conf, false).(*SimpleRunner)
	runner.h3Pool = newA42TestHTTP3Pool(&conf)
	runner.deadlines = newSharedDeadlineManager(configContext(&conf), time.Duration(timeoutSeconds)*time.Second)
	runner.optimization = responseOptimizationReport{mode: responseOptimizationHEAD, headReliable: true}
	runner.configurePreparedRequestHeaders()
	t.Cleanup(func() {
		if err := runner.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
		cancel()
	})
	return runner, &conf
}

func newA42TestHTTP3Pool(conf *ffuf.Config) *http3Pool {
	ctx, cancel := context.WithCancel(configContext(conf))
	return &http3Pool{
		conf:      conf,
		closed:    make(chan struct{}),
		ctx:       ctx,
		cancel:    cancel,
		startDone: make(chan struct{}),
	}
}

func a42ContextHasPreparedMarker(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	return quichttp3.WithPreparedRequestHeaders(ctx) == ctx
}

type a42RoundTripFunc func(*http.Request) (*http.Response, error)

func (f a42RoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func a42HTTPResponse(request *http.Request, status int, body string, length int64) *http.Response {
	return &http.Response{
		Status:        http.StatusText(status),
		StatusCode:    status,
		Proto:         "HTTP/3.0",
		ProtoMajor:    3,
		Header:        http.Header{"Content-Length": []string{strconv.FormatInt(length, 10)}},
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: length,
		Request:       request,
	}
}
