// Package genaiattrs provides shared OpenTelemetry GenAI semantic convention
// attribute keys and helpers for consistent instrumentation across Sympozium.
//
// These follow the OTel GenAI semantic conventions so Dynatrace AI
// Observability can consume telemetry without custom mapping.
//
// Backward compatibility: existing sympozium.* attributes are kept alongside
// the new gen_ai.* attributes for one release cycle.
package genaiattrs

import (
	"fmt"
	"strings"

	"go.opentelemetry.io/otel/attribute"
)

// ---------------------------------------------------------------------------
// GenAI semantic convention attribute keys
// ---------------------------------------------------------------------------

const (
	// Provider
	ProviderNameKey = "gen_ai.provider.name"

	// Models
	RequestModelKey  = "gen_ai.request.model"
	ResponseModelKey = "gen_ai.response.model"

	// Token usage
	InputTokensKey  = "gen_ai.usage.input_tokens"
	OutputTokensKey = "gen_ai.usage.output_tokens"

	// Messages
	InputMessagesKey  = "gen_ai.input.messages"
	OutputMessagesKey = "gen_ai.output.messages"

	// System / agent
	SystemInstructionsKey = "gen_ai.system_instructions"
	AgentNameKey          = "gen_ai.agent.name"

	// Traceloop span kind (task or tool)
	TraceloopSpanKindKey = "traceloop.span.kind"

	// Content filter results
	PromptFilterResultsKey    = "gen_ai.prompt.prompt_filter_results"
	CompletionFilterResultsKey = "gen_ai.completion.content_filter_results"

	// Tool call
	ToolNameKey   = "gen_ai.tool.name"
	ToolCallIDKey = "gen_ai.tool.call.id"
)

// SpanKind values for traceloop.span.kind.
const (
	SpanKindTask = "task"
	SpanKindTool = "tool"
)

// ---------------------------------------------------------------------------
// Attribute builders
// ---------------------------------------------------------------------------

// Provider returns gen_ai.provider.name.
func Provider(name string) attribute.KeyValue {
	return attribute.String(ProviderNameKey, name)
}

// Model returns gen_ai.request.model.
func Model(model string) attribute.KeyValue {
	return attribute.String(RequestModelKey, model)
}

// ResponseModel returns gen_ai.response.model.
func ResponseModel(model string) attribute.KeyValue {
	return attribute.String(ResponseModelKey, model)
}

// InputTokens returns gen_ai.usage.input_tokens.
func InputTokens(n int) attribute.KeyValue {
	return attribute.Int(InputTokensKey, n)
}

// OutputTokens returns gen_ai.usage.output_tokens.
func OutputTokens(n int) attribute.KeyValue {
	return attribute.Int(OutputTokensKey, n)
}

// Agent returns gen_ai.agent.name.
func Agent(name string) attribute.KeyValue {
	return attribute.String(AgentNameKey, name)
}

// SystemInstructions returns gen_ai.system_instructions.
func SystemInstructions(text string) attribute.KeyValue {
	return attribute.String(SystemInstructionsKey, text)
}

// SpanKind returns traceloop.span.kind.
func SpanKind(kind string) attribute.KeyValue {
	return attribute.String(TraceloopSpanKindKey, kind)
}

// ToolName returns gen_ai.tool.name.
func ToolName(name string) attribute.KeyValue {
	return attribute.String(ToolNameKey, name)
}

// ToolCallID returns gen_ai.tool.call.id.
func ToolCallID(id string) attribute.KeyValue {
	return attribute.String(ToolCallIDKey, id)
}

// ---------------------------------------------------------------------------
// Message helpers
// ---------------------------------------------------------------------------

// Message is a simple role/content pair for JSON serialisation.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// MessagesJSON serialises a slice of messages to a JSON string for
// gen_ai.input.messages. Falls back to a simple JSON array.
func MessagesJSON(msgs []Message) attribute.KeyValue {
	if len(msgs) == 0 {
		return attribute.String(InputMessagesKey, "[]")
	}
	var b strings.Builder
	b.WriteString("[")
	for i, m := range msgs {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(fmt.Sprintf(`{"role":"%s","content":%q}`, m.Role, m.Content))
	}
	b.WriteString("]")
	return attribute.String(InputMessagesKey, b.String())
}

// OutputMessagesJSON is the same as MessagesJSON but for output.
func OutputMessagesJSON(msgs []Message) attribute.KeyValue {
	if len(msgs) == 0 {
		return attribute.String(OutputMessagesKey, "[]")
	}
	var b strings.Builder
	b.WriteString("[")
	for i, m := range msgs {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(fmt.Sprintf(`{"role":"%s","content":%q}`, m.Role, m.Content))
	}
	b.WriteString("]")
	return attribute.String(OutputMessagesKey, b.String())
}
