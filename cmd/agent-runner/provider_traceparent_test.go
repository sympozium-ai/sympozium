package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// TestCallOpenAI_PropagatesTraceparent and TestCallAnthropic_PropagatesTraceparent
// are the regression guard for issue #468: LLM provider calls must carry the
// AgentRun's W3C traceparent so a trace-aware downstream gateway/proxy joins
// the same trace instead of receiving no trace context at all.
func TestCallOpenAI_PropagatesTraceparent(t *testing.T) {
	old := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	defer otel.SetTextMapPropagator(old)

	var gotTraceparent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTraceparent = r.Header.Get("traceparent")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id": "chatcmpl-test", "object": "chat.completion", "model": "gpt-4o-mini",
			"choices": []map[string]any{{
				"index":         0,
				"message":       map[string]string{"role": "assistant", "content": "hi"},
				"finish_reason": "stop",
			}},
			"usage": map[string]int{"prompt_tokens": 5, "completion_tokens": 2},
		})
	}))
	defer srv.Close()

	tid, _ := trace.TraceIDFromHex("0123456789abcdef0123456789abcdef")
	sid, _ := trace.SpanIDFromHex("0123456789abcdef")
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: tid, SpanID: sid, TraceFlags: trace.FlagsSampled, Remote: true,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)

	_, _, _, _, err := callOpenAI(ctx, "openai", "test-key", srv.URL, "gpt-4o-mini", "sys", "hi", nil, nil)
	if err != nil {
		t.Fatalf("callOpenAI error: %v", err)
	}

	if gotTraceparent == "" {
		t.Fatal("expected traceparent header on LLM call, got none")
	}
	if !strings.Contains(gotTraceparent, tid.String()) {
		t.Errorf("traceparent %q does not carry parent trace id %s", gotTraceparent, tid)
	}
}

func TestCallAnthropic_PropagatesTraceparent(t *testing.T) {
	old := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	defer otel.SetTextMapPropagator(old)

	var gotTraceparent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTraceparent = r.Header.Get("traceparent")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_test", "type": "message", "role": "assistant",
			"model":       "claude-sonnet-4-20250514",
			"content":     []map[string]string{{"type": "text", "text": "hi"}},
			"stop_reason": "end_turn",
			"usage":       map[string]int{"input_tokens": 5, "output_tokens": 2},
		})
	}))
	defer srv.Close()

	tid, _ := trace.TraceIDFromHex("fedcba9876543210fedcba9876543210")
	sid, _ := trace.SpanIDFromHex("fedcba9876543210")
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: tid, SpanID: sid, TraceFlags: trace.FlagsSampled, Remote: true,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)

	_, _, _, _, err := callAnthropic(ctx, "test-key", srv.URL, "claude-sonnet-4-20250514", "sys", "hi", nil, nil)
	if err != nil {
		t.Fatalf("callAnthropic error: %v", err)
	}

	if gotTraceparent == "" {
		t.Fatal("expected traceparent header on LLM call, got none")
	}
	if !strings.Contains(gotTraceparent, tid.String()) {
		t.Errorf("traceparent %q does not carry parent trace id %s", gotTraceparent, tid)
	}
}
