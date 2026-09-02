// Package metrics publishes the worker's Prometheus instrumentation.
//
// It owns the collectors and the HTTP endpoint that exposes them. The packages
// being measured depend on small interfaces of their own instead of importing
// Prometheus, so instrumentation stays a wiring decision made in main.
package metrics

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Namespace prefixes every metric this package publishes.
const Namespace = "imageforge"

// Job outcome labels for JobsProcessed.
const (
	StatusProcessed = "processed"
	StatusFailed    = "failed"
	StatusSkipped   = "skipped"
)

// DefaultDepthInterval is how often the queue depth gauge is refreshed. The
// figure is advisory, so polling it often would cost API calls for no gain.
const DefaultDepthInterval = 15 * time.Second

// Metrics holds the worker's collectors.
//
// It is safe for concurrent use: every Prometheus collector is.
type Metrics struct {
	registry *prometheus.Registry

	jobsProcessed *prometheus.CounterVec
	jobDuration   *prometheus.HistogramVec
	queueDepth    prometheus.Gauge
	receiveErrors prometheus.Counter
}

// New builds the collectors and registers them on their own registry, so a
// scrape returns this worker's metrics plus the standard Go and process ones,
// and nothing a library registered globally behind our back.
func New() *Metrics {
	registry := prometheus.NewRegistry()

	m := &Metrics{
		registry: registry,
		jobsProcessed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Name:      "jobs_processed_total",
			Help:      "Total jobs handled by this worker, by outcome.",
		}, []string{"status"}),
		jobDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: Namespace,
			Name:      "processing_duration_seconds",
			Help:      "Time taken to process one job, by outcome.",
			// Image work runs from tens of milliseconds for a thumbnail to
			// tens of seconds for a large source, so the buckets span three
			// orders of magnitude rather than using the default web-latency
			// spread.
			Buckets: []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
		}, []string{"status"}),
		queueDepth: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: Namespace,
			Name:      "queue_depth",
			Help:      "Approximate number of jobs waiting on the queue.",
		}),
		receiveErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: Namespace,
			Name:      "queue_receive_errors_total",
			Help:      "Total failed attempts to read from the queue.",
		}),
	}

	registry.MustRegister(
		m.jobsProcessed,
		m.jobDuration,
		m.queueDepth,
		m.receiveErrors,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	// Counters that are only ever incremented on an unhappy path would
	// otherwise not appear until the first failure, which makes an alert on
	// rate() silently unevaluable. Initialize every known label.
	for _, status := range []string{StatusProcessed, StatusFailed, StatusSkipped} {
		m.jobsProcessed.WithLabelValues(status)
	}

	return m
}

// Registry returns the registry the collectors live on.
func (m *Metrics) Registry() *prometheus.Registry { return m.registry }

// JobFinished records the outcome and duration of one job. It satisfies the
// observer interface the worker pool accepts.
func (m *Metrics) JobFinished(status string, d time.Duration) {
	m.jobsProcessed.WithLabelValues(status).Inc()
	m.jobDuration.WithLabelValues(status).Observe(d.Seconds())
}

// SetQueueDepth records how many jobs are waiting.
func (m *Metrics) SetQueueDepth(depth int) { m.queueDepth.Set(float64(depth)) }

// ReceiveFailed records a failed attempt to read from the queue.
func (m *Metrics) ReceiveFailed() { m.receiveErrors.Inc() }

// Handler serves the metrics in the Prometheus text format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{
		// A scrape must not be able to take the worker down with it.
		ErrorHandling: promhttp.ContinueOnError,
	})
}

// PollDepth refreshes the queue depth gauge until ctx is done.
//
// A failing read is logged and skipped rather than retried: the next tick is
// along shortly, and a stale gauge is better than a blocked goroutine.
func (m *Metrics) PollDepth(ctx context.Context, logger *slog.Logger, depth DepthFunc, interval time.Duration) {
	if depth == nil {
		return
	}
	if interval <= 0 {
		interval = DefaultDepthInterval
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	read := func() {
		readCtx, cancel := context.WithTimeout(ctx, interval)
		defer cancel()

		n, err := depth(readCtx)
		if err != nil {
			if ctx.Err() == nil {
				logger.WarnContext(ctx, "reading the queue depth failed",
					slog.String("error", err.Error()))
			}
			return
		}
		m.SetQueueDepth(n)
	}

	// Take one reading immediately, so a scrape in the first interval does not
	// report a depth of zero that was never measured.
	read()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			read()
		}
	}
}

// DepthFunc reports how many jobs are waiting.
type DepthFunc func(ctx context.Context) (int, error)

// Server serves the metrics endpoint on its own listener.
//
// It is deliberately separate from any application port: metrics are for the
// operator, and exposing them on the same address as user traffic makes them
// hard to firewall off.
type Server struct {
	http *http.Server
}

// NewServer builds the metrics server. It serves the metrics at /metrics and a
// liveness probe at /healthz.
func NewServer(addr string, m *Metrics, logger *slog.Logger) *Server {
	mux := http.NewServeMux()
	mux.Handle("/metrics", m.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	return &Server{
		http: &http.Server{
			Addr:              addr,
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       15 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       60 * time.Second,
			ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
		},
	}
}

// Addr returns the address the server listens on.
func (s *Server) Addr() string { return s.http.Addr }

// ListenAndServe blocks until the server stops. A clean shutdown returns nil.
func (s *Server) ListenAndServe() error {
	if err := s.http.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Shutdown stops the server, letting in-flight scrapes finish.
func (s *Server) Shutdown(ctx context.Context) error {
	if err := s.http.Shutdown(ctx); err != nil {
		// A scrape that will not finish must not hold up the worker's exit.
		_ = s.http.Close()
		return err
	}
	return nil
}
