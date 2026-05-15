package controller

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/meta"
)

// MCPServerReconciler reconciles MCPServer objects.
type MCPServerReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Log      logr.Logger
	ImageTag string
}

// mcpBridgeImage returns the fully-qualified mcp-bridge image reference.
func (r *MCPServerReconciler) mcpBridgeImage() string {
	registry := os.Getenv("SYMPOZIUM_IMAGE_REGISTRY")
	if registry == "" {
		registry = imageRegistry // fallback to the package-level default
	}
	tag := os.Getenv("SYMPOZIUM_IMAGE_TAG")
	if tag == "" {
		tag = r.ImageTag
	}
	if tag == "" {
		tag = "latest"
	}
	return fmt.Sprintf("%s/mcp-bridge:%s", registry, tag)
}

// +kubebuilder:rbac:groups=sympozium.ai,resources=mcpservers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=sympozium.ai,resources=mcpservers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete

func (r *MCPServerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("mcpserver", req.NamespacedName)

	var mcpServer sympoziumv1alpha1.MCPServer
	if err := r.Get(ctx, req.NamespacedName, &mcpServer); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil // deleted — OwnerReferences handle cleanup
		}
		return ctrl.Result{}, err
	}

	// External mode: URL set, no deployment needed
	if mcpServer.Spec.URL != "" {
		return r.reconcileExternal(ctx, &mcpServer, log)
	}

	if mcpServer.Spec.Deployment == nil {
		log.Info("MCPServer has no deployment spec and no URL — skipping")
		return ctrl.Result{}, nil
	}

	// Suspended: scale down existing deployment and update status
	if mcpServer.Spec.Suspended {
		return r.reconcileSuspended(ctx, &mcpServer, log)
	}

	// Ensure Deployment
	if err := r.reconcileDeployment(ctx, &mcpServer, log); err != nil {
		return ctrl.Result{}, err
	}

	// Ensure Service
	port := r.servicePort(&mcpServer)
	if err := r.reconcileService(ctx, &mcpServer, port, log); err != nil {
		return ctrl.Result{}, err
	}

	// Update status
	return r.updateStatus(ctx, &mcpServer, port, log)
}

func (r *MCPServerReconciler) reconcileSuspended(ctx context.Context, ms *sympoziumv1alpha1.MCPServer, log logr.Logger) (ctrl.Result, error) {
	log.Info("MCPServer is suspended — scaling down")

	// Scale existing deployment to zero if it exists
	var deploy appsv1.Deployment
	err := r.Get(ctx, types.NamespacedName{Name: ms.Name, Namespace: ms.Namespace}, &deploy)
	if err == nil {
		zero := int32(0)
		if deploy.Spec.Replicas == nil || *deploy.Spec.Replicas != 0 {
			deploy.Spec.Replicas = &zero
			if err := r.Update(ctx, &deploy); err != nil {
				return ctrl.Result{}, err
			}
			log.Info("Scaled deployment to zero")
		}
	} else if !errors.IsNotFound(err) {
		return ctrl.Result{}, err
	}

	ms.Status.Ready = false
	ms.Status.URL = ""
	ms.Status.ToolCount = 0
	ms.Status.Tools = nil

	meta.SetStatusCondition(&ms.Status.Conditions, metav1.Condition{
		Type:               "Suspended",
		Status:             metav1.ConditionTrue,
		Reason:             "Suspended",
		Message:            "MCP server is suspended — configure secrets and tokens, then unsuspend",
		ObservedGeneration: ms.Generation,
	})
	meta.SetStatusCondition(&ms.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             "Suspended",
		Message:            "MCP server is suspended",
		ObservedGeneration: ms.Generation,
	})

	if err := r.Status().Update(ctx, ms); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *MCPServerReconciler) reconcileExternal(ctx context.Context, ms *sympoziumv1alpha1.MCPServer, log logr.Logger) (ctrl.Result, error) {
	log.Info("External MCPServer — no deployment", "url", ms.Spec.URL)

	ms.Status.Ready = true
	ms.Status.URL = ms.Spec.URL
	ms.Status.ToolCount = 0
	meta.SetStatusCondition(&ms.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "External",
		Message:            "External MCP server URL configured",
		ObservedGeneration: ms.Generation,
	})
	if err := r.Status().Update(ctx, ms); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *MCPServerReconciler) servicePort(ms *sympoziumv1alpha1.MCPServer) int32 {
	if ms.Spec.TransportType == "stdio" {
		return 8080
	}
	if ms.Spec.Deployment != nil && ms.Spec.Deployment.Port > 0 {
		return ms.Spec.Deployment.Port
	}
	return 8080
}

func (r *MCPServerReconciler) reconcileDeployment(ctx context.Context, ms *sympoziumv1alpha1.MCPServer, log logr.Logger) error {
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ms.Name,
			Namespace: ms.Namespace,
		},
	}

	result, err := controllerutil.CreateOrUpdate(ctx, r.Client, deploy, func() error {
		if err := controllerutil.SetControllerReference(ms, deploy, r.Scheme); err != nil {
			return err
		}
		replicas := int32(1)
		if ms.Spec.Replicas != nil {
			replicas = *ms.Spec.Replicas
		}
		deploy.Spec.Replicas = &replicas

		labels := map[string]string{
			"app.kubernetes.io/name":       "mcpserver",
			"app.kubernetes.io/instance":   ms.Name,
			"app.kubernetes.io/managed-by": "sympozium",
		}
		deploy.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		deploy.Spec.Template.ObjectMeta = metav1.ObjectMeta{Labels: labels}

		if ms.Spec.TransportType == "stdio" {
			r.buildStdioPodSpec(ctx, ms, &deploy.Spec.Template.Spec)
		} else {
			r.buildHTTPPodSpec(ms, &deploy.Spec.Template.Spec)
		}
		return nil
	})
	if err != nil {
		return err
	}
	log.Info("Deployment reconciled", "result", result)
	return nil
}

func (r *MCPServerReconciler) buildStdioPodSpec(ctx context.Context, ms *sympoziumv1alpha1.MCPServer, podSpec *corev1.PodSpec) {
	dep := ms.Spec.Deployment
	bridgeImage := r.mcpBridgeImage()

	// Init container: copy adapter binary
	podSpec.InitContainers = []corev1.Container{
		{
			Name:    "inject-adapter",
			Image:   bridgeImage,
			Command: []string{"cp", "/usr/local/bin/mcp-bridge", "/adapter/mcp-bridge"},
			VolumeMounts: []corev1.VolumeMount{
				{Name: "adapter-bin", MountPath: "/adapter"},
			},
		},
	}

	// Build env vars
	env := []corev1.EnvVar{
		{Name: "MCP_STDIO_ADAPTER", Value: "true"},
		{Name: "STDIO_CMD", Value: dep.Cmd},
		{Name: "STDIO_ARGS", Value: strings.Join(dep.Args, ",")},
		{Name: "STDIO_SERVER_NAME", Value: ms.Name},
		{Name: "STDIO_PORT", Value: "8080"},
	}
	// Sort env keys for deterministic output (prevents reconcile loops).
	envKeys := make([]string, 0, len(dep.Env))
	for k := range dep.Env {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	for _, k := range envKeys {
		env = append(env, corev1.EnvVar{Name: "STDIO_ENV_" + k, Value: dep.Env[k]})
	}

	// For stdio transport, secret env vars must be prefixed with STDIO_ENV_
	// so the adapter passes them to the child process.
	// We look up secret keys and create individual EnvVar entries with secretKeyRef.
	for _, ref := range dep.SecretRefs {
		secret := &corev1.Secret{}
		if err := r.Get(ctx, client.ObjectKey{Namespace: ms.Namespace, Name: ref.Name}, secret); err != nil {
			ctrl.LoggerFrom(ctx).Error(err, "failed to look up secret for STDIO_ENV prefixing", "secret", ref.Name)
			continue
		}
		secretKeys := make([]string, 0, len(secret.Data))
		for key := range secret.Data {
			secretKeys = append(secretKeys, key)
		}
		sort.Strings(secretKeys)
		for _, key := range secretKeys {
			env = append(env, corev1.EnvVar{
				Name: "STDIO_ENV_" + key,
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: ref.Name},
						Key:                  key,
					},
				},
			})
		}
	}

	container := corev1.Container{
		Name:            "mcp-server",
		Image:           dep.Image,
		ImagePullPolicy: ResolveImagePullPolicy(dep.ImagePullPolicy),
		Command:         []string{"/adapter/mcp-bridge"},
		Args:            []string{"--stdio-adapter"},
		Env:             env,

		VolumeMounts: []corev1.VolumeMount{
			{Name: "adapter-bin", MountPath: "/adapter"},
		},
		Resources: dep.Resources,
		LivenessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{Path: "/healthz", Port: intstr.FromInt(8080)},
			},
			InitialDelaySeconds: 30,
			PeriodSeconds:       15,
			TimeoutSeconds:      5,
			FailureThreshold:    5,
		},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{Path: "/readyz", Port: intstr.FromInt(8080)},
			},
			InitialDelaySeconds: 20,
			PeriodSeconds:       10,
			TimeoutSeconds:      5,
			FailureThreshold:    5,
		},
	}

	podSpec.Containers = []corev1.Container{container}
	podSpec.Volumes = []corev1.Volume{
		{
			Name:         "adapter-bin",
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		},
	}
	podSpec.Volumes = append(podSpec.Volumes, dep.Volumes...)
	podSpec.Containers[0].VolumeMounts = append(podSpec.Containers[0].VolumeMounts, dep.VolumeMounts...)

	if dep.ServiceAccountName != "" {
		podSpec.ServiceAccountName = dep.ServiceAccountName
	}
}

func (r *MCPServerReconciler) buildHTTPPodSpec(ms *sympoziumv1alpha1.MCPServer, podSpec *corev1.PodSpec) {
	dep := ms.Spec.Deployment
	port := dep.Port
	if port == 0 {
		port = 8080
	}

	var env []corev1.EnvVar
	httpEnvKeys := make([]string, 0, len(dep.Env))
	for k := range dep.Env {
		httpEnvKeys = append(httpEnvKeys, k)
	}
	sort.Strings(httpEnvKeys)
	for _, k := range httpEnvKeys {
		env = append(env, corev1.EnvVar{Name: k, Value: dep.Env[k]})
	}
	var envFrom []corev1.EnvFromSource

	for _, ref := range dep.SecretRefs {
		envFrom = append(envFrom, corev1.EnvFromSource{
			SecretRef: &corev1.SecretEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: ref.Name},
			},
		})
	}

	container := corev1.Container{
		Name:            "mcp-server",
		Image:           dep.Image,
		ImagePullPolicy: ResolveImagePullPolicy(dep.ImagePullPolicy),
		Env:             env,
		EnvFrom:         envFrom,
		Resources:       dep.Resources,
		Ports: []corev1.ContainerPort{
			{ContainerPort: port, Protocol: corev1.ProtocolTCP},
		},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(port)},
			},
			InitialDelaySeconds: 3,
			PeriodSeconds:       5,
		},
	}

	if dep.Cmd != "" {
		container.Command = []string{dep.Cmd}
	}
	if len(dep.Args) > 0 {
		container.Args = dep.Args
	}

	container.VolumeMounts = append(container.VolumeMounts, dep.VolumeMounts...)
	podSpec.Containers = []corev1.Container{container}
	podSpec.Volumes = append(podSpec.Volumes, dep.Volumes...)

	if dep.ServiceAccountName != "" {
		podSpec.ServiceAccountName = dep.ServiceAccountName
	}
}

func (r *MCPServerReconciler) reconcileService(ctx context.Context, ms *sympoziumv1alpha1.MCPServer, port int32, log logr.Logger) error {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ms.Name,
			Namespace: ms.Namespace,
		},
	}

	result, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		if err := controllerutil.SetControllerReference(ms, svc, r.Scheme); err != nil {
			return err
		}
		svc.Spec.Selector = map[string]string{
			"app.kubernetes.io/name":     "mcpserver",
			"app.kubernetes.io/instance": ms.Name,
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

func (r *MCPServerReconciler) updateStatus(ctx context.Context, ms *sympoziumv1alpha1.MCPServer, port int32, log logr.Logger) (ctrl.Result, error) {
	// Check deployment readiness
	var deploy appsv1.Deployment
	if err := r.Get(ctx, types.NamespacedName{Name: ms.Name, Namespace: ms.Namespace}, &deploy); err != nil {
		return ctrl.Result{}, err
	}

	ready := deploy.Status.ReadyReplicas > 0
	ms.Status.Ready = ready
	ms.Status.URL = fmt.Sprintf("http://%s.%s.svc:%d", ms.Name, ms.Namespace, port)
	ms.Status.ToolCount = 0

	deployedCondition := metav1.Condition{
		Type:               "Deployed",
		Status:             metav1.ConditionTrue,
		Reason:             "DeploymentCreated",
		Message:            "Deployment and Service created",
		ObservedGeneration: ms.Generation,
	}
	meta.SetStatusCondition(&ms.Status.Conditions, deployedCondition)

	readyStatus := metav1.ConditionFalse
	readyReason := "NotReady"
	readyMsg := "Waiting for deployment to become ready"
	if ready {
		readyStatus = metav1.ConditionTrue
		readyReason = "Ready"
		readyMsg = "MCP server is ready"
	}
	meta.SetStatusCondition(&ms.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             readyStatus,
		Reason:             readyReason,
		Message:            readyMsg,
		ObservedGeneration: ms.Generation,
	})

	// Clear Suspended condition when running
	meta.RemoveStatusCondition(&ms.Status.Conditions, "Suspended")

	if err := r.Status().Update(ctx, ms); err != nil {
		return ctrl.Result{}, err
	}

	if !ready {
		return ctrl.Result{RequeueAfter: 5_000_000_000}, nil // 5s
	}
	return ctrl.Result{}, nil
}

func (r *MCPServerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&sympoziumv1alpha1.MCPServer{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Complete(r)
}
