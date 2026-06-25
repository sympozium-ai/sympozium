package webhook

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	admissionv1 "k8s.io/api/admission/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

func enforcerWithSkillPack(t *testing.T, sp *sympoziumv1alpha1.SkillPack) *PolicyEnforcer {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(sp).Build()
	return &PolicyEnforcer{Client: cl, Log: logr.Discard(), Decoder: decoderFor(t, scheme)}
}

func skillPackWithTools(name string, tools ...sympoziumv1alpha1.SidecarTool) *sympoziumv1alpha1.SkillPack {
	return &sympoziumv1alpha1.SkillPack{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: sympoziumv1alpha1.SkillPackSpec{
			Skills: []sympoziumv1alpha1.Skill{{Name: "s", Content: "x"}},
			Sidecar: &sympoziumv1alpha1.SkillSidecar{
				Image: "example/sidecar:latest",
				Tools: tools,
			},
		},
	}
}

func runReferencing(packName string) *sympoziumv1alpha1.AgentRun {
	return &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "default"},
		Spec: sympoziumv1alpha1.AgentRunSpec{
			AgentRef: "inst",
			Skills:   []sympoziumv1alpha1.SkillRef{{SkillPackRef: packName}},
		},
	}
}

func TestValidateSidecarTools_Valid(t *testing.T) {
	params := apiextensionsv1.JSON{Raw: []byte(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`)}
	sp := skillPackWithTools("svc", sympoziumv1alpha1.SidecarTool{
		Name:           "svc_eval",
		Exec:           []string{"node", "/app/cli.js"},
		PositionalArgs: []string{"id"},
		Parameters:     &params,
	})
	pe := enforcerWithSkillPack(t, sp)

	if err := pe.validateSidecarTools(context.Background(), runReferencing("svc")); err != nil {
		t.Errorf("expected valid, got error: %v", err)
	}
}

func TestValidateSidecarTools_StdinModeValid(t *testing.T) {
	// stdin mode may declare non-positional params (they go to stdin).
	params := apiextensionsv1.JSON{Raw: []byte(`{"type":"object","properties":{"id":{"type":"string"},"catalog":{"type":"object"}},"required":["id"]}`)}
	sp := skillPackWithTools("svc", sympoziumv1alpha1.SidecarTool{
		Name:           "svc_eval",
		Exec:           []string{"node", "/app/cli.js"},
		InputMode:      "stdin",
		PositionalArgs: []string{"id"},
		Parameters:     &params,
	})
	pe := enforcerWithSkillPack(t, sp)

	if err := pe.validateSidecarTools(context.Background(), runReferencing("svc")); err != nil {
		t.Errorf("expected valid stdin-mode tool, got error: %v", err)
	}
}

func TestValidateSidecarTools_PositionalNotRequired(t *testing.T) {
	params := apiextensionsv1.JSON{Raw: []byte(`{"type":"object","properties":{"id":{"type":"string"}}}`)}
	sp := skillPackWithTools("svc", sympoziumv1alpha1.SidecarTool{
		Name:           "svc_eval",
		Exec:           []string{"true"},
		PositionalArgs: []string{"id"},
		Parameters:     &params,
	})
	pe := enforcerWithSkillPack(t, sp)

	err := pe.validateSidecarTools(context.Background(), runReferencing("svc"))
	if err == nil || !strings.Contains(err.Error(), "required") {
		t.Errorf("expected positional-must-be-required error, got %v", err)
	}
}

func TestValidateSidecarTools_PositionalNoParams(t *testing.T) {
	sp := skillPackWithTools("svc", sympoziumv1alpha1.SidecarTool{
		Name:           "svc_eval",
		Exec:           []string{"true"},
		PositionalArgs: []string{"id"},
	})
	pe := enforcerWithSkillPack(t, sp)

	err := pe.validateSidecarTools(context.Background(), runReferencing("svc"))
	if err == nil || !strings.Contains(err.Error(), "no parameters") {
		t.Errorf("expected positionalArgs-without-parameters error, got %v", err)
	}
}

func TestValidateSidecarTools_ArgsModeDropsNonPositional(t *testing.T) {
	// args mode with a declared parameter that is not positional would be
	// silently dropped at runtime — admission must reject it.
	params := apiextensionsv1.JSON{Raw: []byte(`{"type":"object","properties":{"id":{"type":"string"},"extra":{"type":"string"}},"required":["id"]}`)}
	sp := skillPackWithTools("svc", sympoziumv1alpha1.SidecarTool{
		Name:           "svc_eval",
		Exec:           []string{"true"},
		PositionalArgs: []string{"id"},
		Parameters:     &params,
	})
	pe := enforcerWithSkillPack(t, sp)

	err := pe.validateSidecarTools(context.Background(), runReferencing("svc"))
	if err == nil || !strings.Contains(err.Error(), "not in positionalArgs") {
		t.Errorf("expected args-mode non-positional error, got %v", err)
	}
}

func TestValidateSidecarTools_MemoryToolCollision(t *testing.T) {
	sp := skillPackWithTools("svc", sympoziumv1alpha1.SidecarTool{
		Name: "memory_search",
		Exec: []string{"true"},
	})
	pe := enforcerWithSkillPack(t, sp)

	err := pe.validateSidecarTools(context.Background(), runReferencing("svc"))
	if err == nil || !strings.Contains(err.Error(), "built-in") {
		t.Errorf("expected memory-tool collision to be rejected, got %v", err)
	}
}

func TestValidateSidecarTools_BuiltinCollision(t *testing.T) {
	sp := skillPackWithTools("svc", sympoziumv1alpha1.SidecarTool{
		Name: "execute_command",
		Exec: []string{"true"},
	})
	pe := enforcerWithSkillPack(t, sp)

	err := pe.validateSidecarTools(context.Background(), runReferencing("svc"))
	if err == nil || !strings.Contains(err.Error(), "built-in") {
		t.Errorf("expected built-in collision error, got %v", err)
	}
}

func TestValidateSidecarTools_BadName(t *testing.T) {
	sp := skillPackWithTools("svc", sympoziumv1alpha1.SidecarTool{
		Name: "Bad-Name",
		Exec: []string{"true"},
	})
	pe := enforcerWithSkillPack(t, sp)

	err := pe.validateSidecarTools(context.Background(), runReferencing("svc"))
	if err == nil || !strings.Contains(err.Error(), "snake_case") {
		t.Errorf("expected snake_case error, got %v", err)
	}
}

func TestValidateSidecarTools_MissingExec(t *testing.T) {
	sp := skillPackWithTools("svc", sympoziumv1alpha1.SidecarTool{
		Name: "svc_eval",
	})
	pe := enforcerWithSkillPack(t, sp)

	err := pe.validateSidecarTools(context.Background(), runReferencing("svc"))
	if err == nil || !strings.Contains(err.Error(), "exec") {
		t.Errorf("expected exec-required error, got %v", err)
	}
}

func TestValidateSidecarTools_PositionalNotDeclared(t *testing.T) {
	params := apiextensionsv1.JSON{Raw: []byte(`{"type":"object","properties":{"id":{"type":"string"}}}`)}
	sp := skillPackWithTools("svc", sympoziumv1alpha1.SidecarTool{
		Name:           "svc_eval",
		Exec:           []string{"true"},
		PositionalArgs: []string{"missing"},
		Parameters:     &params,
	})
	pe := enforcerWithSkillPack(t, sp)

	err := pe.validateSidecarTools(context.Background(), runReferencing("svc"))
	if err == nil || !strings.Contains(err.Error(), "not a declared parameter") {
		t.Errorf("expected positional-arg error, got %v", err)
	}
}

func skillPackValidatorFor(t *testing.T) *SkillPackValidator {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	return &SkillPackValidator{Log: logr.Discard(), Decoder: decoderFor(t, scheme)}
}

func skillPackAdmissionRequest(t *testing.T, sp *sympoziumv1alpha1.SkillPack) admission.Request {
	t.Helper()
	raw, err := json.Marshal(sp)
	if err != nil {
		t.Fatalf("marshal skillpack: %v", err)
	}
	return admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
		Operation: admissionv1.Create,
		Object:    runtime.RawExtension{Raw: raw},
	}}
}

func TestSkillPackValidator_AllowsValid(t *testing.T) {
	params := apiextensionsv1.JSON{Raw: []byte(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`)}
	sp := skillPackWithTools("svc", sympoziumv1alpha1.SidecarTool{
		Name:           "svc_eval",
		Exec:           []string{"node", "/app/cli.js"},
		PositionalArgs: []string{"id"},
		Parameters:     &params,
	})
	resp := skillPackValidatorFor(t).Handle(context.Background(), skillPackAdmissionRequest(t, sp))
	if !resp.Allowed {
		t.Errorf("expected allowed, got denied: %v", resp.Result)
	}
}

func TestSkillPackValidator_DeniesBadTool(t *testing.T) {
	sp := skillPackWithTools("svc", sympoziumv1alpha1.SidecarTool{
		Name: "Bad-Name",
		Exec: []string{"true"},
	})
	resp := skillPackValidatorFor(t).Handle(context.Background(), skillPackAdmissionRequest(t, sp))
	if resp.Allowed {
		t.Error("expected denial for snake_case violation at SkillPack admission")
	}
}

func TestSkillPackValidator_DeniesDuplicateWithinPack(t *testing.T) {
	sp := skillPackWithTools("svc",
		sympoziumv1alpha1.SidecarTool{Name: "dup", Exec: []string{"true"}},
		sympoziumv1alpha1.SidecarTool{Name: "dup", Exec: []string{"true"}},
	)
	resp := skillPackValidatorFor(t).Handle(context.Background(), skillPackAdmissionRequest(t, sp))
	if resp.Allowed {
		t.Error("expected denial for duplicate tool name within a SkillPack")
	}
}

func TestValidateSidecarTools_DuplicateAcrossPacks(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	sp1 := skillPackWithTools("a", sympoziumv1alpha1.SidecarTool{Name: "dup", Exec: []string{"true"}})
	sp2 := skillPackWithTools("b", sympoziumv1alpha1.SidecarTool{Name: "dup", Exec: []string{"true"}})
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(sp1, sp2).Build()
	pe := &PolicyEnforcer{Client: cl, Log: logr.Discard(), Decoder: decoderFor(t, scheme)}

	run := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "default"},
		Spec: sympoziumv1alpha1.AgentRunSpec{
			AgentRef: "inst",
			Skills: []sympoziumv1alpha1.SkillRef{
				{SkillPackRef: "a"}, {SkillPackRef: "b"},
			},
		},
	}

	err := pe.validateSidecarTools(context.Background(), run)
	if err == nil || !strings.Contains(err.Error(), "unique") {
		t.Errorf("expected duplicate-name error, got %v", err)
	}
}
