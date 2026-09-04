package telemetry

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

// The attribute keys this package stamps onto every log record. They are the
// names to grep for, so they are named once, here.
const (
	// TraceIDKey correlates every line belonging to one job across both
	// services.
	TraceIDKey = "trace_id"
	// SpanIDKey identifies the individual operation within that trace.
	SpanIDKey = "span_id"
	// JobIDKey is the job the line is about.
	JobIDKey = "job_id"
)

// contextKey keeps this package's context values out of anyone else's
// namespace.
type contextKey int

const jobIDContextKey contextKey = iota

// WithJobID returns a context that names the job being worked on, so log
// records taken from it carry the identifier without every call site having to
// pass it along.
func WithJobID(ctx context.Context, jobID string) context.Context {
	if jobID == "" {
		return ctx
	}
	return context.WithValue(ctx, jobIDContextKey, jobID)
}

// JobID returns the job identifier in ctx, or an empty string when there is
// none.
func JobID(ctx context.Context) string {
	jobID, _ := ctx.Value(jobIDContextKey).(string)
	return jobID
}

// logHandler stamps the trace, span and job identifiers from a record's
// context onto the record itself.
//
// This is the piece that makes a trace searchable in the log. The API writes
// "upload accepted" and the worker writes "job processed" minutes later in a
// different process, and the only thing that ties them together is a trace id
// neither of them was asked to log by hand.
type logHandler struct {
	inner slog.Handler
}

// NewLogHandler wraps inner so every record it handles carries the identifiers
// found in the record's context.
//
// Records logged through the Context-taking slog methods — InfoContext,
// LogAttrs and their siblings — carry them. The plain Info and Error methods
// pass a background context and so cannot, which is the reason this codebase
// prefers the Context forms.
func NewLogHandler(inner slog.Handler) slog.Handler {
	if inner == nil {
		return nil
	}
	return &logHandler{inner: inner}
}

func (h *logHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *logHandler) Handle(ctx context.Context, record slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		record.AddAttrs(
			slog.String(TraceIDKey, sc.TraceID().String()),
			slog.String(SpanIDKey, sc.SpanID().String()),
		)
	}
	if jobID := JobID(ctx); jobID != "" && !hasAttr(record, JobIDKey) {
		record.AddAttrs(slog.String(JobIDKey, jobID))
	}
	return h.inner.Handle(ctx, record)
}

// hasAttr reports whether record already carries an attribute named key.
//
// Call sites that know the job identifier still log it themselves, because
// they should not depend on this handler being installed to say what they are
// talking about. Checking first is what stops those records ending up with the
// key twice.
func hasAttr(record slog.Record, key string) bool {
	found := false
	record.Attrs(func(attr slog.Attr) bool {
		if attr.Key == key {
			found = true
		}
		return !found
	})
	return found
}

// WithAttrs and WithGroup have to rewrap, or a logger derived with either one
// quietly loses the identifiers.
//
// A group opened on the logger nests these attributes inside it, since they
// are added to the record rather than ahead of the group. Nothing here opens
// one on a root logger, and the alternative — reordering the handler chain —
// costs more than it is worth.
func (h *logHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &logHandler{inner: h.inner.WithAttrs(attrs)}
}

func (h *logHandler) WithGroup(name string) slog.Handler {
	return &logHandler{inner: h.inner.WithGroup(name)}
}
