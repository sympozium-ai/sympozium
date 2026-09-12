package cellncapability

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestVerifierDoesNotFillMissingRequiredNullableFields(t *testing.T) {
	decision, vector := fixtureDecisionByVector(t, "harness-one-shot")
	key := keyFromByte("schema-test", 0x32)
	clock := ClockFunc(func() time.Time { return time.Unix(vector.Verify.Now, 0) })
	issuer, err := NewIssuer(ControlPlaneIssuer, key, clock)
	if err != nil {
		t.Fatal(err)
	}
	token, err := issuer.Issue(decision, IssueRequest{Audience: AudienceExecution, Operation: "execution.start"})
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewVerifier(ControlPlaneIssuer, []VerificationKey{verificationKey(key)}, clock)
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err := CanonicalDecision(decision)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	delete(object, "parent")
	raw, err = json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Verify(token, raw, contextFromFixture(*vector.Verify)); Reason(err) != ReasonMalformed {
		t.Fatalf("missing parent was normalized into signed null: %v", err)
	}
}

func TestRuntimeSchemaMatchesPublishedContract(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(fixtureRoot(t), "schema", "decision.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, decisionSchemaBytes) {
		t.Fatal("runtime decision schema differs from shared Go/Rust contract")
	}
}

func TestIssuerRefusesReceiverInvalidDecisionShapes(t *testing.T) {
	for name, mutate := range map[string]func(*Decision){
		"kind":               func(d *Decision) { d.Kind = "Other" },
		"empty-run-name":     func(d *Decision) { d.Run.Name = "" },
		"untyped-run-digest": func(d *Decision) { d.Run.SpecSHA256 = "spec" },
		"untyped-budget-id":  func(d *Decision) { d.Budget.BudgetID = "budget" },
		"missing-subject":    func(d *Decision) { d.Subject = SubjectBinding{} },
		"missing-runtime":    func(d *Decision) { d.Runtime = RuntimeBinding{} },
		"missing-policy":     func(d *Decision) { d.Policy = PolicyBinding{} },
		"null-tools":         func(d *Decision) { d.Tools = nil },
		"unknown-lifecycle":  func(d *Decision) { d.Lifecycle = "made-up"; d.Budget.ParentDeadlineUnix = d.Budget.TurnDeadlineUnix },
	} {
		t.Run(name, func(t *testing.T) {
			decision, _ := fixtureDecisionByVector(t, "harness-one-shot")
			now := time.Unix(decision.Windows.IssuedAt+1, 0)
			issuer, err := NewIssuer(ControlPlaneIssuer, keyFromByte("schema-test", 0x32), ClockFunc(func() time.Time { return now }))
			if err != nil {
				t.Fatal(err)
			}
			mutate(&decision)
			if _, err := issuer.Issue(decision, IssueRequest{Audience: AudienceExecution, Operation: "execution.start"}); err == nil {
				t.Fatal("issuer minted receiver-invalid decision")
			}
		})
	}
}
