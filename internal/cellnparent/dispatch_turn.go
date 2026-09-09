package cellnparent

import (
	"context"
	"encoding/json"
	"strings"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ReconcileTurn handles one subsequent turn without recreating its parent or
// retrying an attempted submission. The slot is released only after persistence.
func ReconcileTurn(ctx context.Context, writer client.Client, reader client.Reader, key types.NamespacedName, config string) (bool, error) {
	run, turn, err := readTurnPair(ctx, reader, key)
	if err != nil {
		return false, err
	}
	expected, err := BindTurn(run, turn)
	if err != nil {
		return false, err
	}
	if turn.Status.Execution != nil && turn.Status.Execution.Result != nil {
		return true, ReleaseTurnSlot(ctx, writer, reader, key)
	}
	attempted := turn.Status.Execution != nil && turn.Status.Execution.Attempted
	if turn.Spec.CancelRequested && !attempted {
		return false, ErrReconcile
	}
	binding, transport, err := loadBinding(config, run, attempted)
	if err != nil {
		return false, err
	}
	defer transport.Close()
	if !attempted {
		if err := ClaimTurnSlot(ctx, writer, reader, key, config); err != nil {
			return false, err
		}
		// Claim may have raced another reconcile of this turn. Reread both records
		// before deciding whether this invocation still owns an unsent attempt.
		run, turn, err = readTurnPair(ctx, reader, key)
		if err != nil {
			return false, err
		}
		if run.Status.CellnParent.Binding != binding {
			return false, ErrReconcile
		}
	}
	active := run.Status.CellnParent.ActiveTurn
	if active == nil || active.Name != turn.Name || active.UID != string(turn.UID) {
		return false, ErrTurnBusy
	}
	if turn.Status.Execution == nil {
		turn.Status.ParentIncarnation = binding.Incarnation
		turn.Status.Execution = &expected
		return false, writer.Status().Update(ctx, turn)
	}
	current := turn.Status.Execution
	if current.Result != nil {
		return true, ReleaseTurnSlot(ctx, writer, reader, key)
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
		if err := writer.Status().Update(ctx, turn); err != nil {
			return false, err
		}
		response, err := transport.SubmitAsync(ctx, binding.Incarnation, current.ID, current.Message)
		if err != nil {
			return false, err
		}
		if response.Pending {
			return false, nil
		}
		return saveTurnResult(ctx, writer, reader, key, string(turn.UID), binding, expected, api.CellnParentTurnResult{Succeeded: *response.Succeeded, Answer: response.Answer})
	}
	if turn.Spec.CancelRequested && !turn.Status.CancelAttempted {
		// Persist before transport: even a lost acknowledgement cannot retarget
		// another child or authorize an automatic retry after controller restart.
		turn.Status.CancelAttempted = true
		if err := writer.Status().Update(ctx, turn); err != nil {
			return false, err
		}
		if err := transport.CancelTurn(ctx, binding.Incarnation, current.ID); err != nil {
			return false, err
		}
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
	return saveTurnResult(ctx, writer, reader, key, string(turn.UID), binding, expected, api.CellnParentTurnResult{Succeeded: *record.Succeeded, Answer: record.Answer})
}

func saveTurnResult(ctx context.Context, writer client.Client, reader client.Reader, key types.NamespacedName, uid string, binding api.CellnParentBinding, expected api.CellnParentTurnStatus, result api.CellnParentTurnResult) (bool, error) {
	run, turn, err := readTurnPair(ctx, reader, key)
	if err != nil {
		return false, err
	}
	current := turn.Status.Execution
	if string(turn.UID) != uid || run.Status.CellnParent.Binding != binding || current == nil || !current.Attempted || current.ID != expected.ID || current.Message != expected.Message || current.Child != expected.Child {
		return false, ErrReconcile
	}
	if current.Result != nil {
		if *current.Result != result {
			return false, ErrReconcile
		}
	} else {
		current.Result = &result
		if err := writer.Status().Update(ctx, turn); err != nil {
			return false, err
		}
	}
	return true, ReleaseTurnSlot(ctx, writer, reader, key)
}
