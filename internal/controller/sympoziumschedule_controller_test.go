package controller

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

func newScheduleTestReconciler(t *testing.T, objs ...client.Object) (*SympoziumScheduleReconciler, client.Client) {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add sympozium scheme: %v", err)
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	return &SympoziumScheduleReconciler{
		Client: cl,
		Scheme: scheme,
		Log:    logr.Discard(),
	}, cl
}

func TestSympoziumScheduleReconcile_CopiesProviderAndAuthSecretToRun(t *testing.T) {
	now := time.Now()
	instance := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "inst-a",
			Namespace: "default",
		},
		Spec: sympoziumv1alpha1.AgentSpec{
			Agents: sympoziumv1alpha1.AgentsSpec{
				Default: sympoziumv1alpha1.AgentConfig{
					Model: "claude-3-5-sonnet",
				},
			},
			AuthRefs: []sympoziumv1alpha1.SecretRef{
				{Provider: "anthropic", Secret: "inst-a-anthropic-key"},
			},
		},
	}
	schedule := &sympoziumv1alpha1.SympoziumSchedule{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "inst-a-heartbeat",
			Namespace:         "default",
			CreationTimestamp: metav1.NewTime(now.Add(-2 * time.Minute)),
		},
		Spec: sympoziumv1alpha1.SympoziumScheduleSpec{
			AgentRef: "inst-a",
			Schedule: "* * * * *",
			Task:     "heartbeat",
			Type:     "heartbeat",
		},
	}

	r, cl := newScheduleTestReconciler(t, instance, schedule)
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: schedule.Name, Namespace: schedule.Namespace},
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	run := &sympoziumv1alpha1.AgentRun{}
	if err := cl.Get(context.Background(), types.NamespacedName{
		Name:      schedule.Name + "-1",
		Namespace: schedule.Namespace,
	}, run); err != nil {
		t.Fatalf("get created run: %v", err)
	}
	// The scheduled AgentRun must be owned by its parent Schedule so
	// disabling a Ensemble cascades cleanup and doesn't leave
	// orphan Failed runs referencing a deleted instance.
	if len(run.OwnerReferences) != 1 {
		t.Fatalf("expected scheduled run to have 1 owner reference, got %d", len(run.OwnerReferences))
	}
	if run.OwnerReferences[0].Kind != "SympoziumSchedule" {
		t.Errorf("owner ref kind = %q, want SympoziumSchedule", run.OwnerReferences[0].Kind)
	}
	if run.OwnerReferences[0].Name != schedule.Name {
		t.Errorf("owner ref name = %q, want %q", run.OwnerReferences[0].Name, schedule.Name)
	}
	if run.OwnerReferences[0].Controller == nil || !*run.OwnerReferences[0].Controller {
		t.Errorf("owner ref must be controller=true")
	}

	if run.Spec.Model.Provider != "anthropic" {
		t.Fatalf("provider = %q, want anthropic", run.Spec.Model.Provider)
	}
	if run.Spec.Model.AuthSecretRef != "inst-a-anthropic-key" {
		t.Fatalf("authSecretRef = %q, want inst-a-anthropic-key", run.Spec.Model.AuthSecretRef)
	}

	agentContainers, _, _ := (&AgentRunReconciler{}).buildContainers(run, false, nil, nil, nil, nil)
	// Auth secrets are now injected as individual secretKeyRef entries (Fix 9).
	found := false
	for _, env := range agentContainers[0].Env {
		if env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil &&
			env.ValueFrom.SecretKeyRef.Name == "inst-a-anthropic-key" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected scheduled run auth secret to be mounted via secretKeyRef")
	}
}

func TestSympoziumScheduleReconcile_FiltersWebEndpointSkill(t *testing.T) {
	now := time.Now()
	instance := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "inst-web",
			Namespace: "default",
		},
		Spec: sympoziumv1alpha1.AgentSpec{
			Agents: sympoziumv1alpha1.AgentsSpec{
				Default: sympoziumv1alpha1.AgentConfig{
					Model: "gpt-4o",
				},
			},
			AuthRefs: []sympoziumv1alpha1.SecretRef{
				{Provider: "openai", Secret: "inst-web-openai-key"},
			},
			Skills: []sympoziumv1alpha1.SkillRef{
				{SkillPackRef: "k8s-ops"},
				{SkillPackRef: "web-endpoint", Params: map[string]string{"rate_limit_rpm": "60"}},
				{SkillPackRef: "code-review"},
			},
		},
	}
	schedule := &sympoziumv1alpha1.SympoziumSchedule{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "inst-web-heartbeat",
			Namespace:         "default",
			CreationTimestamp: metav1.NewTime(now.Add(-2 * time.Minute)),
		},
		Spec: sympoziumv1alpha1.SympoziumScheduleSpec{
			AgentRef: "inst-web",
			Schedule: "* * * * *",
			Task:     "heartbeat",
			Type:     "heartbeat",
		},
	}

	r, cl := newScheduleTestReconciler(t, instance, schedule)
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: schedule.Name, Namespace: schedule.Namespace},
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	run := &sympoziumv1alpha1.AgentRun{}
	if err := cl.Get(context.Background(), types.NamespacedName{
		Name:      schedule.Name + "-1",
		Namespace: schedule.Namespace,
	}, run); err != nil {
		t.Fatalf("get created run: %v", err)
	}

	// The web-endpoint skill should be filtered out from scheduled runs.
	if len(run.Spec.Skills) != 2 {
		t.Fatalf("skill count = %d, want 2 (web-endpoint should be filtered)", len(run.Spec.Skills))
	}
	for _, skill := range run.Spec.Skills {
		if skill.SkillPackRef == "web-endpoint" {
			t.Error("web-endpoint skill should not be present in scheduled AgentRun")
		}
	}
	// Verify the other skills are present.
	skillNames := make(map[string]bool)
	for _, s := range run.Spec.Skills {
		skillNames[s.SkillPackRef] = true
	}
	if !skillNames["k8s-ops"] {
		t.Error("k8s-ops skill should be present")
	}
	if !skillNames["code-review"] {
		t.Error("code-review skill should be present")
	}
}

func TestSympoziumScheduleReconcile_SkipsWhenServingRunExists(t *testing.T) {
	now := time.Now()
	instance := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "inst-serving",
			Namespace: "default",
		},
		Spec: sympoziumv1alpha1.AgentSpec{
			Agents: sympoziumv1alpha1.AgentsSpec{
				Default: sympoziumv1alpha1.AgentConfig{
					Model: "gpt-4o",
				},
			},
			AuthRefs: []sympoziumv1alpha1.SecretRef{
				{Provider: "openai", Secret: "inst-serving-openai-key"},
			},
		},
	}
	// Existing serving AgentRun.
	servingRun := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "inst-serving-web",
			Namespace: "default",
			Labels: map[string]string{
				"sympozium.ai/instance": "inst-serving",
			},
		},
		Spec: sympoziumv1alpha1.AgentRunSpec{
			AgentRef:   "inst-serving",
			AgentID:    "web-endpoint",
			SessionKey: "web",
			Task:       sympoziumv1alpha1.NewStringTask("serve"),
			Mode:       "server",
			Model: sympoziumv1alpha1.ModelSpec{
				Provider:      "openai",
				Model:         "gpt-4o",
				AuthSecretRef: "inst-serving-openai-key",
			},
			ImagePullSecrets: []corev1.LocalObjectReference{
				{Name: "inst-serving-secret"},
			},
		},
		Status: sympoziumv1alpha1.AgentRunStatus{
			Phase: sympoziumv1alpha1.AgentRunPhaseServing,
		},
	}
	schedule := &sympoziumv1alpha1.SympoziumSchedule{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "inst-serving-heartbeat",
			Namespace:         "default",
			CreationTimestamp: metav1.NewTime(now.Add(-2 * time.Minute)),
		},
		Spec: sympoziumv1alpha1.SympoziumScheduleSpec{
			AgentRef: "inst-serving",
			Schedule: "* * * * *",
			Task:     "heartbeat",
			Type:     "heartbeat",
		},
	}

	r, cl := newScheduleTestReconciler(t, instance, servingRun, schedule)

	// Need to set status on the serving run after creation since fake client
	// doesn't support status subresource by default.
	servingRun.Status.Phase = sympoziumv1alpha1.AgentRunPhaseServing
	if err := cl.Status().Update(context.Background(), servingRun); err != nil {
		// Status subresource may not be configured; update directly.
		if err2 := cl.Update(context.Background(), servingRun); err2 != nil {
			t.Fatalf("update serving run status: %v (status update: %v)", err2, err)
		}
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: schedule.Name, Namespace: schedule.Namespace},
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	// No new AgentRun should have been created because a serving run exists.
	var runs sympoziumv1alpha1.AgentRunList
	if err := cl.List(context.Background(), &runs, client.InNamespace("default")); err != nil {
		t.Fatalf("list runs: %v", err)
	}

	for _, run := range runs.Items {
		if run.Name != servingRun.Name {
			t.Errorf("unexpected AgentRun created: %s (should skip when serving run exists)", run.Name)
		}
	}
}

func TestSympoziumScheduleReconcile_UnreachableCronDoesNotFire(t *testing.T) {
	now := time.Now()
	instance := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "inst-unreachable",
			Namespace: "default",
		},
		Spec: sympoziumv1alpha1.AgentSpec{
			Agents: sympoziumv1alpha1.AgentsSpec{
				Default: sympoziumv1alpha1.AgentConfig{
					Model: "claude-3-5-sonnet",
				},
			},
			AuthRefs: []sympoziumv1alpha1.SecretRef{
				{Provider: "anthropic", Secret: "inst-unreachable-key"},
			},
		},
	}
	schedule := &sympoziumv1alpha1.SympoziumSchedule{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "inst-unreachable-schedule",
			Namespace:         "default",
			CreationTimestamp: metav1.NewTime(now.Add(-2 * time.Minute)),
		},
		Spec: sympoziumv1alpha1.SympoziumScheduleSpec{
			AgentRef: "inst-unreachable",
			Schedule: "0 0 31 2 *",
			Task:     "discovery",
			Type:     "scheduled",
		},
	}

	r, cl := newScheduleTestReconciler(t, instance, schedule)
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: schedule.Name, Namespace: schedule.Namespace},
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var runs sympoziumv1alpha1.AgentRunList
	if err := cl.List(context.Background(), &runs, client.InNamespace("default")); err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs.Items) != 0 {
		t.Errorf("expected no AgentRuns for unreachable cron (Feb 31), got %d", len(runs.Items))
	}
}

func TestSympoziumScheduleReconcile_ResolvesProviderFromSecretNameFallback(t *testing.T) {
	now := time.Now()
	instance := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "inst-b",
			Namespace: "default",
		},
		Spec: sympoziumv1alpha1.AgentSpec{
			Agents: sympoziumv1alpha1.AgentsSpec{
				Default: sympoziumv1alpha1.AgentConfig{
					Model: "gpt-4.1",
				},
			},
			AuthRefs: []sympoziumv1alpha1.SecretRef{
				{Secret: "inst-b-azure-openai-key"},
			},
		},
	}
	schedule := &sympoziumv1alpha1.SympoziumSchedule{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "inst-b-heartbeat",
			Namespace:         "default",
			CreationTimestamp: metav1.NewTime(now.Add(-2 * time.Minute)),
		},
		Spec: sympoziumv1alpha1.SympoziumScheduleSpec{
			AgentRef: "inst-b",
			Schedule: "* * * * *",
			Task:     "heartbeat",
			Type:     "heartbeat",
		},
	}

	r, cl := newScheduleTestReconciler(t, instance, schedule)
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: schedule.Name, Namespace: schedule.Namespace},
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	run := &sympoziumv1alpha1.AgentRun{}
	if err := cl.Get(context.Background(), types.NamespacedName{
		Name:      schedule.Name + "-1",
		Namespace: schedule.Namespace,
	}, run); err != nil {
		t.Fatalf("get created run: %v", err)
	}

	if run.Spec.Model.Provider != "azure-openai" {
		t.Fatalf("provider = %q, want azure-openai", run.Spec.Model.Provider)
	}
	if run.Spec.Model.AuthSecretRef != "inst-b-azure-openai-key" {
		t.Fatalf("authSecretRef = %q, want inst-b-azure-openai-key", run.Spec.Model.AuthSecretRef)
	}
}

// pipelineScheduleFixtures builds an instance, a pipeline ensemble, and a due
// schedule for the pipeline head, sharing the ensemble label.
func pipelineScheduleFixtures(now time.Time) (*sympoziumv1alpha1.Agent, *sympoziumv1alpha1.Ensemble, *sympoziumv1alpha1.SympoziumSchedule) {
	instance := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "pipe-head", Namespace: "default"},
		Spec: sympoziumv1alpha1.AgentSpec{
			Agents: sympoziumv1alpha1.AgentsSpec{
				Default: sympoziumv1alpha1.AgentConfig{Model: "claude-3-5-sonnet"},
			},
		},
	}
	ensemble := &sympoziumv1alpha1.Ensemble{
		ObjectMeta: metav1.ObjectMeta{Name: "pipe", Namespace: "default"},
		Spec: sympoziumv1alpha1.EnsembleSpec{
			WorkflowType: "pipeline",
			Relationships: []sympoziumv1alpha1.AgentConfigRelationship{
				{Source: "head", Target: "tail", Type: "sequential"},
			},
		},
	}
	schedule := &sympoziumv1alpha1.SympoziumSchedule{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "pipe-head-schedule",
			Namespace:         "default",
			CreationTimestamp: metav1.NewTime(now.Add(-2 * time.Minute)),
			Labels:            map[string]string{"sympozium.ai/ensemble": "pipe"},
		},
		Spec: sympoziumv1alpha1.SympoziumScheduleSpec{
			AgentRef: "pipe-head",
			Schedule: "* * * * *",
			Task:     "kick off pipeline",
			Type:     "sweep",
		},
	}
	return instance, ensemble, schedule
}

// A pipeline head's schedule must NOT fire while another run in the same
// ensemble (e.g. a sequential successor) is still active.
func TestSympoziumScheduleReconcile_SkipsWhilePipelineInFlight(t *testing.T) {
	now := time.Now()
	instance, ensemble, schedule := pipelineScheduleFixtures(now)

	// A successor run from a previous pass is still running.
	activeRun := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pipe-tail-seq-42",
			Namespace: "default",
			Labels:    map[string]string{"sympozium.ai/ensemble": "pipe"},
		},
		Status: sympoziumv1alpha1.AgentRunStatus{Phase: sympoziumv1alpha1.AgentRunPhaseRunning},
	}

	r, cl := newScheduleTestReconciler(t, instance, ensemble, schedule, activeRun)
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: schedule.Name, Namespace: schedule.Namespace},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	run := &sympoziumv1alpha1.AgentRun{}
	err := cl.Get(context.Background(), types.NamespacedName{
		Name:      schedule.Name + "-1",
		Namespace: schedule.Namespace,
	}, run)
	if err == nil {
		t.Fatalf("expected no new run while pipeline in flight, but %s was created", schedule.Name+"-1")
	}
}

// With no active run in the ensemble, the pipeline head's schedule fires and the
// created run carries the ensemble label so future in-flight checks see it.
func TestSympoziumScheduleReconcile_FiresWhenPipelineIdle(t *testing.T) {
	now := time.Now()
	instance, ensemble, schedule := pipelineScheduleFixtures(now)

	r, cl := newScheduleTestReconciler(t, instance, ensemble, schedule)
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: schedule.Name, Namespace: schedule.Namespace},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	run := &sympoziumv1alpha1.AgentRun{}
	if err := cl.Get(context.Background(), types.NamespacedName{
		Name:      schedule.Name + "-1",
		Namespace: schedule.Namespace,
	}, run); err != nil {
		t.Fatalf("expected run to be created when pipeline idle: %v", err)
	}
	if run.Labels["sympozium.ai/ensemble"] != "pipe" {
		t.Errorf("scheduled run ensemble label = %q, want pipe", run.Labels["sympozium.ai/ensemble"])
	}
}

// TestSympoziumScheduleReconcile_PersistsRunTimeout verifies that an
// instance-level RunTimeout is persisted onto the scheduled AgentRun's
// Spec.Timeout. This is the binding gate for the controller watchdog: without
// a persisted timeout, scheduled/sweep runs are killed at the 10m hard default
// regardless of Job activeDeadlineSeconds or the runner's RUN_TIMEOUT env.
func TestSympoziumScheduleReconcile_PersistsRunTimeout(t *testing.T) {
	now := time.Now()
	instance := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "inst-slow",
			Namespace: "default",
		},
		Spec: sympoziumv1alpha1.AgentSpec{
			Agents: sympoziumv1alpha1.AgentsSpec{
				Default: sympoziumv1alpha1.AgentConfig{
					Model:      "claude-3-5-sonnet",
					RunTimeout: "30m",
				},
			},
			AuthRefs: []sympoziumv1alpha1.SecretRef{
				{Provider: "anthropic", Secret: "inst-slow-anthropic-key"},
			},
		},
	}
	schedule := &sympoziumv1alpha1.SympoziumSchedule{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "inst-slow-sweep",
			Namespace:         "default",
			CreationTimestamp: metav1.NewTime(now.Add(-2 * time.Minute)),
		},
		Spec: sympoziumv1alpha1.SympoziumScheduleSpec{
			AgentRef: "inst-slow",
			Schedule: "* * * * *",
			Task:     "sweep",
			Type:     "sweep",
		},
	}

	r, cl := newScheduleTestReconciler(t, instance, schedule)
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: schedule.Name, Namespace: schedule.Namespace},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	run := &sympoziumv1alpha1.AgentRun{}
	if err := cl.Get(context.Background(), types.NamespacedName{
		Name:      schedule.Name + "-1",
		Namespace: schedule.Namespace,
	}, run); err != nil {
		t.Fatalf("get created run: %v", err)
	}

	if run.Spec.Timeout == nil {
		t.Fatalf("expected Spec.Timeout to be persisted, got nil")
	}
	if got, want := run.Spec.Timeout.Duration, 30*time.Minute; got != want {
		t.Fatalf("Spec.Timeout = %s, want %s", got, want)
	}
}

// The Forbid concurrency gate must treat every non-terminal phase as active.
// AwaitingDelegate and PostRunning were both missing from the phase set, so an
// orchestrator parked on delegate_to_persona read as finished and the next cron
// tick stacked a second run on top of the live one.
func TestSympoziumScheduleReconcile_ForbidBlocksOnNonTerminalPhases(t *testing.T) {
	cases := []struct {
		phase     sympoziumv1alpha1.AgentRunPhase
		wantFires bool
	}{
		{sympoziumv1alpha1.AgentRunPhaseAwaitingDelegate, false},
		{sympoziumv1alpha1.AgentRunPhasePostRunning, false},
		{sympoziumv1alpha1.AgentRunPhasePending, false},
		{sympoziumv1alpha1.AgentRunPhaseRunning, false},
		{sympoziumv1alpha1.AgentRunPhaseServing, false},
		{"", false},
		{sympoziumv1alpha1.AgentRunPhaseSucceeded, true},
		{sympoziumv1alpha1.AgentRunPhaseFailed, true},
		{sympoziumv1alpha1.AgentRunPhaseSkipped, true},
	}

	for _, tc := range cases {
		name := string(tc.phase)
		if name == "" {
			name = "Unobserved"
		}
		t.Run(name, func(t *testing.T) {
			now := time.Now()
			instance := &sympoziumv1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{Name: "inst-forbid", Namespace: "default"},
				Spec: sympoziumv1alpha1.AgentSpec{
					Agents: sympoziumv1alpha1.AgentsSpec{
						Default: sympoziumv1alpha1.AgentConfig{Model: "claude-3-5-sonnet"},
					},
				},
			}
			schedule := &sympoziumv1alpha1.SympoziumSchedule{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "inst-forbid-sweep",
					Namespace:         "default",
					CreationTimestamp: metav1.NewTime(now.Add(-2 * time.Minute)),
				},
				Spec: sympoziumv1alpha1.SympoziumScheduleSpec{
					AgentRef: "inst-forbid",
					Schedule: "* * * * *",
					Task:     "sweep",
					Type:     "sweep",
					// The CRD defaults this to Forbid, but the fake client
					// applies no defaulting, so set it explicitly.
					ConcurrencyPolicy: "Forbid",
				},
			}
			// The schedule's previous run. Carries only the schedule label, so
			// the Forbid gate is the only gate that can see it — the
			// serving-run gate further down matches on sympozium.ai/instance.
			prevRun := &sympoziumv1alpha1.AgentRun{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "inst-forbid-sweep-1",
					Namespace: "default",
					Labels:    map[string]string{"sympozium.ai/schedule": "inst-forbid-sweep"},
				},
				Status: sympoziumv1alpha1.AgentRunStatus{Phase: tc.phase},
			}

			r, cl := newScheduleTestReconciler(t, instance, schedule, prevRun)
			if _, err := r.Reconcile(context.Background(), ctrl.Request{
				NamespacedName: types.NamespacedName{Name: schedule.Name, Namespace: schedule.Namespace},
			}); err != nil {
				t.Fatalf("reconcile: %v", err)
			}

			nextName := "inst-forbid-sweep-2"
			err := cl.Get(context.Background(), types.NamespacedName{
				Name:      nextName,
				Namespace: "default",
			}, &sympoziumv1alpha1.AgentRun{})

			switch {
			case tc.wantFires && err != nil:
				t.Fatalf("phase %q is terminal: schedule should have fired and created %s, got: %v",
					tc.phase, nextName, err)
			case !tc.wantFires && err == nil:
				t.Fatalf("phase %q is still active: Forbid should have blocked the tick, but %s was created",
					tc.phase, nextName)
			}
		})
	}
}
