package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// TestOTelSpanHierarchy verifies that the agent-runner produces the expected
// OTel span hierarchy when calling LLMs via mock servers.
// Uses a single TracerProvider to avoid issues with Go OTel's one-shot
// delegate resolution for package-level tracers.
func TestOTelSpanHierarchy(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(exporter),
	)
	defer tp.Shutdown(context.Background())

	// Install test TracerProvider globally so agentTracer picks it up.
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	t.Run("simple_end_turn", func(t *testing.T) {
		exporter.Reset()

		// Mock Anthropic server returning a simple end_turn response.
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"id": "msg_test", "type": "message", "role": "assistant",
				"model":       "claude-sonnet-4-20250514",
				"content":     []map[string]string{{"type": "text", "text": "Hello!"}},
				"stop_reason": "end_turn",
				"usage":       map[string]int{"input_tokens": 10, "output_tokens": 5},
			})
		})
		srv := httptest.NewServer(handler)
		defer srv.Close()

		ctx := context.Background()
		tracer := otel.Tracer("test")
		ctx, rootSpan := tracer.Start(ctx, "test-root")

		text, inTok, outTok, _, err := callAnthropic(ctx, "test-key", "apikey", "", srv.URL, "claude-sonnet-4-20250514", "sys", "Say hello", nil)
		if err != nil {
			t.Fatalf("callAnthropic error: %v", err)
		}
		if text != "Hello!" {
			t.Errorf("text = %q, want %q", text, "Hello!")
		}
		if inTok != 10 || outTok != 5 {
			t.Errorf("tokens = (%d, %d), want (10, 5)", inTok, outTok)
		}

		rootSpan.End()
		tp.ForceFlush(ctx)

		spans := exporter.GetSpans()
		if len(spans) < 2 {
			t.Fatalf("expected at least 2 spans (test-root + gen_ai.chat), got %d", len(spans))
		}

		// Find the gen_ai.chat span.
		var chatSpan *tracetest.SpanStub
		for i := range spans {
			if spans[i].Name == "gen_ai.chat" {
				chatSpan = &spans[i]
				break
			}
		}
		if chatSpan == nil {
			names := make([]string, len(spans))
			for i, s := range spans {
				names[i] = s.Name
			}
			t.Fatalf("gen_ai.chat span not found; got spans: %v", names)
		}

		// Verify the chat span is a child of the root span.
		if chatSpan.Parent.TraceID() != rootSpan.SpanContext().TraceID() {
			t.Errorf("gen_ai.chat span trace ID %s != root trace ID %s",
				chatSpan.Parent.TraceID(), rootSpan.SpanContext().TraceID())
		}

		// Verify GenAI semantic convention attributes.
		attrs := make(map[string]string)
		for _, a := range chatSpan.Attributes {
			attrs[string(a.Key)] = a.Value.Emit()
		}
		if v, ok := attrs["gen_ai.system"]; !ok || v != "anthropic" {
			t.Errorf("gen_ai.system = %q, want %q", v, "anthropic")
		}
		if v, ok := attrs["gen_ai.request.model"]; !ok || v != "claude-sonnet-4-20250514" {
			t.Errorf("gen_ai.request.model = %q, want %q", v, "claude-sonnet-4-20250514")
		}
	})

	t.Run("tool_use_flow", func(t *testing.T) {
		exporter.Reset()

		callCount := 0
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			callCount++
			w.Header().Set("Content-Type", "application/json")

			if callCount == 1 {
				json.NewEncoder(w).Encode(map[string]any{
					"id": "msg_tool", "type": "message", "role": "assistant",
					"model": "claude-sonnet-4-20250514",
					"content": []map[string]any{
						{"type": "tool_use", "id": "toolu_01X", "name": "read_file",
							"input": map[string]string{"path": "/tmp/otel-test.txt"}},
					},
					"stop_reason": "tool_use",
					"usage":       map[string]int{"input_tokens": 10, "output_tokens": 15},
				})
				return
			}

			json.NewEncoder(w).Encode(map[string]any{
				"id": "msg_final", "type": "message", "role": "assistant",
				"model":       "claude-sonnet-4-20250514",
				"content":     []map[string]any{{"type": "text", "text": "Done."}},
				"stop_reason": "end_turn",
				"usage":       map[string]int{"input_tokens": 20, "output_tokens": 5},
			})
		})
		srv := httptest.NewServer(handler)
		defer srv.Close()

		tools := []ToolDef{
			{
				Name:        "read_file",
				Description: "Read a file",
				Parameters: map[string]any{
					"properties": map[string]any{
						"path": map[string]any{"type": "string"},
					},
					"required": []string{"path"},
				},
			},
		}

		ctx := context.Background()
		tracer := otel.Tracer("test")
		ctx, rootSpan := tracer.Start(ctx, "test-root")

		text, _, _, toolCalls, err := callAnthropic(ctx, "key", "apikey", "", srv.URL, "claude-sonnet-4-20250514", "sys", "Read file", tools)
		if err != nil {
			t.Fatalf("callAnthropic error: %v", err)
		}
		if text != "Done." {
			t.Errorf("text = %q, want %q", text, "Done.")
		}
		if toolCalls != 1 {
			t.Errorf("toolCalls = %d, want 1", toolCalls)
		}

		rootSpan.End()
		tp.ForceFlush(ctx)

		spans := exporter.GetSpans()

		// Collect span names.
		spanNames := map[string]int{}
		for _, s := range spans {
			spanNames[s.Name]++
		}

		if spanNames["gen_ai.chat"] < 2 {
			t.Errorf("expected at least 2 gen_ai.chat spans, got %d; all spans: %v",
				spanNames["gen_ai.chat"], spanNames)
		}
		if spanNames["gen_ai.execute_tool"] < 1 {
			t.Errorf("expected at least 1 gen_ai.execute_tool span, got %d; all spans: %v",
				spanNames["gen_ai.execute_tool"], spanNames)
		}

		// All spans should share the same trace ID.
		rootTraceID := rootSpan.SpanContext().TraceID()
		for _, s := range spans {
			if s.SpanContext.TraceID() != rootTraceID {
				t.Errorf("span %q has trace ID %s, want %s",
					s.Name, s.SpanContext.TraceID(), rootTraceID)
			}
		}
	})
}
