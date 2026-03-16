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

	sympoziumv1alpha1 "github.com/alexsjones/sympozium/api/v1alpha1"
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
	instance := &sympoziumv1alpha1.SympoziumInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "inst-a",
			Namespace: "default",
		},
		Spec: sympoziumv1alpha1.SympoziumInstanceSpec{
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
			InstanceRef: "inst-a",
			Schedule:    "* * * * *",
			Task:        "heartbeat",
			Type:        "heartbeat",
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
	if len(run.OwnerReferences) != 0 {
		t.Fatalf("expected scheduled run to have no owner references, got %d", len(run.OwnerReferences))
	}

	if run.Spec.Model.Provider != "anthropic" {
		t.Fatalf("provider = %q, want anthropic", run.Spec.Model.Provider)
	}
	if run.Spec.Model.AuthSecretRef != "inst-a-anthropic-key" {
		t.Fatalf("authSecretRef = %q, want inst-a-anthropic-key", run.Spec.Model.AuthSecretRef)
	}

	agentContainers, _ := (&AgentRunReconciler{}).buildContainers(run, false, nil, nil, nil)
	if len(agentContainers) == 0 || len(agentContainers[0].EnvFrom) == 0 || agentContainers[0].EnvFrom[0].SecretRef == nil {
		t.Fatalf("expected scheduled run auth secret to be mounted via envFrom")
	}
	if got := agentContainers[0].EnvFrom[0].SecretRef.Name; got != "inst-a-anthropic-key" {
		t.Fatalf("mounted secret = %q, want inst-a-anthropic-key", got)
	}
}

func TestSympoziumScheduleReconcile_FiltersWebEndpointSkill(t *testing.T) {
	now := time.Now()
	instance := &sympoziumv1alpha1.SympoziumInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "inst-web",
			Namespace: "default",
		},
		Spec: sympoziumv1alpha1.SympoziumInstanceSpec{
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
			InstanceRef: "inst-web",
			Schedule:    "* * * * *",
			Task:        "heartbeat",
			Type:        "heartbeat",
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
	instance := &sympoziumv1alpha1.SympoziumInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "inst-serving",
			Namespace: "default",
		},
		Spec: sympoziumv1alpha1.SympoziumInstanceSpec{
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
			InstanceRef: "inst-serving",
			AgentID:     "web-endpoint",
			SessionKey:  "web",
			Task:        "serve",
			Mode:        "server",
			Model: sympoziumv1alpha1.ModelSpec{
				Provider:      "openai",
				Model:         "gpt-4o",
				AuthSecretRef: "inst-serving-openai-key",
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
			InstanceRef: "inst-serving",
			Schedule:    "* * * * *",
			Task:        "heartbeat",
			Type:        "heartbeat",
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

func TestSympoziumScheduleReconcile_ResolvesProviderFromSecretNameFallback(t *testing.T) {
	now := time.Now()
	instance := &sympoziumv1alpha1.SympoziumInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "inst-b",
			Namespace: "default",
		},
		Spec: sympoziumv1alpha1.SympoziumInstanceSpec{
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
			InstanceRef: "inst-b",
			Schedule:    "* * * * *",
			Task:        "heartbeat",
			Type:        "heartbeat",
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
