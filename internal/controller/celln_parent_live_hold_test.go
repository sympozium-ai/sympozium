package controller_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnparent"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

func liveParentHoldDuration(t *testing.T) time.Duration {
	t.Helper()
	raw := os.Getenv("CELLN_INTEROP_HOLD_SECONDS")
	if raw == "" {
		return 0
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds < 1 || seconds > 43200 {
		t.Fatal("hold seconds must be 1..43200")
	}
	for _, name := range []string{"CELLN_INTEROP_API_BINARY", "CELLN_INTEROP_CONTROLLER_BINARY", "CELLN_INTEROP_DISPATCHER_BINARY", "CELLN_INTEROP_PROVISION_BINARY", "CELLN_INTEROP_PROXY_BINARY", "CELLN_INTEROP_NATS_BINARY"} {
		if !filepath.IsAbs(os.Getenv(name)) {
			t.Fatalf("hands-on hold requires explicit %s", name)
		}
	}
	if os.Getenv("CELLN_INTEROP_REUSE_TEMPLATE") != "true" || os.Getenv("CELLN_INTEROP_SCOPED_CONTROLLER") != "true" || os.Getenv("CELLN_INTEROP_OWNER_TLS") != "true" {
		t.Fatal("hands-on hold requires template reuse, scoped controller and TLS")
	}
	return time.Duration(seconds) * time.Second
}

func cleanupLiveParentRuns(ctx context.Context, store client.Client, namespace string) bool {
	for ctx.Err() == nil {
		var runs api.AgentRunList
		if err := store.List(ctx, &runs, client.InNamespace(namespace)); err != nil {
			return false
		}
		if len(runs.Items) == 0 {
			return true
		}
		for i := range runs.Items {
			run := &runs.Items[i]
			uid := run.UID
			if err := store.Delete(ctx, run, &client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil && !apierrors.IsNotFound(err) {
				return false
			}
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(100 * time.Millisecond):
		}
	}
	return false
}

// This is an operator-owned development supervisor, not a deployment mode.
// Original proof records remain untouched; a third run owns the hands-on work.
func holdLiveParent(t *testing.T, ctx context.Context, store client.Client, endpoint *liveAPIEndpoint, registrations string, previous *api.AgentRun, managerDone <-chan error, hold time.Duration) {
	t.Helper()
	if !cleanupLiveParentRuns(ctx, store, previous.Namespace) {
		t.Fatal("proof parents did not join before hands-on session")
	}
	var config cellnparent.RegistrationConfig
	raw, err := os.ReadFile(registrations)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.HostTemplates) != 1 || config.LocalProvisioner == nil {
		t.Fatal("one local native template required")
	}
	template := &config.HostTemplates[0]
	lease := int32(max(1800, int(hold/time.Second)+120))
	template.HostLimits.LeaseSeconds = lease
	template.HostLimits.MaxTurns = 12
	template.HostLimits.MaxModelRequests = 36
	template.HostLimits.MaxOutputTokens = 18432
	template.Native.MaxTurns = 12
	template.Native.TotalModelRequests = 36
	template.Native.TotalOutputTokens = 18432
	var parent map[string]json.RawMessage
	if err := json.Unmarshal(template.Native.Parent, &parent); err != nil {
		t.Fatal(err)
	}
	var capabilities map[string]json.RawMessage
	if err := json.Unmarshal(parent["capabilities"], &capabilities); err != nil {
		t.Fatal(err)
	}
	capabilities["timeoutMs"] = json.RawMessage(strconv.FormatInt(int64(lease)*1000, 10))
	parent["capabilities"], err = json.Marshal(capabilities)
	if err != nil {
		t.Fatal(err)
	}
	template.Native.Parent, err = json.Marshal(parent)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	// Same-directory rename makes the operator template update all-or-nothing.
	file, err := os.CreateTemp(filepath.Dir(registrations), ".hands-on-config-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := file.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(file.Name(), registrations); err != nil {
		t.Fatal(err)
	}
	run := &api.AgentRun{TypeMeta: metav1.TypeMeta{APIVersion: "sympozium.ai/v1alpha1", Kind: "AgentRun"}, ObjectMeta: metav1.ObjectMeta{GenerateName: "hands-on-", Namespace: previous.Namespace}, Spec: *previous.Spec.DeepCopy()}
	run.Spec.Enduring = template.HostLimits.DeepCopy()
	run.Spec.Timeout = &metav1.Duration{Duration: time.Duration(lease) * time.Second}
	root := filepath.Dir(os.Getenv("CELLN_INTEROP_RESULT"))
	runYAML, err := yaml.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	yamlPath := filepath.Join(root, "hands-on-run.yaml")
	if err := os.WriteFile(yamlPath, runYAML, 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(ctx, run); err != nil {
		t.Fatal(err)
	}
	readyDeadline := time.Now().Add(60 * time.Second)
	for {
		if err := store.Get(ctx, client.ObjectKeyFromObject(run), run); err != nil {
			t.Fatal(err)
		}
		if run.Status.Phase == api.AgentRunPhaseFailed {
			t.Fatalf("hands-on parent failed: %+v", run.Status.Conditions)
		}
		p := run.Status.CellnParent
		if p != nil && p.InitialTurn != nil && p.InitialTurn.Result != nil && p.InitialTurn.Result.Succeeded && meta.IsStatusConditionTrue(run.Status.Conditions, "CellnParentReady") {
			break
		}
		if time.Now().After(readyDeadline) {
			t.Fatal("hands-on initial turn not ready")
		}
		select {
		case err := <-managerDone:
			t.Fatalf("manager stopped: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	stopPath := filepath.Join(root, "hands-on.stop")
	deadline := time.Now().Add(hold)
	raw, err = json.MarshalIndent(map[string]any{"url": endpoint.URL, "runURL": endpoint.URL + "/runs/" + run.Name, "namespace": run.Namespace, "run": run.Name, "runUID": run.UID, "incarnation": run.Status.CellnParent.Binding.Incarnation, "launchProfile": run.Status.CellnParent.Binding.LaunchProfile, "tokenFile": endpoint.TokenFile, "runYAML": yamlPath, "stopFile": stopPath, "stopAt": deadline.UTC(), "limits": run.Spec.Enduring}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	handoff := filepath.Join(root, "hands-on.json")
	if err := os.WriteFile(handoff, raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("hands-on parent ready; private handoff %s; create %s to join cleanup", handoff, stopPath)
	if os.Getenv("CELLN_INTEROP_BROWSER_CANCEL") == "true" {
		starter := os.Getenv("CELLN_INTEROP_STARTER") == "true"
		webDir := os.Getenv("CELLN_INTEROP_WEB_DIR")
		if webDir == "" {
			t.Fatal("browser cancellation requires the real web UI")
		}
		token, err := os.ReadFile(endpoint.TokenFile)
		if err != nil {
			t.Fatal(err)
		}
		command := exec.CommandContext(ctx, "node", filepath.Join(webDir, "node_modules/cypress/bin/cypress"), "run", "--browser", "electron", "--spec", "cypress/e2e/celln-parent-cancel-live.cy.ts", "--env", "PROOF_NAMESPACE="+run.Namespace+",PROOF_RUN="+run.Name)
		command.Dir = webDir
		command.Env = append(os.Environ(), "CYPRESS_API_TOKEN="+string(token), "CYPRESS_BASE_URL="+endpoint.URL, "CYPRESS_STARTER_TOOLS="+os.Getenv("CELLN_INTEROP_STARTER"))
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("live browser cancellation: %v\n%s", err, output)
		}
		var turns api.AgentRunTurnList
		if err := store.List(ctx, &turns, client.InNamespace(run.Namespace)); err != nil {
			t.Fatal(err)
		}
		if err := store.Get(ctx, client.ObjectKeyFromObject(run), run); err != nil {
			t.Fatal(err)
		}
		expectedTurns := 2
		if starter {
			expectedTurns = 4
		}
		if len(turns.Items) != expectedTurns || run.Status.CellnParent.ActiveTurn != nil {
			t.Fatal("cancellation proof turn count or slot mismatch")
		}
		cancelled, completed := 0, 0
		for _, turn := range turns.Items {
			if turn.Status.Execution == nil || turn.Status.Execution.Result == nil {
				t.Fatal("missing committed result")
			}
			if turn.Spec.CancelRequested && turn.Status.CancelAttempted && !turn.Status.Execution.Result.Succeeded {
				cancelled++
			}
			if !turn.Spec.CancelRequested && turn.Status.Execution.Result.Succeeded {
				completed++
			}
		}
		if cancelled != 1 || completed != expectedTurns-1 {
			t.Fatal("cancellation did not preserve a usable parent")
		}
		raw, err := json.Marshal(map[string]any{"run": run, "turns": turns.Items})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "browser-cancel-proof.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
		t.Log("live UI cancellation committed; same parent completed next borrowed-tool turn")
		return
	}
	for time.Now().Before(deadline) {
		if info, err := os.Lstat(stopPath); err == nil {
			if !info.Mode().IsRegular() {
				t.Fatal("stop signal must be a regular file")
			}
			return
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		select {
		case err := <-managerDone:
			t.Fatalf("manager stopped during hold: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Second):
		}
	}
}
