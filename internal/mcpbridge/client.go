package mcpbridge

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
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

var clientTracer = otel.Tracer("sympozium.ai/mcp-client")

// MaxResponseSize is the default maximum response body size (1MB).
const MaxResponseSize = 1 << 20

// Client communicates with a single MCP server via JSON-RPC 2.0 Streamable HTTP.
type Client struct {
	serverConfig ServerConfig
	httpClient   *http.Client
	mu           sync.Mutex // protects sessionID
	sessionID    string
	nextID       atomic.Int64
}

// NewClient creates an MCP client for the given server configuration.
func NewClient(cfg ServerConfig) *Client {
	return &Client{
		serverConfig: cfg,
		httpClient: &http.Client{
			Timeout: time.Duration(cfg.Timeout) * time.Second,
		},
	}
}

// DiscoverTools initializes the MCP session and lists available tools.
// If initialize fails, it falls back to listing tools directly (some servers
// like Dynatrace MCP do not implement the initialize handshake over stdio).
func (c *Client) DiscoverTools(ctx context.Context) ([]MCPTool, error) {
	if err := c.initialize(ctx); err != nil {
		log.Printf("WARNING: initialize failed for %q, trying tools/list directly: %v", c.serverConfig.Name, err)
	}
	return c.listTools(ctx)
}

// CallTool invokes a tool on the MCP server and returns the result.
func (c *Client) CallTool(ctx context.Context, name string, arguments json.RawMessage, meta map[string]any) (*MCPToolCallResult, error) {
	return c.callTool(ctx, name, arguments, meta)
}

// initialize sends the MCP initialize request to establish a session.
func (c *Client) initialize(ctx context.Context) error {
	params := MCPInitializeParams{
		ProtocolVersion: "2024-11-05",
		Capabilities:    MCPCapabilities{},
		ClientInfo: MCPImplementation{
			Name:    "sympozium-mcp-bridge",
			Version: "1.0.0",
		},
	}

	var result MCPInitializeResult
	if err := c.call(ctx, "initialize", params, &result); err != nil {
		return err
	}

	// Send initialized notification (no response expected, but we do it as a best-effort POST)
	// _ = c.notify(ctx, "notifications/initialized") // TEMP: skip until dec8f88 adapter deployed

	return nil
}

// listTools sends the tools/list request and returns discovered tools.
func (c *Client) listTools(ctx context.Context) ([]MCPTool, error) {
	var result MCPToolsListResult
	if err := c.call(ctx, "tools/list", nil, &result); err != nil {
		return nil, err
	}
	return result.Tools, nil
}

// callTool sends a tools/call request.
func (c *Client) callTool(ctx context.Context, name string, arguments json.RawMessage, meta map[string]any) (*MCPToolCallResult, error) {
	params := MCPToolCallParams{
		Name:      name,
		Arguments: arguments,
		Meta:      meta,
	}

	var result MCPToolCallResult
	if err := c.call(ctx, "tools/call", params, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// call sends a JSON-RPC 2.0 request and unmarshals the result.
func (c *Client) call(ctx context.Context, method string, params any, result any) error {
	ctx, span := clientTracer.Start(ctx, "mcp.jsonrpc",
		trace.WithAttributes(
			attribute.String("mcp.method", method),
			attribute.String("mcp.server", c.serverConfig.Name),
		),
	)
	defer span.End()

	id := c.nextID.Add(1)
	req := JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshalling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.serverConfig.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating HTTP request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")

	// Set session ID if we have one from a previous response
	c.mu.Lock()
	sid := c.sessionID
	c.mu.Unlock()
	if sid != "" {
		httpReq.Header.Set("Mcp-Session-Id", sid)
	}

	// Apply auth
	c.applyAuth(httpReq)

	// Apply custom headers
	for k, v := range c.serverConfig.Headers {
		httpReq.Header.Set(k, v)
	}
	// Propagate W3C trace context to MCP server
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(httpReq.Header))

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "http request failed")
		return fmt.Errorf("HTTP request to %s: %w", c.serverConfig.URL, err)
	}
	defer resp.Body.Close()

	// Capture session ID from response
	if newSID := resp.Header.Get("Mcp-Session-Id"); newSID != "" {
		c.mu.Lock()
		c.sessionID = newSID
		c.mu.Unlock()
	}

	// Read response with size limit
	limited := io.LimitReader(resp.Body, MaxResponseSize+1)
	respBody, err := io.ReadAll(limited)
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}
	if len(respBody) > MaxResponseSize {
		return fmt.Errorf("response exceeds maximum size (%d bytes)", MaxResponseSize)
	}

	span.SetAttributes(attribute.Int("http.status_code", resp.StatusCode))

	if resp.StatusCode != http.StatusOK {
		// Truncate error body to avoid leaking large/sensitive responses into logs
		errBody := string(respBody)
		if len(errBody) > 512 {
			errBody = errBody[:512] + "...(truncated)"
		}
		err := fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, c.serverConfig.URL, errBody)
		span.RecordError(err)
		span.SetStatus(codes.Error, fmt.Sprintf("HTTP %d", resp.StatusCode))
		return err
	}

	// Handle SSE responses (Content-Type: text/event-stream)
	// MCP servers may respond with SSE even when Accept includes application/json
	actualBody := respBody
	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "text/event-stream") || (len(respBody) > 0 && respBody[0] != '{' && respBody[0] != '[') {
		// Parse SSE: look for "data: {...}" lines
		for _, line := range strings.Split(string(respBody), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "data: ") {
				data := strings.TrimPrefix(line, "data: ")
				if len(data) > 0 && data[0] == '{' {
					actualBody = []byte(data)
					break
				}
			}
		}
		if bytes.Equal(actualBody, respBody) && (len(actualBody) == 0 || actualBody[0] != '{') {
			preview := string(respBody)
			if len(preview) > 200 {
				preview = preview[:200]
			}
			return fmt.Errorf("SSE response contained no JSON-RPC data: %s", preview)
		}
	}

	var rpcResp JSONRPCResponse
	if err := json.Unmarshal(actualBody, &rpcResp); err != nil {
		return fmt.Errorf("parsing JSON-RPC response: %w", err)
	}

	if rpcResp.Error != nil {
		err := fmt.Errorf("JSON-RPC error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
		span.RecordError(err)
		span.SetStatus(codes.Error, "jsonrpc error")
		return err
	}

	if result != nil && rpcResp.Result != nil {
		if err := json.Unmarshal(rpcResp.Result, result); err != nil {
			return fmt.Errorf("parsing result: %w", err)
		}
	}

	return nil
}

// notify sends a JSON-RPC 2.0 notification (no id, no response expected).
func (c *Client) notify(ctx context.Context, method string) error {
	type notification struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
	}

	body, err := json.Marshal(notification{JSONRPC: "2.0", Method: method})
	if err != nil {
		return err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.serverConfig.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	c.mu.Lock()
	sid := c.sessionID
	c.mu.Unlock()
	if sid != "" {
		httpReq.Header.Set("Mcp-Session-Id", sid)
	}
	c.applyAuth(httpReq)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	// Drain body to enable connection reuse
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return nil
}

// applyAuth adds authentication headers to the request.
func (c *Client) applyAuth(req *http.Request) {
	if c.serverConfig.Auth == nil {
		return
	}

	token := os.Getenv(c.serverConfig.Auth.SecretKey)
	if token == "" {
		log.Printf("WARNING: auth env var %q is empty for server %q", c.serverConfig.Auth.SecretKey, c.serverConfig.Name)
		return
	}

	switch c.serverConfig.Auth.Type {
	case "bearer":
		req.Header.Set("Authorization", "Bearer "+token)
	case "header":
		req.Header.Set(c.serverConfig.Auth.HeaderName, token)
	}
}
