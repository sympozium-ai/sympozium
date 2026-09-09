package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/sympozium-ai/sympozium/internal/cellnreview"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const controllerProofMount = "/etc/sympozium/celln-catalogue"

func proofServiceHost(t *testing.T) string {
	t.Helper()
	if os.Getenv("CELLN_LIVE_CONTROLLER_IMAGE") == "" {
		return "127.0.0.1"
	}
	addresses, err := net.InterfaceAddrs()
	must(t, err)
	for _, address := range addresses {
		if ip, _, err := net.ParseCIDR(address.String()); err == nil && ip.String() == "10.89.0.1" {
			return "10.89.0.1"
		}
	}
	t.Fatal("controller Pod proof must run inside the isolated Podman private network namespace")
	return ""
}

func freeServiceAddress(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", net.JoinHostPort(proofServiceHost(t), "0"))
	must(t, err)
	defer l.Close()
	return l.Addr().String()
}

func controllerProofObjects(namespace, image string, config cellnreview.ControllerDispatchConfig, credentials map[string][]byte) ([]client.Object, error) {
	if !strings.HasPrefix(namespace, "celln-catalogue-proof-") || !regexp.MustCompile(`^localhost/sympozium-celln-controller:[a-z0-9][a-z0-9.-]{0,100}$`).MatchString(image) || len(config.Bindings) != 1 {
		return nil, fmt.Errorf("explicit private controller proof required")
	}
	binding := config.Bindings[0]
	if binding.Agent.Namespace != namespace {
		return nil, fmt.Errorf("controller binding outside proof namespace")
	}
	for _, endpoint := range []cellnreview.ControllerEndpoint{binding.Issuer, binding.Router} {
		u, err := url.Parse(endpoint.URL)
		if err != nil || u.Scheme != "https" || u.Hostname() != "10.89.0.1" || u.Port() == "" {
			return nil, fmt.Errorf("verified private gateway endpoint required")
		}
	}
	data := map[string][]byte{}
	for _, name := range []string{"issuer-token", "issuer-ca.pem", "router-token", "router-ca.pem"} {
		if len(credentials[name]) == 0 || len(credentials[name]) > 16384 {
			return nil, fmt.Errorf("missing or oversized controller credential")
		}
		data[name] = append([]byte(nil), credentials[name]...)
	}
	if string(data["issuer-token"]) == string(data["router-token"]) {
		return nil, fmt.Errorf("independent credentials required")
	}
	binding.Issuer.TokenFile = controllerProofMount + "/issuer-token"
	binding.Issuer.CAFile = controllerProofMount + "/issuer-ca.pem"
	binding.Router.TokenFile = controllerProofMount + "/router-token"
	binding.Router.CAFile = controllerProofMount + "/router-ca.pem"
	config.Bindings = []cellnreview.ControllerDispatchBinding{binding}
	raw, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}
	data["config.json"] = raw
	meta := func(name string) metav1.ObjectMeta { return metav1.ObjectMeta{Name: name, Namespace: namespace} }
	labels := map[string]string{"app": "celln-controller-proof"}
	one := int32(1)
	yes, no := true, false
	return []client.Object{
		&corev1.ServiceAccount{ObjectMeta: meta("catalogue-controller")},
		&corev1.Secret{ObjectMeta: meta("catalogue-controller"), Data: data},
		&rbacv1.Role{ObjectMeta: meta("catalogue-controller"), Rules: []rbacv1.PolicyRule{
			{APIGroups: []string{"sympozium.ai"}, Resources: []string{"*"}, Verbs: []string{"get", "list", "watch"}},
			{APIGroups: []string{""}, Resources: []string{"pods", "services", "configmaps", "persistentvolumeclaims", "events"}, Verbs: []string{"get", "list", "watch"}},
			{APIGroups: []string{"apps"}, Resources: []string{"deployments", "replicasets", "statefulsets", "daemonsets"}, Verbs: []string{"get", "list", "watch"}},
			{APIGroups: []string{"batch"}, Resources: []string{"jobs"}, Verbs: []string{"get", "list", "watch"}},
			{APIGroups: []string{"networking.k8s.io"}, Resources: []string{"networkpolicies"}, Verbs: []string{"get", "list", "watch"}},
			{APIGroups: []string{"sympozium.ai"}, Resources: []string{"agentruns", "agentruns/status", "agentruns/finalizers", "agents/status", "agentruntimes/status"}, Verbs: []string{"patch", "update"}},
		}},
		&rbacv1.RoleBinding{ObjectMeta: meta("catalogue-controller"), Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: "catalogue-controller", Namespace: namespace}}, RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: "catalogue-controller"}},
		&appsv1.Deployment{ObjectMeta: meta("catalogue-controller"), Spec: appsv1.DeploymentSpec{Replicas: &one, Selector: &metav1.LabelSelector{MatchLabels: labels}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{
			ServiceAccountName: "catalogue-controller",
			Containers: []corev1.Container{{Name: "controller", Image: image, ImagePullPolicy: corev1.PullNever,
				Args:            []string{"--watch-namespace=" + namespace, "--metrics-bind-address=0", "--health-probe-bind-address=:8081", "--max-run-history=100"},
				Env:             []corev1.EnvVar{{Name: "NATS_URL", Value: ""}, {Name: "AGENT_SANDBOX_ENABLED", Value: "false"}, {Name: "CELLN_HARNESS_ENABLED", Value: "true"}, {Name: "CELLN_CATALOGUE_CONFIG", Value: controllerProofMount + "/config.json"}},
				SecurityContext: &corev1.SecurityContext{ReadOnlyRootFilesystem: &yes, RunAsNonRoot: &yes, AllowPrivilegeEscalation: &no, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
				VolumeMounts:    []corev1.VolumeMount{{Name: "catalogue", MountPath: controllerProofMount, ReadOnly: true}},
				ReadinessProbe:  &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/readyz", Port: intstr.FromInt32(8081)}}, PeriodSeconds: 1},
			}},
			Volumes: []corev1.Volume{{Name: "catalogue", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "catalogue-controller"}}}},
		}}}},
	}, nil
}

func deployCatalogueController(t *testing.T, ctx context.Context, c client.Client, namespace, image, configPath string) {
	t.Helper()
	var config cellnreview.ControllerDispatchConfig
	readJSON(t, configPath, &config)
	if len(config.Bindings) != 1 {
		t.Fatal("single proof binding required")
	}
	b := config.Bindings[0]
	credentials := map[string][]byte{}
	for name, path := range map[string]string{"issuer-token": b.Issuer.TokenFile, "issuer-ca.pem": b.Issuer.CAFile, "router-token": b.Router.TokenFile, "router-ca.pem": b.Router.CAFile} {
		raw, err := os.ReadFile(path)
		must(t, err)
		credentials[name] = raw
	}
	objects, err := controllerProofObjects(namespace, image, config, credentials)
	must(t, err)
	configureInteractiveController(t, objects)
	for _, obj := range objects {
		must(t, c.Create(ctx, obj))
	}
	command(t, ctx, nil, "kubectl", "--kubeconfig", os.Getenv("CELLN_CONTROLLER_KUBECONFIG"), "--context", "kind-celln-deployed", "-n", namespace, "rollout", "status", "deployment/catalogue-controller", "--timeout=90s")
}

// Replace only the controller Pod owned by this test's deployment. The
// issuer/router/dispatcher survive, and response observation remains blocked
// until a different ready Pod is observed by the caller.
func restartCatalogueControllerPod(t *testing.T, ctx context.Context, c client.Client, namespace, evidence string) {
	t.Helper()
	if !strings.HasPrefix(namespace, "celln-catalogue-proof-") {
		t.Fatal("refusing controller replacement outside private proof namespace")
	}
	var deployment appsv1.Deployment
	must(t, c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "catalogue-controller"}, &deployment))
	var pods corev1.PodList
	must(t, c.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{"app": "celln-controller-proof"}))
	if len(pods.Items) != 1 {
		t.Fatal("expected exactly one proof controller Pod")
	}
	old := pods.Items[0]
	owner := metav1.GetControllerOf(&old)
	if owner == nil || owner.Kind != "ReplicaSet" {
		t.Fatal("controller Pod lacks ReplicaSet owner")
	}
	var replicaSet appsv1.ReplicaSet
	must(t, c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: owner.Name}, &replicaSet))
	parent := metav1.GetControllerOf(&replicaSet)
	if replicaSet.UID != owner.UID || parent == nil || parent.UID != deployment.UID || parent.Kind != "Deployment" {
		t.Fatal("controller Pod is not owned by the proof deployment")
	}
	uid := old.UID
	must(t, c.Delete(ctx, &old, &client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}))
	for end := time.Now().Add(90 * time.Second); time.Now().Before(end) && ctx.Err() == nil; {
		var previous corev1.Pod
		err := c.Get(ctx, client.ObjectKeyFromObject(&old), &previous)
		if err != nil && !apierrors.IsNotFound(err) {
			t.Fatal(err)
		}
		if apierrors.IsNotFound(err) {
			must(t, c.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{"app": "celln-controller-proof"}))
			for _, pod := range pods.Items {
				newOwner := metav1.GetControllerOf(&pod)
				if pod.UID == uid || pod.DeletionTimestamp != nil || newOwner == nil || newOwner.UID != replicaSet.UID {
					continue
				}
				for _, condition := range pod.Status.Conditions {
					if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
						writeJSON(t, filepath.Join(evidence, "controller-pod-restart.json"), map[string]any{"oldPodUID": uid, "newPodUID": pod.UID, "deploymentUID": deployment.UID, "oldPodDeleted": true, "newPodReady": true})
						t.Logf("controller Pod replaced while observation blocked: %s -> %s", uid, pod.UID)
						return
					}
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("replacement controller Pod did not become ready after original deletion")
}

func TestControllerProofHasNoApprovalWritesOrSecretReads(t *testing.T) {
	ns := "celln-catalogue-proof-boundary"
	config := cellnreview.ControllerDispatchConfig{APIVersion: "sympozium.ai/celln-catalogue-controller-v1", Bindings: []cellnreview.ControllerDispatchBinding{{Issuer: cellnreview.ControllerEndpoint{URL: "https://10.89.0.1:1234"}, Router: cellnreview.ControllerEndpoint{URL: "https://10.89.0.1:2345"}}}}
	config.Bindings[0].Agent.Namespace = ns
	credentials := map[string][]byte{"issuer-token": []byte("issuer"), "router-token": []byte("router"), "issuer-ca.pem": []byte("issuer-ca"), "router-ca.pem": []byte("router-ca"), "model-key": []byte("must-not-mount")}
	objects, err := controllerProofObjects(ns, "localhost/sympozium-celln-controller:test", config, credentials)
	must(t, err)
	for _, obj := range objects {
		if obj.GetNamespace() != ns {
			t.Fatal("cluster-scoped controller authority")
		}
		switch o := obj.(type) {
		case *rbacv1.Role:
			for _, rule := range o.Rules {
				for _, resource := range rule.Resources {
					if resource == "secrets" {
						t.Fatal("Secret API read granted")
					}
					if resource == "configmaps" || resource == "*" {
						for _, verb := range rule.Verbs {
							if verb != "get" && verb != "list" && verb != "watch" {
								t.Fatal("approval write granted")
							}
						}
					}
				}
			}
		case *corev1.Secret:
			if len(o.Data) != 5 || o.Data["model-key"] != nil {
				t.Fatal("unexpected credential mounted")
			}
		case *appsv1.Deployment:
			container := o.Spec.Template.Spec.Containers[0]
			if container.Args[0] != "--watch-namespace="+ns || container.ImagePullPolicy != corev1.PullNever || !container.VolumeMounts[0].ReadOnly {
				t.Fatal("unsafe controller deployment")
			}
		}
	}
	config.Bindings[0].Issuer.URL = "https://127.0.0.1:1234"
	if _, err := controllerProofObjects(ns, "localhost/sympozium-celln-controller:test", config, credentials); err == nil {
		t.Fatal("Pod loopback host endpoint accepted")
	}
}
