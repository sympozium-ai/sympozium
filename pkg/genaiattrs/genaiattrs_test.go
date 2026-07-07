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

func TestMessagesJSON(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
	}
	kv := MessagesJSON(msgs)
	if kv.Key != InputMessagesKey {
		t.Fatalf("expected key %s, got %s", InputMessagesKey, kv.Key)
	}
	want := `[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"}]`
	if kv.Value.AsString() != want {
		t.Fatalf("expected %q, got %q", want, kv.Value.AsString())
	}
}

func TestMessagesJSONEmpty(t *testing.T) {
	kv := MessagesJSON(nil)
	if kv.Value.AsString() != "[]" {
		t.Fatalf("expected [], got %q", kv.Value.AsString())
	}
}
