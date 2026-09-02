// Command worker runs the ImageForge image transformation pool.
//
// IMAGEFORGE_BACKEND selects where the work comes from. The default, "memory",
// uses a filesystem store with an in-memory queue and repository, so the whole
// pipeline runs with no broker, database or cloud account. "aws" uses S3, SQS
// and DynamoDB, pointed at LocalStack when AWS_ENDPOINT_URL is set.
//
// On the memory backend the queue lives inside this process, so a worker
// started on its own has nothing to read and idles. Pass image paths to submit
// them through the same CreateJob use case the API uses and watch the pipeline
// run end to end:
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

	"imageforge/internal/adapters"
	"imageforge/internal/adapters/imageproc"
	"imageforge/internal/adapters/memqueue"
	"imageforge/internal/domain"
	"imageforge/internal/healthcheck"
	"imageforge/internal/metrics"
	"imageforge/internal/ports"
	"imageforge/internal/usecase"
	"imageforge/internal/worker"
)

func main() {
	// The worker serves /healthz on its metrics listener; "worker healthcheck"
	// probes it, so the image needs no curl of its own.
	if healthcheck.Requested(os.Args[1:]) {
		healthcheck.Main(envString("IMAGEFORGE_METRICS_ADDR", ":9090"))
	}

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

	// Signals are trapped before anything is opened, so a Ctrl-C during
	// startup is still handled by the graceful path.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	set, err := adapters.Open(ctx, cfg.backend, adapters.Config{
		StorageDir:  cfg.storageDir,
		QueueBuffer: cfg.queueBuffer,
	})
	if err != nil {
		return err
	}
	defer set.Close()

	processor, err := imageproc.New(imageproc.WithWatermarkText(cfg.watermarkText))
	if err != nil {
		return fmt.Errorf("image processor: %w", err)
	}
	defer imageproc.Shutdown()

	recorder := metrics.New()
	pool := worker.New(
		set.Queue,
		usecase.NewProcessJob(set.Storage, set.Jobs, processor),
		worker.WithSize(cfg.poolSize),
		worker.WithLogger(logger),
		worker.WithObserver(recorder),
		worker.WithShutdownTimeout(cfg.shutdownTimeout),
		worker.WithJobTimeout(cfg.jobTimeout),
	)

	// The metrics endpoint is on its own listener: it is for the operator, and
	// putting it on an application port makes it hard to firewall off.
	metricsServer := metrics.NewServer(cfg.metricsAddr, recorder, logger)
	metricsErr := make(chan error, 1)
	go func() { metricsErr <- metricsServer.ListenAndServe() }()

	// The depth gauge is best-effort: queues that cannot report it simply
	// leave it unset.
	if reporter, ok := set.Queue.(ports.DepthReporter); ok {
		go recorder.PollDepth(ctx, logger, reporter.Depth, cfg.depthInterval)
	} else {
		logger.Debug("the queue cannot report its depth, so the gauge stays unset",
			slog.String("backend", set.Name))
	}

	logger.Info("worker starting",
		slog.String("backend", set.Name),
		slog.String("image_backend", imageproc.Backend),
		slog.String("adapters", set.Description),
		slog.String("metrics_addr", metricsServer.Addr()),
		slog.Int("workers", cfg.poolSize))

	// Seeding happens before the pool starts so the jobs are already queued;
	// the buffered queue holds them until a worker picks each one up.
	if len(args) > 0 {
		if seedErr := seed(ctx, logger, usecase.NewCreateJob(set.Storage, set.Jobs, set.Queue), cfg.seedSpec, args); seedErr != nil {
			return seedErr
		}
	} else if set.Name == adapters.BackendMemory {
		// The in-memory queue lives in this process, so it starts empty and
		// nothing else can put anything in it. On the AWS backend the queue is
		// shared, and idling until work arrives is exactly the job.
		logger.Info("no images given, so this worker will idle",
			slog.String("reason", "the in-memory queue lives in this process and starts empty"),
			slog.String("hint", "pass image paths, or set IMAGEFORGE_BACKEND=aws to share a queue"))
	}

	// Run blocks until a signal arrives, then stops polling and drains what is
	// already in flight.
	runErr := pool.Run(ctx)

	// The metrics server outlives the pool deliberately, so a final scrape can
	// still collect the counters for the jobs that just drained.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), metricsShutdownTimeout)
	defer cancel()
	if err = metricsServer.Shutdown(shutdownCtx); err != nil {
		logger.Warn("the metrics server did not stop cleanly", slog.String("error", err.Error()))
	}
	if err = <-metricsErr; err != nil {
		logger.Warn("the metrics server failed", slog.String("error", err.Error()))
	}

	if runErr != nil {
		return fmt.Errorf("pool: %w", runErr)
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

// metricsShutdownTimeout bounds the wait for in-flight scrapes to finish.
const metricsShutdownTimeout = 5 * time.Second

// config holds the runtime settings, all overridable by environment variable.
type config struct {
	backend         string
	metricsAddr     string
	depthInterval   time.Duration
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
		backend:         adapters.BackendFromEnv(),
		metricsAddr:     envString("IMAGEFORGE_METRICS_ADDR", ":9090"),
		depthInterval:   envDuration("IMAGEFORGE_QUEUE_DEPTH_INTERVAL", metrics.DefaultDepthInterval),
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
