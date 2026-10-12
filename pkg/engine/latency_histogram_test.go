package engine

import (
	"sync"
	"testing"
	"time"
)

func TestResponseLatencyBucketUpperBoundIsConservative(t *testing.T) {
	values := []uint64{
		0, 1, 31, 32, 33, 63, 64, 65,
		999, 1_000, 1_000_000, 17_000_000,
		responseLatencyMaxTrackableNS,
	}
	for _, value := range values {
		index := responseLatencyBucketIndex(value)
		upper := responseLatencyBucketUpperBound(index)
		if upper < value {
			t.Fatalf("value %d mapped to non-conservative upper bound %d", value, upper)
		}
	}
	if got := responseLatencyBucketIndex(responseLatencyMaxTrackableNS); got != responseLatencyMaxBucketIndex {
		t.Fatalf("maximum bucket index = %d, want %d", got, responseLatencyMaxBucketIndex)
	}
}

func TestResponseLatencyHistogramReportsNearestRankP99(t *testing.T) {
	histogram := newResponseLatencyHistogram()
	for id := uint64(0); id < 99; id++ {
		histogram.record(id, time.Millisecond)
	}
	histogram.record(99, 10*time.Millisecond)

	got := histogram.snapshot()
	if got.SampleCount != 100 {
		t.Fatalf("sample count = %d, want 100", got.SampleCount)
	}
	if got.P99UpperBoundNS < uint64(time.Millisecond) || got.P99UpperBoundNS >= uint64(2*time.Millisecond) {
		t.Fatalf("p99 upper bound = %d, want conservative bound near 1ms", got.P99UpperBoundNS)
	}
	if got.OverflowCount != 0 {
		t.Fatalf("overflow count = %d, want 0", got.OverflowCount)
	}
	if got.MaximumTrackableNS != responseLatencyMaxTrackableNS {
		t.Fatalf("maximum trackable = %d, want %d", got.MaximumTrackableNS, responseLatencyMaxTrackableNS)
	}
}

func TestResponseLatencyHistogramCountsConcurrentSamplesAndOverflow(t *testing.T) {
	histogram := newResponseLatencyHistogram()
	const samples = 4096
	var workers sync.WaitGroup
	for id := uint64(0); id < samples; id++ {
		workers.Add(1)
		go func(id uint64) {
			defer workers.Done()
			histogram.record(id, time.Duration(id+1))
		}(id)
	}
	workers.Wait()
	histogram.record(samples, time.Duration(responseLatencyMaxTrackableNS+1))

	got := histogram.snapshot()
	if got.SampleCount != samples+1 {
		t.Fatalf("sample count = %d, want %d", got.SampleCount, samples+1)
	}
	if got.OverflowCount != 1 {
		t.Fatalf("overflow count = %d, want 1", got.OverflowCount)
	}
	if got.Source != "client_final_attempt_time_to_first_byte" ||
		got.Scope != "main_successful_logical_responses" ||
		got.Method != "bounded_histogram_upper_bound" {
		t.Fatalf("unexpected latency provenance: %+v", got)
	}
}

func TestResponseLatencyHistogramNeverUnderstatesOverflowingP99(t *testing.T) {
	histogram := newResponseLatencyHistogram()
	for id := uint64(0); id < 98; id++ {
		histogram.record(id, time.Millisecond)
	}
	for id := uint64(98); id < 100; id++ {
		histogram.record(id, time.Duration(responseLatencyMaxTrackableNS+1))
	}

	got := histogram.snapshot()
	if got.OverflowCount != 2 {
		t.Fatalf("overflow count = %d, want 2", got.OverflowCount)
	}
	if got.P99UpperBoundNS != ^uint64(0) {
		t.Fatalf("overflowing p99 upper bound = %d, want MaxUint64", got.P99UpperBoundNS)
	}
}

func TestResponseLatencyHistogramClampsLastTrackedBucketToMaximum(t *testing.T) {
	histogram := newResponseLatencyHistogram()
	histogram.record(0, time.Duration(responseLatencyMaxTrackableNS))

	got := histogram.snapshot()
	if got.OverflowCount != 0 {
		t.Fatalf("overflow count = %d, want 0", got.OverflowCount)
	}
	if got.P99UpperBoundNS != responseLatencyMaxTrackableNS {
		t.Fatalf("maximum tracked p99 = %d, want %d", got.P99UpperBoundNS, responseLatencyMaxTrackableNS)
	}
}
