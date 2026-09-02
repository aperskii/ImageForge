package worker

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	// Registered so image.DecodeConfig can inspect the results the pool wrote.
	_ "image/jpeg"

	"imageforge/internal/adapters/imageproc"
	"imageforge/internal/adapters/localstorage"
	"imageforge/internal/adapters/memqueue"
	"imageforge/internal/adapters/memrepo"
	"imageforge/internal/domain"
	"imageforge/internal/ports"
	"imageforge/internal/usecase"
)

// stack is a fully wired pipeline over in-process adapters.
type stack struct {
	storage   *localstorage.Storage
	queue     *memqueue.Queue
	jobs      *memrepo.JobRepository
	createJob *usecase.CreateJob
	pool      *Pool
}

// newStack builds the pipeline with the given pool options, torn down when the
// test ends.
func newStack(t *testing.T, queueBuffer int, opts ...Option) *stack {
	t.Helper()

	storage, err := localstorage.New(t.TempDir())
	require.NoError(t, err)

	queue := memqueue.New(queueBuffer)
	t.Cleanup(queue.Close)
	jobs := memrepo.New()

	processor, err := imageproc.New()
	require.NoError(t, err, "the %s backend must be usable", imageproc.Backend)

	opts = append([]Option{WithLogger(discardLogger())}, opts...)

	return &stack{
		storage:   storage,
		queue:     queue,
		jobs:      jobs,
		createJob: usecase.NewCreateJob(storage, jobs, queue),
		pool:      New(queue, usecase.NewProcessJob(storage, jobs, processor), opts...),
	}
}

// discardLogger returns a logger that throws its output away.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// samplePNG encodes a small gradient of the given size. Generating the fixture
// keeps the test self-contained and lets each case pick its own dimensions.
func samplePNG(t *testing.T, width, height int) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 0x80, A: 0xff})
		}
	}

	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

// TestPoolProcessesConcurrentJobs is the race scenario: a hundred jobs are
// submitted from a hundred goroutines while the pool drains the queue, and
// every one of them must reach Done with a result that decodes to the
// dimensions its specification asked for.
//
// Run it under -race to check the JobRepository and the pool itself.
func TestPoolProcessesConcurrentJobs(t *testing.T) {
	t.Parallel()

	const jobCount = 100

	stack := newStack(t, jobCount, WithSize(8))
	source := samplePNG(t, 64, 48)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runErr := make(chan error, 1)
	go func() { runErr <- stack.pool.Run(ctx) }()

	// Every job asks for a different width, so a result cannot accidentally
	// satisfy the assertions for another job.
	type expectation struct {
		width  int
		height int
		format string
		key    string
	}
	expected := make(map[string]expectation, jobCount)
	var mu sync.Mutex

	var wg sync.WaitGroup
	wg.Add(jobCount)
	for i := range jobCount {
		go func(n int) {
			defer wg.Done()

			width := 8 + n // 8..107, all distinct
			format := domain.FormatPNG
			quality := 0
			if n%2 == 0 {
				format, quality = domain.FormatJPEG, 70
			}
			spec := domain.TransformationSpec{Width: width, Format: format, Quality: quality}

			job, err := stack.createJob.Execute(ctx, usecase.CreateJobInput{
				Source: bytes.NewReader(source),
				Spec:   spec,
			})
			if !assert.NoError(t, err) {
				return
			}

			// The source is 64x48, so the height follows the 4:3 ratio.
			mu.Lock()
			expected[job.ID] = expectation{
				width:  width,
				height: (48*width + 32) / 64,
				format: format.String(),
				key:    usecase.ResultKey(job.ID, spec),
			}
			mu.Unlock()
		}(i)
	}
	wg.Wait()

	mu.Lock()
	require.Len(t, expected, jobCount, "every submission produced a distinct job")
	mu.Unlock()

	// Wait for the pool to drain rather than for a fixed duration.
	require.Eventuallyf(t, func() bool {
		return stack.pool.Stats().Processed == jobCount
	}, 60*time.Second, 10*time.Millisecond,
		"pool did not finish; stats: %+v", stack.pool.Stats())

	for id, want := range expected {
		job, err := stack.jobs.Get(context.Background(), id)
		require.NoErrorf(t, err, "job %s", id)

		assert.Equalf(t, domain.StatusDone, job.Status, "job %s: %s", id, job.Error)
		assert.Emptyf(t, job.Error, "job %s", id)
		assert.Equalf(t, want.key, job.ResultKey, "job %s", id)

		object, err := stack.storage.Get(context.Background(), job.ResultKey)
		require.NoErrorf(t, err, "job %s result is missing from storage", id)

		encoded, err := io.ReadAll(object)
		require.NoError(t, err)
		require.NoError(t, object.Close())

		cfg, format, err := image.DecodeConfig(bytes.NewReader(encoded))
		require.NoErrorf(t, err, "job %s result does not decode", id)
		assert.Equalf(t, want.format, format, "job %s format", id)
		assert.Equalf(t, want.width, cfg.Width, "job %s width", id)
		assert.Equalf(t, want.height, cfg.Height, "job %s height", id)
	}

	stats := stack.pool.Stats()
	assert.Equal(t, uint64(jobCount), stats.Processed)
	assert.Zero(t, stats.Failed)
	assert.Zero(t, stats.Skipped, "no job should be delivered twice")

	cancel()
	require.NoError(t, <-runErr)
}

// TestPoolConcurrentRepositoryAccess hammers the repository from the pool and
// from readers at the same time, which is what a client polling GET /jobs/{id}
// does while the worker is writing. It exists to give -race something to catch.
func TestPoolConcurrentRepositoryAccess(t *testing.T) {
	t.Parallel()

	const jobCount = 50

	stack := newStack(t, jobCount, WithSize(4))
	source := samplePNG(t, 32, 32)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runErr := make(chan error, 1)
	go func() { runErr <- stack.pool.Run(ctx) }()

	ids := make([]string, 0, jobCount)
	for i := range jobCount {
		job, err := stack.createJob.Execute(ctx, usecase.CreateJobInput{
			Source: bytes.NewReader(source),
			Spec:   domain.TransformationSpec{Width: 16 + i, Format: domain.FormatPNG},
		})
		require.NoError(t, err)
		ids = append(ids, job.ID)
	}

	// Poll every job from its own goroutine while the pool writes to them.
	pollCtx, stopPolling := context.WithCancel(context.Background())
	var pollers sync.WaitGroup
	pollers.Add(len(ids))
	for _, id := range ids {
		go func(id string) {
			defer pollers.Done()
			for pollCtx.Err() == nil {
				if _, err := stack.jobs.Get(pollCtx, id); err != nil && pollCtx.Err() == nil {
					assert.ErrorIs(t, err, ports.ErrJobNotFound)
				}
			}
		}(id)
	}

	require.Eventually(t, func() bool {
		return stack.pool.Stats().Processed == jobCount
	}, 60*time.Second, 10*time.Millisecond)

	stopPolling()
	pollers.Wait()

	for _, id := range ids {
		job, err := stack.jobs.Get(context.Background(), id)
		require.NoError(t, err)
		assert.Equal(t, domain.StatusDone, job.Status)
	}

	cancel()
	require.NoError(t, <-runErr)
}

// TestPoolWaitsForInFlightJobs asserts the central shutdown promise: a job that
// has already been picked up runs to completion even though the context that
// started the pool is canceled underneath it.
func TestPoolWaitsForInFlightJobs(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	release := make(chan struct{})
	processor := &blockingProcessor{started: started, release: release}

	storage, err := localstorage.New(t.TempDir())
	require.NoError(t, err)
	queue := memqueue.New(4)
	t.Cleanup(queue.Close)
	jobs := memrepo.New()

	createJob := usecase.NewCreateJob(storage, jobs, queue)
	pool := New(queue, usecase.NewProcessJob(storage, jobs, processor),
		WithSize(1), WithLogger(discardLogger()), WithShutdownTimeout(10*time.Second))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	job, err := createJob.Execute(ctx, usecase.CreateJobInput{
		Source: bytes.NewReader(samplePNG(t, 16, 16)),
		Spec:   domain.TransformationSpec{Width: 8, Format: domain.FormatPNG},
	})
	require.NoError(t, err)

	runErr := make(chan error, 1)
	go func() { runErr <- pool.Run(ctx) }()

	// The job is now inside the processor. Pull the rug out.
	select {
	case <-started:
	case <-time.After(30 * time.Second):
		t.Fatal("the job never reached the processor")
	}
	cancel()

	// Give the pool a moment to observe the cancellation, then let the job
	// finish. A pool that abandoned it would already have returned.
	time.Sleep(50 * time.Millisecond)
	close(release)

	select {
	case err = <-runErr:
		require.NoError(t, err, "the pool must drain rather than time out")
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return after the in-flight job finished")
	}

	stored, err := jobs.Get(context.Background(), job.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusDone, stored.Status,
		"the in-flight job must complete despite the shutdown")
	assert.Equal(t, uint64(1), pool.Stats().Processed)
}

// TestPoolShutdownTimeout asserts the other half of that promise: the wait is
// bounded, and a job that never finishes cannot hold the process open.
func TestPoolShutdownTimeout(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	processor := &blockingProcessor{started: started, release: make(chan struct{})}

	storage, err := localstorage.New(t.TempDir())
	require.NoError(t, err)
	queue := memqueue.New(4)
	t.Cleanup(queue.Close)
	jobs := memrepo.New()

	createJob := usecase.NewCreateJob(storage, jobs, queue)
	pool := New(queue, usecase.NewProcessJob(storage, jobs, processor),
		WithSize(1), WithLogger(discardLogger()), WithShutdownTimeout(100*time.Millisecond))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, err = createJob.Execute(ctx, usecase.CreateJobInput{
		Source: bytes.NewReader(samplePNG(t, 16, 16)),
		Spec:   domain.TransformationSpec{Width: 8, Format: domain.FormatPNG},
	})
	require.NoError(t, err)

	runErr := make(chan error, 1)
	go func() { runErr <- pool.Run(ctx) }()

	select {
	case <-started:
	case <-time.After(30 * time.Second):
		t.Fatal("the job never reached the processor")
	}
	cancel()

	select {
	case err = <-runErr:
		require.ErrorIs(t, err, ErrShutdownTimeout)
	case <-time.After(30 * time.Second):
		t.Fatal("Run ignored its shutdown timeout")
	}
}

// TestPoolStopsWhenTheQueueCloses asserts Run returns on its own once the queue
// is closed and drained, without needing its context canceled.
func TestPoolStopsWhenTheQueueCloses(t *testing.T) {
	t.Parallel()

	stack := newStack(t, 8, WithSize(2))

	_, err := stack.createJob.Execute(context.Background(), usecase.CreateJobInput{
		Source: bytes.NewReader(samplePNG(t, 32, 32)),
		Spec:   domain.TransformationSpec{Width: 16, Format: domain.FormatPNG},
	})
	require.NoError(t, err)

	runErr := make(chan error, 1)
	go func() { runErr <- stack.pool.Run(context.Background()) }()

	require.Eventually(t, func() bool {
		return stack.pool.Stats().Processed == 1
	}, 30*time.Second, 10*time.Millisecond)

	stack.queue.Close()

	select {
	case err = <-runErr:
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return after the queue closed")
	}
}

// TestPoolSkipsJobsThatAreNotPending covers duplicate delivery: the same id
// arriving twice must be processed once and skipped once, not processed twice.
func TestPoolSkipsJobsThatAreNotPending(t *testing.T) {
	t.Parallel()

	stack := newStack(t, 8, WithSize(1))

	job, err := stack.createJob.Execute(context.Background(), usecase.CreateJobInput{
		Source: bytes.NewReader(samplePNG(t, 32, 32)),
		Spec:   domain.TransformationSpec{Width: 16, Format: domain.FormatPNG},
	})
	require.NoError(t, err)

	// A second delivery of the same identifier, as an at-least-once queue does.
	require.NoError(t, stack.queue.Enqueue(context.Background(), job.ID))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runErr := make(chan error, 1)
	go func() { runErr <- stack.pool.Run(ctx) }()

	require.Eventuallyf(t, func() bool {
		stats := stack.pool.Stats()
		return stats.Processed == 1 && stats.Skipped == 1
	}, 30*time.Second, 10*time.Millisecond, "stats: %+v", stack.pool.Stats())

	assert.Zero(t, stack.pool.Stats().Failed)

	cancel()
	require.NoError(t, <-runErr)
}

// TestPoolRecordsFailures asserts a failing job is counted and recorded against
// the job, and does not stop the pool taking the next one.
func TestPoolRecordsFailures(t *testing.T) {
	t.Parallel()

	storage, err := localstorage.New(t.TempDir())
	require.NoError(t, err)
	queue := memqueue.New(8)
	t.Cleanup(queue.Close)
	jobs := memrepo.New()

	createJob := usecase.NewCreateJob(storage, jobs, queue)
	pool := New(queue, usecase.NewProcessJob(storage, jobs, &failingProcessor{}),
		WithSize(2), WithLogger(discardLogger()))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ids := make([]string, 0, 3)
	for i := range 3 {
		job, jobErr := createJob.Execute(ctx, usecase.CreateJobInput{
			Source: bytes.NewReader(samplePNG(t, 16, 16)),
			Spec:   domain.TransformationSpec{Width: 8 + i, Format: domain.FormatPNG},
		})
		require.NoError(t, jobErr)
		ids = append(ids, job.ID)
	}

	runErr := make(chan error, 1)
	go func() { runErr <- pool.Run(ctx) }()

	require.Eventually(t, func() bool {
		return pool.Stats().Failed == 3
	}, 30*time.Second, 10*time.Millisecond)

	for _, id := range ids {
		job, getErr := jobs.Get(context.Background(), id)
		require.NoError(t, getErr)
		assert.Equal(t, domain.StatusFailed, job.Status)
		assert.Contains(t, job.Error, "processor is broken")
		assert.Empty(t, job.ResultKey)
	}

	assert.Zero(t, pool.Stats().Processed)

	cancel()
	require.NoError(t, <-runErr)
}

func TestNewAppliesDefaults(t *testing.T) {
	t.Parallel()

	pool := New(nil, nil)
	assert.Equal(t, DefaultSize, pool.size)
	assert.Equal(t, DefaultShutdownTimeout, pool.shutdownTimeout)
	assert.Equal(t, DefaultJobTimeout, pool.jobTimeout)
	assert.NotNil(t, pool.logger)

	tuned := New(nil, nil,
		WithSize(16),
		WithShutdownTimeout(time.Second),
		WithJobTimeout(2*time.Second),
	)
	assert.Equal(t, 16, tuned.Size())
	assert.Equal(t, time.Second, tuned.shutdownTimeout)
	assert.Equal(t, 2*time.Second, tuned.jobTimeout)

	// Meaningless values are ignored rather than breaking the pool.
	guarded := New(nil, nil, WithSize(0), WithShutdownTimeout(-1), WithLogger(nil))
	assert.Equal(t, DefaultSize, guarded.size)
	assert.Equal(t, DefaultShutdownTimeout, guarded.shutdownTimeout)
	assert.NotNil(t, guarded.logger)

	// A non-positive job timeout deliberately removes the bound.
	assert.Zero(t, New(nil, nil, WithJobTimeout(0)).jobTimeout)
}

// blockingProcessor holds a job inside Process until it is released, so tests
// can control exactly when work is in flight.
type blockingProcessor struct {
	once    sync.Once
	started chan struct{}
	release chan struct{}
}

func (p *blockingProcessor) Process(
	ctx context.Context,
	input io.Reader,
	_ domain.TransformationSpec,
) (io.Reader, error) {
	encoded, err := io.ReadAll(input)
	if err != nil {
		return nil, err
	}

	p.once.Do(func() { close(p.started) })

	select {
	case <-p.release:
		return bytes.NewReader(encoded), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// failingProcessor rejects every job.
type failingProcessor struct{}

func (*failingProcessor) Process(context.Context, io.Reader, domain.TransformationSpec) (io.Reader, error) {
	return nil, fmt.Errorf("processor is broken")
}
