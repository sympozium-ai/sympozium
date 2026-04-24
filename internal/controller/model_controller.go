package controller

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

const (
	modelFinalizer   = "sympozium.ai/model-finalizer"
	modelMountPath   = "/models"
	downloadJobImage = "curlimages/curl:8.7.1"
)

// ModelReconciler reconciles Model objects.
type ModelReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Log    logr.Logger
}

// +kubebuilder:rbac:groups=sympozium.ai,resources=models,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=sympozium.ai,resources=models/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete

func (r *ModelReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("model", req.NamespacedName)

	var model sympoziumv1alpha1.Model
	if err := r.Get(ctx, req.NamespacedName, &model); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Initialize phase
	if model.Status.Phase == "" {
		model.Status.Phase = sympoziumv1alpha1.ModelPhasePending
		model.Status.Message = "Model created, provisioning storage"
		if err := r.Status().Update(ctx, &model); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	switch model.Status.Phase {
	case sympoziumv1alpha1.ModelPhasePending:
		return r.reconcilePending(ctx, &model, log)
	case sympoziumv1alpha1.ModelPhaseDownloading:
		return r.reconcileDownloading(ctx, &model, log)
	case sympoziumv1alpha1.ModelPhaseLoading:
		return r.reconcileLoading(ctx, &model, log)
	case sympoziumv1alpha1.ModelPhaseReady:
		return r.reconcileReady(ctx, &model, log)
	case sympoziumv1alpha1.ModelPhaseFailed:
		return r.reconcileFailed(ctx, &model, log)
	}

	return ctrl.Result{}, nil
}

// reconcilePending creates the PVC and starts the download Job.
func (r *ModelReconciler) reconcilePending(ctx context.Context, model *sympoziumv1alpha1.Model, log logr.Logger) (ctrl.Result, error) {
	// Ensure PVC
	if err := r.ensurePVC(ctx, model, log); err != nil {
		return ctrl.Result{}, err
	}

	// Create download Job
	if err := r.ensureDownloadJob(ctx, model, log); err != nil {
		return ctrl.Result{}, err
	}

	// Transition to Downloading
	model.Status.Phase = sympoziumv1alpha1.ModelPhaseDownloading
	model.Status.Message = "Downloading model weights"
	meta.SetStatusCondition(&model.Status.Conditions, metav1.Condition{
		Type:               "Downloaded",
		Status:             metav1.ConditionFalse,
		Reason:             "Downloading",
		Message:            "Model download in progress",
		ObservedGeneration: model.Generation,
	})
	if err := r.Status().Update(ctx, model); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

// reconcileDownloading polls the download Job for completion.
func (r *ModelReconciler) reconcileDownloading(ctx context.Context, model *sympoziumv1alpha1.Model, log logr.Logger) (ctrl.Result, error) {
	jobName := r.downloadJobName(model)
	var job batchv1.Job
	if err := r.Get(ctx, types.NamespacedName{Name: jobName, Namespace: model.Namespace}, &job); err != nil {
		if errors.IsNotFound(err) {
			// Job was cleaned up, restart download
			log.Info("Download job not found, recreating")
			model.Status.Phase = sympoziumv1alpha1.ModelPhasePending
			model.Status.Message = "Download job not found, restarting"
			return ctrl.Result{}, r.Status().Update(ctx, model)
		}
		return ctrl.Result{}, err
	}

	// Check Job status
	if job.Status.Succeeded > 0 {
		log.Info("Model download complete")
		meta.SetStatusCondition(&model.Status.Conditions, metav1.Condition{
			Type:               "Downloaded",
			Status:             metav1.ConditionTrue,
			Reason:             "DownloadComplete",
			Message:            "Model weights downloaded successfully",
			ObservedGeneration: model.Generation,
		})

		// Start inference server
		if err := r.ensureDeployment(ctx, model, log); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.ensureService(ctx, model, log); err != nil {
			return ctrl.Result{}, err
		}

		model.Status.Phase = sympoziumv1alpha1.ModelPhaseLoading
		model.Status.Message = "Loading model into inference server"
		meta.SetStatusCondition(&model.Status.Conditions, metav1.Condition{
			Type:               "ServerReady",
			Status:             metav1.ConditionFalse,
			Reason:             "Loading",
			Message:            "Inference server starting",
			ObservedGeneration: model.Generation,
		})
		if err := r.Status().Update(ctx, model); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	if job.Status.Failed > 0 {
		log.Info("Model download failed")
		model.Status.Phase = sympoziumv1alpha1.ModelPhaseFailed
		model.Status.Message = "Download job failed"
		meta.SetStatusCondition(&model.Status.Conditions, metav1.Condition{
			Type:               "Downloaded",
			Status:             metav1.ConditionFalse,
			Reason:             "DownloadFailed",
			Message:            "Model download job failed",
			ObservedGeneration: model.Generation,
		})
		if err := r.Status().Update(ctx, model); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Still downloading
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

// reconcileLoading checks if the inference server Deployment is ready.
func (r *ModelReconciler) reconcileLoading(ctx context.Context, model *sympoziumv1alpha1.Model, log logr.Logger) (ctrl.Result, error) {
	deployName := r.deploymentName(model)
	var deploy appsv1.Deployment
	if err := r.Get(ctx, types.NamespacedName{Name: deployName, Namespace: model.Namespace}, &deploy); err != nil {
		if errors.IsNotFound(err) {
			// Deployment was deleted, recreate
			if err := r.ensureDeployment(ctx, model, log); err != nil {
				return ctrl.Result{}, err
			}
			if err := r.ensureService(ctx, model, log); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		return ctrl.Result{}, err
	}

	if deploy.Status.ReadyReplicas > 0 {
		log.Info("Inference server ready")
		port := r.inferencePort(model)
		model.Status.Phase = sympoziumv1alpha1.ModelPhaseReady
		model.Status.Endpoint = fmt.Sprintf("http://%s.%s.svc:%d/v1", r.serviceName(model), model.Namespace, port)
		model.Status.Message = "Model is serving inference requests"
		meta.SetStatusCondition(&model.Status.Conditions, metav1.Condition{
			Type:               "ServerReady",
			Status:             metav1.ConditionTrue,
			Reason:             "Ready",
			Message:            "Inference server is ready",
			ObservedGeneration: model.Generation,
		})
		if err := r.Status().Update(ctx, model); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Still loading
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

// reconcileReady verifies the inference server is still healthy.
func (r *ModelReconciler) reconcileReady(ctx context.Context, model *sympoziumv1alpha1.Model, log logr.Logger) (ctrl.Result, error) {
	// Ensure deployment spec stays in sync with the Model spec.
	if err := r.ensureDeployment(ctx, model, log); err != nil {
		return ctrl.Result{}, err
	}

	deployName := r.deploymentName(model)
	var deploy appsv1.Deployment
	if err := r.Get(ctx, types.NamespacedName{Name: deployName, Namespace: model.Namespace}, &deploy); err != nil {
		if errors.IsNotFound(err) {
			log.Info("Deployment disappeared, transitioning to Loading")
			model.Status.Phase = sympoziumv1alpha1.ModelPhaseLoading
			model.Status.Message = "Inference server deployment not found, recreating"
			model.Status.Endpoint = ""
			return ctrl.Result{}, r.Status().Update(ctx, model)
		}
		return ctrl.Result{}, err
	}

	if deploy.Status.ReadyReplicas == 0 {
		log.Info("Inference server no longer ready")
		model.Status.Phase = sympoziumv1alpha1.ModelPhaseLoading
		model.Status.Message = "Inference server lost readiness"
		model.Status.Endpoint = ""
		meta.SetStatusCondition(&model.Status.Conditions, metav1.Condition{
			Type:               "ServerReady",
			Status:             metav1.ConditionFalse,
			Reason:             "NotReady",
			Message:            "Inference server replicas not ready",
			ObservedGeneration: model.Generation,
		})
		if err := r.Status().Update(ctx, model); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	return ctrl.Result{}, nil
}

// reconcileFailed handles retries for failed models.
func (r *ModelReconciler) reconcileFailed(ctx context.Context, model *sympoziumv1alpha1.Model, log logr.Logger) (ctrl.Result, error) {
	// If the spec was updated (generation changed), retry
	downloadedCond := meta.FindStatusCondition(model.Status.Conditions, "Downloaded")
	if downloadedCond != nil && downloadedCond.ObservedGeneration < model.Generation {
		log.Info("Spec updated, retrying")
		model.Status.Phase = sympoziumv1alpha1.ModelPhasePending
		model.Status.Message = "Retrying after spec update"
		if err := r.Status().Update(ctx, model); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}
	return ctrl.Result{}, nil
}

// --- Resource builders ---

func (r *ModelReconciler) pvcName(model *sympoziumv1alpha1.Model) string {
	return fmt.Sprintf("model-%s", model.Name)
}

func (r *ModelReconciler) downloadJobName(model *sympoziumv1alpha1.Model) string {
	return fmt.Sprintf("model-%s-download", model.Name)
}

func (r *ModelReconciler) deploymentName(model *sympoziumv1alpha1.Model) string {
	return fmt.Sprintf("model-%s", model.Name)
}

func (r *ModelReconciler) serviceName(model *sympoziumv1alpha1.Model) string {
	return fmt.Sprintf("model-%s", model.Name)
}

func (r *ModelReconciler) inferencePort(model *sympoziumv1alpha1.Model) int32 {
	if model.Spec.Inference.Port > 0 {
		return model.Spec.Inference.Port
	}
	return 8080
}

func (r *ModelReconciler) inferenceImage(model *sympoziumv1alpha1.Model) string {
	if model.Spec.Inference.Image != "" {
		return model.Spec.Inference.Image
	}
	return "ghcr.io/ggml-org/llama.cpp:server"
}

func (r *ModelReconciler) modelFilename(model *sympoziumv1alpha1.Model) string {
	if model.Spec.Source.Filename != "" {
		return model.Spec.Source.Filename
	}
	return "model.gguf"
}

func (r *ModelReconciler) ensurePVC(ctx context.Context, model *sympoziumv1alpha1.Model, log logr.Logger) error {
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      r.pvcName(model),
			Namespace: model.Namespace,
		},
	}

	result, err := controllerutil.CreateOrUpdate(ctx, r.Client, pvc, func() error {
		if err := controllerutil.SetControllerReference(model, pvc, r.Scheme); err != nil {
			return err
		}

		size := model.Spec.Storage.Size
		if size == "" {
			size = "10Gi"
		}
		storageSize := resource.MustParse(size)

		pvc.Spec.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
		pvc.Spec.Resources = corev1.VolumeResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceStorage: storageSize,
			},
		}

		if model.Spec.Storage.StorageClass != "" {
			pvc.Spec.StorageClassName = &model.Spec.Storage.StorageClass
		}

		return nil
	})
	if err != nil {
		return err
	}
	log.Info("PVC reconciled", "result", result)
	return nil
}

func (r *ModelReconciler) ensureDownloadJob(ctx context.Context, model *sympoziumv1alpha1.Model, log logr.Logger) error {
	jobName := r.downloadJobName(model)

	// Check if Job already exists
	var existing batchv1.Job
	if err := r.Get(ctx, types.NamespacedName{Name: jobName, Namespace: model.Namespace}, &existing); err == nil {
		log.Info("Download job already exists")
		return nil
	}

	filename := r.modelFilename(model)
	modelPath := filepath.Join(modelMountPath, filename)
	tempPath := modelPath + ".tmp"

	// Download with atomic rename: download to .tmp then move
	downloadScript := fmt.Sprintf(`set -e
if [ -f "%s" ]; then
  echo "Model file already exists, skipping download"
  exit 0
fi
echo "Downloading model from %s"
curl -L --fail --retry 3 --retry-delay 5 -o "%s" "%s"
mv "%s" "%s"
echo "Download complete"`,
		modelPath,
		model.Spec.Source.URL,
		tempPath, model.Spec.Source.URL,
		tempPath, modelPath,
	)

	backoffLimit := int32(2)
	ttlSeconds := int32(300)

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: model.Namespace,
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoffLimit,
			TTLSecondsAfterFinished: &ttlSeconds,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{
						{
							Name:    "download",
							Image:   downloadJobImage,
							Command: []string{"sh", "-c", downloadScript},
							VolumeMounts: []corev1.VolumeMount{
								{Name: "model-storage", MountPath: modelMountPath},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "model-storage",
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: r.pvcName(model),
								},
							},
						},
					},
					NodeSelector: model.Spec.NodeSelector,
					Tolerations:  model.Spec.Tolerations,
				},
			},
		},
	}

	if err := controllerutil.SetControllerReference(model, job, r.Scheme); err != nil {
		return err
	}

	if err := r.Create(ctx, job); err != nil {
		return err
	}
	log.Info("Download job created", "job", jobName)
	return nil
}

func (r *ModelReconciler) ensureDeployment(ctx context.Context, model *sympoziumv1alpha1.Model, log logr.Logger) error {
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      r.deploymentName(model),
			Namespace: model.Namespace,
		},
	}

	port := r.inferencePort(model)
	filename := r.modelFilename(model)
	image := r.inferenceImage(model)

	result, err := controllerutil.CreateOrUpdate(ctx, r.Client, deploy, func() error {
		if err := controllerutil.SetControllerReference(model, deploy, r.Scheme); err != nil {
			return err
		}

		replicas := int32(1)
		deploy.Spec.Replicas = &replicas

		labels := map[string]string{
			"app.kubernetes.io/name":       "model",
			"app.kubernetes.io/instance":   model.Name,
			"app.kubernetes.io/managed-by": "sympozium",
		}
		deploy.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		deploy.Spec.Template.ObjectMeta = metav1.ObjectMeta{Labels: labels}

		// Build args: --model <path> --port <port> --host 0.0.0.0 --ctx-size <n> --threads <n> + user args
		ctxSize := model.Spec.Inference.ContextSize
		if ctxSize <= 0 {
			ctxSize = 4096
		}

		// Set thread count from CPU request so llama.cpp uses all allocated cores.
		cpuStr := model.Spec.Resources.CPU
		if cpuStr == "" {
			cpuStr = "4"
		}
		cpuQty := resource.MustParse(cpuStr)
		threads := cpuQty.Value()
		if threads < 1 {
			threads = 1
		}

		args := []string{
			"--model", filepath.Join(modelMountPath, filename),
			"--port", fmt.Sprintf("%d", port),
			"--host", "0.0.0.0",
			"--ctx-size", fmt.Sprintf("%d", ctxSize),
			"--threads", fmt.Sprintf("%d", threads),
		}
		args = append(args, model.Spec.Inference.Args...)

		// Resource requirements
		resources := corev1.ResourceRequirements{}
		if model.Spec.Resources.Memory != "" {
			mem := resource.MustParse(model.Spec.Resources.Memory)
			resources.Requests = corev1.ResourceList{corev1.ResourceMemory: mem}
			resources.Limits = corev1.ResourceList{corev1.ResourceMemory: mem}
		}
		if model.Spec.Resources.CPU != "" {
			cpu := resource.MustParse(model.Spec.Resources.CPU)
			if resources.Requests == nil {
				resources.Requests = corev1.ResourceList{}
			}
			if resources.Limits == nil {
				resources.Limits = corev1.ResourceList{}
			}
			resources.Requests[corev1.ResourceCPU] = cpu
			resources.Limits[corev1.ResourceCPU] = cpu
		}
		if model.Spec.Resources.GPU > 0 {
			gpuQty := resource.MustParse(fmt.Sprintf("%d", model.Spec.Resources.GPU))
			if resources.Limits == nil {
				resources.Limits = corev1.ResourceList{}
			}
			resources.Limits["nvidia.com/gpu"] = gpuQty
		}

		container := corev1.Container{
			Name:      "llama-server",
			Image:     image,
			Args:      args,
			Resources: resources,
			Ports: []corev1.ContainerPort{
				{ContainerPort: port, Protocol: corev1.ProtocolTCP},
			},
			VolumeMounts: []corev1.VolumeMount{
				{Name: "model-storage", MountPath: modelMountPath, ReadOnly: true},
			},
			ReadinessProbe: &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{
					HTTPGet: &corev1.HTTPGetAction{
						Path: "/health",
						Port: intstr.FromInt32(port),
					},
				},
				InitialDelaySeconds: 10,
				PeriodSeconds:       5,
				TimeoutSeconds:      3,
				FailureThreshold:    60, // Allow up to 5 minutes for large model loading
			},
			LivenessProbe: &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{
					HTTPGet: &corev1.HTTPGetAction{
						Path: "/health",
						Port: intstr.FromInt32(port),
					},
				},
				InitialDelaySeconds: 30,
				PeriodSeconds:       15,
				TimeoutSeconds:      5,
				FailureThreshold:    3,
			},
		}

		deploy.Spec.Template.Spec = corev1.PodSpec{
			Containers: []corev1.Container{container},
			Volumes: []corev1.Volume{
				{
					Name: "model-storage",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: r.pvcName(model),
							ReadOnly:  true,
						},
					},
				},
			},
			NodeSelector: model.Spec.NodeSelector,
			Tolerations:  model.Spec.Tolerations,
		}

		return nil
	})
	if err != nil {
		return err
	}
	log.Info("Deployment reconciled", "result", result)
	return nil
}

func (r *ModelReconciler) ensureService(ctx context.Context, model *sympoziumv1alpha1.Model, log logr.Logger) error {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      r.serviceName(model),
			Namespace: model.Namespace,
		},
	}

	port := r.inferencePort(model)

	result, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		if err := controllerutil.SetControllerReference(model, svc, r.Scheme); err != nil {
			return err
		}
		svc.Spec.Selector = map[string]string{
			"app.kubernetes.io/name":     "model",
			"app.kubernetes.io/instance": model.Name,
		}
		svc.Spec.Ports = []corev1.ServicePort{
			{
				Name:       "http",
				Port:       port,
				TargetPort: intstr.FromInt32(port),
				Protocol:   corev1.ProtocolTCP,
			},
		}
		return nil
	})
	if err != nil {
		return err
	}
	log.Info("Service reconciled", "result", result)
	return nil
}

// sanitizeName converts a model name to a valid K8s resource name component
func sanitizeName(name string) string {
	return strings.ReplaceAll(strings.ToLower(name), ".", "-")
}

func (r *ModelReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&sympoziumv1alpha1.Model{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Owns(&batchv1.Job{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Complete(r)
}
