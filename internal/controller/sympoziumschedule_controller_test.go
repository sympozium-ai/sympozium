package controller

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

	if run.Spec.Model.Provider != "anthropic" {
		t.Fatalf("provider = %q, want anthropic", run.Spec.Model.Provider)
	}
	if run.Spec.Model.AuthSecretRef != "inst-a-anthropic-key" {
		t.Fatalf("authSecretRef = %q, want inst-a-anthropic-key", run.Spec.Model.AuthSecretRef)
	}

	agentContainers := (&AgentRunReconciler{}).buildContainers(run, false, nil, nil)
	if len(agentContainers) == 0 || len(agentContainers[0].EnvFrom) == 0 || agentContainers[0].EnvFrom[0].SecretRef == nil {
		t.Fatalf("expected scheduled run auth secret to be mounted via envFrom")
	}
	if got := agentContainers[0].EnvFrom[0].SecretRef.Name; got != "inst-a-anthropic-key" {
		t.Fatalf("mounted secret = %q, want inst-a-anthropic-key", got)
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

