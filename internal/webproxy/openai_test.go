package webproxy

import (
	"testing"
)

func TestHashLabelValue_FixedLength(t *testing.T) {
	result := hashLabelValue("abc")
	if len(result) != 16 {
		t.Errorf("expected length 16, got %d", len(result))
	}
}

func TestHashLabelValue_Deterministic(t *testing.T) {
	r1 := hashLabelValue("test-input")
	r2 := hashLabelValue("test-input")
	if r1 != r2 {
		t.Error("expected same hash for same input")
	}

	r3 := hashLabelValue("different-input")
	if r1 == r3 {
		t.Error("expected different hash for different input")
	}
}

func TestHashLabelValue_ShortInput_AlsoHashed(t *testing.T) {
	result := hashLabelValue("short")
	if len(result) != 16 {
		t.Errorf("expected length 16 for short input, got %d", len(result))
	}
	if result == "short" {
		t.Error("short input should still be hashed, not passed through")
	}
}

func TestWebRequestHash(t *testing.T) {
	h := webRequestHash("key", "instance", "gpt-4", "You are an AI", "Write code")
	if len(h) == 0 {
		t.Error("expected non-empty hash")
	}

	h2 := webRequestHash("key", "instance", "gpt-4", "You are an AI", "Write code")
	if h != h2 {
		t.Error("expected same hash for same input")
	}

	h3 := webRequestHash("key2", "instance", "gpt-4", "You are an AI", "Write code")
	if h == h3 {
		t.Error("expected different hash for different input")
	}
}

func TestWebRequestHash_WithIdempotencyKey(t *testing.T) {
	base := webRequestHash("key", "inst", "gpt-4", "sys", "task")

	// Changing idempotency key should change hash
	if h := webRequestHash("key2", "inst", "gpt-4", "sys", "task"); h == base {
		t.Error("expected different hash for different idempotency key")
	}

	// Changing instance name should change hash
	if h := webRequestHash("key", "inst2", "gpt-4", "sys", "task"); h == base {
		t.Error("expected different hash for different instance name")
	}

	// With idempotency key set, changing model/sys/task should NOT change hash
	if h := webRequestHash("key", "inst", "gpt-5", "sys2", "task2"); h != base {
		t.Error("expected same hash when model/sys/task change but idempotency key stays the same")
	}
}

func TestWebRequestHash_WithoutIdempotencyKey(t *testing.T) {
	base := webRequestHash("", "inst", "gpt-4", "sys", "task")

	// Without idempotency key, all fields affect the hash
	hashes := []string{
		webRequestHash("", "inst2", "gpt-4", "sys", "task"),
		webRequestHash("", "inst", "gpt-5", "sys", "task"),
		webRequestHash("", "inst", "gpt-4", "sys2", "task"),
		webRequestHash("", "inst", "gpt-4", "sys", "task2"),
	}

	for i, h := range hashes {
		if h == base {
			t.Errorf("expected different hash when changing field %d", i)
		}
	}
}

func TestHashLabelValue_EmptyString(t *testing.T) {
	result := hashLabelValue("")
	if len(result) != 16 {
		t.Errorf("expected length 16 for empty input, got %d", len(result))
	}
}

func TestWebRequestHash_WhitespaceOnlyIdempotencyKey(t *testing.T) {
	// Whitespace-only idempotency key should be treated as empty
	h1 := webRequestHash("  \t ", "inst", "gpt-4", "sys", "task")
	h2 := webRequestHash("", "inst", "gpt-4", "sys", "task")
	if h1 != h2 {
		t.Error("expected whitespace-only idempotency key to behave like empty")
	}
}
