package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestLogger returns a logger writing JSON through the handler under test,
// and the buffer it writes to.
func newTestLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	handler := NewLogHandler(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return slog.New(handler), &buf
}

// decodeRecord parses the single JSON record the buffer holds.
func decodeRecord(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()

	line := strings.TrimSpace(buf.String())
	require.NotEmpty(t, line, "expected a log record")
	require.NotContains(t, line, "\n", "expected exactly one log record")

	var record map[string]any
	require.NoError(t, json.Unmarshal([]byte(line), &record))
	return record
}

func TestSetupWithoutAnEndpointStillRecordsSpans(t *testing.T) {
	// The whole point of recording without exporting: a developer with no
	// collector still gets trace ids in the log.
	provider := setupForTest(t, Config{ServiceName: "test"})
	assert.False(t, provider.Exporting())

	ctx, span := Start(context.Background(), "unit")
	defer span.End()

	assert.True(t, span.SpanContext().IsValid())
	assert.Len(t, TraceID(ctx), 32, "a trace id is 16 bytes as hex")
}

func TestSetupRejectsAMalformedEndpoint(t *testing.T) {
	_, err := Setup(context.Background(), Config{
		ServiceName: "test",
		Endpoint:    "http://[::1]:not-a-port/v1/traces",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "OTLP exporter")
}

func TestDisabledSetupRecordsNothing(t *testing.T) {
	provider, err := Setup(context.Background(), Config{ServiceName: "test", Disabled: true})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, provider.Shutdown(context.Background())) })

	assert.False(t, provider.Exporting())
	assert.Empty(t, TraceID(context.Background()))
}

func TestShutdownOnANilProviderIsSafe(t *testing.T) {
	// Callers defer the shutdown before checking the error from Setup, so a
	// nil provider has to be survivable.
	var provider *Provider
	assert.NoError(t, provider.Shutdown(context.Background()))
}

func TestLogHandlerAddsTheTraceAndJobIdentifiers(t *testing.T) {
	setupForTest(t, Config{ServiceName: "test"})
	logger, buf := newTestLogger()

	ctx, span := Start(context.Background(), "unit")
	defer span.End()
	ctx = WithJobID(ctx, "job-42")

	logger.InfoContext(ctx, "processed")

	record := decodeRecord(t, buf)
	assert.Equal(t, span.SpanContext().TraceID().String(), record[TraceIDKey])
	assert.Equal(t, span.SpanContext().SpanID().String(), record[SpanIDKey])
	assert.Equal(t, "job-42", record[JobIDKey])
}

func TestLogHandlerLeavesAnUntracedRecordAlone(t *testing.T) {
	setupForTest(t, Config{ServiceName: "test"})
	logger, buf := newTestLogger()

	logger.InfoContext(context.Background(), "started")

	record := decodeRecord(t, buf)
	assert.NotContains(t, record, TraceIDKey)
	assert.NotContains(t, record, JobIDKey)
}

func TestLogHandlerDoesNotRepeatAJobIdentifierTheCallerLogged(t *testing.T) {
	// Call sites log the job id themselves so they do not depend on this
	// handler being installed. Emitting the key twice would produce a record
	// no JSON consumer agrees on.
	setupForTest(t, Config{ServiceName: "test"})
	logger, buf := newTestLogger()

	ctx := WithJobID(context.Background(), "job-42")
	logger.InfoContext(ctx, "processed", slog.String(JobIDKey, "job-42"))

	assert.Equal(t, 1, strings.Count(buf.String(), `"`+JobIDKey+`"`))
}

func TestLogHandlerSurvivesWithAttrs(t *testing.T) {
	// A derived logger must not silently lose the identifiers, which is what
	// happens when WithAttrs forgets to rewrap.
	setupForTest(t, Config{ServiceName: "test"})
	logger, buf := newTestLogger()

	ctx, span := Start(context.Background(), "unit")
	defer span.End()

	logger.With(slog.String("component", "worker")).InfoContext(ctx, "processed")

	record := decodeRecord(t, buf)
	assert.Equal(t, "worker", record["component"])
	assert.Equal(t, span.SpanContext().TraceID().String(), record[TraceIDKey])
}

func TestJobIDRoundTrips(t *testing.T) {
	assert.Empty(t, JobID(context.Background()))
	assert.Equal(t, "job-1", JobID(WithJobID(context.Background(), "job-1")))
	// An empty identifier is not worth a context value.
	assert.Empty(t, JobID(WithJobID(context.Background(), "")))
}

func TestInjectAndExtractCarryTheTraceAcrossAQueue(t *testing.T) {
	setupForTest(t, Config{ServiceName: "test"})

	producerCtx, producerSpan := Start(context.Background(), "enqueue")
	defer producerSpan.End()

	carrier := Inject(producerCtx)
	require.NotNil(t, carrier, "a recorded span must produce a carrier")
	assert.Contains(t, carrier, "traceparent")

	// The consumer starts from a context that shares nothing with the
	// producer's, exactly as a worker in another process would.
	consumerCtx, consumerSpan := Start(Extract(context.Background(), carrier), "process")
	defer consumerSpan.End()

	assert.Equal(t, TraceID(producerCtx), TraceID(consumerCtx),
		"the two halves of a job belong to one trace")
	assert.NotEqual(t, producerSpan.SpanContext().SpanID(), consumerSpan.SpanContext().SpanID())
}

func TestExtractIgnoresAnUnusableCarrier(t *testing.T) {
	setupForTest(t, Config{ServiceName: "test"})

	for name, carrier := range map[string]Carrier{
		"nil":     nil,
		"empty":   {},
		"garbage": {"traceparent": "not a trace context"},
	} {
		t.Run(name, func(t *testing.T) {
			// A message that cannot be parsed still has to be processed, so
			// the worst outcome allowed here is a trace that starts fresh.
			ctx := Extract(context.Background(), carrier)
			_, span := Start(ctx, "process")
			defer span.End()

			assert.True(t, span.SpanContext().IsValid())
		})
	}
}

func TestInjectWithoutASpanCarriesNothing(t *testing.T) {
	setupForTest(t, Config{ServiceName: "test"})

	assert.Nil(t, Inject(context.Background()),
		"an untraced enqueue should not send empty message attributes")
}

func TestConfigFromEnvReadsTheStandardVariables(t *testing.T) {
	t.Setenv(EnvEndpoint, "collector:4318")
	t.Setenv(EnvServiceName, "renamed")
	t.Setenv(EnvSamplerArg, "0.25")
	t.Setenv(EnvEnvironment, "dev")

	cfg := ConfigFromEnv("imageforge-api", "abc123")

	assert.Equal(t, "renamed", cfg.ServiceName)
	assert.Equal(t, "abc123", cfg.ServiceVersion)
	assert.Equal(t, "collector:4318", cfg.Endpoint)
	assert.InDelta(t, 0.25, cfg.SampleRatio, 1e-9)
	assert.Equal(t, "dev", cfg.Environment)
	assert.True(t, cfg.Insecure)
	assert.False(t, cfg.Disabled)
}

func TestConfigFromEnvPrefersTheTracesEndpoint(t *testing.T) {
	t.Setenv(EnvEndpoint, "shared:4318")
	t.Setenv(EnvTracesEndpoint, "https://traces.example.com/v1/traces")

	cfg := ConfigFromEnv("imageforge-api", "")

	assert.Equal(t, "https://traces.example.com/v1/traces", cfg.Endpoint)
	assert.False(t, cfg.Insecure, "an https endpoint is not insecure")
}

func TestConfigFromEnvFallsBackOnUnparseableValues(t *testing.T) {
	t.Setenv(EnvSamplerArg, "half")
	t.Setenv(EnvDisabled, "perhaps")

	cfg := ConfigFromEnv("imageforge-api", "")

	// A typo in an operational setting must not stop the service, and the
	// default errs towards recording rather than towards silence.
	assert.InDelta(t, 1.0, cfg.SampleRatio, 1e-9)
	assert.False(t, cfg.Disabled)
}

// setupForTest installs a provider for the duration of one test.
//
// The tracer provider is a process global, so these tests cannot run in
// parallel with each other; none of them call t.Parallel.
func setupForTest(t *testing.T, cfg Config) *Provider {
	t.Helper()

	cfg.Logger = slog.New(slog.DiscardHandler)
	provider, err := Setup(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, provider.Shutdown(context.Background())) })

	return provider
}

func TestWithSignalPath(t *testing.T) {
	for name, tc := range map[string]struct{ endpoint, want string }{
		"bare base URL":  {"http://jaeger:4318", "http://jaeger:4318/v1/traces"},
		"trailing slash": {"http://jaeger:4318/", "http://jaeger:4318/v1/traces"},
		"explicit path":  {"http://jaeger:4318/v1/traces", "http://jaeger:4318/v1/traces"},
		"collector behind a prefix": {
			"https://otel.example.com/ingest/v1/traces",
			"https://otel.example.com/ingest/v1/traces",
		},
	} {
		t.Run(name, func(t *testing.T) {
			// A base URL with no path makes the exporter POST to "/", which
			// every collector answers with a 404 that surfaces minutes after
			// startup has already claimed tracing works.
			assert.Equal(t, tc.want, withSignalPath(tc.endpoint))
		})
	}
}
