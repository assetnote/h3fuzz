package engine

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

// TestNewRateThrottle_HugeRateNoPanic regresses P3: conf.Rate > 1000000 makes
// 1000000/rate integer-divide to 0, and a non-positive ticker interval panics.
func TestNewRateThrottle_HugeRateNoPanic(t *testing.T) {
	_ = NewRateThrottle(&ffuf.Config{Rate: 2000000, Threads: 40})
	_ = NewRateThrottle(&ffuf.Config{Rate: 1000001, Threads: 40})
}

func TestRateThrottleWaitUnlimitedReturnsImmediately(t *testing.T) {
	r := NewRateThrottle(&ffuf.Config{Rate: 0, Threads: 40})
	done := make(chan struct{})
	go func() {
		r.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("unlimited Wait blocked")
	}
}

func TestRateThrottleWaitWakesWhenRateIsDisabled(t *testing.T) {
	r := NewRateThrottle(&ffuf.Config{Rate: 1, Threads: 40})
	// Consume the ticker's immediately scheduled first period only after one
	// second, so every waiter below should still be blocked when we disable it.
	var wg sync.WaitGroup
	wg.Add(1)
	done := make(chan struct{})
	go func() {
		defer wg.Done()
		r.Wait()
		close(done)
	}()
	time.Sleep(10 * time.Millisecond)
	r.ChangeRate(0)
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Wait remained blocked after rate was disabled")
	}
	wg.Wait()
}

// TestChangeRate_HugeRateNoPanic regresses the same on the interactive `rate`
// command path, plus the zero/negative branches.
func TestChangeRate_HugeRateNoPanic(t *testing.T) {
	r := NewRateThrottle(&ffuf.Config{Rate: 0, Threads: 40})
	r.ChangeRate(2000000) // 1000000/2000000 == 0
	r.ChangeRate(1000001)
	r.ChangeRate(0)
	r.ChangeRate(-5)
	r.ChangeRate(500) // and a normal value still works
}

func TestCurrentRateUsesRollingWindow(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	now := start
	r := NewRateThrottle(&ffuf.Config{})
	r.now = func() time.Time { return now }
	r.samples = []rateSample{{at: start}}
	r.lastSampleAt = start

	for range 1_000 {
		r.Tick()
	}
	now = now.Add(100 * time.Millisecond)
	if got := r.CurrentRate(); got != 10_000 {
		t.Fatalf("first rolling rate = %d, want 10000", got)
	}

	for range 1_000 {
		r.Tick()
	}
	now = now.Add(50 * time.Millisecond)
	if got := r.CurrentRate(); got != 10_000 {
		t.Fatalf("sub-sample poll changed rate to %d, want 10000", got)
	}

	now = now.Add(50 * time.Millisecond)
	if got := r.CurrentRate(); got != 10_000 {
		t.Fatalf("second rolling rate = %d, want 10000", got)
	}
}

func TestCurrentRateDoesNotDropToZeroBetweenCompletionBursts(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	now := start
	r := NewRateThrottle(&ffuf.Config{})
	r.now = func() time.Time { return now }
	r.samples = []rateSample{{at: start}}
	r.lastSampleAt = start

	for range 2_000 {
		r.Tick()
	}
	now = now.Add(100 * time.Millisecond)
	if got := r.CurrentRate(); got != 20_000 {
		t.Fatalf("burst rate = %d, want 20000", got)
	}

	// No completions in this short display interval. A 10 ms delta sampler
	// reported zero here even though the one-second throughput was non-zero.
	now = now.Add(100 * time.Millisecond)
	if got := r.CurrentRate(); got != 10_000 {
		t.Fatalf("rolling rate after quiet interval = %d, want 10000", got)
	}

	// Once a full quiet window has elapsed, zero is the truthful value.
	now = now.Add(1100 * time.Millisecond)
	if got := r.CurrentRate(); got != 0 {
		t.Fatalf("rate after a quiet window = %d, want 0", got)
	}
}

func TestCurrentRateDropsToZeroAfterUnobservedQuietWindow(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	now := start
	r := NewRateThrottle(&ffuf.Config{})
	r.now = func() time.Time { return now }
	r.samples = []rateSample{{at: start}}
	r.lastSampleAt = start

	for range 1_000 {
		r.Tick()
	}
	// No intermediate CurrentRate call: an old count must not be spread across
	// two seconds and reported as live traffic.
	now = now.Add(2 * time.Second)
	if got := r.CurrentRate(); got != 0 {
		t.Fatalf("rate after an unobserved quiet window = %d, want 0", got)
	}
}

func TestCurrentRateQuietWindowAtUnixEpoch(t *testing.T) {
	start := time.Unix(0, 0)
	now := start
	r := NewRateThrottle(&ffuf.Config{})
	r.now = func() time.Time { return now }
	r.samples = []rateSample{{at: start}}
	r.lastSampleAt = start
	r.Tick() // lastTickAt == 0 is a valid timestamp, not an "unset" sentinel.

	now = now.Add(rateSampleWindow)
	if got := r.CurrentRate(); got != 0 {
		t.Fatalf("rate after a quiet window starting at Unix epoch = %d, want 0", got)
	}
}

func TestCurrentRateResetsAfterClockMovesBackward(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	now := start
	r := NewRateThrottle(&ffuf.Config{})
	r.now = func() time.Time { return now }
	r.samples = []rateSample{{at: start}}
	r.lastSampleAt = start

	for range 1_000 {
		r.Tick()
	}
	now = now.Add(100 * time.Millisecond)
	if got := r.CurrentRate(); got != 10_000 {
		t.Fatalf("rate before clock rollback = %d, want 10000", got)
	}

	now = start.Add(-time.Second)
	if got := r.CurrentRate(); got != 0 {
		t.Fatalf("rate immediately after clock rollback = %d, want 0", got)
	}
	for range 500 {
		r.Tick()
	}
	now = now.Add(100 * time.Millisecond)
	if got := r.CurrentRate(); got != 5_000 {
		t.Fatalf("rate from reset clock baseline = %d, want 5000", got)
	}
}

func TestCurrentRateConcurrentPollingAndBurstyTicks(t *testing.T) {
	const (
		writers        = 8
		readers        = 16
		ticksPerWriter = 5_000
		pollsPerReader = 2_000
	)

	start := time.Unix(1_700_000_000, 0)
	var clock atomic.Int64
	clock.Store(start.UnixNano())
	r := NewRateThrottle(&ffuf.Config{})
	r.now = func() time.Time { return time.Unix(0, clock.Load()) }
	r.samples = []rateSample{{at: start}}
	r.lastSampleAt = start
	clock.Store(start.Add(200 * time.Millisecond).UnixNano())

	startWork := make(chan struct{})
	var invalidRate atomic.Bool
	var wg sync.WaitGroup
	for range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startWork
			for range ticksPerWriter {
				r.Tick()
			}
		}()
	}
	for range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startWork
			for range pollsPerReader {
				if r.CurrentRate() < 0 {
					invalidRate.Store(true)
				}
			}
		}()
	}
	close(startWork)
	wg.Wait()
	if invalidRate.Load() {
		t.Fatal("concurrent CurrentRate returned a negative value")
	}

	clock.Store(start.Add(300 * time.Millisecond).UnixNano())
	want := int64(writers*ticksPerWriter) * int64(time.Second) / int64(300*time.Millisecond)
	if got := r.CurrentRate(); got != want {
		t.Fatalf("rate after concurrent burst = %d, want %d", got, want)
	}
	clock.Store(start.Add(1200 * time.Millisecond).UnixNano())
	if got := r.CurrentRate(); got != 0 {
		t.Fatalf("rate one quiet window after concurrent burst = %d, want 0", got)
	}
}

func TestRateThrottleTickDoesNotAllocate(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	r := NewRateThrottle(&ffuf.Config{})
	r.now = func() time.Time { return start }
	allocations := testing.AllocsPerRun(1_000, r.Tick)
	if allocations != 0 {
		t.Fatalf("Tick allocated %.2f objects per completion, want 0", allocations)
	}
}

func BenchmarkRateThrottleTick(b *testing.B) {
	r := NewRateThrottle(&ffuf.Config{})
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		r.Tick()
	}
}

func BenchmarkRateThrottleTickParallel(b *testing.B) {
	r := NewRateThrottle(&ffuf.Config{})
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			r.Tick()
		}
	})
}
