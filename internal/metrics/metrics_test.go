package metrics

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// scrape renders the current metrics as Prometheus text.
func scrape(t *testing.T, m *Metrics) string {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody)
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	return rec.Body.String()
}

// TestJobFinished checks each outcome lands on its own counter and histogram.
func TestJobFinished(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status string
		want   string
	}{
		{
			name: "processed", status: StatusProcessed,
			want: `imageforge_jobs_processed_total{status="processed"} 1`,
		},
		{
			name: "failed", status: StatusFailed,
			want: `imageforge_jobs_processed_total{status="failed"} 1`,
		},
		{
			name: "skipped", status: StatusSkipped,
			want: `imageforge_jobs_processed_total{status="skipped"} 1`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			m := New()
			m.JobFinished(tt.status, 250*time.Millisecond)

			body := scrape(t, m)
			assert.Contains(t, body, tt.want)
			assert.Contains(t, body,
				fmt.Sprintf(`imageforge_processing_duration_seconds_count{status=%q} 1`, tt.status))
			assert.Contains(t, body,
				fmt.Sprintf(`imageforge_processing_duration_seconds_sum{status=%q} 0.25`, tt.status))
		})
	}
}

// TestCountersStartAtZero guards a real alerting trap: a counter that only
// appears after the first failure makes rate() on it silently unevaluable.
func TestCountersStartAtZero(t *testing.T) {
	t.Parallel()

	body := scrape(t, New())

	for _, status := range []string{StatusProcessed, StatusFailed, StatusSkipped} {
		assert.Containsf(t, body,
			fmt.Sprintf(`imageforge_jobs_processed_total{status=%q} 0`, status),
			"%s must be published before it is ever incremented", status)
	}
}

// TestDurationHistogramBuckets checks an observation lands in the buckets that
// span it, and none below.
func TestDurationHistogramBuckets(t *testing.T) {
	t.Parallel()

	m := New()
	m.JobFinished(StatusProcessed, 300*time.Millisecond)

	body := scrape(t, m)

	assert.Contains(t, body,
		`imageforge_processing_duration_seconds_bucket{status="processed",le="0.25"} 0`,
		"an observation must not count in a bucket below it")
	assert.Contains(t, body,
		`imageforge_processing_duration_seconds_bucket{status="processed",le="0.5"} 1`)
	assert.Contains(t, body,
		`imageforge_processing_duration_seconds_bucket{status="processed",le="+Inf"} 1`)
}

func TestQueueDepthGauge(t *testing.T) {
	t.Parallel()

	m := New()
	assert.Contains(t, scrape(t, m), "imageforge_queue_depth 0")

	m.SetQueueDepth(17)
	assert.Contains(t, scrape(t, m), "imageforge_queue_depth 17")

	// A gauge falls as well as rises.
	m.SetQueueDepth(3)
	assert.Contains(t, scrape(t, m), "imageforge_queue_depth 3")
}

func TestReceiveFailed(t *testing.T) {
	t.Parallel()

	m := New()
	assert.Contains(t, scrape(t, m), "imageforge_queue_receive_errors_total 0")

	m.ReceiveFailed()
	m.ReceiveFailed()
	assert.Contains(t, scrape(t, m), "imageforge_queue_receive_errors_total 2")
}

// TestScrapeIncludesRuntimeMetrics checks the Go and process collectors are
// registered, since those are what make a worker's memory and CPU visible.
func TestScrapeIncludesRuntimeMetrics(t *testing.T) {
	t.Parallel()

	body := scrape(t, New())

	assert.Contains(t, body, "go_goroutines")
	assert.Contains(t, body, "go_memstats_alloc_bytes")
}

func TestPollDepth(t *testing.T) {
	t.Parallel()

	t.Run("takes a reading immediately", func(t *testing.T) {
		t.Parallel()

		m := New()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		done := make(chan struct{})
		go func() {
			defer close(done)
			// A long interval proves the first reading is not waiting for a tick.
			m.PollDepth(ctx, discardLogger(), func(context.Context) (int, error) {
				return 9, nil
			}, time.Hour)
		}()

		assert.Eventually(t, func() bool {
			return strings.Contains(scrape(t, m), "imageforge_queue_depth 9")
		}, 30*time.Second, 10*time.Millisecond)

		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatal("PollDepth did not stop when its context was canceled")
		}
	})

	t.Run("keeps polling and follows the depth", func(t *testing.T) {
		t.Parallel()

		var reading atomic.Int64
		reading.Store(5)

		m := New()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		go m.PollDepth(ctx, discardLogger(), func(context.Context) (int, error) {
			return int(reading.Load()), nil
		}, 5*time.Millisecond)

		assert.Eventually(t, func() bool {
			return strings.Contains(scrape(t, m), "imageforge_queue_depth 5")
		}, 30*time.Second, 5*time.Millisecond)

		reading.Store(12)
		assert.Eventually(t, func() bool {
			return strings.Contains(scrape(t, m), "imageforge_queue_depth 12")
		}, 30*time.Second, 5*time.Millisecond)
	})

	t.Run("a failing read leaves the last value and keeps going", func(t *testing.T) {
		t.Parallel()

		var calls atomic.Int64

		m := New()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		go m.PollDepth(ctx, discardLogger(), func(context.Context) (int, error) {
			// Succeed once, then fail forever.
			if calls.Add(1) == 1 {
				return 7, nil
			}
			return 0, errors.New("queue unreachable")
		}, 5*time.Millisecond)

		assert.Eventually(t, func() bool {
			return calls.Load() > 3
		}, 30*time.Second, 5*time.Millisecond)

		assert.Contains(t, scrape(t, m), "imageforge_queue_depth 7",
			"a failed read must not reset the gauge to zero")
	})

	t.Run("a nil reader is a no-op", func(t *testing.T) {
		t.Parallel()

		done := make(chan struct{})
		go func() {
			defer close(done)
			New().PollDepth(context.Background(), discardLogger(), nil, time.Millisecond)
		}()

		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatal("PollDepth blocked on a nil reader")
		}
	})
}

// TestServerServesMetrics checks the endpoint really is reachable over HTTP on
// its own listener, not just through the handler.
func TestServerServesMetrics(t *testing.T) {
	t.Parallel()

	m := New()
	m.JobFinished(StatusProcessed, time.Second)

	server, addr, _ := startServer(t, m)
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })

	body, status := get(t, "http://"+addr+"/metrics")
	assert.Equal(t, http.StatusOK, status)
	assert.Contains(t, body, `imageforge_jobs_processed_total{status="processed"} 1`)

	body, status = get(t, "http://"+addr+"/healthz")
	assert.Equal(t, http.StatusOK, status)
	assert.Contains(t, body, `"status":"ok"`)
}

// TestServerShutdownIsGraceful covers the shutdown contract: ListenAndServe
// returns nil rather than an error, and the listener is released so the port
// can be bound again.
func TestServerShutdownIsGraceful(t *testing.T) {
	t.Parallel()

	server, addr, serveErr := startServer(t, New())

	// The server is serving before it is asked to stop.
	_, status := get(t, "http://"+addr+"/metrics")
	require.Equal(t, http.StatusOK, status)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, server.Shutdown(ctx))

	select {
	case err := <-serveErr:
		require.NoError(t, err, "a clean shutdown is not an error")
	case <-time.After(30 * time.Second):
		t.Fatal("ListenAndServe did not return after Shutdown")
	}

	// The port is free again, which is what proves the listener was released.
	listener, err := net.Listen("tcp", addr)
	require.NoError(t, err, "the address must be reusable after shutdown")
	require.NoError(t, listener.Close())
}

// TestServerShutdownWithoutServing checks shutting down a server that was never
// started is harmless, which is the path taken when startup fails early.
func TestServerShutdownWithoutServing(t *testing.T) {
	t.Parallel()

	server := NewServer("127.0.0.1:0", New(), discardLogger())
	assert.NoError(t, server.Shutdown(context.Background()))
}

// startServer builds a server bound to a free port and returns its address.
//
// The listener is opened here rather than by ListenAndServe so the test knows
// the port without racing the goroutine that serves on it.
func startServer(t *testing.T, m *Metrics) (server *Server, addr string, serveErr <-chan error) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr = listener.Addr().String()
	require.NoError(t, listener.Close())

	server = NewServer(addr, m, discardLogger())
	errs := make(chan error, 1)
	go func() { errs <- server.ListenAndServe() }()

	// Wait for the port to accept before the test relies on it.
	require.Eventually(t, func() bool {
		conn, dialErr := net.DialTimeout("tcp", addr, time.Second)
		if dialErr != nil {
			return false
		}
		_ = conn.Close()
		return true
	}, 30*time.Second, 10*time.Millisecond, "the metrics server never came up")

	return server, addr, errs
}

// get fetches a URL and returns its body and status.
func get(t *testing.T, url string) (body string, status int) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, http.NoBody)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	content, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(content), resp.StatusCode
}

func TestNewServerAddr(t *testing.T) {
	t.Parallel()
	assert.Equal(t, ":9090", NewServer(":9090", New(), discardLogger()).Addr())
}
