package runner

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

func TestHTTP3PoolDefaultAdaptiveConnections(t *testing.T) {
	target, err := url.Parse("https://example.test/")
	if err != nil {
		t.Fatal(err)
	}
	probe, err := http.NewRequest(http.MethodHead, target.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name               string
		fixedConnections   int
		maximumConnections int
		want               int
	}{
		{name: "ordinary automatic", maximumConnections: 64, want: 8},
		{name: "automatic capped below seed", maximumConnections: 4, want: 4},
		{name: "explicit fixed remains authoritative", fixedConnections: 10, maximumConnections: 64, want: 10},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pool, err := newHTTP3Pool(&ffuf.Config{
				Http3Connections:    test.fixedConnections,
				Http3MaxConnections: test.maximumConnections,
			}, target, probe)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = pool.Close() })
			if got := pool.reconnect.snapshot().DesiredConnections; got != test.want {
				t.Fatalf("initial desired connections = %d, want %d", got, test.want)
			}
		})
	}
}

func TestHTTP3PoolSuggestedWorkersUsesActiveAdaptiveConnections(t *testing.T) {
	p := &http3Pool{
		maxConnections:       64,
		streamsPerConnection: 256,
	}
	p.published.Store(&http3ActiveSlots{slots: make([]*http3PoolSlot, 10)})

	if got, want := p.suggestedWorkers(), 2_560; got != want {
		t.Fatalf("suggested workers = %d, want active stream budget %d", got, want)
	}
}

func TestHTTP3PoolSuggestedWorkersHonorsFixedConnections(t *testing.T) {
	p := &http3Pool{
		maxConnections:       64,
		fixedConnections:     20,
		streamsPerConnection: 256,
	}
	p.published.Store(&http3ActiveSlots{slots: make([]*http3PoolSlot, 10)})

	if got, want := p.suggestedWorkers(), 5_120; got != want {
		t.Fatalf("suggested workers = %d, want fixed stream budget %d", got, want)
	}
}

func TestHTTP3PoolSuggestedWorkersBeforeStartIsSafe(t *testing.T) {
	p := &http3Pool{maxConnections: 64, streamsPerConnection: 256}
	p.published.Store(&http3ActiveSlots{})

	if got := p.suggestedWorkers(); got != 1 {
		t.Fatalf("suggested workers = %d before start, want 1", got)
	}
}

func TestHTTP3PoolTuneSaturationSurvivesExpansionAndBacktrack(t *testing.T) {
	const (
		initialConnections   = 10
		expandedConnections  = 20
		streamsPerConnection = 256
		workers              = initialConnections * streamsPerConnection
	)

	slots := make([]*http3PoolSlot, expandedConnections)
	for i := range slots {
		slots[i] = &http3PoolSlot{}
	}
	pool := &http3Pool{
		slots:                slots,
		active:               initialConnections,
		streamsPerConnection: streamsPerConnection,
	}
	pool.publishActiveLocked()
	pool.limiter = newResizableLimiter(workers)
	pool.workerCeiling.Store(workers)
	pool.limiter.inUse.Store(workers)

	if !pool.limiter.saturatedAt(pool.workerCeiling.Load()) {
		t.Fatal("initial pool should be saturated when every request worker is in the transport")
	}

	// This is the regression step: expanding the pool doubles its admission limit,
	// but intentionally does not double the goroutine count. Saturation must remain
	// true at the worker ceiling so the next clean sample can assess the expanded
	// connection count and either progress or backtrack.
	pool.setActive(expandedConnections)
	if got, want := pool.limiter.limit.Load(), int64(expandedConnections*streamsPerConnection); got != want {
		t.Fatalf("expanded limiter capacity = %d, want %d", got, want)
	}
	if !pool.limiter.saturatedAt(pool.workerCeiling.Load()) {
		t.Fatal("expanded pool was no longer tunable at the unchanged worker ceiling")
	}

	pool.limiter.inUse.Store(workers - 1)
	if pool.limiter.saturatedAt(pool.workerCeiling.Load()) {
		t.Fatal("expanded pool reported saturation before all request workers reached the transport")
	}
	pool.limiter.inUse.Store(workers)

	// A worse expanded sample causes the tuner to backtrack through setActive.
	// The lower stream budget must still be saturated by the same workers, allowing
	// a later steady-state probe to progress again.
	pool.setActive(initialConnections)
	if got := pool.activeConnections(); got != initialConnections {
		t.Fatalf("active connections after backtrack = %d, want %d", got, initialConnections)
	}
	if got, want := pool.limiter.limit.Load(), int64(workers); got != want {
		t.Fatalf("backtracked limiter capacity = %d, want %d", got, want)
	}
	if !pool.limiter.saturatedAt(pool.workerCeiling.Load()) {
		t.Fatal("backtracked pool should remain eligible for future tuning")
	}
}

func TestHTTP3PoolWorkerCeilingChangesOnlyAfterMatchingCapacityAck(t *testing.T) {
	const streams = 256
	slots := make([]*http3PoolSlot, 20)
	for i := range slots {
		slots[i] = &http3PoolSlot{}
	}
	pool := &http3Pool{
		slots:                slots,
		active:               10,
		streamsPerConnection: streams,
		capacity:             newCapacityAdvisor(10*streams, 2*time.Second, time.Second),
	}
	pool.publishActiveLocked()
	pool.limiter = newResizableLimiter(10 * streams)
	pool.workerCeiling.Store(10 * streams)
	t.Cleanup(pool.capacity.close)

	subscription, err := pool.SubscribeCapacity(9)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	t.Cleanup(subscription.Close)
	_ = receiveCapacityProposal(t, subscription.Proposals())

	pool.setActive(20)
	proposal := receiveCapacityProposal(t, subscription.Proposals())
	if proposal.ExecutionGeneration != 9 || proposal.DesiredWorkers != 20*streams {
		t.Fatalf("resize proposal = %+v", proposal)
	}
	if got := pool.workerCeiling.Load(); got != 10*streams {
		t.Fatalf("worker ceiling changed before acknowledgement: %d", got)
	}
	subscription.Acknowledge(ffuf.CapacityAck{
		ExecutionGeneration: 9,
		Epoch:               proposal.Epoch,
		ProposedWorkers:     proposal.DesiredWorkers,
		ActualLiveWorkers:   5_000,
		RemainingInputCap:   5_000,
		SupervisorState:     "running",
		AcknowledgedAt:      time.Now(),
	})
	pool.pollCapacity(time.Now())
	if got := pool.workerCeiling.Load(); got != 5_000 {
		t.Fatalf("acknowledged worker ceiling = %d, want 5000", got)
	}
}

func TestHTTP3PoolCapacityProtocolTracksTenTwentyFortyAndShrink(t *testing.T) {
	const streams = 10
	slots := make([]*http3PoolSlot, 40)
	for i := range slots {
		slots[i] = &http3PoolSlot{}
	}
	pool := &http3Pool{
		slots:                slots,
		active:               10,
		streamsPerConnection: streams,
		maxWorkers:           1_000,
		capacity:             newCapacityAdvisor(10*streams, 2*time.Second, 10*time.Millisecond),
	}
	pool.publishActiveLocked()
	pool.limiter = newResizableLimiter(10 * streams)
	pool.workerCeiling.Store(10 * streams)
	t.Cleanup(pool.capacity.close)

	subscription, err := pool.SubscribeCapacity(41)
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for proposal := range subscription.Proposals() {
			subscription.Acknowledge(ffuf.CapacityAck{
				ExecutionGeneration: proposal.ExecutionGeneration,
				Epoch:               proposal.Epoch,
				ProposedWorkers:     proposal.DesiredWorkers,
				ActualLiveWorkers:   proposal.DesiredWorkers,
				RemainingInputCap:   1_000,
				SupervisorState:     "running",
				AcknowledgedAt:      time.Now(),
			})
		}
	}()

	for _, connections := range []int{10, 20, 40, 10} {
		if connections != 10 || pool.activeConnections() != 10 {
			pool.setActive(connections)
		}
		want := int64(connections * streams)
		waitForRunnerCondition(t, time.Second, "capacity acknowledgement", func() bool {
			pool.pollCapacity(time.Now())
			return pool.workerCeiling.Load() == want && pool.capacity.snapshot().AcknowledgedLiveWorkers == int(want)
		})
		if got := pool.capacity.snapshot().AcknowledgedLiveWorkers; got != int(want) {
			t.Fatalf("%d-connection acknowledged workers = %d, want %d", connections, got, want)
		}
	}

	subscription.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("capacity consumer did not stop after unsubscribe")
	}
}

func waitForRunnerCondition(t *testing.T, timeout time.Duration, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", description)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestHTTP3PoolShrinkWaitsForActiveSlotCapacityWithoutDeadlock(t *testing.T) {
	const (
		opened = 20
		active = 10
	)
	slots := make([]*http3PoolSlot, opened)
	for i := range slots {
		slots[i] = &http3PoolSlot{}
		slots[i].inFlight.Store(1)
	}
	pool := &http3Pool{
		slots:                slots,
		active:               opened,
		streamsPerConnection: 1,
		limiter:              newResizableLimiter(opened),
	}
	pool.limiter.inUse.Store(opened)
	pool.publishActiveLocked()

	// Model an adaptive backoff while every old slot has one request in flight.
	// Permits held by the now-inactive half still count until those responses
	// finish, preventing over-admission into the smaller active snapshot.
	pool.setActive(active)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	acquired := make(chan error, 1)
	go func() { acquired <- pool.limiter.acquire(ctx) }()

	for i := active; i < opened; i++ {
		slots[i].inFlight.Add(-1)
		pool.limiter.release()
	}
	select {
	case err := <-acquired:
		t.Fatalf("waiter admitted while all active slots were full: %v", err)
	case <-time.After(10 * time.Millisecond):
	}

	// The first active response to finish creates both aggregate and per-slot
	// capacity. The blocked request must then acquire and reserve that slot.
	slots[0].inFlight.Add(-1)
	pool.limiter.release()
	select {
	case err := <-acquired:
		if err != nil {
			t.Fatalf("waiter failed after active capacity opened: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("waiter remained blocked after adaptive shrink capacity opened")
	}
	reserved, err := pool.reserveSlot()
	if err != nil {
		t.Fatalf("reserve active slot after shrink: %v", err)
	}
	if reserved != slots[0] {
		t.Fatalf("reserved slot %p, want newly available active slot %p", reserved, slots[0])
	}
	reserved.inFlight.Add(-1)
	pool.limiter.release()
	if got := pool.limiter.inUse.Load(); got != int64(active-1) {
		t.Fatalf("limiter in use after cleanup = %d, want %d", got, active-1)
	}
}

func TestHTTP3ZeroCompletionCongestionRequiresSustainedSaturation(t *testing.T) {
	streak := 0
	for window := 1; window < http3ZeroProgressBackoffWindows; window++ {
		var backoff bool
		streak, backoff = observeHTTP3ZeroProgress(streak, true, 0, 0)
		if backoff {
			t.Fatalf("backoff triggered after only %d zero-progress window(s)", window)
		}
	}
	var backoff bool
	streak, backoff = observeHTTP3ZeroProgress(streak, true, 0, 0)
	if !backoff || streak != http3ZeroProgressBackoffWindows {
		t.Fatalf("sustained congestion result = streak %d, backoff %t", streak, backoff)
	}
}

func TestHTTP3ZeroCompletionCongestionResetsOnEvidence(t *testing.T) {
	tests := []struct {
		name      string
		saturated bool
		completed uint64
		failures  uint64
	}{
		{name: "capacity available", saturated: false},
		{name: "response completed", saturated: true, completed: 1},
		{name: "request failed", saturated: true, failures: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			streak, backoff := observeHTTP3ZeroProgress(
				http3ZeroProgressBackoffWindows-1,
				test.saturated,
				test.completed,
				test.failures,
			)
			if streak != 0 || backoff {
				t.Fatalf("evidence did not reset congestion: streak=%d backoff=%t", streak, backoff)
			}
		})
	}
}

func TestHTTP3PoolZeroCompletionCongestionBacksOffWithoutCancelingInflight(t *testing.T) {
	const connections = 10
	slots := make([]*http3PoolSlot, connections)
	for i := range slots {
		slots[i] = &http3PoolSlot{}
		slots[i].inFlight.Store(1)
	}
	pool := &http3Pool{
		slots:                slots,
		active:               connections,
		streamsPerConnection: 1,
		limiter:              newResizableLimiter(connections),
	}
	pool.limiter.inUse.Store(connections)
	pool.publishActiveLocked()

	streak := 0
	for window := 1; window <= http3ZeroProgressBackoffWindows; window++ {
		var triggered bool
		streak, triggered = pool.maybeBackoffZeroProgress(streak, connections, true, 0, 0)
		if triggered != (window == http3ZeroProgressBackoffWindows) {
			t.Fatalf("window %d triggered backoff = %t", window, triggered)
		}
	}
	if got, want := pool.activeConnections(), connections*3/4; got != want {
		t.Fatalf("active connections after congestion = %d, want %d", got, want)
	}
	if got := pool.slotCount(); got != connections {
		t.Fatalf("backoff closed slots: opened=%d want=%d", got, connections)
	}
	if got := pool.limiter.inUse.Load(); got != connections {
		t.Fatalf("backoff canceled in-flight permits: in_use=%d want=%d", got, connections)
	}
	for index, slot := range slots {
		if got := slot.inFlight.Load(); got != 1 {
			t.Fatalf("slot %d in-flight requests = %d, want 1", index, got)
		}
	}
}

func TestResizableLimiterConcurrentCapacityBound(t *testing.T) {
	const (
		limit   = 4
		workers = 32
	)

	limiter := newResizableLimiter(limit)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	start := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })

	acquired := make(chan struct{}, workers)
	errs := make(chan error, workers)
	var current atomic.Int64
	var maximum atomic.Int64
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := limiter.acquire(ctx); err != nil {
				errs <- err
				return
			}

			now := current.Add(1)
			recordAtomicMaximum(&maximum, now)
			acquired <- struct{}{}
			<-release
			current.Add(-1)
			limiter.release()
		}()
	}

	close(start)
	for i := 0; i < limit; i++ {
		select {
		case <-acquired:
		case <-ctx.Done():
			t.Fatalf("only %d/%d initial permits were acquired: %v", i, limit, ctx.Err())
		}
	}
	waitForCondition(t, 5*time.Second, "remaining goroutines to block in the limiter", func() bool {
		return limiter.waiters.Load() == workers-limit
	})

	if got := current.Load(); got != limit {
		t.Fatalf("in-use workers = %d, want %d", got, limit)
	}
	if got := maximum.Load(); got != limit {
		t.Fatalf("maximum concurrent permits = %d, want %d", got, limit)
	}
	select {
	case <-acquired:
		t.Fatal("limiter admitted more workers than its capacity")
	default:
	}

	releaseOnce.Do(func() { close(release) })
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("acquire failed: %v", err)
	}
	if got := limiter.inUse.Load(); got != 0 {
		t.Fatalf("inUse after balanced releases = %d, want 0", got)
	}
	if got := maximum.Load(); got > limit {
		t.Fatalf("maximum concurrent permits = %d, exceeds limit %d", got, limit)
	}
}

func TestResizableLimiterResizeUpwardWakesAvailableWaiters(t *testing.T) {
	const (
		waiters  = 8
		newLimit = 4
	)

	limiter := newResizableLimiter(1)
	if err := limiter.acquire(context.Background()); err != nil {
		t.Fatalf("acquire initial permit: %v", err)
	}
	initialHeld := true
	defer func() {
		if initialHeld {
			limiter.release()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	hold := make(chan struct{})
	var holdOnce sync.Once
	defer holdOnce.Do(func() { close(hold) })

	acquired := make(chan struct{}, waiters)
	results := make(chan error, waiters)
	for i := 0; i < waiters; i++ {
		go func() {
			err := limiter.acquire(ctx)
			if err != nil {
				results <- err
				return
			}
			acquired <- struct{}{}
			<-hold
			limiter.release()
			results <- nil
		}()
	}

	waitForCondition(t, 5*time.Second, "all goroutines to wait at capacity one", func() bool {
		return limiter.waiters.Load() == waiters
	})
	limiter.resize(newLimit)

	for i := 0; i < newLimit-1; i++ {
		select {
		case <-acquired:
		case <-ctx.Done():
			t.Fatalf("resize admitted only %d/%d newly available permits: %v", i, newLimit-1, ctx.Err())
		}
	}
	waitForCondition(t, 5*time.Second, "limiter to fill the resized capacity", func() bool {
		return limiter.inUse.Load() == newLimit && limiter.waiters.Load() == waiters-(newLimit-1)
	})
	select {
	case <-acquired:
		t.Fatal("resize admitted a waiter beyond the new capacity")
	default:
	}

	limiter.release()
	initialHeld = false
	holdOnce.Do(func() { close(hold) })
	for i := 0; i < waiters; i++ {
		select {
		case err := <-results:
			if err != nil {
				t.Errorf("waiter failed after resize: %v", err)
			}
		case <-ctx.Done():
			t.Fatalf("waiters did not drain after resize: %v", ctx.Err())
		}
	}
	if got := limiter.inUse.Load(); got != 0 {
		t.Fatalf("inUse after resized waiters drained = %d, want 0", got)
	}
}

func TestResizableLimiterCanceledAcquire(t *testing.T) {
	limiter := newResizableLimiter(1)
	if err := limiter.acquire(context.Background()); err != nil {
		t.Fatalf("acquire initial permit: %v", err)
	}
	defer limiter.release()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := limiter.acquire(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled acquire error = %v, want context.Canceled", err)
	}
	if got := limiter.inUse.Load(); got != 1 {
		t.Fatalf("canceled acquire changed inUse to %d, want 1", got)
	}
	if got := limiter.waiters.Load(); got != 0 {
		t.Fatalf("canceled acquire leaked %d waiter(s)", got)
	}
}

func TestResizableLimiterStopWakesAllWaiters(t *testing.T) {
	const waiters = 24

	limiter := newResizableLimiter(1)
	if err := limiter.acquire(context.Background()); err != nil {
		t.Fatalf("acquire initial permit: %v", err)
	}
	defer limiter.release()

	results := make(chan error, waiters)
	for i := 0; i < waiters; i++ {
		go func() { results <- limiter.acquire(context.Background()) }()
	}
	waitForCondition(t, 5*time.Second, "all goroutines to wait before stop", func() bool {
		return limiter.waiters.Load() == waiters
	})

	limiter.stop()
	for i := 0; i < waiters; i++ {
		select {
		case err := <-results:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("stopped waiter error = %v, want context.Canceled", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("stop woke only %d/%d waiters", i, waiters)
		}
	}
	if got := limiter.waiters.Load(); got != 0 {
		t.Fatalf("stop left %d registered waiter(s)", got)
	}
	if got := limiter.inUse.Load(); got != 1 {
		t.Fatalf("stop changed an existing permit count to %d, want 1", got)
	}
}

func TestResizableLimiterReleaseNeverLeavesNegativeInUse(t *testing.T) {
	limiter := newResizableLimiter(1)
	for i := 0; i < 1_000; i++ {
		limiter.release()
		if got := limiter.inUse.Load(); got < 0 {
			t.Fatalf("inUse after unmatched release %d = %d, want non-negative", i, got)
		}
	}

	if err := limiter.acquire(context.Background()); err != nil {
		t.Fatalf("acquire after unmatched releases: %v", err)
	}
	limiter.release()
	limiter.release()
	if got := limiter.inUse.Load(); got != 0 {
		t.Fatalf("inUse after an extra release = %d, want 0", got)
	}
}

func TestHTTP3PoolActiveSlotPublicationUsesSnapshot(t *testing.T) {
	first := &http3PoolSlot{}
	second := &http3PoolSlot{}
	third := &http3PoolSlot{}
	replacement := &http3PoolSlot{}
	pool := &http3Pool{
		slots:  []*http3PoolSlot{first, second, third},
		active: 2,
	}

	pool.publishActiveLocked()
	published := pool.published.Load()
	if published == nil || len(published.slots) != 2 {
		t.Fatalf("published slots = %#v, want two slots", published)
	}
	if got := pool.activeConnections(); got != 2 {
		t.Fatalf("activeConnections = %d, want 2", got)
	}

	pool.slots[0] = replacement
	pool.active = 3
	if published.slots[0] != first || len(published.slots) != 2 {
		t.Fatal("mutating the pool changed an already published active-slot snapshot")
	}

	pool.publishActiveLocked()
	published = pool.published.Load()
	if len(published.slots) != 3 || published.slots[0] != replacement {
		t.Fatalf("republished slots = %#v, want replacement plus all three active slots", published.slots)
	}

	pool.active = 0
	pool.publishActiveLocked()
	if _, err := pool.pickSlot(); err == nil {
		t.Fatal("pickSlot succeeded with an empty published snapshot")
	}
}

func TestHTTP3PoolPickSlotPrefersLowerInFlightCandidate(t *testing.T) {
	slots := make([]*http3PoolSlot, 5)
	for i := range slots {
		slots[i] = &http3PoolSlot{}
	}
	// The first pick compares index 1 with index 0 for a five-slot snapshot.
	slots[1].inFlight.Store(10)
	slots[0].inFlight.Store(1)

	pool := &http3Pool{}
	pool.published.Store(&http3ActiveSlots{slots: slots})
	got, err := pool.pickSlot()
	if err != nil {
		t.Fatalf("pickSlot: %v", err)
	}
	if got != slots[0] {
		t.Fatalf("pickSlot chose slot %p with 10 requests, want lower-load slot %p", got, slots[0])
	}
}

func TestHTTP3PoolConcurrentPublishAndPick(t *testing.T) {
	const iterations = 2_000

	slots := make([]*http3PoolSlot, 8)
	allowed := make(map[*http3PoolSlot]struct{}, len(slots))
	for i := range slots {
		slots[i] = &http3PoolSlot{}
		allowed[slots[i]] = struct{}{}
	}
	pool := &http3Pool{slots: slots, active: 1}
	pool.publishActiveLocked()

	errs := make(chan string, 9)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			pool.mu.Lock()
			pool.active = i%len(slots) + 1
			pool.publishActiveLocked()
			pool.mu.Unlock()
		}
	}()
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				slot, err := pool.pickSlot()
				if err != nil {
					errs <- err.Error()
					return
				}
				if _, ok := allowed[slot]; !ok {
					errs <- "pickSlot returned a slot outside the pool"
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestTrackedHTTP3BodyCompletesExactlyOnce(t *testing.T) {
	readErr := errors.New("read failed")
	closeErr := errors.New("close failed")
	tests := []struct {
		name        string
		readN       int
		readErr     error
		closeErr    error
		closeFirst  bool
		wantSuccess bool
	}{
		{name: "EOF", readErr: io.EOF, wantSuccess: true},
		{name: "data and EOF", readN: 1, readErr: io.EOF, wantSuccess: true},
		{name: "read error", readErr: readErr, wantSuccess: false},
		{name: "clean close before read", readErr: readErr, closeFirst: true, wantSuccess: true},
		{name: "close error before EOF", readErr: io.EOF, closeErr: closeErr, closeFirst: true, wantSuccess: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			completions := 0
			completedSuccessfully := false
			body := &trackedHTTP3Body{
				ReadCloser: staticReadCloser{
					readN:    test.readN,
					readErr:  test.readErr,
					closeErr: test.closeErr,
				},
				done: func(success bool) {
					completions++
					completedSuccessfully = success
				},
			}

			if test.closeFirst {
				_ = body.Close()
				_, _ = body.Read(make([]byte, 1))
			} else {
				_, _ = body.Read(make([]byte, 1))
				_ = body.Close()
			}
			_, _ = body.Read(make([]byte, 1))
			_ = body.Close()

			if completions != 1 {
				t.Fatalf("completion callback count = %d, want 1", completions)
			}
			if completedSuccessfully != test.wantSuccess {
				t.Fatalf("completion success = %t, want %t", completedSuccessfully, test.wantSuccess)
			}
		})
	}
}

func TestTrackedHTTP3BodyConcurrentCompletionExactlyOnce(t *testing.T) {
	const callers = 64

	var completions atomic.Int64
	body := &trackedHTTP3Body{
		ReadCloser: staticReadCloser{readErr: io.EOF},
		done:       func(bool) { completions.Add(1) },
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(read bool) {
			defer wg.Done()
			<-start
			if read {
				_, _ = body.Read(make([]byte, 1))
				return
			}
			_ = body.Close()
		}(i%2 == 0)
	}
	close(start)
	wg.Wait()

	if got := completions.Load(); got != 1 {
		t.Fatalf("completion callback count = %d, want 1", got)
	}
}

func TestTrackedHTTP3BodyRemoteResetBurstReleasesEveryPermit(t *testing.T) {
	const requests = 256
	resetErr := errors.New("peer reset stream")
	limiter := newResizableLimiter(requests)
	slot := &http3PoolSlot{}
	pool := &http3Pool{}
	bodies := make([]*trackedHTTP3Body, requests)
	for i := range bodies {
		if err := limiter.acquire(context.Background()); err != nil {
			t.Fatal(err)
		}
		slot.inFlight.Add(1)
		bodies[i] = &trackedHTTP3Body{
			ReadCloser: staticReadCloser{readErr: resetErr},
			slot:       slot,
			limiter:    limiter,
			pool:       pool,
		}
	}

	var wg sync.WaitGroup
	for i := range bodies {
		wg.Add(1)
		go func(body *trackedHTTP3Body) {
			defer wg.Done()
			_, _ = body.Read(make([]byte, 1))
			_ = body.Close()
		}(bodies[i])
	}
	wg.Wait()

	if got := slot.inFlight.Load(); got != 0 {
		t.Fatalf("slot retained %d reset streams", got)
	}
	if got := limiter.inUse.Load(); got != 0 {
		t.Fatalf("limiter retained %d reset streams", got)
	}
	if got := pool.failures.Load(); got != requests {
		t.Fatalf("failure count = %d, want %d", got, requests)
	}
}

type staticReadCloser struct {
	readN    int
	readErr  error
	closeErr error
}

func (r staticReadCloser) Read(dst []byte) (int, error) {
	n := r.readN
	if n > len(dst) {
		n = len(dst)
	}
	for i := 0; i < n; i++ {
		dst[i] = 'x'
	}
	return n, r.readErr
}

func (r staticReadCloser) Close() error {
	return r.closeErr
}

func recordAtomicMaximum(maximum *atomic.Int64, candidate int64) {
	for {
		current := maximum.Load()
		if candidate <= current || maximum.CompareAndSwap(current, candidate) {
			return
		}
	}
}

func waitForCondition(t *testing.T, timeout time.Duration, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	if !condition() {
		t.Fatalf("timed out waiting for %s", description)
	}
}
