package charts

import (
	"bytes"
	"io"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
)

func singlePlaneValues() []string {
	return []string{
		"celln.enabled=true", "celln.allowInsecureHttp=true", "celln.dispatcher.enabled=true",
		"celln.dispatcher.maxCells=4", "celln.dispatcher.memoryBytes=2147483648",
		"celln.tokenSecret=client", "celln.capabilityTokenSecret=discovery",
		"celln.router.clientTokenSecret=client", "celln.router.backendTokenSecret=backend",
		"celln.router.capabilityTokenSecret=discovery", "celln.router.parentTokenSecret=parent",
		"celln.router.ownershipClaim=ledger", "celln.router.allowInsecureBackends=true",
		"celln.router.backends[0]=http://celln-dispatcher.celln-system.svc:8787",
		"celln.router.image.tag=parent-routing-test",
		"celln.dispatcher.enduring.enabled=true", "celln.dispatcher.enduring.nodeName=kvm-a",
		"celln.dispatcher.enduring.statePath=/var/lib/sympozium-celln/starter",
		"celln.dispatcher.enduring.parentConfigSecret=registrations",
	}
}

func TestSinglePlaneSharesAuthorityAndPinsOwner(t *testing.T) {
	raw, err := renderNativeParent(t, singlePlaneValues())
	if err != nil {
		t.Fatalf("render: %v: %s", err, raw)
	}
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096)
	deployments := map[string]appsv1.Deployment{}
	for {
		var d appsv1.Deployment
		if err := decoder.Decode(&d); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if d.Kind == "Deployment" {
			deployments[d.Name] = d
		}
	}
	if _, ok := deployments["celln-parent-controller"]; ok {
		t.Fatal("separate parent controller rendered")
	}
	const state = "/var/lib/sympozium-celln/starter"
	for _, name := range []string{"sympozium-controller-manager", "celln-dispatcher"} {
		d := deployments[name]
		if d.Spec.Template.Spec.NodeSelector["kubernetes.io/hostname"] != "kvm-a" {
			t.Fatalf("%s can move away from authority", name)
		}
		found := false
		for _, v := range d.Spec.Template.Spec.Volumes {
			if v.HostPath != nil && v.HostPath.Path == state {
				for _, m := range d.Spec.Template.Spec.Containers[0].VolumeMounts {
					if m.Name == v.Name && m.MountPath == state && !m.ReadOnly {
						found = true
					}
				}
			}
		}
		if !found {
			t.Fatalf("%s lacks shared writable absolute state mount", name)
		}
	}
	controller := deployments["sympozium-controller-manager"].Spec.Template.Spec.Containers[0]
	for _, env := range controller.Env {
		if env.Name == "CELLN_PARENT_CONFIG" && env.Value != state+"/approvals" {
			t.Fatal("approvals point at read-only configuration")
		}
	}
	dispatcher := deployments["celln-dispatcher"]
	if dispatcher.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType || !strings.Contains(strings.Join(dispatcher.Spec.Template.Spec.Containers[0].Args, " "), "--root "+state+"/authority") {
		t.Fatal("dispatcher root/lifecycle does not match provisioner")
	}
	if !strings.Contains(strings.Join(deployments["celln-router"].Spec.Template.Spec.Containers[0].Args, " "), "--parent-token-file /etc/celln/parent/token") {
		t.Fatal("gateway parent routing missing")
	}
}

func TestSinglePlaneRefusesSplitOrRelocatableAuthority(t *testing.T) {
	for _, override := range []string{
		"celln.dispatcher.enduring.nodeName=", "celln.dispatcher.enduring.statePath=/",
		"celln.dispatcher.enduring.parentConfigSecret=", "celln.dispatcher.enabled=false",
		"celln.dispatcher.replicas=2", "celln.installer.enabled=true",
		"celln.router.parentTokenSecret=", "celln.dispatcher.enduring.uid=0",
		"controller.nodeSelector.kubernetes\\.io/hostname=another-node",
	} {
		t.Run(override, func(t *testing.T) {
			if raw, err := renderNativeParent(t, append(singlePlaneValues(), override)); err == nil {
				t.Fatalf("unsafe topology rendered: %s", raw)
			}
		})
	}
}
