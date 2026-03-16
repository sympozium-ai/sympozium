// Package mcpbridge implements the MCP bridge sidecar that translates between
// file-based IPC and remote MCP servers via JSON-RPC 2.0 Streamable HTTP.
package mcpbridge

import "encoding/json"

// ServersConfig is the top-level configuration loaded from the server registry YAML.
type ServersConfig struct {
	Servers []ServerConfig `yaml:"servers"`
}

// ServerConfig defines a remote MCP server endpoint.
type ServerConfig struct {
	Name        string            `yaml:"name"`
	URL         string            `yaml:"url"`
	Transport   string            `yaml:"transport"` // "streamable-http"
	ToolsPrefix string            `yaml:"toolsPrefix"`
	Timeout     int               `yaml:"timeout"` // seconds, default 30
	Auth        *AuthConfig       `yaml:"auth,omitempty"`
	Headers     map[string]string `yaml:"headers,omitempty"`
	ToolsAllow  []string          `yaml:"toolsAllow,omitempty"`
	ToolsDeny   []string          `yaml:"toolsDeny,omitempty"`
}

// AuthConfig defines authentication for an MCP server.
type AuthConfig struct {
	Type       string `yaml:"type"`                 // "bearer" or "header"
	SecretKey  string `yaml:"secretKey"`            // env var name with the token value
	HeaderName string `yaml:"headerName,omitempty"` // for type="header"
}

// MCPRequest is written by agent-runner to /ipc/tools/mcp-request-<id>.json.
type MCPRequest struct {
	ID        string            `json:"id"`
	Server    string            `json:"server,omitempty"` // server name or empty (resolved by prefix)
	Tool      string            `json:"tool"`             // prefixed tool name
	Arguments json.RawMessage   `json:"arguments"`        // tool arguments
	Meta      map[string]string `json:"_meta,omitempty"`  // trace context etc.
}

// MCPResult is written by mcp-bridge to /ipc/tools/mcp-result-<id>.json.
type MCPResult struct {
	ID      string          `json:"id"`
	Success bool            `json:"success"`
	Content json.RawMessage `json:"content,omitempty"` // MCP tool result content
	Error   string          `json:"error,omitempty"`
	IsError bool            `json:"isError,omitempty"` // MCP-level error (tool returned error)
}

// MCPToolManifest is written to /ipc/tools/mcp-tools.json at startup.
type MCPToolManifest struct {
	Tools []MCPToolDef `json:"tools"`
}

// MCPToolDef describes a single MCP tool discovered from a remote server.
type MCPToolDef struct {
	Name        string         `json:"name"` // prefixed name
	Description string         `json:"description"`
	Server      string         `json:"server"`      // server name for routing
	Timeout     int            `json:"timeout"`     // server timeout in seconds
	InputSchema map[string]any `json:"inputSchema"` // JSON Schema from MCP
}

// JSON-RPC 2.0 types for MCP protocol communication.

// JSONRPCRequest is a JSON-RPC 2.0 request.
type JSONRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// JSONRPCResponse is a JSON-RPC 2.0 response.
type JSONRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
}

// JSONRPCError is a JSON-RPC 2.0 error object.
type JSONRPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// MCP protocol-specific types.

// MCPInitializeParams are sent with the initialize request.
type MCPInitializeParams struct {
	ProtocolVersion string            `json:"protocolVersion"`
	Capabilities    MCPCapabilities   `json:"capabilities"`
	ClientInfo      MCPImplementation `json:"clientInfo"`
}

// MCPCapabilities declares client capabilities.
type MCPCapabilities struct{}

// MCPImplementation identifies a client or server.
type MCPImplementation struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// MCPInitializeResult is the response from initialize.
type MCPInitializeResult struct {
	ProtocolVersion string            `json:"protocolVersion"`
	Capabilities    json.RawMessage   `json:"capabilities"`
	ServerInfo      MCPImplementation `json:"serverInfo"`
}

// MCPToolsListResult is the response from tools/list.
type MCPToolsListResult struct {
	Tools []MCPTool `json:"tools"`
}

// MCPTool is a tool definition returned by an MCP server.
type MCPTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema"`
}

// MCPToolCallParams are sent with tools/call.
type MCPToolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	Meta      map[string]any  `json:"_meta,omitempty"`
}

// MCPToolCallResult is the response from tools/call.
type MCPToolCallResult struct {
	Content []MCPContent `json:"content"`
	IsError bool         `json:"isError,omitempty"`
}

// MCPContent is a content block in an MCP tool result.
type MCPContent struct {
	Type string `json:"type"` // "text", "image", "resource"
	Text string `json:"text,omitempty"`
}
