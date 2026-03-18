package webproxy

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/sympozium-ai/sympozium/internal/eventbus"
)

// Config holds configuration for the web proxy.
type Config struct {
	InstanceName string
	APIKey       string
	RPM          int // requests per minute
	BurstSize    int
}

// Proxy is the HTTP proxy that exposes a Sympozium agent as an API.
type Proxy struct {
	config   Config
	eventBus eventbus.EventBus
	k8s      client.Client
	log      logr.Logger
	limiter  *RateLimiter
}

// NewProxy creates a new web proxy.
func NewProxy(cfg Config, eb eventbus.EventBus, k8s client.Client, log logr.Logger) *Proxy {
	return &Proxy{
		config:   cfg,
		eventBus: eb,
		k8s:      k8s,
		log:      log,
		limiter:  NewRateLimiter(cfg.RPM, cfg.BurstSize),
	}
}

// Handler returns the HTTP handler for the proxy with auth middleware.
func (p *Proxy) Handler() http.Handler {
	mux := http.NewServeMux()

	// OpenAI-compatible endpoints
	mux.HandleFunc("POST /v1/chat/completions", p.handleChatCompletions)
	mux.HandleFunc("GET /v1/models", p.handleListModels)

	// MCP endpoints
	mux.HandleFunc("GET /sse", p.handleMCPSSE)
	mux.HandleFunc("POST /message", p.handleMCPMessage)

	// Health check
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	return p.authMiddleware(mux)
}

// authMiddleware validates Bearer tokens on all routes except /healthz.
func (p *Proxy) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}

		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			writeError(w, http.StatusUnauthorized, "missing or invalid Authorization header")
			return
		}
		token := strings.TrimPrefix(auth, "Bearer ")
		if token != p.config.APIKey {
			writeError(w, http.StatusUnauthorized, "invalid API key")
			return
		}

		if !p.limiter.Allow() {
			writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}

		next.ServeHTTP(w, r)
	})
}

// writeError writes a JSON error response.
func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]interface{}{
			"message": msg,
			"type":    "error",
		},
	})
}

// writeJSON writes a JSON response.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
