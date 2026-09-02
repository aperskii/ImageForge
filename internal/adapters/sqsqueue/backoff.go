package sqsqueue

import (
	"context"
	"math"
	"math/rand/v2"
	"time"
)

// Backoff defaults applied when no option overrides them.
const (
	// DefaultBackoffBase is the delay after a first failed receive.
	DefaultBackoffBase = 250 * time.Millisecond
	// DefaultBackoffMax caps the delay. It is deliberately shorter than a
	// long-poll wait: a queue that has come back should be noticed promptly.
	DefaultBackoffMax = 30 * time.Second
)

// backoff computes the delay between failed receives.
//
// The delay doubles per consecutive failure up to a ceiling, with full jitter:
// the actual wait is drawn uniformly from [0, capped), so a fleet of workers
// that all lost the queue at the same moment does not come back in lockstep and
// stampede it.
type backoff struct {
	base    time.Duration
	max     time.Duration
	attempt int

	// random draws the jitter. It is a field so tests can make the delay
	// deterministic.
	random func(int64) int64
}

// newBackoff returns a backoff over the given bounds.
func newBackoff(base, maxDelay time.Duration) *backoff {
	return &backoff{base: base, max: maxDelay, random: rand.Int64N}
}

// next records a failure and returns how long to wait before retrying.
func (b *backoff) next() time.Duration {
	// Cap the shift before it is applied: 1<<63 overflows a Duration, and an
	// attempt counter on a queue that is down for hours will get there.
	shift := min(b.attempt, 32)
	capped := time.Duration(math.Min(
		float64(b.base)*math.Pow(2, float64(shift)),
		float64(b.max),
	))
	b.attempt++

	if capped <= 0 {
		return 0
	}
	return time.Duration(b.random(int64(capped)))
}

// reset clears the failure count after a successful receive.
func (b *backoff) reset() { b.attempt = 0 }

// attempts reports how many consecutive failures have been recorded.
func (b *backoff) attempts() int { return b.attempt }

// wait sleeps for d, or returns false as soon as ctx is done.
func wait(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
