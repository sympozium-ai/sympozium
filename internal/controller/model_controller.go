package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
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
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

const (
	modelFinalizer   = "sympozium.ai/model-finalizer"
	modelMountPath   = "/models"
	downloadJobImage = "curlimages/curl:8.7.1"

	llmfitProbeImage      = "ghcr.io/sympozium-ai/sympozium/skill-llmfit:latest"
	llmfitProbeTimeout    = 3 * time.Minute
	llmfitProbeLabelKey   = "sympozium.ai/llmfit-probe"
	placementStartedAnnot = "sympozium.ai/placement-started"
)

// ModelReconciler reconciles Model objects.
type ModelReconciler struct {
	client.Client
	Scheme       *runtime.Scheme
	Log          logr.Logger
	Clientset    kubernetes.Interface
	FitnessCache *FitnessCache // optional: set when llmfit DaemonSet is enabled
}

// +kubebuilder:rbac:groups=sympozium.ai,resources=models,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=sympozium.ai,resources=models/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=pods;pods/log,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch

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
		if model.Spec.Placement.Mode == sympoziumv1alpha1.PlacementAuto && len(model.Spec.NodeSelector) == 0 {
			model.Status.Phase = sympoziumv1alpha1.ModelPhasePlacing
			model.Status.Message = "Auto-selecting best node via llmfit"
		} else {
			model.Status.Phase = sympoziumv1alpha1.ModelPhasePending
			model.Status.Message = "Model created, provisioning storage"
		}
		if err := r.Status().Update(ctx, &model); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	switch model.Status.Phase {
	case sympoziumv1alpha1.ModelPhasePlacing:
		return r.reconcilePlacing(ctx, &model, log)
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

// reconcilePlacing uses llmfit probes to auto-select the best node.
// When a FitnessCache is available (llmfit DaemonSet enabled), placement
// uses cached fitness data for instant results. Falls back to spawning
// probe pods when the cache is empty or stale.
func (r *ModelReconciler) reconcilePlacing(ctx context.Context, model *sympoziumv1alpha1.Model, log logr.Logger) (ctrl.Result, error) {
	// Fast path: use FitnessCache if available and populated.
	if r.FitnessCache != nil && r.FitnessCache.NodeCount() > 0 {
		modelQuery := r.backendFor(model).ModelQuery(model)
		bestNode, bestScore, bestMessage := r.FitnessCache.BestNodeForModel(modelQuery, "good")
		if bestNode != "" {
			log.Info("Placement via fitness cache", "node", bestNode, "score", bestScore, "model", modelQuery)
			model.Spec.NodeSelector = map[string]string{"kubernetes.io/hostname": bestNode}
			if err := r.Update(ctx, model); err != nil {
				return ctrl.Result{}, err
			}
			model.Status.PlacedNode = bestNode
			model.Status.PlacementScore = int(bestScore)
			model.Status.PlacementMessage = bestMessage
			return r.transitionToPending(ctx, model, log)
		}
		log.Info("Fitness cache has no suitable node, falling back to probe pods", "model", modelQuery)
	}

	// Slow path: spawn probe pods per node.
	probeLabel := client.MatchingLabels{llmfitProbeLabelKey: model.Name}

	// Check for timeout.
	if started, ok := model.Annotations[placementStartedAnnot]; ok {
		t, err := time.Parse(time.RFC3339, started)
		if err == nil && time.Since(t) > llmfitProbeTimeout {
			log.Info("Placement probes timed out, falling back to default scheduler")
			r.cleanupProbePods(ctx, model, log)
			model.Status.PlacementMessage = "Auto-placement timed out, using default scheduler"
			return r.transitionToPending(ctx, model, log)
		}
	}

	// List existing probe pods.
	var probes corev1.PodList
	if err := r.List(ctx, &probes, client.InNamespace(model.Namespace), probeLabel); err != nil {
		return ctrl.Result{}, err
	}

	// If no probes exist yet, create them.
	if len(probes.Items) == 0 {
		// Record start time.
		if model.Annotations == nil {
			model.Annotations = map[string]string{}
		}
		model.Annotations[placementStartedAnnot] = time.Now().UTC().Format(time.RFC3339)
		if err := r.Update(ctx, model); err != nil {
			return ctrl.Result{}, err
		}

		// List ready nodes.
		var nodes corev1.NodeList
		if err := r.List(ctx, &nodes); err != nil {
			return ctrl.Result{}, fmt.Errorf("listing nodes: %w", err)
		}

		var readyNodes []corev1.Node
		for _, n := range nodes.Items {
			for _, c := range n.Status.Conditions {
				if c.Type == corev1.NodeReady && c.Status == corev1.ConditionTrue {
					readyNodes = append(readyNodes, n)
					break
				}
			}
		}

		if len(readyNodes) == 0 {
			log.Info("No ready nodes found, skipping placement")
			model.Status.PlacementMessage = "No ready nodes found, using default scheduler"
			return r.transitionToPending(ctx, model, log)
		}

		modelQuery := r.backendFor(model).ModelQuery(model)
		log.Info("Creating llmfit probe pods", "nodes", len(readyNodes), "modelQuery", modelQuery)

		for _, node := range readyNodes {
			podName := r.probePodName(model, node.Name)
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      podName,
					Namespace: model.Namespace,
					Labels: map[string]string{
						llmfitProbeLabelKey:            model.Name,
						"app.kubernetes.io/name":       "llmfit-probe",
						"app.kubernetes.io/managed-by": "sympozium",
					},
				},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					NodeName:      node.Name,
					Tolerations:   []corev1.Toleration{{Operator: corev1.TolerationOpExists}},
					Containers: []corev1.Container{
						{
							Name:            "probe",
							Image:           llmfitProbeImage,
							ImagePullPolicy: corev1.PullIfNotPresent,
							Command:         []string{"/bin/sh", "-lc"},
							Args:            []string{"/usr/local/bin/llmfit-probe-json.sh"},
							Env: []corev1.EnvVar{
								{Name: "MODEL_QUERY", Value: modelQuery},
								{Name: "NODE_NAME", Value: node.Name},
								{Name: "LLMFIT_MIN_FIT", Value: "good"},
								{Name: "LLMFIT_LIMIT", Value: "5"},
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("100m"),
									corev1.ResourceMemory: resource.MustParse("128Mi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("200m"),
									corev1.ResourceMemory: resource.MustParse("256Mi"),
								},
							},
						},
					},
				},
			}

			if err := controllerutil.SetControllerReference(model, pod, r.Scheme); err != nil {
				return ctrl.Result{}, err
			}
			if err := r.Create(ctx, pod); err != nil && !errors.IsAlreadyExists(err) {
				return ctrl.Result{}, fmt.Errorf("creating probe pod for node %s: %w", node.Name, err)
			}
		}

		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// Check if all probes have completed.
	allDone := true
	for i := range probes.Items {
		phase := probes.Items[i].Status.Phase
		if phase != corev1.PodSucceeded && phase != corev1.PodFailed {
			allDone = false
			break
		}
	}

	if !allDone {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// Collect results.
	log.Info("All placement probes completed, collecting results")
	type probeResult struct {
		Node         string `json:"node"`
		MatchedCount int    `json:"matched_count"`
		Top          []struct {
			Name   string  `json:"name"`
			Score  float64 `json:"score"`
			FitLev string  `json:"fit_level"`
		} `json:"top"`
		Error string `json:"error"`
	}

	var bestNode string
	var bestScore float64
	bestScoreInt := 0

	for i := range probes.Items {
		pod := &probes.Items[i]
		if pod.Status.Phase != corev1.PodSucceeded {
			continue
		}

		logs, err := r.readPodLogs(ctx, pod)
		if err != nil {
			log.Info("Failed to read probe logs", "pod", pod.Name, "error", err)
			continue
		}

		var result probeResult
		if err := json.Unmarshal([]byte(logs), &result); err != nil {
			log.Info("Failed to parse probe result", "pod", pod.Name, "error", err)
			continue
		}

		if result.Error != "" || len(result.Top) == 0 {
			continue
		}

		topScore := result.Top[0].Score
		if topScore > bestScore {
			bestScore = topScore
			bestScoreInt = int(topScore)
			bestNode = result.Node
		}
	}

	// Clean up probe pods.
	r.cleanupProbePods(ctx, model, log)

	if bestNode != "" {
		log.Info("Auto-placement selected node", "node", bestNode, "score", bestScore)
		model.Spec.NodeSelector = map[string]string{"kubernetes.io/hostname": bestNode}
		if err := r.Update(ctx, model); err != nil {
			return ctrl.Result{}, err
		}
		model.Status.PlacedNode = bestNode
		model.Status.PlacementScore = bestScoreInt
		model.Status.PlacementMessage = fmt.Sprintf("Selected node %s (score: %d)", bestNode, bestScoreInt)
	} else {
		log.Info("No suitable node found by probes, using default scheduler")
		model.Status.PlacementMessage = "No node scored above threshold, using default scheduler"
	}

	return r.transitionToPending(ctx, model, log)
}

// transitionToPending moves the model from Placing to Pending phase.
func (r *ModelReconciler) transitionToPending(ctx context.Context, model *sympoziumv1alpha1.Model, log logr.Logger) (ctrl.Result, error) {
	// Save placement status fields before metadata update, since r.Update()
	// returns the server's view of the object which may overwrite local changes.
	placedNode := model.Status.PlacedNode
	placementScore := model.Status.PlacementScore
	placementMessage := model.Status.PlacementMessage

	// Clean up placement annotation.
	if model.Annotations != nil {
		delete(model.Annotations, placementStartedAnnot)
		if err := r.Update(ctx, model); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Restore placement fields and set pending status.
	model.Status.PlacedNode = placedNode
	model.Status.PlacementScore = placementScore
	model.Status.PlacementMessage = placementMessage
	model.Status.Phase = sympoziumv1alpha1.ModelPhasePending
	model.Status.Message = "Model created, provisioning storage"
	if err := r.Status().Update(ctx, model); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{Requeue: true}, nil
}

// readPodLogs reads the full log output from a completed pod.
func (r *ModelReconciler) readPodLogs(ctx context.Context, pod *corev1.Pod) (string, error) {
	req := r.Clientset.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{})
	stream, err := req.Stream(ctx)
	if err != nil {
		return "", err
	}
	defer stream.Close()

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, stream); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// cleanupProbePods deletes all llmfit probe pods for the given model.
func (r *ModelReconciler) cleanupProbePods(ctx context.Context, model *sympoziumv1alpha1.Model, log logr.Logger) {
	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(model.Namespace), client.MatchingLabels{llmfitProbeLabelKey: model.Name}); err != nil {
		log.Info("Warning: failed to list probe pods for cleanup", "error", err)
		return
	}
	for i := range pods.Items {
		if err := r.Delete(ctx, &pods.Items[i]); err != nil && !errors.IsNotFound(err) {
			log.Info("Warning: failed to delete probe pod", "pod", pods.Items[i].Name, "error", err)
		}
	}
}

// probePodName returns a deterministic pod name for a probe on a given node.
func (r *ModelReconciler) probePodName(model *sympoziumv1alpha1.Model, nodeName string) string {
	sanitized := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		if r >= 'A' && r <= 'Z' {
			return r + 32 // toLower
		}
		return '-'
	}, nodeName)
	if len(sanitized) > 30 {
		sanitized = sanitized[:30]
	}
	modelPart := model.Name
	if len(modelPart) > 20 {
		modelPart = modelPart[:20]
	}
	return fmt.Sprintf("llmfit-probe-%s-%s", modelPart, sanitized)
}

// modelQueryFromURL extracts a model search query from a GGUF download URL.
// For HuggingFace URLs like /Qwen/Qwen3-8B-GGUF/resolve/main/... it extracts "Qwen/Qwen3-8B".
// Falls back to the filename without extension, or "*" as last resort.
func modelQueryFromURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "*"
	}

	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")

	// HuggingFace pattern: /{org}/{model-name}[-GGUF]/resolve/...
	if len(segments) >= 2 && (strings.Contains(parsed.Host, "huggingface") || strings.Contains(parsed.Host, "hf.co")) {
		modelName := segments[1]
		// Strip -GGUF suffix
		modelName = strings.TrimSuffix(modelName, "-GGUF")
		modelName = strings.TrimSuffix(modelName, "-gguf")
		return segments[0] + "/" + modelName
	}

	// Fallback: last path segment without extension
	if len(segments) > 0 {
		filename := segments[len(segments)-1]
		filename = strings.TrimSuffix(filename, filepath.Ext(filename))
		// Strip common quant suffixes
		for _, suffix := range []string{"-Q4_K_M", "-Q4_K_S", "-Q5_K_M", "-Q5_K_S", "-Q8_0", "-Q6_K", "-Q3_K_M", "-Q2_K", "-f16", "-f32"} {
			filename = strings.TrimSuffix(filename, suffix)
			filename = strings.TrimSuffix(filename, strings.ToLower(suffix))
		}
		if filename != "" {
			return filename
		}
	}

	return "*"
}

// reconcilePending creates the PVC and starts the download Job.
func (r *ModelReconciler) reconcilePending(ctx context.Context, model *sympoziumv1alpha1.Model, log logr.Logger) (ctrl.Result, error) {
	backend := r.backendFor(model)

	// Ensure PVC (all backends use a PVC — for model weights or HF cache).
	if err := r.ensurePVC(ctx, model, log); err != nil {
		return ctrl.Result{}, err
	}

	if backend.NeedsDownload() {
		// llama-cpp: download GGUF file, then transition to Downloading phase.
		if err := r.ensureDownloadJob(ctx, model, log); err != nil {
			return ctrl.Result{}, err
		}

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

	// vLLM/TGI: no download needed — go straight to Loading.
	// Mark download as N/A and start the inference server immediately.
	meta.SetStatusCondition(&model.Status.Conditions, metav1.Condition{
		Type:               "Downloaded",
		Status:             metav1.ConditionTrue,
		Reason:             "NotRequired",
		Message:            "Server type pulls model at startup",
		ObservedGeneration: model.Generation,
	})

	if err := r.ensureDeployment(ctx, model, log); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.ensureService(ctx, model, log); err != nil {
		return ctrl.Result{}, err
	}

	model.Status.Phase = sympoziumv1alpha1.ModelPhaseLoading
	model.Status.Message = "Starting inference server (model will be pulled from HuggingFace)"
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

func (r *ModelReconciler) backendFor(model *sympoziumv1alpha1.Model) inferenceBackend {
	return newInferenceBackend(model.Spec.Inference.ServerType)
}

func (r *ModelReconciler) inferencePort(model *sympoziumv1alpha1.Model) int32 {
	if model.Spec.Inference.Port > 0 {
		return model.Spec.Inference.Port
	}
	return r.backendFor(model).DefaultPort()
}

func (r *ModelReconciler) inferenceImage(model *sympoziumv1alpha1.Model) string {
	if model.Spec.Inference.Image != "" {
		return model.Spec.Inference.Image
	}
	return r.backendFor(model).DefaultImage()
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

	// Download with atomic rename: download to .tmp then move.
	// When a SHA256 checksum is provided, verify after download.
	checksumScript := ""
	if model.Spec.Source.SHA256 != "" {
		checksumScript = fmt.Sprintf(`
echo "Verifying SHA-256 checksum..."
ACTUAL=$(sha256sum "%s" | cut -d ' ' -f1)
EXPECTED="%s"
if [ "$ACTUAL" != "$EXPECTED" ]; then
  echo "Checksum mismatch: expected $EXPECTED, got $ACTUAL"
  rm -f "%s"
  exit 1
fi
echo "Checksum verified"`,
			tempPath, model.Spec.Source.SHA256, tempPath,
		)
	}

	downloadScript := fmt.Sprintf(`set -e
if [ -f "%s" ]; then
  echo "Model file already exists, skipping download"
  exit 0
fi
echo "Downloading model from %s"
curl -L --fail --retry 3 --retry-delay 5 -o "%s" "%s"%s
mv "%s" "%s"
echo "Download complete"`,
		modelPath,
		model.Spec.Source.URL,
		tempPath, model.Spec.Source.URL,
		checksumScript,
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
					RestartPolicy:   corev1.RestartPolicyNever,
					SecurityContext: modelPodSecurityContext(),
					Containers: []corev1.Container{
						{
							Name:            "download",
							Image:           downloadJobImage,
							Command:         []string{"sh", "-c", downloadScript},
							SecurityContext: modelContainerSecurityContext(),
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
	backend := r.backendFor(model)

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      r.deploymentName(model),
			Namespace: model.Namespace,
		},
	}

	port := r.inferencePort(model)
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

		// Delegate args, env, volumes to the backend.
		args := backend.BuildArgs(model, port)
		env := backend.BuildEnv(model)
		volumeMounts := backend.VolumeMounts(model)
		volumes := backend.Volumes(model, r.pvcName(model))
		healthPath := backend.HealthPath()

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
			Name:            backend.ContainerName(),
			Image:           image,
			ImagePullPolicy: ResolveImagePullPolicy(model.Spec.Inference.ImagePullPolicy),
			Args:            args,
			Env:             env,
			Resources:       resources,
			Ports: []corev1.ContainerPort{
				{ContainerPort: port, Protocol: corev1.ProtocolTCP},
			},
			VolumeMounts: volumeMounts,
			ReadinessProbe: &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{
					HTTPGet: &corev1.HTTPGetAction{
						Path: healthPath,
						Port: intstr.FromInt32(port),
					},
				},
				InitialDelaySeconds: 10,
				PeriodSeconds:       5,
				TimeoutSeconds:      3,
				FailureThreshold:    backend.ReadinessFailureThreshold(),
			},
			LivenessProbe: &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{
					HTTPGet: &corev1.HTTPGetAction{
						Path: healthPath,
						Port: intstr.FromInt32(port),
					},
				},
				InitialDelaySeconds: 30,
				PeriodSeconds:       15,
				TimeoutSeconds:      5,
				FailureThreshold:    3,
			},
		}

		container.SecurityContext = modelContainerSecurityContext()

		deploy.Spec.Template.Spec = corev1.PodSpec{
			SecurityContext: modelPodSecurityContext(),
			Containers:      []corev1.Container{container},
			Volumes:         volumes,
			NodeSelector:    model.Spec.NodeSelector,
			Tolerations:     model.Spec.Tolerations,
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

// modelPodSecurityContext returns a restricted PodSecurityContext for model pods.
func modelPodSecurityContext() *corev1.PodSecurityContext {
	runAsNonRoot := true
	runAsUser := int64(1000)
	fsGroup := int64(1000)
	return &corev1.PodSecurityContext{
		RunAsNonRoot: &runAsNonRoot,
		RunAsUser:    &runAsUser,
		FSGroup:      &fsGroup,
		SeccompProfile: &corev1.SeccompProfile{
			Type: corev1.SeccompProfileTypeRuntimeDefault,
		},
	}
}

// modelContainerSecurityContext returns a restricted SecurityContext for model containers.
func modelContainerSecurityContext() *corev1.SecurityContext {
	noPrivEsc := false
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: &noPrivEsc,
		Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{"ALL"},
		},
	}
}

func (r *ModelReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&sympoziumv1alpha1.Model{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Owns(&batchv1.Job{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.Pod{}).
		Complete(r)
}
