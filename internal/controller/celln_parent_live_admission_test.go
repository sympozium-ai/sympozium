package controller_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnauthority"
	"github.com/sympozium-ai/sympozium/internal/cellnparent"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Public metadata comes from the hardware fixture's actual signed artifacts.
// The OCI image is explicitly unused; this proves only native parent placement.
func liveParentCatalogue(t *testing.T, ctx context.Context, store client.Client, agent *api.Agent) (cellnauthority.Loader, string, []api.CellnCatalogueToolRef) {
	t.Helper()
	var catalogue struct {
		SystemPrompt string                       `json:"systemPrompt"`
		Tool         api.CellnToolSpec            `json:"tool"`
		Worker       api.AgentRuntimeCellnProfile `json:"worker"`
		Tools        []struct {
			Name string            `json:"name"`
			Spec api.CellnToolSpec `json:"spec"`
		} `json:"tools"`
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(os.Getenv("CELLN_INTEROP_RESULT")), "interop-catalogue.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &catalogue); err != nil {
		t.Fatal(err)
	}
	worker := &api.AgentRuntime{ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: agent.Namespace}, Spec: api.AgentRuntimeSpec{Image: "proof.invalid/unused@sha256:" + strings.Repeat("0", 64), Celln: &catalogue.Worker}}
	var tools []*api.CellnTool
	if len(catalogue.Tools) == 0 {
		tools = append(tools, &api.CellnTool{ObjectMeta: metav1.ObjectMeta{Name: "uppercase", Namespace: agent.Namespace}, Spec: catalogue.Tool})
	} else {
		if len(catalogue.Tools) > 16 {
			t.Fatal("catalogue exceeds bounded tool selection")
		}
		for _, entry := range catalogue.Tools {
			tools = append(tools, &api.CellnTool{ObjectMeta: metav1.ObjectMeta{Name: entry.Name, Namespace: agent.Namespace}, Spec: entry.Spec})
		}
	}
	objects := []client.Object{worker}
	for _, tool := range tools {
		objects = append(objects, tool)
	}
	for _, obj := range objects {
		if err := store.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
		if err := store.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
			t.Fatal(err)
		}
	}
	agentUID := agent.UID
	agentKey := client.ObjectKeyFromObject(agent)
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := store.Get(ctx, agentKey, agent); err != nil {
			return err
		}
		if agent.UID != agentUID {
			return fmt.Errorf("proof agent identity changed")
		}
		agent.Spec.RuntimeRef = worker.Name
		return store.Update(ctx, agent)
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Get(ctx, client.ObjectKeyFromObject(agent), agent); err != nil {
		t.Fatal(err)
	}
	agentID, err := cellnauthority.IdentifySubject("Agent", agent.ObjectMeta, agent.Spec)
	if err != nil {
		t.Fatal(err)
	}
	workerID, err := cellnauthority.IdentifySubject("AgentRuntime", worker.ObjectMeta, worker.Spec)
	if err != nil {
		t.Fatal(err)
	}
	var grants []cellnauthority.Grant
	var refs []api.CellnCatalogueToolRef
	for _, tool := range tools {
		toolID, err := cellnauthority.Identify(*tool)
		if err != nil {
			t.Fatal(err)
		}
		grants = append(grants, cellnauthority.Grant{Tool: toolID, Limits: *tool.Spec.Limits.DeepCopy()})
		refs = append(refs, api.CellnCatalogueToolRef{Name: tool.Name, Revision: tool.Spec.Revision})
	}
	for _, layer := range []string{"operator", "runtime", "agent"} {
		raw, err := json.Marshal(cellnauthority.GrantDocument{APIVersion: "sympozium.ai/celln-grants-v1", Layer: layer, Agent: agentID, Runtime: workerID, Grants: grants})
		if err != nil {
			t.Fatal(err)
		}
		cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "grant-" + layer, Namespace: agent.Namespace}, Data: map[string]string{"grants.json": string(raw)}}
		if err := store.Create(ctx, cm); err != nil {
			t.Fatal(err)
		}
	}
	ref := func(name string) types.NamespacedName {
		return types.NamespacedName{Namespace: agent.Namespace, Name: "grant-" + name}
	}
	return cellnauthority.Loader{Reader: store, OperatorSource: ref("operator"), RuntimeSource: ref("runtime"), AgentSource: ref("agent")}, catalogue.SystemPrompt, refs
}

func liveParentRegistration(t *testing.T, ctx context.Context, loader cellnauthority.Loader, run *api.AgentRun) (string, string) {
	t.Helper()
	frozen, err := loader.FreezeParentRun(ctx, client.ObjectKeyFromObject(run))
	if err != nil {
		t.Fatal(err)
	}
	digest, err := cellnparent.ParentSelectionDigest(frozen.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	registration := cellnparent.ParentLaunchRegistration{APIVersion: "sympozium.ai/celln-parent-registration-v1", SelectionSHA256: digest, Target: os.Getenv("CELLN_INTEROP_ORIGIN"), Principal: "test:parent", LaunchProfile: os.Getenv("CELLN_INTEROP_LAUNCH"), Incarnation: os.Getenv("CELLN_INTEROP_PARENT"), TokenFile: os.Getenv("CELLN_INTEROP_TOKEN_FILE"), Model: run.Spec.Model, SystemPrompt: run.Spec.SystemPrompt, HostLimits: *run.Spec.Enduring}
	registration.Target, registration.CAFile = liveParentTLS(t, registration.Target)
	config := cellnparent.RegistrationConfig{APIVersion: "sympozium.ai/celln-parent-registrations-v1", Journal: t.TempDir(), Approvals: t.TempDir(), OperatorSource: loader.OperatorSource, RuntimeSource: loader.RuntimeSource, AgentSource: loader.AgentSource, Registrations: []cellnparent.ParentLaunchRegistration{registration}}
	if binary := os.Getenv("CELLN_INTEROP_PROVISION_BINARY"); binary != "" {
		root := filepath.Dir(os.Getenv("CELLN_INTEROP_RESULT"))
		var native cellnparent.NativeProvisionConfig
		raw, err := os.ReadFile(filepath.Join(root, "interop-native-template.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &native); err != nil {
			t.Fatal(err)
		}
		for _, directory := range []string{"parent-issuance", "trusted-parent-permits", "trusted-parent-launches"} {
			entries, err := os.ReadDir(filepath.Join(root, directory))
			if err != nil || len(entries) != 0 {
				t.Fatalf("authority was pre-issued in %s: %v", directory, err)
			}
		}
		config.Registrations = nil
		config.LocalProvisioner = &cellnparent.LocalProvisioner{Binary: binary, Root: root, Journal: config.Journal, Approvals: config.Approvals, Target: registration.Target, TokenFile: registration.TokenFile, CAFile: registration.CAFile}
		config.HostTemplates = []cellnparent.HostProvisionTemplate{{APIVersion: "sympozium.ai/celln-parent-host-template-v1", SelectionSHA256: digest, Scope: "kind-celln-deployed-live-proof", Principal: registration.Principal, Model: registration.Model, HostLimits: registration.HostLimits, Native: native}}
	}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "registrations.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path, config.Approvals
}
