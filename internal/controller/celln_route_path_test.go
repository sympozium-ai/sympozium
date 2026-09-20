package controller

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnscoped"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// routePathObjects is one namespace holding the fleet's host-profile
// connection, an Agent's own Secret-backed connection and a credential-free
// one, all on the same wrapper runtime.
func routePathObjects() []client.Object {
	connection := func(name string, mutate func(*api.ModelConnectionSpec)) *api.ModelConnection {
		c := &api.ModelConnection{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: name, UID: types.UID(name + "-uid"), Generation: 1}, Spec: api.ModelConnectionSpec{Provider: "deepseek", Protocol: "openai-chat", Endpoint: "https://api.deepseek.com/chat/completions", Models: []string{"deepseek-chat"}}}
		mutate(&c.Spec)
		return c
	}
	return []client.Object{
		connection("fleet", func(s *api.ModelConnectionSpec) { s.CredentialProfile = "starter" }),
		connection("own", func(s *api.ModelConnectionSpec) { s.SecretRef = "tenant-key"; s.MaxOutputTokens = 4096 }),
		connection("open", func(*api.ModelConnectionSpec) {}),
		&api.AgentRuntime{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "celln-native"}, Spec: api.AgentRuntimeSpec{CellnProfileRef: &api.CellnRuntimeProfileRef{Name: "celln-native-trial", Revision: "v1"}}},
	}
}

func routePathRun(name, connection, lifecycle string) *api.AgentRun {
	run := &api.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "tenant", UID: types.UID(name + "-uid"), Generation: 1, Finalizers: []string{agentRunFinalizer}},
		Spec: api.AgentRunSpec{AgentRef: "agent", Backend: "celln", Task: api.NewStringTask("bounded task"), ExecutionLifecycle: lifecycle, Cleanup: "delete",
			Model:          api.ModelSpec{ConnectionRef: connection, Model: "deepseek-chat"},
			CellnSelection: &api.CellnCatalogueSelection{RuntimeRef: "celln-native", ToolRefs: []api.CellnCatalogueToolRef{}, ClusterToolRefs: []api.ClusterCellnToolRef{{Name: "workspace-read", Revision: "v1"}}}},
		Status: api.AgentRunStatus{CellnOnly: true},
	}
	if lifecycle == "enduring" {
		run.Spec.Enduring = &api.EnduringRunSpec{LeaseSeconds: 600, MaxTurns: 2, MaxModelRequests: 12, MaxOutputTokens: 49152}
	}
	return run
}

// The resolved route's auth picks the path, not which dispatchers happen to be
// configured: host-profile is provisioned on the fleet, secret/none is
// executed by the scoped receiver (true), on one controller.
func TestSelectionFollowsTheRouteAuth(t *testing.T) {
	platform := stubParentAdmission{platform: true}
	configurations := map[string]func(*AgentRunReconciler){
		"platform and scoped": func(r *AgentRunReconciler) {
			r.ParentAdmission, r.ScopedDispatcher = platform, &cellnscoped.Dispatcher{}
		},
		"platform only": func(r *AgentRunReconciler) { r.ParentAdmission = platform },
	}
	for configuration, configure := range configurations {
		for _, tc := range []struct {
			name, connection string
			mutate           func(*api.AgentRun)
			scoped           bool
		}{
			{name: "host-profile", connection: "fleet", scoped: false},
			{name: "secret", connection: "own", scoped: true},
			{name: "none", connection: "open", scoped: true},
			// A route frozen into the run outranks the live connection: a run
			// normalised for provisioning cannot be moved by editing either.
			{name: "frozen host-profile route naming a Secret connection", connection: "own", mutate: func(run *api.AgentRun) { run.Spec.Model.CredentialProfile = "starter" }, scoped: false},
			// A bound native parent never moves to the scoped receiver.
			{name: "bound native parent whose connection now names a Secret", connection: "own", mutate: func(run *api.AgentRun) { run.Status.CellnParent = &api.CellnParentStatus{} }, scoped: false},
			// A scoped binding always wins recovery.
			{name: "scoped binding whose connection now names a host profile", connection: "fleet", mutate: func(run *api.AgentRun) { run.Status.CellnScoped = &api.CellnScopedStatus{} }, scoped: true},
		} {
			for _, lifecycle := range []string{"enduring", "one-shot"} {
				t.Run(configuration+"/"+lifecycle+"/"+tc.name, func(t *testing.T) {
					run := routePathRun("run", tc.connection, lifecycle)
					if tc.mutate != nil {
						tc.mutate(run)
					}
					r := newAgentRunTestReconciler(t, append(routePathObjects(), run)...)
					configure(r)
					scoped, err := r.sharedCatalogueSelected(context.Background(), run)
					if err != nil || scoped != tc.scoped {
						t.Fatalf("scoped = %v, want %v (err %v)", scoped, tc.scoped, err)
					}
					if lifecycle == "one-shot" && run.Status.CellnParent == nil && run.Status.CellnScoped == nil {
						if provisioned, err := r.platformOneShotSelected(context.Background(), run); err != nil || provisioned == tc.scoped {
							t.Fatalf("one-shot provisioned = %v although scoped = %v (err %v)", provisioned, tc.scoped, err)
						}
					}
				})
			}
		}
	}
	// A run whose route cannot be classified keeps the configured default,
	// where that path's resolver refuses it.
	for name, run := range map[string]*api.AgentRun{"missing connection": routePathRun("run", "absent", "enduring"), "no connection": routePathRun("run", "", "enduring")} {
		for configuration, want := range map[string]bool{"platform and scoped": true, "platform only": false} {
			r := newAgentRunTestReconciler(t, append(routePathObjects(), run)...)
			configurations[configuration](r)
			if scoped, err := r.sharedCatalogueSelected(context.Background(), run); err != nil || scoped != want {
				t.Fatalf("%s, %s: scoped = %v, want %v (err %v)", name, configuration, scoped, want, err)
			}
		}
	}
	// Without a platform-capable admission nothing changes: a configured
	// scoped receiver still owns every enduring catalogue selection.
	r := newAgentRunTestReconciler(t, append(routePathObjects(), routePathRun("run", "fleet", "enduring"))...)
	r.ScopedDispatcher = &cellnscoped.Dispatcher{}
	if scoped, err := r.sharedCatalogueSelected(context.Background(), routePathRun("run", "fleet", "enduring")); err != nil || !scoped {
		t.Fatalf("scoped-only controller changed its selection: %v %v", scoped, err)
	}
}

// One controller, both paths: the host-profile run reaches platform admission
// and the Secret run reaches the scoped dispatcher, never the other way round.
func TestOneControllerServesBothEnduringPaths(t *testing.T) {
	ctx := context.Background()
	fleet, own := routePathRun("fleet-run", "fleet", "enduring"), routePathRun("own-run", "own", "enduring")
	r := newAgentRunTestReconciler(t, append(routePathObjects(), fleet, own)...)
	var admitted atomic.Value
	var admissions atomic.Int32
	r.ParentConfigPath = t.TempDir()
	r.ParentAdmission = platformAdmissionFunc(func(_ context.Context, key types.NamespacedName) error {
		admissions.Add(1)
		admitted.Store(key.Name)
		return errors.New("owner not reachable in this test")
	})
	// An unconfigured dispatcher refuses at Prepare: reaching it is the proof.
	r.ScopedDispatcher = &cellnscoped.Dispatcher{}

	// The first reconciliation freezes the connection's route into the run.
	if result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(fleet)}); err != nil || !result.Requeue || admissions.Load() != 0 {
		t.Fatalf("host-profile run was not normalised first: %+v %v", result, err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(fleet)}); err == nil {
		t.Fatal("host-profile run did not reach platform admission")
	}
	if admissions.Load() != 1 || admitted.Load() != "fleet-run" {
		t.Fatalf("platform admissions = %d (%v)", admissions.Load(), admitted.Load())
	}
	var current api.AgentRun
	if err := r.Get(ctx, client.ObjectKeyFromObject(fleet), &current); err != nil {
		t.Fatal(err)
	}
	if meta.FindStatusCondition(current.Status.Conditions, "CellnScopedExecution") != nil || current.Status.CellnScoped != nil {
		t.Fatalf("host-profile run touched the scoped path: %+v", current.Status)
	}
	if condition := meta.FindStatusCondition(current.Status.Conditions, "CellnParentReady"); condition == nil || condition.Reason != "AdmissionPending" {
		t.Fatalf("host-profile run is not pending platform admission: %+v", current.Status.Conditions)
	}
	// Normalisation froze the host-profile route and no credential reference.
	if current.Spec.Model.CredentialProfile != "starter" || current.Spec.Model.AuthSecretRef != "" {
		t.Fatalf("host-profile run model = %+v", current.Spec.Model)
	}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(own)}); err == nil {
		t.Fatal("Secret run did not reach the scoped dispatcher")
	}
	if admissions.Load() != 1 {
		t.Fatalf("a Secret run reached platform admission (%d admissions, last %v)", admissions.Load(), admitted.Load())
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(own), &current); err != nil {
		t.Fatal(err)
	}
	if condition := meta.FindStatusCondition(current.Status.Conditions, "CellnScopedExecution"); condition == nil || condition.Reason != "PreparationUnconfirmed" {
		t.Fatalf("Secret run did not reach scoped preparation: %+v", current.Status.Conditions)
	}
	if meta.FindStatusCondition(current.Status.Conditions, "CellnParentReady") != nil || current.Status.CellnParent != nil || current.Status.Phase == api.AgentRunPhaseFailed {
		t.Fatalf("Secret run touched the provision path: %+v", current.Status)
	}
	// The Secret is referenced by the connection only; the run never names it.
	if current.Spec.Model.AuthSecretRef != "" || current.Spec.Model.CredentialProfile != "" {
		t.Fatalf("Secret run model = %+v", current.Spec.Model)
	}
}

// Without a scoped receiver a gateway-mediated run is held with a clear
// reason. It never falls back to provisioning and nothing is sent anywhere.
func TestMediatedRunIsHeldWithoutAScopedReceiver(t *testing.T) {
	ctx := context.Background()
	for _, lifecycle := range []string{"enduring", "one-shot"} {
		for _, connection := range []string{"own", "open"} {
			t.Run(lifecycle+"/"+connection, func(t *testing.T) {
				run := routePathRun("held", connection, lifecycle)
				r := newAgentRunTestReconciler(t, append(routePathObjects(), run)...)
				var admissions atomic.Int32
				r.ParentConfigPath = t.TempDir()
				r.ParentAdmission = platformAdmissionFunc(func(context.Context, types.NamespacedName) error {
					admissions.Add(1)
					return nil
				})
				for range 2 {
					result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)})
					if err != nil || result.RequeueAfter == 0 {
						t.Fatalf("held run: result %+v err %v", result, err)
					}
				}
				if admissions.Load() != 0 {
					t.Fatalf("a %s run reached platform admission %d times", connection, admissions.Load())
				}
				var current api.AgentRun
				if err := r.Get(ctx, client.ObjectKeyFromObject(run), &current); err != nil {
					t.Fatal(err)
				}
				condition := meta.FindStatusCondition(current.Status.Conditions, "CellnScopedExecution")
				if condition == nil || condition.Reason != "ScopedDispatchDisabled" || condition.Status != metav1.ConditionFalse {
					t.Fatalf("held run condition = %+v", condition)
				}
				if current.Status.CellnParent != nil || current.Status.CellnScoped != nil || current.Status.JobName != "" || current.Status.CellnIssuance != nil || current.Status.Phase == api.AgentRunPhaseFailed || current.Status.Phase == api.AgentRunPhaseRunning {
					t.Fatalf("held run escaped: %+v", current.Status)
				}
			})
		}
	}
}

// With both paths on one manager a follow-up turn follows its parent run.
func TestTurnFollowsItsParentsPath(t *testing.T) {
	ctx := context.Background()
	parent := routePathRun("fleet-run", "fleet", "enduring")
	parent.Status.CellnParent = &api.CellnParentStatus{}
	scoped := routePathRun("own-run", "own", "enduring")
	scoped.Status.CellnScoped = &api.CellnScopedStatus{}
	unbound := routePathRun("pending-run", "fleet", "enduring")
	reconciler := newAgentRunTestReconciler(t, parent, scoped, unbound)
	r := &AgentRunTurnReconciler{Client: reconciler.Client, APIReader: reconciler.Client, ScopedDispatcher: &cellnscoped.Dispatcher{}, ParentConfigPath: t.TempDir()}
	turn := func(run *api.AgentRun, mutate func(*api.AgentRunTurn)) *api.AgentRunTurn {
		out := &api.AgentRunTurn{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "turn"}, Spec: api.AgentRunTurnSpec{RunName: run.Name, RunUID: string(run.UID), Message: "continue"}}
		if mutate != nil {
			mutate(out)
		}
		return out
	}
	for _, tc := range []struct {
		name   string
		turn   *api.AgentRunTurn
		parent bool
	}{
		{name: "native parent", turn: turn(parent, nil), parent: true},
		{name: "scoped parent", turn: turn(scoped, nil), parent: false},
		{name: "unbound run keeps the scoped handling", turn: turn(unbound, nil), parent: false},
		{name: "another run of the same name", turn: turn(parent, func(t *api.AgentRunTurn) { t.Spec.RunUID = "reused-name" }), parent: false},
		{name: "run is gone", turn: turn(parent, func(t *api.AgentRunTurn) { t.Spec.RunName = "deleted" }), parent: false},
		{name: "scoped turn stays scoped", turn: turn(parent, func(t *api.AgentRunTurn) { t.Status.CellnScoped = &api.CellnScopedStatus{} }), parent: false},
		{name: "parent-path record survives its run", turn: turn(parent, func(t *api.AgentRunTurn) {
			t.Spec.RunName = "deleted"
			t.Status.Execution = &api.CellnParentTurnStatus{ID: "turn", Attempted: true}
		}), parent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := r.turnOnNativeParent(ctx, r.APIReader, tc.turn)
			if err != nil || got != tc.parent {
				t.Fatalf("native parent path = %v, want %v (err %v)", got, tc.parent, err)
			}
		})
	}
}
