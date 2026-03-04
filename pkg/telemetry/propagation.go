package telemetry

import (
	"context"
	"os"

	"go.opentelemetry.io/otel"
)

// envCarrier implements propagation.TextMapCarrier by reading W3C trace
// context headers from environment variables. This bridges the K8s Job
// boundary: the controller injects TRACEPARENT as a pod env var, and the
// agent-runner / IPC bridge extract it at startup.
type envCarrier struct{}

// Get maps W3C header names to environment variable names.
func (envCarrier) Get(key string) string {
	switch key {
	case "traceparent":
		return os.Getenv("TRACEPARENT")
	case "tracestate":
		return os.Getenv("TRACESTATE")
	}
	return ""
}

// Set is a no-op — we only extract from env, never inject.
func (envCarrier) Set(string, string) {}

// Keys returns the header names this carrier can provide.
func (envCarrier) Keys() []string {
	return []string{"traceparent", "tracestate"}
}

// ExtractParentFromEnv reads TRACEPARENT and TRACESTATE environment
// variables and returns a context carrying the remote span context.
//
// If TRACEPARENT is empty or malformed, the returned context has no parent
// span context and a new root trace will be started.
//
// This must be called after Init (which registers the W3C propagator).
func ExtractParentFromEnv(ctx context.Context) context.Context {
	return otel.GetTextMapPropagator().Extract(ctx, envCarrier{})
}
