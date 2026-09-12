package integration

import (
	"bytes"
	"io"
	"os/exec"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
)

func TestGatewayChartRequiresExplicitTrustAndRestrictsIdentity(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm required for render tests")
	}
	base := []string{"template", "gateway-test", "../../charts/sympozium", "--show-only", "templates/model-gateway.yaml", "--set", "modelGateway.enabled=true"}
	image := "modelGateway.image=example/gateway@sha256:" + strings.Repeat("a", 64)
	for _, args := range [][]string{nil, {"--set", image}, {"--set", image, "--set", "modelGateway.configurationClaim=reviewed"}, {"--set", "modelGateway.image=example/gateway:latest", "--set", "modelGateway.configurationClaim=reviewed", "--set-json", `modelGateway.egress=[{}]`}} {
		if out, err := exec.Command("helm", append(append([]string{}, base...), args...)...).CombinedOutput(); err == nil {
			t.Fatalf("incomplete or unpinned deployment rendered: %s", out)
		}
	}
	args := append(base, "--set", image, "--set", "modelGateway.configurationClaim=reviewed", "--set-json", `modelGateway.egress=[{"to":[{"ipBlock":{"cidr":"192.0.2.0/24"}}],"ports":[{"protocol":"TCP","port":443}]}]`)
	raw, err := exec.Command("helm", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("render: %v %s", err, raw)
	}
	decoder := yamlutil.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096)
	kinds := map[string]int{}
	for {
		var obj unstructured.Unstructured
		if err := decoder.Decode(&obj.Object); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		kinds[obj.GetKind()]++
		switch obj.GetKind() {
		case "ClusterRole":
			rules, _, _ := unstructured.NestedSlice(obj.Object, "rules")
			for _, rule := range rules {
				r := rule.(map[string]interface{})
				resources, _, _ := unstructured.NestedStringSlice(r, "resources")
				verbs, _, _ := unstructured.NestedStringSlice(r, "verbs")
				for _, resource := range resources {
					if resource == "secrets" && (len(verbs) != 1 || verbs[0] != "get") {
						t.Fatal("gateway Secret permission widened")
					}
				}
			}
		case "NetworkPolicy":
			ingress, _, _ := unstructured.NestedSlice(obj.Object, "spec", "ingress")
			for _, entry := range ingress {
				peers, _, _ := unstructured.NestedSlice(entry.(map[string]interface{}), "from")
				for _, peer := range peers {
					p := peer.(map[string]interface{})
					if p["namespaceSelector"] == nil || p["podSelector"] == nil {
						t.Fatal("ingress does not conjoin namespace and pod identity")
					}
				}
			}
		case "Deployment":
			sa, _, _ := unstructured.NestedString(obj.Object, "spec", "template", "spec", "serviceAccountName")
			if !strings.HasSuffix(sa, "-model-gateway") {
				t.Fatal("gateway reused another identity")
			}
			pod, _, _ := unstructured.NestedMap(obj.Object, "spec", "template", "spec")
			if pod["initContainers"] != nil {
				t.Fatal("gateway unexpectedly mutates trust/storage on install")
			}
		}
	}
	for _, kind := range []string{"ServiceAccount", "ClusterRole", "ClusterRoleBinding", "Deployment", "Service", "NetworkPolicy"} {
		if kinds[kind] != 1 {
			t.Fatalf("expected exactly one %s: %v", kind, kinds)
		}
	}
	if len(kinds) != 6 {
		t.Fatalf("unexpected component resources: %v", kinds)
	}
}
