package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnscoped"
	"github.com/sympozium-ai/sympozium/internal/modelkey"
)

// One key per Agent: a run may use only a key its own Agent owns, whatever
// the run names, and its sub-agents (runs of the same Agent) share that key.
func TestRunsUseOnlyTheirAgentsOwnKey(t *testing.T) {
	keySecret := func(name, owner string) *corev1.Secret {
		s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}, Data: map[string][]byte{"OPENAI_API_KEY": []byte("k")}}
		if owner != "" {
			s.Annotations = map[string]string{modelkey.OwnerAnnotation: owner}
		}
		return s
	}
	other := &sympoziumv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "other-agent", Namespace: "default"}}
	for _, tc := range []struct {
		name      string
		grants    string
		secret    *corev1.Secret
		parent    bool
		wantError string
	}{
		{name: "own granted key is claimed and used", grants: "own-key", secret: keySecret("own-key", "")},
		{name: "a sub-agent run shares its parent's key", grants: "own-key", secret: keySecret("own-key", "Agent/my-instance"), parent: true},
		{name: "another Agent's key is refused even if granted", grants: "shared", secret: keySecret("shared", "Agent/other-agent"), wantError: "belongs to Agent/other-agent"},
		{name: "an ungranted key is refused", secret: keySecret("someone-elses", ""), wantError: "is not declared in Agent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agent := parityAgent()
			if tc.grants != "" {
				agent.Spec.AuthRefs = []sympoziumv1alpha1.SecretRef{{Provider: "openai", Secret: tc.grants}}
			}
			run := parityRun()
			run.Spec.Model.AuthSecretRef = tc.secret.Name
			if tc.parent {
				run.Spec.Parent = &sympoziumv1alpha1.ParentRunRef{RunName: "parent-run", SpawnDepth: 1}
			}
			r := newAgentRunTestReconciler(t, agent, other, tc.secret, run)
			if _, err := r.reconcilePending(context.Background(), logr.Discard(), run); err != nil {
				t.Fatalf("reconcilePending: %v", err)
			}
			var job batchv1.Job
			jobErr := r.Get(context.Background(), client.ObjectKeyFromObject(run), &job)
			var stored sympoziumv1alpha1.AgentRun
			if err := r.Get(context.Background(), client.ObjectKeyFromObject(run), &stored); err != nil {
				t.Fatal(err)
			}
			if tc.wantError != "" {
				if jobErr == nil {
					t.Fatal("a pod was created with a key the Agent does not own")
				}
				if !strings.Contains(stored.Status.Error, tc.wantError) {
					t.Fatalf("status error %q, want %q", stored.Status.Error, tc.wantError)
				}
				return
			}
			if jobErr != nil {
				t.Fatalf("Job not created: %v (status %q)", jobErr, stored.Status.Error)
			}
			var secret corev1.Secret
			if err := r.Get(context.Background(), client.ObjectKeyFromObject(tc.secret), &secret); err != nil {
				t.Fatal(err)
			}
			if got := secret.Annotations[modelkey.OwnerAnnotation]; got != "Agent/my-instance" {
				t.Fatalf("key owner = %q, want Agent/my-instance", got)
			}
		})
	}
}

// A mediated (scoped) Celln run claims its Agent's key before the gateway is
// asked to serve it, and is refused before any preparation when the key
// belongs to another Agent. The scoped path used to skip this entirely.
func TestScopedRunsClaimTheirAgentsKey(t *testing.T) {
	keyFor := func(owner string) *corev1.Secret {
		s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "key", Namespace: "default"}, Data: map[string][]byte{"OPENAI_API_KEY": []byte("k")}}
		if owner != "" {
			s.Annotations = map[string]string{modelkey.OwnerAnnotation: owner}
		}
		return s
	}
	connection := &sympoziumv1alpha1.ModelConnection{ObjectMeta: metav1.ObjectMeta{Name: "conn", Namespace: "default"}, Spec: sympoziumv1alpha1.ModelConnectionSpec{Provider: "deepseek", Protocol: "openai-chat", Endpoint: "https://api.deepseek.com/chat/completions", SecretRef: "key", Models: []string{"deepseek-chat"}}}
	agent := parityAgent()
	agent.Spec.AuthRefs = []sympoziumv1alpha1.SecretRef{{Provider: "deepseek", Secret: "key"}}
	run := parityRun()
	run.Spec.Model = sympoziumv1alpha1.ModelSpec{ConnectionRef: "conn", Model: "deepseek-chat"}

	// The shared helper claims an unowned key the Agent grants.
	r := newAgentRunTestReconciler(t, agent, connection, keyFor(""), run)
	if refusal, err := r.authorizeRunKeys(context.Background(), run, agent); err != nil || refusal != "" {
		t.Fatalf("claim: refusal %q err %v", refusal, err)
	}
	var claimed corev1.Secret
	if err := r.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "key"}, &claimed); err != nil || claimed.Annotations[modelkey.OwnerAnnotation] != "Agent/my-instance" {
		t.Fatalf("key not claimed through the connection: %v %v", claimed.Annotations, err)
	}

	// The scoped path refuses another Agent's key before preparing anything.
	other := &sympoziumv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "default"}}
	run = parityRun()
	run.Spec.Model = sympoziumv1alpha1.ModelSpec{ConnectionRef: "conn", Model: "deepseek-chat"}
	r = newAgentRunTestReconciler(t, agent, other, connection, keyFor("Agent/other"), run)
	r.ScopedDispatcher = &cellnscoped.Dispatcher{}
	run.Spec.Backend = "celln"
	run.Spec.ExecutionLifecycle = "enduring"
	run.Spec.CellnSelection = &sympoziumv1alpha1.CellnCatalogueSelection{RuntimeRef: "celln-native", ToolRefs: []sympoziumv1alpha1.CellnCatalogueToolRef{}}
	if err := r.Update(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if !scopedCatalogueSelected(run) {
		t.Fatal("test run is not a scoped selection")
	}
	if _, err := r.reconcilePendingScoped(context.Background(), logr.Discard(), run); err != nil {
		t.Fatal(err)
	}
	var stored sympoziumv1alpha1.AgentRun
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(run), &stored); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stored.Status.Error, "belongs to Agent/other") {
		t.Fatalf("scoped run with another Agent's key: phase %q error %q", stored.Status.Phase, stored.Status.Error)
	}
}
