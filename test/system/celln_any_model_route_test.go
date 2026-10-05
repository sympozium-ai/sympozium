//go:build system

package system_test

import (
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The any-model token is a route's only model and never a host-profile
// route's, enforced by the generated CEL rule on a real API server.
func TestCellnAnyModelRouteCRD(t *testing.T) {
	for _, tc := range []struct {
		name, auth string
		models     []string
		allowed    bool
	}{
		{"secret-any-model", "secret", []string{"*"}, true},
		{"secret-exact-models", "secret", []string{"gpt-5", "gpt-4o"}, true},
		{"secret-token-beside-a-name", "secret", []string{"*", "gpt-5"}, false},
		{"host-profile-any-model", "host-profile", []string{"*"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := &api.CellnExecutionPolicy{ObjectMeta: metav1.ObjectMeta{GenerateName: "any-model-test-"}, Spec: api.CellnExecutionPolicySpec{
				NamespaceSelector: metav1.LabelSelector{},
				RuntimeProfiles:   []api.CellnExecutionPolicyRuntime{{Ref: api.CellnRuntimeProfileRef{Name: "starter", Revision: "v1"}}},
				Tools:             []api.CellnExecutionPolicyTool{}, Lifecycles: []string{"enduring"},
				Routes:   []api.CellnExecutionPolicyRoute{{Provider: "openai", Protocol: "openai-chat", Models: tc.models, EndpointOrigins: []string{"https://api.openai.com"}, Auth: tc.auth}},
				Ceilings: api.CellnExecutionPolicyCeilings{MaxTurns: 2, MaxModelRequests: 12, MaxOutputTokens: 6144, MaxParentLeaseSeconds: 600, MaxTurnSeconds: 60},
			}}
			err := k8sClient.Create(testCtx, policy)
			if tc.allowed {
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = k8sClient.Delete(testCtx, policy) })
			} else if !apierrors.IsInvalid(err) {
				t.Fatalf("expected schema refusal, got %v", err)
			}
		})
	}
}
