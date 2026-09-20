package v1alpha1_test

import (
	"os"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"
)

func crdSchema(t *testing.T, file string, path ...string) extv1.JSONSchemaProps {
	t.Helper()
	raw, err := os.ReadFile("../../config/crd/bases/" + file)
	if err != nil {
		t.Fatal(err)
	}
	var crd extv1.CustomResourceDefinition
	if err := yaml.Unmarshal(raw, &crd); err != nil {
		t.Fatal(err)
	}
	node := *crd.Spec.Versions[0].Schema.OpenAPIV3Schema
	for _, name := range path {
		if name == "[]" {
			node = *node.Items.Schema
			continue
		}
		next, ok := node.Properties[name]
		if !ok {
			t.Fatalf("%s: no %q in %v", file, name, path)
		}
		node = next
	}
	return node
}

// A kubebuilder marker cannot name a constant, so the generated schemas are
// held to the conversation bounds here: messages stay at 2048 bytes, answers
// take 8192 everywhere one is stored, and a runtime profile may report the
// current worker task bound.
func TestConversationBoundsMatchSchema(t *testing.T) {
	maxLength := func(file string, path ...string) int64 {
		t.Helper()
		node := crdSchema(t, file, path...)
		if node.MaxLength == nil {
			t.Fatalf("%s %v has no maxLength", file, path)
		}
		return *node.MaxLength
	}
	for _, answer := range [][]string{
		{"sympozium.ai_agentruns.yaml", "status", "cellnParent", "initialTurn", "result", "answer"},
		{"sympozium.ai_agentrunturns.yaml", "status", "execution", "result", "answer"},
		{"sympozium.ai_agentruns.yaml", "spec", "conversation", "seed", "[]", "assistant"},
	} {
		if got := maxLength(answer[0], answer[1:]...); got != api.MaxConversationAnswerBytes {
			t.Fatalf("%v: maxLength %d, want %d", answer, got, api.MaxConversationAnswerBytes)
		}
	}
	for _, message := range [][]string{
		{"sympozium.ai_agentruns.yaml", "status", "cellnParent", "initialTurn", "message"},
		{"sympozium.ai_agentrunturns.yaml", "status", "execution", "message"},
		{"sympozium.ai_agentrunturns.yaml", "spec", "message"},
		{"sympozium.ai_agentruns.yaml", "spec", "conversation", "seed", "[]", "user"},
	} {
		if got := maxLength(message[0], message[1:]...); got != api.MaxConversationMessageBytes {
			t.Fatalf("%v: maxLength %d, want %d", message, got, api.MaxConversationMessageBytes)
		}
	}
	for _, taskBytes := range [][]string{
		{"sympozium.ai_cellnruntimeprofiles.yaml", "spec", "limits", "taskBytes"},
		{"sympozium.ai_agentruntimes.yaml", "spec", "cellnLimits", "taskBytes"},
	} {
		node := crdSchema(t, taskBytes[0], taskBytes[1:]...)
		if node.Maximum == nil || int64(*node.Maximum) != api.MaxWorkerTaskBytes {
			t.Fatalf("%v: maximum %v, want %d", taskBytes, node.Maximum, api.MaxWorkerTaskBytes)
		}
	}
	// Lifetime totals keep their stored-object-compatible range: no minimum
	// was raised to the per-turn allowance.
	// The output-token maximum is 1024 turns of the largest per-turn allowance
	// a backend may have (6 requests × 4096 output tokens per request),
	// wherever a lifetime total is stored.
	if api.MaxLifetimeOutputTokens != 25165824 || api.MaxTurnOutputTokens != 24576 || api.TurnOutputTokens != 3072 || api.DefaultRequestOutputTokens != 512 {
		t.Fatalf("output token bounds: turn %d, largest turn %d, lifetime %d", api.TurnOutputTokens, api.MaxTurnOutputTokens, api.MaxLifetimeOutputTokens)
	}
	for _, totals := range [][]string{
		{"sympozium.ai_agentruns.yaml", "spec", "enduring"},
		{"sympozium.ai_agents.yaml", "spec", "execution", "enduring"},
		{"sympozium.ai_cellnexecutionpolicies.yaml", "spec", "ceilings"},
	} {
		for field, maximum := range map[string]float64{"maxModelRequests": 6144, "maxOutputTokens": api.MaxLifetimeOutputTokens} {
			node := crdSchema(t, totals[0], append(totals[1:], field)...)
			if node.Minimum == nil || *node.Minimum != 0 || node.Maximum == nil || *node.Maximum != maximum {
				t.Fatalf("%v.%s range changed: %v–%v", totals, field, node.Minimum, node.Maximum)
			}
		}
	}
	// A profile's per-turn allowance is whatever its package reports: no
	// maximum that a raised output cap could run into.
	for _, field := range []string{"turnModelRequests", "turnOutputTokens"} {
		if node := crdSchema(t, "sympozium.ai_cellnruntimeprofiles.yaml", "spec", "native", field); node.Maximum != nil || node.Minimum == nil || *node.Minimum != 1 {
			t.Fatalf("native.%s range changed: %v–%v", field, node.Minimum, node.Maximum)
		}
	}
	spec := api.AgentRunSpec{Backend: "celln", ExecutionLifecycle: "enduring", CellnSelection: &api.CellnCatalogueSelection{}, Enduring: &api.EnduringRunSpec{LeaseSeconds: 3600, MaxTurns: 1024, MaxModelRequests: 6144, MaxOutputTokens: api.MaxLifetimeOutputTokens}}
	if got := spec.ValidateLifecycle(); got != "" {
		t.Fatalf("the maximum lifetime total refused: %s", got)
	}
	spec.Enduring.MaxOutputTokens++
	if got := spec.ValidateLifecycle(); got == "" {
		t.Fatal("a lifetime total above the maximum accepted")
	}
}
