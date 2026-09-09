package cellnparent

import (
	"context"
	"errors"
	"fmt"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var ErrTurnBusy = errors.New("parent already owns a different turn; wait for its reconciliation")

func readTurnPair(ctx context.Context, reader client.Reader, key types.NamespacedName) (*api.AgentRun, *api.AgentRunTurn, error) {
	var turn api.AgentRunTurn
	if err := reader.Get(ctx, key, &turn); err != nil {
		return nil, nil, err
	}
	var run api.AgentRun
	if err := reader.Get(ctx, types.NamespacedName{Namespace: turn.Namespace, Name: turn.Spec.RunName}, &run); err != nil {
		return nil, nil, err
	}
	if _, err := BindTurn(&run, &turn); err != nil {
		return nil, nil, err
	}
	return &run, &turn, nil
}

// ClaimTurnSlot serializes subsequent turns with one parent status CAS. It
// admits no HTTP request. The initial turn must already have committed; budgets
// are spent once when the slot is assigned, never refunded after uncertainty.
func ClaimTurnSlot(ctx context.Context, writer client.Client, reader client.Reader, key types.NamespacedName, config string) error {
	run, turn, err := readTurnPair(ctx, reader, key)
	if err != nil {
		return err
	}
	_, transport, err := LoadApproval(config, run)
	if err != nil {
		return err
	}
	transport.Close()
	parent := run.Status.CellnParent
	if run.Status.Phase != api.AgentRunPhaseRunning || parent.InitialTurn == nil || parent.InitialTurn.Result == nil || !parent.InitialTurn.Result.Succeeded {
		return fmt.Errorf("subsequent turn requires a running parent and committed initial turn")
	}
	if turn.Status.Execution != nil && turn.Status.Execution.Result != nil {
		return fmt.Errorf("completed turn cannot acquire another slot")
	}
	if active := parent.ActiveTurn; active != nil {
		if active.Name == turn.Name && active.UID == string(turn.UID) {
			return nil
		}
		return ErrTurnBusy
	}
	if parent.AcceptedTurns < 0 || parent.AcceptedTurns >= run.Spec.Enduring.MaxTurns-1 {
		return fmt.Errorf("parent turn budget exhausted")
	}
	parent.ActiveTurn = &api.CellnActiveTurn{Name: turn.Name, UID: string(turn.UID)}
	parent.AcceptedTurns++
	return writer.Status().Update(ctx, run)
}

// ReleaseTurnSlot requires the exact child's durable completed record, not just
// a received HTTP response. Deleted/missing/uncertain records retain the slot.
// A late acknowledgement can never release another turn's ownership.
func ReleaseTurnSlot(ctx context.Context, writer client.Client, reader client.Reader, key types.NamespacedName) error {
	run, turn, err := readTurnPair(ctx, reader, key)
	if err != nil {
		return err
	}
	execution := turn.Status.Execution
	if execution == nil || !execution.Attempted || execution.Result == nil {
		return fmt.Errorf("turn completion is not durably recorded")
	}
	active := run.Status.CellnParent.ActiveTurn
	if active == nil {
		return nil
	}
	if active.Name != turn.Name || active.UID != string(turn.UID) {
		return ErrTurnBusy
	}
	run.Status.CellnParent.ActiveTurn = nil
	return writer.Status().Update(ctx, run)
}
