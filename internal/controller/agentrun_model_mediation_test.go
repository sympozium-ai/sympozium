package controller

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/harnessgateway"
	"github.com/sympozium-ai/sympozium/internal/modelkey"
)

const (
	mediationGatewayURL = "https://harness-gateway.sympozium-system.svc"
	mediationCA         = "-----BEGIN CERTIFICATE-----\nca\n-----END CERTIFICATE-----\n"
	mediationProfiles   = `insecureOrigins: ["http://llama.local:8080"]
allowedEnvNames: [ANTHROPIC_BASE_URL, ANTHROPIC_API_KEY, OPENAI_BASE_URL, OPENAI_API_KEY, TASK, CLASH_API_KEY, MODEL_BASE_URL]
allowedAuthHeaders: [x-api-key, Authorization]
profiles:
  - name: anthropic
    providers: [anthropic]
    upstream: https://api.anthropic.com
    secretKeys: [ANTHROPIC_API_KEY, API_KEY]
    auth: {type: header, header: x-api-key}
    paths: [/v1/messages]
    env: {baseURL: [ANTHROPIC_BASE_URL], apiKey: [ANTHROPIC_API_KEY]}
  - name: openai
    providers: [openai, llama-server]
    upstream: https://api.openai.com
    secretKeys: [OPENAI_API_KEY, API_KEY]
    auth: {type: header, header: Authorization, prefix: "Bearer "}
    paths: [/v1/chat/completions]
    env: {baseURL: [OPENAI_BASE_URL], apiKey: [OPENAI_API_KEY]}
    clientBasePath: /v1
  - name: clash
    providers: [clash]
    upstream: https://clash.example
    secretKeys: [API_KEY]
    auth: {type: header, header: Authorization}
    paths: [/v1/x]
    env: {baseURL: [TASK], apiKey: [CLASH_API_KEY]}
  - name: clash-owned
    providers: [clash-owned]
    upstream: https://clash.example
    secretKeys: [API_KEY]
    auth: {type: header, header: Authorization}
    paths: [/v1/x]
    env: {baseURL: [MODEL_BASE_URL], apiKey: [CLASH_API_KEY]}
`
)

type fixedProfiles struct{ set *harnessgateway.Set }

func (f fixedProfiles) Load(context.Context) (*harnessgateway.Set, error) { return f.set, nil }

// mediatedRun builds a harness run, its required policy, its Agent, its key and
// a reconciler with the gateway enabled.
func mediatedRun(t *testing.T, name, provider, baseURL string, extra ...client.Object) (*AgentRunReconciler, *sympoziumv1alpha1.AgentRun) {
	t.Helper()
	run := harnessModeRun(nil)
	run.Name, run.UID = name, types.UID(name+"-uid")
	run.Spec.AgentRef = "mediated-agent"
	run.Spec.Model = sympoziumv1alpha1.ModelSpec{Provider: provider, Model: "m", BaseURL: baseURL, AuthSecretRef: "model-key"}
	objects := append([]client.Object{run,
		&sympoziumv1alpha1.Agent{
			ObjectMeta: metav1.ObjectMeta{Name: "mediated-agent", Namespace: run.Namespace},
			Spec:       sympoziumv1alpha1.AgentSpec{PolicyRef: "mediated"},
		},
		&sympoziumv1alpha1.SympoziumPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "mediated", Namespace: run.Namespace},
			Spec: sympoziumv1alpha1.SympoziumPolicySpec{HarnessPolicy: &sympoziumv1alpha1.HarnessPolicySpec{
				Enabled: true, AllowUnmetered: true, ModelMediation: sympoziumv1alpha1.ModelMediationRequired,
			}},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "model-key", Namespace: run.Namespace},
			Data:       map[string][]byte{"ANTHROPIC_API_KEY": []byte("real"), "OPENAI_API_KEY": []byte("real"), "API_KEY": []byte("real")},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "gateway-cert", Namespace: "sympozium-system"},
			Data:       map[string][]byte{"tls.crt": []byte("crt"), "tls.key": []byte("PRIVATE"), "ca.crt": []byte(mediationCA)},
		},
	}, extra...)
	r := newAgentRunTestReconciler(t, objects...)
	set, err := harnessgateway.ParseSet([]byte(mediationProfiles))
	if err != nil {
		t.Fatal(err)
	}
	r.HarnessGateway = &HarnessGatewayConfig{
		URL: mediationGatewayURL, Profiles: fixedProfiles{set}, Reader: r.Client,
		CASecret: types.NamespacedName{Namespace: "sympozium-system", Name: "gateway-cert"},
	}
	return r, run
}

func renderMediated(t *testing.T, r *AgentRunReconciler, run *sympoziumv1alpha1.AgentRun) corev1.PodSpec {
	t.Helper()
	template, err := r.buildAgentPodTemplate(context.Background(), run, false, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildAgentPodTemplate: %v", err)
	}
	return template.Spec
}

func mediatedEnv(c corev1.Container, name string) *corev1.EnvVar {
	for i := range c.Env {
		if c.Env[i].Name == name {
			return &c.Env[i]
		}
	}
	return nil
}

func runToken(t *testing.T, r *AgentRunReconciler, run *sympoziumv1alpha1.AgentRun) *corev1.Secret {
	t.Helper()
	var secret corev1.Secret
	err := r.Get(context.Background(), client.ObjectKey{Namespace: run.Namespace, Name: harnessgateway.RunTokenSecretName(run.Name)}, &secret)
	if errors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return &secret
}

func TestMediatedHarnessGetsTheGatewayAndNoKey(t *testing.T) {
	for _, tc := range []struct {
		name, provider, baseURL string
		wantVar, wantSuffix     string
		wantKeyVar, wantOrigin  string
	}{
		{"anthropic", "anthropic", "", "ANTHROPIC_BASE_URL", "", "ANTHROPIC_API_KEY", "https://api.anthropic.com"},
		{"openai", "openai", "", "OPENAI_BASE_URL", "/v1", "OPENAI_API_KEY", "https://api.openai.com"},
		{"compatible", "llama-server", "http://llama.local:8080/v1", "OPENAI_BASE_URL", "/v1", "OPENAI_API_KEY", "http://llama.local:8080"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, run := mediatedRun(t, "run-a", tc.provider, tc.baseURL)
			ctx := context.Background()
			if err := r.ensureRunModelToken(ctx, run); err != nil {
				t.Fatal(err)
			}
			spec := renderMediated(t, r, run)
			harness := spec.Containers[0]

			want := mediationGatewayURL + "/runs/" + run.Namespace + "/run-a" + tc.wantSuffix
			for _, name := range []string{tc.wantVar, "MODEL_BASE_URL"} {
				if got := mediatedEnv(harness, name); got == nil || got.Value != want {
					t.Errorf("%s = %+v, want %s", name, got, want)
				}
			}
			key := mediatedEnv(harness, tc.wantKeyVar)
			if key == nil || key.ValueFrom == nil || key.ValueFrom.SecretKeyRef.Name != harnessgateway.RunTokenSecretName("run-a") || key.ValueFrom.SecretKeyRef.Key != harnessgateway.TokenSecretKey {
				t.Errorf("%s = %+v, want the run token", tc.wantKeyVar, key)
			}
			if got := mediatedEnv(harness, "NODE_EXTRA_CA_CERTS"); got == nil || !strings.HasSuffix(got.Value, "/ca.crt") {
				t.Errorf("NODE_EXTRA_CA_CERTS = %+v", got)
			}
			for _, container := range slices.Concat(spec.InitContainers, spec.Containers) {
				for _, env := range container.Env {
					if env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil && env.ValueFrom.SecretKeyRef.Name == "model-key" {
						t.Errorf("container %s holds the real key as %s", container.Name, env.Name)
					}
				}
			}

			var keySecret corev1.Secret
			if err := r.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: "model-key"}, &keySecret); err != nil {
				t.Fatal(err)
			}
			if got := modelkey.PinnedOrigin(&keySecret); got != tc.wantOrigin {
				t.Errorf("pinned origin = %q, want %q", got, tc.wantOrigin)
			}
		})
	}
}

func TestTheHarnessMountsOnlyThePublicCA(t *testing.T) {
	r, run := mediatedRun(t, "run-a", "anthropic", "")
	if err := r.ensureRunModelToken(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	token := runToken(t, r, run)
	if string(token.Data[harnessgateway.CASecretKey]) != mediationCA {
		t.Errorf("ca.crt = %q", token.Data[harnessgateway.CASecretKey])
	}
	if _, ok := token.Data["tls.key"]; ok || len(token.Data) != 2 {
		t.Errorf("the token Secret holds %d keys, and only token and ca.crt belong", len(token.Data))
	}
	spec := renderMediated(t, r, run)
	var found bool
	for _, volume := range spec.Volumes {
		if volume.Name != harnessGatewayCAVolume {
			continue
		}
		found = true
		if items := volume.Secret.Items; len(items) != 1 || items[0].Key != harnessgateway.CASecretKey {
			t.Errorf("items = %+v, want ca.crt alone", items)
		}
	}
	if !found {
		t.Error("the CA volume is missing")
	}
}

func TestAMediatedRunHasNoProviderHeadersEnv(t *testing.T) {
	headers := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "headers", Namespace: "default"},
		Data:       map[string][]byte{"X-Secret": []byte("secret-header")},
	}
	r, run := mediatedRun(t, "run-a", "anthropic", "", headers)
	run.Spec.Model.ProviderHeadersSecretRef = "headers"
	ctx := context.Background()
	if err := r.ensureRunModelToken(ctx, run); err != nil {
		t.Fatal(err)
	}
	r.resolveProviderHeaders(ctx, r.Log, run)
	spec := renderMediated(t, r, run)
	if got := mediatedEnv(spec.Containers[0], "MODEL_PROVIDER_HEADERS"); got != nil {
		t.Errorf("MODEL_PROVIDER_HEADERS = %q", got.Value)
	}
}

func TestMediationFailsClosed(t *testing.T) {
	for name, mutate := range map[string]func(*AgentRunReconciler, *sympoziumv1alpha1.AgentRun){
		"a provider with no profile": func(_ *AgentRunReconciler, run *sympoziumv1alpha1.AgentRun) { run.Spec.Model.Provider = "azure-openai" },
		"the gateway not enabled":    func(r *AgentRunReconciler, _ *sympoziumv1alpha1.AgentRun) { r.HarnessGateway = nil },
		"plain http to an unlisted origin": func(_ *AgentRunReconciler, run *sympoziumv1alpha1.AgentRun) {
			run.Spec.Model.BaseURL = "http://attacker.example"
		},
		"the Agent Sandbox backend": func(_ *AgentRunReconciler, run *sympoziumv1alpha1.AgentRun) {
			run.Spec.AgentSandbox = &sympoziumv1alpha1.AgentSandboxSpec{Enabled: true}
		},
		"a key Secret with no matching key": func(_ *AgentRunReconciler, run *sympoziumv1alpha1.AgentRun) {
			run.Spec.Model.AuthSecretRef = "empty-key"
		},
		"a missing key Secret": func(_ *AgentRunReconciler, run *sympoziumv1alpha1.AgentRun) { run.Spec.Model.AuthSecretRef = "absent" },
		"provider headers and no key": func(_ *AgentRunReconciler, run *sympoziumv1alpha1.AgentRun) {
			run.Spec.Model.AuthSecretRef, run.Spec.Model.ProviderHeadersSecretRef = "", "headers"
		},
	} {
		t.Run(name, func(t *testing.T) {
			r, run := mediatedRun(t, "run-a", "anthropic", "", &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "empty-key", Namespace: "default"}})
			mutate(r, run)
			if err := r.ensureRunModelToken(context.Background(), run); err == nil {
				t.Error("ensureRunModelToken should refuse")
			}
			if _, err := r.buildAgentPodTemplate(context.Background(), run, false, nil, nil, nil, nil); err == nil {
				t.Error("no pod may be rendered")
			}
		})
	}
}

func TestAKeyPinnedElsewhereFailsTheRun(t *testing.T) {
	r, run := mediatedRun(t, "run-a", "anthropic", "")
	ctx := context.Background()
	if err := r.ensureRunModelToken(ctx, run); err != nil {
		t.Fatal(err)
	}
	second := run.DeepCopy()
	second.Name, second.UID, second.ResourceVersion = "run-b", "run-b-uid", ""
	second.Spec.Model.Provider = "llama-server"
	second.Spec.Model.BaseURL = "http://llama.local:8080/v1"
	if err := r.ensureRunModelToken(ctx, second); err == nil || !strings.Contains(err.Error(), "pinned") {
		t.Errorf("err = %v, want a pin refusal", err)
	}
}

func TestAPodNotPreparedByAMintIsNotRendered(t *testing.T) {
	r, run := mediatedRun(t, "run-a", "anthropic", "")
	_, err := r.buildAgentPodTemplate(context.Background(), run, false, nil, nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "run model token") {
		t.Errorf("err = %v", err)
	}
}

func TestAProfileCannotOverwriteAControllerVariable(t *testing.T) {
	for provider, name := range map[string]string{"clash": "TASK", "clash-owned": "MODEL_BASE_URL"} {
		r, run := mediatedRun(t, "run-a", provider, "")
		ctx := context.Background()
		if err := r.ensureRunModelToken(ctx, run); err != nil {
			t.Fatal(err)
		}
		if _, err := r.buildAgentPodTemplate(ctx, run, false, nil, nil, nil, nil); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: err = %v", provider, err)
		}
	}
}

func TestNoMediationWhenTheRunHasNoKeyOrThePolicyIsOff(t *testing.T) {
	t.Run("no authSecretRef", func(t *testing.T) {
		r, run := mediatedRun(t, "run-a", "anthropic", "")
		run.Spec.Model.AuthSecretRef = ""
		run.Spec.Model.BaseURL = "http://llama.local:8080/v1"
		if err := r.ensureRunModelToken(context.Background(), run); err != nil {
			t.Fatal(err)
		}
		if runToken(t, r, run) != nil {
			t.Error("a run with no key needs no token")
		}
		spec := renderMediated(t, r, run)
		if got := mediatedEnv(spec.Containers[0], "MODEL_BASE_URL"); got == nil || got.Value != "http://llama.local:8080/v1" {
			t.Errorf("MODEL_BASE_URL = %+v", got)
		}
	})
	t.Run("policy off", func(t *testing.T) {
		r, run := mediatedRun(t, "run-a", "anthropic", "")
		ctx := context.Background()
		var policy sympoziumv1alpha1.SympoziumPolicy
		if err := r.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: "mediated"}, &policy); err != nil {
			t.Fatal(err)
		}
		policy.Spec.HarnessPolicy.ModelMediation = sympoziumv1alpha1.ModelMediationOff
		if err := r.Update(ctx, &policy); err != nil {
			t.Fatal(err)
		}
		if err := r.ensureRunModelToken(ctx, run); err != nil {
			t.Fatal(err)
		}
		if runToken(t, r, run) != nil {
			t.Error("mediation is off, so no token")
		}
		spec := renderMediated(t, r, run)
		key := mediatedEnv(spec.Containers[0], "ANTHROPIC_API_KEY")
		if key == nil || key.ValueFrom.SecretKeyRef.Name != "model-key" {
			t.Errorf("ANTHROPIC_API_KEY = %+v, want today's behaviour", key)
		}
	})
}

func TestTheTokenSurvivesAttempts(t *testing.T) {
	r, run := mediatedRun(t, "run-a", "anthropic", "")
	ctx := context.Background()
	if err := r.ensureRunModelToken(ctx, run); err != nil {
		t.Fatal(err)
	}
	first := string(runToken(t, r, run).Data[harnessgateway.TokenSecretKey])
	firstSpec := renderMediated(t, r, run)
	if err := r.ensureRunModelToken(ctx, run); err != nil {
		t.Fatal(err)
	}
	if got := string(runToken(t, r, run).Data[harnessgateway.TokenSecretKey]); got != first || got == "" {
		t.Error("the token rotated; a pod holding the old one would break")
	}
	secondSpec := renderMediated(t, r, run)
	if mediatedEnv(firstSpec.Containers[0], "ANTHROPIC_API_KEY").ValueFrom.SecretKeyRef.Name !=
		mediatedEnv(secondSpec.Containers[0], "ANTHROPIC_API_KEY").ValueFrom.SecretKeyRef.Name {
		t.Error("attempts reference different Secrets")
	}
}

func TestASuccessorGetsItsOwnToken(t *testing.T) {
	r, run := mediatedRun(t, "run-a", "anthropic", "")
	ctx := context.Background()
	if err := r.ensureRunModelToken(ctx, run); err != nil {
		t.Fatal(err)
	}
	successor := run.DeepCopy()
	successor.Name, successor.UID, successor.ResourceVersion = "run-a-retry-1", "successor-uid", ""
	if err := r.Create(ctx, successor); err != nil {
		t.Fatal(err)
	}
	if err := r.ensureRunModelToken(ctx, successor); err != nil {
		t.Fatal(err)
	}
	if string(runToken(t, r, successor).Data[harnessgateway.TokenSecretKey]) == string(runToken(t, r, run).Data[harnessgateway.TokenSecretKey]) {
		t.Error("a successor shares its predecessor's token")
	}
}

func TestNoTokenExistsDuringBackoff(t *testing.T) {
	r, run := mediatedRun(t, "run-a", "anthropic", "")
	run.Annotations = map[string]string{retryNotBeforeAnnotation: time.Now().Add(time.Hour).Format(time.RFC3339)}
	result, err := r.reconcilePending(context.Background(), ctrl.Log, run)
	if err != nil || result.RequeueAfter == 0 {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	if runToken(t, r, run) != nil {
		t.Error("a token was minted during backoff")
	}
}

func TestTheMinterReplacesAPlantedSecret(t *testing.T) {
	r, run := mediatedRun(t, "run-a", "anthropic", "", &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: harnessgateway.RunTokenSecretName("run-a"), Namespace: "default"},
		Data:       map[string][]byte{harnessgateway.TokenSecretKey: []byte("attacker-chosen")},
	})
	if err := r.ensureRunModelToken(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	token := runToken(t, r, run)
	if string(token.Data[harnessgateway.TokenSecretKey]) == "attacker-chosen" {
		t.Error("the planted token was adopted")
	}
	if !metav1.IsControlledBy(token, run) {
		t.Error("the replacement is not owned by the run")
	}
	if token.Annotations[harnessgateway.OriginAnnotation] != "https://api.anthropic.com" {
		t.Errorf("binding = %v", token.Annotations)
	}
}

func TestReconcileCompletedDeletesTheToken(t *testing.T) {
	for _, phase := range []sympoziumv1alpha1.AgentRunPhase{
		sympoziumv1alpha1.AgentRunPhaseSucceeded, sympoziumv1alpha1.AgentRunPhaseFailed,
	} {
		t.Run(string(phase), func(t *testing.T) {
			r, run := mediatedRun(t, "run-a", "anthropic", "")
			ctx := context.Background()
			if err := r.ensureRunModelToken(ctx, run); err != nil {
				t.Fatal(err)
			}
			run.Status.Phase = phase
			if _, err := r.reconcileCompleted(ctx, ctrl.Log, run); err != nil {
				t.Fatal(err)
			}
			if runToken(t, r, run) != nil {
				t.Error("the token outlived the run")
			}
		})
	}
}

func TestReconcileCompletedLeavesASecretTheRunDoesNotControl(t *testing.T) {
	r, run := mediatedRun(t, "run-a", "anthropic", "", &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: harnessgateway.RunTokenSecretName("run-a"), Namespace: "default"},
	})
	if _, err := r.reconcileCompleted(context.Background(), ctrl.Log, run); err != nil {
		t.Fatal(err)
	}
	if runToken(t, r, run) == nil {
		t.Error("a Secret this run does not own was deleted")
	}
}
