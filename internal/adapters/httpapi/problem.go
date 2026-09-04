package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5/middleware"

	"imageforge/internal/telemetry"
)

// ProblemContentType is the media type RFC 7807 defines for these responses.
const ProblemContentType = "application/problem+json"

// ProblemBaseURI namespaces the problem types this service defines.
//
// RFC 7807 wants a URI that identifies the problem type; it need not resolve,
// but a stable one lets a client switch on it without parsing prose.
const ProblemBaseURI = "https://imageforge.dev/problems/"

// The problem types this API returns. They are the stable part of an error
// response: the title and detail are for people and may be reworded.
const (
	TypeUnauthorized      = ProblemBaseURI + "unauthorized"
	TypeRateLimited       = ProblemBaseURI + "rate-limited"
	TypeInvalidMultipart  = ProblemBaseURI + "invalid-multipart"
	TypeMissingFile       = ProblemBaseURI + "missing-file"
	TypeInvalidSpec       = ProblemBaseURI + "invalid-spec"
	TypeInvalidTransform  = ProblemBaseURI + "invalid-transformation"
	TypeUnsupportedMedia  = ProblemBaseURI + "unsupported-media-type"
	TypePayloadTooLarge   = ProblemBaseURI + "payload-too-large"
	TypeJobNotFound       = ProblemBaseURI + "job-not-found"
	TypeNotFound          = ProblemBaseURI + "not-found"
	TypeMethodNotAllowed  = ProblemBaseURI + "method-not-allowed"
	TypeNotReady          = ProblemBaseURI + "not-ready"
	TypeInternal          = ProblemBaseURI + "internal-error"
	TypeInvalidCredential = ProblemBaseURI + "invalid-credentials"
)

// Problem is an RFC 7807 problem detail.
//
// RequestID and RetryAfter are extension members, which the RFC allows: the
// first ties a failure to the server log, the second repeats the Retry-After
// header somewhere a JSON client will actually look.
type Problem struct {
	// Type identifies the kind of problem.
	Type string `json:"type"`
	// Title is a short, stable summary of that kind.
	Title string `json:"title"`
	// Status is the HTTP status code, repeated in the body per the RFC.
	Status int `json:"status"`
	// Detail explains this particular occurrence.
	Detail string `json:"detail,omitempty"`
	// Instance identifies this occurrence, here the path that produced it.
	Instance string `json:"instance,omitempty"`
	// RequestID ties the failure to the server log.
	RequestID string `json:"request_id,omitempty"`
	// TraceID ties it to the distributed trace, which reaches further than the
	// request identifier does: it also covers the worker that picks the job up
	// afterwards.
	TraceID string `json:"trace_id,omitempty"`
	// RetryAfter is the seconds to wait before retrying, when that is known.
	RetryAfter int `json:"retry_after,omitempty"`
}

// writeProblem responds with an RFC 7807 problem detail.
func writeProblem(w http.ResponseWriter, r *http.Request, status int, problemType, title, detail string) {
	writeProblemWith(w, r, Problem{
		Type:   problemType,
		Title:  title,
		Status: status,
		Detail: detail,
	})
}

// writeProblemWith responds with a problem whose extension members the caller
// has already filled in.
func writeProblemWith(w http.ResponseWriter, r *http.Request, problem Problem) {
	problem.Instance = r.URL.Path
	if problem.RequestID == "" {
		problem.RequestID = middleware.GetReqID(r.Context())
	}
	if problem.TraceID == "" {
		problem.TraceID = telemetry.TraceID(r.Context())
	}

	body, err := json.Marshal(problem)
	if err != nil {
		// A Problem is a handful of strings and ints, so this cannot fail in
		// practice; answer with a valid problem anyway rather than nothing.
		slog.ErrorContext(r.Context(), "encoding a problem response failed",
			slog.String("path", r.URL.Path),
			slog.String("error", err.Error()))
		w.Header().Set("Content-Type", ProblemContentType)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"type":"` + TypeInternal +
			`","title":"Internal Server Error","status":500}`))
		return
	}

	w.Header().Set("Content-Type", ProblemContentType)
	w.WriteHeader(problem.Status)
	// The client hanging up mid-write is not actionable; the request log
	// already records the outcome.
	_, _ = w.Write(body)
}

// writeJSON encodes v as an ordinary JSON response with the given status.
//
// Successful responses stay application/json; only errors are problem+json.
func writeJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		slog.ErrorContext(r.Context(), "encoding a response failed",
			slog.String("path", r.URL.Path),
			slog.String("error", err.Error()))
		writeProblem(w, r, http.StatusInternalServerError, TypeInternal,
			"Internal Server Error", "The response could not be encoded.")
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// slogPath is the request path as a log attribute.
func slogPath(r *http.Request) slog.Attr {
	return slog.String("path", r.URL.Path)
}

// slogError is an error as a log attribute, tolerating a nil error.
func slogError(err error) slog.Attr {
	if err == nil {
		return slog.String("error", "")
	}
	return slog.String("error", err.Error())
}
