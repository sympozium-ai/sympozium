package controller_test

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestHandsOnCleanupStaysInOwnedNamespace(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	store := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&api.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "one", Namespace: "owned", UID: "one"}},
		&api.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "two", Namespace: "owned", UID: "two"}},
		&api.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "older", Namespace: "unrelated", UID: "older"}},
	).Build()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if !cleanupLiveParentRuns(ctx, store, "owned") {
		t.Fatal("owned runs were not removed")
	}
	if err := store.Get(ctx, client.ObjectKey{Namespace: "unrelated", Name: "older"}, &api.AgentRun{}); err != nil {
		t.Fatal("unrelated run changed", err)
	}
}

func TestHandsOnCleanupRetainsUnconfirmedParent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		scheme := runtime.NewScheme()
		if err := api.AddToScheme(scheme); err != nil {
			t.Fatal(err)
		}
		store := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&api.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "unconfirmed", Namespace: "owned", UID: "original", Finalizers: []string{"celln-parent"}}}).Build()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if cleanupLiveParentRuns(ctx, store, "owned") {
			t.Fatal("unconfirmed finalizer counted as removed")
		}
		run := &api.AgentRun{}
		if err := store.Get(context.Background(), client.ObjectKey{Namespace: "owned", Name: "unconfirmed"}, run); err != nil || run.DeletionTimestamp == nil || len(run.Finalizers) != 1 {
			t.Fatal("original parent cleanup authority was removed", err)
		}
	})
}
