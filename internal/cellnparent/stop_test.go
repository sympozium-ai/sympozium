package cellnparent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCleanupAfterSpecChangeAndDeletionRequiresConfirmedOriginalOwner(t *testing.T) {
	for _, code := range []int{200, 404, 409, 502} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			run, binding := admissionFixture(t)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != "POST" || r.URL.Path != "/v1/parents/"+testID+"/stop" {
					t.Error("cleanup attempted something other than original owner stop")
				}
				w.WriteHeader(code)
				fmt.Fprint(w, `{"teardownConfirmed":true}`)
			}))
			defer server.Close()
			binding.Target = server.URL
			run.Status.CellnParent = &api.CellnParentStatus{Binding: binding, CreateAttempted: true}
			run.Spec.Enduring.MaxTurns++ // Cleanup must not re-admit changed intent.
			now := metav1.Now()
			run.DeletionTimestamp = &now
			run.Finalizers = []string{"test/retain-until-joined"}
			scheme := runtime.NewScheme()
			if err := api.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			store := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&api.AgentRun{}).WithObjects(run).Build()
			root := t.TempDir()
			token := filepath.Join(root, "token")
			if err := os.WriteFile(token, []byte("cleanup-parent-credential-long-enough"), 0600); err != nil {
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
			if _, _, err := LoadApproval(path, run); err == nil {
				t.Fatal("changed/deleting run admitted for creation")
			}
			err = ReconcileStop(context.Background(), store, client.ObjectKeyFromObject(run), path)
			if (err == nil) != (code == 200) {
				t.Fatalf("status %d: %v", code, err)
			}
			if calls.Load() != 1 {
				t.Fatal("cleanup retried")
			}
			config.Approvals[0].Binding.Principal = "replacement-principal"
			raw, _ = json.Marshal(config)
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if ReconcileStop(context.Background(), store, client.ObjectKeyFromObject(run), path) == nil {
				t.Fatal("cleanup retargeted principal")
			}
			if calls.Load() != 1 {
				t.Fatal("changed authority reached host")
			}
		})
	}
}
