package cellninstall

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fmt"
	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnparent"
	"github.com/zeebo/blake3"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestPartitionRequiresExplicitNonOverlappingScope(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want bool
	}{
		{nil, false}, {[]string{"--leader-elect"}, false},
		{[]string{"--exclude-watch-namespaces=celln-agents"}, true},
		{[]string{"--exclude-watch-namespaces", "other,celln-agents"}, true},
		{[]string{"--watch-namespace=other"}, true},
		{[]string{"--watch-namespace=celln-agents"}, false},
		{[]string{"--exclude-watch-namespaces=celln-agents", "--exclude-watch-namespaces=other"}, false},
		{[]string{"--watch-namespace=other", "--exclude-watch-namespaces=celln-agents"}, false},
	} {
		if got := Partitioned(tc.args, "celln-agents"); got != tc.want {
			t.Fatalf("%v: %v", tc.args, got)
		}
	}
}

func TestInstallationPublishesBoundConfigurationWithoutSubmittingRun(t *testing.T) {
	for _, mode := range []string{"success", "rollout", "hash", "changed-catalogue", "existing-output", "existing-resource"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			cfg := filepath.Join(dir, "config")
			if err := os.Mkdir(cfg, 0700); err != nil {
				t.Fatal(err)
			}
			var metadata receipt
			for _, name := range []string{"catalogue.json", "native-template.json", "configured.json"} {
				raw, err := os.ReadFile(filepath.Join("testdata", name))
				if err != nil {
					t.Fatal(err)
				}
				// Test fixtures have a final newline; production receipt binds exact bytes.
				raw = []byte(strings.TrimSuffix(string(raw), "\n"))
				if name == "configured.json" {
					if err := json.Unmarshal(raw, &metadata); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(filepath.Join(cfg, name), raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			for file, field := range map[string]*string{"catalogue.json": &metadata.CatalogueHash, "native-template.json": &metadata.NativeTemplateHash} {
				raw, _ := os.ReadFile(filepath.Join(cfg, file))
				*field = fmt.Sprintf("blake3:%x", blake3.Sum256(raw))
			}
			raw, _ := json.Marshal(metadata)
			_ = os.WriteFile(filepath.Join(cfg, "configured.json"), raw, 0600)
			replicas := int32(1)
			deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "sympozium-controller-manager", Namespace: "sympozium-system", Generation: 1}, Spec: appsv1.DeploymentSpec{Replicas: &replicas, Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "manager", Args: []string{"--exclude-watch-namespaces=celln-agents"}}}}}}, Status: appsv1.DeploymentStatus{ObservedGeneration: 1, Replicas: 1, UpdatedReplicas: 1, AvailableReplicas: 1}}
			if mode == "rollout" {
				deployment.Status.UpdatedReplicas = 0
			}
			scheme := runtime.NewScheme()
			_ = api.AddToScheme(scheme)
			_ = corev1.AddToScheme(scheme)
			_ = appsv1.AddToScheme(scheme)
			builder := fake.NewClientBuilder().WithScheme(scheme).WithObjects(deployment).WithInterceptorFuncs(interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				obj.SetUID(types.UID("installed-" + obj.GetName()))
				obj.SetGeneration(1)
				return c.Create(ctx, obj, opts...)
			}})
			if mode == "existing-resource" {
				builder = builder.WithObjects(&api.AgentRuntime{ObjectMeta: metav1.ObjectMeta{Name: "celln-native", Namespace: "celln-agents", UID: "existing-owner"}})
			}
			store := builder.Build()
			output := filepath.Join(dir, "output")
			o := Options{Namespace: "celln-agents", ControllerNamespace: "sympozium-system", ConfigurationDir: cfg, OutputDir: output, StatePath: "/var/lib/sympozium-celln/starter", OwnerTarget: "https://192.0.2.1:19443", Scope: "installation-test", PackageHash: metadata.PackageHash}
			if mode == "hash" {
				o.PackageHash = "blake3:" + strings.Repeat("0", 64)
			}
			if mode == "changed-catalogue" {
				_ = os.WriteFile(filepath.Join(cfg, "catalogue.json"), []byte("{}"), 0600)
			}
			if mode == "existing-output" {
				_ = os.Mkdir(output, 0700)
			}
			err := Install(ctx, store, o)
			var runs api.AgentRunList
			if e := store.List(ctx, &runs); e != nil || len(runs.Items) != 0 {
				t.Fatalf("installer submitted work: %v", e)
			}
			if mode != "success" {
				if err == nil {
					t.Fatal("unsafe installation accepted")
				}
				if _, e := os.Stat(filepath.Join(output, "installed.json")); !os.IsNotExist(e) {
					t.Fatal("failed installation marked complete")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var registration cellnparent.RegistrationConfig
			if _, err := read(filepath.Join(output, "registrations.json"), &registration); err != nil {
				t.Fatal(err)
			}
			if registration.LocalProvisioner == nil || len(registration.HostTemplates) != 1 || len(registration.Registrations) != 0 || registration.OperatorSource.Name == registration.RuntimeSource.Name {
				t.Fatal("incorrect independent registration/grant binding")
			}
			var run api.AgentRun
			if _, err := read(filepath.Join(output, "run.json"), &run); err != nil {
				t.Fatal(err)
			}
			if run.Spec.ValidateLifecycle() != "" || len(run.Spec.CellnSelection.ToolRefs) != 3 {
				t.Fatalf("invalid generated run: %s", run.Spec.ValidateLifecycle())
			}
			var cm corev1.ConfigMapList
			_ = store.List(ctx, &cm, client.InNamespace(o.Namespace))
			if len(cm.Items) != 3 {
				t.Fatal("three independent grant layers required")
			}
		})
	}
}
