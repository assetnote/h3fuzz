package runner

import (
	"context"
	"math/rand/v2"
	"sync"
	"time"
)

const (
	http3ReconnectInitialBackoff = 250 * time.Millisecond
	http3ReconnectMaximumBackoff = 10 * time.Second
)

type http3ReconnectGate struct {
	mu sync.Mutex

	desiredConnections  int
	inFlight            int
	consecutiveFailures int
	notBefore           time.Time
	wake                chan struct{}

	now    func() time.Time
	wait   func(context.Context, time.Duration, <-chan struct{}) error
	random func() float64
}

type http3ReconnectGateOptions struct {
	now    func() time.Time
	wait   func(context.Context, time.Duration, <-chan struct{}) error
	random func() float64
}

type http3ReconnectPermit struct {
	gate *http3ReconnectGate
	once sync.Once
}

type http3ReconnectGateSnapshot struct {
	DesiredConnections  int
	DialLimit           int
	InFlight            int
	ConsecutiveFailures int
	NotBefore           time.Time
}

func newHTTP3ReconnectGate(desiredConnections int, options *http3ReconnectGateOptions) *http3ReconnectGate {
	gate := &http3ReconnectGate{
		desiredConnections: maxInt(1, desiredConnections),
		wake:               make(chan struct{}),
		now:                time.Now,
		wait:               waitHTTP3Reconnect,
		random:             rand.Float64,
	}
	if options != nil {
		if options.now != nil {
			gate.now = options.now
		}
		if options.wait != nil {
			gate.wait = options.wait
		}
		if options.random != nil {
			gate.random = options.random
		}
	}
	return gate
}

func (g *http3ReconnectGate) acquire(ctx context.Context) (*http3ReconnectPermit, error) {
	for {
		g.mu.Lock()
		now := g.now()
		delay := g.notBefore.Sub(now)
		if g.inFlight < g.dialLimitLocked() && delay <= 0 {
			g.inFlight++
			g.mu.Unlock()
			return &http3ReconnectPermit{gate: g}, nil
		}
		wake := g.wake
		if g.inFlight >= g.dialLimitLocked() {
			delay = 0
		}
		g.mu.Unlock()
		if err := g.wait(ctx, delay, wake); err != nil {
			return nil, err
		}
	}
}

func (p *http3ReconnectPermit) finish(success bool) {
	if p == nil || p.gate == nil {
		return
	}
	p.once.Do(func() {
		p.gate.finish(success)
	})
}

func (g *http3ReconnectGate) finish(success bool) {
	g.mu.Lock()
	if g.inFlight > 0 {
		g.inFlight--
	}
	if success {
		g.consecutiveFailures = 0
		g.notBefore = time.Time{}
	} else {
		g.consecutiveFailures++
		delay := http3ReconnectBackoff(g.consecutiveFailures, g.random())
		candidate := g.now().Add(delay)
		if candidate.After(g.notBefore) {
			g.notBefore = candidate
		}
	}
	g.notifyLocked()
	g.mu.Unlock()
}

func (g *http3ReconnectGate) setDesiredConnections(desired int) {
	g.mu.Lock()
	g.desiredConnections = maxInt(1, desired)
	g.notifyLocked()
	g.mu.Unlock()
}

func (g *http3ReconnectGate) snapshot() http3ReconnectGateSnapshot {
	g.mu.Lock()
	defer g.mu.Unlock()
	return http3ReconnectGateSnapshot{
		DesiredConnections:  g.desiredConnections,
		DialLimit:           g.dialLimitLocked(),
		InFlight:            g.inFlight,
		ConsecutiveFailures: g.consecutiveFailures,
		NotBefore:           g.notBefore,
	}
}

func (g *http3ReconnectGate) dialLimitLocked() int {
	return minInt(4, maxInt(1, (g.desiredConnections+3)/4))
}

func (g *http3ReconnectGate) notifyLocked() {
	close(g.wake)
	g.wake = make(chan struct{})
}

func http3ReconnectBackoff(failures int, random float64) time.Duration {
	if failures < 1 {
		failures = 1
	}
	delay := http3ReconnectInitialBackoff
	for step := 1; step < failures && delay < http3ReconnectMaximumBackoff; step++ {
		if delay >= http3ReconnectMaximumBackoff/2 {
			delay = http3ReconnectMaximumBackoff
			break
		}
		delay *= 2
	}
	if random < 0 {
		random = 0
	}
	if random > 1 {
		random = 1
	}
	jittered := time.Duration(float64(delay) * (0.8 + 0.4*random))
	if jittered > http3ReconnectMaximumBackoff {
		return http3ReconnectMaximumBackoff
	}
	return jittered
}

func waitHTTP3Reconnect(ctx context.Context, delay time.Duration, wake <-chan struct{}) error {
	if delay <= 0 {
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-wake:
			return nil
		}
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-wake:
		return nil
	case <-timer.C:
		return nil
	}
}
