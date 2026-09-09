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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestInitialTurnDurableClaimAndLostReplyRecovery(t *testing.T) {
	run, binding := admissionFixture(t)
	if err := json.Unmarshal([]byte(`"Remember violet"`), &run.Spec.Task); err != nil {
		t.Fatal(err)
	}
	digest, err := SpecDigest(run.Spec)
	if err != nil {
		t.Fatal(err)
	}
	binding.SpecSHA256 = digest
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	var posts atomic.Int32
	var store client.Client
	key := client.ObjectKeyFromObject(run)
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			posts.Add(1)
			var saved api.AgentRun
			if err := store.Get(ctx, key, &saved); err != nil {
				t.Error(err)
			}
			if saved.Status.CellnParent.InitialTurn == nil || !saved.Status.CellnParent.InitialTurn.Attempted {
				t.Error("turn sent before durable claim")
			}
			w.WriteHeader(502)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/turns/initial") {
			fmt.Fprintf(w, `{"ownerStatus":"Ready","retryAuthorized":false,"turn":{"stage":"parent-committed","record":{"version":1,"parent":%q,"child":%q,"turnId":"initial","succeeded":true,"answer":"violet"}}}`, testID, childIdentity(testID, "initial"))
			return
		}
		fmt.Fprintf(w, `{"incarnation":%q,"status":"Ready","statusIsLiveOwnerObservation":true,"retryAuthorized":false}`, testID)
	}))
	defer server.Close()
	binding.Target = server.URL
	run.Status.CellnParent = &api.CellnParentStatus{Binding: binding, CreateAttempted: true}
	run.Status.Phase = api.AgentRunPhaseRunning
	store = fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&api.AgentRun{}).WithObjects(run).Build()
	root := t.TempDir()
	token := filepath.Join(root, "token")
	if err := os.WriteFile(token, []byte("initial-turn-controller-credential"), 0600); err != nil {
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
	if done, err := ReconcileInitialTurn(ctx, store, store, key, path); err != nil || done || posts.Load() != 0 {
		t.Fatalf("prepare: %v %v", done, err)
	}
	if _, err := RecoverInitialTurn(ctx, store, store, key, path); !errors.Is(err, ErrReconcile) || posts.Load() != 0 {
		t.Fatal("recovery attempted to submit an unattempted turn")
	}
	if _, err := ReconcileInitialTurn(ctx, store, store, key, path); !errors.Is(err, ErrReconcile) {
		t.Fatalf("lost reply: %v", err)
	}
	for range 2 {
		if done, err := ReconcileInitialTurn(ctx, store, store, key, path); err != nil || !done {
			t.Fatalf("recover: %v %v", done, err)
		}
	}
	var saved api.AgentRun
	if err := store.Get(ctx, key, &saved); err != nil {
		t.Fatal(err)
	}
	if posts.Load() != 1 || saved.Status.CellnParent.InitialTurn.Result.Answer != "violet" || saved.Status.Phase != api.AgentRunPhaseRunning {
		t.Fatal("turn replayed, result lost or enduring run completed")
	}
}
