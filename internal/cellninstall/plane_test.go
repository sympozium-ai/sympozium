package cellninstall

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sympozium-ai/sympozium/internal/cellnparent"
	"github.com/zeebo/blake3"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestManagedPlaneCredentialAndSharedRootBinding(t *testing.T) {
	for _, mode := range []string{"valid", "wrong-token", "wrong-principal", "wrong-root", "missing-model", "different-origin"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			o := Options{StatePath: dir, OutputDir: filepath.Join(dir, "installation"), ControllerNamespace: "sympozium-system", OwnerTarget: ManagedRouterURL}
			p := PlaneOptions{NodeName: "kvm-a", OwnerTokenFile: filepath.Join(dir, "owner-token")}
			for _, sub := range []string{"authority/trusted-parent-models", "authority/parent-issuance", "authority/trusted-parent-permits", "authority/trusted-parent-launches", "approvals", "journal", "installation"} {
				if err := os.MkdirAll(filepath.Join(dir, sub), 0700); err != nil {
					t.Fatal(err)
				}
			}
			const token = "parent-only-high-entropy-test-credential"
			write := func(path string, bytes []byte) {
				t.Helper()
				if err := os.WriteFile(path, bytes, 0600); err != nil {
					t.Fatal(err)
				}
			}
			write(p.OwnerTokenFile, []byte(token))
			model := []byte(`{"credentialFile":"/etc/celln/model-key-must-not-be-read"}`)
			modelHash := fmt.Sprintf("blake3:%x", blake3.Sum256(model))
			if mode != "missing-model" {
				write(filepath.Join(dir, "authority/trusted-parent-models", strings.TrimPrefix(modelHash, "blake3:")+".json"), model)
			}
			registration := cellnparent.RegistrationConfig{
				Approvals: filepath.Join(dir, "approvals"), Journal: filepath.Join(dir, "journal"),
				LocalProvisioner: &cellnparent.LocalProvisioner{Root: filepath.Join(dir, "authority")},
				HostTemplates:    []cellnparent.HostProvisionTemplate{{Principal: "operator", Native: cellnparent.NativeProvisionConfig{ModelProfile: modelHash}}},
			}
			if mode == "wrong-root" {
				registration.LocalProvisioner.Root = "/another/root"
			}
			if mode == "different-origin" {
				o.OwnerTarget = "http://another-owner:8787"
			}
			if mode == "wrong-token" {
				write(p.OwnerTokenFile, []byte("another-parent-credential-at-least-24"))
			}
			if mode == "wrong-principal" {
				registration.HostTemplates[0].Principal = "another-operator"
			}
			raw, _ := json.Marshal(registration)
			write(filepath.Join(o.OutputDir, "registrations.json"), raw)
			write(filepath.Join(dir, "authority/trusted-parent-clients.json"), []byte(fmt.Sprintf(`{"apiVersion":"celln.parent-clients/v1","clients":[{"principal":"operator","tokenHash":"blake3:%x"}]}`, blake3.Sum256([]byte(token)))))
			scheme := runtime.NewScheme()
			_ = corev1.AddToScheme(scheme)
			store := fake.NewClientBuilder().WithScheme(scheme).Build()
			values, err := ConfigurePlane(ctx, store, o, p)
			if mode != "valid" {
				if err == nil {
					t.Fatal("invalid authority accepted")
				}
				var secrets corev1.SecretList
				if err := store.List(ctx, &secrets); err != nil || len(secrets.Items) != 0 {
					t.Fatal("failed preflight published partial authority")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var controller, router corev1.Secret
			if err := store.Get(ctx, types.NamespacedName{Namespace: o.ControllerNamespace, Name: "celln-parent-config"}, &controller); err != nil {
				t.Fatal(err)
			}
			if err := store.Get(ctx, types.NamespacedName{Namespace: "celln-system", Name: "celln-router-parent"}, &router); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(controller.Data["registrations.json"]), token) || strings.Contains(strings.Join(values, "\n"), token) || string(router.Data["token"]) != token {
				t.Fatal("credential boundary violated")
			}
			if err := json.Unmarshal(controller.Data["registrations.json"], &registration); err != nil {
				t.Fatal(err)
			}
			if registration.LocalProvisioner.TokenFile != "/etc/sympozium/celln/token" || registration.LocalProvisioner.Target != ManagedRouterURL || registration.LocalProvisioner.CAFile != "" {
				t.Fatal("parent did not join shared connection")
			}
			if _, err := ConfigurePlane(ctx, store, o, p); err == nil {
				t.Fatal("existing installation silently replaced")
			}
		})
	}
}
