package mcpbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

var bridgeTracer = otel.Tracer("sympozium.ai/mcp-bridge")
var bridgeMeter = otel.Meter("sympozium.ai/mcp-bridge")

var (
	mcpToolCalls, _    = bridgeMeter.Int64Counter("mcp.bridge.tool_calls", metric.WithUnit("{call}"), metric.WithDescription("MCP tool calls dispatched"))
	mcpToolErrors, _   = bridgeMeter.Int64Counter("mcp.bridge.tool_errors", metric.WithUnit("{error}"), metric.WithDescription("MCP tool call errors"))
	mcpToolDuration, _ = bridgeMeter.Float64Histogram("mcp.bridge.tool_duration_ms", metric.WithUnit("ms"), metric.WithDescription("MCP tool call duration"))
)

// maxConcurrent is the maximum number of concurrent MCP requests.
const maxConcurrent = 10

// Bridge is the MCP bridge sidecar process.
type Bridge struct {
	config       *ServersConfig
	ipcPath      string
	manifestPath string
	agentRunID   string
	clients      map[string]*Client // server name -> client
	toolIndex    map[string]string  // prefixed tool name -> server name
	prefixIndex  map[string]string  // tools prefix -> server name
	processed    sync.Map           // dedup fsnotify events
}

// NewBridge creates a new MCP bridge.
func NewBridge(cfg *ServersConfig, ipcPath, manifestPath, agentRunID string) *Bridge {
	prefixIdx := make(map[string]string, len(cfg.Servers))
	for _, s := range cfg.Servers {
		prefixIdx[s.ToolsPrefix] = s.Name
	}

	return &Bridge{
		config:       cfg,
		ipcPath:      ipcPath,
		manifestPath: manifestPath,
		agentRunID:   agentRunID,
		clients:      make(map[string]*Client),
		toolIndex:    make(map[string]string),
		prefixIndex:  prefixIdx,
	}
}

// Run starts the MCP bridge: discovers tools, writes manifest, then watches for requests.
func (b *Bridge) Run(ctx context.Context) error {
	ctx, span := bridgeTracer.Start(ctx, "mcp-bridge.run",
		trace.WithAttributes(attribute.String("agent_run_id", b.agentRunID)),
	)
	defer span.End()

	// Phase 1: Connect to MCP servers and discover tools
	manifest, err := b.discoverTools(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "tool discovery failed")
		return err
	}

	span.SetAttributes(attribute.Int("mcp.tools_discovered", len(manifest.Tools)))

	// Phase 2: Write tool manifest for agent-runner
	if err := WriteManifest(b.manifestPath, manifest); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "manifest write failed")
		return err
	}

	log.Printf("Wrote tool manifest with %d tools to %s", len(manifest.Tools), b.manifestPath)

	// Phase 3: Watch for MCP requests and dispatch
	return b.watchAndDispatch(ctx)
}

// watchAndDispatch watches the IPC tools directory for MCP request files
// and dispatches them to the appropriate MCP server.
func (b *Bridge) watchAndDispatch(ctx context.Context) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("creating fsnotify watcher: %w", err)
	}
	defer watcher.Close()

	if err := os.MkdirAll(b.ipcPath, 0o755); err != nil {
		return fmt.Errorf("creating IPC directory: %w", err)
	}

	if err := watcher.Add(b.ipcPath); err != nil {
		return fmt.Errorf("watching IPC directory: %w", err)
	}

	log.Printf("Watching %s for MCP requests", b.ipcPath)

	// Semaphore for concurrency control
	sem := make(chan struct{}, maxConcurrent)

	// Also watch for agent completion (result.json in parent /ipc/output/)
	outputDir := filepath.Join(filepath.Dir(b.ipcPath), "output")
	_ = watcher.Add(outputDir) // best-effort; dir may not exist yet

	var wg sync.WaitGroup

	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return nil

		case event, ok := <-watcher.Events:
			if !ok {
				wg.Wait()
				return nil
			}

			filename := filepath.Base(event.Name)

			// Exit when agent completes
			if filename == "result.json" && filepath.Dir(event.Name) == outputDir {
				log.Printf("Agent completed (result.json detected), draining in-flight requests")
				wg.Wait()
				return nil
			}

			// Only process mcp-request-*.json files
			if !event.Has(fsnotify.Create) && !event.Has(fsnotify.Write) {
				continue
			}
			if !strings.HasPrefix(filename, "mcp-request-") || !strings.HasSuffix(filename, ".json") {
				continue
			}

			// Dedup: fsnotify fires both Create and Write
			if _, loaded := b.processed.LoadOrStore(event.Name, true); loaded {
				continue
			}

			// Acquire semaphore without blocking the event loop
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				wg.Wait()
				return nil
			}

			wg.Add(1)
			go func(path string) {
				defer wg.Done()
				defer func() { <-sem }() // release
				b.handleRequest(ctx, path)
				b.processed.Delete(path)
			}(event.Name)

		case err, ok := <-watcher.Errors:
			if !ok {
				wg.Wait()
				return nil
			}
			log.Printf("Watcher error: %v", err)
		}
	}
}

// extractIDFromFilename extracts the request ID from a filename like "mcp-request-<id>.json".
func extractIDFromFilename(path string) string {
	base := filepath.Base(path)
	base = strings.TrimPrefix(base, "mcp-request-")
	base = strings.TrimSuffix(base, ".json")
	return base
}

// handleRequest processes a single MCP request file.
func (b *Bridge) handleRequest(ctx context.Context, path string) {
	start := time.Now()

	// Small delay to ensure file write is complete
	time.Sleep(50 * time.Millisecond)

	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("Failed to read request %s: %v", filepath.Base(path), err)
		return
	}

	var req MCPRequest
	if err := json.Unmarshal(data, &req); err != nil {
		log.Printf("Failed to parse request %s: %v", filepath.Base(path), err)
		// Use ID from filename when JSON parse fails
		id := extractIDFromFilename(path)
		b.writeErrorResult(id, path, "invalid request JSON")
		mcpToolErrors.Add(ctx, 1, metric.WithAttributes(attribute.String("error", "invalid_json")))
		return
	}

	// Resolve target server
	serverName := req.Server
	toolName := req.Tool

	if serverName == "" {
		// Resolve by prefix: find the longest matching prefix
		serverName, toolName = b.resolveByPrefix(req.Tool)
		if serverName == "" {
			log.Printf("No server found for tool %q", req.Tool)
			b.writeErrorResult(req.ID, path, fmt.Sprintf("no MCP server found for tool %q", req.Tool))
			mcpToolErrors.Add(ctx, 1, metric.WithAttributes(attribute.String("error", "no_server")))
			return
		}
	} else {
		// Server specified directly — still strip prefix from tool name
		_, toolName = b.resolveByPrefix(req.Tool)
		if toolName == req.Tool {
			// No prefix found — use as-is
			toolName = req.Tool
		}
	}

	// Extract trace context from agent-runner's _meta
	parentCtx := ctx
	if tp, ok := req.Meta["traceparent"]; ok {
		if remoteCtx := extractTraceparent(tp); remoteCtx.IsValid() {
			parentCtx = trace.ContextWithRemoteSpanContext(ctx, remoteCtx)
		}
	}

	// Start a span for the tool call
	ctx, span := bridgeTracer.Start(parentCtx, "mcp.tool_call",
		trace.WithAttributes(
			attribute.String("mcp.tool", toolName),
			attribute.String("mcp.server", serverName),
			attribute.String("mcp.request_id", req.ID),
		),
	)
	defer span.End()

	attrs := metric.WithAttributes(
		attribute.String("mcp.server", serverName),
		attribute.String("mcp.tool", toolName),
	)
	mcpToolCalls.Add(ctx, 1, attrs)

	// Defense in depth: reject tool calls for tools not in the filtered index.
	if _, known := b.toolIndex[req.Tool]; !known {
		log.Printf("Tool %q not in filtered tool index, rejecting", req.Tool)
		b.writeErrorResult(req.ID, path, fmt.Sprintf("tool %q is not available (filtered)", req.Tool))
		span.SetStatus(codes.Error, "tool filtered")
		mcpToolErrors.Add(ctx, 1, attrs)
		return
	}

	client, ok := b.clients[serverName]
	if !ok {
		log.Printf("No client for server %q", serverName)
		b.writeErrorResult(req.ID, path, fmt.Sprintf("MCP server %q not connected", serverName))
		span.SetStatus(codes.Error, "server not connected")
		mcpToolErrors.Add(ctx, 1, attrs)
		return
	}

	// Build meta for trace propagation
	var meta map[string]any
	if len(req.Meta) > 0 {
		meta = make(map[string]any, len(req.Meta))
		for k, v := range req.Meta {
			meta[k] = v
		}
	}

	// Call the tool
	log.Printf("Calling MCP tool %q on server %q (request %s)", toolName, serverName, req.ID)

	callResult, err := client.CallTool(ctx, toolName, req.Arguments, meta)
	if err != nil {
		log.Printf("MCP tool call failed: %v", err)
		b.writeErrorResult(req.ID, path, err.Error())
		span.RecordError(err)
		span.SetStatus(codes.Error, "tool call failed")
		mcpToolErrors.Add(ctx, 1, attrs)
		return
	}

	// Build result
	result := MCPResult{
		ID:      req.ID,
		Success: !callResult.IsError,
		IsError: callResult.IsError,
	}

	if callResult.IsError {
		// Extract error text from content
		for _, c := range callResult.Content {
			if c.Text != "" {
				result.Error = c.Text
				break
			}
		}
		span.SetStatus(codes.Error, "tool returned error")
		mcpToolErrors.Add(ctx, 1, attrs)
	}

	// Marshal content
	contentData, err := json.Marshal(callResult.Content)
	if err != nil {
		log.Printf("Failed to marshal content for request %s: %v", req.ID, err)
		b.writeErrorResult(req.ID, path, "failed to marshal tool result content")
		span.RecordError(err)
		span.SetStatus(codes.Error, "marshal failed")
		return
	}
	result.Content = contentData

	b.writeResult(req.ID, path, &result)

	mcpToolDuration.Record(ctx, float64(time.Since(start).Milliseconds()), attrs)
}

// resolveByPrefix finds the server for a prefixed tool name and returns
// the server name and the unprefixed tool name.
func (b *Bridge) resolveByPrefix(prefixedTool string) (serverName, toolName string) {
	// Check the exact tool index first
	if sn, ok := b.toolIndex[prefixedTool]; ok {
		// Strip prefix: find the prefix for this server and remove it + "_"
		for _, srv := range b.config.Servers {
			if srv.Name == sn && strings.HasPrefix(prefixedTool, srv.ToolsPrefix+"_") {
				return sn, strings.TrimPrefix(prefixedTool, srv.ToolsPrefix+"_")
			}
		}
		return sn, prefixedTool
	}

	// Fall back to prefix matching (for tools discovered after startup)
	for prefix, sn := range b.prefixIndex {
		if strings.HasPrefix(prefixedTool, prefix+"_") {
			return sn, strings.TrimPrefix(prefixedTool, prefix+"_")
		}
	}

	return "", prefixedTool
}

// writeResult writes an MCPResult to the result file.
func (b *Bridge) writeResult(id, reqPath string, result *MCPResult) {
	// Derive result path safely using filepath operations
	dir := filepath.Dir(reqPath)
	base := strings.Replace(filepath.Base(reqPath), "mcp-request-", "mcp-result-", 1)
	resPath := filepath.Join(dir, base)

	data, err := json.Marshal(result)
	if err != nil {
		log.Printf("Failed to marshal result for %s: %v", id, err)
		return
	}

	// Write atomically
	tmp := resPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		log.Printf("Failed to write result for %s: %v", id, err)
		return
	}
	if err := os.Rename(tmp, resPath); err != nil {
		log.Printf("Failed to rename result for %s: %v", id, err)
		os.Remove(tmp)
		return
	}

	// Clean up request file
	if err := os.Remove(reqPath); err != nil && !os.IsNotExist(err) {
		log.Printf("Failed to clean up request file %s: %v", filepath.Base(reqPath), err)
	}
}

// writeErrorResult writes an error MCPResult.
func (b *Bridge) writeErrorResult(id, reqPath, errMsg string) {
	result := &MCPResult{
		ID:      id,
		Success: false,
		Error:   errMsg,
	}
	b.writeResult(id, reqPath, result)
}

// DiscoverAndWriteManifest runs only the discovery phase: connect to MCP servers,
// list tools, write the manifest, then return. Used by the init container.
func (b *Bridge) DiscoverAndWriteManifest(ctx context.Context) error {
	ctx, span := bridgeTracer.Start(ctx, "mcp-bridge.discover",
		trace.WithAttributes(attribute.String("agent_run_id", b.agentRunID)),
	)
	defer span.End()

	manifest, err := b.discoverTools(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "tool discovery failed")
		return err
	}

	span.SetAttributes(attribute.Int("mcp.tools_discovered", len(manifest.Tools)))

	if err := WriteManifest(b.manifestPath, manifest); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "manifest write failed")
		return err
	}

	log.Printf("Wrote tool manifest with %d tools to %s", len(manifest.Tools), b.manifestPath)
	return nil
}

// extractTraceparent parses a W3C traceparent header into a SpanContext.
// Format: 00-<trace-id>-<span-id>-<flags>
func extractTraceparent(tp string) trace.SpanContext {
	parts := strings.Split(tp, "-")
	if len(parts) != 4 || parts[0] != "00" {
		return trace.SpanContext{}
	}

	traceID, err := trace.TraceIDFromHex(parts[1])
	if err != nil {
		return trace.SpanContext{}
	}

	spanID, err := trace.SpanIDFromHex(parts[2])
	if err != nil {
		return trace.SpanContext{}
	}

	var flags trace.TraceFlags
	if parts[3] == "01" {
		flags = trace.FlagsSampled
	}

	return trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: flags,
		Remote:     true,
	})
}
