// Package memqueue implements the ports.Queue port with an in-process buffered
// channel.
//
// It lets the API and worker run with no external broker, for development and
// tests. Nothing is persisted: jobs still in the queue are lost when the
// process exits.
package memqueue

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"imageforge/internal/ports"
)

// Compile-time assertion that Queue satisfies the port.
var (
	_ ports.Queue         = (*Queue)(nil)
	_ ports.DepthReporter = (*Queue)(nil)
)

// ErrClosed is returned when enqueueing onto a closed queue.
var ErrClosed = errors.New("queue is closed")

// DefaultBuffer is the queue depth used when none is given.
const DefaultBuffer = 1024

// Queue transports job identifiers over a buffered channel.
//
// It is safe for concurrent use by any number of producers and consumers.
type Queue struct {
	ch chan string

	mu     sync.RWMutex
	closed bool
}

// New returns a Queue holding up to buffer identifiers before Enqueue blocks.
// A non-positive buffer selects DefaultBuffer.
func New(buffer int) *Queue {
	if buffer <= 0 {
		buffer = DefaultBuffer
	}
	return &Queue{ch: make(chan string, buffer)}
}

// Enqueue publishes jobID for processing.
//
// It blocks while the queue is full, and gives up when ctx is done, so a
// backlog surfaces as a request timeout rather than a silent drop.
func (q *Queue) Enqueue(ctx context.Context, jobID string) error {
	if jobID == "" {
		return errors.New("memqueue: empty job id")
	}

	q.mu.RLock()
	defer q.mu.RUnlock()
	if q.closed {
		return fmt.Errorf("memqueue: enqueue %s: %w", jobID, ErrClosed)
	}

	select {
	case q.ch <- jobID:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("memqueue: enqueue %s: %w", jobID, ctx.Err())
	}
}

// Consume returns a channel of job identifiers.
//
// The returned channel is closed when ctx is done or the queue is closed. Each
// call starts an independent consumer; identifiers are delivered to exactly one
// of them, so several consumers share the work rather than each seeing every
// job.
func (q *Queue) Consume(ctx context.Context) (<-chan string, error) {
	out := make(chan string)

	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case jobID, ok := <-q.ch:
				if !ok {
					return
				}
				select {
				case out <- jobID:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return out, nil
}

// Len reports how many identifiers are waiting. It is intended for tests and
// diagnostics.
func (q *Queue) Len() int { return len(q.ch) }

// Close stops the queue. Consumers drain what is already buffered and then see
// their channel closed. Calling Close more than once is safe.
func (q *Queue) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	q.closed = true
	close(q.ch)
}

// Depth reports how many identifiers are waiting.
//
// Unlike a distributed broker's approximation this figure is exact, because the
// whole queue is one buffered channel in this process.
func (q *Queue) Depth(_ context.Context) (int, error) { return len(q.ch), nil }
