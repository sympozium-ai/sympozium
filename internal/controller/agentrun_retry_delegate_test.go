package controller

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/eventbus"
	"github.com/sympozium-ai/sympozium/internal/ipc"
)

// ── fixtures ─────────────────────────────────────────────────────────────────

// delegateParent is a run blocked in the delegate_to_persona tool call,
// tracking one child.
func delegateParent(childName string) *sympoziumv1alpha1.AgentRun {
	return &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "parent-run", Namespace: "default"},
		Spec: sympoziumv1alpha1.AgentRunSpec{
			AgentRef: "lead",
			Task:     sympoziumv1alpha1.NewStringTask("lead the work"),
		},
		Status: sympoziumv1alpha1.AgentRunStatus{
			Phase: sympoziumv1alpha1.AgentRunPhaseAwaitingDelegate,
			Delegates: []sympoziumv1alpha1.DelegateStatus{
				{ChildRunName: childName, TargetPersona: "researcher", Phase: sympoziumv1alpha1.AgentRunPhasePending},
			},
		},
	}
}

// delegatedGatedRun is gatedRun spawned by delegateParent: same gate, plus the
// spec.parent link the Spawner writes.
func delegatedGatedRun(name string, retrySpec *sympoziumv1alpha1.RetrySpec) *sympoziumv1alpha1.AgentRun {
	run := gatedRun(name, retrySpec)
	run.Spec.Parent = &sympoziumv1alpha1.ParentRunRef{
		RunName:    "parent-run",
		SessionKey: "session-1",
		SpawnDepth: 1,
	}
	return run
}

// delegationRetryFixture wires a reconciler and a SpawnRouter to one client, so
// a retry the controller performs is visible to the router that has to deliver
// the successor's result.
func delegationRetryFixture(t *testing.T, objs ...*sympoziumv1alpha1.AgentRun) (*AgentRunReconciler, *SpawnRouter, *recordingEventBus) {
	t.Helper()
	scheme := retryScheme(t)
	builder := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&sympoziumv1alpha1.AgentRun{})
	for _, o := range objs {
		builder = builder.WithObjects(o)
	}
	cl := builder.Build()
	bus := &recordingEventBus{}

	r := &AgentRunReconciler{Client: cl, Scheme: scheme, Log: logr.Discard(), EventBus: bus}
	sr := &SpawnRouter{Client: cl, Log: logr.Discard(), EventBus: bus}
	sr.storePending("child-run", &pendingDelegation{
		RequestID:       "req-1",
		ParentRunID:     "parent-run",
		ParentNamespace: "default",
	})
	return r, sr, bus
}

func getParent(t *testing.T, c client.Client) *sympoziumv1alpha1.AgentRun {
	t.Helper()
	var parent sympoziumv1alpha1.AgentRun
	if err := c.Get(context.Background(), types.NamespacedName{Name: "parent-run", Namespace: "default"}, &parent); err != nil {
		t.Fatalf("get parent: %v", err)
	}
	return &parent
}

// ── the delegated retry round trip ───────────────────────────────────────────

// A delegated child that retries once and then passes. Each step is the one
// the previous step's state reaches: the parent tracks the successor, waits for
// it rather than recording a failure, and receives its result.
func TestDelegatedChildRetriesThenPasses(t *testing.T) {
	ctx := context.Background()
	child := withVerdict(
		delegatedGatedRun("child-run", &sympoziumv1alpha1.RetrySpec{MaxAttempts: 3}),
		`{"action":"retry","reason":"cite your sources"}`)
	r, sr, bus := delegationRetryFixture(t, delegateParent("child-run"), child)

	if _, err := r.resolveGate(ctx, logr.Discard(), child, true, false); err != nil {
		t.Fatalf("resolveGate: %v", err)
	}

	// 1. The parent tracks the attempt that is running, not the retired one.
	parent := getParent(t, r.Client)
	if got := parent.Status.Delegates[0].ChildRunName; got != "child-run-retry-2" {
		t.Fatalf("delegate childRunName = %q, want child-run-retry-2", got)
	}
	if got := parent.Status.Delegates[0].Phase; got != sympoziumv1alpha1.AgentRunPhasePending {
		t.Errorf("delegate phase = %q, want Pending", got)
	}
	if parent.Status.Phase != sympoziumv1alpha1.AgentRunPhaseAwaitingDelegate {
		t.Errorf("parent phase = %q, want AwaitingDelegate", parent.Status.Phase)
	}

	// The successor stays inside the parent's session, or its own delegations
	// and shared memory would start from scratch.
	successor := getRun(t, r, "child-run-retry-2")
	if successor.Spec.Parent == nil || successor.Spec.Parent.RunName != "parent-run" ||
		successor.Spec.Parent.SessionKey != "session-1" || successor.Spec.Parent.SpawnDepth != 1 {
		t.Errorf("successor lost its parent linkage: %+v", successor.Spec.Parent)
	}

	// 2. The recovery path must read the successor, not the retired attempt.
	// Reading Failed there ends the delegation while the successor still runs.
	res, err := r.reconcileAwaitingDelegate(ctx, logr.Discard(), parent)
	if err != nil {
		t.Fatalf("reconcileAwaitingDelegate: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Error("parent should keep waiting on the successor")
	}
	if parent := getParent(t, r.Client); parent.Status.Phase != sympoziumv1alpha1.AgentRunPhaseAwaitingDelegate {
		t.Errorf("parent phase = %q, want AwaitingDelegate — the retired attempt was read as a failed delegation",
			parent.Status.Phase)
	}

	// 3. The successor passes its gate, and its result reaches the parent even
	// though the delegation was registered under the first attempt's name.
	if err := r.updateStatusWithRetry(ctx, successor, func(ar *sympoziumv1alpha1.AgentRun) {
		ar.Status.Phase = sympoziumv1alpha1.AgentRunPhaseSucceeded
		ar.Status.Result = "sources cited"
	}); err != nil {
		t.Fatalf("settle successor: %v", err)
	}
	sr.handleChildCompleted(ctx, &eventbus.Event{
		Metadata: map[string]string{"agentRunID": "child-run-retry-2", "namespace": "default"},
		Data:     json.RawMessage(`{"status":"success","response":"sources cited"}`),
	})

	result, ok := decodeDelegateResult(t, bus)
	if !ok {
		t.Fatal("no delegate result published; the parent's tool call would block to its run deadline")
	}
	if result.RequestID != "req-1" {
		t.Errorf("requestId = %q, want req-1 — the successor must answer the original call", result.RequestID)
	}
	if result.Status != "success" || result.Response != "sources cited" {
		t.Errorf("delegate result = %+v, want the successor's output", result)
	}

	parent = getParent(t, r.Client)
	if parent.Status.Delegates[0].Phase != sympoziumv1alpha1.AgentRunPhaseSucceeded {
		t.Errorf("delegate phase = %q, want Succeeded", parent.Status.Delegates[0].Phase)
	}
	if parent.Status.Phase != sympoziumv1alpha1.AgentRunPhaseRunning {
		t.Errorf("parent phase = %q, want Running — the delegation is done", parent.Status.Phase)
	}
}

// The retired attempt keeps its Failed phase, so anything still pointed at it
// would read a failed delegation. Nothing may be.
func TestDelegatedRetry_RetiredAttemptIsNoLongerTracked(t *testing.T) {
	ctx := context.Background()
	child := withVerdict(
		delegatedGatedRun("child-run", &sympoziumv1alpha1.RetrySpec{MaxAttempts: 3}),
		`{"action":"retry","reason":"thin"}`)
	r, _, _ := delegationRetryFixture(t, delegateParent("child-run"), child)

	if _, err := r.resolveGate(ctx, logr.Discard(), child, true, false); err != nil {
		t.Fatalf("resolveGate: %v", err)
	}

	retired := getRun(t, r, "child-run")
	if retired.Status.Phase != sympoziumv1alpha1.AgentRunPhaseFailed {
		t.Fatalf("retired attempt phase = %q, want Failed", retired.Status.Phase)
	}
	for _, d := range getParent(t, r.Client).Status.Delegates {
		if d.ChildRunName == "child-run" {
			t.Error("parent still points at the retired attempt")
		}
	}
}

// A run that is nobody's delegate, and a delegate its parent does not list
// (a sequential or controller-side spawn), both retry unchanged.
func TestReassignDelegation_NoOpWithoutATrackedDelegation(t *testing.T) {
	ctx := context.Background()

	t.Run("no parent", func(t *testing.T) {
		run := gatedRun("solo-run", nil)
		r, _ := retryReconciler(t, run)
		if err := r.reassignDelegation(ctx, run, "solo-run-retry-2"); err != nil {
			t.Fatalf("reassignDelegation: %v", err)
		}
	})

	t.Run("parent does not track this child", func(t *testing.T) {
		child := delegatedGatedRun("child-run", nil)
		r, _, _ := delegationRetryFixture(t, delegateParent("some-other-child"), child)
		if err := r.reassignDelegation(ctx, child, "child-run-retry-2"); err != nil {
			t.Fatalf("reassignDelegation: %v", err)
		}
		if got := getParent(t, r.Client).Status.Delegates[0].ChildRunName; got != "some-other-child" {
			t.Errorf("delegate childRunName = %q, want some-other-child", got)
		}
	})

	t.Run("parent already pruned", func(t *testing.T) {
		child := delegatedGatedRun("child-run", nil)
		r, _ := retryReconciler(t, child)
		if err := r.reassignDelegation(ctx, child, "child-run-retry-2"); err != nil {
			t.Fatalf("a missing parent is not an error: %v", err)
		}
	})
}

// If the parent cannot be repointed, the successor has nothing waiting on it.
// Running it anyway would burn tokens on a result no one reads, so the verdict
// resolves as a reject instead.
func TestDelegatedRetry_UnwirableSuccessorIsDiscarded(t *testing.T) {
	ctx := context.Background()
	child := withVerdict(
		delegatedGatedRun("child-run", &sympoziumv1alpha1.RetrySpec{MaxAttempts: 3}),
		`{"action":"retry","reason":"thin"}`)

	scheme := retryScheme(t)
	direct := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&sympoziumv1alpha1.AgentRun{}).
		WithObjects(delegateParent("child-run"), child).
		Build()
	blocked := interceptor.NewClient(direct, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if key.Name == "parent-run" {
				return apierrors.NewInternalError(context.DeadlineExceeded)
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})
	r := &AgentRunReconciler{Client: blocked, Scheme: scheme, Log: logr.Discard(), EventBus: &recordingEventBus{}}

	if _, err := r.resolveGate(ctx, logr.Discard(), child, true, false); err != nil {
		t.Fatalf("resolveGate: %v", err)
	}

	var successor sympoziumv1alpha1.AgentRun
	err := direct.Get(ctx, types.NamespacedName{Name: "child-run-retry-2", Namespace: "default"}, &successor)
	if err == nil {
		t.Error("an unwired successor should be discarded, not left running")
	} else if !apierrors.IsNotFound(err) {
		t.Fatalf("get successor: %v", err)
	}

	settled := getRun(t, r, "child-run")
	if settled.Status.GateVerdict != "rejected" {
		t.Errorf("gateVerdict = %q, want rejected — a retry that cannot proceed is not an approval", settled.Status.GateVerdict)
	}
}

// ── router: following the chain ──────────────────────────────────────────────

func TestClaimPending_FollowsTheRetryChain(t *testing.T) {
	ctx := context.Background()
	successor := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "child-run-retry-2",
			Namespace: "default",
			Labels:    map[string]string{retryOfLabel: "child-run"},
		},
	}
	sr := newTestSpawnRouter(t, successor)
	pd := &pendingDelegation{RequestID: "req-1", ParentRunID: "parent-run", ParentNamespace: "default"}
	sr.storePending("child-run", pd)

	got, originKey, ok := sr.claimPending(ctx, "child-run-retry-2", "default")
	if !ok {
		t.Fatal("successor did not resolve to its delegation")
	}
	if got != pd {
		t.Errorf("claimed the wrong entry: %+v", got)
	}
	if originKey != "child-run" {
		t.Errorf("originKey = %q, want child-run — batch state is keyed by it", originKey)
	}
	if _, still := sr.pending.Load("child-run"); still {
		t.Error("the entry should be claimed, so a second event cannot double-publish")
	}
}

// A run nobody delegated settles on every reconcile loop in the cluster. It
// must cost a map miss, not a read per run.
func TestClaimPending_OrdinaryRunCostsNoRead(t *testing.T) {
	var gets int
	scheme := retryScheme(t)
	direct := fake.NewClientBuilder().WithScheme(scheme).Build()
	counting := interceptor.NewClient(direct, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			gets++
			return c.Get(ctx, key, obj, opts...)
		},
	})
	sr := &SpawnRouter{Client: counting, Log: logr.Discard()}

	if _, _, ok := sr.claimPending(context.Background(), "some-plain-run", "default"); ok {
		t.Error("a run with no delegation must not resolve to one")
	}
	if gets != 0 {
		t.Errorf("reads = %d, want 0", gets)
	}
}

// The edge timeout bounds the delegation, not one attempt of it: it has to
// reach the attempt that is actually running.
func TestExpireDelegation_ExpiresTheLiveAttempt(t *testing.T) {
	ctx := context.Background()
	parent := delegateParent("child-run-retry-2")
	parent.Labels = map[string]string{"sympozium.ai/ensemble": "my-pack"}
	retired := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "child-run", Namespace: "default"},
		Status:     sympoziumv1alpha1.AgentRunStatus{Phase: sympoziumv1alpha1.AgentRunPhaseFailed},
	}
	live := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "child-run-retry-2",
			Namespace: "default",
			Labels:    map[string]string{retryOfLabel: "child-run"},
		},
	}

	sr := newTestSpawnRouter(t, edgePack("delegation", "15m"), parent, retired, live)
	bus := &recordingEventBus{}
	sr.EventBus = bus
	sr.storePending("child-run", &pendingDelegation{
		RequestID:       "req-1",
		ParentRunID:     "parent-run",
		ParentNamespace: "default",
	})

	sr.expireDelegation(ctx, "child-run", "researcher", 15*time.Minute)

	res, ok := decodeDelegateResult(t, bus)
	if !ok || res.Status != "error" {
		t.Fatalf("the parent's tool call was not released with an error: %+v", res)
	}

	// The running successor is the one burning tokens.
	var stillLive sympoziumv1alpha1.AgentRun
	if err := sr.Client.Get(ctx, types.NamespacedName{Name: "child-run-retry-2", Namespace: "default"}, &stillLive); err == nil {
		t.Error("the live attempt should be deleted")
	} else if !apierrors.IsNotFound(err) {
		t.Fatalf("get live attempt: %v", err)
	}

	// The delegate entry names the successor, so the expiry must match on it
	// or the parent waits on an entry that never reaches a terminal phase.
	got := getParent(t, sr.Client)
	if got.Status.Delegates[0].Phase != sympoziumv1alpha1.AgentRunPhaseFailed {
		t.Errorf("delegate phase = %q, want Failed", got.Status.Delegates[0].Phase)
	}
}

// A batch slot is registered under the name the child was spawned with; the
// aggregated result has to report the attempt that produced it.
func TestBatchChildDone_ReportsTheAttemptThatSettled(t *testing.T) {
	sr := newTestSpawnRouter(t)
	sr.EventBus = &recordingEventBus{}
	batch := &pendingBatch{
		batchID:      "batch-1",
		parentRunID:  "parent-run",
		namespace:    "default",
		strategy:     "parallel",
		tasks:        []ipc.SubagentTask{{ID: "t1"}},
		results:      []ipc.SubagentChildResult{{ID: "t1", RunName: "sub-child"}},
		childToIndex: map[string]int{"sub-child": 0},
	}
	sr.batches.Store("batch-1", batch)
	sr.childBatch.Store("sub-child", "batch-1")

	sr.handleBatchChildDone(context.Background(), "sub-child", "sub-child-retry-2", "second time lucky", "")

	if batch.results[0].Status != "success" || batch.results[0].Response != "second time lucky" {
		t.Errorf("batch slot = %+v, want the successor's output", batch.results[0])
	}
	if batch.results[0].RunName != "sub-child-retry-2" {
		t.Errorf("runName = %q, want the attempt that settled", batch.results[0].RunName)
	}
}

// ── labels another controller finds the chain by ─────────────────────────────

// A schedule with concurrencyPolicy: Forbid, the stimulus guard and the web
// proxy's dedupe all look for live work by label. A retired attempt reads as
// finished, so the successor has to be findable the same way or the next tick
// fires on top of a running chain.
func TestResolveGate_SuccessorKeepsTheLabelsWorkIsFoundBy(t *testing.T) {
	run := withVerdict(
		gatedRun("nightly-3", &sympoziumv1alpha1.RetrySpec{MaxAttempts: 3}),
		`{"action":"retry","reason":"no sources"}`)
	run.Labels["sympozium.ai/schedule"] = "nightly"
	run.Labels["sympozium.ai/stimulus"] = "true"
	run.Labels["sympozium.ai/request-hash"] = "abcdef0123456789"

	r, _ := retryReconciler(t, run)
	if _, err := r.resolveGate(context.Background(), logr.Discard(), run, true, false); err != nil {
		t.Fatalf("resolveGate: %v", err)
	}

	successor := getRun(t, r, "nightly-3-retry-2")
	for key, want := range map[string]string{
		"sympozium.ai/schedule":     "nightly",
		"sympozium.ai/stimulus":     "true",
		"sympozium.ai/request-hash": "abcdef0123456789",
	} {
		if got := successor.Labels[key]; got != want {
			t.Errorf("successor %s = %q, want %q", key, got, want)
		}
	}
}

// ── RetryChainContains ───────────────────────────────────────────────────────

func TestRetryChainContains(t *testing.T) {
	ctx := context.Background()
	second := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "web-run-retry-2", Namespace: "default"},
		Status:     sympoziumv1alpha1.AgentRunStatus{RetryOf: "web-run"},
	}
	third := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name: "web-run-retry-3", Namespace: "default",
			Labels: map[string]string{retryOfLabel: "web-run-retry-2"},
		},
	}
	r, _ := retryReconciler(t, second, third)

	tests := []struct {
		name      string
		candidate string
		want      bool
	}{
		{"the run itself", "web-run", true},
		{"its successor", "web-run-retry-2", true},
		{"two attempts on, via the label", "web-run-retry-3", true},
		{"a run that is not in the chain", "other-run", false},
		{"a retry of something else", "other-run-retry-2", false},
		{"nothing", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RetryChainContains(ctx, r.Client, "default", "web-run", tt.candidate); got != tt.want {
				t.Errorf("RetryChainContains(%q) = %v, want %v", tt.candidate, got, tt.want)
			}
		})
	}
}

// A duplicate reconcile finds the successor already created and already wired.
// If the repoint then fails, deleting it would kill the run the parent is
// pointed at — so only the pass that created it may clean it up.
func TestDelegatedRetry_DuplicateReconcileDoesNotDeleteAWiredSuccessor(t *testing.T) {
	ctx := context.Background()
	child := withVerdict(
		delegatedGatedRun("child-run", &sympoziumv1alpha1.RetrySpec{MaxAttempts: 3}),
		`{"action":"retry","reason":"thin"}`)
	// The successor an earlier pass created and pointed the parent at.
	successor := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "child-run-retry-2", Namespace: "default"},
		Status:     sympoziumv1alpha1.AgentRunStatus{Phase: sympoziumv1alpha1.AgentRunPhaseRunning},
	}
	parent := delegateParent("child-run-retry-2")

	scheme := retryScheme(t)
	direct := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&sympoziumv1alpha1.AgentRun{}).
		WithObjects(parent, child, successor).
		Build()
	blocked := interceptor.NewClient(direct, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if key.Name == "parent-run" {
				return apierrors.NewInternalError(context.DeadlineExceeded)
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})
	r := &AgentRunReconciler{Client: blocked, Scheme: scheme, Log: logr.Discard(), EventBus: &recordingEventBus{}}

	if _, err := r.resolveGate(ctx, logr.Discard(), child, true, false); err != nil {
		t.Fatalf("resolveGate: %v", err)
	}

	var still sympoziumv1alpha1.AgentRun
	if err := direct.Get(ctx, types.NamespacedName{Name: "child-run-retry-2", Namespace: "default"}, &still); err != nil {
		t.Fatalf("a successor this pass did not create was deleted: %v", err)
	}
	if still.Status.Phase != sympoziumv1alpha1.AgentRunPhaseRunning {
		t.Errorf("successor phase = %q, want Running — it was already in flight", still.Status.Phase)
	}
}

// The retries-exhausted end of a chain: the last attempt settles as a failure
// and has to release the parent's tool call, not leave it blocked.
func TestDelegatedRetry_SuccessorFailureReachesTheParent(t *testing.T) {
	ctx := context.Background()
	parent := delegateParent("child-run-retry-2")
	successor := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "child-run-retry-2", Namespace: "default"},
		Status:     sympoziumv1alpha1.AgentRunStatus{RetryOf: "child-run"},
	}
	retired := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "child-run", Namespace: "default"},
		Status:     sympoziumv1alpha1.AgentRunStatus{Phase: sympoziumv1alpha1.AgentRunPhaseFailed},
	}
	_, sr, bus := delegationRetryFixture(t, parent, retired, successor)

	sr.handleChildFailed(ctx, &eventbus.Event{
		Metadata: map[string]string{"agentRunID": "child-run-retry-2", "namespace": "default"},
		Data:     json.RawMessage(`{"error":"the gate rejected every attempt"}`),
	})

	res, ok := decodeDelegateResult(t, bus)
	if !ok {
		t.Fatal("no delegate result published; the parent's tool call would block to its run deadline")
	}
	if res.RequestID != "req-1" || res.Status != "error" {
		t.Errorf("delegate result = %+v, want an error answering req-1", res)
	}
	if res.Error != "the gate rejected every attempt" {
		t.Errorf("error = %q, want the successor's", res.Error)
	}

	got := getParent(t, sr.Client)
	if got.Status.Delegates[0].Phase != sympoziumv1alpha1.AgentRunPhaseFailed {
		t.Errorf("delegate phase = %q, want Failed", got.Status.Delegates[0].Phase)
	}
	if got.Status.Phase != sympoziumv1alpha1.AgentRunPhaseRunning {
		t.Errorf("parent phase = %q, want Running — the delegation is resolved", got.Status.Phase)
	}
}

// ── walks are bounded ────────────────────────────────────────────────────────

// Lineage comes off the objects, so a chain can be circular (a hand-edited
// label) or missing a link (an attempt pruned by the history limit). Neither
// may hang a walk or resolve to the wrong delegation.
func TestRetryWalks_TerminateOnCyclesAndGaps(t *testing.T) {
	ctx := context.Background()
	// Two attempts naming each other.
	loopA := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "loop-retry-2", Namespace: "default",
			Labels: map[string]string{retryOfLabel: "loop-retry-3"}},
	}
	loopB := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "loop-retry-3", Namespace: "default",
			Labels: map[string]string{retryOfLabel: "loop-retry-2"}},
	}
	// A successor whose predecessor object is gone.
	orphan := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "pruned-retry-2", Namespace: "default",
			Labels: map[string]string{retryOfLabel: "pruned"}},
	}

	t.Run("claimPending", func(t *testing.T) {
		sr := newTestSpawnRouter(t, loopA, loopB, orphan)
		sr.storePending("some-other-run", &pendingDelegation{ParentRunID: "parent-run"})

		if _, _, ok := sr.claimPending(ctx, "loop-retry-2", "default"); ok {
			t.Error("a circular chain resolved to a delegation")
		}
		if _, _, ok := sr.claimPending(ctx, "pruned-retry-2", "default"); ok {
			t.Error("a chain with a pruned link resolved to a delegation")
		}
		if _, _, ok := sr.claimPending(ctx, "never-seen-retry-2", "default"); ok {
			t.Error("a run that is not in the cluster resolved to a delegation")
		}
		if _, still := sr.pending.Load("some-other-run"); !still {
			t.Error("an unrelated delegation was claimed by a failed walk")
		}
	})

	t.Run("RetryChainContains", func(t *testing.T) {
		r, _ := retryReconciler(t, loopA, loopB, orphan)
		if RetryChainContains(ctx, r.Client, "default", "loop", "loop-retry-2") {
			t.Error("a circular chain reported a match it does not have")
		}
		if RetryChainContains(ctx, r.Client, "default", "gone", "pruned-retry-2") {
			t.Error("a chain with a pruned link reported a match")
		}
	})

	t.Run("liveAttempt", func(t *testing.T) {
		attempt2 := &sympoziumv1alpha1.AgentRun{
			ObjectMeta: metav1.ObjectMeta{Name: "chain-retry-2", Namespace: "default",
				Labels: map[string]string{retryOfLabel: "chain"}},
		}
		attempt3 := &sympoziumv1alpha1.AgentRun{
			ObjectMeta: metav1.ObjectMeta{Name: "chain-retry-3", Namespace: "default",
				Labels: map[string]string{retryOfLabel: "chain-retry-2"}},
		}
		sr := newTestSpawnRouter(t, attempt2, attempt3, loopA, loopB)

		if got := sr.liveAttempt(ctx, "default", "chain"); got != "chain-retry-3" {
			t.Errorf("liveAttempt = %q, want the newest attempt chain-retry-3", got)
		}
		if got := sr.liveAttempt(ctx, "default", "solo"); got != "solo" {
			t.Errorf("liveAttempt = %q, want solo — a run with no successor is the live one", got)
		}
		// Bounded rather than hanging; which of the two it stops on is
		// arbitrary, so assert only that it returned.
		if got := sr.liveAttempt(ctx, "default", "loop-retry-2"); got == "" {
			t.Error("a circular chain did not terminate with a name")
		}
	})
}
