package httpapi

import (
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

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
