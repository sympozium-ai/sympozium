package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// maxToolIterations is the maximum number of tool-call round-trips before
// the agent stops and returns whatever text it has.
var maxToolIterations = 50

// llmRequestTimeout is the per-request timeout for individual LLM API calls.
// For local providers (ollama, lm-studio, vllm) this prevents a single queued
// request from consuming the entire run budget. Cloud providers get no
// per-request timeout by default (they handle queuing server-side).
var llmRequestTimeout time.Duration // 0 means no per-request timeout

// llmMaxRetries is the maximum number of retries for LLM API calls.
// The SDK already uses exponential backoff (0.5s * 2^attempt, max 8s).
// Defaults: 2 for local providers, 5 for cloud providers.
var llmMaxRetries = -1 // -1 means "use provider-appropriate default"

// runTimeout is the overall context deadline for the entire agent run.
// Defaults: 10 minutes for cloud providers, 30 minutes for local providers.
// Override with RUN_TIMEOUT env var (Go duration string, e.g. "30m", "1h").
var runTimeout time.Duration // 0 means "use provider-appropriate default"

func init() {
	if val := os.Getenv("MAX_TOOL_ITERATIONS"); val != "" {
		if n, err := strconv.Atoi(val); err == nil && n > 0 {
			maxToolIterations = n
		}
	}
	if val := os.Getenv("LLM_REQUEST_TIMEOUT"); val != "" {
		if d, err := time.ParseDuration(val); err == nil && d > 0 {
			llmRequestTimeout = d
		}
	}
	if val := os.Getenv("LLM_MAX_RETRIES"); val != "" {
		if n, err := strconv.Atoi(val); err == nil && n >= 0 {
			llmMaxRetries = n
		}
	}
	if val := os.Getenv("RUN_TIMEOUT"); val != "" {
		if d, err := time.ParseDuration(val); err == nil && d > 0 {
			runTimeout = d
		}
	}
}

// isLocalProvider returns true for providers that run inference locally
// (single-GPU, request queuing) where per-request timeouts matter.
func isLocalProvider(provider string) bool {
	switch provider {
	case "ollama", "lm-studio", "vllm", "llamacpp", "local":
		return true
	}
	return false
}

// effectiveMaxRetries returns the retry count for the given provider.
func effectiveMaxRetries(provider string) int {
	if llmMaxRetries >= 0 {
		return llmMaxRetries // explicit override
	}
	if isLocalProvider(provider) {
		return 2
	}
	return 5
}

// effectiveRequestTimeout returns the per-request timeout for the given provider.
func effectiveRequestTimeout(provider string) time.Duration {
	if llmRequestTimeout > 0 {
		return llmRequestTimeout // explicit override
	}
	if isLocalProvider(provider) {
		return 5 * time.Minute
	}
	return 0 // no per-request timeout for cloud providers
}

// effectiveRunTimeout returns the overall run context timeout.
func effectiveRunTimeout(provider string) time.Duration {
	if runTimeout > 0 {
		return runTimeout // explicit override via RUN_TIMEOUT env
	}
	if isLocalProvider(provider) {
		return 30 * time.Minute
	}
	return 10 * time.Minute
}

type agentResult struct {
	Status   string `json:"status"`
	Response string `json:"response,omitempty"`
	Error    string `json:"error,omitempty"`
	Metrics  struct {
		DurationMs   int64 `json:"durationMs"`
		InputTokens  int   `json:"inputTokens"`
		OutputTokens int   `json:"outputTokens"`
		ToolCalls    int   `json:"toolCalls"`
	} `json:"metrics"`
}

type streamChunk struct {
	Type    string `json:"type"`
	Content string `json:"content"`
	Index   int    `json:"index"`
}

func main() {
	log.SetFlags(log.Ltime | log.Lmicroseconds)
	log.Println("agent-runner starting")

	task := getEnv("TASK", "")
	if task == "" {
		if b, err := os.ReadFile("/ipc/input/task.json"); err == nil {
			var input struct {
				Task string `json:"task"`
			}
			if json.Unmarshal(b, &input) == nil && input.Task != "" {
				task = input.Task
			}
		}
	}
	if task == "" {
		fatal("TASK env var is empty and no /ipc/input/task.json found")
	}

	systemPrompt := getEnv("SYSTEM_PROMPT", "You are a helpful AI assistant.")
	provider := strings.ToLower(getEnv("MODEL_PROVIDER", "openai"))
	modelName := getEnv("MODEL_NAME", "gpt-4o-mini")
	baseURL := strings.TrimRight(getEnv("MODEL_BASE_URL", ""), "/")
	memoryEnabled := getEnv("MEMORY_ENABLED", "") == "true"
	toolsEnabled := getEnv("TOOLS_ENABLED", "") == "true"

	// Load skill files and build enhanced system prompt.
	skills := loadSkills(defaultSkillsDir)
	systemPrompt = buildSystemPrompt(systemPrompt, skills, toolsEnabled)

	// If this run was triggered from a channel, inject context so the
	// agent knows how to reply through the originating channel.
	sourceChannel := getEnv("SOURCE_CHANNEL", "")
	sourceChatID := getEnv("SOURCE_CHAT_ID", "")
	if sourceChannel != "" {
		channelCtx := fmt.Sprintf(
			"\n\n## Channel Context\n\n"+
				"This task was received through the **%s** channel (chat ID: %s). "+
				"You can reply through this channel using the `send_channel_message` tool "+
				"with channel=%q and chatId=%q. Use it to deliver results, ask follow-up "+
				"questions, or send notifications to the user.",
			sourceChannel, sourceChatID, sourceChannel, sourceChatID,
		)
		systemPrompt += channelCtx
		log.Printf("channel context injected: channel=%s chatId=%s", sourceChannel, sourceChatID)
	}

	// Resolve tool definitions.
	var tools []ToolDef
	if toolsEnabled {
		tools = defaultTools()
		// Load MCP tools from manifest if the mcp-bridge sidecar is running
		if mcpTools := loadMCPTools("/ipc/tools/mcp-tools.json"); len(mcpTools) > 0 {
			tools = append(tools, mcpTools...)

			// Group tools by server prefix
			serverTools := make(map[string][]string)
			for _, t := range mcpTools {
				parts := strings.SplitN(t.Name, "_", 2)
				prefix := parts[0]
				serverTools[prefix] = append(serverTools[prefix], t.Name)
			}

			var sb strings.Builder
			sb.WriteString("\n\n## Specialized MCP Tools\n\n")
			sb.WriteString(fmt.Sprintf("You have access to %d specialized MCP tools. ", len(mcpTools)))
			sb.WriteString("ALWAYS prefer MCP tools over execute_command when a relevant MCP tool exists.\n\n")
			sb.WriteString("### Diagnostic Methodology\n")
			sb.WriteString("1. **Start targeted**: Use the most specific MCP tool for the problem first\n")
			sb.WriteString("2. **Don't shotgun**: Avoid calling many tools to 'gather info' — diagnose step by step\n")
			sb.WriteString("3. **Read results carefully**: Each MCP tool returns structured diagnostic data. Analyze it before calling more tools.\n")
			sb.WriteString("4. **gRPC != HTTP**: For gRPC issues, check port naming (grpc-*), appProtocol, H2 settings, DestinationRules — NOT path routing\n")
			sb.WriteString("5. **Only fall back to execute_command** for tasks no MCP tool covers (e.g., reading app logs)\n\n")

			sb.WriteString("### Available Tool Groups\n")
			for prefix, tools := range serverTools {
				sb.WriteString(fmt.Sprintf("- **%s** (%d tools): %s\n", prefix, len(tools), strings.Join(tools, ", ")))
			}

			systemPrompt += sb.String()
		}
		log.Printf("tools enabled: %d tool(s) registered", len(tools))
	}

	// Load memory tools if the memory server is available (standalone deployment).
	if memoryTools := initMemoryTools(); len(memoryTools) > 0 {
		tools = append(tools, memoryTools...)

		// Auto-inject relevant memory context so the agent has immediate
		// awareness of past findings without relying on it to call memory_search.
		var memoryContextBlock string
		if memCtx := queryMemoryContext(task, 3); memCtx != "" {
			memoryContextBlock = "\n\n## Relevant Past Context (auto-retrieved)\n\n" +
				"The following memories were automatically retrieved based on your current task:\n\n" +
				memCtx
			log.Printf("auto-injected %d bytes of memory context", len(memCtx))
		}

		systemPrompt += "\n\n## Persistent Memory\n\n" +
			"You have access to persistent memory tools that survive across runs.\n"

		if memoryContextBlock != "" {
			systemPrompt += memoryContextBlock + "\n\n" +
				"Use `memory_search` for additional lookups beyond what was auto-loaded above.\n"
		} else {
			systemPrompt += "**Before starting any investigation**, call `memory_search` with relevant keywords " +
				"to check if similar issues have been diagnosed before.\n"
		}

		systemPrompt += "**After completing your task**, call `memory_store` to save key findings, " +
			"root causes, and resolution steps for future reference.\n" +
			"Be specific in stored content — include service names, namespaces, error messages, and timestamps."

		log.Printf("memory tools loaded: %d tool(s)", len(memoryTools))
	} else if memoryEnabled {
		// Fallback: legacy ConfigMap memory for backward compatibility.
		var memoryContent string
		if b, err := os.ReadFile("/memory/MEMORY.md"); err == nil {
			memoryContent = strings.TrimSpace(string(b))
			log.Printf("loaded legacy memory (%d bytes)", len(memoryContent))
		}
		if memoryContent != "" && memoryContent != "# Agent Memory\n\nNo memories recorded yet." {
			task = fmt.Sprintf("## Your Memory\nThe following is your persistent memory from prior interactions:\n\n%s\n\n## Current Task\n%s", memoryContent, task)
		}
		if memoryEnabled {
			memoryInstruction := "\n\nYou have persistent memory. After completing your task, " +
				"output a memory update block wrapped in markers like this:\n" +
				"__SYMPOZIUM_MEMORY__\n<your updated MEMORY.md content>\n__SYMPOZIUM_MEMORY_END__\n" +
				"Include key facts, preferences, and context from this and past interactions. " +
				"Keep it concise (under 256KB). Use markdown format."
			systemPrompt += memoryInstruction
		}
	}

	apiKey := firstNonEmpty(
		os.Getenv("API_KEY"),
		os.Getenv("OPENAI_API_KEY"),
		os.Getenv("ANTHROPIC_API_KEY"),
		os.Getenv("AZURE_OPENAI_API_KEY"),
		os.Getenv("PROVIDER_API_KEY"),
	)

	log.Printf("provider=%s model=%s baseURL=%s tools=%v task=%q",
		provider, modelName, baseURL, toolsEnabled, truncate(task, 80))
	reqTimeout := effectiveRequestTimeout(provider)
	retries := effectiveMaxRetries(provider)
	if reqTimeout > 0 {
		log.Printf("llm_request_timeout=%s llm_max_retries=%d max_tool_iterations=%d",
			reqTimeout, retries, maxToolIterations)
	} else {
		log.Printf("llm_request_timeout=none llm_max_retries=%d max_tool_iterations=%d",
			retries, maxToolIterations)
	}

	_ = os.MkdirAll("/ipc/output", 0o755)

	rt := effectiveRunTimeout(provider)
	log.Printf("run_timeout=%s", rt)
	ctx, cancel := context.WithTimeout(context.Background(), rt)
	defer cancel()

	obs := initObservability(ctx)
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer shutdownCancel()
		if err := obs.shutdown(shutdownCtx); err != nil {
			log.Printf("failed to shutdown OTel providers: %v", err)
		}
	}()

	// Extract TRACEPARENT from env so the runner trace joins the controller trace.
	if tp := os.Getenv("TRACEPARENT"); tp != "" {
		log.Printf("TRACEPARENT env var found: %s", tp)
		prop := propagation.TraceContext{}
		carrier := propagation.MapCarrier{"traceparent": tp}
		ctx = prop.Extract(ctx, carrier)
		sc := oteltrace.SpanContextFromContext(ctx)
		log.Printf("after extraction: traceID=%s spanID=%s remote=%v valid=%v", sc.TraceID(), sc.SpanID(), sc.IsRemote(), sc.IsValid())
	} else {
		log.Println("TRACEPARENT env var not set")
	}

	ctx, runSpan := obs.startRunSpan(ctx,
		attribute.String("instance", getEnv("INSTANCE_NAME", "")),
		attribute.String("tenant.namespace", getEnv("AGENT_NAMESPACE", "")),
		attribute.String("model", modelName),
		attribute.String("task.summary", truncate(task, 200)),
	)
	writeTraceContextMetadata(ctx)
	logWithTrace(ctx, "info", "agent run started", map[string]any{
		"instance":  getEnv("INSTANCE_NAME", ""),
		"namespace": getEnv("AGENT_NAMESPACE", ""),
		"provider":  provider,
		"model":     modelName,
	})

	start := time.Now()

	var (
		responseText string
		inputTokens  int
		outputTokens int
		toolCalls    int
		err          error
	)

	switch provider {
	case "anthropic":
		responseText, inputTokens, outputTokens, toolCalls, err = callAnthropic(ctx, apiKey, baseURL, modelName, systemPrompt, task, tools)
	case "bedrock":
		responseText, inputTokens, outputTokens, toolCalls, err = callBedrock(ctx, modelName, systemPrompt, task, tools)
	default:
		// OpenAI, Azure OpenAI, Ollama, LM Studio, and any OpenAI-compatible provider
		responseText, inputTokens, outputTokens, toolCalls, err = callOpenAI(ctx, provider, apiKey, baseURL, modelName, systemPrompt, task, tools)
	}

	elapsed := time.Since(start)

	var res agentResult
	res.Metrics.DurationMs = elapsed.Milliseconds()
	res.Metrics.ToolCalls = toolCalls

	debugMode := getEnv("DEBUG", "") == "true"

	if err != nil {
		log.Printf("LLM call failed: %v", err)
		res.Status = "error"
		res.Error = err.Error()
		markSpanError(runSpan, err)
		runSpan.SetStatus(codes.Error, err.Error())
	} else {
		log.Printf("LLM call succeeded (tokens: in=%d out=%d, tool_calls=%d)", inputTokens, outputTokens, toolCalls)
		res.Status = "success"
		res.Response = responseText
		res.Metrics.InputTokens = inputTokens
		res.Metrics.OutputTokens = outputTokens
		runSpan.SetAttributes(
			attribute.Int("gen_ai.usage.input_tokens", inputTokens),
			attribute.Int("gen_ai.usage.output_tokens", outputTokens),
			attribute.Int("gen_ai.tool.call.count", toolCalls),
		)
		runSpan.SetStatus(codes.Ok, "")
	}

	// Auto-store task/response in memory server for future context.
	// Must be synchronous — the process exits shortly after this point,
	// so a goroutine would be killed before the HTTP POST completes.
	if res.Status == "success" && res.Response != "" {
		autoStoreMemory(task, res.Response)
	}

	// Extract and emit memory update before stripping markers from the response.
	if memoryEnabled && res.Response != "" {
		if memUpdate := extractMemoryUpdate(res.Response); memUpdate != "" {
			fmt.Fprintf(os.Stdout, "\n__SYMPOZIUM_MEMORY__%s__SYMPOZIUM_MEMORY_END__\n", memUpdate)
			log.Printf("emitted memory update (%d bytes)", len(memUpdate))
		}
	}

	// Strip memory markers from the response so they don't appear in the
	// TUI feed or channel messages. Keep them only if DEBUG is enabled.
	if !debugMode && res.Response != "" {
		res.Response = stripMemoryMarkers(res.Response)
	}

	if res.Response != "" {
		writeJSON("/ipc/output/stream-0.json", streamChunk{
			Type:    "text",
			Content: res.Response,
			Index:   0,
		})
	}

	writeJSON("/ipc/output/result.json", res)

	// Signal sidecars (tool-executor, etc.) to exit by writing a done sentinel.
	_ = os.WriteFile("/ipc/done", []byte("done"), 0o644)

	// Print a structured marker to stdout so the controller can extract
	// the result from pod logs even after the IPC volume is gone.
	if markerBytes, err := json.Marshal(res); err == nil {
		fmt.Fprintf(os.Stdout, "\n__SYMPOZIUM_RESULT__%s__SYMPOZIUM_END__\n", string(markerBytes))
	}

	if res.Status == "error" {
		obs.recordRunMetrics(ctx, "error", getEnv("INSTANCE_NAME", ""), modelName, getEnv("AGENT_NAMESPACE", ""), elapsed.Milliseconds(), inputTokens, outputTokens)
		logWithTrace(ctx, "error", "agent run failed", map[string]any{"error": res.Error})
		runSpan.End()
		log.Printf("agent-runner finished with error: %s", res.Error)
		os.Exit(1)
	}
	obs.recordRunMetrics(ctx, "success", getEnv("INSTANCE_NAME", ""), modelName, getEnv("AGENT_NAMESPACE", ""), elapsed.Milliseconds(), inputTokens, outputTokens)
	logWithTrace(ctx, "info", "agent run succeeded", map[string]any{
		"duration_ms":   elapsed.Milliseconds(),
		"input_tokens":  inputTokens,
		"output_tokens": outputTokens,
		"tool_calls":    toolCalls,
	})
	runSpan.End()
	log.Println("agent-runner finished successfully")
}

// callAnthropic dispatches an agent run to the Anthropic provider.
// Retained as a thin wrapper around newAnthropicProvider + runAgentLoop for
// backward-compatible test coverage.
func callAnthropic(ctx context.Context, apiKey, baseURL, model, systemPrompt, task string, tools []ToolDef) (string, int, int, int, error) {
	p := newAnthropicProvider(apiKey, baseURL, model, systemPrompt, task, tools)
	return runAgentLoop(ctx, p)
}

// callOpenAI dispatches an agent run to the OpenAI-compatible provider path
// (OpenAI, LM Studio, Ollama, vLLM, Azure OpenAI, …).
func callOpenAI(ctx context.Context, provider, apiKey, baseURL, model, systemPrompt, task string, tools []ToolDef) (string, int, int, int, error) {
	p, err := newOpenAIProvider(provider, apiKey, baseURL, model, systemPrompt, task, tools)
	if err != nil {
		return "", 0, 0, 0, err
	}
	return runAgentLoop(ctx, p)
}

// callBedrock dispatches an agent run to the AWS Bedrock provider.
func callBedrock(ctx context.Context, model, systemPrompt, task string, tools []ToolDef) (string, int, int, int, error) {
	p, err := newBedrockProvider(ctx, model, systemPrompt, task, tools)
	if err != nil {
		return "", 0, 0, 0, err
	}
	return runAgentLoop(ctx, p)
}

// callBedrockWithClient accepts a pre-built client; used by tests to inject
// a mock Bedrock client without hitting AWS.
func callBedrockWithClient(ctx context.Context, client bedrockClientAPI, model, systemPrompt, task string, tools []ToolDef) (string, int, int, int, error) {
	p, err := newBedrockProviderWithClient(client, model, systemPrompt, task, tools)
	if err != nil {
		return "", 0, 0, 0, err
	}
	return runAgentLoop(ctx, p)
}

func writeJSON(path string, v any) {
	dir := filepath.Dir(path)
	_ = os.MkdirAll(dir, 0o755)
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		log.Printf("WARNING: failed to marshal JSON for %s: %v", path, err)
		return
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		log.Printf("WARNING: failed to write %s: %v", path, err)
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func fatal(msg string) {
	log.Println("FATAL: " + msg)
	_ = os.MkdirAll("/ipc/output", 0o755)
	_ = os.WriteFile("/ipc/done", []byte("done"), 0o644)
	writeJSON("/ipc/output/result.json", agentResult{
		Status: "error",
		Error:  msg,
	})
	os.Exit(1)
}

// extractMemoryUpdate looks for a memory update block in the LLM response.
// The agent is instructed to wrap its memory updates in:
//
//	__SYMPOZIUM_MEMORY__
//	<content>
//	__SYMPOZIUM_MEMORY_END__
func extractMemoryUpdate(response string) string {
	const startMarker = "__SYMPOZIUM_MEMORY__"
	const endMarker = "__SYMPOZIUM_MEMORY_END__"

	startIdx := strings.LastIndex(response, startMarker)
	if startIdx < 0 {
		return ""
	}
	payload := response[startIdx+len(startMarker):]
	endIdx := strings.Index(payload, endMarker)
	if endIdx < 0 {
		return ""
	}
	return strings.TrimSpace(payload[:endIdx])
}

// stripMemoryMarkers removes all __SYMPOZIUM_MEMORY__...END__ blocks from the
// response text so they don't appear in the TUI feed or channel messages.
func stripMemoryMarkers(response string) string {
	const startMarker = "__SYMPOZIUM_MEMORY__"
	const endMarker = "__SYMPOZIUM_MEMORY_END__"

	for {
		startIdx := strings.Index(response, startMarker)
		if startIdx < 0 {
			break
		}
		endIdx := strings.Index(response[startIdx:], endMarker)
		if endIdx < 0 {
			// Unclosed marker — strip from startMarker to end of string.
			response = strings.TrimSpace(response[:startIdx])
			break
		}
		// Remove the entire marker block.
		response = response[:startIdx] + response[startIdx+endIdx+len(endMarker):]
	}
	return strings.TrimSpace(response)
}
