package controller

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	sympoziumv1alpha1 "github.com/alexsjones/sympozium/api/v1alpha1"
)

const skillPackFinalizer = "sympozium.ai/skillpack-finalizer"

// SkillPackReconciler reconciles SkillPack objects.
// It generates ConfigMaps from SkillPack CRDs that are then projected
// into agent pods as skill bundles.
type SkillPackReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Log    logr.Logger
}

// +kubebuilder:rbac:groups=sympozium.ai,resources=skillpacks,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=sympozium.ai,resources=skillpacks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=sympozium.ai,resources=skillpacks/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete

// Reconcile handles SkillPack create/update/delete events.
func (r *SkillPackReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("skillpack", req.NamespacedName)

	skillPack := &sympoziumv1alpha1.SkillPack{}
	if err := r.Get(ctx, req.NamespacedName, skillPack); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Handle deletion
	if !skillPack.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, log, skillPack)
	}

	// Add finalizer
	if !controllerutil.ContainsFinalizer(skillPack, skillPackFinalizer) {
		controllerutil.AddFinalizer(skillPack, skillPackFinalizer)
		if err := r.Update(ctx, skillPack); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Reconcile the ConfigMap
	if err := r.reconcileConfigMap(ctx, log, skillPack); err != nil {
		return ctrl.Result{}, err
	}

	// Update status
	skillPack.Status.Phase = "Ready"
	skillPack.Status.ConfigMapName = skillPack.Name
	skillPack.Status.SkillCount = len(skillPack.Spec.Skills)
	if err := r.Status().Update(ctx, skillPack); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// reconcileDelete handles SkillPack deletion.
func (r *SkillPackReconciler) reconcileDelete(ctx context.Context, log logr.Logger, skillPack *sympoziumv1alpha1.SkillPack) (ctrl.Result, error) {
	log.Info("Reconciling SkillPack deletion")

	// Delete the ConfigMap
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      skillPack.Name,
			Namespace: skillPack.Namespace,
		},
	}
	if err := r.Delete(ctx, cm); err != nil && !errors.IsNotFound(err) {
		return ctrl.Result{}, err
	}

	controllerutil.RemoveFinalizer(skillPack, skillPackFinalizer)
	return ctrl.Result{}, r.Update(ctx, skillPack)
}

// reconcileConfigMap creates or updates the ConfigMap for a SkillPack.
func (r *SkillPackReconciler) reconcileConfigMap(ctx context.Context, log logr.Logger, skillPack *sympoziumv1alpha1.SkillPack) error {
	// Build ConfigMap data from skills
	data := make(map[string]string)
	for _, skill := range skillPack.Spec.Skills {
		key := fmt.Sprintf("%s.md", skill.Name)
		content := skill.Content
		if skill.Description != "" {
			content = fmt.Sprintf("# %s\n\n> %s\n\n%s", skill.Name, skill.Description, content)
		}
		data[key] = content
	}

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      skillPack.Name,
			Namespace: skillPack.Namespace,
			Labels: map[string]string{
				"sympozium.ai/component": "skillpack",
				"sympozium.ai/skillpack": skillPack.Name,
			},
		},
		Data: data,
	}

	if err := controllerutil.SetControllerReference(skillPack, cm, r.Scheme); err != nil {
		return err
	}

	// Create or update
	existing := &corev1.ConfigMap{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(cm), existing); err != nil {
		if errors.IsNotFound(err) {
			log.Info("Creating ConfigMap for SkillPack")
			return r.Create(ctx, cm)
		}
		return err
	}

	// Update existing
	existing.Data = data
	existing.Labels = cm.Labels
	log.Info("Updating ConfigMap for SkillPack")
	return r.Update(ctx, existing)
}

// SetupWithManager sets up the controller with the Manager.
func (r *SkillPackReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&sympoziumv1alpha1.SkillPack{}).
		Owns(&corev1.ConfigMap{}).
		Complete(r)
}
