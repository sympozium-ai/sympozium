package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/harnessgateway"
	"github.com/sympozium-ai/sympozium/internal/modelkey"
	"github.com/sympozium-ai/sympozium/internal/substrate"
)

// The Substrate backend runs an AgentRun as an Agent Substrate actor (#699).
// It is experimental and opt-in: only spec.backend=substrate selects it, and
// only on a controller an operator configured for Substrate. It is the first
// backend written against the ExecutionBackend seam rather than wrapped by
// it. What it does not do, on purpose:
//
//   - It never gives the sandbox a credential. The actor reaches the turn
//     endpoint and the model gateway, and both learn who is calling from the
//     JWT Substrate's egress gateway attaches outside the sandbox.
//   - It keeps no state of its own. The run's status says which actor it is,
//     the turn is derived from the run on every request, and the result is
//     written to status by whichever controller replica the actor reached.
//   - It never falls back. A run it cannot execute is refused with a typed
//     code (Preflight, Start) before anything is created, and is never run
//     as a Job.
//
// Scope: one-shot string tasks with a model key. Skills, MCP servers, memory,
// lifecycle hooks, gates and harness runtimes are refused.

const (
	ExecutionBackendSubstrate ExecutionBackendName = "substrate"

	// substrateActorCredential is the placeholder the runner sends where a
	// model key would go. Substrate's egress gateway overwrites it; anything
	// that receives it unreplaced refuses it. It is not a secret.
	substrateActorCredential = "substrate-actor"

	// substrateMaxResultBytes bounds the result document kept in status.
	substrateMaxResultBytes = 256 << 10
	// substrateMaxLogBytes bounds the log tail kept beside the run.
	substrateMaxLogBytes = 256 << 10

	substrateLogsConfigMapPrefix = "sympozium-run-logs-"
	substrateLogsKey             = "agent.log"

	substrateObserveInterval = 10 * time.Second
	// substrateDefaultTimeout bounds a run that sets no spec.timeout, as the
	// Job backend's hard default does. Without it an actor that never
	// reports would keep its run Running, and its model access, for ever.
	substrateDefaultTimeout = 10 * time.Minute
	substrateRetryInterval  = 2 * time.Second
)

// SubstrateAPI is the part of the Substrate Control API the backend uses.
// *substrate.Client implements it.
type SubstrateAPI interface {
	EnsureAtespace(ctx context.Context, name string) error
	EnsureTemplate(ctx context.Context, t substrate.Template) (substrate.TemplateStatus, error)
	CreateActor(ctx context.Context, atespace, name, template string) (substrate.Actor, bool, error)
	GetActor(ctx context.Context, atespace, name string) (substrate.Actor, error)
	ResumeActor(ctx context.Context, atespace, name string) (substrate.Actor, error)
	SuspendActor(ctx context.Context, atespace, name string, durable bool) (substrate.Actor, error)
	DeleteActor(ctx context.Context, atespace, name string) (bool, error)
	SetEgress(ctx context.Context, atespace, actor string, targets []substrate.EgressTarget) error
}

// SubstrateConfig is set when an operator points the controller at a
// Substrate install. Nil means the backend is not configured and a
// backend=substrate run is refused; nothing falls back to another backend.
type SubstrateConfig struct {
	API SubstrateAPI
	// Image is the digest-pinned agent-runner image actors run.
	Image string
	// TurnEndpoint is where actors reach this controller's turn endpoint and
	// ModelGateway where they reach the model gateway, both through
	// Substrate's egress gateway.
	TurnEndpoint SubstrateEndpoint
	ModelGateway SubstrateEndpoint
	// AllowPlaintextHop is the operator's explicit acceptance that the hop
	// from Substrate's egress gateway to an endpoint above without TLS is
	// clear text. Without it the backend refuses every run.
	AllowPlaintextHop bool
	// Audience is what actor JWTs are minted for. Both receivers check it.
	Audience string
	// CPUMillicores and MemoryBytes size every actor.
	CPUMillicores int64
	MemoryBytes   int64
}

// transportValid reports whether every hop an actor's JWT, prompts and
// responses cross is either TLS or one the operator accepted in clear text.
func (c *SubstrateConfig) transportValid() bool {
	return c.AllowPlaintextHop || (c.TurnEndpoint.TLS && c.ModelGateway.TLS)
}

// SubstrateEndpoint is a host and port an actor may reach.
type SubstrateEndpoint struct {
	Hostname string
	Port     int32
	TLS      bool
}

// URL is the endpoint's base URL as the actor addresses it.
func (e SubstrateEndpoint) URL() string {
	scheme := "http"
	if e.TLS {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s:%d", scheme, e.Hostname, e.Port)
}

func (e SubstrateEndpoint) egress(audience string) substrate.EgressTarget {
	return substrate.EgressTarget{Hostname: e.Hostname, Port: e.Port, TLS: e.TLS, ActorJWTAudience: audience}
}

func (c *SubstrateConfig) template(atespace string) substrate.Template {
	// A template is immutable and its golden snapshot freezes the process,
	// environment included. The name covers everything frozen, so a changed
	// image or turn endpoint is a new template, never a stale one.
	sum := sha256.Sum256([]byte(c.Image + "\x00" + c.TurnEndpoint.URL()))
	return substrate.Template{
		Atespace: atespace, Name: "runner-" + hex.EncodeToString(sum[:6]), Image: c.Image, Args: []string{"actor"},
		Env:        map[string]string{"ACTOR_TURN_URL": c.TurnEndpoint.URL(), "ACTOR_HEALTH_ADDR": ":8089"},
		WakeupPath: "/healthz", WakeupPort: 8089, CPUMillicores: c.CPUMillicores, MemoryBytes: c.MemoryBytes,
	}
}

// substrateTimeout is how long a run may take: to start, and then to finish.
func substrateTimeout(run *sympoziumv1alpha1.AgentRun) time.Duration {
	if run.Spec.Timeout != nil {
		return run.Spec.Timeout.Duration
	}
	return substrateDefaultTimeout
}

type substrateBackend struct{ r *AgentRunReconciler }

func (substrateBackend) Name() ExecutionBackendName { return ExecutionBackendSubstrate }

// Capabilities: not_installed without configuration, transport_invalid while
// the clear-text hop is not acknowledged, otherwise unknown, because the
// Control API is not probed yet.
func (b substrateBackend) Capabilities(context.Context) (BackendCapabilities, error) {
	state := BackendNotInstalled
	if cfg := b.r.Substrate; cfg != nil {
		state = BackendStateUnknown
		if !cfg.transportValid() {
			state = BackendTransportInvalid
		}
	}
	return BackendCapabilities{State: state, Lifecycles: []string{"one-shot"}, Logs: LogsTerminal}, nil
}

// substrateOwns: a run that asks for Substrate and has not started as
// anything else, or one that already has an actor. Nothing but
// spec.backend=substrate ever puts a run here: a controller configured for
// Substrate takes no other run, and whether Substrate is installed on the
// cluster is never looked at. The second half keeps a run on its actor if
// spec.backend is edited later; the JobName test keeps a run that started as
// a Job on its Job if spec.backend is edited to substrate.
func substrateOwns(run *sympoziumv1alpha1.AgentRun) bool {
	if run.Status.Substrate != nil {
		return true
	}
	return run.Spec.Backend == string(ExecutionBackendSubstrate) && run.Status.JobName == "" &&
		run.Status.SandboxName == "" && run.Status.SandboxClaimName == "" && run.Status.CellnScoped == nil
}

func (substrateBackend) Owns(_ context.Context, run *sympoziumv1alpha1.AgentRun) (bool, error) {
	return substrateOwns(run), nil
}

// substrateUnsupported names the first thing about a run this backend cannot
// execute yet, or "".
func substrateUnsupported(run *sympoziumv1alpha1.AgentRun) string {
	const keyNote = " (the model is reached through the model gateway, which adds the Agent's key)"
	model := run.Spec.Model
	switch {
	case run.Spec.Task == nil || !run.Spec.Task.IsString():
		return "a string task (no harness runtime or task mode)"
	case run.Spec.ExecutionLifecycle == "enduring" || run.Spec.Enduring != nil || run.Spec.Conversation != nil:
		return "a one-shot run (no enduring lifecycle)"
	case run.Spec.Mode == "server":
		return "task mode (no server mode)"
	case model.ConnectionRef != "":
		return "spec.model.authSecretRef, not connectionRef" + keyNote
	case model.ModelRef != "":
		return "spec.model.authSecretRef, not modelRef" + keyNote
	case model.AuthSecretRef == "":
		return "spec.model.authSecretRef" + keyNote
	case len(run.Spec.Skills) > 0:
		return "no skills (tools are not supported yet)"
	case run.Spec.Lifecycle != nil:
		return "no lifecycle hooks or gates"
	case len(run.Spec.Env) > 0:
		return "no spec.env"
	case len(run.Spec.Volumes) > 0:
		return "no spec.volumes"
	case run.Spec.CellnSelection != nil:
		return "no cellnSelection"
	case run.Spec.AgentSandbox != nil && run.Spec.AgentSandbox.Enabled:
		return "no agentSandbox"
	case len(validation.IsDNS1123Label(run.Name)) > 0:
		return "a run name that is a DNS label of at most 63 characters (it names the actor)"
	}
	return ""
}

// Preflight refuses, from the spec and this controller's configuration
// alone, a run the backend will not execute. Reconcile calls it before shared
// admission, and Start repeats it on the resolved run.
func (b substrateBackend) Preflight(_ context.Context, run *sympoziumv1alpha1.AgentRun) *Refusal {
	cfg := b.r.Substrate
	switch {
	case cfg == nil:
		return &Refusal{Code: RefusalSubstrateNotConfigured, Message: "backend substrate is not configured on this controller; the run is refused and is not run on another backend"}
	case !cfg.transportValid():
		return &Refusal{Code: RefusalSubstrateTransportNotAcknowledged, Message: "backend substrate is configured, but the hop from Substrate's egress gateway to the model gateway and the turn endpoint is clear text and the operator has not acknowledged it (SUBSTRATE_ALLOW_PLAINTEXT_GATEWAY_HOP, chart value substrate.allowPlaintextGatewayHop)"}
	}
	if need := substrateUnsupported(run); need != "" {
		return &Refusal{Code: RefusalSubstrateUnsupportedRun, Message: "backend substrate requires " + need}
	}
	return nil
}

// admit is every check that can refuse a run. It writes nothing: a refused
// run has no status.substrate, no actor, no key binding and no pin on its
// key.
func (b substrateBackend) admit(ctx context.Context, run *sympoziumv1alpha1.AgentRun, in executionStartInput) (*harnessgateway.Route, *Refusal, error) {
	if refusal := b.Preflight(ctx, run); refusal != nil {
		return nil, refusal, nil
	}
	if in.agent != nil && (len(in.agent.Spec.Skills) > 0 || len(in.agent.Spec.MCPServers) > 0) {
		return nil, &Refusal{Code: RefusalSubstrateUnsupportedRun, Message: "backend substrate does not run skills or MCP servers yet, and the run's Agent has some"}, nil
	}
	route, refusal, err := b.r.actorModelRoute(ctx, run)
	if err != nil || refusal != nil {
		return nil, refusal, err
	}
	if err := b.r.checkRunSecrets(ctx, run, route); err != nil {
		if !isModelMediationRefusal(err) {
			return nil, nil, err
		}
		return nil, actorKeyRefusal(err), nil
	}
	return route, nil, nil
}

// actorKeyRefusal types a refusal of the run's key by the checks every
// gateway-mediated run passes. A key pinned to another origin is the shared
// MODEL_KEY_ORIGIN_MISMATCH.
func actorKeyRefusal(err error) *Refusal {
	refusal := &Refusal{Code: RefusalSubstrateNoModelRoute, Message: "backend substrate: " + err.Error()}
	var mismatch *modelkey.OriginMismatchError
	if stderrors.As(err, &mismatch) {
		refusal.Code = RefusalModelKeyOriginMismatch
	}
	return refusal
}

func (b substrateBackend) Start(ctx context.Context, log logr.Logger, run *sympoziumv1alpha1.AgentRun, in executionStartInput) (ctrl.Result, error) {
	r := b.r
	route, refusal, err := b.admit(ctx, run, in)
	if err != nil {
		return ctrl.Result{}, err
	}
	if refusal != nil {
		return ctrl.Result{}, r.refuseExecution(ctx, run, refusal)
	}
	cfg := r.Substrate

	// "Attempted" is persisted before anything exists: the run is bound to
	// its actor, by name, so a replayed reconcile, another replica and the
	// turn endpoint all agree on which actor is the run's. The actor name is
	// the idempotency key for every Control API call below.
	//
	// The update reads the run again and fails on a conflict, so what comes
	// back is the run as stored. A reconcile working from a stale copy (the
	// run already Running, or finished) stops here and starts nothing.
	tpl := cfg.template(run.Namespace)
	var st *sympoziumv1alpha1.SubstrateStatus
	var phase sympoziumv1alpha1.AgentRunPhase
	if err := r.updateStatusWithRetry(ctx, run, func(ar *sympoziumv1alpha1.AgentRun) {
		if ar.Status.Substrate == nil && !ar.Status.Phase.IsTerminal() {
			attempted := metav1.Now()
			ar.Status.Substrate = &sympoziumv1alpha1.SubstrateStatus{Atespace: run.Namespace, Actor: run.Name, Template: tpl.Name, AttemptedAt: &attempted}
		}
		st, phase = ar.Status.Substrate.DeepCopy(), ar.Status.Phase
	}); err != nil {
		return ctrl.Result{}, err
	}
	if st == nil || (phase != "" && phase != sympoziumv1alpha1.AgentRunPhasePending) {
		return ctrl.Result{Requeue: true}, nil
	}
	run.Status.Substrate = st

	// Every step below is retried on a fixed interval when the Control API
	// fails or the golden snapshot is not ready. The run's timeout bounds
	// that: a run that cannot be started in that time is failed, and Release
	// removes whatever the attempts left.
	if limit := substrateTimeout(run); st.AttemptedAt != nil && time.Since(st.AttemptedAt.Time) > limit {
		return ctrl.Result{}, r.failRun(ctx, run, fmt.Sprintf("backend substrate could not start the run within %s", limit))
	}

	// The key path comes first: an actor that could not reach its model
	// through the gateway must never start.
	if err := r.ensureActorModelBinding(ctx, run, st, route); err != nil {
		if isModelMediationRefusal(err) {
			// Refused after "attempted": the key changed between admit and
			// here. Release, on completion, removes whatever exists.
			return ctrl.Result{}, r.refuseExecution(ctx, run, actorKeyRefusal(err))
		}
		return ctrl.Result{}, err
	}

	if err := cfg.API.EnsureAtespace(ctx, st.Atespace); err != nil {
		return substrateRetry(log, "ensure atespace", err)
	}
	golden, err := cfg.API.EnsureTemplate(ctx, tpl)
	if err != nil {
		return substrateRetry(log, "ensure actor template", err)
	}
	if golden.GoldenError != "" {
		log.Info("Substrate golden snapshot failed", "template", tpl.Name, "error", golden.GoldenError)
		return ctrl.Result{}, r.failRun(ctx, run, "backend substrate could not prepare the runner image")
	}
	if !golden.GoldenReady {
		// First run of this image in the namespace; later runs skip this.
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}
	// Create-or-get by name: a replay gets the actor the first attempt made.
	actor, _, err := cfg.API.CreateActor(ctx, st.Atespace, st.Actor, tpl.Name)
	if err != nil {
		return substrateRetry(log, "create actor", err)
	}
	if st.ActorUID != "" && actor.UID != st.ActorUID {
		return ctrl.Result{}, r.failRun(ctx, run, "the run's actor was replaced")
	}
	if actor.UID == "" {
		// The UID is what the turn endpoint and the model gateway hold an
		// actor JWT to. A run without one could never be served.
		return ctrl.Result{}, r.failRun(ctx, run, "backend substrate returned an actor without a UID")
	}
	// Default deny: these two are everything the actor can reach. The JWT
	// goes wherever this provider's client sends its key. An actor has one
	// egress policy, replaced whole, so a replay sets the same policy again.
	gateway := cfg.ModelGateway.egress(cfg.Audience)
	gateway.JWTHeaders = []substrate.JWTHeader{{Name: "Authorization", Prefix: "Bearer "}}
	for _, auth := range route.Profile.AuthHeaders() {
		gateway.JWTHeaders = append(gateway.JWTHeaders, substrate.JWTHeader{Name: auth.Header, Prefix: auth.Prefix})
	}
	targets := []substrate.EgressTarget{cfg.TurnEndpoint.egress(cfg.Audience), gateway}
	if cfg.TurnEndpoint == cfg.ModelGateway {
		targets = targets[1:] // one host and port is one rule
	}
	if err := cfg.API.SetEgress(ctx, st.Atespace, st.Actor, targets); err != nil {
		return substrateRetry(log, "set egress policy", err)
	}

	// Running is what makes the turn claimable, so it is written before the
	// actor wakes. If the resume below is lost, Observe resumes it. Only a
	// Pending run bound to this very actor moves: a stale replay changes
	// nothing and leaves the resume to Observe.
	now := metav1.Now()
	started := false
	if err := r.updateStatusWithRetry(ctx, run, func(ar *sympoziumv1alpha1.AgentRun) {
		started = false
		s := ar.Status.Substrate
		if s == nil || (ar.Status.Phase != "" && ar.Status.Phase != sympoziumv1alpha1.AgentRunPhasePending) || (s.ActorUID != "" && s.ActorUID != actor.UID) {
			return
		}
		ar.Status.Phase = sympoziumv1alpha1.AgentRunPhaseRunning
		if ar.Status.StartedAt == nil {
			ar.Status.StartedAt = &now
		}
		s.ActorUID = actor.UID
		s.ResumedAt = &now
		started = true
	}); err != nil {
		return ctrl.Result{}, err
	}
	if !started {
		return ctrl.Result{Requeue: true}, nil
	}
	resumed, err := cfg.API.ResumeActor(ctx, st.Atespace, st.Actor)
	if err != nil {
		return substrateRetry(log, "resume actor", err)
	}
	b.recordWorker(ctx, run, resumed.Worker)
	return ctrl.Result{RequeueAfter: substrateObserveInterval}, nil
}

// substrateRetry requeues on any Control API error. Nothing here is a verdict
// on the run: the actor name is the idempotency key, so every call is safe to
// repeat, and the run's timeout (substrateTimeout) bounds how long that goes
// on: in Start from status.substrate.attemptedAt, in Observe from
// status.startedAt.
func substrateRetry(log logr.Logger, step string, err error) (ctrl.Result, error) {
	log.Info("Substrate call failed; retrying", "step", step, "error", err.Error())
	return ctrl.Result{RequeueAfter: substrateRetryInterval}, nil
}

func (b substrateBackend) recordWorker(ctx context.Context, run *sympoziumv1alpha1.AgentRun, worker string) {
	if worker == "" || run.Status.Substrate == nil || run.Status.Substrate.Worker == worker {
		return
	}
	_ = b.r.updateStatusWithRetry(ctx, run, func(ar *sympoziumv1alpha1.AgentRun) {
		if ar.Status.Substrate != nil {
			ar.Status.Substrate.Worker = worker
		}
	})
}

func (b substrateBackend) Observe(ctx context.Context, log logr.Logger, run *sympoziumv1alpha1.AgentRun) (ctrl.Result, error) {
	r, cfg, st := b.r, b.r.Substrate, run.Status.Substrate
	if st == nil {
		return ctrl.Result{}, r.failRun(ctx, run, "backend substrate run has no actor")
	}
	if out := st.Outcome; out != nil {
		// The document was written by the sandbox. parseAgentResultFromLogs is
		// the one place agent-reported results and metrics are validated.
		result, errMsg, usage, _ := parseAgentResultFromLogs(resultMarkerStart+out.Result+resultMarkerEnd, log)
		if out.ExitCode == 0 && errMsg == "" && result != "" {
			return r.succeedRun(ctx, run, result, usage)
		}
		if errMsg == "" {
			errMsg = fmt.Sprintf("agent exited with code %d and no result", out.ExitCode)
		}
		return ctrl.Result{}, r.failRun(ctx, run, errMsg)
	}
	if cfg == nil {
		return ctrl.Result{}, r.failRun(ctx, run, "backend substrate is no longer configured on this controller")
	}
	if anchor, limit := runTimeoutAnchor(run), substrateTimeout(run); anchor != nil && time.Since(anchor.Time) > limit {
		// Stop the work before saying the run is over: once Cancel returns
		// the actor has no model access.
		if err := b.Cancel(ctx, log, run); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, r.failRun(ctx, run, fmt.Sprintf("timed out after %s", limit))
	}
	// The result normally arrives by the turn endpoint, which updates status
	// and so wakes this reconcile. This read is the slow path that notices an
	// actor that will never report.
	actor, err := cfg.API.GetActor(ctx, st.Atespace, st.Actor)
	switch {
	case substrate.IsNotFound(err):
		return ctrl.Result{}, r.failRun(ctx, run, "the run's actor no longer exists")
	case err != nil:
		return substrateRetry(log, "get actor", err)
	case st.ActorUID != "" && actor.UID != st.ActorUID:
		return ctrl.Result{}, r.failRun(ctx, run, "the run's actor was replaced")
	case actor.State == substrate.StateCrashed:
		log.Info("Substrate actor crashed", "message", actor.CrashMessage)
		return ctrl.Result{}, r.failRun(ctx, run, "the run's actor crashed")
	case actor.State == substrate.StateSuspended:
		resumed, err := cfg.API.ResumeActor(ctx, st.Atespace, st.Actor)
		if err != nil {
			return substrateRetry(log, "resume actor", err)
		}
		actor = resumed
	}
	b.recordWorker(ctx, run, actor.Worker)
	return ctrl.Result{RequeueAfter: substrateObserveInterval}, nil
}

// Cancel stops the work of a run that has not finished: a timeout, or a
// delete while it runs. The key binding is revoked first, and that is the
// part Cancel guarantees: when it returns nil the gateway refuses the actor,
// whatever the actor is doing. Suspending the actor then stops the turn
// itself; a failure there is logged and not returned, because Release deletes
// the actor and is the step that is confirmed.
//
// Cancel cleans nothing up, and Release does not need it to have run.
func (b substrateBackend) Cancel(ctx context.Context, log logr.Logger, run *sympoziumv1alpha1.AgentRun) error {
	st := run.Status.Substrate
	if st == nil {
		return nil
	}
	if err := b.r.revokeActorModelBinding(ctx, run); err != nil {
		return fmt.Errorf("revoking the actor's model binding: %w", err)
	}
	if st.Released || b.r.Substrate == nil {
		return nil
	}
	if _, err := b.r.Substrate.API.SuspendActor(ctx, st.Atespace, st.Actor, false); err != nil && !substrate.IsNotFound(err) {
		log.Info("Could not suspend a cancelled actor; release deletes it", "actor", st.Atespace+"/"+st.Actor, "error", err.Error())
	}
	return nil
}

// Release deletes the actor and reports whether Substrate confirmed it gone.
// It is idempotent and complete on its own: it does not need Cancel to have
// run. The key binding goes first, so the gateway refuses the actor even if
// the delete is slow.
func (b substrateBackend) Release(ctx context.Context, _ logr.Logger, run *sympoziumv1alpha1.AgentRun) (bool, error) {
	st := run.Status.Substrate
	if st == nil {
		return true, nil
	}
	if err := b.r.revokeActorModelBinding(ctx, run); err != nil {
		return false, err
	}
	if st.Released {
		return true, nil
	}
	if b.r.Substrate == nil {
		return false, stderrors.New("backend substrate is not configured, so the run's actor cannot be released")
	}
	gone, err := b.r.Substrate.API.DeleteActor(ctx, st.Atespace, st.Actor)
	if err != nil || !gone {
		return false, err
	}
	err = b.r.updateStatusWithRetry(ctx, run, func(ar *sympoziumv1alpha1.AgentRun) {
		if ar.Status.Substrate != nil {
			ar.Status.Substrate.Released = true
		}
	})
	if errors.IsNotFound(err) {
		err = nil
	}
	return err == nil, err
}

func (b substrateBackend) Logs(ctx context.Context, _ logr.Logger, run *sympoziumv1alpha1.AgentRun) (string, error) {
	var cm corev1.ConfigMap
	err := b.r.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: substrateLogsConfigMapPrefix + run.Name}, &cm)
	if err != nil || !metav1.IsControlledBy(&cm, run) {
		return "", ErrLogsUnavailable
	}
	return cm.Data[substrateLogsKey], nil
}

// substrateRefusal is a refusal of a Substrate run: by shared admission
// (MODEL_KEY_*) or by the backend itself (SUBSTRATE_*).
func substrateRefusal(run *sympoziumv1alpha1.AgentRun) *Refusal {
	if refusal := admissionRefusal(run); refusal != nil {
		return refusal
	}
	return backendRefusal(run)
}

func (substrateBackend) Phase(run *sympoziumv1alpha1.AgentRun) ExecutionPhase {
	if substrateRefusal(run) != nil {
		return ExecutionRefused
	}
	return coordinationPhase(run.Status.Phase)
}

// Result is the typed terminal result. The receipt is the digest stored with
// the actor's outcome; a run that ended without one (refused, timed out,
// crashed, cancelled) has no receipt.
func (b substrateBackend) Result(run *sympoziumv1alpha1.AgentRun) *ExecutionResult {
	phase := b.Phase(run)
	if !run.Status.Phase.IsTerminal() {
		return nil
	}
	res := &ExecutionResult{Outcome: phase, Output: run.Status.Result}
	if refusal := substrateRefusal(run); refusal != nil {
		res.PublicReason = string(refusal.Code)
	}
	if st := run.Status.Substrate; st != nil {
		res.Receipt = ExecutionReceipt{Digest: st.ReceiptDigest}
	}
	return res
}

func (substrateBackend) Identity(run *sympoziumv1alpha1.AgentRun) RunIdentity {
	workload := ""
	if st := run.Status.Substrate; st != nil {
		workload = "Actor/" + st.Atespace + "/" + st.Actor
	}
	return identityFor(ExecutionBackendSubstrate, run, workload)
}

// substrateReceiptDigest is the execution receipt for an actor-reported
// outcome: which run, which actor (by UID, so not a later actor of the same
// name), and exactly what it reported and when.
func substrateReceiptDigest(run *sympoziumv1alpha1.AgentRun, st *sympoziumv1alpha1.SubstrateStatus, out *sympoziumv1alpha1.SubstrateOutcome) string {
	result := sha256.Sum256([]byte(out.Result))
	h := sha256.New()
	for _, field := range []string{
		"sympozium.ai/substrate-receipt/v1", string(run.UID), st.Atespace, st.Actor, st.ActorUID,
		strconv.FormatInt(int64(out.ExitCode), 10), hex.EncodeToString(result[:]), out.ReportedAt.UTC().Format(time.RFC3339),
	} {
		fmt.Fprintf(h, "%d:%s\n", len(field), field)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// actorModelRoute is mediationRoute for a run whose backend, not its policy,
// keeps the key out of the sandbox. The key goes only to the upstream of the
// operator's profile for the run's provider: a run whose baseURL points
// anywhere else is refused as an origin mismatch.
func (r *AgentRunReconciler) actorModelRoute(ctx context.Context, run *sympoziumv1alpha1.AgentRun) (*harnessgateway.Route, *Refusal, error) {
	if r.HarnessGateway == nil {
		return nil, &Refusal{Code: RefusalSubstrateNoModelRoute, Message: "backend substrate: the model gateway is not enabled (harnessGateway.enabled), and an actor reaches its model no other way"}, nil
	}
	profiles, err := r.HarnessGateway.Profiles.Load(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("loading model gateway profiles: %w", err)
	}
	route, err := profiles.Route(run.Spec.Model.Provider, run.Spec.Model.BaseURL)
	var noProfile *harnessgateway.NoProfileError
	switch {
	case stderrors.As(err, &noProfile):
		return nil, &Refusal{Code: RefusalSubstrateNoModelRoute, Message: "backend substrate: " + err.Error()}, nil
	case err != nil:
		return nil, &Refusal{Code: RefusalModelKeyOriginMismatch, Message: "backend substrate: " + err.Error()}, nil
	case !route.OperatorNamed:
		return nil, &Refusal{Code: RefusalModelKeyOriginMismatch, Message: fmt.Sprintf("backend substrate: spec.model.baseURL origin %s is not the upstream of the model gateway profile for provider %q, and the Agent's key goes nowhere else; an operator must add a profile for it", route.Origin, run.Spec.Model.Provider)}, nil
	}
	return route, nil, nil
}

func actorModelBindingKey(run *sympoziumv1alpha1.AgentRun) types.NamespacedName {
	return types.NamespacedName{Name: harnessgateway.RunTokenSecretName(run.Name), Namespace: run.Namespace}
}

// ensureActorModelBinding records, for the model gateway, which actor may use
// this run's key and what the key was bound to: the Agent's UID, the key
// Secret's UID and the operator profile's origin, to which it also pins the
// key. These are the bindings of every gateway-mediated run, and the gateway
// checks them on every call. The Secret is the run model token Secret without
// a token: there is nothing for the sandbox to hold.
//
// It is idempotent: one Secret per run, by name, kept as first written.
func (r *AgentRunReconciler) ensureActorModelBinding(ctx context.Context, run *sympoziumv1alpha1.AgentRun, st *sympoziumv1alpha1.SubstrateStatus, route *harnessgateway.Route) error {
	if err := r.checkRunSecrets(ctx, run, route); err != nil {
		return err
	}
	bound, _, err := r.runModelBinding(ctx, run, route)
	if err != nil {
		return err
	}
	if err := r.pinRunModelKey(ctx, run, route); err != nil {
		return err
	}
	bound[harnessgateway.ActorAnnotation] = st.Atespace + "/" + st.Actor

	key := actorModelBindingKey(run)
	var existing corev1.Secret
	err = r.uncachedReader().Get(ctx, key, &existing)
	switch {
	case err == nil && metav1.IsControlledBy(&existing, run):
		// Kept as minted: an edited run stops working rather than moving.
		return nil
	case err == nil:
		// A predictable name; one this run does not own may have been planted.
		if delErr := r.Delete(ctx, &existing); delErr != nil && !errors.IsNotFound(delErr) {
			return fmt.Errorf("replacing unowned model binding Secret %s: %w", key.Name, delErr)
		}
	case !errors.IsNotFound(err):
		return err
	}
	secret := corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name: key.Name, Namespace: key.Namespace, Annotations: bound,
		Labels: map[string]string{
			"sympozium.ai/component": "run-model-token",
			"sympozium.ai/agent-run": run.Name,
			"sympozium.ai/instance":  run.Spec.AgentRef,
		},
	}}
	if err := controllerutil.SetControllerReference(run, &secret, r.Scheme); err != nil {
		return err
	}
	if err := r.Create(ctx, &secret); err != nil {
		if !errors.IsAlreadyExists(err) {
			return err
		}
		// A concurrent reconcile made it. That one is as good, if it is ours.
		var raced corev1.Secret
		if getErr := r.uncachedReader().Get(ctx, key, &raced); getErr != nil {
			return getErr
		}
		if !metav1.IsControlledBy(&raced, run) {
			return refuseMediation("model binding Secret %s exists and is not owned by this run", key.Name)
		}
	}
	return nil
}

// revokeActorModelBinding deletes the run's key binding and returns nil only
// once it is gone. From then on the gateway answers the run's actor as it
// answers a wrong token. The read is uncached: a binding written a moment
// ago must not be missed.
func (r *AgentRunReconciler) revokeActorModelBinding(ctx context.Context, run *sympoziumv1alpha1.AgentRun) error {
	var secret corev1.Secret
	err := r.uncachedReader().Get(ctx, actorModelBindingKey(run), &secret)
	if errors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	// One this run does not control is not its binding, and the gateway
	// already refuses it.
	if !metav1.IsControlledBy(&secret, run) {
		return nil
	}
	uid := secret.UID
	if err := r.Delete(ctx, &secret, client.Preconditions{UID: &uid}); err != nil && !errors.IsNotFound(err) {
		return err
	}
	return nil
}

// substrateTurnEnv is the runner's environment for the run's turn. It names
// the model gateway and a placeholder where a key would go, and nothing from
// a Secret.
func (r *AgentRunReconciler) substrateTurnEnv(ctx context.Context, run *sympoziumv1alpha1.AgentRun) (map[string]string, error) {
	route, refusal, err := r.actorModelRoute(ctx, run)
	if err != nil {
		return nil, err
	}
	if refusal != nil {
		return nil, stderrors.New(refusal.Message)
	}
	model := run.Spec.Model
	env := map[string]string{
		"AGENT_RUN_ID":    run.Name,
		"AGENT_ID":        run.Spec.AgentID,
		"AGENT_NAMESPACE": run.Namespace,
		"INSTANCE_NAME":   run.Spec.AgentRef,
		"SESSION_KEY":     run.Spec.SessionKey,
		"TASK":            run.Spec.Task.GetPrompt(),
		"SYSTEM_PROMPT":   run.Spec.SystemPrompt,
		"MODEL_PROVIDER":  model.Provider,
		"MODEL_NAME":      model.Model,
		"MODEL_BASE_URL":  fmt.Sprintf("%s/runs/%s/%s%s", r.Substrate.ModelGateway.URL(), run.Namespace, run.Name, route.Profile.ClientBasePath),
		"THINKING_MODE":   model.Thinking,
	}
	if model.MaxTokens != nil {
		env["MAX_TOKENS"] = strconv.FormatInt(int64(*model.MaxTokens), 10)
	}
	if model.Temperature != "" {
		env["TEMPERATURE"] = model.Temperature
	}
	if run.Spec.Timeout != nil {
		env["RUN_TIMEOUT"] = run.Spec.Timeout.Duration.String()
	}
	if run.Spec.UseContext != nil {
		env["USE_CONTEXT"] = strconv.FormatBool(*run.Spec.UseContext)
	}
	for _, name := range route.Profile.Env.APIKey {
		env[name] = substrateActorCredential
	}
	return env, nil
}

// SubstrateTurns serves turns to actors from AgentRun status. It holds no
// state, so any controller replica can answer any actor.
type SubstrateTurns struct{ r *AgentRunReconciler }

// SubstrateTurns returns the store the turn endpoint is served from.
func (r *AgentRunReconciler) SubstrateTurns() *SubstrateTurns { return &SubstrateTurns{r: r} }

// runFor returns the run an actor is bound to. The actor's identity comes
// from Substrate; that the run names this very actor comes from our status.
// A caller that is not the run's actor gets the same answer as no run at all.
func (s *SubstrateTurns) runFor(ctx context.Context, reader client.Reader, actor substrate.ActorRef) (*sympoziumv1alpha1.AgentRun, bool) {
	var run sympoziumv1alpha1.AgentRun
	if err := reader.Get(ctx, types.NamespacedName{Namespace: actor.Atespace, Name: actor.Name}, &run); err != nil {
		return nil, false
	}
	st := run.Status.Substrate
	if st == nil || st.Atespace != actor.Atespace || st.Actor != actor.Name || run.DeletionTimestamp != nil {
		return nil, false
	}
	// An actor deleted and created again under the same name is not the run's,
	// and a token that does not say which actor it is cannot show otherwise:
	// once the UID is recorded, only a token carrying that UID is the run's.
	// Before it is recorded the run is not Running, so nothing is claimable.
	if st.ActorUID != "" && actor.UID != st.ActorUID {
		return nil, false
	}
	return &run, true
}

// Claim hands the run's turn to its actor while the run is Running and has
// no outcome. The turn's ID is the run's UID, so a result can only be for
// this incarnation of the run.
func (s *SubstrateTurns) Claim(ctx context.Context, actor substrate.ActorRef) (substrate.Turn, bool, error) {
	run, ok := s.runFor(ctx, s.r.Client, actor)
	if !ok || run.Status.Phase != sympoziumv1alpha1.AgentRunPhaseRunning || run.Status.Substrate.Outcome != nil || s.r.Substrate == nil {
		return substrate.Turn{}, false, nil
	}
	env, err := s.r.substrateTurnEnv(ctx, run)
	if err != nil {
		return substrate.Turn{}, false, err
	}
	return substrate.Turn{ID: string(run.UID), Env: env}, true, nil
}

// Complete records what the actor reported, once. Everything in it is
// written by an LLM-driven process: it is bounded here and interpreted later,
// by Observe, with the same validation as any agent result.
//
// The outcome and its receipt digest are written in one status update and
// never again: a second post for the same turn, identical or not, is
// refused with ErrTurnCompleted and changes nothing.
func (s *SubstrateTurns) Complete(ctx context.Context, actor substrate.ActorRef, res substrate.TurnResult) error {
	// Uncached: the claim and the result can arrive within one cache sync.
	run, ok := s.runFor(ctx, s.r.uncachedReader(), actor)
	if !ok || res.ID != string(run.UID) {
		return substrate.ErrNoSuchTurn
	}
	if run.Status.Substrate.Outcome != nil {
		return substrate.ErrTurnCompleted
	}
	if run.Status.Phase != sympoziumv1alpha1.AgentRunPhaseRunning {
		return substrate.ErrNoSuchTurn
	}
	result := string(res.Result)
	if len(result) > substrateMaxResultBytes || !json.Valid(res.Result) {
		result = ""
	}
	// One-second resolution, as the API stores it, so the digest computed
	// here is the digest of what status holds.
	now := metav1.NewTime(time.Now().UTC().Truncate(time.Second))
	stored := false
	if err := s.r.updateStatusWithRetry(ctx, run, func(ar *sympoziumv1alpha1.AgentRun) {
		stored = false
		st := ar.Status.Substrate
		if ar.UID != run.UID || st == nil || st.Outcome != nil || st.ReceiptDigest != "" || ar.Status.Phase != sympoziumv1alpha1.AgentRunPhaseRunning {
			return
		}
		st.Outcome = &sympoziumv1alpha1.SubstrateOutcome{ExitCode: int32(res.ExitCode), Result: result, ReportedAt: now}
		st.ReceiptDigest = substrateReceiptDigest(ar, st, st.Outcome)
		stored = true
	}); err != nil {
		return err
	}
	if !stored {
		// Another post, or the end of the run, got there first.
		return substrate.ErrTurnCompleted
	}
	s.storeLogs(ctx, run, res.Logs)
	return nil
}

// storeLogs keeps the log tail beside the run. Losing it does not lose the
// run's result, so a failure here is not the actor's to retry.
func (s *SubstrateTurns) storeLogs(ctx context.Context, run *sympoziumv1alpha1.AgentRun, logs string) {
	if logs == "" {
		return
	}
	if over := len(logs) - substrateMaxLogBytes; over > 0 {
		logs = logs[over:]
	}
	logs = strings.ToValidUTF8(logs, "\uFFFD")
	cm := corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: substrateLogsConfigMapPrefix + run.Name, Namespace: run.Namespace,
			Labels: map[string]string{"sympozium.ai/component": "run-logs", "sympozium.ai/agent-run": run.Name}},
		Data: map[string]string{substrateLogsKey: logs},
	}
	if err := controllerutil.SetControllerReference(run, &cm, s.r.Scheme); err != nil {
		return
	}
	if err := s.r.Create(ctx, &cm); err != nil && !errors.IsAlreadyExists(err) {
		s.r.Log.Info("could not store actor logs", "run", run.Name, "error", err.Error())
	}
}

// SetupSubstrate enables the Substrate backend on the reconciler and returns
// the turn endpoint to run beside it.
//
// The turn endpoint is nil when it would be served in clear text and the
// operator has not acknowledged that (Settings.AllowPlaintextGatewayHop). The
// backend is still configured, so it can release actors it already made, but
// it reports transport_invalid and refuses every run.
func SetupSubstrate(r *AgentRunReconciler, s *substrate.Settings) (*SubstrateTurnServer, error) {
	if err := s.ValidateForController(); err != nil {
		return nil, err
	}
	api, err := s.Dial()
	if err != nil {
		return nil, err
	}
	verifier, err := s.Verifier()
	if err != nil {
		return nil, err
	}
	r.Substrate = &SubstrateConfig{
		API: api, Image: s.RunnerImage, Audience: s.Audience,
		// Neither endpoint has a TLS listener for actors yet: Substrate's
		// egress gateway cannot be given the CA that would verify one.
		TurnEndpoint:      SubstrateEndpoint{Hostname: s.TurnHost, Port: s.TurnPort},
		ModelGateway:      SubstrateEndpoint{Hostname: s.GatewayHost, Port: s.GatewayPort},
		AllowPlaintextHop: s.AllowPlaintextGatewayHop,
		CPUMillicores:     500, MemoryBytes: 512 << 20,
	}
	if !r.Substrate.transportValid() {
		return nil, nil
	}
	return &SubstrateTurnServer{Addr: s.TurnListen, Handler: substrate.TurnHandler{Verifier: verifier, Store: r.SubstrateTurns()}}, nil
}

// SubstrateTurnServer serves the turn endpoint. It runs on every replica,
// leader or not: it holds no state, and an actor's request may land anywhere.
type SubstrateTurnServer struct {
	Addr    string
	Handler http.Handler
}

func (s *SubstrateTurnServer) NeedLeaderElection() bool { return false }

func (s *SubstrateTurnServer) Start(ctx context.Context) error {
	// Every request is a small JSON exchange, so the whole of one is bounded:
	// an actor cannot hold a connection open by trickling a result body.
	server := &http.Server{Addr: s.Addr, Handler: s.Handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: time.Minute, WriteTimeout: time.Minute, IdleTimeout: time.Minute, MaxHeaderBytes: 64 << 10}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdown)
	return nil
}
