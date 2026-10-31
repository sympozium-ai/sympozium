package main

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/sympozium-ai/sympozium/internal/tooloutput"
)

func TestToolOutputMaxBytes(t *testing.T) {
	t.Setenv(tooloutput.MaxBytesEnv, "")
	if got := toolOutputMaxBytes(); got != tooloutput.DefaultMaxBytes {
		t.Fatalf("default output limit = %d, want %d", got, tooloutput.DefaultMaxBytes)
	}

	t.Setenv(tooloutput.MaxBytesEnv, "12000")
	if got := toolOutputMaxBytes(); got != 12000 {
		t.Fatalf("configured output limit = %d, want 12000", got)
	}

	for _, value := range []string{"0", "-1", "not-a-number"} {
		t.Setenv(tooloutput.MaxBytesEnv, value)
		if got := toolOutputMaxBytes(); got != tooloutput.DefaultMaxBytes {
			t.Errorf("output limit for %q = %d, want default %d", value, got, tooloutput.DefaultMaxBytes)
		}
	}
}

func TestTruncateToolOutput(t *testing.T) {
	got := truncateToolOutput("abcdefgh", 5, "use pagination")
	want := "abcde\n... (output truncated at 5 bytes; use pagination)"
	if got != want {
		t.Fatalf("truncateToolOutput() = %q, want %q", got, want)
	}
	if got := truncateToolOutput("abcde", 5, "use pagination"); got != "abcde" {
		t.Fatalf("exact-limit output = %q, want unchanged content", got)
	}

	got = truncateToolOutput("ab日本語", 4, "use pagination")
	if !utf8.ValidString(got) || !strings.HasPrefix(got, "ab\n") {
		t.Fatalf("truncateToolOutput() split UTF-8: %q", got)
	}
}
