// Package httpapi implements the HTTP transport for ImageForge, driving the
// use cases in internal/usecase through a chi router.
//
// The package owns request decoding, response encoding and the middleware
// stack; it holds no business rules of its own.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	"imageforge/internal/ports"
	"imageforge/internal/usecase"
)

// DefaultMaxUploadBytes is the largest request body accepted by POST /uploads.
const DefaultMaxUploadBytes int64 = 10 << 20 // 10MB

// Multipart field names accepted by POST /uploads.
const (
	// FileField carries the image itself.
	FileField = "file"
	// SpecField carries the JSON transformation specification.
	SpecField = "spec"
)

// maxSpecBytes caps the JSON specification field, which is small by nature and
// should never approach the overall body limit.
const maxSpecBytes = 8 << 10

// multipartMemoryBytes is how much of a multipart upload is buffered in memory
// before the rest spills to a temporary file.
const multipartMemoryBytes = 1 << 20

// Config holds the settings of an API.
type Config struct {
	// Logger receives the structured request log. Defaults to slog.Default().
	Logger *slog.Logger
	// MaxUploadBytes limits the request body. Defaults to
	// DefaultMaxUploadBytes.
	MaxUploadBytes int64
	// AllowedOrigins lists the CORS origins. Defaults to "*".
	AllowedOrigins []string
	// PublicBaseURL, when set, is prefixed to a finished job's result key to
	// build its result URL, for example "https://cdn.example.com". When empty,
	// responses carry the result key alone.
	PublicBaseURL string
	// ReadyCheck reports whether the service can serve traffic. A nil check
	// means always ready.
	ReadyCheck func(context.Context) error
}

// API serves the ImageForge HTTP endpoints.
type API struct {
	createJob *usecase.CreateJob
	jobs      ports.JobRepository
	cfg       Config
}

// New wires the API to its dependencies.
func New(createJob *usecase.CreateJob, jobs ports.JobRepository, cfg Config) *API {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.MaxUploadBytes <= 0 {
		cfg.MaxUploadBytes = DefaultMaxUploadBytes
	}
	if len(cfg.AllowedOrigins) == 0 {
		cfg.AllowedOrigins = []string{"*"}
	}
	cfg.PublicBaseURL = strings.TrimSuffix(cfg.PublicBaseURL, "/")

	return &API{createJob: createJob, jobs: jobs, cfg: cfg}
}

// Routes returns the fully assembled handler, middleware included.
//
// The stack is ordered so that every later layer, and any panic it raises, is
// observed by the request log and reported with the request's identifier.
func (a *API) Routes() http.Handler {
	router := chi.NewRouter()

	router.Use(middleware.RequestID)
	router.Use(echoRequestID)
	router.Use(requestLogger(a.cfg.Logger))
	router.Use(recoverer(a.cfg.Logger))
	router.Use(cors.Handler(cors.Options{
		AllowedOrigins:   a.cfg.AllowedOrigins,
		AllowedMethods:   []string{http.MethodGet, http.MethodPost, http.MethodOptions},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", middleware.RequestIDHeader},
		ExposedHeaders:   []string{"Location", middleware.RequestIDHeader},
		AllowCredentials: false,
		MaxAge:           int((5 * time.Minute).Seconds()),
	}))
	router.Use(middleware.RequestSize(a.cfg.MaxUploadBytes))

	router.Get("/healthz", a.handleHealthz)
	router.Get("/readyz", a.handleReadyz)
	router.Post("/uploads", a.handleUpload)
	router.Get("/jobs/{id}", a.handleGetJob)

	router.NotFound(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, http.StatusNotFound, codeNotFound, "no route matches this path")
	})
	router.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, http.StatusMethodNotAllowed, codeMethodNotAllowed, "this method is not allowed on this path")
	})

	return router
}
