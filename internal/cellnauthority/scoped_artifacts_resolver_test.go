package cellnauthority

import (
	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
	"strings"
	"testing"
)

func TestScopedArtifactResolverExplicitPolicySemantics(t *testing.T) {
	for _, mode := range []string{"catalogue-ceiling", "attenuated", "omitted-capability", "different-operation", "one-shot", "argv", "invalid-effects", "mediated-append", "one-shot-list", "mediated-https", "one-shot-https", "https-effects"} {
		t.Run(mode, func(t *testing.T) {
			f := newPlatformFixture(t, "artifact-tenant", true)
			var run api.AgentRun
			if err := f.client.Get(t.Context(), f.runKey, &run); err != nil {
				t.Fatal(err)
			}
			oneShot := strings.HasPrefix(mode, "one-shot")
			if !oneShot {
				run.Spec.ExecutionLifecycle = "enduring"
				run.Spec.Enduring = &api.EnduringRunSpec{LeaseSeconds: 240, MaxTurns: 4, MaxModelRequests: 8, MaxOutputTokens: 4096}
			}
			if err := f.client.Update(t.Context(), &run); err != nil {
				t.Fatal(err)
			}
			var tool api.ClusterCellnTool
			if err := f.client.Get(t.Context(), types.NamespacedName{Name: "workspace-write-v1"}, &tool); err != nil {
				t.Fatal(err)
			}
			tool.Spec.Limits = artifactLimits()
			if mode == "argv" {
				tool.Spec.InvocationABI = "celln.argv/v1"
			}
			if mode == "mediated-append" {
				tool.Spec.Limits.Artifacts.Operation = "append"
			}
			if mode == "one-shot-list" {
				tool.Spec.Limits.Artifacts.Operation, tool.Spec.Limits.Effects = "list", "none"
			}
			if strings.HasSuffix(mode, "https") || mode == "https-effects" {
				tool.Spec.Limits = httpsLimits()
				tool.Spec.EntryPoint = "/https-fetch"
			}
			if mode == "https-effects" {
				tool.Spec.Limits.Effects = "none"
			}
			if mode == "invalid-effects" {
				tool.Spec.Limits.Effects = "none"
			}
			if err := f.client.Update(t.Context(), &tool); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"a-policy", "z-policy"} {
				var policy api.CellnExecutionPolicy
				if err := f.client.Get(t.Context(), types.NamespacedName{Name: name}, &policy); err != nil {
					t.Fatal(err)
				}
				policy.Spec.Tools[0].Limits = nil
				if mode == "attenuated" || mode == "omitted-capability" || mode == "different-operation" {
					l := artifactLimits()
					l.Artifacts.MaxOperations = 2
					if mode == "omitted-capability" {
						l.Artifacts = nil
					}
					if mode == "different-operation" {
						l.Artifacts.Operation = "read"
						l.Effects = "none"
					}
					policy.Spec.Tools[0].Limits = &l
				}
				if err := f.client.Update(t.Context(), &policy); err != nil {
					t.Fatal(err)
				}
			}
			request := platformRequest(f)
			if !oneShot {
				request.ParentIncarnation = "blake3:" + strings.Repeat("f", 64)
			}
			resolution, err := f.resolver.Resolve(t.Context(), f.runKey, request)
			// A mediated route now carries the fleet's whole starter toolbox:
			// all six run-data operations and the web tools, for one-shot and
			// enduring runs alike. Effects pairing and explicit policy stay.
			allowed := map[string]bool{"catalogue-ceiling": true, "attenuated": true, "one-shot": true, "mediated-append": true, "one-shot-list": true, "mediated-https": true, "one-shot-https": true}[mode]
			if (err == nil) != allowed {
				t.Fatalf("allowed=%v error=%v", allowed, err)
			}
			if allowed && resolution.Decision.Tools[0].Limits.HTTPS != nil {
				if !AnyPublicHost(resolution.Decision.Tools[0].Limits.HTTPS.AllowHosts) || resolution.Decision.Tools[0].Limits.Artifacts != nil {
					t.Fatal("web tool authority changed")
				}
				return
			}
			if allowed && (mode == "one-shot" || mode == "mediated-append" || mode == "one-shot-list") {
				if resolution.Decision.Tools[0].Limits.Artifacts.Operation != tool.Spec.Limits.Artifacts.Operation {
					t.Fatal("operation changed")
				}
				return
			}
			if allowed {
				want := int64(4)
				if mode == "attenuated" {
					want = 2
				}
				if resolution.Decision.Tools[0].Limits.Artifacts.MaxOperations != want {
					t.Fatal("effective ceiling")
				}
				if resolution.Execution.RuntimeLimits.Workspace != "none" || resolution.Execution.Tools[0].Spec.Limits.Artifacts.MaxOperations != 4 {
					t.Fatal("material or workspace authority mutated")
				}
			}
		})
	}
}

func httpsLimits() api.CellnToolLimits {
	return api.CellnToolLimits{TimeoutMillis: 30000, MemoryBytes: 64 << 20, ArgumentBytes: 8192, OutputBytes: 8192, Workspace: "none", Effects: "external-side-effects", HTTPS: &api.CellnHTTPSLimits{AllowHosts: []string{AnyHost}, MaxRequests: 4, MaxResponseBytes: 4096, TimeoutMillis: 10000}}
}
