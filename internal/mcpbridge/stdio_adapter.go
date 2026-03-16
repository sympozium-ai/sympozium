package mcpbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

var (
	adapterTracer = otel.Tracer("sympozium.ai/mcp-bridge/stdio-adapter")
	adapterMeter  = otel.Meter("sympozium.ai/mcp-bridge/stdio-adapter")

	mcpServerRequests, _ = adapterMeter.Int64Counter("mcp.server.requests",
		metric.WithUnit("{request}"),
		metric.WithDescription("Number of JSON-RPC requests forwarded to stdio MCP server"))
	mcpServerDuration, _ = adapterMeter.Float64Histogram("mcp.server.duration",
		metric.WithUnit("ms"),
		metric.WithDescription("Duration of JSON-RPC requests to stdio MCP server"))
)

// jsonRPCRequest is a minimal JSON-RPC 2.0 request envelope for method extraction.
type jsonRPCRequest struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// StdioAdapter wraps a StdioManager and exposes an HTTP server that translates
// JSON-RPC HTTP requests to stdin/stdout of the child process.
type StdioAdapter struct {
	manager    *StdioManager
	serverName string
	port       int
	ready      atomic.Bool
}

// NewStdioAdapter creates a new StdioAdapter.
func NewStdioAdapter(manager *StdioManager, serverName string, port int) *StdioAdapter {
	return &StdioAdapter{
		manager:    manager,
		serverName: serverName,
		port:       port,
	}
}

// Run starts the stdio process and HTTP server, blocking until ctx is cancelled.
func (a *StdioAdapter) Run(ctx context.Context) error {
	if err := a.manager.Start(); err != nil {
		return fmt.Errorf("start stdio process: %w", err)
	}
	defer a.manager.Stop()

	// Mark ready after successful start
	a.ready.Store(true)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /", a.handleJSONRPC)
	mux.HandleFunc("GET /healthz", a.handleHealthz)
	mux.HandleFunc("GET /readyz", a.handleReadyz)

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", a.port),
		Handler: mux,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("stdio adapter listening on :%d (server=%s)", a.port, a.serverName)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

func (a *StdioAdapter) handleJSONRPC(w http.ResponseWriter, r *http.Request) {
	// Extract trace context from incoming request
	propagator := otel.GetTextMapPropagator()
	ctx := propagator.Extract(r.Context(), propagation.HeaderCarrier(r.Header))

	// Parse request body to extract method for span attributes
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, `{"error":"failed to read request body"}`, http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var rpcReq jsonRPCRequest
	_ = json.Unmarshal(body, &rpcReq) // best-effort parse for method extraction

	// Start OTel span
	ctx, span := adapterTracer.Start(ctx, "mcp.server.call",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("mcp.server", a.serverName),
			attribute.String("mcp.method", rpcReq.Method),
			attribute.String("mcp.transport", "stdio"),
		),
	)
	defer span.End()

	start := time.Now()
	mcpServerRequests.Add(ctx, 1,
		metric.WithAttributes(
			attribute.String("mcp.server", a.serverName),
			attribute.String("mcp.method", rpcReq.Method),
		),
	)

	if !a.manager.IsAlive() {
		span.SetStatus(codes.Error, "stdio process not alive")
		http.Error(w, `{"error":"stdio process not alive"}`, http.StatusServiceUnavailable)
		return
	}

	// MCP notifications (no "id" field) don't expect a response.
	// Write to stdin but don't wait for a reply.
	if rpcReq.ID == nil {
		log.Printf("stdio adapter: notification %q (no id), write-only", rpcReq.Method)
		err := a.manager.WriteOnly(ctx, body)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusBadGateway)
			return
		}
		span.SetStatus(codes.Ok, "")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0"}`))
		return
	}

	response, err := a.manager.Send(ctx, body)
	duration := float64(time.Since(start).Milliseconds())
	mcpServerDuration.Record(ctx, duration,
		metric.WithAttributes(
			attribute.String("mcp.server", a.serverName),
			attribute.String("mcp.method", rpcReq.Method),
		),
	)

	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusBadGateway)
		return
	}

	span.SetStatus(codes.Ok, "")
	w.Header().Set("Content-Type", "application/json")
	w.Write(response)
}

func (a *StdioAdapter) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if a.manager.IsAlive() {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"status":"ok"}`)
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"status":"not alive"}`)
	}
}

func (a *StdioAdapter) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if a.manager.IsAlive() && a.ready.Load() {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"status":"ready"}`)
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"status":"not ready"}`)
	}
}
