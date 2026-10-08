package webproxy

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/controller"
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
	config       Config
	eventBus     eventbus.EventBus
	k8s          client.Client
	log          logr.Logger
	limiter      *RateLimiter
	DensityCache *controller.DensityCache // optional: for capacity-aware routing
}

// answersRun reports whether a run lifecycle event settles the run this
// request is waiting on, or a later attempt of it.
//
// A gate hook that returns {"action":"retry"} retires the run this request
// created — it publishes neither a completion nor a failure — and the work
// continues under a successor's name. Matching on the name alone would leave
// the caller waiting for an event that is never published again, so the chain
// is followed instead.
func (p *Proxy) answersRun(ctx context.Context, run *sympoziumv1alpha1.AgentRun, event *eventbus.Event) bool {
	return controller.RetryChainContains(ctx, p.k8s, run.Namespace, run.Name, event.Metadata["agentRunID"])
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

		// Rate-limit before authenticating so failed-auth attempts are also
		// throttled; otherwise brute-forcing the static key runs at wire speed.
		if !p.limiter.Allow() {
			writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}

		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			writeError(w, http.StatusUnauthorized, "missing or invalid Authorization header")
			return
		}
		token := strings.TrimPrefix(auth, "Bearer ")
		if subtle.ConstantTimeCompare([]byte(token), []byte(p.config.APIKey)) != 1 {
			writeError(w, http.StatusUnauthorized, "invalid API key")
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
