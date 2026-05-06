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
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/eventbus"
	"github.com/sympozium-ai/sympozium/internal/orchestrator"
	"gopkg.in/yaml.v3"
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

// allowedAuthSecretKeys lists the only Secret keys that will be injected from
// an auth secret into the agent container. This prevents wholesale secret
// leakage when a secret contains extra keys.
var allowedAuthSecretKeys = []string{
	"OPENAI_API_KEY",
	"ANTHROPIC_API_KEY",
	"AZURE_OPENAI_API_KEY",
	"AZURE_OPENAI_ENDPOINT",
	"OLLAMA_HOST",
	"GOOGLE_API_KEY",
	"MISTRAL_API_KEY",
	"GROQ_API_KEY",
	"DEEPSEEK_API_KEY",
	"OPENROUTER_API_KEY",
	"API_KEY",
}

// deniedEnvVarKeys lists environment variable names that cannot be set via
// agentRun.spec.env to prevent injection attacks.
var deniedEnvVarKeys = map[string]bool{
	"PATH":            true,
	"LD_PRELOAD":      true,
	"LD_LIBRARY_PATH": true,
	"HOME":            true,
	"SHELL":           true,
	"USER":            true,
	"HOSTNAME":        true,
}

// DefaultRunHistoryLimit is how many completed AgentRuns to keep per instance
// before the oldest are pruned.
const DefaultRunHistoryLimit = 50

// AgentRunReconciler reconciles AgentRun objects.
// It watches AgentRun CRDs and reconciles them into Kubernetes Jobs/Pods.
type AgentRunReconciler struct {
	client.Client
	// APIReader bypasses the controller cache for reads — needed when we
	// must see status mutations committed by a concurrent reconcile that
	// the watch-based cache may not yet have observed.
	APIReader       client.Reader
	Scheme          *runtime.Scheme
	Log             logr.Logger
	PodBuilder      *orchestrator.PodBuilder
	Clientset       kubernetes.Interface
	ImageTag        string // release tag for Sympozium images (e.g. "v0.0.25")
	RunHistoryLimit int    // max completed runs to keep per instance (0 = use default)

	// DynamicClient is used for Agent Sandbox CRD operations.
	// Nil when agent-sandbox support is disabled or CRDs are not installed.
	DynamicClient dynamic.Interface

	// EventBus publishes agent lifecycle events (e.g. agent.run.failed) so
	// that components like the web proxy can react without polling the CRD.
	// Optional — nil when NATS is not configured.
	EventBus eventbus.EventBus
}

const imageRegistry = "ghcr.io/sympozium-ai/sympozium"

// updateStatusWithRetry safely updates status handling resourceVersion conflicts
func (r *AgentRunReconciler) updateStatusWithRetry(ctx context.Context, agentRun *sympoziumv1alpha1.AgentRun, mutate func(ar *sympoziumv1alpha1.AgentRun)) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := &sympoziumv1alpha1.AgentRun{}
		if err := r.Get(ctx, client.ObjectKeyFromObject(agentRun), latest); err != nil {
			return err
		}
		mutate(latest)
		return r.Status().Update(ctx, latest)
	})
}

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
func resolveOTelEndpoint(instance *sympoziumv1alpha1.Agent) string {
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
			attribute.String("instance.name", agentRun.Spec.AgentRef),
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
	case sympoziumv1alpha1.AgentRunPhasePostRunning:
		result, err = r.reconcilePostRunning(ctx, log, agentRun)
	case sympoziumv1alpha1.AgentRunPhaseServing:
		result, err = r.reconcileServing(ctx, log, agentRun)
	case sympoziumv1alpha1.AgentRunPhaseAwaitingDelegate:
		result, err = r.reconcileAwaitingDelegate(ctx, log, agentRun)
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
			attribute.String("instance.name", agentRun.Spec.AgentRef),
		),
	)
	defer span.End()

	log.Info("Reconciling pending AgentRun")

	// Resolve modelRef: if the AgentRun references a Model CR, resolve its
	// endpoint to populate provider, baseURL, and model fields automatically.
	if agentRun.Spec.Model.ModelRef != "" {
		model, err := ResolveModelRef(ctx, r.Client, agentRun.Spec.Model.ModelRef, agentRun.Namespace)
		if err != nil {
			return ctrl.Result{}, r.failRun(ctx, agentRun, fmt.Sprintf("model %q not found: %v", agentRun.Spec.Model.ModelRef, err))
		}
		if model.Status.Phase != sympoziumv1alpha1.ModelPhaseReady {
			return ctrl.Result{}, r.failRun(ctx, agentRun, fmt.Sprintf("model %q is not ready (phase: %s)", agentRun.Spec.Model.ModelRef, model.Status.Phase))
		}
		agentRun.Spec.Model.Provider = "openai"
		agentRun.Spec.Model.BaseURL = model.Status.Endpoint
		agentRun.Spec.Model.Model = model.Name
		agentRun.Spec.Model.AuthSecretRef = "" // cluster-internal, no auth needed
	}

	// Validate against policy
	if err := r.validatePolicy(ctx, agentRun); err != nil {
		return ctrl.Result{}, r.failRun(ctx, agentRun, fmt.Sprintf("policy validation failed: %v", err))
	}

	// Validate gate hook constraints.
	if err := validateGateHooks(agentRun); err != nil {
		return ctrl.Result{}, r.failRun(ctx, agentRun, fmt.Sprintf("gate hook validation failed: %v", err))
	}

	// Check membrane token budget before creating the job.
	if err := r.checkTokenBudget(ctx, log, agentRun); err != nil {
		return ctrl.Result{}, r.failRun(ctx, agentRun, fmt.Sprintf("token budget exceeded: %v", err))
	}

	// Agent Sandbox mode — create Sandbox CR instead of Job.
	if agentRun.Spec.AgentSandbox != nil && agentRun.Spec.AgentSandbox.Enabled {
		return r.reconcilePendingAgentSandbox(ctx, log, agentRun)
	}

	// Ensure the sympozium-agent ServiceAccount exists in the target namespace.
	if err := r.ensureAgentServiceAccount(ctx, agentRun.Namespace); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensuring agent service account: %w", err)
	}

	// Create the input ConfigMap with the task
	if err := r.createInputConfigMap(ctx, agentRun); err != nil {
		return ctrl.Result{}, fmt.Errorf("creating input ConfigMap: %w", err)
	}

	// Look up the Agent to check for memory configuration.
	instance := &sympoziumv1alpha1.Agent{}
	memoryEnabled := false
	var observability *sympoziumv1alpha1.ObservabilitySpec
	var mcpServers []sympoziumv1alpha1.MCPServerRef
	if err := r.Get(ctx, client.ObjectKey{
		Namespace: agentRun.Namespace,
		Name:      agentRun.Spec.AgentRef,
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
		// If the AgentRun has no skills, inherit from the Agent.
		// This is a safety net — tuiCreateRun and the schedule controller
		// should already copy skills, but older runs or manual CRs may not.
		// Skip inheritance for web-proxy child runs — they intentionally omit
		// server-mode skills (like web-endpoint) to run as one-shot Jobs.
		if len(agentRun.Spec.Skills) == 0 && len(instance.Spec.Skills) > 0 {
			if agentRun.Labels["sympozium.ai/source"] != "web-proxy" {
				agentRun.Spec.Skills = instance.Spec.Skills
			}
		}
		mcpServers = instance.Spec.MCPServers

		// Extract the owning Ensemble name for persona-aware delegation.
		if packName := instance.Labels["sympozium.ai/ensemble"]; packName != "" {
			agentRun.Labels["sympozium.ai/ensemble"] = packName
		}

		// Propagate RunTimeout from instance config to AgentRun spec if not already set.
		if agentRun.Spec.Timeout == nil && instance.Spec.Agents.Default.RunTimeout != "" {
			if d, err := time.ParseDuration(instance.Spec.Agents.Default.RunTimeout); err == nil && d > 0 {
				agentRun.Spec.Timeout = &metav1.Duration{Duration: d}
			}
		}
	}

	// Resolve MCPServer CRs: for any mcpServer entry without a URL,
	// look up the MCPServer CR by name and use its status.url.
	if len(mcpServers) > 0 {
		mcpServers = r.resolveMCPServerURLs(ctx, agentRun.Namespace, mcpServers)
	}

	// Create MCP ConfigMap if MCP servers are configured.
	if len(mcpServers) > 0 {
		if err := r.ensureMCPConfigMap(ctx, agentRun, mcpServers); err != nil {
			return ctrl.Result{}, fmt.Errorf("creating MCP ConfigMap: %w", err)
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

	// Server mode is only used when explicitly set on the spec (e.g. by the
	// web-endpoint reconciler).  Do not auto-promote task runs to server mode
	// just because a RequiresServer sidecar is attached — feed/one-shot runs
	// inherit all instance skills and must remain task-scoped.
	if agentRun.Spec.Mode == "server" {
		return r.reconcilePendingServer(ctx, log, agentRun, sidecars)
	}

	// Filter out server-only sidecars (RequiresServer) — they are not
	// meaningful in a task-mode Job and would waste resources.
	taskSidecars := make([]resolvedSidecar, 0, len(sidecars))
	for _, sc := range sidecars {
		if sc.sidecar.RequiresServer {
			log.V(1).Info("Skipping server-only sidecar in task mode", "skillPack", sc.skillPackName)
			continue
		}
		taskSidecars = append(taskSidecars, sc)
	}
	sidecars = taskSidecars

	// If the memory skill is attached, verify the memory server Deployment
	// exists AND has at least one ready replica before creating the Job.
	// The instance controller creates it asynchronously — if it hasn't
	// reconciled yet or the pod isn't ready, requeue rather than creating
	// a pod that hangs on the wait-for-memory init container.
	// Give up after 120s to avoid infinite requeue loops.
	if agentRunHasMemorySkill(agentRun) {
		memoryDeployName := fmt.Sprintf("%s-memory", agentRun.Spec.AgentRef)
		var memoryDeploy appsv1.Deployment
		if err := r.Get(ctx, client.ObjectKey{
			Namespace: agentRun.Namespace,
			Name:      memoryDeployName,
		}, &memoryDeploy); err != nil {
			age := time.Since(agentRun.CreationTimestamp.Time)
			if age > 120*time.Second {
				return ctrl.Result{}, r.failRun(ctx, agentRun,
					fmt.Sprintf("memory server deployment %q not found after %s — ensure the instance has been reconciled", memoryDeployName, age.Truncate(time.Second)))
			}
			log.Info("Memory server deployment not found, requeueing", "deployment", memoryDeployName, "age", age.Truncate(time.Second))
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		if memoryDeploy.Status.ReadyReplicas < 1 {
			age := time.Since(agentRun.CreationTimestamp.Time)
			if age > 120*time.Second {
				return ctrl.Result{}, r.failRun(ctx, agentRun,
					fmt.Sprintf("memory server deployment %q has no ready replicas after %s", memoryDeployName, age.Truncate(time.Second)))
			}
			log.Info("Memory server not ready, requeueing", "deployment", memoryDeployName, "readyReplicas", memoryDeploy.Status.ReadyReplicas, "age", age.Truncate(time.Second))
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
	}

	// Mirror skill ConfigMaps from sympozium-system into the agent namespace
	// so projected volumes can reference them (ConfigMaps are namespace-local).
	if err := r.mirrorSkillConfigMaps(ctx, log, agentRun); err != nil {
		log.Error(err, "Failed to mirror skill ConfigMaps, skills may be missing")
	}

	// Create RBAC resources for skill sidecars that need them.
	// This is fatal: without RBAC the agent pod will run but every kubectl/API
	// call inside the skill sidecar will fail with "forbidden". Common causes:
	//   - Expired ServiceAccount tokens (check kube-apiserver logs for
	//     "service account token has expired")
	//   - Clock skew between cluster nodes (`date` on each node vs NTP)
	//   - Controller ClusterRole missing RBAC delegation permissions
	//     (re-run `helm upgrade` to sync the latest chart RBAC)
	if err := r.ensureSkillRBAC(ctx, log, agentRun, sidecars); err != nil {
		return ctrl.Result{}, r.failRun(ctx, agentRun,
			fmt.Sprintf("failed to create skill RBAC — the agent would run without Kubernetes permissions. "+
				"Check controller logs and kube-apiserver for authentication errors. "+
				"Common causes: expired ServiceAccount tokens, clock skew between nodes, "+
				"or missing RBAC permissions on the controller ClusterRole (re-run helm upgrade). "+
				"Underlying error: %v", err))
	}

	// Create RBAC for lifecycle hook containers if needed.
	if err := r.ensureLifecycleRBAC(ctx, log, agentRun); err != nil {
		return ctrl.Result{}, r.failRun(ctx, agentRun,
			fmt.Sprintf("failed to create lifecycle RBAC — hook containers would lack Kubernetes permissions. "+
				"Check controller logs and kube-apiserver for authentication errors. "+
				"Underlying error: %v", err))
	}

	// Create a workspace PVC when postRun lifecycle hooks are defined,
	// so the workspace persists between the main Job and the postRun Job.
	if agentRun.Spec.Lifecycle != nil && len(agentRun.Spec.Lifecycle.PostRun) > 0 {
		if err := r.ensureWorkspacePVC(ctx, agentRun); err != nil {
			return ctrl.Result{}, fmt.Errorf("creating workspace PVC: %w", err)
		}
	}

	// Build and create the Job
	job := r.buildJob(agentRun, memoryEnabled, observability, sidecars, mcpServers)

	// Inject shared workflow memory env vars and init container if the pack has shared memory.
	r.injectSharedMemory(ctx, agentRun, job)

	// Inject ensemble relationship context so the agent-runner can auto-generate
	// delegation/supervision instructions in the system prompt.
	r.injectRelationshipContext(ctx, agentRun, job)

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
	err := r.updateStatusWithRetry(ctx, agentRun, func(ar *sympoziumv1alpha1.AgentRun) {
		now := metav1.Now()
		ar.Status.Phase = sympoziumv1alpha1.AgentRunPhaseRunning
		ar.Status.JobName = job.Name
		ar.Status.StartedAt = &now

		// Set the trace ID so operators can look up the full distributed trace.
		if sc := span.SpanContext(); sc.HasTraceID() {
			ar.Status.TraceID = sc.TraceID().String()
		}
	})
	if err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

// reconcileRunning checks on a running Job and updates status.
func (r *AgentRunReconciler) reconcileRunning(ctx context.Context, log logr.Logger, agentRun *sympoziumv1alpha1.AgentRun) (ctrl.Result, error) {
	ctx, span := controllerTracer.Start(ctx, "agentrun.extract_result",
		trace.WithAttributes(
			attribute.String("agentrun.name", agentRun.Name),
			attribute.String("instance.name", agentRun.Spec.AgentRef),
		),
	)
	defer span.End()

	log.Info("Checking running AgentRun")

	// Agent Sandbox mode — check Sandbox CR status instead of Job.
	if agentRun.Status.SandboxName != "" || agentRun.Status.SandboxClaimName != "" {
		return r.reconcileRunningAgentSandbox(ctx, log, agentRun)
	}

	// Find the Job
	job := &batchv1.Job{}
	jobName := client.ObjectKey{
		Namespace: agentRun.Namespace,
		Name:      agentRun.Status.JobName,
	}
	if err := r.Get(ctx, jobName, job); err != nil {
		if errors.IsNotFound(err) {
			// Guard against the race where the Job was already deleted (to
			// kill sidecars) and a concurrent reconcile has already
			// transitioned the phase to a terminal state. Read with the
			// non-cached APIReader (the watch cache may not have the
			// status update yet) — if the run is already terminal, don't
			// override it with "Job not found".
			fresh := &sympoziumv1alpha1.AgentRun{}
			reader := client.Reader(r.APIReader)
			if reader == nil {
				reader = r.Client
			}
			if getErr := reader.Get(ctx, client.ObjectKeyFromObject(agentRun), fresh); getErr == nil {
				switch fresh.Status.Phase {
				case sympoziumv1alpha1.AgentRunPhaseSucceeded,
					sympoziumv1alpha1.AgentRunPhaseFailed:
					// Already terminal — don't override.
					return ctrl.Result{}, nil
				case sympoziumv1alpha1.AgentRunPhasePostRunning:
					// PostRun container is still executing — let the
					// PostRunning reconcile path handle it.
					return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
				}
			}
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
			podName := podList.Items[0].Name
			err := r.updateStatusWithRetry(ctx, agentRun, func(ar *sympoziumv1alpha1.AgentRun) {
				// ⚠️ re-check condition on fresh object!
				if ar.Status.PodName == "" {
					ar.Status.PodName = podName
				}
			})
			if err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	// hasPostRunHooks is true when lifecycle postRun containers are defined.
	hasPostRunHooks := agentRun.Spec.Lifecycle != nil && len(agentRun.Spec.Lifecycle.PostRun) > 0

	// Check Job completion
	if job.Status.Succeeded > 0 {
		// Extract the LLM response from pod logs before the pod is gone.
		result, _, usage := r.extractResultFromPod(ctx, log, agentRun)
		// Extract and persist memory updates if applicable.
		r.extractAndPersistMemory(ctx, log, agentRun)
		if hasPostRunHooks {
			return r.startPostRun(ctx, log, agentRun, 0, result, usage)
		}
		return r.succeedRun(ctx, agentRun, result, usage)
	}
	if job.Status.Failed > 0 {
		// The Job may report failure because a non-agent container (e.g. ipc-bridge)
		// exited non-zero. Check if the agent itself succeeded via structured result
		// markers in pod logs — if so, treat it as success.
		result, podErr, usage := r.extractResultFromPod(ctx, log, agentRun)
		if result != "" && podErr == "" {
			log.Info("Job failed but agent produced a success result; treating as success")
			r.extractAndPersistMemory(ctx, log, agentRun)
			if hasPostRunHooks {
				return r.startPostRun(ctx, log, agentRun, 0, result, usage)
			}
			return r.succeedRun(ctx, agentRun, result, usage)
		}
		errMsg := "Job failed"
		if podErr != "" {
			errMsg = podErr
		}
		r.extractAndPersistMemory(ctx, log, agentRun)
		r.persistFailureMemory(ctx, log, agentRun, errMsg)
		if hasPostRunHooks {
			return r.startPostRun(ctx, log, agentRun, 1, errMsg, nil)
		}
		return ctrl.Result{}, r.failRun(ctx, agentRun, errMsg)
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
				if hasPostRunHooks {
					return r.startPostRun(ctx, log, agentRun, 0, result, usage)
				}
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
			r.extractAndPersistMemory(ctx, log, agentRun)
			r.persistFailureMemory(ctx, log, agentRun, errMsg)
			_ = r.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationBackground))
			if hasPostRunHooks {
				return r.startPostRun(ctx, log, agentRun, 1, errMsg, nil)
			}
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
			r.extractAndPersistMemory(ctx, log, agentRun)
			r.persistFailureMemory(ctx, log, agentRun, "timeout")
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

	// Clean up workspace PVC if it was created for postRun lifecycle hooks.
	if agentRun.Status.PostRunJobName != "" {
		r.cleanupWorkspacePVC(ctx, log, agentRun)
	}

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

	// Update membrane token budget if this run belongs to a budget-tracked ensemble.
	if err := r.updateTokenBudget(ctx, log, agentRun); err != nil {
		log.Error(err, "Failed to update token budget")
		// Non-fatal: don't block cleanup.
	}

	// Trigger sequential successors: if this run succeeded and belongs to an
	// ensemble with sequential relationships, create runs for target personas.
	if agentRun.Status.Phase == sympoziumv1alpha1.AgentRunPhaseSucceeded {
		if err := r.triggerSequentialSuccessors(ctx, log, agentRun); err != nil {
			log.Error(err, "Failed to trigger sequential successors")
			// Non-fatal: don't block cleanup.
		}
	}

	// Prune old runs beyond the history limit for this instance.
	if err := r.pruneOldRuns(ctx, log, agentRun); err != nil {
		log.Error(err, "Failed to prune old AgentRuns")
		// Non-fatal: don't block reconciliation.
	}

	return ctrl.Result{}, nil
}

// reconcileAwaitingDelegate handles AgentRuns that are waiting for a delegated
// child run to complete. The agent pod is still alive (the delegate tool is
// blocking), so the main responsibility is to skip timeout enforcement while
// the parent waits. The SpawnRouter transitions the parent back to Running
// once the child finishes and delivers the result via IPC.
func (r *AgentRunReconciler) reconcileAwaitingDelegate(ctx context.Context, log logr.Logger, agentRun *sympoziumv1alpha1.AgentRun) (ctrl.Result, error) {
	log.Info("AgentRun awaiting delegate completion",
		"delegates", len(agentRun.Status.Delegates),
	)
	// Requeue periodically so the controller can react to phase transitions
	// made by the SpawnRouter. No timeout check — the parent is blocked on
	// a delegation tool call and the child may take several minutes.
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

// triggerSequentialSuccessors looks up the Ensemble that owns this run's
// instance. For each sequential relationship where the completed persona is the
// source, it creates a new AgentRun for the target persona — implementing the
// "pipeline" execution pattern where one persona's completion triggers the next.
func (r *AgentRunReconciler) triggerSequentialSuccessors(ctx context.Context, log logr.Logger, agentRun *sympoziumv1alpha1.AgentRun) error {
	// Look up the source instance to get the persona name and ensemble.
	if agentRun.Spec.AgentRef == "" {
		return nil
	}
	var sourceInst sympoziumv1alpha1.Agent
	if err := r.Get(ctx, types.NamespacedName{Name: agentRun.Spec.AgentRef, Namespace: agentRun.Namespace}, &sourceInst); err != nil {
		return nil // Instance gone — skip.
	}
	sourcePersona := sourceInst.Labels["sympozium.ai/agent-config"]
	ensembleName := sourceInst.Labels["sympozium.ai/ensemble"]
	if sourcePersona == "" || ensembleName == "" {
		return nil // Not part of an ensemble.
	}

	// Look up the ensemble.
	var ensemble sympoziumv1alpha1.Ensemble
	if err := r.Get(ctx, types.NamespacedName{Name: ensembleName, Namespace: agentRun.Namespace}, &ensemble); err != nil {
		return nil // Ensemble gone — skip.
	}

	// Check if we already triggered successors for this run (prevent duplicates
	// from re-reconciliation). We use a label on the completed run as a marker.
	if agentRun.Labels["sympozium.ai/sequential-triggered"] == "true" {
		return nil
	}

	// Find sequential edges where this persona is the source.
	triggered := false
	for _, rel := range ensemble.Spec.Relationships {
		if rel.Type != "sequential" || rel.Source != sourcePersona {
			continue
		}

		targetPersona := rel.Target
		targetAgentName := ensembleName + "-" + targetPersona
		log.Info("Triggering sequential successor",
			"source", sourcePersona, "target", targetPersona,
			"targetAgent", targetAgentName)

		// Look up the target instance.
		var targetInst sympoziumv1alpha1.Agent
		if err := r.Get(ctx, types.NamespacedName{Name: targetAgentName, Namespace: agentRun.Namespace}, &targetInst); err != nil {
			log.Error(err, "Sequential target instance not found", "instance", targetAgentName)
			continue
		}

		// Build a structured handoff card so the successor agent can clearly
		// distinguish what happened from what it should do next.
		targetTask := ""
		for _, p := range ensemble.Spec.AgentConfigs {
			if p.Name == targetPersona && p.Schedule != nil && p.Schedule.Task != "" {
				targetTask = p.Schedule.Task
				break
			}
		}
		task := buildHandoffTask(sourcePersona, agentRun.Spec.Task, agentRun.Status.Result, targetTask)

		// Create the successor AgentRun.
		runName := fmt.Sprintf("%s-seq-%d", targetAgentName, time.Now().UnixMilli()%100000)
		successorRun := &sympoziumv1alpha1.AgentRun{
			ObjectMeta: metav1.ObjectMeta{
				Name:      runName,
				Namespace: agentRun.Namespace,
				Labels: map[string]string{
					"sympozium.ai/instance":        targetAgentName,
					"sympozium.ai/ensemble":        ensembleName,
					"sympozium.ai/sequential-from": agentRun.Name,
				},
			},
			Spec: sympoziumv1alpha1.AgentRunSpec{
				AgentRef: targetAgentName,
				Task:     task,
				AgentID:  fmt.Sprintf("sequential-from-%s", sourcePersona),
				Model: sympoziumv1alpha1.ModelSpec{
					Provider:      resolveProvider(&targetInst),
					Model:         targetInst.Spec.Agents.Default.Model,
					BaseURL:       targetInst.Spec.Agents.Default.BaseURL,
					AuthSecretRef: resolveAuthSecret(&targetInst),
				},
				ImagePullSecrets: targetInst.Spec.ImagePullSecrets,
				Lifecycle:        targetInst.Spec.Agents.Default.Lifecycle,
				Volumes:          targetInst.Spec.Volumes,
				VolumeMounts:     targetInst.Spec.VolumeMounts,
			},
		}

		// Propagate dry run flag through the pipeline.
		if agentRun.Spec.DryRun {
			successorRun.Spec.DryRun = true
			successorRun.Labels["sympozium.ai/dry-run"] = "true"
		}

		// Copy skills from the target instance.
		for _, skill := range targetInst.Spec.Skills {
			if skill.SkillPackRef == "web-endpoint" {
				continue
			}
			successorRun.Spec.Skills = append(successorRun.Spec.Skills, skill)
		}

		if err := r.Create(ctx, successorRun); err != nil {
			if errors.IsAlreadyExists(err) {
				log.Info("Sequential successor already exists", "run", runName)
				continue
			}
			log.Error(err, "Failed to create sequential successor", "run", runName)
			continue
		}
		log.Info("Created sequential successor run", "run", runName, "target", targetPersona)
		triggered = true
	}

	// Mark this run as having triggered its successors to prevent duplicates.
	if triggered {
		patch := client.MergeFrom(agentRun.DeepCopy())
		if agentRun.Labels == nil {
			agentRun.Labels = make(map[string]string)
		}
		agentRun.Labels["sympozium.ai/sequential-triggered"] = "true"
		if err := r.Patch(ctx, agentRun, patch); err != nil {
			log.Error(err, "Failed to mark run as sequential-triggered")
		}
	}

	return nil
}

// buildHandoffTask produces a structured handoff card for sequential pipeline
// transitions. The card clearly separates what the predecessor was asked to do,
// what it produced, and what the successor should do next.
func buildHandoffTask(sourcePersona, predecessorTask, predecessorResult, targetTask string) string {
	originalTask := extractOriginalTask(predecessorTask)
	if len(originalTask) > 200 {
		originalTask = originalTask[:200] + "..."
	}
	if len(predecessorResult) > 800 {
		predecessorResult = predecessorResult[:800] + "..."
	}
	if targetTask == "" {
		targetTask = "Continue the workflow as your role requires."
	}

	return fmt.Sprintf("## Handoff from %s\n\n### Previous Task\n%s\n\n### Result\n%s\n\n### Your Task\n%s",
		sourcePersona, originalTask, predecessorResult, targetTask)
}

// extractOriginalTask strips nested handoff headers from a task string. When
// pipelines chain (A→B→C), each successor's task is itself a handoff card. This
// function extracts the original task from the innermost "### Previous Task"
// section so context doesn't compound across hops.
func extractOriginalTask(task string) string {
	if !strings.HasPrefix(task, "## Handoff from") {
		return task
	}
	const marker = "### Previous Task\n"
	idx := strings.Index(task, marker)
	if idx < 0 {
		return task
	}
	rest := task[idx+len(marker):]
	endIdx := strings.Index(rest, "\n### ")
	if endIdx < 0 {
		return strings.TrimSpace(rest)
	}
	return strings.TrimSpace(rest[:endIdx])
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
	instanceRef := agentRun.Spec.AgentRef
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
			attribute.String("sympozium.instance", agentRun.Spec.AgentRef),
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

	// Create RBAC for sidecars — fatal if it fails (see reconcilePending for details).
	if err := r.ensureSkillRBAC(ctx, log, agentRun, sidecars); err != nil {
		return ctrl.Result{}, r.failRun(ctx, agentRun,
			fmt.Sprintf("failed to create skill RBAC — the server would run without Kubernetes permissions. "+
				"Check controller logs and kube-apiserver for authentication errors. "+
				"Common causes: expired ServiceAccount tokens, clock skew between nodes, "+
				"or missing RBAC permissions on the controller ClusterRole (re-run helm upgrade). "+
				"Underlying error: %v", err))
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
		corev1.EnvVar{Name: "INSTANCE_NAME", Value: agentRun.Spec.AgentRef},
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
		"sympozium.ai/agent-run":       agentRun.Name,
		"sympozium.ai/instance":        agentRun.Spec.AgentRef,
		"sympozium.ai/component":       "agent-server",
		"app.kubernetes.io/part-of":    "sympozium",
		"app.kubernetes.io/managed-by": "sympozium-controller",
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
					ImagePullSecrets:   agentRun.Spec.ImagePullSecrets,
					NodeSelector:       agentRun.Spec.Model.NodeSelector,
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
	err = r.updateStatusWithRetry(ctx, agentRun, func(ar *sympoziumv1alpha1.AgentRun) {
		now := metav1.Now()
		ar.Status.Phase = sympoziumv1alpha1.AgentRunPhaseServing
		ar.Status.DeploymentName = deployName
		ar.Status.ServiceName = svcName
		ar.Status.StartedAt = &now
	})
	if err != nil {
		return ctrl.Result{}, err
	}

	webEndpointServing.Add(ctx, 1, metric.WithAttributes(
		attribute.String("sympozium.instance", agentRun.Spec.AgentRef),
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

	secretName := agentRun.Spec.AgentRef + "-web-proxy-key"
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
				"sympozium.ai/instance":  agentRun.Spec.AgentRef,
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
			attribute.String("sympozium.instance", agentRun.Spec.AgentRef),
		))
		return
	}

	// Derive hostname.
	hostname := ""
	if params != nil {
		hostname = params["hostname"]
	}
	if hostname == "" && config.Spec.Gateway.BaseDomain != "" {
		hostname = agentRun.Spec.AgentRef + "." + config.Spec.Gateway.BaseDomain
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
				"sympozium.ai/instance":  agentRun.Spec.AgentRef,
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
		attribute.String("sympozium.instance", agentRun.Spec.AgentRef),
	))
	log.Info("Created HTTPRoute for server-mode AgentRun", "route", routeName, "hostname", hostname)
}

// validatePolicy checks the AgentRun against the applicable SympoziumPolicy.
func (r *AgentRunReconciler) validatePolicy(ctx context.Context, agentRun *sympoziumv1alpha1.AgentRun) error {
	// Look up the Agent to find the policy
	instance := &sympoziumv1alpha1.Agent{}
	if err := r.Get(ctx, client.ObjectKey{
		Namespace: agentRun.Namespace,
		Name:      agentRun.Spec.AgentRef,
	}, instance); err != nil {
		return fmt.Errorf("instance %q not found: %w", agentRun.Spec.AgentRef, err)
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
			client.MatchingLabels{"sympozium.ai/instance": agentRun.Spec.AgentRef},
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
	mcpServers []sympoziumv1alpha1.MCPServerRef,
) *batchv1.Job {
	labels := map[string]string{
		"sympozium.ai/agent-run":       agentRun.Name,
		"sympozium.ai/instance":        agentRun.Spec.AgentRef,
		"sympozium.ai/component":       "agent-run",
		"sympozium.ai/role":            "agent",
		"app.kubernetes.io/part-of":    "sympozium",
		"app.kubernetes.io/managed-by": "sympozium-controller",
	}

	ttl := int32(300)
	deadline := int64(600)
	if agentRun.Spec.Timeout != nil {
		deadline = int64(agentRun.Spec.Timeout.Duration.Seconds()) + 60
	}
	backoffLimit := int32(0)

	// Build containers
	containers, initContainers := r.buildContainers(agentRun, memoryEnabled, observability, sidecars, mcpServers)
	volumes := r.buildVolumes(agentRun, memoryEnabled, sidecars, mcpServers)
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
					ImagePullSecrets:   agentRun.Spec.ImagePullSecrets,
					HostNetwork:        hostNetwork,
					HostPID:            hostPID,
					DNSPolicy:          dnsPolicy,
					NodeSelector:       agentRun.Spec.Model.NodeSelector,
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:   &runAsNonRoot,
						RunAsUser:      &runAsUser,
						FSGroup:        &fsGroup,
						SeccompProfile: seccompProfileForPod(agentRun),
					},
					InitContainers: initContainers,
					Containers:     containers,
					Volumes:        volumes,
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
	mcpServers []sympoziumv1alpha1.MCPServerRef,
) ([]corev1.Container, []corev1.Container) {
	readOnly := true
	noPrivEsc := false
	var initContainers []corev1.Container

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

	// Inject RUN_TIMEOUT from the AgentRun spec or instance config.
	if agentRun.Spec.Timeout != nil {
		agentEnv = append(agentEnv, corev1.EnvVar{
			Name: "RUN_TIMEOUT", Value: agentRun.Spec.Timeout.Duration.String(),
		})
	}

	ipcEnv := []corev1.EnvVar{
		{Name: "AGENT_RUN_ID", Value: agentRun.Name},
		{Name: "INSTANCE_NAME", Value: agentRun.Spec.AgentRef},
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
				{Name: "INSTANCE_NAME", Value: agentRun.Spec.AgentRef},
				{Name: "ENSEMBLE_NAME", Value: agentRun.Labels["sympozium.ai/ensemble"]},
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
			Env: func() []corev1.EnvVar {
				env := []corev1.EnvVar{
					{Name: "AGENT_RUN_ID", Value: agentRun.Name},
					{Name: "INSTANCE_NAME", Value: agentRun.Spec.AgentRef},
					{Name: "AGENT_NAMESPACE", Value: agentRun.Namespace},
					{Name: "EVENT_BUS_URL", Value: "nats://nats.sympozium-system.svc:4222"},
				}
				if hasResponseGateHook(agentRun) {
					env = append(env, corev1.EnvVar{Name: "GATE_SUPPRESS_COMPLETION", Value: "true"})
				}
				return env
			}(),
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

	// Inject auth secret keys individually to avoid leaking unrelated secret
	// data into the agent container. Only known provider keys are mounted.
	if agentRun.Spec.Model.AuthSecretRef != "" {
		for _, key := range allowedAuthSecretKeys {
			optional := true
			containers[0].Env = append(containers[0].Env, corev1.EnvVar{
				Name: key,
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: agentRun.Spec.Model.AuthSecretRef,
						},
						Key:      key,
						Optional: &optional,
					},
				},
			})
		}
	}

	// Inject DRY_RUN flag so the agent-runner skips the LLM call.
	if agentRun.Spec.DryRun {
		containers[0].Env = append(containers[0].Env,
			corev1.EnvVar{Name: "DRY_RUN", Value: "true"},
		)
	}

	// Inject CANARY_MODE flag so the agent-runner runs built-in health checks.
	if agentRun.Spec.CanaryMode {
		containers[0].Env = append(containers[0].Env,
			corev1.EnvVar{Name: "CANARY_MODE", Value: "true"},
			corev1.EnvVar{
				Name: "HOST_IP",
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{FieldPath: "status.hostIP"},
				},
			},
		)
	}

	// Add memory volume mount if legacy memory is enabled.
	if memoryEnabled {
		containers[0].VolumeMounts = append(containers[0].VolumeMounts,
			corev1.VolumeMount{Name: "memory", MountPath: "/memory", ReadOnly: true},
		)
		containers[0].Env = append(containers[0].Env,
			corev1.EnvVar{Name: "MEMORY_ENABLED", Value: "true"},
		)
	}

	// Inject MEMORY_SERVER_URL for the standalone memory server.
	if agentRunHasMemorySkill(agentRun) {
		memoryURL := fmt.Sprintf("http://%s-memory.%s.svc:8080", agentRun.Spec.AgentRef, agentRun.Namespace)
		containers[0].Env = append(containers[0].Env,
			corev1.EnvVar{Name: "MEMORY_SERVER_URL", Value: memoryURL},
		)

		// Init container to wait for memory server readiness before agent starts.
		// The controller already checks for ready replicas before creating the pod,
		// so this is a safety net for cases where the memory server becomes briefly
		// unavailable between the controller check and pod scheduling.
		// Timeout after 120s to accommodate resource-constrained environments.
		initContainers = append(initContainers, corev1.Container{
			Name:            "wait-for-memory",
			Image:           "busybox:1.36",
			ImagePullPolicy: corev1.PullIfNotPresent,
			SecurityContext: &corev1.SecurityContext{
				ReadOnlyRootFilesystem:   &readOnly,
				AllowPrivilegeEscalation: &noPrivEsc,
				Capabilities: &corev1.Capabilities{
					Drop: []corev1.Capability{"ALL"},
				},
			},
			Command: []string{"sh", "-c",
				fmt.Sprintf("elapsed=0; until wget -q --spider --timeout=2 %s/health; do echo 'waiting for memory server...'; sleep 2; elapsed=$((elapsed+2)); if [ $elapsed -ge 120 ]; then echo 'ERROR: memory server not ready after 120s'; exit 1; fi; done", memoryURL),
			},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("50m"),
					corev1.ResourceMemory: resource.MustParse("32Mi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("100m"),
					corev1.ResourceMemory: resource.MustParse("64Mi"),
				},
			},
		})
	}

	// Inject custom environment variables from AgentRun spec.
	// Sort keys for deterministic pod specs.
	envKeys := make([]string, 0, len(agentRun.Spec.Env))
	for k := range agentRun.Spec.Env {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	for _, k := range envKeys {
		containers[0].Env = append(containers[0].Env, corev1.EnvVar{
			Name:  k,
			Value: agentRun.Spec.Env[k],
		})
	}

	// Append user-defined volume mounts from the AgentRun spec to the
	// main agent container. Skip any mount whose name collides with a
	// Sympozium-reserved volume.
	for _, vm := range agentRun.Spec.VolumeMounts {
		if isReservedVolumeName(vm.Name) {
			slog.Warn("dropping user-supplied volumeMount on agent container: name collides with a Sympozium-reserved volume",
				"agentrun", agentRun.Name,
				"namespace", agentRun.Namespace,
				"volumeMount", vm.Name,
				"mountPath", vm.MountPath,
				"source", "AgentRun.spec.volumeMounts",
				"reservedNames", reservedVolumeNamesList(),
			)
			continue
		}
		containers[0].VolumeMounts = append(containers[0].VolumeMounts, vm)
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

	// Add MCP bridge sidecar if MCP servers are configured.
	if len(mcpServers) > 0 {
		mcpEnv := []corev1.EnvVar{
			{Name: "AGENT_RUN_ID", Value: agentRun.Name},
			{Name: "MCP_CONFIG_PATH", Value: "/config/mcp-servers.yaml"},
			{Name: "MCP_IPC_PATH", Value: "/ipc/tools"},
			{Name: "MCP_MANIFEST_PATH", Value: "/ipc/tools/mcp-tools.json"},
		}
		if tp := agentRun.Annotations["otel.dev/traceparent"]; tp != "" {
			mcpEnv = append(mcpEnv, corev1.EnvVar{Name: "TRACEPARENT", Value: tp})
		}
		if observability != nil && observability.Enabled {
			mcpEnv = append(mcpEnv, buildObservabilityEnv(agentRun, observability)...)
		}
		// Inject auth secrets as env vars for each MCP server.
		for _, srv := range mcpServers {
			if srv.AuthSecret == "" {
				continue
			}
			envName := fmt.Sprintf("MCP_AUTH_%s", strings.ToUpper(strings.ReplaceAll(srv.Name, "-", "_")))
			key := srv.AuthKey
			if key == "" {
				key = "token"
			}
			mcpEnv = append(mcpEnv, corev1.EnvVar{
				Name: envName,
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: srv.AuthSecret},
						Key:                  key,
						Optional:             boolPtr(true),
					},
				},
			})
		}

		// Init container for MCP tool discovery (runs before agent starts)
		initContainers = append(initContainers, corev1.Container{
			Name:            "mcp-discover",
			Image:           r.imageRef("mcp-bridge"),
			ImagePullPolicy: corev1.PullIfNotPresent,
			SecurityContext: &corev1.SecurityContext{
				ReadOnlyRootFilesystem:   &readOnly,
				AllowPrivilegeEscalation: &noPrivEsc,
				Capabilities: &corev1.Capabilities{
					Drop: []corev1.Capability{"ALL"},
				},
			},
			Env: append(append([]corev1.EnvVar{}, mcpEnv...), corev1.EnvVar{Name: "MCP_DISCOVER_ONLY", Value: "true"}),
			VolumeMounts: []corev1.VolumeMount{
				{Name: "ipc", MountPath: "/ipc"},
				{Name: "mcp-config", MountPath: "/config", ReadOnly: true},
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
		})

		containers = append(containers, corev1.Container{
			Name:            "mcp-bridge",
			Image:           r.imageRef("mcp-bridge"),
			ImagePullPolicy: corev1.PullIfNotPresent,
			SecurityContext: &corev1.SecurityContext{
				ReadOnlyRootFilesystem:   &readOnly,
				AllowPrivilegeEscalation: &noPrivEsc,
				Capabilities: &corev1.Capabilities{
					Drop: []corev1.Capability{"ALL"},
				},
			},
			Env: mcpEnv,
			VolumeMounts: []corev1.VolumeMount{
				{Name: "ipc", MountPath: "/ipc"},
				{Name: "mcp-config", MountPath: "/config", ReadOnly: true},
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
		})
	}

	// Always enable tools — the IPC bridge is always present so
	// send_channel_message, read_file, and list_directory work without
	// sidecars.  execute_command gracefully times out if no skill sidecar
	// is running to handle exec requests.
	containers[0].Env = append(containers[0].Env,
		corev1.EnvVar{Name: "TOOLS_ENABLED", Value: "true"},
	)

	// Expose the list of attached skill-sidecar targets to the agent runner
	// so it can advise the LLM (and validate) on the optional `target` arg
	// of the execute_command tool. Comma-separated, in spec order.
	if len(sidecars) > 0 {
		names := make([]string, 0, len(sidecars))
		for _, sc := range sidecars {
			names = append(names, sc.skillPackName)
		}
		containers[0].Env = append(containers[0].Env,
			corev1.EnvVar{Name: "SYMPOZIUM_SKILL_TARGETS", Value: strings.Join(names, ",")},
		)
	}

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
		// SYMPOZIUM_SKILL_PACK identifies this sidecar to the tool-executor
		// so it can filter exec-requests by their optional `target` field.
		// Requests with target="" remain claimable by any sidecar (legacy).
		envVars = append(envVars, corev1.EnvVar{
			Name:  "SYMPOZIUM_SKILL_PACK",
			Value: sc.skillPackName,
		})
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

		// Append user-defined volume mounts from the SkillPack sidecar.
		// Skip any whose name collides with a Sympozium-reserved volume.
		for _, vm := range sc.sidecar.VolumeMounts {
			if isReservedVolumeName(vm.Name) {
				slog.Warn("dropping skill sidecar volumeMount: name collides with a Sympozium-reserved volume",
					"agentrun", agentRun.Name,
					"namespace", agentRun.Namespace,
					"volumeMount", vm.Name,
					"mountPath", vm.MountPath,
					"skillpack", sc.skillPackName,
					"source", "SkillPack.spec.sidecar.volumeMounts",
					"reservedNames", reservedVolumeNamesList(),
				)
				continue
			}
			mounts = append(mounts, vm)
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
		} else {
			// Apply restricted security context to non-privileged skill sidecars.
			container.SecurityContext = &corev1.SecurityContext{
				AllowPrivilegeEscalation: &noPrivEsc,
				Capabilities: &corev1.Capabilities{
					Drop: []corev1.Capability{"ALL"},
				},
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

	// Inject preRun lifecycle hook containers as init containers.
	// These run after system init containers (wait-for-memory, mcp-discover)
	// but before the agent starts.
	if agentRun.Spec.Lifecycle != nil {
		for _, hook := range agentRun.Spec.Lifecycle.PreRun {
			hookContainer := corev1.Container{
				Name:            fmt.Sprintf("pre-%s", hook.Name),
				Image:           hook.Image,
				ImagePullPolicy: corev1.PullIfNotPresent,
				SecurityContext: &corev1.SecurityContext{
					ReadOnlyRootFilesystem:   &readOnly,
					AllowPrivilegeEscalation: &noPrivEsc,
					Capabilities: &corev1.Capabilities{
						Drop: []corev1.Capability{"ALL"},
					},
				},
				VolumeMounts: []corev1.VolumeMount{
					{Name: "workspace", MountPath: "/workspace"},
					{Name: "ipc", MountPath: "/ipc"},
					{Name: "tmp", MountPath: "/tmp"},
				},
				Env: []corev1.EnvVar{
					{Name: "AGENT_RUN_ID", Value: agentRun.Name},
					{Name: "INSTANCE_NAME", Value: agentRun.Spec.AgentRef},
					{Name: "AGENT_NAMESPACE", Value: agentRun.Namespace},
				},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("100m"),
						corev1.ResourceMemory: resource.MustParse("128Mi"),
					},
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("512Mi"),
					},
				},
			}
			if len(hook.Command) > 0 {
				hookContainer.Command = hook.Command
			}
			if len(hook.Args) > 0 {
				hookContainer.Args = hook.Args
			}
			for _, e := range hook.Env {
				hookContainer.Env = append(hookContainer.Env, corev1.EnvVar{Name: e.Name, Value: e.Value})
			}
			// Forward custom env vars from spec.env.
			for _, k := range envKeys {
				hookContainer.Env = append(hookContainer.Env, corev1.EnvVar{Name: k, Value: agentRun.Spec.Env[k]})
			}
			initContainers = append(initContainers, hookContainer)
		}
	}

	return containers, initContainers
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
		"sympozium.instance.name": agentRun.Spec.AgentRef,
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

// injectSharedMemory adds WORKFLOW_MEMORY_SERVER_URL, WORKFLOW_MEMORY_ACCESS env vars
// and a wait-for-shared-memory init container to the Job's pod template if the
// AgentRun belongs to a Ensemble with shared memory enabled.
func (r *AgentRunReconciler) injectSharedMemory(ctx context.Context, agentRun *sympoziumv1alpha1.AgentRun, job *batchv1.Job) {
	packName := agentRun.Labels["sympozium.ai/ensemble"]
	if packName == "" {
		return
	}

	var pack sympoziumv1alpha1.Ensemble
	if err := r.Get(ctx, types.NamespacedName{Name: packName, Namespace: agentRun.Namespace}, &pack); err != nil {
		return
	}
	if pack.Spec.SharedMemory == nil || !pack.Spec.SharedMemory.Enabled {
		return
	}

	sharedMemoryURL := fmt.Sprintf("http://%s-shared-memory.%s.svc:8080", packName, agentRun.Namespace)

	// Resolve access mode for this persona from the instance's label.
	accessMode := "read-write"
	if agentRun.Spec.AgentRef != "" {
		var inst sympoziumv1alpha1.Agent
		if err := r.Get(ctx, types.NamespacedName{Name: agentRun.Spec.AgentRef, Namespace: agentRun.Namespace}, &inst); err == nil {
			personaName := inst.Labels["sympozium.ai/agent-config"]
			for _, rule := range pack.Spec.SharedMemory.AccessRules {
				if rule.AgentConfig == personaName {
					accessMode = rule.Access
					break
				}
			}
		}
	}

	// Inject env vars into the agent container (first container).
	podSpec := &job.Spec.Template.Spec
	if len(podSpec.Containers) > 0 {
		podSpec.Containers[0].Env = append(podSpec.Containers[0].Env,
			corev1.EnvVar{Name: "WORKFLOW_MEMORY_SERVER_URL", Value: sharedMemoryURL},
			corev1.EnvVar{Name: "WORKFLOW_MEMORY_ACCESS", Value: accessMode},
		)

		// Inject membrane env vars if configured.
		if pack.Spec.SharedMemory.Membrane != nil {
			personaName := ""
			if agentRun.Spec.AgentRef != "" {
				var inst sympoziumv1alpha1.Agent
				if err := r.Get(ctx, types.NamespacedName{Name: agentRun.Spec.AgentRef, Namespace: agentRun.Namespace}, &inst); err == nil {
					personaName = inst.Labels["sympozium.ai/agent-config"]
				}
			}

			// Auto-derive permeability from relationships if not explicitly set.
			membrane := pack.Spec.SharedMemory.Membrane
			if len(membrane.Permeability) == 0 && len(pack.Spec.Relationships) > 0 {
				membrane = membrane.DeepCopy()
				membrane.Permeability = derivePermeability(pack.Spec.AgentConfigs, pack.Spec.Relationships, membrane.DefaultVisibility)
			}

			membraneEnvs := resolveMembraneEnvVars(personaName, membrane, pack.Spec.Relationships)
			podSpec.Containers[0].Env = append(podSpec.Containers[0].Env, membraneEnvs...)
		}
	}

	// Add wait-for-shared-memory init container.
	readOnly := true
	noPrivEsc := false
	podSpec.InitContainers = append(podSpec.InitContainers, corev1.Container{
		Name:            "wait-for-shared-memory",
		Image:           "busybox:1.36",
		ImagePullPolicy: corev1.PullIfNotPresent,
		SecurityContext: &corev1.SecurityContext{
			ReadOnlyRootFilesystem:   &readOnly,
			AllowPrivilegeEscalation: &noPrivEsc,
			Capabilities: &corev1.Capabilities{
				Drop: []corev1.Capability{"ALL"},
			},
		},
		Command: []string{"sh", "-c",
			fmt.Sprintf("elapsed=0; until wget -q --spider --timeout=2 %s/health; do echo 'waiting for shared memory server...'; sleep 2; elapsed=$((elapsed+2)); if [ $elapsed -ge 120 ]; then echo 'ERROR: shared memory server not ready after 120s'; exit 1; fi; done", sharedMemoryURL),
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("50m"),
				corev1.ResourceMemory: resource.MustParse("32Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("64Mi"),
			},
		},
	})
}

// injectRelationshipContext serialises the ensemble's relationships and persona
// display names into env vars on the agent container so the agent-runner can
// auto-generate delegation/supervision instructions in the system prompt.
// This ensures user-created dynamic ensembles get correct routing guidance
// without requiring manual system prompt edits.
func (r *AgentRunReconciler) injectRelationshipContext(ctx context.Context, agentRun *sympoziumv1alpha1.AgentRun, job *batchv1.Job) {
	packName := agentRun.Labels["sympozium.ai/ensemble"]
	if packName == "" {
		return
	}

	var pack sympoziumv1alpha1.Ensemble
	if err := r.Get(ctx, types.NamespacedName{Name: packName, Namespace: agentRun.Namespace}, &pack); err != nil {
		return
	}
	if len(pack.Spec.Relationships) == 0 {
		return
	}

	// Resolve the persona name for this agent instance.
	personaName := ""
	if agentRun.Spec.AgentRef != "" {
		var inst sympoziumv1alpha1.Agent
		if err := r.Get(ctx, types.NamespacedName{Name: agentRun.Spec.AgentRef, Namespace: agentRun.Namespace}, &inst); err == nil {
			personaName = inst.Labels["sympozium.ai/agent-config"]
		}
	}
	if personaName == "" {
		return
	}

	// Build a map of persona name → display name for human-readable context.
	displayNames := make(map[string]string, len(pack.Spec.AgentConfigs))
	for _, ac := range pack.Spec.AgentConfigs {
		if ac.DisplayName != "" {
			displayNames[ac.Name] = ac.DisplayName
		}
	}

	// Filter relationships relevant to this persona (as source).
	type relJSON struct {
		Target      string `json:"target"`
		DisplayName string `json:"displayName,omitempty"`
		Type        string `json:"type"`
		Condition   string `json:"condition,omitempty"`
	}
	var rels []relJSON
	for _, rel := range pack.Spec.Relationships {
		if rel.Source != personaName {
			continue
		}
		rels = append(rels, relJSON{
			Target:      rel.Target,
			DisplayName: displayNames[rel.Target],
			Type:        rel.Type,
			Condition:   rel.Condition,
		})
	}
	if len(rels) == 0 {
		return
	}

	data, err := json.Marshal(rels)
	if err != nil {
		return
	}

	podSpec := &job.Spec.Template.Spec
	if len(podSpec.Containers) > 0 {
		podSpec.Containers[0].Env = append(podSpec.Containers[0].Env,
			corev1.EnvVar{Name: "PERSONA_NAME", Value: personaName},
			corev1.EnvVar{Name: "ENSEMBLE_RELATIONSHIPS", Value: string(data)},
		)
	}
}

// derivePermeability auto-generates permeability rules from the ensemble's
// relationship graph when the membrane is enabled but no explicit permeability
// rules are configured. This gives users sensible defaults without manual config.
//
// Heuristics:
//   - Delegation sources → "trusted" (they produce findings for trusted peers)
//   - Supervision targets → "public" (supervisors need full visibility)
//   - Terminal sequential targets (not source of any edge) → "private"
//   - Everyone else → ensemble default visibility
func derivePermeability(agentConfigs []sympoziumv1alpha1.AgentConfigSpec, relationships []sympoziumv1alpha1.AgentConfigRelationship, defaultVis string) []sympoziumv1alpha1.PermeabilityRule {
	if defaultVis == "" {
		defaultVis = "public"
	}

	// Build role sets from relationships.
	delegationSources := map[string]bool{}
	supervisionTargets := map[string]bool{}
	isSource := map[string]bool{}
	for _, rel := range relationships {
		isSource[rel.Source] = true
		if rel.Type == "delegation" {
			delegationSources[rel.Source] = true
		}
		if rel.Type == "supervision" {
			supervisionTargets[rel.Target] = true
		}
	}

	var rules []sympoziumv1alpha1.PermeabilityRule
	for _, ac := range agentConfigs {
		vis := defaultVis
		if delegationSources[ac.Name] {
			vis = "trusted"
		} else if supervisionTargets[ac.Name] {
			vis = "public"
		} else if !isSource[ac.Name] && len(relationships) > 0 {
			// Terminal node (only a target, never a source) → private
			vis = "private"
		}
		rules = append(rules, sympoziumv1alpha1.PermeabilityRule{
			AgentConfig:       ac.Name,
			DefaultVisibility: vis,
		})
	}
	return rules
}

// resolveMembraneEnvVars computes the membrane environment variables for
// a given agent config within an ensemble.
func resolveMembraneEnvVars(personaName string, membrane *sympoziumv1alpha1.MembraneSpec, relationships []sympoziumv1alpha1.AgentConfigRelationship) []corev1.EnvVar {
	if membrane == nil {
		return nil
	}

	// Resolve default visibility for this persona.
	visibility := membrane.DefaultVisibility
	if visibility == "" {
		visibility = "public"
	}
	var acceptTags []string
	var exposeTags []string
	for _, rule := range membrane.Permeability {
		if rule.AgentConfig == personaName {
			if rule.DefaultVisibility != "" {
				visibility = rule.DefaultVisibility
			}
			acceptTags = rule.AcceptTags
			exposeTags = rule.ExposeTags
			break
		}
	}

	// Resolve trust peers.
	trustPeers := resolveTrustPeers(personaName, membrane.TrustGroups, relationships)

	// Time decay TTL.
	maxAge := ""
	if membrane.TimeDecay != nil && membrane.TimeDecay.TTL != "" {
		maxAge = membrane.TimeDecay.TTL
	}

	envs := []corev1.EnvVar{
		{Name: "WORKFLOW_MEMBRANE_VISIBILITY", Value: visibility},
		{Name: "WORKFLOW_MEMBRANE_TRUST_PEERS", Value: strings.Join(trustPeers, ",")},
		{Name: "WORKFLOW_MEMBRANE_ACCEPT_TAGS", Value: strings.Join(acceptTags, ",")},
		{Name: "WORKFLOW_MEMBRANE_EXPOSE_TAGS", Value: strings.Join(exposeTags, ",")},
		{Name: "WORKFLOW_MEMBRANE_MAX_AGE", Value: maxAge},
	}

	// Propagate per-run token budget if configured.
	if membrane.TokenBudget != nil {
		if membrane.TokenBudget.MaxTokensPerRun > 0 {
			envs = append(envs, corev1.EnvVar{
				Name:  "WORKFLOW_MEMBRANE_MAX_TOKENS_PER_RUN",
				Value: fmt.Sprintf("%d", membrane.TokenBudget.MaxTokensPerRun),
			})
		}
		if membrane.TokenBudget.Action != "" {
			envs = append(envs, corev1.EnvVar{
				Name:  "WORKFLOW_MEMBRANE_TOKEN_BUDGET_ACTION",
				Value: membrane.TokenBudget.Action,
			})
		}
	}

	return envs
}

// checkTokenBudget verifies that the ensemble's token budget has not been
// exceeded before allowing a new AgentRun to start. Returns an error if the
// budget is exceeded and action is "halt".
func (r *AgentRunReconciler) checkTokenBudget(ctx context.Context, log logr.Logger, agentRun *sympoziumv1alpha1.AgentRun) error {
	packName := agentRun.Labels["sympozium.ai/ensemble"]
	if packName == "" {
		return nil
	}

	var pack sympoziumv1alpha1.Ensemble
	if err := r.Get(ctx, types.NamespacedName{Name: packName, Namespace: agentRun.Namespace}, &pack); err != nil {
		return nil // ensemble not found, skip check
	}
	if pack.Spec.SharedMemory == nil || pack.Spec.SharedMemory.Membrane == nil ||
		pack.Spec.SharedMemory.Membrane.TokenBudget == nil {
		return nil
	}
	budget := pack.Spec.SharedMemory.Membrane.TokenBudget
	if budget.MaxTokens <= 0 {
		return nil
	}

	if budget.Action == "warn" {
		if pack.Status.TokenBudgetUsed >= budget.MaxTokens {
			log.Info("Token budget exceeded (warn mode, allowing run)",
				"used", pack.Status.TokenBudgetUsed, "max", budget.MaxTokens)
		}
		return nil
	}

	if pack.Status.TokenBudgetUsed >= budget.MaxTokens {
		return fmt.Errorf("ensemble %q has used %d tokens (limit: %d)", packName, pack.Status.TokenBudgetUsed, budget.MaxTokens)
	}
	return nil
}

// updateTokenBudget aggregates token usage from a completed AgentRun into
// the ensemble's TokenBudgetUsed status field.
func (r *AgentRunReconciler) updateTokenBudget(ctx context.Context, log logr.Logger, agentRun *sympoziumv1alpha1.AgentRun) error {
	packName := agentRun.Labels["sympozium.ai/ensemble"]
	if packName == "" {
		return nil
	}
	if agentRun.Status.TokenUsage == nil || agentRun.Status.TokenUsage.TotalTokens == 0 {
		return nil
	}

	var pack sympoziumv1alpha1.Ensemble
	if err := r.Get(ctx, types.NamespacedName{Name: packName, Namespace: agentRun.Namespace}, &pack); err != nil {
		return nil
	}
	if pack.Spec.SharedMemory == nil || pack.Spec.SharedMemory.Membrane == nil ||
		pack.Spec.SharedMemory.Membrane.TokenBudget == nil {
		return nil
	}

	patch := client.MergeFrom(pack.DeepCopy())
	pack.Status.TokenBudgetUsed += int64(agentRun.Status.TokenUsage.TotalTokens)
	log.Info("Updated ensemble token budget",
		"ensemble", packName,
		"run_tokens", agentRun.Status.TokenUsage.TotalTokens,
		"total_used", pack.Status.TokenBudgetUsed,
		"max", pack.Spec.SharedMemory.Membrane.TokenBudget.MaxTokens)
	return r.Status().Patch(ctx, &pack, patch)
}

// resolveTrustPeers computes the list of agent configs that a given persona
// should trust, based on explicit TrustGroups and implicit trust from
// delegation/supervision relationships.
func resolveTrustPeers(agentConfig string, trustGroups []sympoziumv1alpha1.TrustGroup, relationships []sympoziumv1alpha1.AgentConfigRelationship) []string {
	peers := map[string]bool{}

	// From explicit trust groups.
	for _, group := range trustGroups {
		inGroup := false
		for _, ac := range group.AgentConfigs {
			if ac == agentConfig {
				inGroup = true
				break
			}
		}
		if inGroup {
			for _, ac := range group.AgentConfigs {
				if ac != agentConfig {
					peers[ac] = true
				}
			}
		}
	}

	// From relationships: delegation and supervision edges imply mutual trust.
	for _, rel := range relationships {
		if rel.Type != "delegation" && rel.Type != "supervision" {
			continue
		}
		if rel.Source == agentConfig {
			peers[rel.Target] = true
		}
		if rel.Target == agentConfig {
			peers[rel.Source] = true
		}
	}

	result := make([]string, 0, len(peers))
	for p := range peers {
		result = append(result, p)
	}
	sort.Strings(result)
	return result
}

// buildVolumes constructs the volume list for an agent pod.
func (r *AgentRunReconciler) buildVolumes(agentRun *sympoziumv1alpha1.AgentRun, memoryEnabled bool, sidecars []resolvedSidecar, mcpServers []sympoziumv1alpha1.MCPServerRef) []corev1.Volume {
	workspaceSizeLimit := resource.MustParse("1Gi")
	ipcSizeLimit := resource.MustParse("64Mi")
	tmpSizeLimit := resource.MustParse("256Mi")
	memoryMedium := corev1.StorageMediumMemory

	// Use a PVC for /workspace when postRun lifecycle hooks are defined,
	// so the workspace can be shared between the main Job and the postRun Job.
	var workspaceVolume corev1.Volume
	if agentRun.Spec.Lifecycle != nil && len(agentRun.Spec.Lifecycle.PostRun) > 0 {
		workspaceVolume = corev1.Volume{
			Name: "workspace",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: fmt.Sprintf("%s-workspace", agentRun.Name),
				},
			},
		}
	} else {
		workspaceVolume = corev1.Volume{
			Name: "workspace",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{
					SizeLimit: &workspaceSizeLimit,
				},
			},
		}
	}

	volumes := []corev1.Volume{
		workspaceVolume,
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
					Optional: boolPtr(true),
				},
			})
		}
		if skill.ConfigMapRef != "" {
			sources = append(sources, corev1.VolumeProjection{
				ConfigMap: &corev1.ConfigMapProjection{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: skill.ConfigMapRef,
					},
					Optional: boolPtr(true),
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

	// Add memory ConfigMap volume if legacy memory is enabled.
	if memoryEnabled {
		cmName := fmt.Sprintf("%s-memory", agentRun.Spec.AgentRef)
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

	// Note: memory PVC is mounted on the standalone memory Deployment, not agent pods.

	// Add MCP config volume if MCP servers are configured.
	if len(mcpServers) > 0 {
		cmName := fmt.Sprintf("%s-mcp-servers", agentRun.Name)
		volumes = append(volumes, corev1.Volume{
			Name: "mcp-config",
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

	// Append user-defined volumes from the AgentRun spec (e.g. Vault CSI,
	// PVCs, projected secrets). Skip any entry whose name collides with
	// a Sympozium-reserved volume to avoid breaking core mounts.
	for _, v := range agentRun.Spec.Volumes {
		if isReservedVolumeName(v.Name) {
			slog.Warn("dropping user-supplied volume: name collides with a Sympozium-reserved volume",
				"agentrun", agentRun.Name,
				"namespace", agentRun.Namespace,
				"volume", v.Name,
				"source", "AgentRun.spec.volumes",
				"reservedNames", reservedVolumeNamesList(),
			)
			continue
		}
		volumes = append(volumes, v)
	}

	// Append pod-level volumes contributed by SkillPack sidecars. Track
	// names already used (reserved + agent-run + sidecar-contributed)
	// to avoid duplicate volume entries when multiple sidecars declare
	// the same volume name.
	//
	// Collision policy:
	//   - Reserved name → drop, warn (Sympozium owns that name).
	//   - Same name, structurally-equal VolumeSource → drop the duplicate
	//     silently (no-op; the existing volume already provides it).
	//   - Same name, different VolumeSource → drop, send warning. The
	//     admission webhook is expected to reject this case before it
	//     reaches the controller; the warn here is a safety net.
	seen := make(map[string]corev1.Volume, len(volumes))
	for _, v := range volumes {
		seen[v.Name] = v
	}
	for _, sc := range sidecars {
		for _, v := range sc.sidecar.Volumes {
			if isReservedVolumeName(v.Name) {
				slog.Warn("dropping skill sidecar volume: name collides with a Sympozium-reserved volume",
					"agentrun", agentRun.Name,
					"namespace", agentRun.Namespace,
					"volume", v.Name,
					"skillpack", sc.skillPackName,
					"source", "SkillPack.spec.sidecar.volumes",
					"reservedNames", reservedVolumeNamesList(),
				)
				continue
			}
			if existing, dup := seen[v.Name]; dup {
				if apiequality.Semantic.DeepEqual(existing.VolumeSource, v.VolumeSource) {
					// Identical declaration — silently no-op.
					continue
				}
				slog.Warn("dropping skill sidecar volume: another declaration with the same name but a different VolumeSource is already on this pod",
					"agentrun", agentRun.Name,
					"namespace", agentRun.Namespace,
					"volume", v.Name,
					"skillpack", sc.skillPackName,
					"hint", "another SkillPack, the AgentRun, or core Sympozium already contributes this volume name with a different source; rename to avoid the collision",
				)
				continue
			}
			seen[v.Name] = v
			volumes = append(volumes, v)
		}
	}

	return volumes
}

// reservedVolumeNames are the volume names that Sympozium manages internally
// on every agent pod. User-supplied volumes/mounts with these names are
// silently skipped to prevent accidental clobbering of core functionality.
var reservedVolumeNames = map[string]struct{}{
	"workspace":  {},
	"ipc":        {},
	"skills":     {},
	"tmp":        {},
	"memory":     {},
	"mcp-config": {},
}

func isReservedVolumeName(name string) bool {
	_, ok := reservedVolumeNames[name]
	return ok
}

// reservedVolumeNamesList returns a deterministic, sorted slice of reserved
// volume names for use in log messages.
func reservedVolumeNamesList() []string {
	names := make([]string, 0, len(reservedVolumeNames))
	for n := range reservedVolumeNames {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
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

// agentRunHasMemorySkill returns true if the AgentRun references the "memory" SkillPack.
func agentRunHasMemorySkill(agentRun *sympoziumv1alpha1.AgentRun) bool {
	for _, skill := range agentRun.Spec.Skills {
		if skill.SkillPackRef == "memory" {
			return true
		}
	}
	return false
}

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

// succeedRun marks an AgentRun as succeeded and stores the result, using a safe retry for status updates.
func (r *AgentRunReconciler) succeedRun(ctx context.Context, agentRun *sympoziumv1alpha1.AgentRun, result string, usage *sympoziumv1alpha1.TokenUsage) (ctrl.Result, error) {
	now := metav1.Now()

	err := r.updateStatusWithRetry(ctx, agentRun, func(ar *sympoziumv1alpha1.AgentRun) {
		ar.Status.Phase = sympoziumv1alpha1.AgentRunPhaseSucceeded
		ar.Status.CompletedAt = &now
		ar.Status.Result = result
		ar.Status.TokenUsage = usage
	})
	if err != nil {
		return ctrl.Result{}, err
	}

	// Record run metrics.
	runAttrs := metric.WithAttributes(
		attribute.String("sympozium.agent.status", "succeeded"),
		attribute.String("sympozium.instance", agentRun.Spec.AgentRef),
	)
	agentRunsTotal.Add(ctx, 1, runAttrs)
	if usage != nil && usage.DurationMs > 0 {
		agentDurationHist.Record(ctx, float64(usage.DurationMs), runAttrs)
	}

	// Logging
	logAttrs := []any{
		"agent_run", agentRun.Name,
		"instance", agentRun.Spec.AgentRef,
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

	return ctrl.Result{}, nil
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

	tailLines := int64(500)
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
	cmName := fmt.Sprintf("%s-memory", agentRun.Spec.AgentRef)
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

// failRun marks an AgentRun as failed
func (r *AgentRunReconciler) failRun(ctx context.Context, agentRun *sympoziumv1alpha1.AgentRun, reason string) error {
	now := metav1.Now()

	err := r.updateStatusWithRetry(ctx, agentRun, func(ar *sympoziumv1alpha1.AgentRun) {
		ar.Status.Phase = sympoziumv1alpha1.AgentRunPhaseFailed
		ar.Status.CompletedAt = &now
		ar.Status.Error = reason
	})
	if err != nil {
		return err
	}

	// Record failure metrics
	failAttrs := metric.WithAttributes(
		attribute.String("sympozium.agent.status", "failed"),
		attribute.String("sympozium.instance", agentRun.Spec.AgentRef),
	)
	agentRunsTotal.Add(ctx, 1, failAttrs)
	controllerErrors.Add(ctx, 1, metric.WithAttributes(
		attribute.String("error.type", "agent_run_failed"),
		attribute.String("sympozium.instance", agentRun.Spec.AgentRef),
	))

	// Logging
	slog.ErrorContext(ctx, "agent.run.failed",
		"agent_run", agentRun.Name,
		"instance", agentRun.Spec.AgentRef,
		"error", reason,
	)

	// Publish failure event so web proxy / channel router can unblock.
	if r.EventBus != nil {
		metadata := map[string]string{
			"agentRunID":   agentRun.Name,
			"instanceName": agentRun.Spec.AgentRef,
		}
		data := map[string]string{"error": reason}
		event, err := eventbus.NewEvent(eventbus.TopicAgentRunFailed, metadata, data)
		if err == nil {
			if pubErr := r.EventBus.Publish(ctx, eventbus.TopicAgentRunFailed, event); pubErr != nil {
				slog.ErrorContext(ctx, "failed to publish agent.run.failed event",
					"agent_run", agentRun.Name, "error", pubErr)
			}
		}
	}

	return nil
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

// ensureLifecycleRBAC creates namespace-scoped Role and RoleBinding for lifecycle
// hook containers. This grants the "sympozium-agent" ServiceAccount the permissions
// specified in lifecycle.rbac, so hook containers can interact with Kubernetes
// resources (e.g., create/delete ConfigMaps).
func (r *AgentRunReconciler) ensureLifecycleRBAC(ctx context.Context, log logr.Logger, agentRun *sympoziumv1alpha1.AgentRun) error {
	if agentRun.Spec.Lifecycle == nil || len(agentRun.Spec.Lifecycle.RBAC) == 0 {
		return nil
	}

	roleName := fmt.Sprintf("sympozium-lifecycle-%s", agentRun.Name)
	var rules []rbacv1.PolicyRule
	for _, rule := range agentRun.Spec.Lifecycle.RBAC {
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
				"sympozium.ai/component":  "lifecycle",
				"sympozium.ai/managed-by": "sympozium",
			},
		},
		Rules: rules,
	}
	if err := controllerutil.SetControllerReference(agentRun, role, r.Scheme); err != nil {
		log.Error(err, "Failed to set owner on lifecycle Role")
	}
	if err := r.Create(ctx, role); err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("creating lifecycle Role %s: %w", roleName, err)
	}

	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      roleName,
			Namespace: agentRun.Namespace,
			Labels: map[string]string{
				"sympozium.ai/agent-run":  agentRun.Name,
				"sympozium.ai/component":  "lifecycle",
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
		log.Error(err, "Failed to set owner on lifecycle RoleBinding")
	}
	if err := r.Create(ctx, rb); err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("creating lifecycle RoleBinding %s: %w", roleName, err)
	}

	log.Info("Created lifecycle RBAC", "role", roleName)
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

// ensureMCPConfigMap creates or updates the ConfigMap with MCP server
// configuration for the mcp-bridge sidecar.
func (r *AgentRunReconciler) ensureMCPConfigMap(ctx context.Context, agentRun *sympoziumv1alpha1.AgentRun, mcpServers []sympoziumv1alpha1.MCPServerRef) error {
	// Scope ConfigMap to the AgentRun so each run gets its own config
	// and cleanup is handled by garbage collection.
	cmName := fmt.Sprintf("%s-mcp-servers", agentRun.Name)

	yamlContent, err := buildMCPServersYAML(mcpServers)
	if err != nil {
		return fmt.Errorf("building MCP servers YAML: %w", err)
	}

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cmName,
			Namespace: agentRun.Namespace,
			Labels: map[string]string{
				"sympozium.ai/agent-run": agentRun.Name,
				"sympozium.ai/instance":  agentRun.Spec.AgentRef,
				"sympozium.ai/component": "mcp-config",
			},
		},
		Data: map[string]string{
			"mcp-servers.yaml": yamlContent,
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

// mcpServerYAML is the YAML-safe representation of an MCP server config.
type mcpServerYAML struct {
	Name        string            `yaml:"name"`
	URL         string            `yaml:"url"`
	ToolsPrefix string            `yaml:"toolsPrefix"`
	Timeout     int               `yaml:"timeout"`
	Auth        *mcpAuthYAML      `yaml:"auth,omitempty"`
	Headers     map[string]string `yaml:"headers,omitempty"`
	ToolsAllow  []string          `yaml:"toolsAllow,omitempty"`
	ToolsDeny   []string          `yaml:"toolsDeny,omitempty"`
}

type mcpAuthYAML struct {
	Type     string `yaml:"type"`
	TokenEnv string `yaml:"tokenEnv"`
}

type mcpServersConfigYAML struct {
	Servers []mcpServerYAML `yaml:"servers"`
}

// buildMCPServersYAML generates the YAML config for the mcp-bridge sidecar
// using a proper YAML serializer to avoid injection attacks.
func buildMCPServersYAML(mcpServers []sympoziumv1alpha1.MCPServerRef) (string, error) {
	cfg := mcpServersConfigYAML{
		Servers: make([]mcpServerYAML, 0, len(mcpServers)),
	}

	for _, srv := range mcpServers {
		timeout := srv.Timeout
		if timeout <= 0 {
			timeout = 30
		}

		entry := mcpServerYAML{
			Name:        srv.Name,
			URL:         srv.URL,
			ToolsPrefix: srv.ToolsPrefix,
			Timeout:     timeout,
			Headers:     srv.Headers,
			ToolsAllow:  srv.ToolsAllow,
			ToolsDeny:   srv.ToolsDeny,
		}

		if srv.AuthSecret != "" {
			envName := fmt.Sprintf("MCP_AUTH_%s", strings.ToUpper(strings.ReplaceAll(srv.Name, "-", "_")))
			entry.Auth = &mcpAuthYAML{
				Type:     "bearer",
				TokenEnv: envName,
			}
		}

		cfg.Servers = append(cfg.Servers, entry)
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("marshalling MCP servers config: %w", err)
	}
	return string(data), nil
}

// resolveMCPServerURLs looks up MCPServer CRs for any server ref that has no URL set.
// It checks the agent's namespace first, then "sympozium-system" as a fallback.
func (r *AgentRunReconciler) resolveMCPServerURLs(
	ctx context.Context,
	namespace string,
	servers []sympoziumv1alpha1.MCPServerRef,
) []sympoziumv1alpha1.MCPServerRef {
	resolved := make([]sympoziumv1alpha1.MCPServerRef, 0, len(servers))
	for _, srv := range servers {
		if srv.URL != "" {
			// Inline URL takes precedence — no resolution needed.
			resolved = append(resolved, srv)
			continue
		}

		// Try to find MCPServer CR by name.
		mcpServer := &sympoziumv1alpha1.MCPServer{}
		found := false

		// Check agent namespace first.
		if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: srv.Name}, mcpServer); err == nil {
			found = true
		}

		// Fall back to sympozium-system namespace.
		if !found {
			if err := r.Get(ctx, client.ObjectKey{Namespace: "sympozium-system", Name: srv.Name}, mcpServer); err == nil {
				found = true
			}
		}

		if !found {
			r.Log.Info("MCPServer CR not found, skipping server (no URL)", "name", srv.Name)
			continue
		}

		if !mcpServer.Status.Ready {
			r.Log.Info("MCPServer CR not ready, skipping server", "name", srv.Name)
			continue
		}

		if mcpServer.Status.URL == "" {
			r.Log.Info("MCPServer CR has no resolved URL, skipping server", "name", srv.Name)
			continue
		}

		// Use the resolved URL from MCPServer status.
		srv.URL = mcpServer.Status.URL

		// Inherit toolsPrefix from MCPServer spec if not set on the ref.
		if srv.ToolsPrefix == "" && mcpServer.Spec.ToolsPrefix != "" {
			srv.ToolsPrefix = mcpServer.Spec.ToolsPrefix
		}

		// Inherit timeout from MCPServer spec if not set on the ref.
		if srv.Timeout == 0 && mcpServer.Spec.Timeout > 0 {
			srv.Timeout = mcpServer.Spec.Timeout
		}

		// Inherit toolsAllow/toolsDeny from MCPServer spec if not set on the ref.
		if len(srv.ToolsAllow) == 0 && len(mcpServer.Spec.ToolsAllow) > 0 {
			srv.ToolsAllow = mcpServer.Spec.ToolsAllow
		}
		if len(srv.ToolsDeny) == 0 && len(mcpServer.Spec.ToolsDeny) > 0 {
			srv.ToolsDeny = mcpServer.Spec.ToolsDeny
		}

		r.Log.Info("Resolved MCPServer URL", "name", srv.Name, "url", srv.URL)
		resolved = append(resolved, srv)
	}
	return resolved
}

// startPostRun transitions an AgentRun to the PostRunning phase and creates
// a follow-up Job that executes the postRun lifecycle hook containers.
func (r *AgentRunReconciler) startPostRun(
	ctx context.Context, log logr.Logger,
	agentRun *sympoziumv1alpha1.AgentRun,
	exitCode int32, resultOrError string,
	usage *sympoziumv1alpha1.TokenUsage,
) (ctrl.Result, error) {
	log.Info("Starting postRun lifecycle hooks", "exitCode", exitCode)

	// Store the agent result/error and exit code in status before building the postRun Job.
	err := r.updateStatusWithRetry(ctx, agentRun, func(ar *sympoziumv1alpha1.AgentRun) {
		ar.Status.ExitCode = &exitCode
		if exitCode == 0 {
			ar.Status.Result = resultOrError
		} else {
			ar.Status.Error = resultOrError
		}
		ar.Status.TokenUsage = usage
	})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("updating status before postRun: %w", err)
	}

	postRunJob := r.buildPostRunJob(agentRun, exitCode, resultOrError)
	if err := controllerutil.SetControllerReference(agentRun, postRunJob, r.Scheme); err != nil {
		return ctrl.Result{}, fmt.Errorf("setting owner reference on postRun Job: %w", err)
	}

	if err := r.Create(ctx, postRunJob); err != nil {
		if errors.IsAlreadyExists(err) {
			log.Info("PostRun Job already exists")
		} else {
			return ctrl.Result{}, fmt.Errorf("creating postRun Job: %w", err)
		}
	}

	err = r.updateStatusWithRetry(ctx, agentRun, func(ar *sympoziumv1alpha1.AgentRun) {
		ar.Status.Phase = sympoziumv1alpha1.AgentRunPhasePostRunning
		ar.Status.PostRunJobName = postRunJob.Name
	})
	if err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

// buildPostRunJob constructs a Job that runs the postRun lifecycle hook containers.
// Each hook runs as a sequential init container, followed by a no-op final container.
func (r *AgentRunReconciler) buildPostRunJob(
	agentRun *sympoziumv1alpha1.AgentRun,
	exitCode int32, resultOrError string,
) *batchv1.Job {
	labels := map[string]string{
		"sympozium.ai/agent-run":       agentRun.Name,
		"sympozium.ai/instance":        agentRun.Spec.AgentRef,
		"sympozium.ai/component":       "post-run",
		"app.kubernetes.io/part-of":    "sympozium",
		"app.kubernetes.io/managed-by": "sympozium-controller",
	}

	jobName := fmt.Sprintf("%s-postrun", agentRun.Name)
	// Kubernetes name length limit is 63 chars.
	if len(jobName) > 63 {
		jobName = jobName[:63]
	}

	ttl := int32(300)
	deadline := int64(600) // 10 min default for postRun
	backoffLimit := int32(0)

	readOnly := true
	noPrivEsc := false
	runAsNonRoot := true
	runAsUser := int64(1000)
	fsGroup := int64(1000)

	// Truncate result for env var safety (env vars have a practical limit).
	truncatedResult := resultOrError
	if len(truncatedResult) > 32*1024 {
		truncatedResult = truncatedResult[:32*1024]
	}

	// Build base env vars available to all postRun containers.
	baseEnv := []corev1.EnvVar{
		{Name: "AGENT_RUN_ID", Value: agentRun.Name},
		{Name: "INSTANCE_NAME", Value: agentRun.Spec.AgentRef},
		{Name: "AGENT_NAMESPACE", Value: agentRun.Namespace},
		{Name: "AGENT_EXIT_CODE", Value: fmt.Sprintf("%d", exitCode)},
		{Name: "AGENT_RESULT", Value: truncatedResult},
	}
	// Forward custom env vars from spec.env.
	envKeys := make([]string, 0, len(agentRun.Spec.Env))
	for k := range agentRun.Spec.Env {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	for _, k := range envKeys {
		baseEnv = append(baseEnv, corev1.EnvVar{Name: k, Value: agentRun.Spec.Env[k]})
	}

	// Each postRun hook becomes a sequential init container. Gate hooks are
	// placed first so they always execute even if a later best-effort hook
	// fails, and so the verdict is available as early as possible.
	ordered := make([]sympoziumv1alpha1.LifecycleHookContainer, 0, len(agentRun.Spec.Lifecycle.PostRun))
	for _, hook := range agentRun.Spec.Lifecycle.PostRun {
		if hook.Gate {
			ordered = append([]sympoziumv1alpha1.LifecycleHookContainer{hook}, ordered...)
		} else {
			ordered = append(ordered, hook)
		}
	}

	var initContainers []corev1.Container
	for _, hook := range ordered {
		hookEnv := make([]corev1.EnvVar, len(baseEnv))
		copy(hookEnv, baseEnv)
		for _, e := range hook.Env {
			hookEnv = append(hookEnv, corev1.EnvVar{Name: e.Name, Value: e.Value})
		}
		if hook.Gate {
			hookEnv = append(hookEnv, corev1.EnvVar{Name: "GATE_MODE", Value: "true"})
		}

		hookContainer := corev1.Container{
			Name:            fmt.Sprintf("post-%s", hook.Name),
			Image:           hook.Image,
			ImagePullPolicy: corev1.PullIfNotPresent,
			SecurityContext: &corev1.SecurityContext{
				ReadOnlyRootFilesystem:   &readOnly,
				AllowPrivilegeEscalation: &noPrivEsc,
				Capabilities: &corev1.Capabilities{
					Drop: []corev1.Capability{"ALL"},
				},
			},
			VolumeMounts: []corev1.VolumeMount{
				{Name: "workspace", MountPath: "/workspace"},
				{Name: "tmp", MountPath: "/tmp"},
			},
			Env: hookEnv,
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("100m"),
					corev1.ResourceMemory: resource.MustParse("128Mi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("500m"),
					corev1.ResourceMemory: resource.MustParse("512Mi"),
				},
			},
		}
		if len(hook.Command) > 0 {
			hookContainer.Command = hook.Command
		}
		if len(hook.Args) > 0 {
			hookContainer.Args = hook.Args
		}
		initContainers = append(initContainers, hookContainer)
	}

	// Volumes: workspace PVC + tmp EmptyDir.
	tmpSizeLimit := resource.MustParse("256Mi")
	volumes := []corev1.Volume{
		{
			Name: "workspace",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: fmt.Sprintf("%s-workspace", agentRun.Name),
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

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
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
					ImagePullSecrets:   agentRun.Spec.ImagePullSecrets,
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot: &runAsNonRoot,
						RunAsUser:    &runAsUser,
						FSGroup:      &fsGroup,
					},
					InitContainers: initContainers,
					Containers: []corev1.Container{
						{
							Name:            "done",
							Image:           "busybox:1.36",
							ImagePullPolicy: corev1.PullIfNotPresent,
							Command:         []string{"true"},
							SecurityContext: &corev1.SecurityContext{
								ReadOnlyRootFilesystem:   &readOnly,
								AllowPrivilegeEscalation: &noPrivEsc,
								Capabilities: &corev1.Capabilities{
									Drop: []corev1.Capability{"ALL"},
								},
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("10m"),
									corev1.ResourceMemory: resource.MustParse("8Mi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("50m"),
									corev1.ResourceMemory: resource.MustParse("16Mi"),
								},
							},
						},
					},
					Volumes: volumes,
				},
			},
		},
	}
}

// reconcilePostRunning monitors the postRun Job and transitions to the final phase.
func (r *AgentRunReconciler) reconcilePostRunning(ctx context.Context, log logr.Logger, agentRun *sympoziumv1alpha1.AgentRun) (ctrl.Result, error) {
	if agentRun.Status.PostRunJobName == "" {
		log.Info("PostRunning phase but no postRun Job name; failing")
		return ctrl.Result{}, r.failRun(ctx, agentRun, "postRun Job name missing")
	}

	var job batchv1.Job
	if err := r.Get(ctx, client.ObjectKey{Namespace: agentRun.Namespace, Name: agentRun.Status.PostRunJobName}, &job); err != nil {
		if errors.IsNotFound(err) {
			log.Info("PostRun Job not found, requeueing")
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		return ctrl.Result{}, err
	}

	// Determine the original agent outcome from status.
	agentSucceeded := agentRun.Status.ExitCode != nil && *agentRun.Status.ExitCode == 0

	gated := hasResponseGateHook(agentRun)

	if job.Status.Succeeded > 0 {
		log.Info("PostRun hooks completed successfully")
		if gated {
			return r.resolveGate(ctx, log, agentRun, agentSucceeded, false)
		}
		if agentSucceeded {
			return r.succeedRun(ctx, agentRun, agentRun.Status.Result, agentRun.Status.TokenUsage)
		}
		return ctrl.Result{}, r.failRun(ctx, agentRun, agentRun.Status.Error)
	}

	if job.Status.Failed > 0 {
		log.Info("PostRun hooks failed (best-effort, not changing agent outcome)")
		// Record the postRun failure as a Condition but don't change the agent's outcome.
		_ = r.updateStatusWithRetry(ctx, agentRun, func(ar *sympoziumv1alpha1.AgentRun) {
			now := metav1.Now()
			ar.Status.Conditions = append(ar.Status.Conditions, metav1.Condition{
				Type:               "PostRunFailed",
				Status:             metav1.ConditionTrue,
				LastTransitionTime: now,
				Reason:             "PostRunJobFailed",
				Message:            "One or more postRun lifecycle hooks failed",
			})
		})
		if gated {
			return r.resolveGate(ctx, log, agentRun, agentSucceeded, true)
		}
		if agentSucceeded {
			return r.succeedRun(ctx, agentRun, agentRun.Status.Result, agentRun.Status.TokenUsage)
		}
		return ctrl.Result{}, r.failRun(ctx, agentRun, agentRun.Status.Error)
	}

	// For gated runs, check if a verdict annotation was set while the Job is
	// still running (e.g. manual approval via API/UI). If so, kill the Job
	// and resolve the gate immediately.
	if gated {
		if err := r.Get(ctx, client.ObjectKeyFromObject(agentRun), agentRun); err == nil {
			if _, hasVerdict := agentRun.Annotations["sympozium.ai/gate-verdict"]; hasVerdict {
				log.Info("Gate verdict annotation found while postRun Job still running; terminating Job")
				_ = r.Delete(ctx, &job, client.PropagationPolicy(metav1.DeletePropagationBackground))
				return r.resolveGate(ctx, log, agentRun, agentSucceeded, false)
			}
		}
	}

	// PostRun Job still running -- check timeout.
	if agentRun.Status.StartedAt != nil {
		elapsed := time.Since(agentRun.Status.StartedAt.Time)
		// PostRun gets 10 minutes by default.
		postRunTimeout := 10 * time.Minute
		if elapsed > postRunTimeout {
			log.Info("PostRun Job timed out", "elapsed", elapsed)
			_ = r.Delete(ctx, &job, client.PropagationPolicy(metav1.DeletePropagationForeground))
			if gated {
				return r.resolveGate(ctx, log, agentRun, agentSucceeded, true)
			}
			if agentSucceeded {
				return r.succeedRun(ctx, agentRun, agentRun.Status.Result, agentRun.Status.TokenUsage)
			}
			return ctrl.Result{}, r.failRun(ctx, agentRun, agentRun.Status.Error)
		}
	}

	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

// resolveGate reads the gate verdict annotation, applies the verdict to the
// agent result, publishes the (possibly rewritten) completion event, and
// transitions the run to its final phase.
func (r *AgentRunReconciler) resolveGate(
	ctx context.Context, log logr.Logger,
	agentRun *sympoziumv1alpha1.AgentRun,
	agentSucceeded bool, hookFailed bool,
) (ctrl.Result, error) {
	// Re-fetch to pick up any annotation the gate hook patched.
	if err := r.Get(ctx, client.ObjectKeyFromObject(agentRun), agentRun); err != nil {
		return ctrl.Result{}, fmt.Errorf("re-fetching AgentRun for gate verdict: %w", err)
	}

	verdict := parseGateVerdict(agentRun)
	finalResult := agentRun.Status.Result
	verdictLabel := ""

	gateDefault := "block"
	if agentRun.Spec.Lifecycle != nil && agentRun.Spec.Lifecycle.GateDefault != "" {
		gateDefault = agentRun.Spec.Lifecycle.GateDefault
	}

	switch {
	case verdict != nil && verdict.Action == "approve":
		verdictLabel = "approved"
		log.Info("Gate verdict: approved", "reason", verdict.Reason)
	case verdict != nil && verdict.Action == "reject":
		verdictLabel = "rejected"
		if verdict.Response != "" {
			finalResult = verdict.Response
		} else {
			finalResult = "Response blocked by policy"
		}
		log.Info("Gate verdict: rejected", "reason", verdict.Reason)
	case verdict != nil && verdict.Action == "rewrite":
		verdictLabel = "rewritten"
		finalResult = verdict.Response
		log.Info("Gate verdict: rewritten", "reason", verdict.Reason)
	default:
		// No valid verdict: hook failed, timed out, or wrote nothing.
		if hookFailed {
			verdictLabel = "error"
		} else {
			verdictLabel = "timeout"
		}
		if gateDefault == "block" {
			finalResult = "Response blocked: gate hook did not return a verdict"
			log.Info("Gate verdict missing, gateDefault=block")
		} else {
			verdictLabel = "allowed-by-default"
			log.Info("Gate verdict missing, gateDefault=allow, passing original result")
		}
	}

	// Persist gate verdict in status.
	_ = r.updateStatusWithRetry(ctx, agentRun, func(ar *sympoziumv1alpha1.AgentRun) {
		ar.Status.GateVerdict = verdictLabel
	})

	// Publish the completion event that the IPC bridge suppressed.
	r.publishGatedCompletion(ctx, agentRun, finalResult)

	if agentSucceeded {
		return r.succeedRun(ctx, agentRun, finalResult, agentRun.Status.TokenUsage)
	}
	return ctrl.Result{}, r.failRun(ctx, agentRun, agentRun.Status.Error)
}

// validateGateHooks checks that at most one PostRun hook has gate: true and
// that no PreRun hook uses it.
func validateGateHooks(agentRun *sympoziumv1alpha1.AgentRun) error {
	if agentRun.Spec.Lifecycle == nil {
		return nil
	}
	for _, hook := range agentRun.Spec.Lifecycle.PreRun {
		if hook.Gate {
			return fmt.Errorf("gate: true is only valid on postRun hooks, not preRun (hook %q)", hook.Name)
		}
	}
	gateCount := 0
	for _, hook := range agentRun.Spec.Lifecycle.PostRun {
		if hook.Gate {
			gateCount++
		}
	}
	if gateCount > 1 {
		return fmt.Errorf("at most one postRun hook may set gate: true, found %d", gateCount)
	}
	return nil
}

// hasResponseGateHook returns true when the AgentRun has a postRun lifecycle
// hook with gate: true, meaning agent output must be held until the gate resolves.
func hasResponseGateHook(agentRun *sympoziumv1alpha1.AgentRun) bool {
	if agentRun.Spec.Lifecycle == nil {
		return false
	}
	for _, hook := range agentRun.Spec.Lifecycle.PostRun {
		if hook.Gate {
			return true
		}
	}
	return false
}

// gateVerdict represents the JSON payload a gate hook writes to the
// sympozium.ai/gate-verdict annotation on the AgentRun CR.
type gateVerdict struct {
	Action   string `json:"action"`             // approve, reject, rewrite
	Response string `json:"response,omitempty"` // replacement text for reject/rewrite
	Reason   string `json:"reason,omitempty"`   // audit trail
}

// parseGateVerdict extracts the gate verdict from the AgentRun's annotations.
// Returns nil if the annotation is missing or malformed.
func parseGateVerdict(agentRun *sympoziumv1alpha1.AgentRun) *gateVerdict {
	raw, ok := agentRun.Annotations["sympozium.ai/gate-verdict"]
	if !ok || raw == "" {
		return nil
	}
	var v gateVerdict
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil
	}
	if v.Action != "approve" && v.Action != "reject" && v.Action != "rewrite" {
		return nil
	}
	return &v
}

// publishGatedCompletion publishes the TopicAgentRunCompleted event from the
// controller for gated runs. Normally the IPC bridge publishes this, but when
// gate suppression is active the controller takes over after the gate resolves.
func (r *AgentRunReconciler) publishGatedCompletion(ctx context.Context, agentRun *sympoziumv1alpha1.AgentRun, result string) {
	if r.EventBus == nil {
		return
	}
	metadata := map[string]string{
		"agentRunID":   agentRun.Name,
		"instanceName": agentRun.Spec.AgentRef,
	}
	data := map[string]string{
		"status":   "success",
		"response": result,
	}
	event, err := eventbus.NewEvent(eventbus.TopicAgentRunCompleted, metadata, data)
	if err == nil {
		if pubErr := r.EventBus.Publish(ctx, eventbus.TopicAgentRunCompleted, event); pubErr != nil {
			slog.ErrorContext(ctx, "failed to publish gated completion event",
				"agent_run", agentRun.Name, "error", pubErr)
		}
	}
}

// ensureWorkspacePVC creates a PersistentVolumeClaim for the workspace volume
// when postRun lifecycle hooks are defined. This allows the workspace to persist
// between the main agent Job and the postRun Job.
func (r *AgentRunReconciler) ensureWorkspacePVC(ctx context.Context, agentRun *sympoziumv1alpha1.AgentRun) error {
	pvcName := fmt.Sprintf("%s-workspace", agentRun.Name)
	storageSize := resource.MustParse("1Gi")
	accessMode := corev1.ReadWriteOnce

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName,
			Namespace: agentRun.Namespace,
			Labels: map[string]string{
				"sympozium.ai/agent-run": agentRun.Name,
				"sympozium.ai/component": "workspace",
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{accessMode},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: storageSize,
				},
			},
		},
	}

	if err := controllerutil.SetControllerReference(agentRun, pvc, r.Scheme); err != nil {
		return fmt.Errorf("setting owner reference on workspace PVC: %w", err)
	}

	if err := r.Create(ctx, pvc); err != nil {
		if errors.IsAlreadyExists(err) {
			return nil
		}
		return err
	}
	return nil
}

// cleanupWorkspacePVC deletes the workspace PVC created for postRun lifecycle hooks.
func (r *AgentRunReconciler) cleanupWorkspacePVC(ctx context.Context, log logr.Logger, agentRun *sympoziumv1alpha1.AgentRun) {
	pvcName := fmt.Sprintf("%s-workspace", agentRun.Name)
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName,
			Namespace: agentRun.Namespace,
		},
	}
	if err := r.Delete(ctx, pvc); err != nil && !errors.IsNotFound(err) {
		log.Error(err, "Failed to delete workspace PVC", "pvc", pvcName)
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
