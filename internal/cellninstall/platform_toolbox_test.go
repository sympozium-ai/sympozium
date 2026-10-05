package cellninstall

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnplatform"
	"github.com/zeebo/blake3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// withToolbox gives every backend's published catalogue the toolbox runtime a
// current starter package exports: the worker's shape with its own closure,
// mote and revision suffix.
func withToolbox(t *testing.T, dir, suffix string) {
	t.Helper()
	backends, err := ConfigurationBackends(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, backend := range backends {
		sub := filepath.Join(dir, backend)
		var cat map[string]any
		raw, _ := os.ReadFile(filepath.Join(sub, "catalogue.json"))
		if err := json.Unmarshal(raw, &cat); err != nil {
			t.Fatal(err)
		}
		toolbox := map[string]any{}
		for k, v := range cat["worker"].(map[string]any) {
			toolbox[k] = v
		}
		toolbox["revision"] = fmt.Sprint(toolbox["revision"]) + suffix
		toolbox["closure"] = map[string]any{"hash": "blake3:" + strings.Repeat("c", 64)}
		toolbox["mote"] = map[string]any{"hash": "blake3:" + strings.Repeat("d", 64)}
		cat["toolbox"] = toolbox
		raw, _ = json.Marshal(cat)
		if err := os.WriteFile(filepath.Join(sub, "catalogue.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
		var configured map[string]any
		receipt, _ := os.ReadFile(filepath.Join(sub, "configured.json"))
		if err := json.Unmarshal(receipt, &configured); err != nil {
			t.Fatal(err)
		}
		configured["catalogueHash"] = fmt.Sprintf("blake3:%x", blake3.Sum256(raw))
		receipt, _ = json.Marshal(configured)
		if err := os.WriteFile(filepath.Join(sub, "configured.json"), receipt, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

// A package exporting a toolbox gets one toolbox profile per backend beside
// the backend's own: the same reviewed runtime material with the toolbox's
// closure and mote, recording its tools in catalogue order. A namespace gets
// its runtime wrapper alone, an Agent with its own key selects it with
// exactly those tools, and a package replacement rebinds it to the new
// toolbox, never to the tool-free profile.
func TestInstallPlatformPublishesEachBackendsToolbox(t *testing.T) {
	ctx := context.Background()
	dir, packageHash, principal := starterConfiguration(t)
	withToolbox(t, dir, "")
	store := platformInstallStore(t)
	// The default backend is mediation-only: its sample is the starter
	// Agent's, on the toolbox.
	if err := store.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: fleetNamespace}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: fleetNamespace, Name: FleetModelCredentialSecret}, Data: map[string][]byte{"native": []byte(MediatedBackendCredential), "claude": []byte("a-fleet-key-from-an-earlier-install")}}); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "out")
	o := PlatformOptions{Namespace: "tenant-a", ConfigurationDir: dir, OutputDir: out, Scope: "trial", ClusterID: "cluster-uid", PackageHash: packageHash, Principal: principal, ControllerNamespace: "sympozium-system"}
	for range 2 {
		if err := InstallPlatform(ctx, store, o); err != nil {
			t.Fatal(err)
		}
	}
	var cat catalogue
	if _, err := read(filepath.Join(dir, "native", "catalogue.json"), &cat); err != nil {
		t.Fatal(err)
	}
	_, policyName, toolName := PlatformCatalogueNames("trial")
	var ordered []api.ClusterCellnToolRef
	for _, entry := range cat.Tools {
		ordered = append(ordered, api.ClusterCellnToolRef{Name: toolName(entry.Name), Revision: entry.Spec.Revision})
	}
	for _, backend := range []string{"native", "claude"} {
		var base, toolbox api.CellnRuntimeProfile
		if err := store.Get(ctx, types.NamespacedName{Name: PlatformProfileName("trial", backend)}, &base); err != nil {
			t.Fatal(err)
		}
		name := PlatformProfileName("trial", backend) + ".toolbox"
		if err := store.Get(ctx, types.NamespacedName{Name: name}, &toolbox); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		tools, err := cellnplatform.ToolboxTools(&toolbox)
		if err != nil || !slices.Equal(tools, ordered) || !cellnplatform.Toolbox(&toolbox) || cellnplatform.Toolbox(&base) || cellnplatform.Backend(&toolbox) != backend || cellnplatform.MediationOnly(&toolbox) != cellnplatform.MediationOnly(&base) {
			t.Fatalf("%s labels or tools: %v %v %v", name, err, toolbox.Labels, tools)
		}
		if toolbox.Spec.Closure != cat.Toolbox.Closure || toolbox.Spec.Mote != cat.Toolbox.Mote || base.Spec.Closure != cat.Worker.Closure || toolbox.Annotations[packageAnnotation] != packageHash {
			t.Fatalf("%s artifacts: %+v", name, toolbox.Spec)
		}
		// Everything but the runtime artifacts is the backend's own.
		same := toolbox.Spec.DeepCopy()
		same.Closure, same.Mote = base.Spec.Closure, base.Spec.Mote
		if !reflect.DeepEqual(*same, base.Spec) {
			t.Fatalf("%s differs from its backend beyond the toolbox artifacts", name)
		}
		var wrapper api.AgentRuntime
		wrapperName := cellnplatform.WrapperNames(backend).Runtime + ".toolbox"
		if err := store.Get(ctx, types.NamespacedName{Namespace: "tenant-a", Name: wrapperName}, &wrapper); err != nil || wrapper.Spec.CellnProfileRef.Name != name || wrapper.Labels[cellnplatform.ToolboxLabel] != "true" {
			t.Fatalf("%s wrapper: %v %+v", wrapperName, err, wrapper)
		}
		if _, err := cellnplatform.TenantWrappers("tenant-a", &toolbox, &api.CellnExecutionPolicy{}); err == nil {
			t.Fatalf("%s offered a shared Agent and host-profile connection", name)
		}
	}
	var policy api.CellnExecutionPolicy
	if err := store.Get(ctx, types.NamespacedName{Name: policyName}, &policy); err != nil || len(policy.Spec.RuntimeProfiles) != 4 {
		t.Fatalf("policy does not admit both toolboxes: %v %+v", err, policy.Spec.RuntimeProfiles)
	}
	var agents api.AgentList
	if err := store.List(ctx, &agents); err != nil || slices.ContainsFunc(agents.Items, func(a api.Agent) bool { return strings.Contains(a.Name, "toolbox") }) {
		t.Fatalf("a toolbox got a shared Agent: %v", agents.Items)
	}
	raw, _ := os.ReadFile(filepath.Join(out, "run.json"))
	var run api.AgentRun
	if err := json.Unmarshal(raw, &run); err != nil || run.Spec.CellnSelection.RuntimeRef != "celln-native.toolbox" || !slices.Equal(run.Spec.CellnSelection.ClusterToolRefs, ordered) || run.Spec.AgentRef != StarterAgentName {
		t.Fatalf("the mediated sample does not lend the toolbox: %v %+v", err, run.Spec.CellnSelection)
	}

	// An Agent with its own key, in any admitted namespace.
	profile, tools, err := cellnplatform.OwnKeySelection(ctx, store, "tenant-b", PlatformProfileName("trial", "claude"))
	if err != nil || profile != PlatformProfileName("trial", "claude")+".toolbox" || !slices.Equal(tools, ordered) {
		t.Fatalf("own-key selection: %v %q %v", err, profile, tools)
	}
	runtime, err := cellnplatform.EnsureRuntimeWrapper(ctx, store, "tenant-b", profile)
	if err != nil || runtime != "celln-claude.toolbox" {
		t.Fatalf("own-key runtime wrapper: %v %q", err, runtime)
	}

	// A replacement package rebinds every toolbox wrapper to its new toolbox.
	newPackage := "blake3:" + strings.Repeat("b", 64)
	repackage(t, dir, newPackage)
	withToolbox(t, dir, "-next")
	o.PackageHash, o.Replacing = newPackage, FleetPublication{Exists: true, Package: packageHash, Scope: "trial"}
	if err := InstallPlatform(ctx, store, o); err != nil {
		t.Fatalf("approved replacement refused: %v", err)
	}
	for _, key := range []types.NamespacedName{{Namespace: "tenant-a", Name: "celln-claude.toolbox"}, {Namespace: "tenant-b", Name: "celln-claude.toolbox"}, {Namespace: "tenant-a", Name: "celln-claude"}} {
		var wrapper api.AgentRuntime
		want := PlatformProfileName("trial", "claude")
		if strings.HasSuffix(key.Name, ".toolbox") {
			want += ".toolbox"
		}
		if err := store.Get(ctx, key, &wrapper); err != nil || wrapper.Spec.CellnProfileRef.Name != want || !strings.HasSuffix(wrapper.Spec.CellnProfileRef.Revision, "-next") {
			t.Fatalf("%s not rebound to %s: %v %+v", key, want, err, wrapper.Spec.CellnProfileRef)
		}
	}
}

// A package without a toolbox publishes none, and an Agent with its own key
// then lends no tools: the tool-free runtime serves none on the mediated path.
func TestOwnKeySelectionWithoutToolboxLendsNothing(t *testing.T) {
	ctx := context.Background()
	dir, packageHash, principal := starterConfiguration(t)
	store := platformInstallStore(t)
	o := PlatformOptions{Namespace: "tenant-a", ConfigurationDir: dir, OutputDir: filepath.Join(t.TempDir(), "out"), Scope: "trial", ClusterID: "cluster-uid", PackageHash: packageHash, Principal: principal, ControllerNamespace: "sympozium-system"}
	if err := InstallPlatform(ctx, store, o); err != nil {
		t.Fatal(err)
	}
	var toolbox api.CellnRuntimeProfile
	if err := store.Get(ctx, types.NamespacedName{Name: PlatformProfileName("trial", "native") + ".toolbox"}, &toolbox); err == nil {
		t.Fatal("a toolbox was published for a package without one")
	}
	profile, tools, err := cellnplatform.OwnKeySelection(ctx, store, "tenant-b", PlatformProfileName("trial", "native"))
	if err != nil || profile != PlatformProfileName("trial", "native") || tools != nil {
		t.Fatalf("own-key selection without a toolbox: %v %q %v", err, profile, tools)
	}
	if _, _, err := cellnplatform.OwnKeySelection(ctx, store, "kube-system", PlatformProfileName("trial", "native")); err == nil {
		t.Fatal("own-key selection outside the policy")
	}
}
