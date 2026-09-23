package charts

import (
	"bytes"
	"io"
	"reflect"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
)

func dispatcherValues() []string {
	return []string{
		"celln.enabled=true", "celln.allowInsecureHttp=true", "celln.dispatcher.enabled=true",
		"celln.dispatcher.replicas=1", "celln.dispatcher.maxCells=1",
		"celln.dispatcher.memoryBytes=268435456",
		"celln.tokenSecret=client", "celln.capabilityTokenSecret=discovery",
		"celln.router.clientTokenSecret=client", "celln.router.backendTokenSecret=backend",
		"celln.router.capabilityTokenSecret=discovery",
		"celln.router.ownershipClaim=ledger", "celln.router.allowInsecureBackends=true",
		"celln.router.backends[0]=http://celln-dispatcher.celln-system.svc:8787",
		"celln.router.image.tag=dispatcher-test",
	}
}

func renderedDispatcher(t *testing.T, values []string) appsv1.Deployment {
	t.Helper()
	raw, err := renderNativeParent(t, values)
	if err != nil {
		t.Fatalf("render: %v: %s", err, raw)
	}
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096)
	for {
		var d appsv1.Deployment
		if err := decoder.Decode(&d); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if d.Kind == "Deployment" && d.Name == "celln-dispatcher" {
			return d
		}
	}
	t.Fatal("celln-dispatcher not rendered")
	return appsv1.Deployment{}
}

func TestDispatcherTolerationsReachPodSpec(t *testing.T) {
	d := renderedDispatcher(t, append(dispatcherValues(),
		"celln.dispatcher.tolerations[0].key=node-role.kubernetes.io/control-plane",
		"celln.dispatcher.tolerations[0].operator=Exists",
		"celln.dispatcher.tolerations[0].effect=NoSchedule",
		"celln.dispatcher.tolerations[1].key=dedicated",
		"celln.dispatcher.tolerations[1].operator=Equal",
		"celln.dispatcher.tolerations[1].value=celln",
		"celln.dispatcher.tolerations[1].effect=NoExecute",
		"celln.dispatcher.tolerations[1].tolerationSeconds=120",
	))
	seconds := int64(120)
	want := []corev1.Toleration{
		{Key: "node-role.kubernetes.io/control-plane", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule},
		{Key: "dedicated", Operator: corev1.TolerationOpEqual, Value: "celln", Effect: corev1.TaintEffectNoExecute, TolerationSeconds: &seconds},
	}
	if got := d.Spec.Template.Spec.Tolerations; !reflect.DeepEqual(got, want) {
		t.Fatalf("dispatcher tolerations = %#v, want %#v", got, want)
	}
}

func TestDispatcherDefaultsToNoTolerations(t *testing.T) {
	d := renderedDispatcher(t, dispatcherValues())
	if len(d.Spec.Template.Spec.Tolerations) != 0 {
		t.Fatalf("dispatcher gained implicit tolerations: %#v", d.Spec.Template.Spec.Tolerations)
	}
}
