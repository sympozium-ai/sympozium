package cellnparent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestSubsequentTurnsDispatchOnceRecoverAndReleaseAfterPersistence(t *testing.T) {
	ctx := context.Background()
	run, binding := admissionFixture(t)
	run.Spec.Enduring.MaxTurns = 3
	binding.SpecSHA256, _ = SpecDigest(run.Spec)
	var posts atomic.Int32
	var cancellations atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/cancel") {
			if !strings.HasPrefix(r.URL.Path, "/v1/parents/"+testID+"/turns/") {
				t.Error("cancellation targeted parent instead of exact child")
			}
			cancellations.Add(1)
			// An uncertain acknowledgement must not authorize another send.
			w.WriteHeader(502)
			return
		}
		if r.Method == "POST" {
			if !strings.HasSuffix(r.URL.Path, "/turns") {
				t.Error("unexpected parent creation")
			}
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if posts.Add(1) == 1 {
				w.WriteHeader(502)
				return
			}
			fmt.Fprintf(w, `{"kind":"completed","apiVersion":"celln.parent-context/v1","turnId":%q,"succeeded":true,"answer":"answer"}`, body["turnId"])
			return
		}
		if strings.Contains(r.URL.Path, "/turns/") {
			id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			fmt.Fprintf(w, `{"ownerStatus":"Ready","retryAuthorized":false,"turn":{"stage":"parent-committed","record":{"version":1,"parent":%q,"child":%q,"turnId":%q,"succeeded":true,"answer":"answer"}}}`, testID, childIdentity(testID, id), id)
			return
		}
		fmt.Fprintf(w, `{"incarnation":%q,"status":"Ready","statusIsLiveOwnerObservation":true,"retryAuthorized":false}`, testID)
	}))
	defer server.Close()
	binding.Target = server.URL
	run.Status.Phase = api.AgentRunPhaseRunning
	run.Status.CellnParent = &api.CellnParentStatus{Binding: binding, CreateAttempted: true, InitialTurn: &api.CellnParentTurnStatus{ID: "initial", Message: "hello", Child: testID, Attempted: true, Result: &api.CellnParentTurnResult{Succeeded: true, Answer: "ready"}}}
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	store := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&api.AgentRun{}, &api.AgentRunTurn{}).WithObjects(run).Build()
	root := t.TempDir()
	token := filepath.Join(root, "token")
	if err := os.WriteFile(token, []byte("subsequent-turn-parent-credential"), 0600); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(ApprovalConfig{APIVersion: "sympozium.ai/celln-parent-controller-v1", Approvals: []RunApproval{{Namespace: run.Namespace, Name: run.Name, Binding: binding, TokenFile: token}}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "config.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	for index, name := range []string{"second", "third"} {
		turn, err := NewTurn(run, name, "follow up")
		if err != nil {
			t.Fatal(err)
		}
		turn.UID = types.UID(name + "-uid")
		if err := store.Create(ctx, turn); err != nil {
			t.Fatal(err)
		}
		key := client.ObjectKeyFromObject(turn)
		done, err := ReconcileTurn(ctx, store, store, key, path)
		if err != nil || done {
			t.Fatalf("prepare: %v %v", done, err)
		}
		done, err = ReconcileTurn(ctx, store, store, key, path)
		if index == 0 {
			if !errors.Is(err, ErrReconcile) || done {
				t.Fatalf("ambiguous turn: %v %v", done, err)
			}
			var active api.AgentRun
			if err := store.Get(ctx, client.ObjectKeyFromObject(run), &active); err != nil {
				t.Fatal(err)
			}
			if active.Status.CellnParent.ActiveTurn == nil {
				t.Fatal("lost response released slot")
			}
			var cancelling api.AgentRunTurn
			if err := store.Get(ctx, key, &cancelling); err != nil {
				t.Fatal(err)
			}
			cancelling.Spec.CancelRequested = true
			if err := store.Update(ctx, &cancelling); err != nil {
				t.Fatal(err)
			}
			if done, err := ReconcileTurn(ctx, store, store, key, path); done || !errors.Is(err, ErrReconcile) {
				t.Fatalf("uncertain cancellation: %v %v", done, err)
			}
			if err := store.Get(ctx, key, &cancelling); err != nil {
				t.Fatal(err)
			}
			if !cancelling.Status.CancelAttempted || cancelling.Status.Execution.Result != nil {
				t.Fatal("cancellation attempt not durable or fabricated result")
			}
			if err := store.Get(ctx, client.ObjectKeyFromObject(run), &active); err != nil {
				t.Fatal(err)
			}
			if active.Status.CellnParent.ActiveTurn == nil {
				t.Fatal("uncertain cancellation released slot")
			}
			done, err = ReconcileTurn(ctx, store, store, key, path)
		}
		if err != nil || !done {
			t.Fatalf("completion: %v %v", done, err)
		}
		if done, err := ReconcileTurn(ctx, store, store, key, path); err != nil || !done {
			t.Fatalf("repeat observation: %v %v", done, err)
		}
		var saved api.AgentRunTurn
		if err := store.Get(ctx, key, &saved); err != nil {
			t.Fatal(err)
		}
		if saved.Status.Execution.Result == nil || saved.Status.Execution.Result.Answer != "answer" {
			t.Fatal("result not durable")
		}
	}
	var parent api.AgentRun
	if err := store.Get(ctx, client.ObjectKeyFromObject(run), &parent); err != nil {
		t.Fatal(err)
	}
	if posts.Load() != 2 || cancellations.Load() != 1 || parent.Status.CellnParent.ActiveTurn != nil || parent.Status.CellnParent.AcceptedTurns != 2 || parent.Status.Phase != api.AgentRunPhaseRunning {
		t.Fatal("replay, leaked slot, budget error or parent completed")
	}
}
