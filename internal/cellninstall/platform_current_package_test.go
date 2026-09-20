package cellninstall

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnparent"
	"github.com/zeebo/blake3"
	"k8s.io/apimachinery/pkg/types"
)

// currentPackage rewrites the testdata configuration (an older starter
// package: 3 model requests a turn, one tool call, a 2048-byte task) into
// what the current package publishes.
func currentPackage(t *testing.T, dir string) {
	t.Helper()
	backends, err := ConfigurationBackends(dir)
	if err != nil {
		t.Fatal(err)
	}
	rewrite := func(path string, change func(map[string]any)) string {
		var document map[string]any
		raw, _ := os.ReadFile(path)
		if err := json.Unmarshal(raw, &document); err != nil {
			t.Fatal(err)
		}
		change(document)
		raw, _ = json.Marshal(document)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("blake3:%x", blake3.Sum256(raw))
	}
	for _, backend := range backends {
		sub := filepath.Join(dir, backend)
		catalogueHash := rewrite(filepath.Join(sub, "catalogue.json"), func(cat map[string]any) {
			worker := cat["worker"].(map[string]any)
			worker["json"] = map[string]any{"maxTurns": 6, "maxCalls": 4}
			worker["limits"].(map[string]any)["taskBytes"] = api.MaxWorkerTaskBytes
		})
		nativeHash := rewrite(filepath.Join(sub, "native-template.json"), func(native map[string]any) {
			native["turnModelRequests"], native["turnOutputTokens"] = api.TurnModelRequests, api.TurnOutputTokens
			native["maxTurns"], native["totalModelRequests"], native["totalOutputTokens"] = DefaultFleetLimits.MaxTurns, DefaultFleetLimits.MaxModelRequests, DefaultFleetLimits.MaxOutputTokens
		})
		rewrite(filepath.Join(sub, "configured.json"), func(configured map[string]any) {
			configured["catalogueHash"], configured["nativeTemplateHash"] = catalogueHash, nativeHash
		})
	}
	setHostLimits(t, dir, DefaultFleetLimits)
}

// The current starter package reports a 16384-byte worker task, six model
// requests and four tool calls a turn. The installer publishes that verbatim:
// the profile's taskBytes is what later widens a continued conversation's
// seed, and the default ceilings afford every turn its whole allowance.
func TestInstallPlatformPublishesTheCurrentPackagesBounds(t *testing.T) {
	ctx := context.Background()
	dir, packageHash, principal := starterConfiguration(t)
	currentPackage(t, dir)
	store := platformInstallStore(t)
	o := PlatformOptions{Namespace: "tenant-a", ConfigurationDir: dir, OutputDir: filepath.Join(t.TempDir(), "out"), Scope: "trial", ClusterID: "cluster-uid", PackageHash: packageHash, Principal: principal, ControllerNamespace: "sympozium-system"}
	if err := InstallPlatform(ctx, store, o); err != nil {
		t.Fatal(err)
	}
	var profile api.CellnRuntimeProfile
	if err := store.Get(ctx, types.NamespacedName{Name: PlatformProfileName("trial", "native")}, &profile); err != nil {
		t.Fatal(err)
	}
	if profile.Spec.Limits.TaskBytes != 16384 || profile.Spec.JSON == nil || profile.Spec.JSON.MaxTurns != 6 || profile.Spec.JSON.MaxCalls != 4 || profile.Spec.Native.TurnModelRequests != 6 || profile.Spec.Native.TurnOutputTokens != 3072 {
		t.Fatalf("profile bounds: %+v %+v", profile.Spec.Limits, profile.Spec.JSON)
	}
	if got := cellnparent.SeedBudget(profile.Spec.Limits.TaskBytes); got != 15872 {
		t.Fatalf("seed budget on the current package: %d", got)
	}
	_, policyName, _ := PlatformCatalogueNames("trial")
	var policy api.CellnExecutionPolicy
	if err := store.Get(ctx, types.NamespacedName{Name: policyName}, &policy); err != nil {
		t.Fatal(err)
	}
	c := policy.Spec.Ceilings
	if c.MaxTurns != 256 || c.MaxModelRequests != 1536 || c.MaxOutputTokens != 786432 || TurnsAfforded(c.MaxModelRequests, c.MaxOutputTokens, profile.Spec.Native.TurnModelRequests, profile.Spec.Native.TurnOutputTokens) != c.MaxTurns {
		t.Fatalf("ceilings: %+v", c)
	}
	// The session a new conversation asks for affords its 64 turns too.
	session := SessionDefaults(api.EnduringRunSpec{LeaseSeconds: int32(c.MaxParentLeaseSeconds), MaxTurns: int32(c.MaxTurns), MaxModelRequests: int32(c.MaxModelRequests), MaxOutputTokens: c.MaxOutputTokens})
	if session.MaxTurns != 64 || TurnsAfforded(int64(session.MaxModelRequests), session.MaxOutputTokens, 6, 3072) != 64 {
		t.Fatalf("session defaults: %+v", session)
	}
}

// A backend that allows 4096 output tokens per request publishes a per-turn
// allowance of 24576. The sample run the installer writes asks for the turns
// the default ceilings pay for at that allowance, not 64 it could never take.
func TestInstallPlatformSizesTheSampleRunForItsBackendsAllowance(t *testing.T) {
	ctx := context.Background()
	dir, packageHash, principal := starterConfiguration(t)
	currentPackage(t, dir)
	backends, err := ConfigurationBackends(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, backend := range backends {
		var native, configured map[string]any
		path := filepath.Join(dir, backend, "native-template.json")
		raw, _ := os.ReadFile(path)
		if err := json.Unmarshal(raw, &native); err != nil {
			t.Fatal(err)
		}
		native["turnOutputTokens"] = api.MaxTurnOutputTokens
		raw, _ = json.Marshal(native)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		path = filepath.Join(dir, backend, "configured.json")
		rawConfigured, _ := os.ReadFile(path)
		if err := json.Unmarshal(rawConfigured, &configured); err != nil {
			t.Fatal(err)
		}
		configured["nativeTemplateHash"] = fmt.Sprintf("blake3:%x", blake3.Sum256(raw))
		rawConfigured, _ = json.Marshal(configured)
		if err := os.WriteFile(path, rawConfigured, 0600); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(t.TempDir(), "out")
	o := PlatformOptions{Namespace: "tenant-a", ConfigurationDir: dir, OutputDir: out, Scope: "trial", ClusterID: "cluster-uid", PackageHash: packageHash, Principal: principal, ControllerNamespace: "sympozium-system"}
	if err := InstallPlatform(ctx, platformInstallStore(t), o); err != nil {
		t.Fatal(err)
	}
	var run api.AgentRun
	raw, err := os.ReadFile(filepath.Join(out, "run.json"))
	if err != nil || json.Unmarshal(raw, &run) != nil || run.Spec.Enduring == nil {
		t.Fatalf("sample run: %v %s", err, raw)
	}
	if e := run.Spec.Enduring; e.MaxTurns != 32 || e.MaxModelRequests != 192 || e.MaxOutputTokens != 786432 || run.Spec.ValidateLifecycle() != "" {
		t.Fatalf("sample run budget: %+v", e)
	}
}
