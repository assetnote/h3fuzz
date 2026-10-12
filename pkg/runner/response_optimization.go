package runner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

// responseOptimizationMode is deliberately private. A response optimization is
// selected once, before a run, and callers receive a value report with no
// mutators rather than shared state that can change under active workers.
type responseOptimizationMode uint8

const (
	responseOptimizationGET responseOptimizationMode = iota
	responseOptimizationHEAD
	responseOptimizationRange
	responseOptimizationMaxMissBody = 4 << 10
)

func (m responseOptimizationMode) String() string {
	switch m {
	case responseOptimizationHEAD:
		return http.MethodHead
	case responseOptimizationRange:
		return "RANGE"
	default:
		return http.MethodGet
	}
}

// responseOptimizationReport is the immutable result of the pre-run probes.
// headReason and rangeReason retain the evidence behind a fallback or choice;
// probes is the number of network requests attempted by the detector.
type responseOptimizationReport struct {
	mode                    responseOptimizationMode
	reason                  string
	headReason              string
	rangeReason             string
	probes                  int
	baselineProbes          int
	baselineReliable        bool
	headProbes              int
	rangeProbes             int
	baselineElapsed         time.Duration
	headElapsed             time.Duration
	rangeElapsed            time.Duration
	headAttempted           bool
	headReliable            bool
	rangeAttempted          bool
	rangeReliable           bool
	validateRuntimeStatuses bool
	trustedRuntimeStatuses  [600]bool
}

func getResponseOptimization(reason string) responseOptimizationReport {
	return responseOptimizationReport{
		mode:   responseOptimizationGET,
		reason: reason,
	}
}

// apply mutates a newly-created request immediately before dispatch. It is
// intentionally defensive: a later integration step, preflight, or caller may
// have changed the request since detection. In that case an explicit method,
// body, or Range header always wins over the automatic optimization.
func (r *responseOptimizationReport) apply(req *http.Request) bool {
	if req == nil || r.mode == responseOptimizationGET {
		return false
	}
	if !strings.EqualFold(req.Method, http.MethodGet) || requestHasBody(req) {
		return false
	}
	if strings.TrimSpace(req.Header.Get("Range")) != "" {
		return false
	}

	// Range is defined over the selected representation. Identity encoding keeps
	// Content-Range totals stable and makes the logical full size reconstructable.
	req.Header.Set("Accept-Encoding", "identity")
	switch r.mode {
	case responseOptimizationHEAD:
		req.Method = http.MethodHead
		// quic-go's HTTP/3 ClientConn starts its asynchronous request-body path
		// whenever Body is non-nil. http.NoBody therefore still allocates an 8 KiB
		// copy buffer for every bodyless HEAD request. A nil Body has the same wire
		// semantics and lets the transport close the send stream directly.
		req.Body = nil
		req.GetBody = nil
		req.ContentLength = 0
		req.Header.Del("Content-Length")
		return true
	case responseOptimizationRange:
		req.Header.Set("Range", "bytes=-1")
		return true
	default:
		return false
	}
}

func requestHasBody(req *http.Request) bool {
	if req.ContentLength > 0 {
		return true
	}
	return req.Body != nil && req.Body != http.NoBody
}

// logicalResponseMetadata contains matcher-facing metadata. trustworthy is
// false when a server stops honoring the behavior proven by the pre-run probe;
// integration should retry that request with GET rather than invent metadata.
type logicalResponseMetadata struct {
	statusCode    int
	contentLength int64
	normalized    bool
	trustworthy   bool
}

func (r *responseOptimizationReport) logicalMetadata(statusCode int, header http.Header, bodyBytes int64) logicalResponseMetadata {
	metadata := normalizeResponseMetadata(r.mode, statusCode, header, bodyBytes)
	if !metadata.trustworthy || !r.validateRuntimeStatuses {
		return metadata
	}
	checkStatus := r.mode == responseOptimizationHEAD
	if r.mode == responseOptimizationRange && statusCode != http.StatusPartialContent && statusCode != http.StatusRequestedRangeNotSatisfiable {
		checkStatus = true
	}
	if checkStatus && (statusCode < 0 || statusCode >= len(r.trustedRuntimeStatuses) || !r.trustedRuntimeStatuses[statusCode]) {
		metadata.trustworthy = false
	}
	return metadata
}

// normalizeResponseMetadata reconstructs the representation metadata hidden by
// HEAD or bytes=-1. It never normalizes an unparseable or internally
// inconsistent Content-Range.
func normalizeResponseMetadata(mode responseOptimizationMode, statusCode int, header http.Header, bodyBytes int64) logicalResponseMetadata {
	metadata := logicalResponseMetadata{
		statusCode:    statusCode,
		contentLength: bodyBytes,
		trustworthy:   true,
	}

	switch mode {
	case responseOptimizationHEAD:
		// Redirect implementations frequently omit the small explanatory body (and
		// advertise length zero) for HEAD even when GET carries one. Preserve exact
		// matcher metadata by retrying redirects with GET.
		if statusCode >= 300 && statusCode < 400 {
			metadata.trustworthy = false
			return metadata
		}
		if bodyBytes != 0 {
			metadata.trustworthy = false
			return metadata
		}
		length, ok := headerContentLength(header)
		metadata.contentLength = length
		metadata.normalized = ok
		metadata.trustworthy = ok
		return metadata
	case responseOptimizationRange:
		parsed, ok := parseContentRange(header.Get("Content-Range"))
		switch statusCode {
		case http.StatusPartialContent:
			if !ok || parsed.unsatisfied || bodyBytes != 1 || parsed.start != parsed.end || parsed.end != parsed.total-1 {
				metadata.trustworthy = false
				return metadata
			}
			metadata.statusCode = http.StatusOK
			metadata.contentLength = parsed.total
			metadata.normalized = true
			return metadata
		case http.StatusRequestedRangeNotSatisfiable:
			if !ok || !parsed.unsatisfied || parsed.total != 0 || bodyBytes != 0 {
				metadata.trustworthy = false
				return metadata
			}
			metadata.statusCode = http.StatusOK
			metadata.contentLength = 0
			metadata.normalized = true
			return metadata
		case http.StatusOK:
			// A 200 after bytes=-1 means the server ignored Range. Do not let a
			// full response masquerade as validated optimized metadata.
			metadata.trustworthy = false
			return metadata
		default:
			// Error and redirect statuses were required to survive unchanged by
			// the probe. Their observed body length is already logical.
			return metadata
		}
	default:
		return metadata
	}
}

type parsedContentRange struct {
	start       int64
	end         int64
	total       int64
	unsatisfied bool
}

// parseContentRange accepts the two forms needed by the optimizer:
// "bytes N-N/T" and the empty-entity response "bytes */0".
func parseContentRange(value string) (parsedContentRange, bool) {
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) != 2 || !strings.EqualFold(fields[0], "bytes") {
		return parsedContentRange{}, false
	}

	rangePart, totalPart, ok := strings.Cut(fields[1], "/")
	if !ok || rangePart == "" || totalPart == "" || totalPart == "*" {
		return parsedContentRange{}, false
	}
	total, err := strconv.ParseInt(totalPart, 10, 64)
	if err != nil || total < 0 {
		return parsedContentRange{}, false
	}
	if rangePart == "*" {
		return parsedContentRange{total: total, unsatisfied: true}, true
	}

	startPart, endPart, ok := strings.Cut(rangePart, "-")
	if !ok || startPart == "" || endPart == "" {
		return parsedContentRange{}, false
	}
	start, startErr := strconv.ParseInt(startPart, 10, 64)
	end, endErr := strconv.ParseInt(endPart, 10, 64)
	if startErr != nil || endErr != nil || start < 0 || end < start || total == 0 || end >= total {
		return parsedContentRange{}, false
	}
	return parsedContentRange{start: start, end: end, total: total}, true
}

type responseOptimizationClient interface {
	Do(*http.Request) (*http.Response, error)
}

type responseProbeResult struct {
	statusCode     int
	contentType    string
	location       string
	finalURL       string
	bodyLength     int64
	declaredLength int64
	contentRange   string
}

type responseProbeOutcome struct {
	result responseProbeResult
	err    error
}

type responseProbeSample struct {
	name string
	url  string
	full responseProbeOutcome
	head responseProbeOutcome
	rng  responseProbeOutcome
}

// detectResponseOptimization performs a conservative, bounded pre-run probe.
// Failure to prove an optimization is never fatal to the scan: the report
// explains the fallback and leaves requests as ordinary GETs.
func detectResponseOptimization(
	ctx context.Context,
	client responseOptimizationClient,
	conf *ffuf.Config,
	baseReq *ffuf.Request,
) responseOptimizationReport {
	keywords, ineligible := responseOptimizationEligibility(conf, baseReq)
	if ineligible != "" {
		return getResponseOptimization(ineligible)
	}
	if client == nil {
		return getResponseOptimization("response optimization unavailable: HTTP client is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	probeURLs, err := responseOptimizationProbeURLs(baseReq.Url, keywords)
	if err != nil {
		return getResponseOptimization("response optimization unavailable: " + err.Error())
	}
	samples := []responseProbeSample{
		{name: "likely-existing", url: probeURLs[0]},
		{name: "random-miss-1", url: probeURLs[1]},
		{name: "random-miss-2", url: probeURLs[2]},
	}
	report := getResponseOptimization("")

	baselineStarted := time.Now()
	for i := range samples {
		report.probes++
		report.baselineProbes++
		samples[i].full.result, samples[i].full.err = executeResponseOptimizationProbe(
			ctx, client, baseReq, http.MethodGet, samples[i].url, false,
		)
		if samples[i].full.err != nil {
			report.baselineElapsed = time.Since(baselineStarted)
			report.reason = fmt.Sprintf("GET baseline probe %s failed after %d probes: %v", samples[i].name, report.probes, samples[i].full.err)
			return report
		}
	}
	report.baselineElapsed = time.Since(baselineStarted)
	report.baselineReliable = true

	report.headAttempted = true
	headStarted := time.Now()
	for i := range samples {
		report.probes++
		report.headProbes++
		samples[i].head.result, samples[i].head.err = executeResponseOptimizationProbe(
			ctx, client, baseReq, http.MethodHead, samples[i].url, false,
		)
	}
	report.headElapsed = time.Since(headStarted)
	if reason := qualifyHEADOptimization(samples); reason == "" {
		report.mode = responseOptimizationHEAD
		report.headReliable = true
		report.setTrustedRuntimeStatuses(samples, responseOptimizationHEAD)
		report.headReason = "HEAD preserved status, redirect behavior, content type, and full length across all three samples"
		report.reason = report.headReason
		report.rangeReason = "not run because HEAD was reliable"
		return report
	} else {
		report.headReason = reason
	}

	report.rangeAttempted = true
	rangeStarted := time.Now()
	for i := range samples {
		report.probes++
		report.rangeProbes++
		samples[i].rng.result, samples[i].rng.err = executeResponseOptimizationProbe(
			ctx, client, baseReq, http.MethodGet, samples[i].url, true,
		)
	}
	report.rangeElapsed = time.Since(rangeStarted)
	if reason := qualifyRangeOptimization(samples); reason == "" {
		report.mode = responseOptimizationRange
		report.rangeReliable = true
		report.setTrustedRuntimeStatuses(samples, responseOptimizationRange)
		report.rangeReason = "Range bytes=-1 returned a trustworthy one-byte 206 for the 200 hit and preserved both miss statuses"
		report.reason = report.rangeReason + "; HEAD rejected: " + report.headReason
		return report
	} else {
		report.rangeReason = reason
	}

	report.reason = "using GET; HEAD rejected: " + report.headReason + "; Range rejected: " + report.rangeReason
	return report
}

func (r *responseOptimizationReport) setTrustedRuntimeStatuses(samples []responseProbeSample, mode responseOptimizationMode) {
	r.validateRuntimeStatuses = true
	for _, sample := range samples {
		status := sample.full.result.statusCode
		if mode == responseOptimizationHEAD {
			status = sample.head.result.statusCode
		} else if mode == responseOptimizationRange {
			status = sample.rng.result.statusCode
		}
		if status >= 0 && status < len(r.trustedRuntimeStatuses) {
			r.trustedRuntimeStatuses[status] = true
		}
	}
}

func responseOptimizationEligibility(conf *ffuf.Config, baseReq *ffuf.Request) ([]string, string) {
	if conf == nil {
		return nil, "response optimization unavailable: configuration is nil"
	}
	if conf.NoResponseOptimization {
		return nil, "response optimization disabled by configuration"
	}
	if baseReq == nil {
		return nil, "response optimization unavailable: base request is nil"
	}
	if !strings.EqualFold(baseReq.Method, http.MethodGet) {
		return nil, "response optimization requires an explicit GET discovery request"
	}
	if len(baseReq.Data) != 0 {
		return nil, "response optimization requires a bodyless GET"
	}
	if conf.Raw {
		return nil, "response optimization requires GET because raw URL semantics are enabled"
	}
	if headerValue(baseReq.Headers, "Range") != "" {
		return nil, "response optimization will not override a user-supplied Range header"
	}
	if encoding := strings.TrimSpace(headerValue(baseReq.Headers, "Accept-Encoding")); encoding != "" && !strings.EqualFold(encoding, "identity") {
		return nil, "response optimization will not override a user-supplied non-identity Accept-Encoding header"
	}
	if len(conf.Preflights) > 0 {
		return nil, "response optimization requires GET because preflight substitutions can change request semantics"
	}
	if conf.AutoCalibration || conf.AutoCalibrationPerHost {
		return nil, "response optimization requires GET because autocalibration is enabled"
	}
	if conf.AuditLog != "" || conf.OutputDirectory != "" || conf.OutputFile != "" {
		return nil, "response optimization requires GET because audit or output artifacts are enabled"
	}
	if conf.ScraperNeedsResponseBody {
		return nil, "response optimization requires GET because an active scraper consumes response bodies"
	}
	if reason := bodyDependentMatcherReason(conf, baseReq.Url); reason != "" {
		return nil, reason
	}

	keywords := responseOptimizationKeywords(conf)
	active := make([]string, 0, len(keywords))
	for _, keyword := range keywords {
		if strings.Contains(baseReq.Url, keyword) {
			active = append(active, keyword)
		}
	}
	if len(active) == 0 {
		return nil, "response optimization requires a fuzz keyword in the request URL"
	}

	parsed, err := url.Parse(baseReq.Url)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, "response optimization requires an absolute HTTP URL"
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, "response optimization requires an HTTP or HTTPS URL"
	}
	// A scan can have separate providers for its path and headers. The URL-active
	// keywords above identify what to remove for the baseline URL, but every
	// configured keyword must be checked here so PATH in the URL plus TENANT in a
	// header cannot be mistaken for URL-only discovery.
	for _, keyword := range keywords {
		if strings.Contains(parsed.Host, keyword) || strings.Contains(baseReq.Host, keyword) {
			return nil, "response optimization does not apply to host discovery"
		}
		for name, value := range baseReq.Headers {
			if strings.Contains(name, keyword) || strings.Contains(value, keyword) {
				return nil, "response optimization does not apply when headers are also fuzzed"
			}
		}
	}
	return active, ""
}

func bodyDependentMatcherReason(conf *ffuf.Config, rawURL string) string {
	if conf.MatcherManager == nil {
		return ""
	}
	bodyDependent := func(providers map[string]ffuf.FilterProvider) string {
		for _, name := range []string{"regexp", "word", "line", "time"} {
			if _, ok := providers[name]; ok {
				return name
			}
		}
		return ""
	}
	if name := bodyDependent(conf.MatcherManager.GetMatchers()); name != "" {
		return fmt.Sprintf("response optimization requires GET because a body-dependent %s matcher is configured", name)
	}
	if name := bodyDependent(conf.MatcherManager.GetFilters()); name != "" {
		return fmt.Sprintf("response optimization requires GET because a body-dependent %s filter is configured", name)
	}
	if parsed, err := url.Parse(rawURL); err == nil {
		if name := bodyDependent(conf.MatcherManager.FiltersForDomain(parsed.Host)); name != "" {
			return fmt.Sprintf("response optimization requires GET because a per-host body-dependent %s filter is configured", name)
		}
	}
	return ""
}

func responseOptimizationKeywords(conf *ffuf.Config) []string {
	seen := make(map[string]struct{})
	keywords := make([]string, 0, len(conf.InputProviders)+2)
	add := func(keyword string) {
		if keyword == "" {
			return
		}
		if _, ok := seen[keyword]; ok {
			return
		}
		seen[keyword] = struct{}{}
		keywords = append(keywords, keyword)
	}
	// InputProviders is the source of truth for both wordlist and command input
	// keywords. CommandKeywords is historical command-input bookkeeping and, for a
	// named -input-cmd, may contain the command text rather than its keyword. Using
	// it here made ordinary `-w words.txt:PATH -u .../PATH` scans silently ineligible
	// for response optimization.
	for _, provider := range conf.InputProviders {
		add(provider.Keyword)
	}
	// Preserve compatibility with small programmatic configurations that predate
	// InputProviders and populated CommandKeywords directly. Production CLI
	// configurations always have an InputProvider for each active fuzz source.
	if len(keywords) == 0 {
		for _, keyword := range conf.CommandKeywords {
			add(keyword)
		}
	}
	if len(keywords) == 0 {
		add(conf.AutoCalibrationKeyword)
		add("FUZZ")
	}
	sort.SliceStable(keywords, func(i, j int) bool { return len(keywords[i]) > len(keywords[j]) })
	return keywords
}

func responseOptimizationProbeURLs(rawURL string, keywords []string) ([3]string, error) {
	var urls [3]string
	urls[0] = replaceKeywords(rawURL, keywords, "")
	for i := 1; i < len(urls); i++ {
		urls[i] = replaceKeywords(rawURL, keywords, responseOptimizationMissToken())
	}
	for _, candidate := range urls {
		parsed, err := url.Parse(candidate)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return [3]string{}, fmt.Errorf("could not construct probe URL from %q", candidate)
		}
	}
	if urls[0] == urls[1] || urls[0] == urls[2] || urls[1] == urls[2] {
		return [3]string{}, fmt.Errorf("could not construct distinct response-optimization samples")
	}
	return urls, nil
}

func replaceKeywords(value string, keywords []string, replacement string) string {
	for _, keyword := range keywords {
		value = strings.ReplaceAll(value, keyword, replacement)
	}
	return value
}

var responseOptimizationMissCounter atomic.Uint64

func responseOptimizationMissToken() string {
	var random [12]byte
	if _, err := rand.Read(random[:]); err == nil {
		return ".h3fuzz-response-probe-" + hex.EncodeToString(random[:])
	}
	return fmt.Sprintf(".h3fuzz-response-probe-%d", responseOptimizationMissCounter.Add(1))
}

func executeResponseOptimizationProbe(
	ctx context.Context,
	client responseOptimizationClient,
	baseReq *ffuf.Request,
	method string,
	rawURL string,
	ranged bool,
) (responseProbeResult, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
	if err != nil {
		return responseProbeResult{}, err
	}
	for name, value := range baseReq.Headers {
		if strings.EqualFold(name, "Host") {
			if req.Host == "" {
				req.Host = value
			}
			continue
		}
		if strings.EqualFold(name, "Range") || strings.EqualFold(name, "Content-Length") {
			continue
		}
		req.Header.Set(name, value)
	}
	if baseReq.Host != "" {
		req.Host = baseReq.Host
	}
	req.Header.Set("Accept-Encoding", "identity")
	if ranged {
		req.Header.Set("Range", "bytes=-1")
	}

	resp, err := client.Do(req)
	if err != nil {
		return responseProbeResult{}, err
	}
	if resp == nil {
		return responseProbeResult{}, fmt.Errorf("HTTP client returned a nil response")
	}
	if resp.Body == nil {
		resp.Body = http.NoBody
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, int64(MAX_DOWNLOAD_SIZE)+1))
	closeErr := resp.Body.Close()
	if readErr != nil {
		return responseProbeResult{}, readErr
	}
	if closeErr != nil {
		return responseProbeResult{}, closeErr
	}
	if len(body) > MAX_DOWNLOAD_SIZE {
		return responseProbeResult{}, fmt.Errorf("probe response exceeded %d bytes", MAX_DOWNLOAD_SIZE)
	}

	finalURL := rawURL
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	return responseProbeResult{
		statusCode:     resp.StatusCode,
		contentType:    strings.TrimSpace(resp.Header.Get("Content-Type")),
		location:       strings.TrimSpace(resp.Header.Get("Location")),
		finalURL:       finalURL,
		bodyLength:     int64(len(body)),
		declaredLength: responseDeclaredLength(resp),
		contentRange:   strings.TrimSpace(resp.Header.Get("Content-Range")),
	}, nil
}

func responseDeclaredLength(resp *http.Response) int64 {
	if resp.ContentLength >= 0 {
		return resp.ContentLength
	}
	length, ok := headerContentLength(resp.Header)
	if !ok {
		return -1
	}
	return length
}

func headerContentLength(header http.Header) (int64, bool) {
	value := strings.TrimSpace(header.Get("Content-Length"))
	if value == "" {
		return -1, false
	}
	length, err := strconv.ParseInt(value, 10, 64)
	if err != nil || length < 0 {
		return -1, false
	}
	return length, true
}

func qualifyHEADOptimization(samples []responseProbeSample) string {
	if len(samples) == 0 {
		return "no response samples were available"
	}
	if status := samples[0].full.result.statusCode; status < 200 || status >= 300 {
		return fmt.Sprintf("likely-existing GET returned %d; HEAD requires an ordinary 2xx hit", status)
	}
	for _, sample := range samples {
		if sample.head.err != nil {
			return fmt.Sprintf("%s HEAD probe failed: %v", sample.name, sample.head.err)
		}
		full := sample.full.result
		head := sample.head.result
		if head.statusCode != full.statusCode {
			return fmt.Sprintf("%s HEAD status %d did not preserve GET status %d", sample.name, head.statusCode, full.statusCode)
		}
		if head.finalURL != full.finalURL || head.location != full.location {
			return fmt.Sprintf("%s HEAD did not preserve redirect behavior", sample.name)
		}
		if head.contentType != full.contentType {
			return fmt.Sprintf("%s HEAD content type %q did not preserve GET content type %q", sample.name, head.contentType, full.contentType)
		}
		if head.bodyLength != 0 {
			return fmt.Sprintf("%s HEAD returned %d body bytes", sample.name, head.bodyLength)
		}
		if head.declaredLength < 0 || head.declaredLength != full.bodyLength {
			return fmt.Sprintf("%s HEAD length %d did not preserve GET length %d", sample.name, head.declaredLength, full.bodyLength)
		}
	}
	return ""
}

func qualifyRangeOptimization(samples []responseProbeSample) string {
	if len(samples) == 0 {
		return "no response samples were available"
	}
	for _, sample := range samples {
		if sample.rng.err != nil {
			return fmt.Sprintf("%s Range probe failed: %v", sample.name, sample.rng.err)
		}
		if sample.rng.result.finalURL != sample.full.result.finalURL || sample.rng.result.location != sample.full.result.location {
			return fmt.Sprintf("%s Range did not preserve redirect behavior", sample.name)
		}
	}

	// The two generated samples are misses by construction. A soft-404 200 does
	// not preserve its status under a valid Range response, so it is deliberately
	// rejected rather than normalized on an unproven endpoint.
	for _, sample := range samples[1:] {
		if sample.rng.result.statusCode != sample.full.result.statusCode {
			return fmt.Sprintf("%s Range status %d did not preserve GET miss status %d", sample.name, sample.rng.result.statusCode, sample.full.result.statusCode)
		}
		if sample.rng.result.bodyLength != sample.full.result.bodyLength || sample.rng.result.contentType != sample.full.result.contentType {
			return fmt.Sprintf("%s Range did not preserve the GET miss representation", sample.name)
		}
		if sample.rng.result.bodyLength > responseOptimizationMaxMissBody {
			return fmt.Sprintf("%s Range miss still returned %d body bytes (limit %d)", sample.name, sample.rng.result.bodyLength, responseOptimizationMaxMissBody)
		}
	}

	hit := samples[0]
	if hit.full.result.statusCode != http.StatusOK {
		return fmt.Sprintf("likely-existing GET returned %d; Range requires an ordinary 200 hit", hit.full.result.statusCode)
	}
	if hit.rng.result.contentType != hit.full.result.contentType {
		return fmt.Sprintf("likely-existing Range content type %q did not preserve GET content type %q", hit.rng.result.contentType, hit.full.result.contentType)
	}
	if hit.full.result.bodyLength == 0 {
		parsed, ok := parseContentRange(hit.rng.result.contentRange)
		if !ok || !parsed.unsatisfied || parsed.total != 0 || hit.rng.result.statusCode != http.StatusRequestedRangeNotSatisfiable || hit.rng.result.bodyLength != 0 {
			return "empty 200 entity did not produce a valid 416 bytes */0 response"
		}
		return "no non-empty ordinary 200 hit produced the required one-byte 206"
	}
	if hit.rng.result.statusCode != http.StatusPartialContent {
		return fmt.Sprintf("likely-existing Range returned %d instead of 206", hit.rng.result.statusCode)
	}
	if hit.rng.result.bodyLength != 1 || hit.rng.result.declaredLength != 1 {
		return fmt.Sprintf("likely-existing Range returned body length %d and declared length %d instead of one byte", hit.rng.result.bodyLength, hit.rng.result.declaredLength)
	}
	parsed, ok := parseContentRange(hit.rng.result.contentRange)
	if !ok || parsed.unsatisfied {
		return fmt.Sprintf("likely-existing Range returned invalid Content-Range %q", hit.rng.result.contentRange)
	}
	if parsed.total != hit.full.result.bodyLength {
		return fmt.Sprintf("likely-existing Range total %d did not match GET length %d", parsed.total, hit.full.result.bodyLength)
	}
	if parsed.start != parsed.end || parsed.end != parsed.total-1 {
		return fmt.Sprintf("likely-existing Range %q was not the representation's final byte", hit.rng.result.contentRange)
	}
	return ""
}

func headerValue(headers map[string]string, wanted string) string {
	for name, value := range headers {
		if strings.EqualFold(name, wanted) {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
