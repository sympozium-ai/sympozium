// Package apiserver provides the HTTP + WebSocket API server for Sympozium.
package apiserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-logr/logr"
	"github.com/gorilla/websocket"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/alexsjones/sympozium/api/v1alpha1"
	"github.com/alexsjones/sympozium/internal/eventbus"
)

// Server is the Sympozium API server.
type Server struct {
	client   client.Client
	eventBus eventbus.EventBus
	log      logr.Logger
	upgrader websocket.Upgrader
}

// NewServer creates a new API server.
func NewServer(c client.Client, bus eventbus.EventBus, log logr.Logger) *Server {
	return &Server{
		client:   c,
		eventBus: bus,
		log:      log,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}
}

// Start starts the HTTP server.
func (s *Server) Start(addr string) error {
	mux := http.NewServeMux()

	// Instance endpoints
	mux.HandleFunc("GET /api/v1/instances", s.listInstances)
	mux.HandleFunc("GET /api/v1/instances/{name}", s.getInstance)
	mux.HandleFunc("DELETE /api/v1/instances/{name}", s.deleteInstance)

	// Run endpoints
	mux.HandleFunc("GET /api/v1/runs", s.listRuns)
	mux.HandleFunc("GET /api/v1/runs/{name}", s.getRun)
	mux.HandleFunc("POST /api/v1/runs", s.createRun)

	// Policy endpoints
	mux.HandleFunc("GET /api/v1/policies", s.listPolicies)
	mux.HandleFunc("GET /api/v1/policies/{name}", s.getPolicy)

	// Skill endpoints
	mux.HandleFunc("GET /api/v1/skills", s.listSkills)
	mux.HandleFunc("GET /api/v1/skills/{name}", s.getSkill)

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

	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	s.log.Info("Starting API server", "addr", addr)
	return server.ListenAndServe()
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

	run := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: req.InstanceRef + "-",
			Namespace:    ns,
		},
		Spec: sympoziumv1alpha1.AgentRunSpec{
			InstanceRef: req.InstanceRef,
			AgentID:     req.AgentID,
			SessionKey:  req.SessionKey,
			Task:        req.Task,
		},
	}

	if req.Model != "" {
		run.Spec.Model = sympoziumv1alpha1.ModelSpec{
			Model: req.Model,
		}
	}

	if err := s.client.Create(r.Context(), run); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, run)
}

// --- Policy handlers ---

func (s *Server) listPolicies(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var list sympoziumv1alpha1.SympoziumPolicyList
	if err := s.client.List(r.Context(), &list, client.InNamespace(ns)); err != nil {
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
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var list sympoziumv1alpha1.SkillPackList
	if err := s.client.List(r.Context(), &list, client.InNamespace(ns)); err != nil {
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

// --- WebSocket streaming ---

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
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

	// Write loop (forward events to client)
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-events:
			data, _ := json.Marshal(event)
			if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
				return
			}
		}
	}
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
