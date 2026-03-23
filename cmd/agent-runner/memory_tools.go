package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// Memory tool name constants.
const (
	ToolMemorySearch = "memory_search"
	ToolMemoryStore  = "memory_store"
	ToolMemoryList   = "memory_list"
)

// memoryToolNames contains all memory tool names for lookup.
var memoryToolNames = map[string]bool{
	ToolMemorySearch: true,
	ToolMemoryStore:  true,
	ToolMemoryList:   true,
}

// isMemoryTool returns true if the tool name is a memory tool.
func isMemoryTool(name string) bool {
	return memoryToolNames[name]
}

// memoryServerURL is the HTTP endpoint of the memory server.
// Set from MEMORY_SERVER_URL env var at startup.
var memoryServerURL string

// memoryHTTPClient is a shared HTTP client with reasonable timeouts.
var memoryHTTPClient = &http.Client{Timeout: 5 * time.Second}

const (
	memoryMaxRetries  = 3
	memoryBaseBackoff = 500 * time.Millisecond
)

// memoryToolDefs returns the static tool definitions for memory tools.
func memoryToolDefs() []ToolDef {
	return []ToolDef{
		{
			Name:        ToolMemorySearch,
			Description: "Search agent memory for relevant past findings, investigations, and context. Use this before starting any investigation to check if similar issues have been seen before.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "Natural language search query (e.g., 'kafka consumer lag', 'OOM crash in payments service').",
					},
					"top_k": map[string]any{
						"type":        "integer",
						"description": "Maximum number of results to return (default: 5).",
					},
				},
				"required": []string{"query"},
			},
		},
		{
			Name:        ToolMemoryStore,
			Description: "Store a finding, investigation result, or important context in persistent memory for future agent runs. Include enough detail for a future agent to understand and reuse the information.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"content": map[string]any{
						"type":        "string",
						"description": "The content to store. Be specific: include root cause, resolution steps, service names, and namespace.",
					},
					"tags": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Tags for categorization (e.g., ['kafka', 'consumer-lag', 'payments-ns']).",
					},
				},
				"required": []string{"content"},
			},
		},
		{
			Name:        ToolMemoryList,
			Description: "List recent memory entries, optionally filtered by tag. Use this to browse what the agent has learned over time.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"tags": map[string]any{
						"type":        "string",
						"description": "Filter by tag (e.g., 'kafka'). Returns entries whose tags contain this string.",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum number of entries to return (default: 20).",
					},
				},
			},
		},
	}
}

// memoryAPIResponse matches the memory server's JSON response format.
type memoryAPIResponse struct {
	Success bool            `json:"success"`
	Content json.RawMessage `json:"content,omitempty"`
	Error   string          `json:"error,omitempty"`
}

// executeMemoryTool dispatches a memory tool call via HTTP to the memory server.
func executeMemoryTool(ctx context.Context, toolName string, argsJSON string) string {
	if memoryServerURL == "" {
		return "Error: memory server not configured (MEMORY_SERVER_URL not set)"
	}

	var args map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return fmt.Sprintf("Error parsing arguments: %v", err)
	}

	var resp *http.Response
	var err error

	for attempt := 0; attempt <= memoryMaxRetries; attempt++ {
		if attempt > 0 {
			backoff := memoryBaseBackoff * time.Duration(1<<(attempt-1))
			log.Printf("memory tool retry %d/%d after %v", attempt, memoryMaxRetries, backoff)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return fmt.Sprintf("Memory server error: %v", ctx.Err())
			}
		}

		switch toolName {
		case ToolMemorySearch:
			resp, err = memoryPost(ctx, "/search", args)
		case ToolMemoryStore:
			resp, err = memoryPost(ctx, "/store", args)
		case ToolMemoryList:
			resp, err = memoryGet(ctx, "/list", args)
		default:
			return fmt.Sprintf("Unknown memory tool: %s", toolName)
		}

		if err == nil {
			break
		}
		log.Printf("memory server call failed (attempt %d/%d): %v", attempt+1, memoryMaxRetries+1, err)
	}

	if err != nil {
		return fmt.Sprintf("Memory server error after %d attempts: %v", memoryMaxRetries+1, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return fmt.Sprintf("Error reading memory server response: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Sprintf("Memory server returned %d: %s", resp.StatusCode, string(body))
	}

	var apiResp memoryAPIResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return string(body)
	}

	if !apiResp.Success {
		return fmt.Sprintf("Memory error: %s", apiResp.Error)
	}

	return formatMemoryContent(apiResp.Content)
}

func memoryPost(ctx context.Context, path string, body any) (*http.Response, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", memoryServerURL+path, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return memoryHTTPClient.Do(req)
}

func memoryGet(ctx context.Context, path string, args map[string]any) (*http.Response, error) {
	url := memoryServerURL + path
	sep := "?"
	if tags, ok := args["tags"].(string); ok && tags != "" {
		url += sep + "tags=" + tags
		sep = "&"
	}
	if limit, ok := args["limit"].(float64); ok && limit > 0 {
		url += sep + "limit=" + fmt.Sprintf("%d", int(limit))
	}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	return memoryHTTPClient.Do(req)
}

// formatMemoryContent formats the API response content for the LLM.
func formatMemoryContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "(no results)"
	}

	// Try to format as an array of memory entries.
	var entries []map[string]any
	if err := json.Unmarshal(raw, &entries); err == nil && len(entries) > 0 {
		var sb strings.Builder
		for i, entry := range entries {
			if i > 0 {
				sb.WriteString("\n---\n")
			}
			content, _ := entry["content"].(string)
			tags, _ := entry["tags"].([]any)
			createdAt, _ := entry["created_at"].(string)
			id, _ := entry["id"].(float64)

			if id > 0 {
				sb.WriteString(fmt.Sprintf("**Memory #%d**", int(id)))
			}
			if createdAt != "" {
				sb.WriteString(fmt.Sprintf(" (%s)", createdAt))
			}
			if len(tags) > 0 {
				tagStrs := make([]string, 0, len(tags))
				for _, t := range tags {
					if s, ok := t.(string); ok {
						tagStrs = append(tagStrs, s)
					}
				}
				sb.WriteString(fmt.Sprintf(" [%s]", strings.Join(tagStrs, ", ")))
			}
			sb.WriteString("\n")
			sb.WriteString(content)
			sb.WriteString("\n")
		}
		return sb.String()
	}

	// For non-array responses (e.g., store result), return as-is.
	return string(raw)
}

func initMemoryTools() []ToolDef {
	memoryServerURL = os.Getenv("MEMORY_SERVER_URL")
	if memoryServerURL == "" {
		return nil
	}
	// Strip trailing slash.
	memoryServerURL = strings.TrimRight(memoryServerURL, "/")

	log.Printf("Memory server configured: %s", memoryServerURL)
	return memoryToolDefs()
}
