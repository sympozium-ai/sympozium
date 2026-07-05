package controller

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

func TestResolveAgentDisplayName(t *testing.T) {
	withDisplay := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "researcher-1"},
		Spec:       sympoziumv1alpha1.AgentSpec{DisplayName: "Research Assistant"},
	}
	if got := resolveAgentDisplayName(withDisplay); got != "Research Assistant" {
		t.Errorf("resolveAgentDisplayName = %q, want %q", got, "Research Assistant")
	}

	noDisplay := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "researcher-1"},
	}
	if got := resolveAgentDisplayName(noDisplay); got != "researcher-1" {
		t.Errorf("resolveAgentDisplayName fallback = %q, want %q", got, "researcher-1")
	}

	whitespaceDisplay := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "researcher-1"},
		Spec:       sympoziumv1alpha1.AgentSpec{DisplayName: "   "},
	}
	if got := resolveAgentDisplayName(whitespaceDisplay); got != "researcher-1" {
		t.Errorf("resolveAgentDisplayName whitespace = %q, want fallback %q", got, "researcher-1")
	}
}

func TestDisplayNameForReply(t *testing.T) {
	withAnnotation := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{"sympozium.ai/agent-display-name": "Research Assistant"},
		},
		Spec: sympoziumv1alpha1.AgentRunSpec{AgentRef: "researcher-1"},
	}
	if got := displayNameForReply(withAnnotation); got != "Research Assistant" {
		t.Errorf("displayNameForReply = %q, want %q", got, "Research Assistant")
	}

	noAnnotation := &sympoziumv1alpha1.AgentRun{
		Spec: sympoziumv1alpha1.AgentRunSpec{AgentRef: "researcher-1"},
	}
	if got := displayNameForReply(noAnnotation); got != "researcher-1" {
		t.Errorf("displayNameForReply fallback = %q, want %q", got, "researcher-1")
	}
}

// TestBuildContainers_AgentDisplayNameEnv verifies the ipc-bridge container gets
// AGENT_DISPLAY_NAME from the run annotation (so the bridge validates outbound
// attribution against the human display name), and omits it when absent.
func TestBuildContainers_AgentDisplayNameEnv(t *testing.T) {
	r := &AgentRunReconciler{}

	// With the annotation present.
	run := newTestRun()
	run.Annotations = map[string]string{"sympozium.ai/agent-display-name": "Research Assistant"}
	cs, _ := r.buildContainers(run, false, nil, nil, nil)
	if cs[1].Name != "ipc-bridge" {
		t.Fatalf("cs[1] = %q, want ipc-bridge", cs[1].Name)
	}
	if got, ok := findEnv(cs[1].Env, "AGENT_DISPLAY_NAME"); !ok || got != "Research Assistant" {
		t.Errorf("ipc-bridge AGENT_DISPLAY_NAME = %q (ok=%v), want %q", got, ok, "Research Assistant")
	}

	// Without the annotation, the env must be omitted (bridge falls back to INSTANCE_NAME).
	plain := newTestRun()
	cs2, _ := r.buildContainers(plain, false, nil, nil, nil)
	if _, ok := findEnv(cs2[1].Env, "AGENT_DISPLAY_NAME"); ok {
		t.Error("AGENT_DISPLAY_NAME should be absent when the run has no display-name annotation")
	}
}
