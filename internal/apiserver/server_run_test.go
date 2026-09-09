package apiserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

func TestCreateRunEnduringLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name, lifecycle, limits, task string
		want                          int
	}{
		{"enduring", "enduring", `{"leaseSeconds":300,"maxTurns":4,"maxModelRequests":12,"maxOutputTokens":4096}`, "remember violet", http.StatusCreated},
		{"one-hour enduring", "enduring", `{"leaseSeconds":3600,"maxTurns":4,"maxModelRequests":12,"maxOutputTokens":4096}`, "remember violet", http.StatusCreated},
		{"one-shot", "one-shot", `null`, "one task", http.StatusCreated},
		{"legacy", "", `null`, "one task", http.StatusCreated},
		{"missing limits", "enduring", `null`, "task", http.StatusBadRequest},
		{"wrong lifecycle", "persistent", `null`, "task", http.StatusBadRequest},
		{"stray limits", "one-shot", `{"leaseSeconds":300,"maxTurns":4}`, "task", http.StatusBadRequest},
		{"over budget", "enduring", `{"leaseSeconds":300,"maxTurns":1025}`, "task", http.StatusBadRequest},
		{"oversized initial turn", "enduring", `{"leaseSeconds":300,"maxTurns":4}`, strings.Repeat("é", 1025), http.StatusBadRequest},
		{"NUL initial turn", "enduring", `{"leaseSeconds":300,"maxTurns":4}`, "bad\x00message", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			agent := &sympoziumv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "default"}}
			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(agent).Build()
			srv := NewServer(cl, nil, nil, logr.Discard())
			body, err := json.Marshal(map[string]any{"agentRef": "agent", "task": tc.task, "systemPrompt": "Retain conversation context.", "backend": "celln", "provider": "deepseek", "model": "deepseek-chat", "cellnSelection": map[string]any{"toolRefs": []any{}}, "executionLifecycle": tc.lifecycle, "enduring": json.RawMessage(tc.limits)})
			if err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			srv.Handler(nil).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/runs", bytes.NewReader(body)))
			if response.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", response.Code, tc.want, response.Body.String())
			}
			var stored sympoziumv1alpha1.AgentRunList
			if err := cl.List(t.Context(), &stored); err != nil {
				t.Fatal(err)
			}
			if tc.want != http.StatusCreated {
				if len(stored.Items) != 0 {
					t.Fatal("invalid request persisted")
				}
				return
			}
			if len(stored.Items) != 1 {
				t.Fatal("missing run")
			}
			run := stored.Items[0]
			if tc.lifecycle == "enduring" && (run.Spec.Timeout == nil || run.Spec.Timeout.Duration != time.Duration(run.Spec.Enduring.LeaseSeconds)*time.Second) {
				t.Fatal("default timeout must follow the requested parent lease, not the one-shot default")
			}
			if run.Spec.ExecutionLifecycle != tc.lifecycle || run.Status.CellnParent != nil || run.Status.Phase != "" {
				t.Fatal("lifecycle lost or admission fabricated")
			}
			if tc.lifecycle == "enduring" && (run.Spec.Enduring == nil || run.Spec.Enduring.MaxTurns != 4 || run.Spec.Enduring.MaxModelRequests != 12) {
				t.Fatal("requested ceilings lost")
			}
			if run.Spec.SystemPrompt != "Retain conversation context." {
				t.Fatal("explicit parent persona lost")
			}
		})
	}
}

func TestCreateRunEnduringRefusesNonCatalogueExecution(t *testing.T) {
	for _, backend := range []string{"celln", "job", ""} {
		body, err := json.Marshal(map[string]any{
			"agentRef": "agent", "task": "task", "backend": backend,
			"executionLifecycle": "enduring", "enduring": map[string]int{"leaseSeconds": 300, "maxTurns": 4},
		})
		if err != nil {
			t.Fatal(err)
		}
		// No client: invalid intent must be refused before any Kubernetes lookup.
		srv := NewServer(nil, nil, nil, logr.Discard())
		response := httptest.NewRecorder()
		srv.Handler(nil).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/runs", bytes.NewReader(body)))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("backend %q: %d %s", backend, response.Code, response.Body.String())
		}
	}
}

func TestCreateRunWithRuntimeRefCarriesHarnessPrompt(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	agent := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "test-agent", Namespace: "default"},
		Spec: sympoziumv1alpha1.AgentSpec{Agents: sympoziumv1alpha1.AgentsSpec{
			Default: sympoziumv1alpha1.AgentConfig{Model: "local", BaseURL: "http://llm.local/v1"},
		}},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(agent).Build()
	srv := NewServer(cl, nil, nil, logr.Discard())
	body := bytes.NewBufferString(`{"agentRef":"test-agent","task":"prove harness prompt","runtimeRef":"reference-v1"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runs", body)
	resp := httptest.NewRecorder()

	srv.Handler(nil).ServeHTTP(resp, req)
	if resp.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusCreated, resp.Body.String())
	}
	var created sympoziumv1alpha1.AgentRun
	if err := json.Unmarshal(resp.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Spec.Task.Mode != "harness" {
		t.Fatalf("task.mode = %q, want harness", created.Spec.Task.Mode)
	}
	if got := created.Spec.Task.Parameters["runtime"]; got != "reference-v1" {
		t.Errorf("task.parameters.runtime = %q", got)
	}
	if got := created.Spec.Task.Parameters["prompt"]; got != "prove harness prompt" {
		t.Errorf("task.parameters.prompt = %q", got)
	}
}

func TestCreateCatalogueRunPreservesIntentAndUsesHostModelAuthority(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	agent := &sympoziumv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "default"}, Spec: sympoziumv1alpha1.AgentSpec{RuntimeRef: "default-runtime", AuthRefs: []sympoziumv1alpha1.SecretRef{}}}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(agent).Build()
	srv := NewServer(cl, nil, nil, logr.Discard())
	body := `{"agentRef":"agent","task":"use tools","backend":"celln","provider":"deepseek","model":"deepseek-chat","cellnSelection":{"runtimeRef":"override-runtime","toolRefs":[{"name":"uppercase","revision":"v1"},{"name":"length","revision":"v1"}]}}`
	response := httptest.NewRecorder()
	srv.Handler(nil).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/runs", bytes.NewBufferString(body)))
	if response.Code != http.StatusCreated {
		t.Fatalf("catalogue create: %d %s", response.Code, response.Body.String())
	}
	if got := response.Result().Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("committed response content type = %q, want application/json", got)
	}
	var run sympoziumv1alpha1.AgentRun
	if err := json.Unmarshal(response.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	if !run.Spec.Task.IsString() || run.Spec.Celln != nil || run.Spec.CellnSelection == nil || run.Spec.CellnSelection.RuntimeRef != "override-runtime" || len(run.Spec.CellnSelection.ToolRefs) != 2 || run.Spec.CellnSelection.ToolRefs[0].Name != "uppercase" || run.Spec.CellnSelection.ToolRefs[1].Name != "length" {
		t.Fatal("catalogue intent changed or converted to OCI task")
	}
	if run.Spec.Model.Provider != "deepseek" || run.Spec.Model.Model != "deepseek-chat" || run.Spec.Model.AuthSecretRef != "" || run.Spec.Model.BaseURL != "" {
		t.Fatal("model authority inherited instead of explicit host selection")
	}
}

func TestCreateCatalogueRunRefusesAmbiguousOrImplicitModel(t *testing.T) {
	for _, body := range []string{
		`{"agentRef":"agent","task":"task","backend":"celln","provider":"deepseek","model":"deepseek-chat","celln":{},"cellnSelection":{"toolRefs":[]}}`,
		`{"agentRef":"agent","task":"task","backend":"job","provider":"deepseek","model":"deepseek-chat","cellnSelection":{"toolRefs":[]}}`,
		`{"agentRef":"agent","task":"task","backend":"celln","model":"deepseek-chat","cellnSelection":{"toolRefs":[]}}`,
		`{"agentRef":"agent","task":"task","backend":"celln","provider":"deepseek","cellnSelection":{"toolRefs":[]}}`,
		`{"agentRef":"agent","task":"task","backend":"celln","provider":"deepseek","model":"deepseek-chat","runtimeRef":"ambiguous","cellnSelection":{"toolRefs":[]}}`,
		`{"agentRef":"agent","task":"task","backend":"celln","provider":"deepseek","model":"deepseek-chat","cellnSelection":{}}`,
	} {
		srv := NewServer(nil, nil, nil, logr.Discard())
		response := httptest.NewRecorder()
		srv.Handler(nil).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/runs", bytes.NewBufferString(body)))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("ambiguous catalogue accepted: %d", response.Code)
		}
	}
}

func TestCreateCatalogueRunRefusesInheritedIntegrations(t *testing.T) {
	for _, integration := range []string{"skill", "mcp"} {
		t.Run(integration, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			agent := &sympoziumv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "default"}}
			if integration == "skill" {
				agent.Spec.Skills = []sympoziumv1alpha1.SkillRef{{SkillPackRef: "k8s-ops"}}
			} else {
				agent.Spec.MCPServers = []sympoziumv1alpha1.MCPServerRef{{Name: "kubernetes"}}
			}
			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(agent).Build()
			srv := NewServer(cl, nil, nil, logr.Discard())
			response := httptest.NewRecorder()
			srv.Handler(nil).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/runs", strings.NewReader(`{"agentRef":"agent","task":"inspect cluster","backend":"celln","provider":"deepseek","model":"deepseek-chat","cellnSelection":{"toolRefs":[]}}`)))
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "SkillPacks or MCP") {
				t.Fatalf("unexpected response: %d %s", response.Code, response.Body.String())
			}
			var list sympoziumv1alpha1.AgentRunList
			if err := cl.List(t.Context(), &list); err != nil {
				t.Fatal(err)
			}
			if len(list.Items) != 0 {
				t.Fatal("incompatible run persisted")
			}
		})
	}
}
