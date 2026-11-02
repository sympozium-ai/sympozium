package runauthority

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

func secretVolume(name, secret string) corev1.Volume {
	return corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: secret}}}
}

func secretHook(secret string) api.LifecycleHookContainer {
	return api.LifecycleHookContainer{Name: "probe", Image: "busybox:1.36", Command: []string{"sh", "-c", "echo $PROBE"},
		Env: []api.EnvVar{{Name: "PROBE", ValueFrom: &api.EnvVarSource{SecretKeyRef: &api.SecretKeySelector{Name: secret, Key: "TOKEN"}}}}}
}

func agent() *api.Agent {
	return &api.Agent{ObjectMeta: metav1.ObjectMeta{Name: "victim", Namespace: "ns"}}
}

func run() *api.AgentRun {
	return &api.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns"}}
}

// The cluster repro: a run mounting an undeclared Secret, and a hook reading
// one, are refused.
func TestARunCannotReachUndeclaredSecrets(t *testing.T) {
	r := run()
	r.Spec.Volumes = []corev1.Volume{secretVolume("probe", "probe-secret")}
	if err := CheckRun(agent(), r); err == nil || !strings.Contains(err.Error(), "only emptyDir") {
		t.Fatalf("secret volume: %v", err)
	}
	for _, v := range []corev1.VolumeSource{
		{Projected: &corev1.ProjectedVolumeSource{}},
		{HostPath: &corev1.HostPathVolumeSource{Path: "/"}},
		{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "x"}},
		{CSI: &corev1.CSIVolumeSource{Driver: "secrets-store.csi.k8s.io"}},
		{EmptyDir: &corev1.EmptyDirVolumeSource{}, Secret: &corev1.SecretVolumeSource{SecretName: "s"}},
	} {
		r := run()
		r.Spec.Volumes = []corev1.Volume{{Name: "v", VolumeSource: v}}
		if CheckRun(agent(), r) == nil {
			t.Errorf("volume source %+v allowed", v)
		}
	}
	r = run()
	r.Spec.Lifecycle = &api.LifecycleHooks{PreRun: []api.LifecycleHookContainer{secretHook("probe-secret")}}
	if err := CheckRun(agent(), r); err == nil || !strings.Contains(err.Error(), "reads a Secret") {
		t.Fatalf("hook: %v", err)
	}
	r = run()
	r.Spec.Lifecycle = &api.LifecycleHooks{PostRun: []api.LifecycleHookContainer{secretHook("probe-secret")}}
	if CheckRun(agent(), r) == nil {
		t.Fatal("postRun hook allowed")
	}
	for _, resources := range [][]string{{"secrets"}, {"*"}, {"pods", "Secrets"}} {
		r = run()
		r.Spec.Lifecycle = &api.LifecycleHooks{RBAC: []api.RBACRule{{APIGroups: []string{""}, Resources: resources, Verbs: []string{"get"}}}}
		if CheckRun(agent(), r) == nil {
			t.Errorf("rbac over %v allowed", resources)
		}
	}
}

// What the Agent declares, the run may carry exactly; and plain volumes,
// Secret-free hooks and Roles are untouched.
func TestAgentDeclaredItemsAndPlainOnesAreAllowed(t *testing.T) {
	a := agent()
	a.Spec.Volumes = []corev1.Volume{secretVolume("creds", "agent-secret")}
	a.Spec.Volumes = append(a.Spec.Volumes, corev1.Volume{Name: "certs", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "corp-ca"}}}})
	a.Spec.VolumeMounts = []corev1.VolumeMount{{Name: "certs", MountPath: "/etc/ssl/certs/corp"}}
	a.Spec.Agents.Default.Lifecycle = &api.LifecycleHooks{
		PreRun: []api.LifecycleHookContainer{secretHook("gh-pat")},
		RBAC:   []api.RBACRule{{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"get"}}},
	}
	r := run()
	r.Spec.Volumes = []corev1.Volume{secretVolume("creds", "agent-secret"), a.Spec.Volumes[1], {Name: "scratch", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}, {Name: "cfg", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "c"}}}}}
	r.Spec.VolumeMounts = []corev1.VolumeMount{{Name: "certs", MountPath: "/etc/ssl/certs/corp"}, {Name: "scratch", MountPath: "/data"}}
	r.Spec.Lifecycle = a.Spec.Agents.Default.Lifecycle.DeepCopy()
	r.Spec.Lifecycle.PreRun = append(r.Spec.Lifecycle.PreRun, api.LifecycleHookContainer{Name: "plain", Image: "busybox", Env: []api.EnvVar{{Name: "A", Value: "b"}}})
	if err := CheckRun(a, r); err != nil {
		t.Fatal(err)
	}
	// The Agent's Secret hook with another image is not the Agent's hook.
	r.Spec.Lifecycle.PreRun[0].Image = "attacker/exfil"
	if CheckRun(a, r) == nil {
		t.Fatal("a changed declared hook was allowed")
	}
	// Nor is the Agent's Secret volume under another Secret name.
	r = run()
	r.Spec.Volumes = []corev1.Volume{secretVolume("creds", "someone-elses")}
	if CheckRun(a, r) == nil {
		t.Fatal("a changed declared volume was allowed")
	}
}

func TestARunCannotMountOverTheRunner(t *testing.T) {
	for _, p := range []string{"/agent-runner", "/etc/hosts", "/etc/ssl/certs", "/usr/lib", "/", "//etc/../etc/passwd", "/var/run/secrets/kubernetes.io/serviceaccount"} {
		r := run()
		r.Spec.VolumeMounts = []corev1.VolumeMount{{Name: "v", MountPath: p}}
		if CheckRun(agent(), r) == nil {
			t.Errorf("mount at %q allowed", p)
		}
	}
	for _, p := range []string{"/data", "/workspace-extra", "/home/user", "/etcetera"} {
		r := run()
		r.Spec.VolumeMounts = []corev1.VolumeMount{{Name: "v", MountPath: p}}
		if err := CheckRun(agent(), r); err != nil {
			t.Errorf("mount at %q refused: %v", p, err)
		}
	}
}

// Every hook Role is the Agent's: creating pods or exec into a runner reaches
// Secrets as surely as a rule over secrets does.
func TestEveryRunRBACRuleMustBeDeclared(t *testing.T) {
	for _, rule := range []api.RBACRule{
		{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"create"}},
		{APIGroups: []string{""}, Resources: []string{"pods/exec"}, Verbs: []string{"create"}},
		{APIGroups: []string{"batch"}, Resources: []string{"jobs"}, Verbs: []string{"create"}},
		{APIGroups: []string{""}, Resources: []string{"configmaps"}, Verbs: []string{"get"}},
	} {
		r := run()
		r.Spec.Lifecycle = &api.LifecycleHooks{RBAC: []api.RBACRule{rule}}
		if CheckRun(agent(), r) == nil {
			t.Errorf("undeclared rule %v allowed", rule)
		}
		a := agent()
		a.Spec.Agents.Default.Lifecycle = &api.LifecycleHooks{RBAC: []api.RBACRule{rule}}
		if err := CheckRun(a, r); err != nil {
			t.Errorf("declared rule %v refused: %v", rule, err)
		}
	}
}

// A declared mount over a protected path is not enough: the volume behind it
// must be the Agent's too.
func TestADeclaredProtectedMountNeedsTheDeclaredVolume(t *testing.T) {
	a := agent()
	a.Spec.Volumes = []corev1.Volume{{Name: "certs", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "corp-ca"}}}}}
	a.Spec.VolumeMounts = []corev1.VolumeMount{{Name: "certs", MountPath: "/etc/ssl/certs"}}
	r := run()
	r.Spec.VolumeMounts = a.Spec.VolumeMounts
	r.Spec.Volumes = []corev1.Volume{{Name: "certs", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "attacker-ca"}}}}}
	if CheckRun(a, r) == nil {
		t.Fatal("run-defined volume behind a declared protected mount allowed")
	}
	r.Spec.Volumes = a.Spec.Volumes
	if err := CheckRun(a, r); err != nil {
		t.Fatal(err)
	}
}
