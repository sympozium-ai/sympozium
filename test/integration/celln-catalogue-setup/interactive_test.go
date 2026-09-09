package main

import (
	"context"
	"net"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Interactive mode is a bounded development session using the real deployed
// components and admission workflow. It does not qualify persistent installation.
func startInteractiveBus(t *testing.T, ctx context.Context, dir string) {
	t.Helper()
	binary := os.Getenv("CELLN_LIVE_NATS_BINARY")
	if !filepath.IsAbs(binary) {
		t.Fatal("interactive mode requires an explicit local NATS server binary")
	}
	addr := freeServiceAddress(t)
	host, port, err := net.SplitHostPort(addr)
	must(t, err)
	token := string(freshProofToken(t))
	config := filepath.Join(dir, "nats.json")
	writeJSON(t, config, map[string]any{"listen": net.JoinHostPort(host, port), "authorization": map[string]string{"token": token}, "jetstream": map[string]string{"store_dir": filepath.Join(dir, "nats-state")}})
	startProcess(t, ctx, nil, binary, "--config", config)
	waitTCP(t, addr)
	u := url.URL{Scheme: "nats", Host: addr, User: url.User(token)}
	t.Setenv("CELLN_INTERACTIVE_NATS_URL", u.String())
	tokenPath := filepath.Join(dir, "browser-token")
	must(t, os.WriteFile(tokenPath, freshProofToken(t), 0600))
	t.Setenv("CELLN_INTERACTIVE_BROWSER_TOKEN_FILE", tokenPath)
}

func liveBrowserToken(t *testing.T) string {
	t.Helper()
	if os.Getenv("CELLN_LIVE_INTERACTIVE") != "1" {
		return browserProofToken
	}
	raw, err := os.ReadFile(os.Getenv("CELLN_INTERACTIVE_BROWSER_TOKEN_FILE"))
	must(t, err)
	return string(raw)
}

func configureInteractiveBrowser(t *testing.T, namespace string, objects []client.Object) {
	t.Helper()
	if os.Getenv("CELLN_LIVE_INTERACTIVE") != "1" {
		return
	}
	bus := os.Getenv("CELLN_INTERACTIVE_NATS_URL")
	if bus == "" {
		t.Fatal("interactive event bus missing")
	}
	for _, obj := range objects {
		switch o := obj.(type) {
		case *corev1.Secret:
			o.StringData["token"] = liveBrowserToken(t)
			o.StringData["nats-url"] = bus
		case *appsv1.Deployment:
			container := &o.Spec.Template.Spec.Containers[0]
			container.Args = []string{"--event-bus-url=$(SESSION_NATS_URL)", "--namespace=" + namespace}
			container.Env = append(container.Env, corev1.EnvVar{Name: "SESSION_NATS_URL", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "browser-token"}, Key: "nats-url"}}})
		case *rbacv1.ClusterRole:
			// Dashboard/topology catalogue reads; never approval or credential reads.
			o.Rules = append(o.Rules, rbacv1.PolicyRule{APIGroups: []string{"sympozium.ai"}, Resources: []string{"ensembles", "harnesssessions", "sympoziumschedules", "mcpservers"}, Verbs: []string{"get", "list", "watch"}})
		}
	}
}

func configureInteractiveController(t *testing.T, objects []client.Object) {
	t.Helper()
	if os.Getenv("CELLN_LIVE_INTERACTIVE") != "1" {
		return
	}
	for _, obj := range objects {
		switch o := obj.(type) {
		case *corev1.Secret:
			o.Data["nats-url"] = []byte(os.Getenv("CELLN_INTERACTIVE_NATS_URL"))
		case *appsv1.Deployment:
			for i := range o.Spec.Template.Spec.Containers[0].Env {
				e := &o.Spec.Template.Spec.Containers[0].Env[i]
				if e.Name == "NATS_URL" {
					e.Value = ""
					e.ValueFrom = &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "catalogue-controller"}, Key: "nats-url"}}
				}
			}
		}
	}
}

func holdInteractiveSession(t *testing.T, ctx context.Context, c client.Client, namespace, endpoint, evidence string, first api.AgentRun) {
	t.Helper()
	// A second browser submission proves that registration is reusable for a new
	// persisted run, not just for the original setup UID.
	runBrowser(t, ctx, endpoint, namespace, "", first.Spec.Task.GetPrompt())
	var second api.AgentRun
	for end := time.Now().Add(200 * time.Second); time.Now().Before(end); {
		var runs api.AgentRunList
		must(t, c.List(ctx, &runs, client.InNamespace(namespace)))
		for _, run := range runs.Items {
			if run.UID != first.UID {
				second = run
			}
		}
		if second.Status.Phase == api.AgentRunPhaseSucceeded || second.Status.Phase == api.AgentRunPhaseFailed {
			break
		}
		time.Sleep(time.Second)
	}
	if second.Status.Phase != api.AgentRunPhaseSucceeded {
		t.Fatalf("repeat browser run failed: %s %s", second.Status.Phase, second.Status.Error)
	}
	validateLiveResult(t, second)
	runBrowser(t, ctx, endpoint, namespace, second.Name, "")
	writeJSON(t, filepath.Join(evidence, "repeat-agentrun.json"), second)
	expires, _ := ctx.Deadline()
	expires = expires.Add(-5 * time.Minute)
	writeJSON(t, filepath.Join(evidence, "interactive-ready.json"), map[string]any{
		"status": "ready", "namespace": namespace, "internalURL": endpoint, "pid": os.Getpid(), "expiresAt": expires,
		"browserTokenFile": os.Getenv("CELLN_INTERACTIVE_BROWSER_TOKEN_FILE"), "firstRun": first.Name, "secondRun": second.Name,
		"scope": "bounded one-shot development session; real AI; no conversational persistence or reboot qualification",
	})
	t.Logf("INTERACTIVE READY namespace=%s; two real browser/model runs passed; expires=%s; SIGUSR1 to PID %d shuts down with cleanup", namespace, expires.Format(time.RFC3339), os.Getpid())
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGUSR1)
	defer signal.Stop(stop)
	select {
	case <-stop:
	case <-time.After(time.Until(expires)):
	case <-ctx.Done():
	}
	writeJSON(t, filepath.Join(evidence, "interactive-stopped.json"), map[string]any{"status": "stopping", "time": time.Now()})
}

func configureInteractiveDiscovery(t *testing.T, ctx context.Context, c client.Client, namespace, routerURL, caPath, tokenPath string) {
	t.Helper()
	var secret corev1.Secret
	must(t, c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "browser-token"}, &secret))
	var err error
	secret.Data["capability-token"], err = os.ReadFile(tokenPath)
	must(t, err)
	secret.Data["router-ca.pem"], err = os.ReadFile(caPath)
	must(t, err)
	must(t, c.Update(ctx, &secret))
	var deployment appsv1.Deployment
	must(t, c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "browser"}, &deployment))
	container := &deployment.Spec.Template.Spec.Containers[0]
	container.Env = append(container.Env,
		corev1.EnvVar{Name: "CELLN_ENABLED", Value: "true"},
		corev1.EnvVar{Name: "CELLN_ROUTER_URL", Value: routerURL},
		corev1.EnvVar{Name: "CELLN_CAPABILITY_TOKEN_FILE", Value: "/var/run/secrets/sympozium-ui-token/capability-token"},
		corev1.EnvVar{Name: "SSL_CERT_FILE", Value: "/var/run/secrets/sympozium-ui-token/router-ca.pem"},
	)
	for i := range deployment.Spec.Template.Spec.Volumes {
		v := &deployment.Spec.Template.Spec.Volumes[i]
		if v.Name == "token" {
			v.Secret.Items = append(v.Secret.Items, corev1.KeyToPath{Key: "capability-token", Path: "capability-token"}, corev1.KeyToPath{Key: "router-ca.pem", Path: "router-ca.pem"})
		}
	}
	must(t, c.Update(ctx, &deployment))
	command(t, ctx, nil, "kubectl", "--kubeconfig", os.Getenv("CELLN_CONTROLLER_KUBECONFIG"), "--context", "kind-celln-deployed", "-n", namespace, "rollout", "status", "deployment/browser", "--timeout=90s")
}

func cleanupInteractiveRuns(t *testing.T, ctx context.Context, c client.Client, namespace, evidence string) {
	t.Helper()
	var runs api.AgentRunList
	if err := c.List(ctx, &runs, client.InNamespace(namespace)); err != nil {
		t.Errorf("session runs lookup: %v", err)
		return
	}
	writeJSON(t, filepath.Join(evidence, "session-runs-at-stop.json"), runs)
	for i := range runs.Items {
		run := &runs.Items[i]
		uid := run.UID
		if err := c.Delete(ctx, run, &client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); client.IgnoreNotFound(err) != nil {
			t.Errorf("session run cleanup: %v", err)
		}
	}
	for ctx.Err() == nil {
		must(t, c.List(ctx, &runs, client.InNamespace(namespace)))
		if len(runs.Items) == 0 {
			return
		}
		time.Sleep(time.Second)
	}
	t.Error("session run finalizers did not finish before shutdown")
}
