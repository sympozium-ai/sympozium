package cellnparent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/zeebo/blake3"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func childIdentity(parent, turn string) string {
	wire, _ := json.Marshal([]string{parent, turn})
	sum := blake3.Sum256(wire)
	return fmt.Sprintf("blake3:%x", sum)
}

// ReconcileInitialTurn delivers the frozen initial task once. The enduring
// parent remains live after completion; this is not AgentRun success.
func ReconcileInitialTurn(ctx context.Context, writer client.Client, reader client.Reader, key types.NamespacedName, config string) (bool, error) {
	return reconcileInitialTurn(ctx, writer, reader, key, config, false)
}

// RecoverInitialTurn reads evidence for an attempted turn only. It cannot
// prepare or submit a turn when the owner is stopped or context is lost.
func RecoverInitialTurn(ctx context.Context, writer client.Client, reader client.Reader, key types.NamespacedName, config string) (bool, error) {
	return reconcileInitialTurn(ctx, writer, reader, key, config, true)
}

func reconcileInitialTurn(ctx context.Context, writer client.Client, reader client.Reader, key types.NamespacedName, config string, recoveryOnly bool) (bool, error) {
	var run api.AgentRun
	if err := reader.Get(ctx, key, &run); err != nil {
		return false, err
	}
	binding, transport, err := LoadApproval(config, &run)
	if err != nil {
		return false, err
	}
	defer transport.Close()
	if run.Status.CellnParent == nil || !run.Status.CellnParent.CreateAttempted {
		return false, fmt.Errorf("initial turn requires an admitted parent")
	}
	message := run.Spec.Task.GetPrompt()
	if !run.Spec.Task.IsString() || strings.TrimSpace(message) == "" || len(message) > 2048 || strings.ContainsRune(message, 0) {
		return false, fmt.Errorf("initial parent task requires bounded text")
	}
	expected := api.CellnParentTurnStatus{ID: "initial", Message: message, Child: childIdentity(binding.Incarnation, "initial")}
	current := run.Status.CellnParent.InitialTurn
	if recoveryOnly && (current == nil || !current.Attempted) {
		return false, ErrReconcile
	}
	if current == nil {
		run.Status.CellnParent.InitialTurn = &expected
		return false, writer.Status().Update(ctx, &run)
	}
	if current.ID != expected.ID || current.Message != expected.Message || current.Child != expected.Child {
		return false, fmt.Errorf("frozen initial turn changed")
	}
	if current.Result != nil {
		return true, nil
	}
	if !current.Attempted {
		owner, err := transport.Status(ctx, binding.Incarnation)
		if err != nil {
			return false, err
		}
		if !owner.Live || owner.Status != "Ready" {
			return false, ErrReconcile
		}
		current.Attempted = true
		if err := writer.Status().Update(ctx, &run); err != nil {
			return false, err
		}
		reply, err := transport.SubmitAsync(ctx, binding.Incarnation, current.ID, current.Message)
		if err != nil {
			return false, err
		}
		if reply.Pending {
			return false, nil
		}
		return persistInitialResult(ctx, writer, reader, key, binding, expected, api.CellnParentTurnResult{Succeeded: *reply.Succeeded, Answer: reply.Answer})
	}
	evidence, err := transport.Turn(ctx, binding.Incarnation, current.ID)
	if err != nil {
		return false, err
	}
	if evidence.Turn.Stage != "parent-committed" {
		return false, nil
	}
	var record struct {
		Child     string `json:"child"`
		Succeeded *bool  `json:"succeeded"`
		Answer    string `json:"answer"`
	}
	if json.Unmarshal(evidence.Turn.Record, &record) != nil || record.Child != expected.Child || record.Succeeded == nil || strings.TrimSpace(record.Answer) == "" || len(record.Answer) > 2048 || strings.ContainsRune(record.Answer, 0) {
		return false, ErrReconcile
	}
	return persistInitialResult(ctx, writer, reader, key, binding, expected, api.CellnParentTurnResult{Succeeded: *record.Succeeded, Answer: record.Answer})
}

func persistInitialResult(ctx context.Context, writer client.Client, reader client.Reader, key types.NamespacedName, binding api.CellnParentBinding, expected api.CellnParentTurnStatus, result api.CellnParentTurnResult) (bool, error) {
	var run api.AgentRun
	if err := reader.Get(ctx, key, &run); err != nil {
		return false, err
	}
	if string(run.UID) != binding.RunUID || run.Status.CellnParent == nil || run.Status.CellnParent.Binding != binding {
		return false, ErrReconcile
	}
	current := run.Status.CellnParent.InitialTurn
	if current == nil || !current.Attempted || current.ID != expected.ID || current.Child != expected.Child || current.Message != expected.Message {
		return false, ErrReconcile
	}
	if current.Result != nil {
		if *current.Result != result {
			return false, ErrReconcile
		}
		return true, nil
	}
	current.Result = &result
	if err := writer.Status().Update(ctx, &run); err != nil {
		return false, err
	}
	return true, nil
}
