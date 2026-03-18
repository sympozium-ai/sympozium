package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

func TestListMCPServersEmpty(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/mcpservers?namespace=default", nil)
	rec := httptest.NewRecorder()
	srv.buildMux(nil, "").ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var items []sympoziumv1alpha1.MCPServer
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected 0 items, got %d", len(items))
	}
}

func TestCreateAndGetMCPServer(t *testing.T) {
	srv, cl := newTestServer(t)

	// Create
	payload := CreateMCPServerRequest{
		Name:          "dynatrace",
		TransportType: "http",
		ToolsPrefix:   "dt",
		URL:           "http://dynatrace-mcp:8080",
		Timeout:       60,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/mcpservers?namespace=default", bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	srv.buildMux(nil, "").ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", rec.Code, rec.Body.String())
	}

	// Verify in cluster
	var got sympoziumv1alpha1.MCPServer
	if err := cl.Get(context.Background(), client.ObjectKey{Name: "dynatrace", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get mcpserver: %v", err)
	}
	if got.Spec.TransportType != "http" {
		t.Fatalf("expected transport http, got %s", got.Spec.TransportType)
	}
	if got.Spec.ToolsPrefix != "dt" {
		t.Fatalf("expected prefix dt, got %s", got.Spec.ToolsPrefix)
	}
	if got.Spec.URL != "http://dynatrace-mcp:8080" {
		t.Fatalf("expected URL http://dynatrace-mcp:8080, got %s", got.Spec.URL)
	}
	if got.Spec.Timeout != 60 {
		t.Fatalf("expected timeout 60, got %d", got.Spec.Timeout)
	}

	// Get via API
	req = httptest.NewRequest(http.MethodGet, "/api/v1/mcpservers/dynatrace?namespace=default", nil)
	rec = httptest.NewRecorder()
	srv.buildMux(nil, "").ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on get, got %d body=%s", rec.Code, rec.Body.String())
	}

	var fetched sympoziumv1alpha1.MCPServer
	if err := json.Unmarshal(rec.Body.Bytes(), &fetched); err != nil {
		t.Fatalf("decode get response: %v", err)
	}
	if fetched.Spec.ToolsPrefix != "dt" {
		t.Fatalf("get response prefix mismatch: %s", fetched.Spec.ToolsPrefix)
	}
}

func TestCreateMCPServerWithDeployment(t *testing.T) {
	srv, cl := newTestServer(t)

	payload := CreateMCPServerRequest{
		Name:          "k8s-net",
		TransportType: "stdio",
		ToolsPrefix:   "k8snet",
		Image:         "ghcr.io/org/k8s-net-mcp:v1",
	}
	raw, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/mcpservers?namespace=default", bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	srv.buildMux(nil, "").ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", rec.Code, rec.Body.String())
	}

	var got sympoziumv1alpha1.MCPServer
	if err := cl.Get(context.Background(), client.ObjectKey{Name: "k8s-net", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get mcpserver: %v", err)
	}
	if got.Spec.Deployment == nil {
		t.Fatal("expected deployment to be set")
	}
	if got.Spec.Deployment.Image != "ghcr.io/org/k8s-net-mcp:v1" {
		t.Fatalf("expected image ghcr.io/org/k8s-net-mcp:v1, got %s", got.Spec.Deployment.Image)
	}
}

func TestCreateMCPServerValidation(t *testing.T) {
	srv, _ := newTestServer(t)

	// Missing required fields
	payload := `{"name":"bad"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mcpservers?namespace=default", bytes.NewBufferString(payload))
	rec := httptest.NewRecorder()
	srv.buildMux(nil, "").ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCreateMCPServerDuplicate(t *testing.T) {
	existing := &sympoziumv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "existing", Namespace: "default"},
		Spec: sympoziumv1alpha1.MCPServerSpec{
			TransportType: "http",
			ToolsPrefix:   "ex",
		},
	}
	srv, _ := newTestServer(t, existing)

	payload := CreateMCPServerRequest{
		Name:          "existing",
		TransportType: "http",
		ToolsPrefix:   "ex",
	}
	raw, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/mcpservers?namespace=default", bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	srv.buildMux(nil, "").ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestDeleteMCPServer(t *testing.T) {
	existing := &sympoziumv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "to-delete", Namespace: "default"},
		Spec: sympoziumv1alpha1.MCPServerSpec{
			TransportType: "http",
			ToolsPrefix:   "del",
		},
	}
	srv, cl := newTestServer(t, existing)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/mcpservers/to-delete?namespace=default", nil)
	rec := httptest.NewRecorder()
	srv.buildMux(nil, "").ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d body=%s", rec.Code, rec.Body.String())
	}

	// Verify deleted
	var got sympoziumv1alpha1.MCPServer
	err := cl.Get(context.Background(), client.ObjectKey{Name: "to-delete", Namespace: "default"}, &got)
	if err == nil {
		t.Fatal("expected mcpserver to be deleted")
	}
}

func TestDeleteMCPServerNotFound(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/mcpservers/nonexistent?namespace=default", nil)
	rec := httptest.NewRecorder()
	srv.buildMux(nil, "").ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPatchMCPServer(t *testing.T) {
	existing := &sympoziumv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "to-patch", Namespace: "default"},
		Spec: sympoziumv1alpha1.MCPServerSpec{
			TransportType: "http",
			ToolsPrefix:   "old",
			Timeout:       30,
		},
	}
	srv, cl := newTestServer(t, existing)

	newPrefix := "new"
	newTimeout := 90
	payload := PatchMCPServerRequest{
		ToolsPrefix: &newPrefix,
		Timeout:     &newTimeout,
		ToolsAllow:  []string{"read_logs", "get_metrics"},
	}
	raw, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/mcpservers/to-patch?namespace=default", bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	srv.buildMux(nil, "").ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var got sympoziumv1alpha1.MCPServer
	if err := cl.Get(context.Background(), client.ObjectKey{Name: "to-patch", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get mcpserver: %v", err)
	}
	if got.Spec.ToolsPrefix != "new" {
		t.Fatalf("expected prefix new, got %s", got.Spec.ToolsPrefix)
	}
	if got.Spec.Timeout != 90 {
		t.Fatalf("expected timeout 90, got %d", got.Spec.Timeout)
	}
	if len(got.Spec.ToolsAllow) != 2 {
		t.Fatalf("expected 2 toolsAllow, got %d", len(got.Spec.ToolsAllow))
	}
	// Transport should remain unchanged
	if got.Spec.TransportType != "http" {
		t.Fatalf("expected transport unchanged (http), got %s", got.Spec.TransportType)
	}
}

func TestPatchMCPServerNotFound(t *testing.T) {
	srv, _ := newTestServer(t)

	payload := `{"timeout":60}`
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/mcpservers/ghost?namespace=default", bytes.NewBufferString(payload))
	rec := httptest.NewRecorder()
	srv.buildMux(nil, "").ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestListMCPServersReturnsAll(t *testing.T) {
	s1 := &sympoziumv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "server-a", Namespace: "default"},
		Spec:       sympoziumv1alpha1.MCPServerSpec{TransportType: "http", ToolsPrefix: "a"},
	}
	s2 := &sympoziumv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "server-b", Namespace: "other"},
		Spec:       sympoziumv1alpha1.MCPServerSpec{TransportType: "stdio", ToolsPrefix: "b"},
	}
	srv, _ := newTestServer(t, s1, s2)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/mcpservers", nil)
	rec := httptest.NewRecorder()
	srv.buildMux(nil, "").ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var items []sympoziumv1alpha1.MCPServer
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items across namespaces, got %d", len(items))
	}
}

func TestGetMCPServerNotFound(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/mcpservers/nope?namespace=default", nil)
	rec := httptest.NewRecorder()
	srv.buildMux(nil, "").ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}
