package cellnparent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Real Celln HTTP/KVM/model execution, fake Kubernetes persistence. The operator
// approval is supplied by this proof, not produced by automatic issuance.
func proveLiveCoordinator(t *testing.T, ctx context.Context) {
	t.Helper()
	if os.Getenv("CELLN_INTEROP_BORROWED") != "true" {
		t.Fatal("coordinator proof requires borrowed-tool fixture")
	}
	run := &api.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "interop", Namespace: "proof", UID: "interop-parent-uid"}, Spec: api.AgentRunSpec{
		AgentRef: "agent", Backend: "celln", ExecutionLifecycle: "enduring",
		Task:           api.NewStringTask("My value is violet. Call uppercase with my value and answer only the tool result text."),
		CellnSelection: &api.CellnCatalogueSelection{ToolRefs: []api.CellnCatalogueToolRef{{Name: "uppercase", Revision: "v1"}}},
		// Match the independently prepared borrowed-tool hardware fixture's
		// ceilings (three requests/1536 tokens per child, two turns, 180s lease).
		Enduring: &api.EnduringRunSpec{LeaseSeconds: 180, MaxTurns: 2, MaxModelRequests: 6, MaxOutputTokens: 3072},
	}}
	store := coordinatorStore(t, ctx, run)
	digest, err := SpecDigest(run.Spec)
	if err != nil {
		t.Fatal(err)
	}
	binding := api.CellnParentBinding{Target: os.Getenv("CELLN_INTEROP_ORIGIN"), Principal: "test:parent", RunUID: string(run.UID), SpecSHA256: digest, LaunchProfile: os.Getenv("CELLN_INTEROP_LAUNCH"), Incarnation: os.Getenv("CELLN_INTEROP_PARENT")}
	raw, err := json.Marshal(ApprovalConfig{APIVersion: "sympozium.ai/celln-parent-controller-v1", Approvals: []RunApproval{{Namespace: run.Namespace, Name: run.Name, Binding: binding, TokenFile: os.Getenv("CELLN_INTEROP_TOKEN_FILE")}}})
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(t.TempDir(), "approval.json")
	if err := os.WriteFile(config, raw, 0600); err != nil {
		t.Fatal(err)
	}
	key := client.ObjectKeyFromObject(run)
	poll := func(step func() (bool, error)) {
		t.Helper()
		for {
			done, err := step()
			if err != nil {
				t.Fatal(err)
			}
			if done {
				return
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(50 * time.Millisecond):
			}
		}
	}
	poll(func() (bool, error) {
		observed, err := ReconcileStart(ctx, store, store, key, config)
		return observed.Owner != nil && observed.Owner.Live && observed.Owner.Status == "Ready", err
	})
	if err := store.Get(ctx, key, run); err != nil {
		t.Fatal(err)
	}
	run.Status.Phase = api.AgentRunPhaseRunning
	if err := store.Status().Update(ctx, run); err != nil {
		t.Fatal(err)
	}
	poll(func() (bool, error) { return ReconcileInitialTurn(ctx, store, store, key, config) })
	if err := store.Get(ctx, key, run); err != nil {
		t.Fatal(err)
	}
	initial := run.Status.CellnParent.InitialTurn
	if initial == nil || initial.Result == nil || !initial.Result.Succeeded {
		t.Fatal("initial result not saved")
	}
	turn, err := NewTurn(run, "follow-up", "Find the value stated in the first user message of this conversation. Call uppercase on that value now. Answer only the tool result text.")
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("CELLN_INTEROP_KUBE_CONTEXT") == "" {
		turn.UID = "interop-turn-uid"
	}
	if err := store.Create(ctx, turn); err != nil {
		t.Fatal(err)
	}
	turnKey := client.ObjectKeyFromObject(turn)
	poll(func() (bool, error) { return ReconcileTurn(ctx, store, store, turnKey, config) })
	if err := store.Get(ctx, turnKey, turn); err != nil {
		t.Fatal(err)
	}
	if err := store.Get(ctx, key, run); err != nil {
		t.Fatal(err)
	}
	if run.Status.Phase != api.AgentRunPhaseRunning || run.Status.CellnParent.ActiveTurn != nil || run.Status.CellnParent.AcceptedTurns != 1 {
		t.Fatal("parent phase, serialization slot or turn accounting incorrect")
	}
	completed := turn.Status.Execution
	if completed == nil || completed.Result == nil || !completed.Result.Succeeded || !strings.Contains(strings.ToLower(completed.Result.Answer), "violet") || completed.Child == initial.Child {
		t.Fatal("subsequent turn did not preserve context in a distinct child")
	}
	// Completed reconciliations must be observations, not another model turn.
	for i := 0; i < 2; i++ {
		if done, err := ReconcileTurn(ctx, store, store, turnKey, config); err != nil || !done {
			t.Fatalf("completed reconciliation: %v", err)
		}
	}
	if os.Getenv("CELLN_INTEROP_KUBE_CONTEXT") != "" {
		changedTurn := turn.DeepCopy()
		changedTurn.Status.Execution.Result.Answer = "changed committed answer"
		if err := store.Status().Update(ctx, changedTurn); !apierrors.IsInvalid(err) {
			t.Fatalf("API server did not refuse committed-result mutation: %v", err)
		}
		changedRun := run.DeepCopy()
		changedRun.Status.CellnParent.CreateAttempted = false
		if err := store.Status().Update(ctx, changedRun); !apierrors.IsInvalid(err) {
			t.Fatalf("API server did not refuse parent-attempt rollback: %v", err)
		}
	}
	archive, err := json.Marshal(map[string]any{"run": run, "turn": turn, "kubernetesContext": os.Getenv("CELLN_INTEROP_KUBE_CONTEXT")})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("CELLN_INTEROP_RESULT")+".resources.json", archive, 0600); err != nil {
		t.Fatal(err)
	}
	results := []TurnResult{}
	for _, execution := range []*api.CellnParentTurnStatus{initial, completed} {
		results = append(results, TurnResult{Kind: "completed", Version: "celln.parent-context/v1", TurnID: execution.ID, Succeeded: &execution.Result.Succeeded, Answer: execution.Result.Answer})
	}
	raw, err = json.Marshal(results)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("CELLN_INTEROP_RESULT"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("real parent and turn coordinators passed; Kubernetes context=%q (empty means fake storage)", os.Getenv("CELLN_INTEROP_KUBE_CONTEXT"))
}
