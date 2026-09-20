package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	cap "github.com/sympozium-ai/sympozium/internal/cellncapability"
	"github.com/sympozium-ai/sympozium/internal/cellninstall"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// The chart's celln.mediation mode renders gateway.json and the bootstrap
// publishes the keyset; both must be what this binary's strict loaders accept.
func TestChartMediationConfigurationAndBootstrapKeysetLoad(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm required for chart rendering")
	}
	raw, err := exec.Command("helm", "template", "test", "../../charts/sympozium", "--show-only", "templates/model-gateway.yaml",
		"-f", "../../charts/testdata/celln-fleet-values.yaml", "-f", "../../charts/testdata/celln-mediation-values.yaml").CombinedOutput()
	if err != nil {
		t.Fatalf("render: %v: %s", err, raw)
	}
	var rendered string
	for _, document := range bytes.Split(raw, []byte("\n---")) {
		var cm corev1.ConfigMap
		if err := yaml.Unmarshal(document, &cm); err == nil && cm.Kind == "ConfigMap" {
			rendered = cm.Data["gateway.json"]
		}
	}
	var cfg config
	if err := cap.StrictDecode([]byte(rendered), &cfg); err != nil {
		t.Fatalf("gateway refuses the rendered configuration: %v\n%s", err, rendered)
	}
	if cfg.ClusterID != "evaluation-cluster" || cfg.Issuer != cellninstall.MediationIssuer || cfg.Listen != ":8443" || cfg.TLSCertificateFile == "" || cfg.TLSKeyFile == "" || cfg.RegistrationTokenFile == "" || cfg.DatabaseURLFile == "" ||
		len(cfg.ReadinessNamespaces) != 1 || cfg.ReadinessNamespaces[0] != "team-a" || !strings.HasSuffix(cfg.VerificationKeysFile, "/jwks.json") {
		t.Fatalf("rendered configuration incomplete: %+v", cfg)
	}

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	store := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "sympozium-system"}}, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "celln-system"}}).Build()
	gateway, receiver := cellninstall.DefaultMediationHosts("sympozium", "sympozium-system")
	trust, err := cellninstall.PrepareMediationTrust(t.Context(), store, cellninstall.MediationOptions{ClusterID: "evaluation-cluster", SystemNamespace: "sympozium-system", GatewayHosts: gateway, ReceiverHosts: receiver})
	if err != nil {
		t.Fatal(err)
	}
	var published corev1.ConfigMap
	if err := store.Get(t.Context(), types.NamespacedName{Namespace: "sympozium-system", Name: cellninstall.MediationTrustConfigMap}, &published); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "jwks.json")
	if err := os.WriteFile(path, []byte(published.Data["jwks.json"]), 0o644); err != nil {
		t.Fatal(err)
	}
	keys, err := loadKeys(path)
	if err != nil || len(keys) != 1 || keys[0].KeyID != trust.KeyID {
		t.Fatalf("gateway refuses the bootstrapped keyset: %v %v", keys, err)
	}
	if _, err := cap.NewVerifier(cfg.Issuer, keys, nil); err != nil {
		t.Fatal(err)
	}
}
