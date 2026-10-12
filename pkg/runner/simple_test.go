package runner

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

// gzipBomb returns a gzip stream that decompresses to `decompressed` bytes of
// zeros. The compressed form is tiny (zeros compress ~1000x), so the test moves
// almost no data over the wire while forcing a large decompression.
func gzipBomb(t *testing.T, decompressed int) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	chunk := make([]byte, 1<<20) // 1 MiB of zeros, reused
	for written := 0; written < decompressed; {
		n := len(chunk)
		if r := decompressed - written; r < n {
			n = r
		}
		if _, err := zw.Write(chunk[:n]); err != nil {
			t.Fatalf("gzip write: %v", err)
		}
		written += n
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func runnerFor(t *testing.T, url string) (ffuf.RunnerProvider, ffuf.Request) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	conf := ffuf.NewConfig(ctx, cancel)
	conf.Method = "GET"
	runner := NewSimpleRunner(&conf, false)
	req := ffuf.NewRequest(&conf)
	req.Method = "GET"
	req.Url = url
	return runner, req
}

// A malicious server returning a decompression bomb must not be read into memory
// unbounded. The response is capped at MAX_DOWNLOAD_SIZE and marked Cancelled
// rather than OOM-killing the process. Regression test for the gzip-bomb DoS
// (CWE-409): before the fix, io.ReadAll on the decompressed stream had no bound.
func TestExecute_GzipBombIsCapped(t *testing.T) {
	const decompressed = 64 << 20 // 64 MiB, well over the 5 MiB cap
	bomb := gzipBomb(t, decompressed)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Length", strconv.Itoa(len(bomb)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(bomb)
	}))
	defer srv.Close()

	// Path 1: default client. The transport adds Accept-Encoding: gzip itself,
	// transparently decodes, and strips Content-Encoding/Content-Length, so the
	// header-based guard never fires. The LimitReader is what saves us.
	t.Run("transparent transport decode", func(t *testing.T) {
		runner, req := runnerFor(t, srv.URL+"/")
		resp, err := runner.Execute(&req)
		if err != nil {
			t.Fatalf("execute: %v", err)
		}
		if !resp.Cancelled {
			t.Fatalf("expected cancelled response, got %d bytes of data", len(resp.Data))
		}
		if len(resp.Data) > MAX_DOWNLOAD_SIZE {
			t.Fatalf("read %d bytes, must not exceed MAX_DOWNLOAD_SIZE (%d)", len(resp.Data), MAX_DOWNLOAD_SIZE)
		}
	})

	// Path 2: client sends its own Accept-Encoding, so the transport does NOT
	// auto-decode. Content-Encoding: gzip survives and Content-Length reflects the
	// small compressed size, passing the guard, so the manual decoder + LimitReader
	// must catch it.
	t.Run("manual decode with preserved headers", func(t *testing.T) {
		runner, req := runnerFor(t, srv.URL+"/")
		req.Headers["Accept-Encoding"] = "gzip"
		resp, err := runner.Execute(&req)
		if err != nil {
			t.Fatalf("execute: %v", err)
		}
		if !resp.Cancelled {
			t.Fatalf("expected cancelled response, got %d bytes of data", len(resp.Data))
		}
		if len(resp.Data) > MAX_DOWNLOAD_SIZE {
			t.Fatalf("read %d bytes, must not exceed MAX_DOWNLOAD_SIZE (%d)", len(resp.Data), MAX_DOWNLOAD_SIZE)
		}
	})
}

// A normal, small response must still be read in full and not cancelled, so the
// size cap does not regress ordinary fuzzing behaviour.
func TestExecute_NormalResponseUnaffected(t *testing.T) {
	body := []byte("hello world")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	runner, req := runnerFor(t, srv.URL+"/")
	resp, err := runner.Execute(&req)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if resp.Cancelled {
		t.Fatal("normal response must not be cancelled")
	}
	if !bytes.Equal(resp.Data, body) {
		t.Fatalf("body = %q, want %q", resp.Data, body)
	}
}

func TestExecute_RetriesOptimizedTransportErrorsAsFullGET(t *testing.T) {
	tests := []struct {
		name            string
		mode            responseOptimizationMode
		wantFirstMethod string
		wantFirstRange  string
	}{
		{name: "HEAD", mode: responseOptimizationHEAD, wantFirstMethod: http.MethodHead},
		{name: "Range", mode: responseOptimizationRange, wantFirstMethod: http.MethodGet, wantFirstRange: "bytes=-1"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			conf := ffuf.NewConfig(ctx, cancel)
			conf.Method = http.MethodGet
			conf.RunTelemetry = "test-only"
			runner := NewSimpleRunner(&conf, false).(*SimpleRunner)
			runner.optimization = responseOptimizationReport{mode: test.mode}

			type observedRequest struct {
				method string
				range_ string
			}
			var calls []observedRequest
			runner.client.Transport = testRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls = append(calls, observedRequest{method: req.Method, range_: req.Header.Get("Range")})
				if len(calls) == 1 {
					return nil, errors.New("optimized stream rejected")
				}
				body := "fallback body"
				return &http.Response{
					Status:        "200 OK",
					StatusCode:    http.StatusOK,
					Header:        http.Header{"Content-Length": []string{strconv.Itoa(len(body))}},
					Body:          io.NopCloser(strings.NewReader(body)),
					ContentLength: int64(len(body)),
					Request:       req,
				}, nil
			})

			req := ffuf.NewRequest(&conf)
			req.Method = http.MethodGet
			req.Url = "https://example.test/asset"
			resp, err := runner.Execute(&req)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if string(resp.Data) != "fallback body" {
				t.Fatalf("fallback response body = %q, want %q", resp.Data, "fallback body")
			}
			if len(calls) != 2 {
				t.Fatalf("transport calls = %d, want optimized attempt plus one GET retry", len(calls))
			}
			if calls[0].method != test.wantFirstMethod || calls[0].range_ != test.wantFirstRange {
				t.Fatalf("optimized request = method %q Range %q, want method %q Range %q", calls[0].method, calls[0].range_, test.wantFirstMethod, test.wantFirstRange)
			}
			if calls[1].method != http.MethodGet || calls[1].range_ != "" {
				t.Fatalf("fallback request = method %q Range %q, want an unmodified GET", calls[1].method, calls[1].range_)
			}
			telemetry := runner.RunTelemetry().RequestStrategy
			if telemetry.OptimizedAttempts != 1 || telemetry.OptimizedSuccesses != 0 || telemetry.FullGETFallbacks != 1 || telemetry.FallbackTransportErrors != 1 || telemetry.FallbackUntrustworthy != 0 {
				t.Fatalf("optimization telemetry = %+v", telemetry)
			}
		})
	}
}

func TestExecute_OptimizedTransportErrorRetriesOnlyOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conf := ffuf.NewConfig(ctx, cancel)
	conf.Method = http.MethodGet
	runner := NewSimpleRunner(&conf, false).(*SimpleRunner)
	runner.optimization = responseOptimizationReport{mode: responseOptimizationHEAD}

	calls := 0
	runner.client.Transport = testRoundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("transport unavailable")
	})
	req := ffuf.NewRequest(&conf)
	req.Method = http.MethodGet
	req.Url = "https://example.test/asset"
	if _, err := runner.Execute(&req); err == nil {
		t.Fatal("Execute succeeded when both optimized and fallback requests failed")
	}
	if calls != 2 {
		t.Fatalf("transport calls = %d, want exactly two", calls)
	}
}

type testRoundTripFunc func(*http.Request) (*http.Response, error)

func (f testRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// chunkedBodyServer streams `total` bytes with no Content-Length, so the response
// is framed chunked and the numeric size guard cannot fire.
func chunkedBodyServer(t *testing.T, total int) *httptest.Server {
	t.Helper()
	chunk := bytes.Repeat([]byte("A"), 1<<20)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for written := 0; written < total; written += len(chunk) {
			if _, err := w.Write(chunk); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
}

func runnerWithConfig(t *testing.T, url string, mutate func(*ffuf.Config)) (ffuf.RunnerProvider, ffuf.Request) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	conf := ffuf.NewConfig(ctx, cancel)
	conf.Method = "GET"
	if mutate != nil {
		mutate(&conf)
	}
	runner := NewSimpleRunner(&conf, false)
	req := ffuf.NewRequest(&conf)
	req.Method = "GET"
	req.Url = url
	return runner, req
}

// allocatedBy reports how many bytes were allocated while fn ran.
func allocatedBy(fn func()) uint64 {
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// The -od and -audit-log snapshot path takes its own copy of the response body,
// and it runs before the LimitReader that bounds resp.Data. Without a bound of its
// own it reads an attacker-chosen amount into memory, so the size cap held for
// resp.Data but not for resp.Raw. Regression test for that: with either output
// feature enabled, a body far over the cap must not be materialised.
func TestExecute_SnapshotIsBoundedWithOutputFeatures(t *testing.T) {
	const body = 64 << 20 // 64 MiB against a 5 MiB cap
	// The ceiling is a multiple of the body, not an absolute figure. The test origin
	// runs in this process and allocates the body it streams, so roughly one body's
	// worth is charged here no matter how little the client reads; the race detector
	// adds more on top. The signal is the multiple: bounded stays near that floor,
	// unbounded allocates several times the body because it buffers, copies and
	// re-serialises it.
	const allocCeiling = 2 * body

	cases := []struct {
		name   string
		mutate func(*ffuf.Config)
	}{
		{"output directory", func(c *ffuf.Config) { c.OutputDirectory = t.TempDir() }},
		{"audit log", func(c *ffuf.Config) { c.AuditLog = filepath.Join(t.TempDir(), "audit.log") }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := chunkedBodyServer(t, body)
			defer srv.Close()

			runner, req := runnerWithConfig(t, srv.URL+"/", tc.mutate)
			var resp ffuf.Response
			allocated := allocatedBy(func() {
				var err error
				resp, err = runner.Execute(&req)
				if err != nil {
					t.Fatalf("execute: %v", err)
				}
			})

			if allocated > allocCeiling {
				t.Errorf("allocated %d bytes for a %d byte body, want at most %d; the snapshot is reading past the cap",
					allocated, body, allocCeiling)
			}
			if len(resp.Raw) > MAX_DOWNLOAD_SIZE {
				t.Errorf("resp.Raw is %d bytes, must not exceed MAX_DOWNLOAD_SIZE (%d)", len(resp.Raw), MAX_DOWNLOAD_SIZE)
			}
			if !resp.Cancelled {
				t.Error("expected an over-cap response to be cancelled")
			}
		})
	}
}

// A gzip bomb reaches the same snapshot path: the transport requests gzip itself,
// decompresses transparently and strips Content-Length, so the numeric guard is
// skipped and the snapshot sees the expanded stream.
func TestExecute_SnapshotIsBoundedForGzipBomb(t *testing.T) {
	const decompressed = 64 << 20
	bomb := gzipBomb(t, decompressed)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(bomb)
	}))
	defer srv.Close()

	runner, req := runnerWithConfig(t, srv.URL+"/", func(c *ffuf.Config) {
		c.OutputDirectory = t.TempDir()
	})
	resp, err := runner.Execute(&req)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(resp.Raw) > MAX_DOWNLOAD_SIZE {
		t.Errorf("resp.Raw is %d bytes, must not exceed MAX_DOWNLOAD_SIZE (%d)", len(resp.Raw), MAX_DOWNLOAD_SIZE)
	}
}

// A response within the cap must still be captured in full, so the size bound does
// not quietly truncate ordinary -od output.
func TestExecute_SnapshotKeptForNormalResponse(t *testing.T) {
	body := []byte("hello world")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	runner, req := runnerWithConfig(t, srv.URL+"/", func(c *ffuf.Config) {
		c.OutputDirectory = t.TempDir()
	})
	resp, err := runner.Execute(&req)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if resp.Cancelled {
		t.Fatal("a normal response must not be cancelled")
	}
	if !strings.Contains(resp.Raw, string(body)) {
		t.Errorf("resp.Raw does not contain the body: %q", resp.Raw)
	}
}

// -ignore-body used to live inside the Content-Length branch, so it did nothing for
// a chunked response. It must hold regardless of framing.
func TestExecute_IgnoreBodyAppliesToChunkedResponse(t *testing.T) {
	srv := chunkedBodyServer(t, 32<<10)
	defer srv.Close()

	runner, req := runnerWithConfig(t, srv.URL+"/", func(c *ffuf.Config) { c.IgnoreBody = true })
	resp, err := runner.Execute(&req)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !resp.Cancelled {
		t.Error("expected the response to be cancelled")
	}
	if len(resp.Data) != 0 {
		t.Errorf("read %d bytes of body despite -ignore-body", len(resp.Data))
	}
}

// Moving the -ignore-body check must not lose the server-declared length, which the
// size filters read.
func TestExecute_IgnoreBodyKeepsContentLength(t *testing.T) {
	body := make([]byte, 32<<10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	runner, req := runnerWithConfig(t, srv.URL+"/", func(c *ffuf.Config) { c.IgnoreBody = true })
	resp, err := runner.Execute(&req)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !resp.Cancelled {
		t.Error("expected the response to be cancelled")
	}
	if resp.ContentLength != int64(len(body)) {
		t.Errorf("ContentLength = %d, want %d", resp.ContentLength, len(body))
	}
}

// Response headers sit outside MAX_DOWNLOAD_SIZE, so the transport needs its own
// ceiling; Go's default is 10 MB.
func TestNewSimpleRunner_BoundsResponseHeaders(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conf := ffuf.NewConfig(ctx, cancel)
	runner := NewSimpleRunner(&conf, false).(*SimpleRunner)

	transport, ok := runner.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport is %T, want *http.Transport", runner.client.Transport)
	}
	if transport.MaxResponseHeaderBytes <= 0 {
		t.Fatal("MaxResponseHeaderBytes is unset, response headers are bounded only by the net/http default")
	}
	if transport.MaxResponseHeaderBytes > 4<<20 {
		t.Errorf("MaxResponseHeaderBytes = %d, larger than intended", transport.MaxResponseHeaderBytes)
	}
}

func TestNewSimpleRunner_RegularHTTPCapacityTracksWorkerConcurrency(t *testing.T) {
	tests := []struct {
		name            string
		threads         int
		wantConnections int
		wantGlobalIdle  int
	}{
		{
			name:            "preserves legacy capacity for smaller scans",
			threads:         40,
			wantConnections: 500,
			wantGlobalIdle:  1000,
		},
		{
			name:            "grows capacity with configured workers",
			threads:         1000,
			wantConnections: 1000,
			wantGlobalIdle:  1000,
		},
		{
			name:            "keeps the global idle pool large enough for high concurrency",
			threads:         1500,
			wantConnections: 1500,
			wantGlobalIdle:  1500,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			conf := ffuf.NewConfig(ctx, cancel)
			conf.Threads = test.threads

			runner := NewSimpleRunner(&conf, false).(*SimpleRunner)
			transport := runner.tcpTransport
			if transport == nil {
				t.Fatal("regular HTTP transport is nil")
			}

			if transport.MaxConnsPerHost != test.wantConnections {
				t.Errorf("MaxConnsPerHost = %d, want %d for %d workers", transport.MaxConnsPerHost, test.wantConnections, test.threads)
			}
			if transport.MaxIdleConnsPerHost != test.wantConnections {
				t.Errorf("MaxIdleConnsPerHost = %d, want %d for %d workers", transport.MaxIdleConnsPerHost, test.wantConnections, test.threads)
			}
			if transport.MaxIdleConns != test.wantGlobalIdle {
				t.Errorf("MaxIdleConns = %d, want %d for %d workers", transport.MaxIdleConns, test.wantGlobalIdle, test.threads)
			}
		})
	}
}

func TestSimpleRunner_RegularHTTPCanUseWorkersBeyondLegacyPoolSize(t *testing.T) {
	// The previous fixed transport limit admitted only 500 requests at once,
	// even when the scheduler configured more workers. Holding each handler open
	// makes admission observable without depending on request timing or RPS.
	const workers = 501

	var active atomic.Int32
	var peakActive atomic.Int32
	var reachedOnce sync.Once
	reachedConfiguredConcurrency := make(chan struct{})
	releaseHandlers := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() { close(releaseHandlers) })
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		concurrent := active.Add(1)
		defer active.Add(-1)
		for peak := peakActive.Load(); concurrent > peak && !peakActive.CompareAndSwap(peak, concurrent); peak = peakActive.Load() {
		}
		if concurrent >= workers {
			reachedOnce.Do(func() { close(reachedConfiguredConcurrency) })
		}
		<-releaseHandlers
		w.WriteHeader(http.StatusNoContent)
	}))
	defer func() {
		release()
		srv.Close()
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conf := ffuf.NewConfig(ctx, cancel)
	conf.Method = http.MethodGet
	conf.Threads = workers
	conf.Timeout = 10
	runner := NewSimpleRunner(&conf, false)

	errs := make(chan error, workers)
	var requests sync.WaitGroup
	requests.Add(workers)
	for range workers {
		go func() {
			defer requests.Done()
			req := ffuf.NewRequest(&conf)
			req.Method = http.MethodGet
			req.Url = srv.URL + "/"
			_, err := runner.Execute(&req)
			errs <- err
		}()
		// Avoid overflowing the kernel's SYN backlog. The requests remain active
		// in the handler, so this still verifies the transport's admission limit.
		time.Sleep(time.Millisecond)
	}

	select {
	case <-reachedConfiguredConcurrency:
		release()
	case <-time.After(5 * time.Second):
		release()
		requests.Wait()
		t.Fatalf("regular HTTP admitted at most %d of %d configured workers", peakActive.Load(), workers)
	}

	requests.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("execute: %v", err)
		}
	}
}
