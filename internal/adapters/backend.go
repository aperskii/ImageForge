package adapters

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"imageforge/internal/adapters/awscfg"
	"imageforge/internal/adapters/dynamorepo"
	"imageforge/internal/adapters/localstorage"
	"imageforge/internal/adapters/memqueue"
	"imageforge/internal/adapters/memrepo"
	"imageforge/internal/adapters/s3storage"
	"imageforge/internal/adapters/sqsqueue"
	"imageforge/internal/ports"
)

// EnvBackend selects which set of adapters the binaries wire up.
const EnvBackend = "IMAGEFORGE_BACKEND"

// The values EnvBackend accepts.
const (
	// BackendMemory runs on in-process adapters: a filesystem store, an
	// in-memory queue and an in-memory job repository. It needs nothing
	// external and is the default.
	BackendMemory = "memory"
	// BackendAWS runs on S3, SQS and DynamoDB, pointed at LocalStack when
	// AWS_ENDPOINT_URL is set and at real AWS otherwise.
	BackendAWS = "aws"
)

// ErrUnknownBackend is returned for a backend name that is not recognized.
var ErrUnknownBackend = errors.New("unknown backend")

// Set is the group of adapters a binary runs on.
type Set struct {
	// Name is the backend that produced this set.
	Name string
	// Description summarizes where the data actually goes, for the startup log.
	Description string

	Storage ports.Storage
	Queue   ports.Queue
	Jobs    ports.JobRepository

	// Close releases anything the set holds. It is never nil.
	Close func()
}

// ReceiveErrorHandler is notified whenever reading from the queue fails, with
// the consecutive failure count and the delay before the next attempt.
//
// It is a package variable rather than a field on Config because it is wired
// once per process, in main, and only the AWS backend has anything to report.
var ReceiveErrorHandler = func(err error, attempt int, delay time.Duration) {
	slog.Warn("reading from the queue failed, backing off",
		slog.Int("attempt", attempt),
		slog.Duration("retry_in", delay),
		slog.String("error", err.Error()))
}

// onReceiveError forwards to the installed handler, so replacing the variable
// after a queue was built still takes effect.
func onReceiveError(err error, attempt int, delay time.Duration) {
	if ReceiveErrorHandler != nil {
		ReceiveErrorHandler(err, attempt, delay)
	}
}

// Config holds the settings the in-process adapters need. The AWS adapters read
// their own from the environment through awscfg.
type Config struct {
	// StorageDir is the filesystem store root, used by the memory backend.
	StorageDir string
	// QueueBuffer is the in-memory queue depth, used by the memory backend.
	QueueBuffer int
}

// BackendFromEnv returns the backend named by EnvBackend, defaulting to
// BackendMemory so a developer with nothing running still gets a working stack.
func BackendFromEnv() string {
	if v, ok := os.LookupEnv(EnvBackend); ok && strings.TrimSpace(v) != "" {
		return strings.ToLower(strings.TrimSpace(v))
	}
	return BackendMemory
}

// Open builds the adapter set for the named backend.
func Open(ctx context.Context, backend string, cfg Config) (*Set, error) {
	switch backend {
	case BackendMemory, "":
		return openMemory(cfg)
	case BackendAWS:
		return openAWS(ctx)
	default:
		return nil, fmt.Errorf("adapters: %w: %q (want %q or %q)",
			ErrUnknownBackend, backend, BackendMemory, BackendAWS)
	}
}

// openMemory wires the in-process adapters.
func openMemory(cfg Config) (*Set, error) {
	storage, err := localstorage.New(cfg.StorageDir)
	if err != nil {
		return nil, fmt.Errorf("adapters: storage: %w", err)
	}
	queue := memqueue.New(cfg.QueueBuffer)

	return &Set{
		Name:        BackendMemory,
		Description: "filesystem store at " + storage.Root() + ", in-memory queue and repository",
		Storage:     storage,
		Queue:       queue,
		Jobs:        memrepo.New(),
		Close:       queue.Close,
	}, nil
}

// openAWS wires the S3, SQS and DynamoDB adapters from the environment.
func openAWS(ctx context.Context) (*Set, error) {
	settings := awscfg.SettingsFromEnv()
	if err := settings.Validate(); err != nil {
		return nil, fmt.Errorf("adapters: %w", err)
	}

	cfg, err := awscfg.Load(ctx, settings)
	if err != nil {
		return nil, fmt.Errorf("adapters: %w", err)
	}

	storage, err := s3storage.New(awscfg.S3(cfg, settings), settings.Bucket)
	if err != nil {
		return nil, fmt.Errorf("adapters: %w", err)
	}
	queue, err := sqsqueue.New(ctx, awscfg.SQS(cfg, settings), settings.Queue,
		sqsqueue.WithReceiveErrorHandler(onReceiveError))
	if err != nil {
		return nil, fmt.Errorf("adapters: %w", err)
	}
	jobs, err := dynamorepo.New(awscfg.DynamoDB(cfg, settings), settings.Table)
	if err != nil {
		return nil, fmt.Errorf("adapters: %w", err)
	}

	endpoint := settings.Endpoint
	if endpoint == "" {
		endpoint = "aws"
	}

	return &Set{
		Name: BackendAWS,
		Description: fmt.Sprintf("%s: bucket=%s queue=%s table=%s region=%s",
			endpoint, settings.Bucket, queue.URL(), settings.Table, settings.Region),
		Storage: storage,
		Queue:   queue,
		Jobs:    jobs,
		Close:   func() {},
	}, nil
}
