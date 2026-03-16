package mcpbridge

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	yaml := `servers:
  - name: k8s-networking
    url: http://mcp-k8s-networking.tools.svc:8080/mcp
    transport: streamable-http
    toolsPrefix: k8s_net
    timeout: 30
    auth:
      type: bearer
      secretKey: MCP_K8S_NET_TOKEN
    headers:
      X-Custom: value
  - name: otel-collector
    url: http://otel-collector-mcp.observability.svc:8080/mcp
    toolsPrefix: otel
`
	dir := t.TempDir()
	path := filepath.Join(dir, "servers.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	if len(cfg.Servers) != 2 {
		t.Fatalf("expected 2 servers, got %d", len(cfg.Servers))
	}

	s0 := cfg.Servers[0]
	if s0.Name != "k8s-networking" {
		t.Errorf("server[0].Name = %q, want %q", s0.Name, "k8s-networking")
	}
	if s0.Timeout != 30 {
		t.Errorf("server[0].Timeout = %d, want 30", s0.Timeout)
	}
	if s0.Auth == nil || s0.Auth.Type != "bearer" {
		t.Errorf("server[0].Auth not parsed correctly")
	}
	if s0.Headers["X-Custom"] != "value" {
		t.Errorf("server[0].Headers not parsed correctly")
	}

	s1 := cfg.Servers[1]
	if s1.Timeout != DefaultTimeout {
		t.Errorf("server[1].Timeout = %d, want default %d", s1.Timeout, DefaultTimeout)
	}
	if s1.Transport != "streamable-http" {
		t.Errorf("server[1].Transport = %q, want default \"streamable-http\"", s1.Transport)
	}
}

func TestLoadConfigMissingFile(t *testing.T) {
	_, err := LoadConfig("/nonexistent/path.yaml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestValidateConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     ServersConfig
		wantErr string
	}{
		{
			name: "valid config",
			cfg: ServersConfig{
				Servers: []ServerConfig{
					{Name: "s1", URL: "http://localhost:8080/mcp", ToolsPrefix: "s1", Transport: "streamable-http"},
					{Name: "s2", URL: "http://localhost:8081/mcp", ToolsPrefix: "s2", Transport: "streamable-http"},
				},
			},
			wantErr: "",
		},
		{
			name:    "empty config is valid",
			cfg:     ServersConfig{},
			wantErr: "",
		},
		{
			name: "missing name",
			cfg: ServersConfig{
				Servers: []ServerConfig{
					{URL: "http://localhost:8080/mcp", ToolsPrefix: "s1", Transport: "streamable-http"},
				},
			},
			wantErr: "name is required",
		},
		{
			name: "missing url",
			cfg: ServersConfig{
				Servers: []ServerConfig{
					{Name: "s1", ToolsPrefix: "s1", Transport: "streamable-http"},
				},
			},
			wantErr: "url is required",
		},
		{
			name: "missing prefix",
			cfg: ServersConfig{
				Servers: []ServerConfig{
					{Name: "s1", URL: "http://localhost:8080/mcp", Transport: "streamable-http"},
				},
			},
			wantErr: "toolsPrefix is required",
		},
		{
			name: "duplicate name",
			cfg: ServersConfig{
				Servers: []ServerConfig{
					{Name: "s1", URL: "http://localhost:8080/mcp", ToolsPrefix: "a", Transport: "streamable-http"},
					{Name: "s1", URL: "http://localhost:8081/mcp", ToolsPrefix: "b", Transport: "streamable-http"},
				},
			},
			wantErr: "duplicate server name",
		},
		{
			name: "duplicate prefix",
			cfg: ServersConfig{
				Servers: []ServerConfig{
					{Name: "s1", URL: "http://localhost:8080/mcp", ToolsPrefix: "same", Transport: "streamable-http"},
					{Name: "s2", URL: "http://localhost:8081/mcp", ToolsPrefix: "same", Transport: "streamable-http"},
				},
			},
			wantErr: "duplicate toolsPrefix",
		},
		{
			name: "invalid transport",
			cfg: ServersConfig{
				Servers: []ServerConfig{
					{Name: "s1", URL: "http://localhost:8080/mcp", ToolsPrefix: "s1", Transport: "stdio"},
				},
			},
			wantErr: "unsupported transport",
		},
		{
			name: "invalid auth type",
			cfg: ServersConfig{
				Servers: []ServerConfig{
					{Name: "s1", URL: "http://localhost:8080/mcp", ToolsPrefix: "s1", Transport: "streamable-http",
						Auth: &AuthConfig{Type: "mtls", SecretKey: "key"}},
				},
			},
			wantErr: "unsupported auth type",
		},
		{
			name: "header auth missing headerName",
			cfg: ServersConfig{
				Servers: []ServerConfig{
					{Name: "s1", URL: "http://localhost:8080/mcp", ToolsPrefix: "s1", Transport: "streamable-http",
						Auth: &AuthConfig{Type: "header", SecretKey: "key"}},
				},
			},
			wantErr: "auth.headerName is required",
		},
		{
			name: "invalid url",
			cfg: ServersConfig{
				Servers: []ServerConfig{
					{Name: "s1", URL: "not a url", ToolsPrefix: "s1", Transport: "streamable-http"},
				},
			},
			wantErr: "invalid url",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateConfig(&tt.cfg)
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			} else {
				if err == nil {
					t.Errorf("expected error containing %q, got nil", tt.wantErr)
				} else if !contains(err.Error(), tt.wantErr) {
					t.Errorf("error %q does not contain %q", err.Error(), tt.wantErr)
				}
			}
		})
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && searchString(s, sub)
}

func searchString(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
