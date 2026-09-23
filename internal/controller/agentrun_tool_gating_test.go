package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// A SympoziumPolicy's tool gating reaches the pod through the controller:
// nothing else applies it, because the webhook is validation-only.
func TestPolicyToolGatingReachesAgentContainer(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	agent := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "gated", Namespace: "default"},
		Spec:       sympoziumv1alpha1.AgentSpec{PolicyRef: "restrictive"},
	}
	policy := &sympoziumv1alpha1.SympoziumPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "restrictive", Namespace: "default"},
		Spec: sympoziumv1alpha1.SympoziumPolicySpec{ToolGating: &sympoziumv1alpha1.ToolGatingSpec{
			DefaultAction: "deny",
			Rules: []sympoziumv1alpha1.ToolGatingRule{
				{Tool: "read_file", Action: "allow"},
				{Tool: "execute_command", Action: "deny"},
			},
		}},
	}
	r := &AgentRunReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(agent, policy).Build(),
		Scheme: scheme, Log: logr.Discard(),
	}
	run := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run", Namespace: "default"},
		Spec:       sympoziumv1alpha1.AgentRunSpec{AgentRef: "gated"},
	}

	podRun, err := r.withPolicyToolGating(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	template, err := r.buildAgentPodTemplate(context.Background(), podRun, false, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{}
	for _, e := range template.Spec.Containers[0].Env {
		env[e.Name] = e.Value
	}
	if env["TOOL_POLICY_ALLOW"] != "read_file" || !strings.Contains(env["TOOL_POLICY_DENY"], "execute_command") {
		t.Fatalf("agent container policy env = allow %q deny %q; want the policy's rules", env["TOOL_POLICY_ALLOW"], env["TOOL_POLICY_DENY"])
	}
	if run.Spec.ToolPolicy != nil {
		t.Fatalf("stored run was modified: %+v", run.Spec.ToolPolicy)
	}
}
