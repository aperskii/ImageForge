// Package worker runs image transformation jobs off the queue.
//
// A Pool holds a fixed number of goroutines, each pulling job identifiers from
// the ports.Queue and handing them to the ProcessJob use case. The pool owns
// concurrency and lifecycle only; every decision about what processing a job
// means belongs to the use case.
package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"imageforge/internal/ports"
	"imageforge/internal/telemetry"
	"imageforge/internal/usecase"
)

// ErrShutdownTimeout is returned by Run when jobs were still in flight after
// the shutdown timeout elapsed. The pool cancels them and unwinds, so the
// process can exit, but their outcome is unknown.
var ErrShutdownTimeout = errors.New("worker: timed out waiting for in-flight jobs")

// Defaults applied when no option overrides them.
const (
	// DefaultSize is the number of concurrent workers.
	DefaultSize = 4
	// DefaultShutdownTimeout bounds how long Run waits for in-flight jobs
	// after its context is canceled.
	DefaultShutdownTimeout = 30 * time.Second
	// DefaultJobTimeout bounds a single job. Image work is CPU-bound and
	// finite; a job that exceeds this is stuck rather than slow.
	DefaultJobTimeout = 2 * time.Minute
	// settleTimeout bounds the call that acknowledges a delivery, so a wedged
	// broker cannot hold a worker after its job is done.
	settleTimeout = 10 * time.Second
)

// Stats counts the outcomes a pool has seen since it started.
type Stats struct {
	// Processed counts jobs that reached a completed state.
	Processed uint64
	// Failed counts jobs the use case reported an error for.
	Failed uint64
	// Skipped counts deliveries for jobs that were not pending, which is the
	// normal outcome of a duplicate delivery from an at-least-once queue.
	Skipped uint64
}

// Pool runs jobs from a queue across a fixed number of goroutines.
type Pool struct {
	queue   ports.Queue
	process *usecase.ProcessJob

	size            int
	logger          *slog.Logger
	observer        Observer
	shutdownTimeout time.Duration
	jobTimeout      time.Duration

	processed atomic.Uint64
	failed    atomic.Uint64
	skipped   atomic.Uint64
}

// Observer receives the outcome of every job the pool handles.
//
// It is an interface, rather than a metrics client, so this package can be
// instrumented without depending on whichever one is in use. Status is one of
// the StatusX constants below.
type Observer interface {
	JobFinished(status string, d time.Duration)
}

// The outcomes reported to an Observer.
const (
	StatusProcessed = "processed"
	StatusFailed    = "failed"
	StatusSkipped   = "skipped"
)

// outcomeOf names what became of a job, for the span and the observer.
func outcomeOf(err error) string {
	switch {
	case errors.Is(err, usecase.ErrJobNotPending):
		return StatusSkipped
	case err != nil:
		return StatusFailed
	default:
		return StatusProcessed
	}
}

// nopObserver is the default, so the pool never has to check for nil.
type nopObserver struct{}

func (nopObserver) JobFinished(string, time.Duration) {}

// Option overrides a Pool setting.
type Option func(*Pool)

// WithSize sets the number of concurrent workers. A non-positive size leaves
// the default in place.
func WithSize(size int) Option {
	return func(p *Pool) {
		if size > 0 {
			p.size = size
		}
	}
}

// WithLogger sets the logger. A nil logger leaves the default in place.
func WithLogger(logger *slog.Logger) Option {
	return func(p *Pool) {
		if logger != nil {
			p.logger = logger
		}
	}
}

// WithShutdownTimeout bounds how long Run waits for in-flight jobs once its
// context is canceled. A non-positive duration leaves the default in place.
func WithShutdownTimeout(d time.Duration) Option {
	return func(p *Pool) {
		if d > 0 {
			p.shutdownTimeout = d
		}
	}
}

// WithObserver installs an observer for job outcomes. A nil observer leaves the
// default no-op in place.
func WithObserver(o Observer) Option {
	return func(p *Pool) {
		if o != nil {
			p.observer = o
		}
	}
}

// WithJobTimeout bounds a single job. A non-positive duration removes the
// bound, letting a job run until the pool shuts down.
func WithJobTimeout(d time.Duration) Option {
	return func(p *Pool) { p.jobTimeout = d }
}

// New wires a pool to its queue and use case.
func New(queue ports.Queue, process *usecase.ProcessJob, opts ...Option) *Pool {
	pool := &Pool{
		queue:           queue,
		process:         process,
		size:            DefaultSize,
		logger:          slog.Default(),
		observer:        nopObserver{},
		shutdownTimeout: DefaultShutdownTimeout,
		jobTimeout:      DefaultJobTimeout,
	}
	for _, opt := range opts {
		opt(pool)
	}
	return pool
}

// Size reports how many workers the pool runs.
func (p *Pool) Size() int { return p.size }

// Stats returns a snapshot of the outcomes seen so far. The three counters are
// read independently, so a snapshot taken while jobs are running may not be
// internally consistent.
func (p *Pool) Stats() Stats {
	return Stats{
		Processed: p.processed.Load(),
		Failed:    p.failed.Load(),
		Skipped:   p.skipped.Load(),
	}
}

// Run starts the workers and blocks until the queue closes or ctx is canceled.
//
// Canceling ctx stops the pool taking on new work. Jobs already in flight keep
// running: they are given a context derived from ctx with the cancellation
// removed, so a shutdown does not abort work that is nearly done. Run then
// waits up to the shutdown timeout for them, and returns ErrShutdownTimeout if
// they outlast it, having canceled them.
//
// That final wait relies on the adapters behind the ports honoring context
// cancellation, which is the contract every port method's ctx argument states.
//
// Run is not reentrant: use one Run per Pool.
func (p *Pool) Run(ctx context.Context) error {
	deliveries, err := p.consume(ctx)
	if err != nil {
		return fmt.Errorf("worker: consume: %w", err)
	}

	// Jobs must be able to outlive the cancellation of ctx, or waiting for
	// them on shutdown would be pointless. They keep its values, and gain a
	// cancellation the pool controls itself.
	jobCtx, abandonJobs := context.WithCancel(context.WithoutCancel(ctx))
	defer abandonJobs()

	var wg sync.WaitGroup
	wg.Add(p.size)
	for i := range p.size {
		go func(id int) {
			defer wg.Done()
			p.work(ctx, jobCtx, id, deliveries)
		}(i)
	}

	p.logger.InfoContext(ctx, "worker pool started", slog.Int("workers", p.size))

	stopped := make(chan struct{})
	go func() {
		wg.Wait()
		close(stopped)
	}()

	select {
	case <-stopped:
		// The queue closed and every worker drained it.
		p.logger.InfoContext(ctx, "worker pool stopped", slog.Any("stats", p.Stats()))
		return nil
	case <-ctx.Done():
	}

	p.logger.InfoContext(ctx, "worker pool draining in-flight jobs",
		slog.Duration("timeout", p.shutdownTimeout))

	timer := time.NewTimer(p.shutdownTimeout)
	defer timer.Stop()

	select {
	case <-stopped:
		p.logger.InfoContext(ctx, "worker pool stopped cleanly", slog.Any("stats", p.Stats()))
		return nil
	case <-timer.C:
		abandonJobs()
		<-stopped
		p.logger.ErrorContext(ctx, "worker pool abandoned in-flight jobs",
			slog.Duration("timeout", p.shutdownTimeout),
			slog.Any("stats", p.Stats()))
		return ErrShutdownTimeout
	}
}

// consume opens the queue, preferring the delivery form when the queue offers
// it.
//
// A queue that implements ports.DeliveryConsumer hands over the trace context
// that traveled with each job. One that does not still works: its jobs simply
// arrive with no metadata and are traced from here on rather than from the
// upload that created them.
func (p *Pool) consume(ctx context.Context) (<-chan ports.Delivery, error) {
	if consumer, ok := p.queue.(ports.DeliveryConsumer); ok {
		return consumer.ConsumeDeliveries(ctx)
	}

	ids, err := p.queue.Consume(ctx)
	if err != nil {
		return nil, err
	}

	out := make(chan ports.Delivery)
	go func() {
		defer close(out)
		for jobID := range ids {
			select {
			case out <- ports.Delivery{JobID: jobID}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// work pulls deliveries until the queue closes or lifeCtx is canceled.
//
// lifeCtx decides whether to take on more work; jobCtx is what a job runs under
// and outlives lifeCtx by design.
func (p *Pool) work(lifeCtx, jobCtx context.Context, workerID int, deliveries <-chan ports.Delivery) {
	for {
		select {
		case <-lifeCtx.Done():
			return
		case delivery, ok := <-deliveries:
			if !ok {
				return
			}
			p.handle(jobCtx, workerID, delivery)
		}
	}
}

// handle runs one job and records its outcome.
//
// A failing job is logged and counted, never propagated: one bad image must not
// stop the pool. The use case has already recorded the failure against the job
// itself, so the state a client polls for is correct either way.
func (p *Pool) handle(ctx context.Context, workerID int, delivery ports.Delivery) {
	jobID := delivery.JobID

	// Continue the trace the upload started rather than beginning a new one.
	// This is the join: everything the API logged about this job and
	// everything logged below share one trace id, across two processes and
	// however long the job waited in between.
	ctx = telemetry.Extract(ctx, delivery.Meta)
	ctx = telemetry.WithJobID(ctx, jobID)

	ctx, span := telemetry.Start(ctx, "job.process",
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			attribute.String("imageforge.job.id", jobID),
			attribute.Int("imageforge.worker.id", workerID),
		))
	defer span.End()

	if p.jobTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.jobTimeout)
		defer cancel()
	}

	started := time.Now()
	job, err := p.process.Execute(ctx, jobID)
	elapsed := time.Since(started)

	span.SetAttributes(attribute.String("imageforge.job.outcome", outcomeOf(err)))
	if err != nil && !errors.Is(err, usecase.ErrJobNotPending) {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}

	switch {
	case errors.Is(err, usecase.ErrJobNotPending):
		p.skipped.Add(1)
		p.observer.JobFinished(StatusSkipped, elapsed)
		p.logger.DebugContext(ctx, "job skipped",
			slog.Int("worker", workerID),
			slog.String("job_id", jobID),
			slog.Duration("duration", elapsed),
			slog.String("reason", err.Error()))
		// A duplicate delivery is settled, not retried: redelivering it would
		// only produce the same skip.
		p.settle(ctx, workerID, jobID, true)
	case err != nil:
		p.failed.Add(1)
		p.observer.JobFinished(StatusFailed, elapsed)
		p.logger.ErrorContext(ctx, "job failed",
			slog.Int("worker", workerID),
			slog.String("job_id", jobID),
			slog.Duration("duration", elapsed),
			slog.String("error", err.Error()))
		p.settle(ctx, workerID, jobID, false)
	default:
		p.processed.Add(1)
		p.observer.JobFinished(StatusProcessed, elapsed)
		p.logger.InfoContext(ctx, "job processed",
			slog.Int("worker", workerID),
			slog.String("job_id", jobID),
			slog.String("result_key", job.ResultKey),
			slog.Duration("duration", elapsed))
		p.settle(ctx, workerID, jobID, true)
	}
}

// settle tells an acknowledging queue what became of a delivery.
//
// Queues that hand out a job exactly once do not implement ports.Acknowledger,
// and for them this is a no-op. Settling uses a context detached from the job's
// own: a job that failed because its context expired must still be able to
// return its message to the queue.
func (p *Pool) settle(ctx context.Context, workerID int, jobID string, done bool) {
	acker, ok := p.queue.(ports.Acknowledger)
	if !ok {
		return
	}

	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), settleTimeout)
	defer cancel()

	var err error
	if done {
		err = acker.Ack(settleCtx, jobID)
	} else {
		err = acker.Nack(settleCtx, jobID)
	}
	if err == nil {
		return
	}

	// A delivery that cannot be settled reappears when its visibility timeout
	// expires, so this costs a repeat, not the job.
	p.logger.ErrorContext(settleCtx, "settling the delivery failed",
		slog.Int("worker", workerID),
		slog.String("job_id", jobID),
		slog.Bool("acknowledged", done),
		slog.String("error", err.Error()))
}
