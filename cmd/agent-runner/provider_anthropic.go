package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	anthropicoption "github.com/anthropics/anthropic-sdk-go/option"
)

// anthropicProvider adapts the Anthropic Messages API to LLMProvider.
type anthropicProvider struct {
	client   anthropic.Client
	model    string
	system   string
	messages []anthropic.MessageParam
	tools    []anthropic.ToolUnionParam
}

func newAnthropicProvider(apiKey, baseURL, model, systemPrompt, task string, tools []ToolDef) *anthropicProvider {
	opts := []anthropicoption.RequestOption{
		anthropicoption.WithMaxRetries(effectiveMaxRetries("anthropic")),
	}
	if t := effectiveRequestTimeout("anthropic"); t > 0 {
		opts = append(opts, anthropicoption.WithRequestTimeout(t))
	}
	if apiKey != "" {
		opts = append(opts, anthropicoption.WithAPIKey(apiKey))
	}
	if baseURL != "" {
		opts = append(opts, anthropicoption.WithBaseURL(baseURL))
	}

	var anthropicTools []anthropic.ToolUnionParam
	for _, t := range tools {
		schema := anthropic.ToolInputSchemaParam{
			Properties: t.Parameters["properties"],
		}
		if req, ok := t.Parameters["required"].([]string); ok {
			schema.Required = req
		}
		tool := anthropic.ToolUnionParamOfTool(schema, t.Name)
		tool.OfTool.Description = anthropic.String(t.Description)
		anthropicTools = append(anthropicTools, tool)
	}

	return &anthropicProvider{
		client: anthropic.NewClient(opts...),
		model:  model,
		system: systemPrompt,
		messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(task)),
		},
		tools: anthropicTools,
	}
}

func (p *anthropicProvider) Name() string  { return "anthropic" }
func (p *anthropicProvider) Model() string { return p.model }

func (p *anthropicProvider) Chat(ctx context.Context) (ChatResult, error) {
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(p.model),
		MaxTokens: int64(8192),
		System: []anthropic.TextBlockParam{
			{Text: p.system},
		},
		Messages: p.messages,
	}
	if len(p.tools) > 0 {
		params.Tools = p.tools
	}

	msg, err := p.client.Messages.New(ctx, params)
	if err != nil {
		var apiErr *anthropic.Error
		if errors.As(err, &apiErr) {
			return ChatResult{}, fmt.Errorf("Anthropic API error (HTTP %d): %s",
				apiErr.StatusCode, truncate(apiErr.Error(), 500))
		}
		return ChatResult{}, fmt.Errorf("Anthropic API error: %w", err)
	}

	var textContent strings.Builder
	var toolUseBlocks []anthropic.ToolUseBlock
	for _, block := range msg.Content {
		switch v := block.AsAny().(type) {
		case anthropic.TextBlock:
			textContent.WriteString(v.Text)
		case anthropic.ToolUseBlock:
			toolUseBlocks = append(toolUseBlocks, v)
		}
	}

	result := ChatResult{
		Text:         textContent.String(),
		InputTokens:  int(msg.Usage.InputTokens),
		OutputTokens: int(msg.Usage.OutputTokens),
		FinishReason: string(msg.StopReason),
	}

	// Continue the loop only when the model explicitly stopped to call
	// tools. Anthropic guarantees StopReasonToolUse when tool_use blocks
	// should trigger follow-up tool execution.
	if msg.StopReason == anthropic.StopReasonToolUse && len(toolUseBlocks) > 0 {
		for _, tu := range toolUseBlocks {
			result.ToolCalls = append(result.ToolCalls, ToolCall{
				ID:    tu.ID,
				Name:  tu.Name,
				Input: string(tu.Input),
			})
		}
		// Append the assistant message (text + tool_use) to history.
		var assistantBlocks []anthropic.ContentBlockParamUnion
		for _, block := range msg.Content {
			switch v := block.AsAny().(type) {
			case anthropic.TextBlock:
				assistantBlocks = append(assistantBlocks, anthropic.NewTextBlock(v.Text))
			case anthropic.ToolUseBlock:
				assistantBlocks = append(assistantBlocks,
					anthropic.NewToolUseBlock(v.ID, json.RawMessage(v.Input), v.Name))
			}
		}
		p.messages = append(p.messages, anthropic.NewAssistantMessage(assistantBlocks...))
	}

	return result, nil
}

func (p *anthropicProvider) AddToolResults(results []ToolResult) {
	var blocks []anthropic.ContentBlockParamUnion
	for _, r := range results {
		blocks = append(blocks, anthropic.NewToolResultBlock(r.CallID, r.Content, r.IsError))
	}
	p.messages = append(p.messages, anthropic.NewUserMessage(blocks...))
}
