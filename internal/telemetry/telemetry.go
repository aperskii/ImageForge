// Package telemetry configures distributed tracing and joins it to the
// structured log, so one job can be followed from the upload request, through
// the queue, to the worker that finishes it.
//
// Two things are deliberately separate here. Spans are always recorded: a
// tracer provider is installed whether or not a collector is configured, so
// every log line carries a real trace identifier even on a laptop with nothing
// listening. An OTLP endpoint turns on exporting, and nothing else. That keeps
// the end-to-end story — grep one trace id, see every line from both services
// — available with no infrastructure at all, and makes a collector an upgrade
// rather than a prerequisite.
//
// Only adapters, commands and the worker pool import this package. The domain
// and the use cases stay free of it, as they are of every other detail of how
// this service happens to be operated.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

// ScopeName is the instrumentation scope every span this codebase creates is
// recorded under.
const ScopeName = "imageforge"

// exportTimeout bounds the flush of pending spans during shutdown. A process
// that is stopping should not wait long on a collector that is not answering.
const exportTimeout = 5 * time.Second

// Config describes what to trace and where to send it.
type Config struct {
	// ServiceName identifies the process in a trace: "imageforge-api" or
	// "imageforge-worker".
	ServiceName string
	// ServiceVersion is the build being traced, typically a commit sha.
	ServiceVersion string
	// Environment is the deployment name, such as "dev".
	Environment string
	// Endpoint is an OTLP/HTTP collector, given as "host:port" or as a full
	// URL. When empty, spans are recorded but never exported.
	Endpoint string
	// Insecure sends to the endpoint over plain HTTP. It is the right setting
	// for a collector on the same private network and the wrong one across
	// the internet.
	Insecure bool
	// SampleRatio is the fraction of traces to record, between 0 and 1.
	// Sampling is parent-based, so a decision taken by the API is honored by
	// the worker and no trace is ever half recorded. Zero selects 1.
	SampleRatio float64
	// Disabled turns tracing off altogether. Nothing is recorded, so log
	// records carry no trace identifiers either; it is the escape hatch, not
	// the way to stop exporting.
	Disabled bool
	// Logger receives the note about what tracing was configured to do.
	Logger *slog.Logger
}

// Provider owns the tracer provider a process installed, and shuts it down.
type Provider struct {
	tp        *sdktrace.TracerProvider
	exporting bool
}

// Setup installs a tracer provider and the W3C propagators as the process
// globals, and returns the handle that shuts them down.
//
// It fails only when the exporter cannot be built, which means the endpoint is
// malformed. It does not fail because the collector is unreachable: the
// exporter connects lazily and retries, so a collector that is down delays
// spans rather than stopping the service.
func Setup(ctx context.Context, cfg Config) (*Provider, error) {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.ServiceName == "" {
		cfg.ServiceName = ScopeName
	}
	if cfg.Disabled {
		logger.Info("tracing is disabled", slog.String("setting", EnvDisabled))
		return &Provider{}, nil
	}
	if cfg.SampleRatio <= 0 || cfg.SampleRatio > 1 {
		cfg.SampleRatio = 1
	}

	attrs := []attribute.KeyValue{semconv.ServiceName(cfg.ServiceName)}
	if cfg.ServiceVersion != "" {
		attrs = append(attrs, semconv.ServiceVersion(cfg.ServiceVersion))
	}
	if cfg.Environment != "" {
		attrs = append(attrs, semconv.DeploymentEnvironmentNameKey.String(cfg.Environment))
	}

	// Merging with the default resource picks up the SDK, process and host
	// attributes. The schema URL has to be the one the SDK's own resource
	// carries, or the merge reports a conflict and the result loses its
	// schema.
	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(semconv.SchemaURL, attrs...))
	if err != nil {
		return nil, fmt.Errorf("telemetry: build the resource: %w", err)
	}

	opts := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(res),
		// Parent-based, so the worker records the trace the API started
		// whenever the API decided to record it. Sampling the two halves of a
		// job independently is how a trace ends up missing the half that
		// explains it.
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRatio))),
	}

	endpoint := strings.TrimSpace(cfg.Endpoint)
	exporting := endpoint != ""
	if exporting {
		exporter, expErr := newExporter(ctx, endpoint, cfg.Insecure)
		if expErr != nil {
			return nil, expErr
		}
		opts = append(opts, sdktrace.WithBatcher(exporter, sdktrace.WithExportTimeout(exportTimeout)))
	}

	tp := sdktrace.NewTracerProvider(opts...)
	otel.SetTracerProvider(tp)

	// TraceContext is the W3C standard every other propagator understands,
	// and Baggage rides alongside it. Setting them globally is what lets the
	// otelhttp and otelaws instrumentation interoperate without being told.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	// The SDK reports its own failures through this handler, which otherwise
	// writes to standard error and bypasses the structured log entirely.
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		logger.Warn("the tracing pipeline reported an error", slog.String("error", err.Error()))
	}))

	if exporting {
		logger.Info("tracing configured",
			slog.String("service", cfg.ServiceName),
			slog.String("endpoint", endpoint),
			slog.Float64("sample_ratio", cfg.SampleRatio))
	} else {
		logger.Info("tracing configured without an exporter",
			slog.String("service", cfg.ServiceName),
			slog.String("detail", "trace ids appear in the log; set OTEL_EXPORTER_OTLP_ENDPOINT to export the spans as well"))
	}

	return &Provider{tp: tp, exporting: exporting}, nil
}

// newExporter builds the OTLP/HTTP exporter for endpoint, which may be a bare
// host:port or a full URL.
func newExporter(ctx context.Context, endpoint string, insecure bool) (*otlptrace.Exporter, error) {
	// The endpoint is checked here because the SDK will not check it: given a
	// malformed URL it writes a line to its own logger, keeps the default
	// endpoint and returns no error. A typo would then look exactly like a
	// working configuration that exports nothing.
	if err := validateEndpoint(endpoint); err != nil {
		return nil, err
	}

	var opts []otlptracehttp.Option

	switch {
	case strings.HasPrefix(endpoint, "http://"):
		opts = append(opts, otlptracehttp.WithEndpointURL(withSignalPath(endpoint)), otlptracehttp.WithInsecure())
	case strings.HasPrefix(endpoint, "https://"):
		opts = append(opts, otlptracehttp.WithEndpointURL(withSignalPath(endpoint)))
	default:
		opts = append(opts, otlptracehttp.WithEndpoint(endpoint))
		if insecure {
			opts = append(opts, otlptracehttp.WithInsecure())
		}
	}

	exporter, err := otlptracehttp.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("telemetry: build the OTLP exporter for %q: %w", endpoint, err)
	}
	return exporter, nil
}

// tracesPath is where an OTLP/HTTP collector receives spans.
const tracesPath = "/v1/traces"

// withSignalPath appends the OTLP traces path to a base URL that has none.
//
// A URL given without a path makes the exporter POST to "/", which every
// collector answers with 404 — and which shows up only as a warning long after
// startup said tracing was configured. A URL that already names a path is left
// alone, so a collector behind a prefix can still be addressed exactly.
func withSignalPath(endpoint string) string {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		// validateEndpoint has already run; leave a URL it accepted alone.
		return endpoint
	}
	if parsed.Path == "" || parsed.Path == "/" {
		parsed.Path = tracesPath
	}
	return parsed.String()
}

// validateEndpoint rejects an endpoint the exporter would not actually use.
//
// Both accepted forms are checked: a full URL has to parse and name a host,
// and a bare authority has to be a host with an optional numeric port.
func validateEndpoint(endpoint string) error {
	if strings.Contains(endpoint, "://") {
		parsed, err := url.Parse(endpoint)
		if err != nil {
			return fmt.Errorf("telemetry: build the OTLP exporter for %q: %w", endpoint, err)
		}
		if parsed.Host == "" {
			return fmt.Errorf("telemetry: build the OTLP exporter for %q: no host", endpoint)
		}
		return nil
	}

	if strings.ContainsAny(endpoint, "/?#") {
		return fmt.Errorf("telemetry: build the OTLP exporter for %q: "+
			"a bare endpoint is a host and port, so give a full URL to include a path", endpoint)
	}
	if !strings.Contains(endpoint, ":") {
		// A host on its own is fine; the exporter supplies the default port.
		return nil
	}

	host, port, err := net.SplitHostPort(endpoint)
	if err != nil {
		return fmt.Errorf("telemetry: build the OTLP exporter for %q: %w", endpoint, err)
	}
	if host == "" {
		return fmt.Errorf("telemetry: build the OTLP exporter for %q: no host", endpoint)
	}
	if _, err = strconv.Atoi(port); err != nil {
		return fmt.Errorf("telemetry: build the OTLP exporter for %q: %q is not a port", endpoint, port)
	}
	return nil
}

// Exporting reports whether spans are being sent anywhere.
func (p *Provider) Exporting() bool { return p != nil && p.exporting }

// Shutdown flushes pending spans and releases the provider. It is safe to call
// on a nil Provider, so a caller can defer it before checking the error Setup
// returned.
func (p *Provider) Shutdown(ctx context.Context) error {
	if p == nil || p.tp == nil {
		return nil
	}
	if err := p.tp.Shutdown(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("telemetry: shutdown: %w", err)
	}
	return nil
}

// Tracer returns the tracer this codebase records spans with.
func Tracer() trace.Tracer { return otel.Tracer(ScopeName) }

// Start begins a span on the package tracer.
func Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	return Tracer().Start(ctx, name, opts...)
}

// End closes span, recording err on it first when there is one.
//
// It exists because the "record, set status, end" sequence is easy to get half
// right, and a span that ends without its error looks like a success in every
// trace view there is.
func End(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
}

// TraceID returns the trace identifier in ctx, or an empty string when ctx
// carries no span.
func TraceID(ctx context.Context) string {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.HasTraceID() {
		return ""
	}
	return sc.TraceID().String()
}
