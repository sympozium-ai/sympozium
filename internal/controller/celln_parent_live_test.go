package controller_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/apiserver"
	"github.com/sympozium-ai/sympozium/internal/cellnparent"
	"github.com/sympozium-ai/sympozium/internal/controller"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

// Same external entrypoint as the client proof, built from this package instead.
// Real manager watches + Kubernetes + Celln + model + prepared-registration admission.
func TestLiveCellnParentClient(t *testing.T) {
	if os.Getenv("CELLN_INTEROP_ORIGIN") == "" {
		t.Skip("requires Celln hardware launcher")
	}
	if os.Getenv("CELLN_INTEROP_KUBE_CONTEXT") != "kind-celln-deployed" || os.Getenv("CELLN_INTEROP_BORROWED") != "true" {
		t.Fatal("explicit Kind context and borrowed fixture required")
	}
	hold := liveParentHoldDuration(t)
	starter := os.Getenv("CELLN_INTEROP_STARTER") == "true"
	ctx, cancel := context.WithTimeout(t.Context(), 85*time.Second+hold)
	defer cancel()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{CurrentContext: "kind-celln-deployed"}).ClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	store, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "celln-parent-manager-"}}
	if err := store.Create(ctx, ns); err != nil {
		t.Fatal(err)
	}
	t.Logf("isolated manager namespace: %s", ns.Name)
	managerOwnsCleanup := false
	t.Cleanup(func() {
		if managerOwnsCleanup {
			return
		}
		// Before the manager starts, no parent can have been dispatched.
		cleanup, end := context.WithTimeout(context.Background(), 10*time.Second)
		defer end()
		uid := ns.UID
		if err := store.Delete(cleanup, ns, &client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil {
			t.Errorf("pre-start namespace cleanup: %v", err)
		}
	})
	agent := &api.Agent{ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: ns.Name}}
	if err := store.Create(ctx, agent); err != nil {
		t.Fatal(err)
	}
	loader, systemPrompt, toolRefs := liveParentCatalogue(t, ctx, store, agent)
	apiToken := rand.Text()
	tokenReader := apiserver.NewTokenReader(filepath.Join(t.TempDir(), "unused-token-file"), logr.Discard())
	tokenReader.Seed(apiToken)
	bus := liveParentBus(t)
	apiServer := apiserver.NewServer(store, bus, nil, logr.Discard())
	previewBindings := []apiserver.CellnPreviewBinding{{Agent: client.ObjectKeyFromObject(agent), OperatorSource: loader.OperatorSource, RuntimeSource: loader.RuntimeSource, AgentSource: loader.AgentSource}}
	if err := apiServer.ConfigureCellnPreview(store, previewBindings); err != nil {
		t.Fatal(err)
	}
	previewConfig, err := json.Marshal(map[string]any{"apiVersion": "sympozium.ai/celln-permission-preview-v1", "bindings": previewBindings})
	if err != nil {
		t.Fatal(err)
	}
	previewPath := filepath.Join(t.TempDir(), "preview.json")
	if err := os.WriteFile(previewPath, previewConfig, 0600); err != nil {
		t.Fatal(err)
	}
	handler := apiServer.Handler(tokenReader)
	webDir := os.Getenv("CELLN_INTEROP_WEB_DIR")
	if webDir != "" {
		if !filepath.IsAbs(webDir) {
			t.Fatal("absolute built web directory required")
		}
		if _, err := os.Stat(filepath.Join(webDir, "dist", "index.html")); err != nil {
			t.Fatal(err)
		}
		handler = apiServer.HandlerWithUI(tokenReader, os.DirFS(filepath.Join(webDir, "dist")))
	}
	server := liveParentAPI(t, handler, config, ns.Name, apiToken, previewPath)
	t.Cleanup(server.Close)
	unauthorized, err := server.Client().Get(server.URL + "/api/v1/runs?namespace=" + ns.Name)
	if err != nil {
		t.Fatal(err)
	}
	unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatal("API admitted an unauthenticated history request")
	}
	request := func(method, path string, payload any, expected int, output any) {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequestWithContext(ctx, method, server.URL+path, bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+apiToken)
		req.Header.Set("Content-Type", "application/json")
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != expected {
			t.Fatalf("API %s %s: status %d: %s", method, path, res.StatusCode, body)
		}
		if output != nil {
			if err := json.Unmarshal(body, output); err != nil {
				t.Fatal(err)
			}
		}
	}
	run := &api.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "conversation", Namespace: ns.Name}, Spec: api.AgentRunSpec{
		AgentRef: "agent", AgentID: "primary", SessionKey: "manager-proof", Backend: "celln", ExecutionLifecycle: "enduring",
		Task:           api.NewStringTask("My value is violet. Call uppercase with my value and answer only the tool result text."),
		Model:          api.ModelSpec{Provider: "deepseek", Model: "deepseek-chat"},
		CellnSelection: &api.CellnCatalogueSelection{ToolRefs: toolRefs},
		Enduring:       &api.EnduringRunSpec{LeaseSeconds: 180, MaxTurns: 2, MaxModelRequests: 6, MaxOutputTokens: 3072, RequireToolCall: true},
	}}
	if starter {
		run.Spec.Task = api.NewStringTask("Call workspace-write with name notes.txt, revision 0, content violet. On success reply only violet.")
	}
	if webDir != "" {
		command := exec.CommandContext(ctx, "node", filepath.Join(webDir, "node_modules/cypress/bin/cypress"), "run", "--browser", "electron", "--spec", "cypress/e2e/celln-parent-create-live.cy.ts", "--env", "PROOF_NAMESPACE="+ns.Name)
		command.Dir = webDir
		command.Env = append(os.Environ(), "CYPRESS_API_TOKEN="+apiToken, "CYPRESS_BASE_URL="+server.URL, "CYPRESS_PARENT_SYSTEM_PROMPT="+systemPrompt, "CYPRESS_STARTER_TOOLS="+os.Getenv("CELLN_INTEROP_STARTER"))
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("live browser creation: %v\n%s", err, output)
		}
		var created api.AgentRunList
		if err := store.List(ctx, &created, client.InNamespace(ns.Name)); err != nil {
			t.Fatal(err)
		}
		if len(created.Items) != 1 {
			t.Fatalf("browser created %d runs, expected exactly one", len(created.Items))
		}
		run = created.Items[0].DeepCopy()
		if run.Spec.ExecutionLifecycle != "enduring" || run.Spec.Backend != "celln" || run.Spec.CellnSelection == nil || run.Spec.CellnSelection.RuntimeRef != "worker" || !slices.Equal(run.Spec.CellnSelection.ToolRefs, toolRefs) || run.Spec.Enduring == nil || !run.Spec.Enduring.RequireToolCall || run.Spec.Enduring.MaxTurns != 2 || run.Spec.SystemPrompt != systemPrompt {
			t.Fatalf("browser creation intent mismatch: lifecycle=%q backend=%q selection=%+v enduring=%+v personaMatches=%v", run.Spec.ExecutionLifecycle, run.Spec.Backend, run.Spec.CellnSelection, run.Spec.Enduring, run.Spec.SystemPrompt == systemPrompt)
		}
		t.Log("live browser created the initial enduring run with explicit harness and borrowed-tool selection")
	} else {
		request("POST", "/api/v1/runs?namespace="+ns.Name, apiserver.CreateRunRequest{
			AgentRef: run.Spec.AgentRef, Task: run.Spec.Task.GetPrompt(), AgentID: run.Spec.AgentID, SessionKey: run.Spec.SessionKey,
			Backend: "celln", Provider: "deepseek", Model: "deepseek-chat", Timeout: "180s", CellnSelection: run.Spec.CellnSelection,
			ExecutionLifecycle: "enduring", Enduring: run.Spec.Enduring,
			SystemPrompt: systemPrompt,
		}, http.StatusCreated, run)
	}
	if err := store.Get(ctx, client.ObjectKeyFromObject(run), run); err != nil {
		t.Fatal(err)
	}
	registrationPath, approval := liveParentRegistration(t, ctx, loader, run)
	managerConfig := liveParentControllerConfig(t, ctx, store, config, ns.Name)
	mgr, err := ctrl.NewManager(managerConfig, ctrl.Options{Scheme: scheme, Cache: cache.Options{DefaultNamespaces: map[string]cache.Config{ns.Name: {}}}, Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: "0", LeaderElection: false})
	if err != nil {
		t.Fatal(err)
	}
	r := &controller.AgentRunReconciler{Client: mgr.GetClient(), APIReader: mgr.GetAPIReader(), Scheme: scheme, Log: logr.Discard(), ParentConfigPath: approval, EventBus: bus}
	r.ParentOnly = os.Getenv("CELLN_INTEROP_SCOPED_CONTROLLER") == "true"
	r.ParentAdmission, err = cellnparent.LoadRegistrationDispatcher(registrationPath, approval, mgr.GetAPIReader())
	if err != nil {
		t.Fatal(err)
	}
	if err := r.SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	turnController := &controller.AgentRunTurnReconciler{Client: mgr.GetClient(), APIReader: mgr.GetAPIReader(), ParentConfigPath: approval}
	if err := turnController.SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	// testing.T.Context and the function's deferred cancel end before Cleanup.
	// Keep the manager alive until cleanup has observed deletion/finalization.
	stopManager, managerDone := startLiveParentManager(t, mgr, managerConfig, config, ns.Name, registrationPath, approval)
	key := client.ObjectKeyFromObject(run)
	t.Cleanup(func() {
		cleanupBudget := 12 * time.Second
		if hold > 0 {
			cleanupBudget = 75 * time.Second
		}
		cleanup, end := context.WithTimeout(context.Background(), cleanupBudget)
		defer end()
		// Stop admitting user HTTP work before collecting all runs in this
		// isolated namespace. Keep the controller alive for joined teardown.
		server.Close()
		removed := cleanupLiveParentRuns(cleanup, store, ns.Name)
		stopManager()
		select {
		case err := <-managerDone:
			if err != nil {
				t.Errorf("manager exit: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("manager did not stop")
		}
		if !removed {
			t.Errorf("run cleanup uncertain; retaining namespace %s", ns.Name)
			return
		}
		uid := ns.UID
		if err := store.Delete(cleanup, ns, &client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil {
			t.Errorf("namespace cleanup: %v", err)
		}
	})
	managerOwnsCleanup = true
	poll := func(ready func() bool) {
		t.Helper()
		for !ready() {
			select {
			case err := <-managerDone:
				t.Fatalf("manager stopped early: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	poll(func() bool {
		if err := store.Get(ctx, key, run); err != nil {
			t.Fatal(err)
		}
		if run.Status.Phase == api.AgentRunPhaseFailed {
			t.Fatalf("parent failed: %+v", run.Status.Conditions)
		}
		p := run.Status.CellnParent
		return run.Status.Phase == api.AgentRunPhaseRunning && meta.IsStatusConditionTrue(run.Status.Conditions, "CellnParentReady") && p != nil && p.InitialTurn != nil && p.InitialTurn.Result != nil
	})
	turn := &api.AgentRunTurn{}
	turnPath := "/api/v1/runs/" + run.Name + "/turns?namespace=" + ns.Name
	turnInput := map[string]string{"runUID": string(run.UID), "requestId": "follow-up", "message": "Find the value stated in the first user message of this conversation. Call uppercase on that value now. Answer only the tool result text."}
	if starter {
		turnInput["message"] = "Call workspace-read for notes.txt now. Reply only with the content returned by the tool."
	}
	if webDir != "" {
		command := exec.CommandContext(ctx, "node", filepath.Join(webDir, "node_modules/cypress/bin/cypress"), "run", "--browser", "electron", "--spec", "cypress/e2e/celln-parent-live.cy.ts", "--env", "PROOF_NAMESPACE="+ns.Name+",PROOF_RUN="+run.Name)
		command.Dir = webDir
		command.Env = append(os.Environ(), "CYPRESS_API_TOKEN="+apiToken, "CYPRESS_BASE_URL="+server.URL, "CYPRESS_STARTER_TOOLS="+os.Getenv("CELLN_INTEROP_STARTER"))
		if bus != nil {
			command.Env = append(command.Env, "CYPRESS_EXPECT_STREAM=1")
		}
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("live browser: %v\n%s", err, output)
		}
		t.Log("live browser submitted follow-up and rendered committed answer")
		var browserHistory struct {
			Items []api.AgentRunTurn `json:"items"`
		}
		request("GET", turnPath, nil, http.StatusOK, &browserHistory)
		if len(browserHistory.Items) != 1 {
			t.Fatal("browser did not create exactly one turn")
		}
		*turn = browserHistory.Items[0]
	} else {
		request("POST", turnPath, turnInput, http.StatusAccepted, turn)
		var repeated api.AgentRunTurn
		request("POST", turnPath, turnInput, http.StatusOK, &repeated)
		if repeated.UID != turn.UID {
			t.Fatal("same request minted another turn")
		}
	}
	turnKey := client.ObjectKeyFromObject(turn)
	poll(func() bool {
		if err := store.Get(ctx, turnKey, turn); err != nil {
			t.Fatal(err)
		}
		if err := store.Get(ctx, key, run); err != nil {
			t.Fatal(err)
		}
		return turn.Status.Execution != nil && turn.Status.Execution.Result != nil && run.Status.CellnParent.ActiveTurn == nil
	})
	initial := run.Status.CellnParent.InitialTurn
	var history struct {
		RunUID string             `json:"runUID"`
		Items  []api.AgentRunTurn `json:"items"`
	}
	request("GET", turnPath, nil, http.StatusOK, &history)
	if history.RunUID != string(run.UID) || len(history.Items) != 1 || history.Items[0].UID != turn.UID || history.Items[0].Status.Execution.Result == nil {
		t.Fatal("API history did not return the committed turn")
	}
	completed := turn.Status.Execution
	if run.Status.Phase != api.AgentRunPhaseRunning || run.Status.CellnParent.AcceptedTurns != 1 || !initial.Result.Succeeded || !completed.Result.Succeeded || !strings.Contains(strings.ToLower(completed.Result.Answer), "violet") || initial.Child == completed.Child {
		t.Fatal("manager did not preserve parent/context/turn accounting")
	}
	results := []cellnparent.TurnResult{}
	for _, execution := range []*api.CellnParentTurnStatus{initial, completed} {
		results = append(results, cellnparent.TurnResult{Kind: "completed", Version: "celln.parent-context/v1", TurnID: execution.ID, Succeeded: &execution.Result.Succeeded, Answer: execution.Result.Answer})
	}
	admission := "prepared-registration"
	if os.Getenv("CELLN_INTEROP_PROVISION_BINARY") != "" {
		admission = "local-provisioner"
		binding := run.Status.CellnParent.Binding
		raw, err := json.Marshal(map[string]any{"runUID": run.UID, "incarnation": binding.Incarnation, "launchProfile": binding.LaunchProfile})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(filepath.Dir(os.Getenv("CELLN_INTEROP_RESULT")), "interop-provisioned.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for suffix, value := range map[string]any{"": results, ".resources.json": map[string]any{"run": run, "turn": turn, "driver": "real-api-controller-manager", "admission": admission, "browser": webDir != "", "browserCreation": webDir != "", "eventBus": bus != nil, "namespace": ns.Name}} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(os.Getenv("CELLN_INTEROP_RESULT")+suffix, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if os.Getenv("CELLN_INTEROP_REUSE_TEMPLATE") == "true" {
		if admission != "local-provisioner" {
			t.Fatal("template reuse requires automatic provisioning")
		}
		original := run.DeepCopy()
		originalConfig, err := os.ReadFile(registrationPath)
		if err != nil {
			t.Fatal(err)
		}
		if os.Getenv("CELLN_INTEROP_BROWSER_DELETE") == "true" {
			if webDir == "" {
				t.Fatal("browser deletion requires web proof")
			}
			command := exec.CommandContext(ctx, "node", filepath.Join(webDir, "node_modules/cypress/bin/cypress"), "run", "--browser", "electron", "--spec", "cypress/e2e/celln-parent-delete-live.cy.ts", "--env", "PROOF_NAMESPACE="+ns.Name+",PROOF_RUN="+run.Name+",PROOF_UID="+string(run.UID))
			command.Dir = webDir
			command.Env = append(os.Environ(), "CYPRESS_API_TOKEN="+apiToken, "CYPRESS_BASE_URL="+server.URL)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("live browser deletion: %v\n%s", err, output)
			}
			t.Log("browser refused mismatched deletion UID and requested original run teardown")
		} else {
			uid := run.UID
			if err := store.Delete(ctx, run, &client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil {
				t.Fatal(err)
			}
		}
		poll(func() bool { return apierrors.IsNotFound(store.Get(ctx, key, &api.AgentRun{})) })
		second := &api.AgentRun{ObjectMeta: metav1.ObjectMeta{GenerateName: "template-reuse-", Namespace: ns.Name}, Spec: *original.Spec.DeepCopy()}
		if err := store.Create(ctx, second); err != nil {
			t.Fatal(err)
		}
		// Cleanup now owns the second run; the first is confirmed finalized.
		run = second
		key = client.ObjectKeyFromObject(run)
		poll(func() bool {
			if err := store.Get(ctx, key, run); err != nil {
				t.Fatal(err)
			}
			if run.Status.Phase == api.AgentRunPhaseFailed {
				t.Fatalf("second parent failed: %+v", run.Status.Conditions)
			}
			p := run.Status.CellnParent
			return run.Status.Phase == api.AgentRunPhaseRunning && meta.IsStatusConditionTrue(run.Status.Conditions, "CellnParentReady") && p != nil && p.InitialTurn != nil && p.InitialTurn.Result != nil
		})
		secondTurn := &api.AgentRunTurn{}
		request("POST", "/api/v1/runs/"+run.Name+"/turns?namespace="+ns.Name, map[string]string{"runUID": string(run.UID), "requestId": "reuse-follow-up", "message": turnInput["message"]}, http.StatusAccepted, secondTurn)
		poll(func() bool {
			if err := store.Get(ctx, client.ObjectKeyFromObject(secondTurn), secondTurn); err != nil {
				t.Fatal(err)
			}
			if err := store.Get(ctx, key, run); err != nil {
				t.Fatal(err)
			}
			return secondTurn.Status.Execution != nil && secondTurn.Status.Execution.Result != nil && run.Status.CellnParent.ActiveTurn == nil
		})
		p := run.Status.CellnParent
		if run.UID == original.UID || p.Binding.Incarnation == original.Status.CellnParent.Binding.Incarnation || p.Binding.LaunchProfile == original.Status.CellnParent.Binding.LaunchProfile || p.AcceptedTurns != 1 {
			t.Fatal("template reuse carried another run identity/accounting")
		}
		secondResults := []cellnparent.TurnResult{}
		for _, execution := range []*api.CellnParentTurnStatus{p.InitialTurn, secondTurn.Status.Execution} {
			if !execution.Result.Succeeded || !strings.Contains(strings.ToLower(execution.Result.Answer), "violet") {
				t.Fatal("second run lost context/result")
			}
			secondResults = append(secondResults, cellnparent.TurnResult{Kind: "completed", Version: "celln.parent-context/v1", TurnID: execution.ID, Succeeded: &execution.Result.Succeeded, Answer: execution.Result.Answer})
		}
		currentConfig, err := os.ReadFile(registrationPath)
		if err != nil || !bytes.Equal(originalConfig, currentConfig) {
			t.Fatal("operator configuration changed for second run")
		}
		raw, err := json.Marshal(map[string]any{"runUID": run.UID, "incarnation": p.Binding.Incarnation, "launchProfile": p.Binding.LaunchProfile, "results": secondResults, "run": run, "turn": secondTurn, "templateUnchanged": true})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(filepath.Dir(os.Getenv("CELLN_INTEROP_RESULT")), "interop-reused.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
		t.Log("unchanged operator template provisioned a second distinct parent and two fresh turns")
	}
	if hold > 0 {
		holdLiveParent(t, ctx, store, server, registrationPath, run, managerDone, hold)
	}
}
