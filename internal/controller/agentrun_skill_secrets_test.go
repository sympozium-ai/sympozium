package controller

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

func TestWithoutKeyAccessDropsOnlyKeyReachingCoreResources(t *testing.T) {
	got, err := withoutKeyAccess([]sympoziumv1alpha1.RBACRule{
		{APIGroups: []string{""}, Resources: []string{"pods", "pods/log", "pods/exec", "pods/attach", "secrets", "configmaps"}, Verbs: []string{"get"}},
		{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"get"}},
		{APIGroups: []string{"apps"}, Resources: []string{"deployments"}, Verbs: []string{"get"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []sympoziumv1alpha1.RBACRule{
		{APIGroups: []string{""}, Resources: []string{"pods", "pods/log", "configmaps"}, Verbs: []string{"get"}},
		{APIGroups: []string{"apps"}, Resources: []string{"deployments"}, Verbs: []string{"get"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	for _, resource := range []string{"*", "pods/*"} {
		if _, err := withoutKeyAccess([]sympoziumv1alpha1.RBACRule{{APIGroups: []string{"*"}, Resources: []string{resource}, Verbs: []string{"get"}}}); err == nil {
			t.Fatalf("%q cannot be narrowed and must be refused", resource)
		}
	}
}

// Skills lose Secret, exec and attach access unless the Agent's policy opts
// in, and only opted-in runs get the account prefix the admission policy
// exempts.
func TestSkillSecretAccessFollowsThePolicy(t *testing.T) {
	sidecar := resolvedSidecar{skillPackName: "k8s-ops", sidecar: sympoziumv1alpha1.SkillSidecar{
		RBAC:        []sympoziumv1alpha1.RBACRule{{APIGroups: []string{""}, Resources: []string{"pods", "secrets", "pods/exec"}, Verbs: []string{"get"}}},
		ClusterRBAC: []sympoziumv1alpha1.RBACRule{{APIGroups: []string{""}, Resources: []string{"nodes", "secrets"}, Verbs: []string{"get"}}},
	}}
	for _, allow := range []bool{false, true} {
		agent := parityAgent()
		agent.Spec.PolicyRef = "policy"
		policy := &sympoziumv1alpha1.SympoziumPolicy{ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: "default"}}
		if allow {
			policy.Spec.SkillPolicy = &sympoziumv1alpha1.SkillPolicySpec{AllowSecretAccess: true}
		}
		run := parityRun()
		r := newAgentRunTestReconciler(t, agent, policy, run)
		ctx := context.Background()
		if err := r.ensureAgentServiceAccount(ctx, run); err != nil {
			t.Fatal(err)
		}
		wantPrefix := restrictedRunAccountPrefix
		if allow {
			wantPrefix = trustedRunAccountPrefix
		}
		if !strings.HasPrefix(run.Status.ServiceAccountName, wantPrefix) || agentRunServiceAccountName(run) != run.Status.ServiceAccountName {
			t.Fatalf("allow=%v: service account %q, want prefix %q", allow, run.Status.ServiceAccountName, wantPrefix)
		}
		if err := r.ensureSkillRBAC(ctx, logr.Discard(), run, []resolvedSidecar{sidecar}); err != nil {
			t.Fatal(err)
		}
		var role rbacv1.Role
		if err := r.Get(ctx, client.ObjectKey{Namespace: "default", Name: "sympozium-skill-k8s-ops-" + run.Name}, &role); err != nil {
			t.Fatal(err)
		}
		var clusterRole rbacv1.ClusterRole
		if err := r.Get(ctx, client.ObjectKey{Name: "sympozium-skill-k8s-ops-" + run.Name}, &clusterRole); err != nil {
			t.Fatal(err)
		}
		granted := strings.Join(append(role.Rules[0].Resources, clusterRole.Rules[0].Resources...), ",")
		if hasSecrets := strings.Contains(granted, "secrets") || strings.Contains(granted, "pods/exec"); hasSecrets != allow {
			t.Fatalf("allow=%v: granted %s", allow, granted)
		}
	}
}
