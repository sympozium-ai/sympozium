package controller

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// newChannelTestReconciler builds an AgentReconciler with a scheme that
// includes appsv1 (Deployments) so reconcileChannels can create them.
func newChannelTestReconciler(t *testing.T, objs ...client.Object) (*AgentReconciler, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1: %v", err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add appsv1: %v", err)
	}
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add sympozium scheme: %v", err)
	}
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&sympoziumv1alpha1.Agent{}).
		Build()
	return &AgentReconciler{
		Client: cl,
		Scheme: scheme,
		Log:    logr.Discard(),
	}, cl
}

// ── channelMountsCSI ─────────────────────────────────────────────────────────

func TestChannelMountsCSI(t *testing.T) {
	cases := []struct {
		name string
		ch   sympoziumv1alpha1.ChannelSpec
		want bool
	}{
		{
			name: "no volumes",
			ch:   sympoziumv1alpha1.ChannelSpec{Type: "slack"},
			want: false,
		},
		{
			name: "non-CSI volume",
			ch: sympoziumv1alpha1.ChannelSpec{
				Type: "slack",
				Volumes: []corev1.Volume{
					{Name: "data", VolumeSource: corev1.VolumeSource{
						EmptyDir: &corev1.EmptyDirVolumeSource{},
					}},
				},
			},
			want: false,
		},
		{
			name: "CSI volume present",
			ch: sympoziumv1alpha1.ChannelSpec{
				Type: "slack",
				Volumes: []corev1.Volume{
					{Name: "vault", VolumeSource: corev1.VolumeSource{
						CSI: &corev1.CSIVolumeSource{Driver: "secrets-store.csi.k8s.io"},
					}},
				},
			},
			want: true,
		},
		{
			name: "mixed: CSI among others",
			ch: sympoziumv1alpha1.ChannelSpec{
				Type: "slack",
				Volumes: []corev1.Volume{
					{Name: "data", VolumeSource: corev1.VolumeSource{
						EmptyDir: &corev1.EmptyDirVolumeSource{},
					}},
					{Name: "vault", VolumeSource: corev1.VolumeSource{
						CSI: &corev1.CSIVolumeSource{Driver: "secrets-store.csi.k8s.io"},
					}},
				},
			},
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := channelMountsCSI(tc.ch); got != tc.want {
				t.Errorf("channelMountsCSI = %v, want %v", got, tc.want)
			}
		})
	}
}

// ── reconcileChannels: secret existence gating ───────────────────────────────

// When a channel has no CSI volume and the configRef Secret is missing, the
// reconciler must NOT create the channel Deployment and must surface an Error
// channel status.
func TestReconcileChannels_BlocksWhenSecretMissingAndNoCSI(t *testing.T) {
	instance := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "inst", Namespace: "ns"},
		Spec: sympoziumv1alpha1.AgentSpec{
			Channels: []sympoziumv1alpha1.ChannelSpec{
				{
					Type:      "slack",
					ConfigRef: sympoziumv1alpha1.SecretRef{Secret: "missing-secret"},
				},
			},
		},
	}

	r, cl := newChannelTestReconciler(t, instance)

	if err := r.reconcileChannels(context.Background(), instance); err != nil {
		t.Fatalf("reconcileChannels: %v", err)
	}

	if len(instance.Status.Channels) != 1 {
		t.Fatalf("expected 1 channel status, got %d", len(instance.Status.Channels))
	}
	st := instance.Status.Channels[0]
	if st.Status != "Error" {
		t.Errorf("status = %q, want Error", st.Status)
	}

	// No deployment should have been created.
	var deploy appsv1.Deployment
	err := cl.Get(context.Background(), types.NamespacedName{
		Name:      "inst-channel-slack",
		Namespace: "ns",
	}, &deploy)
	if !errors.IsNotFound(err) {
		t.Errorf("expected deployment to be absent, got err=%v", err)
	}
}

// When the channel mounts a CSI volume the reconciler must proceed to create
// the channel Deployment even though the configRef Secret does not yet exist
// — the SPC will materialize it on first mount.
func TestReconcileChannels_AllowsMissingSecretWhenCSIVolumePresent(t *testing.T) {
	instance := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "inst", Namespace: "ns"},
		Spec: sympoziumv1alpha1.AgentSpec{
			Channels: []sympoziumv1alpha1.ChannelSpec{
				{
					Type:      "slack",
					ConfigRef: sympoziumv1alpha1.SecretRef{Secret: "vault-managed"},
					Volumes: []corev1.Volume{
						{Name: "vault", VolumeSource: corev1.VolumeSource{
							CSI: &corev1.CSIVolumeSource{Driver: "secrets-store.csi.k8s.io"},
						}},
					},
				},
			},
		},
	}

	r, cl := newChannelTestReconciler(t, instance)

	if err := r.reconcileChannels(context.Background(), instance); err != nil {
		t.Fatalf("reconcileChannels: %v", err)
	}

	if len(instance.Status.Channels) != 1 {
		t.Fatalf("expected 1 channel status, got %d", len(instance.Status.Channels))
	}
	if instance.Status.Channels[0].Status == "Error" {
		t.Errorf("status = Error %q, want Pending/Connected", instance.Status.Channels[0].Message)
	}

	// Deployment should now exist.
	var deploy appsv1.Deployment
	if err := cl.Get(context.Background(), types.NamespacedName{
		Name:      "inst-channel-slack",
		Namespace: "ns",
	}, &deploy); err != nil {
		t.Fatalf("expected deployment to be created, got: %v", err)
	}
}

// ── ensureChannelServiceAccount ──────────────────────────────────────────────

func TestEnsureChannelServiceAccount_CreatesWhenMissing(t *testing.T) {
	r, cl := newChannelTestReconciler(t)

	if err := r.ensureChannelServiceAccount(context.Background(), "ns"); err != nil {
		t.Fatalf("ensureChannelServiceAccount: %v", err)
	}

	var sa corev1.ServiceAccount
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "sympozium-channel", Namespace: "ns"}, &sa); err != nil {
		t.Fatalf("get sa: %v", err)
	}
	if sa.Labels["app.kubernetes.io/managed-by"] != "sympozium" {
		t.Errorf("missing managed-by label, got: %v", sa.Labels)
	}
}

func TestEnsureChannelServiceAccount_NoopWhenPresent(t *testing.T) {
	existing := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sympozium-channel",
			Namespace: "ns",
			Labels:    map[string]string{"existing": "true"},
		},
	}
	r, cl := newChannelTestReconciler(t, existing)

	if err := r.ensureChannelServiceAccount(context.Background(), "ns"); err != nil {
		t.Fatalf("ensureChannelServiceAccount: %v", err)
	}

	var sa corev1.ServiceAccount
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "sympozium-channel", Namespace: "ns"}, &sa); err != nil {
		t.Fatalf("get sa: %v", err)
	}
	if sa.Labels["existing"] != "true" {
		t.Errorf("ensureChannelServiceAccount overwrote existing SA: labels=%v", sa.Labels)
	}
}

// reconcileChannels must create the SA before it creates any channel Deployment.
func TestReconcileChannels_CreatesServiceAccount(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "tg-secret", Namespace: "ns"},
	}
	instance := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "inst", Namespace: "ns"},
		Spec: sympoziumv1alpha1.AgentSpec{
			Channels: []sympoziumv1alpha1.ChannelSpec{
				{Type: "telegram", ConfigRef: sympoziumv1alpha1.SecretRef{Secret: "tg-secret"}},
			},
		},
	}
	r, cl := newChannelTestReconciler(t, instance, secret)

	if err := r.reconcileChannels(context.Background(), instance); err != nil {
		t.Fatalf("reconcileChannels: %v", err)
	}

	var sa corev1.ServiceAccount
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "sympozium-channel", Namespace: "ns"}, &sa); err != nil {
		t.Fatalf("expected sympozium-channel SA to be created: %v", err)
	}
}

// ── buildChannelDeployment: SA wiring ────────────────────────────────────────

func TestBuildChannelDeployment_UsesChannelServiceAccount(t *testing.T) {
	r := &AgentReconciler{}
	instance := newTestInstance()
	deploy := r.buildChannelDeployment(instance, instance.Spec.Channels[0], "test-instance-channel-telegram")
	if got := deploy.Spec.Template.Spec.ServiceAccountName; got != "sympozium-channel" {
		t.Errorf("ServiceAccountName = %q, want sympozium-channel", got)
	}
}
