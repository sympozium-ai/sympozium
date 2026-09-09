package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-logr/logr"
	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/apiserver"
	"github.com/sympozium-ai/sympozium/internal/cellnauthority"
	"github.com/sympozium-ai/sympozium/internal/cellnreview"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Explicitly billable, host-KVM test. The caller pauses/restores only the known
// isolated proof controller. No environment-default cluster is ever selected.
func TestLiveCatalogueHarness(t *testing.T) {
	if os.Getenv("CELLN_LIVE_CATALOGUE") != "1" {
		t.Skip("explicit isolated live catalogue/model proof required")
	}
	path := func(name string) string {
		p := os.Getenv(name)
		if !filepath.IsAbs(p) {
			t.Fatalf("absolute %s required", name)
		}
		return p
	}
	kube := path("CELLN_CONTROLLER_KUBECONFIG")
	fixture := path("CELLN_COMPOSITION_FIXTURE")
	binary := path("CELLN_COMPOSITION_BINARY")
	operatorAdmission := os.Getenv("CELLN_LIVE_OPERATOR_ADMISSION") == "1"
	materializer := ""
	if !operatorAdmission {
		materializer = path("CELLN_ISSUANCE_MATERIALIZER")
	}
	packagePath := path("CELLN_HARNESS_PACKAGE")
	cli := path("CELLN_LIVE_SYMPOZIUM_BINARY")
	automatic := os.Getenv("CELLN_LIVE_AUTOMATIC_ISSUANCE") == "1"
	httpSubmission := os.Getenv("CELLN_LIVE_HTTP_SUBMISSION") == "1"
	browserSubmission := os.Getenv("CELLN_LIVE_BROWSER_SUBMISSION") == "1"
	if browserSubmission {
		httpSubmission = true
	}
	if os.Getenv("CELLN_LIVE_APISERVER_IMAGE") != "" && !browserSubmission {
		t.Fatal("deployed API image requires browser submission; no silent loopback fallback")
	}
	cancelActive := os.Getenv("CELLN_LIVE_CANCEL_ACTIVE") == "1"
	cancelUnissued := os.Getenv("CELLN_LIVE_CANCEL_UNISSUED") == "1"
	restartIssuer := os.Getenv("CELLN_LIVE_RESTART_ISSUER") == "1"
	if restartIssuer && (os.Getenv("CELLN_LIVE_ISSUER_PROCESS") != "1" || cancelActive || cancelUnissued) {
		t.Fatal("issuer restart requires standalone issuer and a successful execution journey")
	}
	browserCancel := os.Getenv("CELLN_LIVE_BROWSER_CANCEL") == "1"
	if browserCancel && (!(cancelUnissued || cancelActive) || !browserSubmission || os.Getenv("CELLN_LIVE_APISERVER_IMAGE") == "") {
		t.Fatal("browser cancellation requires deployed API/browser and a cancellation mode")
	}
	if cancelUnissued && (!automatic || os.Getenv("CELLN_LIVE_CONTROLLER_IMAGE") == "" || cancelActive || os.Getenv("CELLN_LIVE_LOST_RESPONSE") == "1" || os.Getenv("CELLN_LIVE_RESTART_CONTROLLER") == "1") {
		t.Fatal("unissued cancellation requires automatic controller-Pod mode without other fault modes")
	}
	lostResponse := os.Getenv("CELLN_LIVE_LOST_RESPONSE") == "1"
	restartController := os.Getenv("CELLN_LIVE_RESTART_CONTROLLER") == "1"
	controllerImage := os.Getenv("CELLN_LIVE_CONTROLLER_IMAGE")
	if os.Getenv("CELLN_LIVE_NETWORK_PROBE_IMAGE") != "" && (controllerImage == "" || lostResponse || cancelActive || cancelUnissued) {
		t.Fatal("tenant network proof requires the normal controller-Pod journey")
	}
	if controllerImage != "" && (!automatic || os.Getenv("CELLN_LIVE_ISSUER_PROCESS") != "1") {
		t.Fatal("controller Pod proof requires automatic standalone issuance")
	}
	if restartController && !lostResponse {
		t.Fatal("controller restart proof requires an uncertain accepted response")
	}
	if lostResponse && (!automatic || cancelActive) {
		t.Fatal("lost-response proof requires automatic issuance and cannot be combined with active cancellation")
	}
	if cancelActive && ((httpSubmission && !browserCancel) || !automatic) {
		t.Fatal("active cancellation requires automatic submission and explicit browser cancellation for HTTP runs")
	}
	if httpSubmission && !automatic {
		t.Fatal("HTTP submission requires automatic issuance")
	}
	controller := path("CELLN_LIVE_CONTROLLER_BINARY")
	config, err := clientcmd.LoadFromFile(kube)
	must(t, err)
	if config.CurrentContext != "kind-celln-deployed" {
		t.Fatal("only isolated kind-celln-deployed permitted")
	}
	rest, err := clientcmd.NewDefaultClientConfig(*config, &clientcmd.ConfigOverrides{}).ClientConfig()
	must(t, err)
	scheme := runtime.NewScheme()
	must(t, api.AddToScheme(scheme))
	must(t, corev1.AddToScheme(scheme))
	must(t, appsv1.AddToScheme(scheme))
	must(t, batchv1.AddToScheme(scheme))
	must(t, rbacv1.AddToScheme(scheme))
	must(t, networkingv1.AddToScheme(scheme))
	c, err := client.New(rest, client.Options{Scheme: scheme})
	must(t, err)
	lifetime := 6 * time.Minute
	interactive := os.Getenv("CELLN_LIVE_INTERACTIVE") == "1"
	if interactive {
		if !automatic || !browserSubmission || controllerImage == "" || os.Getenv("CELLN_LIVE_APISERVER_IMAGE") == "" || cancelActive || cancelUnissued || lostResponse || restartIssuer {
			t.Fatal("interactive session requires the normal deployed browser/controller journey")
		}
		lifetime = 8*time.Hour + 5*time.Minute // reserve cleanup time after session expiry
	}
	ctx, cancel := context.WithTimeout(context.Background(), lifetime)
	t.Cleanup(cancel)
	var deployment appsv1.Deployment
	must(t, c.Get(ctx, types.NamespacedName{Namespace: "sympozium-system", Name: "harness-proof-controller"}, &deployment))
	if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 0 || deployment.Status.Replicas != 0 || deployment.Status.ObservedGeneration < deployment.Generation {
		t.Fatal("proof controller must be stopped")
	}
	dir := t.TempDir()
	evidence := filepath.Join(dir, "evidence")
	if parent := os.Getenv("CELLN_LIVE_EVIDENCE_PARENT"); parent != "" {
		if !filepath.IsAbs(parent) {
			t.Fatal("absolute evidence parent required")
		}
		evidence, err = os.MkdirTemp(parent, "catalogue-live-")
		must(t, err)
	} else {
		must(t, os.Mkdir(evidence, 0700))
	}
	t.Logf("evidence directory: %s", evidence)
	if interactive {
		startInteractiveBus(t, ctx, dir)
	}
	// Registered before resource/process cleanups, so a finalizer or teardown
	// failure cannot leave an apparent overall passing evidence record.
	t.Cleanup(func() {
		status := "passed"
		if t.Failed() {
			status = "failed"
		}
		writeJSON(t, filepath.Join(evidence, "test-outcome.json"), map[string]any{"status": status, "includesRegisteredCleanup": true})
	})
	credential := os.Getenv("CELLN_LIVE_CREDENTIAL_FILE")
	if source := os.Getenv("CELLN_LIVE_DEEPSEEK_ZSHRC"); source != "" {
		if credential != "" || !filepath.IsAbs(source) {
			t.Fatal("choose one explicit host credential source")
		}
		raw, err := os.ReadFile(source)
		must(t, err)
		if len(raw) > 1<<20 {
			t.Fatal("shell configuration exceeds bound")
		}
		matches := regexp.MustCompile(`(?m)^\s*(?:export\s+)?DEEPSEEK_API_KEY\s*=\s*(['"]?)(sk-[A-Za-z0-9_-]+)(['"]?)\s*(?:#.*)?$`).FindAllSubmatch(raw, -1)
		if len(matches) != 1 || string(matches[0][1]) != string(matches[0][3]) {
			t.Fatal("exactly one literal DeepSeek assignment required; shell configuration is never sourced")
		}
		credential = filepath.Join(dir, "deepseek-key")
		must(t, os.WriteFile(credential, matches[0][2], 0600))
	}
	if !filepath.IsAbs(credential) {
		t.Fatal("absolute host credential file or explicit literal shell configuration required")
	}
	root := filepath.Join(dir, "store")
	must(t, os.CopyFS(root, os.DirFS(fixture)))
	var source catalogue
	readJSON(t, filepath.Join(root, "catalogue.json"), &source)
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "celln-catalogue-proof-", Labels: map[string]string{"sympozium.ai/celln-catalogue-proof": "true"}}}
	must(t, c.Create(ctx, ns))
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		uid := ns.UID
		if err := c.Delete(cleanup, ns, &client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil {
			t.Errorf("namespace cleanup %s: %v", ns.Name, err)
		}
	})
	report, err := populate(ctx, c, ns.Name, source)
	must(t, err)
	frozen := report["frozen"].(*cellnauthority.FrozenSelection)
	l := cellnauthority.Loader{Reader: c, OperatorSource: types.NamespacedName{Namespace: ns.Name, Name: "operator"}, RuntimeSource: types.NamespacedName{Namespace: ns.Name, Name: "runtime"}, AgentSource: types.NamespacedName{Namespace: ns.Name, Name: "agent"}}
	runName := "run"
	var browserURL string
	if httpSubmission {
		// The setup run has never been issued and the controller is stopped.
		// Replace it with a run created by the actual HTTP handler, then freeze
		// that persisted UID/spec. Do not patch HTTP output to fit the fixture.
		var setup api.AgentRun
		must(t, c.Get(ctx, types.NamespacedName{Namespace: ns.Name, Name: runName}, &setup))
		if setup.Status.CellnIssuance != nil || setup.Status.CellnActionID != "" || len(setup.Finalizers) != 0 {
			t.Fatal("setup run unexpectedly active")
		}
		uid := setup.UID
		must(t, c.Delete(ctx, &setup, &client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}))
		var created api.AgentRun
		if browserSubmission {
			browserURL = browserServer(t, ctx, c, ns.Name)
			runBrowser(t, ctx, browserURL, ns.Name, "", setup.Spec.Task.GetPrompt())
			var runs api.AgentRunList
			must(t, c.List(ctx, &runs, client.InNamespace(ns.Name)))
			if len(runs.Items) != 1 {
				t.Fatalf("browser must create exactly one run, got %d", len(runs.Items))
			}
			created = runs.Items[0]
		} else {
			// Loopback HTTP-only mode is not a deployment/auth proof.
			server := httptest.NewServer(apiserver.NewServer(c, nil, nil, logr.Discard()).Handler(nil))
			t.Cleanup(server.Close)
			body, err := json.Marshal(apiserver.CreateRunRequest{AgentRef: setup.Spec.AgentRef, Task: setup.Spec.Task.GetPrompt(), Model: "deepseek-chat", Provider: "deepseek", Backend: "celln", Timeout: "180s", CellnSelection: setup.Spec.CellnSelection})
			must(t, err)
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/api/v1/runs?namespace="+url.QueryEscape(ns.Name), bytes.NewReader(body))
			must(t, err)
			req.Header.Set("Content-Type", "application/json")
			response, err := server.Client().Do(req)
			must(t, err)
			raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
			response.Body.Close()
			must(t, err)
			if response.StatusCode != http.StatusCreated {
				t.Fatalf("HTTP create failed: %d %s", response.StatusCode, raw)
			}
			must(t, json.Unmarshal(raw, &created))
		}
		if created.Namespace != ns.Name || created.UID == "" || created.UID == uid || created.Name == "" {
			t.Fatal("HTTP did not return a new persisted run")
		}
		runName = created.Name
		selected := make([]cellnauthority.Selection, 0, len(setup.Spec.CellnSelection.ToolRefs))
		for _, ref := range setup.Spec.CellnSelection.ToolRefs {
			selected = append(selected, cellnauthority.Selection{Name: ref.Name, Revision: ref.Revision})
		}
		frozen, err = l.FreezeRun(ctx, client.ObjectKeyFromObject(&created), selected, 33554432)
		must(t, err)
		writeJSON(t, filepath.Join(evidence, "http-created-run.json"), created)
		t.Logf("actual HTTP submission persisted run %s/%s UID=%s; no Kubernetes patch applied", created.Namespace, created.Name, created.UID)
	}
	ml := cellnauthority.ModelLoader{Selection: l, Source: types.NamespacedName{Namespace: ns.Name, Name: "model"}}
	o := cellnreview.ComposeOptions{Binary: binary, PolicyRoot: root, KeyFile: filepath.Join(root, "public-fixture-seed"), OutputDir: filepath.Join(dir, "composed")}
	composition, err := cellnreview.Compose(ctx, l, *frozen, o)
	must(t, err)
	var artifacts cellnauthority.ExecutionArtifacts
	if operatorAdmission {
		artifacts = admitLiveCandidate(t, ctx, binary, root, o.OutputDir, packagePath, evidence)
	} else {
		must(t, json.Unmarshal(command(t, ctx, nil, materializer, root, o.OutputDir, packagePath), &artifacts))
	}
	var signed struct {
		Publisher string `json:"publisher"`
	}
	readJSON(t, filepath.Join(o.OutputDir, "signed-closure.json"), &signed)
	// Only a host mapping references the credential; no Kubernetes Secret, CLI
	// argument containing the key, environment injection or guest copy is used.
	writeJSON(t, filepath.Join(root, "model-credentials.json"), map[string]any{"apiVersion": "sympozium.ai/celln-host-credentials-v1", "profiles": map[string]string{"catalogue-proof": credential}})
	issuerProcess := os.Getenv("CELLN_LIVE_ISSUER_PROCESS") == "1"
	var issuerURL, issuerToken, issuerCA string
	var restartIssuerProcess func()
	if issuerProcess {
		issuerURL, issuerToken, issuerCA, restartIssuerProcess = liveIssuerProcess(t, ctx, dir, evidence, cli, kube, binary, root, signed.Publisher, ns.Name)
	} else {
		managed, err := cellnreview.NewManagedIssuer(cellnreview.IssueOptions{Binary: binary, PolicyRoot: root, ComposerPublisher: signed.Publisher, ProfileLifetime: 5 * time.Minute}, map[types.NamespacedName]cellnauthority.ModelLoader{{Namespace: ns.Name, Name: "agent"}: ml}, time.Second)
		must(t, err)
		issuerURL, issuerToken, issuerCA = liveIssuer(t, ctx, dir, managed)
	}
	backendToken, routerToken := filepath.Join(dir, "backend-token"), filepath.Join(dir, "router-token")
	must(t, os.WriteFile(backendToken, freshProofToken(t), 0600))
	must(t, os.WriteFile(routerToken, freshProofToken(t), 0600))
	backendAddr, routerAddr := freeAddress(t), freeAddress(t)
	backend := "http://" + backendAddr
	startProcess(t, ctx, nil, binary, "--root", root, "dispatcher", "--listen", backendAddr, "--token-file", backendToken, "--node-name", "catalogue-live-proof", "--mote-store", filepath.Join(root, "motes"), "--tool-store", filepath.Join(root, "tools"), "--allow-egress-host", "api.deepseek.com", "--egress-slots", "1")
	waitTCP(t, backendAddr)
	routeArgs := []string{"route", "--listen", routerAddr, "--backends", backend, "--token-file", backendToken, "--client-token-file", routerToken, "--ownership-dir", filepath.Join(dir, "ownership")}
	capabilityToken := filepath.Join(dir, "capability-token")
	if interactive {
		must(t, os.WriteFile(capabilityToken, freshProofToken(t), 0600))
		routeArgs = append(routeArgs, "--capability-token-file", capabilityToken)
	}
	startProcess(t, ctx, nil, binary, routeArgs...)
	waitTCP(t, routerAddr)
	target, err := url.Parse("http://" + routerAddr)
	must(t, err)
	proxy := httputil.NewSingleHostReverseProxy(target)
	transport := &http.Transport{Proxy: nil}
	proxy.Transport = transport
	t.Cleanup(transport.CloseIdleConnections)
	var loss *dispatchResponseLoss
	var routerHandler http.Handler = proxy
	var executionPosts atomic.Int32
	if cancelUnissued {
		routerHandler = http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			if request.Method == http.MethodPost && request.URL.Path == "/v1/executions" {
				executionPosts.Add(1)
			}
			proxy.ServeHTTP(w, request)
		})
	}
	if lostResponse {
		loss = newDispatchResponseLoss(proxy)
		routerHandler = loss
		t.Cleanup(func() { loss.released.Store(true) })
	}
	router := httptest.NewUnstartedServer(routerHandler)
	if controllerImage != "" {
		must(t, router.Listener.Close())
		router.Listener, err = net.Listen("tcp", net.JoinHostPort(proofServiceHost(t), "0"))
		must(t, err)
	}
	router.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{freshProofCertificate(t, proofServiceHost(t))}}
	router.StartTLS()
	t.Cleanup(router.Close)
	routerCA := filepath.Join(dir, "router-ca.pem")
	must(t, os.WriteFile(routerCA, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: router.Certificate().Raw}), 0600))
	if interactive {
		configureInteractiveDiscovery(t, ctx, c, ns.Name, router.URL, routerCA, capabilityToken)
		browserURL = forwardBrowserServer(t, ctx, ns.Name, os.Getenv("CELLN_LIVE_APISERVER_IMAGE"))
	}
	// Run separately from response-loss/cancellation modes, whose exact POST
	// counts describe execution attempts rather than authentication probes.
	if !lostResponse && !cancelActive && !cancelUnissued {
		if image := os.Getenv("CELLN_LIVE_NETWORK_PROBE_IMAGE"); image != "" {
			proveTenantHostNetwork(t, ctx, c, image, issuerURL, router.URL, evidence)
		}
		approval, err := ml.Resolve(ctx, *frozen)
		must(t, err)
		proveLiveServiceCredentialSeparation(t, ctx, issuerURL, issuerCA, issuerToken, router.URL, routerCA, routerToken, backendToken, evidence, root, filepath.Join(dir, "ownership"), cellnreview.IssuerRequest{APIVersion: "sympozium.ai/celln-issuer-request-v1", Frozen: *frozen, Approval: *approval, Artifacts: artifacts})
	}
	args := []string{"--kubeconfig", kube, "--namespace", ns.Name, "celln-tool", "issue-run", "agent", "--run", "run", "--grant-namespace", ns.Name, "--operator-grants", "operator", "--runtime-grants", "runtime", "--agent-grants", "agent", "--model-policy", "model", "--execution-mote", artifacts.Mote.Hash, "--execution-closure", artifacts.Closure.Hash, "--issuer-url", issuerURL, "--issuer-token-file", issuerToken, "--issuer-ca-file", issuerCA, "--router-url", router.URL, "--backend", backend}
	for _, tool := range source.Tools {
		args = append(args, "--tool", tool.Name+"@"+tool.Spec.Revision)
	}
	var issuedReport struct {
		IssuancePersisted    bool `json:"issuancePersisted"`
		ControllerMayExecute bool `json:"controllerMayExecute"`
	}
	if !automatic {
		must(t, json.Unmarshal(command(t, ctx, nil, cli, args...), &issuedReport))
		if !issuedReport.IssuancePersisted || !issuedReport.ControllerMayExecute {
			t.Fatal("CLI did not acknowledge durable execution hand-off")
		}
	}
	var run api.AgentRun
	key := types.NamespacedName{Namespace: ns.Name, Name: runName}
	must(t, c.Get(ctx, key, &run))
	if !automatic && (run.Status.CellnIssuance == nil || run.Status.CellnIssuance.Phase != "Issued" || run.Status.CellnActionID != "") {
		t.Fatal("issuance did not precede dispatch")
	}
	if automatic && run.Status.CellnIssuance != nil {
		t.Fatal("automatic proof was manually issued")
	}
	configPath := filepath.Join(dir, "controller.json")
	var registrations []cellnreview.RegisteredComposition
	if automatic && !cancelUnissued {
		registrations = []cellnreview.RegisteredComposition{{Sources: frozen.Prepared.Composition.Sources, ImageBytes: frozen.Prepared.Composition.ImageBytes, Artifacts: artifacts}}
	}
	writeJSON(t, configPath, cellnreview.ControllerDispatchConfig{APIVersion: "sympozium.ai/celln-catalogue-controller-v1", Bindings: []cellnreview.ControllerDispatchBinding{{Compositions: registrations, Agent: types.NamespacedName{Namespace: ns.Name, Name: "agent"}, Issuer: cellnreview.ControllerEndpoint{URL: issuerURL, TokenFile: issuerToken, CAFile: issuerCA}, Router: cellnreview.ControllerEndpoint{URL: router.URL, TokenFile: routerToken, CAFile: routerCA}, Backend: backend, OperatorSource: l.OperatorSource, RuntimeSource: l.RuntimeSource, AgentSource: l.AgentSource, ModelSource: ml.Source}}})
	env := cleanControllerEnv(kube, configPath)
	var restart func()
	// Registered after the controller process cleanup: remove the run while
	// cancellation/finalizer reconciliation is still available, even on failure.
	cleanupRun := func() {
		cleanup, stop := context.WithTimeout(context.Background(), 50*time.Second)
		defer stop()
		if interactive {
			cleanupInteractiveRuns(t, cleanup, c, ns.Name, evidence)
			return
		}
		if err := c.Delete(cleanup, &api.AgentRun{ObjectMeta: metav1.ObjectMeta{Namespace: ns.Name, Name: runName}}); client.IgnoreNotFound(err) != nil {
			t.Errorf("run cleanup: %v", err)
			return
		}
		for cleanup.Err() == nil {
			var current api.AgentRun
			if err := c.Get(cleanup, key, &current); err != nil {
				if client.IgnoreNotFound(err) == nil {
					return
				}
				t.Errorf("cleanup lookup: %v", err)
				return
			}
			time.Sleep(time.Second)
		}
		t.Error("run finalizer did not complete before controller shutdown")
	}
	if controllerImage != "" {
		t.Cleanup(cleanupRun)
		deployCatalogueController(t, ctx, c, ns.Name, controllerImage, configPath)
		restart = func() { restartCatalogueControllerPod(t, ctx, c, ns.Name, evidence) }
	} else {
		restart = startProcess(t, ctx, env, controller, "--metrics-bind-address=0", "--health-probe-bind-address=0", "--max-run-history=100", "--watch-namespace="+ns.Name)
		t.Cleanup(cleanupRun)
	}
	deadline := time.Now().Add(200 * time.Second)
	var browserDelete func()
	if browserCancel {
		browserDelete = func() { runBrowserAction(t, ctx, browserURL, ns.Name, runName, "", "cancel") }
	}
	if cancelUnissued {
		proveUnissuedCatalogueCancellation(t, ctx, c, key, run.UID, &executionPosts, evidence, browserDelete)
		return
	}
	if cancelActive {
		proveActiveCatalogueCancellation(t, ctx, c, key, run.UID, root, backend, backendToken, router, routerToken, evidence, browserDelete)
		return
	}
	var recoveryIdentity *api.AgentRun
	if lostResponse {
		var beforeRestore func()
		if restartController {
			beforeRestore = restart
		}
		recoveryIdentity = observeLostCatalogueResponse(t, ctx, c, key, loss, evidence, beforeRestore)
	}
	for time.Now().Before(deadline) {
		must(t, c.Get(ctx, key, &run))
		if run.Status.Phase == api.AgentRunPhaseSucceeded || run.Status.Phase == api.AgentRunPhaseFailed {
			break
		}
		time.Sleep(time.Second)
	}
	if run.Status.Phase != api.AgentRunPhaseSucceeded {
		writeJSON(t, filepath.Join(evidence, "agentrun.json"), run)
		t.Fatalf("catalogue run did not succeed: phase=%s error=%s", run.Status.Phase, run.Status.Error)
	}
	validateLiveResult(t, run)
	if lostResponse {
		if run.UID != recoveryIdentity.UID || run.Status.CellnActionID != recoveryIdentity.Status.CellnActionID || run.Status.CellnRequest != recoveryIdentity.Status.CellnRequest || loss.posts.Load() != 1 {
			t.Fatal("lost response recovery changed execution identity or submitted a replacement")
		}
		writeJSON(t, filepath.Join(evidence, "lost-response-recovery.json"), map[string]any{"status": "execution-checks-passed", "scope": "actual controller with injected TLS proxy response loss; surviving issuer/router/dispatcher; final cleanup outcome is in test-outcome.json", "controllerPodImage": controllerImage, "controllerRestartedWhileUncertain": restartController, "runUID": run.UID, "action": run.Status.CellnActionID, "acceptedResponseLost": loss.dropped.Load(), "uncertainStatusObserved": true, "executionPosts": loss.posts.Load(), "savedRequestUnchanged": true, "phase": run.Status.Phase})
	}
	if browserSubmission {
		runBrowser(t, ctx, browserURL, ns.Name, runName, "")
	}
	if run.Status.CellnIssuance == nil || run.Status.CellnIssuance.Phase != "Issued" {
		t.Fatal("terminal run lacks committed issuance")
	}
	var jobs batchv1.JobList
	must(t, c.List(ctx, &jobs, client.InNamespace(ns.Name)))
	if len(jobs.Items) != 0 {
		t.Fatal("unexpected workload Jobs")
	}
	var audit struct {
		Receipt   json.RawMessage `json:"receipt"`
		Execution struct {
			ModelGrant string `json:"modelGrant"`
			Broker     struct {
				Requests int `json:"requests"`
			} `json:"broker"`
		} `json:"execution"`
		Events []struct {
			Phase string `json:"phase"`
		} `json:"events"`
	}
	auditBytes := authenticatedGet(t, ctx, router.Client(), router.URL+"/v1/executions/"+run.Status.CellnActionID+"/audit", routerToken)
	must(t, json.Unmarshal(auditBytes, &audit))
	dissolved := false
	for _, event := range audit.Events {
		if event.Phase == "Dissolved" {
			dissolved = true
		}
	}
	var request struct {
		Harness struct {
			ModelGrant api.CellnImmutableRef `json:"modelGrant"`
		} `json:"harness"`
	}
	must(t, json.Unmarshal([]byte(run.Status.CellnRequest), &request))
	if audit.Execution.Broker.Requests != 3 || audit.Execution.ModelGrant != request.Harness.ModelGrant.Hash || !dissolved || !sameJSON(audit.Receipt, []byte(run.Status.CellnReceipt)) {
		t.Fatal("audit does not correlate model budget, grant, receipt and dissolution")
	}
	var node struct {
		Node struct {
			LiveCells int `json:"live_cells"`
		} `json:"node"`
	}
	must(t, json.Unmarshal(authenticatedGet(t, ctx, &http.Client{Timeout: 10 * time.Second}, backend+"/v1/node", backendToken), &node))
	if node.Node.LiveCells != 0 {
		t.Fatal("live cells remain after terminal result")
	}
	writeJSON(t, filepath.Join(evidence, "agentrun.json"), run)
	must(t, os.WriteFile(filepath.Join(evidence, "audit.json"), auditBytes, 0600))
	writeJSON(t, filepath.Join(evidence, "node.json"), node)
	writeJSON(t, filepath.Join(evidence, "jobs.json"), jobs)
	var issued cellnreview.IssuedSelection
	must(t, json.Unmarshal([]byte(run.Status.CellnIssuance.Result), &issued))
	profilePath := filepath.Join(root, "trusted-model-profiles", issued.Profile+".json")
	if interactive {
		holdInteractiveSession(t, ctx, c, ns.Name, browserURL, evidence, run)
		return
	}
	if restartIssuer {
		journalPath := filepath.Join(root, "sympozium-issuer-journal", issued.Profile+".json")
		profileBefore, err := os.ReadFile(profilePath)
		must(t, err)
		journalBefore, err := os.ReadFile(journalPath)
		must(t, err)
		restartIssuerProcess()
		profileAfter, err := os.ReadFile(profilePath)
		must(t, err)
		journalAfter, err := os.ReadFile(journalPath)
		must(t, err)
		if !bytes.Equal(profileBefore, profileAfter) || !bytes.Equal(journalBefore, journalAfter) {
			t.Fatal("issuer restart changed existing profile or journal; renewal/replacement is not recovery")
		}
		writeJSON(t, filepath.Join(evidence, "issuer-restart.json"), map[string]any{"status": "recovery-checks-passed", "scope": "actual issuer process killed/reaped/restarted after successful execution; same boot and surviving controller/router/dispatcher; not systemd installation or host reboot", "runUID": run.UID, "action": run.Status.CellnActionID, "authenticatedGateReopened": true, "profileBytesUnchanged": true, "journalBytesUnchanged": true, "profileSHA256": fmt.Sprintf("%x", sha256.Sum256(profileAfter)), "journalSHA256": fmt.Sprintf("%x", sha256.Sum256(journalAfter)), "withdrawalAndCleanupOutcome": "subsequent checks and test-outcome.json"})
	}
	// Current approval withdrawal must not prevent retrieving the existing
	// owner, but must remove host admission before any new use.
	must(t, c.Delete(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: ns.Name, Name: "model"}}))
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); {
		if _, err := os.Stat(profilePath); os.IsNotExist(err) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if _, err := os.Stat(profilePath); !os.IsNotExist(err) {
		t.Fatal("model approval withdrawal did not remove profile")
	}
	withdrawnRequest := filepath.Join(dir, "withdrawn-request.json")
	must(t, os.WriteFile(withdrawnRequest, issued.Request, 0600))
	refusal := exec.CommandContext(ctx, binary, "--root", root, "harness-grant", withdrawnRequest, "--profile", issued.Profile)
	if out, err := refusal.CombinedOutput(); err == nil {
		t.Fatalf("withdrawn host profile still issued a grant: %s", out)
	}
	routerClient, err := cellnreview.NewRouterClient(router.URL, routerToken, routerCA)
	must(t, err)
	defer routerClient.CloseIdleConnections()
	record, err := routerClient.Lookup(ctx, cellnreview.DispatchRoute{RouterURL: router.URL, Backend: backend}, run.Status.CellnActionID)
	must(t, err)
	if record.Phase != "Succeeded" || string(record.Receipt) != run.Status.CellnReceipt { // Compare JSON semantics, not serialization order.
		var a, b any
		must(t, json.Unmarshal(record.Receipt, &a))
		must(t, json.Unmarshal([]byte(run.Status.CellnReceipt), &b))
		x, _ := json.Marshal(a)
		y, _ := json.Marshal(b)
		if record.Phase != "Succeeded" || string(x) != string(y) {
			t.Fatal("owner receipt changed after withdrawal")
		}
	}
	writeJSON(t, filepath.Join(evidence, "summary.json"), map[string]any{"status": "execution-checks-passed", "scope": "actual controller/issuer/router/KVM with isolated Kind API and real DeepSeek; not production qualification; final cleanup outcome is in test-outcome.json", "controllerPodImage": controllerImage, "standaloneIssuer": issuerProcess, "deployedBrowserAPIImage": os.Getenv("CELLN_LIVE_APISERVER_IMAGE"), "browserSubmission": browserSubmission, "operatorAdmission": operatorAdmission, "automaticRegisteredIssuance": automatic, "issuanceCLIUsed": !automatic, "namespace": ns.Name, "runUID": run.UID, "action": run.Status.CellnActionID, "closure": composition.Closure, "brokerRequests": 3, "toolCalls": 2, "jobs": 0, "liveCells": 0, "modelPolicyWithdrawn": true, "hostReissuanceRefused": true, "ownerReceiptRetained": true})
	t.Logf("PASS actual Kind named catalogue -> signed composition -> TLS issuer -> durable status -> configured controller -> TLS pinned router -> KVM -> DeepSeek uppercase and length -> terminal receipt -> model-policy withdrawal -> retained owner receipt; automaticRegisteredIssuance=%t namespace=%s runUID=%s action=%s closure=%s", automatic, ns.Name, run.UID, run.Status.CellnActionID, composition.Closure)
}

func sameJSON(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	p, _ := json.Marshal(x)
	q, _ := json.Marshal(y)
	return string(p) == string(q)
}
func authenticatedGet(t *testing.T, ctx context.Context, c *http.Client, url, tokenPath string) []byte {
	t.Helper()
	token, err := os.ReadFile(tokenPath)
	must(t, err)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	must(t, err)
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	res, err := c.Do(req)
	must(t, err)
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("proof observation HTTP %d", res.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1048577))
	must(t, err)
	if len(raw) > 1048576 {
		t.Fatal("observation exceeds bound")
	}
	return raw
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func readJSON(t *testing.T, path string, out any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	must(t, err)
	must(t, json.Unmarshal(raw, out))
}
func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	must(t, err)
	must(t, os.WriteFile(path, raw, 0600))
}
func command(t *testing.T, ctx context.Context, env []string, binary string, args ...string) []byte {
	t.Helper()
	cmd := exec.CommandContext(ctx, binary, args...)
	if env != nil {
		cmd.Env = env
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s failed: %v: %s", filepath.Base(binary), err, out)
	}
	return out
}
func freeAddress(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, err)
	addr := l.Addr().String()
	must(t, l.Close())
	return addr
}
func waitTCP(t *testing.T, addr string) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); {
		c, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("listener did not start: " + addr)
}

// The returned restart function kills and reaps the owned process before
// starting a new one with the same configuration. Its original cleanup stays
// registered, so restarting a controller cannot reorder finalizer cleanup.
func startProcess(t *testing.T, ctx context.Context, env []string, binary string, args ...string) func() {
	t.Helper()
	log, err := os.CreateTemp(t.TempDir(), "process.log")
	must(t, err)
	var cancel context.CancelFunc
	var done chan error
	var pid int
	start := func() {
		child, stop := context.WithCancel(ctx)
		cancel = stop
		cmd := exec.CommandContext(child, binary, args...)
		if env != nil {
			cmd.Env = env
		}
		cmd.Stdout, cmd.Stderr = log, log
		if err := cmd.Start(); err != nil {
			cancel()
			t.Fatal(err)
		}
		pid = cmd.Process.Pid
		done = make(chan error, 1)
		completion := done
		go func() { completion <- cmd.Wait() }()
	}
	stop := func() {
		if done == nil {
			return
		}
		cancel()
		select {
		case <-done:
			done = nil
		case <-time.After(10 * time.Second):
			t.Fatal("owned process did not stop; refusing overlapping restart")
		}
	}
	start()
	t.Cleanup(func() {
		stop()
		_ = log.Close()
		if t.Failed() {
			raw, _ := os.ReadFile(log.Name())
			if len(raw) > 12000 {
				raw = raw[len(raw)-12000:]
			}
			t.Logf("%s log: %s", filepath.Base(binary), raw)
		}
	})
	return func() {
		previousPID := pid
		stop()
		start()
		t.Logf("restarted owned %s process: oldPID=%d newPID=%d", filepath.Base(binary), previousPID, pid)
	}
}
func cleanControllerEnv(kube, config string) []string {
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "CELLN_") || key == "KUBECONFIG" || key == "NATS_URL" || key == "AGENT_SANDBOX_ENABLED" || key == "OTEL_EXPORTER_OTLP_ENDPOINT" || key == "SYMPOZIUM_PRICING_CONFIGMAP" {
			continue
		}
		env = append(env, entry)
	}
	return append(env, "KUBECONFIG="+kube, "NATS_URL=", "AGENT_SANDBOX_ENABLED=false", "CELLN_HARNESS_ENABLED=true", "CELLN_CATALOGUE_CONFIG="+config)
}
func issuerTLSFiles(t *testing.T, dir string) (string, string, string) {
	t.Helper()
	cert := freshProofCertificate(t, proofServiceHost(t))
	key, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	must(t, err)
	token, ca, private := filepath.Join(dir, "issuer-token"), filepath.Join(dir, "issuer-ca.pem"), filepath.Join(dir, "issuer-key.pem")
	must(t, os.WriteFile(token, freshProofToken(t), 0600))
	must(t, os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}), 0600))
	must(t, os.WriteFile(private, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0600))
	return token, ca, private
}
func liveIssuer(t *testing.T, ctx context.Context, dir string, m *cellnreview.ManagedIssuer) (string, string, string) {
	t.Helper()
	token, ca, private := issuerTLSFiles(t, dir)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, err)
	child, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- cellnreview.ServeIssuer(child, l, m, token, ca, private) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("issuer shutdown: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("issuer did not stop")
		}
	})
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); {
		ready, _ := m.Status()
		if ready {
			return "https://" + l.Addr().String(), token, ca
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("issuer provisioning gate did not open")
	return "", "", ""
}
func validateLiveResult(t *testing.T, run api.AgentRun) {
	t.Helper()
	var calls []map[string]any
	models := 0
	completed := false
	for _, line := range strings.Split(run.Status.Result, "\n") {
		if !strings.HasPrefix(line, "CELLN_HARNESS_EVENT ") {
			continue
		}
		var event map[string]any
		must(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "CELLN_HARNESS_EVENT ")), &event))
		switch event["type"] {
		case "model":
			models++
		case "tool":
			calls = append(calls, event)
		case "completed":
			completed = event["answer"] == "CELLN has length 5"
		}
	}
	if models != 3 || len(calls) != 2 || !completed {
		t.Fatalf("unexpected model/tool event counts or answer: models=%d tools=%d completed=%t", models, len(calls), completed)
	}
	want := []struct {
		name   string
		result string
	}{{"uppercase", `{"text":"CELLN"}`}, {"length", `{"length":5}`}}
	for i, w := range want {
		raw, err := json.Marshal(calls[i]["result"])
		must(t, err)
		if calls[i]["name"] != w.name || string(raw) != w.result {
			t.Fatal(fmt.Sprintf("tool %d did not return expected output", i))
		}
	}
	if run.Status.CellnReceipt == "" || run.Status.CellnActionID == "" {
		t.Fatal("missing correlated receipt/identity")
	}
}
