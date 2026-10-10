package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// newMCPServerTestReconciler builds an MCPServerReconciler backed by a fake
// client pre-loaded with the supplied objects. The status subresource must be
// registered for MCPServer or status writes fail silently.
func newMCPServerTestReconciler(t *testing.T, objs ...client.Object) (*MCPServerReconciler, client.Client) {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add appsv1 scheme: %v", err)
	}
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add sympozium scheme: %v", err)
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&sympoziumv1alpha1.MCPServer{}).
		Build()

	return &MCPServerReconciler{
		Client: cl,
		Scheme: scheme,
		Log:    logr.Discard(),
	}, cl
}

// mcpTestServer returns an httptest MCP server that answers initialize and
// tools/list with the supplied tool names.
func mcpTestServer(t *testing.T, toolNames []string) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Mcp-Session-Id", "test-session")

		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{
				"protocolVersion": "2024-11-05",
				"serverInfo":      map[string]any{"name": "test", "version": "1.0"},
			}
		case "tools/list":
			tools := make([]map[string]any, 0, len(toolNames))
			for _, n := range toolNames {
				tools = append(tools, map[string]any{"name": n, "inputSchema": map[string]any{"type": "object"}})
			}
			result = map[string]any{"tools": tools}
		default:
			http.Error(w, "method not found", http.StatusNotFound)
			return
		}

		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  result,
		}
		json.NewEncoder(w).Encode(resp)
	}))
}

// TestMCPServerExternalDiscoversTools verifies that an external MCPServer
// (Spec.URL set) populates status.toolCount and status.tools from the MCP
// server's tools/list response and sets the ToolsDiscovered condition.
func TestMCPServerExternalDiscoversTools(t *testing.T) {
	srv := mcpTestServer(t, []string{"get_date", "get_time"})
	defer srv.Close()

	ms := &sympoziumv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "date-time",
			Namespace: "default",
		},
		Spec: sympoziumv1alpha1.MCPServerSpec{
			TransportType: "http",
			URL:           srv.URL,
			ToolsPrefix:   "dt",
			Timeout:       5,
		},
	}

	r, cl := newMCPServerTestReconciler(t, ms)

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "date-time", Namespace: "default"},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var got sympoziumv1alpha1.MCPServer
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "date-time", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get mcpserver: %v", err)
	}

	if got.Status.ToolCount != 2 {
		t.Fatalf("expected toolCount 2, got %d", got.Status.ToolCount)
	}
	if len(got.Status.Tools) != 2 || got.Status.Tools[0] != "get_date" || got.Status.Tools[1] != "get_time" {
		t.Fatalf("expected tools [get_date get_time], got %v", got.Status.Tools)
	}

	cond := meta.FindStatusCondition(got.Status.Conditions, "ToolsDiscovered")
	if cond == nil {
		t.Fatalf("expected ToolsDiscovered condition, got %v", got.Status.Conditions)
	}
	if cond.Status != metav1.ConditionTrue {
		t.Fatalf("expected ToolsDiscovered True, got %s (%s)", cond.Status, cond.Message)
	}
}

// TestMCPServerManagedReadyDiscoversTools verifies that a managed MCPServer
// whose Deployment is ready populates tool status via the MCP client.
func TestMCPServerManagedReadyDiscoversTools(t *testing.T) {
	srv := mcpTestServer(t, []string{"get_date", "get_time"})
	defer srv.Close()

	ms := &sympoziumv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "date-time",
			Namespace: "default",
		},
		Spec: sympoziumv1alpha1.MCPServerSpec{
			TransportType: "stdio",
			ToolsPrefix:   "dt",
			Timeout:       5,
			Deployment: &sympoziumv1alpha1.MCPServerDeployment{
				Image: "example/date-time:latest",
			},
		},
	}

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "date-time",
			Namespace: "default",
		},
		Status: appsv1.DeploymentStatus{
			ReadyReplicas: 1,
		},
	}

	r, cl := newMCPServerTestReconciler(t, ms, deploy)
	// Test hook: point discovery at the reachable httptest server instead of
	// the in-cluster .svc URL.
	r.discoveryURLOverride = srv.URL

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "date-time", Namespace: "default"},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var got sympoziumv1alpha1.MCPServer
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "date-time", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get mcpserver: %v", err)
	}

	if got.Status.ToolCount != 2 {
		t.Fatalf("expected toolCount 2, got %d", got.Status.ToolCount)
	}
	if len(got.Status.Tools) != 2 || got.Status.Tools[0] != "get_date" || got.Status.Tools[1] != "get_time" {
		t.Fatalf("expected tools [get_date get_time], got %v", got.Status.Tools)
	}

	cond := meta.FindStatusCondition(got.Status.Conditions, "ToolsDiscovered")
	if cond == nil {
		t.Fatalf("expected ToolsDiscovered condition, got %v", got.Status.Conditions)
	}
	if cond.Status != metav1.ConditionTrue {
		t.Fatalf("expected ToolsDiscovered True, got %s (%s)", cond.Status, cond.Message)
	}
}

// TestMCPServerDiscoveryFailureSetsCondition verifies that when tools/list
// fails, the ToolsDiscovered condition is False with the error as the message
// and Ready is not affected.
func TestMCPServerDiscoveryFailureSetsCondition(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	ms := &sympoziumv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "broken",
			Namespace: "default",
		},
		Spec: sympoziumv1alpha1.MCPServerSpec{
			TransportType: "http",
			URL:           srv.URL,
			ToolsPrefix:   "dt",
			Timeout:       5,
		},
	}

	r, cl := newMCPServerTestReconciler(t, ms)

	res, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "broken", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if res.RequeueAfter != toolDiscoveryRetry {
		t.Fatalf("expected a requeue after %s to retry discovery, got %s", toolDiscoveryRetry, res.RequeueAfter)
	}

	var got sympoziumv1alpha1.MCPServer
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "broken", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get mcpserver: %v", err)
	}

	cond := meta.FindStatusCondition(got.Status.Conditions, "ToolsDiscovered")
	if cond == nil {
		t.Fatalf("expected ToolsDiscovered condition, got %v", got.Status.Conditions)
	}
	if cond.Status != metav1.ConditionFalse {
		t.Fatalf("expected ToolsDiscovered False, got %s", cond.Status)
	}
	if cond.Message == "" {
		t.Fatalf("expected ToolsDiscovered message to carry the error, got empty")
	}
	// Ready must remain True for the external path regardless of discovery.
	if !got.Status.Ready {
		t.Fatalf("expected Ready to stay true independent of discovery")
	}
}
