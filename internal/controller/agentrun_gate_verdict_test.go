package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// gateTokenRun returns a run with one gate hook, the shape ensureGateVerdictToken
// mints for.
func gateTokenRun(name string, gate bool) *sympoziumv1alpha1.AgentRun {
	run := gatedPostRunRun(name)
	// A real AgentRun always carries a UID, and metav1.IsControlledBy compares
	// nothing else. Without one every run looks like the controller of every
	// owned object, because empty matches empty — which would make the
	// ownership check below vacuously true.
	run.UID = types.UID("uid-" + name)
	if !gate {
		for i := range run.Spec.Lifecycle.PostRun {
			run.Spec.Lifecycle.PostRun[i].Gate = false
		}
	}
	return run
}

func gateSecret(t *testing.T, r *AgentRunReconciler, run string) *corev1.Secret {
	t.Helper()
	var s corev1.Secret
	err := r.Get(context.Background(), types.NamespacedName{
		Name: "sympozium-run-gate-" + run, Namespace: "default",
	}, &s)
	if err != nil {
		return nil
	}
	return &s
}

func TestEnsureGateVerdictToken_MintsForAGatedRun(t *testing.T) {
	run := gateTokenRun("demo-run", true)
	r := newAgentRunTestReconciler(t, run)

	if err := r.ensureGateVerdictToken(context.Background(), run); err != nil {
		t.Fatalf("ensureGateVerdictToken: %v", err)
	}

	secret := gateSecret(t, r, "demo-run")
	if secret == nil {
		t.Fatal("no gate verdict Secret was created")
	}
	token := secret.StringData[GateVerdictSecretKey]
	if token == "" {
		token = string(secret.Data[GateVerdictSecretKey])
	}
	if len(token) != gateVerdictTokenBytes*2 {
		t.Errorf("token length = %d, want %d hex chars", len(token), gateVerdictTokenBytes*2)
	}

	// Owned by the run, so it is garbage-collected with it rather than
	// outliving the gate it belongs to.
	owners := secret.GetOwnerReferences()
	if len(owners) != 1 || owners[0].Name != "demo-run" || owners[0].Kind != "AgentRun" {
		t.Errorf("owner references = %+v, want a single AgentRun/demo-run", owners)
	}
}

// The gate container reads the token once at startup, so a second reconcile
// must not rotate it out from under a hook that already holds it.
func TestEnsureGateVerdictToken_IsIdempotent(t *testing.T) {
	run := gateTokenRun("demo-run", true)
	r := newAgentRunTestReconciler(t, run)
	ctx := context.Background()

	if err := r.ensureGateVerdictToken(ctx, run); err != nil {
		t.Fatalf("first call: %v", err)
	}
	first := gateSecret(t, r, "demo-run")
	if first == nil {
		t.Fatal("no Secret after the first call")
	}

	if err := r.ensureGateVerdictToken(ctx, run); err != nil {
		t.Fatalf("second call: %v", err)
	}
	second := gateSecret(t, r, "demo-run")
	if second == nil {
		t.Fatal("no Secret after the second call")
	}

	if string(second.Data[GateVerdictSecretKey]) != string(first.Data[GateVerdictSecretKey]) ||
		second.StringData[GateVerdictSecretKey] != first.StringData[GateVerdictSecretKey] {
		t.Error("the token was rotated on the second reconcile; a hook holding the first would be locked out")
	}
}

func TestEnsureGateVerdictToken_SkipsAnUngatedRun(t *testing.T) {
	run := gateTokenRun("demo-run", false)
	r := newAgentRunTestReconciler(t, run)

	if err := r.ensureGateVerdictToken(context.Background(), run); err != nil {
		t.Fatalf("ensureGateVerdictToken: %v", err)
	}
	if gateSecret(t, r, "demo-run") != nil {
		t.Error("a run with no gate hook was given a verdict token")
	}
}

// The credential reaches the gate container and nothing else — the property
// that lets the hook drop its RBAC.
func TestBuildPostRunJob_GateCredentialIsScopedToTheGateContainer(t *testing.T) {
	t.Setenv("SYMPOZIUM_API_URL", "http://sympozium-apiserver.sympozium-system.svc:8080")

	run := gatedPostRunRun("demo-run")
	run.Spec.Lifecycle.PostRun = append(run.Spec.Lifecycle.PostRun,
		sympoziumv1alpha1.LifecycleHookContainer{Name: "notify", Image: "notify:latest"})

	r := newAgentRunTestReconciler(t, run)
	job := r.buildPostRunJob(run, 0, "result")

	var gate, plain *corev1.Container
	for i := range job.Spec.Template.Spec.InitContainers {
		c := &job.Spec.Template.Spec.InitContainers[i]
		switch c.Name {
		case "post-check":
			gate = c
		case "post-notify":
			plain = c
		}
	}
	if gate == nil || plain == nil {
		t.Fatalf("expected post-check and post-notify init containers, got %d", len(job.Spec.Template.Spec.InitContainers))
	}

	var ref *corev1.SecretKeySelector
	sawURL := false
	for _, e := range gate.Env {
		switch e.Name {
		case "GATE_VERDICT_TOKEN":
			if e.Value != "" {
				t.Error("the token was inlined as a literal; it must come from a SecretKeyRef")
			}
			if e.ValueFrom != nil {
				ref = e.ValueFrom.SecretKeyRef
			}
		case "SYMPOZIUM_API_URL":
			sawURL = true
		}
	}
	if ref == nil {
		t.Fatal("gate container has no GATE_VERDICT_TOKEN SecretKeyRef")
	}
	if ref.Name != "sympozium-run-gate-demo-run" || ref.Key != GateVerdictSecretKey {
		t.Errorf("SecretKeyRef = %s/%s, want sympozium-run-gate-demo-run/%s", ref.Name, ref.Key, GateVerdictSecretKey)
	}
	if !sawURL {
		t.Error("gate container has no SYMPOZIUM_API_URL")
	}

	for _, e := range plain.Env {
		if e.Name == "GATE_VERDICT_TOKEN" || e.Name == "SYMPOZIUM_API_URL" {
			t.Errorf("non-gate hook was given %s; the credential must not leak to other hooks", e.Name)
		}
	}
}

// An older chart sets no API URL. Handing out a token the hook cannot use
// would be worse than leaving it on the annotation path.
func TestBuildPostRunJob_NoAPIURLMeansNoCredential(t *testing.T) {
	t.Setenv("SYMPOZIUM_API_URL", "")

	run := gatedPostRunRun("demo-run")
	r := newAgentRunTestReconciler(t, run)
	job := r.buildPostRunJob(run, 0, "result")

	for _, c := range job.Spec.Template.Spec.InitContainers {
		for _, e := range c.Env {
			if e.Name == "GATE_VERDICT_TOKEN" || e.Name == "SYMPOZIUM_API_URL" {
				t.Errorf("container %s got %s with no API URL configured", c.Name, e.Name)
			}
		}
	}
}

// Secret names are derived from the run name, and run names are predictable,
// so a Secret can be planted at the name a future run will use. Adopting it
// would let whoever planted it approve that run's output.
func TestEnsureGateVerdictToken_ReplacesAPlantedSecret(t *testing.T) {
	run := gateTokenRun("demo-run", true)
	planted := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sympozium-run-gate-demo-run",
			Namespace: "default",
		},
		Data: map[string][]byte{GateVerdictSecretKey: []byte("attacker-chosen-token")},
	}
	r := newAgentRunTestReconciler(t, run, planted)

	if err := r.ensureGateVerdictToken(context.Background(), run); err != nil {
		t.Fatalf("ensureGateVerdictToken: %v", err)
	}

	got := gateSecret(t, r, "demo-run")
	if got == nil {
		t.Fatal("the Secret is gone; it should have been replaced, not just deleted")
	}
	token := got.StringData[GateVerdictSecretKey]
	if token == "" {
		token = string(got.Data[GateVerdictSecretKey])
	}
	if token == "attacker-chosen-token" {
		t.Error("the planted token was adopted as this run's credential")
	}
	owners := got.GetOwnerReferences()
	if len(owners) != 1 || owners[0].Name != "demo-run" {
		t.Errorf("replacement owner references = %+v, want a single AgentRun/demo-run", owners)
	}
}

// A Secret owned by a DIFFERENT run must not be adopted either — the run name
// is in the Secret name, but nothing stops an unrelated object sitting there.
func TestEnsureGateVerdictToken_ReplacesAnotherRunsSecret(t *testing.T) {
	run := gateTokenRun("demo-run", true)
	other := gateTokenRun("other-run", true)
	r := newAgentRunTestReconciler(t, run, other)

	// Mint for "other-run", then rename the Secret onto "demo-run"'s slot.
	if err := r.ensureGateVerdictToken(context.Background(), other); err != nil {
		t.Fatalf("seed: %v", err)
	}
	seeded := gateSecret(t, r, "other-run")
	if seeded == nil {
		t.Fatal("seed produced no Secret")
	}
	squatter := seeded.DeepCopy()
	squatter.ObjectMeta = metav1.ObjectMeta{
		Name:            "sympozium-run-gate-demo-run",
		Namespace:       "default",
		OwnerReferences: seeded.GetOwnerReferences(),
	}
	squatter.Data = map[string][]byte{GateVerdictSecretKey: []byte("other-runs-token")}
	if err := r.Create(context.Background(), squatter); err != nil {
		t.Fatalf("create squatter: %v", err)
	}

	if err := r.ensureGateVerdictToken(context.Background(), run); err != nil {
		t.Fatalf("ensureGateVerdictToken: %v", err)
	}

	got := gateSecret(t, r, "demo-run")
	if got == nil {
		t.Fatal("no Secret after replacement")
	}
	if string(got.Data[GateVerdictSecretKey]) == "other-runs-token" {
		t.Error("a Secret owned by another run was adopted")
	}
	owners := got.GetOwnerReferences()
	if len(owners) != 1 || owners[0].Name != "demo-run" {
		t.Errorf("owner references = %+v, want a single AgentRun/demo-run", owners)
	}
}

// The signing key is minted next to the token, and is a separate value: the
// agent holds one, the gate hook the other, and neither can stand in for the
// other's.
func TestEnsureGateVerdictToken_MintsADistinctAttemptKey(t *testing.T) {
	run := gateTokenRun("demo-run", true)
	r := newAgentRunTestReconciler(t, run)

	if err := r.ensureGateVerdictToken(context.Background(), run); err != nil {
		t.Fatalf("ensureGateVerdictToken: %v", err)
	}
	secret := gateSecret(t, r, "demo-run")
	if secret == nil {
		t.Fatal("no gate Secret was created")
	}
	key := secret.Data[GateAttemptKeySecretKey]
	if len(key) != gateVerdictTokenBytes*2 {
		t.Errorf("attempt key length = %d, want %d hex chars", len(key), gateVerdictTokenBytes*2)
	}
	if string(key) == string(secret.Data[GateVerdictSecretKey]) {
		t.Error("the attempt key equals the verdict token")
	}
}

// A run that minted its Secret before the signing key existed gets the key
// added, and keeps the token a hook may already hold.
func TestEnsureGateVerdictToken_AddsTheAttemptKeyToAnOlderSecret(t *testing.T) {
	run := gateTokenRun("demo-run", true)
	older := gateSecretFor(run)
	delete(older.Data, GateAttemptKeySecretKey)
	r := newAgentRunTestReconciler(t, run, older)

	if err := r.ensureGateVerdictToken(context.Background(), run); err != nil {
		t.Fatalf("ensureGateVerdictToken: %v", err)
	}
	got := gateSecret(t, r, "demo-run")
	if string(got.Data[GateVerdictSecretKey]) != "test-token" {
		t.Errorf("token = %q, want the original test-token kept", got.Data[GateVerdictSecretKey])
	}
	if len(got.Data[GateAttemptKeySecretKey]) == 0 {
		t.Error("no attempt key was added to the older Secret")
	}
}

// The reader applies the minter's rule: a Secret this run does not control is
// not its key, or whoever planted it could sign attempts and verdicts.
func TestGateAttemptKey_RefusesASecretTheRunDoesNotOwn(t *testing.T) {
	run := gateTokenRun("demo-run", true)
	planted := gateSecretFor(run)
	planted.OwnerReferences = nil
	r := newAgentRunTestReconciler(t, run, planted)

	if key, err := r.gateAttemptKey(context.Background(), run); err == nil {
		t.Fatalf("got key %q from a Secret the run does not own", key)
	}
}
