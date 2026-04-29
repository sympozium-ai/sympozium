package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

func newTestModelCR(name string) *sympoziumv1alpha1.Model {
	return &sympoziumv1alpha1.Model{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
		Spec: sympoziumv1alpha1.ModelCRDSpec{
			Source: sympoziumv1alpha1.ModelSource{
				URL:      "https://example.com/model.gguf",
				Filename: "model.gguf",
			},
		},
	}
}

// ── Fix 1: Model pods must have security contexts ────────────────────────────

func TestModelDownloadJob_HasPodSecurityContext(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = batchv1.AddToScheme(scheme)
	_ = sympoziumv1alpha1.AddToScheme(scheme)

	model := newTestModelCR("test-model")
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "model-test-model", Namespace: "default"},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pvc).Build()
	r := &ModelReconciler{Client: cl, Scheme: scheme, Log: logr.Discard()}

	if err := r.ensureDownloadJob(context.Background(), model, logr.Discard()); err != nil {
		t.Fatalf("ensureDownloadJob: %v", err)
	}

	var job batchv1.Job
	if err := cl.Get(context.Background(), types.NamespacedName{
		Name: "model-test-model-download", Namespace: "default",
	}, &job); err != nil {
		t.Fatalf("get job: %v", err)
	}

	psc := job.Spec.Template.Spec.SecurityContext
	if psc == nil {
		t.Fatal("pod security context is nil on download job")
	}
	if psc.RunAsNonRoot == nil || !*psc.RunAsNonRoot {
		t.Error("download job: RunAsNonRoot should be true")
	}
	if psc.RunAsUser == nil || *psc.RunAsUser != 1000 {
		t.Errorf("download job: RunAsUser = %v, want 1000", psc.RunAsUser)
	}
	if psc.SeccompProfile == nil || psc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Error("download job: seccomp profile should be RuntimeDefault")
	}
}

func TestModelDownloadJob_ContainerSecurityContext(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = batchv1.AddToScheme(scheme)
	_ = sympoziumv1alpha1.AddToScheme(scheme)

	model := newTestModelCR("test-model")
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "model-test-model", Namespace: "default"},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pvc).Build()
	r := &ModelReconciler{Client: cl, Scheme: scheme, Log: logr.Discard()}

	if err := r.ensureDownloadJob(context.Background(), model, logr.Discard()); err != nil {
		t.Fatalf("ensureDownloadJob: %v", err)
	}

	var job batchv1.Job
	if err := cl.Get(context.Background(), types.NamespacedName{
		Name: "model-test-model-download", Namespace: "default",
	}, &job); err != nil {
		t.Fatalf("get job: %v", err)
	}

	container := job.Spec.Template.Spec.Containers[0]
	sc := container.SecurityContext
	if sc == nil {
		t.Fatal("download container security context is nil")
	}
	if sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
		t.Error("download container: AllowPrivilegeEscalation should be false")
	}
	if sc.Capabilities == nil || len(sc.Capabilities.Drop) == 0 {
		t.Error("download container: should drop ALL capabilities")
	} else if sc.Capabilities.Drop[0] != "ALL" {
		t.Errorf("download container: should drop ALL, got %v", sc.Capabilities.Drop)
	}
}

func TestModelDeployment_HasPodSecurityContext(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = batchv1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	_ = sympoziumv1alpha1.AddToScheme(scheme)

	model := newTestModelCR("test-model")
	cl := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &ModelReconciler{Client: cl, Scheme: scheme, Log: logr.Discard()}

	if err := r.ensureDeployment(context.Background(), model, logr.Discard()); err != nil {
		t.Fatalf("ensureDeployment: %v", err)
	}

	var deploy appsv1.Deployment
	if err := cl.Get(context.Background(), types.NamespacedName{
		Name: "model-test-model", Namespace: "default",
	}, &deploy); err != nil {
		t.Fatalf("get deployment: %v", err)
	}

	psc := deploy.Spec.Template.Spec.SecurityContext
	if psc == nil {
		t.Fatal("deployment pod security context is nil")
	}
	if psc.RunAsNonRoot == nil || !*psc.RunAsNonRoot {
		t.Error("model deployment: RunAsNonRoot should be true")
	}
	if psc.RunAsUser == nil || *psc.RunAsUser != 1000 {
		t.Errorf("model deployment: RunAsUser = %v, want 1000", psc.RunAsUser)
	}

	// Check container security context
	container := deploy.Spec.Template.Spec.Containers[0]
	sc := container.SecurityContext
	if sc == nil {
		t.Fatal("model deployment container security context is nil")
	}
	if sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
		t.Error("model deployment container: AllowPrivilegeEscalation should be false")
	}
	if sc.Capabilities == nil || len(sc.Capabilities.Drop) == 0 || sc.Capabilities.Drop[0] != "ALL" {
		t.Error("model deployment container: should drop ALL capabilities")
	}
}

// ── Fix 8: SHA256 checksum in download script ────────────────────────────────

func TestModelDownloadJob_SHA256Verification(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = batchv1.AddToScheme(scheme)
	_ = sympoziumv1alpha1.AddToScheme(scheme)

	model := newTestModelCR("checksum-model")
	model.Spec.Source.SHA256 = "abc123def456abc123def456abc123def456abc123def456abc123def456abcd"

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "model-checksum-model", Namespace: "default"},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pvc).Build()
	r := &ModelReconciler{Client: cl, Scheme: scheme, Log: logr.Discard()}

	if err := r.ensureDownloadJob(context.Background(), model, logr.Discard()); err != nil {
		t.Fatalf("ensureDownloadJob: %v", err)
	}

	var job batchv1.Job
	if err := cl.Get(context.Background(), types.NamespacedName{
		Name: "model-checksum-model-download", Namespace: "default",
	}, &job); err != nil {
		t.Fatalf("get job: %v", err)
	}

	script := job.Spec.Template.Spec.Containers[0].Command[2]
	if !strings.Contains(script, "sha256sum") {
		t.Error("download script should contain sha256sum verification when SHA256 is set")
	}
	if !strings.Contains(script, model.Spec.Source.SHA256) {
		t.Error("download script should contain the expected checksum")
	}
	if !strings.Contains(script, "Checksum mismatch") {
		t.Error("download script should fail on checksum mismatch")
	}
}

func TestModelDownloadJob_NoSHA256_NoVerification(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = batchv1.AddToScheme(scheme)
	_ = sympoziumv1alpha1.AddToScheme(scheme)

	model := newTestModelCR("no-checksum-model")
	// No SHA256 set

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "model-no-checksum-model", Namespace: "default"},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pvc).Build()
	r := &ModelReconciler{Client: cl, Scheme: scheme, Log: logr.Discard()}

	if err := r.ensureDownloadJob(context.Background(), model, logr.Discard()); err != nil {
		t.Fatalf("ensureDownloadJob: %v", err)
	}

	var job batchv1.Job
	if err := cl.Get(context.Background(), types.NamespacedName{
		Name: "model-no-checksum-model-download", Namespace: "default",
	}, &job); err != nil {
		t.Fatalf("get job: %v", err)
	}

	script := job.Spec.Template.Spec.Containers[0].Command[2]
	if strings.Contains(script, "sha256sum") {
		t.Error("download script should NOT contain sha256sum when SHA256 is not set")
	}
}

// ── Helper function unit tests ───────────────────────────────────────────────

func TestModelPodSecurityContext(t *testing.T) {
	psc := modelPodSecurityContext()

	if psc.RunAsNonRoot == nil || !*psc.RunAsNonRoot {
		t.Error("RunAsNonRoot should be true")
	}
	if psc.RunAsUser == nil || *psc.RunAsUser != 1000 {
		t.Errorf("RunAsUser = %v, want 1000", psc.RunAsUser)
	}
	if psc.FSGroup == nil || *psc.FSGroup != 1000 {
		t.Errorf("FSGroup = %v, want 1000", psc.FSGroup)
	}
	if psc.SeccompProfile == nil || psc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Error("SeccompProfile should be RuntimeDefault")
	}
}

func TestModelContainerSecurityContext(t *testing.T) {
	sc := modelContainerSecurityContext()

	if sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
		t.Error("AllowPrivilegeEscalation should be false")
	}
	if sc.Capabilities == nil {
		t.Fatal("Capabilities should not be nil")
	}
	if len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" {
		t.Errorf("should drop ALL capabilities, got %v", sc.Capabilities.Drop)
	}
}
