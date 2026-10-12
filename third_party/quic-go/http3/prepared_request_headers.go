package http3

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptrace"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/quic-go/qpack"
	"github.com/quic-go/quic-go/qlogwriter"
	"golang.org/x/net/http/httpguts"
	"golang.org/x/net/http2/hpack"
)

const (
	preparedRequestHeadersMaxHeaderNames = 8
	preparedRequestHeadersMaxValues      = 16
	preparedRequestHeadersMaxStaticBytes = 16 << 10
	preparedRequestHeadersMaxPathBytes   = 8 << 10
	preparedRequestHeadersMaxFrameBytes  = 32 << 10
)

type preparedRequestHeadersContextKey struct{}

type preparedRequestHeadersConfig struct {
	counters *preparedRequestHeadersCounters
}

// PreparedRequestHeadersStats reports use of the explicitly enabled prepared
// request-header path. Every attempt is counted as either a hit or exactly one
// fallback reason.
type PreparedRequestHeadersStats struct {
	Attempts            uint64
	Hits                uint64
	FallbackQLog        uint64
	FallbackTrace       uint64
	FallbackMethod      uint64
	FallbackBody        uint64
	FallbackCompression uint64
	FallbackURL         uint64
	FallbackHeaders     uint64
	FallbackQPACK       uint64
}

type preparedRequestHeadersFallback uint8

const (
	preparedRequestHeadersHit preparedRequestHeadersFallback = iota
	preparedRequestHeadersFallbackQLog
	preparedRequestHeadersFallbackTrace
	preparedRequestHeadersFallbackMethod
	preparedRequestHeadersFallbackBody
	preparedRequestHeadersFallbackCompression
	preparedRequestHeadersFallbackURL
	preparedRequestHeadersFallbackHeaders
	preparedRequestHeadersFallbackQPACK
	preparedRequestHeadersOutcomeCount
)

type preparedRequestHeadersCounters struct {
	outcomes [preparedRequestHeadersOutcomeCount]atomic.Uint64
}

// WithPreparedRequestHeaders explicitly enables prepared request headers for
// requests derived from ctx. It is safe to call more than once. The HTTP/3
// client still validates every request and uses the generic encoder whenever
// the request isn't eligible.
//
// This variant doesn't collect telemetry and therefore performs no atomic
// counter updates on the request path. Use WithPreparedRequestHeadersTelemetry
// when runtime counters are required. The parent ctx must not be nil.
func WithPreparedRequestHeaders(ctx context.Context) context.Context {
	if ctx == nil {
		panic("http3: nil context passed to WithPreparedRequestHeaders")
	}
	if config, _ := ctx.Value(preparedRequestHeadersContextKey{}).(*preparedRequestHeadersConfig); config != nil {
		return ctx
	}
	return context.WithValue(ctx, preparedRequestHeadersContextKey{}, &preparedRequestHeadersConfig{})
}

// WithPreparedRequestHeadersTelemetry explicitly enables prepared request
// headers and concurrency-safe hit and fallback counters for requests derived
// from ctx. It is safe to call more than once on the returned context. The
// parent ctx must not be nil.
func WithPreparedRequestHeadersTelemetry(ctx context.Context) context.Context {
	if ctx == nil {
		panic("http3: nil context passed to WithPreparedRequestHeadersTelemetry")
	}
	if config, _ := ctx.Value(preparedRequestHeadersContextKey{}).(*preparedRequestHeadersConfig); config != nil && config.counters != nil {
		return ctx
	}
	return context.WithValue(ctx, preparedRequestHeadersContextKey{}, &preparedRequestHeadersConfig{
		counters: &preparedRequestHeadersCounters{},
	})
}

// SnapshotPreparedRequestHeaders returns a consistent per-counter snapshot for
// ctx. The boolean is false when telemetry wasn't enabled. Attempts is derived
// from the mutually exclusive outcome counters, so accounting remains exact
// while the request path performs only one atomic increment per attempt.
func SnapshotPreparedRequestHeaders(ctx context.Context) (PreparedRequestHeadersStats, bool) {
	if ctx == nil {
		return PreparedRequestHeadersStats{}, false
	}
	config, _ := ctx.Value(preparedRequestHeadersContextKey{}).(*preparedRequestHeadersConfig)
	if config == nil || config.counters == nil {
		return PreparedRequestHeadersStats{}, false
	}
	values := [preparedRequestHeadersOutcomeCount]uint64{}
	for i := range values {
		values[i] = config.counters.outcomes[i].Load()
	}
	stats := PreparedRequestHeadersStats{
		Hits:                values[preparedRequestHeadersHit],
		FallbackQLog:        values[preparedRequestHeadersFallbackQLog],
		FallbackTrace:       values[preparedRequestHeadersFallbackTrace],
		FallbackMethod:      values[preparedRequestHeadersFallbackMethod],
		FallbackBody:        values[preparedRequestHeadersFallbackBody],
		FallbackCompression: values[preparedRequestHeadersFallbackCompression],
		FallbackURL:         values[preparedRequestHeadersFallbackURL],
		FallbackHeaders:     values[preparedRequestHeadersFallbackHeaders],
		FallbackQPACK:       values[preparedRequestHeadersFallbackQPACK],
	}
	for _, value := range values {
		stats.Attempts += value
	}
	return stats, true
}

func preparedRequestHeadersConfigFromRequest(req *http.Request) *preparedRequestHeadersConfig {
	if req == nil {
		return nil
	}
	config, _ := req.Context().Value(preparedRequestHeadersContextKey{}).(*preparedRequestHeadersConfig)
	return config
}

func (c *preparedRequestHeadersConfig) record(outcome preparedRequestHeadersFallback) {
	if c != nil && c.counters != nil {
		c.counters.outcomes[outcome].Add(1)
	}
}

type preparedRequestHeaderTemplate struct {
	rawHost string
	scheme  string
	headers http.Header
	prefix  []byte
	suffix  []byte
}

func (p *preparedRequestHeaderTemplate) zero() {
	if p == nil {
		return
	}
	zeroBytesToCapacity(p.prefix)
	zeroBytesToCapacity(p.suffix)
	p.prefix = nil
	p.suffix = nil
	for key, values := range p.headers {
		for i := range values {
			values[i] = ""
		}
		delete(p.headers, key)
	}
	p.headers = nil
	p.rawHost = ""
	p.scheme = ""
}

type preparedRequestHeaderBuffers struct {
	path  []byte
	frame []byte
}

var preparedRequestHeaderBufferPool = sync.Pool{
	New: func() any { return &preparedRequestHeaderBuffers{} },
}

func zeroBytesToCapacity(buf []byte) {
	if cap(buf) != 0 {
		clear(buf[:cap(buf)])
	}
}

func releasePreparedRequestHeaderBuffers(buffers *preparedRequestHeaderBuffers) {
	zeroBytesToCapacity(buffers.path)
	zeroBytesToCapacity(buffers.frame)
	pathCapacity := cap(buffers.path)
	frameCapacity := cap(buffers.frame)
	buffers.path = buffers.path[:0]
	buffers.frame = buffers.frame[:0]
	if pathCapacity > preparedRequestHeadersMaxPathBytes*2 || frameCapacity > preparedRequestHeadersMaxFrameBytes {
		return
	}
	preparedRequestHeaderBufferPool.Put(buffers)
}

func validatePreparedRequestHeaders(req *http.Request, gzip bool) (rawHost, path string, reason preparedRequestHeadersFallback) {
	if req.Method != http.MethodHead {
		return "", "", preparedRequestHeadersFallbackMethod
	}
	if req.Body != nil || req.GetBody != nil || req.ContentLength != 0 ||
		len(req.Trailer) != 0 || len(req.TransferEncoding) != 0 {
		return "", "", preparedRequestHeadersFallbackBody
	}
	if gzip {
		return "", "", preparedRequestHeadersFallbackCompression
	}
	if req.URL == nil || req.URL.Scheme != "https" || req.URL.Host == "" || req.URL.User != nil ||
		req.URL.Opaque != "" || req.URL.RawPath != "" || req.URL.RawQuery != "" || req.URL.ForceQuery ||
		req.URL.Fragment != "" || req.URL.RawFragment != "" {
		return "", "", preparedRequestHeadersFallbackURL
	}
	rawHost = req.Host
	if rawHost == "" {
		rawHost = req.URL.Host
	}
	if len(rawHost) > 1024 {
		return "", "", preparedRequestHeadersFallbackURL
	}
	authority, err := httpguts.PunycodeHostPort(rawHost)
	if err != nil || !httpguts.ValidHostHeader(authority) {
		return "", "", preparedRequestHeadersFallbackURL
	}
	path = req.URL.RequestURI()
	if len(path) == 0 || len(path) > preparedRequestHeadersMaxPathBytes || !validPseudoPath(path) {
		return "", "", preparedRequestHeadersFallbackURL
	}

	if len(req.Header) < 2 || len(req.Header) > preparedRequestHeadersMaxHeaderNames {
		return "", "", preparedRequestHeadersFallbackHeaders
	}
	var headerKeys [preparedRequestHeadersMaxHeaderNames]string
	headerKeyCount := 0
	acceptEncoding := false
	userAgent := false
	valueCount := 0
	rawBytes := 0
	for key, values := range req.Header {
		if !httpguts.ValidHeaderFieldName(key) ||
			strings.EqualFold(key, "host") || strings.EqualFold(key, "content-length") ||
			strings.EqualFold(key, "connection") || strings.EqualFold(key, "proxy-connection") ||
			strings.EqualFold(key, "transfer-encoding") || strings.EqualFold(key, "upgrade") ||
			strings.EqualFold(key, "keep-alive") || strings.EqualFold(key, "range") {
			return "", "", preparedRequestHeadersFallbackHeaders
		}
		for _, previous := range headerKeys[:headerKeyCount] {
			if strings.EqualFold(previous, key) {
				return "", "", preparedRequestHeadersFallbackHeaders
			}
		}
		headerKeys[headerKeyCount] = key
		headerKeyCount++
		valueCount += len(values)
		rawBytes += len(key)
		if valueCount > preparedRequestHeadersMaxValues || rawBytes > preparedRequestHeadersMaxStaticBytes {
			return "", "", preparedRequestHeadersFallbackHeaders
		}
		for _, value := range values {
			if !httpguts.ValidHeaderFieldValue(value) {
				return "", "", preparedRequestHeadersFallbackHeaders
			}
			rawBytes += len(value)
			if rawBytes > preparedRequestHeadersMaxStaticBytes {
				return "", "", preparedRequestHeadersFallbackHeaders
			}
		}
		switch {
		case strings.EqualFold(key, "accept-encoding"):
			if key != "Accept-Encoding" || acceptEncoding || len(values) != 1 || values[0] != "identity" {
				return "", "", preparedRequestHeadersFallbackHeaders
			}
			acceptEncoding = true
		case strings.EqualFold(key, "user-agent"):
			if key != "User-Agent" || userAgent || len(values) != 1 || values[0] == "" {
				return "", "", preparedRequestHeadersFallbackHeaders
			}
			userAgent = true
		}
	}
	if !acceptEncoding || !userAgent {
		return "", "", preparedRequestHeadersFallbackHeaders
	}
	return rawHost, path, preparedRequestHeadersHit
}

func encodePreparedRequestHeaderBlock(fields []qpack.HeaderField) ([]byte, bool) {
	var buffer bytes.Buffer
	encoder := qpack.NewEncoder(&buffer)
	for _, field := range fields {
		if err := encoder.WriteField(field); err != nil {
			zeroBytesToCapacity(buffer.Bytes())
			return nil, false
		}
	}
	if err := encoder.Close(); err != nil {
		zeroBytesToCapacity(buffer.Bytes())
		return nil, false
	}
	block := buffer.Bytes()
	if len(block) < 2 || block[0] != 0 || block[1] != 0 {
		zeroBytesToCapacity(block)
		return nil, false
	}
	result := append([]byte(nil), block...)
	zeroBytesToCapacity(block)
	return result, true
}

// appendPreparedQPACKInteger appends the integer representation from RFC 9204
// section 4.1.1. It leaves the high bits of the first byte clear so the caller
// can set the field-line pattern.
func appendPreparedQPACKInteger(dst []byte, prefixBits uint8, value uint64) []byte {
	maxPrefixValue := uint64(1<<prefixBits) - 1
	if value < maxPrefixValue {
		return append(dst, byte(value))
	}
	dst = append(dst, byte(maxPrefixValue))
	value -= maxPrefixValue
	for value >= 128 {
		dst = append(dst, byte(value%128+128))
		value /= 128
	}
	return append(dst, byte(value))
}

// appendPreparedPathField encodes a :path field using QPACK's static-table
// name reference. Static template construction still uses qpack.Encoder. This
// small hot-path encoder is byte-parity tested and fuzzed against qpack v0.6.0.
func appendPreparedPathField(dst []byte, path string) []byte {
	if path == "/" {
		// QPACK static-table entry 1 is exactly :path: /.
		return append(dst, 0xc1)
	}
	offset := len(dst)
	dst = appendPreparedQPACKInteger(dst, 4, 1)
	dst[offset] ^= 0x50 // 01NTxxxx: N = 0, T = 1.
	offset = len(dst)
	dst = appendPreparedQPACKInteger(dst, 7, uint64(hpack.HuffmanEncodeLength(path)))
	dst[offset] ^= 0x80
	return hpack.AppendHuffmanString(dst, path)
}

func newPreparedRequestHeaderTemplate(req *http.Request, rawHost string) (*preparedRequestHeaderTemplate, bool) {
	authority, err := httpguts.PunycodeHostPort(rawHost)
	if err != nil || !httpguts.ValidHostHeader(authority) {
		return nil, false
	}
	prefix, ok := encodePreparedRequestHeaderBlock([]qpack.HeaderField{
		{Name: ":authority", Value: authority},
		{Name: ":method", Value: http.MethodHead},
	})
	if !ok {
		return nil, false
	}
	keys := make([]string, 0, len(req.Header))
	for key := range req.Header {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	fields := make([]qpack.HeaderField, 0, 1+len(req.Header))
	fields = append(fields, qpack.HeaderField{Name: ":scheme", Value: req.URL.Scheme})
	for _, key := range keys {
		for _, value := range req.Header[key] {
			fields = append(fields, qpack.HeaderField{Name: strings.ToLower(key), Value: value})
		}
	}
	suffixBlock, ok := encodePreparedRequestHeaderBlock(fields)
	if !ok || len(prefix)+len(suffixBlock)-2 > preparedRequestHeadersMaxStaticBytes {
		zeroBytesToCapacity(prefix)
		zeroBytesToCapacity(suffixBlock)
		return nil, false
	}
	suffix := append([]byte(nil), suffixBlock[2:]...)
	zeroBytesToCapacity(suffixBlock)
	return &preparedRequestHeaderTemplate{
		rawHost: rawHost,
		scheme:  req.URL.Scheme,
		headers: req.Header.Clone(),
		prefix:  prefix,
		suffix:  suffix,
	}, true
}

func (p *preparedRequestHeaderTemplate) matches(req *http.Request, rawHost string) bool {
	if p == nil || rawHost != p.rawHost || req.URL.Scheme != p.scheme || len(req.Header) != len(p.headers) {
		return false
	}
	for key, want := range p.headers {
		got, ok := req.Header[key]
		if !ok || len(got) != len(want) {
			return false
		}
		for i := range want {
			if got[i] != want[i] {
				return false
			}
		}
	}
	return true
}

func (w *requestWriter) prepareRequestHeader(
	req *http.Request,
	gzip bool,
	qlogger qlogwriter.Recorder,
) (*preparedRequestHeaderBuffers, bool) {
	config := preparedRequestHeadersConfigFromRequest(req)
	if config == nil {
		return nil, false
	}
	if qlogger != nil {
		config.record(preparedRequestHeadersFallbackQLog)
		return nil, false
	}
	trace := httptrace.ContextClientTrace(req.Context())
	if traceHasWroteHeaderField(trace) || (trace != nil && trace.WroteHeaders != nil) {
		config.record(preparedRequestHeadersFallbackTrace)
		return nil, false
	}
	rawHost, path, reason := validatePreparedRequestHeaders(req, gzip)
	if reason != preparedRequestHeadersHit {
		config.record(reason)
		return nil, false
	}

	w.mutex.Lock()
	prepared := w.preparedRequestHeaders
	if !prepared.matches(req, rawHost) {
		var ok bool
		prepared, ok = newPreparedRequestHeaderTemplate(req, rawHost)
		if !ok {
			w.mutex.Unlock()
			config.record(preparedRequestHeadersFallbackQPACK)
			return nil, false
		}
		w.preparedRequestHeaders.zero()
		w.preparedRequestHeaders = prepared
	}
	buffers := preparedRequestHeaderBufferPool.Get().(*preparedRequestHeaderBuffers)
	buffers.path = appendPreparedPathField(buffers.path[:0], path)
	payloadLength := len(prepared.prefix) + len(buffers.path) + len(prepared.suffix)
	buffers.frame = (&headersFrame{Length: uint64(payloadLength)}).Append(buffers.frame[:0])
	buffers.frame = append(buffers.frame, prepared.prefix...)
	buffers.frame = append(buffers.frame, buffers.path...)
	buffers.frame = append(buffers.frame, prepared.suffix...)
	w.mutex.Unlock()

	if len(buffers.frame) > preparedRequestHeadersMaxFrameBytes {
		releasePreparedRequestHeaderBuffers(buffers)
		config.record(preparedRequestHeadersFallbackQPACK)
		return nil, false
	}
	config.record(preparedRequestHeadersHit)
	return buffers, true
}

func (w *requestWriter) writePreparedRequestHeader(
	wr io.Writer,
	req *http.Request,
	gzip bool,
	qlogger qlogwriter.Recorder,
) (bool, error) {
	buffers, ok := w.prepareRequestHeader(req, gzip, qlogger)
	if !ok {
		return false, nil
	}
	_, err := wr.Write(buffers.frame)
	releasePreparedRequestHeaderBuffers(buffers)
	return true, err
}
