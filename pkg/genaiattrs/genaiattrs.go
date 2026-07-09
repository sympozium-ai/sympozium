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
	"go.opentelemetry.io/otel/attribute"
)

// ---------------------------------------------------------------------------
// GenAI semantic convention attribute keys
// ---------------------------------------------------------------------------

const (
	// Provider. ProviderNameKey is the current OTel convention; SystemKey is
	// the deprecated predecessor, kept for one release cycle for back-compat
	// (Dynatrace AI Observability + existing dashboards still key off it).
	ProviderNameKey = "gen_ai.provider.name"
	SystemKey       = "gen_ai.system"

	// Models
	RequestModelKey  = "gen_ai.request.model"
	ResponseModelKey = "gen_ai.response.model"

	// Token usage
	InputTokensKey  = "gen_ai.usage.input_tokens"
	OutputTokensKey = "gen_ai.usage.output_tokens"

	// System / agent
	SystemInstructionsKey = "gen_ai.system_instructions"
	AgentNameKey          = "gen_ai.agent.name"

	// Traceloop span kind (task or tool)
	TraceloopSpanKindKey = "traceloop.span.kind"

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

// System returns the deprecated gen_ai.system attribute. Emitted alongside
// gen_ai.provider.name for one release cycle so existing consumers keep working.
func System(name string) attribute.KeyValue {
	return attribute.String(SystemKey, name)
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

// NOTE: gen_ai.input.messages / gen_ai.output.messages and the prompt/completion
// content-filter-result attributes are intentionally NOT emitted. Capturing raw
// message content on spans is an opt-in per OTel convention
// (OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT) because it carries PII and
// large payloads; Dynatrace AI Observability treats these as optional. They are
// scoped out of this change rather than shipped as dead exported API.
