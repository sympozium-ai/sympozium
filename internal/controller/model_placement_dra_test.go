package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	llmfitv1alpha1 "github.com/sympozium-ai/llmfit-dra/api/v1alpha1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/dra"
)

func draTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := llmfitv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

func draModel(mode sympoziumv1alpha1.PlacementMode) *sympoziumv1alpha1.Model {
	m := newTestModel("default", "qwen", sympoziumv1alpha1.ModelPhasePlacing)
	m.Spec.Placement = sympoziumv1alpha1.ModelPlacement{
		Mode:             mode,
		MinTps:           ptr.To(int32(25)),
		MinComputeTFLOPS: ptr.To(int64(120)),
	}
	m.Spec.Source = sympoziumv1alpha1.ModelSource{ModelID: "Qwen/Qwen2.5-1.5B-Instruct"}
	return m
}

func TestUsesDRAPlacement(t *testing.T) {
	r := &ModelReconciler{DRA: dra.Static(true)}
	if !r.usesDRAPlacement(draModel(sympoziumv1alpha1.PlacementDRA)) {
		t.Error("mode dra must use claim placement")
	}
	if !r.usesDRAPlacement(draModel(sympoziumv1alpha1.PlacementAuto)) {
		t.Error("mode auto with DRA available must use claim placement")
	}
	r.DRA = dra.Static(false)
	if r.usesDRAPlacement(draModel(sympoziumv1alpha1.PlacementAuto)) {
		t.Error("mode auto without DRA must fall back to legacy placement")
	}
	if r.usesDRAPlacement(draModel(sympoziumv1alpha1.PlacementManual)) {
		t.Error("manual mode must never use claim placement")
	}
}

func TestReconcilePlacingDRACreatesClaimAndWaits(t *testing.T) {
	scheme := draTestScheme(t)
	model := draModel(sympoziumv1alpha1.PlacementDRA)
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(model).WithStatusSubresource(model, &llmfitv1alpha1.ModelClaim{}).Build()
	r := &ModelReconciler{Client: cl, Scheme: scheme, DRA: dra.Static(true)}

	res, err := r.reconcilePlacingDRA(context.Background(), model, logr.Discard())
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Error("unresolved claim must requeue")
	}

	var mc llmfitv1alpha1.ModelClaim
	if err := cl.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "qwen"}, &mc); err != nil {
		t.Fatalf("ModelClaim not created: %v", err)
	}
	if mc.Spec.Model != ModelQueryForModel(model) {
		t.Errorf("claim model = %q, want the model query", mc.Spec.Model)
	}
	if mc.Spec.MinTps == nil || *mc.Spec.MinTps != 25 {
		t.Errorf("minTps not mapped: %+v", mc.Spec.MinTps)
	}
	if mc.Spec.MinComputeTFLOPS == nil || *mc.Spec.MinComputeTFLOPS != 120 {
		t.Errorf("minComputeTFLOPS not mapped: %+v", mc.Spec.MinComputeTFLOPS)
	}
	if !metav1.IsControlledBy(&mc, model) {
		t.Errorf("claim must be controller-owned by the Model: %+v", mc.OwnerReferences)
	}

	var fresh sympoziumv1alpha1.Model
	if err := cl.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "qwen"}, &fresh); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fresh.Status.PlacementMessage, "waiting for ModelClaim") {
		t.Errorf("placement message = %q", fresh.Status.PlacementMessage)
	}
}

func TestReconcilePlacingDRAResolvedProceedsWithShortfallSurfaced(t *testing.T) {
	scheme := draTestScheme(t)
	model := draModel(sympoziumv1alpha1.PlacementDRA)
	mc := &llmfitv1alpha1.ModelClaim{ObjectMeta: metav1.ObjectMeta{Name: "qwen", Namespace: "default"}}
	apimeta.SetStatusCondition(&mc.Status.Conditions, metav1.Condition{
		Type: llmfitv1alpha1.ConditionResolved, Status: metav1.ConditionTrue,
		Reason: llmfitv1alpha1.ReasonResolved, Message: "resolved",
	})
	apimeta.SetStatusCondition(&mc.Status.Conditions, metav1.Condition{
		Type: llmfitv1alpha1.ConditionSatisfiable, Status: metav1.ConditionFalse,
		Reason: llmfitv1alpha1.ReasonNoCandidates, Message: "closest device gpu0 (node strix): compute 59 < 120 TFLOPS",
	})
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(model, mc).WithStatusSubresource(model, mc).Build()
	r := &ModelReconciler{Client: cl, Scheme: scheme, DRA: dra.Static(true)}

	if _, err := r.reconcilePlacingDRA(context.Background(), model, logr.Discard()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var fresh sympoziumv1alpha1.Model
	if err := cl.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "qwen"}, &fresh); err != nil {
		t.Fatal(err)
	}
	// Satisfiable is advisory: the Model proceeds to Pending, and the exact
	// shortfall is the placement message (pods queue; operators see why).
	if fresh.Status.Phase != sympoziumv1alpha1.ModelPhasePending {
		t.Errorf("phase = %s, want Pending", fresh.Status.Phase)
	}
	if !strings.Contains(fresh.Status.PlacementMessage, "compute 59 < 120 TFLOPS") {
		t.Errorf("placement message = %q, want the shortfall", fresh.Status.PlacementMessage)
	}
	if fresh.Status.PlacedNode != "" {
		t.Errorf("PlacedNode must stay empty in claim placement (scheduler decides), got %q", fresh.Status.PlacedNode)
	}
}
