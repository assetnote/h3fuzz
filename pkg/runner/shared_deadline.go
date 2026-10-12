package runner

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// sharedDeadlineManager amortizes request timeout timers across a small number
// of deadline buckets. A net/http Client timeout creates and stops one timer per
// request, which is measurable overhead at HTTP/3 fuzzing rates. Requests that
// start in the same quantum share one standard library deadline context and its
// timer instead.
//
// The deadline is rounded up. Every request therefore receives at least the
// configured timeout and at most one additional quantum of grace.
type sharedDeadlineManager struct {
	root    context.Context
	cancel  context.CancelFunc
	timeout time.Duration
	quantum time.Duration

	current atomic.Pointer[sharedDeadlineBucket]
	mu      sync.Mutex
	once    sync.Once
}

type sharedDeadlineBucket struct {
	ctx      context.Context
	deadline time.Time
	// Retaining cancel documents that the deadline resource intentionally lives
	// until expiry. Root cancellation still releases every live bucket when the
	// runner closes.
	cancel context.CancelFunc
}

func newSharedDeadlineManager(parent context.Context, timeout time.Duration) *sharedDeadlineManager {
	if parent == nil {
		parent = context.Background()
	}
	if timeout <= 0 {
		return nil
	}
	root, cancel := context.WithCancel(parent)
	return &sharedDeadlineManager{
		root:    root,
		cancel:  cancel,
		timeout: timeout,
		quantum: 50 * time.Millisecond,
	}
}

// context returns the current bucket context. The atomic fast path avoids a
// shared lock for almost every request. A mutex and double-check are needed only
// once per quantum when the current bucket has less than a full timeout left.
func (m *sharedDeadlineManager) context() context.Context {
	if m == nil {
		return context.Background()
	}
	now := time.Now()
	if bucket := m.current.Load(); bucket != nil && bucket.deadline.Sub(now) >= m.timeout {
		return bucket.ctx
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	now = time.Now()
	if bucket := m.current.Load(); bucket != nil && bucket.deadline.Sub(now) >= m.timeout {
		return bucket.ctx
	}

	deadline := now.Truncate(m.quantum).Add(m.timeout + m.quantum)
	ctx, cancel := context.WithDeadline(m.root, deadline)
	bucket := &sharedDeadlineBucket{ctx: ctx, deadline: deadline, cancel: cancel}
	m.current.Store(bucket)
	return ctx
}

func (m *sharedDeadlineManager) close() {
	if m == nil {
		return
	}
	m.once.Do(m.cancel)
}
