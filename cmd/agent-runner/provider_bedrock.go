package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/aws/smithy-go"
)

// bedrockClientAPI is the subset of the Bedrock Runtime client used by the
// Bedrock provider. It exists so tests can inject a mock without hitting AWS.
type bedrockClientAPI interface {
	Converse(ctx context.Context, params *bedrockruntime.ConverseInput, optFns ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error)
}

// newBedrockClient creates a real Bedrock Runtime client from the default AWS config.
// AWS SDK v2 auto-discovers credentials from AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY,
// AWS_SESSION_TOKEN, and AWS_REGION environment variables.
func newBedrockClient(ctx context.Context) (bedrockClientAPI, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	return bedrockruntime.NewFromConfig(cfg), nil
}

// bedrockProvider adapts the Bedrock Converse API to LLMProvider.
type bedrockProvider struct {
	client   bedrockClientAPI
	model    string
	system   string
	messages []types.Message
	tools    []types.Tool
}

func newBedrockProvider(ctx context.Context, model, systemPrompt, task string, tools []ToolDef) (*bedrockProvider, error) {
	client, err := newBedrockClient(ctx)
	if err != nil {
		return nil, err
	}
	return newBedrockProviderWithClient(client, model, systemPrompt, task, tools)
}

func newBedrockProviderWithClient(client bedrockClientAPI, model, systemPrompt, task string, tools []ToolDef) (*bedrockProvider, error) {
	var bedrockTools []types.Tool
	for _, t := range tools {
		schemaBytes, err := json.Marshal(t.Parameters)
		if err != nil {
			return nil, fmt.Errorf("marshaling tool schema for %s: %w", t.Name, err)
		}
		bedrockTools = append(bedrockTools, &types.ToolMemberToolSpec{
			Value: types.ToolSpecification{
				Name:        aws.String(t.Name),
				Description: aws.String(t.Description),
				InputSchema: &types.ToolInputSchemaMemberJson{
					Value: document.NewLazyDocument(json.RawMessage(schemaBytes)),
				},
			},
		})
	}

	return &bedrockProvider{
		client: client,
		model:  model,
		system: systemPrompt,
		messages: []types.Message{
			{
				Role: types.ConversationRoleUser,
				Content: []types.ContentBlock{
					&types.ContentBlockMemberText{Value: task},
				},
			},
		},
		tools: bedrockTools,
	}, nil
}

func (p *bedrockProvider) Name() string  { return "bedrock" }
func (p *bedrockProvider) Model() string { return p.model }

func (p *bedrockProvider) Chat(ctx context.Context) (ChatResult, error) {
	input := &bedrockruntime.ConverseInput{
		ModelId:  aws.String(p.model),
		Messages: p.messages,
		System: []types.SystemContentBlock{
			&types.SystemContentBlockMemberText{Value: p.system},
		},
	}
	if len(p.tools) > 0 {
		input.ToolConfig = &types.ToolConfiguration{
			Tools: p.tools,
		}
	}

	converseCtx := ctx
	if t := effectiveRequestTimeout("bedrock"); t > 0 {
		var cancel context.CancelFunc
		converseCtx, cancel = context.WithTimeout(ctx, t)
		defer cancel()
	}

	output, err := p.client.Converse(converseCtx, input)
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) {
			return ChatResult{}, fmt.Errorf("Bedrock API error (%s): %s",
				apiErr.ErrorCode(), apiErr.ErrorMessage())
		}
		return ChatResult{}, fmt.Errorf("Bedrock API error: %w", err)
	}

	result := ChatResult{
		FinishReason: string(output.StopReason),
	}
	if output.Usage != nil {
		result.InputTokens = int(aws.ToInt32(output.Usage.InputTokens))
		result.OutputTokens = int(aws.ToInt32(output.Usage.OutputTokens))
	}

	outputMsg, ok := output.Output.(*types.ConverseOutputMemberMessage)
	if !ok {
		return result, fmt.Errorf("unexpected Bedrock output shape")
	}

	var textContent string
	var toolUseBlocks []bedrockToolUse
	for _, block := range outputMsg.Value.Content {
		switch v := block.(type) {
		case *types.ContentBlockMemberText:
			textContent += v.Value
		case *types.ContentBlockMemberToolUse:
			inputBytes, _ := v.Value.Input.MarshalSmithyDocument()
			toolUseBlocks = append(toolUseBlocks, bedrockToolUse{
				ToolUseID: aws.ToString(v.Value.ToolUseId),
				Name:      aws.ToString(v.Value.Name),
				Input:     string(inputBytes),
			})
		}
	}
	result.Text = textContent

	if output.StopReason == types.StopReasonToolUse && len(toolUseBlocks) > 0 {
		for _, tu := range toolUseBlocks {
			result.ToolCalls = append(result.ToolCalls, ToolCall{
				ID:    tu.ToolUseID,
				Name:  tu.Name,
				Input: tu.Input,
			})
		}
		// Record the assistant's full content (text + tool_use) for history.
		p.messages = append(p.messages, types.Message{
			Role:    types.ConversationRoleAssistant,
			Content: append([]types.ContentBlock(nil), outputMsg.Value.Content...),
		})
	}

	return result, nil
}

func (p *bedrockProvider) AddToolResults(results []ToolResult) {
	var resultContent []types.ContentBlock
	for _, r := range results {
		toolResult := &types.ContentBlockMemberToolResult{
			Value: types.ToolResultBlock{
				ToolUseId: aws.String(r.CallID),
				Content: []types.ToolResultContentBlock{
					&types.ToolResultContentBlockMemberText{Value: r.Content},
				},
			},
		}
		if r.IsError {
			toolResult.Value.Status = types.ToolResultStatusError
		}
		resultContent = append(resultContent, toolResult)
	}
	p.messages = append(p.messages, types.Message{
		Role:    types.ConversationRoleUser,
		Content: resultContent,
	})
}

// bedrockToolUse holds the parsed fields from a Bedrock tool_use content block.
type bedrockToolUse struct {
	ToolUseID string
	Name      string
	Input     string
}
