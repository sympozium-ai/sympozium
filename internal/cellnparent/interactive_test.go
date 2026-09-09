package cellnparent

import (
	"strings"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

func TestInteractiveTurnDataAndIdentityIsolation(t *testing.T) {
	run, binding := admissionFixture(t)
	run.Status.CellnParent = &api.CellnParentStatus{Binding: binding, CreateAttempted: true}
	turn, err := NewTurn(run, "turn-one", "What was my original value?")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BindTurn(run, turn); err == nil {
		t.Fatal("unpersisted turn admitted")
	}
	turn.UID = "turn-uid"
	first, err := BindTurn(run, turn)
	if err != nil {
		t.Fatal(err)
	}
	turn.Status.Execution = &first
	turn.Status.ParentIncarnation = binding.Incarnation
	if _, err := BindTurn(run, turn); err != nil {
		t.Fatal(err)
	}
	changed := turn.DeepCopy()
	changed.Spec.Message = "replacement input"
	if _, err := BindTurn(run, changed); err == nil {
		t.Fatal("input replacement admitted")
	}
	changed = turn.DeepCopy()
	changed.Namespace = "another-tenant"
	if _, err := BindTurn(run, changed); err == nil {
		t.Fatal("cross-namespace turn admitted")
	}
	replaced := run.DeepCopy()
	replaced.UID = "reused-run-name"
	if _, err := BindTurn(replaced, turn); err == nil {
		t.Fatal("turn retargeted to replacement parent")
	}
	second := turn.DeepCopy()
	second.UID = "new-turn-uid"
	second.Status = api.AgentRunTurnStatus{}
	other, err := BindTurn(run, second)
	if err != nil || other.ID != first.ID || other.Child != first.Child {
		t.Fatal("turn name reuse minted fresh execution authority")
	}
	second.Spec.Message = "different input after record deletion"
	changedInput, err := BindTurn(run, second)
	if err != nil || changedInput.ID == first.ID || changedInput.Child == first.Child {
		t.Fatal("changed input could inherit a previous committed answer")
	}
	for _, input := range []string{"", " ", "nul\x00", strings.Repeat("é", 1025)} {
		if _, err := NewTurn(run, "turn-two", input); err == nil {
			t.Fatal("invalid message admitted")
		}
	}
}
