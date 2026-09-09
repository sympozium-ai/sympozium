package charts

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	kubeyaml "k8s.io/apimachinery/pkg/util/yaml"
)

func nativeParentValues() []string {
	return []string{
		"celln.nativeParent.enabled=true",
		"celln.nativeParent.namespace=celln-agents",
		"controller.watchNamespace=sympozium-system",
		"celln.nativeParent.nodeName=framework",
		"celln.nativeParent.statePath=/var/lib/sympozium-celln/starter",
		"celln.nativeParent.configSecret=parent-config",
		"celln.nativeParent.image.repository=example.invalid/parent-controller",
		"celln.nativeParent.image.digest=sha256:" + strings.Repeat("a", 64),
		"celln.permissionPreviewConfigMap=parent-preview",
	}
}

func renderNativeParent(t *testing.T, values []string) ([]byte, error) {
	t.Helper()
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm required for chart rendering")
	}
	args := []string{"template", "test", "sympozium"}
	for _, value := range values {
		args = append(args, "--set", value)
	}
	return exec.Command("helm", args...).CombinedOutput()
}

func TestNativeParentRenderingRefusesUnsafeConfiguration(t *testing.T) {
	for _, value := range []string{
		"rbac.create=false",
		"controller.watchNamespace=",
		"controller.watchNamespace=celln-agents",
		"celln.nativeParent.namespace=sympozium-system",
		"celln.nativeParent.nodeName=",
		"celln.nativeParent.configSecret=",
		"celln.permissionPreviewConfigMap=",
		"celln.nativeParent.image.repository=",
		"celln.nativeParent.image.digest=latest",
		"celln.nativeParent.statePath=/",
		"celln.nativeParent.statePath=/var/lib/celln",
		"celln.nativeParent.statePath=/var/lib/sympozium-celln/../celln",
		"celln.nativeParent.uid=0",
		"celln.nativeParent.gid=0",
		"celln.nativeParent.grantConfigMaps.agent=grant-runtime",
	} {
		t.Run(value, func(t *testing.T) {
			if raw, err := renderNativeParent(t, append(nativeParentValues(), value)); err == nil || !bytes.Contains(raw, []byte("nativeParent")) {
				t.Fatalf("expected explicit nativeParent refusal, got %v: %s", err, raw)
			}
		})
	}
}

func TestNativeParentUninstallRefusesBeforeMutation(t *testing.T) {
	raw, err := renderNativeParent(t, nativeParentValues())
	if err != nil {
		t.Fatalf("render: %v: %s", err, raw)
	}
	decoder := kubeyaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096)
	var script string
	for {
		var object unstructured.Unstructured
		if err := decoder.Decode(&object); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if object.GetKind() == "Job" && object.GetName() == "sympozium-pre-delete-finalizers" {
			var job batchv1.Job
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, &job); err != nil {
				t.Fatal(err)
			}
			script = job.Spec.Template.Spec.Containers[0].Command[2]
		}
	}
	if script == "" || strings.Contains(script, "agents.sympozium.ai agentruns.sympozium.ai") {
		t.Fatal("missing uninstall guard or unsafe AgentRun finalizer stripping")
	}
	for _, mode := range []string{"live", "read-failed", "empty"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			fake := `#!/bin/sh
if [ "$1 $2" = "get agentruns.sympozium.ai" ]; then
  case "$GUARD_TEST_MODE" in
    live) echo parent-run; exit 0 ;;
    read-failed) exit 1 ;;
    empty) exit 0 ;;
  esac
fi
case "$1" in
  scale|patch) echo mutation >> "$GUARD_TEST_LOG" ;;
esac
`
			if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(fake), 0700); err != nil {
				t.Fatal(err)
			}
			log := filepath.Join(dir, "mutations")
			command := exec.Command("/bin/sh", "-c", script)
			command.Env = []string{"PATH=" + dir + ":/usr/bin:/bin", "GUARD_TEST_MODE=" + mode, "GUARD_TEST_LOG=" + log}
			output, err := command.CombinedOutput()
			_, mutationErr := os.Stat(log)
			if mode == "empty" {
				if err != nil || mutationErr != nil {
					t.Fatalf("empty namespace guard failed: %v %s", err, output)
				}
			} else if err == nil || !os.IsNotExist(mutationErr) {
				t.Fatalf("unsafe uninstall passed or mutated state: %v %s", err, output)
			}
		})
	}
}

func TestNativeParentRendering(t *testing.T) {
	raw, err := renderNativeParent(t, nativeParentValues())
	if err != nil {
		t.Fatalf("render: %v: %s", err, raw)
	}
	decoder := kubeyaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096)
	foundParent, foundGeneral, foundRole := false, false, false
	for {
		var object unstructured.Unstructured
		if err := decoder.Decode(&object); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if object.GetKind() == "Namespace" && object.GetName() == "celln-agents" {
			t.Fatal("Helm must not own/delete the namespace containing enduring runs")
		}
		if object.GetKind() == "Deployment" {
			var d appsv1.Deployment
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, &d); err != nil {
				t.Fatal(err)
			}
			if d.Name == "sympozium-controller-manager" {
				foundGeneral = strings.Contains(strings.Join(d.Spec.Template.Spec.Containers[0].Args, " "), "--watch-namespace=sympozium-system")
			}
			if d.Name != "celln-parent-controller" {
				continue
			}
			foundParent = true
			pod := d.Spec.Template.Spec
			if d.Namespace != "celln-agents" || *d.Spec.Replicas != 1 || d.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType || pod.NodeSelector["kubernetes.io/hostname"] != "framework" {
				t.Fatal("parent controller is not single-owner and pinned to its state host")
			}
			if pod.HostNetwork || pod.HostPID || pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken || pod.SecurityContext.FSGroup != nil || *pod.SecurityContext.RunAsUser == 0 {
				t.Fatal("unexpected host privileges or implicit credentials/ownership changes")
			}
			if len(pod.Containers) != 1 || len(pod.Volumes) != 3 {
				t.Fatal("unexpected sidecars or credential/state mounts")
			}
			c := pod.Containers[0]
			if !strings.Contains(c.Image, "@sha256:") || !*c.SecurityContext.ReadOnlyRootFilesystem || *c.SecurityContext.AllowPrivilegeEscalation {
				t.Fatal("parent controller must use pinned image and restricted filesystem")
			}
			if strings.Join(c.Args, " ") != "--celln-parent-only --watch-namespace=celln-agents --leader-elect=false --metrics-bind-address=0 --health-probe-bind-address=:8081" {
				t.Fatalf("unexpected startup: %v", c.Args)
			}
			if len(c.Env) != 2 || c.Env[0].Value != "/var/lib/sympozium-celln/starter/approvals" || c.Env[1].Value != "/etc/sympozium/celln-parent/registrations.json" {
				t.Fatal("unexpected credentials or incorrect authority configuration")
			}
			for _, v := range pod.Volumes {
				switch v.Name {
				case "state":
					if v.HostPath == nil || v.HostPath.Path != "/var/lib/sympozium-celln/starter" || string(*v.HostPath.Type) != "Directory" {
						t.Fatal("host store must already exist; never silently recreate authority")
					}
				case "configuration":
					if v.Secret == nil || v.Secret.SecretName != "parent-config" {
						t.Fatal("operator configuration missing")
					}
				case "kubernetes-identity":
					if v.Projected == nil || len(v.Projected.Sources) != 3 || v.Projected.Sources[0].ServiceAccountToken == nil || *v.Projected.Sources[0].ServiceAccountToken.ExpirationSeconds != 600 {
						t.Fatal("explicit rotating Kubernetes credential required")
					}
				default:
					t.Fatalf("unexpected volume %s", v.Name)
				}
			}
		}
		if object.GetKind() == "Role" && object.GetName() == "celln-parent-controller" {
			foundRole = true
			var role rbacv1.Role
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, &role); err != nil {
				t.Fatal(err)
			}
			if role.Namespace != "celln-agents" || len(role.Rules) != 6 {
				t.Fatal("unexpected parent role scope")
			}
			for _, rule := range role.Rules {
				for _, resource := range rule.Resources {
					if resource == "secrets" || resource == "pods" || resource == "jobs" || resource == "*" {
						t.Fatalf("parent role has excessive authority: %v", rule)
					}
					if resource == "configmaps" && (strings.Join(rule.ResourceNames, ",") != "grant-operator,grant-runtime,grant-agent" || strings.Join(rule.Verbs, ",") != "get") {
						t.Fatal("grant reads must be name-scoped and read-only")
					}
				}
			}
		}
	}
	if !foundParent || !foundGeneral || !foundRole {
		t.Fatal("parent, scoped general controller or restricted role missing")
	}
}

func TestNativeParentDisabledByDefault(t *testing.T) {
	raw, err := renderNativeParent(t, nil)
	if err != nil {
		t.Fatalf("render: %v: %s", err, raw)
	}
	if bytes.Contains(raw, []byte("name: celln-parent-controller")) || bytes.Contains(raw, []byte("--watch-namespace=")) {
		t.Fatal("native parent deployment or changed controller scope enabled implicitly")
	}
}
