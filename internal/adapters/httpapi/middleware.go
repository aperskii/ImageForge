package httpapi

import (
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/trace"

	"imageforge/internal/telemetry"
)

// TraceIDHeader carries the trace identifier back to the client, so a caller
// reporting a problem can quote the one string that finds every log line about
// it, in both services.
const TraceIDHeader = "X-Trace-Id"

// withTracing wraps the whole router in a server span, continuing the trace in
// the request's traceparent header when it has one.
//
// It sits outside every other middleware so that the request log, the panic
// handler and the problem responses all see a context that already carries the
// span.
func withTracing(next http.Handler) http.Handler {
	return otelhttp.NewHandler(next, "http",
		// Probes are excluded. A load balancer health check every few seconds
		// produces far more spans than the traffic does, and none of them say
		// anything.
		otelhttp.WithFilter(func(r *http.Request) bool { return !isProbe(r.URL.Path) }),
		// The span is named before chi has matched a route, so the method is
		// all that is known here. traceRoute renames it once the pattern is.
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string { return r.Method }),
	)
}

// isProbe reports whether path is one of the operational endpoints.
func isProbe(path string) bool {
	return path == "/healthz" || path == "/readyz"
}

// traceRoute names the server span after the route that matched and publishes
// the trace identifier in the response.
//
// The name is set on the way out because chi only knows the pattern once it
// has routed. Naming a span "GET /jobs/{id}" rather than "GET /jobs/a1b2c3"
// is what keeps a trace backend able to aggregate: the second form produces a
// distinct operation for every job that has ever been polled.
func traceRoute(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if traceID := telemetry.TraceID(r.Context()); traceID != "" {
			w.Header().Set(TraceIDHeader, traceID)
		}

		//nolint:contextcheck // the closure reads the request context directly.
		defer func() {
			span := trace.SpanFromContext(r.Context())
			if !span.IsRecording() {
				return
			}
			if pattern := routePattern(r); pattern != "" {
				span.SetName(r.Method + " " + pattern)
			}
		}()

		next.ServeHTTP(w, r)
	})
}

// routePattern returns the chi route pattern that matched, or an empty string
// when nothing did.
func routePattern(r *http.Request) string {
	rctx := chi.RouteContext(r.Context())
	if rctx == nil {
		return ""
	}
	// A pattern accumulated through sub-routers keeps the wildcards that
	// joined them.
	return strings.ReplaceAll(rctx.RoutePattern(), "/*/", "/")
}

// echoRequestID copies the request identifier into the response headers.
//
// chi's RequestID middleware only places it in the context, but a client needs
// it in the response to quote when reporting a problem, and the error bodies
// already carry it.
func echoRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id := middleware.GetReqID(r.Context()); id != "" {
			w.Header().Set(middleware.RequestIDHeader, id)
		}
		next.ServeHTTP(w, r)
	})
}

// requestLogger logs one structured record per request, after it completes.
//
// It records the outcome rather than the arrival, so a single line carries the
// status, the size and the duration alongside the request identifier.
func requestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started := time.Now()
			wrapped := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

			//nolint:contextcheck // the closure reads the request context directly.
			defer func() {
				record := slog.Group("http",
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.Int("status", wrapped.Status()),
					slog.Int("bytes", wrapped.BytesWritten()),
					slog.Duration("duration", time.Since(started)),
					slog.String("remote_addr", r.RemoteAddr),
				)

				level := slog.LevelInfo
				switch {
				case wrapped.Status() >= http.StatusInternalServerError:
					level = slog.LevelError
				case wrapped.Status() >= http.StatusBadRequest:
					level = slog.LevelWarn
				}

				logger.LogAttrs(r.Context(), level, "request completed",
					slog.String("request_id", middleware.GetReqID(r.Context())),
					record)
			}()

			next.ServeHTTP(wrapped, r)
		})
	}
}

// recoverer turns a panic in a handler into a logged 500 rather than a dropped
// connection.
//
// http.ErrAbortHandler is deliberately re-panicked: it is the documented way
// for a handler to abandon a response, and net/http handles it itself.
func recoverer(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			//nolint:contextcheck // the closure reads the request context directly.
			defer func() {
				recovered := recover()
				if recovered == nil {
					return
				}
				if err, ok := recovered.(error); ok && err == http.ErrAbortHandler { //nolint:errorlint // identity is the documented check.
					panic(recovered)
				}

				logger.LogAttrs(r.Context(), slog.LevelError, "panic recovered",
					slog.String("request_id", middleware.GetReqID(r.Context())),
					slog.Any("panic", recovered),
					slog.String("stack", string(debug.Stack())))

				writeProblem(w, r, http.StatusInternalServerError, TypeInternal,
					"Internal Server Error", "The server encountered an unexpected condition.")
			}()

			next.ServeHTTP(w, r)
		})
	}
}
