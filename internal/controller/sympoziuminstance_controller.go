// Package controller contains the reconciliation logic for Sympozium CRDs.
package controller

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/go-logr/logr"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	sympoziumv1alpha1 "github.com/alexsjones/sympozium/api/v1alpha1"
)

const sympoziumInstanceFinalizer = "sympozium.ai/finalizer"

// SympoziumInstanceReconciler reconciles a SympoziumInstance object.
type SympoziumInstanceReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Log      logr.Logger
	ImageTag string // release tag for Sympozium images
}

// +kubebuilder:rbac:groups=sympozium.ai,resources=sympoziuminstances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=sympozium.ai,resources=sympoziuminstances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=sympozium.ai,resources=sympoziuminstances/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets;configmaps;services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes,verbs=get;list;watch;create;update;patch;delete

// Reconcile handles SympoziumInstance reconciliation.
func (r *SympoziumInstanceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("sympoziuminstance", req.NamespacedName)

	var instance sympoziumv1alpha1.SympoziumInstance
	if err := r.Get(ctx, req.NamespacedName, &instance); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Handle deletion
	if !instance.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&instance, sympoziumInstanceFinalizer) {
			log.Info("Cleaning up instance resources")
			if err := r.cleanupChannelDeployments(ctx, &instance); err != nil {
				return ctrl.Result{}, err
			}
			if err := r.cleanupMemoryConfigMap(ctx, &instance); err != nil {
				log.Error(err, "failed to cleanup memory ConfigMap")
			}
			patch := client.MergeFrom(instance.DeepCopy())
			controllerutil.RemoveFinalizer(&instance, sympoziumInstanceFinalizer)
			if err := r.Patch(ctx, &instance, patch); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// Add finalizer if missing
	if !controllerutil.ContainsFinalizer(&instance, sympoziumInstanceFinalizer) {
		patch := client.MergeFrom(instance.DeepCopy())
		controllerutil.AddFinalizer(&instance, sympoziumInstanceFinalizer)
		if err := r.Patch(ctx, &instance, patch); err != nil {
			return ctrl.Result{}, err
		}
		// Re-fetch so subsequent operations use the latest resourceVersion.
		if err := r.Get(ctx, req.NamespacedName, &instance); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Reconcile channel deployments
	if err := r.reconcileChannels(ctx, &instance); err != nil {
		log.Error(err, "failed to reconcile channels")
		statusBase := instance.DeepCopy()
		instance.Status.Phase = "Error"
		_ = r.Status().Patch(ctx, &instance, client.MergeFrom(statusBase))
		return ctrl.Result{RequeueAfter: 30 * time.Second}, err
	}

	// Reconcile memory ConfigMap
	if err := r.reconcileMemoryConfigMap(ctx, log, &instance); err != nil {
		log.Error(err, "failed to reconcile memory ConfigMap")
	}

	// Reconcile web endpoint
	if err := r.reconcileWebEndpoint(ctx, &instance); err != nil {
		log.Error(err, "failed to reconcile web endpoint")
	}

	// Count active agent pods
	activeCount, hasServing, err := r.countActiveAgentPods(ctx, &instance)
	if err != nil {
		log.Error(err, "failed to count agent pods")
	}

	// Update status
	statusBase := instance.DeepCopy()
	if hasServing {
		instance.Status.Phase = "Serving"
	} else {
		instance.Status.Phase = "Running"
	}
	instance.Status.ActiveAgentPods = activeCount
	if err := r.Status().Patch(ctx, &instance, client.MergeFrom(statusBase)); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
}

// reconcileChannels ensures a Deployment exists for each configured channel.
func (r *SympoziumInstanceReconciler) reconcileChannels(ctx context.Context, instance *sympoziumv1alpha1.SympoziumInstance) error {
	channelStatuses := make([]sympoziumv1alpha1.ChannelStatus, 0, len(instance.Spec.Channels))

	for _, ch := range instance.Spec.Channels {
		deployName := fmt.Sprintf("%s-channel-%s", instance.Name, ch.Type)

		// WhatsApp channels need a PVC for credential persistence (QR link survives restarts)
		if ch.Type == "whatsapp" {
			if err := r.ensureWhatsAppPVC(ctx, instance, deployName); err != nil {
				return err
			}
		}

		var deploy appsv1.Deployment
		err := r.Get(ctx, types.NamespacedName{
			Name:      deployName,
			Namespace: instance.Namespace,
		}, &deploy)

		if errors.IsNotFound(err) {
			// Create channel deployment
			deploy := r.buildChannelDeployment(instance, ch, deployName)
			if err := controllerutil.SetControllerReference(instance, deploy, r.Scheme); err != nil {
				return err
			}
			if err := r.Create(ctx, deploy); err != nil {
				return err
			}
			channelStatuses = append(channelStatuses, sympoziumv1alpha1.ChannelStatus{
				Type:   ch.Type,
				Status: "Pending",
			})
		} else if err != nil {
			return err
		} else {
			status := "Connected"
			if deploy.Status.ReadyReplicas == 0 {
				status = "Disconnected"
			}
			channelStatuses = append(channelStatuses, sympoziumv1alpha1.ChannelStatus{
				Type:   ch.Type,
				Status: status,
			})
		}
	}

	instance.Status.Channels = channelStatuses
	return nil
}

// buildChannelDeployment creates a Deployment spec for a channel pod.
func (r *SympoziumInstanceReconciler) buildChannelDeployment(
	instance *sympoziumv1alpha1.SympoziumInstance,
	ch sympoziumv1alpha1.ChannelSpec,
	name string,
) *appsv1.Deployment {
	replicas := int32(1)
	tag := r.ImageTag
	if tag == "" {
		tag = "latest"
	}
	registry := os.Getenv("SYMPOZIUM_IMAGE_REGISTRY")
	if registry == "" {
		registry = "ghcr.io/alexsjones/sympozium"
	}
	image := fmt.Sprintf("%s/channel-%s:%s", registry, ch.Type, tag)

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: instance.Namespace,
			Labels: map[string]string{
				"sympozium.ai/component": "channel",
				"sympozium.ai/channel":   ch.Type,
				"sympozium.ai/instance":  instance.Name,
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"sympozium.ai/component": "channel",
					"sympozium.ai/channel":   ch.Type,
					"sympozium.ai/instance":  instance.Name,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"sympozium.ai/component": "channel",
						"sympozium.ai/channel":   ch.Type,
						"sympozium.ai/instance":  instance.Name,
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:            "channel",
							Image:           image,
							ImagePullPolicy: corev1.PullIfNotPresent,
							Env: []corev1.EnvVar{
								{Name: "INSTANCE_NAME", Value: instance.Name},
								{Name: "EVENT_BUS_URL", Value: "nats://nats.sympozium-system.svc:4222"},
								{Name: "OTEL_EXPORTER_OTLP_ENDPOINT", Value: resolveOTelEndpoint(instance)},
								{Name: "OTEL_EXPORTER_OTLP_PROTOCOL", Value: "grpc"},
								{Name: "OTEL_SERVICE_NAME", Value: fmt.Sprintf("sympozium-channel-%s", ch.Type)},
							},
						},
					},
				},
			},
		},
	}

	// Inject channel credentials from secret (if referenced)
	if ch.ConfigRef.Secret != "" {
		deploy.Spec.Template.Spec.Containers[0].EnvFrom = []corev1.EnvFromSource{
			{
				SecretRef: &corev1.SecretEnvSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: ch.ConfigRef.Secret,
					},
				},
			},
		}
	}

	// WhatsApp channels need a persistent volume for credential storage
	if ch.Type == "whatsapp" {
		pvcName := fmt.Sprintf("%s-data", name)
		deploy.Spec.Strategy = appsv1.DeploymentStrategy{
			Type: appsv1.RecreateDeploymentStrategyType, // prevent two pods mounting the same PVC
		}
		deploy.Spec.Template.Spec.Volumes = []corev1.Volume{
			{
				Name: "whatsapp-data",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: pvcName,
					},
				},
			},
		}
		deploy.Spec.Template.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{
			{
				Name:      "whatsapp-data",
				MountPath: "/data",
			},
		}
	}

	return deploy
}

// ensureWhatsAppPVC creates a PVC for the WhatsApp credential store if it doesn't exist.
func (r *SympoziumInstanceReconciler) ensureWhatsAppPVC(ctx context.Context, instance *sympoziumv1alpha1.SympoziumInstance, deployName string) error {
	pvcName := fmt.Sprintf("%s-data", deployName)
	var pvc corev1.PersistentVolumeClaim
	err := r.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: instance.Namespace}, &pvc)
	if err == nil {
		return nil // already exists
	}
	if !errors.IsNotFound(err) {
		return err
	}

	pvc = corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName,
			Namespace: instance.Namespace,
			Labels: map[string]string{
				"sympozium.ai/component": "channel",
				"sympozium.ai/channel":   "whatsapp",
				"sympozium.ai/instance":  instance.Name,
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("256Mi"),
				},
			},
		},
	}

	if err := controllerutil.SetControllerReference(instance, &pvc, r.Scheme); err != nil {
		return err
	}

	r.Log.Info("Creating WhatsApp credential PVC", "name", pvcName)
	return r.Create(ctx, &pvc)
}

// cleanupChannelDeployments removes channel deployments owned by the instance.
func (r *SympoziumInstanceReconciler) cleanupChannelDeployments(ctx context.Context, instance *sympoziumv1alpha1.SympoziumInstance) error {
	var deploys appsv1.DeploymentList
	if err := r.List(ctx, &deploys,
		client.InNamespace(instance.Namespace),
		client.MatchingLabels{"sympozium.ai/instance": instance.Name, "sympozium.ai/component": "channel"},
	); err != nil {
		return err
	}

	for i := range deploys.Items {
		if err := r.Delete(ctx, &deploys.Items[i]); err != nil && !errors.IsNotFound(err) {
			return err
		}
	}

	return nil
}

// countActiveAgentPods counts running agent pods for this instance.
func (r *SympoziumInstanceReconciler) countActiveAgentPods(ctx context.Context, instance *sympoziumv1alpha1.SympoziumInstance) (int, bool, error) {
	var runs sympoziumv1alpha1.AgentRunList
	if err := r.List(ctx, &runs,
		client.InNamespace(instance.Namespace),
		client.MatchingLabels{"sympozium.ai/instance": instance.Name},
	); err != nil {
		return 0, false, err
	}

	count := 0
	hasServing := false
	for _, run := range runs.Items {
		if run.Status.Phase == sympoziumv1alpha1.AgentRunPhaseRunning ||
			run.Status.Phase == sympoziumv1alpha1.AgentRunPhaseServing {
			count++
		}
		if run.Status.Phase == sympoziumv1alpha1.AgentRunPhaseServing {
			hasServing = true
		}
	}
	return count, hasServing, nil
}

// reconcileMemoryConfigMap ensures the memory ConfigMap exists when memory is
// enabled for the instance. The ConfigMap is named "<instance>-memory" and
// contains a single key "MEMORY.md".
func (r *SympoziumInstanceReconciler) reconcileMemoryConfigMap(ctx context.Context, log logr.Logger, instance *sympoziumv1alpha1.SympoziumInstance) error {
	if instance.Spec.Memory == nil || !instance.Spec.Memory.Enabled {
		return nil
	}

	cmName := fmt.Sprintf("%s-memory", instance.Name)
	var cm corev1.ConfigMap
	err := r.Get(ctx, types.NamespacedName{Name: cmName, Namespace: instance.Namespace}, &cm)
	if err == nil {
		return nil // Already exists.
	}
	if !errors.IsNotFound(err) {
		return err
	}

	// Create the memory ConfigMap with initial content.
	initialContent := "# Agent Memory\n\nNo memories recorded yet.\n"
	cm = corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cmName,
			Namespace: instance.Namespace,
			Labels: map[string]string{
				"sympozium.ai/instance":  instance.Name,
				"sympozium.ai/component": "memory",
			},
		},
		Data: map[string]string{
			"MEMORY.md": initialContent,
		},
	}

	if err := controllerutil.SetControllerReference(instance, &cm, r.Scheme); err != nil {
		return err
	}

	log.Info("Creating memory ConfigMap", "name", cmName)
	return r.Create(ctx, &cm)
}

// cleanupMemoryConfigMap deletes the memory ConfigMap for an instance.
func (r *SympoziumInstanceReconciler) cleanupMemoryConfigMap(ctx context.Context, instance *sympoziumv1alpha1.SympoziumInstance) error {
	cmName := fmt.Sprintf("%s-memory", instance.Name)
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cmName,
			Namespace: instance.Namespace,
		},
	}
	if err := r.Delete(ctx, cm); err != nil && !errors.IsNotFound(err) {
		return err
	}
	return nil
}

// reconcileWebEndpoint ensures a server-mode AgentRun exists when the
// "web-endpoint" skill is present. The AgentRun controller handles creating
// the actual Deployment + Service.
func (r *SympoziumInstanceReconciler) reconcileWebEndpoint(ctx context.Context, instance *sympoziumv1alpha1.SympoziumInstance) error {
	for _, skill := range instance.Spec.Skills {
		if skill.SkillPackRef == "web-endpoint" || skill.SkillPackRef == "skillpack-web-endpoint" {
			return r.ensureWebEndpointAgentRun(ctx, instance, skill)
		}
	}
	return nil
}

// ensureWebEndpointAgentRun checks for an existing server-mode AgentRun for
// this instance and creates one if none exists. The AgentRun controller will
// detect the RequiresServer sidecar and create a Deployment + Service.
func (r *SympoziumInstanceReconciler) ensureWebEndpointAgentRun(ctx context.Context, instance *sympoziumv1alpha1.SympoziumInstance, webSkill sympoziumv1alpha1.SkillRef) error {
	// Check if a serving AgentRun already exists for this instance.
	var runs sympoziumv1alpha1.AgentRunList
	if err := r.List(ctx, &runs,
		client.InNamespace(instance.Namespace),
		client.MatchingLabels{
			"sympozium.ai/instance":  instance.Name,
			"sympozium.ai/component": "web-endpoint",
		},
	); err != nil {
		return fmt.Errorf("list web-endpoint agent runs: %w", err)
	}

	for _, run := range runs.Items {
		if run.DeletionTimestamp.IsZero() {
			return nil
		}
	}

	// No existing run — create one.
	runName := fmt.Sprintf("%s-web-endpoint", instance.Name)

	agentRun := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      runName,
			Namespace: instance.Namespace,
			Labels: map[string]string{
				"sympozium.ai/instance":  instance.Name,
				"sympozium.ai/component": "web-endpoint",
			},
		},
		Spec: sympoziumv1alpha1.AgentRunSpec{
			InstanceRef: instance.Name,
			AgentID:     "web-endpoint",
			SessionKey:  "web-endpoint",
			Task:        "Serve HTTP requests for this instance",
			Mode:        "server",
			Model: sympoziumv1alpha1.ModelSpec{
				Provider:      resolveProvider(instance),
				Model:         instance.Spec.Agents.Default.Model,
				AuthSecretRef: resolveAuthSecret(instance),
			},
			Skills: []sympoziumv1alpha1.SkillRef{webSkill},
		},
	}

	if instance.Spec.Agents.Default.BaseURL != "" {
		agentRun.Spec.Model.BaseURL = instance.Spec.Agents.Default.BaseURL
	}

	if err := controllerutil.SetControllerReference(instance, agentRun, r.Scheme); err != nil {
		return fmt.Errorf("set owner reference: %w", err)
	}

	if err := r.Create(ctx, agentRun); err != nil {
		if errors.IsAlreadyExists(err) {
			return nil
		}
		return fmt.Errorf("create web-endpoint AgentRun: %w", err)
	}

	r.Log.Info("created web-endpoint AgentRun", "instance", instance.Name, "run", runName)
	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *SympoziumInstanceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&sympoziumv1alpha1.SympoziumInstance{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Complete(r)
}
