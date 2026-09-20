//go:build system

package system_test

import (
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Exercise the generated CEL rule on an API server, not a fake client.
func TestCellnKeylessRouteCRD(t *testing.T) {
	for _, tc := range []struct {
		name, auth        string
		insecure, allowed bool
	}{
		{"keyless-approved", "none", true, true},
		{"keyless-unapproved", "none", false, false},
		{"secret-insecure", "secret", true, false},
		{"secret-unapproved", "secret", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := &api.CellnExecutionPolicy{ObjectMeta: metav1.ObjectMeta{GenerateName: "keyless-test-"}, Spec: api.CellnExecutionPolicySpec{
				NamespaceSelector: metav1.LabelSelector{},
				RuntimeProfiles:   []api.CellnExecutionPolicyRuntime{{Ref: api.CellnRuntimeProfileRef{Name: "starter", Revision: "v1"}}},
				Tools:             []api.CellnExecutionPolicyTool{}, Lifecycles: []string{"enduring"},
				Routes:   []api.CellnExecutionPolicyRoute{{Provider: "llama-server", Protocol: "openai-chat", Models: []string{"local"}, EndpointOrigins: []string{"http://192.168.1.237:8080"}, Auth: tc.auth, AllowInsecure: tc.insecure}},
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
