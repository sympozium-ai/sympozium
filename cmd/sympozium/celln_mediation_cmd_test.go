package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sympozium-ai/sympozium/internal/cellninstall"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCellnMediationBootstrapPrintsCredentialFreeValues(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	previous := k8sClient
	k8sClient = fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "sympozium-system"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "celln-system"}},
	).Build()
	defer func() { k8sClient = previous }()

	cmd := newCellnMediationCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"bootstrap"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("bootstrap ran without --cluster-id")
	}

	out := filepath.Join(t.TempDir(), "values.yaml")
	cmd = newCellnMediationCmd()
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"bootstrap", "--cluster-id", "evaluation", "--values-out", out})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"enabled: true", `clusterId: "evaluation"`, "name: sympozium-control-plane", "keyId: \"mediation-", "controllerSecret: " + cellninstall.MediationControllerSecret} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("values lack %q:\n%s", want, raw)
		}
	}
	var controller corev1.Secret
	if err := k8sClient.Get(t.Context(), types.NamespacedName{Namespace: "sympozium-system", Name: cellninstall.MediationControllerSecret}, &controller); err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{string(raw), stdout.String(), stderr.String()} {
		for key, value := range controller.Data {
			if strings.Contains(output, string(value)) || strings.Contains(output, "PRIVATE KEY") {
				t.Fatalf("command output carries %s", key)
			}
		}
	}
	if !strings.Contains(stderr.String(), "published new") {
		t.Fatalf("unexpected report: %s", stderr.String())
	}
}

// apply-routes takes no route input: it publishes only what the chart
// recorded, and says why when there is nothing to work from.
func TestCellnMediationApplyRoutesNeedsAFleetAndARecord(t *testing.T) {
	previous := k8sClient
	defer func() { k8sClient = previous }()
	for _, tc := range []struct {
		name    string
		objects []client.Object
		want    string
	}{
		{name: "no fleet", want: "no Celln fleet is installed"},
		{name: "mediation off", objects: []client.Object{configureDaemonSet()}, want: "mediation is not enabled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k8sClient = preflightClient(t, tc.objects...)
			cmd := newCellnMediationCmd()
			var stdout bytes.Buffer
			cmd.SetOut(&stdout)
			cmd.SetErr(&stdout)
			cmd.SetArgs([]string{"apply-routes"})
			if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("apply-routes: %v", err)
			}
			if cmd.Flags().Lookup("route") != nil {
				t.Fatal("apply-routes must not accept routes of its own")
			}
		})
	}
}
