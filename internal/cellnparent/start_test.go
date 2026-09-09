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
	"sync/atomic"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestStartupPersistsBeforePostAndReconcilesLostReplyWithoutReplay(t *testing.T) {
	run, binding := admissionFixture(t)
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	store := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&api.AgentRun{}).WithObjects(run).Build()
	key := client.ObjectKeyFromObject(run)
	ctx := context.Background()
	var posts, gets atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			posts.Add(1)
			var saved api.AgentRun
			if err := store.Get(ctx, key, &saved); err != nil {
				t.Error(err)
			}
			if saved.Status.CellnParent == nil || !saved.Status.CellnParent.CreateAttempted {
				t.Error("POST preceded durable creation claim")
			}
			// An ambiguous failed acknowledgement may follow a successful remote create.
			w.WriteHeader(502)
			return
		}
		gets.Add(1)
		fmt.Fprintf(w, `{"incarnation":%q,"status":"Ready","statusIsLiveOwnerObservation":true,"retryAuthorized":false}`, testID)
	}))
	defer server.Close()
	binding.Target = server.URL
	root := t.TempDir()
	token := filepath.Join(root, "token")
	if err := os.WriteFile(token, []byte("startup-test-parent-credential"), 0600); err != nil {
		t.Fatal(err)
	}
	config := ApprovalConfig{APIVersion: "sympozium.ai/celln-parent-controller-v1", Approvals: []RunApproval{{Namespace: run.Namespace, Name: run.Name, Binding: binding, TokenFile: token}}}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "config.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	first, err := ReconcileStart(ctx, store, store, key, path)
	if err != nil || !first.Prepared || first.CreationAccepted || posts.Load() != 0 {
		t.Fatalf("unsafe prepare: %+v %v", first, err)
	}
	if _, err := ReconcileStart(ctx, store, store, key, path); !errors.Is(err, ErrReconcile) {
		t.Fatalf("lost reply: %v", err)
	}
	for range 2 {
		observed, err := ReconcileStart(ctx, store, store, key, path)
		if err != nil || observed.Owner == nil || observed.Owner.Status != "Ready" {
			t.Fatalf("%+v %v", observed, err)
		}
	}
	if posts.Load() != 1 || gets.Load() != 2 {
		t.Fatalf("posts=%d gets=%d", posts.Load(), gets.Load())
	}
}
