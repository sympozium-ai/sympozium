package cellninstall

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
)

// setHostLimits rewrites the host limits every backend's nodes published, as
// a configuration with other --celln-fleet-max-* flags would.
func setHostLimits(t *testing.T, dir string, limits FleetLimits) {
	t.Helper()
	backends, err := ConfigurationBackends(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, backend := range backends {
		path := filepath.Join(dir, backend, "configured.json")
		var configured map[string]any
		raw, _ := os.ReadFile(path)
		if err := json.Unmarshal(raw, &configured); err != nil {
			t.Fatal(err)
		}
		configured["hostLimits"] = map[string]any{"leaseSeconds": limits.LeaseSeconds, "maxTurns": limits.MaxTurns, "maxModelRequests": limits.MaxModelRequests, "maxOutputTokens": limits.MaxOutputTokens}
		raw, _ = json.Marshal(configured)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

// A scope's ceilings were sized for the package it was configured with. When
// the scope moves to a package whose turns reserve more, the policy's
// ceilings are rewritten from the new configuration's host limits; a rerun of
// an unchanged package never rewrites them.
func TestInstallPlatformRewritesCeilingsOnlyOnPackageReplacement(t *testing.T) {
	ctx := context.Background()
	dir, oldPackage, principal := starterConfiguration(t)
	// What a fleet installed before the per-turn allowance doubled carries.
	setHostLimits(t, dir, FleetLimits{LeaseSeconds: 86400, MaxTurns: 256, MaxModelRequests: 768, MaxOutputTokens: 393216})
	store := platformInstallStore(t)
	o := PlatformOptions{Namespace: "tenant-a", ConfigurationDir: dir, OutputDir: filepath.Join(t.TempDir(), "out"), Scope: "trial", ClusterID: "cluster-uid", PackageHash: oldPackage, Principal: principal, ControllerNamespace: "sympozium-system"}
	if err := InstallPlatform(ctx, store, o); err != nil {
		t.Fatal(err)
	}
	_, policyName, _ := PlatformCatalogueNames("trial")
	ceilings := func() api.CellnExecutionPolicyCeilings {
		t.Helper()
		var policy api.CellnExecutionPolicy
		if err := store.Get(ctx, types.NamespacedName{Name: policyName}, &policy); err != nil {
			t.Fatal(err)
		}
		return policy.Spec.Ceilings
	}
	if got := ceilings(); got.MaxTurns != 256 || got.MaxModelRequests != 768 || got.MaxOutputTokens != 393216 {
		t.Fatalf("installed ceilings: %+v", got)
	}
	if afforded := TurnsAfforded(768, 393216, api.TurnModelRequests, api.TurnOutputTokens); afforded != 128 {
		t.Fatalf("old ceilings afford %d turns at the current allowance, want 128", afforded)
	}

	// The same package with other host limits: the published policy stands.
	current, err := FleetLimits{}.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	setHostLimits(t, dir, current)
	if err := InstallPlatform(ctx, store, o); err != nil {
		t.Fatal(err)
	}
	if got := ceilings(); got.MaxModelRequests != 768 || got.MaxOutputTokens != 393216 {
		t.Fatalf("a rerun of an unchanged package rewrote the ceilings: %+v", got)
	}

	// An approved move to a new package takes the ceilings the installer's
	// limits configured on the nodes.
	newPackage := "blake3:" + strings.Repeat("b", 64)
	repackage(t, dir, newPackage)
	o.PackageHash, o.Replacing = newPackage, FleetPublication{Exists: true, Package: oldPackage, Scope: "trial"}
	if err := InstallPlatform(ctx, store, o); err != nil {
		t.Fatalf("approved replacement refused: %v", err)
	}
	got := ceilings()
	if got.MaxTurns != 256 || got.MaxModelRequests != 1536 || got.MaxOutputTokens != 786432 || got.MaxParentLeaseSeconds != 86400 {
		t.Fatalf("replacement kept the old ceilings: %+v", got)
	}
	if TurnsAfforded(got.MaxModelRequests, got.MaxOutputTokens, api.TurnModelRequests, api.TurnOutputTokens) != got.MaxTurns {
		t.Fatalf("rewritten ceilings do not afford their turns: %+v", got)
	}
}
