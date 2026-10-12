package runner

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

type countingResponseBody struct {
	remaining int
	read      int
	closed    bool
}

func (b *countingResponseBody) Read(dst []byte) (int, error) {
	if b.remaining == 0 {
		return 0, io.EOF
	}
	n := len(dst)
	if n > b.remaining {
		n = b.remaining
	}
	for i := 0; i < n; i++ {
		dst[i] = 'x'
	}
	b.remaining -= n
	b.read += n
	return n, nil
}

func (b *countingResponseBody) Close() error {
	b.closed = true
	return nil
}

func optimizedExecuteRunner(t testing.TB, mode responseOptimizationMode) (*SimpleRunner, *ffuf.Config, ffuf.Request) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	conf := ffuf.NewConfig(ctx, cancel)
	conf.Method = http.MethodGet
	conf.RunTelemetry = "test-only"
	runner := NewSimpleRunner(&conf, false).(*SimpleRunner)
	runner.optimization = responseOptimizationReport{mode: mode}
	req := ffuf.NewRequest(&conf)
	req.Method = http.MethodGet
	req.Url = "https://example.test/asset"
	return runner, &conf, req
}

func optimizedHTTPResponse(req *http.Request, status int, header http.Header, body io.ReadCloser, length int64) *http.Response {
	return &http.Response{
		Status:        strconv.Itoa(status) + " test",
		StatusCode:    status,
		Proto:         "HTTP/3.0",
		ProtoMajor:    3,
		Header:        header,
		Body:          body,
		ContentLength: length,
		Request:       req,
	}
}

func TestExecuteOptimizedValidationReadsOnlyModeMinimum(t *testing.T) {
	tests := []struct {
		name      string
		mode      responseOptimizationMode
		status    int
		header    http.Header
		bodySize  int
		wantRead  int
		configure func(*responseOptimizationReport)
	}{
		{
			name:     "HEAD unexpected body reads one byte",
			mode:     responseOptimizationHEAD,
			status:   http.StatusOK,
			header:   http.Header{"Content-Length": []string{"65536"}},
			bodySize: 64 << 10,
			wantRead: 1,
		},
		{
			name:     "Range ignored 200 falls back from headers",
			mode:     responseOptimizationRange,
			status:   http.StatusOK,
			header:   http.Header{"Content-Length": []string{"65536"}},
			bodySize: 64 << 10,
			wantRead: 0,
		},
		{
			name:     "Range oversized trusted miss reads bounded proof",
			mode:     responseOptimizationRange,
			status:   http.StatusNotFound,
			header:   http.Header{"Content-Length": []string{"65536"}},
			bodySize: 64 << 10,
			wantRead: responseOptimizationMaxMissBody + 1,
			configure: func(report *responseOptimizationReport) {
				report.validateRuntimeStatuses = true
				report.trustedRuntimeStatuses[http.StatusNotFound] = true
			},
		},
		{
			name:     "HEAD unseen 405 falls back without body read",
			mode:     responseOptimizationHEAD,
			status:   http.StatusMethodNotAllowed,
			header:   http.Header{"Content-Length": []string{"0"}},
			bodySize: 0,
			wantRead: 0,
			configure: func(report *responseOptimizationReport) {
				report.validateRuntimeStatuses = true
				report.trustedRuntimeStatuses[http.StatusOK] = true
			},
		},
		{
			name:     "Range unseen 400 falls back without body read",
			mode:     responseOptimizationRange,
			status:   http.StatusBadRequest,
			header:   http.Header{"Content-Length": []string{"0"}},
			bodySize: 0,
			wantRead: 0,
			configure: func(report *responseOptimizationReport) {
				report.validateRuntimeStatuses = true
				report.trustedRuntimeStatuses[http.StatusPartialContent] = true
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner, _, req := optimizedExecuteRunner(t, test.mode)
			if test.configure != nil {
				test.configure(&runner.optimization)
			}
			optimizedBody := &countingResponseBody{remaining: test.bodySize}
			calls := 0
			runner.client.Transport = testRoundTripFunc(func(httpReq *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					return optimizedHTTPResponse(httpReq, test.status, test.header.Clone(), optimizedBody, int64(test.bodySize)), nil
				}
				return optimizedHTTPResponse(
					httpReq,
					http.StatusOK,
					http.Header{"Content-Length": []string{"8"}, "Content-Type": []string{"text/plain"}},
					io.NopCloser(bytes.NewBufferString("fallback")),
					8,
				), nil
			})

			resp, err := runner.Execute(&req)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if calls != 2 || string(resp.Data) != "fallback" {
				t.Fatalf("fallback result: calls=%d body=%q", calls, resp.Data)
			}
			if resp.StatusCode != http.StatusOK || resp.ContentLength != 8 || resp.ContentType != "text/plain" {
				t.Fatalf("fallback logical response = status %d length %d type %q", resp.StatusCode, resp.ContentLength, resp.ContentType)
			}
			if optimizedBody.read != test.wantRead {
				t.Fatalf("optimized body bytes read = %d, want %d", optimizedBody.read, test.wantRead)
			}
			if !optimizedBody.closed {
				t.Fatal("optimized body was not closed before fallback")
			}
			telemetry := runner.RunTelemetry().RequestStrategy
			if telemetry.OptimizedAttempts != 1 || telemetry.OptimizedSuccesses != 0 || telemetry.FullGETFallbacks != 1 || telemetry.FallbackUntrustworthy != 1 || telemetry.FallbackTransportErrors != 0 {
				t.Fatalf("optimization telemetry = %+v", telemetry)
			}
		})
	}
}

func TestExecuteOptimizedBuildsLogicalResponseDirectly(t *testing.T) {
	tests := []struct {
		name              string
		mode              responseOptimizationMode
		status            int
		header            http.Header
		body              string
		declaredLength    int64
		wantStatus        int64
		wantContentLength int64
	}{
		{
			name:              "HEAD",
			mode:              responseOptimizationHEAD,
			status:            http.StatusOK,
			header:            http.Header{"Content-Type": []string{"text/plain"}, "X-Test": []string{"head"}},
			declaredLength:    17,
			wantStatus:        http.StatusOK,
			wantContentLength: 17,
		},
		{
			name:              "Range",
			mode:              responseOptimizationRange,
			status:            http.StatusPartialContent,
			header:            http.Header{"Content-Length": []string{"1"}, "Content-Range": []string{"bytes 16-16/17"}, "Content-Type": []string{"text/plain"}, "X-Test": []string{"range"}},
			body:              "x",
			declaredLength:    1,
			wantStatus:        http.StatusOK,
			wantContentLength: 17,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner, conf, req := optimizedExecuteRunner(t, test.mode)
			conf.IgnoreBody = true
			body := &countingResponseBody{remaining: len(test.body)}
			runner.client.Transport = testRoundTripFunc(func(httpReq *http.Request) (*http.Response, error) {
				return optimizedHTTPResponse(httpReq, test.status, test.header.Clone(), body, test.declaredLength), nil
			})

			resp, err := runner.Execute(&req)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if resp.StatusCode != test.wantStatus || resp.ContentLength != test.wantContentLength {
				t.Fatalf("logical metadata = status %d length %d", resp.StatusCode, resp.ContentLength)
			}
			if resp.ContentType != "text/plain" || len(resp.Headers["X-Test"]) == 0 || resp.Headers["X-Test"][0] == "" {
				t.Fatalf("response headers/content type not preserved: type=%q headers=%v", resp.ContentType, resp.Headers)
			}
			if test.mode == responseOptimizationHEAD && (len(resp.Headers["Content-Length"]) == 0 || resp.Headers["Content-Length"][0] != "17") {
				t.Fatalf("HEAD Content-Length metadata not preserved: headers=%v", resp.Headers)
			}
			if resp.Request != &req || resp.Request.Timestamp.IsZero() || !resp.Timestamp.Equal(resp.Request.Timestamp.Add(resp.Duration)) {
				t.Fatalf("request/timestamp metadata not preserved: response=%+v request=%+v", resp.Timestamp, resp.Request.Timestamp)
			}
			if resp.Data != nil || resp.ContentWords != 0 || resp.ContentLines != 0 || !resp.Cancelled {
				t.Fatalf("optimized body semantics changed: data=%q words=%d lines=%d cancelled=%t", resp.Data, resp.ContentWords, resp.ContentLines, resp.Cancelled)
			}
			if !body.closed {
				t.Fatal("optimized response body was not closed")
			}
			telemetry := runner.RunTelemetry().RequestStrategy
			if telemetry.OptimizedAttempts != 1 || telemetry.OptimizedSuccesses != 1 || telemetry.FullGETFallbacks != 0 {
				t.Fatalf("optimization telemetry = %+v", telemetry)
			}
		})
	}
}

func TestExecuteOptimizedTimingUsesRoundTripToHeadersWithoutTimingTrace(t *testing.T) {
	runner, _, req := optimizedExecuteRunner(t, responseOptimizationHEAD)
	runner.telemetryOn = false
	var transportEntered, transportReturned time.Time
	runner.client.Transport = testRoundTripFunc(func(httpReq *http.Request) (*http.Response, error) {
		if trace := httptrace.ContextClientTrace(httpReq.Context()); trace != nil {
			t.Fatalf("optimized request unexpectedly installed timing trace: %+v", trace)
		}
		transportEntered = time.Now()
		time.Sleep(time.Millisecond)
		transportReturned = time.Now()
		return optimizedHTTPResponse(httpReq, http.StatusOK, http.Header{
			"Content-Length": []string{"128"},
			"Content-Type":   []string{"text/plain"},
		}, http.NoBody, 128), nil
	})

	resp, err := runner.Execute(&req)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if resp.Duration < transportReturned.Sub(transportEntered) {
		t.Fatalf("optimized duration %s does not cover RoundTrip interval %s", resp.Duration, transportReturned.Sub(transportEntered))
	}
	if req.Timestamp.After(transportEntered) {
		t.Fatalf("optimized request timestamp %s is after transport entry %s", req.Timestamp, transportEntered)
	}
	if resp.Timestamp.Before(transportReturned) || !resp.Timestamp.Equal(req.Timestamp.Add(resp.Duration)) {
		t.Fatalf("optimized response timestamp %s does not represent returned headers (transport returned %s)", resp.Timestamp, transportReturned)
	}
}

func TestExecuteOptimizedTelemetryTimingRetainsWriteToFirstByteTrace(t *testing.T) {
	runner, _, req := optimizedExecuteRunner(t, responseOptimizationHEAD)
	if !runner.telemetryOn {
		t.Fatal("test requires run telemetry")
	}
	var beforeWrite, afterWrite, beforeFirstByte, afterFirstByte time.Time
	runner.client.Transport = testRoundTripFunc(func(httpReq *http.Request) (*http.Response, error) {
		trace := httptrace.ContextClientTrace(httpReq.Context())
		if trace == nil || trace.WroteRequest == nil || trace.GotFirstResponseByte == nil {
			t.Fatalf("telemetry-on optimized trace = %+v, want exact timing callbacks", trace)
		}
		beforeWrite = time.Now()
		trace.WroteRequest(httptrace.WroteRequestInfo{})
		afterWrite = time.Now()
		beforeFirstByte = time.Now()
		trace.GotFirstResponseByte()
		afterFirstByte = time.Now()
		return optimizedHTTPResponse(httpReq, http.StatusOK, http.Header{
			"Content-Length": []string{"128"},
			"Content-Type":   []string{"text/plain"},
		}, http.NoBody, 128), nil
	})

	resp, err := runner.Execute(&req)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if req.Timestamp.Before(beforeWrite) || req.Timestamp.After(afterWrite) {
		t.Fatalf("telemetry request timestamp %s not captured by WroteRequest interval [%s, %s]", req.Timestamp, beforeWrite, afterWrite)
	}
	if resp.Timestamp.Before(beforeFirstByte) || resp.Timestamp.After(afterFirstByte) {
		t.Fatalf("telemetry response timestamp %s not captured by GotFirstResponseByte interval [%s, %s]", resp.Timestamp, beforeFirstByte, afterFirstByte)
	}
}

func TestExecuteGenericTimingRetainsWriteToFirstByteSemantics(t *testing.T) {
	runner, _, req := optimizedExecuteRunner(t, responseOptimizationGET)
	runner.telemetryOn = false
	var beforeWrite, afterWrite, beforeFirstByte, afterFirstByte time.Time
	runner.client.Transport = testRoundTripFunc(func(httpReq *http.Request) (*http.Response, error) {
		trace := httptrace.ContextClientTrace(httpReq.Context())
		if trace == nil || trace.WroteRequest == nil || trace.GotFirstResponseByte == nil {
			t.Fatalf("generic request trace = %+v, want exact timing callbacks", trace)
		}
		beforeWrite = time.Now()
		trace.WroteRequest(httptrace.WroteRequestInfo{})
		afterWrite = time.Now()
		beforeFirstByte = time.Now()
		trace.GotFirstResponseByte()
		afterFirstByte = time.Now()
		return optimizedHTTPResponse(httpReq, http.StatusOK, http.Header{
			"Content-Length": []string{"0"},
			"Content-Type":   []string{"text/plain"},
		}, http.NoBody, 0), nil
	})

	resp, err := runner.Execute(&req)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if req.Timestamp.Before(beforeWrite) || req.Timestamp.After(afterWrite) {
		t.Fatalf("generic request timestamp %s not captured by WroteRequest interval [%s, %s]", req.Timestamp, beforeWrite, afterWrite)
	}
	if resp.Timestamp.Before(beforeFirstByte) || resp.Timestamp.After(afterFirstByte) {
		t.Fatalf("generic response timestamp %s not captured by GotFirstResponseByte interval [%s, %s]", resp.Timestamp, beforeFirstByte, afterFirstByte)
	}
	if !resp.Timestamp.Equal(req.Timestamp.Add(resp.Duration)) {
		t.Fatalf("generic timestamp relation changed: request=%s duration=%s response=%s", req.Timestamp, resp.Duration, resp.Timestamp)
	}
}

func TestExecuteGenericTimingWithoutCallbacksKeepsZeroDuration(t *testing.T) {
	runner, _, req := optimizedExecuteRunner(t, responseOptimizationGET)
	runner.telemetryOn = false
	var transportEntered time.Time
	runner.client.Transport = testRoundTripFunc(func(httpReq *http.Request) (*http.Response, error) {
		trace := httptrace.ContextClientTrace(httpReq.Context())
		if trace == nil || trace.WroteRequest == nil || trace.GotFirstResponseByte == nil {
			t.Fatalf("generic request trace = %+v, want exact timing callbacks", trace)
		}
		transportEntered = time.Now()
		return optimizedHTTPResponse(httpReq, http.StatusOK, http.Header{
			"Content-Length": []string{"0"},
		}, http.NoBody, 0), nil
	})

	resp, err := runner.Execute(&req)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if resp.Duration != 0 {
		t.Fatalf("duration without callbacks = %s, want zero", resp.Duration)
	}
	if req.Timestamp.IsZero() || req.Timestamp.After(transportEntered) {
		t.Fatalf("seeded request timestamp %s is zero or after transport entry %s", req.Timestamp, transportEntered)
	}
	if !resp.Timestamp.Equal(req.Timestamp) {
		t.Fatalf("zero-duration response timestamp %s != request timestamp %s", resp.Timestamp, req.Timestamp)
	}
}

func TestExactRequestTimingConcurrentAndPostReturnCallbacksAreRaceSafe(t *testing.T) {
	runner, _, req := optimizedExecuteRunner(t, responseOptimizationGET)
	runner.telemetryOn = false
	var callbacks sync.WaitGroup
	var trace *httptrace.ClientTrace
	runner.client.Transport = testRoundTripFunc(func(httpReq *http.Request) (*http.Response, error) {
		trace = httptrace.ContextClientTrace(httpReq.Context())
		if trace == nil || trace.WroteRequest == nil || trace.GotFirstResponseByte == nil {
			t.Fatalf("generic request trace = %+v, want exact timing callbacks", trace)
		}
		start := make(chan struct{})
		for worker := 0; worker < 8; worker++ {
			callbacks.Add(1)
			go func(firstByteFirst bool) {
				defer callbacks.Done()
				<-start
				for iteration := 0; iteration < 100; iteration++ {
					if firstByteFirst {
						trace.GotFirstResponseByte()
						trace.WroteRequest(httptrace.WroteRequestInfo{})
					} else {
						trace.WroteRequest(httptrace.WroteRequestInfo{})
						trace.GotFirstResponseByte()
					}
				}
			}(worker%2 == 0)
		}
		close(start)
		return optimizedHTTPResponse(httpReq, http.StatusOK, http.Header{
			"Content-Length": []string{"0"},
		}, http.NoBody, 0), nil
	})

	resp, err := runner.Execute(&req)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	duration, requestTimestamp, responseTimestamp := resp.Duration, req.Timestamp, resp.Timestamp
	callbacks.Wait()
	// httptrace explicitly permits callbacks after RoundTrip returns. Exercise
	// both orders after Execute has already frozen its response snapshot.
	trace.GotFirstResponseByte()
	trace.WroteRequest(httptrace.WroteRequestInfo{})
	if resp.Duration != duration || !req.Timestamp.Equal(requestTimestamp) || !resp.Timestamp.Equal(responseTimestamp) {
		t.Fatalf("post-return callbacks mutated response snapshot: before=(%s,%s,%s) after=(%s,%s,%s)",
			duration, requestTimestamp, responseTimestamp, resp.Duration, req.Timestamp, resp.Timestamp)
	}
	if resp.Duration < 0 || !resp.Timestamp.Equal(req.Timestamp.Add(resp.Duration)) {
		t.Fatalf("invalid timing snapshot: request=%s duration=%s response=%s", req.Timestamp, resp.Duration, resp.Timestamp)
	}
}

func TestExactRequestTimingTracksLatestCompletedHopAndFreezes(t *testing.T) {
	timing, trace := newExactRequestTiming()
	seed := time.Now().Add(-5 * time.Millisecond)
	timing.beginAt(seed)
	trace.GotFirstResponseByte()
	first := timing.snapshot()
	if !first.firstByteSeen || first.firstByteTime <= 0 {
		t.Fatalf("first-hop snapshot = %+v, want recorded positive duration", first)
	}

	// A duplicate first-byte callback has no pending write and must not replace
	// the completed hop.
	trace.GotFirstResponseByte()
	if duplicate := timing.snapshot(); duplicate != first {
		t.Fatalf("duplicate GotFirstResponseByte changed completed interval: first=%+v duplicate=%+v", first, duplicate)
	}

	// A redirect's write opens a pending hop, but cannot erase the last completed
	// interval unless its own first-byte callback arrives.
	trace.WroteRequest(httptrace.WroteRequestInfo{})
	if pending := timing.snapshot(); pending != first {
		t.Fatalf("lone WroteRequest erased completed interval: first=%+v pending=%+v", first, pending)
	}
	time.Sleep(time.Millisecond)
	trace.GotFirstResponseByte()
	second := timing.snapshot()
	if !second.firstByteSeen || !second.start.After(first.start) || second.firstByteTime <= 0 {
		t.Fatalf("second-hop snapshot = %+v, want a later completed positive interval than %+v", second, first)
	}

	frozen := timing.freeze()
	trace.WroteRequest(httptrace.WroteRequestInfo{})
	trace.GotFirstResponseByte()
	if afterFreeze := timing.snapshot(); afterFreeze != frozen {
		t.Fatalf("post-freeze callbacks changed completed interval: frozen=%+v after=%+v", frozen, afterFreeze)
	}
}

func TestExecuteFollowRedirectsUsesFinalHopTiming(t *testing.T) {
	const finalHopDelay = 50 * time.Millisecond
	firstHopReceived := make(chan time.Time, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/start":
			firstHopReceived <- time.Now()
			http.Redirect(w, req, "/final", http.StatusFound)
		case "/final":
			time.Sleep(finalHopDelay)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, req)
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conf := ffuf.NewConfig(ctx, cancel)
	conf.Method = http.MethodGet
	conf.FollowRedirects = true
	runner := NewSimpleRunner(&conf, false).(*SimpleRunner)
	req := ffuf.NewRequest(&conf)
	req.Method = http.MethodGet
	req.Url = server.URL + "/start"

	resp, err := runner.Execute(&req)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want followed final status %d", resp.StatusCode, http.StatusNoContent)
	}
	firstHopStarted := <-firstHopReceived
	if !req.Timestamp.After(firstHopStarted) {
		t.Fatalf("request timestamp %s did not advance past first-hop receipt %s", req.Timestamp, firstHopStarted)
	}
	if resp.Duration < finalHopDelay {
		t.Fatalf("duration = %s, want final-hop delay of at least %s", resp.Duration, finalHopDelay)
	}
	if !resp.Timestamp.Equal(req.Timestamp.Add(resp.Duration)) {
		t.Fatalf("timestamp relation changed: request=%s duration=%s response=%s", req.Timestamp, resp.Duration, resp.Timestamp)
	}
}

func TestExecuteOptimizedFallbackRestoresExactTimingTrace(t *testing.T) {
	runner, _, req := optimizedExecuteRunner(t, responseOptimizationHEAD)
	runner.telemetryOn = false
	runner.optimization.validateRuntimeStatuses = true
	var calls int
	var beforeWrite, afterWrite, beforeFirstByte, afterFirstByte time.Time
	runner.client.Transport = testRoundTripFunc(func(httpReq *http.Request) (*http.Response, error) {
		calls++
		trace := httptrace.ContextClientTrace(httpReq.Context())
		if calls == 1 {
			if trace != nil {
				t.Fatalf("optimized attempt unexpectedly installed timing trace: %+v", trace)
			}
			return optimizedHTTPResponse(httpReq, http.StatusMethodNotAllowed, http.Header{
				"Content-Length": []string{"0"},
			}, http.NoBody, 0), nil
		}
		if httpReq.Method != http.MethodGet {
			t.Fatalf("fallback method = %s, want GET", httpReq.Method)
		}
		if trace == nil || trace.WroteRequest == nil || trace.GotFirstResponseByte == nil {
			t.Fatalf("fallback trace = %+v, want exact timing callbacks", trace)
		}
		beforeWrite = time.Now()
		trace.WroteRequest(httptrace.WroteRequestInfo{})
		afterWrite = time.Now()
		beforeFirstByte = time.Now()
		trace.GotFirstResponseByte()
		afterFirstByte = time.Now()
		return optimizedHTTPResponse(httpReq, http.StatusOK, http.Header{
			"Content-Length": []string{"2"},
			"Content-Type":   []string{"text/plain"},
		}, io.NopCloser(bytes.NewBufferString("ok")), 2), nil
	})

	resp, err := runner.Execute(&req)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if calls != 2 {
		t.Fatalf("transport calls = %d, want optimized attempt plus fallback", calls)
	}
	if req.Timestamp.Before(beforeWrite) || req.Timestamp.After(afterWrite) {
		t.Fatalf("fallback request timestamp %s not captured by WroteRequest interval [%s, %s]", req.Timestamp, beforeWrite, afterWrite)
	}
	if resp.Timestamp.Before(beforeFirstByte) || resp.Timestamp.After(afterFirstByte) {
		t.Fatalf("fallback response timestamp %s not captured by GotFirstResponseByte interval [%s, %s]", resp.Timestamp, beforeFirstByte, afterFirstByte)
	}
}

func TestExecuteOptimizedTransportFallbackRestoresExactTimingTrace(t *testing.T) {
	runner, _, req := optimizedExecuteRunner(t, responseOptimizationHEAD)
	runner.telemetryOn = false
	var calls int
	var beforeWrite, afterWrite, beforeFirstByte, afterFirstByte time.Time
	runner.client.Transport = testRoundTripFunc(func(httpReq *http.Request) (*http.Response, error) {
		calls++
		trace := httptrace.ContextClientTrace(httpReq.Context())
		if calls == 1 {
			if trace != nil {
				t.Fatalf("optimized attempt unexpectedly installed timing trace: %+v", trace)
			}
			return nil, errors.New("optimized attempt failed")
		}
		if httpReq.Method != http.MethodGet {
			t.Fatalf("fallback method = %s, want GET", httpReq.Method)
		}
		if trace == nil || trace.WroteRequest == nil || trace.GotFirstResponseByte == nil {
			t.Fatalf("transport fallback trace = %+v, want exact timing callbacks", trace)
		}
		beforeWrite = time.Now()
		trace.WroteRequest(httptrace.WroteRequestInfo{})
		afterWrite = time.Now()
		beforeFirstByte = time.Now()
		trace.GotFirstResponseByte()
		afterFirstByte = time.Now()
		return optimizedHTTPResponse(httpReq, http.StatusOK, http.Header{
			"Content-Length": []string{"2"},
			"Content-Type":   []string{"text/plain"},
		}, io.NopCloser(bytes.NewBufferString("ok")), 2), nil
	})

	resp, err := runner.Execute(&req)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if calls != 2 {
		t.Fatalf("transport calls = %d, want optimized attempt plus fallback", calls)
	}
	if req.Timestamp.Before(beforeWrite) || req.Timestamp.After(afterWrite) {
		t.Fatalf("transport fallback request timestamp %s not captured by WroteRequest interval [%s, %s]", req.Timestamp, beforeWrite, afterWrite)
	}
	if resp.Timestamp.Before(beforeFirstByte) || resp.Timestamp.After(afterFirstByte) {
		t.Fatalf("transport fallback response timestamp %s not captured by GotFirstResponseByte interval [%s, %s]", resp.Timestamp, beforeFirstByte, afterFirstByte)
	}
}

func TestExecuteOptimizedTelemetryFallbackIsolatesLateInitialCallbacks(t *testing.T) {
	runner, _, req := optimizedExecuteRunner(t, responseOptimizationHEAD)
	if !runner.telemetryOn {
		t.Fatal("test requires run telemetry")
	}
	var calls int
	var initialTrace *httptrace.ClientTrace
	var beforeWrite, afterWrite, beforeFirstByte, afterFirstByte time.Time
	runner.client.Transport = testRoundTripFunc(func(httpReq *http.Request) (*http.Response, error) {
		calls++
		trace := httptrace.ContextClientTrace(httpReq.Context())
		if trace == nil || trace.WroteRequest == nil || trace.GotFirstResponseByte == nil {
			t.Fatalf("attempt %d trace = %+v, want exact timing callbacks", calls, trace)
		}
		if calls == 1 {
			initialTrace = trace
			return nil, errors.New("optimized attempt failed")
		}
		if trace == initialTrace {
			t.Fatal("fallback reused timing trace that can receive late initial-attempt callbacks")
		}
		beforeWrite = time.Now()
		trace.WroteRequest(httptrace.WroteRequestInfo{})
		afterWrite = time.Now()
		beforeFirstByte = time.Now()
		trace.GotFirstResponseByte()
		afterFirstByte = time.Now()
		// These callbacks belong to the failed attempt and arrive after the
		// fallback interval completed. They must update only its retired state.
		initialTrace.GotFirstResponseByte()
		initialTrace.WroteRequest(httptrace.WroteRequestInfo{})
		return optimizedHTTPResponse(httpReq, http.StatusOK, http.Header{
			"Content-Length": []string{"2"},
			"Content-Type":   []string{"text/plain"},
		}, io.NopCloser(bytes.NewBufferString("ok")), 2), nil
	})

	resp, err := runner.Execute(&req)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if calls != 2 {
		t.Fatalf("transport calls = %d, want optimized attempt plus fallback", calls)
	}
	if req.Timestamp.Before(beforeWrite) || req.Timestamp.After(afterWrite) {
		t.Fatalf("fallback request timestamp %s not captured by its WroteRequest interval [%s, %s]", req.Timestamp, beforeWrite, afterWrite)
	}
	if resp.Timestamp.Before(beforeFirstByte) || resp.Timestamp.After(afterFirstByte) {
		t.Fatalf("fallback response timestamp %s not captured by its GotFirstResponseByte interval [%s, %s]", resp.Timestamp, beforeFirstByte, afterFirstByte)
	}
}

func TestExecuteRejectedOptimizationRetainsExactTimingTrace(t *testing.T) {
	runner, _, req := optimizedExecuteRunner(t, responseOptimizationHEAD)
	runner.telemetryOn = false
	req.Method = http.MethodPost
	runner.client.Transport = testRoundTripFunc(func(httpReq *http.Request) (*http.Response, error) {
		trace := httptrace.ContextClientTrace(httpReq.Context())
		if trace == nil || trace.WroteRequest == nil || trace.GotFirstResponseByte == nil {
			t.Fatalf("rejected optimization trace = %+v, want exact timing callbacks", trace)
		}
		trace.WroteRequest(httptrace.WroteRequestInfo{})
		trace.GotFirstResponseByte()
		return optimizedHTTPResponse(httpReq, http.StatusOK, http.Header{
			"Content-Length": []string{"0"},
		}, http.NoBody, 0), nil
	})

	if _, err := runner.Execute(&req); err != nil {
		t.Fatalf("Execute: %v", err)
	}
}

var benchmarkRequestTimingContext context.Context
var benchmarkRequestTimingDuration time.Duration

func BenchmarkRequestTimingPath(b *testing.B) {
	base := context.Background()
	b.Run("ExactWriteToFirstByte", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			timing, trace := newExactRequestTiming()
			ctx := requestAttemptContext(base, trace, false)
			timing.beginAt(time.Now())
			trace.WroteRequest(httptrace.WroteRequestInfo{})
			trace.GotFirstResponseByte()
			snapshot := timing.freeze()
			benchmarkRequestTimingContext = ctx
			benchmarkRequestTimingDuration = snapshot.firstByteTime
		}
	})
	b.Run("OptimizedRoundTripToHeaders", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			start := time.Now()
			benchmarkRequestTimingContext = requestAttemptContext(base, nil, false)
			benchmarkRequestTimingDuration = time.Since(start)
		}
	})
}

func BenchmarkExecuteOptimizedHEAD(b *testing.B) {
	benchmarkExecuteOptimized(b, responseOptimizationHEAD)
}

func BenchmarkExecuteOptimizedRange(b *testing.B) {
	benchmarkExecuteOptimized(b, responseOptimizationRange)
}

func benchmarkExecuteOptimized(b *testing.B, mode responseOptimizationMode) {
	runner, _, req := optimizedExecuteRunner(b, mode)
	// Keep these microbenchmarks comparable to the default telemetry-disabled
	// request path. Telemetry-enabled accounting is covered by the regression
	// tests and the end-to-end harness report.
	runner.telemetryOn = false
	runner.client.Transport = testRoundTripFunc(func(httpReq *http.Request) (*http.Response, error) {
		if mode == responseOptimizationHEAD {
			return optimizedHTTPResponse(httpReq, http.StatusOK, http.Header{
				"Content-Length": []string{"128"},
				"Content-Type":   []string{"text/plain"},
			}, http.NoBody, 128), nil
		}
		return optimizedHTTPResponse(httpReq, http.StatusPartialContent, http.Header{
			"Content-Length": []string{"1"},
			"Content-Range":  []string{"bytes 127-127/128"},
			"Content-Type":   []string{"text/plain"},
		}, io.NopCloser(bytes.NewReader([]byte{'x'})), 1), nil
	})

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := runner.Execute(&req); err != nil {
			b.Fatal(err)
		}
	}
}
