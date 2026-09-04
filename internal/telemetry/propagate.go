package telemetry

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// Carrier moves trace context as plain string pairs, which is all a queue
// message attribute, an HTTP header or a database column can hold.
//
// The keys are the W3C ones — "traceparent" and "tracestate" — so a message
// this service publishes can be read by anything that speaks trace context,
// and vice versa.
type Carrier map[string]string

// Compile-time assertion that a Carrier can be handed to a propagator.
var _ propagation.TextMapCarrier = Carrier(nil)

// Get returns the value for key, or an empty string.
func (c Carrier) Get(key string) string { return c[key] }

// Set stores value under key, allocating nothing: a nil Carrier cannot be
// written to, so Inject builds the map before injecting into it.
func (c Carrier) Set(key, value string) { c[key] = value }

// Keys lists the keys the carrier holds.
func (c Carrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for key := range c {
		keys = append(keys, key)
	}
	return keys
}

// Inject returns the trace context in ctx as a carrier ready to travel with a
// message, or nil when ctx carries no trace.
//
// Returning nil rather than an empty map is deliberate: a queue adapter can
// then decide whether to send message attributes at all, and an untraced
// message costs nothing.
func Inject(ctx context.Context) Carrier {
	carrier := Carrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	if len(carrier) == 0 {
		return nil
	}
	return carrier
}

// Extract returns a context continuing the trace described by carrier.
//
// A message with no trace context, or with one this service cannot parse,
// yields ctx unchanged, so a span started from it begins a new trace instead
// of failing. Losing the link between two halves of a job is a worse outcome
// than a broken message, but it is not a reason to drop the job.
func Extract(ctx context.Context, carrier Carrier) context.Context {
	if len(carrier) == 0 {
		return ctx
	}
	return otel.GetTextMapPropagator().Extract(ctx, carrier)
}
