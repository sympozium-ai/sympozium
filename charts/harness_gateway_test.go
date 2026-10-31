package charts

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	sigsyaml "sigs.k8s.io/yaml"

	"github.com/sympozium-ai/sympozium/internal/harnessgateway"
)

func TestHarnessGatewayAbsentByDefault(t *testing.T) {
	raw, err := renderNativeParent(t, nil)
	if err != nil {
		t.Fatalf("%v\n%s", err, raw)
	}
	for _, unwanted := range []string{"harness-gateway", "SYMPOZIUM_HARNESS_GATEWAY"} {
		if strings.Contains(string(raw), unwanted) {
			t.Errorf("a default install contains %q", unwanted)
		}
	}
}

func TestHarnessGatewayNeedsACertificateSource(t *testing.T) {
	raw, err := renderNativeParent(t, []string{"harnessGateway.enabled=true", "certManager.enabled=false"})
	if err == nil || !strings.Contains(string(raw), "harnessGateway.tls.secretName") {
		t.Errorf("err = %v, output = %.300s", err, raw)
	}
	raw, err = renderNativeParent(t, []string{"harnessGateway.enabled=true", "certManager.enabled=false", "harnessGateway.tls.secretName=operator-cert"})
	if err != nil {
		t.Fatalf("%v\n%s", err, raw)
	}
	out := string(raw)
	if !strings.Contains(out, "secretName: operator-cert") || strings.Contains(out, "sympozium-harness-gateway-ca") {
		t.Error("an operator certificate should replace the chart's")
	}
}

func TestHarnessGatewayRender(t *testing.T) {
	raw, err := renderNativeParent(t, []string{
		"harnessGateway.enabled=true",
		"harnessGateway.insecureOrigins[0]=http://llama.local:8080",
		"harnessGateway.namespaces[0]=tenant-a",
		"networkPolicies.enabled=true",
	})
	if err != nil {
		t.Fatalf("%v\n%s", err, raw)
	}
	out := string(raw)
	for _, want := range []string{
		"isCA: true",
		"harness-gateway.sympozium-system.svc",
		"readOnlyRootFilesystem: true",
		"HARNESS_GATEWAY_MAX_CONCURRENT",
		"value: \"32\"",
		"HARNESS_GATEWAY_MAX_CONCURRENT_PER_RUN",
		"value: \"16\"",
		"https://harness-gateway.sympozium-system.svc",
		"sympozium.ai/agent-run",
		"namespace: \"tenant-a\"",
		"app.kubernetes.io/name: harness-gateway",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render is missing %q", want)
		}
	}
}

// What the ConfigMap holds is what both loaders parse.
func TestHarnessGatewayShippedProfilesAreValid(t *testing.T) {
	raw, err := renderNativeParent(t, []string{
		"harnessGateway.enabled=true",
		"harnessGateway.insecureOrigins[0]=http://llama.local:8080",
		"harnessGateway.extraAllowedEnvNames[0]=MINE_BASE_URL",
		"harnessGateway.extraAllowedEnvNames[1]=MINE_API_KEY",
		"harnessGateway.extraAllowedAuthHeaders[0]=X-Key",
		"harnessGateway.profiles[0].name=mine",
		"harnessGateway.profiles[0].providers[0]=mine",
		"harnessGateway.profiles[0].upstream=https://mine.example",
		"harnessGateway.profiles[0].secretKeys[0]=API_KEY",
		"harnessGateway.profiles[0].auth.type=header",
		"harnessGateway.profiles[0].auth.header=X-Key",
		"harnessGateway.profiles[0].paths[0]=/v1/x",
		"harnessGateway.profiles[0].env.baseURL[0]=MINE_BASE_URL",
		"harnessGateway.profiles[0].env.apiKey[0]=MINE_API_KEY",
	})
	if err != nil {
		t.Fatalf("%v\n%s", err, raw)
	}
	_, after, ok := strings.Cut(string(raw), "  profiles.yaml: |\n")
	if !ok {
		t.Fatal("no profiles.yaml in the render")
	}
	data, _, _ := strings.Cut(after, "\n---")
	var lines []string
	for _, line := range strings.Split(data, "\n") {
		lines = append(lines, strings.TrimPrefix(line, "    "))
	}
	set, err := harnessgateway.ParseSet([]byte(strings.Join(lines, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Rejected) != 0 {
		t.Fatalf("rejected: %v", set.Rejected)
	}
	for _, provider := range []string{"anthropic", "openai", "deepseek", "mine"} {
		if _, err := set.Route(provider, ""); err != nil {
			t.Errorf("%s: %v", provider, err)
		}
	}
	if route, err := set.Route("openai", "http://llama.local:8080/v1"); err != nil || !route.Private {
		t.Errorf("route = %+v, err = %v", route, err)
	}
}

func harnessGatewayObjects(t *testing.T, values ...string) map[string]map[string]any {
	t.Helper()
	raw, err := renderNativeParent(t, append([]string{"harnessGateway.enabled=true"}, values...))
	if err != nil {
		t.Fatalf("%v\n%s", err, raw)
	}
	objects := map[string]map[string]any{}
	for _, document := range bytes.Split(raw, []byte("\n---")) {
		var object map[string]any
		if err := sigsyaml.Unmarshal(document, &object); err != nil {
			t.Fatal(err)
		}
		if object == nil {
			continue
		}
		meta, _ := object["metadata"].(map[string]any)
		name, _ := meta["name"].(string)
		namespace, _ := meta["namespace"].(string)
		kind, _ := object["kind"].(string)
		objects[kind+"/"+namespace+"/"+name] = object
	}
	return objects
}

// The gateway reads tenant keys. It gets no verb beyond get, on nothing beyond
// what it needs.
func TestHarnessGatewayRBACIsGetOnly(t *testing.T) {
	check := func(t *testing.T, role map[string]any, name string) {
		t.Helper()
		if role == nil {
			t.Fatalf("%s is missing", name)
		}
		for _, entry := range role["rules"].([]any) {
			rule := entry.(map[string]any)
			if got := fmt.Sprint(rule["verbs"]); got != "[get]" {
				t.Errorf("%s verbs = %s", name, got)
			}
			for _, resource := range rule["resources"].([]any) {
				if !slices.Contains([]string{"secrets", "configmaps", "agentruns", "agents", "ensembles"}, resource.(string)) {
					t.Errorf("%s grants %v", name, resource)
				}
			}
		}
	}
	t.Run("cluster-wide", func(t *testing.T) {
		objects := harnessGatewayObjects(t)
		check(t, objects["ClusterRole//harness-gateway"], "ClusterRole")
		profiles := objects["Role/sympozium-system/harness-gateway-profiles"]
		check(t, profiles, "profiles Role")
		rule := profiles["rules"].([]any)[0].(map[string]any)
		if got := fmt.Sprint(rule["resourceNames"]); got != "[sympozium-harness-gateway-profiles]" {
			t.Errorf("the profiles Role reaches %s", got)
		}
	})
	t.Run("a namespace list replaces the cluster role", func(t *testing.T) {
		objects := harnessGatewayObjects(t, "harnessGateway.namespaces[0]=tenant-a")
		if objects["ClusterRole//harness-gateway"] != nil || objects["ClusterRoleBinding//harness-gateway"] != nil {
			t.Error("a namespace list must not keep cluster-wide Secret access")
		}
		check(t, objects["Role/tenant-a/harness-gateway"], "tenant Role")
	})
}

func TestHarnessGatewayPodIsHardened(t *testing.T) {
	raw, err := renderNativeParent(t, []string{"harnessGateway.enabled=true"})
	if err != nil {
		t.Fatalf("%v\n%s", err, raw)
	}
	var deployment appsv1.Deployment
	for _, document := range bytes.Split(raw, []byte("\n---")) {
		if bytes.Contains(document, []byte("kind: Deployment")) && bytes.Contains(document, []byte("name: harness-gateway\n")) {
			if err := sigsyaml.Unmarshal(document, &deployment); err != nil {
				t.Fatal(err)
			}
		}
	}
	spec := deployment.Spec.Template.Spec
	if len(spec.Containers) != 1 {
		t.Fatalf("containers = %d", len(spec.Containers))
	}
	if spec.SecurityContext == nil || spec.SecurityContext.RunAsNonRoot == nil || !*spec.SecurityContext.RunAsNonRoot || spec.SecurityContext.SeccompProfile == nil || spec.SecurityContext.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Errorf("pod security = %+v", spec.SecurityContext)
	}
	security := spec.Containers[0].SecurityContext
	if security == nil || security.ReadOnlyRootFilesystem == nil || !*security.ReadOnlyRootFilesystem || security.AllowPrivilegeEscalation == nil || *security.AllowPrivilegeEscalation || security.Capabilities == nil || fmt.Sprint(security.Capabilities.Drop) != "[ALL]" {
		t.Errorf("container security = %+v", security)
	}
	for _, env := range spec.Containers[0].Env {
		if env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil {
			t.Errorf("%s comes from a Secret", env.Name)
		}
	}
	if len(spec.Containers[0].EnvFrom) != 0 {
		t.Error("the gateway must not take a whole Secret as env")
	}
	volume := spec.Volumes[0].Secret
	if volume == nil || volume.DefaultMode == nil || *volume.DefaultMode != 0o440 {
		t.Errorf("TLS volume = %+v", volume)
	}
	if len(volume.Items) != 2 {
		t.Errorf("TLS items = %+v", volume.Items)
	}
}

func TestHarnessGatewayNetworkPolicy(t *testing.T) {
	objects := harnessGatewayObjects(t, "networkPolicies.enabled=true")
	policy := objects["NetworkPolicy/sympozium-system/harness-gateway"]
	if policy == nil {
		t.Fatal("the gateway has no NetworkPolicy")
	}
	spec := policy["spec"].(map[string]any)
	ingress := fmt.Sprint(spec["ingress"])
	if !strings.Contains(ingress, "sympozium.ai/agent-run") || !strings.Contains(ingress, "8443") {
		t.Errorf("ingress = %s", ingress)
	}
	if fmt.Sprint(spec["policyTypes"]) != "[Ingress]" {
		t.Errorf("policyTypes = %v", spec["policyTypes"])
	}
	restricted := harnessGatewayObjects(t, "harnessGateway.egress[0].ports[0].port=443")
	if got := restricted["NetworkPolicy/sympozium-system/harness-gateway"]["spec"].(map[string]any); !strings.Contains(fmt.Sprint(got["policyTypes"]), "Egress") || got["egress"] == nil {
		t.Errorf("a configured egress is not applied: %v", got)
	}
}

// With agent NetworkPolicies on, a mediated harness must still reach the gateway.
func TestAgentsMayReachTheGatewayOnlyWhenItIsEnabled(t *testing.T) {
	const rule = "app.kubernetes.io/name: harness-gateway"
	on, err := renderNativeParent(t, []string{"harnessGateway.enabled=true", "networkPolicies.enabled=true"})
	if err != nil {
		t.Fatalf("%v\n%s", err, on)
	}
	if _, after, ok := strings.Cut(string(on), "sympozium-agent-allow-eventbus"); !ok || !strings.Contains(after, rule) {
		t.Error("agents have no egress rule to the gateway")
	}
	off, err := renderNativeParent(t, []string{"networkPolicies.enabled=true"})
	if err != nil {
		t.Fatalf("%v\n%s", err, off)
	}
	if strings.Contains(string(off), rule) {
		t.Error("a rule to a gateway that is not installed")
	}
}

// An older release's values have no harnessGateway block at all.
func TestAReuseValuesUpgradeStillRenders(t *testing.T) {
	raw, err := renderNativeParent(t, []string{"harnessGateway=null"})
	if err != nil {
		t.Fatalf("%v\n%s", err, raw)
	}
	if strings.Contains(string(raw), "SYMPOZIUM_HARNESS_GATEWAY") {
		t.Error("the gateway rendered with no values")
	}
}

func TestTheChartIssuesACANotASelfSignedLeaf(t *testing.T) {
	objects := harnessGatewayObjects(t)
	ca := objects["Certificate/sympozium-system/sympozium-harness-gateway-ca"]
	if ca == nil || ca["spec"].(map[string]any)["isCA"] != true {
		t.Fatalf("CA = %v", ca)
	}
	leaf := objects["Certificate/sympozium-system/sympozium-harness-gateway-cert"]
	if leaf == nil {
		t.Fatal("no serving certificate")
	}
	spec := leaf["spec"].(map[string]any)
	if fmt.Sprint(spec["issuerRef"]) != "map[kind:Issuer name:sympozium-harness-gateway-ca]" {
		t.Errorf("issuer = %v", spec["issuerRef"])
	}
	if !strings.Contains(fmt.Sprint(spec["dnsNames"]), "harness-gateway.sympozium-system.svc") {
		t.Errorf("dnsNames = %v", spec["dnsNames"])
	}
}
