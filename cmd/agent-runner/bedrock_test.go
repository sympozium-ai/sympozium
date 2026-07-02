package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

// mockBedrockClient implements bedrockClientAPI for testing.
type mockBedrockClient struct {
	calls   int
	handler func(ctx context.Context, input *bedrockruntime.ConverseInput) (*bedrockruntime.ConverseOutput, error)
}

func (m *mockBedrockClient) Converse(ctx context.Context, params *bedrockruntime.ConverseInput, optFns ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error) {
	m.calls++
	return m.handler(ctx, params)
}

func TestCallBedrock_MockClient(t *testing.T) {
	client := &mockBedrockClient{
		handler: func(ctx context.Context, input *bedrockruntime.ConverseInput) (*bedrockruntime.ConverseOutput, error) {
			// Verify model ID is passed through.
			if aws.ToString(input.ModelId) != "anthropic.claude-sonnet-4-20250514-v1:0" {
				t.Errorf("unexpected model: %s", aws.ToString(input.ModelId))
			}
			// Verify system prompt is set.
			if len(input.System) == 0 {
				t.Error("expected system prompt")
			}
			return &bedrockruntime.ConverseOutput{
				Output: &types.ConverseOutputMemberMessage{
					Value: types.Message{
						Role: types.ConversationRoleAssistant,
						Content: []types.ContentBlock{
							&types.ContentBlockMemberText{Value: "Hello from Bedrock!"},
						},
					},
				},
				StopReason: types.StopReasonEndTurn,
				Usage: &types.TokenUsage{
					InputTokens:  aws.Int32(10),
					OutputTokens: aws.Int32(5),
				},
			}, nil
		},
	}

	ctx := t.Context()
	text, inTok, outTok, toolCalls, err := callBedrockWithClient(ctx, client, "anthropic.claude-sonnet-4-20250514-v1:0", "You are helpful.", "Say hello", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "Hello from Bedrock!" {
		t.Errorf("got text=%q, want %q", text, "Hello from Bedrock!")
	}
	if inTok != 10 {
		t.Errorf("got inputTokens=%d, want 10", inTok)
	}
	if outTok != 5 {
		t.Errorf("got outputTokens=%d, want 5", outTok)
	}
	if toolCalls != 0 {
		t.Errorf("got toolCalls=%d, want 0", toolCalls)
	}
	if client.calls != 1 {
		t.Errorf("got %d calls, want 1", client.calls)
	}
}

func TestCallBedrock_ToolUseFlow(t *testing.T) {
	client := &mockBedrockClient{
		handler: func(ctx context.Context, input *bedrockruntime.ConverseInput) (*bedrockruntime.ConverseOutput, error) {
			// First call: return a tool_use block.
			if len(input.Messages) == 1 {
				// The tool schema must serialize as a JSON object; smithy
				// documents encode []byte/json.RawMessage as a byte array,
				// which Bedrock rejects (issue #255).
				toolSpec, ok := input.ToolConfig.Tools[0].(*types.ToolMemberToolSpec)
				if !ok {
					t.Fatalf("unexpected tool type %T", input.ToolConfig.Tools[0])
				}
				schemaDoc := toolSpec.Value.InputSchema.(*types.ToolInputSchemaMemberJson).Value
				schemaJSON, err := schemaDoc.MarshalSmithyDocument()
				if err != nil {
					t.Fatalf("marshaling tool schema: %v", err)
				}
				var schema map[string]any
				if err := json.Unmarshal(schemaJSON, &schema); err != nil {
					t.Fatalf("tool schema is not a JSON object: %v (got %s)", err, schemaJSON)
				}
				if schema["type"] != "object" {
					t.Errorf("got schema type %v, want object", schema["type"])
				}

				inputDoc := document.NewLazyDocument(map[string]any{"command": "echo hi"})
				return &bedrockruntime.ConverseOutput{
					Output: &types.ConverseOutputMemberMessage{
						Value: types.Message{
							Role: types.ConversationRoleAssistant,
							Content: []types.ContentBlock{
								&types.ContentBlockMemberToolUse{
									Value: types.ToolUseBlock{
										ToolUseId: aws.String("tool-1"),
										Name:      aws.String("execute_command"),
										Input:     inputDoc,
									},
								},
							},
						},
					},
					StopReason: types.StopReasonToolUse,
					Usage: &types.TokenUsage{
						InputTokens:  aws.Int32(15),
						OutputTokens: aws.Int32(8),
					},
				}, nil
			}

			// Verify tool result was passed back.
			lastMsg := input.Messages[len(input.Messages)-1]
			if lastMsg.Role != types.ConversationRoleUser {
				t.Errorf("expected user role for tool result, got %s", lastMsg.Role)
			}

			// Second call: return final text.
			return &bedrockruntime.ConverseOutput{
				Output: &types.ConverseOutputMemberMessage{
					Value: types.Message{
						Role: types.ConversationRoleAssistant,
						Content: []types.ContentBlock{
							&types.ContentBlockMemberText{Value: "Done!"},
						},
					},
				},
				StopReason: types.StopReasonEndTurn,
				Usage: &types.TokenUsage{
					InputTokens:  aws.Int32(20),
					OutputTokens: aws.Int32(3),
				},
			}, nil
		},
	}

	ctx := t.Context()
	tools := []ToolDef{
		{
			Name:        "execute_command",
			Description: "Execute a command",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{"type": "string"},
				},
				"required": []string{"command"},
			},
		},
	}

	text, inTok, outTok, toolCalls, err := callBedrockWithClient(ctx, client, "model", "system", "task", tools)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "Done!" {
		t.Errorf("got text=%q, want %q", text, "Done!")
	}
	if inTok != 35 {
		t.Errorf("got inputTokens=%d, want 35", inTok)
	}
	if outTok != 11 {
		t.Errorf("got outputTokens=%d, want 11", outTok)
	}
	if toolCalls != 1 {
		t.Errorf("got toolCalls=%d, want 1", toolCalls)
	}
	if client.calls != 2 {
		t.Errorf("got %d API calls, want 2", client.calls)
	}
}

func TestCallBedrock_ServerError(t *testing.T) {
	client := &mockBedrockClient{
		handler: func(ctx context.Context, input *bedrockruntime.ConverseInput) (*bedrockruntime.ConverseOutput, error) {
			return nil, fmt.Errorf("connection refused")
		},
	}

	ctx := t.Context()
	_, _, _, _, err := callBedrockWithClient(ctx, client, "model", "system", "task", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "Bedrock API error") {
		t.Errorf("error should mention Bedrock: %v", err)
	}
}
