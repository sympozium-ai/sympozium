package controller

import (
	"context"
	"fmt"
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

	// If gateway is nil or disabled, clean up and set status
	if config.Spec.Gateway == nil || !config.Spec.Gateway.Enabled {
		if err := r.cleanupGatewayResources(ctx, &config); err != nil {
			log.Error(err, "failed to clean up gateway resources")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, r.updateStatus(ctx, &config, "Disabled", "", nil)
	}

	// Reconcile GatewayClass (cluster-scoped — no ownerRef, use label)
	if err := r.reconcileGatewayClass(ctx, &config); err != nil {
		log.Error(err, "failed to reconcile GatewayClass")
		_ = r.updateStatus(ctx, &config, "Error", fmt.Sprintf("GatewayClass: %v", err), nil)
		return ctrl.Result{}, err
	}

	// Reconcile Gateway (namespace-scoped — ownerRef)
	if err := r.reconcileGateway(ctx, &config); err != nil {
		log.Error(err, "failed to reconcile Gateway")
		_ = r.updateStatus(ctx, &config, "Error", fmt.Sprintf("Gateway: %v", err), nil)
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
	if err := r.updateStatus(ctx, &config, phase, "", gwStatus); err != nil {
		return ctrl.Result{}, err
	}

	// Requeue to pick up Gateway status changes
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
	statusBase := config.DeepCopy()
	config.Status.Phase = phase
	config.Status.Gateway = gwStatus

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
