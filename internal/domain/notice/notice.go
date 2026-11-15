// Package notice delivers the service's own alerts ("the modem is
// unreachable", "the heartbeat is missing") and keeps the ones that could not
// be delivered, so an alert raised while Telegram is unreachable still
// arrives once it is back.
package notice

import (
	"context"
	"sync"
)

// DefaultMax is how many undelivered notices are kept; past it the oldest go.
const DefaultMax = 20

// Queue holds undelivered notices in order.
type Queue struct {
	send  func(ctx context.Context, text string) error
	limit int

	mu      sync.Mutex
	pending []string
}

// New returns a queue that delivers through send and keeps at most limit
// undelivered notices.
func New(send func(ctx context.Context, text string) error, limit int) *Queue {
	return &Queue{send: send, limit: limit, mu: sync.Mutex{}, pending: nil}
}

// Post queues text and tries to deliver everything queued.
func (q *Queue) Post(ctx context.Context, text string) error {
	q.mu.Lock()

	q.pending = append(q.pending, text)
	if over := len(q.pending) - q.limit; over > 0 {
		q.pending = q.pending[over:]
	}

	q.mu.Unlock()

	return q.Flush(ctx)
}

// Flush delivers the queued notices oldest first and stops at the first that
// fails, which stays queued with everything behind it.
func (q *Queue) Flush(ctx context.Context) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	for len(q.pending) > 0 {
		if err := q.send(ctx, q.pending[0]); err != nil {
			return err
		}

		q.pending = q.pending[1:]
	}

	return nil
}

// Len is how many notices wait to be delivered.
func (q *Queue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()

	return len(q.pending)
}
