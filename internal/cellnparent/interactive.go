package cellnparent

import (
	"encoding/json"
	"fmt"
	"strings"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/zeebo/blake3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

// NewTurn builds a data-only child record after the API authenticates access to
// this namespaced run. Persist it before using its Kubernetes UID for dispatch.
// This does not grant authority or assert host readiness.
func NewTurn(run *api.AgentRun, name, message string) (*api.AgentRunTurn, error) {
	if len(validation.IsDNS1123Subdomain(name)) != 0 || len(name) > 253 || run.Namespace == "" || run.Name == "" || run.UID == "" || run.DeletionTimestamp != nil || run.Spec.ExecutionLifecycle != "enduring" || run.Spec.ValidateLifecycle() != "" {
		return nil, fmt.Errorf("turn requires a valid name and live enduring parent")
	}
	if err := validateTurnMessage(message); err != nil {
		return nil, err
	}
	return &api.AgentRunTurn{TypeMeta: metav1.TypeMeta{APIVersion: api.GroupVersion.String(), Kind: "AgentRunTurn"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: run.Namespace, OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(run, api.GroupVersion.WithKind("AgentRun"))}},
		Spec:       api.AgentRunTurnSpec{RunName: run.Name, RunUID: string(run.UID), Message: message}}, nil
}

func validateTurnMessage(message string) error {
	if strings.TrimSpace(message) == "" || len(message) > 2048 || strings.ContainsRune(message, 0) {
		return fmt.Errorf("turn message exceeds bounded text contract")
	}
	return nil
}

// BindTurn requires a persisted child UID but derives the host identity from
// parent UID, turn name and exact input, retaining replay exclusion for the same
// request after name reuse without associating old answers with changed input. Dispatch
// requires fresh operator approval, a parent serialization claim and readiness.
func BindTurn(run *api.AgentRun, turn *api.AgentRunTurn) (api.CellnParentTurnStatus, error) {
	var zero api.CellnParentTurnStatus
	if run.UID == "" || run.Status.CellnParent == nil || run.Status.CellnParent.Binding.RunUID != string(run.UID) || !run.Status.CellnParent.CreateAttempted || !hashPattern.MatchString(run.Status.CellnParent.Binding.Incarnation) || turn.UID == "" || turn.Namespace != run.Namespace || turn.Spec.RunName != run.Name || turn.Spec.RunUID != string(run.UID) || run.DeletionTimestamp != nil || turn.DeletionTimestamp != nil || run.Spec.ExecutionLifecycle != "enduring" || run.Spec.ValidateLifecycle() != "" {
		return zero, fmt.Errorf("turn does not bind the original enduring parent")
	}
	if err := validateTurnMessage(turn.Spec.Message); err != nil {
		return zero, err
	}
	owner := metav1.GetControllerOf(turn)
	if owner == nil || owner.UID != run.UID || owner.Name != run.Name || owner.Kind != "AgentRun" || owner.APIVersion != api.GroupVersion.String() {
		return zero, fmt.Errorf("turn owner reference mismatch")
	}
	// Name reuse within the same parent must not mint retry authority after
	// record deletion. The host journal retains this identity independently.
	wire, _ := json.Marshal([]string{string(run.UID), turn.Name, turn.Spec.Message})
	sum := blake3.Sum256(wire)
	id := fmt.Sprintf("%x", sum)
	expected := api.CellnParentTurnStatus{ID: id, Message: turn.Spec.Message, Child: childIdentity(run.Status.CellnParent.Binding.Incarnation, id)}
	if turn.Status.ParentIncarnation != "" && turn.Status.ParentIncarnation != run.Status.CellnParent.Binding.Incarnation {
		return zero, ErrReconcile
	}
	if saved := turn.Status.Execution; saved != nil && (saved.ID != expected.ID || saved.Child != expected.Child || saved.Message != expected.Message) {
		return zero, ErrReconcile
	}
	return expected, nil
}
