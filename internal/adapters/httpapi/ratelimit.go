package httpapi

import (
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Rate limiter defaults applied when no configuration overrides them.
const (
	// DefaultRateLimit is the sustained requests per second allowed per client.
	DefaultRateLimit = 5
	// DefaultRateBurst is how many requests a client may make back to back
	// before the sustained rate applies. Uploading a handful of images at once
	// is normal; a hundred is not.
	DefaultRateBurst = 20
	// clientTTL is how long an idle client's bucket is kept. Evicting is what
	// stops the limiter's own memory from being a way to attack the server.
	clientTTL = 10 * time.Minute
	// sweepInterval is how often idle buckets are collected.
	sweepInterval = time.Minute
)

// rateLimiter holds one token bucket per client.
//
// It is safe for concurrent use.
type rateLimiter struct {
	limit rate.Limit
	burst int
	ttl   time.Duration
	now   func() time.Time

	mu      sync.Mutex
	clients map[string]*clientBucket

	// stop closes to end the sweeper. Closing twice would panic, so Close
	// guards it with stopOnce.
	stop     chan struct{}
	stopOnce sync.Once
}

// clientBucket is one client's bucket and when it was last used.
type clientBucket struct {
	limiter *rate.Limiter
	seen    time.Time
}

// newRateLimiter starts a limiter and its sweeper.
//
// A non-positive limit or burst disables limiting entirely, which is what an
// operator gets by configuring zero.
func newRateLimiter(limit float64, burst int, now func() time.Time) *rateLimiter {
	if now == nil {
		now = time.Now
	}

	limiter := &rateLimiter{
		limit:   rate.Limit(limit),
		burst:   burst,
		ttl:     clientTTL,
		now:     now,
		clients: make(map[string]*clientBucket),
		stop:    make(chan struct{}),
	}

	if limiter.enabled() {
		go limiter.sweep()
	}
	return limiter
}

// enabled reports whether the limiter actually limits anything.
func (l *rateLimiter) enabled() bool { return l.limit > 0 && l.burst > 0 }

// allow reports whether the client may proceed, and how long to wait if not.
func (l *rateLimiter) allow(clientID string) (allowed bool, retryAfter time.Duration) {
	if !l.enabled() {
		return true, 0
	}

	now := l.now()

	l.mu.Lock()
	bucket, ok := l.clients[clientID]
	if !ok {
		bucket = &clientBucket{limiter: rate.NewLimiter(l.limit, l.burst)}
		l.clients[clientID] = bucket
	}
	bucket.seen = now
	l.mu.Unlock()

	reservation := bucket.limiter.ReserveN(now, 1)
	if !reservation.OK() {
		// Only possible when burst is zero, which enabled() has excluded.
		return false, time.Second
	}

	delay := reservation.DelayFrom(now)
	if delay == 0 {
		return true, 0
	}

	// The request is refused, so put the token back rather than holding a
	// reservation for a request that will never be made.
	reservation.CancelAt(now)
	return false, delay
}

// sweep drops buckets for clients that have gone quiet.
func (l *rateLimiter) sweep() {
	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-l.stop:
			return
		case <-ticker.C:
			cutoff := l.now().Add(-l.ttl)

			l.mu.Lock()
			for id, bucket := range l.clients {
				if bucket.seen.Before(cutoff) {
					delete(l.clients, id)
				}
			}
			l.mu.Unlock()
		}
	}
}

// tracked reports how many clients have buckets. It is for tests and
// diagnostics.
func (l *rateLimiter) tracked() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.clients)
}

// Close stops the sweeper. Calling it more than once is safe.
func (l *rateLimiter) Close() {
	l.stopOnce.Do(func() { close(l.stop) })
}

// rateLimit refuses a client that is over its budget.
//
// It is mounted inside requireAuth, so the client id is the authenticated
// subject rather than anything the caller controls. Keying on a header or an
// address a client can change would make the limit trivial to sidestep.
func (a *API) rateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clientID, ok := ClientID(r.Context())
		if !ok {
			// Unreachable behind requireAuth, but failing closed is the only
			// safe answer if this is ever mounted on its own.
			writeProblem(w, r, http.StatusUnauthorized, TypeUnauthorized,
				"Unauthorized", "This endpoint requires a bearer token.")
			return
		}

		allowed, retryAfter := a.limiter.allow(clientID)
		if allowed {
			next.ServeHTTP(w, r)
			return
		}

		seconds := int(math.Ceil(retryAfter.Seconds()))
		if seconds < 1 {
			seconds = 1
		}

		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		a.cfg.Logger.WarnContext(r.Context(), "rate limited a client",
			slogPath(r),
			slogClient(clientID),
			slog.Int("retry_after_seconds", seconds))

		writeProblemWith(w, r, Problem{
			Type:   TypeRateLimited,
			Title:  "Too Many Requests",
			Status: http.StatusTooManyRequests,
			Detail: fmt.Sprintf(
				"This client is over its budget of %.0f requests per second. Retry in %d second(s).",
				float64(a.limiter.limit), seconds),
			RetryAfter: seconds,
		})
	})
}
