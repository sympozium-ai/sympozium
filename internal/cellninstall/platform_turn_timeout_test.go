package cellninstall

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/zeebo/blake3"
	"k8s.io/apimachinery/pkg/types"
)

// rewriteWorker edits a materialized backend's worker in both published files
// and re-seals the receipt, as a node configuring another cap would.
func rewriteWorker(t *testing.T, dir, backend string, native, catalogue func(capabilities, limits map[string]any)) {
	t.Helper()
	sub := filepath.Join(dir, backend)
	hashes := map[string]string{}
	for file, edit := range map[string]func(map[string]any){
		"native-template.json": func(doc map[string]any) {
			native(doc["worker"].(map[string]any)["capabilities"].(map[string]any), nil)
		},
		"catalogue.json": func(doc map[string]any) {
			catalogue(nil, doc["worker"].(map[string]any)["limits"].(map[string]any))
		},
	} {
		raw, err := os.ReadFile(filepath.Join(sub, file))
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		edit(doc)
		raw, _ = json.Marshal(doc)
		if err := os.WriteFile(filepath.Join(sub, file), raw, 0600); err != nil {
			t.Fatal(err)
		}
		hashes[file] = fmt.Sprintf("blake3:%x", blake3.Sum256(raw))
	}
	var configured map[string]any
	raw, _ := os.ReadFile(filepath.Join(sub, "configured.json"))
	if err := json.Unmarshal(raw, &configured); err != nil {
		t.Fatal(err)
	}
	configured["nativeTemplateHash"], configured["catalogueHash"] = hashes["native-template.json"], hashes["catalogue.json"]
	raw, _ = json.Marshal(configured)
	if err := os.WriteFile(filepath.Join(sub, "configured.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
}

// A backend with a larger output-token cap is configured with a longer turn.
// It is still the same package: its profile carries its own lifetime and the
// policy's turn ceiling admits the longest one.
func TestInstallPlatformAdmitsBackendsDifferingOnlyInTurnLifetime(t *testing.T) {
	ctx := context.Background()
	dir, packageHash, principal := starterConfiguration(t)
	writeBackendConfiguration(t, dir, testBackend{"slow", "llama-server", "openai-chat", "slow.gguf", "https://slow.example/v1/chat/completions", "trial-slow"})
	rewriteWorker(t, dir, "slow",
		func(capabilities, _ map[string]any) { capabilities["timeoutMs"] = 240000 },
		func(_, limits map[string]any) { limits["timeoutMillis"] = 240000 })
	store := platformInstallStore(t)
	o := PlatformOptions{Namespace: "tenant-a", ConfigurationDir: dir, OutputDir: filepath.Join(t.TempDir(), "out"), Scope: "trial", ClusterID: "cluster-uid", PackageHash: packageHash, Principal: principal, ControllerNamespace: "sympozium-system"}
	if err := InstallPlatform(ctx, store, o); err != nil {
		t.Fatalf("a longer turn lifetime was refused: %v", err)
	}
	_, policyName, _ := PlatformCatalogueNames("trial")
	var policy api.CellnExecutionPolicy
	if err := store.Get(ctx, types.NamespacedName{Name: policyName}, &policy); err != nil {
		t.Fatal(err)
	}
	if policy.Spec.Ceilings.MaxTurnSeconds != 240 {
		t.Fatalf("maxTurnSeconds = %d, want the longest backend's 240", policy.Spec.Ceilings.MaxTurnSeconds)
	}
	for backend, want := range map[string]int64{"native": 60000, "slow": 240000} {
		var profile api.CellnRuntimeProfile
		if err := store.Get(ctx, types.NamespacedName{Name: PlatformProfileName("trial", backend)}, &profile); err != nil {
			t.Fatal(err)
		}
		if profile.Spec.Limits.TimeoutMillis != want {
			t.Fatalf("%s timeoutMillis = %d, want %d", backend, profile.Spec.Limits.TimeoutMillis, want)
		}
	}
}

func TestInstallPlatformStillRefusesOtherWorkerDifferences(t *testing.T) {
	for name, edit := range map[string]struct {
		native, catalogue func(capabilities, limits map[string]any)
		want              string
	}{
		"another memory grant": {
			func(capabilities, _ map[string]any) { capabilities["memoryBytes"] = 134217728 },
			func(_, _ map[string]any) {},
			"one scope carries one package",
		},
		"a lifetime the catalogue does not state": {
			func(capabilities, _ map[string]any) { capabilities["timeoutMs"] = 240000 },
			func(_, _ map[string]any) {},
			"bounded lifetime matching its catalogue",
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir, packageHash, principal := starterConfiguration(t)
			rewriteWorker(t, dir, "claude", edit.native, edit.catalogue)
			o := PlatformOptions{Namespace: "tenant-a", ConfigurationDir: dir, OutputDir: filepath.Join(t.TempDir(), "out"), Scope: "trial", ClusterID: "cluster-uid", PackageHash: packageHash, Principal: principal, ControllerNamespace: "sympozium-system"}
			err := InstallPlatform(context.Background(), platformInstallStore(t), o)
			if err == nil || !strings.Contains(err.Error(), edit.want) {
				t.Fatalf("err = %v, want %q", err, edit.want)
			}
		})
	}
}

// Added to a running scope, the longer-lived backend raises the policy's turn
// ceiling; without that its runs would still be held to the old one.
func TestInstallPlatformRaisesTurnCeilingForAnAddedBackend(t *testing.T) {
	ctx := context.Background()
	dir, packageHash, principal := starterConfiguration(t)
	store := platformInstallStore(t)
	o := PlatformOptions{Namespace: "tenant-a", ConfigurationDir: dir, OutputDir: filepath.Join(t.TempDir(), "out"), Scope: "trial", ClusterID: "cluster-uid", PackageHash: packageHash, Principal: principal, ControllerNamespace: "sympozium-system"}
	if err := InstallPlatform(ctx, store, o); err != nil {
		t.Fatal(err)
	}
	writeBackendConfiguration(t, dir, testBackend{"slow", "llama-server", "openai-chat", "slow.gguf", "https://slow.example/v1/chat/completions", "trial-slow"})
	rewriteWorker(t, dir, "slow",
		func(capabilities, _ map[string]any) { capabilities["timeoutMs"] = 240000 },
		func(_, limits map[string]any) { limits["timeoutMillis"] = 240000 })
	if err := InstallPlatform(ctx, store, o); err != nil {
		t.Fatalf("rerun with a longer-lived backend refused: %v", err)
	}
	_, policyName, _ := PlatformCatalogueNames("trial")
	var policy api.CellnExecutionPolicy
	if err := store.Get(ctx, types.NamespacedName{Name: policyName}, &policy); err != nil {
		t.Fatal(err)
	}
	if policy.Spec.Ceilings.MaxTurnSeconds != 240 || len(policy.Spec.RuntimeProfiles) != 3 {
		t.Fatalf("policy after the added backend: %+v", policy.Spec.Ceilings)
	}
}
