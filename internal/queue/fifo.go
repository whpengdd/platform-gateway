package queue

import (
	"context"
	"sync"
	"time"

	"platform-gateway/internal/backend"
)

// FIFO is a per-backend in-memory queue: max N in flight, extra waiters, then 503.
type FIFO struct {
	name        string
	maxInFlight int
	queueSize   int
	waitTimeout time.Duration

	mu       sync.Mutex
	inFlight int
	waiters  []*waiter
}

type waiter struct {
	ready chan struct{}
}

func New(name string, maxInFlight, queueSize int, waitTimeout time.Duration) *FIFO {
	if maxInFlight < 1 {
		maxInFlight = 1
	}
	if queueSize < 0 {
		queueSize = 0
	}
	if waitTimeout <= 0 {
		waitTimeout = 30 * time.Second
	}
	return &FIFO{
		name:        name,
		maxInFlight: maxInFlight,
		queueSize:   queueSize,
		waitTimeout: waitTimeout,
	}
}

func (q *FIFO) Name() string { return q.name }

func (q *FIFO) Snapshot() (inFlight, waiters int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.inFlight, len(q.waiters)
}

func (q *FIFO) WaitTimeout() time.Duration { return q.waitTimeout }

func (q *FIFO) Run(ctx context.Context, op backend.Operation) error {
	if err := q.acquire(ctx); err != nil {
		return err
	}
	defer q.release()
	if err := ctx.Err(); err != nil {
		return err
	}
	return op(ctx)
}

func (q *FIFO) acquire(ctx context.Context) error {
	q.mu.Lock()
	if q.inFlight < q.maxInFlight {
		q.inFlight++
		q.mu.Unlock()
		return nil
	}
	if len(q.waiters) >= q.queueSize {
		retry := q.waitTimeout
		q.mu.Unlock()
		return &backend.BusyError{RetryAfter: retry}
	}
	w := &waiter{ready: make(chan struct{})}
	q.waiters = append(q.waiters, w)
	q.mu.Unlock()

	timer := time.NewTimer(q.waitTimeout)
	defer timer.Stop()
	select {
	case <-w.ready:
		return nil
	case <-timer.C:
		if q.cancelWait(w) {
			return &backend.WaitTimeoutError{}
		}
		<-w.ready
		return nil
	case <-ctx.Done():
		if q.cancelWait(w) {
			return ctx.Err()
		}
		<-w.ready
		return nil
	}
}

func (q *FIFO) cancelWait(w *waiter) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, x := range q.waiters {
		if x == w {
			q.waiters = append(q.waiters[:i], q.waiters[i+1:]...)
			return true
		}
	}
	return false
}

func (q *FIFO) release() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.waiters) > 0 {
		w := q.waiters[0]
		q.waiters = q.waiters[1:]
		close(w.ready)
		return
	}
	q.inFlight--
}
