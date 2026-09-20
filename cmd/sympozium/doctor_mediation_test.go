package main

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellninstall"
)

func TestDoctorMediation(t *testing.T) {
	record := func(content string) client.Object {
		return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: cellnSystemNamespace, Name: cellninstall.MediationRecordConfigMap}, Data: map[string]string{"mediation.json": content}}
	}
	trust := func(names ...string) []client.Object {
		controller, gatewaySecret, node, trustMap := names[0], names[1], names[2], names[3]
		return []client.Object{
			&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: helmNamespace, Name: controller}},
			&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: helmNamespace, Name: gatewaySecret}},
			&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: cellnSystemNamespace, Name: node}},
			&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: helmNamespace, Name: trustMap}},
			&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: cellnSystemNamespace, Name: trustMap}},
		}
	}
	defaults := trust(cellninstall.MediationControllerSecret, cellninstall.MediationGatewaySecret, cellninstall.MediationNodeSecret, cellninstall.MediationTrustConfigMap)
	gateway := func(available int32) client.Object {
		return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: helmNamespace, Name: "sympozium-model-gateway", Generation: 1}, Status: appsv1.DeploymentStatus{ObservedGeneration: 1, AvailableReplicas: available}}
	}
	policy := func(routes ...sympoziumv1alpha1.CellnExecutionPolicyRoute) client.Object {
		return &sympoziumv1alpha1.CellnExecutionPolicy{ObjectMeta: metav1.ObjectMeta{Name: "celln-fleet-starter"}, Spec: sympoziumv1alpha1.CellnExecutionPolicySpec{Routes: append([]sympoziumv1alpha1.CellnExecutionPolicyRoute{
			{Provider: "deepseek", Protocol: "openai-chat", Models: []string{"deepseek-chat"}, EndpointOrigins: []string{"https://api.deepseek.com"}, Auth: "host-profile"}}, routes...)}}
	}
	anthropic := sympoziumv1alpha1.CellnExecutionPolicyRoute{Provider: "anthropic", Protocol: "anthropic-messages", Models: []string{"claude-a"}, EndpointOrigins: []string{"https://api.anthropic.com"}, Auth: "secret"}
	const declared = `{"mediateBackends":false,"routes":[{"provider":"anthropic","protocol":"anthropic-messages","models":["claude-a"],"endpointOrigins":["https://api.anthropic.com"]}]}`
	const nothing = `{"mediateBackends":false,"routes":[]}`
	with := func(groups ...[]client.Object) []client.Object {
		var out []client.Object
		for _, g := range groups {
			out = append(out, g...)
		}
		return out
	}
	for _, tc := range []struct {
		name    string
		objects []client.Object
		release *releaseInfo
		status  string
		want    []string
		absent  []string
	}{
		{name: "no fleet", status: statusPass, want: []string{"no Celln fleet is installed"}},
		{name: "disabled", objects: []client.Object{configureDaemonSet()}, status: statusPass, want: []string{"disabled", "ScopedDispatchDisabled"}},
		{name: "enabled and coherent", objects: with([]client.Object{configureDaemonSet(), record(declared), gateway(1), policy(anthropic)}, defaults), status: statusPass,
			want: []string{"enabled", "celln-fleet-starter", "anthropic/claude-a (anthropic-messages, auth=secret) at https://api.anthropic.com"}, absent: []string{"AUTH_ROUTE_MISMATCH"}},
		{name: "enabled without any route", objects: with([]client.Object{configureDaemonSet(), record(nothing), gateway(1), policy()}, defaults), status: statusWarn,
			want: []string{"Agents with their own key will be refused AUTH_ROUTE_MISMATCH; declare a route with --celln-mediated-route", "sympozium install --celln-fleet"}},
		{name: "declared but not published", objects: with([]client.Object{configureDaemonSet(), record(declared), gateway(1), policy()}, defaults), status: statusWarn,
			want: []string{"declared but not in policy celln-fleet-starter yet: anthropic/claude-a", "sympozium celln-mediation apply-routes"}},
		{name: "backends declared but not published", objects: with([]client.Object{configureDaemonSet(), record(`{"mediateBackends":true,"routes":[]}`), gateway(1), policy()}, defaults), status: statusWarn,
			want: []string{"celln.mediation.mediateBackends", "apply-routes"}},
		{name: "a published route outlives its declaration", objects: with([]client.Object{configureDaemonSet(), record(nothing), gateway(1), policy(anthropic)}, defaults), status: statusPass, want: []string{"anthropic/claude-a"}},
		{name: "no bootstrap", objects: []client.Object{configureDaemonSet(), record(declared), gateway(1), policy(anthropic)}, status: statusFail,
			want: []string{"Secret " + helmNamespace + "/celln-mediation-controller", "Secret celln-system/celln-mediation-node", "ConfigMap celln-system/celln-mediation-trust", "sympozium celln-mediation bootstrap"}},
		{name: "bootstrap objects under the release's names", objects: with([]client.Object{configureDaemonSet(), record(declared), gateway(1), policy(anthropic)}, trust("own-controller", "own-gateway", "own-node", "own-trust")),
			release: &releaseInfo{Config: map[string]interface{}{"celln": map[string]interface{}{"mediation": map[string]interface{}{"controllerSecret": "own-controller", "gatewaySecret": "own-gateway", "nodeSecret": "own-node", "trustConfigMap": "own-trust"}}}}, status: statusPass, want: []string{"enabled"}},
		{name: "no gateway", objects: with([]client.Object{configureDaemonSet(), record(declared), policy(anthropic)}, defaults), status: statusFail, want: []string{"deploys no model gateway"}},
		{name: "gateway not ready", objects: with([]client.Object{configureDaemonSet(), record(declared), gateway(0), policy(anthropic)}, defaults), status: statusFail, want: []string{"sympozium-model-gateway", "not ready", "logs deploy/sympozium-model-gateway"}},
		{name: "unusable record", objects: with([]client.Object{configureDaemonSet(), record(`{"routes":[{"provider":"openai","protocol":"openai-chat","models":["*"],"endpointOrigins":["https://api.openai.com"]}]}`)}, defaults), status: statusFail, want: []string{"no wildcard", "celln.mediation.routes"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := testDoctor(t, append([]client.Object{kvmNode("node-a")}, tc.objects...)...)
			f := d.checkMediation(context.Background(), tc.release)
			var text strings.Builder
			printDoctorReport(&text, doctorReport{Findings: []doctorFinding{f}})
			if f.Status != tc.status {
				t.Fatalf("got %s:\n%s", f.Status, text.String())
			}
			for _, want := range tc.want {
				if !strings.Contains(text.String(), want) {
					t.Fatalf("missing %q in\n%s", want, text.String())
				}
			}
			for _, absent := range tc.absent {
				if strings.Contains(text.String(), absent) {
					t.Fatalf("unexpected %q in\n%s", absent, text.String())
				}
			}
		})
	}
	// It is part of every run, after the fleet checks.
	d := testDoctor(t, kvmNode("node-a"))
	if f := findingOf(t, d.run(context.Background()), "Mediated model access"); f.Status != statusPass {
		t.Fatalf("a cluster without a fleet: %+v", f)
	}
}
