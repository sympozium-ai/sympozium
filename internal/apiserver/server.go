// Package apiserver provides the HTTP + WebSocket API server for Sympozium.
package apiserver

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/gorilla/websocket"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/alexsjones/sympozium/api/v1alpha1"
	"github.com/alexsjones/sympozium/internal/eventbus"
)

// Server is the Sympozium API server.
type Server struct {
	client   client.Client
	eventBus eventbus.EventBus
	kube     kubernetes.Interface
	log      logr.Logger
	upgrader websocket.Upgrader
}

// NewServer creates a new API server.
func NewServer(c client.Client, bus eventbus.EventBus, kube kubernetes.Interface, log logr.Logger) *Server {
	return &Server{
		client:   c,
		eventBus: bus,
		kube:     kube,
		log:      log,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}
}

// Start starts the HTTP server (headless, no embedded UI).
// When token is non-empty the auth middleware is applied.
func (s *Server) Start(addr, token string) error {
	server := &http.Server{
		Addr:              addr,
		Handler:           s.buildMux(nil, token),
		ReadHeaderTimeout: 10 * time.Second,
	}

	s.log.Info("Starting API server", "addr", addr, "auth", token != "")
	return server.ListenAndServe()
}

// StartWithUI starts the HTTP server with an embedded frontend SPA
// and optional bearer-token authentication.
func (s *Server) StartWithUI(addr, token string, frontendFS fs.FS) error {
	server := &http.Server{
		Addr:              addr,
		Handler:           s.buildMux(frontendFS, token),
		ReadHeaderTimeout: 10 * time.Second,
	}

	s.log.Info("Starting API server with UI", "addr", addr)
	return server.ListenAndServe()
}

// buildMux creates the HTTP mux with all API routes.
// When frontendFS is non-nil, it serves the SPA for non-API paths.
// When token is non-empty, API routes require Bearer authentication.
func (s *Server) buildMux(frontendFS fs.FS, token string) http.Handler {
	mux := http.NewServeMux()

	// Instance endpoints
	mux.HandleFunc("GET /api/v1/instances", s.listInstances)
	mux.HandleFunc("GET /api/v1/instances/{name}", s.getInstance)
	mux.HandleFunc("POST /api/v1/instances", s.createInstance)
	mux.HandleFunc("DELETE /api/v1/instances/{name}", s.deleteInstance)
	mux.HandleFunc("PATCH /api/v1/instances/{name}", s.patchInstance)
	mux.HandleFunc("GET /api/v1/instances/{name}/web-endpoint", s.getWebEndpointStatus)

	// Run endpoints
	mux.HandleFunc("GET /api/v1/runs", s.listRuns)
	mux.HandleFunc("GET /api/v1/runs/{name}", s.getRun)
	mux.HandleFunc("GET /api/v1/runs/{name}/telemetry", s.getRunTelemetry)
	mux.HandleFunc("POST /api/v1/runs", s.createRun)
	mux.HandleFunc("DELETE /api/v1/runs/{name}", s.deleteRun)

	// Observability endpoints
	mux.HandleFunc("GET /api/v1/observability/metrics", s.getObservabilityMetrics)

	// Policy endpoints
	mux.HandleFunc("GET /api/v1/policies", s.listPolicies)
	mux.HandleFunc("GET /api/v1/policies/{name}", s.getPolicy)

	// Skill endpoints
	mux.HandleFunc("GET /api/v1/skills", s.listSkills)
	mux.HandleFunc("GET /api/v1/skills/{name}", s.getSkill)

	// GitHub GitOps skill auth endpoints (PAT token)
	mux.HandleFunc("POST /api/v1/skills/github-gitops/auth/token", s.handleGithubAuthToken)
	mux.HandleFunc("GET /api/v1/skills/github-gitops/auth/status", s.handleGithubAuthStatus)

	// Schedule endpoints
	mux.HandleFunc("GET /api/v1/schedules", s.listSchedules)
	mux.HandleFunc("GET /api/v1/schedules/{name}", s.getSchedule)
	mux.HandleFunc("POST /api/v1/schedules", s.createSchedule)
	mux.HandleFunc("DELETE /api/v1/schedules/{name}", s.deleteSchedule)

	// PersonaPack endpoints
	mux.HandleFunc("GET /api/v1/personapacks", s.listPersonaPacks)
	mux.HandleFunc("POST /api/v1/personapacks/install-defaults", s.installDefaultPersonaPacks)
	mux.HandleFunc("GET /api/v1/personapacks/{name}", s.getPersonaPack)
	mux.HandleFunc("PATCH /api/v1/personapacks/{name}", s.patchPersonaPack)
	mux.HandleFunc("DELETE /api/v1/personapacks/{name}", s.deletePersonaPack)

	// Gateway config endpoints (singleton SympoziumConfig)
	mux.HandleFunc("GET /api/v1/gateway", s.getGatewayConfig)
	mux.HandleFunc("POST /api/v1/gateway", s.createGatewayConfig)
	mux.HandleFunc("PATCH /api/v1/gateway", s.patchGatewayConfig)
	mux.HandleFunc("DELETE /api/v1/gateway", s.deleteGatewayConfig)
	mux.HandleFunc("GET /api/v1/gateway/metrics", s.getGatewayMetrics)

	// Provider discovery endpoints (model listing, node discovery)
	mux.HandleFunc("GET /api/v1/providers/nodes", s.listProviderNodes)
	mux.HandleFunc("GET /api/v1/providers/models", s.proxyProviderModels)

	// Cluster info
	mux.HandleFunc("GET /api/v1/cluster", s.getClusterInfo)

	// Namespace listing
	mux.HandleFunc("GET /api/v1/namespaces", s.listNamespaces)
	mux.HandleFunc("GET /api/v1/pods", s.listPods)
	mux.HandleFunc("GET /api/v1/pods/{name}/logs", s.getPodLogs)

	// WebSocket streaming
	mux.HandleFunc("/ws/stream", s.handleStream)

	// Health & metrics
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})
	mux.Handle("/metrics", promhttp.Handler())

	// If a frontend FS is provided, serve it as an SPA fallback.
	if frontendFS != nil {
		mux.HandleFunc("/", s.spaHandler(frontendFS))
	}

	// Wrap the mux with otelhttp for automatic HTTP span instrumentation.
	handler := otelhttp.NewHandler(mux, "sympozium-apiserver",
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return "sympozium.api." + r.Method
		}),
	)

	// Wrap with auth middleware if a token is configured.
	if token != "" {
		return authMiddleware(token, handler)
	}

	return handler
}

// authMiddleware returns an http.Handler that checks for a valid Bearer token
// or ?token= query parameter. Health and metrics endpoints are exempted.
func authMiddleware(expectedToken string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// Skip auth for health, metrics, and static assets.
		if path == "/healthz" || path == "/readyz" || path == "/metrics" {
			next.ServeHTTP(w, r)
			return
		}

		// Skip auth for non-API paths (frontend SPA assets).
		if !strings.HasPrefix(path, "/api/") && !strings.HasPrefix(path, "/ws/") {
			next.ServeHTTP(w, r)
			return
		}

		// Check Authorization header or query param.
		token := ""
		if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
			token = strings.TrimPrefix(auth, "Bearer ")
		}
		if token == "" {
			token = r.URL.Query().Get("token")
		}
		if token != expectedToken {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// spaHandler serves the embedded SPA. Known static files are served directly;
// all other paths return index.html for client-side routing.
func (s *Server) spaHandler(frontendFS fs.FS) http.HandlerFunc {
	fileServer := http.FileServer(http.FS(frontendFS))
	return func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/" {
			path = "index.html"
		} else {
			path = strings.TrimPrefix(path, "/")
		}

		// Try to open the file. If it exists, serve it.
		if f, err := frontendFS.Open(path); err == nil {
			f.Close()
			fileServer.ServeHTTP(w, r)
			return
		}

		// Fallback to index.html for SPA routing.
		r.URL.Path = "/"
		fileServer.ServeHTTP(w, r)
	}
}

// --- Instance handlers ---

func (s *Server) listInstances(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var list sympoziumv1alpha1.SympoziumInstanceList
	if err := s.client.List(r.Context(), &list, client.InNamespace(ns)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, list.Items)
}

func (s *Server) getInstance(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var inst sympoziumv1alpha1.SympoziumInstance
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: name, Namespace: ns}, &inst); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	writeJSON(w, inst)
}

func (s *Server) deleteInstance(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	inst := &sympoziumv1alpha1.SympoziumInstance{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
	}
	if err := s.client.Delete(r.Context(), inst); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// PatchInstanceRequest is the request body for partially updating a SympoziumInstance.
type PatchInstanceRequest struct {
	WebEndpoint *PatchWebEndpoint `json:"webEndpoint,omitempty"`
}

// PatchWebEndpoint is the web endpoint patch payload.
type PatchWebEndpoint struct {
	Enabled   *bool   `json:"enabled,omitempty"`
	Hostname  *string `json:"hostname,omitempty"`
	RateLimit *struct {
		RequestsPerMinute *int `json:"requestsPerMinute,omitempty"`
	} `json:"rateLimit,omitempty"`
}

func (s *Server) patchInstance(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var req PatchInstanceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	var inst sympoziumv1alpha1.SympoziumInstance
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: name, Namespace: ns}, &inst); err != nil {
		if k8serrors.IsNotFound(err) {
			http.Error(w, "instance not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if req.WebEndpoint != nil {
		if req.WebEndpoint.Enabled != nil && !*req.WebEndpoint.Enabled {
			// Disable — remove the web-endpoint skill.
			var filtered []sympoziumv1alpha1.SkillRef
			for _, s := range inst.Spec.Skills {
				if s.SkillPackRef != "web-endpoint" && s.SkillPackRef != "skillpack-web-endpoint" {
					filtered = append(filtered, s)
				}
			}
			inst.Spec.Skills = filtered
		} else {
			// Enable — add web-endpoint as a skill.
			params := map[string]string{}
			if req.WebEndpoint.Hostname != nil && *req.WebEndpoint.Hostname != "" {
				params["hostname"] = *req.WebEndpoint.Hostname
			}
			if req.WebEndpoint.RateLimit != nil && req.WebEndpoint.RateLimit.RequestsPerMinute != nil {
				params["rate_limit_rpm"] = fmt.Sprintf("%d", *req.WebEndpoint.RateLimit.RequestsPerMinute)
			}

			// Check if web-endpoint skill already exists.
			found := false
			for i, s := range inst.Spec.Skills {
				if s.SkillPackRef == "web-endpoint" || s.SkillPackRef == "skillpack-web-endpoint" {
					inst.Spec.Skills[i].Params = params
					found = true
					break
				}
			}
			if !found {
				inst.Spec.Skills = append(inst.Spec.Skills, sympoziumv1alpha1.SkillRef{
					SkillPackRef: "web-endpoint",
					Params:       params,
				})
			}
		}
	}

	if err := s.client.Update(r.Context(), &inst); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, inst)
}

// WebEndpointStatusResponse is the response for the web-endpoint status endpoint.
type WebEndpointStatusResponse struct {
	Enabled        bool   `json:"enabled"`
	DeploymentName string `json:"deploymentName,omitempty"`
	ServiceName    string `json:"serviceName,omitempty"`
	GatewayReady   bool   `json:"gatewayReady"`
	RouteURL       string `json:"routeURL,omitempty"`
	AuthSecretName string `json:"authSecretName,omitempty"`
}

func (s *Server) getWebEndpointStatus(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var inst sympoziumv1alpha1.SympoziumInstance
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: name, Namespace: ns}, &inst); err != nil {
		if k8serrors.IsNotFound(err) {
			http.Error(w, "instance not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	resp := WebEndpointStatusResponse{}

	// Check for web-endpoint skill.
	for _, skill := range inst.Spec.Skills {
		if skill.SkillPackRef == "web-endpoint" || skill.SkillPackRef == "skillpack-web-endpoint" {
			resp.Enabled = true
			break
		}
	}

	if !resp.Enabled {
		writeJSON(w, resp)
		return
	}

	// Find server-mode AgentRun for this instance.
	var runs sympoziumv1alpha1.AgentRunList
	if err := s.client.List(r.Context(), &runs,
		client.InNamespace(ns),
		client.MatchingLabels{"sympozium.ai/instance": name},
	); err == nil {
		for _, run := range runs.Items {
			if run.Status.Phase == sympoziumv1alpha1.AgentRunPhaseServing {
				resp.DeploymentName = run.Status.DeploymentName
				resp.ServiceName = run.Status.ServiceName
				break
			}
		}
	}

	// Check gateway readiness.
	var config sympoziumv1alpha1.SympoziumConfig
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: "default", Namespace: "sympozium-system"}, &config); err == nil {
		if config.Status.Gateway != nil && config.Status.Gateway.Ready {
			resp.GatewayReady = true
		}
	}

	writeJSON(w, resp)
}

// CreateInstanceRequest is the request body for creating a new SympoziumInstance.
type CreateInstanceRequest struct {
	Name              string                          `json:"name"`
	Provider          string                          `json:"provider"`
	Model             string                          `json:"model"`
	BaseURL           string                          `json:"baseURL,omitempty"`
	SecretName        string                          `json:"secretName,omitempty"`
	APIKey            string                          `json:"apiKey,omitempty"`
	PolicyRef         string                          `json:"policyRef,omitempty"`
	Skills            []sympoziumv1alpha1.SkillRef    `json:"skills,omitempty"`
	Channels          []sympoziumv1alpha1.ChannelSpec `json:"channels,omitempty"`
	HeartbeatInterval string                          `json:"heartbeatInterval,omitempty"`
	NodeSelector      map[string]string               `json:"nodeSelector,omitempty"`
}

func (s *Server) createInstance(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var req CreateInstanceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.Name == "" || req.Provider == "" || req.Model == "" {
		http.Error(w, "name, provider, and model are required", http.StatusBadRequest)
		return
	}

	inst := &sympoziumv1alpha1.SympoziumInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      req.Name,
			Namespace: ns,
		},
		Spec: sympoziumv1alpha1.SympoziumInstanceSpec{
			Agents: sympoziumv1alpha1.AgentsSpec{
				Default: sympoziumv1alpha1.AgentConfig{
					Model: req.Model,
				},
			},
			Memory: &sympoziumv1alpha1.MemorySpec{
				Enabled:   true,
				MaxSizeKB: 256,
			},
			Observability: &sympoziumv1alpha1.ObservabilitySpec{
				Enabled:      true,
				OTLPEndpoint: "sympozium-otel-collector.sympozium-system.svc:4317",
				OTLPProtocol: "grpc",
				ServiceName:  "sympozium",
			},
		},
	}

	if req.BaseURL != "" {
		inst.Spec.Agents.Default.BaseURL = req.BaseURL
	}
	if len(req.NodeSelector) > 0 {
		inst.Spec.Agents.Default.NodeSelector = req.NodeSelector
	}

	if req.Provider != "" && req.APIKey != "" && req.SecretName == "" {
		req.SecretName = defaultProviderSecretName(req.Name, req.Provider)
		envKey := providerEnvKey(req.Provider)
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      req.SecretName,
				Namespace: ns,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "sympozium",
					"sympozium.ai/instance":        req.Name,
				},
			},
			StringData: map[string]string{envKey: req.APIKey},
		}
		createErr := s.client.Create(r.Context(), secret)
		if createErr != nil && !k8serrors.IsAlreadyExists(createErr) {
			http.Error(w, "failed to create credentials secret: "+createErr.Error(), http.StatusInternalServerError)
			return
		}
		if k8serrors.IsAlreadyExists(createErr) {
			existing := &corev1.Secret{}
			if err := s.client.Get(r.Context(), types.NamespacedName{Name: req.SecretName, Namespace: ns}, existing); err != nil {
				http.Error(w, "failed to get existing secret: "+err.Error(), http.StatusInternalServerError)
				return
			}
			if existing.Data == nil {
				existing.Data = map[string][]byte{}
			}
			existing.Data[envKey] = []byte(req.APIKey)
			if err := s.client.Update(r.Context(), existing); err != nil {
				http.Error(w, "failed to update credentials secret: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}
	}

	if req.Provider != "" && req.SecretName != "" {
		existing := &corev1.Secret{}
		if err := s.client.Get(r.Context(), types.NamespacedName{Name: req.SecretName, Namespace: ns}, existing); err != nil {
			if k8serrors.IsNotFound(err) {
				http.Error(w, fmt.Sprintf("secret %q not found in namespace %q", req.SecretName, ns), http.StatusBadRequest)
				return
			}
			http.Error(w, "failed to get secret: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	if req.SecretName != "" {
		inst.Spec.AuthRefs = []sympoziumv1alpha1.SecretRef{
			{Provider: req.Provider, Secret: req.SecretName},
		}
	}

	if req.PolicyRef != "" {
		inst.Spec.PolicyRef = req.PolicyRef
	}

	if len(req.Skills) > 0 {
		inst.Spec.Skills = req.Skills
	}

	if len(req.Channels) > 0 {
		inst.Spec.Channels = req.Channels
	}

	if err := s.client.Create(r.Context(), inst); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Auto-create a heartbeat schedule when an interval is provided.
	if req.HeartbeatInterval != "" {
		cron := intervalToCronExpr(req.HeartbeatInterval)
		sched := &sympoziumv1alpha1.SympoziumSchedule{
			ObjectMeta: metav1.ObjectMeta{
				Name:      req.Name + "-heartbeat",
				Namespace: ns,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "sympozium",
					"sympozium.ai/instance":        req.Name,
				},
			},
			Spec: sympoziumv1alpha1.SympoziumScheduleSpec{
				InstanceRef:       req.Name,
				Schedule:          cron,
				Task:              "heartbeat",
				Type:              "heartbeat",
				ConcurrencyPolicy: "Forbid",
				IncludeMemory:     true,
			},
		}
		if err := s.client.Create(r.Context(), sched); err != nil && !k8serrors.IsAlreadyExists(err) {
			// Log but don't fail the instance creation.
			s.log.Error(err, "failed to create heartbeat schedule", "instance", req.Name)
		}
	}

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, inst)
}

// intervalToCronExpr converts a human-readable interval (e.g. "1h", "30m") to a cron expression.
func intervalToCronExpr(interval string) string {
	switch strings.ToLower(strings.TrimSpace(interval)) {
	case "1m", "1min":
		return "* * * * *"
	case "5m", "5min":
		return "*/5 * * * *"
	case "10m", "10min":
		return "*/10 * * * *"
	case "15m":
		return "*/15 * * * *"
	case "30m":
		return "*/30 * * * *"
	case "1h":
		return "0 * * * *"
	case "2h":
		return "0 */2 * * *"
	case "6h":
		return "0 */6 * * *"
	case "12h":
		return "0 */12 * * *"
	case "24h", "1d":
		return "0 0 * * *"
	default:
		if strings.Contains(interval, " ") {
			return interval
		}
		return "0 * * * *"
	}
}

// --- Run handlers ---

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var list sympoziumv1alpha1.AgentRunList
	if err := s.client.List(r.Context(), &list, client.InNamespace(ns)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, list.Items)
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var run sympoziumv1alpha1.AgentRun
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: name, Namespace: ns}, &run); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	writeJSON(w, run)
}

// CreateRunRequest is the request body for creating a new AgentRun.
type CreateRunRequest struct {
	InstanceRef string `json:"instanceRef"`
	Task        string `json:"task"`
	AgentID     string `json:"agentId,omitempty"`
	SessionKey  string `json:"sessionKey,omitempty"`
	Model       string `json:"model,omitempty"`
	Timeout     string `json:"timeout,omitempty"`
}

func (s *Server) createRun(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var req CreateRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.InstanceRef == "" || req.Task == "" {
		http.Error(w, "instanceRef and task are required", http.StatusBadRequest)
		return
	}

	if req.AgentID == "" {
		req.AgentID = "primary"
	}
	if req.SessionKey == "" {
		req.SessionKey = fmt.Sprintf("session-%d", time.Now().UnixNano())
	}
	if req.Timeout == "" {
		req.Timeout = "5m"
	}

	// Look up the SympoziumInstance to inherit auth, model, and skills.
	var inst sympoziumv1alpha1.SympoziumInstance
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: req.InstanceRef, Namespace: ns}, &inst); err != nil {
		if k8serrors.IsNotFound(err) {
			http.Error(w, fmt.Sprintf("instance %q not found in namespace %q", req.InstanceRef, ns), http.StatusNotFound)
		} else {
			http.Error(w, "failed to get instance: "+err.Error(), http.StatusInternalServerError)
		}
		return
	}

	// Resolve auth secret and provider from instance — first AuthRef wins.
	authSecret := ""
	provider := "openai"
	if len(inst.Spec.AuthRefs) > 0 {
		authSecret = inst.Spec.AuthRefs[0].Secret
		if inst.Spec.AuthRefs[0].Provider != "" {
			provider = inst.Spec.AuthRefs[0].Provider
		}
	}

	// Infer provider from baseURL for keyless local providers (e.g., Ollama via node-probe).
	if len(inst.Spec.AuthRefs) == 0 && inst.Spec.Agents.Default.BaseURL != "" {
		if strings.Contains(inst.Spec.Agents.Default.BaseURL, "ollama") || strings.Contains(inst.Spec.Agents.Default.BaseURL, ":11434") {
			provider = "ollama"
		} else if strings.Contains(inst.Spec.Agents.Default.BaseURL, "lm-studio") || strings.Contains(inst.Spec.Agents.Default.BaseURL, ":1234") {
			provider = "lm-studio"
		} else {
			provider = "custom"
		}
	}

	// Cloud providers require an API key; local providers with a baseURL do not.
	if authSecret == "" && inst.Spec.Agents.Default.BaseURL == "" {
		http.Error(w, fmt.Sprintf("instance %q has no API key configured (authRefs is empty)", req.InstanceRef), http.StatusBadRequest)
		return
	}

	// Use request-supplied model or fall back to the instance default.
	model := req.Model
	if model == "" {
		model = inst.Spec.Agents.Default.Model
	}

	run := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: req.InstanceRef + "-",
			Namespace:    ns,
			Labels: map[string]string{
				"sympozium.ai/instance": req.InstanceRef,
			},
		},
		Spec: sympoziumv1alpha1.AgentRunSpec{
			InstanceRef: req.InstanceRef,
			AgentID:     req.AgentID,
			SessionKey:  req.SessionKey,
			Task:        req.Task,
			Model: sympoziumv1alpha1.ModelSpec{
				Provider:      provider,
				Model:         model,
				BaseURL:       inst.Spec.Agents.Default.BaseURL,
				AuthSecretRef: authSecret,
				NodeSelector:  inst.Spec.Agents.Default.NodeSelector,
			},
			Skills: inst.Spec.Skills,
		},
	}

	if err := s.client.Create(r.Context(), run); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, run)
}

func (s *Server) deleteRun(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	run := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
	}
	if err := s.client.Delete(r.Context(), run); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- Policy handlers ---

func (s *Server) listPolicies(w http.ResponseWriter, r *http.Request) {
	// Policies are platform-wide shared resources — list across all namespaces.
	var list sympoziumv1alpha1.SympoziumPolicyList
	if err := s.client.List(r.Context(), &list); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, list.Items)
}

func (s *Server) getPolicy(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var pol sympoziumv1alpha1.SympoziumPolicy
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: name, Namespace: ns}, &pol); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	writeJSON(w, pol)
}

// --- Skill handlers ---

func (s *Server) listSkills(w http.ResponseWriter, r *http.Request) {
	// SkillPacks are platform-wide shared resources — list across all namespaces.
	var list sympoziumv1alpha1.SkillPackList
	if err := s.client.List(r.Context(), &list); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, list.Items)
}

func (s *Server) getSkill(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var sk sympoziumv1alpha1.SkillPack
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: name, Namespace: ns}, &sk); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	writeJSON(w, sk)
}

// --- Schedule handlers ---

func (s *Server) listSchedules(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var list sympoziumv1alpha1.SympoziumScheduleList
	if err := s.client.List(r.Context(), &list, client.InNamespace(ns)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, list.Items)
}

func (s *Server) getSchedule(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var sched sympoziumv1alpha1.SympoziumSchedule
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: name, Namespace: ns}, &sched); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	writeJSON(w, sched)
}

// CreateScheduleRequest is the request body for creating a new SympoziumSchedule.
type CreateScheduleRequest struct {
	// Name is the schedule resource name. If empty, a name is generated from instanceRef.
	Name string `json:"name,omitempty"`
	// InstanceRef is the name of the SympoziumInstance this schedule belongs to.
	InstanceRef string `json:"instanceRef"`
	// Schedule is a cron expression (e.g. "0 * * * *").
	Schedule string `json:"schedule"`
	// Task is the task description sent to the agent on each trigger.
	Task string `json:"task"`
	// Type categorises the schedule: heartbeat, scheduled, or sweep.
	Type string `json:"type,omitempty"`
	// Suspend pauses scheduling when true.
	Suspend bool `json:"suspend,omitempty"`
	// ConcurrencyPolicy controls behaviour when a trigger fires while the previous run is active.
	ConcurrencyPolicy string `json:"concurrencyPolicy,omitempty"`
}

func (s *Server) createSchedule(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var req CreateScheduleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.InstanceRef == "" || req.Schedule == "" || req.Task == "" {
		http.Error(w, "instanceRef, schedule, and task are required", http.StatusBadRequest)
		return
	}

	sched := &sympoziumv1alpha1.SympoziumSchedule{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns,
		},
		Spec: sympoziumv1alpha1.SympoziumScheduleSpec{
			InstanceRef: req.InstanceRef,
			Schedule:    req.Schedule,
			Task:        req.Task,
			Suspend:     req.Suspend,
		},
	}

	if req.Name != "" {
		sched.ObjectMeta.Name = req.Name
	} else {
		sched.ObjectMeta.GenerateName = req.InstanceRef + "-schedule-"
	}

	if req.Type != "" {
		sched.Spec.Type = req.Type
	}
	if req.ConcurrencyPolicy != "" {
		sched.Spec.ConcurrencyPolicy = req.ConcurrencyPolicy
	}

	if err := s.client.Create(r.Context(), sched); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, sched)
}

func (s *Server) deleteSchedule(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	sched := &sympoziumv1alpha1.SympoziumSchedule{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
	}
	if err := s.client.Delete(r.Context(), sched); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- PersonaPack handlers ---

func (s *Server) listPersonaPacks(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var list sympoziumv1alpha1.PersonaPackList
	if err := s.client.List(r.Context(), &list, client.InNamespace(ns)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, list.Items)
}

func (s *Server) getPersonaPack(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var pp sympoziumv1alpha1.PersonaPack
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: name, Namespace: ns}, &pp); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	writeJSON(w, pp)
}

// PatchPersonaPackRequest represents a partial update to a PersonaPack.
type PatchPersonaPackRequest struct {
	Enabled           *bool                        `json:"enabled,omitempty"`
	Provider          string                       `json:"provider,omitempty"`
	SecretName        string                       `json:"secretName,omitempty"`
	APIKey            string                       `json:"apiKey,omitempty"`
	Model             string                       `json:"model,omitempty"`
	BaseURL           string                       `json:"baseURL,omitempty"`
	Channels          []string                     `json:"channels,omitempty"`
	ChannelConfigs    map[string]string            `json:"channelConfigs,omitempty"`
	PolicyRef         string                       `json:"policyRef,omitempty"`
	HeartbeatInterval string                       `json:"heartbeatInterval,omitempty"`
	SkillParams       map[string]map[string]string `json:"skillParams,omitempty"`
	GithubToken       string                       `json:"githubToken,omitempty"`
}

func (s *Server) patchPersonaPack(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var req PatchPersonaPackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	var pp sympoziumv1alpha1.PersonaPack
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: name, Namespace: ns}, &pp); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	if req.Enabled != nil {
		pp.Spec.Enabled = *req.Enabled
	}

	// Auto-create a K8s Secret when the user provides a raw API key.
	if req.Provider != "" && req.APIKey != "" && req.SecretName == "" {
		req.SecretName = defaultProviderSecretName(name, req.Provider)
		envKey := providerEnvKey(req.Provider)
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      req.SecretName,
				Namespace: ns,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "sympozium",
					"sympozium.ai/persona-pack":    name,
				},
			},
			StringData: map[string]string{envKey: req.APIKey},
		}
		createErr := s.client.Create(r.Context(), secret)
		if createErr != nil && !k8serrors.IsAlreadyExists(createErr) {
			http.Error(w, "failed to create credentials secret: "+createErr.Error(), http.StatusInternalServerError)
			return
		}
		if k8serrors.IsAlreadyExists(createErr) {
			// Update the existing secret with the new key.
			existing := &corev1.Secret{}
			if err := s.client.Get(r.Context(), types.NamespacedName{Name: req.SecretName, Namespace: ns}, existing); err != nil {
				http.Error(w, "failed to get existing secret: "+err.Error(), http.StatusInternalServerError)
				return
			}
			if existing.Data == nil {
				existing.Data = map[string][]byte{}
			}
			existing.Data[envKey] = []byte(req.APIKey)
			if err := s.client.Update(r.Context(), existing); err != nil {
				http.Error(w, "failed to update credentials secret: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}
	}

	if req.Provider != "" && req.SecretName != "" {
		existing := &corev1.Secret{}
		if err := s.client.Get(r.Context(), types.NamespacedName{Name: req.SecretName, Namespace: ns}, existing); err != nil {
			if k8serrors.IsNotFound(err) {
				http.Error(w, fmt.Sprintf("secret %q not found in namespace %q", req.SecretName, ns), http.StatusBadRequest)
				return
			}
			http.Error(w, "failed to get secret: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	if req.Provider != "" && req.SecretName != "" {
		pp.Spec.AuthRefs = []sympoziumv1alpha1.SecretRef{
			{Provider: req.Provider, Secret: req.SecretName},
		}
	}

	if req.Model != "" {
		for i := range pp.Spec.Personas {
			pp.Spec.Personas[i].Model = req.Model
		}
	}

	if req.BaseURL != "" {
		pp.Spec.BaseURL = req.BaseURL
	}

	if len(req.Channels) > 0 {
		for i := range pp.Spec.Personas {
			pp.Spec.Personas[i].Channels = req.Channels
		}
	}

	if req.ChannelConfigs != nil {
		pp.Spec.ChannelConfigs = req.ChannelConfigs
	}

	if req.PolicyRef != "" {
		pp.Spec.PolicyRef = req.PolicyRef
	}

	if req.HeartbeatInterval != "" {
		for i := range pp.Spec.Personas {
			if pp.Spec.Personas[i].Schedule != nil {
				pp.Spec.Personas[i].Schedule.Interval = req.HeartbeatInterval
				pp.Spec.Personas[i].Schedule.Cron = "" // clear cron so interval takes precedence
			}
		}
	}

	if len(req.SkillParams) > 0 {
		if pp.Spec.SkillParams == nil {
			pp.Spec.SkillParams = make(map[string]map[string]string)
		}
		for skill, params := range req.SkillParams {
			pp.Spec.SkillParams[skill] = params
		}
	}

	// Store GitHub token as a cluster secret when provided inline.
	if req.GithubToken != "" {
		if err := s.writeGithubTokenSecret(req.GithubToken); err != nil {
			http.Error(w, "failed to store GitHub token: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	if err := s.client.Update(r.Context(), &pp); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, pp)
}

func (s *Server) deletePersonaPack(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	pp := &sympoziumv1alpha1.PersonaPack{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
	}
	if err := s.client.Delete(r.Context(), pp); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- WebSocket streaming ---

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	if s.eventBus == nil {
		http.Error(w, "streaming not available (no event bus)", http.StatusServiceUnavailable)
		return
	}

	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.log.Error(err, "failed to upgrade websocket")
		return
	}
	defer conn.Close()

	// Subscribe to agent events
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	events, err := s.eventBus.Subscribe(ctx, eventbus.TopicAgentStreamChunk)
	if err != nil {
		s.log.Error(err, "failed to subscribe to events")
		return
	}

	// Read loop (handle client messages / keep-alive)
	go func() {
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				cancel()
				return
			}
		}
	}()

	// Ping ticker — keeps the connection alive through proxies/port-forwards.
	pingTicker := time.NewTicker(15 * time.Second)
	defer pingTicker.Stop()

	// Write loop (forward events to client)
	for {
		select {
		case <-ctx.Done():
			return
		case <-pingTicker.C:
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case event, ok := <-events:
			if !ok {
				return
			}
			data, _ := json.Marshal(event)
			if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
				return
			}
		}
	}
}

// providerEnvKey returns the environment variable key for a provider's API key.
func providerEnvKey(provider string) string {
	switch provider {
	case "openai":
		return "OPENAI_API_KEY"
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	case "azure-openai":
		return "AZURE_OPENAI_API_KEY"
	default:
		return "PROVIDER_API_KEY"
	}
}

func defaultProviderSecretName(resourceName, provider string) string {
	provider = strings.TrimSpace(strings.ToLower(provider))
	if provider == "" {
		return resourceName + "-credentials"
	}
	return fmt.Sprintf("%s-%s-key", resourceName, provider)
}

func (s *Server) listNamespaces(w http.ResponseWriter, r *http.Request) {
	var nsList corev1.NamespaceList
	if err := s.client.List(r.Context(), &nsList); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	names := make([]string, 0, len(nsList.Items))
	for _, ns := range nsList.Items {
		names = append(names, ns.Name)
	}
	writeJSON(w, names)
}

type PodInfo struct {
	Name         string            `json:"name"`
	Namespace    string            `json:"namespace"`
	Phase        string            `json:"phase"`
	NodeName     string            `json:"nodeName,omitempty"`
	PodIP        string            `json:"podIP,omitempty"`
	StartTime    *metav1.Time      `json:"startTime,omitempty"`
	RestartCount int32             `json:"restartCount"`
	InstanceRef  string            `json:"instanceRef,omitempty"`
	Labels       map[string]string `json:"labels,omitempty"`
}

func (s *Server) listPods(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var list corev1.PodList
	if err := s.client.List(r.Context(), &list, client.InNamespace(ns)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	out := make([]PodInfo, 0, len(list.Items))
	for _, p := range list.Items {
		var restarts int32
		for _, cs := range p.Status.ContainerStatuses {
			restarts += cs.RestartCount
		}
		inst := ""
		if p.Labels != nil {
			inst = p.Labels["sympozium.ai/instance"]
		}
		out = append(out, PodInfo{
			Name:         p.Name,
			Namespace:    p.Namespace,
			Phase:        string(p.Status.Phase),
			NodeName:     p.Spec.NodeName,
			PodIP:        p.Status.PodIP,
			StartTime:    p.Status.StartTime,
			RestartCount: restarts,
			InstanceRef:  inst,
			Labels:       p.Labels,
		})
	}

	writeJSON(w, out)
}

func (s *Server) getPodLogs(w http.ResponseWriter, r *http.Request) {
	if s.kube == nil {
		http.Error(w, "pod logs unavailable", http.StatusServiceUnavailable)
		return
	}

	name := r.PathValue("name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	tail := int64(200)
	req := s.kube.CoreV1().Pods(ns).GetLogs(name, &corev1.PodLogOptions{TailLines: &tail})
	raw, err := req.Do(r.Context()).Raw()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"logs": string(raw)})
}

type ObservabilityMetricsResponse struct {
	CollectorReachable bool               `json:"collectorReachable"`
	CollectorError     string             `json:"collectorError,omitempty"`
	CollectedAt        string             `json:"collectedAt"`
	Namespace          string             `json:"namespace"`
	AgentRunsTotal     float64            `json:"agentRunsTotal"`
	InputTokensTotal   float64            `json:"inputTokensTotal"`
	OutputTokensTotal  float64            `json:"outputTokensTotal"`
	ToolInvocations    float64            `json:"toolInvocations"`
	RunStatus          map[string]float64 `json:"runStatus,omitempty"`
	InputByModel       []MetricBreakdown  `json:"inputByModel,omitempty"`
	OutputByModel      []MetricBreakdown  `json:"outputByModel,omitempty"`
	ToolsByName        []MetricBreakdown  `json:"toolsByName,omitempty"`
	RawMetricNames     []string           `json:"rawMetricNames,omitempty"`
}

type MetricBreakdown struct {
	Label string  `json:"label"`
	Value float64 `json:"value"`
}

type promSample struct {
	Name   string
	Labels map[string]string
	Value  float64
}

func (s *Server) getObservabilityMetrics(w http.ResponseWriter, r *http.Request) {
	namespace := r.URL.Query().Get("namespace")
	if namespace == "" {
		namespace = "default"
	}

	resp := ObservabilityMetricsResponse{
		CollectorReachable: false,
		CollectedAt:        time.Now().UTC().Format(time.RFC3339),
		Namespace:          namespace,
		RunStatus:          map[string]float64{},
	}

	// Try the OTel collector first.
	raw, err := s.readCollectorMetrics(r.Context())
	if err == nil {
		resp.CollectorReachable = true
		samples := parsePrometheusSamples(raw)

		inputByModel := map[string]float64{}
		outputByModel := map[string]float64{}
		toolsByName := map[string]float64{}
		metricNames := map[string]struct{}{}

		for _, sample := range samples {
			metricNames[sample.Name] = struct{}{}
			switch sample.Name {
			case "sympozium_agent_runs_total":
				resp.AgentRunsTotal += sample.Value
				if status := sample.Labels["status"]; status != "" {
					resp.RunStatus[status] += sample.Value
				}
			case "gen_ai_usage_input_tokens_total":
				resp.InputTokensTotal += sample.Value
				if model := sample.Labels["model"]; model != "" {
					inputByModel[model] += sample.Value
				}
			case "gen_ai_usage_output_tokens_total":
				resp.OutputTokensTotal += sample.Value
				if model := sample.Labels["model"]; model != "" {
					outputByModel[model] += sample.Value
				}
			case "sympozium_tool_invocations_total":
				resp.ToolInvocations += sample.Value
				if toolName := sample.Labels["tool_name"]; toolName != "" {
					toolsByName[toolName] += sample.Value
				}
			}
		}

		resp.InputByModel = mapToMetricBreakdown(inputByModel)
		resp.OutputByModel = mapToMetricBreakdown(outputByModel)
		resp.ToolsByName = mapToMetricBreakdown(toolsByName)

		names := make([]string, 0, len(metricNames))
		for n := range metricNames {
			names = append(names, n)
		}
		sort.Strings(names)
		resp.RawMetricNames = names

		writeJSON(w, resp)
		return
	}

	// Collector unreachable — fall back to AgentRun CRD aggregation.
	resp.CollectorError = err.Error()

	var runs sympoziumv1alpha1.AgentRunList
	if err := s.client.List(r.Context(), &runs, client.InNamespace(namespace)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	inputByModel := map[string]float64{}
	outputByModel := map[string]float64{}
	toolsByName := map[string]float64{"tool_calls": 0}

	for i := range runs.Items {
		run := runs.Items[i]
		resp.AgentRunsTotal++
		phase := strings.TrimSpace(strings.ToLower(string(run.Status.Phase)))
		if phase == "" {
			phase = "unknown"
		}
		resp.RunStatus[phase]++

		model := strings.TrimSpace(run.Spec.Model.Model)
		if model == "" {
			model = "unknown"
		}
		if run.Status.TokenUsage != nil {
			in := float64(run.Status.TokenUsage.InputTokens)
			out := float64(run.Status.TokenUsage.OutputTokens)
			tools := float64(run.Status.TokenUsage.ToolCalls)
			resp.InputTokensTotal += in
			resp.OutputTokensTotal += out
			resp.ToolInvocations += tools
			inputByModel[model] += in
			outputByModel[model] += out
			toolsByName["tool_calls"] += tools
		}
	}

	resp.InputByModel = mapToMetricBreakdown(inputByModel)
	resp.OutputByModel = mapToMetricBreakdown(outputByModel)
	resp.ToolsByName = mapToMetricBreakdown(toolsByName)
	resp.RawMetricNames = []string{"sympozium.agent.runs", "gen_ai.usage.input_tokens", "gen_ai.usage.output_tokens", "sympozium.tool.invocations"}

	writeJSON(w, resp)
}

func mapToMetricBreakdown(m map[string]float64) []MetricBreakdown {
	out := make([]MetricBreakdown, 0, len(m))
	for k, v := range m {
		out = append(out, MetricBreakdown{Label: k, Value: v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Value == out[j].Value {
			return out[i].Label < out[j].Label
		}
		return out[i].Value > out[j].Value
	})
	return out
}

func (s *Server) readCollectorMetrics(ctx context.Context) (string, error) {
	if s.kube == nil {
		return "", fmt.Errorf("kubernetes client unavailable")
	}

	httpClient := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		"http://sympozium-otel-collector.sympozium-system.svc:8889/metrics",
		nil,
	)
	if err != nil {
		return "", fmt.Errorf("failed to create collector metrics request: %w", err)
	}
	res, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to query collector metrics: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", fmt.Errorf("collector metrics request failed: HTTP %d", res.StatusCode)
	}
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read collector metrics body: %w", err)
	}
	return string(raw), nil
}

func parsePrometheusSamples(raw string) []promSample {
	out := []promSample{}
	scanner := bufio.NewScanner(strings.NewReader(raw))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		value, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			continue
		}
		name, labels := parsePromMetricSelector(fields[0])
		out = append(out, promSample{Name: name, Labels: labels, Value: value})
	}
	return out
}

func parsePromMetricSelector(selector string) (string, map[string]string) {
	start := strings.Index(selector, "{")
	end := strings.LastIndex(selector, "}")
	if start < 0 || end < 0 || end <= start {
		return selector, map[string]string{}
	}
	name := selector[:start]
	return name, parsePromLabels(selector[start+1 : end])
}

func parsePromLabels(raw string) map[string]string {
	out := map[string]string{}
	for _, part := range splitCommaRespectQuotes(raw) {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		k := strings.TrimSpace(kv[0])
		v := strings.TrimSpace(kv[1])
		v = strings.Trim(v, "\"")
		v = strings.ReplaceAll(v, `\"`, `"`)
		if k != "" {
			out[k] = v
		}
	}
	return out
}

func splitCommaRespectQuotes(s string) []string {
	parts := []string{}
	var b strings.Builder
	inQuotes := false
	escaped := false
	for _, r := range s {
		switch {
		case escaped:
			b.WriteRune(r)
			escaped = false
		case r == '\\':
			b.WriteRune(r)
			escaped = true
		case r == '"':
			b.WriteRune(r)
			inQuotes = !inQuotes
		case r == ',' && !inQuotes:
			part := strings.TrimSpace(b.String())
			if part != "" {
				parts = append(parts, part)
			}
			b.Reset()
		default:
			b.WriteRune(r)
		}
	}
	last := strings.TrimSpace(b.String())
	if last != "" {
		parts = append(parts, last)
	}
	return parts
}

func (s *Server) getRunTelemetry(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var run sympoziumv1alpha1.AgentRun
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: name, Namespace: ns}, &run); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	writeJSON(w, map[string]interface{}{
		"runName":   name,
		"namespace": ns,
		"podName":   run.Status.PodName,
		"phase":     run.Status.Phase,
		"traceIds":  []string{},
		"events":    []interface{}{},
	})
}

type InstallDefaultPersonaPacksResponse struct {
	SourceNamespace string   `json:"sourceNamespace"`
	TargetNamespace string   `json:"targetNamespace"`
	Copied          []string `json:"copied"`
	AlreadyPresent  []string `json:"alreadyPresent"`
}

func (s *Server) installDefaultPersonaPacks(w http.ResponseWriter, r *http.Request) {
	targetNS := r.URL.Query().Get("namespace")
	if targetNS == "" {
		targetNS = "default"
	}
	sourceNS := "sympozium-system"

	var sourceList sympoziumv1alpha1.PersonaPackList
	if err := s.client.List(r.Context(), &sourceList, client.InNamespace(sourceNS)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	resp := InstallDefaultPersonaPacksResponse{SourceNamespace: sourceNS, TargetNamespace: targetNS, Copied: []string{}, AlreadyPresent: []string{}}
	for _, src := range sourceList.Items {
		var existing sympoziumv1alpha1.PersonaPack
		err := s.client.Get(r.Context(), types.NamespacedName{Name: src.Name, Namespace: targetNS}, &existing)
		if err == nil {
			resp.AlreadyPresent = append(resp.AlreadyPresent, src.Name)
			continue
		}
		if !k8serrors.IsNotFound(err) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		pack := &sympoziumv1alpha1.PersonaPack{ObjectMeta: metav1.ObjectMeta{Name: src.Name, Namespace: targetNS, Labels: src.Labels, Annotations: src.Annotations}, Spec: src.Spec}
		pack.Spec.Enabled = false
		if err := s.client.Create(r.Context(), pack); err != nil {
			if k8serrors.IsAlreadyExists(err) {
				resp.AlreadyPresent = append(resp.AlreadyPresent, src.Name)
				continue
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		resp.Copied = append(resp.Copied, src.Name)
	}

	writeJSON(w, resp)
}

// GatewayConfigResponse is the response for gateway config endpoints.
type GatewayConfigResponse struct {
	Enabled                  bool   `json:"enabled"`
	GatewayClassName         string `json:"gatewayClassName,omitempty"`
	Name                     string `json:"name,omitempty"`
	BaseDomain               string `json:"baseDomain,omitempty"`
	TLSEnabled               bool   `json:"tlsEnabled"`
	CertManagerClusterIssuer string `json:"certManagerClusterIssuer,omitempty"`
	TLSSecretName            string `json:"tlsSecretName,omitempty"`
	// Status fields
	Phase         string `json:"phase,omitempty"`
	Ready         bool   `json:"ready"`
	Address       string `json:"address,omitempty"`
	ListenerCount int    `json:"listenerCount,omitempty"`
	Message       string `json:"message,omitempty"`
}

// PatchGatewayConfigRequest is the request body for patching gateway config.
type PatchGatewayConfigRequest struct {
	Enabled                  *bool   `json:"enabled,omitempty"`
	GatewayClassName         *string `json:"gatewayClassName,omitempty"`
	Name                     *string `json:"name,omitempty"`
	BaseDomain               *string `json:"baseDomain,omitempty"`
	TLSEnabled               *bool   `json:"tlsEnabled,omitempty"`
	CertManagerClusterIssuer *string `json:"certManagerClusterIssuer,omitempty"`
	TLSSecretName            *string `json:"tlsSecretName,omitempty"`
}

func gatewayConfigResponseFromCR(config *sympoziumv1alpha1.SympoziumConfig) GatewayConfigResponse {
	resp := GatewayConfigResponse{
		Phase: config.Status.Phase,
	}
	if config.Status.Gateway != nil {
		resp.Ready = config.Status.Gateway.Ready
		resp.Address = config.Status.Gateway.Address
		resp.ListenerCount = config.Status.Gateway.ListenerCount
	}
	for _, c := range config.Status.Conditions {
		if c.Type == "Ready" && c.Message != "" {
			resp.Message = c.Message
			break
		}
	}
	if gw := config.Spec.Gateway; gw != nil {
		resp.Enabled = gw.Enabled
		resp.GatewayClassName = gw.GatewayClassName
		resp.Name = gw.Name
		resp.BaseDomain = gw.BaseDomain
		if gw.TLS != nil {
			resp.TLSEnabled = gw.TLS.Enabled
			resp.CertManagerClusterIssuer = gw.TLS.CertManagerClusterIssuer
			resp.TLSSecretName = gw.TLS.SecretName
		}
	}
	return resp
}

func (s *Server) getGatewayConfig(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var config sympoziumv1alpha1.SympoziumConfig
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: "default", Namespace: ns}, &config); err != nil {
		if k8serrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			// Return empty/disabled state (handles both missing CR and missing CRD)
			writeJSON(w, GatewayConfigResponse{})
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, gatewayConfigResponseFromCR(&config))
}

func (s *Server) createGatewayConfig(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var req PatchGatewayConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	config := sympoziumv1alpha1.SympoziumConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "default",
			Namespace: ns,
		},
		Spec: sympoziumv1alpha1.SympoziumConfigSpec{
			Gateway: &sympoziumv1alpha1.GatewaySpec{},
		},
	}
	applyGatewayPatch(config.Spec.Gateway, &req)

	if err := s.client.Create(r.Context(), &config); err != nil {
		if k8serrors.IsAlreadyExists(err) {
			http.Error(w, "gateway config already exists, use PATCH to update", http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, gatewayConfigResponseFromCR(&config))
}

func (s *Server) patchGatewayConfig(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var req PatchGatewayConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	var config sympoziumv1alpha1.SympoziumConfig
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: "default", Namespace: ns}, &config); err != nil {
		if k8serrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			http.Error(w, "gateway config not found, use POST to create", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if config.Spec.Gateway == nil {
		config.Spec.Gateway = &sympoziumv1alpha1.GatewaySpec{}
	}
	applyGatewayPatch(config.Spec.Gateway, &req)

	if err := s.client.Update(r.Context(), &config); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, gatewayConfigResponseFromCR(&config))
}

func (s *Server) deleteGatewayConfig(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var config sympoziumv1alpha1.SympoziumConfig
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: "default", Namespace: ns}, &config); err != nil {
		if k8serrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			http.Error(w, "gateway config not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if err := s.client.Delete(r.Context(), &config); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func applyGatewayPatch(gw *sympoziumv1alpha1.GatewaySpec, req *PatchGatewayConfigRequest) {
	if req.Enabled != nil {
		gw.Enabled = *req.Enabled
	}
	if req.GatewayClassName != nil {
		gw.GatewayClassName = *req.GatewayClassName
	}
	if req.Name != nil {
		gw.Name = *req.Name
	}
	if req.BaseDomain != nil {
		gw.BaseDomain = *req.BaseDomain
	}
	if req.TLSEnabled != nil || req.CertManagerClusterIssuer != nil || req.TLSSecretName != nil {
		if gw.TLS == nil {
			gw.TLS = &sympoziumv1alpha1.GatewayTLSSpec{}
		}
		if req.TLSEnabled != nil {
			gw.TLS.Enabled = *req.TLSEnabled
		}
		if req.CertManagerClusterIssuer != nil {
			gw.TLS.CertManagerClusterIssuer = *req.CertManagerClusterIssuer
		}
		if req.TLSSecretName != nil {
			gw.TLS.SecretName = *req.TLSSecretName
		}
	}
}

// GatewayMetricsResponse is the response for the gateway metrics endpoint.
type GatewayMetricsResponse struct {
	TotalRequests    int             `json:"totalRequests"`
	SuccessCount     int             `json:"successCount"`
	ErrorCount       int             `json:"errorCount"`
	AvgDurationSec   float64         `json:"avgDurationSec"`
	UptimeSec        int64           `json:"uptimeSec"`
	ServingInstances int             `json:"servingInstances"`
	Buckets          []GatewayBucket `json:"buckets"`
}

// GatewayBucket is a single time bucket in the gateway metrics timeseries.
type GatewayBucket struct {
	Timestamp   int64   `json:"ts"`
	Requests    int     `json:"requests"`
	Errors      int     `json:"errors"`
	AvgDuration float64 `json:"avgDurationSec"`
}

func (s *Server) getGatewayMetrics(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}
	rangeParam := r.URL.Query().Get("range")
	if rangeParam == "" {
		rangeParam = "24h"
	}

	var window time.Duration
	var bucketSize time.Duration
	switch rangeParam {
	case "1h":
		window = 1 * time.Hour
		bucketSize = 5 * time.Minute
	case "7d":
		window = 7 * 24 * time.Hour
		bucketSize = 24 * time.Hour
	default: // "24h"
		window = 24 * time.Hour
		bucketSize = 1 * time.Hour
	}

	now := time.Now().UTC()
	cutoff := now.Add(-window)

	// List web-proxy AgentRuns.
	var runs sympoziumv1alpha1.AgentRunList
	if err := s.client.List(r.Context(), &runs,
		client.InNamespace(ns),
		client.MatchingLabels{"sympozium.ai/source": "web-proxy"},
	); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Initialize buckets.
	numBuckets := int(window / bucketSize)
	buckets := make([]GatewayBucket, numBuckets)
	bucketDurTotal := make([]float64, numBuckets)
	bucketDurCount := make([]int, numBuckets)
	for i := range buckets {
		buckets[i].Timestamp = cutoff.Add(time.Duration(i) * bucketSize).UnixMilli()
	}

	resp := GatewayMetricsResponse{Buckets: buckets}
	var durTotal float64
	var durCount int

	for i := range runs.Items {
		run := &runs.Items[i]
		created := run.CreationTimestamp.Time
		if created.Before(cutoff) {
			continue
		}

		resp.TotalRequests++
		isFailed := run.Status.Phase == sympoziumv1alpha1.AgentRunPhaseFailed
		if isFailed {
			resp.ErrorCount++
		} else {
			resp.SuccessCount++
		}

		var durSec float64
		if run.Status.TokenUsage != nil && run.Status.TokenUsage.DurationMs > 0 {
			durSec = float64(run.Status.TokenUsage.DurationMs) / 1000.0
			durTotal += durSec
			durCount++
		}

		// Place into bucket.
		idx := int(created.Sub(cutoff) / bucketSize)
		if idx >= numBuckets {
			idx = numBuckets - 1
		}
		if idx >= 0 {
			buckets[idx].Requests++
			if isFailed {
				buckets[idx].Errors++
			}
			if durSec > 0 {
				bucketDurTotal[idx] += durSec
				bucketDurCount[idx]++
			}
		}
	}

	// Compute bucket averages.
	for i := range buckets {
		if bucketDurCount[i] > 0 {
			buckets[i].AvgDuration = bucketDurTotal[i] / float64(bucketDurCount[i])
		}
	}

	if durCount > 0 {
		resp.AvgDurationSec = durTotal / float64(durCount)
	}

	// Count serving instances and compute uptime.
	var allRuns sympoziumv1alpha1.AgentRunList
	if err := s.client.List(r.Context(), &allRuns,
		client.InNamespace(ns),
		client.MatchingLabels{"sympozium.ai/source": "web-proxy"},
	); err == nil {
		for i := range allRuns.Items {
			run := &allRuns.Items[i]
			if run.Status.Phase == sympoziumv1alpha1.AgentRunPhaseServing {
				resp.ServingInstances++
				uptime := int64(now.Sub(run.CreationTimestamp.Time).Seconds())
				if uptime > resp.UptimeSec {
					resp.UptimeSec = uptime
				}
			}
		}
	}

	writeJSON(w, resp)
}

// ClusterInfoResponse is the response for GET /api/v1/cluster.
type ClusterInfoResponse struct {
	Nodes      int    `json:"nodes"`
	Namespaces int    `json:"namespaces"`
	Pods       int    `json:"pods"`
	Version    string `json:"version,omitempty"`
}

func (s *Server) getClusterInfo(w http.ResponseWriter, r *http.Request) {
	var resp ClusterInfoResponse

	// Count nodes
	var nodeList corev1.NodeList
	if err := s.client.List(r.Context(), &nodeList); err == nil {
		resp.Nodes = len(nodeList.Items)
	}

	// Count all namespaces
	var nsList corev1.NamespaceList
	if err := s.client.List(r.Context(), &nsList); err == nil {
		resp.Namespaces = len(nsList.Items)
	}

	// Count all pods cluster-wide
	var podList corev1.PodList
	if err := s.client.List(r.Context(), &podList); err == nil {
		resp.Pods = len(podList.Items)
	}

	// Get cluster version
	if s.kube != nil {
		if info, err := s.kube.Discovery().ServerVersion(); err == nil {
			resp.Version = info.GitVersion
		}
	}

	writeJSON(w, resp)
}

// ── Provider discovery endpoints ─────────────────────────────────────────────

// ProviderNode describes a node with inference providers discovered by the node-probe DaemonSet.
type ProviderNode struct {
	NodeName  string            `json:"nodeName"`
	NodeIP    string            `json:"nodeIP"`
	Providers []NodeProvider    `json:"providers"`
	Labels    map[string]string `json:"labels,omitempty"`
}

// NodeProvider describes a single inference provider found on a node.
type NodeProvider struct {
	Name      string   `json:"name"`
	Port      int      `json:"port"`
	ProxyPort int      `json:"proxyPort,omitempty"`
	Models    []string `json:"models"`
	LastProbe string   `json:"lastProbe,omitempty"`
}

// ProviderModelsResponse is the response from the model proxy endpoint.
type ProviderModelsResponse struct {
	Models []string `json:"models"`
	Source string   `json:"source"`
}

// listProviderNodes returns nodes annotated by the node-probe DaemonSet with inference providers.
func (s *Server) listProviderNodes(w http.ResponseWriter, r *http.Request) {
	var nodeList corev1.NodeList
	if err := s.client.List(r.Context(), &nodeList); err != nil {
		http.Error(w, "failed to list nodes: "+err.Error(), http.StatusInternalServerError)
		return
	}

	providerFilter := r.URL.Query().Get("provider")

	var result []ProviderNode
	for _, node := range nodeList.Items {
		annotations := node.Annotations
		if annotations == nil {
			continue
		}

		// Only include nodes marked healthy by the probe.
		if annotations["sympozium.ai/inference-healthy"] != "true" {
			continue
		}

		lastProbe := annotations["sympozium.ai/inference-last-probe"]

		// Parse inference annotations to find providers.
		var providers []NodeProvider
		for key, val := range annotations {
			if !strings.HasPrefix(key, "sympozium.ai/inference-") {
				continue
			}
			suffix := strings.TrimPrefix(key, "sympozium.ai/inference-")

			// Skip meta annotations, models annotations, and proxy-port.
			if suffix == "healthy" || suffix == "last-probe" || suffix == "proxy-port" || strings.HasPrefix(suffix, "models-") {
				continue
			}

			providerName := suffix
			if providerFilter != "" && providerName != providerFilter {
				continue
			}

			port := 0
			fmt.Sscanf(val, "%d", &port)
			if port == 0 {
				continue
			}

			var models []string
			if modelsStr, ok := annotations["sympozium.ai/inference-models-"+providerName]; ok && modelsStr != "" {
				models = strings.Split(modelsStr, ",")
			}

			proxyPort := 0
			if pp, ok := annotations["sympozium.ai/inference-proxy-port"]; ok {
				fmt.Sscanf(pp, "%d", &proxyPort)
			}

			providers = append(providers, NodeProvider{
				Name:      providerName,
				Port:      port,
				ProxyPort: proxyPort,
				Models:    models,
				LastProbe: lastProbe,
			})
		}

		if len(providers) == 0 {
			continue
		}

		// Find InternalIP.
		nodeIP := ""
		for _, addr := range node.Status.Addresses {
			if addr.Type == corev1.NodeInternalIP {
				nodeIP = addr.Address
				break
			}
		}

		result = append(result, ProviderNode{
			NodeName:  node.Name,
			NodeIP:    nodeIP,
			Providers: providers,
			Labels:    node.Labels,
		})
	}

	if result == nil {
		result = []ProviderNode{}
	}
	writeJSON(w, result)
}

// proxyProviderModels proxies a model listing request to an in-cluster or node-based inference provider.
func (s *Server) proxyProviderModels(w http.ResponseWriter, r *http.Request) {
	baseURL := r.URL.Query().Get("baseURL")
	if baseURL == "" {
		http.Error(w, "baseURL query parameter is required", http.StatusBadRequest)
		return
	}

	// SSRF protection: validate the URL.
	parsed, err := url.Parse(baseURL)
	if err != nil {
		http.Error(w, "invalid baseURL", http.StatusBadRequest)
		return
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		http.Error(w, "baseURL must use http or https scheme", http.StatusBadRequest)
		return
	}

	// Resolve hostname and check for disallowed IPs.
	hostname := parsed.Hostname()
	ips, err := net.LookupHost(hostname)
	if err != nil {
		http.Error(w, "cannot resolve baseURL hostname", http.StatusBadRequest)
		return
	}
	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			continue
		}
		// Block link-local (metadata endpoints).
		if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			http.Error(w, "baseURL resolves to a disallowed address", http.StatusForbidden)
			return
		}
	}

	provider := r.URL.Query().Get("provider")

	// Determine the models endpoint URL.
	modelsURL := ""
	if provider == "ollama" || strings.Contains(baseURL, ":11434") {
		// Ollama uses /api/tags.
		modelsURL = strings.TrimRight(baseURL, "/")
		// If baseURL ends with /v1, strip it for the Ollama native API.
		modelsURL = strings.TrimSuffix(modelsURL, "/v1")
		modelsURL += "/api/tags"
	} else {
		// OpenAI-compatible: /v1/models.
		modelsURL = strings.TrimRight(baseURL, "/")
		if !strings.HasSuffix(modelsURL, "/v1") {
			modelsURL += "/v1"
		}
		modelsURL += "/models"
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(modelsURL)
	if err != nil {
		http.Error(w, "failed to reach provider: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		http.Error(w, fmt.Sprintf("provider returned HTTP %d", resp.StatusCode), http.StatusBadGateway)
		return
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		http.Error(w, "failed to read provider response", http.StatusBadGateway)
		return
	}

	// Parse models from response.
	models := parseProviderModels(body)

	writeJSON(w, ProviderModelsResponse{
		Models: models,
		Source: "live",
	})
}

// parseProviderModels extracts model names from a JSON response.
// Supports Ollama format and OpenAI-compatible format.
func parseProviderModels(body []byte) []string {
	// Try Ollama format: {"models":[{"name":"llama3:latest"}]}
	var ollamaResp struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &ollamaResp); err == nil && len(ollamaResp.Models) > 0 {
		names := make([]string, 0, len(ollamaResp.Models))
		for _, m := range ollamaResp.Models {
			name := m.Name
			// Strip ":latest" tag for cleaner display.
			if idx := strings.Index(name, ":"); idx > 0 {
				if name[idx+1:] == "latest" {
					name = name[:idx]
				}
			}
			names = append(names, name)
		}
		sort.Strings(names)
		return names
	}

	// Try OpenAI-compatible format: {"data":[{"id":"model-name"}]}
	var openaiResp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &openaiResp); err == nil && len(openaiResp.Data) > 0 {
		names := make([]string, 0, len(openaiResp.Data))
		for _, m := range openaiResp.Data {
			names = append(names, m.ID)
		}
		sort.Strings(names)
		return names
	}

	return []string{}
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
