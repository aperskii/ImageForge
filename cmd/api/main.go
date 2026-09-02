// Command api serves the ImageForge HTTP API.
//
// By default it runs entirely on in-process adapters — a filesystem store, an
// in-memory queue and an in-memory job repository — so `make run-api` needs no
// database, broker or cloud account. Jobs are accepted and queued; nothing
// drains that queue until the worker is implemented, so they stay pending.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"imageforge/internal/adapters/httpapi"
	"imageforge/internal/adapters/localstorage"
	"imageforge/internal/adapters/memqueue"
	"imageforge/internal/adapters/memrepo"
	"imageforge/internal/usecase"
)

// Timeouts applied to the HTTP server and to shutdown.
const (
	readHeaderTimeout = 10 * time.Second
	readTimeout       = 60 * time.Second
	writeTimeout      = 60 * time.Second
	idleTimeout       = 120 * time.Second
	shutdownTimeout   = 15 * time.Second
)

func main() {
	if err := run(); err != nil {
		slog.Error("api exited with an error", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

// run builds the server, serves until a signal arrives and then shuts down.
// It exists so that every deferred cleanup runs before main calls os.Exit.
func run() error {
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

	api := httpapi.New(
		usecase.NewCreateJob(storage, jobs, queue),
		jobs,
		httpapi.Config{
			Logger:         logger,
			MaxUploadBytes: cfg.maxUploadBytes,
			AllowedOrigins: cfg.corsOrigins,
			PublicBaseURL:  cfg.publicBaseURL,
		},
	)

	server := &http.Server{
		Addr:              cfg.addr,
		Handler:           api.Routes(),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}

	// Signals must be trapped before the listener opens, so a fast Ctrl-C is
	// still handled gracefully.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("api listening",
			slog.String("addr", cfg.addr),
			slog.String("storage_dir", storage.Root()))
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case err := <-serveErr:
		if err != nil {
			return fmt.Errorf("serve: %w", err)
		}
		return nil
	case <-ctx.Done():
		logger.Info("shutdown signal received, draining connections",
			slog.Duration("timeout", shutdownTimeout))
	}

	// Stop trapping signals so a second Ctrl-C aborts a stuck drain.
	stop()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		// Force the listener shut so in-flight handlers cannot outlive us.
		_ = server.Close()
		return fmt.Errorf("graceful shutdown: %w", err)
	}

	logger.Info("api stopped cleanly")
	return <-serveErr
}

// config holds the runtime settings, all overridable by environment variable.
type config struct {
	addr           string
	storageDir     string
	publicBaseURL  string
	corsOrigins    []string
	maxUploadBytes int64
	queueBuffer    int
	logLevel       slog.Level
}

// loadConfig reads the environment, falling back to defaults that work with no
// configuration at all.
func loadConfig() config {
	cfg := config{
		addr:           envString("IMAGEFORGE_ADDR", ":8080"),
		storageDir:     envString("IMAGEFORGE_STORAGE_DIR", ".data/storage"),
		publicBaseURL:  envString("IMAGEFORGE_PUBLIC_BASE_URL", ""),
		maxUploadBytes: envInt64("IMAGEFORGE_MAX_UPLOAD_BYTES", httpapi.DefaultMaxUploadBytes),
		queueBuffer:    int(envInt64("IMAGEFORGE_QUEUE_BUFFER", memqueue.DefaultBuffer)),
		logLevel:       envLogLevel("IMAGEFORGE_LOG_LEVEL", slog.LevelInfo),
	}

	if origins := envString("IMAGEFORGE_CORS_ORIGINS", "*"); origins != "" {
		for _, origin := range strings.Split(origins, ",") {
			if trimmed := strings.TrimSpace(origin); trimmed != "" {
				cfg.corsOrigins = append(cfg.corsOrigins, trimmed)
			}
		}
	}

	return cfg
}

// envString returns the environment value for key, or fallback when unset.
func envString(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
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
