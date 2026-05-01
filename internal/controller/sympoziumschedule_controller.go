package controller

import (
	"context"
	"fmt"
	"strconv"
	"strings"
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

// maxScheduleRunCreateRetries bounds the collision-retry loop when the
// scheduler's chosen run suffix clashes with an existing AgentRun (e.g. from
// a prior incarnation of this schedule before it was deleted and recreated).
const maxScheduleRunCreateRetries = 100

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
	// (LastRunTime is nil) we backdate by one full cron interval so that
	// sched.Next() finds exactly one past tick — triggering one immediate
	// run without the duplicates caused by a 24h backdate.
	var lastRun time.Time
	if schedule.Status.LastRunTime != nil {
		lastRun = schedule.Status.LastRunTime.Time
	} else {
		// First run — compute the interval between two consecutive ticks
		// and backdate by that amount so the first tick lands before now.
		firstTick := sched.Next(now)
		secondTick := sched.Next(firstTick)
		interval := secondTick.Sub(firstTick)
		lastRun = now.Add(-interval)
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
		client.MatchingLabels{"sympozium.ai/instance": schedule.Spec.AgentRef},
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
		memoryContent := r.readMemoryConfigMap(ctx, schedule.Namespace, schedule.Spec.AgentRef)
		if memoryContent != "" {
			task = fmt.Sprintf("## Memory Context\n%s\n\n## Task\n%s", memoryContent, task)
		}
	}

	// Look up instance to get model config.
	instance := &sympoziumv1alpha1.Agent{}
	if err := r.Get(ctx, client.ObjectKey{
		Namespace: schedule.Namespace,
		Name:      schedule.Spec.AgentRef,
	}, instance); err != nil {
		log.Error(err, "instance not found", "instance", schedule.Spec.AgentRef)
		schedule.Status.Phase = "Error"
		_ = r.Status().Update(ctx, schedule)
		return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
	}

	// Pick the next available run number. The naive `TotalRuns+1` choice
	// collides when this schedule has been deleted and recreated (e.g.
	// Ensemble disabled then re-enabled) because the counter resets
	// but the old AgentRun resources persist. List existing runs that
	// belong to this schedule, find the highest numeric suffix, and
	// start from there.
	nextNum, err := r.nextScheduledRunNumber(ctx, schedule)
	if err != nil {
		log.Error(err, "failed to list existing scheduled runs; falling back to TotalRuns counter")
		nextNum = int(schedule.Status.TotalRuns) + 1
	}

	// Create the AgentRun.
	runName := fmt.Sprintf("%s-%d", schedule.Name, nextNum)
	agentRun := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      runName,
			Namespace: schedule.Namespace,
			Labels: map[string]string{
				"sympozium.ai/instance": schedule.Spec.AgentRef,
				"sympozium.ai/schedule": schedule.Name,
				"sympozium.ai/type":     schedule.Spec.Type,
			},
		},
		Spec: sympoziumv1alpha1.AgentRunSpec{
			AgentRef: schedule.Spec.AgentRef,
			Task:     task,
			AgentID:  fmt.Sprintf("schedule-%s", schedule.Name),
			Model: sympoziumv1alpha1.ModelSpec{
				Provider: resolveProvider(instance),
				Model:    instance.Spec.Agents.Default.Model,
			},
			ImagePullSecrets: instance.Spec.ImagePullSecrets,
			Lifecycle:        instance.Spec.Agents.Default.Lifecycle,
			Volumes:          instance.Spec.Volumes,
			VolumeMounts:     instance.Spec.VolumeMounts,
		},
	}

	// Enable canary mode for system canary runs.
	if instance.Labels["sympozium.ai/ensemble"] == "system-canary" {
		agentRun.Spec.CanaryMode = true
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

	// Resolve auth secret from the instance.
	agentRun.Spec.Model.AuthSecretRef = resolveAuthSecret(instance)

	// Copy skill refs, excluding server-mode skills (e.g. web-endpoint) that
	// should not be spawned as ephemeral schedule runs.
	for _, skill := range instance.Spec.Skills {
		if skill.SkillPackRef == "web-endpoint" || skill.SkillPackRef == "skillpack-web-endpoint" {
			continue
		}
		agentRun.Spec.Skills = append(agentRun.Spec.Skills, skill)
	}

	// Link the AgentRun to its parent Schedule via a controller owner
	// reference. When the Schedule is deleted (e.g. Ensemble is
	// disabled and its owned Schedule is cascade-deleted), Kubernetes
	// garbage collection will remove the AgentRuns too — so disabling
	// a pack no longer leaves orphan Failed runs cluttering the UX
	// with "instance not found" errors from policy validation.
	if err := controllerutil.SetControllerReference(schedule, agentRun, r.Scheme); err != nil {
		log.Error(err, "failed to set controller reference on AgentRun")
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// Collision-retry loop: if another actor (or a race with our own
	// prior reconcile) created the same name, bump the suffix and try
	// again. Bounded to avoid an unbounded spin.
	for attempt := 0; attempt < maxScheduleRunCreateRetries; attempt++ {
		err := r.Create(ctx, agentRun)
		if err == nil {
			break
		}
		if !errors.IsAlreadyExists(err) {
			log.Error(err, "failed to create AgentRun")
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		log.Info("scheduled run name collision; trying next suffix",
			"run", runName, "attempt", attempt+1)
		nextNum++
		runName = fmt.Sprintf("%s-%d", schedule.Name, nextNum)
		agentRun.ObjectMeta.Name = runName
		if attempt == maxScheduleRunCreateRetries-1 {
			log.Error(fmt.Errorf("exceeded collision retries"),
				"could not pick a unique scheduled run name", "lastAttempted", runName)
			return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
		}
	}

	log.Info("Created scheduled AgentRun", "run", runName, "type", schedule.Spec.Type)

	// Update status. Use the actual number that was committed (after any
	// collision retries) so TotalRuns stays in sync with observable state.
	nowMeta := metav1.Now()
	schedule.Status.LastRunTime = &nowMeta
	schedule.Status.LastRunName = runName
	if int64(nextNum) > schedule.Status.TotalRuns {
		schedule.Status.TotalRuns = int64(nextNum)
	}

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

// nextScheduledRunNumber returns the next numeric suffix to use when naming
// a scheduled AgentRun, picking the higher of:
//
//   - status.TotalRuns + 1 (the counter the scheduler maintains), and
//   - 1 + the maximum observed suffix on existing AgentRuns that belong to
//     this schedule (identified via the sympozium.ai/schedule label).
//
// The second branch handles the case where this schedule was previously
// deleted (e.g. Ensemble disabled) and recreated: TotalRuns resets to 0
// but orphan AgentRuns from the previous incarnation persist with names like
// `<schedule>-1`, `<schedule>-2`, … Picking a suffix that's already in use
// would collide silently and leave the scheduler emitting ghost "created"
// log lines without actually creating runs.
func (r *SympoziumScheduleReconciler) nextScheduledRunNumber(ctx context.Context, schedule *sympoziumv1alpha1.SympoziumSchedule) (int, error) {
	base := int(schedule.Status.TotalRuns) + 1
	var runs sympoziumv1alpha1.AgentRunList
	if err := r.List(ctx, &runs,
		client.InNamespace(schedule.Namespace),
		client.MatchingLabels{"sympozium.ai/schedule": schedule.Name},
	); err != nil {
		return base, err
	}
	prefix := schedule.Name + "-"
	maxObserved := 0
	for i := range runs.Items {
		name := runs.Items[i].Name
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		suffix := strings.TrimPrefix(name, prefix)
		n, err := strconv.Atoi(suffix)
		if err != nil || n <= 0 {
			continue
		}
		if n > maxObserved {
			maxObserved = n
		}
	}
	if maxObserved+1 > base {
		return maxObserved + 1, nil
	}
	return base, nil
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
