package eventbus

import (
	"context"
	"testing"

	"github.com/nats-io/nats.go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// TestTraceContextPropagation verifies that InjectTraceContext and
// ExtractTraceContext correctly round-trip W3C traceparent through
// NATS message headers.
func TestTraceContextPropagation(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(exporter),
	)
	defer tp.Shutdown(context.Background())

	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	defer otel.SetTracerProvider(prev)

	prevProp := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	defer otel.SetTextMapPropagator(prevProp)

	// Create a span to get a real trace context.
	ctx := context.Background()
	tracer := tp.Tracer("test")
	ctx, span := tracer.Start(ctx, "publisher-span")
	defer span.End()

	originalTraceID := span.SpanContext().TraceID()
	originalSpanID := span.SpanContext().SpanID()

	// Inject into NATS headers (simulates Publish).
	headers := nats.Header{}
	InjectTraceContext(ctx, headers)

	// Verify traceparent header was set.
	tp_header := headers.Get("traceparent")
	if tp_header == "" {
		t.Fatal("traceparent header not set after InjectTraceContext")
	}

	// Extract from NATS headers (simulates Subscribe).
	extractedCtx := ExtractTraceContext(context.Background(), headers)
	extractedSC := trace.SpanContextFromContext(extractedCtx)

	// The extracted context should have the same trace ID.
	if extractedSC.TraceID() != originalTraceID {
		t.Errorf("extracted trace ID = %s, want %s", extractedSC.TraceID(), originalTraceID)
	}

	// The parent span ID should match the original span.
	if extractedSC.SpanID() != originalSpanID {
		t.Errorf("extracted span ID = %s, want %s", extractedSC.SpanID(), originalSpanID)
	}

	// The extracted context should be marked as remote.
	if !extractedSC.IsRemote() {
		t.Error("expected extracted span context to be marked as remote")
	}

	// The extracted context should be valid.
	if !extractedSC.IsValid() {
		t.Error("expected extracted span context to be valid")
	}
}

// TestExtractTraceContext_NilHeaders verifies that nil headers return
// the original context unchanged.
func TestExtractTraceContext_NilHeaders(t *testing.T) {
	ctx := context.Background()
	result := ExtractTraceContext(ctx, nil)

	sc := trace.SpanContextFromContext(result)
	if sc.IsValid() {
		t.Error("expected invalid span context from nil headers")
	}
}

// TestExtractTraceContext_EmptyHeaders verifies that empty headers return
// the original context unchanged.
func TestExtractTraceContext_EmptyHeaders(t *testing.T) {
	ctx := context.Background()
	result := ExtractTraceContext(ctx, nats.Header{})

	sc := trace.SpanContextFromContext(result)
	if sc.IsValid() {
		t.Error("expected invalid span context from empty headers")
	}
}

// TestNatsHeaderCarrier verifies the carrier interface implementation.
func TestNatsHeaderCarrier(t *testing.T) {
	h := nats.Header{}
	carrier := &natsHeaderCarrier{header: h}

	// Set and Get
	carrier.Set("traceparent", "00-abc-def-01")
	if got := carrier.Get("traceparent"); got != "00-abc-def-01" {
		t.Errorf("Get(traceparent) = %q, want %q", got, "00-abc-def-01")
	}

	// Keys
	carrier.Set("tracestate", "foo=bar")
	keys := carrier.Keys()
	if len(keys) != 2 {
		t.Errorf("Keys() returned %d keys, want 2", len(keys))
	}

	found := map[string]bool{}
	for _, k := range keys {
		found[k] = true
	}
	if !found["traceparent"] || !found["tracestate"] {
		t.Errorf("Keys() = %v, want [traceparent, tracestate]", keys)
	}
}
