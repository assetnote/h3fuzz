package webserver

import (
	"context"
	"errors"
	"math"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

func preflightReportFromFFUF(raw ffuf.ProtocolPreflightReport, state, errText string) PreflightReport {
	report := PreflightReport{
		State:                state,
		HTTP3Advertised:      raw.HTTP3.Advertised,
		HTTP3Negotiated:      raw.HTTP3.Negotiated,
		HTTP3Advertisement:   raw.HTTP3.Advertisement,
		AdvertisementCheck:   preflightCheckFromFFUF(raw.HTTP3.AdvertisementProbe),
		NegotiationCheck:     preflightCheckFromFFUF(raw.HTTP3.NegotiationProbe),
		Fingerprint:          raw.HTTP3.Fingerprint,
		HEADCheck:            preflightCheckFromFFUF(raw.RequestStrategy.HEAD),
		RangeCheck:           preflightCheckFromFFUF(raw.RequestStrategy.Range),
		SelectedProtocol:     raw.SelectedProtocol,
		SelectedStrategy:     raw.RequestStrategy.Selected,
		StrategyReason:       raw.RequestStrategy.Reason,
		EffectiveWorkers:     raw.EffectiveConcurrency.Workers,
		EffectiveConnections: raw.EffectiveConcurrency.Connections,
		EffectiveStreams:     raw.EffectiveConcurrency.MaximumConcurrentStreams,
		StreamsPerConnection: raw.EffectiveConcurrency.StreamsPerConnection,
		DowngradeReason:      raw.DowngradeReason,
		TimingsMS: PreflightTimings{
			Advertisement: raw.Timings.AdvertisementMillis,
			HTTP3:         raw.Timings.HTTP3Millis,
			Baseline:      raw.Timings.BaselineMillis,
			HEAD:          raw.Timings.HEADMillis,
			Range:         raw.Timings.RangeMillis,
			Total:         raw.Timings.TotalMillis,
		},
		Error: errText,
	}
	if !raw.RanAt.IsZero() {
		ranAt := raw.RanAt.UTC()
		report.RanAt = &ranAt
	}
	if !raw.CompletedAt.IsZero() {
		completedAt := raw.CompletedAt.UTC()
		report.CompletedAt = &completedAt
	}
	if report.State == "" {
		report.State = PreflightNotRun
	}
	return report
}

func preflightCheckFromFFUF(probe ffuf.PreflightProbeReport) PreflightCheck {
	return PreflightCheck{
		Attempted: probe.Attempted,
		Reliable:  probe.Reliable,
		Reason:    probe.Reason,
		Requests:  probe.Requests,
		ElapsedMS: probe.ElapsedMillis,
	}
}

type throughputSample struct {
	at        time.Time
	completed int
}

type throughputTracker struct {
	mu      sync.Mutex
	samples []throughputSample
	peak    float64
}

func (t *throughputTracker) enrich(progress *Progress, now time.Time) {
	if progress == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	if n := len(t.samples); n > 0 && progress.CompletedCount < t.samples[n-1].completed {
		t.samples = t.samples[:0]
		t.peak = 0
	}
	t.samples = append(t.samples, throughputSample{at: now, completed: progress.CompletedCount})
	cutoff := now.Add(-31 * time.Second)
	first := 0
	// Retain the newest sample at or before the cutoff as an anchor. Dropping
	// every old sample makes a long pause/polling gap look like "no sample" and
	// incorrectly revives the non-zero lifetime average on resume.
	for first+1 < len(t.samples) && !t.samples[first+1].at.After(cutoff) {
		first++
	}
	if first > 0 {
		t.samples = append([]throughputSample(nil), t.samples[first:]...)
	}

	progress.CurrentRate = float64(progress.ReqSec)
	windowRateAvailable := false
	if rate, ok := sampleRate(t.samples, now.Add(-30*time.Second)); ok {
		progress.Average30s = rate
		windowRateAvailable = true
	}
	if !progress.StartedAt.IsZero() {
		seconds := now.Sub(progress.StartedAt).Seconds()
		if seconds > 0 {
			progress.OverallAverage = float64(progress.CompletedCount) / seconds
		}
	}
	if !windowRateAvailable {
		progress.Average30s = progress.OverallAverage
	}
	if progress.CurrentRate > t.peak {
		t.peak = progress.CurrentRate
	}
	progress.PeakRate = t.peak

	remaining := progress.ReqTotal - progress.CompletedCount
	etaRate := progress.Average30s
	if !windowRateAvailable {
		etaRate = progress.OverallAverage
	}
	if remaining > 0 && etaRate > 0 {
		seconds := int64(math.Ceil(float64(remaining) / etaRate))
		progress.ETASeconds = &seconds
	} else if remaining <= 0 && progress.ReqTotal > 0 {
		seconds := int64(0)
		progress.ETASeconds = &seconds
	}
}

func sampleRate(samples []throughputSample, cutoff time.Time) (float64, bool) {
	if len(samples) < 2 {
		return 0, false
	}
	latest := samples[len(samples)-1]
	oldest := samples[0]
	for i := len(samples) - 2; i >= 0; i-- {
		oldest = samples[i]
		if !samples[i].at.After(cutoff) {
			break
		}
	}
	seconds := latest.at.Sub(oldest.at).Seconds()
	completed := latest.completed - oldest.completed
	if seconds <= 0 || completed < 0 {
		return 0, false
	}
	return float64(completed) / seconds, true
}

const maxDiagnosticSamples = 3

func classifyRequestError(err error) string {
	if err == nil {
		return "other"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "dns"
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "quic") && (strings.Contains(message, "handshake") || strings.Contains(message, "dial")):
		return "quic_handshake"
	case strings.Contains(message, "tls") || strings.Contains(message, "x509") || strings.Contains(message, "certificate"):
		return "tls"
	case errors.Is(err, context.DeadlineExceeded) || strings.Contains(message, "timeout") || strings.Contains(message, "timed out") || strings.Contains(message, "deadline exceeded"):
		return "timeout"
	case strings.Contains(message, "connection reset") || strings.Contains(message, "broken pipe") || strings.Contains(message, "unexpected eof"):
		return "connection_reset"
	default:
		return "other"
	}
}

func diagnosticSuggestion(category string) string {
	switch category {
	case "dns":
		return "Verify the hostname and resolver configuration, or try a known-good DNS server."
	case "tls":
		return "Check SNI, client certificates, TLS interception, and the target's certificate configuration."
	case "quic_handshake":
		return "Confirm UDP/443 reachability, reduce HTTP/3 connections or streams, or disable HTTP/3 to compare."
	case "timeout":
		return "Increase the timeout or lower the request rate and concurrency."
	case "connection_reset":
		return "Lower concurrency/rate and check whether the server or intermediary is closing connections."
	case "rate_limiting":
		return "Set a lower rate, reduce concurrency, and consider adding jitter between requests."
	case "persistence_backpressure":
		return "Use faster local storage or narrower matchers/filters so fewer results need persistence."
	default:
		return "Inspect representative errors and the terminal log, then adjust transport or request settings."
	}
}

func addDiagnostic(target map[string]*ErrorDiagnostic, category, sample string) {
	if category == "" {
		category = "other"
	}
	diagnostic := target[category]
	if diagnostic == nil {
		diagnostic = &ErrorDiagnostic{Category: category, Samples: []string{}, Suggestion: diagnosticSuggestion(category)}
		target[category] = diagnostic
	}
	diagnostic.Count++
	sample = strings.TrimSpace(sample)
	if len(sample) > 512 {
		sample = sample[:512]
	}
	if sample == "" || len(diagnostic.Samples) >= maxDiagnosticSamples {
		return
	}
	for _, existing := range diagnostic.Samples {
		if existing == sample {
			return
		}
	}
	diagnostic.Samples = append(diagnostic.Samples, sample)
}

func cloneDiagnostics(source map[string]*ErrorDiagnostic) []ErrorDiagnostic {
	result := make([]ErrorDiagnostic, 0, len(source))
	for _, diagnostic := range source {
		copy := *diagnostic
		copy.Samples = append([]string(nil), diagnostic.Samples...)
		result = append(result, copy)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Category < result[j].Category })
	return result
}
