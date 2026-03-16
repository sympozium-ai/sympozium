package mcpbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

// discoverTools connects to all configured MCP servers, lists their tools,
// and builds a unified tool manifest with prefixed names.
// Servers that fail discovery are retried with backoff (6 attempts, 10s apart).
func (b *Bridge) discoverTools(ctx context.Context) (*MCPToolManifest, error) {
	manifest := &MCPToolManifest{
		Tools: []MCPToolDef{},
	}

	for _, srv := range b.config.Servers {
		var tools []MCPTool
		var err error
		var client *Client

		maxRetries := 6
		retryInterval := 10 * time.Second

		for attempt := 1; attempt <= maxRetries; attempt++ {
			client = NewClient(srv)
			tools, err = client.DiscoverTools(ctx)
			if err == nil {
				break
			}
			if attempt < maxRetries {
				log.Printf("WARNING: discover attempt %d/%d failed for %q: %v (retrying in %s)",
					attempt, maxRetries, srv.Name, err, retryInterval)
				select {
				case <-ctx.Done():
					return manifest, ctx.Err()
				case <-time.After(retryInterval):
				}
			} else {
				log.Printf("WARNING: all %d discover attempts failed for %q: %v", maxRetries, srv.Name, err)
			}
		}

		if err != nil {
			continue
		}

		b.clients[srv.Name] = client

		// Apply allow/deny filtering before registering tools.
		tools = filterTools(tools, srv.ToolsAllow, srv.ToolsDeny)

		for _, tool := range tools {
			prefixedName := srv.ToolsPrefix + "_" + tool.Name
			b.toolIndex[prefixedName] = srv.Name

			manifest.Tools = append(manifest.Tools, MCPToolDef{
				Name:        prefixedName,
				Description: tool.Description,
				Server:      srv.Name,
				Timeout:     srv.Timeout,
				InputSchema: tool.InputSchema,
			})
		}

		log.Printf("Discovered %d tools from %q (prefix=%q)", len(tools), srv.Name, srv.ToolsPrefix)
	}

	return manifest, nil
}

// WriteManifest writes the tool manifest atomically to the given path.
func WriteManifest(path string, manifest *MCPToolManifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling manifest: %w", err)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating manifest directory: %w", err)
	}

	// Write atomically: temp file + rename
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("writing temp manifest: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("renaming manifest: %w", err)
	}

	return nil
}

// filterTools applies allow/deny lists to filter MCP tools.
// If allow is non-empty, only tools in that set pass. Then deny removes any remaining.
func filterTools(tools []MCPTool, allow, deny []string) []MCPTool {
	if len(allow) == 0 && len(deny) == 0 {
		return tools
	}
	allowSet := toSet(allow)
	denySet := toSet(deny)
	var filtered []MCPTool
	for _, t := range tools {
		if len(allowSet) > 0 && !allowSet[t.Name] {
			continue
		}
		if denySet[t.Name] {
			continue
		}
		filtered = append(filtered, t)
	}
	return filtered
}

// toSet converts a string slice to a set (map[string]bool).
func toSet(items []string) map[string]bool {
	if len(items) == 0 {
		return nil
	}
	s := make(map[string]bool, len(items))
	for _, item := range items {
		s[item] = true
	}
	return s
}
