package runner

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

type sharedDeadlineValueKey struct{}

func TestSharedDeadlineManagerSharesBucketsAndPreservesParentValues(t *testing.T) {
	want := &struct{}{}
	parent := context.WithValue(context.Background(), sharedDeadlineValueKey{}, want)
	m := newSharedDeadlineManager(parent, time.Second)
	defer m.close()

	first := m.context()
	second := m.context()
	if first != second {
		t.Fatal("requests created together should share a deadline context")
	}
	if got := first.Value(sharedDeadlineValueKey{}); got != want {
		t.Fatalf("context value = %v, want %v", got, want)
	}
	deadline, ok := first.Deadline()
	if !ok {
		t.Fatal("shared context has no deadline")
	}
	if remaining := time.Until(deadline); remaining < m.timeout || remaining > m.timeout+m.quantum {
		t.Fatalf("remaining timeout = %s, want [%s, %s]", remaining, m.timeout, m.timeout+m.quantum)
	}
}

func TestSharedDeadlineManagerExpiresBucket(t *testing.T) {
	timeout := 30 * time.Millisecond
	m := newSharedDeadlineManager(context.Background(), timeout)
	m.quantum = 10 * time.Millisecond
	defer m.close()

	started := time.Now()
	ctx := m.context()
	select {
	case <-ctx.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("shared deadline did not fire")
	}
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("Err() = %v, want DeadlineExceeded", ctx.Err())
	}
	if !os.IsTimeout(ctx.Err()) {
		t.Fatalf("Err() = %v, want an os.IsTimeout error", ctx.Err())
	}
	if elapsed := time.Since(started); elapsed < timeout || elapsed > timeout+m.quantum*3 {
		t.Fatalf("deadline fired after %s, expected %s..%s", elapsed, timeout, timeout+m.quantum*3)
	}
}

func TestSharedDeadlineManagerHonorsParentCancellation(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	m := newSharedDeadlineManager(parent, time.Second)
	ctx := m.context()
	cancel()

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("parent cancellation was not propagated")
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("Err() = %v, want Canceled", ctx.Err())
	}
	m.close()
}

func TestSharedDeadlineManagerCloseIsIdempotentAndCancelsBuckets(t *testing.T) {
	m := newSharedDeadlineManager(context.Background(), time.Second)
	ctx := m.context()
	m.close()
	m.close()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("Err() = %v, want Canceled", ctx.Err())
	}
}

func TestSharedDeadlineManagerUsesParentEarlierDeadline(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	m := newSharedDeadlineManager(parent, time.Second)
	defer m.close()

	ctx := m.context()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("earlier parent deadline was not propagated")
	}
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("Err() = %v, want DeadlineExceeded", ctx.Err())
	}
}

func TestSharedDeadlineManagerConcurrentBucketRotation(t *testing.T) {
	m := newSharedDeadlineManager(context.Background(), 5*time.Millisecond)
	m.quantum = time.Millisecond
	defer m.close()

	for round := 0; round < 5; round++ {
		var wg sync.WaitGroup
		for i := 0; i < 64; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ctx := m.context()
				if _, ok := ctx.Deadline(); !ok {
					t.Error("shared context has no deadline")
				}
			}()
		}
		wg.Wait()
		time.Sleep(2 * time.Millisecond)
	}
}

func TestSimpleRunnerSharedDeadlineBoundsResponseHeaders(t *testing.T) {
	conf := newTestConfig("https://example.test/FUZZ")
	conf.Method = http.MethodGet
	r := NewSimpleRunner(conf, false).(*SimpleRunner)
	r.client.Transport = testRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
	r.client.Timeout = 0
	r.deadlines = newSharedDeadlineManager(conf.Context, 25*time.Millisecond)
	defer r.Close()

	req := ffuf.NewRequest(conf)
	req.Method = http.MethodGet
	req.Url = "https://example.test/path"
	started := time.Now()
	_, err := r.Execute(&req)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Execute error = %v, want DeadlineExceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("blocked response headers took %s to cancel", elapsed)
	}
}

type deadlineBlockingBody struct {
	ctx context.Context
}

func (b *deadlineBlockingBody) Read([]byte) (int, error) {
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func (*deadlineBlockingBody) Close() error { return nil }

func TestSimpleRunnerSharedDeadlineBoundsResponseBody(t *testing.T) {
	conf := newTestConfig("https://example.test/FUZZ")
	conf.Method = http.MethodGet
	r := NewSimpleRunner(conf, false).(*SimpleRunner)
	r.client.Transport = testRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Status:        "200 OK",
			Proto:         "HTTP/3.0",
			ProtoMajor:    3,
			ProtoMinor:    0,
			Header:        make(http.Header),
			Body:          &deadlineBlockingBody{ctx: req.Context()},
			ContentLength: -1,
			Request:       req,
		}, nil
	})
	r.client.Timeout = 0
	r.deadlines = newSharedDeadlineManager(conf.Context, 25*time.Millisecond)
	defer r.Close()

	req := ffuf.NewRequest(conf)
	req.Method = http.MethodGet
	req.Url = "https://example.test/path"
	_, err := r.Execute(&req)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Execute error = %v, want DeadlineExceeded", err)
	}
}

func TestSimpleRunnerSharedDeadlineBoundsPreflight(t *testing.T) {
	conf := newTestConfig("https://example.test/FUZZ")
	reqFile := writeTempRequest(t, "GET / HTTP/1.1\nHost: example.test\n\n")
	chain := []ffuf.PreflightConfig{{RequestFile: reqFile}}
	r := newTestRunner(conf)
	r.client.Transport = testRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
	r.deadlines = newSharedDeadlineManager(conf.Context, 25*time.Millisecond)
	defer r.deadlines.close()

	_, err := r.runPreflightChain(chain, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("preflight error = %v, want DeadlineExceeded", err)
	}
}

func TestSimpleRunnerOptimizationFallbackGetsFreshSharedDeadline(t *testing.T) {
	for _, blockedPhase := range []string{"round-trip", "body"} {
		t.Run(blockedPhase, func(t *testing.T) {
			conf := newTestConfig("https://example.test/FUZZ")
			conf.Method = http.MethodGet
			r := NewSimpleRunner(conf, false).(*SimpleRunner)
			r.optimization = responseOptimizationReport{mode: responseOptimizationHEAD}
			r.client.Transport = testRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.Method == http.MethodHead {
					if blockedPhase == "round-trip" {
						<-req.Context().Done()
						return nil, req.Context().Err()
					}
					return &http.Response{
						StatusCode:    http.StatusOK,
						Status:        "200 OK",
						Proto:         "HTTP/3.0",
						ProtoMajor:    3,
						Header:        http.Header{"Content-Length": []string{"2"}},
						Body:          &deadlineBlockingBody{ctx: req.Context()},
						ContentLength: 2,
						Request:       req,
					}, nil
				}
				return &http.Response{
					StatusCode:    http.StatusOK,
					Status:        "200 OK",
					Proto:         "HTTP/3.0",
					ProtoMajor:    3,
					Header:        http.Header{"Content-Length": []string{"2"}},
					Body:          io.NopCloser(strings.NewReader("ok")),
					ContentLength: 2,
					Request:       req,
				}, nil
			})
			r.client.Timeout = 0
			r.deadlines = newSharedDeadlineManager(conf.Context, 25*time.Millisecond)
			defer r.Close()

			req := ffuf.NewRequest(conf)
			req.Method = http.MethodGet
			req.Url = "https://example.test/path"
			resp, err := r.Execute(&req)
			if err != nil {
				t.Fatalf("Execute fallback failed: %v", err)
			}
			if string(resp.Data) != "ok" {
				t.Fatalf("fallback body = %q, want ok", resp.Data)
			}
		})
	}
}

var _ io.ReadCloser = (*deadlineBlockingBody)(nil)
