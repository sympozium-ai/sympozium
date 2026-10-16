package controller

import (
	"context"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// ── Writer-token Secret and memory-server env ────────────────────────────────

// getWriterSecret fetches a writer-token Secret and checks it holds a
// 32-byte hex token owned by ownerKind/ownerName.
func getWriterSecret(t *testing.T, cl client.Client, name, ownerKind, ownerName string) string {
	t.Helper()
	var secret corev1.Secret
	if err := cl.Get(context.Background(), types.NamespacedName{Name: name, Namespace: "default"}, &secret); err != nil {
		t.Fatalf("get writer secret %q: %v", name, err)
	}
	token := string(secret.Data[memoryWriterTokenKey])
	if raw, err := hex.DecodeString(token); err != nil || len(raw) != 32 {
		t.Errorf("token %q is not 32 random bytes in hex", token)
	}
	owner := metav1.GetControllerOf(&secret)
	if owner == nil || owner.Kind != ownerKind || owner.Name != ownerName {
		t.Errorf("controller owner = %+v, want %s/%s", owner, ownerKind, ownerName)
	}
	return token
}

// memoryServerEnv returns the named env var on the memory-server container of
// a Deployment.
func memoryServerEnv(t *testing.T, cl client.Client, deployName, envName string) (corev1.EnvVar, bool) {
	t.Helper()
	var deploy appsv1.Deployment
	if err := cl.Get(context.Background(), types.NamespacedName{Name: deployName, Namespace: "default"}, &deploy); err != nil {
		t.Fatalf("get deployment %q: %v", deployName, err)
	}
	c := memoryServerContainer(&deploy.Spec.Template.Spec)
	if c == nil {
		t.Fatal("memory-server container not found")
	}
	for _, e := range c.Env {
		if e.Name == envName {
			return e, true
		}
	}
	return corev1.EnvVar{}, false
}

func TestReconcileMemoryDeployment_CreatesWriterToken(t *testing.T) {
	instance := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "default"},
		Spec: sympoziumv1alpha1.AgentSpec{
			Skills: []sympoziumv1alpha1.SkillRef{{SkillPackRef: "memory"}},
		},
	}
	r, cl := newInstanceTestReconciler(t, instance)

	if err := r.reconcileMemoryDeployment(context.Background(), logr.Discard(), instance); err != nil {
		t.Fatalf("reconcileMemoryDeployment: %v", err)
	}
	token := getWriterSecret(t, cl, "agent-memory-writer-token", "Agent", "agent")

	env, ok := memoryServerEnv(t, cl, "agent-memory", memoryWriterTokenEnvName)
	if !ok {
		t.Fatal("memory-server container has no MEMORY_WRITER_TOKEN")
	}
	ref := env.ValueFrom.SecretKeyRef
	if ref.Name != "agent-memory-writer-token" || ref.Key != memoryWriterTokenKey || env.Value != "" {
		t.Errorf("MEMORY_WRITER_TOKEN = %+v, want a SecretKeyRef to agent-memory-writer-token/token", env)
	}
	if ref.Optional != nil && *ref.Optional {
		t.Error("the writer token reference must be required, so the server cannot start without it")
	}

	// A second reconcile keeps the token: rotating it would break running pods.
	if err := r.reconcileMemoryDeployment(context.Background(), logr.Discard(), instance); err != nil {
		t.Fatalf("second reconcileMemoryDeployment: %v", err)
	}
	if again := getWriterSecret(t, cl, "agent-memory-writer-token", "Agent", "agent"); again != token {
		t.Error("reconcile replaced an existing writer token")
	}
}

// TestReconcileMemoryDeployment_AddsWriterTokenOnExisting is the upgrade path:
// a memory Deployment created by an older release gets the Secret and env.
func TestReconcileMemoryDeployment_AddsWriterTokenOnExisting(t *testing.T) {
	instance := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "default"},
		Spec: sympoziumv1alpha1.AgentSpec{
			Skills: []sympoziumv1alpha1.SkillRef{{SkillPackRef: "memory"}},
		},
	}
	deploy := memoryDeploy("agent-memory", "default", []corev1.EnvVar{
		{Name: "MEMORY_DB_PATH", Value: "/data/memory.db"},
	})
	r, cl := newInstanceTestReconciler(t, instance, deploy)

	if err := r.reconcileMemoryDeployment(context.Background(), logr.Discard(), instance); err != nil {
		t.Fatalf("reconcileMemoryDeployment: %v", err)
	}
	getWriterSecret(t, cl, "agent-memory-writer-token", "Agent", "agent")
	if _, ok := memoryServerEnv(t, cl, "agent-memory", memoryWriterTokenEnvName); !ok {
		t.Error("existing memory Deployment did not pick up MEMORY_WRITER_TOKEN on reconcile")
	}
}

func TestReconcileSharedMemory_WriterToken(t *testing.T) {
	pack := &sympoziumv1alpha1.Ensemble{
		ObjectMeta: metav1.ObjectMeta{Name: "crew", Namespace: "default"},
		Spec: sympoziumv1alpha1.EnsembleSpec{
			SharedMemory: &sympoziumv1alpha1.SharedMemorySpec{Enabled: true},
		},
	}
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "crew-shared-memory-db", Namespace: "default"},
	}
	deploy := memoryDeploy("crew-shared-memory", "default", []corev1.EnvVar{
		{Name: "MEMORY_DB_PATH", Value: "/data/memory.db"},
	})
	agentR, cl := newInstanceTestReconciler(t, pack, pvc, deploy)
	r := &EnsembleReconciler{Client: cl, Scheme: agentR.Scheme, Log: logr.Discard()}

	if err := r.reconcileSharedMemory(context.Background(), logr.Discard(), pack); err != nil {
		t.Fatalf("reconcileSharedMemory: %v", err)
	}
	getWriterSecret(t, cl, "crew-shared-memory-writer-token", "Ensemble", "crew")
	env, ok := memoryServerEnv(t, cl, "crew-shared-memory", memoryWriterTokenEnvName)
	if !ok || env.ValueFrom.SecretKeyRef.Name != "crew-shared-memory-writer-token" {
		t.Errorf("shared memory-server MEMORY_WRITER_TOKEN = %+v, want a ref to crew-shared-memory-writer-token", env)
	}
}

func TestCleanup_DeletesWriterTokenSecrets(t *testing.T) {
	writerSecret := func(name string) *corev1.Secret {
		return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}}
	}
	instance := &sympoziumv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "default"}}
	pack := &sympoziumv1alpha1.Ensemble{ObjectMeta: metav1.ObjectMeta{Name: "crew", Namespace: "default"}}
	agentR, cl := newInstanceTestReconciler(t, instance, pack,
		writerSecret("agent-memory-writer-token"), writerSecret("crew-shared-memory-writer-token"))

	if err := agentR.cleanupMemoryDeployment(context.Background(), instance); err != nil {
		t.Fatalf("cleanupMemoryDeployment: %v", err)
	}
	ensembleR := &EnsembleReconciler{Client: cl, Scheme: agentR.Scheme, Log: logr.Discard()}
	if err := ensembleR.cleanupSharedMemory(context.Background(), logr.Discard(), pack); err != nil {
		t.Fatalf("cleanupSharedMemory: %v", err)
	}

	for _, name := range []string{"agent-memory-writer-token", "crew-shared-memory-writer-token"} {
		var s corev1.Secret
		if err := cl.Get(context.Background(), types.NamespacedName{Name: name, Namespace: "default"}, &s); !errors.IsNotFound(err) {
			t.Errorf("secret %s still exists after cleanup (err = %v)", name, err)
		}
	}
}

// ── Run pods: the token reaches the agent container only ────────────────────

// tokenEnvNames are the env vars that carry memory tokens.
var tokenEnvNames = map[string]bool{
	memoryWriterTokenEnvName:         true,
	workflowMemoryWriterTokenEnvName: true,
	"MEMORY_ADMIN_TOKEN":             true,
}

// assertNoMemoryTokens fails if container holds a memory token, by env name or
// by any reference to a writer-token or admin Secret.
func assertNoMemoryTokens(t *testing.T, c corev1.Container, adminSecret string) {
	t.Helper()
	isTokenSecret := func(name string) bool {
		return strings.HasSuffix(name, "-writer-token") || name == adminSecret
	}
	for _, e := range c.Env {
		if tokenEnvNames[e.Name] {
			t.Errorf("container %q has %s", c.Name, e.Name)
		}
		if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil && isTokenSecret(e.ValueFrom.SecretKeyRef.Name) {
			t.Errorf("container %q env %s reads token Secret %s", c.Name, e.Name, e.ValueFrom.SecretKeyRef.Name)
		}
	}
	for _, src := range c.EnvFrom {
		if src.SecretRef != nil && isTokenSecret(src.SecretRef.Name) {
			t.Errorf("container %q loads token Secret %s via envFrom", c.Name, src.SecretRef.Name)
		}
	}
}

func envNamed(c corev1.Container, name string) (corev1.EnvVar, bool) {
	for _, e := range c.Env {
		if e.Name == name {
			return e, true
		}
	}
	return corev1.EnvVar{}, false
}

func TestBuildContainers_WriterTokenOnlyInAgentContainer(t *testing.T) {
	// Enable adminDelete too: the admin token must never reach a run pod.
	const adminSecret = "sympozium-memory-admin-token"
	t.Setenv("MEMORY_ADMIN_TOKEN_SECRET", adminSecret)

	r := &AgentRunReconciler{}
	run := newTestRun()
	run.Spec.Skills = []sympoziumv1alpha1.SkillRef{{SkillPackRef: "memory"}, {SkillPackRef: "k8s-ops"}}
	sidecars := []resolvedSidecar{{
		skillPackName: "k8s-ops",
		sidecar:       sympoziumv1alpha1.SkillSidecar{Image: "ghcr.io/sympozium-ai/sympozium/skill-k8s-ops:latest"},
	}}

	containers, initContainers, err := r.buildContainers(run, false, nil, sidecars, nil, nil)
	if err != nil {
		t.Fatalf("buildContainers: %v", err)
	}

	var sawAgent, sawSidecar bool
	for _, c := range containers {
		if c.Name != agentContainerName {
			sawSidecar = sawSidecar || c.Name == "skill-k8s-ops"
			assertNoMemoryTokens(t, c, adminSecret)
			continue
		}
		sawAgent = true
		env, ok := envNamed(c, memoryWriterTokenEnvName)
		want := memoryWriterTokenEnv(memoryWriterTokenEnvName, "my-instance-memory")
		if !ok || !equality.Semantic.DeepEqual(env, want) {
			t.Errorf("agent MEMORY_WRITER_TOKEN = %+v, want %+v", env, want)
		}
		if _, ok := envNamed(c, "MEMORY_ADMIN_TOKEN"); ok {
			t.Error("agent container must never get MEMORY_ADMIN_TOKEN")
		}
	}
	if !sawAgent || !sawSidecar {
		t.Fatalf("expected an agent container and the k8s-ops sidecar, got %d containers", len(containers))
	}
	for _, c := range initContainers {
		assertNoMemoryTokens(t, c, adminSecret)
	}
}

func TestInjectSharedMemory_WriterTokenForReadWritePersonasOnly(t *testing.T) {
	pack := &sympoziumv1alpha1.Ensemble{
		ObjectMeta: metav1.ObjectMeta{Name: "crew", Namespace: "default"},
		Spec: sympoziumv1alpha1.EnsembleSpec{
			SharedMemory: &sympoziumv1alpha1.SharedMemorySpec{
				Enabled:     true,
				AccessRules: []sympoziumv1alpha1.SharedMemoryAccessRule{{AgentConfig: "reader", Access: "read-only"}},
			},
		},
	}
	persona := func(name, config string) *sympoziumv1alpha1.Agent {
		return &sympoziumv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "default", Labels: map[string]string{"sympozium.ai/agent-config": config},
		}}
	}
	r := newAgentRunTestReconciler(t, pack, persona("crew-writer", "writer"), persona("crew-reader", "reader"))

	for _, tc := range []struct {
		agentRef  string
		wantToken bool
	}{
		{"crew-writer", true},
		{"crew-reader", false},
	} {
		run := newTestRun()
		run.Labels = map[string]string{"sympozium.ai/ensemble": "crew"}
		run.Spec.AgentRef = tc.agentRef
		podSpec := corev1.PodSpec{Containers: []corev1.Container{
			{Name: agentContainerName},
			{Name: "skill-k8s-ops"},
		}}

		r.injectSharedMemory(context.Background(), run, &podSpec)

		agent := podSpec.Containers[0]
		env, ok := envNamed(agent, workflowMemoryWriterTokenEnvName)
		if ok != tc.wantToken {
			t.Errorf("%s: has WORKFLOW_MEMORY_WRITER_TOKEN = %v, want %v", tc.agentRef, ok, tc.wantToken)
		}
		if ok && env.ValueFrom.SecretKeyRef.Name != "crew-shared-memory-writer-token" {
			t.Errorf("%s: token ref = %s, want crew-shared-memory-writer-token", tc.agentRef, env.ValueFrom.SecretKeyRef.Name)
		}
		assertNoMemoryTokens(t, podSpec.Containers[1], "")
		for _, c := range podSpec.InitContainers {
			assertNoMemoryTokens(t, c, "")
		}
	}
}

// ── The controller's own write: failure memories ────────────────────────────

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPersistFailureMemory_SendsWriterToken(t *testing.T) {
	var got []*http.Request
	old := memoryStoreClient
	memoryStoreClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		got = append(got, r)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"success":true}`)), Header: http.Header{}}, nil
	})}
	defer func() { memoryStoreClient = old }()

	run := newTestRun()
	run.Spec.Skills = []sympoziumv1alpha1.SkillRef{{SkillPackRef: "memory"}}

	// Without the Secret the controller skips the write instead of sending an
	// unauthenticated one.
	r := newAgentRunTestReconciler(t)
	r.persistFailureMemory(context.Background(), logr.Discard(), run, "Job failed")
	if len(got) != 0 {
		t.Fatalf("sent %d requests without a writer token Secret, want 0", len(got))
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "my-instance-memory-writer-token", Namespace: "default"},
		Data:       map[string][]byte{memoryWriterTokenKey: []byte("tok123")},
	}
	r = newAgentRunTestReconciler(t, secret)
	r.persistFailureMemory(context.Background(), logr.Discard(), run, "Job failed")
	if len(got) != 1 {
		t.Fatalf("sent %d requests, want 1", len(got))
	}
	if got[0].URL.Path != "/store" || got[0].Header.Get("Authorization") != "Bearer tok123" {
		t.Errorf("request = %s %s with Authorization %q, want POST /store with Bearer tok123",
			got[0].Method, got[0].URL.Path, got[0].Header.Get("Authorization"))
	}
}

// ── Harness mode: the agent container is not agent-runner ───────────────────
//
// In mode: harness the agent container runs an operator-supplied harness
// image instead of agent-runner, and harnesses commonly run model-chosen shell
// commands. The writer token must not be in its env, or the model could read
// it and write to or forget from memory under any agent's name.

func TestBuildContainers_NoWriterTokenInHarnessMode(t *testing.T) {
	r := &AgentRunReconciler{}
	run := harnessModeRun(nil)
	run.Spec.Skills = []sympoziumv1alpha1.SkillRef{{SkillPackRef: "memory"}}

	containers, initContainers, err := r.buildContainers(run, false, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildContainers: %v", err)
	}
	agent := containerByName(containers, agentContainerName)
	if agent == nil || agent.Image != harnessTestImage {
		t.Fatalf("expected the agent container to run the harness image, got %+v", agent)
	}
	for _, c := range append(containers, initContainers...) {
		assertNoMemoryTokens(t, c, "")
	}
}

func TestInjectSharedMemory_NoWriterTokenInHarnessMode(t *testing.T) {
	pack := &sympoziumv1alpha1.Ensemble{
		ObjectMeta: metav1.ObjectMeta{Name: "crew", Namespace: "default"},
		Spec: sympoziumv1alpha1.EnsembleSpec{
			SharedMemory: &sympoziumv1alpha1.SharedMemorySpec{Enabled: true},
		},
	}
	writer := &sympoziumv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{
		Name: "crew-writer", Namespace: "default", Labels: map[string]string{"sympozium.ai/agent-config": "writer"},
	}}
	r := newAgentRunTestReconciler(t, pack, writer)

	run := harnessModeRun(nil)
	run.Labels = map[string]string{"sympozium.ai/ensemble": "crew"}
	run.Spec.AgentRef = "crew-writer" // read-write: would get the token as agent-runner
	podSpec := corev1.PodSpec{Containers: []corev1.Container{{Name: agentContainerName, Image: harnessTestImage}}}

	r.injectSharedMemory(context.Background(), run, &podSpec)

	assertNoMemoryTokens(t, podSpec.Containers[0], "")
}
