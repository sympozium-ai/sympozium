package apiserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// A shared-catalogue run may select an Agent's own Secret-backed connection:
// it is frozen from the connection without any credential reference. A legacy
// namespaced selection has no gateway and keeps requiring a host profile.
func TestCreateRunOnAnAgentsOwnSecretConnection(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = sympoziumv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	agent := &sympoziumv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "mine"}, Spec: sympoziumv1alpha1.AgentSpec{RuntimeRef: "celln-native", AuthRefs: []sympoziumv1alpha1.SecretRef{{Provider: "anthropic", Secret: "my-anthropic-key"}}}}
	connection := &sympoziumv1alpha1.ModelConnection{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "my-anthropic", UID: "connection-uid"}, Spec: sympoziumv1alpha1.ModelConnectionSpec{Provider: "anthropic", Protocol: "anthropic-messages", Endpoint: "https://api.anthropic.com/v1/messages", SecretRef: "my-anthropic-key", Models: []string{"claude"}, MaxOutputTokens: 4096}}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(agent, connection).Build()
	srv := NewServer(cl, nil, nil, logr.Discard())
	post := func(selection string) *httptest.ResponseRecorder {
		res := httptest.NewRecorder()
		body := `{"agentRef":"mine","task":"hello","backend":"celln","executionLifecycle":"enduring","enduring":{"leaseSeconds":600,"maxTurns":2,"maxModelRequests":12,"maxOutputTokens":49152},"model":"claude","modelConnectionRef":"my-anthropic","cellnSelection":` + selection + `}`
		srv.Handler(nil).ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/api/v1/runs?namespace=team-a", strings.NewReader(body)))
		return res
	}
	res := post(`{"runtimeRef":"celln-native","toolRefs":[],"clusterToolRefs":[{"name":"celln-trial-workspace-read","revision":"v1"}]}`)
	var run sympoziumv1alpha1.AgentRun
	if res.Code != http.StatusCreated || json.Unmarshal(res.Body.Bytes(), &run) != nil {
		t.Fatalf("mediated run refused: %d %s", res.Code, res.Body.String())
	}
	model := run.Spec.Model
	if model.ConnectionRef != "my-anthropic" || model.Provider != "anthropic" || model.Protocol != "anthropic-messages" || model.BaseURL != connection.Spec.Endpoint || model.ConnectionRevision == "" {
		t.Fatalf("route not frozen from the connection: %+v", model)
	}
	if model.AuthSecretRef != "" || model.CredentialProfile != "" || len(run.Spec.Env) != 0 || strings.Contains(res.Body.String(), "my-anthropic-key") {
		t.Fatalf("the Secret reached the run: %s", res.Body.String())
	}
	if res := post(`{"runtimeRef":"celln-native","toolRefs":[{"name":"workspace-read","revision":"v1"}]}`); res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "host credential profile") {
		t.Fatalf("legacy namespaced selection accepted a Secret connection: %d %s", res.Code, res.Body.String())
	}
}
