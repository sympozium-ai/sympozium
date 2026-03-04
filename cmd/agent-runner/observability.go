package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alexsjones/sympozium/pkg/telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

type agentObservability struct {
	enabled bool
	tracer  trace.Tracer

	shutdown func(context.Context) error

	agentRuns       metric.Int64Counter
	agentRunDurMs   metric.Float64Histogram
	inTok           metric.Int64Counter
	outTok          metric.Int64Counter
	toolInvocations metric.Int64Counter
	skillDurMs      metric.Float64Histogram
}

var obs = &agentObservability{
	tracer:   otel.Tracer("sympozium/agent-runner"),
	shutdown: func(context.Context) error { return nil },
}

// initObservability initialises the OTel SDK via pkg/telemetry, which is the
// shared init path used by all Sympozium components. Agent-specific span
// helpers and metric instruments are layered on top.
func initObservability(ctx context.Context) *agentObservability {
	enabled := strings.EqualFold(getEnv("SYMPOZIUM_OTEL_ENABLED", ""), "true")
	if !enabled {
		return obs
	}

	// Resolve endpoint from SYMPOZIUM_OTEL_* or standard OTEL_* env vars.
	endpoint := firstNonEmpty(
		getEnv("SYMPOZIUM_OTEL_OTLP_ENDPOINT", ""),
		getEnv("OTEL_EXPORTER_OTLP_ENDPOINT", ""),
	)
	if endpoint == "" {
		log.Println("observability enabled but no OTLP endpoint set; skipping OTel bootstrap")
		return obs
	}
	// pkg/telemetry.Init reads OTEL_EXPORTER_OTLP_ENDPOINT from the environment.
	os.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", endpoint)

	serviceName := firstNonEmpty(
		getEnv("SYMPOZIUM_OTEL_SERVICE_NAME", ""),
		getEnv("OTEL_SERVICE_NAME", ""),
		"sympozium-agent-runner",
	)

	tel, err := telemetry.Init(ctx, telemetry.Config{
		ServiceName:     serviceName,
		BatchTimeout:    1 * time.Second,
		ShutdownTimeout: 10 * time.Second,
	})
	if err != nil {
		log.Printf("failed to initialize OTel via pkg/telemetry: %v", err)
		return obs
	}

	o := &agentObservability{
		enabled:  true,
		tracer:   tel.Tracer(),
		shutdown: tel.Shutdown,
	}
	o.initMetrics()
	obs = o
	return o
}

func (o *agentObservability) initMetrics() {
	meter := otel.Meter("sympozium/agent-runner")
	var err error

	o.agentRuns, err = meter.Int64Counter("sympozium.agent.runs")
	if err != nil {
		log.Printf("failed creating metric sympozium.agent.runs: %v", err)
	}
	o.agentRunDurMs, err = meter.Float64Histogram("sympozium.agent.run.duration")
	if err != nil {
		log.Printf("failed creating metric sympozium.agent.run.duration: %v", err)
	}
	o.inTok, err = meter.Int64Counter("gen_ai.usage.input_tokens")
	if err != nil {
		log.Printf("failed creating metric gen_ai.usage.input_tokens: %v", err)
	}
	o.outTok, err = meter.Int64Counter("gen_ai.usage.output_tokens")
	if err != nil {
		log.Printf("failed creating metric gen_ai.usage.output_tokens: %v", err)
	}
	o.toolInvocations, err = meter.Int64Counter("sympozium.tool.invocations")
	if err != nil {
		log.Printf("failed creating metric sympozium.tool.invocations: %v", err)
	}
	o.skillDurMs, err = meter.Float64Histogram("sympozium.skill.duration")
	if err != nil {
		log.Printf("failed creating metric sympozium.skill.duration: %v", err)
	}
}

func (o *agentObservability) startRunSpan(ctx context.Context, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	if o == nil {
		return ctx, trace.SpanFromContext(ctx)
	}
	return o.tracer.Start(ctx, "sympozium.agent.run", trace.WithAttributes(attrs...))
}

func (o *agentObservability) startChatSpan(ctx context.Context, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	if o == nil {
		return ctx, trace.SpanFromContext(ctx)
	}
	return o.tracer.Start(ctx, "gen_ai.chat", trace.WithAttributes(attrs...))
}

func (o *agentObservability) startToolSpan(ctx context.Context, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	if o == nil {
		return ctx, trace.SpanFromContext(ctx)
	}
	return o.tracer.Start(ctx, "gen_ai.execute_tool", trace.WithAttributes(attrs...))
}

func (o *agentObservability) startSkillSpan(ctx context.Context, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	if o == nil {
		return ctx, trace.SpanFromContext(ctx)
	}
	return o.tracer.Start(ctx, "sympozium.skill.exec", trace.WithAttributes(attrs...))
}

func (o *agentObservability) recordRunMetrics(
	ctx context.Context,
	status, instance, model, namespace string,
	durationMs int64,
	inputTokens, outputTokens int,
) {
	if o == nil || !o.enabled {
		return
	}
	attrs := metric.WithAttributes(
		attribute.String("instance", instance),
		attribute.String("status", status),
		attribute.String("namespace", namespace),
		attribute.String("model", model),
	)
	if o.agentRuns != nil {
		o.agentRuns.Add(ctx, 1, attrs)
	}
	if o.agentRunDurMs != nil {
		o.agentRunDurMs.Record(ctx, float64(durationMs), attrs)
	}
	if inputTokens > 0 && o.inTok != nil {
		o.inTok.Add(ctx, int64(inputTokens), metric.WithAttributes(attribute.String("model", model)))
	}
	if outputTokens > 0 && o.outTok != nil {
		o.outTok.Add(ctx, int64(outputTokens), metric.WithAttributes(attribute.String("model", model)))
	}
}

func (o *agentObservability) recordToolInvocation(ctx context.Context, toolName, status string) {
	if o == nil || !o.enabled || o.toolInvocations == nil {
		return
	}
	o.toolInvocations.Add(ctx, 1, metric.WithAttributes(
		attribute.String("tool_name", toolName),
		attribute.String("status", status),
	))
}

func (o *agentObservability) recordSkillDuration(ctx context.Context, skillName string, d time.Duration) {
	if o == nil || !o.enabled || o.skillDurMs == nil {
		return
	}
	o.skillDurMs.Record(ctx, float64(d.Milliseconds()), metric.WithAttributes(
		attribute.String("skill_name", skillName),
	))
}

func markSpanError(span trace.Span, err error) {
	if span == nil || err == nil {
		return
	}
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}

func writeTraceContextMetadata(ctx context.Context) {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return
	}

	payload := map[string]string{
		"trace_id":      sc.TraceID().String(),
		"span_id":       sc.SpanID().String(),
		"traceparent":   formatTraceparent(sc),
		"agent_run_id":  getEnv("AGENT_RUN_ID", ""),
		"instance_name": getEnv("INSTANCE_NAME", ""),
		"namespace":     getEnv("AGENT_NAMESPACE", ""),
		"model":         getEnv("MODEL_NAME", ""),
	}

	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return
	}
	path := "/workspace/.sympozium/trace-context.json"
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.WriteFile(path, data, 0o644)
}

func traceMetadata(ctx context.Context) map[string]string {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return nil
	}
	return map[string]string{
		"trace_id":    sc.TraceID().String(),
		"span_id":     sc.SpanID().String(),
		"traceparent": formatTraceparent(sc),
	}
}

func formatTraceparent(sc trace.SpanContext) string {
	if !sc.IsValid() {
		return ""
	}
	flags := "00"
	if sc.TraceFlags().IsSampled() {
		flags = "01"
	}
	return fmt.Sprintf("00-%s-%s-%s", sc.TraceID().String(), sc.SpanID().String(), flags)
}

func logWithTrace(ctx context.Context, level, msg string, fields map[string]any) {
	entry := map[string]any{
		"time":  time.Now().UTC().Format(time.RFC3339Nano),
		"level": level,
		"msg":   msg,
	}
	for k, v := range fields {
		entry[k] = v
	}
	if meta := traceMetadata(ctx); meta != nil {
		entry["trace_id"] = meta["trace_id"]
		entry["span_id"] = meta["span_id"]
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return
	}
	log.Println(string(line))
}
