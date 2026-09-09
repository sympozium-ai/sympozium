package cellnparent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnauthority"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestRegisteredParentBindsLiveSelectionWithoutWidening(t *testing.T) {
	for _, mode := range []string{"valid", "narrower", "lease", "turns", "requests", "tokens", "model", "persona", "selection", "relative-token", "changed-run", "withdrawn-grant", "pod-env", "required-tool-mismatch"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			agent := &api.Agent{ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "tenant", UID: "agent-uid", Generation: 1}, Spec: api.AgentSpec{RuntimeRef: "worker"}}
			rt := &api.AgentRuntime{ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "tenant", UID: "runtime-uid", Generation: 1}, Spec: api.AgentRuntimeSpec{Celln: &api.AgentRuntimeCellnProfile{ContractVersion: "celln.json-tools/v1"}}}
			run := &api.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "run", Namespace: "tenant", UID: "run-uid", Generation: 1}, Spec: api.AgentRunSpec{AgentRef: "agent", Backend: "celln", ExecutionLifecycle: "enduring", Task: api.NewStringTask("remember violet"), Model: api.ModelSpec{Provider: "deepseek", Model: "deepseek-chat"}, CellnSelection: &api.CellnCatalogueSelection{ToolRefs: []api.CellnCatalogueToolRef{}}, Enduring: &api.EnduringRunSpec{LeaseSeconds: 180, MaxTurns: 4, MaxModelRequests: 12, MaxOutputTokens: 4096}}}
			if mode == "pod-env" {
				run.Spec.Env = map[string]string{"UNSUPPORTED": "value"}
			}
			agentID, _ := cellnauthority.IdentifySubject("Agent", agent.ObjectMeta, agent.Spec)
			runtimeID, _ := cellnauthority.IdentifySubject("AgentRuntime", rt.ObjectMeta, rt.Spec)
			objects := []client.Object{agent, rt, run}
			for _, layer := range []string{"operator", "runtime", "agent"} {
				raw, err := json.Marshal(cellnauthority.GrantDocument{APIVersion: "sympozium.ai/celln-grants-v1", Layer: layer, Agent: agentID, Runtime: runtimeID, Grants: []cellnauthority.Grant{}})
				if err != nil {
					t.Fatal(err)
				}
				objects = append(objects, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: layer, Namespace: "operator", UID: types.UID(layer + "-uid")}, Data: map[string]string{"grants.json": string(raw)}})
			}
			scheme := runtime.NewScheme()
			if err := api.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			store := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
			loader := cellnauthority.Loader{Reader: store, OperatorSource: types.NamespacedName{Namespace: "operator", Name: "operator"}, RuntimeSource: types.NamespacedName{Namespace: "operator", Name: "runtime"}, AgentSource: types.NamespacedName{Namespace: "operator", Name: "agent"}}
			frozen, err := loader.FreezeParentRun(ctx, client.ObjectKeyFromObject(run))
			if err != nil {
				t.Fatal(err)
			}
			intent, intentErr := PrepareProvisionIntent(ctx, loader, client.ObjectKeyFromObject(run))
			if (intentErr == nil) != (mode != "pod-env") {
				t.Fatalf("provision intent: %v", intentErr)
			}
			if intent != nil {
				if mode == "valid" {
					testHostProvisionPlan(t, ctx, loader, *intent)
				}
				for _, phase := range []api.AgentRunPhase{api.AgentRunPhaseRunning, api.AgentRunPhaseSucceeded, api.AgentRunPhaseFailed} {
					blocked := loader
					blocked.Reader = provisionStatusReader{Reader: store, phase: phase}
					if _, err := PrepareProvisionIntent(ctx, blocked, client.ObjectKeyFromObject(run)); err == nil {
						t.Fatalf("provisioned run in phase %s", phase)
					}
				}
				blocked := loader
				blocked.Reader = provisionStatusReader{Reader: store, bound: true}
				if _, err := PrepareProvisionIntent(ctx, blocked, client.ObjectKeyFromObject(run)); err == nil {
					t.Fatal("provisioned an already-bound run")
				}
				before, err := intent.Digest()
				if err != nil || intent.Revalidate(ctx, loader) != nil {
					t.Fatalf("fresh provision intent: %v", err)
				}
				changed := *intent
				changed.Spec = *intent.Spec.DeepCopy()
				changed.Spec.Task = api.NewStringTask("forged task")
				after, _ := changed.Digest()
				if before == after || changed.Revalidate(ctx, loader) == nil {
					t.Fatal("modified provision intent accepted")
				}
				fresh, err := PrepareProvisionIntent(ctx, loader, client.ObjectKeyFromObject(run))
				if err != nil {
					t.Fatal(err)
				}
				retry, _ := fresh.Digest()
				if retry != before {
					t.Fatal("unchanged intent digest drifted")
				}
			}
			digest, err := ParentSelectionDigest(frozen.Snapshot)
			if err != nil {
				t.Fatal(err)
			}
			hash := "blake3:" + strings.Repeat("a", 64)
			registration := ParentLaunchRegistration{APIVersion: "sympozium.ai/celln-parent-registration-v1", SelectionSHA256: digest, Target: "https://owner.example", Principal: "tenant", LaunchProfile: hash, Incarnation: hash, TokenFile: "/operator/token", Model: run.Spec.Model, HostLimits: *run.Spec.Enduring}
			switch mode {
			case "required-tool-mismatch":
				registration.HostLimits.RequireToolCall = true
			case "narrower":
				registration.HostLimits.MaxTurns = 2
			case "lease":
				registration.HostLimits.LeaseSeconds++
			case "turns":
				registration.HostLimits.MaxTurns++
			case "requests":
				registration.HostLimits.MaxModelRequests++
			case "tokens":
				registration.HostLimits.MaxOutputTokens++
			case "model":
				registration.Model.Model = "another-model"
			case "persona":
				registration.SystemPrompt = "different persona"
			case "selection":
				registration.SelectionSHA256 = "sha256:" + strings.Repeat("0", 64)
			case "relative-token":
				registration.TokenFile = "token"
			case "changed-run":
				if err := store.Get(ctx, client.ObjectKeyFromObject(run), run); err != nil {
					t.Fatal(err)
				}
				run.Spec.Task = api.NewStringTask("changed")
				if err := store.Update(ctx, run); err != nil {
					t.Fatal(err)
				}
			case "withdrawn-grant":
				if err := store.Delete(ctx, objects[3]); err != nil {
					t.Fatal(err)
				}
			}
			bound, err := BindRegisteredParent(ctx, loader, *frozen, registration)
			if intent != nil && (mode == "changed-run" || mode == "withdrawn-grant") && intent.Revalidate(ctx, loader) == nil {
				t.Fatal("stale provision intent accepted")
			}
			want := mode == "valid" || mode == "narrower"
			if (err == nil) != want {
				t.Fatalf("%s: %v", mode, err)
			}
			if want && (bound.Binding.RunUID != string(run.UID) || bound.Binding.SpecSHA256 != frozen.Run.SpecSHA256 || bound.Binding.Incarnation != hash) {
				t.Fatal("run/launch identity changed")
			}
			if want {
				dispatchConfig := RegistrationConfig{APIVersion: "sympozium.ai/celln-parent-registrations-v1", Journal: t.TempDir(), Approvals: t.TempDir(), OperatorSource: loader.OperatorSource, RuntimeSource: loader.RuntimeSource, AgentSource: loader.AgentSource, Registrations: []ParentLaunchRegistration{registration}}
				dispatchPath := filepath.Join(t.TempDir(), "registrations.json")
				writeConfig := func() {
					t.Helper()
					raw, err := json.Marshal(dispatchConfig)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(dispatchPath, raw, 0600); err != nil {
						t.Fatal(err)
					}
				}
				writeConfig()
				dispatcher, err := LoadRegistrationDispatcher(dispatchPath, dispatchConfig.Approvals, store)
				if err != nil {
					t.Fatal(err)
				}
				for range 2 {
					if err := dispatcher.Admit(ctx, client.ObjectKeyFromObject(run)); err != nil {
						t.Fatal(err)
					}
				}
				dispatchConfig.Registrations = nil
				writeConfig()
				if err := dispatcher.Admit(ctx, client.ObjectKeyFromObject(run)); err == nil {
					t.Fatal("withdrawn registration ignored")
				}
				dispatchConfig.Registrations = []ParentLaunchRegistration{registration}
				dispatchConfig.Journal = t.TempDir()
				writeConfig()
				if err := dispatcher.Admit(ctx, client.ObjectKeyFromObject(run)); err == nil {
					t.Fatal("journal replacement accepted")
				}
				selectionJournal := t.TempDir()
				selectionApprovals := t.TempDir()
				key := client.ObjectKeyFromObject(run)
				unmatched := registration
				unmatched.SelectionSHA256 = "sha256:" + strings.Repeat("0", 64)
				for _, entries := range [][]ParentLaunchRegistration{nil, {unmatched}, {registration, registration}} {
					if _, err := SelectRegisteredParent(ctx, loader, key, entries, selectionJournal, selectionApprovals); err == nil {
						t.Fatal("missing/ambiguous selection accepted")
					}
				}
				if entries, err := os.ReadDir(selectionJournal); err != nil || len(entries) != 0 {
					t.Fatalf("refused selection consumed journal: %v", err)
				}
				// Simulate publication unavailable after the durable choice/claim.
				if _, err := SelectRegisteredParent(ctx, loader, key, []ParentLaunchRegistration{registration}, selectionJournal, filepath.Join(selectionApprovals, "missing")); err == nil {
					t.Fatal("missing publication directory accepted")
				}
				replacement := registration
				replacement.Incarnation = "blake3:" + strings.Repeat("b", 64)
				if _, err := SelectRegisteredParent(ctx, loader, key, []ParentLaunchRegistration{replacement}, selectionJournal, selectionApprovals); err == nil {
					t.Fatal("publication failure allowed replacement parent")
				}
				if _, err := os.Stat(filepath.Join(selectionJournal, replacement.Incarnation[7:]+".json")); !os.IsNotExist(err) {
					t.Fatalf("replacement incarnation was consumed: %v", err)
				}
				for i := 0; i < 2; i++ {
					selected, err := SelectRegisteredParent(ctx, loader, key, []ParentLaunchRegistration{registration}, selectionJournal, selectionApprovals)
					if err != nil || selected != bound {
						t.Fatalf("selection recovery changed binding: %v", err)
					}
				}
				journal := t.TempDir()
				approvals := t.TempDir()
				for i := 0; i < 2; i++ {
					claimed, err := ClaimRegisteredParent(ctx, loader, *frozen, registration, journal)
					if err != nil || claimed != bound {
						t.Fatalf("bind/claim changed approval: %v", err)
					}
					published, err := PublishRegisteredParent(ctx, loader, *frozen, registration, journal, approvals)
					if err != nil || published != bound {
						t.Fatalf("approval publication: %v", err)
					}
					loaded, transport, err := LoadApproval(approvals, run)
					if err != nil {
						t.Fatal(err)
					}
					transport.Close()
					if loaded != bound.Binding {
						t.Fatal("controller loaded different published binding")
					}
				}
			}
		})
	}
}

type provisionStatusReader struct {
	client.Reader
	phase api.AgentRunPhase
	bound bool
}

func (r provisionStatusReader) Get(ctx context.Context, key client.ObjectKey, object client.Object, options ...client.GetOption) error {
	if err := r.Reader.Get(ctx, key, object, options...); err != nil {
		return err
	}
	if run, ok := object.(*api.AgentRun); ok {
		run.Status.Phase = r.phase
		if r.bound {
			run.Status.CellnParent = &api.CellnParentStatus{}
		}
	}
	return nil
}
