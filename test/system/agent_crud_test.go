package system_test

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestAgentCreateViaAPI(t *testing.T) {
	ns := createTestNamespace(t)
	name := "sys-agent-create"

	body := map[string]any{
		"name":     name,
		"provider": "lm-studio",
		"model":    "qwen/qwen3.5-9b",
		"baseURL":  "http://fake:1234/v1",
	}
	rec := httpDo(t, http.MethodPost, fmt.Sprintf("/api/v1/agents?namespace=%s", ns), body)
	requireStatus(t, rec, http.StatusCreated)

	t.Cleanup(func() {
		httpDo(t, http.MethodDelete, fmt.Sprintf("/api/v1/agents/%s?namespace=%s", name, ns), nil)
	})

	// Verify Agent CR exists with correct spec.
	var agent sympoziumv1alpha1.Agent
	assertExists(t, &agent, ns, name)

	cfg := agent.Spec.Agents.Default
	if cfg.Model != "qwen/qwen3.5-9b" {
		t.Errorf("model = %q, want qwen/qwen3.5-9b", cfg.Model)
	}
	if cfg.BaseURL != "http://fake:1234/v1" {
		t.Errorf("baseURL = %q", cfg.BaseURL)
	}
}

func TestAgentDeleteViaAPI(t *testing.T) {
	ns := createTestNamespace(t)
	name := "sys-agent-delete"

	body := map[string]any{
		"name":     name,
		"provider": "lm-studio",
		"model":    "test",
		"baseURL":  "http://fake:1234/v1",
	}
	rec := httpDo(t, http.MethodPost, fmt.Sprintf("/api/v1/agents?namespace=%s", ns), body)
	requireStatus(t, rec, http.StatusCreated)

	// Delete.
	rec = httpDo(t, http.MethodDelete, fmt.Sprintf("/api/v1/agents/%s?namespace=%s", name, ns), nil)
	if rec.Code != 200 && rec.Code != 204 {
		t.Fatalf("delete status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// Verify gone.
	pollUntil(t, 10*time.Second, 200*time.Millisecond, func() bool {
		var a sympoziumv1alpha1.Agent
		return k8sClient.Get(testCtx, client.ObjectKey{Namespace: ns, Name: name}, &a) != nil
	})
}

func TestAgentGetViaAPI(t *testing.T) {
	ns := createTestNamespace(t)
	name := "sys-agent-get"

	body := map[string]any{
		"name":     name,
		"provider": "lm-studio",
		"model":    "test",
		"baseURL":  "http://fake:1234/v1",
	}
	rec := httpDo(t, http.MethodPost, fmt.Sprintf("/api/v1/agents?namespace=%s", ns), body)
	requireStatus(t, rec, http.StatusCreated)
	t.Cleanup(func() {
		httpDo(t, http.MethodDelete, fmt.Sprintf("/api/v1/agents/%s?namespace=%s", name, ns), nil)
	})

	// GET via API.
	rec = httpDo(t, http.MethodGet, fmt.Sprintf("/api/v1/agents/%s?namespace=%s", name, ns), nil)
	requireStatus(t, rec, http.StatusOK)

	var agent sympoziumv1alpha1.Agent
	agent, _ = httpJSON[sympoziumv1alpha1.Agent](t, http.MethodGet, fmt.Sprintf("/api/v1/agents/%s?namespace=%s", name, ns), nil)
	if agent.Name != name {
		t.Errorf("agent name = %q, want %q", agent.Name, name)
	}
}

func TestAgentListViaAPI(t *testing.T) {
	ns := createTestNamespace(t)

	// Create two agents.
	for _, n := range []string{"sys-list-a", "sys-list-b"} {
		body := map[string]any{
			"name":     n,
			"provider": "lm-studio",
			"model":    "test",
			"baseURL":  "http://fake:1234/v1",
		}
		rec := httpDo(t, http.MethodPost, fmt.Sprintf("/api/v1/agents?namespace=%s", ns), body)
		requireStatus(t, rec, http.StatusCreated)
		t.Cleanup(func() {
			httpDo(t, http.MethodDelete, fmt.Sprintf("/api/v1/agents/%s?namespace=%s", n, ns), nil)
		})
	}

	// List — the API returns a bare JSON array, not a wrapped object.
	// Poll to allow the informer cache to sync both agents.
	pollUntil(t, 5*time.Second, 200*time.Millisecond, func() bool {
		resp, code := httpJSON[[]sympoziumv1alpha1.Agent](t, http.MethodGet, fmt.Sprintf("/api/v1/agents?namespace=%s", ns), nil)
		return code == http.StatusOK && len(resp) >= 2
	})
}
