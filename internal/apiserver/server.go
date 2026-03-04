// Package apiserver provides the HTTP + WebSocket API server for Sympozium.
package apiserver

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/gorilla/websocket"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
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

	// GitHub GitOps skill auth endpoints (device-flow OAuth)
	mux.HandleFunc("POST /api/v1/skills/github-gitops/auth", s.handleGithubAuth)
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

	// Wrap with auth middleware if a token is configured.
	if token != "" {
		return authMiddleware(token, mux)
	}

	return mux
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
		},
	}

	if req.BaseURL != "" {
		inst.Spec.Agents.Default.BaseURL = req.BaseURL
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
	if authSecret == "" {
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
	Enabled           *bool             `json:"enabled,omitempty"`
	Provider          string            `json:"provider,omitempty"`
	SecretName        string            `json:"secretName,omitempty"`
	APIKey            string            `json:"apiKey,omitempty"`
	Model             string            `json:"model,omitempty"`
	BaseURL           string            `json:"baseURL,omitempty"`
	ChannelConfigs    map[string]string `json:"channelConfigs,omitempty"`
	PolicyRef         string            `json:"policyRef,omitempty"`
	HeartbeatInterval string            `json:"heartbeatInterval,omitempty"`
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

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
