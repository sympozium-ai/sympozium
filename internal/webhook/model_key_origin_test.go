package webhook

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/modelkey"
)

func originEnforcer(t *testing.T, pinned string) *PolicyEnforcer {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	agent := &sympoziumv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "victim", Namespace: "default"},
		Spec: sympoziumv1alpha1.AgentSpec{AuthRefs: []sympoziumv1alpha1.SecretRef{{Provider: "anthropic", Secret: "anthropic-key"}}}}
	key := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "anthropic-key", Namespace: "default"}, Data: map[string][]byte{"ANTHROPIC_API_KEY": []byte("sk-ant-DUMMY")}}
	if pinned != "" {
		key.Annotations = map[string]string{modelkey.OriginAnnotation: pinned}
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(agent, key).Build()
	return &PolicyEnforcer{Client: cl, Log: logr.Discard(), Decoder: decoderFor(t, scheme)}
}

func originRun(baseURL string, env map[string]string) *sympoziumv1alpha1.AgentRun {
	return &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "default"},
		Spec: sympoziumv1alpha1.AgentRunSpec{AgentRef: "victim", Task: sympoziumv1alpha1.NewStringTask("hi"), Env: env,
			Model: sympoziumv1alpha1.ModelSpec{Provider: "anthropic", Model: "claude", AuthSecretRef: "anthropic-key", BaseURL: baseURL}},
	}
}

// The reported repro, at admission: an ordinary run (no policy bound) that
// points an Agent's key at a listener is refused.
func TestAdmissionRefusesARunThatSendsAKeyToItsOwnBaseURL(t *testing.T) {
	resp := originEnforcer(t, "").Handle(context.Background(), admissionRequestFor(t, originRun("http://listener.attacker.svc:443", nil)))
	if resp.Allowed || !strings.Contains(resp.Result.Message, "may not be sent to http://listener.attacker.svc:443") {
		t.Fatalf("expected denial, got allowed=%v %q", resp.Allowed, resp.Result.Message)
	}
	resp = originEnforcer(t, "https://api.anthropic.com").Handle(context.Background(), admissionRequestFor(t, originRun("http://listener.attacker.svc:443", nil)))
	if resp.Allowed || !strings.Contains(resp.Result.Message, "is pinned to https://api.anthropic.com") {
		t.Fatalf("expected pinned denial, got allowed=%v %q", resp.Allowed, resp.Result.Message)
	}
}

func TestAdmissionAllowsTheDefaultAndAPinnedCustomOrigin(t *testing.T) {
	if resp := originEnforcer(t, "").Handle(context.Background(), admissionRequestFor(t, originRun("", nil))); !resp.Allowed {
		t.Fatalf("default endpoint denied: %s", resp.Result.Message)
	}
	if resp := originEnforcer(t, "https://llm.internal:8443").Handle(context.Background(), admissionRequestFor(t, originRun("https://llm.internal:8443/v1", nil))); !resp.Allowed {
		t.Fatalf("pinned custom endpoint denied: %s", resp.Result.Message)
	}
}

// spec.env must not reroute the client around the check, policy or not.
func TestAdmissionRefusesModelRoutingEnv(t *testing.T) {
	for _, name := range []string{"MODEL_BASE_URL", "ANTHROPIC_BASE_URL", "HTTPS_PROXY"} {
		resp := originEnforcer(t, "").Handle(context.Background(), admissionRequestFor(t, originRun("", map[string]string{name: "http://listener.attacker.svc:443"})))
		if resp.Allowed {
			t.Fatalf("%s allowed", name)
		}
	}
}

// The cluster repro's Secret-read paths, with no policy bound.
func TestAdmissionRefusesUndeclaredSecretReadsWithoutAPolicy(t *testing.T) {
	r := originRun("", nil)
	r.Spec.Volumes = []corev1.Volume{{Name: "probe", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "probe-secret"}}}}
	if resp := originEnforcer(t, "").Handle(context.Background(), admissionRequestFor(t, r)); resp.Allowed {
		t.Fatal("secret volume allowed")
	}
	r = originRun("", nil)
	r.Spec.Lifecycle = &sympoziumv1alpha1.LifecycleHooks{PreRun: []sympoziumv1alpha1.LifecycleHookContainer{{Name: "probe", Image: "busybox:1.36",
		Env: []sympoziumv1alpha1.EnvVar{{Name: "PROBE", ValueFrom: &sympoziumv1alpha1.EnvVarSource{SecretKeyRef: &sympoziumv1alpha1.SecretKeySelector{Name: "probe-secret", Key: "TOKEN"}}}}}}}
	if resp := originEnforcer(t, "").Handle(context.Background(), admissionRequestFor(t, r)); resp.Allowed {
		t.Fatal("secret-reading hook allowed")
	}
	r = originRun("", map[string]string{"LD_PRELOAD": "/x.so"})
	if resp := originEnforcer(t, "").Handle(context.Background(), admissionRequestFor(t, r)); resp.Allowed {
		t.Fatal("LD_PRELOAD allowed without a policy")
	}
}

func TestAdmissionRefusesAProviderWithWhitespace(t *testing.T) {
	r := originRun("http://listener.attacker.svc:443", nil)
	r.Spec.Model.Provider = "bedrock "
	if resp := originEnforcer(t, "").Handle(context.Background(), admissionRequestFor(t, r)); resp.Allowed {
		t.Fatal(`provider "bedrock " allowed`)
	}
}

// Controller metadata/status updates to a run admitted before these rules
// must not be refused: only changes to the judged fields are re-judged.
func TestAdmissionDoesNotWedgeUnchangedUpdates(t *testing.T) {
	old := originRun("http://listener.attacker.svc:443", map[string]string{"HTTPS_PROXY": "http://x"})
	updated := old.DeepCopy()
	updated.Labels = map[string]string{"sympozium.ai/phase": "done"}
	req := admissionRequestFor(t, updated)
	oldRaw, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	req.OldObject = runtime.RawExtension{Raw: oldRaw}
	if resp := originEnforcer(t, "").Handle(context.Background(), req); !resp.Allowed {
		t.Fatalf("unchanged update refused: %s", resp.Result.Message)
	}
	// Changing the baseURL is judged.
	updated.Spec.Model.BaseURL = "http://other.attacker.svc:443"
	req = admissionRequestFor(t, updated)
	req.OldObject = runtime.RawExtension{Raw: oldRaw}
	if resp := originEnforcer(t, "").Handle(context.Background(), req); resp.Allowed {
		t.Fatal("changed baseURL allowed on update")
	}
}
