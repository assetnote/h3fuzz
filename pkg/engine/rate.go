package engine

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

type RateThrottle struct {
	Config         *ffuf.Config
	RateMutex      sync.Mutex
	RateLimiter    *time.Ticker
	configuredRate atomic.Int64
	changed        chan struct{}
	completed      atomic.Uint64
	lastTickAt     atomic.Int64
	sampleMutex    sync.Mutex
	samples        []rateSample
	lastSampleAt   time.Time
	sampleRate     int64
	now            func() time.Time
}

type rateSample struct {
	at    time.Time
	count uint64
}

const (
	rateSampleInterval = 100 * time.Millisecond
	rateSampleWindow   = time.Second
)

func NewRateThrottle(conf *ffuf.Config) *RateThrottle {
	now := time.Now()
	r := &RateThrottle{
		Config:       conf,
		changed:      make(chan struct{}),
		samples:      make([]rateSample, 1, 16),
		lastSampleAt: now,
		now:          time.Now,
	}
	r.samples[0] = rateSample{at: now}
	r.configuredRate.Store(conf.Rate)

	if conf.Rate > 0 {
		ratemicros := 1000000 / conf.Rate
		if ratemicros < 1 {
			// conf.Rate > 1000000 divides to 0; NewTicker panics on a non-positive
			// interval. Clamp to the 1us floor (~1M req/s).
			ratemicros = 1
		}
		r.RateLimiter = time.NewTicker(time.Microsecond * time.Duration(ratemicros))
	} else {
		// Unlimited mode must not create a 1us runtime timer or make every request
		// wait for a ticker. A stopped ticker can be Reset if an interactive rate is
		// configured later.
		r.RateLimiter = time.NewTicker(time.Hour)
		r.RateLimiter.Stop()
	}
	return r
}

// CurrentRate reports processed wordlist inputs over a rolling one-second window.
// Progress rendering can poll every few milliseconds while HTTP/3 completions
// arrive in batches. Sampling those tiny intervals made the displayed value
// alternate between zero and implausibly high spikes. Keeping sparse snapshots
// makes the display stable; Tick only adds atomic counter/activity updates.
func (r *RateThrottle) CurrentRate() int64 {
	r.sampleMutex.Lock()
	defer r.sampleMutex.Unlock()
	now := r.now()
	count := r.completed.Load()
	if now.Before(r.lastSampleAt) {
		// time.Now carries a monotonic component in production, but injected clocks
		// and restored VMs can move backwards. Start a fresh baseline rather than
		// keeping a positive cached rate until wall time catches up.
		r.resetRateSamples(now, count)
		return 0
	}
	if lastTickAt := r.lastTickAt.Load(); count > 0 {
		// A caller may not poll during the quiet period. The sample ring alone cannot
		// distinguish an old burst from recent work in that case, so the lock-free
		// activity timestamp is the authoritative idle check.
		lastTick := time.Unix(0, lastTickAt)
		if !now.Before(lastTick) && now.Sub(lastTick) >= rateSampleWindow {
			r.resetRateSamples(now, count)
			return 0
		}
	}
	if now.Sub(r.lastSampleAt) < rateSampleInterval {
		return r.sampleRate
	}

	r.samples = append(r.samples, rateSample{at: now, count: count})
	r.lastSampleAt = now
	cutoff := now.Add(-rateSampleWindow)
	remove := 0
	// Retain the newest snapshot at or before the cutoff. It anchors the
	// calculation as close as possible to a full one-second window.
	for remove+1 < len(r.samples) && !r.samples[remove+1].at.After(cutoff) {
		remove++
	}
	if remove > 0 {
		copy(r.samples, r.samples[remove:])
		r.samples = r.samples[:len(r.samples)-remove]
	}

	oldest := r.samples[0]
	newest := r.samples[len(r.samples)-1]
	elapsed := newest.at.Sub(oldest.at)
	if elapsed <= 0 {
		return r.sampleRate
	}
	delta := newest.count - oldest.count
	r.sampleRate = int64(delta) * int64(time.Second) / elapsed.Nanoseconds()
	return r.sampleRate
}

func (r *RateThrottle) resetRateSamples(now time.Time, count uint64) {
	if len(r.samples) == 0 {
		r.samples = append(r.samples, rateSample{})
	} else {
		r.samples = r.samples[:1]
	}
	r.samples[0] = rateSample{at: now, count: count}
	r.lastSampleAt = now
	r.sampleRate = 0
}

// CurrentConfiguredRate returns the configured rate under the lock, for callers
// (e.g. the interactive handler) that only need to display it.
func (r *RateThrottle) CurrentConfiguredRate() int64 {
	return r.configuredRate.Load()
}

// Wait blocks until a configured rate-limit token is available. The common
// unlimited path is one atomic load and no timer/channel operation. If the rate
// changes while a caller is waiting, changed wakes it to re-evaluate rather than
// leaving it blocked on a ticker that was just stopped.
func (r *RateThrottle) Wait() {
	for {
		if r.configuredRate.Load() <= 0 {
			return
		}
		r.RateMutex.Lock()
		if r.configuredRate.Load() <= 0 {
			r.RateMutex.Unlock()
			return
		}
		ticks := r.RateLimiter.C
		changed := r.changed
		r.RateMutex.Unlock()
		select {
		case <-ticks:
			return
		case <-changed:
		}
	}
}

func (r *RateThrottle) ChangeRate(rate int) {
	r.RateMutex.Lock()
	defer r.RateMutex.Unlock()

	var period time.Duration
	if rate > 0 {
		period = time.Microsecond * time.Duration(1000000/rate)
	}
	if period <= 0 {
		// rate > 1000000 makes 1000000/rate integer-divide to 0; a non-positive
		// ticker interval panics. Clamp to the 1us floor (~1M req/s).
		period = time.Microsecond
	}

	r.Config.Rate = int64(rate)
	r.configuredRate.Store(int64(rate))
	if rate > 0 {
		r.RateLimiter.Reset(period)
	} else {
		r.RateLimiter.Stop()
	}
	close(r.changed)
	r.changed = make(chan struct{})
}

// Tick records one completed input. It remains lock-free and allocation-free;
// the timestamp lets CurrentRate prove a full quiet window even when progress
// rendering did not poll between the burst and the idle period.
func (r *RateThrottle) Tick() {
	r.lastTickAt.Store(r.now().UnixNano())
	r.completed.Add(1)
}
