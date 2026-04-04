package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/azure"
	openaioption "github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"
)

// openaiProvider adapts the OpenAI Go SDK to the LLMProvider interface.
// It also handles all OpenAI-compatible backends: LM Studio, Ollama, vLLM,
// llamacpp, Azure OpenAI, and any OpenAI-schema provider.
type openaiProvider struct {
	client   openai.Client
	provider string // provider identifier for telemetry ("openai", "lm-studio", …)
	model    string
	messages []openai.ChatCompletionMessageParamUnion
	tools    []openai.ChatCompletionToolUnionParam
}

// newOpenAIProvider constructs an openaiProvider with the given config.
// The provider string determines SDK defaults and telemetry tags
// ("openai" | "lm-studio" | "ollama" | "azure-openai" | …).
func newOpenAIProvider(provider, apiKey, baseURL, model, systemPrompt, task string, tools []ToolDef) (*openaiProvider, error) {
	retries := effectiveMaxRetries(provider)
	reqTimeout := effectiveRequestTimeout(provider)

	opts := []openaioption.RequestOption{
		openaioption.WithMaxRetries(retries),
	}
	if reqTimeout > 0 {
		opts = append(opts, openaioption.WithRequestTimeout(reqTimeout))
	}

	switch provider {
	case "azure-openai":
		if baseURL == "" {
			return nil, fmt.Errorf("Azure OpenAI requires MODEL_BASE_URL to be set")
		}
		apiVersion := getEnv("AZURE_OPENAI_API_VERSION", "2024-06-01")
		opts = append(opts,
			azure.WithEndpoint(baseURL, apiVersion),
			azure.WithAPIKey(apiKey),
		)
	default:
		if apiKey != "" {
			opts = append(opts, openaioption.WithAPIKey(apiKey))
		}
		if baseURL != "" {
			opts = append(opts, openaioption.WithBaseURL(baseURL))
		} else if provider == "ollama" {
			opts = append(opts, openaioption.WithBaseURL("http://ollama.default.svc:11434/v1"))
		} else if provider == "lm-studio" {
			opts = append(opts, openaioption.WithBaseURL("http://localhost:1234/v1"))
		}
	}

	var oaiTools []openai.ChatCompletionToolUnionParam
	for _, t := range tools {
		oaiTools = append(oaiTools, openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
			Name:        t.Name,
			Description: openai.String(t.Description),
			Parameters:  shared.FunctionParameters(t.Parameters),
		}))
	}

	return &openaiProvider{
		client:   openai.NewClient(opts...),
		provider: provider,
		model:    model,
		messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(systemPrompt),
			openai.UserMessage(task),
		},
		tools: oaiTools,
	}, nil
}

func (p *openaiProvider) Name() string  { return p.provider }
func (p *openaiProvider) Model() string { return p.model }

func (p *openaiProvider) Chat(ctx context.Context) (ChatResult, error) {
	params := openai.ChatCompletionNewParams{
		Model:    openai.ChatModel(p.model),
		Messages: p.messages,
	}
	if len(p.tools) > 0 {
		params.Tools = p.tools
	}

	completion, err := p.client.Chat.Completions.New(ctx, params)
	if err != nil {
		var apiErr *openai.Error
		if errors.As(err, &apiErr) {
			return ChatResult{}, fmt.Errorf("OpenAI API error (HTTP %d): %s",
				apiErr.StatusCode, truncate(apiErr.Error(), 500))
		}
		return ChatResult{}, fmt.Errorf("OpenAI API error: %w", err)
	}

	if len(completion.Choices) == 0 {
		return ChatResult{
				InputTokens:  int(completion.Usage.PromptTokens),
				OutputTokens: int(completion.Usage.CompletionTokens),
			},
			fmt.Errorf("no choices in completion response")
	}

	choice := completion.Choices[0]
	result := ChatResult{
		Text:         choice.Message.Content,
		InputTokens:  int(completion.Usage.PromptTokens),
		OutputTokens: int(completion.Usage.CompletionTokens),
		FinishReason: choice.FinishReason,
	}

	// Only treat tool calls as actionable when the model signalled it's
	// awaiting tool results. Some providers stuff tool_calls into a final
	// message with finish_reason="stop"; the OpenAI contract is to loop
	// only when finish_reason="tool_calls".
	if choice.FinishReason == "tool_calls" && len(choice.Message.ToolCalls) > 0 {
		for _, tc := range choice.Message.ToolCalls {
			fc := tc.AsFunction()
			result.ToolCalls = append(result.ToolCalls, ToolCall{
				ID:    fc.ID,
				Name:  fc.Function.Name,
				Input: fc.Function.Arguments,
			})
		}
		// Record the assistant message with tool_calls so the next Chat
		// call includes it in history.
		p.messages = append(p.messages, choice.Message.ToParam())
	}

	return result, nil
}

func (p *openaiProvider) AddToolResults(results []ToolResult) {
	for _, r := range results {
		p.messages = append(p.messages, openai.ToolMessage(r.Content, r.CallID))
	}
}
