package telemetry

import (
	"os"
	"strconv"
	"strings"
)

// The environment variables read by ConfigFromEnv.
//
// The OTEL_ names are the ones the OpenTelemetry specification defines, so an
// operator who has configured any other instrumented service already knows
// them, and a collector's own documentation applies unchanged.
const (
	// EnvEndpoint is the OTLP collector for every signal.
	EnvEndpoint = "OTEL_EXPORTER_OTLP_ENDPOINT"
	// EnvTracesEndpoint overrides EnvEndpoint for traces alone.
	EnvTracesEndpoint = "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"
	// EnvServiceName overrides the name the process reports itself under.
	EnvServiceName = "OTEL_SERVICE_NAME"
	// EnvSamplerArg is the sampling ratio, between 0 and 1.
	EnvSamplerArg = "OTEL_TRACES_SAMPLER_ARG"
	// EnvDisabled turns tracing off altogether, including the trace ids in the
	// log.
	EnvDisabled = "OTEL_SDK_DISABLED"
	// EnvEnvironment names the deployment, and is this project's own rather
	// than a standard one.
	EnvEnvironment = "IMAGEFORGE_ENV"
)

// ConfigFromEnv reads a tracing configuration from the environment, falling
// back to a configuration that records spans and exports nothing.
//
// serviceName and version are what the process knows about itself;
// OTEL_SERVICE_NAME overrides the first, because an operator running two
// copies of the same binary needs to tell them apart.
func ConfigFromEnv(serviceName, version string) Config {
	endpoint := env(EnvTracesEndpoint, env(EnvEndpoint, ""))

	return Config{
		ServiceName:    env(EnvServiceName, serviceName),
		ServiceVersion: version,
		Environment:    env(EnvEnvironment, ""),
		Endpoint:       endpoint,
		// A collector addressed over plain http:// is being reached across a
		// private network, which is the usual arrangement and the one
		// docker-compose sets up here.
		Insecure:    !strings.HasPrefix(endpoint, "https://"),
		SampleRatio: envFloat(EnvSamplerArg, 1),
		Disabled:    envBool(EnvDisabled, false),
	}
}

// env returns the value of key, trimmed, or fallback when it is unset or
// blank.
func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return fallback
}

// envFloat reads key as a number, returning fallback when it is unset or
// unparseable. A bad value is not worth failing a startup over: the default
// records everything, which is the safe direction to be wrong in.
func envFloat(key string, fallback float64) float64 {
	raw, ok := os.LookupEnv(key)
	if !ok {
		return fallback
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return fallback
	}
	return v
}

// envBool reads key as a boolean, returning fallback when it is unset or
// unparseable.
func envBool(key string, fallback bool) bool {
	raw, ok := os.LookupEnv(key)
	if !ok {
		return fallback
	}
	v, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return fallback
	}
	return v
}
