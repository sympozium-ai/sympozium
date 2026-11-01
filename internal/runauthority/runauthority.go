// Package runauthority keeps an AgentRun author from reaching what the run's
// Agent has not declared. The controller builds the run's pod with its own
// identity, so anything a run names in its pod spec (a Secret volume, a hook
// reading a Secret, a hook Role over Secrets, a mount over the runner) is read
// with the controller's authority, not the author's. Creating AgentRuns must
// not amount to reading every Secret in the namespace.
//
// The rule is the Agent's: a run may carry these only exactly as its Agent
// declares them (the controller copies Agent volumes, mounts and lifecycle
// onto the runs it creates). It holds whether or not a SympoziumPolicy is
// bound, in admission and again in the controller.
package runauthority

import (
	"fmt"
	"path"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// protectedMountPaths are where a run-supplied mount could replace the
// agent-runner, the system's trust store or name resolution, or the pod's
// service account token.
var protectedMountPaths = []string{"/agent-runner", "/etc", "/usr", "/lib", "/lib64", "/bin", "/sbin", "/var/run/secrets", "/run/secrets"}

// CheckRun refuses a run whose pod spec reaches beyond its Agent: a volume
// source other than emptyDir, configMap or downwardAPI, a mount over a
// protected path, a hook reading a Secret, or a hook Role over Secrets, unless
// the Agent declares exactly that item.
func CheckRun(agent *api.Agent, run *api.AgentRun) error {
	for _, v := range run.Spec.Volumes {
		if plainVolume(v) || slices.ContainsFunc(agent.Spec.Volumes, func(d corev1.Volume) bool { return apiequality.Semantic.DeepEqual(d, v) }) {
			continue
		}
		return fmt.Errorf("AgentRun.spec.volumes[%q]: only emptyDir, configMap and downwardAPI volumes may be set on a run; declare other volumes (Secrets, PVCs, host paths, CSI, projected) on Agent %q", v.Name, agent.Name)
	}
	for _, m := range run.Spec.VolumeMounts {
		p := protectedPath(m.MountPath)
		if p == "" {
			continue
		}
		// Over a protected path, both the mount and the volume behind it must
		// be the Agent's: a declared mount over a run-defined volume would
		// still let the run choose the content.
		if slices.ContainsFunc(agent.Spec.VolumeMounts, func(d corev1.VolumeMount) bool { return apiequality.Semantic.DeepEqual(d, m) }) && sameVolumeAsAgent(agent, run, m.Name) {
			continue
		}
		return fmt.Errorf("AgentRun.spec.volumeMounts[%q]: %s is reserved; a run may not mount over it unless its Agent declares that mount and volume", m.Name, p)
	}
	if run.Spec.Lifecycle == nil {
		return nil
	}
	declared := agent.Spec.Agents.Default.Lifecycle
	if declared == nil {
		declared = &api.LifecycleHooks{}
	}
	for phase, hooks := range map[string][2][]api.LifecycleHookContainer{
		"preRun":  {run.Spec.Lifecycle.PreRun, declared.PreRun},
		"postRun": {run.Spec.Lifecycle.PostRun, declared.PostRun},
	} {
		for _, h := range hooks[0] {
			if !readsSecret(h) || slices.ContainsFunc(hooks[1], func(d api.LifecycleHookContainer) bool { return apiequality.Semantic.DeepEqual(d, h) }) {
				continue
			}
			return fmt.Errorf("lifecycle.%s hook %q reads a Secret: hooks with secretKeyRef env must be declared on Agent %q and are used exactly as declared", phase, h.Name, agent.Name)
		}
	}
	// Any hook Role reaches Secrets one way or another (create a pod that
	// mounts one, exec into a runner holding a key), so every rule is the
	// Agent's to declare.
	for _, rule := range run.Spec.Lifecycle.RBAC {
		if slices.ContainsFunc(declared.RBAC, func(d api.RBACRule) bool { return apiequality.Semantic.DeepEqual(d, rule) }) {
			continue
		}
		return fmt.Errorf("lifecycle.rbac rule over %v: a run may not grant its hooks permissions its Agent does not declare; declare the rule on Agent %q", rule.Resources, agent.Name)
	}
	return nil
}

func plainVolume(v corev1.Volume) bool {
	s := v.VolumeSource
	set := 0
	for _, ok := range []bool{s.EmptyDir != nil, s.ConfigMap != nil, s.DownwardAPI != nil} {
		if ok {
			set++
		}
	}
	plain := corev1.VolumeSource{EmptyDir: s.EmptyDir, ConfigMap: s.ConfigMap, DownwardAPI: s.DownwardAPI}
	return set == 1 && apiequality.Semantic.DeepEqual(plain, s)
}

func protectedPath(p string) string {
	clean := path.Clean("/" + strings.TrimSpace(p))
	if clean == "/" {
		return "/"
	}
	for _, protected := range protectedMountPaths {
		if clean == protected || strings.HasPrefix(clean, protected+"/") {
			return protected
		}
	}
	return ""
}

func readsSecret(h api.LifecycleHookContainer) bool {
	for _, e := range h.Env {
		if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil {
			return true
		}
	}
	return false
}

// sameVolumeAsAgent reports whether the run's volume of this name is exactly
// the Agent's.
func sameVolumeAsAgent(agent *api.Agent, run *api.AgentRun, name string) bool {
	var runVolume, agentVolume *corev1.Volume
	for i := range run.Spec.Volumes {
		if run.Spec.Volumes[i].Name == name {
			runVolume = &run.Spec.Volumes[i]
		}
	}
	for i := range agent.Spec.Volumes {
		if agent.Spec.Volumes[i].Name == name {
			agentVolume = &agent.Spec.Volumes[i]
		}
	}
	return runVolume != nil && agentVolume != nil && apiequality.Semantic.DeepEqual(*runVolume, *agentVolume)
}
