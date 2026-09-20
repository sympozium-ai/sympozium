package apiserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnplatform"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCellnPlatformProfilesAndWrappersFollowTheNamespacePolicy(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = sympoziumv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	raw := func(s string) apiextensionsv1.JSON { return apiextensionsv1.JSON{Raw: []byte(s)} }
	profile := &sympoziumv1alpha1.CellnRuntimeProfile{ObjectMeta: metav1.ObjectMeta{Name: "celln-native-trial"}, Spec: sympoziumv1alpha1.CellnRuntimeProfileSpec{Revision: "v1", Native: &sympoziumv1alpha1.CellnNativeProvisioning{CredentialProfile: "trial", SystemPrompt: "host persona", Template: raw(`{"model":"deepseek-chat","url":"https://api.deepseek.com/chat/completions"}`)}}}
	// A second backend of the same scope: its profile is labeled with the
	// backend name and gets its own wrapper names.
	local := &sympoziumv1alpha1.CellnRuntimeProfile{ObjectMeta: metav1.ObjectMeta{Name: "celln-native-trial-local", Labels: map[string]string{cellnplatform.BackendLabel: "local"}}, Spec: sympoziumv1alpha1.CellnRuntimeProfileSpec{Revision: "v1", Native: &sympoziumv1alpha1.CellnNativeProvisioning{TurnModelRequests: 6, TurnOutputTokens: 24576, CredentialProfile: "trial-local", SystemPrompt: "host persona", Template: raw(`{"model":"qwen.gguf","url":"http://100.81.163.75:8080/v1/chat/completions","allow_insecure":true}`)}}}
	policy := &sympoziumv1alpha1.CellnExecutionPolicy{ObjectMeta: metav1.ObjectMeta{Name: "celln-fleet-trial"}, Spec: sympoziumv1alpha1.CellnExecutionPolicySpec{
		NamespaceSelector: cellnplatform.OpenSelector(cellnplatform.SystemNamespaces("sympozium-system")),
		RuntimeProfiles:   []sympoziumv1alpha1.CellnExecutionPolicyRuntime{{Ref: sympoziumv1alpha1.CellnRuntimeProfileRef{Name: profile.Name, Revision: "v1"}}, {Ref: sympoziumv1alpha1.CellnRuntimeProfileRef{Name: local.Name, Revision: "v1"}}},
		Tools:             []sympoziumv1alpha1.CellnExecutionPolicyTool{{Ref: sympoziumv1alpha1.ClusterCellnToolRef{Name: "celln-trial-workspace-read", Revision: "v1"}}},
		Routes: []sympoziumv1alpha1.CellnExecutionPolicyRoute{
			{Provider: "deepseek", Protocol: "openai-chat", Models: []string{"deepseek-chat"}, EndpointOrigins: []string{"https://api.deepseek.com"}, Auth: "host-profile"},
			{Provider: "llama-server", Protocol: "openai-chat", Models: []string{"qwen.gguf"}, EndpointOrigins: []string{"http://100.81.163.75:8080"}, Auth: "host-profile", AllowInsecure: true},
		},
		Ceilings: sympoziumv1alpha1.CellnExecutionPolicyCeilings{MaxTurns: 256, MaxModelRequests: 1536, MaxOutputTokens: 786432, MaxParentLeaseSeconds: 86400, MaxTurnSeconds: 60},
	}}
	ns := func(name string) *corev1.Namespace {
		return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{cellnplatform.NamespaceNameLabel: name}}}
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(profile, local, policy, ns("team-a"), ns("sympozium-system")).Build()
	srv := NewServer(cl, nil, nil, logr.Discard())
	get := func(namespace string) []CellnPlatformProfile {
		t.Helper()
		res := httptest.NewRecorder()
		srv.Handler(nil).ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/v1/celln-platform/profiles?namespace="+namespace, nil))
		if res.Code != http.StatusOK {
			t.Fatalf("%s: status %d: %s", namespace, res.Code, res.Body.String())
		}
		var out []CellnPlatformProfile
		if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	got := get("team-a")
	byName := map[string]CellnPlatformProfile{}
	for _, p := range got {
		byName[p.Name] = p
	}
	if p := byName["celln-native-trial"]; len(got) != 2 || p.Provider != "deepseek" || p.Model != "deepseek-chat" || p.CredentialProfile != "trial" || p.Backend != "native" || p.Wrapper != "celln-native" || p.Agent != "celln-agent" || len(p.Tools) != 1 || p.Tools[0].Name != "celln-trial-workspace-read" || p.SystemPrompt != "host persona" || p.Ceilings.LeaseSeconds != 86400 || p.SessionDefaults.LeaseSeconds != 14400 || p.SessionDefaults.MaxTurns != 64 || p.SessionDefaults.MaxModelRequests != 384 || p.SessionDefaults.MaxOutputTokens != 196608 {
		t.Fatalf("tenant profiles: %+v", got)
	}
	if p := byName["celln-native-trial-local"]; p.Provider != "llama-server" || p.Model != "qwen.gguf" || p.CredentialProfile != "trial-local" || p.Backend != "local" || p.Wrapper != "celln-local" || p.Agent != "celln-agent-local" {
		t.Fatalf("second backend must be offered with its own wrapper names: %+v", p)
	}
	// The local backend allows 4096 output tokens per request, so its turns
	// reserve 24576: the shared ceilings pay for 32 of them, and its session
	// defaults are those 32 turns rather than 64 turns it could never take.
	if d := byName["celln-native-trial-local"].SessionDefaults; d.MaxTurns != 32 || d.MaxModelRequests != 192 || d.MaxOutputTokens != 786432 || d.LeaseSeconds != 14400 {
		t.Fatalf("session defaults must follow the profile's own per-turn allowance: %+v", d)
	}
	if got := get("sympozium-system"); len(got) != 0 {
		t.Fatalf("control-plane namespace offered profiles: %+v", got)
	}
	post := func(namespace, body string) *httptest.ResponseRecorder {
		res := httptest.NewRecorder()
		srv.Handler(nil).ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/api/v1/celln-platform/wrappers?namespace="+namespace, strings.NewReader(body)))
		return res
	}
	res := post("team-a", `{"profile":"celln-native-trial"}`)
	var wrappers cellnplatform.Wrappers
	if res.Code != http.StatusOK || json.Unmarshal(res.Body.Bytes(), &wrappers) != nil || wrappers.Connection != "celln-native" || len(wrappers.Created) != 3 {
		t.Fatalf("ensure: %d %s", res.Code, res.Body.String())
	}
	var connection sympoziumv1alpha1.ModelConnection
	if err := cl.Get(t.Context(), types.NamespacedName{Namespace: "team-a", Name: "celln-native"}, &connection); err != nil || connection.Spec.CredentialProfile != "trial" {
		t.Fatalf("wrapper connection: %v", err)
	}
	if res := post("team-a", `{"profile":"celln-native-trial"}`); res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"created":[]`) {
		t.Fatalf("second ensure: %d %s", res.Code, res.Body.String())
	}
	if res := post("sympozium-system", `{"profile":"celln-native-trial"}`); res.Code != http.StatusForbidden {
		t.Fatalf("excluded namespace prepared: %d %s", res.Code, res.Body.String())
	}
	if res := post("team-a", `{}`); res.Code != http.StatusBadRequest {
		t.Fatalf("empty profile accepted: %d", res.Code)
	}
	// A run on a fleet wrapper that names no persona gets the profile's bound
	// persona, so the API and UI never trip the plan's persona check.
	res = httptest.NewRecorder()
	body := `{"agentRef":"celln-agent","task":"Where is Botswana?","backend":"celln","executionLifecycle":"one-shot","model":"deepseek-chat","modelConnectionRef":"celln-native","cellnSelection":{"runtimeRef":"celln-native","toolRefs":[],"clusterToolRefs":[{"name":"celln-trial-workspace-read","revision":"v1"}]}}`
	srv.Handler(nil).ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/api/v1/runs?namespace=team-a", strings.NewReader(body)))
	var run sympoziumv1alpha1.AgentRun
	if res.Code != http.StatusCreated && res.Code != http.StatusOK || json.Unmarshal(res.Body.Bytes(), &run) != nil {
		t.Fatalf("one-shot on a wrapper refused: %d %s", res.Code, res.Body.String())
	}
	if run.Spec.SystemPrompt != "host persona" || run.Spec.ExecutionLifecycle != "one-shot" || run.Spec.Enduring != nil || run.Spec.CellnSelection == nil || run.Spec.CellnSelection.RuntimeRef != "celln-native" {
		t.Fatalf("run did not inherit the profile's persona as a platform one-shot: %+v", run.Spec)
	}
}
