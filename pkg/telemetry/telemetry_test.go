package telemetry

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// --- Init / Noop tests ---

func TestInit_WithoutEndpoint_ReturnsNoop(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	tel, err := Init(context.Background(), Config{
		ServiceName: "test-service",
	})
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	if tel.IsEnabled() {
		t.Error("expected IsEnabled() = false when no endpoint set")
	}

	// Noop tracer/meter should still be usable without panic.
	tracer := tel.Tracer()
	if tracer == nil {
		t.Error("expected non-nil Tracer from noop")
	}

	meter := tel.Meter()
	if meter == nil {
		t.Error("expected non-nil Meter from noop")
	}

	logger := tel.Logger()
	if logger == nil {
		t.Error("expected non-nil Logger from noop")
	}

	// TracerProvider and MeterProvider should be nil for noop.
	if tel.TracerProvider() != nil {
		t.Error("expected nil TracerProvider for noop")
	}
	if tel.MeterProvider() != nil {
		t.Error("expected nil MeterProvider for noop")
	}
}

func TestInit_NoopShutdown(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	tel, err := Init(context.Background(), Config{ServiceName: "test"})
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	// Shutdown on noop should succeed immediately.
	if err := tel.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown() error = %v", err)
	}
}

func TestInit_NoopSpansWork(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	tel, err := Init(context.Background(), Config{ServiceName: "test"})
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	ctx := context.Background()
	ctx, span := tel.Tracer().Start(ctx, "test-span")
	defer span.End()

	// Noop span should not panic and should have invalid span context.
	sc := span.SpanContext()
	if sc.IsValid() {
		t.Error("expected invalid span context from noop tracer")
	}
}

// --- Config defaults ---

func TestConfig_Defaults(t *testing.T) {
	cfg := Config{ServiceName: "test"}
	cfg.applyDefaults()

	if cfg.BatchTimeout != defaultBatchTimeout {
		t.Errorf("BatchTimeout = %v, want %v", cfg.BatchTimeout, defaultBatchTimeout)
	}
	if cfg.ShutdownTimeout != defaultShutdownTimeout {
		t.Errorf("ShutdownTimeout = %v, want %v", cfg.ShutdownTimeout, defaultShutdownTimeout)
	}
	if cfg.SamplingRatio != defaultSamplingRatio {
		t.Errorf("SamplingRatio = %v, want %v", cfg.SamplingRatio, defaultSamplingRatio)
	}
}

func TestConfig_CustomValues(t *testing.T) {
	cfg := Config{
		ServiceName:     "test",
		BatchTimeout:    1 * time.Second,
		ShutdownTimeout: 10 * time.Second,
		SamplingRatio:   0.5,
		Namespace:       "production",
	}
	cfg.applyDefaults()

	if cfg.BatchTimeout != 1*time.Second {
		t.Errorf("BatchTimeout = %v, want 1s", cfg.BatchTimeout)
	}
	if cfg.ShutdownTimeout != 10*time.Second {
		t.Errorf("ShutdownTimeout = %v, want 10s", cfg.ShutdownTimeout)
	}
	if cfg.SamplingRatio != 0.5 {
		t.Errorf("SamplingRatio = %v, want 0.5", cfg.SamplingRatio)
	}
	if cfg.Namespace != "production" {
		t.Errorf("Namespace = %v, want production", cfg.Namespace)
	}
}

func TestConfig_NamespaceFromEnv(t *testing.T) {
	t.Setenv("NAMESPACE", "kube-system")

	cfg := Config{ServiceName: "test"}
	cfg.applyDefaults()

	if cfg.Namespace != "kube-system" {
		t.Errorf("Namespace = %v, want kube-system", cfg.Namespace)
	}
}

// --- Resource tests ---

func TestBuildResource_AllFields(t *testing.T) {
	t.Setenv("POD_NAME", "agent-run-abc123")
	t.Setenv("NODE_NAME", "worker-1")
	t.Setenv("INSTANCE_NAME", "my-assistant")

	cfg := Config{
		ServiceName:    "sympozium-agent-runner",
		ServiceVersion: "v0.0.49",
		Namespace:      "default",
	}

	res, err := buildResource(cfg)
	if err != nil {
		t.Fatalf("buildResource() error = %v", err)
	}

	attrs := resourceAttrs(res)

	assertAttr(t, attrs, "service.name", "sympozium-agent-runner")
	assertAttr(t, attrs, "service.version", "v0.0.49")
	assertAttr(t, attrs, "k8s.namespace.name", "default")
	assertAttr(t, attrs, "k8s.pod.name", "agent-run-abc123")
	assertAttr(t, attrs, "k8s.node.name", "worker-1")
	assertAttr(t, attrs, "sympozium.instance", "my-assistant")
	assertAttr(t, attrs, "sympozium.component", "agent-runner")
}

func TestBuildResource_MissingEnv(t *testing.T) {
	// Ensure env vars are not set.
	t.Setenv("POD_NAME", "")
	t.Setenv("NODE_NAME", "")
	t.Setenv("INSTANCE_NAME", "")

	cfg := Config{
		ServiceName: "sympozium-controller",
		Namespace:   "default",
	}

	res, err := buildResource(cfg)
	if err != nil {
		t.Fatalf("buildResource() error = %v", err)
	}

	attrs := resourceAttrs(res)

	assertAttr(t, attrs, "service.name", "sympozium-controller")
	assertAttr(t, attrs, "sympozium.component", "controller")

	// These should not be present.
	if _, ok := attrs["k8s.pod.name"]; ok {
		t.Error("expected k8s.pod.name to be absent when POD_NAME is empty")
	}
	if _, ok := attrs["k8s.node.name"]; ok {
		t.Error("expected k8s.node.name to be absent when NODE_NAME is empty")
	}
	if _, ok := attrs["sympozium.instance"]; ok {
		t.Error("expected sympozium.instance to be absent when INSTANCE_NAME is empty")
	}
}

func TestBuildResource_ComponentDerivation(t *testing.T) {
	tests := []struct {
		serviceName string
		wantComp    string
	}{
		{"sympozium-agent-runner", "agent-runner"},
		{"sympozium-controller", "controller"},
		{"sympozium-ipc-bridge", "ipc-bridge"},
		{"sympozium-channel-telegram", "channel-telegram"},
		{"custom-service", "custom-service"},
	}

	for _, tt := range tests {
		t.Run(tt.serviceName, func(t *testing.T) {
			cfg := Config{ServiceName: tt.serviceName}
			res, err := buildResource(cfg)
			if err != nil {
				t.Fatalf("buildResource() error = %v", err)
			}
			attrs := resourceAttrs(res)
			assertAttr(t, attrs, "sympozium.component", tt.wantComp)
		})
	}
}

// --- Propagation tests ---

func TestExtractParentFromEnv_Valid(t *testing.T) {
	// Register W3C propagator (normally done by Init).
	otel.SetTextMapPropagator(propagation.TraceContext{})

	t.Setenv("TRACEPARENT", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")

	ctx := ExtractParentFromEnv(context.Background())
	sc := trace.SpanContextFromContext(ctx)

	if !sc.IsValid() {
		t.Fatal("expected valid SpanContext after extracting TRACEPARENT")
	}
	if !sc.IsRemote() {
		t.Error("expected remote SpanContext")
	}
	if sc.TraceID().String() != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("TraceID = %s, want 4bf92f3577b34da6a3ce929d0e0e4736", sc.TraceID())
	}
	if sc.SpanID().String() != "00f067aa0ba902b7" {
		t.Errorf("SpanID = %s, want 00f067aa0ba902b7", sc.SpanID())
	}
	if !sc.IsSampled() {
		t.Error("expected sampled flag to be set (flags=01)")
	}
}

func TestExtractParentFromEnv_Empty(t *testing.T) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Setenv("TRACEPARENT", "")

	ctx := ExtractParentFromEnv(context.Background())
	sc := trace.SpanContextFromContext(ctx)

	if sc.IsValid() {
		t.Error("expected invalid SpanContext when TRACEPARENT is empty")
	}
}

func TestExtractParentFromEnv_Malformed(t *testing.T) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Setenv("TRACEPARENT", "not-a-valid-traceparent")

	ctx := ExtractParentFromEnv(context.Background())
	sc := trace.SpanContextFromContext(ctx)

	if sc.IsValid() {
		t.Error("expected invalid SpanContext when TRACEPARENT is malformed")
	}
}

func TestExtractParentFromEnv_WithTraceState(t *testing.T) {
	otel.SetTextMapPropagator(propagation.TraceContext{})

	t.Setenv("TRACEPARENT", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	t.Setenv("TRACESTATE", "vendorname=opaquevalue")

	ctx := ExtractParentFromEnv(context.Background())
	sc := trace.SpanContextFromContext(ctx)

	if !sc.IsValid() {
		t.Fatal("expected valid SpanContext")
	}
	if sc.TraceState().Len() == 0 {
		t.Error("expected non-empty TraceState")
	}
}

// --- envCarrier unit tests ---

func TestEnvCarrier_Get(t *testing.T) {
	t.Setenv("TRACEPARENT", "00-abc-def-01")
	t.Setenv("TRACESTATE", "key=val")

	c := envCarrier{}

	if got := c.Get("traceparent"); got != "00-abc-def-01" {
		t.Errorf("Get(traceparent) = %q, want %q", got, "00-abc-def-01")
	}
	if got := c.Get("tracestate"); got != "key=val" {
		t.Errorf("Get(tracestate) = %q, want %q", got, "key=val")
	}
	if got := c.Get("unknown"); got != "" {
		t.Errorf("Get(unknown) = %q, want empty", got)
	}
}

func TestEnvCarrier_Keys(t *testing.T) {
	c := envCarrier{}
	keys := c.Keys()
	if len(keys) != 2 {
		t.Errorf("Keys() len = %d, want 2", len(keys))
	}
}

// --- Integration test: Init creates working span hierarchy ---

func TestInit_SpanCreation_WithInMemoryExporter(t *testing.T) {
	// This test uses the SDK directly (not Init) to avoid needing a real
	// gRPC endpoint, while validating the same span creation patterns.
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(exporter),
	)
	defer func() { _ = tp.Shutdown(context.Background()) }()

	tracer := tp.Tracer("test-service")

	// Simulate agent.run → gen_ai.chat → tool.execute hierarchy.
	ctx := context.Background()

	ctx, rootSpan := tracer.Start(ctx, "agent.run")

	_, chatSpan := tracer.Start(ctx, "gen_ai.chat")
	chatSpan.End()

	_, toolSpan := tracer.Start(ctx, "tool.execute")
	toolSpan.End()

	rootSpan.End()

	spans := exporter.GetSpans()
	if len(spans) != 3 {
		t.Fatalf("got %d spans, want 3", len(spans))
	}

	// Verify all spans share the same trace ID.
	traceID := spans[0].SpanContext.TraceID()
	for _, s := range spans {
		if s.SpanContext.TraceID() != traceID {
			t.Errorf("span %q has trace ID %s, want %s", s.Name, s.SpanContext.TraceID(), traceID)
		}
	}

	// Verify child spans have root as parent.
	rootSpanID := findSpanByName(spans, "agent.run").SpanContext.SpanID()

	chatStub := findSpanByName(spans, "gen_ai.chat")
	if chatStub.Parent.SpanID() != rootSpanID {
		t.Errorf("gen_ai.chat parent = %s, want %s", chatStub.Parent.SpanID(), rootSpanID)
	}

	toolStub := findSpanByName(spans, "tool.execute")
	if toolStub.Parent.SpanID() != rootSpanID {
		t.Errorf("tool.execute parent = %s, want %s", toolStub.Parent.SpanID(), rootSpanID)
	}
}

func TestInit_ParentPropagation_WithInMemoryExporter(t *testing.T) {
	// Verify that a parent span context from TRACEPARENT is correctly
	// linked when creating child spans.
	otel.SetTextMapPropagator(propagation.TraceContext{})

	t.Setenv("TRACEPARENT", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(exporter),
	)
	defer func() { _ = tp.Shutdown(context.Background()) }()

	tracer := tp.Tracer("test-service")

	// Extract parent from env (simulating agent-runner startup).
	ctx := ExtractParentFromEnv(context.Background())

	// Create a child span.
	_, span := tracer.Start(ctx, "agent.run")
	span.End()

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}

	s := spans[0]
	// The child span should inherit the trace ID from TRACEPARENT.
	if s.SpanContext.TraceID().String() != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("child TraceID = %s, want 4bf92f3577b34da6a3ce929d0e0e4736", s.SpanContext.TraceID())
	}
	// The parent span ID should be the one from TRACEPARENT.
	if s.Parent.SpanID().String() != "00f067aa0ba902b7" {
		t.Errorf("child ParentSpanID = %s, want 00f067aa0ba902b7", s.Parent.SpanID())
	}
}

// --- helpers ---

func resourceAttrs(res *resource.Resource) map[string]string {
	m := make(map[string]string)
	for _, kv := range res.Attributes() {
		m[string(kv.Key)] = kv.Value.Emit()
	}
	return m
}

func assertAttr(t *testing.T, attrs map[string]string, key, want string) {
	t.Helper()
	got, ok := attrs[key]
	if !ok {
		t.Errorf("missing attribute %q", key)
		return
	}
	if got != want {
		t.Errorf("attribute %q = %q, want %q", key, got, want)
	}
}

func findSpanByName(spans tracetest.SpanStubs, name string) *tracetest.SpanStub {
	for i := range spans {
		if spans[i].Name == name {
			return &spans[i]
		}
	}
	return nil
}
