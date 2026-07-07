package genaiattrs

import (
	"testing"
)

func TestProvider(t *testing.T) {
	kv := Provider("anthropic")
	if kv.Key != ProviderNameKey || kv.Value.AsString() != "anthropic" {
		t.Fatalf("unexpected: %+v", kv)
	}
}

func TestSystem(t *testing.T) {
	kv := System("anthropic")
	if kv.Key != SystemKey || kv.Value.AsString() != "anthropic" {
		t.Fatalf("unexpected: %+v", kv)
	}
}

func TestResponseModel(t *testing.T) {
	kv := ResponseModel("claude-sonnet-4")
	if kv.Key != ResponseModelKey || kv.Value.AsString() != "claude-sonnet-4" {
		t.Fatalf("unexpected: %+v", kv)
	}
}

func TestModel(t *testing.T) {
	kv := Model("claude-sonnet-4")
	if kv.Key != RequestModelKey || kv.Value.AsString() != "claude-sonnet-4" {
		t.Fatalf("unexpected: %+v", kv)
	}
}

func TestInputTokens(t *testing.T) {
	kv := InputTokens(42)
	if kv.Key != InputTokensKey || kv.Value.AsInt64() != 42 {
		t.Fatalf("unexpected: %+v", kv)
	}
}

func TestSpanKind(t *testing.T) {
	kv := SpanKind(SpanKindTask)
	if kv.Key != TraceloopSpanKindKey || kv.Value.AsString() != "task" {
		t.Fatalf("unexpected: %+v", kv)
	}
}
