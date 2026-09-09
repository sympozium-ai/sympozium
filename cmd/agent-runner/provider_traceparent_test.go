package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// Regression guard for issue #468. Deliberately no otel.SetTextMapPropagator
// call: production skips telemetry.Init (which sets the global) when OTel is
// disabled or unreachable, so these tests must pass without it.

func newSpanContext(traceHex, spanHex string) trace.SpanContext {
	tid, _ := trace.TraceIDFromHex(traceHex)
	sid, _ := trace.SpanIDFromHex(spanHex)
	return trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: tid, SpanID: sid, TraceFlags: trace.FlagsSampled, Remote: true,
	})
}

func TestCallOpenAI_PropagatesTraceparent(t *testing.T) {
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

	runs := []trace.SpanContext{
		newSpanContext("0123456789abcdef0123456789abcdef", "0123456789abcdef"),
		newSpanContext("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "aaaaaaaaaaaaaaaa"),
	}
	for _, sc := range runs {
		gotTraceparent = ""
		ctx := trace.ContextWithSpanContext(context.Background(), sc)

		_, _, _, _, err := callOpenAI(ctx, "openai", "test-key", srv.URL, "gpt-4o-mini", "sys", "hi", nil, nil)
		if err != nil {
			t.Fatalf("callOpenAI error: %v", err)
		}

		if gotTraceparent == "" {
			t.Fatal("expected traceparent header on LLM call, got none")
		}
		if !strings.Contains(gotTraceparent, sc.TraceID().String()) {
			t.Errorf("traceparent %q does not carry run trace id %s", gotTraceparent, sc.TraceID())
		}
	}
}

func TestCallAnthropic_PropagatesTraceparent(t *testing.T) {
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

	runs := []trace.SpanContext{
		newSpanContext("fedcba9876543210fedcba9876543210", "fedcba9876543210"),
		newSpanContext("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "bbbbbbbbbbbbbbbb"),
	}
	for _, sc := range runs {
		gotTraceparent = ""
		ctx := trace.ContextWithSpanContext(context.Background(), sc)

		_, _, _, _, err := callAnthropic(ctx, "test-key", srv.URL, "claude-sonnet-4-20250514", "sys", "hi", nil, nil)
		if err != nil {
			t.Fatalf("callAnthropic error: %v", err)
		}

		if gotTraceparent == "" {
			t.Fatal("expected traceparent header on LLM call, got none")
		}
		if !strings.Contains(gotTraceparent, sc.TraceID().String()) {
			t.Errorf("traceparent %q does not carry run trace id %s", gotTraceparent, sc.TraceID())
		}
	}
}

// TestTracingHTTPClient_IgnoresGlobalPropagator proves injection works even
// with the global propagator left at its default no-op.
func TestTracingHTTPClient_IgnoresGlobalPropagator(t *testing.T) {
	// Confirm the default global propagator really is a no-op before relying on that.
	noop := propagation.NewCompositeTextMapPropagator()
	carrier := propagation.MapCarrier{}
	noop.Inject(context.Background(), carrier)
	if len(carrier) != 0 {
		t.Fatalf("test assumption broken: default composite propagator injected headers")
	}

	var gotTraceparent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTraceparent = r.Header.Get("traceparent")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sc := newSpanContext("11111111111111111111111111111111"[:32], "1111111111111111")
	ctx := trace.ContextWithSpanContext(context.Background(), sc)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := tracingHTTPClient().Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	resp.Body.Close()

	if gotTraceparent == "" {
		t.Fatal("expected traceparent header even with a no-op global propagator")
	}
	if !strings.Contains(gotTraceparent, sc.TraceID().String()) {
		t.Errorf("traceparent %q does not carry run trace id %s", gotTraceparent, sc.TraceID())
	}
}
