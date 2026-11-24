package controller

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/harnessgateway"
)

// defaultUnit is substrateUnit for a run that names no backend, on a
// controller where Substrate is the default plane.
func defaultUnit(t *testing.T, mutate func(run *sympoziumv1alpha1.AgentRun, agent *sympoziumv1alpha1.Agent, key *corev1.Secret)) (*AgentRunReconciler, *sympoziumv1alpha1.AgentRun, *countingSubstrate) {
	t.Helper()
	r, run, api := substrateUnit(t, func(run *sympoziumv1alpha1.AgentRun, agent *sympoziumv1alpha1.Agent, key *corev1.Secret) {
		run.Spec.Backend = ""
		if mutate != nil {
			mutate(run, agent, key)
		}
	})
	r.Substrate.Default = true
	return r, run, api
}

func planeCondition(run *sympoziumv1alpha1.AgentRun) *metav1.Condition {
	return meta.FindStatusCondition(run.Status.Conditions, ConditionExecutionPlane)
}

func startedSomewhere(run *sympoziumv1alpha1.AgentRun) bool {
	return run.Status.JobName != "" || run.Status.Substrate != nil || isTerminal(run)
}

// A run that names no backend runs as an actor when Substrate is the default
// and can execute it. Its key is bound to the actor at the gateway and never
// judged as a Job's: the plane is chosen before shared admission, which
// would otherwise pin the key to the run's own baseURL, where the actor can
// never use it (found live on 2026-11-24, when every defaulted run failed on
// its pin).
func TestRunWithNoBackendDefaultsToSubstrate(t *testing.T) {
	r, run, api := defaultUnit(t, nil)
	stored := reconcileUntil(t, r, run, isRunning)
	if stored.Status.Substrate == nil || stored.Status.JobName != "" {
		t.Fatalf("substrate %+v job %q, want an actor and no Job", stored.Status.Substrate, stored.Status.JobName)
	}
	if c := planeCondition(stored); c == nil || c.Status != metav1.ConditionTrue {
		t.Errorf("%s = %+v, want True", ConditionExecutionPlane, c)
	}
	if api.count("CreateActor") != 1 {
		t.Errorf("CreateActor calls = %d, want 1", api.count("CreateActor"))
	}
	if binding := bindingSecret(t, r, stored); binding == nil || binding.Annotations[harnessgateway.ActorAnnotation] == "" {
		t.Error("the defaulted run's key is not bound to its actor")
	}
}

// With the default off, or no Substrate, nothing changes: the run is a Job
// and no decision is recorded, because none was made.
func TestRunWithNoBackendIsAJobWhenSubstrateIsNotTheDefault(t *testing.T) {
	for name, configure := range map[string]func(*AgentRunReconciler){
		"default off":    func(r *AgentRunReconciler) { r.Substrate.Default = false },
		"not configured": func(r *AgentRunReconciler) { r.Substrate = nil },
		"clear-text hop not acknowledged": func(r *AgentRunReconciler) {
			r.Substrate.AllowPlaintextHop = false
		},
	} {
		r, run, api := defaultUnit(t, nil)
		configure(r)
		stored := reconcileUntil(t, r, run, startedSomewhere)
		if stored.Status.JobName == "" || stored.Status.Substrate != nil || api.total() != 0 {
			t.Errorf("%s: job %q substrate %+v control API calls %d (error %q)", name, stored.Status.JobName, stored.Status.Substrate, api.total(), stored.Status.Error)
		}
		if c := planeCondition(stored); c != nil {
			t.Errorf("%s: recorded %+v, want no decision", name, c)
		}
	}
}

// A run that names a backend is never moved by the default.
func TestNamedBackendIsNeverReplacedByTheDefault(t *testing.T) {
	r, run, api := defaultUnit(t, func(run *sympoziumv1alpha1.AgentRun, _ *sympoziumv1alpha1.Agent, _ *corev1.Secret) {
		run.Spec.Backend = "job"
	})
	stored := reconcileUntil(t, r, run, startedSomewhere)
	if stored.Status.JobName == "" || stored.Status.Substrate != nil || api.total() != 0 || planeCondition(stored) != nil {
		t.Fatalf("backend job: job %q substrate %+v control API calls %d condition %+v", stored.Status.JobName, stored.Status.Substrate, api.total(), planeCondition(stored))
	}
}

// A run that needs something only a pod has keeps it: it runs as a Job and
// says why. An explicit backend: substrate run is refused for the same
// reasons; a defaulted one must never be, or the default would take something
// away from runs that worked before it.
func TestDefaultFallsBackToAJobRatherThanRefuseOrDropAnything(t *testing.T) {
	type mutation = func(*sympoziumv1alpha1.AgentRun, *sympoziumv1alpha1.Agent, *corev1.Secret)
	for name, mutate := range map[string]mutation{
		"agent skills": func(_ *sympoziumv1alpha1.AgentRun, a *sympoziumv1alpha1.Agent, _ *corev1.Secret) {
			a.Spec.Skills = []sympoziumv1alpha1.SkillRef{{SkillPackRef: "k8s-ops"}}
		},
		"agent MCP servers": func(_ *sympoziumv1alpha1.AgentRun, a *sympoziumv1alpha1.Agent, _ *corev1.Secret) {
			a.Spec.MCPServers = []sympoziumv1alpha1.MCPServerRef{{Name: "github", ToolsPrefix: "gh"}}
		},
		"agent memory": func(_ *sympoziumv1alpha1.AgentRun, a *sympoziumv1alpha1.Agent, _ *corev1.Secret) {
			a.Spec.Memory = &sympoziumv1alpha1.MemorySpec{Enabled: true}
		},
		"ensemble member": func(r *sympoziumv1alpha1.AgentRun, _ *sympoziumv1alpha1.Agent, _ *corev1.Secret) {
			r.Labels = map[string]string{"sympozium.ai/ensemble": "team"}
		},
		"dry run": func(r *sympoziumv1alpha1.AgentRun, _ *sympoziumv1alpha1.Agent, _ *corev1.Secret) {
			r.Spec.DryRun = true
		},
		"spec.env": func(r *sympoziumv1alpha1.AgentRun, _ *sympoziumv1alpha1.Agent, _ *corev1.Secret) {
			r.Spec.Env = map[string]string{"A": "b"}
		},
		"a provider the gateway has no profile for": func(r *sympoziumv1alpha1.AgentRun, a *sympoziumv1alpha1.Agent, _ *corev1.Secret) {
			r.Spec.Model.Provider = "some-other-provider"
			a.Spec.AuthRefs[0].Provider = "some-other-provider"
		},
	} {
		r, run, api := defaultUnit(t, mutate)
		stored := reconcileUntil(t, r, run, startedSomewhere)
		if stored.Status.Substrate != nil || api.total() != 0 {
			t.Errorf("%s: substrate %+v control API calls %d, want none", name, stored.Status.Substrate, api.total())
		}
		if meta.FindStatusCondition(stored.Status.Conditions, conditionExecutionAdmitted) != nil {
			t.Errorf("%s: the run was refused (%q); the default must fall back, not refuse", name, stored.Status.Error)
		}
		c := planeCondition(stored)
		if c == nil || c.Status != metav1.ConditionFalse || c.Reason != "RunsAsJob" || !strings.Contains(c.Message, "requires") {
			t.Errorf("%s: %s = %+v, want False/RunsAsJob with what was missing", name, ConditionExecutionPlane, c)
		}
	}
}

// The plane is chosen once. A run that started as an actor stays on it when
// the default is switched off, and one sent to a Job is not moved when the
// reason it was sent there goes away.
func TestDefaultPlaneIsDecidedOnce(t *testing.T) {
	r, run, _ := defaultUnit(t, nil)
	reconcileUntil(t, r, run, isRunning)
	r.Substrate.Default = false
	stored := reconcileUntil(t, r, run, func(*sympoziumv1alpha1.AgentRun) bool { return true })
	if backend, err := r.executionBackends().admitted(context.Background(), stored); err != nil || backend.Name() != ExecutionBackendSubstrate {
		t.Fatalf("after the default was switched off: backend %v err %v, want substrate", backend, err)
	}

	r, run, api := defaultUnit(t, func(_ *sympoziumv1alpha1.AgentRun, a *sympoziumv1alpha1.Agent, _ *corev1.Secret) {
		a.Spec.Memory = &sympoziumv1alpha1.MemorySpec{Enabled: true}
	})
	if _, err := r.chooseDefaultPlane(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	var agent sympoziumv1alpha1.Agent
	if err := r.Get(context.Background(), client.ObjectKey{Namespace: run.Namespace, Name: run.Spec.AgentRef}, &agent); err != nil {
		t.Fatal(err)
	}
	agent.Spec.Memory = nil
	if err := r.Update(context.Background(), &agent); err != nil {
		t.Fatal(err)
	}
	stored = reconcileUntil(t, r, run, startedSomewhere)
	if stored.Status.Substrate != nil || api.total() != 0 {
		t.Fatalf("a run already sent to a Job was moved to an actor: %+v", stored.Status.Substrate)
	}
}
