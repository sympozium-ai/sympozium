package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/harnessgateway"
	"github.com/sympozium-ai/sympozium/internal/modelkey"
)

const (
	realKeyValue      = "sk-the-real-provider-key"
	secretHeaderValue = "the-secret-header-value"
	inlineHeaderValue = "the-inline-header-value"
)

func flowProfiles(t *testing.T, insecureOrigin string) *harnessgateway.Set {
	t.Helper()
	set, err := harnessgateway.ParseSet([]byte(`insecureOrigins: ["` + insecureOrigin + `"]
allowedEnvNames: [OPENAI_BASE_URL, OPENAI_API_KEY]
allowedAuthHeaders: [Authorization]
profiles:
  - name: openai
    providers: [openai]
    upstream: https://api.openai.com
    secretKeys: [OPENAI_API_KEY]
    auth: {type: header, header: Authorization, prefix: "Bearer "}
    paths: [/v1/chat/completions]
    env: {baseURL: [OPENAI_BASE_URL], apiKey: [OPENAI_API_KEY]}
    clientBasePath: /v1
`))
	if err != nil {
		t.Fatal(err)
	}
	return set
}

// callGateway drives the real gateway against the reconciler's own objects, with
// the token the run's Secret holds.
func callGateway(t *testing.T, r *AgentRunReconciler, set *harnessgateway.Set, run *sympoziumv1alpha1.AgentRun, token string) int {
	t.Helper()
	gateway := httptest.NewServer((&harnessgateway.Gateway{Reader: r.Client, Profiles: fixedProfiles{set}}).Handler())
	defer gateway.Close()
	req, err := http.NewRequest(http.MethodPost, gateway.URL+"/runs/"+run.Namespace+"/"+run.Name+"/v1/chat/completions", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	return response.StatusCode
}

// One token serves every attempt of an in-place chain, the real gateway honours
// it only while a pod may call the model, and the token goes when the run ends.
func TestReconcile_AMediatedHarnessChainKeepsOneTokenAndTheKeyOutOfEveryPod(t *testing.T) {
	var upstreamCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		if got := r.Header.Get("Authorization"); got != "Bearer "+realKeyValue {
			t.Errorf("the upstream saw Authorization = %q", got)
		}
	}))
	defer upstream.Close()
	set := flowProfiles(t, upstream.URL)

	var r *AgentRunReconciler
	var chainRun *sympoziumv1alpha1.AgentRun
	var token string
	ctx := context.Background()
	tokenNow := func() (string, bool) {
		var secret corev1.Secret
		err := r.Get(ctx, client.ObjectKey{Namespace: chainRun.Namespace, Name: harnessgateway.RunTokenSecretName(chainRun.Name)}, &secret)
		if err != nil {
			return "", false
		}
		return string(secret.Data[harnessgateway.TokenSecretKey]), true
	}
	assertKeyless := func(job *batchv1.Job) {
		t.Helper()
		raw, err := json.Marshal(job)
		if err != nil {
			t.Fatal(err)
		}
		for _, leak := range []string{realKeyValue, `"name":"model-key"`, upstream.URL} {
			if strings.Contains(string(raw), leak) {
				t.Errorf("Job %s holds %q", job.Name, leak)
			}
		}
		harness := job.Spec.Template.Spec.Containers[0]
		key := mediatedEnv(harness, "OPENAI_API_KEY")
		if key == nil || key.ValueFrom == nil || key.ValueFrom.SecretKeyRef.Name != harnessgateway.RunTokenSecretName(chainRun.Name) {
			t.Errorf("Job %s OPENAI_API_KEY = %+v", job.Name, key)
		}
	}
	wantGateway := func(stage string, want int) {
		t.Helper()
		if got := callGateway(t, r, set, chainRun, token); got != want {
			t.Errorf("%s: gateway status = %d, want %d", stage, got, want)
		}
	}

	driveHarnessChainWith(t, "approve", "approved", "attempt 2: slugify returns \"untitled\" when empty", harnessChainHooks{
		setup: func(run *sympoziumv1alpha1.AgentRun, agent *sympoziumv1alpha1.Agent, policy *sympoziumv1alpha1.SympoziumPolicy) []client.Object {
			chainRun = run
			run.Spec.Model.Provider, run.Spec.Model.BaseURL, run.Spec.Model.AuthSecretRef = "openai", upstream.URL, "model-key"
			agent.Spec.AuthRefs = []sympoziumv1alpha1.SecretRef{{Provider: "openai", Secret: "model-key"}}
			policy.Spec.HarnessPolicy.ModelMediation = sympoziumv1alpha1.ModelMediationRequired
			return []client.Object{
				&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "model-key", Namespace: run.Namespace}, Data: map[string][]byte{"OPENAI_API_KEY": []byte(realKeyValue)}},
				&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "gateway-cert", Namespace: "sympozium-system"}, Data: map[string][]byte{"ca.crt": []byte(mediationCA)}},
			}
		},
		configure: func(reconciler *AgentRunReconciler) {
			r = reconciler
			r.HarnessGateway = &HarnessGatewayConfig{
				URL: mediationGatewayURL, Profiles: fixedProfiles{set}, Reader: r.Client,
				CASecret: types.NamespacedName{Namespace: "sympozium-system", Name: "gateway-cert"},
			}
		},
		step: func(stage string, job *batchv1.Job) {
			switch stage {
			case "attempt-1-running":
				var ok bool
				if token, ok = tokenNow(); !ok || token == "" {
					t.Fatal("no token during attempt 1")
				}
				assertKeyless(job)
				wantGateway(stage, http.StatusOK)
			case "awaiting-gate":
				// Nothing may call the model while the gate judges /workspace.
				wantGateway(stage, http.StatusForbidden)
			case "attempt-2-pending":
				if now, _ := tokenNow(); now != token {
					t.Error("the token rotated between attempts")
				}
				wantGateway(stage, http.StatusOK)
			case "attempt-2-running":
				if now, _ := tokenNow(); now != token {
					t.Error("attempt 2 holds a different token")
				}
				assertKeyless(job)
				wantGateway(stage, http.StatusOK)
			case "completed":
				if _, ok := tokenNow(); ok {
					t.Error("the token outlived the run")
				}
				wantGateway(stage, http.StatusUnauthorized)
			}
		},
	})
	if upstreamCalls != 3 {
		t.Errorf("the upstream saw %d calls, want the 3 allowed ones", upstreamCalls)
	}
}

// The real retry path: createRetryRun copies no Secret, and the retired run's
// token dies with it.
func TestResolveGate_AMediatedSuccessorGetsItsOwnTokenAndThePredecessorsDies(t *testing.T) {
	run := withVerdict(
		gatedRun("demo-run", &sympoziumv1alpha1.RetrySpec{MaxAttempts: 3}),
		`{"action":"retry","reason":"build failed","response":"npm ERR!"}`)
	run.UID = "demo-run-uid"
	run.Spec.Task = harnessModeRun(nil).Spec.Task
	run.Spec.AgentRef = "mediated-agent"
	run.Spec.Model = sympoziumv1alpha1.ModelSpec{Provider: "anthropic", Model: "m", AuthSecretRef: "model-key"}
	r, _ := retryReconciler(t, run)
	ctx := context.Background()
	template, _ := mediatedRun(t, "unused", "anthropic", "")
	for _, obj := range []client.Object{
		&sympoziumv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "mediated-agent", Namespace: run.Namespace}, Spec: sympoziumv1alpha1.AgentSpec{PolicyRef: "mediated"}},
		&sympoziumv1alpha1.SympoziumPolicy{ObjectMeta: metav1.ObjectMeta{Name: "mediated", Namespace: run.Namespace}, Spec: sympoziumv1alpha1.SympoziumPolicySpec{HarnessPolicy: &sympoziumv1alpha1.HarnessPolicySpec{Enabled: true, AllowUnmetered: true, ModelMediation: sympoziumv1alpha1.ModelMediationRequired}}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "model-key", Namespace: run.Namespace}, Data: map[string][]byte{"ANTHROPIC_API_KEY": []byte(realKeyValue)}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "gateway-cert", Namespace: "sympozium-system"}, Data: map[string][]byte{"ca.crt": []byte(mediationCA)}},
	} {
		if err := r.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	r.HarnessGateway = &HarnessGatewayConfig{
		URL: mediationGatewayURL, Profiles: template.HarnessGateway.Profiles, Reader: r.Client,
		CASecret: types.NamespacedName{Namespace: "sympozium-system", Name: "gateway-cert"},
	}
	if err := r.ensureRunModelToken(ctx, run); err != nil {
		t.Fatal(err)
	}
	first := string(runToken(t, r, run).Data[harnessgateway.TokenSecretKey])

	if _, err := r.resolveGate(ctx, logr.Discard(), run, true, false); err != nil {
		t.Fatalf("resolveGate: %v", err)
	}
	successor := getRun(t, r, "demo-run-retry-2")
	if successor.Spec.Model.AuthSecretRef != "model-key" {
		t.Errorf("successor authSecretRef = %q", successor.Spec.Model.AuthSecretRef)
	}
	if runToken(t, r, successor) != nil {
		t.Error("a successor must not inherit a token Secret")
	}
	if err := r.ensureRunModelToken(ctx, successor); err != nil {
		t.Fatal(err)
	}
	if got := string(runToken(t, r, successor).Data[harnessgateway.TokenSecretKey]); got == first || got == "" {
		t.Error("the successor shares its predecessor's token")
	}

	predecessor := getRun(t, r, "demo-run")
	if predecessor.Status.Phase != sympoziumv1alpha1.AgentRunPhaseFailed {
		t.Fatalf("predecessor phase = %s", predecessor.Status.Phase)
	}
	if _, err := r.reconcileCompleted(ctx, logr.Discard(), predecessor); err != nil {
		t.Fatal(err)
	}
	if runToken(t, r, predecessor) != nil {
		t.Error("the predecessor's token outlived it")
	}
	if runToken(t, r, successor) == nil {
		t.Error("cleaning up the predecessor removed the successor's token")
	}
}

func reconcileMediated(t *testing.T, r *AgentRunReconciler, run *sympoziumv1alpha1.AgentRun) *sympoziumv1alpha1.AgentRun {
	t.Helper()
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return getRun(t, r, run.Name)
}

func grantKey(t *testing.T, r *AgentRunReconciler, run *sympoziumv1alpha1.AgentRun) {
	t.Helper()
	ctx := context.Background()
	var agent sympoziumv1alpha1.Agent
	if err := r.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: run.Spec.AgentRef}, &agent); err != nil {
		t.Fatal(err)
	}
	agent.Spec.AuthRefs = []sympoziumv1alpha1.SecretRef{{Provider: run.Spec.Model.Provider, Secret: "model-key"}, {Secret: "headers"}}
	if err := r.Update(ctx, &agent); err != nil {
		t.Fatal(err)
	}
}

// Through the real reconcile: no value of any credential lands in the Job, the
// run's stored spec or the token Secret.
func TestReconcile_NoCredentialValueLandsInTheJob(t *testing.T) {
	headers := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "headers", Namespace: "default"},
		Data:       map[string][]byte{"X-Secret": []byte(secretHeaderValue)},
	}
	r, run := mediatedRun(t, "run-a", "anthropic", "", headers)
	run.Spec.Model.ProviderHeadersSecretRef = "headers"
	run.Spec.Model.ProviderHeaders = map[string]string{"X-Inline": inlineHeaderValue}
	if err := r.Update(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	grantKey(t, r, run)
	ctx := context.Background()
	var key corev1.Secret
	if err := r.Get(ctx, client.ObjectKey{Namespace: "default", Name: "model-key"}, &key); err != nil {
		t.Fatal(err)
	}
	key.Data["ANTHROPIC_API_KEY"] = []byte(realKeyValue)
	if err := r.Update(ctx, &key); err != nil {
		t.Fatal(err)
	}

	got := reconcileMediated(t, r, run)
	if got.Status.Error != "" {
		t.Fatalf("run failed: %s", got.Status.Error)
	}
	var job batchv1.Job
	if err := r.Get(ctx, client.ObjectKey{Namespace: "default", Name: run.Name}, &job); err != nil {
		t.Fatalf("no Job: %v", err)
	}
	raw, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{realKeyValue, secretHeaderValue, inlineHeaderValue, `"name":"model-key"`, `"name":"headers"`, "MODEL_PROVIDER_HEADERS"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("the Job holds %q", leak)
		}
	}
	for _, name := range allowedAuthSecretKeys {
		if name == "ANTHROPIC_API_KEY" {
			continue
		}
		if mediatedEnv(job.Spec.Template.Spec.Containers[0], name) != nil {
			t.Errorf("the harness still has %s", name)
		}
	}

	stored := getRun(t, r, run.Name)
	storedRaw, _ := json.Marshal(stored)
	if strings.Contains(string(storedRaw), secretHeaderValue) || strings.Contains(string(storedRaw), realKeyValue) {
		t.Error("a credential value was persisted on the run")
	}

	token := runToken(t, r, run)
	tokenRaw, _ := json.Marshal(token)
	for _, leak := range []string{b64(realKeyValue), b64(secretHeaderValue), realKeyValue, secretHeaderValue} {
		if strings.Contains(string(tokenRaw), leak) {
			t.Errorf("the token Secret holds %q", leak)
		}
	}
	if err := r.Get(ctx, client.ObjectKey{Namespace: "default", Name: "model-key"}, &key); err != nil {
		t.Fatal(err)
	}
	if string(key.Data["ANTHROPIC_API_KEY"]) != realKeyValue {
		t.Error("pinning changed the key's value")
	}
}

func b64(value string) string {
	raw, _ := json.Marshal([]byte(value))
	return strings.Trim(string(raw), `"`)
}

func TestReconcile_AnUnmediatableRunFailsAndCreatesNoJob(t *testing.T) {
	for name, tc := range map[string]struct {
		provider string
		mutate   func(*AgentRunReconciler)
		want     string
	}{
		"no profile":          {provider: "azure-openai", want: "no profile"},
		"gateway not enabled": {provider: "anthropic", mutate: func(r *AgentRunReconciler) { r.HarnessGateway = nil }, want: "harnessGateway.enabled"},
	} {
		t.Run(name, func(t *testing.T) {
			r, run := mediatedRun(t, "run-a", tc.provider, "")
			grantKey(t, r, run)
			if tc.mutate != nil {
				tc.mutate(r)
			}
			got := reconcileMediated(t, r, run)
			if got.Status.Phase != sympoziumv1alpha1.AgentRunPhaseFailed || !strings.Contains(got.Status.Error, tc.want) {
				t.Errorf("phase = %s error = %q", got.Status.Phase, got.Status.Error)
			}
			err := r.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: run.Name}, &batchv1.Job{})
			if !apierrors.IsNotFound(err) {
				t.Errorf("a Job exists: err = %v", err)
			}
		})
	}
}

func TestMediationReadsFailClosed(t *testing.T) {
	ctx := context.Background()
	t.Run("a missing Agent", func(t *testing.T) {
		r, run := mediatedRun(t, "run-a", "anthropic", "")
		run.Spec.AgentRef = "gone"
		if _, err := r.buildAgentPodTemplate(ctx, run, false, nil, nil, nil, nil); err == nil {
			t.Error("no pod may be rendered")
		}
	})
	t.Run("a missing policy", func(t *testing.T) {
		r, run := mediatedRun(t, "run-a", "anthropic", "")
		var policy sympoziumv1alpha1.SympoziumPolicy
		if err := r.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: "mediated"}, &policy); err != nil {
			t.Fatal(err)
		}
		if err := r.Delete(ctx, &policy); err != nil {
			t.Fatal(err)
		}
		if _, err := r.buildAgentPodTemplate(ctx, run, false, nil, nil, nil, nil); err == nil {
			t.Error("no pod may be rendered")
		}
	})
	t.Run("a missing or empty gateway CA", func(t *testing.T) {
		r, run := mediatedRun(t, "run-a", "anthropic", "")
		r.HarnessGateway.CASecret.Name = "absent"
		if err := r.ensureRunModelToken(ctx, run); err == nil {
			t.Error("a missing CA must refuse")
		}
		r.HarnessGateway.CASecret = types.NamespacedName{Namespace: "default", Name: "model-key"} // holds no ca.crt
		if err := r.ensureRunModelToken(ctx, run); err == nil || !strings.Contains(err.Error(), "ca.crt") {
			t.Errorf("err = %v", err)
		}
		if runToken(t, r, run) != nil {
			t.Error("a token was minted without a CA")
		}
	})
	t.Run("profiles that cannot load", func(t *testing.T) {
		r, run := mediatedRun(t, "run-a", "anthropic", "")
		r.HarnessGateway.Profiles = failingProfiles{}
		if err := r.ensureRunModelToken(ctx, run); err == nil {
			t.Error("must refuse")
		}
		if _, err := r.buildAgentPodTemplate(ctx, run, false, nil, nil, nil, nil); err == nil {
			t.Error("no pod may be rendered")
		}
	})
	t.Run("a token Secret planted after minting", func(t *testing.T) {
		r, run := mediatedRun(t, "run-a", "anthropic", "")
		if err := r.ensureRunModelToken(ctx, run); err != nil {
			t.Fatal(err)
		}
		token := runToken(t, r, run)
		token.OwnerReferences = nil
		if err := r.Update(ctx, token); err != nil {
			t.Fatal(err)
		}
		_, err := r.buildAgentPodTemplate(ctx, run, false, nil, nil, nil, nil)
		if err == nil || !strings.Contains(err.Error(), "not owned") {
			t.Errorf("err = %v", err)
		}
	})
}

type failingProfiles struct{}

func (failingProfiles) Load(context.Context) (*harnessgateway.Set, error) {
	return nil, errors.New("no profiles")
}

func TestABindingRecordsWhatTheTokenWasMintedFor(t *testing.T) {
	headers := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "headers", Namespace: "default", UID: "headers-uid"}}
	r, run := mediatedRun(t, "run-a", "anthropic", "", headers)
	run.Spec.Model.ProviderHeadersSecretRef = "headers"
	ctx := context.Background()
	var agent sympoziumv1alpha1.Agent
	if err := r.Get(ctx, client.ObjectKey{Namespace: "default", Name: "mediated-agent"}, &agent); err != nil {
		t.Fatal(err)
	}
	agent.UID = "agent-uid"
	if err := r.Update(ctx, &agent); err != nil {
		t.Fatal(err)
	}
	if err := r.ensureRunModelToken(ctx, run); err != nil {
		t.Fatal(err)
	}
	var key corev1.Secret
	if err := r.Get(ctx, client.ObjectKey{Namespace: "default", Name: "model-key"}, &key); err != nil {
		t.Fatal(err)
	}
	token := runToken(t, r, run)
	for annotation, want := range map[string]string{
		harnessgateway.AgentUIDAnnotation:         "agent-uid",
		harnessgateway.KeySecretUIDAnnotation:     string(key.UID),
		harnessgateway.HeadersSecretUIDAnnotation: "headers-uid",
		harnessgateway.OriginAnnotation:           "https://api.anthropic.com",
	} {
		if got := token.Annotations[annotation]; got != want {
			t.Errorf("%s = %q, want %q", annotation, got, want)
		}
	}

	// An edited run must stop working, not move the token.
	run.Spec.Model.BaseURL = "http://llama.local:8080/v1"
	run.Spec.Model.Provider = "llama-server"
	key.Annotations[modelkey.OriginAnnotation] = "http://llama.local:8080"
	if err := r.Update(ctx, &key); err != nil {
		t.Fatal(err)
	}
	if err := r.ensureRunModelToken(ctx, run); err != nil {
		t.Fatal(err)
	}
	if got := runToken(t, r, run).Annotations[harnessgateway.OriginAnnotation]; got != "https://api.anthropic.com" {
		t.Errorf("the binding moved to %q", got)
	}

	missing, other := mediatedRun(t, "run-b", "anthropic", "")
	other.Spec.Model.ProviderHeadersSecretRef = "absent"
	if err := missing.ensureRunModelToken(ctx, other); err == nil {
		t.Error("a missing headers Secret must refuse")
	}
}

func TestARotatedCAReachesTheTokenSecretAndKeepsTheToken(t *testing.T) {
	r, run := mediatedRun(t, "run-a", "anthropic", "")
	ctx := context.Background()
	if err := r.ensureRunModelToken(ctx, run); err != nil {
		t.Fatal(err)
	}
	before := string(runToken(t, r, run).Data[harnessgateway.TokenSecretKey])
	var cert corev1.Secret
	if err := r.Get(ctx, r.HarnessGateway.CASecret, &cert); err != nil {
		t.Fatal(err)
	}
	cert.Data["ca.crt"] = []byte("rotated")
	if err := r.Update(ctx, &cert); err != nil {
		t.Fatal(err)
	}
	if err := r.ensureRunModelToken(ctx, run); err != nil {
		t.Fatal(err)
	}
	token := runToken(t, r, run)
	if string(token.Data[harnessgateway.CASecretKey]) != "rotated" || string(token.Data[harnessgateway.TokenSecretKey]) != before {
		t.Errorf("ca = %q, token changed = %v", token.Data[harnessgateway.CASecretKey], string(token.Data[harnessgateway.TokenSecretKey]) != before)
	}
}

func TestMintingRaces(t *testing.T) {
	ctx := context.Background()
	race := func(t *testing.T, owned bool) (*AgentRunReconciler, *sympoziumv1alpha1.AgentRun, error) {
		t.Helper()
		r, run := mediatedRun(t, "run-a", "anthropic", "")
		r.APIReader = nil
		r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
			Create: func(ctx context.Context, inner client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if obj.GetName() != harnessgateway.RunTokenSecretName("run-a") {
					return inner.Create(ctx, obj, opts...)
				}
				competing := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Name: obj.GetName(), Namespace: obj.GetNamespace()},
					Data:       map[string][]byte{harnessgateway.TokenSecretKey: []byte("the-other-reconcile")},
				}
				if owned {
					competing.OwnerReferences = obj.GetOwnerReferences()
				}
				if err := inner.Create(ctx, competing); err != nil {
					return err
				}
				return apierrors.NewAlreadyExists(schema.GroupResource{Resource: "secrets"}, obj.GetName())
			},
		})
		return r, run, r.ensureRunModelToken(ctx, run)
	}
	t.Run("another reconcile of this run wins", func(t *testing.T) {
		r, run, err := race(t, true)
		if err != nil {
			t.Fatal(err)
		}
		if got := string(runToken(t, r, run).Data[harnessgateway.TokenSecretKey]); got != "the-other-reconcile" {
			t.Errorf("token = %q", got)
		}
	})
	t.Run("a planted Secret wins", func(t *testing.T) {
		_, _, err := race(t, false)
		if err == nil || !strings.Contains(err.Error(), "not owned") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestAssertNoModelSecretInPod(t *testing.T) {
	run := &sympoziumv1alpha1.AgentRun{Spec: sympoziumv1alpha1.AgentRunSpec{Model: sympoziumv1alpha1.ModelSpec{AuthSecretRef: "key", ProviderHeadersSecretRef: "headers"}}}
	ref := func(name string) corev1.Container {
		return corev1.Container{Name: "c", Env: []corev1.EnvVar{{Name: "E", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: name}}}}}}
	}
	from := corev1.Container{Name: "c", EnvFrom: []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "headers"}}}}}
	for name, tc := range map[string]struct {
		spec corev1.PodSpec
		ok   bool
	}{
		"a clean pod":                    {corev1.PodSpec{Containers: []corev1.Container{ref("token")}}, true},
		"the key as env":                 {corev1.PodSpec{Containers: []corev1.Container{ref("key")}}, false},
		"the headers Secret as env":      {corev1.PodSpec{Containers: []corev1.Container{ref("headers")}}, false},
		"the key in an init container":   {corev1.PodSpec{InitContainers: []corev1.Container{ref("key")}}, false},
		"the key in a sidecar":           {corev1.PodSpec{Containers: []corev1.Container{ref("token"), ref("key")}}, false},
		"a whole Secret through envFrom": {corev1.PodSpec{Containers: []corev1.Container{from}}, false},
		"the key as a volume":            {corev1.PodSpec{Volumes: []corev1.Volume{{Name: "v", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "key"}}}}}, false},
	} {
		t.Run(name, func(t *testing.T) {
			if err := assertNoModelSecretInPod(run, &tc.spec); (err == nil) != tc.ok {
				t.Errorf("err = %v", err)
			}
		})
	}
}

func TestOnlyTheHarnessHoldsTheToken(t *testing.T) {
	r, run := mediatedRun(t, "run-a", "anthropic", "")
	ctx := context.Background()
	if err := r.ensureRunModelToken(ctx, run); err != nil {
		t.Fatal(err)
	}
	sidecars := []resolvedSidecar{{skillPackName: "skill", sidecar: sympoziumv1alpha1.SkillSidecar{Image: "ghcr.io/acme/skill:1"}}}
	template, err := r.buildAgentPodTemplate(ctx, run, false, nil, sidecars, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	spec := template.Spec
	if len(spec.Containers) < 2 {
		t.Fatalf("expected other containers, got %d", len(spec.Containers))
	}
	tokenSecret := harnessgateway.RunTokenSecretName(run.Name)
	for i, container := range slices.Concat(spec.Containers, spec.InitContainers) {
		for _, env := range container.Env {
			holds := env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil && env.ValueFrom.SecretKeyRef.Name == tokenSecret
			if holds && i != 0 {
				t.Errorf("container %s holds the token as %s", container.Name, env.Name)
			}
		}
		mountsCA := slices.ContainsFunc(container.VolumeMounts, func(m corev1.VolumeMount) bool { return m.Name == harnessGatewayCAVolume })
		if mountsCA != (i == 0) {
			t.Errorf("container %s mounts the CA = %v", container.Name, mountsCA)
		}
	}
	for _, mount := range spec.Containers[0].VolumeMounts {
		if mount.Name == harnessGatewayCAVolume && !mount.ReadOnly {
			t.Error("the CA mount must be read-only")
		}
	}
}

func TestPinningRefusesAndPreserves(t *testing.T) {
	ctx := context.Background()
	t.Run("an already pinned key is left alone", func(t *testing.T) {
		r, run := mediatedRun(t, "run-a", "anthropic", "")
		for range 2 {
			if err := r.ensureRunModelToken(ctx, run); err != nil {
				t.Fatal(err)
			}
		}
		var key corev1.Secret
		if err := r.Get(ctx, client.ObjectKey{Namespace: "default", Name: "model-key"}, &key); err != nil {
			t.Fatal(err)
		}
		if modelkey.PinnedOrigin(&key) != "https://api.anthropic.com" {
			t.Errorf("origin = %q", modelkey.PinnedOrigin(&key))
		}
	})
	t.Run("a refused pin mints no token", func(t *testing.T) {
		r, run := mediatedRun(t, "run-a", "anthropic", "")
		var key corev1.Secret
		if err := r.Get(ctx, client.ObjectKey{Namespace: "default", Name: "model-key"}, &key); err != nil {
			t.Fatal(err)
		}
		key.Annotations = map[string]string{modelkey.OriginAnnotation: "https://elsewhere.example"}
		if err := r.Update(ctx, &key); err != nil {
			t.Fatal(err)
		}
		if err := r.ensureRunModelToken(ctx, run); err == nil {
			t.Error("a key pinned elsewhere must refuse")
		}
		if runToken(t, r, run) != nil {
			t.Error("a token was minted for a refused key")
		}
		if err := r.Get(ctx, client.ObjectKey{Namespace: "default", Name: "model-key"}, &key); err != nil {
			t.Fatal(err)
		}
		if modelkey.PinnedOrigin(&key) != "https://elsewhere.example" {
			t.Errorf("the pin moved to %q", modelkey.PinnedOrigin(&key))
		}
	})
}

// A run on the built-in agent-runner keeps today's path, whatever the policy says.
func TestANonHarnessRunIsNeverMediated(t *testing.T) {
	r, run := mediatedRun(t, "run-a", "anthropic", "")
	run.Spec.Task = sympoziumv1alpha1.NewStringTask("no harness")
	if err := r.ensureRunModelToken(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if runToken(t, r, run) != nil {
		t.Error("the built-in agent-runner path changed")
	}
}

// A SkillPack can mount a Secret into a sidecar, in the same pod as the harness.
// A mediated run must refuse a pod that would bring the key back this way.
func TestASidecarCannotBringTheKeyBackIntoThePod(t *testing.T) {
	r, run := mediatedRun(t, "run-a", "anthropic", "")
	ctx := context.Background()
	if err := r.ensureRunModelToken(ctx, run); err != nil {
		t.Fatal(err)
	}
	sidecars := []resolvedSidecar{{skillPackName: "skill", sidecar: sympoziumv1alpha1.SkillSidecar{Image: "ghcr.io/acme/skill:1", SecretRef: "model-key"}}}
	_, err := r.buildAgentPodTemplate(ctx, run, false, nil, sidecars, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "model Secret") {
		t.Errorf("err = %v", err)
	}
}

func TestTheTokenIsShapedLikeTheKindOfKey(t *testing.T) {
	const variants = `allowedEnvNames: [ANTHROPIC_BASE_URL, ANTHROPIC_API_KEY]
allowedAuthHeaders: [x-api-key, Authorization]
profiles:
  - name: anthropic
    providers: [anthropic]
    upstream: https://api.anthropic.com
    secretKeys: [ANTHROPIC_API_KEY]
    auth: {type: header, header: x-api-key}
    credentials:
      - {keyPrefix: sk-ant-oat, fakeKeyPattern: "sk-ant-oat01-{token}", auth: {type: header, header: Authorization, prefix: "Bearer "}}
      - {keyPrefix: sk-ant-api, fakeKeyPattern: "sk-ant-api03-{token}"}
    paths: [/v1/messages]
    env: {baseURL: [ANTHROPIC_BASE_URL], apiKey: [ANTHROPIC_API_KEY]}
`
	for name, tc := range map[string]struct {
		key, prefix string
	}{
		"an API key":         {"sk-ant-api03-REALSECRET", "sk-ant-api03-"},
		"a subscription key": {"sk-ant-oat01-REALSECRET", "sk-ant-oat01-"},
		"a wrapped key":      {"sk-ant-oat01-\nREALSECRET\n", "sk-ant-oat01-"},
		"any other key":      {"proxy-REALSECRET", ""},
	} {
		t.Run(name, func(t *testing.T) {
			r, run := mediatedRun(t, "run-a", "anthropic", "")
			set, err := harnessgateway.ParseSet([]byte(variants))
			if err != nil || len(set.Rejected) != 0 {
				t.Fatalf("err = %v, rejected = %v", err, set.Rejected)
			}
			r.HarnessGateway.Profiles = fixedProfiles{set}
			ctx := context.Background()
			var key corev1.Secret
			if err := r.Get(ctx, client.ObjectKey{Namespace: "default", Name: "model-key"}, &key); err != nil {
				t.Fatal(err)
			}
			key.Data["ANTHROPIC_API_KEY"] = []byte(tc.key)
			if err := r.Update(ctx, &key); err != nil {
				t.Fatal(err)
			}
			if err := r.ensureRunModelToken(ctx, run); err != nil {
				t.Fatal(err)
			}
			secret := runToken(t, r, run)
			token := string(secret.Data[harnessgateway.TokenSecretKey])
			if !strings.HasPrefix(token, tc.prefix) || len(token) < len(tc.prefix)+2*runModelTokenBytes {
				t.Errorf("token = %q, want the prefix %q and the full random part", token, tc.prefix)
			}
			raw, _ := json.Marshal(secret)
			if strings.Contains(token, "REALSECRET") || strings.Contains(string(raw), b64(tc.key)) {
				t.Error("the token Secret holds the real key")
			}
			spec := renderMediated(t, r, run)
			if got := mediatedEnv(spec.Containers[0], modelMediatedEnv); got == nil || got.Value != "true" {
				t.Errorf("%s = %+v", modelMediatedEnv, got)
			}
		})
	}
}

// Two runs of one kind of key share the pattern, not the token.
func TestShapedTokensAreStillRandom(t *testing.T) {
	r, first := mediatedRun(t, "run-a", "anthropic", "")
	ctx := context.Background()
	if err := r.ensureRunModelToken(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := first.DeepCopy()
	second.Name, second.UID, second.ResourceVersion = "run-b", "run-b-uid", ""
	if err := r.Create(ctx, second); err != nil {
		t.Fatal(err)
	}
	if err := r.ensureRunModelToken(ctx, second); err != nil {
		t.Fatal(err)
	}
	if string(runToken(t, r, first).Data[harnessgateway.TokenSecretKey]) == string(runToken(t, r, second).Data[harnessgateway.TokenSecretKey]) {
		t.Error("two runs share a token")
	}
}

func TestOnlyTheControllerCanSayARunIsMediated(t *testing.T) {
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
	spec := corev1.PodSpec{Containers: []corev1.Container{{Name: "harness", Env: []corev1.EnvVar{{Name: modelMediatedEnv, Value: "true"}, {Name: "KEPT", Value: "1"}}}}}
	if err := r.applyModelMediation(ctx, run, &spec); err != nil {
		t.Fatal(err)
	}
	if mediatedEnv(spec.Containers[0], modelMediatedEnv) != nil {
		t.Error("a run with a real key claims to be mediated")
	}
	if mediatedEnv(spec.Containers[0], "KEPT") == nil {
		t.Error("other env was removed")
	}
}
