//go:build system

package system_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/controller"
	"github.com/sympozium-ai/sympozium/internal/harnessgateway"
	"github.com/sympozium-ai/sympozium/internal/modelkey"
	"github.com/sympozium-ai/sympozium/internal/substrate"
)

// The Substrate backend against a real API server and the real controller,
// with Substrate's Control API replaced by fakeSubstrate. The test plays the
// actor: it claims the run's turn and posts a result the way the runner in
// the sandbox does, and calls the real model gateway as that actor.

var (
	agentRuns    *controller.AgentRunReconciler
	substrateAPI = &fakeSubstrate{actors: map[string]*fakeActor{}, stats: map[string]*actorStats{}, fail: map[string]int{}}
	// providerStub records the credential the gateway sent upstream.
	providerSeen = make(chan string, 16)
	providerStub = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerSeen <- r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
)

func substrateConfig() *controller.SubstrateConfig {
	return &controller.SubstrateConfig{
		API:          substrateAPI,
		Image:        "registry.example/agent-runner@sha256:" + strings.Repeat("b", 64),
		TurnEndpoint: controller.SubstrateEndpoint{Hostname: "controller.test.svc", Port: 8090},
		ModelGateway: controller.SubstrateEndpoint{Hostname: "gateway.test.svc", Port: 8091},
		// Both endpoints are clear text; a controller without this refuses
		// every run (see the unit tests for that direction).
		AllowPlaintextHop: true,
		Audience:          "sympozium",
	}
}

type fakeActor struct {
	uid    string
	state  substrate.State
	egress []substrate.EgressTarget
}

// actorStats counts, per actor name, what the controller asked for and what
// came into existence. They outlive the actor.
type actorStats struct {
	created     int // actors created under this name
	createCalls int
	egressSets  int
	resumes     int
	suspends    int
	deletes     int
	// bindingAtSuspend is whether the run's key binding still existed when
	// the actor was suspended.
	bindingAtSuspend []bool
	order            []string
}

type fakeSubstrate struct {
	mu     sync.Mutex
	actors map[string]*fakeActor
	stats  map[string]*actorStats
	// fail is how many more times a "<verb> <atespace>/<name>" call fails.
	fail map[string]int
	seq  int
}

func (f *fakeSubstrate) stat(key string) *actorStats {
	s, ok := f.stats[key]
	if !ok {
		s = &actorStats{}
		f.stats[key] = s
	}
	return s
}

func (f *fakeSubstrate) failing(verb, key string) error {
	if f.fail[verb+" "+key] > 0 {
		f.fail[verb+" "+key]--
		return status.Error(codes.Unavailable, verb+": control API unavailable")
	}
	return nil
}

// failNext makes the next n calls of verb for the actor fail.
func (f *fakeSubstrate) failNext(verb, key string, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail[verb+" "+key] = n
}

func (f *fakeSubstrate) EnsureAtespace(context.Context, string) error { return nil }

func (f *fakeSubstrate) EnsureTemplate(context.Context, substrate.Template) (substrate.TemplateStatus, error) {
	return substrate.TemplateStatus{GoldenReady: true}, nil
}

func (f *fakeSubstrate) CreateActor(_ context.Context, atespace, name, _ string) (substrate.Actor, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := atespace + "/" + name
	f.stat(key).createCalls++
	if err := f.failing("CreateActor", key); err != nil {
		return substrate.Actor{}, false, err
	}
	a, exists := f.actors[key]
	if !exists {
		f.seq++
		a = &fakeActor{uid: fmt.Sprintf("uid-%d", f.seq), state: substrate.StateSuspended}
		f.actors[key] = a
		f.stat(key).created++
		f.stat(key).order = append(f.stat(key).order, "create")
	}
	return substrate.Actor{Atespace: atespace, Name: name, UID: a.uid, State: a.state}, !exists, nil
}

func (f *fakeSubstrate) get(atespace, name string) (*fakeActor, substrate.Actor, error) {
	a, ok := f.actors[atespace+"/"+name]
	if !ok {
		return nil, substrate.Actor{}, status.Error(codes.NotFound, "no such actor")
	}
	out := substrate.Actor{Atespace: atespace, Name: name, UID: a.uid, State: a.state}
	if a.state == substrate.StateRunning {
		out.Worker = "worker-1"
	}
	return a, out, nil
}

func (f *fakeSubstrate) GetActor(_ context.Context, atespace, name string) (substrate.Actor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, actor, err := f.get(atespace, name)
	return actor, err
}

func (f *fakeSubstrate) ResumeActor(_ context.Context, atespace, name string) (substrate.Actor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := atespace + "/" + name
	if err := f.failing("ResumeActor", key); err != nil {
		return substrate.Actor{}, err
	}
	a, _, err := f.get(atespace, name)
	if err != nil {
		return substrate.Actor{}, err
	}
	a.state = substrate.StateRunning
	f.stat(key).resumes++
	f.stat(key).order = append(f.stat(key).order, "resume")
	_, actor, _ := f.get(atespace, name)
	return actor, nil
}

func (f *fakeSubstrate) SuspendActor(ctx context.Context, atespace, name string, _ bool) (substrate.Actor, error) {
	// Outside the lock: what the API server holds at the moment the turn is
	// stopped.
	var binding corev1.Secret
	err := k8sClient.Get(ctx, client.ObjectKey{Namespace: atespace, Name: harnessgateway.RunTokenSecretName(name)}, &binding)
	f.mu.Lock()
	defer f.mu.Unlock()
	key := atespace + "/" + name
	a, _, getErr := f.get(atespace, name)
	if getErr != nil {
		return substrate.Actor{}, getErr
	}
	a.state = substrate.StateSuspended
	s := f.stat(key)
	s.suspends++
	s.bindingAtSuspend = append(s.bindingAtSuspend, !apierrors.IsNotFound(err))
	s.order = append(s.order, "suspend")
	_, actor, _ := f.get(atespace, name)
	return actor, nil
}

func (f *fakeSubstrate) DeleteActor(_ context.Context, atespace, name string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := atespace + "/" + name
	if _, ok := f.actors[key]; ok {
		delete(f.actors, key)
		f.stat(key).deletes++
		f.stat(key).order = append(f.stat(key).order, "delete")
	}
	return true, nil
}

func (f *fakeSubstrate) SetEgress(_ context.Context, atespace, name string, targets []substrate.EgressTarget) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := atespace + "/" + name
	if err := f.failing("SetEgress", key); err != nil {
		return err
	}
	a, _, err := f.get(atespace, name)
	if err != nil {
		return err
	}
	a.egress = targets
	f.stat(key).egressSets++
	return nil
}

func (f *fakeSubstrate) actor(key string) (fakeActor, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.actors[key]
	if !ok {
		return fakeActor{}, false
	}
	return *a, true
}

func (f *fakeSubstrate) statsFor(key string) actorStats {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := *f.stat(key)
	s.bindingAtSuspend = append([]bool(nil), s.bindingAtSuspend...)
	s.order = append([]string(nil), s.order...)
	return s
}

func (f *fakeSubstrate) wasDeleted(key string) bool { return f.statsFor(key).deletes > 0 }

// actorJWTs stands in for Substrate's issuer at the model gateway: each
// token is an actor, with the UID its JWT carries (or none).
type actorJWTs map[string]substrate.ActorRef

func (a actorJWTs) VerifyActor(_ context.Context, token string) (string, string, error) {
	if actor, ok := a[token]; ok {
		return actor.String(), actor.UID, nil
	}
	return "", "", errors.New("not an actor JWT")
}

func substrateAgent(ns, name string) *sympoziumv1alpha1.Agent {
	return &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: sympoziumv1alpha1.AgentSpec{
			AuthRefs: []sympoziumv1alpha1.SecretRef{{Provider: "openai", Secret: "sub-model-key"}},
			Agents:   sympoziumv1alpha1.AgentsSpec{Default: sympoziumv1alpha1.AgentConfig{Model: "m"}},
		},
	}
}

func substrateFixtures(t *testing.T, ns string, agent *sympoziumv1alpha1.Agent, extra ...client.Object) {
	t.Helper()
	objects := append([]client.Object{
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "sub-model-key", Namespace: ns},
			Data:       map[string][]byte{"OPENAI_API_KEY": []byte("sk-the-agents-real-key")},
		},
		agent,
	}, extra...)
	for _, obj := range objects {
		if err := k8sClient.Create(testCtx, obj); err != nil {
			t.Fatalf("create %T: %v", obj, err)
		}
	}
}

func substrateRun(ns, name, agent string) *sympoziumv1alpha1.AgentRun {
	var task sympoziumv1alpha1.TaskSpec
	_ = json.Unmarshal([]byte(`"say hello"`), &task)
	return &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: sympoziumv1alpha1.AgentRunSpec{
			AgentRef: agent, AgentID: agent, Task: &task, Backend: "substrate",
			Model: sympoziumv1alpha1.ModelSpec{Provider: "openai", Model: "m", AuthSecretRef: "sub-model-key"},
		},
	}
}

func getRun(t *testing.T, run *sympoziumv1alpha1.AgentRun) sympoziumv1alpha1.AgentRun {
	t.Helper()
	var got sympoziumv1alpha1.AgentRun
	if err := k8sClient.Get(testCtx, client.ObjectKeyFromObject(run), &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func waitPhase(t *testing.T, run *sympoziumv1alpha1.AgentRun, want sympoziumv1alpha1.AgentRunPhase) sympoziumv1alpha1.AgentRun {
	t.Helper()
	var got sympoziumv1alpha1.AgentRun
	pollUntil(t, 30*time.Second, 100*time.Millisecond, func() bool {
		got = getRun(t, run)
		if got.Status.Phase != want && got.Status.Phase.IsTerminal() {
			t.Fatalf("run ended %s (%s), want %s", got.Status.Phase, got.Status.Error, want)
		}
		return got.Status.Phase == want
	})
	return got
}

// seamResult is the run as the ExecutionBackend seam reports it.
func seamResult(t *testing.T, run *sympoziumv1alpha1.AgentRun) (controller.ExecutionBackendName, *controller.ExecutionResult) {
	t.Helper()
	name, res, err := agentRuns.ExecutionResult(testCtx, run)
	if err != nil {
		t.Fatal(err)
	}
	return name, res
}

func bindingExists(t *testing.T, ns, run string) bool {
	t.Helper()
	var binding corev1.Secret
	err := k8sClient.Get(testCtx, client.ObjectKey{Namespace: ns, Name: harnessgateway.RunTokenSecretName(run)}, &binding)
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatal(err)
	}
	return err == nil
}

// actorGateway is the real model gateway on its actor listener, with tokens
// standing in for actor JWTs.
func actorGateway(t *testing.T, jwts actorJWTs) func(ns, run, jwt string) int {
	t.Helper()
	gateway := httptest.NewServer((&harnessgateway.Gateway{
		Reader: k8sClient, Profiles: harnessGatewayConfig(k8sClient).Profiles, Actors: jwts,
	}).ActorHandler())
	t.Cleanup(gateway.Close)
	return func(ns, run, jwt string) int {
		req, _ := http.NewRequest(http.MethodPost, gateway.URL+"/runs/"+ns+"/"+run+"/v1/chat/completions", strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer "+jwt)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
}

func noProviderCall(t *testing.T, what string) {
	t.Helper()
	select {
	case seen := <-providerSeen:
		t.Fatalf("%s reached the provider with %q", what, seen)
	default:
	}
}

func TestSubstrateRunExecutesAsAnActorWithoutTheKey(t *testing.T) {
	ns := createTestNamespace(t)
	substrateFixtures(t, ns, substrateAgent(ns, "sub-agent"))
	run := substrateRun(ns, "sub-run", "sub-agent")
	if err := k8sClient.Create(testCtx, run); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(testCtx, run) })

	running := waitPhase(t, run, sympoziumv1alpha1.AgentRunPhaseRunning)
	st := running.Status.Substrate
	actorKey := ns + "/sub-run"
	if st == nil || st.Atespace != ns || st.Actor != "sub-run" || st.ActorUID == "" {
		t.Fatalf("status.substrate = %+v", st)
	}
	if running.Status.JobName != "" {
		t.Errorf("a Job %q was created for a substrate run", running.Status.JobName)
	}
	if name, res := seamResult(t, &running); name != controller.ExecutionBackendSubstrate || res != nil {
		t.Errorf("a Running run: backend %s result %+v", name, res)
	}
	pollUntil(t, 10*time.Second, 50*time.Millisecond, func() bool { return substrateAPI.statsFor(actorKey).resumes > 0 })
	actor, _ := substrateAPI.actor(actorKey)
	if len(actor.egress) != 2 || actor.egress[0].Hostname != "controller.test.svc" || actor.egress[1].Hostname != "gateway.test.svc" {
		t.Errorf("egress = %+v, want exactly the turn endpoint and the model gateway", actor.egress)
	}
	for _, target := range actor.egress {
		if target.ActorJWTAudience != "sympozium" {
			t.Errorf("egress to %s carries no actor JWT", target.Hostname)
		}
	}

	turns := agentRuns.SubstrateTurns()
	self := substrate.ActorRef{Atespace: ns, Name: "sub-run", UID: st.ActorUID}
	var turn substrate.Turn
	pollUntil(t, 10*time.Second, 50*time.Millisecond, func() bool {
		var ok bool
		var err error
		turn, ok, err = turns.Claim(testCtx, self)
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		return ok
	})
	if turn.ID != string(running.UID) || turn.Env["TASK"] != "say hello" {
		t.Errorf("turn = %+v", turn)
	}
	wantURL := "http://gateway.test.svc:8091/runs/" + ns + "/sub-run/v1"
	if turn.Env["MODEL_BASE_URL"] != wantURL {
		t.Errorf("MODEL_BASE_URL = %q, want %q", turn.Env["MODEL_BASE_URL"], wantURL)
	}
	for name, value := range turn.Env {
		if strings.Contains(value, "sk-the-agents-real-key") {
			t.Errorf("turn env %s carries the Agent's key", name)
		}
	}
	if turn.Env["OPENAI_API_KEY"] != "substrate-actor" {
		t.Errorf("OPENAI_API_KEY = %q, want the placeholder", turn.Env["OPENAI_API_KEY"])
	}

	// The key binding is what every gateway-mediated run has (the Agent's
	// UID, the key Secret's UID, the operator profile's origin) plus the
	// actor, and it holds no token and no key. The key is pinned to that
	// origin.
	var agent sympoziumv1alpha1.Agent
	var key, binding corev1.Secret
	for obj, name := range map[client.Object]string{&agent: "sub-agent", &key: "sub-model-key", &binding: harnessgateway.RunTokenSecretName("sub-run")} {
		if err := k8sClient.Get(testCtx, client.ObjectKey{Namespace: ns, Name: name}, obj); err != nil {
			t.Fatal(err)
		}
	}
	if len(binding.Data) != 0 {
		t.Errorf("the key binding holds data: %v", binding.Data)
	}
	for annotation, want := range map[string]string{
		harnessgateway.AgentUIDAnnotation:     string(agent.UID),
		harnessgateway.KeySecretUIDAnnotation: string(key.UID),
		harnessgateway.OriginAnnotation:       strings.ToLower(providerStub.URL),
		harnessgateway.ActorAnnotation:        actorKey,
	} {
		if got := binding.Annotations[annotation]; got != want {
			t.Errorf("binding %s = %q, want %q", annotation, got, want)
		}
	}
	if !modelkey.PinnedTo(&key, providerStub.URL) {
		t.Errorf("the key is pinned to %q, want the operator profile's upstream %s", modelkey.PinnedOrigin(&key), providerStub.URL)
	}

	// Nobody else gets the turn: another actor, a new actor of this name,
	// or a token that does not say which actor of this name it is.
	for name, other := range map[string]substrate.ActorRef{
		"another actor":       {Atespace: ns, Name: "someone-else", UID: "x"},
		"another namespace":   {Atespace: "default", Name: "sub-run", UID: st.ActorUID},
		"a replacement actor": {Atespace: ns, Name: "sub-run", UID: "not-" + st.ActorUID},
		"a token with no UID": {Atespace: ns, Name: "sub-run"},
	} {
		if _, ok, _ := turns.Claim(testCtx, other); ok {
			t.Errorf("%s claimed the run's turn", name)
		}
		err := turns.Complete(testCtx, other, substrate.TurnResult{ID: turn.ID, Result: json.RawMessage(`{"status":"success","response":"forged"}`)})
		if !errors.Is(err, substrate.ErrNoSuchTurn) {
			t.Errorf("%s completed the run's turn: %v", name, err)
		}
	}

	// The real model gateway, reading what the controller wrote: the run's
	// actor gets the Agent's key added; any other caller is refused.
	call := actorGateway(t, actorJWTs{
		"jwt-self":      self,
		"jwt-other":     {Atespace: ns, Name: "someone-else", UID: st.ActorUID},
		"jwt-no-uid":    {Atespace: ns, Name: "sub-run"},
		"jwt-recreated": {Atespace: ns, Name: "sub-run", UID: "not-" + st.ActorUID},
	})
	if status := call(ns, "sub-run", "jwt-other"); status != http.StatusUnauthorized {
		t.Errorf("another actor on this run's path: status %d, want 401", status)
	}
	if status := call(ns, "sub-run", "jwt-no-uid"); status != http.StatusUnauthorized {
		t.Errorf("a JWT with no actor UID: status %d, want 401", status)
	}
	if status := call(ns, "sub-run", "jwt-recreated"); status != http.StatusUnauthorized {
		t.Errorf("a recreated actor of the same name: status %d, want 401", status)
	}
	if status := call(ns, "sub-run", "substrate-actor"); status != http.StatusUnauthorized {
		t.Errorf("unreplaced placeholder: status %d, want 401", status)
	}
	noProviderCall(t, "a refused call")
	if status := call(ns, "sub-run", "jwt-self"); status != http.StatusOK {
		t.Fatalf("the run's actor: status %d, want 200", status)
	}
	if seen := <-providerSeen; seen != "Bearer sk-the-agents-real-key" {
		t.Errorf("provider saw %q, want the Agent's key", seen)
	}

	// The gateway holds the key to its pin on every call, as for any
	// mediated run: a key no longer pinned to the profile's origin, or one
	// the Agent no longer grants, stops the actor mid-run.
	pinned := key.Annotations[modelkey.OriginAnnotation]
	setPin := func(value string) {
		t.Helper()
		var current corev1.Secret
		if err := k8sClient.Get(testCtx, client.ObjectKeyFromObject(&key), &current); err != nil {
			t.Fatal(err)
		}
		if value == "" {
			delete(current.Annotations, modelkey.OriginAnnotation)
		} else {
			current.Annotations[modelkey.OriginAnnotation] = value
		}
		if err := k8sClient.Update(testCtx, &current); err != nil {
			t.Fatal(err)
		}
	}
	for name, pin := range map[string]string{"an unpinned key": "", "a key pinned elsewhere": "https://elsewhere.example"} {
		setPin(pin)
		if status := call(ns, "sub-run", "jwt-self"); status != http.StatusForbidden {
			t.Errorf("%s: the run's actor got status %d, want 403", name, status)
		}
		noProviderCall(t, name)
	}
	setPin(pinned)
	if status := call(ns, "sub-run", "jwt-self"); status != http.StatusOK {
		t.Fatalf("with the pin restored: status %d, want 200", status)
	}
	<-providerSeen

	// A result with a wrong turn ID is not this run's.
	if err := turns.Complete(testCtx, self, substrate.TurnResult{ID: "stale", Result: json.RawMessage(`{"status":"success","response":"x"}`)}); !errors.Is(err, substrate.ErrNoSuchTurn) {
		t.Errorf("stale turn ID accepted: %v", err)
	}
	result := `{"status":"success","response":"hello from the actor","metrics":{"durationMs":12,"inputTokens":-5,"outputTokens":3,"toolCalls":0}}`
	if err := turns.Complete(testCtx, self, substrate.TurnResult{ID: turn.ID, Result: json.RawMessage(result), Logs: "line one\nline two\n"}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	// 1c: the result is recorded once. A second post is refused.
	if err := turns.Complete(testCtx, self, substrate.TurnResult{ID: turn.ID, Result: json.RawMessage(`{"status":"success","response":"a second answer"}`), Logs: "second\n"}); err == nil {
		t.Error("a second result post was accepted")
	}

	done := waitPhase(t, run, sympoziumv1alpha1.AgentRunPhaseSucceeded)
	if done.Status.Result != "hello from the actor" {
		t.Errorf("result = %q", done.Status.Result)
	}
	if done.Status.TokenUsage != nil && done.Status.TokenUsage.InputTokens < 0 {
		t.Errorf("a negative agent-reported metric reached status: %+v", done.Status.TokenUsage)
	}
	if done.Status.Substrate.Outcome == nil || done.Status.Substrate.ResumedAt == nil || done.Status.Substrate.Worker == "" {
		t.Errorf("attribution missing from status.substrate: %+v", done.Status.Substrate)
	}
	// 1c: Result() carries the receipt for the terminal run.
	receipt := done.Status.Substrate.ReceiptDigest
	_, res := seamResult(t, &done)
	if res == nil || res.Outcome != controller.ExecutionSucceeded || res.Output != "hello from the actor" ||
		!strings.HasPrefix(receipt, "sha256:") || res.Receipt.Digest != receipt {
		t.Fatalf("Result() = %+v, status receipt %q", res, receipt)
	}
	if err := turns.Complete(testCtx, self, substrate.TurnResult{ID: turn.ID, Result: json.RawMessage(`{"status":"success","response":"late"}`)}); err == nil {
		t.Error("a result post after completion was accepted")
	}

	// Release is confirmed: the actor is gone, status says so, the key
	// binding is gone, and the gateway now refuses the same actor.
	pollUntil(t, 30*time.Second, 100*time.Millisecond, func() bool {
		got := getRun(t, run)
		return substrateAPI.wasDeleted(actorKey) && got.Status.Substrate.Released && len(got.Finalizers) == 0
	})
	pollUntil(t, 30*time.Second, 100*time.Millisecond, func() bool { return !bindingExists(t, ns, "sub-run") })
	if status := call(ns, "sub-run", "jwt-self"); status != http.StatusUnauthorized {
		t.Errorf("after the run ended its actor got status %d, want 401", status)
	}
	noProviderCall(t, "a finished run's actor")
	if _, ok, _ := turns.Claim(testCtx, self); ok {
		t.Error("a finished run still hands out its turn")
	}
	var logs corev1.ConfigMap
	if err := k8sClient.Get(testCtx, client.ObjectKey{Namespace: ns, Name: "sympozium-run-logs-sub-run"}, &logs); err != nil || !strings.Contains(logs.Data["agent.log"], "line two") || strings.Contains(logs.Data["agent.log"], "second") {
		t.Errorf("logs beside the run = %q (err %v), want the first post's", logs.Data["agent.log"], err)
	}

	// 1c: the receipt is write-once at the API too. Nothing with status
	// access can rewrite it, drop it, or move the run to another actor.
	final := getRun(t, run)
	if final.Status.Substrate.ReceiptDigest != receipt || final.Status.Substrate.Outcome.Result != result {
		t.Fatalf("the outcome or receipt changed after completion: %+v", final.Status.Substrate)
	}
	for name, tamper := range map[string]func(*sympoziumv1alpha1.SubstrateStatus){
		"rewrite the receipt": func(s *sympoziumv1alpha1.SubstrateStatus) { s.ReceiptDigest = "sha256:" + strings.Repeat("0", 64) },
		"drop the receipt":    func(s *sympoziumv1alpha1.SubstrateStatus) { s.ReceiptDigest = "" },
		"move to another actor": func(s *sympoziumv1alpha1.SubstrateStatus) {
			s.Actor = "someone-else"
		},
	} {
		tampered := getRun(t, run)
		tamper(tampered.Status.Substrate)
		if err := k8sClient.Status().Update(testCtx, &tampered); !apierrors.IsInvalid(err) {
			t.Errorf("%s: the API answered %v, want Invalid", name, err)
		}
	}
	// Nor can the binding be dropped whole, which would take the receipt
	// with it.
	unbound := getRun(t, run)
	unbound.Status.Substrate = nil
	if err := k8sClient.Status().Update(testCtx, &unbound); !apierrors.IsInvalid(err) {
		t.Errorf("drop status.substrate: the API answered %v, want Invalid", err)
	}
	if stats := substrateAPI.statsFor(actorKey); stats.created != 1 || stats.deletes != 1 {
		t.Errorf("actor lifecycle: %+v", stats)
	}
}

// 1a: a refused run ends as Refused with a typed code through the seam, and
// creates nothing. Shared admission refusals keep their MODEL_KEY_* codes; a
// policy denial stays a plain failure, as on the Job backend.
func TestSubstrateRunRefusedBeforeAnyActorExists(t *testing.T) {
	ns := createTestNamespace(t)
	withSkills := substrateAgent(ns, "sub-skilled")
	withSkills.Spec.Skills = []sympoziumv1alpha1.SkillRef{{SkillPackRef: "k8s-ops"}}
	// Its own key: shared admission claims a key for the Agent that grants
	// it before the backend is asked, and one key serves one Agent.
	withSkills.Spec.AuthRefs = []sympoziumv1alpha1.SecretRef{{Provider: "openai", Secret: "sub-skilled-key"}}
	ungranted := substrateAgent(ns, "sub-ungranted")
	ungranted.Spec.AuthRefs = nil
	denied := substrateAgent(ns, "sub-denied")
	denied.Spec.PolicyRef = "sub-no-runs"
	pinnedElsewhere := substrateAgent(ns, "sub-pinned")
	pinnedElsewhere.Spec.AuthRefs = []sympoziumv1alpha1.SecretRef{{Provider: "openai", Secret: "sub-pinned-key"}}
	substrateFixtures(t, ns, substrateAgent(ns, "sub-agent"), withSkills, ungranted, denied, pinnedElsewhere,
		&sympoziumv1alpha1.SympoziumPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "sub-no-runs", Namespace: ns},
			Spec:       sympoziumv1alpha1.SympoziumPolicySpec{SubagentPolicy: &sympoziumv1alpha1.SubagentPolicySpec{MaxDepth: 1, MaxConcurrent: 5}},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "sub-pinned-key", Namespace: ns, Annotations: map[string]string{modelkey.OriginAnnotation: "https://elsewhere.example"}},
			Data:       map[string][]byte{"OPENAI_API_KEY": []byte("sk-pinned-elsewhere")},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "sub-skilled-key", Namespace: ns},
			Data:       map[string][]byte{"OPENAI_API_KEY": []byte("sk-skilled")},
		},
	)
	deep := func(run *sympoziumv1alpha1.AgentRun) {
		// Deeper than the denying policy allows.
		run.Spec.Parent = &sympoziumv1alpha1.ParentRunRef{RunName: "parent", SessionKey: "s", SpawnDepth: 2}
	}
	for _, tc := range []struct {
		run, agent string
		mutate     func(*sympoziumv1alpha1.AgentRun)
		outcome    controller.ExecutionPhase
		code       controller.RefusalCode
		errorHas   string
	}{
		{run: "sub-ungranted-run", agent: "sub-ungranted", outcome: controller.ExecutionRefused, code: controller.RefusalModelKeyNotGranted},
		{run: "sub-denied-run", agent: "sub-denied", mutate: deep, outcome: controller.ExecutionFailed, errorHas: "policy validation failed"},
		{run: "sub-skilled-run", agent: "sub-skilled", mutate: func(run *sympoziumv1alpha1.AgentRun) {
			run.Spec.Model.AuthSecretRef = "sub-skilled-key"
		}, outcome: controller.ExecutionRefused, code: controller.RefusalSubstrateUnsupportedRun},
		{run: "sub-env-run", agent: "sub-agent", mutate: func(run *sympoziumv1alpha1.AgentRun) {
			run.Spec.Env = map[string]string{"FOO": "bar"}
		}, outcome: controller.ExecutionRefused, code: controller.RefusalSubstrateUnsupportedRun},
		{run: "sub-hooks-run", agent: "sub-agent", mutate: func(run *sympoziumv1alpha1.AgentRun) {
			run.Spec.Lifecycle = &sympoziumv1alpha1.LifecycleHooks{PreRun: []sympoziumv1alpha1.LifecycleHookContainer{{Name: "pre", Image: "busybox"}}}
		}, outcome: controller.ExecutionRefused, code: controller.RefusalSubstrateUnsupportedRun},
		{run: "sub-modelref-run", agent: "sub-agent", mutate: func(run *sympoziumv1alpha1.AgentRun) {
			run.Spec.Model.ModelRef, run.Spec.Model.AuthSecretRef = "local", ""
		}, outcome: controller.ExecutionRefused, code: controller.RefusalSubstrateUnsupportedRun},
		{run: "sub-noprofile-run", agent: "sub-agent", mutate: func(run *sympoziumv1alpha1.AgentRun) {
			run.Spec.Model.Provider = "mistral"
		}, outcome: controller.ExecutionRefused, code: controller.RefusalSubstrateNoModelRoute},
		// 4: the key answers to the operator profile's origin and to its pin.
		{run: "sub-elsewhere-run", agent: "sub-agent", mutate: func(run *sympoziumv1alpha1.AgentRun) {
			run.Spec.Model.BaseURL = "https://exfil.example/v1"
		}, outcome: controller.ExecutionRefused, code: controller.RefusalModelKeyOriginMismatch},
		{run: "sub-pinned-run", agent: "sub-pinned", mutate: func(run *sympoziumv1alpha1.AgentRun) {
			run.Spec.Model.AuthSecretRef = "sub-pinned-key"
		}, outcome: controller.ExecutionRefused, code: controller.RefusalModelKeyOriginMismatch},
	} {
		t.Run(tc.run, func(t *testing.T) {
			run := substrateRun(ns, tc.run, tc.agent)
			if tc.mutate != nil {
				tc.mutate(run)
			}
			if err := k8sClient.Create(testCtx, run); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = k8sClient.Delete(testCtx, run) })
			failed := waitPhase(t, run, sympoziumv1alpha1.AgentRunPhaseFailed)
			name, res := seamResult(t, &failed)
			if name != controller.ExecutionBackendSubstrate || res == nil || res.Outcome != tc.outcome {
				t.Fatalf("backend %s Result() = %+v (status.error %q), want outcome %s", name, res, failed.Status.Error, tc.outcome)
			}
			if tc.code != "" && res.PublicReason != string(tc.code) {
				t.Errorf("PublicReason = %q (status.error %q), want %s", res.PublicReason, failed.Status.Error, tc.code)
			}
			if tc.outcome == controller.ExecutionRefused && res.PublicReason == "" {
				t.Errorf("a refusal without a code: %q", failed.Status.Error)
			}
			if !strings.Contains(failed.Status.Error, tc.errorHas) {
				t.Errorf("error = %q, want it to mention %q", failed.Status.Error, tc.errorHas)
			}
			// Nothing was created: no binding to an actor, no Control API
			// call for it, no key binding, no logs, no Job.
			if failed.Status.Substrate != nil {
				t.Errorf("a refused run was bound to an actor: %+v", failed.Status.Substrate)
			}
			if stats := substrateAPI.statsFor(ns + "/" + tc.run); stats.createCalls != 0 || stats.created != 0 || stats.egressSets != 0 || stats.resumes != 0 {
				t.Errorf("the Control API was called for a refused run: %+v", stats)
			}
			if bindingExists(t, ns, tc.run) {
				t.Error("a refused run has a key binding")
			}
			var logs corev1.ConfigMap
			if err := k8sClient.Get(testCtx, client.ObjectKey{Namespace: ns, Name: "sympozium-run-logs-" + tc.run}, &logs); !apierrors.IsNotFound(err) {
				t.Errorf("a refused run has a logs ConfigMap: %v", err)
			}
			var job batchv1.Job
			if err := k8sClient.Get(testCtx, client.ObjectKey{Namespace: ns, Name: tc.run}, &job); !apierrors.IsNotFound(err) || failed.Status.JobName != "" {
				t.Errorf("a refused substrate run fell back to a Job (%v, status.jobName %q)", err, failed.Status.JobName)
			}
			t.Logf("%s %s: %s", res.Outcome, res.PublicReason, failed.Status.Error)
		})
	}
	// The key a refused run named for another origin keeps its pin.
	var key corev1.Secret
	if err := k8sClient.Get(testCtx, client.ObjectKey{Namespace: ns, Name: "sub-pinned-key"}, &key); err != nil || modelkey.PinnedOrigin(&key) != "https://elsewhere.example" {
		t.Errorf("the pinned key's origin is now %q (err %v)", modelkey.PinnedOrigin(&key), err)
	}
}

// 2: a controller configured for Substrate leaves every run that does not
// ask for it on the backend it had.
func TestSubstrateControllerLeavesOtherRunsOnTheJobBackend(t *testing.T) {
	ns := createTestNamespace(t)
	substrateFixtures(t, ns, substrateAgent(ns, "sub-agent"))
	for _, backend := range []string{"", "job"} {
		name := "plain-run-" + backend + "x"
		run := substrateRun(ns, name, "sub-agent")
		run.Spec.Backend = backend
		if err := k8sClient.Create(testCtx, run); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = k8sClient.Delete(testCtx, run) })
		var got sympoziumv1alpha1.AgentRun
		pollUntil(t, 30*time.Second, 100*time.Millisecond, func() bool {
			got = getRun(t, run)
			if got.Status.Phase.IsTerminal() {
				t.Fatalf("the plain run ended %s: %s", got.Status.Phase, got.Status.Error)
			}
			return got.Status.JobName != ""
		})
		var job batchv1.Job
		if err := k8sClient.Get(testCtx, client.ObjectKey{Namespace: ns, Name: got.Status.JobName}, &job); err != nil {
			t.Errorf("backend %q: no Job: %v", backend, err)
		}
		if backendName, _ := seamResult(t, &got); backendName != controller.ExecutionBackendJob {
			t.Errorf("backend %q: owned by %s", backend, backendName)
		}
		if got.Status.Substrate != nil {
			t.Errorf("backend %q: bound to an actor: %+v", backend, got.Status.Substrate)
		}
		if stats := substrateAPI.statsFor(ns + "/" + name); stats.createCalls != 0 || stats.created != 0 {
			t.Errorf("backend %q: the Control API was called: %+v", backend, stats)
		}
		// Unmediated, so no key binding to an actor either.
		var binding corev1.Secret
		err := k8sClient.Get(testCtx, client.ObjectKey{Namespace: ns, Name: harnessgateway.RunTokenSecretName(name)}, &binding)
		if err == nil && binding.Annotations[harnessgateway.ActorAnnotation] != "" {
			t.Errorf("backend %q: a key binding names an actor", backend)
		}
	}
}

// 1b: Start is idempotent through the real controller. The Control API fails
// between steps, the reconcile is replayed (by the manager and, directly, by
// this test, as a second replica or a restarted controller would), and there
// is still one actor, one egress policy on it and one key binding.
func TestSubstrateStartIsIdempotentAcrossReplays(t *testing.T) {
	ns := createTestNamespace(t)
	substrateFixtures(t, ns, substrateAgent(ns, "sub-agent"))
	actorKey := ns + "/sub-replayed"
	substrateAPI.failNext("CreateActor", actorKey, 1)
	substrateAPI.failNext("SetEgress", actorKey, 2)
	substrateAPI.failNext("ResumeActor", actorKey, 1)

	run := substrateRun(ns, "sub-replayed", "sub-agent")
	if err := k8sClient.Create(testCtx, run); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(testCtx, run) })

	// While it is being started, replay the reconcile from outside the
	// manager's queue, and watch that the binding is never replaced and that
	// "attempted" is on the run before the Control API hears of it.
	var bindingUID types.UID
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)}
	var running sympoziumv1alpha1.AgentRun
	pollUntil(t, 60*time.Second, 50*time.Millisecond, func() bool {
		_, _ = agentRuns.Reconcile(testCtx, req)
		running = getRun(t, run)
		if running.Status.Phase.IsTerminal() {
			t.Fatalf("run ended %s: %s", running.Status.Phase, running.Status.Error)
		}
		stats := substrateAPI.statsFor(actorKey)
		if stats.createCalls > 0 && getRun(t, run).Status.Substrate == nil {
			t.Fatal("the Control API was called before status.substrate was persisted")
		}
		var binding corev1.Secret
		if err := k8sClient.Get(testCtx, client.ObjectKey{Namespace: ns, Name: harnessgateway.RunTokenSecretName("sub-replayed")}, &binding); err == nil {
			if bindingUID == "" {
				bindingUID = binding.UID
			} else if binding.UID != bindingUID {
				t.Fatalf("the key binding was replaced: %s then %s", bindingUID, binding.UID)
			}
		}
		return running.Status.Phase == sympoziumv1alpha1.AgentRunPhaseRunning && stats.resumes > 0
	})
	for i := 0; i < 5; i++ {
		_, _ = agentRuns.Reconcile(testCtx, req)
	}

	stats := substrateAPI.statsFor(actorKey)
	if stats.created != 1 {
		t.Errorf("%d actors were created for one run (%d CreateActor calls)", stats.created, stats.createCalls)
	}
	if stats.createCalls < 2 {
		t.Errorf("CreateActor was called %d times; the test meant to replay it", stats.createCalls)
	}
	if stats.resumes != 1 {
		t.Errorf("the actor was resumed %d times, want once", stats.resumes)
	}
	actor, exists := substrateAPI.actor(actorKey)
	after := getRun(t, run)
	if !exists || after.Status.Substrate == nil || after.Status.Substrate.ActorUID != actor.uid || len(actor.egress) != 2 {
		t.Errorf("status.substrate %+v, actor %+v", after.Status.Substrate, actor)
	}
	var bindings corev1.SecretList
	if err := k8sClient.List(testCtx, &bindings, client.InNamespace(ns), client.MatchingLabels{"sympozium.ai/component": "run-model-token"}); err != nil {
		t.Fatal(err)
	}
	if len(bindings.Items) != 1 || bindings.Items[0].UID != bindingUID {
		t.Errorf("want the one key binding first created (%s); got %d", bindingUID, len(bindings.Items))
	}
	t.Logf("control API for %s: %+v", actorKey, stats)
}

// 1d and 4: a run stopped early is cancelled before it is released. The key
// binding is revoked first, so the actor loses its model; then the turn is
// stopped; release (delete, confirmed) follows on its own.
func TestSubstrateRunTimedOutIsCancelledThenReleased(t *testing.T) {
	ns := createTestNamespace(t)
	substrateFixtures(t, ns, substrateAgent(ns, "sub-agent"))
	actorKey := ns + "/sub-slow"
	run := substrateRun(ns, "sub-slow", "sub-agent")
	run.Spec.Timeout = &metav1.Duration{Duration: 3 * time.Second}
	if err := k8sClient.Create(testCtx, run); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(testCtx, run) })

	running := waitPhase(t, run, sympoziumv1alpha1.AgentRunPhaseRunning)
	call := actorGateway(t, actorJWTs{"jwt-slow": {Atespace: ns, Name: "sub-slow", UID: running.Status.Substrate.ActorUID}})
	if status := call(ns, "sub-slow", "jwt-slow"); status != http.StatusOK {
		t.Fatalf("while Running the actor got status %d, want 200", status)
	}
	<-providerSeen

	// Nobody plays the actor, so the run times out.
	failed := waitPhase(t, run, sympoziumv1alpha1.AgentRunPhaseFailed)
	if !strings.Contains(failed.Status.Error, "timed out") {
		t.Fatalf("error = %q", failed.Status.Error)
	}
	if _, res := seamResult(t, &failed); res == nil || res.Outcome != controller.ExecutionFailed || res.Receipt.Digest != "" {
		t.Errorf("Result() = %+v, want a plain failure with no receipt", res)
	}
	stats := substrateAPI.statsFor(actorKey)
	if stats.suspends != 1 || stats.bindingAtSuspend[0] {
		t.Errorf("cancel: %d suspends, binding still present when the turn was stopped: %v", stats.suspends, stats.bindingAtSuspend)
	}
	if status := call(ns, "sub-slow", "jwt-slow"); status != http.StatusUnauthorized {
		t.Errorf("after cancel the actor got status %d, want 401", status)
	}
	noProviderCall(t, "a cancelled run's actor")
	self := substrate.ActorRef{Atespace: ns, Name: "sub-slow", UID: running.Status.Substrate.ActorUID}
	if _, ok, _ := agentRuns.SubstrateTurns().Claim(testCtx, self); ok {
		t.Error("a cancelled run still hands out its turn")
	}
	if err := agentRuns.SubstrateTurns().Complete(testCtx, self, substrate.TurnResult{ID: string(running.UID), Result: json.RawMessage(`{"status":"success","response":"late"}`)}); err == nil {
		t.Error("a cancelled run accepted a result")
	}

	pollUntil(t, 30*time.Second, 100*time.Millisecond, func() bool {
		got := getRun(t, run)
		return got.Status.Substrate.Released && len(got.Finalizers) == 0
	})
	stats = substrateAPI.statsFor(actorKey)
	if want := "create resume suspend delete"; strings.Join(stats.order, " ") != want {
		t.Errorf("actor lifecycle %v, want %q", stats.order, want)
	}
	if final := getRun(t, run); final.Status.Phase != sympoziumv1alpha1.AgentRunPhaseFailed || final.Status.Substrate.Outcome != nil {
		t.Errorf("after release: phase %s outcome %+v", final.Status.Phase, final.Status.Substrate.Outcome)
	}
}

func TestSubstrateRunDeletedWhileRunningIsCancelledThenReleased(t *testing.T) {
	ns := createTestNamespace(t)
	substrateFixtures(t, ns, substrateAgent(ns, "sub-agent"))
	actorKey := ns + "/sub-cancelled"
	run := substrateRun(ns, "sub-cancelled", "sub-agent")
	if err := k8sClient.Create(testCtx, run); err != nil {
		t.Fatal(err)
	}
	running := waitPhase(t, run, sympoziumv1alpha1.AgentRunPhaseRunning)
	pollUntil(t, 10*time.Second, 50*time.Millisecond, func() bool { return substrateAPI.statsFor(actorKey).resumes > 0 })
	call := actorGateway(t, actorJWTs{"jwt-cancelled": {Atespace: ns, Name: "sub-cancelled", UID: running.Status.Substrate.ActorUID}})
	if err := k8sClient.Delete(testCtx, run); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, 30*time.Second, 100*time.Millisecond, func() bool {
		var got sympoziumv1alpha1.AgentRun
		err := k8sClient.Get(testCtx, client.ObjectKeyFromObject(run), &got)
		return apierrors.IsNotFound(err) && substrateAPI.wasDeleted(actorKey)
	})
	stats := substrateAPI.statsFor(actorKey)
	if stats.suspends != 1 || stats.bindingAtSuspend[0] {
		t.Errorf("cancel: %d suspends, binding present when the turn was stopped: %v", stats.suspends, stats.bindingAtSuspend)
	}
	if want := "create resume suspend delete"; strings.Join(stats.order, " ") != want {
		t.Errorf("actor lifecycle %v, want %q", stats.order, want)
	}
	if status := call(ns, "sub-cancelled", "jwt-cancelled"); status != http.StatusUnauthorized {
		t.Errorf("after the run was deleted its actor got status %d, want 401", status)
	}
	noProviderCall(t, "a deleted run's actor")
}
