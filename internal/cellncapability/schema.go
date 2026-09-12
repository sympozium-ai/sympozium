package cellncapability

import (
	_ "embed"
	"encoding/json"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed schema/decision.schema.json
var decisionSchemaBytes []byte

// Use the same published schema as the independent Rust receiver. Handwritten
// semantic checks alone must not let the issuer mint receiver-invalid authority.
var compiledDecisionSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	var document any
	if err := json.Unmarshal(decisionSchemaBytes, &document); err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	const uri = "urn:celln:decision"
	if err := compiler.AddResource(uri, document); err != nil {
		return nil, err
	}
	return compiler.Compile(uri)
})

func decisionConformsToSchema(decision Decision) bool {
	raw, err := json.Marshal(decision)
	return err == nil && rawDecisionConformsToSchema(raw)
}

func rawDecisionConformsToSchema(raw []byte) bool {
	schema, err := compiledDecisionSchema()
	if err != nil {
		return false
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	return schema.Validate(value) == nil
}
