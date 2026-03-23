package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"github.com/robfig/cron/v3"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

const sympoziumScheduleFinalizer = "sympozium.ai/schedule-finalizer"

// SympoziumScheduleReconciler reconciles SympoziumSchedule objects.
type SympoziumScheduleReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Log    logr.Logger
}

// +kubebuilder:rbac:groups=sympozium.ai,resources=sympoziumschedules,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=sympozium.ai,resources=sympoziumschedules/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=sympozium.ai,resources=sympoziumschedules/finalizers,verbs=update

// Reconcile handles SympoziumSchedule create/update/delete events.
func (r *SympoziumScheduleReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("sympoziumschedule", req.NamespacedName)

	schedule := &sympoziumv1alpha1.SympoziumSchedule{}
	if err := r.Get(ctx, req.NamespacedName, schedule); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Handle deletion.
	if !schedule.DeletionTimestamp.IsZero() {
		controllerutil.RemoveFinalizer(schedule, sympoziumScheduleFinalizer)
		return ctrl.Result{}, r.Update(ctx, schedule)
	}

	// Add finalizer.
	if !controllerutil.ContainsFinalizer(schedule, sympoziumScheduleFinalizer) {
		controllerutil.AddFinalizer(schedule, sympoziumScheduleFinalizer)
		if err := r.Update(ctx, schedule); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Handle suspended schedules.
	if schedule.Spec.Suspend {
		if schedule.Status.Phase != "Suspended" {
			schedule.Status.Phase = "Suspended"
			_ = r.Status().Update(ctx, schedule)
		}
		return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
	}

	// Parse the cron schedule.
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	sched, err := parser.Parse(schedule.Spec.Schedule)
	if err != nil {
		log.Error(err, "invalid cron expression", "schedule", schedule.Spec.Schedule)
		schedule.Status.Phase = "Error"
		_ = r.Status().Update(ctx, schedule)
		return ctrl.Result{}, nil
	}

	now := time.Now()

	// Compute next run time from last run.  When a schedule has never fired
	// (LastRunTime is nil) we use a time far enough in the past that
	// sched.Next() returns a time before now, causing an immediate trigger.
	var lastRun time.Time
	if schedule.Status.LastRunTime != nil {
		lastRun = schedule.Status.LastRunTime.Time
	} else {
		// First run — trigger immediately by pretending the last run was
		// long ago so the next computed cron tick is already in the past.
		lastRun = schedule.CreationTimestamp.Time.Add(-24 * time.Hour)
	}
	nextRun := sched.Next(lastRun)

	// Update status with next run time.
	nextRunMeta := metav1.NewTime(nextRun)
	schedule.Status.NextRunTime = &nextRunMeta
	schedule.Status.Phase = "Active"

	// Check if it's time to fire.
	if now.Before(nextRun) {
		delay := nextRun.Sub(now)
		if delay > 60*time.Second {
			delay = 60 * time.Second
		}
		_ = r.Status().Update(ctx, schedule)
		return ctrl.Result{RequeueAfter: delay}, nil
	}

	// Check concurrency policy.
	if schedule.Spec.ConcurrencyPolicy == "Forbid" && schedule.Status.LastRunName != "" {
		lastAgentRun := &sympoziumv1alpha1.AgentRun{}
		if err := r.Get(ctx, client.ObjectKey{
			Namespace: schedule.Namespace,
			Name:      schedule.Status.LastRunName,
		}, lastAgentRun); err == nil {
			if lastAgentRun.Status.Phase == sympoziumv1alpha1.AgentRunPhaseRunning ||
				lastAgentRun.Status.Phase == sympoziumv1alpha1.AgentRunPhasePending ||
				lastAgentRun.Status.Phase == sympoziumv1alpha1.AgentRunPhaseServing ||
				lastAgentRun.Status.Phase == "" {
				log.Info("Skipping trigger — previous run still active (Forbid policy)")
				_ = r.Status().Update(ctx, schedule)
				return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
			}
		}
	}

	// Skip if there's already a serving AgentRun for this instance.
	// A serving run means a web-proxy Deployment is active — no need to
	// spawn additional heartbeat runs.
	var allRuns sympoziumv1alpha1.AgentRunList
	if err := r.List(ctx, &allRuns,
		client.InNamespace(schedule.Namespace),
		client.MatchingLabels{"sympozium.ai/instance": schedule.Spec.InstanceRef},
	); err == nil {
		for _, run := range allRuns.Items {
			if run.Status.Phase == sympoziumv1alpha1.AgentRunPhaseServing {
				log.Info("Skipping trigger — instance has a serving AgentRun",
					"servingRun", run.Name)
				_ = r.Status().Update(ctx, schedule)
				return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
			}
		}
	}

	// Build the task, optionally including memory context.
	task := schedule.Spec.Task
	if schedule.Spec.IncludeMemory {
		memoryContent := r.readMemoryConfigMap(ctx, schedule.Namespace, schedule.Spec.InstanceRef)
		if memoryContent != "" {
			task = fmt.Sprintf("## Memory Context\n%s\n\n## Task\n%s", memoryContent, task)
		}
	}

	// Look up instance to get model config.
	instance := &sympoziumv1alpha1.SympoziumInstance{}
	if err := r.Get(ctx, client.ObjectKey{
		Namespace: schedule.Namespace,
		Name:      schedule.Spec.InstanceRef,
	}, instance); err != nil {
		log.Error(err, "instance not found", "instance", schedule.Spec.InstanceRef)
		schedule.Status.Phase = "Error"
		_ = r.Status().Update(ctx, schedule)
		return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
	}

	// Create the AgentRun.
	runName := fmt.Sprintf("%s-%d", schedule.Name, schedule.Status.TotalRuns+1)
	agentRun := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      runName,
			Namespace: schedule.Namespace,
			Labels: map[string]string{
				"sympozium.ai/instance": schedule.Spec.InstanceRef,
				"sympozium.ai/schedule": schedule.Name,
				"sympozium.ai/type":     schedule.Spec.Type,
			},
		},
		Spec: sympoziumv1alpha1.AgentRunSpec{
			InstanceRef: schedule.Spec.InstanceRef,
			Task:        task,
			AgentID:     fmt.Sprintf("schedule-%s", schedule.Name),
			Model: sympoziumv1alpha1.ModelSpec{
				Provider: resolveProvider(instance),
				Model:    instance.Spec.Agents.Default.Model,
			},
		},
	}

	// Copy model config from instance.
	if instance.Spec.Agents.Default.BaseURL != "" {
		agentRun.Spec.Model.BaseURL = instance.Spec.Agents.Default.BaseURL
	}
	if instance.Spec.Agents.Default.Thinking != "" {
		agentRun.Spec.Model.Thinking = instance.Spec.Agents.Default.Thinking
	}
	if len(instance.Spec.Agents.Default.NodeSelector) > 0 {
		agentRun.Spec.Model.NodeSelector = instance.Spec.Agents.Default.NodeSelector
	}

	// Resolve auth secret and mode from the instance.
	agentRun.Spec.Model.AuthSecretRef = resolveAuthSecret(instance)
	agentRun.Spec.Model.AuthMode = resolveAuthMode(instance)

	// Copy skill refs, excluding server-mode skills (e.g. web-endpoint) that
	// should not be spawned as ephemeral schedule runs.
	for _, skill := range instance.Spec.Skills {
		if skill.SkillPackRef == "web-endpoint" || skill.SkillPackRef == "skillpack-web-endpoint" {
			continue
		}
		agentRun.Spec.Skills = append(agentRun.Spec.Skills, skill)
	}

	if err := r.Create(ctx, agentRun); err != nil {
		if !errors.IsAlreadyExists(err) {
			log.Error(err, "failed to create AgentRun")
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
	}

	log.Info("Created scheduled AgentRun", "run", runName, "type", schedule.Spec.Type)

	// Update status.
	nowMeta := metav1.Now()
	schedule.Status.LastRunTime = &nowMeta
	schedule.Status.LastRunName = runName
	schedule.Status.TotalRuns++

	// Recompute next run from now.
	next := sched.Next(now)
	nextMeta := metav1.NewTime(next)
	schedule.Status.NextRunTime = &nextMeta

	_ = r.Status().Update(ctx, schedule)

	delay := next.Sub(now)
	if delay > 60*time.Second {
		delay = 60 * time.Second
	}
	return ctrl.Result{RequeueAfter: delay}, nil
}

// readMemoryConfigMap reads the MEMORY.md content from the instance's memory
// ConfigMap. Returns empty string if not found.
func (r *SympoziumScheduleReconciler) readMemoryConfigMap(ctx context.Context, namespace, instanceName string) string {
	cmName := fmt.Sprintf("%s-memory", instanceName)
	var configMap corev1.ConfigMap
	if err := r.Get(ctx, client.ObjectKey{
		Namespace: namespace,
		Name:      cmName,
	}, &configMap); err != nil {
		return ""
	}
	return configMap.Data["MEMORY.md"]
}

// SetupWithManager sets up the controller with the Manager.
func (r *SympoziumScheduleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&sympoziumv1alpha1.SympoziumSchedule{}).
		Owns(&sympoziumv1alpha1.AgentRun{}).
		Complete(r)
}
