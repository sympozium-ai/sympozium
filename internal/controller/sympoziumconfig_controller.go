package controller

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

var configMeter = otel.Meter("sympozium.ai/config-controller")

// Gateway readiness metric.
var gatewayReadyGauge, _ = configMeter.Int64UpDownCounter("sympozium.gateway.ready",
	metric.WithUnit("{gateway}"),
	metric.WithDescription("1 when gateway is programmed, 0 otherwise"),
)

const (
	sympoziumConfigFinalizer = "sympozium.ai/config-finalizer"
	managedByLabel           = "sympozium.ai/managed-by"
	managedByValue           = "sympoziumconfig"
)

// SympoziumConfigReconciler reconciles a SympoziumConfig object.
type SympoziumConfigReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Log    logr.Logger
}

// +kubebuilder:rbac:groups=sympozium.ai,resources=sympoziumconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=sympozium.ai,resources=sympoziumconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=sympozium.ai,resources=sympoziumconfigs/finalizers,verbs=update
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gatewayclasses;gateways,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=sympozium.ai,resources=ensembles,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=sympozium.ai,resources=agentruns,verbs=get;list;watch

// Reconcile handles SympoziumConfig reconciliation.
func (r *SympoziumConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("sympoziumconfig", req.NamespacedName)

	var config sympoziumv1alpha1.SympoziumConfig
	if err := r.Get(ctx, req.NamespacedName, &config); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Handle deletion via finalizer
	if !config.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&config, sympoziumConfigFinalizer) {
			if err := r.cleanupGatewayResources(ctx, &config); err != nil {
				log.Error(err, "failed to clean up gateway resources")
				return ctrl.Result{}, err
			}
			if err := r.cleanupCanaryResources(ctx, &config); err != nil {
				log.Error(err, "failed to clean up canary resources")
				return ctrl.Result{}, err
			}
			controllerutil.RemoveFinalizer(&config, sympoziumConfigFinalizer)
			if err := r.Update(ctx, &config); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// Add finalizer if missing
	if !controllerutil.ContainsFinalizer(&config, sympoziumConfigFinalizer) {
		controllerutil.AddFinalizer(&config, sympoziumConfigFinalizer)
		if err := r.Update(ctx, &config); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Reconcile system canary (independent of gateway)
	if err := r.reconcileCanary(ctx, log, &config); err != nil {
		log.Error(err, "failed to reconcile canary")
	}
	canaryStatus := r.readCanaryStatus(ctx, &config)

	// If gateway is nil or disabled, clean up and set status
	if config.Spec.Gateway == nil || !config.Spec.Gateway.Enabled {
		if err := r.cleanupGatewayResources(ctx, &config); err != nil {
			log.Error(err, "failed to clean up gateway resources")
			return ctrl.Result{}, err
		}
		phase := "Disabled"
		// Still requeue if canary is enabled to track its status
		if err := r.updateStatusFull(ctx, &config, phase, "", nil, canaryStatus); err != nil {
			return ctrl.Result{}, err
		}
		if config.Spec.Canary != nil && config.Spec.Canary.Enabled {
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		return ctrl.Result{}, nil
	}

	// Reconcile GatewayClass (cluster-scoped — no ownerRef, use label)
	if err := r.reconcileGatewayClass(ctx, &config); err != nil {
		log.Error(err, "failed to reconcile GatewayClass")
		_ = r.updateStatusFull(ctx, &config, "Error", fmt.Sprintf("GatewayClass: %v", err), nil, canaryStatus)
		return ctrl.Result{}, err
	}

	// Reconcile Gateway (namespace-scoped — ownerRef)
	if err := r.reconcileGateway(ctx, &config); err != nil {
		log.Error(err, "failed to reconcile Gateway")
		_ = r.updateStatusFull(ctx, &config, "Error", fmt.Sprintf("Gateway: %v", err), nil, canaryStatus)
		return ctrl.Result{}, err
	}

	// Read Gateway status and update our status
	gwStatus, err := r.readGatewayStatus(ctx, &config)
	if err != nil {
		log.V(1).Info("could not read gateway status", "error", err)
	}

	phase := "Pending"
	if gwStatus != nil && gwStatus.Ready {
		phase = "Ready"
	}
	if err := r.updateStatusFull(ctx, &config, phase, "", gwStatus, canaryStatus); err != nil {
		return ctrl.Result{}, err
	}

	// Requeue to pick up Gateway and canary status changes
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

func (r *SympoziumConfigReconciler) reconcileGatewayClass(ctx context.Context, config *sympoziumv1alpha1.SympoziumConfig) error {
	gw := config.Spec.Gateway
	className := gw.GatewayClassName
	if className == "" {
		className = "sympozium"
	}

	var existing gatewayv1.GatewayClass
	err := r.Get(ctx, types.NamespacedName{Name: className}, &existing)
	if err == nil {
		// Already exists — ensure it has our label
		if existing.Labels == nil || existing.Labels[managedByLabel] != managedByValue {
			if existing.Labels == nil {
				existing.Labels = make(map[string]string)
			}
			existing.Labels[managedByLabel] = managedByValue
			return r.Update(ctx, &existing)
		}
		return nil
	}
	if !errors.IsNotFound(err) {
		return err
	}

	gc := gatewayv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: className,
			Labels: map[string]string{
				managedByLabel: managedByValue,
			},
		},
		Spec: gatewayv1.GatewayClassSpec{
			ControllerName: "gateway.envoyproxy.io/gatewayclass-controller",
		},
	}

	r.Log.Info("Creating GatewayClass", "name", className)
	return r.Create(ctx, &gc)
}

func (r *SympoziumConfigReconciler) reconcileGateway(ctx context.Context, config *sympoziumv1alpha1.SympoziumConfig) error {
	gw := config.Spec.Gateway
	gatewayName := gw.Name
	if gatewayName == "" {
		gatewayName = "sympozium-gateway"
	}
	className := gw.GatewayClassName
	if className == "" {
		className = "sympozium"
	}

	// Build listeners
	listeners := []gatewayv1.Listener{
		{
			Name:     "http",
			Protocol: gatewayv1.HTTPProtocolType,
			Port:     80,
			AllowedRoutes: &gatewayv1.AllowedRoutes{
				Namespaces: &gatewayv1.RouteNamespaces{
					From: fromPtr(gatewayv1.NamespacesFromSame),
				},
			},
		},
	}

	if gw.TLS != nil && gw.TLS.Enabled && gw.BaseDomain != "" {
		secretName := gw.TLS.SecretName
		if secretName == "" {
			secretName = "sympozium-wildcard-cert"
		}
		tlsMode := gatewayv1.TLSModeTerminate
		listeners = append(listeners, gatewayv1.Listener{
			Name:     "https",
			Protocol: gatewayv1.HTTPSProtocolType,
			Port:     443,
			Hostname: hostnamePtr(fmt.Sprintf("*.%s", gw.BaseDomain)),
			TLS: &gatewayv1.GatewayTLSConfig{
				Mode: &tlsMode,
				CertificateRefs: []gatewayv1.SecretObjectReference{
					{
						Kind: kindPtr("Secret"),
						Name: gatewayv1.ObjectName(secretName),
					},
				},
			},
			AllowedRoutes: &gatewayv1.AllowedRoutes{
				Namespaces: &gatewayv1.RouteNamespaces{
					From: fromPtr(gatewayv1.NamespacesFromSame),
				},
			},
		})
	}

	// Build annotations
	annotations := map[string]string{}
	if gw.TLS != nil && gw.TLS.CertManagerClusterIssuer != "" {
		annotations["cert-manager.io/cluster-issuer"] = gw.TLS.CertManagerClusterIssuer
	}

	var existing gatewayv1.Gateway
	err := r.Get(ctx, types.NamespacedName{Name: gatewayName, Namespace: config.Namespace}, &existing)
	if err == nil {
		// Update existing Gateway
		existing.Spec.GatewayClassName = gatewayv1.ObjectName(className)
		existing.Spec.Listeners = listeners
		existing.Annotations = annotations
		return r.Update(ctx, &existing)
	}
	if !errors.IsNotFound(err) {
		return err
	}

	gateway := gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:        gatewayName,
			Namespace:   config.Namespace,
			Labels:      map[string]string{managedByLabel: managedByValue},
			Annotations: annotations,
		},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: gatewayv1.ObjectName(className),
			Listeners:        listeners,
		},
	}

	if err := controllerutil.SetControllerReference(config, &gateway, r.Scheme); err != nil {
		return err
	}

	r.Log.Info("Creating Gateway", "name", gatewayName)
	return r.Create(ctx, &gateway)
}

func (r *SympoziumConfigReconciler) readGatewayStatus(ctx context.Context, config *sympoziumv1alpha1.SympoziumConfig) (*sympoziumv1alpha1.GatewayStatusInfo, error) {
	gw := config.Spec.Gateway
	gatewayName := gw.Name
	if gatewayName == "" {
		gatewayName = "sympozium-gateway"
	}

	var gateway gatewayv1.Gateway
	if err := r.Get(ctx, types.NamespacedName{Name: gatewayName, Namespace: config.Namespace}, &gateway); err != nil {
		return nil, err
	}

	info := &sympoziumv1alpha1.GatewayStatusInfo{
		ListenerCount: len(gateway.Spec.Listeners),
	}

	// Check programmed condition
	for _, cond := range gateway.Status.Conditions {
		if cond.Type == string(gatewayv1.GatewayConditionProgrammed) && cond.Status == metav1.ConditionTrue {
			info.Ready = true
		}
	}

	// Get address
	if len(gateway.Status.Addresses) > 0 {
		info.Address = gateway.Status.Addresses[0].Value
	}

	// Emit gateway readiness metric.
	if info.Ready {
		gatewayReadyGauge.Add(ctx, 1)
	}

	return info, nil
}

func (r *SympoziumConfigReconciler) cleanupGatewayResources(ctx context.Context, config *sympoziumv1alpha1.SympoziumConfig) error {
	// Delete Gateway resources by label
	var gateways gatewayv1.GatewayList
	if err := r.List(ctx, &gateways, client.InNamespace(config.Namespace), client.MatchingLabels{managedByLabel: managedByValue}); err != nil {
		if !errors.IsNotFound(err) {
			r.Log.V(1).Info("could not list Gateways", "error", err)
		}
	} else {
		for i := range gateways.Items {
			if err := r.Delete(ctx, &gateways.Items[i]); err != nil && !errors.IsNotFound(err) {
				return err
			}
		}
	}

	// Delete GatewayClass resources by label (cluster-scoped)
	var classes gatewayv1.GatewayClassList
	if err := r.List(ctx, &classes, client.MatchingLabels{managedByLabel: managedByValue}); err != nil {
		if !errors.IsNotFound(err) {
			r.Log.V(1).Info("could not list GatewayClasses", "error", err)
		}
	} else {
		for i := range classes.Items {
			if err := r.Delete(ctx, &classes.Items[i]); err != nil && !errors.IsNotFound(err) {
				return err
			}
		}
	}

	return nil
}

func (r *SympoziumConfigReconciler) updateStatus(ctx context.Context, config *sympoziumv1alpha1.SympoziumConfig, phase, message string, gwStatus *sympoziumv1alpha1.GatewayStatusInfo) error {
	return r.updateStatusFull(ctx, config, phase, message, gwStatus, nil)
}

func (r *SympoziumConfigReconciler) updateStatusFull(ctx context.Context, config *sympoziumv1alpha1.SympoziumConfig, phase, message string, gwStatus *sympoziumv1alpha1.GatewayStatusInfo, canaryStatus *sympoziumv1alpha1.CanaryStatusInfo) error {
	statusBase := config.DeepCopy()
	config.Status.Phase = phase
	config.Status.Gateway = gwStatus
	if canaryStatus != nil {
		config.Status.Canary = canaryStatus
	}

	condStatus := metav1.ConditionTrue
	reason := "Ready"
	switch phase {
	case "Error":
		condStatus = metav1.ConditionFalse
		reason = "ReconcileError"
	case "Pending":
		condStatus = metav1.ConditionFalse
		reason = "Pending"
	case "Disabled":
		condStatus = metav1.ConditionFalse
		reason = "Disabled"
		message = ""
	}
	meta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             condStatus,
		ObservedGeneration: config.Generation,
		Reason:             reason,
		Message:            message,
	})

	return r.Status().Patch(ctx, config, client.MergeFrom(statusBase))
}

// ── Canary reconciliation ───────────────────────────────────────────────────

const canaryEnsembleName = "system-canary"

// reconcileCanary ensures the canary Ensemble exists and mirrors the config.
func (r *SympoziumConfigReconciler) reconcileCanary(ctx context.Context, log logr.Logger, config *sympoziumv1alpha1.SympoziumConfig) error {
	enabled := config.Spec.Canary != nil && config.Spec.Canary.Enabled
	interval := "30m"
	if config.Spec.Canary != nil && config.Spec.Canary.Interval != "" {
		interval = config.Spec.Canary.Interval
	}

	var existing sympoziumv1alpha1.Ensemble
	err := r.Get(ctx, types.NamespacedName{Name: canaryEnsembleName, Namespace: config.Namespace}, &existing)

	if errors.IsNotFound(err) {
		if !enabled {
			return nil // nothing to do
		}
		// Create the canary ensemble
		ensemble := r.buildCanaryEnsemble(config, interval)
		if setErr := controllerutil.SetControllerReference(config, &ensemble, r.Scheme); setErr != nil {
			return setErr
		}
		log.Info("Creating system canary ensemble", "interval", interval)
		return r.Create(ctx, &ensemble)
	}
	if err != nil {
		return err
	}

	// Ensemble exists — sync enabled state, interval, and provider config
	desired := r.buildCanaryEnsemble(config, interval)
	needsUpdate := false
	if existing.Spec.Enabled != enabled {
		existing.Spec.Enabled = enabled
		needsUpdate = true
	}
	if enabled && len(existing.Spec.AgentConfigs) > 0 {
		ac := &existing.Spec.AgentConfigs[0]
		if ac.Schedule != nil && ac.Schedule.Interval != interval {
			ac.Schedule.Interval = interval
			needsUpdate = true
		}
		if ac.Model != desired.Spec.AgentConfigs[0].Model {
			ac.Model = desired.Spec.AgentConfigs[0].Model
			needsUpdate = true
		}
		if ac.Provider != desired.Spec.AgentConfigs[0].Provider {
			ac.Provider = desired.Spec.AgentConfigs[0].Provider
			needsUpdate = true
		}
		if ac.BaseURL != desired.Spec.AgentConfigs[0].BaseURL {
			ac.BaseURL = desired.Spec.AgentConfigs[0].BaseURL
			needsUpdate = true
		}
	}
	if fmt.Sprintf("%v", existing.Spec.AuthRefs) != fmt.Sprintf("%v", desired.Spec.AuthRefs) {
		existing.Spec.AuthRefs = desired.Spec.AuthRefs
		needsUpdate = true
	}
	if existing.Spec.BaseURL != desired.Spec.BaseURL {
		existing.Spec.BaseURL = desired.Spec.BaseURL
		needsUpdate = true
	}
	if needsUpdate {
		log.Info("Updating system canary ensemble", "enabled", enabled, "interval", interval)
		return r.Update(ctx, &existing)
	}
	return nil
}

// readCanaryStatus reads the latest canary run and parses the health status.
func (r *SympoziumConfigReconciler) readCanaryStatus(ctx context.Context, config *sympoziumv1alpha1.SympoziumConfig) *sympoziumv1alpha1.CanaryStatusInfo {
	status := &sympoziumv1alpha1.CanaryStatusInfo{}

	var ensemble sympoziumv1alpha1.Ensemble
	if err := r.Get(ctx, types.NamespacedName{Name: canaryEnsembleName, Namespace: config.Namespace}, &ensemble); err != nil {
		return status
	}
	status.EnsembleCreated = true

	// Find the most recent canary AgentRun
	var runs sympoziumv1alpha1.AgentRunList
	if err := r.List(ctx, &runs,
		client.InNamespace(config.Namespace),
		client.MatchingLabels{"sympozium.ai/instance": canaryEnsembleName + "-canary"},
	); err != nil {
		return status
	}

	if len(runs.Items) == 0 {
		return status
	}

	// Sort by creation timestamp descending to find the latest
	sort.Slice(runs.Items, func(i, j int) bool {
		return runs.Items[j].CreationTimestamp.Before(&runs.Items[i].CreationTimestamp)
	})

	latest := runs.Items[0]
	status.LastRunPhase = string(latest.Status.Phase)
	if latest.Status.CompletedAt != nil {
		status.LastRunTime = latest.Status.CompletedAt.Format(time.RFC3339)
	}

	// Parse the health status from the result text
	if latest.Status.Result != "" {
		result := strings.ToUpper(latest.Status.Result)
		switch {
		case strings.Contains(result, "UNHEALTHY"):
			status.HealthStatus = "unhealthy"
		case strings.Contains(result, "DEGRADED"):
			status.HealthStatus = "degraded"
		case strings.Contains(result, "HEALTHY"):
			status.HealthStatus = "healthy"
		default:
			status.HealthStatus = "unknown"
		}
	} else if latest.Status.Phase == "Failed" {
		status.HealthStatus = "unhealthy"
	}

	return status
}

// cleanupCanaryResources removes the canary ensemble.
func (r *SympoziumConfigReconciler) cleanupCanaryResources(ctx context.Context, config *sympoziumv1alpha1.SympoziumConfig) error {
	var ensemble sympoziumv1alpha1.Ensemble
	if err := r.Get(ctx, types.NamespacedName{Name: canaryEnsembleName, Namespace: config.Namespace}, &ensemble); err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}
	return r.Delete(ctx, &ensemble)
}

// buildCanaryEnsemble returns the canary Ensemble spec.
func (r *SympoziumConfigReconciler) buildCanaryEnsemble(config *sympoziumv1alpha1.SympoziumConfig, interval string) sympoziumv1alpha1.Ensemble {
	canary := config.Spec.Canary

	persona := sympoziumv1alpha1.AgentConfigSpec{
		Name:         "canary",
		DisplayName:  "System Canary",
		SystemPrompt: "System canary health check",
		Schedule: &sympoziumv1alpha1.AgentConfigSchedule{
			Type:     "heartbeat",
			Interval: interval,
			Task:     "canary",
		},
	}

	// Apply per-persona model/provider overrides from canary config.
	if canary != nil {
		if canary.Model != "" {
			persona.Model = canary.Model
		}
		if canary.Provider != "" {
			persona.Provider = canary.Provider
		}
		if canary.BaseURL != "" {
			persona.BaseURL = canary.BaseURL
		}
	}

	spec := sympoziumv1alpha1.EnsembleSpec{
		Enabled:      true,
		Description:  "System health canary — validates end-to-end platform health on a schedule.",
		Category:     "platform",
		Version:      "1.0.0",
		WorkflowType: "autonomous",
		AgentConfigs: []sympoziumv1alpha1.AgentConfigSpec{persona},
	}

	// Set ensemble-level auth and base URL.
	if canary != nil {
		if canary.AuthSecretRef != "" {
			spec.AuthRefs = []sympoziumv1alpha1.SecretRef{{Secret: canary.AuthSecretRef}}
		}
		if canary.BaseURL != "" {
			spec.BaseURL = canary.BaseURL
		}
	}

	return sympoziumv1alpha1.Ensemble{
		ObjectMeta: metav1.ObjectMeta{
			Name:      canaryEnsembleName,
			Namespace: config.Namespace,
			Labels: map[string]string{
				"sympozium.ai/canary":          "true",
				"app.kubernetes.io/managed-by": "sympozium-config",
			},
		},
		Spec: spec,
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *SympoziumConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&sympoziumv1alpha1.SympoziumConfig{}).
		Complete(r)
}

// Helper functions for Gateway API pointer types.
func fromPtr(f gatewayv1.FromNamespaces) *gatewayv1.FromNamespaces { return &f }
func hostnamePtr(h string) *gatewayv1.Hostname {
	hn := gatewayv1.Hostname(h)
	return &hn
}
func kindPtr(k string) *gatewayv1.Kind {
	kind := gatewayv1.Kind(k)
	return &kind
}
