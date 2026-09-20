package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnauthority"
	"github.com/sympozium-ai/sympozium/internal/cellnscoped"
	"github.com/sympozium-ai/sympozium/internal/controller"
	"github.com/sympozium-ai/sympozium/internal/modelbudget"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func assertDeniedNamespace(ctx context.Context, c client.Client, o options, run *api.AgentRun) error {
	before := run.DeepCopy()
	_, err := (cellnauthority.PlatformResolver{Reader: c}).Resolve(ctx, client.ObjectKeyFromObject(run), cellnauthority.PlatformResolveRequest{ClusterID: o.contextName, Operation: "execution.start"})
	if err == nil || cellnauthority.PlatformReason(err) != cellnauthority.ReasonPolicyWithdrawn {
		return fmt.Errorf("non-tenant namespace was not denied before issuance: %v", err)
	}
	var after api.AgentRun
	if getErr := c.Get(ctx, client.ObjectKeyFromObject(run), &after); getErr != nil {
		return getErr
	}
	if after.Status.CellnScoped != nil || after.Status.Phase != "" || before.UID != after.UID {
		return errors.New("denied namespace acquired execution state")
	}
	return nil
}

func assertNativeProvenance(state *api.CellnScopedStatus, parent bool) error {
	if state == nil || state.ReceiverID == "" || state.Owner == "" || state.ReceiptDigest == "" || state.CellID == "" || state.ExecutionProvenance == "" || state.SubstrateProvenance == "" {
		return errors.New("receiver owner, receipt, cell, or native evidence is missing")
	}
	if parent != (state.ParentIncarnation != "" && state.ParentID != "" && state.ChildID != "") {
		return errors.New("parent correlation does not match lifecycle")
	}
	return nil
}

func exactUsage(got modelbudget.Usage, runRequests, runReserved, runObserved, turnRequests, turnReserved, turnObserved int64, runClosed, turnClosed bool) error {
	if got.RunReservedRequests != runRequests || got.RunReservedOutputTokens != runReserved || got.RunObservedOutputTokens != runObserved || got.TurnReservedRequests != turnRequests || got.TurnReservedOutputTokens != turnReserved || got.TurnObservedOutputTokens != turnObserved || got.RunClosed != runClosed || got.TurnClosed != turnClosed {
		return fmt.Errorf("got %+v", got)
	}
	return nil
}

func driveEnduringReady(ctx context.Context, reconciler *controller.AgentRunReconciler, dispatcher *cellnscoped.Dispatcher, c client.Client, original *api.AgentRun) (api.AgentRun, *cellnauthority.FinalizedPreparation, error) {
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(original)}
	for {
		if _, err := reconciler.Reconcile(ctx, request); err != nil {
			return api.AgentRun{}, nil, diagnostic(ctx, c, request.NamespacedName, "enduring root", err)
		}
		var current api.AgentRun
		if err := c.Get(ctx, request.NamespacedName, &current); err != nil {
			return current, nil, err
		}
		if current.Status.Phase == api.AgentRunPhaseFailed {
			return current, nil, errors.New(current.Status.Error)
		}
		if current.Status.Phase == api.AgentRunPhaseRunning && current.Status.Result != "" && current.Status.CellnScoped != nil && current.Status.CellnScoped.NativePhase == "Running" && current.Status.CellnScoped.ReceiptDigest != "" {
			prepared, err := dispatcher.Store.Load(ctx, current.Status.CellnScoped.PreparationName)
			if err != nil {
				return current, nil, err
			}
			final, err := dispatcher.Store.LoadFinal(ctx, current.Status.CellnScoped.DecisionName, prepared)
			return current, final, err
		}
		select {
		case <-ctx.Done():
			return current, nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func createTurn(ctx context.Context, c client.Client, parent *api.AgentRun, name, message string) (*api.AgentRunTurn, error) {
	control := true
	turn := &api.AgentRunTurn{ObjectMeta: metav1.ObjectMeta{Namespace: parent.Namespace, Name: name, OwnerReferences: []metav1.OwnerReference{{APIVersion: api.GroupVersion.String(), Kind: "AgentRun", Name: parent.Name, UID: parent.UID, Controller: &control}}}, Spec: api.AgentRunTurnSpec{RunName: parent.Name, RunUID: string(parent.UID), Message: message}}
	if err := c.Create(ctx, turn); err != nil {
		return turn, err
	}
	return turn, nil
}

func createAndDriveTurn(ctx context.Context, reconciler *controller.AgentRunTurnReconciler, c client.Client, parent *api.AgentRun, name, message string) (api.AgentRunTurn, error) {
	turn, err := createTurn(ctx, c, parent, name, message)
	if err != nil {
		return api.AgentRunTurn{}, err
	}
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(turn)}
	for {
		if _, err := reconciler.Reconcile(ctx, request); err != nil {
			return *turn, fmt.Errorf("turn reconcile: %w", err)
		}
		var current api.AgentRunTurn
		if err := c.Get(ctx, request.NamespacedName, &current); err != nil {
			return current, err
		}
		condition := findTurnComplete(current.Status.Conditions)
		if condition != nil && condition.Reason == "Committed" && current.Status.CellnScoped != nil && current.Status.CellnScoped.CleanupConfirmed && !slices.Contains(current.Finalizers, "sympozium.ai/agentrunturn-finalizer") {
			return current, nil
		}
		select {
		case <-ctx.Done():
			return current, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func findTurnComplete(conditions []metav1.Condition) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == "CellnTurnComplete" && conditions[i].Status == metav1.ConditionTrue {
			return &conditions[i]
		}
	}
	return nil
}

func createAndProveExhaustedTurn(ctx context.Context, reconciler *controller.AgentRunTurnReconciler, c client.Client, parent *api.AgentRun, recorder *providerRecorder) (*api.AgentRunTurn, error) {
	calls := recorder.attempts.Load()
	turn, err := createTurn(ctx, c, parent, "turn-exhausted", "Call uppercase with text surplus.")
	if err != nil {
		return nil, err
	}
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(turn)}
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
		_, _ = reconciler.Reconcile(ctx, request)
		var current api.AgentRunTurn
		if err := c.Get(ctx, request.NamespacedName, &current); err != nil {
			return nil, err
		}
		for _, condition := range current.Status.Conditions {
			if condition.Type == "CellnTurnComplete" && condition.Status == metav1.ConditionTrue && condition.Reason == "BudgetExhausted" && current.Status.CellnScoped != nil && current.Status.CellnScoped.CleanupConfirmed {
				if recorder.attempts.Load() != calls || current.Status.CellnScoped.StartAttempted {
					return &current, errors.New("native/provider called after budget exhaustion")
				}
				return &current, nil
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return nil, errors.New("third enduring turn did not fail with durable AUTH_BUDGET_EXHAUSTED")
}

func finishTurnAfterRootCleanup(ctx context.Context, reconciler *controller.AgentRunTurnReconciler, c client.Client, turn *api.AgentRunTurn) error {
	var requested api.AgentRunTurn
	if err := c.Get(ctx, client.ObjectKeyFromObject(turn), &requested); err != nil {
		if apierrors.IsNotFound(err) && turn.Status.CellnScoped != nil && turn.Status.CellnScoped.CleanupConfirmed {
			return nil
		}
		return err
	}
	if requested.UID != turn.UID {
		return errors.New("turn identity changed before cleanup")
	}
	uid := requested.UID
	if err := c.Delete(ctx, &requested, &client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(turn)}
	for {
		if _, err := reconciler.Reconcile(ctx, request); err != nil {
			return err
		}
		var current api.AgentRunTurn
		err := c.Get(ctx, request.NamespacedName, &current)
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if current.Status.CellnScoped != nil && current.Status.CellnScoped.CleanupConfirmed && !slices.Contains(current.Finalizers, "sympozium.ai/agentrunturn-finalizer") {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func beginEnduringCleanup(ctx context.Context, reconciler *controller.AgentRunReconciler, c client.Client, original *api.AgentRun) (api.AgentRun, error) {
	var current api.AgentRun
	if err := c.Get(ctx, client.ObjectKeyFromObject(original), &current); err != nil {
		return current, err
	}
	uid := current.UID
	if err := c.Delete(ctx, &current, &client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil {
		return current, err
	}
	last := current
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(original)}
	for {
		if _, err := reconciler.Reconcile(ctx, request); err != nil {
			return last, err
		}
		err := c.Get(ctx, request.NamespacedName, &current)
		if apierrors.IsNotFound(err) {
			// The controller may remove the root finalizer in the same reconcile
			// that confirms teardown. Verify its independent native owner record;
			// object absence alone is never cleanup evidence.
			s := last.Status.CellnScoped
			if s == nil {
				return last, errors.New("root disappeared without protected owner identity")
			}
			prepared, err := reconciler.ScopedDispatcher.Store.Load(ctx, s.PreparationName)
			if err != nil {
				return last, err
			}
			final, err := reconciler.ScopedDispatcher.Store.LoadFinal(ctx, s.DecisionName, prepared)
			if err != nil {
				return last, err
			}
			observed, err := reconciler.ScopedDispatcher.Read(ctx, s.ReceiverID, final)
			if err != nil || observed.ID != s.ReceiverID || observed.Owner != s.Owner || !observed.CleanupConfirmed {
				return last, errors.New("root disappeared without independent native teardown confirmation")
			}
			s.CleanupConfirmed = true
			s.NativePhase = observed.Phase
			return last, nil
		}
		if err != nil {
			return last, err
		}
		last = current
		if current.Status.CellnScoped != nil && current.Status.CellnScoped.CleanupConfirmed {
			return current, nil
		}
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

type liveRuns struct {
	direct, oneShot, enduring, denied *api.AgentRun
	oneShotSecret, enduringSecret     *corev1.Secret
}

func createAuthorityAndRuns(ctx context.Context, c client.Client, o options, pkg nativePackage, providerURL, oneShotSecretValue, enduringSecretValue string) (*createdObjects, liveRuns, error) {
	_, policyName := resourceNames(o.namespace)
	marker := o.reviewOwnership
	if marker == "" {
		marker = "scoped-live-" + o.namespace
	}
	objects := &createdObjects{preparationNamespace: o.preparationNamespace}
	var runs liveRuns
	create := func(object client.Object) error {
		if err := c.Create(ctx, object); err != nil {
			return fmt.Errorf("create %T %s/%s: %w", object, object.GetNamespace(), object.GetName(), err)
		}
		return nil
	}
	if o.reviewOwnership == "" {
		for _, name := range []string{o.namespace, o.enduringNamespace, o.deniedNamespace, o.preparationNamespace} {
			labels := map[string]string{"sympozium.ai/celln-review": marker}
			if name == o.namespace || name == o.enduringNamespace {
				labels["sympozium.ai/celln-review-tenant"] = "enabled"
			}
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
			if err := create(ns); err != nil {
				return objects, runs, err
			}
			objects.namespaces = append(objects.namespaces, ns)
		}
	}
	for _, source := range []*api.CellnRuntimeProfile{&pkg.OneShot, &pkg.Enduring} {
		profile := source.DeepCopy()
		profile.ResourceVersion = ""
		profile.UID = ""
		profile.CreationTimestamp = metav1.Time{}
		if err := create(profile); err != nil {
			return objects, runs, err
		}
		objects.profiles = append(objects.profiles, profile)
	}
	objects.tool = pkg.Uppercase.DeepCopy()
	objects.tool.ResourceVersion = ""
	objects.tool.UID = ""
	objects.tool.CreationTimestamp = metav1.Time{}
	if err := create(objects.tool); err != nil {
		return objects, runs, err
	}
	objects.policy = &api.CellnExecutionPolicy{ObjectMeta: metav1.ObjectMeta{Name: policyName}, Spec: api.CellnExecutionPolicySpec{
		NamespaceSelector: metav1.LabelSelector{MatchLabels: map[string]string{"sympozium.ai/celln-review": marker, "sympozium.ai/celln-review-tenant": "enabled"}},
		RuntimeProfiles:   []api.CellnExecutionPolicyRuntime{{Ref: api.CellnRuntimeProfileRef{Name: pkg.OneShot.Name, Revision: pkg.OneShot.Spec.Revision}}, {Ref: api.CellnRuntimeProfileRef{Name: pkg.Enduring.Name, Revision: pkg.Enduring.Spec.Revision}}},
		Tools:             []api.CellnExecutionPolicyTool{{Ref: api.ClusterCellnToolRef{Name: pkg.Uppercase.Name, Revision: pkg.Uppercase.Spec.Revision}}}, Lifecycles: []string{"direct-one-shot", "harness-one-shot", "enduring"},
		Routes:   []api.CellnExecutionPolicyRoute{{Provider: modelProvider, Protocol: modelProtocol, Models: []string{modelName}, EndpointOrigins: []string{providerURL}, Auth: "secret"}},
		Ceilings: api.CellnExecutionPolicyCeilings{MaxTurns: 2, MaxModelRequests: 4, MaxOutputTokens: 2048, MaxParentLeaseSeconds: 300, MaxTurnSeconds: 120},
	}}
	if err := create(objects.policy); err != nil {
		return objects, runs, err
	}
	setup := func(namespace, profileName, secretValue string) (*api.AgentRuntime, *api.Agent, *corev1.Secret, *api.ModelConnection, error) {
		profile := pkg.OneShot
		if profileName == pkg.Enduring.Name {
			profile = pkg.Enduring
		}
		runtimeWrapper := &api.AgentRuntime{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "runtime"}, Spec: api.AgentRuntimeSpec{CellnProfileRef: &api.CellnRuntimeProfileRef{Name: profile.Name, Revision: profile.Spec.Revision}}}
		agent := &api.Agent{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "agent"}, Spec: api.AgentSpec{RuntimeRef: "runtime", Agents: api.AgentsSpec{Default: api.AgentConfig{}},
			// The Agent owner grants the connection's Secret; a run cannot borrow
			// a Secret-backed connection its Agent was not given.
			AuthRefs: []api.SecretRef{{Provider: modelProvider, Secret: "model-credential"}}}}
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "model-credential"}, Type: corev1.SecretTypeOpaque, Data: map[string][]byte{"OPENAI_API_KEY": []byte(secretValue)}}
		connection := &api.ModelConnection{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "model"}, Spec: api.ModelConnectionSpec{Provider: modelProvider, Protocol: modelProtocol, Endpoint: providerURL + "/v1/chat/completions", SecretRef: secret.Name, Models: []string{modelName}, AllowInsecure: true}}
		for _, object := range []client.Object{runtimeWrapper, agent, secret, connection} {
			if err := create(object); err != nil {
				return nil, nil, nil, nil, err
			}
			objects.namespaced = append(objects.namespaced, object)
		}
		return runtimeWrapper, agent, secret, connection, nil
	}
	oneRuntime, oneAgent, oneSecret, oneConnection, err := setup(o.namespace, pkg.OneShot.Name, oneShotSecretValue)
	if err != nil {
		return objects, runs, err
	}
	runs.oneShotSecret = oneSecret
	selection := func(runtime string) *api.CellnCatalogueSelection {
		return &api.CellnCatalogueSelection{RuntimeRef: runtime, ToolRefs: []api.CellnCatalogueToolRef{}, ClusterToolRefs: []api.ClusterCellnToolRef{{Name: pkg.Uppercase.Name, Revision: pkg.Uppercase.Spec.Revision}}}
	}
	runs.direct = &api.AgentRun{ObjectMeta: metav1.ObjectMeta{Namespace: o.namespace, Name: "direct-uppercase"}, Spec: api.AgentRunSpec{AgentRef: oneAgent.Name, AgentID: "default", SessionKey: "direct", Task: api.NewStringTask(directArguments), Backend: "celln", ExecutionLifecycle: "one-shot", CellnSelection: selection(oneRuntime.Name), Cleanup: "delete"}}
	runs.oneShot = &api.AgentRun{ObjectMeta: metav1.ObjectMeta{Namespace: o.namespace, Name: "composed-uppercase"}, Spec: api.AgentRunSpec{AgentRef: oneAgent.Name, AgentID: "default", SessionKey: "one-shot", Task: api.NewStringTask(modelTask), SystemPrompt: "Use the selected uppercase tool exactly once and return only its text field.", Model: api.ModelSpec{ConnectionRef: oneConnection.Name, Model: modelName}, Backend: "celln", ExecutionLifecycle: "one-shot", CellnSelection: selection(oneRuntime.Name), Cleanup: "delete"}}
	endRuntime, endAgent, endSecret, endConnection, err := setup(o.enduringNamespace, pkg.Enduring.Name, enduringSecretValue)
	if err != nil {
		return objects, runs, err
	}
	runs.enduringSecret = endSecret
	runs.enduring = &api.AgentRun{ObjectMeta: metav1.ObjectMeta{Namespace: o.enduringNamespace, Name: "enduring-uppercase"}, Spec: api.AgentRunSpec{AgentRef: endAgent.Name, AgentID: "default", SessionKey: "enduring", Task: api.NewStringTask(modelTask), SystemPrompt: "Use the selected uppercase tool exactly once on every turn and return only its text field.", Model: api.ModelSpec{ConnectionRef: endConnection.Name, Model: modelName}, Backend: "celln", ExecutionLifecycle: "enduring", Enduring: &api.EnduringRunSpec{LeaseSeconds: 300, MaxTurns: 2, MaxModelRequests: 4, MaxOutputTokens: 2048}, CellnSelection: selection(endRuntime.Name), Cleanup: "delete"}}
	denRuntime, denAgent, _, _, err := setup(o.deniedNamespace, pkg.OneShot.Name, oneShotSecretValue)
	if err != nil {
		return objects, runs, err
	}
	runs.denied = &api.AgentRun{ObjectMeta: metav1.ObjectMeta{Namespace: o.deniedNamespace, Name: "denied-uppercase"}, Spec: api.AgentRunSpec{AgentRef: denAgent.Name, AgentID: "default", Task: api.NewStringTask(directArguments), Backend: "celln", ExecutionLifecycle: "one-shot", CellnSelection: selection(denRuntime.Name), Cleanup: "delete"}}
	for _, run := range []*api.AgentRun{runs.direct, runs.oneShot, runs.enduring, runs.denied} {
		if err := create(run); err != nil {
			return objects, runs, err
		}
		objects.runs = append(objects.runs, run)
	}
	return objects, runs, nil
}

func driveToTerminal(ctx context.Context, reconciler *controller.AgentRunReconciler, dispatcher *cellnscoped.Dispatcher, c client.Client, original *api.AgentRun) (api.AgentRun, cellnscoped.OperationStatus, *cellnauthority.FinalizedPreparation, error) {
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(original)}
	for {
		if _, err := reconciler.Reconcile(ctx, request); err != nil {
			return api.AgentRun{}, cellnscoped.OperationStatus{}, nil, diagnostic(ctx, c, request.NamespacedName, "reconcile to terminal", err)
		}
		var current api.AgentRun
		if err := c.Get(ctx, request.NamespacedName, &current); err != nil {
			return current, cellnscoped.OperationStatus{}, nil, err
		}
		if current.Status.Phase == api.AgentRunPhaseFailed {
			return current, cellnscoped.OperationStatus{}, nil, diagnostic(ctx, c, request.NamespacedName, "scoped execution failed", errors.New(current.Status.Error))
		}
		if current.Status.Phase == api.AgentRunPhaseSucceeded {
			if current.Status.CellnScoped == nil || current.Status.CellnScoped.ReceiverID == "" || current.Status.CellnScoped.ReceiptDigest == "" {
				return current, cellnscoped.OperationStatus{}, nil, errors.New("terminal run lacks scoped owner/receipt identity")
			}
			prepared, err := dispatcher.Store.Load(ctx, current.Status.CellnScoped.PreparationName)
			if err != nil {
				return current, cellnscoped.OperationStatus{}, nil, err
			}
			final, err := dispatcher.Store.LoadFinal(ctx, current.Status.CellnScoped.DecisionName, prepared)
			if err != nil {
				return current, cellnscoped.OperationStatus{}, nil, err
			}
			_, execution, model, err := dispatcher.StartTokens(final)
			if err != nil {
				return current, cellnscoped.OperationStatus{}, final, fmt.Errorf("issue duplicate-start permits inside original window: %w", err)
			}
			duplicate, err := dispatcher.Start(ctx, current.Status.CellnScoped.ReceiverID, current.Status.CellnScoped.Owner, execution, model)
			if err != nil {
				return current, duplicate, final, fmt.Errorf("native duplicate-start recovery: %w", err)
			}
			if duplicate.ID != current.Status.CellnScoped.ReceiverID || duplicate.Owner != current.Status.CellnScoped.Owner || duplicate.Phase != "Succeeded" {
				return current, duplicate, final, fmt.Errorf("duplicate start did not recover original terminal owner: idMatch=%t ownerMatch=%t phase=%s", duplicate.ID == current.Status.CellnScoped.ReceiverID, duplicate.Owner == current.Status.CellnScoped.Owner, duplicate.Phase)
			}
			return current, duplicate, final, nil
		}
		select {
		case <-ctx.Done():
			return current, cellnscoped.OperationStatus{}, nil, diagnostic(ctx, c, request.NamespacedName, "terminal timeout", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func driveCleanup(ctx context.Context, reconciler *controller.AgentRunReconciler, c client.Client, original *api.AgentRun) (api.AgentRun, error) {
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(original)}
	for {
		if _, err := reconciler.Reconcile(ctx, request); err != nil {
			return api.AgentRun{}, diagnostic(ctx, c, request.NamespacedName, "reconcile cleanup", err)
		}
		var current api.AgentRun
		if err := c.Get(ctx, request.NamespacedName, &current); err != nil {
			return current, err
		}
		if current.Status.CellnScoped != nil && current.Status.CellnScoped.CleanupConfirmed && !containsFinalizer(current.Finalizers) {
			return current, nil
		}
		select {
		case <-ctx.Done():
			return current, diagnostic(ctx, c, request.NamespacedName, "cleanup timeout", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func containsFinalizer(values []string) bool {
	return slices.Contains(values, "sympozium.ai/agentrun-finalizer")
}

func diagnostic(ctx context.Context, c client.Client, key types.NamespacedName, stage string, cause error) error {
	var run api.AgentRun
	if err := c.Get(ctx, key, &run); err != nil {
		return fmt.Errorf("%s: %v (status unavailable: %v)", stage, cause, err)
	}
	condition := "none"
	for _, item := range run.Status.Conditions {
		if item.Type == "CellnScopedExecution" {
			condition = item.Reason + ":" + item.Message
		}
	}
	native := "none"
	if run.Status.CellnScoped != nil {
		native = run.Status.CellnScoped.NativePhase
	}
	return fmt.Errorf("%s: %v (phase=%s native=%s condition=%s)", stage, cause, run.Status.Phase, native, condition)
}

func assertProtectedRecords(ctx context.Context, c client.Client, namespace string, run api.AgentRun, providerSecrets ...string) error {
	if run.Status.CellnScoped == nil {
		return errors.New("scoped status missing while checking protected records")
	}
	for _, name := range []string{run.Status.CellnScoped.PreparationName, run.Status.CellnScoped.DecisionName} {
		var record corev1.ConfigMap
		if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &record); err != nil {
			return fmt.Errorf("protected record %q unavailable: %w", name, err)
		}
		if record.Immutable == nil || !*record.Immutable || record.Namespace != namespace {
			return fmt.Errorf("protected record %q is not immutable in its dedicated namespace", name)
		}
		for _, value := range record.Data {
			leaked := strings.Contains(value, "Bearer ")
			for _, secret := range providerSecrets {
				leaked = leaked || strings.Contains(value, secret)
			}
			if leaked {
				return fmt.Errorf("protected record %q contains credential material", name)
			}
		}
	}
	return nil
}

func rememberProtected(ctx context.Context, c client.Client, objects *createdObjects, namespace string, state *api.CellnScopedStatus) error {
	if state == nil {
		return errors.New("cannot track missing protected scoped state")
	}
	for _, name := range []string{state.PreparationName, state.DecisionName} {
		if name == "" {
			return errors.New("protected record name is empty")
		}
		var record corev1.ConfigMap
		if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &record); err != nil {
			return err
		}
		if record.Immutable == nil || !*record.Immutable {
			return fmt.Errorf("protected record %q is mutable", name)
		}
		duplicate := false
		for _, existing := range objects.protected {
			if existing.GetUID() == record.UID {
				duplicate = true
			}
		}
		if !duplicate {
			objects.protected = append(objects.protected, record.DeepCopy())
		}
	}
	return nil
}

func deleteRunThroughController(ctx context.Context, reconciler *controller.AgentRunReconciler, c client.Client, original *api.AgentRun) error {
	if original == nil || original.UID == "" {
		return nil
	}
	key := client.ObjectKeyFromObject(original)
	var current api.AgentRun
	if err := c.Get(ctx, key, &current); apierrors.IsNotFound(err) {
		return nil
	} else if err != nil {
		return err
	}
	if current.UID != original.UID {
		return errors.New("refusing cleanup of a replacement run UID")
	}
	uid := current.UID
	if err := c.Delete(ctx, &current, &client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("request failed-run deletion: %w", err)
	}
	return wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 20*time.Second, true, func(poll context.Context) (bool, error) {
		if _, err := reconciler.Reconcile(poll, ctrl.Request{NamespacedName: key}); err != nil {
			return false, err
		}
		var probe api.AgentRun
		err := c.Get(poll, key, &probe)
		return apierrors.IsNotFound(err), client.IgnoreNotFound(err)
	})
}

func cleanupCreated(ctx context.Context, c client.Client, objects *createdObjects) error {
	if objects == nil {
		return nil
	}
	var failures []string
	// Recover any protected names persisted before a failed harness step so an
	// existing preparation namespace never accumulates this runner's records.
	for _, original := range objects.runs {
		if original == nil {
			continue
		}
		var current api.AgentRun
		if err := c.Get(ctx, client.ObjectKeyFromObject(original), &current); err == nil && current.Status.CellnScoped != nil {
			if err := rememberProtected(ctx, c, objects, objects.preparationNamespace, current.Status.CellnScoped); err != nil {
				failures = append(failures, err.Error())
			}
		} else if err != nil && !apierrors.IsNotFound(err) {
			failures = append(failures, err.Error())
		}
	}
	for _, namespace := range []string{namespaceOf(objects.runs, 0), namespaceOf(objects.runs, 2)} {
		if namespace == "" {
			continue
		}
		var turns api.AgentRunTurnList
		if err := c.List(ctx, &turns, client.InNamespace(namespace)); err != nil {
			failures = append(failures, err.Error())
			continue
		}
		for i := range turns.Items {
			if turns.Items[i].Status.CellnScoped != nil {
				if err := rememberProtected(ctx, c, objects, objects.preparationNamespace, turns.Items[i].Status.CellnScoped); err != nil {
					failures = append(failures, err.Error())
				}
			}
		}
	}
	remove := func(object client.Object) {
		if object == nil || object.GetUID() == "" {
			return
		}
		uid := object.GetUID()
		if err := c.Delete(ctx, object, &client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil && !apierrors.IsNotFound(err) {
			failures = append(failures, fmt.Sprintf("%s/%s: %v", object.GetNamespace(), object.GetName(), err))
		}
	}
	// Runs should already have completed native/gateway cleanup. Pre-existing
	// review namespaces are never deleted; only objects recorded here are ours.
	for _, run := range objects.runs {
		remove(run)
	}
	for _, object := range objects.protected {
		remove(object)
	}
	for _, object := range objects.namespaced {
		remove(object)
	}
	remove(objects.policy)
	remove(objects.tool)
	for _, profile := range objects.profiles {
		remove(profile)
	}
	for _, namespace := range objects.namespaces {
		remove(namespace)
	}
	if len(failures) != 0 {
		return fmt.Errorf("created-resource cleanup failed: %s", strings.Join(failures, "; "))
	}
	all := append([]client.Object{}, objects.namespaced...)
	all = append(all, objects.protected...)
	for _, run := range objects.runs {
		all = append(all, run)
	}
	all = append(all, objects.policy, objects.tool)
	for _, profile := range objects.profiles {
		all = append(all, profile)
	}
	for _, namespace := range objects.namespaces {
		all = append(all, namespace)
	}
	for _, object := range all {
		if object == nil || object.GetUID() == "" {
			continue
		}
		key := client.ObjectKeyFromObject(object)
		err := wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 20*time.Second, true, func(poll context.Context) (bool, error) {
			probe, ok := object.DeepCopyObject().(client.Object)
			if !ok {
				return false, errors.New("created object is not a Kubernetes client object")
			}
			err := c.Get(poll, key, probe)
			return apierrors.IsNotFound(err), client.IgnoreNotFound(err)
		})
		if err != nil {
			return fmt.Errorf("created-resource deletion unconfirmed for %s/%s: %w", key.Namespace, key.Name, err)
		}
	}
	return nil
}

func namespaceOf(runs []*api.AgentRun, index int) string {
	if index >= 0 && index < len(runs) && runs[index] != nil {
		return runs[index].Namespace
	}
	return ""
}
