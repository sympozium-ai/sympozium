package webhook

import (
	"context"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/modelkey"
)

// A run the harness gateway mediates sends no key from its pod, so admission
// does not judge its baseURL by the key's pin (the controller holds it to the
// operator's profile upstream instead). Only the Agent's policy makes a run
// mediated.

func mediationEnforcer(t *testing.T, mediation, pinned string) *PolicyEnforcer {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	agent := &sympoziumv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "inst", Namespace: "default"},
		Spec: sympoziumv1alpha1.AgentSpec{PolicyRef: "p", AuthRefs: []sympoziumv1alpha1.SecretRef{
			{Provider: "openrouter", Secret: "or-key"}, {Provider: "llama-server", Secret: "llama-key"},
		}}}
	policy := &sympoziumv1alpha1.SympoziumPolicy{ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "default"},
		Spec: sympoziumv1alpha1.SympoziumPolicySpec{HarnessPolicy: &sympoziumv1alpha1.HarnessPolicySpec{
			Enabled: true, AllowUnmetered: true, ModelMediation: mediation,
		}}}
	orKey := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "or-key", Namespace: "default"}, Data: map[string][]byte{"OPENAI_API_KEY": []byte("sk-or-DUMMY")}}
	if pinned != "" {
		orKey.Annotations = map[string]string{modelkey.OriginAnnotation: pinned}
	}
	llamaKey := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "llama-key", Namespace: "default"}, Data: map[string][]byte{"OPENAI_API_KEY": []byte("sk-DUMMY")}}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(agent, policy, orKey, llamaKey).Build()
	return &PolicyEnforcer{Client: cl, Log: logr.Discard(), Decoder: decoderFor(t, scheme)}
}

func mediationRun(provider, secret, baseURL string) *sympoziumv1alpha1.AgentRun {
	run := capabilityRun(harnessTaskSpec(nil))
	run.Spec.Model = sympoziumv1alpha1.ModelSpec{Provider: provider, Model: "m", AuthSecretRef: secret, BaseURL: baseURL}
	return run
}

func TestAdmissionAllowsAMediatedOpenRouterRunWithNoBaseURL(t *testing.T) {
	run := mediationRun("openrouter", "or-key", "")
	if resp := mediationEnforcer(t, sympoziumv1alpha1.ModelMediationRequired, "https://openrouter.ai").Handle(context.Background(), admissionRequestFor(t, run)); !resp.Allowed {
		t.Fatalf("mediated run denied: %s", resp.Result.Message)
	}
	// Unmediated, the runner's own default for openrouter is its pinned origin.
	if resp := mediationEnforcer(t, sympoziumv1alpha1.ModelMediationOff, "https://openrouter.ai").Handle(context.Background(), admissionRequestFor(t, run)); !resp.Allowed {
		t.Fatalf("unmediated openrouter run denied: %s", resp.Result.Message)
	}
}

func TestAdmissionAllowsAMediatedCustomProfileRunToReachItsFirstUsePin(t *testing.T) {
	run := mediationRun("llama-server", "llama-key", "")
	if resp := mediationEnforcer(t, sympoziumv1alpha1.ModelMediationRequired, "").Handle(context.Background(), admissionRequestFor(t, run)); !resp.Allowed {
		t.Fatalf("mediated run denied: %s", resp.Result.Message)
	}
}

func TestAdmissionStillRefusesAnUnmediatedUnpinnedKeyToACustomEndpoint(t *testing.T) {
	run := mediationRun("llama-server", "llama-key", "http://llama.local:8080/v1")
	resp := mediationEnforcer(t, sympoziumv1alpha1.ModelMediationOff, "").Handle(context.Background(), admissionRequestFor(t, run))
	if resp.Allowed || !strings.Contains(resp.Result.Message, "may not be sent to http://llama.local:8080") {
		t.Fatalf("allowed=%v %q", resp.Allowed, resp.Result.Message)
	}
	// Not a harness: its runner pod holds the key, policy or not.
	run.Spec.Task = sympoziumv1alpha1.NewStringTask("hi")
	if resp := mediationEnforcer(t, sympoziumv1alpha1.ModelMediationRequired, "").Handle(context.Background(), admissionRequestFor(t, run)); resp.Allowed {
		t.Fatal("a non-harness run was admitted as mediated")
	}
}

func TestAdmissionDoesNotLetARunClaimMediation(t *testing.T) {
	run := mediationRun("llama-server", "llama-key", "http://llama.local:8080/v1")
	run.Annotations = map[string]string{"sympozium.ai/model-mediation": "required"}
	run.Labels = map[string]string{"sympozium.ai/model-mediated": "true"}
	resp := mediationEnforcer(t, sympoziumv1alpha1.ModelMediationOff, "").Handle(context.Background(), admissionRequestFor(t, run))
	if resp.Allowed || !strings.Contains(resp.Result.Message, "may not be sent to http://llama.local:8080") {
		t.Fatalf("a run that claims mediation: allowed=%v %q", resp.Allowed, resp.Result.Message)
	}
	run.Spec.Env = map[string]string{"SYMPOZIUM_MODEL_MEDIATED": "true"}
	if resp := mediationEnforcer(t, sympoziumv1alpha1.ModelMediationOff, "").Handle(context.Background(), admissionRequestFor(t, run)); resp.Allowed {
		t.Fatal("a run that claims mediation in spec.env was admitted")
	}
	// A scoped Celln selection is never treated as mediated.
	run = mediationRun("llama-server", "llama-key", "http://llama.local:8080/v1")
	run.Spec.Backend = "celln"
	run.Spec.CellnSelection = &sympoziumv1alpha1.CellnCatalogueSelection{}
	pe := mediationEnforcer(t, sympoziumv1alpha1.ModelMediationRequired, "")
	agent := &sympoziumv1alpha1.Agent{}
	if err := pe.Client.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "inst"}, agent); err != nil {
		t.Fatal(err)
	}
	if pe.modelKeyMediated(context.Background(), run, agent) {
		t.Fatal("a scoped Celln run counted as mediated")
	}
}
