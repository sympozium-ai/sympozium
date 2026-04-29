package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

// ToolCall is a provider-agnostic representation of an LLM-requested tool
// invocation. Input is the raw JSON arguments string the model emitted.
type ToolCall struct {
	ID    string
	Name  string
	Input string
}

// ToolResult is a provider-agnostic tool execution result that the loop
// feeds back into the model on the next turn.
type ToolResult struct {
	CallID  string
	Content string
	IsError bool
}

// ChatResult is what a provider returns from one round-trip with the model.
//
//   - If ToolCalls is empty, Text is the final response and the loop exits.
//   - If ToolCalls is non-empty, the loop executes them and calls Chat again.
//     Text may still be non-empty (reasoning preamble) but is ignored in that
//     case — the loop only surfaces text from the terminal turn.
type ChatResult struct {
	Text         string
	ToolCalls    []ToolCall
	InputTokens  int
	OutputTokens int
	FinishReason string
}

// LLMProvider is a stateful adapter for one chat conversation with a
// particular LLM backend. Each provider owns its SDK client, its own
// message history, and its own tool-schema conversion. The agent loop
// treats providers as opaque: it only sees ChatResult/ToolCall/ToolResult.
//
// Implementations MUST be safe to use sequentially (no concurrent calls)
// and MUST preserve conversation state across Chat → AddToolResults → Chat
// cycles so the model sees a coherent history.
type LLMProvider interface {
	// Name identifies the provider system for telemetry (e.g. "openai",
	// "anthropic", "bedrock", "lm-studio").
	Name() string
	// Model returns the resolved model identifier.
	Model() string
	// Chat sends the current conversation state to the model and records
	// the assistant's reply (text + tool calls) internally for the next turn.
	Chat(ctx context.Context) (ChatResult, error)
	// AddToolResults records tool execution results in the conversation so
	// the next Chat call can reference them.
	AddToolResults(results []ToolResult)
}

// runAgentLoop drives a provider through iterative tool calling until the
// model produces a terminal response or the iteration budget is exhausted.
// It owns telemetry, token accumulation, tool dispatch, and failure logging —
// providers just translate between SDK-specific types and the shared shapes.
//
// Accumulates text from every turn so that if the model's terminal turn has
// empty content (common with reasoning/instruct local models that exhaust
// their output budget on tool-call preamble), the user still sees the
// intermediate reasoning in the UX instead of a blank response.
//
// Returns (responseText, inputTokens, outputTokens, toolCalls, error).
func runAgentLoop(ctx context.Context, p LLMProvider) (string, int, int, int, error) {
	totalInputTokens := 0
	totalOutputTokens := 0
	totalToolCalls := 0
	var accumulated strings.Builder

	// Per-run token budget enforcement from membrane config.
	maxTokensPerRun := int64(0)
	tokenBudgetAction := "halt"
	if v := os.Getenv("WORKFLOW_MEMBRANE_MAX_TOKENS_PER_RUN"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			maxTokensPerRun = n
		}
	}
	if v := os.Getenv("WORKFLOW_MEMBRANE_TOKEN_BUDGET_ACTION"); v != "" {
		tokenBudgetAction = v
	}

	for i := 0; i < maxToolIterations; i++ {
		chatCtx, chatSpan := obs.startChatSpan(ctx,
			attribute.String("gen_ai.system", p.Name()),
			attribute.String("gen_ai.request.model", p.Model()),
		)
		res, err := p.Chat(chatCtx)
		if err != nil {
			markSpanError(chatSpan, err)
			chatSpan.End()
			return "", totalInputTokens, totalOutputTokens, totalToolCalls, err
		}

		totalInputTokens += res.InputTokens
		totalOutputTokens += res.OutputTokens
		chatSpan.SetAttributes(
			attribute.Int("gen_ai.usage.input_tokens", res.InputTokens),
			attribute.Int("gen_ai.usage.output_tokens", res.OutputTokens),
		)
		if res.FinishReason != "" {
			chatSpan.SetAttributes(attribute.String("gen_ai.response.finish_reasons", res.FinishReason))
		}
		chatSpan.SetStatus(codes.Ok, "")
		chatSpan.End()

		// Per-run token budget check.
		if maxTokensPerRun > 0 {
			used := int64(totalInputTokens + totalOutputTokens)
			if used >= maxTokensPerRun {
				msg := fmt.Sprintf("Per-run token budget exceeded (%d/%d tokens used)", used, maxTokensPerRun)
				if tokenBudgetAction == "halt" {
					log.Printf("TOKEN BUDGET HALT: %s", msg)
					text := accumulated.String()
					if trimmed := strings.TrimSpace(res.Text); trimmed != "" {
						text = trimmed
					}
					if text == "" {
						text = msg
					}
					return text, totalInputTokens, totalOutputTokens, totalToolCalls, nil
				}
				log.Printf("TOKEN BUDGET WARN: %s", msg)
			}
		}

		// Accumulate this turn's text (reasoning preamble on tool-calling
		// turns, or the final answer on the terminal turn) so we can surface
		// something useful if the terminal text ends up empty.
		if trimmed := strings.TrimSpace(res.Text); trimmed != "" {
			if accumulated.Len() > 0 {
				accumulated.WriteString("\n\n")
			}
			accumulated.WriteString(trimmed)
		}

		// Terminal turn: no tool calls.
		if len(res.ToolCalls) == 0 {
			// Prefer the terminal turn's own text when non-empty; otherwise
			// fall back to the accumulated reasoning so the UX always shows
			// what the model produced during this run.
			if strings.TrimSpace(res.Text) != "" {
				return res.Text, totalInputTokens, totalOutputTokens, totalToolCalls, nil
			}
			if accumulated.Len() > 0 {
				log.Printf("WARNING: terminal turn had empty text after %d tool iterations; "+
					"discarding %d chars of intermediate reasoning", i, accumulated.Len())
				return "(Agent completed its task via tool calls but did not produce a final text summary.)",
					totalInputTokens, totalOutputTokens, totalToolCalls, nil
			}
			log.Printf("WARNING: terminal turn had empty text and no prior reasoning to fall back on")
			return "", totalInputTokens, totalOutputTokens, totalToolCalls, nil
		}

		// Execute each tool call and gather results for the next turn. The
		// model is informed of failures via the isError flag on each tool
		// result — no additional warning is emitted here.
		results := make([]ToolResult, 0, len(res.ToolCalls))
		for _, call := range res.ToolCalls {
			totalToolCalls++
			log.Printf("tool_call [%d]: %s id=%s", totalToolCalls, call.Name, call.ID)
			out := executeToolCallWithTelemetry(ctx, call.Name, call.Input, call.ID)
			results = append(results, ToolResult{
				CallID:  call.ID,
				Content: out,
				IsError: strings.HasPrefix(out, "Error:"),
			})
		}
		p.AddToolResults(results)
	}

	return "", totalInputTokens, totalOutputTokens, totalToolCalls,
		fmt.Errorf("exceeded maximum tool-call iterations (%d)", maxToolIterations)
}
