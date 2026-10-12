package runner

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestHTTP3ReconnectGateExactConcurrentDialLimit(t *testing.T) {
	tests := []struct {
		desired int
		want    int
	}{
		{desired: 1, want: 1},
		{desired: 4, want: 1},
		{desired: 5, want: 2},
		{desired: 8, want: 2},
		{desired: 10, want: 3},
		{desired: 13, want: 4},
		{desired: 64, want: 4},
	}
	for _, test := range tests {
		gate := newHTTP3ReconnectGate(test.desired, nil)
		if got := gate.snapshot().DialLimit; got != test.want {
			t.Errorf("desired %d: dial limit = %d, want %d", test.desired, got, test.want)
		}
	}

	gate := newHTTP3ReconnectGate(10, nil)
	permits := make([]*http3ReconnectPermit, 0, 3)
	for range 3 {
		permit, err := gate.acquire(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		permits = append(permits, permit)
	}
	if snapshot := gate.snapshot(); snapshot.InFlight != 3 || snapshot.DialLimit != 3 {
		t.Fatalf("full gate snapshot = %+v, want in-flight=limit=3", snapshot)
	}

	fourth := make(chan *http3ReconnectPermit, 1)
	fourthErr := make(chan error, 1)
	go func() {
		permit, err := gate.acquire(context.Background())
		if err != nil {
			fourthErr <- err
			return
		}
		fourth <- permit
	}()
	select {
	case permit := <-fourth:
		permit.finish(true)
		t.Fatal("fourth replacement dial passed a limit of three")
	case err := <-fourthErr:
		t.Fatalf("fourth replacement dial failed while waiting: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	permits[0].finish(true)
	select {
	case permit := <-fourth:
		permit.finish(true)
	case err := <-fourthErr:
		t.Fatalf("fourth replacement dial failed after release: %v", err)
	case <-time.After(time.Second):
		t.Fatal("fourth replacement dial did not start after a permit was released")
	}
	for _, permit := range permits[1:] {
		permit.finish(true)
	}
	if got := gate.snapshot().InFlight; got != 0 {
		t.Fatalf("replacement permits left in-flight=%d, want 0", got)
	}
}

func TestHTTP3ReconnectGateBroadcastsCoalescedCapacity(t *testing.T) {
	waitEntered := make(chan struct{}, 4)
	releaseWaiters := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	released := false
	defer func() {
		if !released {
			close(releaseWaiters)
		}
	}()
	gate := newHTTP3ReconnectGate(13, &http3ReconnectGateOptions{
		wait: func(ctx context.Context, _ time.Duration, wake <-chan struct{}) error {
			waitEntered <- struct{}{}
			select {
			case <-ctx.Done():
				return context.Cause(ctx)
			case <-releaseWaiters:
			}
			select {
			case <-ctx.Done():
				return context.Cause(ctx)
			case <-wake:
				return nil
			}
		},
	})
	occupied := make([]*http3ReconnectPermit, 0, 4)
	for range 4 {
		permit, err := gate.acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		occupied = append(occupied, permit)
	}
	granted := make(chan *http3ReconnectPermit, 4)
	for range 4 {
		go func() {
			permit, err := gate.acquire(ctx)
			if err == nil {
				granted <- permit
			}
		}()
	}
	for range 4 {
		select {
		case <-waitEntered:
		case <-time.After(time.Second):
			t.Fatal("replacement waiter did not reach the coalescing barrier")
		}
	}
	for _, permit := range occupied {
		permit.finish(true)
	}
	close(releaseWaiters)
	released = true
	acquired := make([]*http3ReconnectPermit, 0, 4)
	for range 4 {
		select {
		case permit := <-granted:
			acquired = append(acquired, permit)
		case <-time.After(time.Second):
			t.Fatalf("only %d of 4 waiters observed released capacity", len(acquired))
		}
	}
	for _, permit := range acquired {
		permit.finish(true)
	}
}

func TestHTTP3ReconnectGateTotalOriginFailureBacksOffWithoutStorm(t *testing.T) {
	clock := &http3ReconnectFakeClock{current: time.Unix(1_700_000_000, 0)}
	gate := newHTTP3ReconnectGate(64, &http3ReconnectGateOptions{
		now:    clock.now,
		wait:   clock.wait,
		random: func() float64 { return 0.5 },
	})
	wantBackoff := []time.Duration{
		250 * time.Millisecond,
		500 * time.Millisecond,
		time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		10 * time.Second,
		10 * time.Second,
	}
	for index, want := range wantBackoff {
		permit, err := gate.acquire(context.Background())
		if err != nil {
			t.Fatalf("failure %d acquire: %v", index+1, err)
		}
		permit.finish(false)
		snapshot := gate.snapshot()
		if snapshot.DialLimit != 4 || snapshot.InFlight != 0 {
			t.Fatalf("failure %d snapshot = %+v, want limit 4 and no active dial", index+1, snapshot)
		}
		if got := snapshot.NotBefore.Sub(clock.now()); got != want {
			t.Fatalf("failure %d backoff = %s, want %s", index+1, got, want)
		}
	}
	if waits := clock.waitsSnapshot(); len(waits) != len(wantBackoff)-1 {
		t.Fatalf("failed reconnect waits = %v, want %d waits", waits, len(wantBackoff)-1)
	} else if want := wantBackoff[:len(wantBackoff)-1]; !reflect.DeepEqual(waits, want) {
		t.Fatalf("failed reconnect waits = %v, want exact %v", waits, want)
	}

	if got := http3ReconnectBackoff(1, 0); got != 200*time.Millisecond {
		t.Fatalf("minimum jitter = %s, want 200ms", got)
	}
	if got := http3ReconnectBackoff(1, 1); got != 300*time.Millisecond {
		t.Fatalf("maximum jitter = %s, want 300ms", got)
	}
	if got := http3ReconnectBackoff(20, 1); got != 10*time.Second {
		t.Fatalf("maximum capped backoff = %s, want 10s", got)
	}
}

func TestHTTP3ReconnectGateWaitHonorsCallerCancellation(t *testing.T) {
	gate := newHTTP3ReconnectGate(1, nil)
	permit, err := gate.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, acquireErr := gate.acquire(ctx)
		result <- acquireErr
	}()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled replacement wait error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled replacement wait did not return")
	}
	permit.finish(true)
}

type http3ReconnectFakeClock struct {
	mu      sync.Mutex
	current time.Time
	waits   []time.Duration
}

func (c *http3ReconnectFakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current
}

func (c *http3ReconnectFakeClock) wait(ctx context.Context, delay time.Duration, _ <-chan struct{}) error {
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	default:
	}
	c.mu.Lock()
	c.waits = append(c.waits, delay)
	c.current = c.current.Add(delay)
	c.mu.Unlock()
	return nil
}

func (c *http3ReconnectFakeClock) waitRespectingWake(ctx context.Context, delay time.Duration, wake <-chan struct{}) error {
	if delay <= 0 {
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-wake:
			return nil
		}
	}
	return c.wait(ctx, delay, wake)
}

func (c *http3ReconnectFakeClock) waitsSnapshot() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.waits...)
}
