package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5/middleware"
)

// Machine-readable error codes returned in the "code" field.
const (
	codeInvalidMultipart      = "invalid_multipart"
	codeMissingFile           = "missing_file"
	codeInvalidSpec           = "invalid_spec"
	codeInvalidTransformation = "invalid_transformation"
	codePayloadTooLarge       = "payload_too_large"
	codeJobNotFound           = "job_not_found"
	codeNotFound              = "not_found"
	codeMethodNotAllowed      = "method_not_allowed"
	codeNotReady              = "not_ready"
	codeInternal              = "internal_error"
)

// errorResponse is the body of every non-2xx response.
type errorResponse struct {
	Error errorBody `json:"error"`
}

// errorBody describes a single failure.
type errorBody struct {
	// Code is a stable machine-readable identifier for the failure.
	Code string `json:"code"`
	// Message is a human-readable explanation, safe to show to the caller.
	Message string `json:"message"`
	// RequestID ties the failure to the server log.
	RequestID string `json:"request_id,omitempty"`
}

// writeJSON encodes v as the response body with the given status.
//
// The body is encoded before the header is written, so an encoding failure
// still produces a well-formed 500 rather than a truncated success.
func writeJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		slog.ErrorContext(r.Context(), "encoding response failed",
			slog.String("path", r.URL.Path),
			slog.String("error", err.Error()))
		http.Error(w, `{"error":{"code":"internal_error","message":"failed to encode the response"}}`,
			http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	// The client hanging up mid-write is not actionable here; the request log
	// already records the outcome.
	_, _ = w.Write(body)
}

// writeError responds with a JSON error body carrying the request identifier.
func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	writeJSON(w, r, status, errorResponse{Error: errorBody{
		Code:      code,
		Message:   message,
		RequestID: middleware.GetReqID(r.Context()),
	}})
}
