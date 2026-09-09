package v1alpha1_test

import (
	"encoding/json"
	"os"
	"testing"

	ext "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema/cel"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/yaml"
)

func TestTurnCancellationSchemaIsMonotonicAndIdentityBound(t *testing.T) {
	raw, err := os.ReadFile("../../config/crd/bases/sympozium.ai_agentrunturns.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var crd extv1.CustomResourceDefinition
	if err := yaml.Unmarshal(raw, &crd); err != nil {
		t.Fatal(err)
	}
	var internal ext.JSONSchemaProps
	if err := extv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(crd.Spec.Versions[0].Schema.OpenAPIV3Schema, &internal, nil); err != nil {
		t.Fatal(err)
	}
	structural, err := schema.NewStructural(&internal)
	if err != nil {
		t.Fatal(err)
	}
	validator := cel.NewValidator(structural, true, 10000000)
	base := `{"apiVersion":"sympozium.ai/v1alpha1","kind":"AgentRunTurn","metadata":{"name":"turn"},"spec":{"runName":"parent","runUID":"parent-uid","message":"hello"},"status":{"execution":{"id":"one","message":"hello","child":"blake3:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","attempted":true}}}`
	decode := func() map[string]interface{} {
		var value map[string]interface{}
		if err := json.Unmarshal([]byte(base), &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	for _, change := range []string{"request", "attempt", "withdraw-request", "remove-request", "clear-attempt", "remove-attempt", "remove-status", "unattempted", "retarget", "edit-message"} {
		t.Run(change, func(t *testing.T) {
			old, current := decode(), decode()
			oldSpec, spec := old["spec"].(map[string]interface{}), current["spec"].(map[string]interface{})
			oldStatus, status := old["status"].(map[string]interface{}), current["status"].(map[string]interface{})
			spec["cancelRequested"] = true
			if change != "request" {
				oldSpec["cancelRequested"] = true
			}
			if change == "clear-attempt" || change == "remove-attempt" || change == "remove-status" {
				oldStatus["cancelAttempted"] = true
			}
			switch change {
			case "attempt":
				status["cancelAttempted"] = true
			case "withdraw-request":
				spec["cancelRequested"] = false
			case "remove-request":
				delete(spec, "cancelRequested")
			case "clear-attempt":
				status["cancelAttempted"] = false
			case "remove-status":
				delete(current, "status")
			case "unattempted":
				oldStatus["execution"].(map[string]interface{})["attempted"] = false
				status["execution"].(map[string]interface{})["attempted"] = false
				delete(oldSpec, "cancelRequested")
			case "retarget":
				spec["runUID"] = "replacement"
			case "edit-message":
				spec["message"] = "new input"
			}
			errs, _ := validator.Validate(t.Context(), field.NewPath("turn"), structural, current, old, 10000000)
			allowed := change == "request" || change == "attempt"
			if (len(errs) == 0) != allowed {
				t.Fatalf("allowed=%v errors=%v", allowed, errs)
			}
		})
	}
}

// Exercise the generated schema with Kubernetes' CEL validator, rather than
// merely checking that the intended rule appears in a YAML file.
func TestEnduringSchemaRejectsLegacyBoundaryStripping(t *testing.T) {
	raw, err := os.ReadFile("../../config/crd/bases/sympozium.ai_agentruns.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var crd extv1.CustomResourceDefinition
	if err := yaml.Unmarshal(raw, &crd); err != nil {
		t.Fatal(err)
	}
	props := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"]
	var internal ext.JSONSchemaProps
	if err := extv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(&props, &internal, nil); err != nil {
		t.Fatal(err)
	}
	structural, err := schema.NewStructural(&internal)
	if err != nil {
		t.Fatal(err)
	}
	validator := cel.NewValidator(structural, false, 10000000)
	old := map[string]interface{}{
		"executionLifecycle": "enduring", "backend": "celln",
		"cellnSelection": map[string]interface{}{"toolRefs": []interface{}{}},
		"enduring":       map[string]interface{}{"leaseSeconds": int64(60), "maxTurns": int64(3), "maxModelRequests": int64(6), "maxOutputTokens": int64(3072)},
	}
	for _, change := range []string{"unchanged", "strip-all", "one-shot", "backend", "selection", "limits"} {
		t.Run(change, func(t *testing.T) {
			current := make(map[string]interface{}, len(old))
			for key, value := range old {
				current[key] = value
			}
			switch change {
			case "strip-all":
				delete(current, "executionLifecycle")
				delete(current, "cellnSelection")
				delete(current, "enduring")
			case "one-shot":
				current["executionLifecycle"] = "one-shot"
				delete(current, "enduring")
			case "backend":
				current["backend"] = "kubernetes"
			case "selection":
				delete(current, "cellnSelection")
			case "limits":
				delete(current, "enduring")
			}
			errs, _ := validator.Validate(t.Context(), field.NewPath("spec"), structural, current, old, 10000000)
			if (len(errs) == 0) != (change == "unchanged") {
				t.Fatalf("unexpected transition validation: %v", errs)
			}
		})
	}
}
