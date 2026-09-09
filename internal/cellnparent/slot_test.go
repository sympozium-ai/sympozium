package cellnparent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestTurnSlotSerializesClaimantsRetainsUncertaintyAndConsumesBudget(t *testing.T) {
	ctx := context.Background()
	run, binding := admissionFixture(t)
	run.Spec.Enduring.MaxTurns = 3
	digest, err := SpecDigest(run.Spec)
	if err != nil {
		t.Fatal(err)
	}
	binding.SpecSHA256 = digest
	run.Status.Phase = api.AgentRunPhaseRunning
	run.Status.CellnParent = &api.CellnParentStatus{Binding: binding, CreateAttempted: true, InitialTurn: &api.CellnParentTurnStatus{ID: "initial", Message: "hello", Child: testID, Attempted: true, Result: &api.CellnParentTurnResult{Succeeded: true, Answer: "ready"}}}
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	turns := make([]*api.AgentRunTurn, 3)
	objects := []client.Object{run}
	for i, name := range []string{"one", "two", "three"} {
		turns[i], err = NewTurn(run, name, "follow up")
		if err != nil {
			t.Fatal(err)
		}
		turns[i].UID = types.UID(name + "-uid")
		objects = append(objects, turns[i])
	}
	store := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&api.AgentRun{}, &api.AgentRunTurn{}).WithObjects(objects...).Build()
	root := t.TempDir()
	path := filepath.Join(root, "config.json")
	raw, err := json.Marshal(ApprovalConfig{APIVersion: "sympozium.ai/celln-parent-controller-v1", Approvals: []RunApproval{{Namespace: run.Namespace, Name: run.Name, Binding: binding, TokenFile: filepath.Join(root, "unopened-token")}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	winners := make(chan int, 2)
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := ClaimTurnSlot(ctx, store, store, client.ObjectKeyFromObject(turns[i]), path)
			if err == nil {
				winners <- i
			} else if !errors.Is(err, ErrTurnBusy) && !apierrors.IsConflict(err) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	close(winners)
	winner := -1
	count := 0
	for index := range winners {
		winner = index
		count++
	}
	if count != 1 {
		t.Fatalf("slot winners=%d", count)
	}
	key := client.ObjectKeyFromObject(turns[winner])
	if err := ClaimTurnSlot(ctx, store, store, key, path); err != nil {
		t.Fatal(err)
	}
	var saved api.AgentRun
	if err := store.Get(ctx, client.ObjectKeyFromObject(run), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Status.CellnParent.AcceptedTurns != 1 {
		t.Fatal("repeat claim consumed extra budget")
	}
	if ReleaseTurnSlot(ctx, store, store, key) == nil {
		t.Fatal("uncertain turn released slot")
	}
	complete := func(index int) {
		t.Helper()
		var turn api.AgentRunTurn
		if err := store.Get(ctx, client.ObjectKeyFromObject(turns[index]), &turn); err != nil {
			t.Fatal(err)
		}
		execution, err := BindTurn(&saved, &turn)
		if err != nil {
			t.Fatal(err)
		}
		execution.Attempted = true
		execution.Result = &api.CellnParentTurnResult{Succeeded: true, Answer: "done"}
		turn.Status.Execution = &execution
		turn.Status.ParentIncarnation = binding.Incarnation
		if err := store.Status().Update(ctx, &turn); err != nil {
			t.Fatal(err)
		}
	}
	complete(winner)
	if err := ReleaseTurnSlot(ctx, store, store, key); err != nil {
		t.Fatal(err)
	}
	loser := 1 - winner
	loserKey := client.ObjectKeyFromObject(turns[loser])
	if err := ClaimTurnSlot(ctx, store, store, loserKey, path); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(ReleaseTurnSlot(ctx, store, store, key), ErrTurnBusy) {
		t.Fatal("late completion released another owner")
	}
	complete(loser)
	if err := ReleaseTurnSlot(ctx, store, store, loserKey); err != nil {
		t.Fatal(err)
	}
	if ClaimTurnSlot(ctx, store, store, client.ObjectKeyFromObject(turns[2]), path) == nil {
		t.Fatal("turn budget exceeded")
	}
	if err := store.Get(ctx, client.ObjectKeyFromObject(run), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Status.CellnParent.AcceptedTurns != 2 || saved.Status.CellnParent.ActiveTurn != nil {
		t.Fatal("budget refunded or slot leaked after confirmed completion")
	}
}
