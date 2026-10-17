package controller

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/ipc"
	"github.com/sympozium-ai/sympozium/internal/pricing"
)

// In-place gate retry keeps the agent pod alive across a gate cycle instead of
// cloning the spec onto a successor AgentRun (agentrun_retry.go).
//
// A successor loses three things the retried attempt needs: the conversation,
// the workspace, and warm state (sidecars, MCP connections, prefix cache).
// Parking loses none of them. The runner holds its live provider, the gate Job
// reads the same workspace, and the verdict arrives as a user turn.
//
// Flow, one attempt:
//
//	Running        agent works, then parks and prints an attempt marker
//	AwaitingGate   controller records the attempt, runs the gate Job
//	retry          "continue" goes into the gate ConfigMap, attempt++, back to Running
//	anything else  "stop" releases the pod, run resolves through resolveGate
//
// The log marker and the gate ConfigMap are the transport; the CR stays the
// system of record. Every UI surface,
// maxChainTokens and maxAttempts read AgentRun.status, so each attempt is
// recorded in status.attempts[] as it lands.
//
// Pod death (eviction, OOM, node loss) is not recovered from. A retry verdict
// for a pod that is no longer parked resolves the run on the attempt it has
// (agentStillParked). This branches in front of the successor-clone path and
// leaves it intact.

const (
	// gateVerdictAttemptAnnotation records which attempt a manually-submitted
	// verdict was written against, so a stale browser tab cannot resolve an
	// attempt that was already consumed. Gate hooks do not set it: a hook's
	// verdict comes from the Job judging the current attempt.
	gateVerdictAttemptAnnotation = "sympozium.ai/gate-verdict-attempt"

	// gateChannelVolume is the agent pod volume that mounts the run's gate
	// ConfigMap read-only at ipc.GateDir.
	gateChannelVolume = "gate"

	// gateVerdictSeqAnnotation is set on the agent pod after each verdict
	// write. kubelet copies a ConfigMap change into a mounted volume only when
	// it syncs the pod, about once a minute on its own. Any update to the pod
	// object starts that sync at once: about half a second instead of a minute.
	gateVerdictSeqAnnotation = "sympozium.ai/gate-verdict-seq"

	// attemptTextMaxChars bounds AttemptStatus.Result and .GateReason. Both
	// come from outside the control plane, and status grows by one entry per
	// attempt, so unbounded text could push the object past the apiserver's
	// size limit and make the run unpatchable.
	attemptTextMaxChars = 2000
)

// gateInPlaceEnabled reports whether this run parks between attempts rather
// than being superseded by a successor.
//
// A gate with no retry has nothing to continue into, and retry with no gate has
// nothing to park for, so both are required. lifecycle.retry.inPlace is the
// operator's switch on top of that. It defaults off, so a run written for
// successor retry keeps that behaviour.
//
// Backends are not named here. Every caller is already inside a path that
// builds or owns a pod, and a backend with no pod to park in cannot reach
// them: reconcilePending forks to the Celln backend before any pod is
// rendered, and reconcileRunning forks before checkParkedAttempt. Such a run
// falls through to the successor-clone retry on its own.
func gateInPlaceEnabled(agentRun *sympoziumv1alpha1.AgentRun) bool {
	spec := gateRetrySpec(agentRun)
	if spec == nil || !hasResponseGateHook(agentRun) {
		return false
	}
	// The park loop is agent-runner's. A mode that replaces it (harness) has
	// nothing that parks, and runs model-chosen tools in the agent container,
	// where the gate key would let the model sign its own attempt markers.
	if taskModeReplacesAgentContainer(agentRun.Spec.Task) {
		return false
	}
	// There is no mutating webhook, so the kubebuilder default is not
	// guaranteed to have been applied — resolve nil here (see CLAUDE.md).
	return spec.InPlace != nil && *spec.InPlace
}

// gateParkBudget is how long one park may last: the gate Job's own budget plus
// slack for scheduling it and for the controller to observe the result. The
// runner uses it as its park timeout and the Job deadline is sized from it, so
// both bounds move together when a gate hook's timeout changes.
func gateParkBudget(agentRun *sympoziumv1alpha1.AgentRun) time.Duration {
	return postRunBudget(agentRun.Spec.Lifecycle) + 5*time.Minute
}

// parseAttemptResultFromLogs returns the attempt result for `want`, or nil
// when the runner has not published it yet.
//
// It matches on the attempt number rather than taking the last marker: the log
// holds every attempt this pod has published, and acting on attempt 1's marker
// while attempt 2 runs would gate the wrong answer.
//
// Only a marker signed with the run's key counts. The runner logs model-chosen
// strings, so the model can print a marker of its own mid-attempt; unsigned,
// that would start the gate early and report whatever token count it liked
// against maxChainTokens. The final-result marker does not need this: it is
// read after the container exits, and LastIndex finds the runner's own.
func parseAttemptResultFromLogs(logs string, want int, key []byte, log logr.Logger) *ipc.AttemptResult {
	rest := logs
	for {
		start := strings.LastIndex(rest, ipc.AttemptMarkerStart)
		if start < 0 {
			return nil
		}
		payload := rest[start+len(ipc.AttemptMarkerStart):]
		rest = rest[:start]

		end := strings.Index(payload, ipc.AttemptMarkerEnd)
		if end < 0 {
			continue
		}
		parsed, err := ipc.ParseAttemptMarker(strings.TrimSpace(payload[:end]))
		if err != nil {
			log.V(1).Info("could not parse attempt marker", "err", err)
			continue
		}
		if parsed.Attempt != want {
			continue
		}
		if !parsed.Verify(key) {
			log.Info("ignoring an attempt marker not signed with the run's gate key", "attempt", parsed.Attempt)
			continue
		}
		sanitizeAttemptMetrics(&parsed, log)
		return &parsed
	}
}

// sanitizeAttemptMetrics drops negative agent-reported metrics and clamps the
// rest, as parseAgentResultFromLogs does for the final result. They feed the
// same budgets: a negative count would decrement the shared ensemble ledger,
// an absurd one would exhaust it.
func sanitizeAttemptMetrics(a *ipc.AttemptResult, log logr.Logger) {
	if a.Metrics.InputTokens < 0 || a.Metrics.OutputTokens < 0 ||
		a.Metrics.ToolCalls < 0 || a.Metrics.DurationMs < 0 {
		log.Info("dropping negative agent-reported attempt metrics", "attempt", a.Attempt)
		a.Metrics.InputTokens = 0
		a.Metrics.OutputTokens = 0
		a.Metrics.ToolCalls = 0
		a.Metrics.DurationMs = 0
	}
	a.Metrics.InputTokens = min(a.Metrics.InputTokens, maxAgentReportedMetric)
	a.Metrics.OutputTokens = min(a.Metrics.OutputTokens, maxAgentReportedMetric)
	a.Metrics.ToolCalls = min(a.Metrics.ToolCalls, maxAgentReportedMetric)
	a.Metrics.DurationMs = min(a.Metrics.DurationMs, int64(maxAgentReportedMetric))
}

// attemptTokenUsage converts an attempt's metrics to a TokenUsage, nil when it
// reported none.
func attemptTokenUsage(a *ipc.AttemptResult) *sympoziumv1alpha1.TokenUsage {
	if a.Metrics.InputTokens == 0 && a.Metrics.OutputTokens == 0 {
		return nil
	}
	return &sympoziumv1alpha1.TokenUsage{
		InputTokens:  a.Metrics.InputTokens,
		OutputTokens: a.Metrics.OutputTokens,
		TotalTokens:  a.Metrics.InputTokens + a.Metrics.OutputTokens,
		ToolCalls:    a.Metrics.ToolCalls,
		DurationMs:   a.Metrics.DurationMs,
	}
}

// boundAttemptText truncates untrusted text destined for status, marking the
// cut so a reader never mistakes a fragment for the whole thing.
func boundAttemptText(s string) string {
	if len(s) <= attemptTextMaxChars {
		return s
	}
	return s[:attemptTextMaxChars] + fmt.Sprintf("\n\n[truncated: %d characters, first %d shown]",
		len(s), attemptTextMaxChars)
}

// checkParkedAttempt starts the gate once an attempt marker lands. Called from
// reconcileRunning: a parked pod never terminates, so it never trips the
// Job-completion paths.
//
// handled=false means the attempt is still working; reconcileRunning carries
// on with its ordinary checks.
func (r *AgentRunReconciler) checkParkedAttempt(
	ctx context.Context, log logr.Logger, agentRun *sympoziumv1alpha1.AgentRun,
) (handled bool, result ctrl.Result, err error) {
	if r.Clientset == nil || agentRun.Status.PodName == "" {
		return false, ctrl.Result{}, nil
	}

	// Parked means alive. Once the agent container exits its markers are
	// history, and gating one would start a cycle whose verdict has no pod to
	// go back to. reconcileRunning's ordinary completion path handles it.
	if done, _, _, _ := r.checkAgentContainer(ctx, log, agentRun); done {
		return false, ctrl.Result{}, nil
	}

	attempt := currentAttempt(agentRun)
	key, err := r.gateAttemptKey(ctx, agentRun)
	if err != nil {
		log.Info("cannot verify parked attempts without the gate key", "err", err.Error())
		return false, ctrl.Result{}, nil
	}
	logs, ok := r.readAgentLogs(ctx, log, agentRun)
	if !ok {
		return false, ctrl.Result{}, nil
	}

	parked := parseAttemptResultFromLogs(logs, attempt, key, log)
	if parked == nil {
		return false, ctrl.Result{}, nil
	}

	log.Info("Agent parked awaiting a gate verdict", "attempt", attempt)
	res, err := r.startGateForAttempt(ctx, log, agentRun, parked)
	return true, res, err
}

// startGateForAttempt records the attempt on the CR, then creates the gate Job
// that judges it.
//
// That order survives a controller restart between the two steps: the entry is
// already there, and a duplicate Create is absorbed by IsAlreadyExists.
func (r *AgentRunReconciler) startGateForAttempt(
	ctx context.Context, log logr.Logger,
	agentRun *sympoziumv1alpha1.AgentRun, parked *ipc.AttemptResult,
) (ctrl.Result, error) {
	now := metav1.Now()
	// Always 0: the runner parks only after a successful attempt. A failed one
	// ends the chain and exits, and reconcileRunning's completion path takes it.
	exitCode := int32(0)
	usage := attemptTokenUsage(parked)
	cost := r.estimateAttemptCost(ctx, agentRun, usage, now)

	if err := r.updateStatusWithRetry(ctx, agentRun, func(ar *sympoziumv1alpha1.AgentRun) {
		entry := ensureAttemptEntry(ar, parked.Attempt)
		entry.CompletedAt = &now
		entry.Result = boundAttemptText(parked.Response)
		entry.TokenUsage = usage
		entry.CostEstimate = cost

		ar.Status.ExitCode = &exitCode
		ar.Status.Result = parked.Response
		// Top-level usage is the chain's, not the attempt's: status.tokenUsage
		// means "what this run cost", which here is every attempt so far.
		ar.Status.TokenUsage = sumAttemptUsage(ar.Status.Attempts)
	}); err != nil {
		return ctrl.Result{}, fmt.Errorf("recording parked attempt %d: %w", parked.Attempt, err)
	}

	gateJob := r.buildPostRunJob(agentRun, exitCode, parked.Response)
	gateJob.Name = gateJobNameForAttempt(agentRun, parked.Attempt)
	r.pinGateJobToAgentNode(ctx, log, agentRun, gateJob)
	if err := controllerutil.SetControllerReference(agentRun, gateJob, r.Scheme); err != nil {
		return ctrl.Result{}, fmt.Errorf("setting owner reference on gate Job: %w", err)
	}
	if err := r.Create(ctx, gateJob); err != nil {
		if !errors.IsAlreadyExists(err) {
			return ctrl.Result{}, fmt.Errorf("creating gate Job for attempt %d: %w", parked.Attempt, err)
		}
		// A duplicate reconcile already created it. A Job this run does not
		// own is not its gate, and its result must not judge this attempt.
		var existing batchv1.Job
		if err := r.Get(ctx, client.ObjectKeyFromObject(gateJob), &existing); err != nil {
			return ctrl.Result{}, fmt.Errorf("reading existing gate Job %s: %w", gateJob.Name, err)
		}
		if !metav1.IsControlledBy(&existing, agentRun) {
			return ctrl.Result{}, fmt.Errorf("gate Job %s exists and is not owned by this run", gateJob.Name)
		}
	}

	if err := r.updateStatusWithRetry(ctx, agentRun, func(ar *sympoziumv1alpha1.AgentRun) {
		ar.Status.Phase = sympoziumv1alpha1.AgentRunPhaseAwaitingGate
		ar.Status.PostRunJobName = gateJob.Name
	}); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

// pinGateJobToAgentNode schedules the gate onto the node running the parked
// agent pod.
//
// Ordinary postRun runs after the agent pod is gone, so the two never share the
// workspace. A parked pod still holds it, and the PVC is ReadWriteOnce — which
// allows several pods only on the same node. Unpinned, the gate can land
// elsewhere and sit Pending until it times out: every attempt would fail as a
// gate timeout on a multi-node cluster while passing on a single-node one.
//
// Best-effort — an unreadable pod leaves the Job unpinned.
func (r *AgentRunReconciler) pinGateJobToAgentNode(
	ctx context.Context, log logr.Logger,
	agentRun *sympoziumv1alpha1.AgentRun, job *batchv1.Job,
) {
	if agentRun.Status.PodName == "" {
		return
	}
	var pod corev1.Pod
	if err := r.Get(ctx, client.ObjectKey{Namespace: agentRun.Namespace, Name: agentRun.Status.PodName}, &pod); err != nil {
		log.V(1).Info("could not read the agent pod to pin the gate Job", "err", err)
		return
	}
	if pod.Spec.NodeName == "" {
		return
	}

	// matchFields, not Spec.NodeName: this still goes through the scheduler,
	// so the gate queues on a full node instead of being force-placed on one
	// that cannot take it.
	job.Spec.Template.Spec.Affinity = &corev1.Affinity{
		NodeAffinity: &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{{
					MatchFields: []corev1.NodeSelectorRequirement{{
						Key:      "metadata.name",
						Operator: corev1.NodeSelectorOpIn,
						Values:   []string{pod.Spec.NodeName},
					}},
				}},
			},
		},
	}
}

// gateJobNameForAttempt gives each attempt's gate its own Job. One shared name
// would collide with the previous attempt's completed Job, and a Job spec is
// immutable, so attempt 2 could never be gated.
//
// A name too long to fit is cut and given a hash of the run name, as
// runServiceAccountName does, so two runs that share a long prefix do not
// share a gate Job.
func gateJobNameForAttempt(agentRun *sympoziumv1alpha1.AgentRun, attempt int) string {
	suffix := fmt.Sprintf("-gate-%d", attempt)
	base := fmt.Sprintf("%s-postrun", agentRun.Name)
	if len(base)+len(suffix) > jobNameMaxLen {
		sum := sha256.Sum256([]byte(agentRun.Name))
		hash := fmt.Sprintf("-%x", sum[:4])
		base = strings.TrimRight(base[:jobNameMaxLen-len(suffix)-len(hash)], "-") + hash
	}
	return base + suffix
}

// jobNameMaxLen bounds a Job name: it becomes a label value on the pods the
// Job creates, and label values over 63 characters are rejected.
const jobNameMaxLen = 63

// ensureAttemptEntry returns the status entry for `attempt`, appending one the
// first time. The pointer is into the slice, so callers mutate in place.
func ensureAttemptEntry(ar *sympoziumv1alpha1.AgentRun, attempt int) *sympoziumv1alpha1.AttemptStatus {
	for i := range ar.Status.Attempts {
		if ar.Status.Attempts[i].Attempt == attempt {
			return &ar.Status.Attempts[i]
		}
	}
	now := metav1.Now()
	ar.Status.Attempts = append(ar.Status.Attempts, sympoziumv1alpha1.AttemptStatus{
		Attempt:   attempt,
		StartedAt: &now,
	})
	return &ar.Status.Attempts[len(ar.Status.Attempts)-1]
}

// estimateAttemptCost prices one attempt. Fail-open like succeedRun: a missing
// or unmatched price table yields absence, never a misleading $0.
//
// Priced per attempt so a chain's spend is visible while it still runs —
// status.costEstimate only freezes at completion.
func (r *AgentRunReconciler) estimateAttemptCost(
	ctx context.Context, agentRun *sympoziumv1alpha1.AgentRun,
	usage *sympoziumv1alpha1.TokenUsage, at metav1.Time,
) *sympoziumv1alpha1.CostEstimate {
	if usage == nil || r.Pricing == nil || pricing.Exempt(agentRun.Spec.Model) {
		return nil
	}
	table, err := r.Pricing.Load(ctx)
	if err != nil {
		return nil
	}
	est := pricing.Estimate(table, agentRun.Spec.Model.Provider, agentRun.Spec.Model.Model, usage)
	if est == nil {
		return nil
	}
	est.Source = pricing.SourceDefaultTable
	est.EstimatedAt = &at
	return est
}

// sumAttemptUsage aggregates the chain's usage. Nil when no attempt reported
// any, so status.tokenUsage stays absent rather than reading as a real zero.
func sumAttemptUsage(attempts []sympoziumv1alpha1.AttemptStatus) *sympoziumv1alpha1.TokenUsage {
	var out sympoziumv1alpha1.TokenUsage
	var any bool
	for _, a := range attempts {
		if a.TokenUsage == nil {
			continue
		}
		any = true
		out.InputTokens += a.TokenUsage.InputTokens
		out.OutputTokens += a.TokenUsage.OutputTokens
		out.TotalTokens += a.TokenUsage.TotalTokens
		out.ToolCalls += a.TokenUsage.ToolCalls
		out.DurationMs += a.TokenUsage.DurationMs
	}
	if !any {
		return nil
	}
	return &out
}

// reconcileAwaitingGate monitors the gate Job for a parked attempt.
//
// Same shape as reconcilePostRunning, but not shareable: that one always
// resolves terminally, this one may hand the verdict back to a live pod.
func (r *AgentRunReconciler) reconcileAwaitingGate(
	ctx context.Context, log logr.Logger, agentRun *sympoziumv1alpha1.AgentRun,
) (ctrl.Result, error) {
	if agentRun.Status.PostRunJobName == "" {
		return ctrl.Result{}, r.failRun(ctx, agentRun, "AwaitingGate phase but no gate Job name")
	}

	agentSucceeded := agentRun.Status.ExitCode != nil && *agentRun.Status.ExitCode == 0

	var job batchv1.Job
	if err := r.Get(ctx, client.ObjectKey{Namespace: agentRun.Namespace, Name: agentRun.Status.PostRunJobName}, &job); err != nil {
		if !errors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		// The Job is deleted as soon as a verdict arrives (below), before the
		// verdict is delivered. If delivery failed, the verdict is still on
		// the run: resolve from it rather than wait for a Job that is gone.
		if _, has := agentRun.Annotations["sympozium.ai/gate-verdict"]; has {
			return r.resolveInPlaceGate(ctx, log, agentRun, agentSucceeded, false)
		}
		// Right after Create the cache may not show the Job yet, so wait.
		// But not past the gate's own budget: a Job that was never created
		// or was deleted by hand would hold the run forever.
		if gateStartedLongAgo(agentRun) {
			log.Info("Gate Job never appeared; resolving as a failed hook", "job", agentRun.Status.PostRunJobName)
			return r.resolveInPlaceGate(ctx, log, agentRun, agentSucceeded, true)
		}
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	if job.Status.Succeeded > 0 {
		return r.resolveInPlaceGate(ctx, log, agentRun, agentSucceeded, false)
	}
	if job.Status.Failed > 0 {
		log.Info("Gate hook Job failed")
		return r.resolveInPlaceGate(ctx, log, agentRun, agentSucceeded, true)
	}

	// A human may answer through the API while the hook still runs. Kill the
	// hook and act on the verdict, as reconcilePostRunning does.
	if err := r.Get(ctx, client.ObjectKeyFromObject(agentRun), agentRun); err == nil {
		if _, has := agentRun.Annotations["sympozium.ai/gate-verdict"]; has {
			log.Info("Gate verdict arrived while the gate Job was still running; terminating it")
			_ = r.Delete(ctx, &job, client.PropagationPolicy(metav1.DeletePropagationBackground))
			return r.resolveInPlaceGate(ctx, log, agentRun, agentSucceeded, false)
		}
	}

	if anchor := postRunJobStart(&job); !anchor.IsZero() {
		if elapsed := time.Since(anchor); elapsed > postRunBudget(agentRun.Spec.Lifecycle)+postRunTimeoutGrace {
			log.Info("Gate Job timed out", "elapsed", elapsed)
			_ = r.Delete(ctx, &job, client.PropagationPolicy(metav1.DeletePropagationForeground))
			return r.resolveInPlaceGate(ctx, log, agentRun, agentSucceeded, true)
		}
	}

	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

// gateStartedLongAgo reports whether the current attempt's gate should have
// reported by now. The anchor is the attempt's CompletedAt, which
// startGateForAttempt sets before it creates the Job. With no anchor there is
// no way to bound the wait, so it reports true.
func gateStartedLongAgo(agentRun *sympoziumv1alpha1.AgentRun) bool {
	attempt := currentAttempt(agentRun)
	for _, a := range agentRun.Status.Attempts {
		if a.Attempt == attempt && a.CompletedAt != nil {
			return time.Since(a.CompletedAt.Time) > postRunBudget(agentRun.Spec.Lifecycle)+postRunTimeoutGrace
		}
	}
	return true
}

// resolveInPlaceGate applies a verdict to a parked run.
//
// A permitted "retry" continues on the same pod: the verdict goes into the gate
// ConfigMap, status.attempt advances, the run returns to Running. Everything else —
// including a retry the policy refuses — releases the pod and resolves through
// resolveGate.
//
// Budgets are checked before the verdict is delivered, unlike the successor
// path where the successor is created first and the chain summed after.
func (r *AgentRunReconciler) resolveInPlaceGate(
	ctx context.Context, log logr.Logger,
	agentRun *sympoziumv1alpha1.AgentRun, agentSucceeded bool, hookFailed bool,
) (ctrl.Result, error) {
	if err := r.Get(ctx, client.ObjectKeyFromObject(agentRun), agentRun); err != nil {
		return ctrl.Result{}, fmt.Errorf("re-fetching AgentRun for gate verdict: %w", err)
	}

	attempt := currentAttempt(agentRun)
	verdict := parseGateVerdict(agentRun)

	// A verdict stamped for an earlier attempt is stale — a browser tab still
	// showing attempt 1's controls after attempt 2 started.
	if verdict != nil && !verdictAppliesToAttempt(agentRun, attempt) {
		log.Info("Ignoring gate verdict submitted against a superseded attempt",
			"currentAttempt", attempt,
			"verdictAttempt", agentRun.Annotations[gateVerdictAttemptAnnotation])
		if err := r.clearGateVerdict(ctx, agentRun); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	if verdict != nil && verdict.Action == "retry" {
		spec := gateRetrySpec(agentRun)
		exhausted, why := r.inPlaceExhausted(ctx, agentRun, spec, attempt)
		if !exhausted {
			// Only a parked runner reads the verdict. One that left its park
			// (park timeout, /ipc/done, a crash) would never run the next
			// attempt, and moving to Running would gate this output again.
			parked, err := r.agentStillParked(ctx, agentRun)
			if err != nil {
				return ctrl.Result{}, err
			}
			if parked {
				return r.startNextAttempt(ctx, log, agentRun, verdict, spec, attempt)
			}
			why = "the agent pod is no longer parked"
		}
		log.Info("Gate verdict: retry, but the chain cannot continue", "reason", why, "attempt", attempt)
	}

	// Terminal: release the pod, then resolve through the shared path so a
	// continued run ends in the same state a single-attempt gated run does.
	r.publishGateStop(ctx, agentRun, attempt)
	return r.resolveGate(ctx, log, agentRun, agentSucceeded, hookFailed)
}

// agentStillParked reports whether the agent pod can still take a verdict:
// it exists, is not being deleted, and its agent container has not exited.
func (r *AgentRunReconciler) agentStillParked(ctx context.Context, agentRun *sympoziumv1alpha1.AgentRun) (bool, error) {
	if agentRun.Status.PodName == "" {
		return false, nil
	}
	var pod corev1.Pod
	if err := r.Get(ctx, client.ObjectKey{Namespace: agentRun.Namespace, Name: agentRun.Status.PodName}, &pod); err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("reading the agent pod: %w", err)
	}
	if pod.DeletionTimestamp != nil {
		return false, nil
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name == "agent" {
			return cs.State.Terminated == nil, nil
		}
	}
	// No status for the agent container yet means it has not started, which
	// a pod that already published an attempt cannot be.
	return false, nil
}

// verdictAppliesToAttempt reports whether the pending verdict targets the
// attempt currently awaiting one. An unstamped verdict is a hook's, from the
// Job judging this attempt, so it always applies.
func verdictAppliesToAttempt(agentRun *sympoziumv1alpha1.AgentRun, attempt int) bool {
	raw, ok := agentRun.Annotations[gateVerdictAttemptAnnotation]
	if !ok || raw == "" {
		return true
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return true
	}
	return n == attempt
}

// inPlaceExhausted reports whether the chain must stop, and why. Both bounds
// are operator-owned: maxAttempts caps the count, maxChainTokens the spend.
func (r *AgentRunReconciler) inPlaceExhausted(
	ctx context.Context, agentRun *sympoziumv1alpha1.AgentRun,
	spec *sympoziumv1alpha1.RetrySpec, attempt int,
) (bool, string) {
	if spec == nil {
		return true, "lifecycle.retry is not configured"
	}
	if attempt >= spec.MaxAttempts {
		return true, fmt.Sprintf("attempt %d of %d", attempt, spec.MaxAttempts)
	}
	if spec.MaxChainTokens > 0 {
		if used := r.retryChainTokens(ctx, agentRun); used >= spec.MaxChainTokens {
			return true, fmt.Sprintf("chain tokens %d >= %d", used, spec.MaxChainTokens)
		}
	}
	return false, ""
}

// startNextAttempt delivers the verdict to the parked pod and returns the run to
// Running for the next attempt.
func (r *AgentRunReconciler) startNextAttempt(
	ctx context.Context, log logr.Logger,
	agentRun *sympoziumv1alpha1.AgentRun, verdict *gateVerdict,
	spec *sympoziumv1alpha1.RetrySpec, attempt int,
) (ctrl.Result, error) {
	next := attempt + 1

	// Publish before the phase moves. On failure the run stays in AwaitingGate
	// and the next reconcile retries; flipping to Running first would make a
	// pod that was never told to continue look like one that is working.
	if err := r.publishGateVerdict(ctx, agentRun, &ipc.GateVerdict{
		Attempt:     attempt,
		Action:      ipc.GateVerdictActionContinue,
		Reason:      verdict.Reason,
		Output:      truncateForStatus(verdict.Response, retryGateOutputLimit()),
		MaxAttempts: spec.MaxAttempts,
	}); err != nil {
		return ctrl.Result{}, fmt.Errorf("delivering gate verdict for attempt %d: %w", attempt, err)
	}

	// Delete this attempt's gate Job so a completed one is never re-read as the
	// next attempt's verdict.
	if agentRun.Status.PostRunJobName != "" {
		_ = r.Delete(ctx, &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
			Name:      agentRun.Status.PostRunJobName,
			Namespace: agentRun.Namespace,
		}}, client.PropagationPolicy(metav1.DeletePropagationBackground))
	}

	now := metav1.Now()
	if err := r.updateStatusWithRetry(ctx, agentRun, func(ar *sympoziumv1alpha1.AgentRun) {
		entry := ensureAttemptEntry(ar, attempt)
		entry.GateVerdict = "retried"
		entry.GateReason = boundAttemptText(verdict.Reason)

		ar.Status.Attempt = next
		ar.Status.GateVerdict = "retried"
		ar.Status.Phase = sympoziumv1alpha1.AgentRunPhaseRunning
		ar.Status.PostRunJobName = ""
		ensureAttemptEntry(ar, next).StartedAt = &now
	}); err != nil {
		return ctrl.Result{}, err
	}

	// resolveGate reads the annotation unconditionally, so a leftover verdict
	// would resolve attempt N+1 against attempt N's answer. The successor path
	// gets this free by not copying it; one CR has to clear it.
	if err := r.clearGateVerdict(ctx, agentRun); err != nil {
		return ctrl.Result{}, err
	}

	slog.InfoContext(ctx, "agent.run.continued",
		"agent_run", agentRun.Name,
		"instance", agentRun.Spec.AgentRef,
		"attempt", next,
		"max_attempts", spec.MaxAttempts,
	)
	log.Info("Continued parked run on the next attempt", "attempt", next, "maxAttempts", spec.MaxAttempts)

	return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
}

// clearGateVerdict removes the verdict annotations so the next attempt starts
// with no pending decision.
func (r *AgentRunReconciler) clearGateVerdict(ctx context.Context, agentRun *sympoziumv1alpha1.AgentRun) error {
	if agentRun.Annotations == nil {
		return nil
	}
	if _, hasVerdict := agentRun.Annotations["sympozium.ai/gate-verdict"]; !hasVerdict {
		if _, hasAttempt := agentRun.Annotations[gateVerdictAttemptAnnotation]; !hasAttempt {
			return nil
		}
	}
	patch := client.MergeFrom(agentRun.DeepCopy())
	delete(agentRun.Annotations, "sympozium.ai/gate-verdict")
	delete(agentRun.Annotations, gateVerdictAttemptAnnotation)
	if err := r.Patch(ctx, agentRun, patch); err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("clearing gate verdict annotation: %w", err)
	}
	return nil
}

// gateChannelName names the ConfigMap that carries verdicts to a parked run.
func gateChannelName(agentRun *sympoziumv1alpha1.AgentRun) string {
	return agentRun.Name + "-gate"
}

// ensureGateChannel returns the run's gate ConfigMap, creating it if needed.
//
// The agent container mounts it read-only, and no other container mounts it,
// so nothing in the pod can write a verdict. The name is predictable, so a
// ConfigMap at it that this run does not control is replaced, as
// ensureGateVerdictToken does for the gate Secret.
func (r *AgentRunReconciler) ensureGateChannel(ctx context.Context, agentRun *sympoziumv1alpha1.AgentRun) (*corev1.ConfigMap, error) {
	key := client.ObjectKey{Name: gateChannelName(agentRun), Namespace: agentRun.Namespace}

	var existing corev1.ConfigMap
	err := r.Get(ctx, key, &existing)
	switch {
	case err == nil && metav1.IsControlledBy(&existing, agentRun):
		return &existing, nil
	case err == nil:
		logf.FromContext(ctx).Info("Replacing a gate ConfigMap this run does not own",
			"configmap", key.Name, "namespace", key.Namespace)
		if delErr := r.Delete(ctx, &existing); delErr != nil && !errors.IsNotFound(delErr) {
			return nil, fmt.Errorf("replacing unowned gate ConfigMap %s: %w", key.Name, delErr)
		}
	case !errors.IsNotFound(err):
		return nil, err
	}

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      key.Name,
			Namespace: key.Namespace,
			Labels: map[string]string{
				"sympozium.ai/agent-run": agentRun.Name,
				"sympozium.ai/instance":  agentRun.Spec.AgentRef,
				"sympozium.ai/component": "gate-channel",
			},
		},
	}
	if err := controllerutil.SetControllerReference(agentRun, cm, r.Scheme); err != nil {
		return nil, err
	}
	if err := r.Create(ctx, cm); err != nil {
		if !errors.IsAlreadyExists(err) {
			return nil, err
		}
		// A concurrent reconcile created it first. Use it only if it is ours.
		var raced corev1.ConfigMap
		if getErr := r.Get(ctx, key, &raced); getErr != nil {
			return nil, getErr
		}
		if !metav1.IsControlledBy(&raced, agentRun) {
			return nil, fmt.Errorf("gate ConfigMap %s exists and is not owned by this run", key.Name)
		}
		return &raced, nil
	}
	return cm, nil
}

// publishGateVerdict signs the verdict and writes it into the run's gate
// ConfigMap, where the parked runner reads it from its read-only mount. The
// runner ignores an unsigned verdict, so anyone else who can edit the
// ConfigMap cannot continue the run.
func (r *AgentRunReconciler) publishGateVerdict(ctx context.Context, agentRun *sympoziumv1alpha1.AgentRun, v *ipc.GateVerdict) error {
	key, err := r.gateAttemptKey(ctx, agentRun)
	if err != nil {
		return err
	}
	if err := v.Sign(key); err != nil {
		return fmt.Errorf("signing gate verdict: %w", err)
	}
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encoding gate verdict: %w", err)
	}

	var written *corev1.ConfigMap
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cm, err := r.ensureGateChannel(ctx, agentRun)
		if err != nil {
			return err
		}
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		cm.Data[ipc.GateVerdictFile(v.Attempt)] = string(data)
		if err := r.Update(ctx, cm); err != nil {
			return err
		}
		written = cm
		return nil
	}); err != nil {
		return fmt.Errorf("writing gate verdict for attempt %d: %w", v.Attempt, err)
	}

	r.nudgeAgentPod(ctx, agentRun, written.ResourceVersion)
	return nil
}

// nudgeAgentPod sets gateVerdictSeqAnnotation on the agent pod so kubelet
// syncs it now and the verdict lands in the mount at once. The value is the
// ConfigMap's resourceVersion, which changes on every write, so a repeated
// write still changes the pod.
//
// Best-effort: if the patch fails, kubelet's own periodic sync still delivers
// the verdict, about a minute later.
func (r *AgentRunReconciler) nudgeAgentPod(ctx context.Context, agentRun *sympoziumv1alpha1.AgentRun, seq string) {
	if agentRun.Status.PodName == "" {
		return
	}
	patch, err := json.Marshal(map[string]any{
		"metadata": map[string]any{
			"annotations": map[string]string{gateVerdictSeqAnnotation: seq},
		},
	})
	if err != nil {
		return
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: agentRun.Status.PodName, Namespace: agentRun.Namespace}}
	if err := r.Patch(ctx, pod, client.RawPatch(types.MergePatchType, patch)); err != nil {
		slog.WarnContext(ctx, "could not nudge the parked pod; the verdict lands on kubelet's next sync",
			"agent_run", agentRun.Name, "pod", pod.Name, "error", err)
	}
}

// publishGateStop releases a parked pod so it can write its final result and
// exit. Best-effort: a pod that never hears it falls back on its park timeout.
func (r *AgentRunReconciler) publishGateStop(ctx context.Context, agentRun *sympoziumv1alpha1.AgentRun, attempt int) {
	if err := r.publishGateVerdict(ctx, agentRun, &ipc.GateVerdict{
		Attempt: attempt,
		Action:  ipc.GateVerdictActionStop,
	}); err != nil {
		slog.WarnContext(ctx, "could not tell the parked pod to stop",
			"agent_run", agentRun.Name, "attempt", attempt, "error", err)
	}
}

// recordFinalAttemptVerdict stamps the terminal verdict onto the attempt it
// judged, so status.attempts[] ends complete rather than trailing off.
func (r *AgentRunReconciler) recordFinalAttemptVerdict(
	ctx context.Context, agentRun *sympoziumv1alpha1.AgentRun, verdictLabel, reason string,
) {
	if len(agentRun.Status.Attempts) == 0 {
		return
	}
	attempt := currentAttempt(agentRun)
	now := metav1.Now()
	_ = r.updateStatusWithRetry(ctx, agentRun, func(ar *sympoziumv1alpha1.AgentRun) {
		entry := ensureAttemptEntry(ar, attempt)
		entry.GateVerdict = verdictLabel
		entry.GateReason = boundAttemptText(reason)
		if entry.CompletedAt == nil {
			entry.CompletedAt = &now
		}
	})
}
