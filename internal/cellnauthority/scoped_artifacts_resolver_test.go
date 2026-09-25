package cellnauthority

import (
	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
	"strings"
	"testing"
)

func TestScopedArtifactResolverExplicitPolicySemantics(t *testing.T) {
	for _, mode := range []string{"catalogue-ceiling", "attenuated", "omitted-capability", "different-operation", "one-shot", "argv", "invalid-effects"} {
		t.Run(mode, func(t *testing.T) {
			f := newPlatformFixture(t, "artifact-tenant", true)
			var run api.AgentRun
			if err := f.client.Get(t.Context(), f.runKey, &run); err != nil {
				t.Fatal(err)
			}
			if mode != "one-shot" {
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
			if mode != "one-shot" {
				request.ParentIncarnation = "blake3:" + strings.Repeat("f", 64)
			}
			resolution, err := f.resolver.Resolve(t.Context(), f.runKey, request)
			allowed := mode == "catalogue-ceiling" || mode == "attenuated"
			if (err == nil) != allowed {
				t.Fatalf("allowed=%v error=%v", allowed, err)
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
