package main

import (
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/openai/openai-go/v3"
)

// TestResetContext_Anthropic verifies that calling ResetContext on the
// Anthropic provider rebuilds the message slice to the seed state (a single
// user message containing the initial task) so the next Chat or Prompt
// call begins like the first turn. VEL-1081.
func TestResetContext_Anthropic(t *testing.T) {
	p := newAnthropicProvider("", "", "claude-3-5-sonnet", "you are helpful", "initial task", nil, nil)
	if got := len(p.messages); got != 1 {
		t.Fatalf("seed message count = %d, want 1", got)
	}
	p.messages = append(p.messages, anthropic.NewAssistantMessage(anthropic.NewTextBlock("reply 1")))
	p.messages = append(p.messages, anthropic.NewUserMessage(anthropic.NewTextBlock("follow up")))
	if got := len(p.messages); got != 3 {
		t.Fatalf("mutated message count = %d, want 3", got)
	}
	p.ResetContext()
	if got := len(p.messages); got != 1 {
		t.Fatalf("post-reset message count = %d, want 1", got)
	}
}

// TestResetContext_OpenAI mirrors TestResetContext_Anthropic for the
// OpenAI provider. ResetContext must drop messages back to the
// [system, initial_task] seed the provider was constructed with.
func TestResetContext_OpenAI(t *testing.T) {
	p, err := newOpenAIProvider("openai", "", "", "gpt-4o-mini", "you are helpful", "initial task", nil, nil)
	if err != nil {
		t.Fatalf("newOpenAIProvider: %v", err)
	}
	if got := len(p.messages); got != 2 {
		t.Fatalf("seed message count = %d, want 2", got)
	}
	p.messages = append(p.messages, openai.UserMessage("follow up"))
	if got := len(p.messages); got != 3 {
		t.Fatalf("mutated message count = %d, want 3", got)
	}
	p.ResetContext()
	if got := len(p.messages); got != 2 {
		t.Fatalf("post-reset message count = %d, want 2", got)
	}
}
