package engine

import (
	"math/bits"
	"sync/atomic"
	"time"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

const (
	responseLatencyExactLimit     = uint64(32)
	responseLatencySubBuckets     = uint64(32)
	responseLatencyShardCount     = uint64(64)
	responseLatencyMaxTrackableNS = uint64(120 * time.Second)
	responseLatencyOverflowIndex  = responseLatencyBucketCount
	responseLatencyBucketCount    = responseLatencyMaxBucketIndex + 1
	responseLatencyMaxBucketIndex = 1048
)

// responseLatencyHistogram spreads writes by logical input ID. A successful
// response performs one atomic increment and no allocation on the hot path.
// The extra final cell in every shard is the overflow bucket.
type responseLatencyHistogram struct {
	shards [][]uint64
}

func newResponseLatencyHistogram() *responseLatencyHistogram {
	shards := make([][]uint64, responseLatencyShardCount)
	for index := range shards {
		shards[index] = make([]uint64, responseLatencyBucketCount+1)
	}
	return &responseLatencyHistogram{shards: shards}
}

func responseLatencyBucketIndex(value uint64) uint64 {
	if value < responseLatencyExactLimit {
		return value
	}
	exponent := uint64(bits.Len64(value) - 1)
	base := uint64(1) << exponent
	step := base / responseLatencySubBuckets
	delta := value - base
	subBucket := (delta + step - 1) / step
	return responseLatencyExactLimit +
		(exponent-uint64(bits.Len64(responseLatencyExactLimit)-1))*responseLatencySubBuckets +
		subBucket
}

func responseLatencyBucketUpperBound(index uint64) uint64 {
	if index < responseLatencyExactLimit {
		return index
	}
	relative := index - responseLatencyExactLimit
	exponent := uint64(bits.Len64(responseLatencyExactLimit)-1) + relative/responseLatencySubBuckets
	subBucket := relative % responseLatencySubBuckets
	base := uint64(1) << exponent
	return base + subBucket*(base/responseLatencySubBuckets)
}

func (h *responseLatencyHistogram) record(logicalID uint64, duration time.Duration) {
	if h == nil {
		return
	}
	value := uint64(0)
	if duration > 0 {
		value = uint64(duration)
	}
	index := uint64(responseLatencyOverflowIndex)
	if value <= responseLatencyMaxTrackableNS {
		index = responseLatencyBucketIndex(value)
	}
	shard := h.shards[logicalID%responseLatencyShardCount]
	atomic.AddUint64(&shard[index], 1)
}

func (h *responseLatencyHistogram) snapshot() ffuf.RunResponseLatencyTelemetry {
	report := ffuf.RunResponseLatencyTelemetry{
		SchemaVersion:      1,
		Source:             "client_final_attempt_time_to_first_byte",
		Scope:              "main_successful_logical_responses",
		Clock:              "monotonic",
		Unit:               "nanoseconds",
		Method:             "bounded_histogram_upper_bound",
		MaximumTrackableNS: responseLatencyMaxTrackableNS,
	}
	if h == nil {
		return report
	}
	counts := make([]uint64, responseLatencyBucketCount)
	for _, shard := range h.shards {
		for index := uint64(0); index < responseLatencyBucketCount; index++ {
			counts[index] += atomic.LoadUint64(&shard[index])
		}
		report.OverflowCount += atomic.LoadUint64(&shard[responseLatencyOverflowIndex])
	}
	for _, count := range counts {
		report.SampleCount += count
	}
	report.SampleCount += report.OverflowCount
	if report.SampleCount == 0 {
		return report
	}
	rank := (99*report.SampleCount + 99) / 100
	var cumulative uint64
	for index, count := range counts {
		cumulative += count
		if cumulative >= rank {
			report.P99UpperBoundNS = responseLatencyBucketUpperBound(uint64(index))
			if report.P99UpperBoundNS > responseLatencyMaxTrackableNS {
				report.P99UpperBoundNS = responseLatencyMaxTrackableNS
			}
			return report
		}
	}
	// An overflowing sample occupies the p99 rank. MaxUint64 remains a truthful
	// upper bound for time.Duration nanoseconds and also guarantees that strict
	// consumers reject the report as above the advertised trackable range.
	report.P99UpperBoundNS = ^uint64(0)
	return report
}
