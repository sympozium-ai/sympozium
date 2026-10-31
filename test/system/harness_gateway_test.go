//go:build system

package system_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/harnessgateway"
)

// TestMediatedHarnessJobHoldsTheTokenAndNotTheKey runs the real controller
// against a real apiserver: the token Secret appears, the Job holds it and not
// the key, and the Secret goes when the run ends.
func TestMediatedHarnessJobHoldsTheTokenAndNotTheKey(t *testing.T) {
	ns := createTestNamespace(t)
	for _, obj := range []client.Object{
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: harnessGatewayCASecret, Namespace: harnessGatewayCANamespace},
			Data:       map[string][]byte{"ca.crt": []byte("ca"), "tls.key": []byte("PRIVATE")},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "sys-model-key", Namespace: ns},
			Data:       map[string][]byte{"ANTHROPIC_API_KEY": []byte("real-key")},
		},
		&sympoziumv1alpha1.SympoziumPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "sys-mediated", Namespace: ns},
			Spec: sympoziumv1alpha1.SympoziumPolicySpec{HarnessPolicy: &sympoziumv1alpha1.HarnessPolicySpec{
				Enabled: true, AllowUnmetered: true, ModelMediation: sympoziumv1alpha1.ModelMediationRequired,
			}},
		},
		&sympoziumv1alpha1.Agent{
			ObjectMeta: metav1.ObjectMeta{Name: "sys-mediated-agent", Namespace: ns},
			Spec: sympoziumv1alpha1.AgentSpec{
				PolicyRef: "sys-mediated",
				AuthRefs:  []sympoziumv1alpha1.SecretRef{{Provider: "anthropic", Secret: "sys-model-key"}},
				Agents:    sympoziumv1alpha1.AgentsSpec{Default: sympoziumv1alpha1.AgentConfig{Model: "m"}},
			},
		},
	} {
		if err := k8sClient.Create(testCtx, obj); err != nil && !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("create %T: %v", obj, err)
		}
	}

	var task sympoziumv1alpha1.TaskSpec
	if err := json.Unmarshal([]byte(`{"mode":"harness","parameters":{`+
		`"image":"registry.example/harness@sha256:`+strings.Repeat("a", 64)+`","prompt":"go"}}`), &task); err != nil {
		t.Fatalf("task: %v", err)
	}
	run := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "sys-run-mediated", Namespace: ns},
		Spec: sympoziumv1alpha1.AgentRunSpec{
			AgentRef: "sys-mediated-agent",
			AgentID:  "sys-mediated-agent",
			Task:     &task,
			Model:    sympoziumv1alpha1.ModelSpec{Provider: "anthropic", Model: "m", AuthSecretRef: "sys-model-key"},
		},
	}
	if err := k8sClient.Create(testCtx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(testCtx, run) })

	var job batchv1.Job
	pollUntil(t, 30*time.Second, 200*time.Millisecond, func() bool {
		var got sympoziumv1alpha1.AgentRun
		if k8sClient.Get(testCtx, client.ObjectKeyFromObject(run), &got) == nil && got.Status.Error != "" {
			t.Fatalf("run failed: %s", got.Status.Error)
		}
		return k8sClient.Get(testCtx, client.ObjectKey{Namespace: ns, Name: run.Name}, &job) == nil
	})

	tokenKey := client.ObjectKey{Namespace: ns, Name: harnessgateway.RunTokenSecretName(run.Name)}
	var token corev1.Secret
	if err := k8sClient.Get(testCtx, tokenKey, &token); err != nil {
		t.Fatalf("token Secret: %v", err)
	}
	if _, leaked := token.Data["tls.key"]; leaked || len(token.Data[harnessgateway.TokenSecretKey]) == 0 {
		t.Errorf("token Secret keys are wrong: %d keys", len(token.Data))
	}
	var sawToken bool
	for _, container := range job.Spec.Template.Spec.Containers {
		for _, env := range container.Env {
			if env.ValueFrom == nil || env.ValueFrom.SecretKeyRef == nil {
				continue
			}
			switch env.ValueFrom.SecretKeyRef.Name {
			case "sys-model-key":
				t.Errorf("container %s holds the real key as %s", container.Name, env.Name)
			case tokenKey.Name:
				sawToken = true
			}
		}
	}
	if !sawToken {
		t.Error("no container holds the run token")
	}

	var current sympoziumv1alpha1.AgentRun
	if err := k8sClient.Get(testCtx, client.ObjectKeyFromObject(run), &current); err != nil {
		t.Fatal(err)
	}
	current.Status.Phase = sympoziumv1alpha1.AgentRunPhaseSucceeded
	if err := k8sClient.Status().Update(testCtx, &current); err != nil {
		t.Fatalf("finish the run: %v", err)
	}
	pollUntil(t, 30*time.Second, 200*time.Millisecond, func() bool {
		var gone corev1.Secret
		return client.IgnoreNotFound(k8sClient.Get(testCtx, tokenKey, &gone)) == nil && gone.Name == ""
	})
}
