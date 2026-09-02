// Command worker runs the ImageForge image transformation pool.
//
// It wires the pool to the in-process adapters — a filesystem store, an
// in-memory queue and an in-memory job repository — so the whole pipeline can
// be exercised locally with no broker, database or cloud account.
//
// Those adapters live inside this process, so a worker started on its own has
// nothing to read: it idles until it is shut down. To watch the pipeline end to
// end, pass image paths, which are submitted through the same CreateJob use
// case the API uses and then picked up by the pool:
//
//	go run ./cmd/worker photo.png diagram.png
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"imageforge/internal/adapters/imageproc"
	"imageforge/internal/adapters/localstorage"
	"imageforge/internal/adapters/memqueue"
	"imageforge/internal/adapters/memrepo"
	"imageforge/internal/domain"
	"imageforge/internal/usecase"
	"imageforge/internal/worker"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("worker exited with an error", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

// run builds the pool, seeds any images named on the command line and serves
// until a signal arrives. It exists so deferred cleanup runs before os.Exit.
func run(args []string) error {
	cfg := loadConfig()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.logLevel}))
	slog.SetDefault(logger)

	storage, err := localstorage.New(cfg.storageDir)
	if err != nil {
		return fmt.Errorf("storage: %w", err)
	}
	queue := memqueue.New(cfg.queueBuffer)
	defer queue.Close()
	jobs := memrepo.New()

	processor, err := imageproc.New(imageproc.WithWatermarkText(cfg.watermarkText))
	if err != nil {
		return fmt.Errorf("image processor: %w", err)
	}
	defer imageproc.Shutdown()

	pool := worker.New(
		queue,
		usecase.NewProcessJob(storage, jobs, processor),
		worker.WithSize(cfg.poolSize),
		worker.WithLogger(logger),
		worker.WithShutdownTimeout(cfg.shutdownTimeout),
		worker.WithJobTimeout(cfg.jobTimeout),
	)

	// Signals are trapped before any work starts, so an immediate Ctrl-C is
	// still handled by the graceful path.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("worker starting",
		slog.String("backend", imageproc.Backend),
		slog.String("storage_dir", storage.Root()),
		slog.Int("workers", cfg.poolSize))

	// Seeding happens before the pool starts so the jobs are already queued;
	// the buffered queue holds them until a worker picks each one up.
	if len(args) > 0 {
		if seedErr := seed(ctx, logger, usecase.NewCreateJob(storage, jobs, queue), cfg.seedSpec, args); seedErr != nil {
			return seedErr
		}
	} else {
		logger.Info("no images given, so this worker will idle",
			slog.String("reason", "the in-memory queue lives in this process and starts empty"),
			slog.String("hint", "pass image paths to submit jobs: go run ./cmd/worker photo.png"))
	}

	// Run blocks until a signal arrives, then drains what is in flight.
	if err = pool.Run(ctx); err != nil {
		return fmt.Errorf("pool: %w", err)
	}

	logger.Info("worker stopped cleanly", slog.Any("stats", pool.Stats()))
	return nil
}

// seed submits each named image through the CreateJob use case, exactly as the
// API would, so the pool picks them up from the queue.
func seed(
	ctx context.Context,
	logger *slog.Logger,
	createJob *usecase.CreateJob,
	spec domain.TransformationSpec,
	paths []string,
) error {
	for _, path := range paths {
		file, err := os.Open(path) //nolint:gosec // the operator named this path on the command line.
		if err != nil {
			return fmt.Errorf("seed %q: %w", path, err)
		}

		job, err := createJob.Execute(ctx, usecase.CreateJobInput{Source: file, Spec: spec})
		_ = file.Close()
		if err != nil {
			return fmt.Errorf("seed %q: %w", path, err)
		}

		logger.Info("job submitted",
			slog.String("source", filepath.Base(path)),
			slog.String("job_id", job.ID))
	}
	return nil
}

// config holds the runtime settings, all overridable by environment variable.
type config struct {
	storageDir      string
	watermarkText   string
	poolSize        int
	queueBuffer     int
	shutdownTimeout time.Duration
	jobTimeout      time.Duration
	seedSpec        domain.TransformationSpec
	logLevel        slog.Level
}

// loadConfig reads the environment, falling back to defaults that work with no
// configuration at all.
func loadConfig() config {
	return config{
		storageDir:      envString("IMAGEFORGE_STORAGE_DIR", ".data/storage"),
		watermarkText:   envString("IMAGEFORGE_WATERMARK_TEXT", imageproc.DefaultWatermarkText),
		poolSize:        int(envInt64("IMAGEFORGE_WORKERS", worker.DefaultSize)),
		queueBuffer:     int(envInt64("IMAGEFORGE_QUEUE_BUFFER", memqueue.DefaultBuffer)),
		shutdownTimeout: envDuration("IMAGEFORGE_SHUTDOWN_TIMEOUT", worker.DefaultShutdownTimeout),
		jobTimeout:      envDuration("IMAGEFORGE_JOB_TIMEOUT", worker.DefaultJobTimeout),
		seedSpec: domain.TransformationSpec{
			Width:         int(envInt64("IMAGEFORGE_SEED_WIDTH", 640)),
			Format:        domain.Format(envString("IMAGEFORGE_SEED_FORMAT", string(domain.FormatJPEG))),
			Quality:       int(envInt64("IMAGEFORGE_SEED_QUALITY", 80)),
			StripMetadata: true,
		},
		logLevel: envLogLevel("IMAGEFORGE_LOG_LEVEL", slog.LevelInfo),
	}
}

// envString returns the environment value for key, or fallback when unset.
func envString(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}

// envInt64 returns the environment value for key as an int64. An unset or
// unparseable value yields fallback.
func envInt64(key string, fallback int64) int64 {
	raw, ok := os.LookupEnv(key)
	if !ok {
		return fallback
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		slog.Warn("ignoring an unparseable setting",
			slog.String("key", key), slog.String("value", raw))
		return fallback
	}
	return v
}

// envDuration returns the environment value for key as a duration, accepting
// any form time.ParseDuration does.
func envDuration(key string, fallback time.Duration) time.Duration {
	raw, ok := os.LookupEnv(key)
	if !ok {
		return fallback
	}
	v, err := time.ParseDuration(raw)
	if err != nil {
		slog.Warn("ignoring an unparseable duration",
			slog.String("key", key), slog.String("value", raw))
		return fallback
	}
	return v
}

// envLogLevel returns the environment value for key as a slog level.
func envLogLevel(key string, fallback slog.Level) slog.Level {
	raw, ok := os.LookupEnv(key)
	if !ok {
		return fallback
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(raw)); err != nil {
		slog.Warn("ignoring an unparseable log level",
			slog.String("key", key), slog.String("value", raw))
		return fallback
	}
	return level
}
