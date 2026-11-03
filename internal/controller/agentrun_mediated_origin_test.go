package controller

import (
	"context"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/harnessgateway"
	"github.com/sympozium-ai/sympozium/internal/modelkey"
)

// The origin check #707 added judges where a run's pod sends its key. A run the
// harness gateway mediates sends no key: these hold that such a run is judged by
// mediation (the operator's profile upstream and the key's pin to it), and that
// nothing a run sets can make it count as mediated.

const openRouterProfile = `  - name: openrouter
    providers: [openrouter]
    upstream: https://openrouter.ai/api
    secretKeys: [OPENAI_API_KEY, API_KEY]
    auth: {type: header, header: Authorization, prefix: "Bearer "}
    paths: [/v1/chat/completions]
    env: {baseURL: [OPENAI_BASE_URL], apiKey: [OPENAI_API_KEY]}
    clientBasePath: /v1
`

// mediatedOriginRun is mediatedRun with an openrouter profile added, the key granted for
// the run's provider only, the key pinned to pin ("" leaves it unpinned) and the
// policy's modelMediation set to mediation.
func mediatedOriginRun(t *testing.T, provider, baseURL, pin, mediation string) (*AgentRunReconciler, *sympoziumv1alpha1.AgentRun) {
	t.Helper()
	r, run := mediatedRun(t, "run-a", provider, baseURL)
	set, err := harnessgateway.ParseSet([]byte(mediationProfiles + openRouterProfile))
	if err != nil {
		t.Fatal(err)
	}
	r.HarnessGateway.Profiles = fixedProfiles{set}
	grantKey(t, r, run)
	ctx := context.Background()
	if pin != "" {
		key := mediatedKey(t, r)
		key.Annotations = map[string]string{modelkey.OriginAnnotation: pin}
		if err := r.Update(ctx, key); err != nil {
			t.Fatal(err)
		}
	}
	setMediation(t, r, mediation)
	return r, run
}

func setMediation(t *testing.T, r *AgentRunReconciler, mediation string) {
	t.Helper()
	ctx := context.Background()
	var policy sympoziumv1alpha1.SympoziumPolicy
	if err := r.Get(ctx, client.ObjectKey{Namespace: "default", Name: "mediated"}, &policy); err != nil {
		t.Fatal(err)
	}
	policy.Spec.HarnessPolicy.ModelMediation = mediation
	if err := r.Update(ctx, &policy); err != nil {
		t.Fatal(err)
	}
}

func mediatedKey(t *testing.T, r *AgentRunReconciler) *corev1.Secret {
	t.Helper()
	var key corev1.Secret
	if err := r.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "model-key"}, &key); err != nil {
		t.Fatal(err)
	}
	return &key
}

func originAgent(t *testing.T, r *AgentRunReconciler, run *sympoziumv1alpha1.AgentRun) *sympoziumv1alpha1.Agent {
	t.Helper()
	var agent sympoziumv1alpha1.Agent
	if err := r.Get(context.Background(), client.ObjectKey{Namespace: run.Namespace, Name: run.Spec.AgentRef}, &agent); err != nil {
		t.Fatal(err)
	}
	return &agent
}

func storeRun(t *testing.T, r *AgentRunReconciler, run *sympoziumv1alpha1.AgentRun) {
	t.Helper()
	if err := r.Update(context.Background(), run); err != nil {
		t.Fatal(err)
	}
}

func wantJob(t *testing.T, r *AgentRunReconciler, run *sympoziumv1alpha1.AgentRun) *batchv1.Job {
	t.Helper()
	var job batchv1.Job
	if err := r.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: run.Name}, &job); err != nil {
		t.Fatalf("no Job: %v", err)
	}
	for _, c := range job.Spec.Template.Spec.Containers {
		for _, e := range c.Env {
			if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil && e.ValueFrom.SecretKeyRef.Name == "model-key" {
				t.Errorf("container %s holds the model key in %s", c.Name, e.Name)
			}
		}
	}
	return &job
}

func wantNoJob(t *testing.T, r *AgentRunReconciler, run *sympoziumv1alpha1.AgentRun) {
	t.Helper()
	err := r.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: run.Name}, &batchv1.Job{})
	if !apierrors.IsNotFound(err) {
		t.Errorf("a Job exists: err = %v", err)
	}
}

// Steward's case: with no baseURL, RunOrigin is the runner default
// (api.openai.com), so #707 alone refuses a key pinned to openrouter.ai even
// though the gateway sends it only to the openrouter profile's upstream.
func TestAMediatedOpenRouterRunWithNoBaseURLIsAdmitted(t *testing.T) {
	r, run := mediatedOriginRun(t, "openrouter", "", "https://openrouter.ai", sympoziumv1alpha1.ModelMediationRequired)
	refusal, err := r.authorizeRunKeys(context.Background(), run, originAgent(t, r, run))
	if err != nil || refusal != nil {
		t.Fatalf("authorizeRunKeys: refusal = %+v err = %v", refusal, err)
	}
	got := reconcileMediated(t, r, run)
	if got.Status.Phase == sympoziumv1alpha1.AgentRunPhaseFailed {
		t.Fatalf("run failed: %s", got.Status.Error)
	}
	job := wantJob(t, r, run)
	if v := mediatedEnv(job.Spec.Template.Spec.Containers[0], "OPENAI_BASE_URL"); v == nil || !strings.HasPrefix(v.Value, mediationGatewayURL) {
		t.Errorf("OPENAI_BASE_URL = %+v, want the gateway", v)
	}
	if runToken(t, r, run) == nil {
		t.Error("no run token was minted")
	}
	if got := modelkey.PinnedOrigin(mediatedKey(t, r)); got != "https://openrouter.ai" {
		t.Errorf("pin = %q", got)
	}

	// Unmediated, the runner's own default for openrouter is the pinned origin.
	r, run = mediatedOriginRun(t, "openrouter", "", "https://openrouter.ai", sympoziumv1alpha1.ModelMediationOff)
	refusal, err = r.authorizeRunKeys(context.Background(), run, originAgent(t, r, run))
	if err != nil || refusal != nil {
		t.Fatalf("unmediated openrouter: refusal = %+v err = %v", refusal, err)
	}

	// A provider without a hosted default carries the key to OpenAI's
	// endpoint, which the pin refuses.
	r, run = mediatedOriginRun(t, "custom", "", "https://openrouter.ai", sympoziumv1alpha1.ModelMediationOff)
	refusal, err = r.authorizeRunKeys(context.Background(), run, originAgent(t, r, run))
	if err != nil || refusal == nil || refusal.Code != RefusalModelKeyOriginMismatch {
		t.Fatalf("unmediated custom: refusal = %+v err = %v, want %s", refusal, err, RefusalModelKeyOriginMismatch)
	}
}

// A custom profile's unpinned key is pinned by mediation to the profile's
// upstream on first use; #707's check must not refuse it first.
func TestAMediatedCustomProfileRunReachesTheFirstUsePin(t *testing.T) {
	r, run := mediatedOriginRun(t, "llama-server", "", "", sympoziumv1alpha1.ModelMediationRequired)
	refusal, err := r.authorizeRunKeys(context.Background(), run, originAgent(t, r, run))
	if err != nil || refusal != nil {
		t.Fatalf("authorizeRunKeys: refusal = %+v err = %v", refusal, err)
	}
	if got := modelkey.PinnedOrigin(mediatedKey(t, r)); got != "" {
		t.Fatalf("authorizeRunKeys pinned the key to %q; mediation pins it", got)
	}
	got := reconcileMediated(t, r, run)
	if got.Status.Phase == sympoziumv1alpha1.AgentRunPhaseFailed {
		t.Fatalf("run failed: %s", got.Status.Error)
	}
	wantJob(t, r, run)
	if got := modelkey.PinnedOrigin(mediatedKey(t, r)); got != "http://llama.local:8080" {
		t.Errorf("first-use pin = %q, want the profile upstream", got)
	}
}

// Mediation's own checks still apply: a baseURL off the profile's origin, or a
// key pinned elsewhere, is refused without a pin or a pod.
func TestAMediatedRunIsStillHeldToItsProfile(t *testing.T) {
	for name, tc := range map[string]struct{ provider, baseURL, pin string }{
		"baseURL off the profile":      {"llama-server", "http://listener.attacker.svc:443", ""},
		"key pinned to another origin": {"openrouter", "", "https://api.openai.com"},
	} {
		t.Run(name, func(t *testing.T) {
			r, run := mediatedOriginRun(t, tc.provider, tc.baseURL, tc.pin, sympoziumv1alpha1.ModelMediationRequired)
			got := reconcileMediated(t, r, run)
			if got.Status.Phase != sympoziumv1alpha1.AgentRunPhaseFailed {
				t.Fatalf("phase = %s, want Failed", got.Status.Phase)
			}
			wantNoJob(t, r, run)
			if got := modelkey.PinnedOrigin(mediatedKey(t, r)); got != tc.pin {
				t.Errorf("pin = %q, want %q", got, tc.pin)
			}
		})
	}
}

// Non-mediated paths are unchanged: an unpinned key may not go to a custom
// endpoint, whether mediation is off or the run is not a harness.
func TestAnUnmediatedRunWithAnUnpinnedKeyToACustomEndpointIsRefused(t *testing.T) {
	t.Run("policy off", func(t *testing.T) {
		r, run := mediatedOriginRun(t, "llama-server", "http://llama.local:8080/v1", "", sympoziumv1alpha1.ModelMediationOff)
		got := reconcileMediated(t, r, run)
		if got.Status.Phase != sympoziumv1alpha1.AgentRunPhaseFailed || !strings.Contains(got.Status.Error, "may not be sent to http://llama.local:8080") {
			t.Fatalf("phase = %s error = %q", got.Status.Phase, got.Status.Error)
		}
		wantNoJob(t, r, run)
		if got := modelkey.PinnedOrigin(mediatedKey(t, r)); got != "" {
			t.Errorf("the key was pinned to %q", got)
		}
	})
	t.Run("not a harness, under a policy that requires mediation", func(t *testing.T) {
		r, run := mediatedOriginRun(t, "llama-server", "http://llama.local:8080/v1", "", sympoziumv1alpha1.ModelMediationRequired)
		run.Spec.Task = sympoziumv1alpha1.NewStringTask("hi")
		refusal, err := r.authorizeRunKeys(context.Background(), run, originAgent(t, r, run))
		if err != nil || refusal == nil {
			t.Fatalf("refusal = %+v err = %v, want a refusal", refusal, err)
		}
	})
}

// Mediation is decided by the Agent's policy alone. Nothing the run author
// writes (env, annotations, labels) makes a run count as mediated, and a policy
// switched off after the check is caught when the pod is rendered.
func TestARunCannotClaimMediationToSkipTheOriginCheck(t *testing.T) {
	ctx := context.Background()
	t.Run("run-controlled fields", func(t *testing.T) {
		r, run := mediatedOriginRun(t, "llama-server", "http://llama.local:8080/v1", "", sympoziumv1alpha1.ModelMediationOff)
		run.Annotations = map[string]string{"sympozium.ai/model-mediation": "required"}
		run.Labels = map[string]string{"sympozium.ai/model-mediated": "true"}
		storeRun(t, r, run)
		if mediated, err := r.runKeyIsMediated(ctx, run); err != nil || mediated {
			t.Fatalf("mediated = %v err = %v", mediated, err)
		}
		refusal, err := r.authorizeRunKeys(ctx, run, originAgent(t, r, run))
		if err != nil || refusal == nil {
			t.Fatalf("refusal = %+v err = %v, want a refusal", refusal, err)
		}
		run.Spec.Env = map[string]string{modelMediatedEnv: "true"}
		if mediated, _ := r.runKeyIsMediated(ctx, run); mediated {
			t.Fatal("spec.env made the run mediated")
		}
		if refusal, err := r.authorizeRunKeys(ctx, run, originAgent(t, r, run)); err != nil || refusal == nil {
			t.Fatalf("with %s in spec.env: refusal = %+v err = %v, want a refusal", modelMediatedEnv, refusal, err)
		}
	})
	t.Run("a scoped Celln run is never treated as mediated", func(t *testing.T) {
		r, run := mediatedOriginRun(t, "llama-server", "http://llama.local:8080/v1", "", sympoziumv1alpha1.ModelMediationRequired)
		run.Spec.Backend = "celln"
		run.Spec.CellnSelection = &sympoziumv1alpha1.CellnCatalogueSelection{}
		if mediated, err := r.runKeyIsMediated(ctx, run); err != nil || mediated {
			t.Fatalf("mediated = %v err = %v", mediated, err)
		}
	})
	t.Run("policy switched off between the check and the pod", func(t *testing.T) {
		r, run := mediatedOriginRun(t, "llama-server", "http://llama.local:8080/v1", "", sympoziumv1alpha1.ModelMediationRequired)
		if refusal, err := r.authorizeRunKeys(ctx, run, originAgent(t, r, run)); err != nil || refusal != nil {
			t.Fatalf("authorizeRunKeys: refusal = %+v err = %v", refusal, err)
		}
		setMediation(t, r, sympoziumv1alpha1.ModelMediationOff)
		_, err := r.buildAgentPodTemplate(ctx, run, false, nil, nil, nil, nil)
		if err == nil || !isModelMediationRefusal(err) || modelMediationCanRetry(err) {
			t.Fatalf("err = %v, want a refusal that fails the run", err)
		}
		if got := modelkey.PinnedOrigin(mediatedKey(t, r)); got != "" {
			t.Errorf("the key was pinned to %q", got)
		}
	})
}
