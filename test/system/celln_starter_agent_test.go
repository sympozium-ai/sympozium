//go:build system

package system_test

import (
	"errors"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellninstall"
	"github.com/sympozium-ai/sympozium/internal/modelkey"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// The installer's key becomes one Agent's own key: the starter objects pass
// the API server's schemas, the key is that Agent's alone, and a rerun
// changes nothing.
func TestStarterAgentOwnsTheInstallerKey(t *testing.T) {
	ns := createTestNamespace(t)
	model, err := cellninstall.FleetModel{Provider: cellninstall.ModelProviderOpenAI, Name: "gpt-test"}.Resolve("starter")
	if err != nil {
		t.Fatal(err)
	}
	o := cellninstall.StarterAgentOptions{Namespace: ns, Backend: cellninstall.FleetBackend{Name: "native", Model: model}, Credential: "sk-system-journey-0123456789abcdef", Runtime: "celln-native"}
	created, err := cellninstall.EnsureStarterAgent(testCtx, k8sClient, o)
	if err != nil || len(created) != 3 {
		t.Fatalf("created %v: %v", created, err)
	}
	if created, err := cellninstall.EnsureStarterAgent(testCtx, k8sClient, o); err != nil || len(created) != 0 {
		t.Fatalf("rerun created %v: %v", created, err)
	}
	var starter api.Agent
	if err := k8sClient.Get(testCtx, types.NamespacedName{Namespace: ns, Name: cellninstall.StarterAgentName}, &starter); err != nil {
		t.Fatal(err)
	}
	var connection api.ModelConnection
	if err := k8sClient.Get(testCtx, types.NamespacedName{Namespace: ns, Name: "starter"}, &connection); err != nil || connection.Spec.SecretRef != "starter-model-key" {
		t.Fatalf("connection %+v: %v", connection.Spec, err)
	}
	var secret corev1.Secret
	if err := k8sClient.Get(testCtx, types.NamespacedName{Namespace: ns, Name: "starter-model-key"}, &secret); err != nil || !modelkey.OwnedBy(&secret, &starter) {
		t.Fatalf("secret owner %v: %v", secret.Annotations, err)
	}
	if err := modelkey.Check(testCtx, k8sClient, &starter, "openai", "starter-model-key"); err != nil {
		t.Fatalf("the starter Agent may not use its own key: %v", err)
	}
	// Another Agent that names the same Secret is refused: one key per Agent.
	other := &api.Agent{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "other"}, Spec: api.AgentSpec{Agents: api.AgentsSpec{Default: api.AgentConfig{Model: "gpt-test"}}, AuthRefs: []api.SecretRef{{Provider: "openai", Secret: "starter-model-key"}}}}
	if err := k8sClient.Create(testCtx, other); err != nil {
		t.Fatal(err)
	}
	var conflict *modelkey.ConflictError
	if err := modelkey.Check(testCtx, k8sClient, other, "openai", "starter-model-key"); !errors.As(err, &conflict) {
		t.Fatalf("another Agent may use the starter key: %v", err)
	}
}
