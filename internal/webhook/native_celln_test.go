package webhook

import (
	"context"
	"github.com/go-logr/logr"
	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"strings"
	"testing"
)

func TestNativeCellnDoesNotInheritOCIAdapter(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	agent := &api.Agent{ObjectMeta: metav1.ObjectMeta{Name: "inst", Namespace: "default"}, Spec: api.AgentSpec{RuntimeRef: "native"}}
	// No OCI image or Ready condition: native execution readiness is independently
	// checked by catalogue grants and the host provisioner, never fabricated here.
	rt := &api.AgentRuntime{ObjectMeta: metav1.ObjectMeta{Name: "native", Namespace: "default"}}
	pe := &PolicyEnforcer{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(agent, rt).Build(), Log: logr.Discard(), Decoder: decoderFor(t, scheme)}
	for _, tc := range []struct {
		name, backend           string
		selection, oci, allowed bool
	}{
		{"native catalogue", "celln", true, false, true},
		{"explicit OCI remains forbidden", "celln", true, true, false},
		{"ordinary runtime still requires Ready", "", false, false, false},
		{"selection alone cannot bypass OCI", "", true, false, false},
		{"legacy Celln does not drop inherited runtime", "celln", false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := capabilityRun(api.NewStringTask("read notes.txt"))
			run.Spec.Backend = tc.backend
			if tc.selection {
				run.Spec.CellnSelection = &api.CellnCatalogueSelection{RuntimeRef: "native"}
			}
			if tc.oci {
				run.Spec.Task = harnessTaskSpec(nil)
			}
			resp := pe.Handle(context.Background(), admissionRequestFor(t, run))
			if resp.Allowed != tc.allowed {
				t.Fatalf("allowed=%v want %v: %v", resp.Allowed, tc.allowed, resp.Result)
			}
		})
	}
}

func TestNativeCellnRejectsUnsupportedSkillsAndMCPAtAdmission(t *testing.T) {
	for _, source := range []string{"agent skill", "run skill", "agent mcp"} {
		t.Run(source, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := api.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			agent := &api.Agent{ObjectMeta: metav1.ObjectMeta{Name: "inst", Namespace: "default"}}
			run := capabilityRun(api.NewStringTask("inspect cluster"))
			run.Spec.Backend = "celln"
			run.Spec.CellnSelection = &api.CellnCatalogueSelection{RuntimeRef: "native"}
			switch source {
			case "agent skill":
				agent.Spec.Skills = []api.SkillRef{{SkillPackRef: "k8s-ops"}}
			case "run skill":
				run.Spec.Skills = []api.SkillRef{{SkillPackRef: "k8s-ops"}}
			case "agent mcp":
				agent.Spec.MCPServers = []api.MCPServerRef{{Name: "kubernetes"}}
			}
			pe := &PolicyEnforcer{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(agent).Build(), Log: logr.Discard(), Decoder: decoderFor(t, scheme)}
			resp := pe.Handle(context.Background(), admissionRequestFor(t, run))
			if resp.Allowed || resp.Result == nil || !strings.Contains(resp.Result.Message, "SkillPacks or Agent MCP") {
				t.Fatalf("unexpected response: %+v", resp)
			}
		})
	}
}
