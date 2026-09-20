package apiserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnparent"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// A restart seeds the new run with as much of the conversation as the fleet's
// starter package takes: all of it on a current package (taskBytes 16384),
// only the newest exchanges that fit 1536 bytes on an old one or when the
// package cannot be established.
func TestContinueRunSizesTheSeedForTheFleetsPackage(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	seed := make([]api.ConversationExchange, 5)
	for i := range seed {
		seed[i] = api.ConversationExchange{User: strings.Repeat("u", 200), Assistant: strings.Repeat("a", 1000)}
	}
	for name, tc := range map[string]struct {
		taskBytes int64
		want      int
	}{"current package": {16384, 5}, "old package": {2048, 1}, "no profile": {0, 1}} {
		t.Run(name, func(t *testing.T) {
			run := &api.AgentRun{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "parent", UID: "parent-uid"},
				Spec: api.AgentRunSpec{AgentRef: "agent", Backend: "celln", ExecutionLifecycle: "enduring", Task: api.NewStringTask(cellnparent.ResumeMessage), CellnSelection: &api.CellnCatalogueSelection{RuntimeRef: "celln-native"},
					Enduring:     &api.EnduringRunSpec{LeaseSeconds: 60, MaxTurns: 3, MaxModelRequests: 18, MaxOutputTokens: 9216},
					Conversation: &api.ConversationSpec{Continuation: "automatic", ContinuesFrom: "earlier", Depth: 1, Seed: seed}}}
			objects := []client.Object{run}
			if tc.taskBytes != 0 {
				objects = append(objects,
					&api.AgentRuntime{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "celln-native"}, Spec: api.AgentRuntimeSpec{CellnProfileRef: &api.CellnRuntimeProfileRef{Name: "celln-native-starter", Revision: "v1"}}},
					&api.CellnRuntimeProfile{ObjectMeta: metav1.ObjectMeta{Name: "celln-native-starter"}, Spec: api.CellnRuntimeProfileSpec{Revision: "v1", Limits: api.AgentRuntimeCellnLimits{TaskBytes: tc.taskBytes}}})
			}
			store := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
			handler := NewServer(store, nil, nil, logr.Discard()).Handler(nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/runs/parent/continue?namespace=default&uid=parent-uid&keep=true", nil))
			if rec.Code != http.StatusCreated {
				t.Fatalf("%d: %s", rec.Code, rec.Body.String())
			}
			var next api.AgentRun
			if err := json.Unmarshal(rec.Body.Bytes(), &next); err != nil {
				t.Fatal(err)
			}
			if got := len(next.Spec.Conversation.Seed); got != tc.want || !cellnparent.SeedFits(next.Spec.Conversation.Seed, cellnparent.SeedBudget(tc.taskBytes)) {
				t.Fatalf("seed carries %d exchanges, want %d", got, tc.want)
			}
		})
	}
}
