package controller

import (
	"context"
	"reflect"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestParentOnlyDoesNotMutateUnrelatedWorkloads(t *testing.T) {
	for _, mode := range []string{"pod", "celln-one-shot", "existing-job", "existing-deployment", "existing-celln-request"} {
		t.Run(mode, func(t *testing.T) {
			run := &api.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "run", Namespace: "tenant", UID: "uid", Generation: 1}, Spec: api.AgentRunSpec{Backend: "celln", ExecutionLifecycle: "enduring"}}
			switch mode {
			case "pod":
				run.Spec.Backend = "kubernetes"
			case "celln-one-shot":
				run.Spec.ExecutionLifecycle = "one-shot"
			case "existing-job":
				run.Status.JobName = "job"
			case "existing-deployment":
				run.Status.DeploymentName = "deployment"
			case "existing-celln-request":
				run.Status.CellnRequest = "already-issued"
			}
			scheme := runtime.NewScheme()
			if err := api.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			store := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
			key := client.ObjectKeyFromObject(run)
			if err := store.Get(context.Background(), key, run); err != nil {
				t.Fatal(err)
			}
			before := run.DeepCopy()
			r := &AgentRunReconciler{ParentOnly: true, Client: store, APIReader: store, Scheme: scheme}
			if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
				t.Fatal(err)
			}
			if err := store.Get(context.Background(), key, run); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, run) {
				t.Fatal("parent-only controller mutated unrelated run")
			}
		})
	}
}

func TestParentOnlyRetainsOriginalParentRecovery(t *testing.T) {
	run := &api.AgentRun{Spec: api.AgentRunSpec{Backend: "celln", ExecutionLifecycle: "enduring"}}
	if !parentOnlyRun(run) {
		t.Fatal("native parent ignored")
	}
	run.Spec.Backend = "kubernetes"
	run.Spec.ExecutionLifecycle = "one-shot"
	run.Status.CellnParent = &api.CellnParentStatus{}
	if !parentOnlyRun(run) {
		t.Fatal("changed intent discarded original parent cleanup")
	}
}
