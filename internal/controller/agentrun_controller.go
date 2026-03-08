package controller

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	sympoziumv1alpha1 "github.com/alexsjones/sympozium/api/v1alpha1"
	"github.com/alexsjones/sympozium/internal/orchestrator"
)

var controllerTracer = otel.Tracer("sympozium.ai/controller")
var controllerMeter = otel.Meter("sympozium.ai/controller")

// Controller metric instruments.
var (
	agentRunsTotal, _    = controllerMeter.Int64Counter("sympozium.agent.runs", metric.WithUnit("{run}"), metric.WithDescription("Agent runs completed"))
	agentDurationHist, _ = controllerMeter.Float64Histogram("sympozium.agent.duration_ms", metric.WithUnit("ms"), metric.WithDescription("Agent run duration"))
	controllerErrors, _  = controllerMeter.Int64Counter("sympozium.errors", metric.WithUnit("{error}"), metric.WithDescription("Errors encountered"))

	// Web endpoint server-mode metrics.
	webEndpointServing, _         = controllerMeter.Int64UpDownCounter("sympozium.web_endpoint.serving", metric.WithUnit("{deployment}"), metric.WithDescription("Active server-mode Deployments"))
	webEndpointGatewayNotReady, _ = controllerMeter.Int64Counter("sympozium.web_endpoint.gateway_not_ready", metric.WithUnit("{check}"), metric.WithDescription("Gateway check failures"))
	webEndpointRouteCreated, _    = controllerMeter.Int64Counter("sympozium.web_endpoint.route_created", metric.WithUnit("{route}"), metric.WithDescription("HTTPRoutes created"))
)

const agentRunFinalizer = "sympozium.ai/agentrun-finalizer"
const systemNamespace = "sympozium-system"

// DefaultRunHistoryLimit is how many completed AgentRuns to keep per instance
// before the oldest are pruned.
const DefaultRunHistoryLimit = 50

// AgentRunReconciler reconciles AgentRun objects.
// It watches AgentRun CRDs and reconciles them into Kubernetes Jobs/Pods.
type AgentRunReconciler struct {
	client.Client
	Scheme          *runtime.Scheme
	Log             logr.Logger
	PodBuilder      *orchestrator.PodBuilder
	Clientset       kubernetes.Interface
	ImageTag        string // release tag for Sympozium images (e.g. "v0.0.25")
	RunHistoryLimit int    // max completed runs to keep per instance (0 = use default)
}

const imageRegistry = "ghcr.io/alexsjones/sympozium"

// imageRef returns a fully qualified image reference using the reconciler's tag.
func (r *AgentRunReconciler) imageRef(name string) string {
	tag := r.ImageTag
	if tag == "" {
		tag = "latest"
	}
	return fmt.Sprintf("%s/%s:%s", imageRegistry, name, tag)
}

// resolveOTelEndpoint returns the OTLP endpoint for agent pods.
// Priority: instance CRD → controller's own env → empty (noop).
func resolveOTelEndpoint(instance *sympoziumv1alpha1.SympoziumInstance) string {
	if instance != nil && instance.Spec.Observability != nil {
		if !instance.Spec.Observability.Enabled {
			return ""
		}
		if instance.Spec.Observability.OTLPEndpoint != "" {
			return instance.Spec.Observability.OTLPEndpoint
		}
	}
	return os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
}

// formatTraceparent returns a W3C traceparent string from a span context,
// or empty string if the span context is invalid.
// extractTraceparent parses a W3C traceparent string and returns a context
// with the remote span context set, so new spans become children of it.
func extractTraceparent(ctx context.Context, tp string) context.Context {
	prop := propagation.TraceContext{}
	carrier := propagation.MapCarrier{"traceparent": tp}
	return prop.Extract(ctx, carrier)
}

func formatTraceparent(sc trace.SpanContext) string {
	if !sc.IsValid() {
		return ""
	}
	flags := "00"
	if sc.IsSampled() {
		flags = "01"
	}
	return fmt.Sprintf("00-%s-%s-%s", sc.TraceID(), sc.SpanID(), flags)
}

// +kubebuilder:rbac:groups=sympozium.ai,resources=agentruns,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=sympozium.ai,resources=agentruns/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=sympozium.ai,resources=agentruns/finalizers,verbs=update
// +kubebuilder:rbac:groups=batch,resources=jobs;cronjobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=pods;pods/log;pods/exec;pods/portforward,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=configmaps;secrets;services;endpoints;persistentvolumeclaims;serviceaccounts;replicationcontrollers;resourcequotas;limitranges,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=nodes;namespaces;persistentvolumes,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments;statefulsets;replicasets;daemonsets;controllerrevisions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies;ingresses;ingressclasses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=autoscaling,resources=horizontalpodautoscalers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=policy,resources=poddisruptionbudgets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=storage.k8s.io,resources=storageclasses;volumeattachments,verbs=get;list;watch
// +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings;clusterroles;clusterrolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=sympozium.ai,resources=sympoziumconfigs,verbs=get;list;watch

// Reconcile handles AgentRun create/update/delete events.
func (r *AgentRunReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	reconcileStart := time.Now()
	log := r.Log.WithValues("agentrun", req.NamespacedName)

	// Fetch the AgentRun first so we can extract traceparent.
	agentRun := &sympoziumv1alpha1.AgentRun{}
	if err := r.Get(ctx, req.NamespacedName, agentRun); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// If the AgentRun carries a traceparent annotation (set by channel router),
	// use it as parent context so this span joins the original trace.
	if tp := agentRun.Annotations["otel.dev/traceparent"]; tp != "" {
		ctx = extractTraceparent(ctx, tp)
	}

	ctx, span := controllerTracer.Start(ctx, "agentrun.reconcile",
		trace.WithAttributes(
			attribute.String("agentrun.name", req.Name),
			attribute.String("namespace", req.Namespace),
			attribute.String("agentrun.phase", string(agentRun.Status.Phase)),
			attribute.String("instance.name", agentRun.Spec.InstanceRef),
		),
	)
	defer span.End()

	// Handle deletion
	if !agentRun.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, log, agentRun)
	}

	// Add finalizer only for non-terminal runs. Completed/failed runs have
	// their finalizer removed in reconcileCompleted; we must not re-add it or
	// we create an infinite remove→add→remove loop.
	// Serving-mode runs are long-lived and also need a finalizer.
	isTerminal := agentRun.Status.Phase == sympoziumv1alpha1.AgentRunPhaseSucceeded ||
		agentRun.Status.Phase == sympoziumv1alpha1.AgentRunPhaseFailed
	if !isTerminal && !controllerutil.ContainsFinalizer(agentRun, agentRunFinalizer) {
		controllerutil.AddFinalizer(agentRun, agentRunFinalizer)
		if err := r.Update(ctx, agentRun); err != nil {
			if errors.IsConflict(err) {
				return ctrl.Result{Requeue: true}, nil
			}
			span.RecordError(err)
			span.SetStatus(codes.Error, "add finalizer failed")
			return ctrl.Result{}, err
		}
	}

	// Reconcile based on current phase
	var result ctrl.Result
	var err error
	switch agentRun.Status.Phase {
	case "", sympoziumv1alpha1.AgentRunPhasePending:
		result, err = r.reconcilePending(ctx, log, agentRun)
	case sympoziumv1alpha1.AgentRunPhaseRunning:
		result, err = r.reconcileRunning(ctx, log, agentRun)
	case sympoziumv1alpha1.AgentRunPhaseServing:
		result, err = r.reconcileServing(ctx, log, agentRun)
	case sympoziumv1alpha1.AgentRunPhaseSucceeded, sympoziumv1alpha1.AgentRunPhaseFailed:
		result, err = r.reconcileCompleted(ctx, log, agentRun)
	default:
		log.Info("Unknown phase", "phase", agentRun.Status.Phase)
		return ctrl.Result{}, nil
	}

	span.SetAttributes(attribute.Float64("reconcile.duration_ms", float64(time.Since(reconcileStart).Milliseconds())))
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "reconcile failed")
	}
	return result, err
}

// reconcilePending handles an AgentRun that needs a Job created.
func (r *AgentRunReconciler) reconcilePending(ctx context.Context, log logr.Logger, agentRun *sympoziumv1alpha1.AgentRun) (ctrl.Result, error) {
	ctx, span := controllerTracer.Start(ctx, "agentrun.create_job",
		trace.WithAttributes(
			attribute.String("agentrun.name", agentRun.Name),
			attribute.String("instance.name", agentRun.Spec.InstanceRef),
		),
	)
	defer span.End()

	log.Info("Reconciling pending AgentRun")

	// Validate against policy
	if err := r.validatePolicy(ctx, agentRun); err != nil {
		return ctrl.Result{}, r.failRun(ctx, agentRun, fmt.Sprintf("policy validation failed: %v", err))
	}

	// Ensure the sympozium-agent ServiceAccount exists in the target namespace.
	if err := r.ensureAgentServiceAccount(ctx, agentRun.Namespace); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensuring agent service account: %w", err)
	}

	// Create the input ConfigMap with the task
	if err := r.createInputConfigMap(ctx, agentRun); err != nil {
		return ctrl.Result{}, fmt.Errorf("creating input ConfigMap: %w", err)
	}

	// Look up the SympoziumInstance to check for memory configuration.
	instance := &sympoziumv1alpha1.SympoziumInstance{}
	memoryEnabled := false
	var observability *sympoziumv1alpha1.ObservabilitySpec
	if err := r.Get(ctx, client.ObjectKey{
		Namespace: agentRun.Namespace,
		Name:      agentRun.Spec.InstanceRef,
	}, instance); err == nil {
		if instance.Spec.Memory != nil && instance.Spec.Memory.Enabled {
			memoryEnabled = true
		}
		if instance.Spec.Observability != nil && instance.Spec.Observability.Enabled {
			obsCopy := *instance.Spec.Observability
			if len(instance.Spec.Observability.ResourceAttributes) > 0 {
				obsCopy.ResourceAttributes = make(map[string]string, len(instance.Spec.Observability.ResourceAttributes))
				for k, v := range instance.Spec.Observability.ResourceAttributes {
					obsCopy.ResourceAttributes[k] = v
				}
			}
			observability = &obsCopy
		}
		// If the AgentRun has no skills, inherit from the SympoziumInstance.
		// This is a safety net — tuiCreateRun and the schedule controller
		// should already copy skills, but older runs or manual CRs may not.
		// Skip inheritance for web-proxy child runs — they intentionally omit
		// server-mode skills (like web-endpoint) to run as one-shot Jobs.
		if len(agentRun.Spec.Skills) == 0 && len(instance.Spec.Skills) > 0 {
			if agentRun.Labels["sympozium.ai/source"] != "web-proxy" {
				agentRun.Spec.Skills = instance.Spec.Skills
			}
		}
	}

	// Write traceparent annotation so buildContainers can inject TRACEPARENT env var.
	traceparent := formatTraceparent(span.SpanContext())
	if traceparent != "" {
		if agentRun.Annotations == nil {
			agentRun.Annotations = map[string]string{}
		}
		agentRun.Annotations["otel.dev/traceparent"] = traceparent
	}

	// Resolve skill sidecars from SkillPack CRDs.
	sidecars := r.resolveSkillSidecars(ctx, log, agentRun)

	// Detect server mode: if any sidecar has RequiresServer, switch to server mode.
	effectiveMode := agentRun.Spec.Mode
	if effectiveMode == "" {
		effectiveMode = "task"
	}
	for _, sc := range sidecars {
		if sc.sidecar.RequiresServer {
			effectiveMode = "server"
			break
		}
	}
	if effectiveMode == "server" {
		return r.reconcilePendingServer(ctx, log, agentRun, sidecars)
	}

	// Mirror skill ConfigMaps from sympozium-system into the agent namespace
	// so projected volumes can reference them (ConfigMaps are namespace-local).
	if err := r.mirrorSkillConfigMaps(ctx, log, agentRun); err != nil {
		log.Error(err, "Failed to mirror skill ConfigMaps, skills may be missing")
	}

	// Create RBAC resources for skill sidecars that need them.
	if err := r.ensureSkillRBAC(ctx, log, agentRun, sidecars); err != nil {
		log.Error(err, "Failed to create skill RBAC, continuing without")
	}

	// Build and create the Job
	job := r.buildJob(agentRun, memoryEnabled, observability, sidecars)
	if err := controllerutil.SetControllerReference(agentRun, job, r.Scheme); err != nil {
		return ctrl.Result{}, fmt.Errorf("setting owner reference: %w", err)
	}

	if err := r.Create(ctx, job); err != nil {
		if errors.IsAlreadyExists(err) {
			log.Info("Job already exists")
		} else {
			return ctrl.Result{}, fmt.Errorf("creating Job: %w", err)
		}
	}

	// Update status to Running
	now := metav1.Now()
	agentRun.Status.Phase = sympoziumv1alpha1.AgentRunPhaseRunning
	agentRun.Status.JobName = job.Name
	agentRun.Status.StartedAt = &now
	// Set the trace ID so operators can look up the full distributed trace.
	if sc := span.SpanContext(); sc.HasTraceID() {
		agentRun.Status.TraceID = sc.TraceID().String()
	}
	if err := r.Status().Update(ctx, agentRun); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

// reconcileRunning checks on a running Job and updates status.
func (r *AgentRunReconciler) reconcileRunning(ctx context.Context, log logr.Logger, agentRun *sympoziumv1alpha1.AgentRun) (ctrl.Result, error) {
	ctx, span := controllerTracer.Start(ctx, "agentrun.extract_result",
		trace.WithAttributes(
			attribute.String("agentrun.name", agentRun.Name),
			attribute.String("instance.name", agentRun.Spec.InstanceRef),
		),
	)
	defer span.End()

	log.Info("Checking running AgentRun")

	// Find the Job
	job := &batchv1.Job{}
	jobName := client.ObjectKey{
		Namespace: agentRun.Namespace,
		Name:      agentRun.Status.JobName,
	}
	if err := r.Get(ctx, jobName, job); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, r.failRun(ctx, agentRun, "Job not found")
		}
		return ctrl.Result{}, err
	}

	// Update pod name from Job
	if agentRun.Status.PodName == "" {
		podList := &corev1.PodList{}
		if err := r.List(ctx, podList,
			client.InNamespace(agentRun.Namespace),
			client.MatchingLabels{"sympozium.ai/agent-run": agentRun.Name},
		); err == nil && len(podList.Items) > 0 {
			agentRun.Status.PodName = podList.Items[0].Name
			_ = r.Status().Update(ctx, agentRun)
		}
	}

	// Check Job completion
	if job.Status.Succeeded > 0 {
		// Extract the LLM response from pod logs before the pod is gone.
		result, _, usage := r.extractResultFromPod(ctx, log, agentRun)
		// Extract and persist memory updates if applicable.
		r.extractAndPersistMemory(ctx, log, agentRun)
		return r.succeedRun(ctx, agentRun, result, usage)
	}
	if job.Status.Failed > 0 {
		_, podErr, _ := r.extractResultFromPod(ctx, log, agentRun)
		if podErr != "" {
			return ctrl.Result{}, r.failRun(ctx, agentRun, podErr)
		}
		return ctrl.Result{}, r.failRun(ctx, agentRun, "Job failed")
	}

	// When the pod has skill sidecar containers (3+ containers), those
	// sidecars may keep the pod alive long after the agent has finished,
	// preventing the Job from reporting success. Detect agent completion
	// at the container level and clean up proactively.
	// For simple 2-container pods (agent + ipc-bridge), skip this check —
	// the ipc-bridge exits shortly after the agent and the Job completes
	// naturally.
	if agentRun.Status.PodName != "" {
		if done, exitCode, reason, hasSidecars := r.checkAgentContainer(ctx, log, agentRun); done && hasSidecars {
			if exitCode == 0 {
				log.Info("Agent container terminated successfully; cleaning up lingering sidecars")
				result, _, usage := r.extractResultFromPod(ctx, log, agentRun)
				r.extractAndPersistMemory(ctx, log, agentRun)
				// Delete the Job so Kubernetes kills remaining sidecar containers.
				_ = r.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationBackground))
				return r.succeedRun(ctx, agentRun, result, usage)
			}
			errMsg := fmt.Sprintf("agent container exited with code %d", exitCode)
			if reason != "" {
				errMsg = fmt.Sprintf("%s (%s)", errMsg, reason)
			}
			log.Info("Agent container terminated with error; cleaning up", "exitCode", exitCode, "reason", reason)
			// Try to extract the error from pod logs before cleaning up.
			if _, logErr, _ := r.extractResultFromPod(ctx, log, agentRun); logErr != "" {
				errMsg = logErr
			}
			_ = r.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationBackground))
			return ctrl.Result{}, r.failRun(ctx, agentRun, errMsg)
		}
	}

	// Check timeout (explicit spec timeout or hard default for scheduled runs).
	if agentRun.Status.StartedAt != nil {
		elapsed := time.Since(agentRun.Status.StartedAt.Time)
		timeout := 10 * time.Minute // default hard timeout
		if agentRun.Spec.Timeout != nil {
			timeout = agentRun.Spec.Timeout.Duration
		}
		if elapsed > timeout {
			log.Info("AgentRun timed out", "elapsed", elapsed, "timeout", timeout)
			// Delete the Job to kill the pod
			_ = r.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationForeground))
			return ctrl.Result{}, r.failRun(ctx, agentRun, "timeout")
		}
	}

	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

// checkAgentContainer inspects the pod's container statuses and returns:
//   - done: whether the "agent" container has terminated
//   - exitCode: the container exit code (only meaningful when done=true)
//   - reason: the termination reason string (e.g. "OOMKilled", "Error")
//   - hasSidecars: whether the pod has more than 2 containers (agent + ipc-bridge),
//     indicating skill sidecars that could keep the pod alive after the agent exits
func (r *AgentRunReconciler) checkAgentContainer(ctx context.Context, log logr.Logger, agentRun *sympoziumv1alpha1.AgentRun) (done bool, exitCode int32, reason string, hasSidecars bool) {
	pod := &corev1.Pod{}
	if err := r.Get(ctx, client.ObjectKey{
		Namespace: agentRun.Namespace,
		Name:      agentRun.Status.PodName,
	}, pod); err != nil {
		return false, 0, "", false
	}

	hasSidecars = len(pod.Spec.Containers) > 2

	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name != "agent" {
			continue
		}
		if cs.State.Terminated != nil {
			return true, cs.State.Terminated.ExitCode, cs.State.Terminated.Reason, hasSidecars
		}
		return false, 0, "", hasSidecars
	}
	return false, 0, "", hasSidecars
}

// reconcileCompleted handles cleanup of completed AgentRuns.
// Instead of deleting immediately, it keeps up to RunHistoryLimit completed
// runs per instance and prunes only the oldest ones beyond that threshold.
func (r *AgentRunReconciler) reconcileCompleted(ctx context.Context, log logr.Logger, agentRun *sympoziumv1alpha1.AgentRun) (ctrl.Result, error) {
	// Clean up cluster-scoped RBAC created for skill sidecars.
	r.cleanupSkillRBAC(ctx, log, agentRun)

	// Remove the finalizer so the CR can be deleted later if needed.
	// Use a Patch (not Update) to avoid overwriting status fields (like
	// tokenUsage) that were set by the status subresource update in succeedRun.
	if controllerutil.ContainsFinalizer(agentRun, agentRunFinalizer) {
		log.Info("Removing finalizer from completed AgentRun")
		patch := client.MergeFrom(agentRun.DeepCopy())
		controllerutil.RemoveFinalizer(agentRun, agentRunFinalizer)
		if err := r.Patch(ctx, agentRun, patch); err != nil {
			if errors.IsConflict(err) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, err
		}
	}

	// Prune old runs beyond the history limit for this instance.
	if err := r.pruneOldRuns(ctx, log, agentRun); err != nil {
		log.Error(err, "Failed to prune old AgentRuns")
		// Non-fatal: don't block reconciliation.
	}

	return ctrl.Result{}, nil
}

// runHistoryLimit returns the effective run history limit.
func (r *AgentRunReconciler) runHistoryLimit() int {
	if r.RunHistoryLimit > 0 {
		return r.RunHistoryLimit
	}
	return DefaultRunHistoryLimit
}

// pruneOldRuns lists all completed runs for the same instance and deletes the
// oldest ones when the total exceeds the configured RunHistoryLimit.
func (r *AgentRunReconciler) pruneOldRuns(ctx context.Context, log logr.Logger, agentRun *sympoziumv1alpha1.AgentRun) error {
	instanceRef := agentRun.Spec.InstanceRef
	if instanceRef == "" {
		return nil
	}

	var allRuns sympoziumv1alpha1.AgentRunList
	if err := r.List(ctx, &allRuns,
		client.InNamespace(agentRun.Namespace),
		client.MatchingLabels{"sympozium.ai/instance": instanceRef},
	); err != nil {
		return fmt.Errorf("listing runs for instance %s: %w", instanceRef, err)
	}

	// Collect only completed (Succeeded/Failed) runs.
	var completed []sympoziumv1alpha1.AgentRun
	for _, run := range allRuns.Items {
		if run.Status.Phase == "Succeeded" || run.Status.Phase == "Failed" {
			completed = append(completed, run)
		}
	}

	limit := r.runHistoryLimit()
	if len(completed) <= limit {
		return nil
	}

	// Sort oldest first by creation timestamp.
	sort.Slice(completed, func(i, j int) bool {
		return completed[i].CreationTimestamp.Before(&completed[j].CreationTimestamp)
	})

	pruneCount := len(completed) - limit
	log.Info("Pruning old AgentRuns", "instance", instanceRef, "total", len(completed), "limit", limit, "pruning", pruneCount)

	for i := 0; i < pruneCount; i++ {
		run := &completed[i]
		log.Info("Deleting old AgentRun", "name", run.Name, "created", run.CreationTimestamp.Time)
		if err := r.Delete(ctx, run); err != nil {
			if !errors.IsNotFound(err) {
				return fmt.Errorf("deleting run %s: %w", run.Name, err)
			}
		}
	}

	return nil
}

// reconcileDelete handles AgentRun deletion.
func (r *AgentRunReconciler) reconcileDelete(ctx context.Context, log logr.Logger, agentRun *sympoziumv1alpha1.AgentRun) (ctrl.Result, error) {
	log.Info("Reconciling AgentRun deletion")

	// Clean up cluster-scoped RBAC resources created for skill sidecars.
	r.cleanupSkillRBAC(ctx, log, agentRun)

	// Delete the Job if it exists
	if agentRun.Status.JobName != "" {
		job := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:      agentRun.Status.JobName,
				Namespace: agentRun.Namespace,
			},
		}
		if err := r.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationForeground)); err != nil && !errors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
	}

	// Clean up server-mode resources (Deployment, Service, HTTPRoute).
	if agentRun.Status.DeploymentName != "" {
		deploy := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      agentRun.Status.DeploymentName,
				Namespace: agentRun.Namespace,
			},
		}
		if err := r.Delete(ctx, deploy, client.PropagationPolicy(metav1.DeletePropagationForeground)); err != nil && !errors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		webEndpointServing.Add(ctx, -1, metric.WithAttributes(
			attribute.String("sympozium.instance", agentRun.Spec.InstanceRef),
		))
	}
	if agentRun.Status.ServiceName != "" {
		svc := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      agentRun.Status.ServiceName,
				Namespace: agentRun.Namespace,
			},
		}
		if err := r.Delete(ctx, svc); err != nil && !errors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
	}
	// Clean up HTTPRoute if it exists
	routeName := agentRun.Name + "-web"
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      routeName,
			Namespace: agentRun.Namespace,
		},
	}
	if err := r.Delete(ctx, route); err != nil && !errors.IsNotFound(err) {
		log.V(1).Info("Could not delete HTTPRoute", "name", routeName, "err", err)
	}

	patch := client.MergeFrom(agentRun.DeepCopy())
	controllerutil.RemoveFinalizer(agentRun, agentRunFinalizer)
	return ctrl.Result{}, r.Patch(ctx, agentRun, patch)
}

// reconcilePendingServer handles creating a Deployment + Service for server-mode AgentRuns.
// Server mode is triggered when a skill sidecar has RequiresServer=true (e.g. web-endpoint).
// The Deployment contains only the web-proxy sidecar; actual LLM work happens in
// per-request ephemeral AgentRun Jobs created by the web-proxy.
func (r *AgentRunReconciler) reconcilePendingServer(ctx context.Context, log logr.Logger, agentRun *sympoziumv1alpha1.AgentRun, sidecars []resolvedSidecar) (ctrl.Result, error) {
	log.Info("Reconciling pending server-mode AgentRun")

	// Ensure ServiceAccount exists.
	if err := r.ensureAgentServiceAccount(ctx, agentRun.Namespace); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensuring agent service account: %w", err)
	}

	// Create RBAC for sidecars.
	if err := r.ensureSkillRBAC(ctx, log, agentRun, sidecars); err != nil {
		log.Error(err, "Failed to create skill RBAC, continuing without")
	}

	// Find the server sidecar (first one with RequiresServer=true).
	var serverSidecar *resolvedSidecar
	for i := range sidecars {
		if sidecars[i].sidecar.RequiresServer {
			serverSidecar = &sidecars[i]
			break
		}
	}
	if serverSidecar == nil {
		return ctrl.Result{}, r.failRun(ctx, agentRun, "no sidecar with requiresServer=true found")
	}

	// Ensure API key Secret.
	secretName, err := r.ensureServerAPIKeySecret(ctx, agentRun, serverSidecar.params)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("ensuring server API key secret: %w", err)
	}

	// Build env vars for the web-proxy container.
	var envVars []corev1.EnvVar
	for _, e := range serverSidecar.sidecar.Env {
		envVars = append(envVars, corev1.EnvVar{Name: e.Name, Value: e.Value})
	}
	envVars = append(envVars,
		corev1.EnvVar{Name: "INSTANCE_NAME", Value: agentRun.Spec.InstanceRef},
		corev1.EnvVar{
			Name: "WEB_PROXY_API_KEY",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
					Key:                  "api-key",
				},
			},
		},
	)

	// Inject rate limit params from skill params.
	if rpm, ok := serverSidecar.params["rate_limit_rpm"]; ok {
		envVars = append(envVars, corev1.EnvVar{Name: "RATE_LIMIT_RPM", Value: rpm})
	}
	if burst, ok := serverSidecar.params["rate_limit_burst"]; ok {
		envVars = append(envVars, corev1.EnvVar{Name: "RATE_LIMIT_BURST", Value: burst})
	}

	// Inject per-instance SKILL_<KEY> env vars.
	for k, v := range serverSidecar.params {
		envKey := "SKILL_" + strings.ToUpper(k)
		envVars = append(envVars, corev1.EnvVar{Name: envKey, Value: v})
	}

	// Build container ports.
	var containerPorts []corev1.ContainerPort
	for _, p := range serverSidecar.sidecar.Ports {
		protocol := corev1.ProtocolTCP
		if p.Protocol != "" {
			protocol = corev1.Protocol(p.Protocol)
		}
		containerPorts = append(containerPorts, corev1.ContainerPort{
			Name:          p.Name,
			ContainerPort: p.ContainerPort,
			Protocol:      protocol,
		})
	}

	cpuReq := "100m"
	memReq := "128Mi"
	if serverSidecar.sidecar.Resources != nil {
		if serverSidecar.sidecar.Resources.CPU != "" {
			cpuReq = serverSidecar.sidecar.Resources.CPU
		}
		if serverSidecar.sidecar.Resources.Memory != "" {
			memReq = serverSidecar.sidecar.Resources.Memory
		}
	}

	labels := map[string]string{
		"sympozium.ai/agent-run": agentRun.Name,
		"sympozium.ai/instance":  agentRun.Spec.InstanceRef,
		"sympozium.ai/component": "agent-server",
	}

	deployName := agentRun.Name + "-server"
	replicas := int32(1)

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deployName,
			Namespace: agentRun.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					RestartPolicy:      corev1.RestartPolicyAlways,
					ServiceAccountName: "sympozium-agent",
					Containers: []corev1.Container{
						{
							Name:            "web-proxy",
							Image:           serverSidecar.sidecar.Image,
							ImagePullPolicy: corev1.PullIfNotPresent,
							Env:             envVars,
							Ports:           containerPorts,
							LivenessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: "/healthz",
										Port: intstr.FromInt(8080),
									},
								},
								InitialDelaySeconds: 5,
								PeriodSeconds:       10,
							},
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: "/healthz",
										Port: intstr.FromInt(8080),
									},
								},
								InitialDelaySeconds: 3,
								PeriodSeconds:       5,
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse(cpuReq),
									corev1.ResourceMemory: resource.MustParse(memReq),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("500m"),
									corev1.ResourceMemory: resource.MustParse("256Mi"),
								},
							},
							SecurityContext: &corev1.SecurityContext{
								RunAsNonRoot:             boolPtr(true),
								ReadOnlyRootFilesystem:   boolPtr(true),
								AllowPrivilegeEscalation: boolPtr(false),
							},
						},
					},
				},
			},
		},
	}

	if err := controllerutil.SetControllerReference(agentRun, deploy, r.Scheme); err != nil {
		return ctrl.Result{}, fmt.Errorf("setting owner reference on Deployment: %w", err)
	}

	if err := r.Create(ctx, deploy); err != nil {
		if !errors.IsAlreadyExists(err) {
			return ctrl.Result{}, fmt.Errorf("creating server Deployment: %w", err)
		}
		log.Info("Server Deployment already exists", "name", deployName)
	}

	// Build Service from sidecar's Ports.
	svcName := agentRun.Name + "-server"
	var svcPorts []corev1.ServicePort
	for _, p := range serverSidecar.sidecar.Ports {
		protocol := corev1.ProtocolTCP
		if p.Protocol != "" {
			protocol = corev1.Protocol(p.Protocol)
		}
		svcPorts = append(svcPorts, corev1.ServicePort{
			Name:       p.Name,
			Port:       p.ContainerPort,
			TargetPort: intstr.FromInt32(p.ContainerPort),
			Protocol:   protocol,
		})
	}

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      svcName,
			Namespace: agentRun.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Selector: labels,
			Ports:    svcPorts,
		},
	}

	if err := controllerutil.SetControllerReference(agentRun, svc, r.Scheme); err != nil {
		return ctrl.Result{}, fmt.Errorf("setting owner reference on Service: %w", err)
	}

	if err := r.Create(ctx, svc); err != nil {
		if !errors.IsAlreadyExists(err) {
			return ctrl.Result{}, fmt.Errorf("creating server Service: %w", err)
		}
		log.Info("Server Service already exists", "name", svcName)
	}

	// Conditionally create HTTPRoute.
	r.maybeCreateHTTPRoute(ctx, log, agentRun, serverSidecar.params, svcName)

	// Update status.
	now := metav1.Now()
	agentRun.Status.Phase = sympoziumv1alpha1.AgentRunPhaseServing
	agentRun.Status.DeploymentName = deployName
	agentRun.Status.ServiceName = svcName
	agentRun.Status.StartedAt = &now
	if err := r.Status().Update(ctx, agentRun); err != nil {
		return ctrl.Result{}, err
	}

	webEndpointServing.Add(ctx, 1, metric.WithAttributes(
		attribute.String("sympozium.instance", agentRun.Spec.InstanceRef),
	))
	log.Info("Server-mode AgentRun is now Serving", "deployment", deployName, "service", svcName)

	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

// ensureServerAPIKeySecret creates or returns an API key Secret for a server-mode AgentRun.
func (r *AgentRunReconciler) ensureServerAPIKeySecret(ctx context.Context, agentRun *sympoziumv1alpha1.AgentRun, params map[string]string) (string, error) {
	// Use user-provided secret if specified.
	if ref, ok := params["auth_secret_ref"]; ok && ref != "" {
		return ref, nil
	}

	secretName := agentRun.Spec.InstanceRef + "-web-proxy-key"
	var secret corev1.Secret
	err := r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: agentRun.Namespace}, &secret)
	if err == nil {
		return secretName, nil
	}
	if !errors.IsNotFound(err) {
		return "", err
	}

	// Generate random API key.
	keyBytes := make([]byte, 24)
	if _, err := rand.Read(keyBytes); err != nil {
		return "", fmt.Errorf("generate random key: %w", err)
	}
	apiKey := "sk-" + hex.EncodeToString(keyBytes)

	secret = corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: agentRun.Namespace,
			Labels: map[string]string{
				"sympozium.ai/component": "web-proxy",
				"sympozium.ai/instance":  agentRun.Spec.InstanceRef,
			},
		},
		StringData: map[string]string{
			"api-key": apiKey,
		},
	}

	if err := controllerutil.SetControllerReference(agentRun, &secret, r.Scheme); err != nil {
		return "", err
	}

	return secretName, r.Create(ctx, &secret)
}

// reconcileServing monitors a server-mode AgentRun (Deployment health, gateway readiness).
func (r *AgentRunReconciler) reconcileServing(ctx context.Context, log logr.Logger, agentRun *sympoziumv1alpha1.AgentRun) (ctrl.Result, error) {
	log.V(1).Info("Checking serving AgentRun", "deployment", agentRun.Status.DeploymentName)

	// Check Deployment health.
	if agentRun.Status.DeploymentName != "" {
		deploy := &appsv1.Deployment{}
		if err := r.Get(ctx, client.ObjectKey{
			Namespace: agentRun.Namespace,
			Name:      agentRun.Status.DeploymentName,
		}, deploy); err != nil {
			if errors.IsNotFound(err) {
				return ctrl.Result{}, r.failRun(ctx, agentRun, "server Deployment not found")
			}
			return ctrl.Result{}, err
		}

		if deploy.Status.ReadyReplicas == 0 && deploy.Status.Replicas > 0 {
			log.Info("Server Deployment not ready yet", "replicas", deploy.Status.Replicas, "ready", deploy.Status.ReadyReplicas)
		}
	}

	// Re-check gateway readiness and create/update HTTPRoute.
	if agentRun.Status.ServiceName != "" {
		// Resolve the server sidecar params for hostname.
		sidecars := r.resolveSkillSidecars(ctx, log, agentRun)
		for _, sc := range sidecars {
			if sc.sidecar.RequiresServer {
				r.maybeCreateHTTPRoute(ctx, log, agentRun, sc.params, agentRun.Status.ServiceName)
				break
			}
		}
	}

	// Server runs requeue periodically — no timeout enforcement.
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

// maybeCreateHTTPRoute creates an HTTPRoute for a server-mode AgentRun if the
// gateway is enabled and ready. If no gateway is configured, it skips silently.
func (r *AgentRunReconciler) maybeCreateHTTPRoute(ctx context.Context, log logr.Logger, agentRun *sympoziumv1alpha1.AgentRun, params map[string]string, svcName string) {
	// Look up SympoziumConfig.
	var config sympoziumv1alpha1.SympoziumConfig
	if err := r.Get(ctx, types.NamespacedName{Name: "default", Namespace: systemNamespace}, &config); err != nil {
		log.V(1).Info("SympoziumConfig not found, skipping HTTPRoute")
		return
	}

	// Check if gateway is enabled.
	if config.Spec.Gateway == nil || !config.Spec.Gateway.Enabled {
		log.V(1).Info("Gateway not enabled, skipping HTTPRoute")
		return
	}

	// Check if gateway is ready.
	if config.Status.Gateway == nil || !config.Status.Gateway.Ready {
		log.Info("Gateway not ready, skipping HTTPRoute creation")
		webEndpointGatewayNotReady.Add(ctx, 1, metric.WithAttributes(
			attribute.String("sympozium.instance", agentRun.Spec.InstanceRef),
		))
		return
	}

	// Derive hostname.
	hostname := ""
	if params != nil {
		hostname = params["hostname"]
	}
	if hostname == "" && config.Spec.Gateway.BaseDomain != "" {
		hostname = agentRun.Spec.InstanceRef + "." + config.Spec.Gateway.BaseDomain
	}
	if hostname == "" {
		log.V(1).Info("No hostname available for HTTPRoute")
		return
	}

	routeName := agentRun.Name + "-web"

	// Check if route already exists.
	var existing gatewayv1.HTTPRoute
	if err := r.Get(ctx, types.NamespacedName{Name: routeName, Namespace: agentRun.Namespace}, &existing); err == nil {
		return // Already exists
	}

	gatewayName := config.Spec.Gateway.Name
	if gatewayName == "" {
		gatewayName = "sympozium-gateway"
	}
	gatewayNS := gatewayv1.Namespace(systemNamespace)
	port := gatewayv1.PortNumber(8080)

	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      routeName,
			Namespace: agentRun.Namespace,
			Labels: map[string]string{
				"sympozium.ai/agent-run": agentRun.Name,
				"sympozium.ai/instance":  agentRun.Spec.InstanceRef,
				"sympozium.ai/component": "agent-server",
			},
		},
		Spec: gatewayv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{
					{
						Name:      gatewayv1.ObjectName(gatewayName),
						Namespace: &gatewayNS,
					},
				},
			},
			Hostnames: []gatewayv1.Hostname{gatewayv1.Hostname(hostname)},
			Rules: []gatewayv1.HTTPRouteRule{
				{
					BackendRefs: []gatewayv1.HTTPBackendRef{
						{
							BackendRef: gatewayv1.BackendRef{
								BackendObjectReference: gatewayv1.BackendObjectReference{
									Name: gatewayv1.ObjectName(svcName),
									Port: &port,
								},
							},
						},
					},
				},
			},
		},
	}

	if err := controllerutil.SetControllerReference(agentRun, route, r.Scheme); err != nil {
		log.Error(err, "Failed to set owner reference on HTTPRoute")
		return
	}

	if err := r.Create(ctx, route); err != nil {
		if !errors.IsAlreadyExists(err) {
			log.Error(err, "Failed to create HTTPRoute", "name", routeName)
		}
		return
	}

	webEndpointRouteCreated.Add(ctx, 1, metric.WithAttributes(
		attribute.String("sympozium.instance", agentRun.Spec.InstanceRef),
	))
	log.Info("Created HTTPRoute for server-mode AgentRun", "route", routeName, "hostname", hostname)
}

// validatePolicy checks the AgentRun against the applicable SympoziumPolicy.
func (r *AgentRunReconciler) validatePolicy(ctx context.Context, agentRun *sympoziumv1alpha1.AgentRun) error {
	// Look up the SympoziumInstance to find the policy
	instance := &sympoziumv1alpha1.SympoziumInstance{}
	if err := r.Get(ctx, client.ObjectKey{
		Namespace: agentRun.Namespace,
		Name:      agentRun.Spec.InstanceRef,
	}, instance); err != nil {
		return fmt.Errorf("instance %q not found: %w", agentRun.Spec.InstanceRef, err)
	}

	if instance.Spec.PolicyRef == "" {
		return nil // No policy, allow
	}

	policy := &sympoziumv1alpha1.SympoziumPolicy{}
	if err := r.Get(ctx, client.ObjectKey{
		Namespace: agentRun.Namespace,
		Name:      instance.Spec.PolicyRef,
	}, policy); err != nil {
		return fmt.Errorf("policy %q not found: %w", instance.Spec.PolicyRef, err)
	}

	// Validate sub-agent depth
	if agentRun.Spec.Parent != nil && policy.Spec.SubagentPolicy != nil {
		if agentRun.Spec.Parent.SpawnDepth > policy.Spec.SubagentPolicy.MaxDepth {
			return fmt.Errorf("sub-agent depth %d exceeds max %d",
				agentRun.Spec.Parent.SpawnDepth, policy.Spec.SubagentPolicy.MaxDepth)
		}
	}

	// Validate concurrency
	if policy.Spec.SubagentPolicy != nil {
		activeRuns := &sympoziumv1alpha1.AgentRunList{}
		if err := r.List(ctx, activeRuns,
			client.InNamespace(agentRun.Namespace),
			client.MatchingLabels{"sympozium.ai/instance": agentRun.Spec.InstanceRef},
		); err == nil {
			running := 0
			for _, run := range activeRuns.Items {
				if run.Status.Phase == sympoziumv1alpha1.AgentRunPhaseRunning {
					running++
				}
			}
			if running >= policy.Spec.SubagentPolicy.MaxConcurrent {
				return fmt.Errorf("concurrency limit reached: %d/%d", running, policy.Spec.SubagentPolicy.MaxConcurrent)
			}
		}
	}

	return nil
}

// ensureAgentServiceAccount creates the sympozium-agent ServiceAccount in the
// given namespace if it does not already exist. This is needed because agent
// Jobs reference this SA and run in the user's namespace, not sympozium-system.
func (r *AgentRunReconciler) ensureAgentServiceAccount(ctx context.Context, namespace string) error {
	sa := &corev1.ServiceAccount{}
	err := r.Get(ctx, client.ObjectKey{Name: "sympozium-agent", Namespace: namespace}, sa)
	if err == nil {
		return nil // already exists
	}
	if !errors.IsNotFound(err) {
		return fmt.Errorf("checking for agent service account: %w", err)
	}
	sa = &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sympozium-agent",
			Namespace: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "sympozium",
			},
		},
	}
	if err := r.Create(ctx, sa); err != nil {
		if errors.IsAlreadyExists(err) {
			return nil
		}
		return fmt.Errorf("creating agent service account: %w", err)
	}
	return nil
}

// seccompProfileForPod determines the pod-level seccomp profile.
// Priority: AgentRun spec > default (RuntimeDefault).
func seccompProfileForPod(agentRun *sympoziumv1alpha1.AgentRun) *corev1.SeccompProfile {
	if agentRun.Spec.Sandbox != nil &&
		agentRun.Spec.Sandbox.SecurityContext != nil &&
		agentRun.Spec.Sandbox.SecurityContext.SeccompProfile != nil {
		return &corev1.SeccompProfile{
			Type: corev1.SeccompProfileType(agentRun.Spec.Sandbox.SecurityContext.SeccompProfile.Type),
		}
	}
	return &corev1.SeccompProfile{
		Type: corev1.SeccompProfileTypeRuntimeDefault,
	}
}

// buildJob constructs the Kubernetes Job for an AgentRun.
func (r *AgentRunReconciler) buildJob(
	agentRun *sympoziumv1alpha1.AgentRun,
	memoryEnabled bool,
	observability *sympoziumv1alpha1.ObservabilitySpec,
	sidecars []resolvedSidecar,
) *batchv1.Job {
	labels := map[string]string{
		"sympozium.ai/agent-run": agentRun.Name,
		"sympozium.ai/instance":  agentRun.Spec.InstanceRef,
		"sympozium.ai/component": "agent-run",
	}

	ttl := int32(300)
	deadline := int64(600)
	if agentRun.Spec.Timeout != nil {
		deadline = int64(agentRun.Spec.Timeout.Duration.Seconds()) + 60
	}
	backoffLimit := int32(0)

	// Build containers
	containers := r.buildContainers(agentRun, memoryEnabled, observability, sidecars)
	volumes := r.buildVolumes(agentRun, memoryEnabled, sidecars)
	hostNetwork, hostPID := derivePodHostAccess(sidecars)
	dnsPolicy := corev1.DNSClusterFirst
	if hostNetwork {
		dnsPolicy = corev1.DNSClusterFirstWithHostNet
	}

	runAsNonRoot := true
	runAsUser := int64(1000)
	fsGroup := int64(1000)

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      agentRun.Name,
			Namespace: agentRun.Namespace,
			Labels:    labels,
		},
		Spec: batchv1.JobSpec{
			TTLSecondsAfterFinished: &ttl,
			ActiveDeadlineSeconds:   &deadline,
			BackoffLimit:            &backoffLimit,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					RestartPolicy:      corev1.RestartPolicyNever,
					ServiceAccountName: "sympozium-agent",
					HostNetwork:        hostNetwork,
					HostPID:            hostPID,
					DNSPolicy:          dnsPolicy,
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:   &runAsNonRoot,
						RunAsUser:      &runAsUser,
						FSGroup:        &fsGroup,
						SeccompProfile: seccompProfileForPod(agentRun),
					},
					Containers: containers,
					Volumes:    volumes,
				},
			},
		},
	}
}

// buildContainers constructs the container list for an agent pod.
func (r *AgentRunReconciler) buildContainers(
	agentRun *sympoziumv1alpha1.AgentRun,
	memoryEnabled bool,
	observability *sympoziumv1alpha1.ObservabilitySpec,
	sidecars []resolvedSidecar,
) []corev1.Container {
	readOnly := true
	noPrivEsc := false

	agentEnv := []corev1.EnvVar{
		{Name: "AGENT_RUN_ID", Value: agentRun.Name},
		{Name: "AGENT_ID", Value: agentRun.Spec.AgentID},
		{Name: "SESSION_KEY", Value: agentRun.Spec.SessionKey},
		{Name: "TASK", Value: agentRun.Spec.Task},
		{Name: "SYSTEM_PROMPT", Value: agentRun.Spec.SystemPrompt},
		{Name: "MODEL_PROVIDER", Value: agentRun.Spec.Model.Provider},
		{Name: "MODEL_NAME", Value: agentRun.Spec.Model.Model},
		{Name: "MODEL_BASE_URL", Value: agentRun.Spec.Model.BaseURL},
		{Name: "THINKING_MODE", Value: agentRun.Spec.Model.Thinking},
	}

	ipcEnv := []corev1.EnvVar{
		{Name: "AGENT_RUN_ID", Value: agentRun.Name},
		{Name: "INSTANCE_NAME", Value: agentRun.Spec.InstanceRef},
		{Name: "EVENT_BUS_URL", Value: "nats://nats.sympozium-system.svc:4222"},
	}

	// Inject OTel env vars when observability is configured.
	if tp := agentRun.Annotations["otel.dev/traceparent"]; tp != "" {
		agentEnv = append(agentEnv, corev1.EnvVar{Name: "TRACEPARENT", Value: tp})
		ipcEnv = append(ipcEnv, corev1.EnvVar{Name: "TRACEPARENT", Value: tp})
	}
	if observability != nil && observability.OTLPEndpoint != "" {
		agentEnv = append(agentEnv,
			corev1.EnvVar{Name: "OTEL_EXPORTER_OTLP_ENDPOINT", Value: observability.OTLPEndpoint},
			corev1.EnvVar{Name: "OTEL_SERVICE_NAME", Value: "sympozium-agent-runner"},
		)
		ipcEnv = append(ipcEnv,
			corev1.EnvVar{Name: "OTEL_EXPORTER_OTLP_ENDPOINT", Value: observability.OTLPEndpoint},
			corev1.EnvVar{Name: "OTEL_SERVICE_NAME", Value: "sympozium-ipc-bridge"},
		)
	}

	containers := []corev1.Container{
		// Main agent container
		{
			Name:            "agent",
			Image:           r.imageRef("agent-runner"),
			ImagePullPolicy: corev1.PullIfNotPresent,
			SecurityContext: &corev1.SecurityContext{
				ReadOnlyRootFilesystem:   &readOnly,
				AllowPrivilegeEscalation: &noPrivEsc,
				Capabilities: &corev1.Capabilities{
					Drop: []corev1.Capability{"ALL"},
				},
			},
			Env: []corev1.EnvVar{
				{Name: "AGENT_RUN_ID", Value: agentRun.Name},
				{Name: "AGENT_ID", Value: agentRun.Spec.AgentID},
				{Name: "SESSION_KEY", Value: agentRun.Spec.SessionKey},
				{Name: "INSTANCE_NAME", Value: agentRun.Spec.InstanceRef},
				{Name: "AGENT_NAMESPACE", Value: agentRun.Namespace},
				{Name: "TASK", Value: agentRun.Spec.Task},
				{Name: "SYSTEM_PROMPT", Value: agentRun.Spec.SystemPrompt},
				{Name: "MODEL_PROVIDER", Value: agentRun.Spec.Model.Provider},
				{Name: "MODEL_NAME", Value: agentRun.Spec.Model.Model},
				{Name: "MODEL_BASE_URL", Value: agentRun.Spec.Model.BaseURL},
				{Name: "THINKING_MODE", Value: agentRun.Spec.Model.Thinking},
			},
			VolumeMounts: []corev1.VolumeMount{
				{Name: "workspace", MountPath: "/workspace"},
				{Name: "skills", MountPath: "/skills", ReadOnly: true},
				{Name: "ipc", MountPath: "/ipc"},
				{Name: "tmp", MountPath: "/tmp"},
			},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("250m"),
					corev1.ResourceMemory: resource.MustParse("512Mi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1"),
					corev1.ResourceMemory: resource.MustParse("1Gi"),
				},
			},
		},
		// IPC bridge sidecar
		{
			Name:            "ipc-bridge",
			Image:           r.imageRef("ipc-bridge"),
			ImagePullPolicy: corev1.PullIfNotPresent,
			SecurityContext: &corev1.SecurityContext{
				ReadOnlyRootFilesystem:   &readOnly,
				AllowPrivilegeEscalation: &noPrivEsc,
				Capabilities: &corev1.Capabilities{
					Drop: []corev1.Capability{"ALL"},
				},
			},
			Env: []corev1.EnvVar{
				{Name: "AGENT_RUN_ID", Value: agentRun.Name},
				{Name: "INSTANCE_NAME", Value: agentRun.Spec.InstanceRef},
				{Name: "AGENT_NAMESPACE", Value: agentRun.Namespace},
				{Name: "EVENT_BUS_URL", Value: "nats://nats.sympozium-system.svc:4222"},
			},
			VolumeMounts: []corev1.VolumeMount{
				{Name: "ipc", MountPath: "/ipc"},
			},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("50m"),
					corev1.ResourceMemory: resource.MustParse("64Mi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("100m"),
					corev1.ResourceMemory: resource.MustParse("128Mi"),
				},
			},
		},
	}

	// Inject auth secret if provided.
	if agentRun.Spec.Model.AuthSecretRef != "" {
		containers[0].EnvFrom = []corev1.EnvFromSource{
			{
				SecretRef: &corev1.SecretEnvSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: agentRun.Spec.Model.AuthSecretRef,
					},
				},
			},
		}
	}

	// Add memory volume mount if memory is enabled.
	if memoryEnabled {
		containers[0].VolumeMounts = append(containers[0].VolumeMounts,
			corev1.VolumeMount{Name: "memory", MountPath: "/memory", ReadOnly: true},
		)
		containers[0].Env = append(containers[0].Env,
			corev1.EnvVar{Name: "MEMORY_ENABLED", Value: "true"},
		)
	}

	// Add sandbox sidecar if enabled
	if agentRun.Spec.Sandbox != nil && agentRun.Spec.Sandbox.Enabled {
		sandboxImage := r.imageRef("sandbox")
		if agentRun.Spec.Sandbox.Image != "" {
			sandboxImage = agentRun.Spec.Sandbox.Image
		}

		containers = append(containers, corev1.Container{
			Name:            "sandbox",
			Image:           sandboxImage,
			ImagePullPolicy: corev1.PullIfNotPresent,
			SecurityContext: &corev1.SecurityContext{
				ReadOnlyRootFilesystem: &readOnly,
				Capabilities: &corev1.Capabilities{
					Drop: []corev1.Capability{"ALL"},
				},
			},
			Command: []string{"sleep", "infinity"},
			VolumeMounts: []corev1.VolumeMount{
				{Name: "workspace", MountPath: "/workspace"},
				{Name: "tmp", MountPath: "/tmp"},
			},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("100m"),
					corev1.ResourceMemory: resource.MustParse("256Mi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("500m"),
					corev1.ResourceMemory: resource.MustParse("512Mi"),
				},
			},
		})
	}

	// Always enable tools — the IPC bridge is always present so
	// send_channel_message, read_file, and list_directory work without
	// sidecars.  execute_command gracefully times out if no skill sidecar
	// is running to handle exec requests.
	containers[0].Env = append(containers[0].Env,
		corev1.EnvVar{Name: "TOOLS_ENABLED", Value: "true"},
	)

	// Pass channel context so the agent knows how to reply when the run
	// was triggered by a channel message (WhatsApp, Telegram, etc.).
	if ch := agentRun.Annotations["sympozium.ai/reply-channel"]; ch != "" {
		containers[0].Env = append(containers[0].Env,
			corev1.EnvVar{Name: "SOURCE_CHANNEL", Value: ch},
		)
	}
	if cid := agentRun.Annotations["sympozium.ai/reply-chat-id"]; cid != "" {
		containers[0].Env = append(containers[0].Env,
			corev1.EnvVar{Name: "SOURCE_CHAT_ID", Value: cid},
		)
	}

	// Inject per-instance OpenTelemetry configuration.
	if observability != nil && observability.Enabled {
		containers[0].Env = append(containers[0].Env, buildObservabilityEnv(agentRun, observability)...)
		containers[1].Env = append(containers[1].Env, buildObservabilityEnv(agentRun, observability)...)
	}

	// Inject skill sidecar containers.
	for _, sc := range sidecars {
		cmd := sc.sidecar.Command

		var envVars []corev1.EnvVar
		for _, e := range sc.sidecar.Env {
			envVars = append(envVars, corev1.EnvVar{Name: e.Name, Value: e.Value})
		}

		mounts := []corev1.VolumeMount{
			{Name: "ipc", MountPath: "/ipc"},
			{Name: "tmp", MountPath: "/tmp"},
		}
		if sc.sidecar.MountWorkspace {
			mounts = append(mounts, corev1.VolumeMount{Name: "workspace", MountPath: "/workspace"})
		}

		hostAccessEnabled := sc.sidecar.HostAccess != nil && sc.sidecar.HostAccess.Enabled
		if hostAccessEnabled {
			for idx, hostMount := range sc.sidecar.HostAccess.Mounts {
				if hostMount.HostPath == "" || hostMount.MountPath == "" {
					continue
				}
				readOnly := true
				if hostMount.ReadOnly != nil {
					readOnly = *hostMount.ReadOnly
				}
				mounts = append(mounts, corev1.VolumeMount{
					Name:      hostAccessVolumeName(sc.skillPackName, idx),
					MountPath: hostMount.MountPath,
					ReadOnly:  readOnly,
				})
			}
		}

		cpuReq := "100m"
		memReq := "128Mi"
		if sc.sidecar.Resources != nil {
			if sc.sidecar.Resources.CPU != "" {
				cpuReq = sc.sidecar.Resources.CPU
			}
			if sc.sidecar.Resources.Memory != "" {
				memReq = sc.sidecar.Resources.Memory
			}
		}

		container := corev1.Container{
			Name:            fmt.Sprintf("skill-%s", sc.skillPackName),
			Image:           sc.sidecar.Image,
			ImagePullPolicy: corev1.PullIfNotPresent,
			Env:             envVars,
			VolumeMounts:    mounts,
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse(cpuReq),
					corev1.ResourceMemory: resource.MustParse(memReq),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse(cpuReq),
					corev1.ResourceMemory: resource.MustParse(memReq),
				},
			},
		}

		if hostAccessEnabled {
			sidecarSC := &corev1.SecurityContext{}
			if sc.sidecar.HostAccess.Privileged {
				privileged := true
				allowPrivEsc := true
				sidecarSC.Privileged = &privileged
				sidecarSC.AllowPrivilegeEscalation = &allowPrivEsc
				// Privileged sidecars need Unconfined seccomp to
				// avoid blocking syscalls required for node-level operations.
				sidecarSC.SeccompProfile = &corev1.SeccompProfile{
					Type: corev1.SeccompProfileTypeUnconfined,
				}
			}
			if sc.sidecar.HostAccess.RunAsRoot {
				runAsUser := int64(0)
				runAsNonRoot := false
				sidecarSC.RunAsUser = &runAsUser
				sidecarSC.RunAsNonRoot = &runAsNonRoot
			}
			if sidecarSC.Privileged != nil || sidecarSC.RunAsUser != nil || sidecarSC.RunAsNonRoot != nil {
				container.SecurityContext = sidecarSC
			}
		}
		// Only set Command if the SkillPack specifies one; otherwise
		// let the container image's default CMD (tool-executor) run.
		if len(cmd) > 0 {
			container.Command = cmd
		}

		// Inject per-instance SKILL_<KEY> env vars from SkillRef.Params.
		for k, v := range sc.params {
			envKey := "SKILL_" + strings.ToUpper(k)
			container.Env = append(container.Env, corev1.EnvVar{Name: envKey, Value: v})
		}

		// Mount the skill's SecretRef if one is configured.
		if sc.sidecar.SecretRef != "" {
			mountPath := sc.sidecar.SecretMountPath
			if mountPath == "" {
				mountPath = "/secrets/" + sc.sidecar.SecretRef
			}
			volName := "skill-secret-" + sc.skillPackName
			container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
				Name:      volName,
				MountPath: mountPath,
				ReadOnly:  true,
			})
		}

		containers = append(containers, container)
	}

	return containers
}

func buildObservabilityEnv(agentRun *sympoziumv1alpha1.AgentRun, obs *sympoziumv1alpha1.ObservabilitySpec) []corev1.EnvVar {
	env := []corev1.EnvVar{
		{Name: "SYMPOZIUM_OTEL_ENABLED", Value: "true"},
	}

	if obs.OTLPEndpoint != "" {
		env = append(env,
			corev1.EnvVar{Name: "SYMPOZIUM_OTEL_OTLP_ENDPOINT", Value: obs.OTLPEndpoint},
			corev1.EnvVar{Name: "OTEL_EXPORTER_OTLP_ENDPOINT", Value: obs.OTLPEndpoint},
		)
	}
	if obs.OTLPProtocol != "" {
		env = append(env,
			corev1.EnvVar{Name: "SYMPOZIUM_OTEL_OTLP_PROTOCOL", Value: obs.OTLPProtocol},
			corev1.EnvVar{Name: "OTEL_EXPORTER_OTLP_PROTOCOL", Value: obs.OTLPProtocol},
		)
	}
	if obs.ServiceName != "" {
		env = append(env,
			corev1.EnvVar{Name: "SYMPOZIUM_OTEL_SERVICE_NAME", Value: obs.ServiceName},
			corev1.EnvVar{Name: "OTEL_SERVICE_NAME", Value: obs.ServiceName},
		)
	}

	attrs := map[string]string{
		"sympozium.instance.name": agentRun.Spec.InstanceRef,
		"sympozium.agent_run.id":  agentRun.Name,
		"k8s.namespace.name":      agentRun.Namespace,
	}
	for k, v := range obs.ResourceAttributes {
		attrs[k] = v
	}
	var pairs []string
	for k, v := range attrs {
		if k == "" || v == "" {
			continue
		}
		pairs = append(pairs, fmt.Sprintf("%s=%s", k, v))
	}
	sort.Strings(pairs)
	if len(pairs) > 0 {
		resourceAttrs := strings.Join(pairs, ",")
		env = append(env,
			corev1.EnvVar{Name: "SYMPOZIUM_OTEL_RESOURCE_ATTRIBUTES", Value: resourceAttrs},
			corev1.EnvVar{Name: "OTEL_RESOURCE_ATTRIBUTES", Value: resourceAttrs},
		)
	}

	return env
}

// buildVolumes constructs the volume list for an agent pod.
func (r *AgentRunReconciler) buildVolumes(agentRun *sympoziumv1alpha1.AgentRun, memoryEnabled bool, sidecars []resolvedSidecar) []corev1.Volume {
	workspaceSizeLimit := resource.MustParse("1Gi")
	ipcSizeLimit := resource.MustParse("64Mi")
	tmpSizeLimit := resource.MustParse("256Mi")
	memoryMedium := corev1.StorageMediumMemory

	volumes := []corev1.Volume{
		{
			Name: "workspace",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{
					SizeLimit: &workspaceSizeLimit,
				},
			},
		},
		{
			Name: "ipc",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{
					Medium:    memoryMedium,
					SizeLimit: &ipcSizeLimit,
				},
			},
		},
		{
			Name: "tmp",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{
					SizeLimit: &tmpSizeLimit,
				},
			},
		},
	}

	// Build skills projected volume from skill references
	var sources []corev1.VolumeProjection
	for _, skill := range agentRun.Spec.Skills {
		if skill.SkillPackRef != "" {
			sources = append(sources, corev1.VolumeProjection{
				ConfigMap: &corev1.ConfigMapProjection{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: skill.SkillPackRef,
					},
				},
			})
		}
		if skill.ConfigMapRef != "" {
			sources = append(sources, corev1.VolumeProjection{
				ConfigMap: &corev1.ConfigMapProjection{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: skill.ConfigMapRef,
					},
				},
			})
		}
	}

	if len(sources) > 0 {
		volumes = append(volumes, corev1.Volume{
			Name: "skills",
			VolumeSource: corev1.VolumeSource{
				Projected: &corev1.ProjectedVolumeSource{
					Sources: sources,
				},
			},
		})
	} else {
		// Empty skills volume
		volumes = append(volumes, corev1.Volume{
			Name: "skills",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		})
	}

	// Add memory ConfigMap volume if memory is enabled.
	if memoryEnabled {
		cmName := fmt.Sprintf("%s-memory", agentRun.Spec.InstanceRef)
		volumes = append(volumes, corev1.Volume{
			Name: "memory",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: cmName,
					},
					Optional: boolPtr(true),
				},
			},
		})
	}

	// Add Secret volumes for skill sidecars that require credentials.
	for _, sc := range sidecars {
		if sc.sidecar.SecretRef == "" {
			continue
		}
		volName := "skill-secret-" + sc.skillPackName
		volumes = append(volumes, corev1.Volume{
			Name: volName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: sc.sidecar.SecretRef,
					Optional:   boolPtr(true),
				},
			},
		})
	}

	// Add hostPath volumes for skill sidecars with host access enabled.
	for _, sc := range sidecars {
		if sc.sidecar.HostAccess == nil || !sc.sidecar.HostAccess.Enabled {
			continue
		}
		for idx, mount := range sc.sidecar.HostAccess.Mounts {
			if mount.HostPath == "" || mount.MountPath == "" {
				continue
			}
			hostPath := mount.HostPath
			volumes = append(volumes, corev1.Volume{
				Name: hostAccessVolumeName(sc.skillPackName, idx),
				VolumeSource: corev1.VolumeSource{
					HostPath: &corev1.HostPathVolumeSource{
						Path: hostPath,
					},
				},
			})
		}
	}

	return volumes
}

func derivePodHostAccess(sidecars []resolvedSidecar) (hostNetwork bool, hostPID bool) {
	for _, sc := range sidecars {
		if sc.sidecar.HostAccess == nil || !sc.sidecar.HostAccess.Enabled {
			continue
		}
		if sc.sidecar.HostAccess.HostNetwork {
			hostNetwork = true
		}
		if sc.sidecar.HostAccess.HostPID {
			hostPID = true
		}
	}
	return hostNetwork, hostPID
}

func hostAccessVolumeName(skillPackName string, index int) string {
	raw := strings.ToLower(fmt.Sprintf("host-%s-%d", skillPackName, index))
	var b strings.Builder
	for _, ch := range raw {
		switch {
		case ch >= 'a' && ch <= 'z':
			b.WriteRune(ch)
		case ch >= '0' && ch <= '9':
			b.WriteRune(ch)
		default:
			b.WriteByte('-')
		}
	}
	name := strings.Trim(b.String(), "-")
	if name == "" {
		name = "host-mount"
	}
	if len(name) > 63 {
		name = name[:63]
	}
	return name
}

// boolPtr returns a pointer to a bool.
func boolPtr(b bool) *bool { return &b }

// createInputConfigMap creates a ConfigMap with the agent's task input.
func (r *AgentRunReconciler) createInputConfigMap(ctx context.Context, agentRun *sympoziumv1alpha1.AgentRun) error {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-input", agentRun.Name),
			Namespace: agentRun.Namespace,
			Labels: map[string]string{
				"sympozium.ai/agent-run": agentRun.Name,
			},
		},
		Data: map[string]string{
			"task":          agentRun.Spec.Task,
			"system-prompt": agentRun.Spec.SystemPrompt,
			"agent-id":      agentRun.Spec.AgentID,
			"session-key":   agentRun.Spec.SessionKey,
		},
	}

	if err := controllerutil.SetControllerReference(agentRun, cm, r.Scheme); err != nil {
		return err
	}

	if err := r.Create(ctx, cm); err != nil {
		if errors.IsAlreadyExists(err) {
			return nil
		}
		return err
	}
	return nil
}

// succeedRun marks an AgentRun as succeeded and stores the result.
func (r *AgentRunReconciler) succeedRun(ctx context.Context, agentRun *sympoziumv1alpha1.AgentRun, result string, usage *sympoziumv1alpha1.TokenUsage) (ctrl.Result, error) {
	now := metav1.Now()
	agentRun.Status.Phase = sympoziumv1alpha1.AgentRunPhaseSucceeded
	agentRun.Status.CompletedAt = &now
	agentRun.Status.Result = result
	agentRun.Status.TokenUsage = usage

	// Record run metrics.
	runAttrs := metric.WithAttributes(
		attribute.String("sympozium.agent.status", "succeeded"),
		attribute.String("sympozium.instance", agentRun.Spec.InstanceRef),
	)
	agentRunsTotal.Add(ctx, 1, runAttrs)
	if usage != nil && usage.DurationMs > 0 {
		agentDurationHist.Record(ctx, float64(usage.DurationMs), runAttrs)
	}

	logAttrs := []any{
		"agent_run", agentRun.Name,
		"instance", agentRun.Spec.InstanceRef,
		"status", "succeeded",
	}
	if usage != nil {
		logAttrs = append(logAttrs,
			"input_tokens", usage.InputTokens,
			"output_tokens", usage.OutputTokens,
			"tool_calls", usage.ToolCalls,
			"duration_ms", usage.DurationMs,
		)
	}
	slog.InfoContext(ctx, "agent.run.succeeded", logAttrs...)

	return ctrl.Result{}, r.Status().Update(ctx, agentRun)
}

const (
	resultMarkerStart = "__SYMPOZIUM_RESULT__"
	resultMarkerEnd   = "__SYMPOZIUM_END__"
)

// extractResultFromPod reads the agent container logs and looks for the
// structured result marker written by agent-runner.
// Returns (result, errorMessage, tokenUsage). For failed runs, errorMessage is
// populated from the structured marker when available.
func (r *AgentRunReconciler) extractResultFromPod(ctx context.Context, log logr.Logger, agentRun *sympoziumv1alpha1.AgentRun) (string, string, *sympoziumv1alpha1.TokenUsage) {
	if r.Clientset == nil || agentRun.Status.PodName == "" {
		return "", "", nil
	}

	tailLines := int64(80)
	opts := &corev1.PodLogOptions{
		Container: "agent",
		TailLines: &tailLines,
	}
	req := r.Clientset.CoreV1().Pods(agentRun.Namespace).GetLogs(agentRun.Status.PodName, opts)
	stream, err := req.Stream(ctx)
	if err != nil {
		log.V(1).Info("could not read pod logs for result", "err", err)
		return "", "", nil
	}
	defer stream.Close()

	raw, err := io.ReadAll(stream)
	if err != nil {
		log.V(1).Info("error reading pod logs", "err", err)
		return "", "", nil
	}

	return parseAgentResultFromLogs(string(raw), log)
}

// parseAgentResultFromLogs parses the structured result marker emitted by the
// agent-runner and extracts either the success response or the failure message.
func parseAgentResultFromLogs(logs string, log logr.Logger) (string, string, *sympoziumv1alpha1.TokenUsage) {
	startIdx := strings.LastIndex(logs, resultMarkerStart)
	if startIdx < 0 {
		if fallbackErr := extractLikelyProviderErrorFromLogs(logs); fallbackErr != "" {
			return "", fallbackErr, nil
		}
		return "", "", nil
	}
	payload := logs[startIdx+len(resultMarkerStart):]
	endIdx := strings.Index(payload, resultMarkerEnd)
	if endIdx < 0 {
		return "", "", nil
	}
	jsonStr := strings.TrimSpace(payload[:endIdx])

	// Parse the full agent result including metrics.
	var parsed struct {
		Status   string `json:"status"`
		Response string `json:"response"`
		Error    string `json:"error"`
		Metrics  struct {
			DurationMs   int64 `json:"durationMs"`
			InputTokens  int   `json:"inputTokens"`
			OutputTokens int   `json:"outputTokens"`
			ToolCalls    int   `json:"toolCalls"`
		} `json:"metrics"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		log.V(1).Info("could not parse result JSON", "err", err)
		return "", "", nil
	}

	var usage *sympoziumv1alpha1.TokenUsage
	if parsed.Metrics.InputTokens > 0 || parsed.Metrics.OutputTokens > 0 {
		usage = &sympoziumv1alpha1.TokenUsage{
			InputTokens:  parsed.Metrics.InputTokens,
			OutputTokens: parsed.Metrics.OutputTokens,
			TotalTokens:  parsed.Metrics.InputTokens + parsed.Metrics.OutputTokens,
			ToolCalls:    parsed.Metrics.ToolCalls,
			DurationMs:   parsed.Metrics.DurationMs,
		}
		log.Info("extracted token usage",
			"inputTokens", usage.InputTokens,
			"outputTokens", usage.OutputTokens,
			"totalTokens", usage.TotalTokens,
			"toolCalls", usage.ToolCalls,
			"durationMs", usage.DurationMs)
	}

	if parsed.Status == "error" {
		msg := strings.TrimSpace(parsed.Error)
		if msg == "" {
			msg = "agent run failed"
		}
		return "", msg, nil
	}

	return parsed.Response, "", usage
}

// extractLikelyProviderErrorFromLogs scans plain log lines for provider quota
// and rate-limit failures when no structured marker can be parsed.
func extractLikelyProviderErrorFromLogs(logs string) string {
	lines := strings.Split(logs, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		has429 := strings.Contains(lower, "http 429") ||
			strings.Contains(lower, "status 429") ||
			strings.Contains(lower, "status code: 429") ||
			strings.Contains(lower, "(429)") ||
			strings.Contains(lower, " 429 ")
		hasQuotaSignal := strings.Contains(lower, "insufficient_quota") ||
			strings.Contains(lower, "quota") ||
			strings.Contains(lower, "rate limit") ||
			strings.Contains(lower, "too many requests")
		if has429 || hasQuotaSignal {
			return truncateForStatus(line, 500)
		}
	}
	return ""
}

func truncateForStatus(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}

const (
	memoryMarkerStart = "__SYMPOZIUM_MEMORY__"
	memoryMarkerEnd   = "__SYMPOZIUM_MEMORY_END__"
)

// extractAndPersistMemory reads the agent container logs for a memory update
// marker and patches the instance's memory ConfigMap with the new content.
func (r *AgentRunReconciler) extractAndPersistMemory(ctx context.Context, log logr.Logger, agentRun *sympoziumv1alpha1.AgentRun) {
	if r.Clientset == nil || agentRun.Status.PodName == "" {
		return
	}

	tailLines := int64(100)
	opts := &corev1.PodLogOptions{
		Container: "agent",
		TailLines: &tailLines,
	}
	req := r.Clientset.CoreV1().Pods(agentRun.Namespace).GetLogs(agentRun.Status.PodName, opts)
	stream, err := req.Stream(ctx)
	if err != nil {
		return
	}
	defer stream.Close()

	raw, err := io.ReadAll(stream)
	if err != nil {
		return
	}

	logs := string(raw)
	startIdx := strings.LastIndex(logs, memoryMarkerStart)
	if startIdx < 0 {
		return
	}
	payload := logs[startIdx+len(memoryMarkerStart):]
	endIdx := strings.Index(payload, memoryMarkerEnd)
	if endIdx < 0 {
		return
	}
	memoryContent := strings.TrimSpace(payload[:endIdx])
	if memoryContent == "" {
		return
	}

	// Patch the memory ConfigMap.
	cmName := fmt.Sprintf("%s-memory", agentRun.Spec.InstanceRef)
	var cm corev1.ConfigMap
	if err := r.Get(ctx, client.ObjectKey{
		Namespace: agentRun.Namespace,
		Name:      cmName,
	}, &cm); err != nil {
		log.V(1).Info("memory ConfigMap not found, skipping memory update", "err", err)
		return
	}

	cm.Data["MEMORY.md"] = memoryContent
	if err := r.Update(ctx, &cm); err != nil {
		log.V(1).Info("failed to update memory ConfigMap", "err", err)
		return
	}
	log.Info("Updated memory ConfigMap", "configmap", cmName, "bytes", len(memoryContent))
}

// failRun marks an AgentRun as failed.
func (r *AgentRunReconciler) failRun(ctx context.Context, agentRun *sympoziumv1alpha1.AgentRun, reason string) error {
	now := metav1.Now()
	agentRun.Status.Phase = sympoziumv1alpha1.AgentRunPhaseFailed
	agentRun.Status.CompletedAt = &now
	agentRun.Status.Error = reason

	// Record failure metrics.
	failAttrs := metric.WithAttributes(
		attribute.String("sympozium.agent.status", "failed"),
		attribute.String("sympozium.instance", agentRun.Spec.InstanceRef),
	)
	agentRunsTotal.Add(ctx, 1, failAttrs)
	controllerErrors.Add(ctx, 1, metric.WithAttributes(
		attribute.String("error.type", "agent_run_failed"),
		attribute.String("sympozium.instance", agentRun.Spec.InstanceRef),
	))

	slog.ErrorContext(ctx, "agent.run.failed",
		"agent_run", agentRun.Name,
		"instance", agentRun.Spec.InstanceRef,
		"error", reason,
	)

	return r.Status().Update(ctx, agentRun)
}

// --- Skill sidecar resolution and RBAC ---

// resolvedSidecar pairs a SkillPack name with its sidecar spec and per-instance params.
type resolvedSidecar struct {
	skillPackName string
	sidecar       sympoziumv1alpha1.SkillSidecar
	params        map[string]string // per-instance SKILL_* env vars (from SkillRef.Params)
}

// resolveSkillSidecars looks up SkillPack CRDs for the AgentRun's active
// skills and returns any that have a sidecar defined.
func (r *AgentRunReconciler) resolveSkillSidecars(ctx context.Context, log logr.Logger, agentRun *sympoziumv1alpha1.AgentRun) []resolvedSidecar {
	var sidecars []resolvedSidecar
	for _, ref := range agentRun.Spec.Skills {
		if ref.SkillPackRef == "" {
			continue
		}
		// The SkillPackRef on the AgentRun may be the ConfigMap name produced by
		// the SkillPack controller (e.g. "skillpack-k8s-ops"). Try to resolve
		// the SkillPack CRD by stripping the "skillpack-" prefix first.
		spName := ref.SkillPackRef
		if strings.HasPrefix(spName, "skillpack-") {
			spName = strings.TrimPrefix(spName, "skillpack-")
		}

		sp := &sympoziumv1alpha1.SkillPack{}
		if err := r.Get(ctx, client.ObjectKey{
			Namespace: agentRun.Namespace,
			Name:      spName,
		}, sp); err != nil {
			// SkillPack not in agent namespace — try sympozium-system (default
			// location for built-in skills installed by `sympozium install`).
			if err2 := r.Get(ctx, client.ObjectKey{
				Namespace: systemNamespace,
				Name:      spName,
			}, sp); err2 != nil {
				log.V(1).Info("SkillPack not found, skipping sidecar", "name", spName)
				continue
			}
		}

		if sp.Spec.Sidecar != nil && sp.Spec.Sidecar.Image != "" {
			sidecars = append(sidecars, resolvedSidecar{
				skillPackName: spName,
				sidecar:       *sp.Spec.Sidecar,
				params:        ref.Params,
			})
		}
	}
	return sidecars
}

// mirrorSkillConfigMaps copies skill ConfigMaps from sympozium-system into the
// AgentRun's namespace so that projected volumes can reference them.
// ConfigMap volume projections are namespace-local in Kubernetes, so when
// SkillPacks live in sympozium-system their ConfigMaps must be mirrored.
func (r *AgentRunReconciler) mirrorSkillConfigMaps(ctx context.Context, log logr.Logger, agentRun *sympoziumv1alpha1.AgentRun) error {
	if agentRun.Namespace == systemNamespace {
		return nil // no mirroring needed
	}
	for _, ref := range agentRun.Spec.Skills {
		cmName := ref.SkillPackRef
		if cmName == "" {
			cmName = ref.ConfigMapRef
		}
		if cmName == "" {
			continue
		}

		// Look for the ConfigMap in sympozium-system.
		source := &corev1.ConfigMap{}
		if err := r.Get(ctx, client.ObjectKey{
			Namespace: systemNamespace,
			Name:      cmName,
		}, source); err != nil {
			log.V(1).Info("Skill ConfigMap not found in sympozium-system, skipping mirror", "configmap", cmName)
			continue
		}

		// Check if ConfigMap already exists in the agent namespace.
		existing := &corev1.ConfigMap{}
		if err := r.Get(ctx, client.ObjectKey{
			Namespace: agentRun.Namespace,
			Name:      cmName,
		}, existing); err == nil {
			// Already present — update data to ensure we have the latest skills.
			existing.Data = source.Data
			if err := r.Update(ctx, existing); err != nil {
				log.Error(err, "Failed to update mirrored skill ConfigMap", "configmap", cmName)
			} else {
				log.V(1).Info("Updated mirrored skill ConfigMap with latest data", "configmap", cmName)
			}
			continue
		}

		// Create a mirror copy in the agent namespace, owned by the AgentRun
		// so it is garbage-collected when the run is deleted.
		mirror := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cmName,
				Namespace: agentRun.Namespace,
				Labels: map[string]string{
					"sympozium.ai/component":  "skillpack-mirror",
					"sympozium.ai/agent-run":  agentRun.Name,
					"sympozium.ai/managed-by": "sympozium",
				},
			},
			Data: source.Data,
		}
		if err := controllerutil.SetControllerReference(agentRun, mirror, r.Scheme); err != nil {
			log.Error(err, "Failed to set owner reference on skill ConfigMap mirror", "configmap", cmName)
			continue
		}
		if err := r.Create(ctx, mirror); err != nil {
			if !errors.IsAlreadyExists(err) {
				log.Error(err, "Failed to mirror skill ConfigMap", "configmap", cmName)
			}
		} else {
			log.Info("Mirrored skill ConfigMap into agent namespace", "configmap", cmName, "from", systemNamespace)
		}
	}
	return nil
}

// ensureSkillRBAC creates Role/ClusterRole and bindings for skill sidecars.
// Resources are labelled with the AgentRun name for cleanup.
func (r *AgentRunReconciler) ensureSkillRBAC(ctx context.Context, log logr.Logger, agentRun *sympoziumv1alpha1.AgentRun, sidecars []resolvedSidecar) error {
	for _, sc := range sidecars {
		// Namespace-scoped Role + RoleBinding
		if len(sc.sidecar.RBAC) > 0 {
			roleName := fmt.Sprintf("sympozium-skill-%s-%s", sc.skillPackName, agentRun.Name)
			var rules []rbacv1.PolicyRule
			for _, rule := range sc.sidecar.RBAC {
				rules = append(rules, rbacv1.PolicyRule{
					APIGroups: rule.APIGroups,
					Resources: rule.Resources,
					Verbs:     rule.Verbs,
				})
			}

			role := &rbacv1.Role{
				ObjectMeta: metav1.ObjectMeta{
					Name:      roleName,
					Namespace: agentRun.Namespace,
					Labels: map[string]string{
						"sympozium.ai/agent-run":  agentRun.Name,
						"sympozium.ai/skill":      sc.skillPackName,
						"sympozium.ai/managed-by": "sympozium",
					},
				},
				Rules: rules,
			}
			if err := controllerutil.SetControllerReference(agentRun, role, r.Scheme); err != nil {
				log.Error(err, "Failed to set owner on Role")
			}
			if err := r.Create(ctx, role); err != nil && !errors.IsAlreadyExists(err) {
				return fmt.Errorf("creating skill Role %s: %w", roleName, err)
			}

			rb := &rbacv1.RoleBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:      roleName,
					Namespace: agentRun.Namespace,
					Labels: map[string]string{
						"sympozium.ai/agent-run":  agentRun.Name,
						"sympozium.ai/skill":      sc.skillPackName,
						"sympozium.ai/managed-by": "sympozium",
					},
				},
				RoleRef: rbacv1.RoleRef{
					APIGroup: "rbac.authorization.k8s.io",
					Kind:     "Role",
					Name:     roleName,
				},
				Subjects: []rbacv1.Subject{
					{
						Kind:      "ServiceAccount",
						Name:      "sympozium-agent",
						Namespace: agentRun.Namespace,
					},
				},
			}
			if err := controllerutil.SetControllerReference(agentRun, rb, r.Scheme); err != nil {
				log.Error(err, "Failed to set owner on RoleBinding")
			}
			if err := r.Create(ctx, rb); err != nil && !errors.IsAlreadyExists(err) {
				return fmt.Errorf("creating skill RoleBinding %s: %w", roleName, err)
			}
			log.Info("Created skill RBAC (namespaced)", "role", roleName, "skill", sc.skillPackName)
		}

		// Cluster-scoped ClusterRole + ClusterRoleBinding
		if len(sc.sidecar.ClusterRBAC) > 0 {
			crName := fmt.Sprintf("sympozium-skill-%s-%s", sc.skillPackName, agentRun.Name)
			var rules []rbacv1.PolicyRule
			for _, rule := range sc.sidecar.ClusterRBAC {
				rules = append(rules, rbacv1.PolicyRule{
					APIGroups: rule.APIGroups,
					Resources: rule.Resources,
					Verbs:     rule.Verbs,
				})
			}

			cr := &rbacv1.ClusterRole{
				ObjectMeta: metav1.ObjectMeta{
					Name: crName,
					Labels: map[string]string{
						"sympozium.ai/agent-run":  agentRun.Name,
						"sympozium.ai/skill":      sc.skillPackName,
						"sympozium.ai/managed-by": "sympozium",
					},
				},
				Rules: rules,
			}
			if err := r.Create(ctx, cr); err != nil && !errors.IsAlreadyExists(err) {
				return fmt.Errorf("creating skill ClusterRole %s: %w", crName, err)
			}

			crb := &rbacv1.ClusterRoleBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: crName,
					Labels: map[string]string{
						"sympozium.ai/agent-run":  agentRun.Name,
						"sympozium.ai/skill":      sc.skillPackName,
						"sympozium.ai/managed-by": "sympozium",
					},
				},
				RoleRef: rbacv1.RoleRef{
					APIGroup: "rbac.authorization.k8s.io",
					Kind:     "ClusterRole",
					Name:     crName,
				},
				Subjects: []rbacv1.Subject{
					{
						Kind:      "ServiceAccount",
						Name:      "sympozium-agent",
						Namespace: agentRun.Namespace,
					},
				},
			}
			if err := r.Create(ctx, crb); err != nil && !errors.IsAlreadyExists(err) {
				return fmt.Errorf("creating skill ClusterRoleBinding %s: %w", crName, err)
			}
			log.Info("Created skill RBAC (cluster)", "clusterRole", crName, "skill", sc.skillPackName)
		}
	}
	return nil
}

// cleanupSkillRBAC removes cluster-scoped RBAC resources created for an AgentRun.
// Namespace-scoped resources (Role, RoleBinding) are cleaned up automatically
// via owner references and garbage collection.
func (r *AgentRunReconciler) cleanupSkillRBAC(ctx context.Context, log logr.Logger, agentRun *sympoziumv1alpha1.AgentRun) {
	// List ClusterRoles owned by this run
	crList := &rbacv1.ClusterRoleList{}
	if err := r.List(ctx, crList, client.MatchingLabels{
		"sympozium.ai/agent-run":  agentRun.Name,
		"sympozium.ai/managed-by": "sympozium",
	}); err == nil {
		for i := range crList.Items {
			if err := r.Delete(ctx, &crList.Items[i]); err != nil && !errors.IsNotFound(err) {
				log.V(1).Info("Failed to delete ClusterRole", "name", crList.Items[i].Name, "err", err)
			}
		}
	}

	// List ClusterRoleBindings owned by this run
	crbList := &rbacv1.ClusterRoleBindingList{}
	if err := r.List(ctx, crbList, client.MatchingLabels{
		"sympozium.ai/agent-run":  agentRun.Name,
		"sympozium.ai/managed-by": "sympozium",
	}); err == nil {
		for i := range crbList.Items {
			if err := r.Delete(ctx, &crbList.Items[i]); err != nil && !errors.IsNotFound(err) {
				log.V(1).Info("Failed to delete ClusterRoleBinding", "name", crbList.Items[i].Name, "err", err)
			}
		}
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *AgentRunReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&sympoziumv1alpha1.AgentRun{}).
		Owns(&batchv1.Job{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Complete(r)
}
