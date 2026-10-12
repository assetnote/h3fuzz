package runner

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
	"github.com/quic-go/quic-go"
)

const (
	defaultAdaptiveHTTP3Connections = 8
	http3TuneSampleInterval         = 250 * time.Millisecond
	http3TuneWindow                 = time.Second
	http3TuneImprovement            = 1.03
	http3SteadyProbeInterval        = 30 * time.Second
	http3ZeroProgressBackoffWindows = 3
	maxAutomaticWorkers             = 65536
)

type http3PoolSlot struct {
	transport   *ownedHTTP3Transport
	diagnostics *quicSlotDiagnostics
	pool        *http3Pool
	inFlight    atomic.Int64
}

type http3ActiveSlots struct {
	slots []*http3PoolSlot
}

type http3PoolTelemetry struct {
	completed         uint64
	errors            uint64
	inFlight          int64
	activeConnections int
	openedConnections int
	limiterInUse      int64
	limiterWaiters    int64
	closed            bool
	diagnostics       *quicDiagnosticsSnapshot
	capacity          *capacityAdvisorSnapshot
}

type quicApplicationAttemptContextKey struct{}

func withQUICApplicationAttempt(ctx context.Context) context.Context {
	return context.WithValue(ctx, quicApplicationAttemptContextKey{}, true)
}

func markQUICApplicationAttempt(req *http.Request) *http.Request {
	return req.WithContext(withQUICApplicationAttempt(req.Context()))
}

func isQUICApplicationAttempt(req *http.Request) bool {
	marked, _ := req.Context().Value(quicApplicationAttemptContextKey{}).(bool)
	return marked
}

// http3Pool is an http.RoundTripper backed by multiple independent QUIC
// connections to the same origin. A separate http3.Transport per slot is
// intentional: each transport otherwise keeps a single cached connection per
// origin, which leaves one packet-processing path as the throughput bottleneck.
type http3Pool struct {
	conf                   *ffuf.Config
	target                 *url.URL
	tlsConfig              *tls.Config
	quicConfig             *quic.Config
	probe                  *http.Request
	diagnostics            *quicDiagnostics
	diagnosticSlotSequence atomic.Uint64

	streamsPerConnection int
	maxConnections       int
	maxWorkers           int
	fixedConnections     int

	mu        sync.RWMutex
	slots     []*http3PoolSlot
	active    int
	published atomic.Pointer[http3ActiveSlots]
	next      atomic.Uint64

	limiter   *resizableLimiter
	reconnect *http3ReconnectGate

	completed atomic.Uint64
	failures  atomic.Uint64
	// workerCeiling is the number of request workers that can concurrently reach
	// the transport. Adaptive expansion can raise the stream-admission limit above
	// this value, so tuner saturation must be measured against the smaller of the
	// stream budget and the worker budget. Otherwise the first 8 -> 16 connection
	// expansion makes saturation mathematically impossible with the deliberately
	// low initial worker count and tuning freezes forever.
	workerCeiling atomic.Int64
	capacity      *capacityAdvisor

	startOnce       sync.Once
	startDone       chan struct{}
	startActive     int
	startErr        error
	lifecycleMu     sync.Mutex
	lifecycleClosed bool
	startRunning    bool
	closeOnce       sync.Once
	closed          chan struct{}
	ctx             context.Context
	cancel          context.CancelFunc
	tuneWG          sync.WaitGroup
}

func newHTTP3Pool(conf *ffuf.Config, target *url.URL, probe *http.Request) (*http3Pool, error) {
	tlsConf, err := http3TLSConfig(conf, target)
	if err != nil {
		return nil, err
	}
	maxConnections := conf.Http3MaxConnections
	if maxConnections <= 0 {
		maxConnections = 64
	}
	streams := conf.Http3Streams
	if streams <= 0 {
		streams = 256
	}
	maxWorkers := conf.Http3MaxWorkers
	if maxWorkers <= 0 || maxWorkers > maxAutomaticWorkers {
		maxWorkers = maxAutomaticWorkers
	}
	var diagnostics *quicDiagnostics
	if conf.RunTelemetry != "" {
		diagnostics, err = newQUICDiagnostics(conf.QuicTelemetry, conf.QuicQlogDir)
		if err != nil {
			return nil, err
		}
	}
	poolContext, cancel := context.WithCancel(configContext(conf))
	p := &http3Pool{
		conf:                 conf,
		target:               target,
		tlsConfig:            tlsConf,
		quicConfig:           http3QUICConfig(time.Duration(conf.Http3ProbeTimeout) * time.Second),
		probe:                probe,
		diagnostics:          diagnostics,
		streamsPerConnection: streams,
		maxConnections:       maxConnections,
		maxWorkers:           maxWorkers,
		fixedConnections:     conf.Http3Connections,
		startDone:            make(chan struct{}),
		closed:               make(chan struct{}),
		ctx:                  poolContext,
		cancel:               cancel,
	}
	initialConnections := p.fixedConnections
	if initialConnections <= 0 {
		initialConnections = defaultAdaptiveHTTP3Connections
	}
	initialConnections = minInt(initialConnections, p.maxConnections)
	p.reconnect = newHTTP3ReconnectGate(maxInt(1, initialConnections), nil)
	p.published.Store(&http3ActiveSlots{})
	if conf.ThreadsExplicit && conf.Threads > 0 {
		p.workerCeiling.Store(int64(conf.Threads))
	} else {
		p.capacity = newCapacityAdvisor(1, maxDuration(2*time.Second, 2*http3TuneWindow), http3TuneWindow)
	}
	return p, nil
}

func (p *http3Pool) newSlotTransport(slotDiagnostics *quicSlotDiagnostics) *ownedHTTP3Transport {
	quicConfig := p.quicConfig.Clone()
	if p.diagnostics != nil && slotDiagnostics != nil {
		quicConfig.Tracer = p.diagnostics.tracerForSlot(slotDiagnostics, quicConfig.Tracer)
	}
	return newOwnedHTTP3Transport(p.target.Host, p.tlsConfig.Clone(), quicConfig, p.reconnect)
}

func (p *http3Pool) start(ctx context.Context) (int, error) {
	p.startOnce.Do(func() {
		p.lifecycleMu.Lock()
		if p.lifecycleClosed {
			p.startErr = errors.New("HTTP/3 pool is closed")
			p.lifecycleMu.Unlock()
			close(p.startDone)
			return
		}
		p.startRunning = true
		p.lifecycleMu.Unlock()

		p.startActive, p.startErr = p.startInner(ctx)
		close(p.startDone)
	})
	<-p.startDone
	return p.startActive, p.startErr
}

func (p *http3Pool) startInner(parent context.Context) (int, error) {
	if parent == nil {
		parent = context.Background()
	}
	warmContext, cancelWarm := context.WithCancel(p.ctx)
	stopParent := context.AfterFunc(parent, cancelWarm)
	defer func() {
		stopParent()
		cancelWarm()
	}()
	desired := p.fixedConnections
	if desired == 0 {
		desired = defaultAdaptiveHTTP3Connections
	}
	if desired > p.maxConnections {
		desired = p.maxConnections
	}
	if desired < 1 {
		desired = 1
	}
	if err := p.warmTo(warmContext, desired); err != nil {
		return 0, err
	}
	if p.fixedConnections > 0 && p.slotCount() != desired {
		return 0, fmt.Errorf("opened %d of %d requested HTTP/3 connections", p.slotCount(), desired)
	}
	p.mu.Lock()
	p.active = len(p.slots)
	active := p.active
	p.publishActiveLocked()
	p.mu.Unlock()
	if p.reconnect != nil {
		p.reconnect.setDesiredConnections(active)
	}
	p.limiter = newResizableLimiter(active * p.streamsPerConnection)
	if p.workerCeiling.Load() <= 0 {
		p.workerCeiling.Store(int64(p.workerCountForConnections(active)))
	}
	if p.capacity != nil {
		p.capacity.setDesired(p.workerCountForConnections(active))
	}
	if p.fixedConnections == 0 && active < p.maxConnections {
		p.tuneWG.Add(1)
		go p.tuneLoop()
	}
	return active, nil
}

func (p *http3Pool) RoundTrip(req *http.Request) (*http.Response, error) {
	applicationAttempt := isQUICApplicationAttempt(req)
	limiter := p.limiter
	if limiter == nil {
		err := errors.New("HTTP/3 pool was not started")
		if p.diagnostics != nil && applicationAttempt {
			p.diagnostics.recordUnattributedApplicationFailure(err)
		}
		return nil, err
	}
	var slot *http3PoolSlot
	for {
		if err := limiter.acquire(req.Context()); err != nil {
			if p.diagnostics != nil && applicationAttempt {
				p.diagnostics.recordUnattributedApplicationFailure(err)
			}
			return nil, err
		}
		var reserveErr error
		slot, reserveErr = p.reserveSlot()
		if reserveErr == nil {
			break
		}
		// The tuner may shrink the active snapshot between aggregate admission
		// and per-slot reservation. Release and re-admit against the new limit;
		// returning a synthetic request failure here would incorrectly penalize
		// a healthy target and trigger another backoff.
		limiter.release()
	}
	request, observation := p.instrumentQUICRequest(slot, req)
	resp, err := slot.transport.RoundTrip(request)
	if observation != nil {
		observation.finish(err)
	}
	if err != nil {
		slot.inFlight.Add(-1)
		limiter.release()
		p.failures.Add(1)
		return nil, err
	}
	if body, ok := resp.Body.(*ownedHTTP3ResponseBody); ok {
		// Generation ownership and pool completion need the same EOF / Close
		// lifetime. Attach the pool counters to the existing wrapper instead of
		// allocating a second response-body wrapper on every healthy request.
		body.slot = slot
		return resp, nil
	}
	resp.Body = &trackedHTTP3Body{
		ReadCloser: resp.Body,
		slot:       slot,
		limiter:    limiter,
		pool:       p,
	}
	return resp, nil
}

func (p *http3Pool) instrumentQUICRequest(slot *http3PoolSlot, req *http.Request) (*http.Request, *quicAttemptObservation) {
	if p.diagnostics == nil || slot == nil || slot.diagnostics == nil {
		return req, nil
	}
	var observation *quicAttemptObservation
	var trace *httptrace.ClientTrace
	if isQUICApplicationAttempt(req) {
		observation, trace = p.diagnostics.newAttemptTrace(slot.diagnostics)
	} else {
		trace = p.diagnostics.lifecycleClientTrace(slot.diagnostics)
	}
	return req.WithContext(httptrace.WithClientTrace(req.Context(), trace)), observation
}

func (p *http3Pool) reserveSlot() (*http3PoolSlot, error) {
	active := p.published.Load()
	if active == nil || len(active.slots) == 0 {
		return nil, errors.New("HTTP/3 connection pool has no live connections")
	}
	n := len(active.slots)
	start := int(p.next.Add(1) % uint64(n))
	// A full scan is rare (the aggregate limiter guarantees capacity) and makes
	// the per-connection stream limit exact even when two-choice sampling happens
	// to select full slots while another connection is idle.
	for offset := 0; offset < n; offset++ {
		slot := active.slots[(start+offset)%n]
		for {
			used := slot.inFlight.Load()
			if used >= int64(p.streamsPerConnection) {
				break
			}
			if slot.inFlight.CompareAndSwap(used, used+1) {
				return slot, nil
			}
		}
	}
	return nil, errors.New("all HTTP/3 connections reached their stream limit")
}

type trackedHTTP3Body struct {
	io.ReadCloser
	finished atomic.Bool
	slot     *http3PoolSlot
	limiter  *resizableLimiter
	pool     *http3Pool
	// done is a test seam for exercising completion independently of a pool.
	// Production wrappers use the direct pointers above and allocate no closure.
	done func(bool)
}

func (b *trackedHTTP3Body) finish(success bool) {
	if !b.finished.CompareAndSwap(false, true) {
		return
	}
	if b.done != nil {
		b.done(success)
		return
	}
	finishHTTP3PoolResponse(b.slot, b.limiter, b.pool, success)
}

func finishHTTP3PoolResponse(slot *http3PoolSlot, limiter *resizableLimiter, pool *http3Pool, success bool) {
	slot.inFlight.Add(-1)
	limiter.release()
	if success {
		pool.completed.Add(1)
	} else {
		pool.failures.Add(1)
	}
}

func (b *trackedHTTP3Body) Read(dst []byte) (int, error) {
	n, err := b.ReadCloser.Read(dst)
	if err == io.EOF {
		b.finish(true)
	} else if err != nil {
		b.finish(false)
	}
	return n, err
}

func (b *trackedHTTP3Body) Close() error {
	err := b.ReadCloser.Close()
	// A clean early Close is an intentional stream cancellation (for example
	// -ignore-body or an oversized response), not a transport failure. Response
	// headers have already completed, which is the unit the tuner is measuring.
	b.finish(err == nil)
	return err
}

func (p *http3Pool) pickSlot() (*http3PoolSlot, error) {
	active := p.published.Load()
	if active == nil || len(active.slots) == 0 {
		return nil, errors.New("HTTP/3 connection pool has no live connections")
	}
	n := uint64(len(active.slots))
	sequence := p.next.Add(1)
	first := sequence % n
	a := active.slots[first]
	if n == 1 {
		return a, nil
	}
	// SplitMix64 produces a second, decorrelated choice. Multiplication modulo a
	// power-of-two pool made both choices identical for common sizes (2, 4, ...).
	mixed := sequence + 0x9e3779b97f4a7c15
	mixed = (mixed ^ (mixed >> 30)) * 0xbf58476d1ce4e5b9
	mixed = (mixed ^ (mixed >> 27)) * 0x94d049bb133111eb
	mixed ^= mixed >> 31
	second := mixed % n
	if second == first {
		second = (second + 1) % n
	}
	b := active.slots[second]
	if b.inFlight.Load() < a.inFlight.Load() {
		return b, nil
	}
	return a, nil
}

func (p *http3Pool) warmTo(parent context.Context, desired int) error {
	p.mu.RLock()
	existing := len(p.slots)
	p.mu.RUnlock()
	if desired <= existing {
		return nil
	}
	if desired > p.maxConnections {
		desired = p.maxConnections
	}

	type result struct {
		slot *http3PoolSlot
		err  error
	}
	results := make(chan result, desired-existing)
	var wg sync.WaitGroup
	for i := existing; i < desired; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slot, err := p.openWarmSlot(parent)
			results <- result{slot: slot, err: err}
		}()
	}
	wg.Wait()
	close(results)

	var firstErr error
	opened := make([]*http3PoolSlot, 0, desired-existing)
	for result := range results {
		if result.err != nil {
			if firstErr == nil {
				firstErr = result.err
			}
			continue
		}
		opened = append(opened, result.slot)
	}
	if len(opened) > 0 {
		p.mu.Lock()
		p.slots = append(p.slots, opened...)
		p.mu.Unlock()
	}
	if len(opened) == 0 && desired > existing {
		if firstErr == nil {
			firstErr = fmt.Errorf("could not expand HTTP/3 pool from %d to %d connections", existing, desired)
		}
		return firstErr
	}
	p.mu.RLock()
	total := len(p.slots)
	p.mu.RUnlock()
	if total == 0 {
		if firstErr == nil {
			firstErr = errors.New("no HTTP/3 connections opened")
		}
		return firstErr
	}
	return nil
}

func (p *http3Pool) openWarmSlot(parent context.Context) (*http3PoolSlot, error) {
	timeout := time.Duration(p.conf.Http3ProbeTimeout) * time.Second
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	var slotDiagnostics *quicSlotDiagnostics
	if p.diagnostics != nil {
		slotID := int(p.diagnosticSlotSequence.Add(1) - 1)
		slotDiagnostics = newQUICSlotDiagnostics(slotID)
	}
	transport := p.newSlotTransport(slotDiagnostics)
	request := p.probe.Clone(ctx)
	if p.diagnostics != nil {
		request = request.WithContext(httptrace.WithClientTrace(request.Context(), p.diagnostics.lifecycleClientTrace(slotDiagnostics)))
	}
	resp, err := transport.RoundTrip(request)
	if err != nil {
		_ = transport.Close()
		return nil, err
	}
	readBytes, readErr := io.Copy(io.Discard, io.LimitReader(resp.Body, fingerprintBodyLimit+1))
	closeErr := resp.Body.Close()
	if readErr != nil || closeErr != nil || readBytes > fingerprintBodyLimit || resp.ProtoMajor != 3 {
		_ = transport.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if readBytes > fingerprintBodyLimit {
			return nil, fmt.Errorf("HEAD warmup returned more than %d response-body bytes", fingerprintBodyLimit)
		}
		return nil, fmt.Errorf("warmup response used %s", resp.Proto)
	}
	return &http3PoolSlot{transport: transport, diagnostics: slotDiagnostics, pool: p}, nil
}

func (p *http3Pool) tuneLoop() {
	defer p.tuneWG.Done()
	ticker := time.NewTicker(http3TuneSampleInterval)
	defer ticker.Stop()
	var capacityNotifications <-chan struct{}
	if p.capacity != nil {
		capacityNotifications = p.capacity.notifications()
	}

	lastCompleted := p.completed.Load()
	lastFailures := p.failures.Load()
	windowStarted := time.Now()
	saturatedFor := time.Duration(0)
	bestConnections := p.activeConnections()
	bestRPS := 0.0
	upperBoundary := 0
	transition := false
	steadySince := time.Now()
	failureWindows := 0
	cleanWindows := 0
	zeroProgressWindows := 0

	for {
		select {
		case <-p.closed:
			return
		case <-p.ctx.Done():
			return
		case <-capacityNotifications:
			if p.pollCapacity(time.Now()) {
				// Only a matching acknowledgement establishes a new worker
				// ceiling and measurement baseline. Stale/late tuples do neither.
				lastCompleted = p.completed.Load()
				lastFailures = p.failures.Load()
				windowStarted = time.Now()
				saturatedFor = 0
				transition = true
			}
		case <-ticker.C:
			capacityApplied := p.pollCapacity(time.Now())
			if p.capacity != nil {
				capacity := p.capacity.snapshot()
				if capacityApplied || capacity.Pending || capacity.Suspended {
					lastCompleted = p.completed.Load()
					lastFailures = p.failures.Load()
					windowStarted = time.Now()
					saturatedFor = 0
					if capacityApplied {
						transition = true
					}
					continue
				}
			}
			if p.limiter.saturatedAt(p.workerCeiling.Load()) {
				saturatedFor += http3TuneSampleInterval
			}
			elapsed := time.Since(windowStarted)
			if elapsed < http3TuneWindow {
				continue
			}
			completed := p.completed.Load()
			failures := p.failures.Load()
			deltaCompleted := completed - lastCompleted
			deltaFailures := failures - lastFailures
			rps := float64(deltaCompleted) / elapsed.Seconds()
			// Only tune while demand has kept the current pool genuinely busy.
			// Short bursts otherwise look like an improvement and over-open QUIC
			// connections. Turbo Intruder requires saturation for most of the
			// measurement window; use the same 75% threshold here.
			saturated := saturatedFor >= (elapsed*3)/4
			eligible := saturated && deltaCompleted > 0

			lastCompleted, lastFailures = completed, failures
			windowStarted = time.Now()
			saturatedFor = 0
			current := p.activeConnections()
			total := deltaCompleted + deltaFailures
			failureRate := 0.0
			if total > 0 {
				failureRate = float64(deltaFailures) / float64(total)
			}
			var zeroProgressBackoff bool
			zeroProgressWindows, zeroProgressBackoff = p.maybeBackoffZeroProgress(
				zeroProgressWindows, current, saturated, deltaCompleted, deltaFailures,
			)
			if zeroProgressBackoff {
				if p.activeConnections() < current {
					transition = true
				}
				steadySince = time.Now()
				continue
			}
			significantFailure := deltaFailures > 0 && (deltaCompleted == 0 || deltaFailures >= 3 || failureRate >= 0.001)
			if significantFailure {
				failureWindows++
				cleanWindows = 0
				backoff := current * 3 / 4
				if failureWindows > 1 {
					backoff = current / 2
				}
				if backoff < 1 {
					backoff = 1
				}
				p.setActive(backoff)
				transition = true
				steadySince = time.Now()
				continue
			}
			if failureWindows > 0 {
				cleanWindows++
				if cleanWindows < 2 {
					continue
				}
				failureWindows = 0
				cleanWindows = 0
			}
			if transition {
				transition = false
				continue
			}
			if !eligible {
				continue
			}
			if bestRPS == 0 {
				bestRPS, bestConnections = rps, current
				if current < p.maxConnections {
					transition = p.tryTuneResize(minInt(current*2, p.maxConnections))
				}
				continue
			}
			if rps >= bestRPS*http3TuneImprovement {
				bestRPS, bestConnections = rps, current
				upperBoundary = 0
				if current < p.maxConnections {
					transition = p.tryTuneResize(minInt(current*2, p.maxConnections))
				} else {
					steadySince = time.Now()
				}
				continue
			}

			if current > bestConnections {
				upperBoundary = current
			}
			if upperBoundary-bestConnections > 1 {
				transition = p.tryTuneResize(bestConnections + (upperBoundary-bestConnections)/2)
				continue
			}
			if current != bestConnections {
				p.setActive(bestConnections)
				transition = true
			}
			if time.Since(steadySince) >= http3SteadyProbeInterval && bestConnections < p.maxConnections {
				probe := bestConnections + maxInt(1, bestConnections/10)
				transition = p.tryTuneResize(minInt(probe, p.maxConnections))
				steadySince = time.Now()
			}
		}
	}
}

// observeHTTP3ZeroProgress recognizes congestion before request deadlines turn
// it into a synchronized failure burst. It deliberately needs several complete
// tuning windows with a saturated limiter and no terminal request signal. Any
// completion, failure, or spare admission capacity resets the evidence.
func observeHTTP3ZeroProgress(streak int, saturated bool, completed, failures uint64) (int, bool) {
	if !saturated || completed != 0 || failures != 0 {
		return 0, false
	}
	streak++
	return streak, streak >= http3ZeroProgressBackoffWindows
}

func (p *http3Pool) maybeBackoffZeroProgress(streak, current int, saturated bool, completed, failures uint64) (int, bool) {
	streak, backoff := observeHTTP3ZeroProgress(streak, saturated, completed, failures)
	if !backoff {
		return streak, false
	}

	// A fully occupied pool with no response or error signal is usually waiting
	// on a remote admission/WAF timeout. Do not cancel requests that may still
	// complete, but reduce the active snapshot used after they drain so the
	// retry/recovery wave cannot immediately recreate the same congestion.
	target := current * 3 / 4
	if target < 1 {
		target = 1
	}
	if target < current {
		p.setActive(target)
	}
	return 0, true
}

func (p *http3Pool) tryTuneResize(desired int) bool {
	timeout := time.Duration(p.conf.Http3ProbeTimeout) * time.Second
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	ctx, cancel := context.WithTimeout(p.ctx, timeout)
	defer cancel()
	if err := p.warmTo(ctx, desired); err != nil {
		return false
	}
	p.setActive(minInt(desired, p.slotCount()))
	return true
}

func (p *http3Pool) setActive(active int) {
	p.mu.Lock()
	previous := p.active
	if active < 1 {
		active = 1
	}
	if active > len(p.slots) {
		active = len(p.slots)
	}
	p.active = active
	p.publishActiveLocked()
	p.mu.Unlock()
	if p.reconnect != nil {
		p.reconnect.setDesiredConnections(active)
	}
	if p.limiter != nil {
		p.limiter.resize(active * p.streamsPerConnection)
	}
	if p.capacity != nil && active != previous {
		cause := ffuf.CapacityCausePoolGrowth
		if active < previous {
			cause = ffuf.CapacityCausePoolShrink
		}
		p.capacity.propose(p.workerCountForConnections(active), cause)
	}
}

// SubscribeCapacity implements ffuf.CapacityAdvisor without exposing the
// concrete HTTP/3 pool to the engine.
func (p *http3Pool) SubscribeCapacity(executionGeneration uint64) (ffuf.CapacitySubscription, error) {
	if p == nil || p.capacity == nil {
		return nil, errors.New("adaptive HTTP/3 worker capacity is unavailable")
	}
	return p.capacity.subscribe(executionGeneration)
}

// pollCapacity advances tuple timeout/recovery state and applies only a matching
// live-worker acknowledgement to the pool's tuning ceiling.
func (p *http3Pool) pollCapacity(now time.Time) bool {
	if p == nil || p.capacity == nil {
		return false
	}
	p.capacity.poll(now)
	applied := false
	for {
		select {
		case outcome := <-p.capacity.results():
			if outcome.Status == capacityOutcomeApplied {
				p.workerCeiling.Store(int64(outcome.Acknowledgement.ActualLiveWorkers))
				applied = true
			}
		default:
			return applied
		}
	}
}

func (p *http3Pool) activeConnections() int {
	active := p.published.Load()
	if active == nil {
		return 0
	}
	return len(active.slots)
}

func (p *http3Pool) publishActiveLocked() {
	slots := append([]*http3PoolSlot(nil), p.slots[:p.active]...)
	p.published.Store(&http3ActiveSlots{slots: slots})
}

func (p *http3Pool) slotCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.slots)
}

func (p *http3Pool) suggestedWorkers() int {
	// Adaptive mode starts with a deliberately small active set and grows only
	// when measured throughput improves. Sizing workers for the maximum pool
	// created thousands of idle goroutines up front (64 * 256 by default), adding
	// scheduler and stack pressure before the tuner had opened those connections.
	// The initial active stream budget is enough to keep the current pool full;
	// those workers can be redistributed when later connections become active.
	connections := p.activeConnections()
	if p.fixedConnections > 0 {
		connections = p.fixedConnections
	}
	return p.workerCountForConnections(connections)
}

func (p *http3Pool) workerCountForConnections(connections int) int {
	if connections <= 0 || p.streamsPerConnection <= 0 {
		return 1
	}
	maxWorkers := p.maxWorkers
	if maxWorkers <= 0 || maxWorkers > maxAutomaticWorkers {
		maxWorkers = maxAutomaticWorkers
	}
	if connections > maxWorkers/p.streamsPerConnection {
		return maxWorkers
	}
	workers := connections * p.streamsPerConnection
	if workers > maxWorkers {
		return maxWorkers
	}
	return maxInt(1, workers)
}

func (p *http3Pool) Close() error {
	var closeErr error
	p.closeOnce.Do(func() {
		p.lifecycleMu.Lock()
		p.lifecycleClosed = true
		startRunning := p.startRunning
		p.lifecycleMu.Unlock()
		close(p.closed)
		p.cancel()
		if startRunning {
			<-p.startDone
		}
		if p.limiter != nil {
			p.limiter.stop()
		}
		p.tuneWG.Wait()
		if p.capacity != nil {
			p.capacity.close()
		}
		p.mu.RLock()
		slots := append([]*http3PoolSlot(nil), p.slots...)
		p.mu.RUnlock()
		for _, slot := range slots {
			if err := slot.transport.Close(); err != nil && closeErr == nil {
				closeErr = err
			}
		}
		if p.diagnostics != nil {
			timeout := time.Duration(p.conf.Http3ProbeTimeout) * time.Second
			if err := p.diagnostics.waitForDrain(timeout); err != nil {
				closeErr = errors.Join(closeErr, err)
			}
		}
	})
	return closeErr
}

func (p *http3Pool) telemetrySnapshot() http3PoolTelemetry {
	result := http3PoolTelemetry{
		completed: p.completed.Load(),
		errors:    p.failures.Load(),
	}
	active := p.published.Load()
	if active != nil {
		result.activeConnections = len(active.slots)
	}
	p.mu.RLock()
	result.openedConnections = len(p.slots)
	diagnosticSlots := make([]*quicSlotDiagnostics, 0, len(p.slots))
	for _, slot := range p.slots {
		result.inFlight += slot.inFlight.Load()
		if slot.diagnostics != nil {
			diagnosticSlots = append(diagnosticSlots, slot.diagnostics)
		}
	}
	p.mu.RUnlock()
	if p.diagnostics != nil {
		snapshot := p.diagnostics.snapshot(diagnosticSlots)
		result.diagnostics = &snapshot
	}
	if p.capacity != nil {
		snapshot := p.capacity.snapshot()
		result.capacity = &snapshot
	}
	if p.limiter != nil {
		result.limiterInUse = p.limiter.inUse.Load()
		result.limiterWaiters = p.limiter.waiters.Load()
	}
	select {
	case <-p.closed:
		result.closed = true
	default:
	}
	return result
}

func configContext(conf *ffuf.Config) context.Context {
	if conf != nil && conf.Context != nil {
		return conf.Context
	}
	return context.Background()
}

// resizableLimiter bounds stream admission without recreating a channel when
// the adaptive connection count changes. Waiters always honor request context.
type resizableLimiter struct {
	limit    atomic.Int64
	inUse    atomic.Int64
	waiters  atomic.Int64
	wake     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

func newResizableLimiter(limit int) *resizableLimiter {
	if limit < 1 {
		limit = 1
	}
	l := &resizableLimiter{wake: make(chan struct{}, 1), done: make(chan struct{})}
	l.limit.Store(int64(limit))
	return l
}

func (l *resizableLimiter) acquire(ctx context.Context) error {
	for {
		select {
		case <-l.done:
			return context.Canceled
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		used := l.inUse.Load()
		limit := l.limit.Load()
		if used < limit {
			if !l.inUse.CompareAndSwap(used, used+1) {
				// Another caller won this slot. Re-read immediately; do not turn
				// harmless CAS contention into serialized wake-token admission.
				continue
			}
			// Cascade a single wake token so a large upward resize admits every
			// newly available slot without broadcasting to all waiters.
			if used+1 < limit {
				l.signal()
			}
			select {
			case <-l.done:
				l.release()
				return context.Canceled
			case <-ctx.Done():
				l.release()
				return ctx.Err()
			default:
			}
			return nil
		}
		l.waiters.Add(1)
		select {
		case <-l.wake:
			l.waiters.Add(-1)
		case <-l.done:
			l.waiters.Add(-1)
			return context.Canceled
		case <-ctx.Done():
			l.waiters.Add(-1)
			return ctx.Err()
		}
	}
}

func (l *resizableLimiter) release() {
	if l.inUse.Add(-1) < 0 {
		l.inUse.Store(0)
	}
	l.signal()
}

func (l *resizableLimiter) resize(limit int) {
	if limit < 1 {
		limit = 1
	}
	l.limit.Store(int64(limit))
	l.signal()
}

func (l *resizableLimiter) saturatedAt(workerCeiling int64) bool {
	capacity := l.limit.Load()
	if workerCeiling > 0 && workerCeiling < capacity {
		capacity = workerCeiling
	}
	return l.waiters.Load() > 0 || l.inUse.Load() >= capacity
}

func (l *resizableLimiter) signal() {
	select {
	case l.wake <- struct{}{}:
	default:
	}
}

func (l *resizableLimiter) stop() {
	l.stopOnce.Do(func() { close(l.done) })
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}
