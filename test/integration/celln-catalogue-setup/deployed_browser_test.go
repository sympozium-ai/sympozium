package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// This public, ephemeral test credential is deliberately not a model, issuer,
// or router credential. The Service is ClusterIP; forwarding binds loopback.
const browserProofToken = "public-loopback-browser-fixture"

func browserDeploymentObjects(namespace, image string) ([]client.Object, error) {
	if !strings.HasPrefix(namespace, "celln-catalogue-proof-") || !regexp.MustCompile(`^localhost/sympozium-celln-api:[a-z0-9][a-z0-9.-]{0,100}$`).MatchString(image) {
		return nil, fmt.Errorf("private proof namespace and explicit local proof image required")
	}
	meta := func(name string) metav1.ObjectMeta { return metav1.ObjectMeta{Name: name, Namespace: namespace} }
	ref := func(name string) map[string]string { return map[string]string{"namespace": namespace, "name": name} }
	preview, err := json.Marshal(map[string]any{"apiVersion": "sympozium.ai/celln-permission-preview-v1", "bindings": []any{map[string]any{
		"agent": ref("agent"), "operatorSource": ref("operator"), "runtimeSource": ref("runtime"), "agentSource": ref("agent"),
	}}})
	if err != nil {
		return nil, err
	}
	labels := map[string]string{"app": "celln-browser-proof"}
	replicas := int32(1)
	readOnly, nonRoot := true, true
	allowEscalation := false
	// controller-runtime caches list/watch across namespaces. Grant only selected
	// catalogue/run reads cluster-wide; approval reads and run writes
	// remain namespaced. No cluster-wide Secret/ConfigMap or write permission.
	clusterRole := &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: namespace + "-browser"}, Rules: []rbacv1.PolicyRule{
		{APIGroups: []string{"sympozium.ai"}, Resources: []string{"agents", "agentruns", "agentruntimes", "cellntools", "skillpacks", "sympoziumpolicies", "models", "sympoziumconfigs"}, Verbs: []string{"get", "list", "watch"}},
		{APIGroups: []string{""}, Resources: []string{"nodes", "namespaces", "pods"}, Verbs: []string{"get", "list", "watch"}},
	}}
	role := &rbacv1.Role{ObjectMeta: meta("browser"), Rules: []rbacv1.PolicyRule{
		{APIGroups: []string{"sympozium.ai"}, Resources: []string{"agentruns"}, Verbs: []string{"create", "patch", "update", "delete"}},
		{APIGroups: []string{""}, Resources: []string{"configmaps"}, ResourceNames: []string{"operator", "runtime", "agent"}, Verbs: []string{"get"}},
	}}
	subjects := []rbacv1.Subject{{Kind: "ServiceAccount", Name: "browser", Namespace: namespace}}
	return []client.Object{
		&corev1.ServiceAccount{ObjectMeta: meta("browser")},
		&corev1.Secret{ObjectMeta: meta("browser-token"), StringData: map[string]string{"token": browserProofToken}},
		&corev1.ConfigMap{ObjectMeta: meta("browser-preview"), Data: map[string]string{"config.json": string(preview)}},
		clusterRole,
		&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: clusterRole.Name}, Subjects: subjects, RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: clusterRole.Name}},
		role,
		&rbacv1.RoleBinding{ObjectMeta: meta("browser"), Subjects: subjects, RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: role.Name}},
		&corev1.Service{ObjectMeta: meta("browser"), Spec: corev1.ServiceSpec{Selector: labels, Ports: []corev1.ServicePort{{Port: 8080, TargetPort: intstr.FromInt32(8080)}}}},
		&appsv1.Deployment{ObjectMeta: meta("browser"), Spec: appsv1.DeploymentSpec{Replicas: &replicas, Selector: &metav1.LabelSelector{MatchLabels: labels}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{
			ServiceAccountName: "browser",
			Containers: []corev1.Container{{Name: "apiserver", Image: image, ImagePullPolicy: corev1.PullNever,
				Args:            []string{"--event-bus-url=", "--namespace=" + namespace},
				Env:             []corev1.EnvVar{{Name: "CELLN_PERMISSION_PREVIEW_CONFIG", Value: "/etc/sympozium/celln-preview/config.json"}},
				SecurityContext: &corev1.SecurityContext{ReadOnlyRootFilesystem: &readOnly, RunAsNonRoot: &nonRoot, AllowPrivilegeEscalation: &allowEscalation, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
				VolumeMounts:    []corev1.VolumeMount{{Name: "token", MountPath: "/var/run/secrets/sympozium-ui-token", ReadOnly: true}, {Name: "preview", MountPath: "/etc/sympozium/celln-preview", ReadOnly: true}},
				ReadinessProbe:  &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/readyz", Port: intstr.FromInt32(8080)}}, PeriodSeconds: 1},
			}},
			Volumes: []corev1.Volume{{Name: "token", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "browser-token", Items: []corev1.KeyToPath{{Key: "token", Path: "token"}}}}}, {Name: "preview", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "browser-preview"}, Items: []corev1.KeyToPath{{Key: "config.json", Path: "config.json"}}}}}},
		}}}},
	}, nil
}

func deployedBrowserServer(t *testing.T, ctx context.Context, c client.Client, namespace, image string) string {
	t.Helper()
	objects, err := browserDeploymentObjects(namespace, image)
	must(t, err)
	configureInteractiveBrowser(t, namespace, objects)
	for _, obj := range objects {
		must(t, c.Create(ctx, obj))
		if obj.GetNamespace() == "" {
			// Namespace deletion cannot collect cluster-scoped RBAC. Register
			// cleanup immediately, with identity preconditions, including failures.
			t.Cleanup(func() {
				cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				uid := obj.GetUID()
				if err := c.Delete(cleanup, obj, &client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil {
					t.Errorf("browser RBAC cleanup: %v", err)
				}
			})
		}
	}
	return forwardBrowserServer(t, ctx, namespace, image)
}

func forwardBrowserServer(t *testing.T, ctx context.Context, namespace, image string) string {
	t.Helper()
	kube := os.Getenv("CELLN_CONTROLLER_KUBECONFIG")
	args := []string{"--kubeconfig", kube, "--context", "kind-celln-deployed", "-n", namespace}
	command(t, ctx, nil, "kubectl", append(append([]string{}, args...), "rollout", "status", "deployment/browser", "--timeout=90s")...)
	child, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(child, "kubectl", append(args, "port-forward", "service/browser", ":8080", "--address=127.0.0.1")...)
	stdout, err := cmd.StdoutPipe()
	must(t, err)
	must(t, cmd.Start())
	done := make(chan error, 1)
	address := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "Forwarding from 127.0.0.1:") {
				host, _, _ := strings.Cut(strings.TrimPrefix(line, "Forwarding from "), " ->")
				select {
				case address <- "http://" + host:
				default:
				}
			}
		}
		done <- cmd.Wait()
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("browser forwarding did not stop")
		}
	})
	var endpoint string
	select {
	case endpoint = <-address:
	case <-ctx.Done():
		t.Fatal("browser forwarding did not start")
	case <-time.After(20 * time.Second):
		t.Fatal("browser forwarding startup timed out")
	}
	// Positive and negative authentication checks use the actual deployed API,
	// before a browser can create any run. Wrong credentials must not succeed.
	for _, token := range []string{"", "wrong-proof-token", liveBrowserToken(t)} {
		req, err := http.NewRequestWithContext(ctx, "GET", endpoint+"/api/v1/runs?namespace="+namespace, nil)
		must(t, err)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
		must(t, err)
		resp.Body.Close()
		want := http.StatusUnauthorized
		if token == liveBrowserToken(t) {
			want = http.StatusOK
		}
		if resp.StatusCode != want {
			t.Fatalf("deployed API auth status %d, want %d", resp.StatusCode, want)
		}
		if token != liveBrowserToken(t) {
			for _, path := range []string{"/api/v1/runs", "/api/v1/celln-selection/preview"} {
				req, err := http.NewRequestWithContext(ctx, "POST", endpoint+path+"?namespace="+namespace, strings.NewReader(`{}`))
				must(t, err)
				req.Header.Set("Content-Type", "application/json")
				if token != "" {
					req.Header.Set("Authorization", "Bearer "+token)
				}
				resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
				must(t, err)
				resp.Body.Close()
				if resp.StatusCode != http.StatusUnauthorized {
					t.Fatalf("deployed API unauthenticated POST %s: %d", path, resp.StatusCode)
				}
			}
		}
	}
	t.Logf("deployed browser API: namespace=%s image=%s; absent/wrong bearer refused", namespace, image)
	return endpoint
}

func TestBrowserDeploymentBoundary(t *testing.T) {
	objects, err := browserDeploymentObjects("celln-catalogue-proof-test", "localhost/sympozium-celln-api:test")
	must(t, err)
	for _, obj := range objects {
		switch o := obj.(type) {
		case *rbacv1.ClusterRole:
			for _, rule := range o.Rules {
				for _, verb := range rule.Verbs {
					if verb != "get" && verb != "list" && verb != "watch" {
						t.Fatal("cluster write authority")
					}
				}
				for _, resource := range rule.Resources {
					if resource == "secrets" || resource == "configmaps" || resource == "*" {
						t.Fatal("cluster credential/approval read authority")
					}
				}
			}
		case *rbacv1.Role:
			for _, rule := range o.Rules {
				if rule.Resources[0] == "configmaps" && (len(rule.Verbs) != 1 || rule.Verbs[0] != "get" || len(rule.ResourceNames) != 3) {
					t.Fatal("approval authority widened")
				}
			}
		case *appsv1.Deployment:
			container := o.Spec.Template.Spec.Containers[0]
			if container.ImagePullPolicy != corev1.PullNever || len(container.Env) != 1 || container.Env[0].Name != "CELLN_PERMISSION_PREVIEW_CONFIG" {
				t.Fatal("unexpected image/credential configuration")
			}
			for _, mount := range container.VolumeMounts {
				if !mount.ReadOnly {
					t.Fatal("writable credential/configuration mount")
				}
			}
		}
	}
	for _, input := range [][2]string{{"default", "localhost/sympozium-celln-api:test"}, {"celln-catalogue-proof-test", "remote/api:latest"}} {
		if _, err := browserDeploymentObjects(input[0], input[1]); err == nil {
			t.Fatal("unsafe deployment accepted")
		}
	}
}
