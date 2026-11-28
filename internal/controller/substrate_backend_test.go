package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/controller/taskmodes"
	"github.com/sympozium-ai/sympozium/internal/harnessgateway"
	"github.com/sympozium-ai/sympozium/internal/modelkey"
	"github.com/sympozium-ai/sympozium/internal/substrate"
)

// countingSubstrate is a Control API that counts what it is asked to do and
// what it actually creates, and can fail a step a number of times.
type countingSubstrate struct {
	mu       sync.Mutex
	actors   map[string]*substrate.Actor
	egress   map[string]int // policies set, per actor
	calls    map[string]int // every call, by verb
	created  int            // actors that came into existence
	deleted  int
	failNext map[string]int // verb -> how many more calls fail
	seq      int
	// onSuspend runs inside SuspendActor, so a test can see what existed at
	// the moment the turn was stopped.
	onSuspend func()
	// noUID makes CreateActor return actors without a UID.
	noUID bool
}

func newCountingSubstrate() *countingSubstrate {
	return &countingSubstrate{actors: map[string]*substrate.Actor{}, egress: map[string]int{}, calls: map[string]int{}, failNext: map[string]int{}}
}

func (f *countingSubstrate) enter(verb string) error {
	f.calls[verb]++
	if f.failNext[verb] > 0 {
		f.failNext[verb]--
		return fmt.Errorf("%s: control API unavailable", verb)
	}
	return nil
}

func (f *countingSubstrate) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		n += c
	}
	return n
}

func (f *countingSubstrate) count(verb string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[verb]
}

func (f *countingSubstrate) EnsureAtespace(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.enter("EnsureAtespace")
}

func (f *countingSubstrate) EnsureTemplate(context.Context, substrate.Template) (substrate.TemplateStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return substrate.TemplateStatus{GoldenReady: true}, f.enter("EnsureTemplate")
}

func (f *countingSubstrate) CreateActor(_ context.Context, atespace, name, _ string) (substrate.Actor, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("CreateActor"); err != nil {
		return substrate.Actor{}, false, err
	}
	key := atespace + "/" + name
	if a, ok := f.actors[key]; ok {
		return *a, false, nil
	}
	f.seq++
	f.created++
	a := &substrate.Actor{Atespace: atespace, Name: name, UID: fmt.Sprintf("actor-uid-%d", f.seq), State: substrate.StateSuspended}
	if f.noUID {
		a.UID = ""
	}
	f.actors[key] = a
	return *a, true, nil
}

var errNoActor = errors.New("no such actor")

func (f *countingSubstrate) GetActor(_ context.Context, atespace, name string) (substrate.Actor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("GetActor"); err != nil {
		return substrate.Actor{}, err
	}
	a, ok := f.actors[atespace+"/"+name]
	if !ok {
		return substrate.Actor{}, errNoActor
	}
	return *a, nil
}

func (f *countingSubstrate) ResumeActor(_ context.Context, atespace, name string) (substrate.Actor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("ResumeActor"); err != nil {
		return substrate.Actor{}, err
	}
	a, ok := f.actors[atespace+"/"+name]
	if !ok {
		return substrate.Actor{}, errNoActor
	}
	a.State, a.Worker = substrate.StateRunning, "worker-1"
	return *a, nil
}

func (f *countingSubstrate) SuspendActor(_ context.Context, atespace, name string, _ bool) (substrate.Actor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("SuspendActor"); err != nil {
		return substrate.Actor{}, err
	}
	if f.onSuspend != nil {
		f.onSuspend()
	}
	a, ok := f.actors[atespace+"/"+name]
	if !ok {
		return substrate.Actor{}, errNoActor
	}
	a.State, a.Worker = substrate.StateSuspended, ""
	return *a, nil
}

func (f *countingSubstrate) DeleteActor(_ context.Context, atespace, name string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("DeleteActor"); err != nil {
		return false, err
	}
	if _, ok := f.actors[atespace+"/"+name]; ok {
		delete(f.actors, atespace+"/"+name)
		f.deleted++
	}
	return true, nil
}

func (f *countingSubstrate) SetEgress(_ context.Context, atespace, name string, _ []substrate.EgressTarget) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("SetEgress"); err != nil {
		return err
	}
	f.egress[atespace+"/"+name]++
	return nil
}

// substrateUnit is a backend=substrate run with everything it needs: its
// Agent granting the key, the key, the model gateway with the openai profile
// and a controller configured for Substrate with the clear-text hop
// acknowledged. Tests take away one thing at a time.
func substrateUnit(t *testing.T, mutate func(run *sympoziumv1alpha1.AgentRun, agent *sympoziumv1alpha1.Agent, key *corev1.Secret), extra ...client.Object) (*AgentRunReconciler, *sympoziumv1alpha1.AgentRun, *countingSubstrate) {
	t.Helper()
	run := newTestRun()
	run.Name, run.UID = "sub-run", types.UID("sub-run-uid")
	run.Spec.Backend = "substrate"
	run.Spec.Model = sympoziumv1alpha1.ModelSpec{Provider: "openai", Model: "m", AuthSecretRef: "model-key"}
	agent := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: run.Spec.AgentRef, Namespace: run.Namespace, UID: "agent-uid"},
		Spec:       sympoziumv1alpha1.AgentSpec{AuthRefs: []sympoziumv1alpha1.SecretRef{{Provider: "openai", Secret: "model-key"}}},
	}
	key := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "model-key", Namespace: run.Namespace, UID: "key-uid"},
		Data:       map[string][]byte{"OPENAI_API_KEY": []byte("sk-the-agents-real-key")},
	}
	if mutate != nil {
		mutate(run, agent, key)
	}
	r := newAgentRunTestReconciler(t, append([]client.Object{run, agent, key}, extra...)...)
	set, err := harnessgateway.ParseSet([]byte(mediationProfiles))
	if err != nil {
		t.Fatal(err)
	}
	r.HarnessGateway = &HarnessGatewayConfig{URL: mediationGatewayURL, Profiles: fixedProfiles{set}, Reader: r.Client}
	api := newCountingSubstrate()
	r.Substrate = &SubstrateConfig{
		API: api, Image: "registry.example/agent-runner@sha256:" + strings.Repeat("b", 64), Audience: "sympozium",
		TurnEndpoint:      SubstrateEndpoint{Hostname: "turns.test.svc", Port: 8090},
		ModelGateway:      SubstrateEndpoint{Hostname: "gateway.test.svc", Port: 8080},
		AllowPlaintextHop: true,
	}
	return r, run, api
}

// reconcileUntil reconciles the run like the manager would, until done says
// stop, and returns the stored run.
func reconcileUntil(t *testing.T, r *AgentRunReconciler, run *sympoziumv1alpha1.AgentRun, done func(*sympoziumv1alpha1.AgentRun) bool) *sympoziumv1alpha1.AgentRun {
	t.Helper()
	var stored sympoziumv1alpha1.AgentRun
	for i := 0; i < 10; i++ {
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)}); err != nil {
			t.Fatalf("reconcile %d: %v", i, err)
		}
		if err := r.Get(context.Background(), client.ObjectKeyFromObject(run), &stored); err != nil {
			t.Fatal(err)
		}
		if done(&stored) {
			return &stored
		}
	}
	t.Fatalf("run did not get there: phase %q error %q substrate %+v", stored.Status.Phase, stored.Status.Error, stored.Status.Substrate)
	return nil
}

func isRunning(run *sympoziumv1alpha1.AgentRun) bool {
	return run.Status.Phase == sympoziumv1alpha1.AgentRunPhaseRunning
}

func isTerminal(run *sympoziumv1alpha1.AgentRun) bool { return run.Status.Phase.IsTerminal() }

func bindingSecret(t *testing.T, r *AgentRunReconciler, run *sympoziumv1alpha1.AgentRun) *corev1.Secret {
	t.Helper()
	var secret corev1.Secret
	err := r.Get(context.Background(), actorModelBindingKey(run), &secret)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return &secret
}

// assertNothingCreated is the "a refused run creates nothing" half of the
// contract: no binding to an actor, no Control API call at all, no key
// binding, no logs, no Job.
func assertNothingCreated(t *testing.T, r *AgentRunReconciler, run *sympoziumv1alpha1.AgentRun, api *countingSubstrate) {
	t.Helper()
	if run.Status.Substrate != nil {
		t.Errorf("a refused run was bound to an actor: %+v", run.Status.Substrate)
	}
	if n := api.total(); n != 0 {
		t.Errorf("a refused run made %d Control API calls: %v", n, api.calls)
	}
	if secret := bindingSecret(t, r, run); secret != nil {
		t.Errorf("a refused run has a key binding Secret")
	}
	var logs corev1.ConfigMap
	if err := r.Get(context.Background(), client.ObjectKey{Namespace: run.Namespace, Name: substrateLogsConfigMapPrefix + run.Name}, &logs); !apierrors.IsNotFound(err) {
		t.Errorf("a refused run has a logs ConfigMap: %v", err)
	}
	var jobs batchv1.JobList
	if err := r.List(context.Background(), &jobs, client.InNamespace(run.Namespace)); err != nil || len(jobs.Items) != 0 {
		t.Errorf("a refused substrate run produced %d Jobs (err %v); it must never fall back", len(jobs.Items), err)
	}
	if run.Status.JobName != "" {
		t.Errorf("a refused substrate run has status.jobName %q", run.Status.JobName)
	}
}

// 1a: every refusal the backend makes before doing work ends the run as
// ExecutionRefused with a typed code readable through Result(), and creates
// nothing.
func TestSubstrateRefusalsAreTypedAndCreateNothing(t *testing.T) {
	type fixture = func(run *sympoziumv1alpha1.AgentRun, agent *sympoziumv1alpha1.Agent, key *corev1.Secret)
	cases := []struct {
		name   string
		mutate fixture
		// configure changes the controller, after the fixture exists.
		configure func(r *AgentRunReconciler)
		want      RefusalCode
		// keyStays is the pin the key had before and must still have.
		keyStays string
	}{
		{name: "backend not configured on this controller", configure: func(r *AgentRunReconciler) { r.Substrate = nil }, want: RefusalSubstrateNotConfigured},
		{name: "plaintext hop not acknowledged", configure: func(r *AgentRunReconciler) { r.Substrate.AllowPlaintextHop = false }, want: RefusalSubstrateTransportNotAcknowledged},
		{name: "harness runtime task", mutate: func(run *sympoziumv1alpha1.AgentRun, _ *sympoziumv1alpha1.Agent, _ *corev1.Secret) {
			run.Spec.Task = &sympoziumv1alpha1.TaskSpec{Mode: taskmodes.Harness, Parameters: map[string]string{"prompt": "p", "image": harnessTestImage}}
		}, want: RefusalSubstrateUnsupportedRun},
		{name: "spec.env", mutate: func(run *sympoziumv1alpha1.AgentRun, _ *sympoziumv1alpha1.Agent, _ *corev1.Secret) {
			run.Spec.Env = map[string]string{"FOO": "bar"}
		}, want: RefusalSubstrateUnsupportedRun},
		{name: "enduring lifecycle", mutate: func(run *sympoziumv1alpha1.AgentRun, _ *sympoziumv1alpha1.Agent, _ *corev1.Secret) {
			run.Spec.ExecutionLifecycle = "enduring"
		}, want: RefusalSubstrateUnsupportedRun},
		{name: "server mode", mutate: func(run *sympoziumv1alpha1.AgentRun, _ *sympoziumv1alpha1.Agent, _ *corev1.Secret) {
			run.Spec.Mode = "server"
		}, want: RefusalSubstrateUnsupportedRun},
		{name: "lifecycle hooks and gates", mutate: func(run *sympoziumv1alpha1.AgentRun, _ *sympoziumv1alpha1.Agent, _ *corev1.Secret) {
			run.Spec.Lifecycle = &sympoziumv1alpha1.LifecycleHooks{PostRun: []sympoziumv1alpha1.LifecycleHookContainer{{Name: "gate", Image: "busybox", Gate: true}}}
		}, want: RefusalSubstrateUnsupportedRun},
		{name: "missing authSecretRef", mutate: func(run *sympoziumv1alpha1.AgentRun, _ *sympoziumv1alpha1.Agent, _ *corev1.Secret) {
			run.Spec.Model.AuthSecretRef = ""
		}, want: RefusalSubstrateUnsupportedRun},
		{name: "modelRef", mutate: func(run *sympoziumv1alpha1.AgentRun, _ *sympoziumv1alpha1.Agent, _ *corev1.Secret) {
			run.Spec.Model.ModelRef = "local-model"
		}, want: RefusalSubstrateUnsupportedRun},
		{name: "connectionRef", mutate: func(run *sympoziumv1alpha1.AgentRun, _ *sympoziumv1alpha1.Agent, _ *corev1.Secret) {
			run.Spec.Model.ConnectionRef = "a-connection"
		}, want: RefusalSubstrateUnsupportedRun},
		{name: "skills on the run", mutate: func(run *sympoziumv1alpha1.AgentRun, _ *sympoziumv1alpha1.Agent, _ *corev1.Secret) {
			run.Spec.Skills = []sympoziumv1alpha1.SkillRef{{SkillPackRef: "k8s-ops"}}
		}, want: RefusalSubstrateUnsupportedRun},
		{name: "skills on the Agent", mutate: func(_ *sympoziumv1alpha1.AgentRun, agent *sympoziumv1alpha1.Agent, _ *corev1.Secret) {
			agent.Spec.Skills = []sympoziumv1alpha1.SkillRef{{SkillPackRef: "k8s-ops"}}
		}, want: RefusalSubstrateUnsupportedRun},
		{name: "MCP tools on the Agent", mutate: func(_ *sympoziumv1alpha1.AgentRun, agent *sympoziumv1alpha1.Agent, _ *corev1.Secret) {
			agent.Spec.MCPServers = []sympoziumv1alpha1.MCPServerRef{{Name: "tools"}}
		}, want: RefusalSubstrateUnsupportedRun},
		{name: "run name not a DNS label", mutate: func(run *sympoziumv1alpha1.AgentRun, _ *sympoziumv1alpha1.Agent, _ *corev1.Secret) {
			run.Name = "sub.run"
		}, want: RefusalSubstrateUnsupportedRun},
		{name: "model gateway not enabled", configure: func(r *AgentRunReconciler) { r.HarnessGateway = nil }, want: RefusalSubstrateNoModelRoute},
		{name: "no gateway profile for the provider", mutate: func(run *sympoziumv1alpha1.AgentRun, agent *sympoziumv1alpha1.Agent, _ *corev1.Secret) {
			run.Spec.Model.Provider = "mistral"
			agent.Spec.AuthRefs = []sympoziumv1alpha1.SecretRef{{Provider: "mistral", Secret: "model-key"}}
		}, want: RefusalSubstrateNoModelRoute},
		{name: "key holds nothing the provider reads", mutate: func(_ *sympoziumv1alpha1.AgentRun, _ *sympoziumv1alpha1.Agent, key *corev1.Secret) {
			key.Data = map[string][]byte{"SOMETHING_ELSE": []byte("x")}
		}, want: RefusalSubstrateNoModelRoute},
		{name: "baseURL is not the operator profile's upstream", mutate: func(run *sympoziumv1alpha1.AgentRun, _ *sympoziumv1alpha1.Agent, _ *corev1.Secret) {
			run.Spec.Model.BaseURL = "https://exfil.example/v1"
		}, want: RefusalModelKeyOriginMismatch},
		{name: "key pinned to another origin", mutate: func(_ *sympoziumv1alpha1.AgentRun, _ *sympoziumv1alpha1.Agent, key *corev1.Secret) {
			key.Annotations = map[string]string{modelkey.OriginAnnotation: "https://elsewhere.example"}
		}, want: RefusalModelKeyOriginMismatch, keyStays: "https://elsewhere.example"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, run, api := substrateUnit(t, tc.mutate)
			if tc.configure != nil {
				tc.configure(r)
			}
			stored := reconcileUntil(t, r, run, isTerminal)
			backend := substrateBackend{r: r}
			if phase := backend.Phase(stored); phase != ExecutionRefused {
				t.Fatalf("Phase() = %s (status.error %q), want %s", phase, stored.Status.Error, ExecutionRefused)
			}
			res := backend.Result(stored)
			if res == nil || res.Outcome != ExecutionRefused || res.PublicReason != string(tc.want) {
				t.Fatalf("Result() = %+v, want Refused with %s (status.error %q)", res, tc.want, stored.Status.Error)
			}
			if res.Receipt.Digest != "" {
				t.Errorf("a refused run has a receipt %q", res.Receipt.Digest)
			}
			if name, viaSeam, err := r.ExecutionResult(context.Background(), stored); err != nil || name != ExecutionBackendSubstrate || viaSeam == nil || viaSeam.PublicReason != string(tc.want) {
				t.Errorf("ExecutionResult() = %s %+v %v", name, viaSeam, err)
			}
			assertNothingCreated(t, r, stored, api)
			var key corev1.Secret
			if err := r.Get(context.Background(), client.ObjectKey{Namespace: run.Namespace, Name: "model-key"}, &key); err != nil {
				t.Fatal(err)
			}
			if pinned := modelkey.PinnedOrigin(&key); pinned != tc.keyStays {
				t.Errorf("a refused run left the key pinned to %q, want %q", pinned, tc.keyStays)
			}
			t.Logf("%s: %s", res.PublicReason, stored.Status.Error)
		})
	}
}

// 1a: shared admission refusals keep their MODEL_KEY_* codes on a Substrate
// run, and a plain failure is not relabelled as a refusal.
func TestSubstrateSharedAdmissionRefusalKeepsItsCode(t *testing.T) {
	r, run, api := substrateUnit(t, func(_ *sympoziumv1alpha1.AgentRun, agent *sympoziumv1alpha1.Agent, _ *corev1.Secret) {
		agent.Spec.AuthRefs = nil // the Agent does not grant the key
	})
	stored := reconcileUntil(t, r, run, isTerminal)
	res := substrateBackend{r: r}.Result(stored)
	if res == nil || res.Outcome != ExecutionRefused || res.PublicReason != string(RefusalModelKeyNotGranted) {
		t.Fatalf("Result() = %+v (status.error %q), want Refused %s", res, stored.Status.Error, RefusalModelKeyNotGranted)
	}
	assertNothingCreated(t, r, stored, api)

	failed := newTestRun()
	failed.Spec.Backend = "substrate"
	failed.Status.Phase = sympoziumv1alpha1.AgentRunPhaseFailed
	now := metav1.Now()
	failed.Status.CompletedAt = &now
	if phase := (substrateBackend{}).Phase(failed); phase != ExecutionFailed {
		t.Errorf("a plain failure reads as %s", phase)
	}
}

// 2: only spec.backend=substrate selects the backend. A controller configured
// for Substrate leaves every other run where it was, and a run that asks for
// Substrate is never the Job backend's.
func TestSubstrateIsOptInPerRun(t *testing.T) {
	ctx := context.Background()
	// A plain run on a controller configured for Substrate is a Job.
	r, run, api := substrateUnit(t, func(run *sympoziumv1alpha1.AgentRun, _ *sympoziumv1alpha1.Agent, _ *corev1.Secret) {
		run.Spec.Backend = ""
	})
	stored := reconcileUntil(t, r, run, func(run *sympoziumv1alpha1.AgentRun) bool { return run.Status.JobName != "" || isTerminal(run) })
	if stored.Status.JobName == "" || stored.Status.Substrate != nil || api.total() != 0 {
		t.Fatalf("a plain run on a Substrate-configured controller: job %q substrate %+v control API calls %v (error %q)",
			stored.Status.JobName, stored.Status.Substrate, api.calls, stored.Status.Error)
	}
	if binding := bindingSecret(t, r, stored); binding != nil && binding.Annotations[harnessgateway.ActorAnnotation] != "" {
		t.Error("a plain run was bound to an actor")
	}

	backends := r.executionBackends()
	owner := func(run *sympoziumv1alpha1.AgentRun) ExecutionBackendName {
		backend, err := backends.admitted(ctx, run)
		if err != nil {
			t.Fatal(err)
		}
		return backend.Name()
	}
	for name, tc := range map[string]struct {
		mutate func(*sympoziumv1alpha1.AgentRun)
		want   ExecutionBackendName
	}{
		"no backend":                {func(*sympoziumv1alpha1.AgentRun) {}, ExecutionBackendJob},
		"backend job":               {func(run *sympoziumv1alpha1.AgentRun) { run.Spec.Backend = "job" }, ExecutionBackendJob},
		"backend substrate":         {func(run *sympoziumv1alpha1.AgentRun) { run.Spec.Backend = "substrate" }, ExecutionBackendSubstrate},
		"already bound to an actor": {func(run *sympoziumv1alpha1.AgentRun) { run.Status.Substrate = &sympoziumv1alpha1.SubstrateStatus{} }, ExecutionBackendSubstrate},
		"started as a Job, backend edited to substrate": {func(run *sympoziumv1alpha1.AgentRun) {
			run.Spec.Backend, run.Status.JobName = "substrate", "its-job"
		}, ExecutionBackendJob},
	} {
		candidate := newTestRun()
		tc.mutate(candidate)
		if got := owner(candidate); got != tc.want {
			t.Errorf("%s: owned by %s, want %s", name, got, tc.want)
		}
		jobOwns, _ := backends.job.Owns(ctx, candidate)
		subOwns, _ := backends.substrate.Owns(ctx, candidate)
		if jobOwns == subOwns {
			t.Errorf("%s: job owns %v and substrate owns %v; exactly one must", name, jobOwns, subOwns)
		}
	}

	// A run with a scoped Celln identity is Celln's (Reconcile sends it there
	// first), whatever spec.backend is edited to: neither of these two owns
	// it, and its key is not judged as an actor's.
	scoped := newTestRun()
	scoped.Spec.Backend, scoped.Status.CellnScoped = "substrate", &sympoziumv1alpha1.CellnScopedStatus{}
	if jobOwns, _ := backends.job.Owns(ctx, scoped); jobOwns || substrateOwns(scoped) {
		t.Errorf("a scoped Celln run edited to backend substrate: job owns %v, substrate owns %v; want neither", jobOwns, substrateOwns(scoped))
	}

	// The other direction: asking for Substrate where it is not configured is
	// refused, typed, and never becomes a Job. Ownership does not depend on
	// configuration.
	r, run, api = substrateUnit(t, nil)
	r.Substrate = nil
	if got := owner(run); got != ExecutionBackendSubstrate {
		t.Errorf("without configuration a backend=substrate run is owned by %s", got)
	}
	stored = reconcileUntil(t, r, run, isTerminal)
	if res := (substrateBackend{r: r}).Result(stored); res == nil || res.PublicReason != string(RefusalSubstrateNotConfigured) {
		t.Fatalf("Result() = %+v", res)
	}
	assertNothingCreated(t, r, stored, api)
}

// 1b: Start is idempotent. Whatever is replayed (a Control API failure
// between steps, a reconcile from a stale copy of the run, a controller
// restart), there is one actor, one egress policy on it and one key binding,
// and "attempted" is on the run before the first Control API call.
func TestSubstrateStartIsIdempotent(t *testing.T) {
	ctx := context.Background()
	for _, failing := range []string{"", "EnsureAtespace", "EnsureTemplate", "CreateActor", "SetEgress", "ResumeActor"} {
		t.Run("replay after "+failing, func(t *testing.T) {
			r, run, api := substrateUnit(t, nil)
			if failing != "" {
				api.failNext[failing] = 2
			}
			// The copy the first reconcile worked from: Pending, unbound.
			stale := run.DeepCopy()

			var firstBinding types.UID
			stored := reconcileUntil(t, r, run, func(run *sympoziumv1alpha1.AgentRun) bool {
				// "Attempted" precedes any Control API call.
				if api.total() > 0 && run.Status.Substrate == nil {
					t.Fatalf("Control API called (%v) before status.substrate was persisted", api.calls)
				}
				if secret := bindingSecret(t, r, run); secret != nil {
					if firstBinding == "" {
						firstBinding = secret.UID
					} else if secret.UID != firstBinding {
						t.Fatalf("the key binding was replaced: %s then %s", firstBinding, secret.UID)
					}
				}
				a, exists := api.actors[run.Namespace+"/"+run.Name]
				return isRunning(run) && exists && a.State == substrate.StateRunning
			})
			st := stored.Status.Substrate
			if st == nil || st.Actor != run.Name || st.Atespace != run.Namespace || st.ActorUID == "" {
				t.Fatalf("status.substrate = %+v", st)
			}
			before := api.total()

			// Replays of Start itself: from the stale Pending copy (a stale
			// cache, a restart that re-reads an old object) and from the
			// current one.
			in := executionStartInput{agent: &sympoziumv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: run.Spec.AgentRef, Namespace: run.Namespace}}}
			for i, replay := range []*sympoziumv1alpha1.AgentRun{stale.DeepCopy(), stale.DeepCopy(), stored.DeepCopy()} {
				if _, err := (substrateBackend{r: r}).Start(ctx, logr.Discard(), replay, in); err != nil {
					t.Fatalf("replay %d: %v", i, err)
				}
			}
			if after := api.total(); after != before {
				t.Errorf("replayed Start made %d more Control API calls on a run already Running: %v", after-before, api.calls)
			}

			if api.created != 1 || len(api.actors) != 1 {
				t.Errorf("%d actors created, %d exist; want exactly one (calls %v)", api.created, len(api.actors), api.calls)
			}
			if resumed := api.count("ResumeActor"); failing != "ResumeActor" && resumed != 1 {
				t.Errorf("ResumeActor called %d times, want 1", resumed)
			}
			var secrets corev1.SecretList
			if err := r.List(ctx, &secrets, client.InNamespace(run.Namespace), client.MatchingLabels{"sympozium.ai/component": "run-model-token"}); err != nil {
				t.Fatal(err)
			}
			if len(secrets.Items) != 1 || secrets.Items[0].UID != firstBinding ||
				secrets.Items[0].Annotations[harnessgateway.ActorAnnotation] != run.Namespace+"/"+run.Name {
				t.Errorf("want one key binding, the first one, for this actor; got %d: %+v", len(secrets.Items), secrets.Items)
			}
			var after sympoziumv1alpha1.AgentRun
			if err := r.Get(ctx, client.ObjectKeyFromObject(run), &after); err != nil {
				t.Fatal(err)
			}
			if after.Status.Substrate.ActorUID != st.ActorUID || !isRunning(&after) {
				t.Errorf("a replay moved the run: %+v phase %s", after.Status.Substrate, after.Status.Phase)
			}
			t.Logf("calls: %v", api.calls)
		})
	}
}

// 1b: a replayed Start on a run that already finished changes nothing: it
// does not make the run Running again and does not resume or recreate.
func TestSubstrateStartReplayAfterCompletionChangesNothing(t *testing.T) {
	ctx := context.Background()
	r, run, api := substrateUnit(t, nil)
	stale := run.DeepCopy()
	stored := reconcileUntil(t, r, run, isRunning)
	turns := r.SubstrateTurns()
	self := substrate.ActorRef{Atespace: run.Namespace, Name: run.Name, UID: stored.Status.Substrate.ActorUID}
	if err := turns.Complete(ctx, self, substrate.TurnResult{ID: string(run.UID), Result: json.RawMessage(`{"status":"success","response":"done"}`)}); err != nil {
		t.Fatal(err)
	}
	done := reconcileUntil(t, r, run, func(run *sympoziumv1alpha1.AgentRun) bool {
		return run.Status.Phase == sympoziumv1alpha1.AgentRunPhaseSucceeded && run.Status.Substrate.Released
	})
	before := api.total()
	in := executionStartInput{agent: &sympoziumv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: run.Spec.AgentRef, Namespace: run.Namespace}}}
	if _, err := (substrateBackend{r: r}).Start(ctx, logr.Discard(), stale, in); err != nil {
		t.Fatal(err)
	}
	var after sympoziumv1alpha1.AgentRun
	if err := r.Get(ctx, client.ObjectKeyFromObject(run), &after); err != nil {
		t.Fatal(err)
	}
	if after.Status.Phase != sympoziumv1alpha1.AgentRunPhaseSucceeded || api.total() != before || api.created != 1 || bindingSecret(t, r, run) != nil {
		t.Errorf("a stale Start on a finished run: phase %s, %d new calls, %d actors created, binding %v",
			after.Status.Phase, api.total()-before, api.created, bindingSecret(t, r, run) != nil)
	}
	if after.Status.Substrate.ReceiptDigest != done.Status.Substrate.ReceiptDigest {
		t.Errorf("the receipt changed")
	}
}

// The turn belongs to one actor, by UID. Once the UID is on the run's status
// a token for the same name is the run's only if it carries that UID: one
// with none, or with another (an actor deleted and created again under the
// name), neither claims the turn nor completes it. Before the UID is
// recorded the run is not Running, and nothing is claimable at all.
func TestSubstrateTurnIsHeldToTheRecordedActorUID(t *testing.T) {
	ctx := context.Background()
	r, run, api := substrateUnit(t, nil)
	api.failNext["CreateActor"] = 1
	turns := r.SubstrateTurns()
	result := substrate.TurnResult{ID: string(run.UID), Result: json.RawMessage(`{"status":"success","response":"done"}`)}
	named := func(uid string) substrate.ActorRef {
		return substrate.ActorRef{Atespace: run.Namespace, Name: run.Name, UID: uid}
	}
	refused := func(when string, actor substrate.ActorRef) {
		t.Helper()
		if _, ok, err := turns.Claim(ctx, actor); ok || err != nil {
			t.Errorf("%s: claim ok=%v err=%v, want refused", when, ok, err)
		}
		if err := turns.Complete(ctx, actor, result); !errors.Is(err, substrate.ErrNoSuchTurn) {
			t.Errorf("%s: complete err=%v, want ErrNoSuchTurn", when, err)
		}
	}

	bound := reconcileUntil(t, r, run, func(run *sympoziumv1alpha1.AgentRun) bool { return run.Status.Substrate != nil })
	if bound.Status.Substrate.ActorUID != "" || isRunning(bound) {
		t.Fatalf("want a bound run with no actor yet: phase %q substrate %+v", bound.Status.Phase, bound.Status.Substrate)
	}
	refused("before the UID is recorded, a token with no UID", named(""))
	refused("before the UID is recorded, a token with a UID", named("actor-uid-1"))

	stored := reconcileUntil(t, r, run, isRunning)
	uid := stored.Status.Substrate.ActorUID
	if uid == "" {
		t.Fatal("a Running run has no recorded actor UID")
	}
	refused("a token with no UID", named(""))
	refused("a token with a different UID", named("not-"+uid))

	turn, ok, err := turns.Claim(ctx, named(uid))
	if err != nil || !ok || turn.ID != string(run.UID) {
		t.Fatalf("the run's actor: claim %+v ok=%v err=%v", turn, ok, err)
	}
	// Claiming changed nothing for the others.
	refused("after the claim, a token with no UID", named(""))
	refused("after the claim, a token with a different UID", named("not-"+uid))
	var untouched sympoziumv1alpha1.AgentRun
	if err := r.Get(ctx, client.ObjectKeyFromObject(run), &untouched); err != nil {
		t.Fatal(err)
	}
	if untouched.Status.Substrate.Outcome != nil {
		t.Fatalf("a refused post was recorded: %+v", untouched.Status.Substrate.Outcome)
	}
	if err := turns.Complete(ctx, named(uid), result); err != nil {
		t.Fatalf("the run's actor: complete: %v", err)
	}
}

// An actor the Control API returns without a UID cannot be held to anything,
// so the run fails instead of becoming Running.
func TestSubstrateRunFailsOnAnActorWithoutAUID(t *testing.T) {
	r, run, api := substrateUnit(t, nil)
	api.noUID = true
	failed := reconcileUntil(t, r, run, isTerminal)
	if failed.Status.Phase != sympoziumv1alpha1.AgentRunPhaseFailed || !strings.Contains(failed.Status.Error, "without a UID") {
		t.Fatalf("phase %s error %q", failed.Status.Phase, failed.Status.Error)
	}
	if n := api.count("ResumeActor"); n != 0 {
		t.Errorf("the actor was resumed %d times", n)
	}
}

// 1c: the outcome and its receipt are written once. Result() carries the
// receipt, and a second post from the actor is refused and changes nothing.
func TestSubstrateReceiptIsWrittenOnce(t *testing.T) {
	ctx := context.Background()
	r, run, _ := substrateUnit(t, nil)
	stored := reconcileUntil(t, r, run, isRunning)
	backend := substrateBackend{r: r}
	if res := backend.Result(stored); res != nil {
		t.Fatalf("a Running run has a Result: %+v", res)
	}
	turns := r.SubstrateTurns()
	self := substrate.ActorRef{Atespace: run.Namespace, Name: run.Name, UID: stored.Status.Substrate.ActorUID}
	first := substrate.TurnResult{ID: string(run.UID), Result: json.RawMessage(`{"status":"success","response":"the first answer"}`), Logs: "first\n"}
	if err := turns.Complete(ctx, self, first); err != nil {
		t.Fatalf("first result: %v", err)
	}
	var recorded sympoziumv1alpha1.AgentRun
	if err := r.Get(ctx, client.ObjectKeyFromObject(run), &recorded); err != nil {
		t.Fatal(err)
	}
	st := recorded.Status.Substrate
	if st.Outcome == nil || !strings.HasPrefix(st.ReceiptDigest, "sha256:") || len(st.ReceiptDigest) != len("sha256:")+64 {
		t.Fatalf("outcome %+v receipt %q", st.Outcome, st.ReceiptDigest)
	}
	// The digest is over exactly what status holds, and over the actor.
	if want := substrateReceiptDigest(&recorded, st, st.Outcome); st.ReceiptDigest != want {
		t.Errorf("receipt %s is not the digest of the stored outcome %s", st.ReceiptDigest, want)
	}
	other := st.DeepCopy()
	other.ActorUID = "another-actor"
	if substrateReceiptDigest(&recorded, other, st.Outcome) == st.ReceiptDigest {
		t.Error("the receipt does not cover the actor UID")
	}
	changed := *st.Outcome
	changed.Result = `{"status":"success","response":"forged"}`
	if substrateReceiptDigest(&recorded, st, &changed) == st.ReceiptDigest {
		t.Error("the receipt does not cover the result")
	}

	// A second post, while the run is still Running: a different result, and
	// the very same one. Both are refused; nothing moves.
	for name, second := range map[string]substrate.TurnResult{
		"a different result": {ID: string(run.UID), ExitCode: 1, Result: json.RawMessage(`{"status":"error","error":"overwritten"}`), Logs: "second\n"},
		"the same result":    first,
	} {
		if err := turns.Complete(ctx, self, second); !errors.Is(err, substrate.ErrTurnCompleted) {
			t.Errorf("%s posted again: err %v, want ErrTurnCompleted", name, err)
		}
	}
	var again sympoziumv1alpha1.AgentRun
	if err := r.Get(ctx, client.ObjectKeyFromObject(run), &again); err != nil {
		t.Fatal(err)
	}
	if again.Status.Substrate.ReceiptDigest != st.ReceiptDigest || again.Status.Substrate.Outcome.Result != st.Outcome.Result ||
		!again.Status.Substrate.Outcome.ReportedAt.Equal(&st.Outcome.ReportedAt) {
		t.Fatalf("a second post changed the outcome or the receipt: %+v", again.Status.Substrate)
	}
	var logs corev1.ConfigMap
	if err := r.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: substrateLogsConfigMapPrefix + run.Name}, &logs); err != nil || logs.Data[substrateLogsKey] != "first\n" {
		t.Errorf("logs = %q (err %v), want the first post's", logs.Data[substrateLogsKey], err)
	}

	done := reconcileUntil(t, r, run, isTerminal)
	res := backend.Result(done)
	if res == nil || res.Outcome != ExecutionSucceeded || res.Output != "the first answer" || res.Receipt.Digest != st.ReceiptDigest {
		t.Fatalf("Result() = %+v, want Succeeded with receipt %s", res, st.ReceiptDigest)
	}
	// And after the run ended.
	if err := turns.Complete(ctx, self, substrate.TurnResult{ID: string(run.UID), Result: json.RawMessage(`{"status":"success","response":"late"}`)}); !errors.Is(err, substrate.ErrTurnCompleted) {
		t.Errorf("a post after completion: err %v, want ErrTurnCompleted", err)
	}
	released := reconcileUntil(t, r, run, func(run *sympoziumv1alpha1.AgentRun) bool { return run.Status.Substrate.Released })
	if got := backend.Result(released).Receipt.Digest; got != st.ReceiptDigest {
		t.Errorf("the receipt changed after release: %s", got)
	}
}

// 1d: Cancel stops the work and cleans nothing up; Release cleans up, is
// confirmed and idempotent, and does not need Cancel to have run.
func TestSubstrateCancelIsSeparateFromRelease(t *testing.T) {
	ctx := context.Background()
	if _, ok := ExecutionBackend(substrateBackend{}).(executionCanceler); !ok {
		t.Fatal("the Substrate backend has no Cancel")
	}
	for _, other := range []ExecutionBackend{jobBackend{}, cellnBackend{}} {
		if _, ok := other.(executionCanceler); ok {
			t.Errorf("%s gained a Cancel; its behaviour must not change", other.Name())
		}
	}

	t.Run("cancel revokes the key binding, then stops the turn, and deletes nothing", func(t *testing.T) {
		r, run, api := substrateUnit(t, nil)
		stored := reconcileUntil(t, r, run, isRunning)
		if bindingSecret(t, r, run) == nil {
			t.Fatal("a Running run has no key binding")
		}
		bindingAtSuspend := true
		api.onSuspend = func() { bindingAtSuspend = bindingSecret(t, r, run) != nil }
		backend := substrateBackend{r: r}
		if err := backend.Cancel(ctx, logr.Discard(), stored); err != nil {
			t.Fatal(err)
		}
		if api.count("SuspendActor") != 1 || bindingAtSuspend {
			t.Errorf("suspend calls %d, binding still present when the turn was stopped: %v; the binding must be revoked first", api.count("SuspendActor"), bindingAtSuspend)
		}
		if bindingSecret(t, r, run) != nil {
			t.Error("after Cancel the actor still has a key binding")
		}
		if a := api.actors[run.Namespace+"/"+run.Name]; a == nil || a.State != substrate.StateSuspended || api.deleted != 0 {
			t.Errorf("Cancel must stop the actor, not release it: %+v, %d deleted", a, api.deleted)
		}
		var after sympoziumv1alpha1.AgentRun
		if err := r.Get(ctx, client.ObjectKeyFromObject(run), &after); err != nil {
			t.Fatal(err)
		}
		if after.Status.Substrate.Released {
			t.Error("Cancel marked the run released")
		}
		// Idempotent, and Release still does its own work afterwards.
		if err := backend.Cancel(ctx, logr.Discard(), &after); err != nil {
			t.Fatalf("second Cancel: %v", err)
		}
		if done, err := backend.Release(ctx, logr.Discard(), &after); err != nil || !done {
			t.Fatalf("Release after Cancel: %v %v", done, err)
		}
		if api.deleted != 1 || len(api.actors) != 0 {
			t.Errorf("Release did not delete the actor: %d deleted, %d left", api.deleted, len(api.actors))
		}
	})

	t.Run("release without cancel is complete, confirmed and idempotent", func(t *testing.T) {
		r, run, api := substrateUnit(t, nil)
		stored := reconcileUntil(t, r, run, isRunning)
		backend := substrateBackend{r: r}
		// Not confirmed while the Control API cannot confirm it.
		api.failNext["DeleteActor"] = 1
		if done, err := backend.Release(ctx, logr.Discard(), stored); done || err == nil {
			t.Fatalf("Release reported done=%v err=%v while the delete failed", done, err)
		}
		if bindingSecret(t, r, run) != nil {
			t.Error("the key binding outlived an unconfirmed release; it must go first")
		}
		for i := 0; i < 3; i++ {
			var current sympoziumv1alpha1.AgentRun
			if err := r.Get(ctx, client.ObjectKeyFromObject(run), &current); err != nil {
				t.Fatal(err)
			}
			if done, err := backend.Release(ctx, logr.Discard(), &current); err != nil || !done {
				t.Fatalf("Release %d: done=%v err=%v", i, done, err)
			}
		}
		var after sympoziumv1alpha1.AgentRun
		if err := r.Get(ctx, client.ObjectKeyFromObject(run), &after); err != nil {
			t.Fatal(err)
		}
		if !after.Status.Substrate.Released || api.deleted != 1 || api.count("SuspendActor") != 0 {
			t.Errorf("released=%v deleted=%d suspends=%d; Release must not need Cancel", after.Status.Substrate.Released, api.deleted, api.count("SuspendActor"))
		}
		// A run that never started has nothing to release.
		if done, err := backend.Release(ctx, logr.Discard(), newTestRun()); err != nil || !done {
			t.Errorf("Release of an unbound run: %v %v", done, err)
		}
	})

	t.Run("a timeout cancels before the run is failed", func(t *testing.T) {
		r, run, api := substrateUnit(t, func(run *sympoziumv1alpha1.AgentRun, _ *sympoziumv1alpha1.Agent, _ *corev1.Secret) {
			run.Spec.Timeout = &metav1.Duration{Duration: time.Minute}
		})
		reconcileUntil(t, r, run, isRunning)
		long := metav1.NewTime(time.Now().Add(-time.Hour))
		if err := r.updateStatusWithRetry(ctx, run, func(ar *sympoziumv1alpha1.AgentRun) { ar.Status.StartedAt = &long }); err != nil {
			t.Fatal(err)
		}
		bindingAtSuspend := true
		api.onSuspend = func() { bindingAtSuspend = bindingSecret(t, r, run) != nil }
		failed := reconcileUntil(t, r, run, isTerminal)
		if failed.Status.Phase != sympoziumv1alpha1.AgentRunPhaseFailed || !strings.Contains(failed.Status.Error, "timed out") {
			t.Fatalf("phase %s error %q", failed.Status.Phase, failed.Status.Error)
		}
		if api.count("SuspendActor") != 1 || bindingAtSuspend || bindingSecret(t, r, run) != nil {
			t.Errorf("timeout: suspends %d, binding present at suspend %v, binding left %v", api.count("SuspendActor"), bindingAtSuspend, bindingSecret(t, r, run) != nil)
		}
		// A timeout is a failure of a run that started, not a refusal.
		if phase := (substrateBackend{r: r}).Phase(failed); phase != ExecutionFailed {
			t.Errorf("a timed-out run reads as %s", phase)
		}
		// The actor's late result is not accepted.
		self := substrate.ActorRef{Atespace: run.Namespace, Name: run.Name, UID: failed.Status.Substrate.ActorUID}
		if err := r.SubstrateTurns().Complete(ctx, self, substrate.TurnResult{ID: string(run.UID), Result: json.RawMessage(`{"status":"success","response":"late"}`)}); err == nil {
			t.Error("a cancelled run accepted a result")
		}
		released := reconcileUntil(t, r, run, func(run *sympoziumv1alpha1.AgentRun) bool { return run.Status.Substrate.Released })
		if api.deleted != 1 || released.Status.Substrate.Outcome != nil {
			t.Errorf("after the timeout: %d actors deleted, outcome %+v", api.deleted, released.Status.Substrate.Outcome)
		}
	})

	t.Run("a run with no spec.timeout still times out", func(t *testing.T) {
		r, run, api := substrateUnit(t, nil)
		started := reconcileUntil(t, r, run, isRunning)
		if started.Spec.Timeout != nil {
			t.Fatalf("the run has a timeout: %v", started.Spec.Timeout)
		}
		// Within the default, an actor that has not reported is left alone.
		recent := metav1.NewTime(time.Now().Add(-substrateDefaultTimeout / 2))
		if err := r.updateStatusWithRetry(ctx, run, func(ar *sympoziumv1alpha1.AgentRun) { ar.Status.StartedAt = &recent }); err != nil {
			t.Fatal(err)
		}
		if still := reconcileUntil(t, r, run, func(*sympoziumv1alpha1.AgentRun) bool { return true }); !isRunning(still) {
			t.Fatalf("phase %s error %q before the default timeout", still.Status.Phase, still.Status.Error)
		}
		long := metav1.NewTime(time.Now().Add(-2 * substrateDefaultTimeout))
		if err := r.updateStatusWithRetry(ctx, run, func(ar *sympoziumv1alpha1.AgentRun) { ar.Status.StartedAt = &long }); err != nil {
			t.Fatal(err)
		}
		failed := reconcileUntil(t, r, run, isTerminal)
		if failed.Status.Phase != sympoziumv1alpha1.AgentRunPhaseFailed || !strings.Contains(failed.Status.Error, "timed out") {
			t.Fatalf("phase %s error %q", failed.Status.Phase, failed.Status.Error)
		}
		if api.count("SuspendActor") != 1 || bindingSecret(t, r, run) != nil {
			t.Errorf("suspends %d, binding left %v", api.count("SuspendActor"), bindingSecret(t, r, run) != nil)
		}
	})

	t.Run("a run that cannot be started is failed after its timeout, then released", func(t *testing.T) {
		r, run, api := substrateUnit(t, func(run *sympoziumv1alpha1.AgentRun, _ *sympoziumv1alpha1.Agent, _ *corev1.Secret) {
			run.Spec.Timeout = &metav1.Duration{Duration: time.Minute}
		})
		api.failNext["CreateActor"] = 1 << 20
		// Within the timeout the start is retried and the run stays Pending.
		pending := reconcileUntil(t, r, run, func(run *sympoziumv1alpha1.AgentRun) bool { return api.count("CreateActor") >= 2 })
		if pending.Status.Phase.IsTerminal() || pending.Status.Substrate == nil || pending.Status.Substrate.AttemptedAt == nil {
			t.Fatalf("phase %q substrate %+v", pending.Status.Phase, pending.Status.Substrate)
		}
		long := metav1.NewTime(time.Now().Add(-time.Hour))
		if err := r.updateStatusWithRetry(ctx, run, func(ar *sympoziumv1alpha1.AgentRun) { ar.Status.Substrate.AttemptedAt = &long }); err != nil {
			t.Fatal(err)
		}
		before := api.count("CreateActor")
		failed := reconcileUntil(t, r, run, isTerminal)
		if failed.Status.Phase != sympoziumv1alpha1.AgentRunPhaseFailed || !strings.Contains(failed.Status.Error, "could not start the run within 1m") {
			t.Fatalf("phase %s error %q", failed.Status.Phase, failed.Status.Error)
		}
		if api.count("CreateActor") != before || api.created != 0 {
			t.Errorf("after the deadline: %d more CreateActor calls, %d actors created", api.count("CreateActor")-before, api.created)
		}
		// Not a refusal: the run was admitted and attempted.
		if phase := (substrateBackend{r: r}).Phase(failed); phase != ExecutionFailed {
			t.Errorf("reads as %s", phase)
		}
		released := reconcileUntil(t, r, run, func(run *sympoziumv1alpha1.AgentRun) bool { return run.Status.Substrate.Released })
		if bindingSecret(t, r, released) != nil || len(api.actors) != 0 {
			t.Errorf("after release: binding left %v, %d actors", bindingSecret(t, r, released) != nil, len(api.actors))
		}
	})

	t.Run("a delete while Running cancels, then releases", func(t *testing.T) {
		r, run, api := substrateUnit(t, nil)
		reconcileUntil(t, r, run, isRunning)
		var order []string
		api.onSuspend = func() {
			if bindingSecret(t, r, run) != nil {
				order = append(order, "suspend-with-binding")
			} else {
				order = append(order, "suspend-after-revoke")
			}
		}
		var current sympoziumv1alpha1.AgentRun
		if err := r.Get(ctx, client.ObjectKeyFromObject(run), &current); err != nil {
			t.Fatal(err)
		}
		if err := r.Delete(ctx, &current); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 5; i++ {
			if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)}); err != nil {
				t.Fatal(err)
			}
		}
		if err := r.Get(ctx, client.ObjectKeyFromObject(run), &current); !apierrors.IsNotFound(err) {
			t.Fatalf("the run is still there: %v (finalizers %v)", err, current.Finalizers)
		}
		if len(order) != 1 || order[0] != "suspend-after-revoke" || api.deleted != 1 || len(api.actors) != 0 {
			t.Errorf("order %v, %d deleted, %d actors left", order, api.deleted, len(api.actors))
		}
	})
}

// 5: the clear-text hop is reported and refused until acknowledged.
func TestSubstrateCapabilitiesReportTheTransport(t *testing.T) {
	ctx := context.Background()
	r := &AgentRunReconciler{}
	state := func() BackendState {
		caps, err := substrateBackend{r: r}.Capabilities(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return caps.State
	}
	if got := state(); got != BackendNotInstalled {
		t.Errorf("unconfigured: %s", got)
	}
	r.Substrate = &SubstrateConfig{TurnEndpoint: SubstrateEndpoint{Hostname: "t", Port: 1}, ModelGateway: SubstrateEndpoint{Hostname: "g", Port: 2}}
	if got := state(); got != BackendTransportInvalid {
		t.Errorf("clear-text hop, not acknowledged: %s, want %s", got, BackendTransportInvalid)
	}
	r.Substrate.AllowPlaintextHop = true
	if got := state(); got == BackendTransportInvalid || got == BackendNotInstalled {
		t.Errorf("acknowledged: %s", got)
	}
	// TLS on both hops needs no acknowledgement; TLS on one is not enough.
	r.Substrate.AllowPlaintextHop = false
	r.Substrate.ModelGateway.TLS = true
	if got := state(); got != BackendTransportInvalid {
		t.Errorf("TLS to the gateway only: %s", got)
	}
	r.Substrate.TurnEndpoint.TLS = true
	if got := state(); got == BackendTransportInvalid {
		t.Errorf("TLS on both hops: %s", got)
	}
}

// 4: the turn carries a placeholder where a key would go, and nothing that
// came from the Agent's key Secret.
func TestSubstrateTurnNeverCarriesTheKey(t *testing.T) {
	ctx := context.Background()
	r, run, _ := substrateUnit(t, nil)
	stored := reconcileUntil(t, r, run, isRunning)
	turn, ok, err := r.SubstrateTurns().Claim(ctx, substrate.ActorRef{Atespace: run.Namespace, Name: run.Name, UID: stored.Status.Substrate.ActorUID})
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	raw, _ := json.Marshal(turn)
	if strings.Contains(string(raw), "sk-the-agents-real-key") {
		t.Fatalf("the turn carries the Agent's key: %s", raw)
	}
	if turn.Env["OPENAI_API_KEY"] != substrateActorCredential {
		t.Errorf("OPENAI_API_KEY = %q, want the placeholder", turn.Env["OPENAI_API_KEY"])
	}
	// The binding is what every gateway-mediated run has, plus the actor,
	// and holds no token and no key.
	binding := bindingSecret(t, r, run)
	if binding == nil || len(binding.Data) != 0 {
		t.Fatalf("binding = %+v, want a Secret with no data", binding)
	}
	for annotation, want := range map[string]string{
		harnessgateway.AgentUIDAnnotation:     "agent-uid",
		harnessgateway.KeySecretUIDAnnotation: "key-uid",
		harnessgateway.OriginAnnotation:       "https://api.openai.com",
		harnessgateway.ActorAnnotation:        run.Namespace + "/" + run.Name,
	} {
		if got := binding.Annotations[annotation]; got != want {
			t.Errorf("binding %s = %q, want %q", annotation, got, want)
		}
	}
	var key corev1.Secret
	if err := r.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: "model-key"}, &key); err != nil {
		t.Fatal(err)
	}
	if !modelkey.PinnedTo(&key, "https://api.openai.com") {
		t.Errorf("the key is pinned to %q, want the operator profile's upstream", modelkey.PinnedOrigin(&key))
	}
}
