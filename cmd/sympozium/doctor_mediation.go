package main

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellninstall"
)

const (
	mediatedRouteRemedy = "sympozium install --celln-fleet ... --celln-mediated-route provider=anthropic,protocol=anthropic-messages,origin=https://api.anthropic.com,models=<model>   # or set celln.mediation.routes and run: sympozium celln-mediation apply-routes"
	applyRoutesRemedy   = "sympozium celln-mediation apply-routes   # appends the declared routes to the scope's policy; removes nothing"
)

// mediationObjectNames are the bootstrap objects' names: the chart's
// defaults, or what the release's celln.mediation values name instead.
func mediationObjectNames(rel *releaseInfo) (controller, gateway, node, trust string) {
	controller, gateway, node, trust = cellninstall.MediationControllerSecret, cellninstall.MediationGatewaySecret, cellninstall.MediationNodeSecret, cellninstall.MediationTrustConfigMap
	if rel == nil {
		return
	}
	celln, _ := rel.Config["celln"].(map[string]interface{})
	mediation, _ := celln["mediation"].(map[string]interface{})
	for key, target := range map[string]*string{"controllerSecret": &controller, "gatewaySecret": &gateway, "nodeSecret": &node, "trustConfigMap": &trust} {
		if name, _ := mediation[key].(string); name != "" {
			*target = name
		}
	}
	return
}

// checkMediation reports whether Agents can use their own provider key on
// the fleet. Mediation is enabled when the chart rendered the operator's
// declaration into celln-system; it then needs the bootstrapped trust, a
// ready model gateway, and at least one auth "secret" route in the scope's
// policy, because the resolver matches a connection against those routes
// exactly and refuses everything else.
func (d *doctor) checkMediation(ctx context.Context, rel *releaseInfo) doctorFinding {
	const check = "Mediated model access"
	if installed, err := d.fleetInstalled(ctx); err != nil {
		return failed(check, err)
	} else if !installed {
		return doctorFinding{Check: check, Status: statusPass, Summary: "no Celln fleet is installed; mediated model access runs on a fleet"}
	}
	record, err := cellninstall.ReadMediationRecord(ctx, d.client)
	if err != nil {
		return doctorFinding{Check: check, Status: statusFail, Summary: err.Error(), Remedy: []string{"correct celln.mediation.routes in the release's values and upgrade; the chart refuses the same input"}}
	}
	if !record.Enabled {
		return doctorFinding{Check: check, Status: statusPass, Summary: "disabled (celln.mediation.enabled=false): fleet backends serve every run; a run on an Agent's own Secret-backed ModelConnection is held with ScopedDispatchDisabled"}
	}
	f := doctorFinding{Check: check, Status: statusPass}
	raise := func(status string) {
		if f.Status != statusFail {
			f.Status = status
		}
	}
	controller, gateway, node, trust := mediationObjectNames(rel)
	for _, object := range []struct {
		kind, namespace, name string
		into                  client.Object
	}{
		{"Secret", helmNamespace, controller, &corev1.Secret{}},
		{"Secret", helmNamespace, gateway, &corev1.Secret{}},
		{"Secret", cellnSystemNamespace, node, &corev1.Secret{}},
		{"ConfigMap", helmNamespace, trust, &corev1.ConfigMap{}},
		{"ConfigMap", cellnSystemNamespace, trust, &corev1.ConfigMap{}},
	} {
		if err := d.client.Get(ctx, types.NamespacedName{Namespace: object.namespace, Name: object.name}, object.into); apierrors.IsNotFound(err) {
			f.Details = append(f.Details, doctorDetail{Object: fmt.Sprintf("%s %s/%s", object.kind, object.namespace, object.name), Problem: "missing; the controller, dispatchers and gateway cannot start without it"})
			raise(statusFail)
		} else if err != nil {
			return failed(check, err)
		}
	}
	if f.Status == statusFail {
		f.Remedy = append(f.Remedy, "sympozium celln-mediation bootstrap --cluster-id <the release's celln.mediation.clusterId>   # verifies or publishes all five objects; never replaces one")
	}
	var deployments appsv1.DeploymentList
	if err := d.client.List(ctx, &deployments, client.InNamespace(helmNamespace)); err != nil {
		return failed(check, err)
	}
	index := slices.IndexFunc(deployments.Items, func(dep appsv1.Deployment) bool { return strings.HasSuffix(dep.Name, "-model-gateway") })
	switch {
	case index < 0:
		f.Details = append(f.Details, doctorDetail{Object: "Deployment " + helmNamespace + "/<release>-model-gateway", Problem: "missing; the release enables mediation but deploys no model gateway", Remedy: "sympozium install   # or helm upgrade with the mediation values"})
		raise(statusFail)
	case deployments.Items[index].Status.AvailableReplicas < 1 || deployments.Items[index].Status.ObservedGeneration < deployments.Items[index].Generation:
		name := deployments.Items[index].Name
		f.Details = append(f.Details, doctorDetail{Object: "Deployment " + helmNamespace + "/" + name, Problem: "not ready; ready means PostgreSQL, RBAC and keys were accepted", Remedy: fmt.Sprintf("kubectl -n %s logs deploy/%s", helmNamespace, name)})
		raise(statusFail)
	}
	// The routes that count are the ones in the scope's policy: that is what
	// the resolver matches, whatever the release declares.
	var published []sympoziumv1alpha1.CellnExecutionPolicyRoute
	policyName := ""
	if facts, err := cellninstall.ReadFleetFacts(ctx, d.client); err == nil {
		_, policyName, _ = cellninstall.PlatformCatalogueNames(facts.Scope)
		var policy sympoziumv1alpha1.CellnExecutionPolicy
		if err := d.client.Get(ctx, types.NamespacedName{Name: policyName}, &policy); err != nil && !apierrors.IsNotFound(err) && !meta.IsNoMatchError(err) {
			return failed(check, err)
		}
		for _, route := range policy.Spec.Routes {
			if route.Auth == "secret" || route.Auth == "none" {
				published = append(published, route)
			}
		}
	}
	var pending []string
	for _, declared := range record.Routes {
		route, err := declared.PolicyRoute()
		if err == nil && !slices.ContainsFunc(published, func(have sympoziumv1alpha1.CellnExecutionPolicyRoute) bool { return reflect.DeepEqual(have, route) }) {
			pending = append(pending, fmt.Sprintf("%s/%s", route.Provider, strings.Join(route.Models, "+")))
		}
	}
	switch {
	case len(published) == 0 && len(pending) == 0 && !record.MediateBackends:
		raise(statusWarn)
		f.Warning = "no auth \"secret\" route is declared or published: Agents with their own key will be refused AUTH_ROUTE_MISMATCH; declare a route with --celln-mediated-route"
		f.Remedy = append(f.Remedy, mediatedRouteRemedy)
	case len(pending) != 0 || (len(published) == 0 && record.MediateBackends):
		raise(statusWarn)
		what := "celln.mediation.mediateBackends"
		if len(pending) != 0 {
			what = strings.Join(pending, ", ")
		}
		f.Warning = fmt.Sprintf("declared but not in policy %s yet: %s; until then Agents with their own key for it are refused AUTH_ROUTE_MISMATCH", policyName, what)
		f.Remedy = append(f.Remedy, applyRoutesRemedy)
	}
	routes := make([]string, 0, len(published))
	for _, route := range published {
		routes = append(routes, fmt.Sprintf("%s/%s (%s, auth=%s) at %s", route.Provider, strings.Join(route.Models, "+"), route.Protocol, route.Auth, strings.Join(route.EndpointOrigins, "+")))
	}
	switch {
	case f.Status == statusFail:
		f.Summary = fmt.Sprintf("enabled, but %d of its parts are missing or not ready", len(f.Details))
	case len(routes) == 0:
		f.Summary = "enabled; trust objects present and the model gateway is ready, but no Agent may bring its own key yet"
	default:
		f.Summary = fmt.Sprintf("enabled; trust objects present, the model gateway is ready and policy %s permits: %s", policyName, strings.Join(routes, "; "))
	}
	return f
}
