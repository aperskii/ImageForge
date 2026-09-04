// Package httpapi implements the HTTP transport for ImageForge, driving the
// use cases in internal/usecase through a chi router.
//
// The package owns request decoding, response encoding and the middleware
// stack; it holds no business rules of its own. Errors are RFC 7807 problem
// details, served as application/problem+json.
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
	// RateLimit is the sustained requests per second allowed per client.
	// Defaults to DefaultRateLimit; a negative value disables limiting.
	RateLimit float64
	// RateBurst is how many requests a client may make back to back. Defaults
	// to DefaultRateBurst; a negative value disables limiting.
	RateBurst int
	// clock overrides time for tests, in both the issuer and the limiter.
	clock func() time.Time
}

// API serves the ImageForge HTTP endpoints.
type API struct {
	createJob *usecase.CreateJob
	jobs      ports.JobRepository
	issuer    *TokenIssuer
	limiter   *rateLimiter
	cfg       Config
}

// New wires the API to its dependencies.
//
// The issuer both mints the tokens /auth/token hands out and verifies the ones
// presented to the protected routes, so a caller cannot configure one without
// the other.
func New(createJob *usecase.CreateJob, jobs ports.JobRepository, issuer *TokenIssuer, cfg Config) *API {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.MaxUploadBytes <= 0 {
		cfg.MaxUploadBytes = DefaultMaxUploadBytes
	}
	if len(cfg.AllowedOrigins) == 0 {
		cfg.AllowedOrigins = []string{"*"}
	}
	if cfg.RateLimit == 0 {
		cfg.RateLimit = DefaultRateLimit
	}
	if cfg.RateBurst == 0 {
		cfg.RateBurst = DefaultRateBurst
	}
	cfg.PublicBaseURL = strings.TrimSuffix(cfg.PublicBaseURL, "/")

	return &API{
		createJob: createJob,
		jobs:      jobs,
		issuer:    issuer,
		limiter:   newRateLimiter(cfg.RateLimit, cfg.RateBurst, cfg.clock),
		cfg:       cfg,
	}
}

// Close releases what the API holds. It is safe to call more than once.
func (a *API) Close() { a.limiter.Close() }

// Routes returns the fully assembled handler, middleware included.
//
// The stack is ordered so that every later layer, and any panic it raises, is
// observed by the request log and reported with the request's identifier.
func (a *API) Routes() http.Handler {
	// Outermost, so every middleware below runs inside the server span and
	// every line they log carries its trace id.
	return withTracing(a.routes())
}

// routes builds the router itself, without the tracing wrapper.
//
// It is separate so that a test can mount a route of its own on the result and
// still exercise the real middleware stack, which type-asserting the handler
// Routes returns no longer allows.
func (a *API) routes() chi.Router {
	router := chi.NewRouter()

	router.Use(middleware.RequestID)
	router.Use(echoRequestID)
	router.Use(traceRoute)
	router.Use(requestLogger(a.cfg.Logger))
	router.Use(recoverer(a.cfg.Logger))
	router.Use(cors.Handler(cors.Options{
		AllowedOrigins: a.cfg.AllowedOrigins,
		AllowedMethods: []string{http.MethodGet, http.MethodPost, http.MethodOptions},
		AllowedHeaders: []string{
			"Accept", "Authorization", "Content-Type",
			middleware.RequestIDHeader,
			// So a browser client that is itself traced can hand its trace to
			// this service rather than starting a second one.
			"traceparent", "tracestate",
		},
		ExposedHeaders:   []string{"Location", "Retry-After", middleware.RequestIDHeader, TraceIDHeader},
		AllowCredentials: false,
		MaxAge:           int((5 * time.Minute).Seconds()),
	}))
	router.Use(middleware.RequestSize(a.cfg.MaxUploadBytes))

	// Probes stay open: a load balancer cannot hold a token, and a liveness
	// check that fails on authentication is worse than useless.
	router.Get("/healthz", a.handleHealthz)
	router.Get("/readyz", a.handleReadyz)

	// Issuing a token cannot itself require one.
	router.Post("/auth/token", a.handleToken)

	// Everything that touches a job is authenticated, and rate limited by the
	// authenticated client rather than by anything the caller can change.
	router.Group(func(protected chi.Router) {
		protected.Use(a.requireAuth)
		protected.Use(a.rateLimit)

		protected.Post("/uploads", a.handleUpload)
		protected.Get("/jobs/{id}", a.handleGetJob)
	})

	router.NotFound(func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, r, http.StatusNotFound, TypeNotFound,
			"Not Found", "No route matches this path.")
	})
	router.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, r, http.StatusMethodNotAllowed, TypeMethodNotAllowed,
			"Method Not Allowed", "This method is not allowed on this path.")
	})

	return router
}

// slogClient is a client id as a log attribute.
func slogClient(clientID string) slog.Attr {
	return slog.String("client_id", clientID)
}
