package controller

import (
	"context"
	"errors"
	"strings"

	"github.com/go-logr/logr"
	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/modelkey"
)

// The ExecutionBackend seam separates what Sympozium coordinates (Agents and
// their keys, policy and tool gating, ensembles, gates, spend, attribution)
// from where a run executes. See docs/design/execution-backend-seam.md.
//
// This first step wraps the existing Job and Celln (scoped platform) paths
// with no behaviour change. Agent Sandbox, server mode and enduring-run turns
// (AgentRunTurnReconciler, which stays wired to cellnscoped) are not behind
// the seam yet.

// ExecutionBackendName names an execution backend.
type ExecutionBackendName string

const (
	ExecutionBackendJob   ExecutionBackendName = "job"
	ExecutionBackendCelln ExecutionBackendName = "celln"
)

// ExecutionPhase is the backend-neutral phase of one run's execution. It is
// deliberately coarser than AgentRunPhase: gates, postRun hooks, delegation
// and serving are coordination states owned above the seam.
type ExecutionPhase string

const (
	ExecutionPending   ExecutionPhase = "Pending"
	ExecutionRunning   ExecutionPhase = "Running"
	ExecutionSucceeded ExecutionPhase = "Succeeded"
	ExecutionFailed    ExecutionPhase = "Failed"
	// ExecutionRefused: the run was refused rather than failed: by shared
	// admission (a MODEL_KEY_* code) or by the backend (for Celln, a native
	// Refused phase, possibly after StartAttempted). Retry policy and spend
	// accounting treat it differently from Failed.
	ExecutionRefused ExecutionPhase = "Refused"
	// ExecutionUncertain: the backend cannot confirm the outcome yet (Celln:
	// CellnScopedExecution Unknown, including the NativeOwnerUncertain grace
	// period). Nothing may be replayed or replaced.
	ExecutionUncertain ExecutionPhase = "Uncertain"
	// ExecutionContextLost: the backend reports the run's execution context
	// gone and the run ended (Celln: the receiver's AUTH_CONTEXT_LOST). Also
	// not a plain failure.
	ExecutionContextLost ExecutionPhase = "ContextLost"
	ExecutionUnknown     ExecutionPhase = "Unknown"
)

// RefusalCode is a public reason for refusing a run. Celln's AUTH_* codes
// (internal/cellnauthority) and the shared model-key codes below are
// RefusalCodes. A closed enum is a target, not done.
type RefusalCode string

// Shared admission refusals: authorizeRunKeys, on every backend.
const (
	RefusalModelKeyNotGranted     RefusalCode = "MODEL_KEY_NOT_GRANTED"
	RefusalModelKeyConflict       RefusalCode = "MODEL_KEY_CONFLICT"
	RefusalModelKeyOriginMismatch RefusalCode = "MODEL_KEY_ORIGIN_MISMATCH"
)

// Conditions that carry typed outcomes the seam reads instead of parsing
// status.error.
const (
	conditionModelKeyAuthorized = "ModelKeyAuthorized"
	conditionCellnContextLost   = "CellnContextLost"
)

// modelKeyRefusal types a modelkey refusal error.
func modelKeyRefusal(err error) *Refusal {
	refusal := &Refusal{Code: RefusalModelKeyNotGranted, Message: err.Error()}
	var conflict *modelkey.ConflictError
	var origin *modelkey.OriginMismatchError
	switch {
	case errors.As(err, &conflict):
		refusal.Code = RefusalModelKeyConflict
	case errors.As(err, &origin):
		refusal.Code = RefusalModelKeyOriginMismatch
	}
	return refusal
}

// terminalMark returns a typed outcome condition only when failRun wrote it
// with this run's Failed transition: same status update, LastTransitionTime
// equal to CompletedAt. A mark from any other transition is ignored, so a
// stale marker can never relabel a later, unrelated failure.
func terminalMark(run *sympoziumv1alpha1.AgentRun, conditionType string, status metav1.ConditionStatus) *metav1.Condition {
	if run.Status.Phase != sympoziumv1alpha1.AgentRunPhaseFailed || run.Status.CompletedAt == nil {
		return nil
	}
	c := meta.FindStatusCondition(run.Status.Conditions, conditionType)
	if c == nil || c.Status != status || !c.LastTransitionTime.Equal(run.Status.CompletedAt) {
		return nil
	}
	return c
}

// admissionRefusal reads a shared admission refusal off a failed run.
func admissionRefusal(run *sympoziumv1alpha1.AgentRun) *Refusal {
	c := terminalMark(run, conditionModelKeyAuthorized, metav1.ConditionFalse)
	if c == nil {
		return nil
	}
	return &Refusal{Code: RefusalCode(c.Reason), Message: c.Message}
}

// Refusal is an admission refusal: the run never started.
type Refusal struct {
	Code    RefusalCode
	Message string
}

// ExecutionReceipt is opaque backend evidence for a terminal result (request
// ID, resolved hashes, verdict). Sympozium stores and shows it, never
// interprets it, and treats it as write-once.
type ExecutionReceipt struct {
	Digest string
}

// ExecutionResult is the typed terminal result of a run's execution.
// PublicReason is tenant-safe: free-text native diagnostics never go here.
type ExecutionResult struct {
	Outcome      ExecutionPhase
	PublicReason string
	Output       string
	Receipt      ExecutionReceipt
}

// BackendState is whether a backend is usable on this cluster.
type BackendState string

const (
	BackendReady            BackendState = "ready"
	BackendNotInstalled     BackendState = "not_installed"
	BackendTransportInvalid BackendState = "transport_invalid"
	BackendStateUnknown     BackendState = "unknown"
)

// LogSupport says what a backend can return from Logs.
type LogSupport string

const (
	LogsNone     LogSupport = "none"
	LogsTerminal LogSupport = "terminal"
	LogsStream   LogSupport = "stream"
)

// BackendCapabilities describes a backend to the console, doctor and
// admission.
type BackendCapabilities struct {
	State      BackendState
	Lifecycles []string
	Logs       LogSupport
	Turns      bool
}

var (
	// ErrBackendTemporarilyUnavailable tells the caller to requeue rather
	// than fail the run.
	ErrBackendTemporarilyUnavailable = errors.New("execution backend temporarily unavailable")
	// ErrLogsUnsupported: the backend exposes no logs for this run.
	ErrLogsUnsupported = errors.New("execution backend does not expose logs")
	// ErrLogsUnavailable: logs are supported but cannot be read now.
	ErrLogsUnavailable = errors.New("execution backend logs unavailable")
)

// RunIdentity names a run's execution for audit, policy and attribution.
// Attribution keys on the run (Namespace, Name, UID), never on Workload,
// because a backend may multiplex many runs onto one worker. It carries no
// Secret name, key or UID.
type RunIdentity struct {
	Backend   ExecutionBackendName
	Namespace string
	Name      string
	UID       types.UID
	// Workload is the backend's handle for the execution (e.g. "Job/<name>").
	Workload string
}

// executionStartInput is what the coordination layer hands a backend's Start
// after shared admission. It carries no key material, but it is not yet
// Secret-free: agent (and the run the backend also receives) still name
// Secrets, spec.model.authSecretRef among them, because the Job backend
// renders the pod from them and injects the Agent's key into the container
// env (the documented Job exception in docs/design/execution-backend-seam.md).
// Narrowing this to Secret-free fields is a target; Celln ignores agent.
type executionStartInput struct {
	agent                   *sympoziumv1alpha1.Agent
	harnessRuntimeRef       string
	inheritedHarnessRuntime bool
}

// ExecutionBackend is the seam. Start, Observe and Release are reconcile
// shaped for now because they wrap existing reconcile functions unchanged;
// splitting them into Admit/Start(admission, idempotencyKey)/Phase/Cancel is
// the next step (see the design note). Phase, Result, Identity and
// Capabilities are read-only views of the run's status.
type ExecutionBackend interface {
	Name() ExecutionBackendName
	Capabilities(ctx context.Context) (BackendCapabilities, error)
	// Owns reports whether this backend executes the run.
	Owns(ctx context.Context, run *sympoziumv1alpha1.AgentRun) (bool, error)
	Start(ctx context.Context, log logr.Logger, run *sympoziumv1alpha1.AgentRun, in executionStartInput) (ctrl.Result, error)
	Observe(ctx context.Context, log logr.Logger, run *sympoziumv1alpha1.AgentRun) (ctrl.Result, error)
	// Release is idempotent cleanup of the run's execution. confirmed is
	// false while cleanup is still in progress.
	Release(ctx context.Context, log logr.Logger, run *sympoziumv1alpha1.AgentRun) (confirmed bool, err error)
	Logs(ctx context.Context, log logr.Logger, run *sympoziumv1alpha1.AgentRun) (string, error)
	Phase(run *sympoziumv1alpha1.AgentRun) ExecutionPhase
	Result(run *sympoziumv1alpha1.AgentRun) *ExecutionResult
	Identity(run *sympoziumv1alpha1.AgentRun) RunIdentity
}

type executionBackendSet struct {
	job   ExecutionBackend
	celln ExecutionBackend
}

func (r *AgentRunReconciler) executionBackends() executionBackendSet {
	return executionBackendSet{job: jobBackend{r: r}, celln: cellnBackend{r: r}}
}

// coordinationPhase maps the coordination-layer phase onto the execution
// phase both wrapped backends share today.
func coordinationPhase(p sympoziumv1alpha1.AgentRunPhase) ExecutionPhase {
	switch p {
	case "", sympoziumv1alpha1.AgentRunPhasePending:
		return ExecutionPending
	case sympoziumv1alpha1.AgentRunPhaseSucceeded, sympoziumv1alpha1.AgentRunPhaseSkipped:
		return ExecutionSucceeded
	case sympoziumv1alpha1.AgentRunPhaseFailed:
		return ExecutionFailed
	case sympoziumv1alpha1.AgentRunPhaseRunning, sympoziumv1alpha1.AgentRunPhasePostRunning,
		sympoziumv1alpha1.AgentRunPhaseAwaitingGate, sympoziumv1alpha1.AgentRunPhaseAwaitingDelegate,
		sympoziumv1alpha1.AgentRunPhaseServing:
		return ExecutionRunning
	}
	return ExecutionUnknown
}

func identityFor(backend ExecutionBackendName, run *sympoziumv1alpha1.AgentRun, workload string) RunIdentity {
	return RunIdentity{Backend: backend, Namespace: run.Namespace, Name: run.Name, UID: run.UID, Workload: workload}
}

// jobBackend runs an AgentRun as a Kubernetes Job. Known exception to "no
// key inside the backend": buildContainers injects the Agent's provider key
// and provider headers into the agent container's env.
type jobBackend struct{ r *AgentRunReconciler }

func (jobBackend) Name() ExecutionBackendName { return ExecutionBackendJob }

func (jobBackend) Capabilities(context.Context) (BackendCapabilities, error) {
	return BackendCapabilities{State: BackendReady, Lifecycles: []string{"one-shot"}, Logs: LogsTerminal}, nil
}

// Owns: the Job backend is the default for anything the Celln and Agent
// Sandbox paths do not take; Reconcile and reconcilePending decide those first.
func (jobBackend) Owns(_ context.Context, run *sympoziumv1alpha1.AgentRun) (bool, error) {
	if run.Spec.Backend == "celln" || run.Status.CellnScoped != nil {
		return false, nil
	}
	if run.Spec.AgentSandbox != nil && run.Spec.AgentSandbox.Enabled {
		return false, nil
	}
	return run.Status.SandboxName == "" && run.Status.SandboxClaimName == "", nil
}

func (b jobBackend) Start(ctx context.Context, log logr.Logger, run *sympoziumv1alpha1.AgentRun, in executionStartInput) (ctrl.Result, error) {
	return b.r.startJob(ctx, log, run, in)
}

func (b jobBackend) Observe(ctx context.Context, log logr.Logger, run *sympoziumv1alpha1.AgentRun) (ctrl.Result, error) {
	return b.r.observeJob(ctx, log, run)
}

func (b jobBackend) Release(ctx context.Context, _ logr.Logger, run *sympoziumv1alpha1.AgentRun) (bool, error) {
	if run.Status.JobName == "" {
		return true, nil
	}
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: run.Status.JobName, Namespace: run.Namespace}}
	if err := b.r.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationForeground)); err != nil && !apierrors.IsNotFound(err) {
		return false, err
	}
	return true, nil
}

func (b jobBackend) Logs(ctx context.Context, log logr.Logger, run *sympoziumv1alpha1.AgentRun) (string, error) {
	logs, ok := b.r.readAgentLogs(ctx, log, run)
	if !ok {
		return "", ErrLogsUnavailable
	}
	return logs, nil
}

func (jobBackend) Phase(run *sympoziumv1alpha1.AgentRun) ExecutionPhase {
	if admissionRefusal(run) != nil {
		return ExecutionRefused
	}
	return coordinationPhase(run.Status.Phase)
}

func (b jobBackend) Result(run *sympoziumv1alpha1.AgentRun) *ExecutionResult {
	phase := b.Phase(run)
	if phase != ExecutionSucceeded && phase != ExecutionFailed && phase != ExecutionRefused {
		return nil
	}
	res := &ExecutionResult{Outcome: phase, Output: run.Status.Result}
	if refusal := admissionRefusal(run); refusal != nil {
		res.PublicReason = string(refusal.Code)
	}
	return res
}

func (jobBackend) Identity(run *sympoziumv1alpha1.AgentRun) RunIdentity {
	workload := ""
	if run.Status.JobName != "" {
		workload = "Job/" + run.Status.JobName
	}
	return identityFor(ExecutionBackendJob, run, workload)
}

// cellnBackend runs an AgentRun on the Celln scoped platform. Its enduring
// turns (AgentRunTurn) stay outside the seam.
type cellnBackend struct{ r *AgentRunReconciler }

func (cellnBackend) Name() ExecutionBackendName { return ExecutionBackendCelln }

// Capabilities does not probe the receiver yet; readiness lives in the API
// server's Celln capability discovery and moves here in a later step.
func (cellnBackend) Capabilities(context.Context) (BackendCapabilities, error) {
	return BackendCapabilities{State: BackendStateUnknown, Lifecycles: []string{"one-shot", "enduring"}, Logs: LogsNone, Turns: true}, nil
}

func (b cellnBackend) Owns(ctx context.Context, run *sympoziumv1alpha1.AgentRun) (bool, error) {
	return b.r.sharedCatalogueSelected(ctx, run)
}

func (b cellnBackend) Start(ctx context.Context, log logr.Logger, run *sympoziumv1alpha1.AgentRun, _ executionStartInput) (ctrl.Result, error) {
	return b.r.reconcilePendingScoped(ctx, log, run)
}

func (b cellnBackend) Observe(ctx context.Context, log logr.Logger, run *sympoziumv1alpha1.AgentRun) (ctrl.Result, error) {
	return b.r.reconcileRunningScoped(ctx, log, run)
}

func (b cellnBackend) Release(ctx context.Context, _ logr.Logger, run *sympoziumv1alpha1.AgentRun) (bool, error) {
	return b.r.cleanupScoped(ctx, run)
}

func (cellnBackend) Logs(context.Context, logr.Logger, *sympoziumv1alpha1.AgentRun) (string, error) {
	return "", ErrLogsUnsupported
}

// cellnRefusal reports a Celln run the backend refused: a shared admission
// refusal (model keys), or a terminal owner record whose native phase is
// Refused, before or after StartAttempted (a receiver may refuse once asked
// to start). The code is the public AUTH_* reason scopedFailureSummary put
// first in status.error when it mapped one; status does not store the code
// typed yet, so an unmapped refusal has an empty code.
func cellnRefusal(run *sympoziumv1alpha1.AgentRun) *Refusal {
	if refusal := admissionRefusal(run); refusal != nil {
		return refusal
	}
	if run.Status.Phase != sympoziumv1alpha1.AgentRunPhaseFailed || run.Status.CellnScoped == nil || run.Status.CellnScoped.NativePhase != "Refused" {
		return nil
	}
	refusal := &Refusal{Message: run.Status.Error}
	if code, _, found := strings.Cut(run.Status.Error, ":"); found && strings.HasPrefix(code, "AUTH_") {
		refusal.Code = RefusalCode(code)
	}
	return refusal
}

func (cellnBackend) Phase(run *sympoziumv1alpha1.AgentRun) ExecutionPhase {
	if cellnRefusal(run) != nil {
		return ExecutionRefused
	}
	// A receiver-reported AUTH_CONTEXT_LOST ends the run through failRun and
	// is marked by its own condition.
	if terminalMark(run, conditionCellnContextLost, metav1.ConditionTrue) != nil {
		return ExecutionContextLost
	}
	// Unknown on a live run (NativeOwnerUncertain is the grace period, which
	// can still end as a LeaseElapsed success) is Uncertain, not ContextLost.
	if c := meta.FindStatusCondition(run.Status.Conditions, "CellnScopedExecution"); c != nil && c.Status == metav1.ConditionUnknown && !run.Status.Phase.IsTerminal() {
		return ExecutionUncertain
	}
	return coordinationPhase(run.Status.Phase)
}

func (b cellnBackend) Result(run *sympoziumv1alpha1.AgentRun) *ExecutionResult {
	phase := b.Phase(run)
	if !run.Status.Phase.IsTerminal() {
		return nil
	}
	res := &ExecutionResult{Outcome: phase, Output: run.Status.Result}
	if refusal := cellnRefusal(run); refusal != nil {
		res.PublicReason = string(refusal.Code)
	} else if phase == ExecutionContextLost {
		res.PublicReason = "AUTH_CONTEXT_LOST"
	}
	if run.Status.CellnScoped != nil {
		res.Receipt = ExecutionReceipt{Digest: run.Status.CellnScoped.ReceiptDigest}
	}
	return res
}

func (cellnBackend) Identity(run *sympoziumv1alpha1.AgentRun) RunIdentity {
	workload := ""
	if run.Status.CellnScoped != nil && run.Status.CellnScoped.PreparationName != "" {
		workload = "CellnPreparation/" + run.Status.CellnScoped.PreparationName
	}
	return identityFor(ExecutionBackendCelln, run, workload)
}
