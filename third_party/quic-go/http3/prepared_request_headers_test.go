package http3

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quic-go/qpack"
	"github.com/quic-go/quic-go/qlogwriter"
	"github.com/quic-go/quic-go/quicvarint"
	"github.com/quic-go/quic-go/testutils/events"
)

const preparedRequestHeadersTestUserAgent = "h3fuzz/a42"

func newPreparedRequestHeadersTestRequest(t testing.TB, ctx context.Context, path string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, "https://127.0.0.1:9448"+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header = http.Header{
		"Accept-Encoding":       {"identity"},
		"User-Agent":            {preparedRequestHeadersTestUserAgent},
		"X-H3fuzz-Harness-Role": {"request"},
		"X-H3fuzz-Run":          {"a42"},
	}
	return req
}

func decodePreparedRequestHeaderFrame(frame []byte) ([]qpack.HeaderField, error) {
	typ, n, err := quicvarint.Parse(frame)
	if err != nil {
		return nil, err
	}
	if typ != 1 {
		return nil, fmt.Errorf("frame type = %d, want HEADERS", typ)
	}
	length, m, err := quicvarint.Parse(frame[n:])
	if err != nil {
		return nil, err
	}
	payload := frame[n+m:]
	if uint64(len(payload)) != length {
		return nil, fmt.Errorf("payload length = %d, declared %d", len(payload), length)
	}
	decode := qpack.NewDecoder().Decode(payload)
	fields := make([]qpack.HeaderField, 0, 8)
	for {
		field, err := decode()
		if err == io.EOF {
			return fields, nil
		}
		if err != nil {
			return nil, err
		}
		fields = append(fields, field)
	}
}

func preparedRequestHeaderFieldsByName(fields []qpack.HeaderField) map[string][]string {
	grouped := make(map[string][]string)
	for _, field := range fields {
		grouped[field.Name] = append(grouped[field.Name], field.Value)
	}
	return grouped
}

func writeGenericRequestHeaderFrame(writer *requestWriter, req *http.Request) ([]byte, error) {
	var frame bytes.Buffer
	if err := writer.writeHeaders(&frame, req, false, 0, nil); err != nil {
		return nil, err
	}
	return append([]byte(nil), frame.Bytes()...), nil
}

func assertPreparedRequestHeaderParity(t testing.TB, req *http.Request) {
	t.Helper()
	genericFrame, err := writeGenericRequestHeaderFrame(newRequestWriter(), req)
	if err != nil {
		t.Fatalf("generic writer: %v", err)
	}
	preparedReq := req.Clone(WithPreparedRequestHeaders(req.Context()))
	var preparedFrame bytes.Buffer
	if err := newRequestWriter().WriteRequestHeader(&preparedFrame, preparedReq, false, 0, nil); err != nil {
		t.Fatalf("prepared writer: %v", err)
	}
	genericFields, err := decodePreparedRequestHeaderFrame(genericFrame)
	if err != nil {
		t.Fatalf("decode generic: %v", err)
	}
	preparedFields, err := decodePreparedRequestHeaderFrame(preparedFrame.Bytes())
	if err != nil {
		t.Fatalf("decode prepared: %v", err)
	}
	if !reflect.DeepEqual(preparedRequestHeaderFieldsByName(genericFields), preparedRequestHeaderFieldsByName(preparedFields)) {
		t.Fatalf("decoded fields differ\ngeneric:  %#v\nprepared: %#v", genericFields, preparedFields)
	}
	const pseudoHeaderCount = 4
	if len(genericFields) < pseudoHeaderCount || len(preparedFields) < pseudoHeaderCount ||
		!reflect.DeepEqual(genericFields[:pseudoHeaderCount], preparedFields[:pseudoHeaderCount]) {
		t.Fatalf("pseudo-header order differs\ngeneric:  %#v\nprepared: %#v", genericFields, preparedFields)
	}
}

func TestPreparedRequestHeadersDefaultOff(t *testing.T) {
	req := newPreparedRequestHeadersTestRequest(t, context.Background(), "/bench/default-off")
	writer := newRequestWriter()
	if err := writer.WriteRequestHeader(io.Discard, req, false, 0, nil); err != nil {
		t.Fatal(err)
	}
	if writer.preparedRequestHeaders != nil {
		t.Fatal("prepared request headers were used without explicit opt-in")
	}
	if _, ok := SnapshotPreparedRequestHeaders(req.Context()); ok {
		t.Fatal("telemetry unexpectedly enabled")
	}
}

func TestPreparedRequestHeadersContextAPI(t *testing.T) {
	plain := WithPreparedRequestHeaders(context.Background())
	if again := WithPreparedRequestHeaders(plain); again != plain {
		t.Fatal("marker-only API isn't idempotent")
	}
	if _, ok := SnapshotPreparedRequestHeaders(plain); ok {
		t.Fatal("marker-only context reports telemetry")
	}

	telemetry := WithPreparedRequestHeadersTelemetry(plain)
	if again := WithPreparedRequestHeadersTelemetry(telemetry); again != telemetry {
		t.Fatal("telemetry API isn't idempotent")
	}
	if again := WithPreparedRequestHeaders(telemetry); again != telemetry {
		t.Fatal("marker API replaced a telemetry-enabled context")
	}
	derived, cancel := context.WithCancel(telemetry)
	cancel()
	if stats, ok := SnapshotPreparedRequestHeaders(derived); !ok || stats != (PreparedRequestHeadersStats{}) {
		t.Fatalf("initial derived-context snapshot = (%+v, %t)", stats, ok)
	}
	if _, ok := SnapshotPreparedRequestHeaders(nil); ok {
		t.Fatal("nil context reports telemetry")
	}
}

func TestPreparedRequestHeadersContextAPINilParentPanics(t *testing.T) {
	tests := []struct {
		name string
		call func()
	}{
		{name: "marker only", call: func() { WithPreparedRequestHeaders(nil) }},
		{name: "telemetry", call: func() { WithPreparedRequestHeadersTelemetry(nil) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("nil parent context didn't panic")
				}
			}()
			test.call()
		})
	}
}

func TestPreparedRequestHeadersTelemetryHit(t *testing.T) {
	ctx := WithPreparedRequestHeadersTelemetry(context.Background())
	req := newPreparedRequestHeadersTestRequest(t, ctx, "/bench/hit")
	if err := newRequestWriter().WriteRequestHeader(io.Discard, req, false, 0, nil); err != nil {
		t.Fatal(err)
	}
	stats, ok := SnapshotPreparedRequestHeaders(ctx)
	if !ok || stats.Attempts != 1 || stats.Hits != 1 {
		t.Fatalf("stats = (%+v, %t)", stats, ok)
	}
}

func TestPreparedRequestHeadersWriteErrorStillCountsAsHit(t *testing.T) {
	ctx := WithPreparedRequestHeadersTelemetry(context.Background())
	req := newPreparedRequestHeadersTestRequest(t, ctx, "/bench/write-error")
	err := newRequestWriter().WriteRequestHeader(preparedRequestHeadersErrorWriter{}, req, false, 0, nil)
	if err != io.ErrClosedPipe {
		t.Fatalf("write error = %v, want %v", err, io.ErrClosedPipe)
	}
	stats, ok := SnapshotPreparedRequestHeaders(ctx)
	if !ok || stats.Attempts != 1 || stats.Hits != 1 {
		t.Fatalf("stats = (%+v, %t)", stats, ok)
	}
}

type preparedRequestHeadersErrorWriter struct{}

func (preparedRequestHeadersErrorWriter) Write([]byte) (int, error) {
	return 0, io.ErrClosedPipe
}

func TestPreparedRequestHeadersFallbackReasons(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*http.Request) (qlogger bool)
		gzip   bool
		want   PreparedRequestHeadersStats
	}{
		{name: "qlog", mutate: func(*http.Request) bool { return true }, want: PreparedRequestHeadersStats{FallbackQLog: 1}},
		{name: "wrote header field trace", mutate: func(req *http.Request) bool {
			clone := req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{WroteHeaderField: func(string, []string) {}}))
			*req = *clone
			return false
		}, want: PreparedRequestHeadersStats{FallbackTrace: 1}},
		{name: "wrote headers trace", mutate: func(req *http.Request) bool {
			clone := req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{WroteHeaders: func() {}}))
			*req = *clone
			return false
		}, want: PreparedRequestHeadersStats{FallbackTrace: 1}},
		{name: "method", mutate: func(req *http.Request) bool { req.Method = http.MethodGet; return false }, want: PreparedRequestHeadersStats{FallbackMethod: 1}},
		{name: "body", mutate: func(req *http.Request) bool {
			req.Body = io.NopCloser(strings.NewReader(""))
			return false
		}, want: PreparedRequestHeadersStats{FallbackBody: 1}},
		{name: "compression", mutate: func(*http.Request) bool { return false }, gzip: true, want: PreparedRequestHeadersStats{FallbackCompression: 1}},
		{name: "url", mutate: func(req *http.Request) bool { req.URL.RawQuery = "x=1"; return false }, want: PreparedRequestHeadersStats{FallbackURL: 1}},
		{name: "headers", mutate: func(req *http.Request) bool { req.Header.Set("Range", ""); return false }, want: PreparedRequestHeadersStats{FallbackHeaders: 1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := WithPreparedRequestHeadersTelemetry(context.Background())
			req := newPreparedRequestHeadersTestRequest(t, ctx, "/bench/fallback")
			useQLog := test.mutate(req)
			var qlogger qlogwriter.Recorder
			if useQLog {
				qlogger = &events.Recorder{}
			}
			if err := newRequestWriter().WriteRequestHeader(io.Discard, req, test.gzip, 7, qlogger); err != nil {
				t.Fatal(err)
			}
			stats, ok := SnapshotPreparedRequestHeaders(ctx)
			test.want.Attempts = 1
			if !ok || stats != test.want {
				t.Fatalf("stats = (%+v, %t), want %+v", stats, ok, test.want)
			}
		})
	}
}

func TestPreparedRequestHeadersFailClosedHeaderShapes(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(http.Header)
	}{
		{name: "accept encoding missing", mutate: func(h http.Header) { h.Del("Accept-Encoding") }},
		{name: "accept encoding wrong case key", mutate: func(h http.Header) {
			h["accept-encoding"] = h["Accept-Encoding"]
			delete(h, "Accept-Encoding")
		}},
		{name: "accept encoding multiple", mutate: func(h http.Header) { h["Accept-Encoding"] = []string{"identity", "identity"} }},
		{name: "accept encoding gzip", mutate: func(h http.Header) { h.Set("Accept-Encoding", "gzip") }},
		{name: "user agent missing", mutate: func(h http.Header) { h.Del("User-Agent") }},
		{name: "user agent empty", mutate: func(h http.Header) { h.Set("User-Agent", "") }},
		{name: "user agent multiple", mutate: func(h http.Header) { h["User-Agent"] = []string{"first", "second"} }},
		{name: "user agent wrong case key", mutate: func(h http.Header) {
			h["user-agent"] = h["User-Agent"]
			delete(h, "User-Agent")
		}},
		{name: "user agent case duplicate", mutate: func(h http.Header) { h["user-agent"] = []string{"second"} }},
		{name: "regular case duplicate", mutate: func(h http.Header) { h["x-h3fuzz-run"] = []string{"second"} }},
		{name: "range mixed case", mutate: func(h http.Header) { h["rAnGe"] = nil }},
		{name: "host", mutate: func(h http.Header) { h["Host"] = []string{"elsewhere"} }},
		{name: "content length", mutate: func(h http.Header) { h["Content-Length"] = []string{"0"} }},
		{name: "connection", mutate: func(h http.Header) { h["Connection"] = []string{"close"} }},
		{name: "invalid value", mutate: func(h http.Header) { h["X-Bad"] = []string{"bad\x00value"} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := WithPreparedRequestHeadersTelemetry(context.Background())
			req := newPreparedRequestHeadersTestRequest(t, ctx, "/bench/headers")
			test.mutate(req.Header)
			writer := newRequestWriter()
			buffers, ok := writer.prepareRequestHeader(req, false, nil)
			if ok {
				releasePreparedRequestHeaderBuffers(buffers)
				t.Fatal("ineligible header shape used prepared path")
			}
			stats, _ := SnapshotPreparedRequestHeaders(ctx)
			if stats.Attempts != 1 || stats.FallbackHeaders != 1 {
				t.Fatalf("stats = %+v", stats)
			}
		})
	}
}

func TestPreparedRequestHeadersDecodedSemanticParity(t *testing.T) {
	paths := []string{
		"/",
		"/bench/admin",
		"/bench/Az09-._~",
		"/bench/abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ._~-",
	}
	for _, path := range paths {
		req := newPreparedRequestHeadersTestRequest(t, context.Background(), path)
		req.Header["X-Multi"] = []string{"first", "second", "third"}
		assertPreparedRequestHeaderParity(t, req)
	}

	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~"
	state := uint64(0x9e3779b97f4a7c15)
	for i := 0; i < 2000; i++ {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		value := make([]byte, int(state%257))
		for j := range value {
			state ^= state << 13
			state ^= state >> 7
			state ^= state << 17
			value[j] = alphabet[state%uint64(len(alphabet))]
		}
		assertPreparedRequestHeaderParity(t, newPreparedRequestHeadersTestRequest(t, context.Background(), "/bench/"+string(value)))
	}
}

func stockPreparedPathField(path string) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := qpack.NewEncoder(&buffer)
	if err := encoder.WriteField(qpack.HeaderField{Name: ":path", Value: path}); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	block := buffer.Bytes()
	if len(block) < 2 || block[0] != 0 || block[1] != 0 {
		return nil, fmt.Errorf("unexpected QPACK prefix %x", block)
	}
	return block[2:], nil
}

func assertPreparedPathFieldExactParity(t testing.TB, path string) {
	t.Helper()
	want, err := stockPreparedPathField(path)
	if err != nil {
		t.Fatal(err)
	}
	got := appendPreparedPathField(nil, path)
	if !bytes.Equal(got, want) {
		t.Fatalf("encoded :path differs for %x\nstock: %x\nlocal: %x", path, want, got)
	}
}

func TestPreparedPathFieldExactQPACKV060Parity(t *testing.T) {
	for _, path := range []string{
		"/",
		"*",
		"/bench/Az09-._~",
		"/" + strings.Repeat("a", 14),
		"/" + strings.Repeat("a", 15),
		"/" + strings.Repeat("a", 126),
		"/" + strings.Repeat("a", 127),
		"/" + strings.Repeat("a", 128),
		"/" + strings.Repeat("a", preparedRequestHeadersMaxPathBytes-1),
	} {
		assertPreparedPathFieldExactParity(t, path)
	}
	allBytes := make([]byte, 257)
	allBytes[0] = '/'
	for value := 0; value < 256; value++ {
		allBytes[value+1] = byte(value)
	}
	assertPreparedPathFieldExactParity(t, string(allBytes))
}

func TestPreparedRequestHeadersBoundaries(t *testing.T) {
	t.Run("path maximum is inclusive", func(t *testing.T) {
		ctx := WithPreparedRequestHeadersTelemetry(context.Background())
		req := newPreparedRequestHeadersTestRequest(t, ctx, "/"+strings.Repeat("a", preparedRequestHeadersMaxPathBytes-1))
		buffers, ok := newRequestWriter().prepareRequestHeader(req, false, nil)
		if !ok {
			t.Fatal("maximum path rejected")
		}
		releasePreparedRequestHeaderBuffers(buffers)
		stats, _ := SnapshotPreparedRequestHeaders(ctx)
		if stats.Hits != 1 {
			t.Fatalf("stats = %+v", stats)
		}
	})
	t.Run("path over maximum falls back", func(t *testing.T) {
		ctx := WithPreparedRequestHeadersTelemetry(context.Background())
		req := newPreparedRequestHeadersTestRequest(t, ctx, "/"+strings.Repeat("a", preparedRequestHeadersMaxPathBytes))
		if buffers, ok := newRequestWriter().prepareRequestHeader(req, false, nil); ok {
			releasePreparedRequestHeaderBuffers(buffers)
			t.Fatal("oversized path accepted")
		}
		stats, _ := SnapshotPreparedRequestHeaders(ctx)
		if stats.FallbackURL != 1 {
			t.Fatalf("stats = %+v", stats)
		}
	})
	t.Run("header name count", func(t *testing.T) {
		ctx := WithPreparedRequestHeadersTelemetry(context.Background())
		req := newPreparedRequestHeadersTestRequest(t, ctx, "/bench/count")
		for i := 0; len(req.Header) <= preparedRequestHeadersMaxHeaderNames; i++ {
			req.Header[fmt.Sprintf("X-Extra-%d", i)] = []string{"x"}
		}
		if buffers, ok := newRequestWriter().prepareRequestHeader(req, false, nil); ok {
			releasePreparedRequestHeaderBuffers(buffers)
			t.Fatal("too many header names accepted")
		}
	})
	t.Run("header value count", func(t *testing.T) {
		ctx := WithPreparedRequestHeadersTelemetry(context.Background())
		req := newPreparedRequestHeadersTestRequest(t, ctx, "/bench/values")
		req.Header["X-Many"] = make([]string, preparedRequestHeadersMaxValues)
		if buffers, ok := newRequestWriter().prepareRequestHeader(req, false, nil); ok {
			releasePreparedRequestHeaderBuffers(buffers)
			t.Fatal("too many header values accepted")
		}
	})
	t.Run("static bytes", func(t *testing.T) {
		ctx := WithPreparedRequestHeadersTelemetry(context.Background())
		req := newPreparedRequestHeadersTestRequest(t, ctx, "/bench/static")
		req.Header.Set("X-Large", strings.Repeat("x", preparedRequestHeadersMaxStaticBytes))
		if buffers, ok := newRequestWriter().prepareRequestHeader(req, false, nil); ok {
			releasePreparedRequestHeaderBuffers(buffers)
			t.Fatal("oversized static headers accepted")
		}
	})
}

func TestPreparedRequestHeadersZeroesCacheOnReplacement(t *testing.T) {
	ctx := WithPreparedRequestHeaders(context.Background())
	writer := newRequestWriter()
	first := newPreparedRequestHeadersTestRequest(t, ctx, "/bench/first")
	if err := writer.WriteRequestHeader(io.Discard, first, false, 0, nil); err != nil {
		t.Fatal(err)
	}
	old := writer.preparedRequestHeaders
	prefixBacking := old.prefix[:cap(old.prefix)]
	suffixBacking := old.suffix[:cap(old.suffix)]
	second := newPreparedRequestHeadersTestRequest(t, ctx, "/bench/second")
	second.Header.Set("X-H3fuzz-Run", "different")
	if err := writer.WriteRequestHeader(io.Discard, second, false, 0, nil); err != nil {
		t.Fatal(err)
	}
	if writer.preparedRequestHeaders == old {
		t.Fatal("template wasn't replaced")
	}
	if old.rawHost != "" || old.scheme != "" || old.headers != nil || old.prefix != nil || old.suffix != nil {
		t.Fatalf("old template retained references: %#v", old)
	}
	for _, backing := range [][]byte{prefixBacking, suffixBacking} {
		for i, value := range backing {
			if value != 0 {
				t.Fatalf("cache byte %d wasn't zeroed", i)
			}
		}
	}
}

func TestPreparedRequestHeadersZeroesPooledBuffers(t *testing.T) {
	buffers := &preparedRequestHeaderBuffers{frame: make([]byte, 32, 128)}
	buffers.path = append(buffers.path, bytes.Repeat([]byte{0xa5}, 64)...)
	for i := range buffers.frame {
		buffers.frame[i] = 0x5a
	}
	pathBacking := buffers.path[:cap(buffers.path)]
	frameBacking := buffers.frame[:cap(buffers.frame)]
	releasePreparedRequestHeaderBuffers(buffers)
	for name, backing := range map[string][]byte{"path": pathBacking, "frame": frameBacking} {
		for i, value := range backing {
			if value != 0 {
				t.Fatalf("%s byte %d wasn't zeroed", name, i)
			}
		}
	}
}

type preparedRequestHeadersBlockingWriter struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *preparedRequestHeadersBlockingWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return len(p), nil
}

func TestPreparedRequestHeadersReleasesLockBeforeStreamIO(t *testing.T) {
	ctx := WithPreparedRequestHeaders(context.Background())
	writer := newRequestWriter()
	first := &preparedRequestHeadersBlockingWriter{entered: make(chan struct{}), release: make(chan struct{})}
	second := &preparedRequestHeadersBlockingWriter{entered: make(chan struct{}), release: make(chan struct{})}
	errors := make(chan error, 2)
	go func() {
		errors <- writer.WriteRequestHeader(first, newPreparedRequestHeadersTestRequest(t, ctx, "/bench/first"), false, 0, nil)
	}()
	select {
	case <-first.entered:
	case <-time.After(time.Second):
		t.Fatal("first stream write wasn't entered")
	}
	go func() {
		errors <- writer.WriteRequestHeader(second, newPreparedRequestHeadersTestRequest(t, ctx, "/bench/second"), false, 0, nil)
	}()
	select {
	case <-second.entered:
	case <-time.After(time.Second):
		close(first.release)
		t.Fatal("second request serialized behind blocked stream I/O")
	}
	close(first.release)
	close(second.release)
	for range 2 {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}
}

func TestPreparedRequestHeadersConcurrentTemplateReplacement(t *testing.T) {
	ctx := WithPreparedRequestHeadersTelemetry(context.Background())
	writer := newRequestWriter()
	const goroutines = 16
	const iterations = 250
	var failures atomic.Uint64
	var wait sync.WaitGroup
	for goroutine := 0; goroutine < goroutines; goroutine++ {
		wait.Add(1)
		go func(id int) {
			defer wait.Done()
			for iteration := 0; iteration < iterations; iteration++ {
				req := newPreparedRequestHeadersTestRequest(t, ctx, fmt.Sprintf("/bench/%d/%d", id, iteration))
				req.Header.Set("X-H3fuzz-Run", fmt.Sprintf("shape-%d", (id+iteration)%3))
				if err := writer.WriteRequestHeader(io.Discard, req, false, 0, nil); err != nil {
					failures.Add(1)
				}
			}
		}(goroutine)
	}
	wait.Wait()
	if failures.Load() != 0 {
		t.Fatalf("%d writes failed", failures.Load())
	}
	stats, _ := SnapshotPreparedRequestHeaders(ctx)
	if stats.Attempts != goroutines*iterations || stats.Hits != stats.Attempts {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestPreparedRequestHeadersOutcomeAccounting(t *testing.T) {
	ctx := WithPreparedRequestHeadersTelemetry(context.Background())
	config := preparedRequestHeadersConfigFromRequest(newPreparedRequestHeadersTestRequest(t, ctx, "/"))
	for outcome := preparedRequestHeadersHit; outcome < preparedRequestHeadersOutcomeCount; outcome++ {
		config.record(outcome)
	}
	stats, ok := SnapshotPreparedRequestHeaders(ctx)
	if !ok || stats.Attempts != uint64(preparedRequestHeadersOutcomeCount) || stats.Hits != 1 ||
		stats.FallbackQLog != 1 || stats.FallbackTrace != 1 || stats.FallbackMethod != 1 ||
		stats.FallbackBody != 1 || stats.FallbackCompression != 1 || stats.FallbackURL != 1 ||
		stats.FallbackHeaders != 1 || stats.FallbackQPACK != 1 {
		t.Fatalf("stats = (%+v, %t)", stats, ok)
	}
}

func FuzzPreparedRequestHeadersDecodedSemanticParity(f *testing.F) {
	for _, seed := range [][]byte{nil, []byte("admin"), []byte("Az09-._~"), bytes.Repeat([]byte{0xff}, 256)} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 2048 {
			raw = raw[:2048]
		}
		const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~"
		value := make([]byte, len(raw))
		for i, b := range raw {
			value[i] = alphabet[int(b)%len(alphabet)]
		}
		assertPreparedRequestHeaderParity(t, newPreparedRequestHeadersTestRequest(t, context.Background(), "/bench/"+string(value)))
	})
}

func FuzzPreparedPathFieldExactQPACKV060Parity(f *testing.F) {
	for _, seed := range [][]byte{
		nil,
		[]byte("admin"),
		[]byte("Az09-._~"),
		bytes.Repeat([]byte{0xff}, 256),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) >= preparedRequestHeadersMaxPathBytes {
			raw = raw[:preparedRequestHeadersMaxPathBytes-1]
		}
		path := append([]byte{'/'}, raw...)
		assertPreparedPathFieldExactParity(t, string(path))
	})
}

func benchmarkPreparedRequestHeaders(b *testing.B, telemetry bool, parallel bool) {
	ctx := context.Background()
	if telemetry {
		ctx = WithPreparedRequestHeadersTelemetry(ctx)
	} else {
		ctx = WithPreparedRequestHeaders(ctx)
	}
	writer := newRequestWriter()
	newRequest := func() *http.Request {
		return newPreparedRequestHeadersTestRequest(b, ctx, "/bench/abcdefghijklmnopqrstuvwxyz0123456789")
	}
	if err := writer.WriteRequestHeader(io.Discard, newRequest(), false, 0, nil); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	if parallel {
		b.RunParallel(func(pb *testing.PB) {
			parallelWriter := newRequestWriter()
			req := newRequest()
			if err := parallelWriter.WriteRequestHeader(io.Discard, req, false, 0, nil); err != nil {
				b.Fatal(err)
			}
			for pb.Next() {
				if err := parallelWriter.WriteRequestHeader(io.Discard, req, false, 0, nil); err != nil {
					b.Fatal(err)
				}
			}
		})
		return
	}
	req := newRequest()
	for i := 0; i < b.N; i++ {
		if err := writer.WriteRequestHeader(io.Discard, req, false, 0, nil); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPreparedRequestHeadersAccounting(b *testing.B) {
	for _, telemetry := range []bool{false, true} {
		name := "marker-only"
		if telemetry {
			name = "telemetry"
		}
		b.Run(name, func(b *testing.B) { benchmarkPreparedRequestHeaders(b, telemetry, false) })
		b.Run(name+"-parallel", func(b *testing.B) { benchmarkPreparedRequestHeaders(b, telemetry, true) })
	}
}

func BenchmarkPreparedPathFieldEncoding(b *testing.B) {
	const path = "/bench/abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ._~-"
	b.Run("local-exact-parity", func(b *testing.B) {
		buffer := make([]byte, 0, 512)
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			buffer = appendPreparedPathField(buffer[:0], path)
		}
	})
	b.Run("stock-public-encoder", func(b *testing.B) {
		var buffer bytes.Buffer
		buffer.Grow(512)
		encoder := qpack.NewEncoder(&buffer)
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			buffer.Reset()
			if err := encoder.WriteField(qpack.HeaderField{Name: ":path", Value: path}); err != nil {
				b.Fatal(err)
			}
			if err := encoder.Close(); err != nil {
				b.Fatal(err)
			}
			block := buffer.Bytes()
			if len(block) < 2 || block[0] != 0 || block[1] != 0 {
				b.Fatalf("unexpected QPACK prefix %x", block)
			}
		}
	})
}

func TestPreparedRequestHeadersTemplateEncodingIsDeterministic(t *testing.T) {
	req := newPreparedRequestHeadersTestRequest(t, context.Background(), "/bench/sorted")
	first, ok := newPreparedRequestHeaderTemplate(req, req.URL.Host)
	if !ok {
		t.Fatal("failed to build first template")
	}
	second, ok := newPreparedRequestHeaderTemplate(req, req.URL.Host)
	if !ok {
		t.Fatal("failed to build second template")
	}
	if !bytes.Equal(first.prefix, second.prefix) || !bytes.Equal(first.suffix, second.suffix) {
		t.Fatal("template encoding depends on Go map iteration order")
	}
	first.zero()
	second.zero()
}
