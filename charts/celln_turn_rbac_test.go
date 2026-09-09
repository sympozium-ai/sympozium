package charts

import (
	"bytes"
	"io"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	kubeyaml "k8s.io/apimachinery/pkg/util/yaml"
	"slices"
	"strings"
	"testing"
)

func TestAPITurnRBACAllowsCancellationWithoutStatusAuthority(t *testing.T) {
	raw, err := renderNativeParent(t, nativeParentValues())
	if err != nil {
		t.Fatalf("render: %v %s", err, raw)
	}
	decoder := kubeyaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096)
	found := false
	for {
		var obj unstructured.Unstructured
		if err := decoder.Decode(&obj); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if obj.GetKind() != "ClusterRole" || !strings.HasSuffix(obj.GetName(), "-apiserver") {
			continue
		}
		var role rbacv1.ClusterRole
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &role); err != nil {
			t.Fatal(err)
		}
		for _, rule := range role.Rules {
			if slices.Contains(rule.Resources, "agentrunturns/status") {
				t.Fatal("API must not own turn results")
			}
			if slices.Contains(rule.Resources, "agentrunturns") {
				found = true
				for _, verb := range []string{"get", "list", "watch", "create", "update"} {
					if !slices.Contains(rule.Verbs, verb) {
						t.Fatalf("missing turn %s", verb)
					}
				}
			}
		}
	}
	if !found {
		t.Fatal("API turn role not rendered")
	}
}
