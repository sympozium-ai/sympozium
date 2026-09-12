package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestPositiveDecisionsConformToPublishedSchema(t *testing.T) {
	dir := t.TempDir()
	if err := writeSchemas(dir); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "schema", "decision.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document any
	if err = json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	const uri = "urn:celln:decision"
	if err = compiler.AddResource(uri, document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile(uri)
	if err != nil {
		t.Fatal(err)
	}
	cases := buildCases()
	for _, v := range cases.Vectors {
		if v.Evaluator != "verify" || v.Expect.Outcome == "reject" {
			continue
		}
		t.Run(v.Name, func(t *testing.T) {
			var decision any
			if err := json.Unmarshal([]byte(cases.Decisions[v.DecisionRef].Canonical), &decision); err != nil {
				t.Fatal(err)
			}
			if err := schema.Validate(decision); err != nil {
				t.Fatal(err)
			}
		})
	}
}
