// Package cellninstall publishes an operator-reviewed native starter catalogue.
// It does not issue a run, read model keys, replace resources or start an owner.
package cellninstall

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/zeebo/blake3"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnauthority"
	"github.com/sympozium-ai/sympozium/internal/cellnparent"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Options struct {
	Namespace, ConfigurationDir, OutputDir, StatePath string
	OwnerTarget, Scope                                string
	ControllerNamespace                               string
	PackageHash                                       string
}

type catalogue struct {
	SystemPrompt string                       `json:"systemPrompt"`
	Worker       api.AgentRuntimeCellnProfile `json:"worker"`
	Tools        []struct {
		Name string            `json:"name"`
		Spec api.CellnToolSpec `json:"spec"`
	} `json:"tools"`
}

type receipt struct {
	APIVersion          string              `json:"apiVersion"`
	PackageHash         string              `json:"packageHash"`
	CatalogueHash       string              `json:"catalogueHash"`
	NativeTemplateHash  string              `json:"nativeTemplateHash"`
	Principal           string              `json:"principal"`
	ModelProfile        string              `json:"modelProfile"`
	Model               api.ModelSpec       `json:"model"`
	HostLimits          api.EnduringRunSpec `json:"hostLimits"`
	ExecutionAuthorized bool                `json:"executionAuthorized"`
	Readiness           string              `json:"readiness"`
}

func read(path string, output any) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return "", fmt.Errorf("bounded regular installation input required: %s", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return "", fmt.Errorf("installation input exceeds bound")
	}
	return fmt.Sprintf("blake3:%x", blake3.Sum256(raw)), json.Unmarshal(raw, output)
}

// Partitioned recognizes the explicit controller scope flags produced by the
// chart. It is a deployment preflight, not an authorization boundary.
func Partitioned(args []string, namespace string) bool {
	var watch, exclude string
	for i := 0; i < len(args); i++ {
		for _, option := range []struct {
			key   string
			value *string
		}{{"--watch-namespace", &watch}, {"--exclude-watch-namespaces", &exclude}} {
			if strings.HasPrefix(args[i], option.key+"=") {
				*option.value = strings.TrimPrefix(args[i], option.key+"=")
			} else if args[i] == option.key && i+1 < len(args) {
				i++
				*option.value = args[i]
			}
		}
	}
	if watch != "" {
		return watch != namespace && exclude == ""
	}
	for _, name := range strings.Split(exclude, ",") {
		if name == namespace {
			return true
		}
	}
	return false
}

func Install(ctx context.Context, store client.Client, o Options) error {
	origin, originErr := url.Parse(o.OwnerTarget)
	if store == nil || len(validation.IsDNS1123Label(o.Namespace)) != 0 || o.Namespace == o.ControllerNamespace || o.Scope == "" || originErr != nil || (origin.Scheme != "https" && origin.Scheme != "http") || origin.Hostname() == "" || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" || (origin.Path != "" && origin.Path != "/") || !strings.HasPrefix(o.PackageHash, "blake3:") || len(o.PackageHash) != 71 {
		return fmt.Errorf("dedicated namespace, scope and HTTPS owner required")
	}
	for _, path := range []string{o.ConfigurationDir, o.OutputDir, o.StatePath} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return fmt.Errorf("clean absolute installation paths required")
		}
	}
	if !strings.HasPrefix(o.StatePath, "/var/lib/sympozium-celln/") || len(validation.IsDNS1123Label(strings.TrimPrefix(o.StatePath, "/var/lib/sympozium-celln/"))) != 0 {
		return fmt.Errorf("dedicated host state path required")
	}
	var d appsv1.Deployment
	if err := store.Get(ctx, types.NamespacedName{Namespace: o.ControllerNamespace, Name: "sympozium-controller-manager"}, &d); err != nil {
		return err
	}
	// The unified plane folds the parent reconcilers into the one controller, so
	// the legacy namespace partition (a separate parent-only controller watching
	// a dedicated namespace) is no longer required. We still require the
	// controller to be fully rolled out before writing native authority.
	if len(d.Spec.Template.Spec.Containers) != 1 || d.Spec.Replicas == nil || *d.Spec.Replicas < 1 || d.Status.ObservedGeneration < d.Generation || d.Status.Replicas != *d.Spec.Replicas || d.Status.UpdatedReplicas != *d.Spec.Replicas || d.Status.AvailableReplicas != *d.Spec.Replicas {
		return fmt.Errorf("general controller must finish its rollout before native installation")
	}
	var cat catalogue
	var configured receipt
	var native cellnparent.NativeProvisionConfig
	hashes := map[string]string{}
	for file, target := range map[string]any{"catalogue.json": &cat, "configured.json": &configured, "native-template.json": &native} {
		hash, err := read(filepath.Join(o.ConfigurationDir, file), target)
		if err != nil {
			return err
		}
		hashes[file] = hash
	}
	if configured.PackageHash != o.PackageHash || configured.CatalogueHash != hashes["catalogue.json"] || configured.NativeTemplateHash != hashes["native-template.json"] {
		return fmt.Errorf("configuration differs from reviewed package/receipt")
	}
	if configured.APIVersion != "celln.native-starter-configured/v1" || configured.ExecutionAuthorized || configured.Readiness != "not_established" || native.ModelProfile != configured.ModelProfile || len(cat.Tools) != 3 || cat.Worker.ContractVersion != "celln.json-tools/v1" {
		return fmt.Errorf("operator starter configuration mismatch")
	}
	names := map[string]bool{"workspace-read": true, "workspace-write": true, "https-fetch": true}
	for _, tool := range cat.Tools {
		if !names[tool.Name] {
			return fmt.Errorf("unexpected/duplicate starter tool")
		}
		delete(names, tool.Name)
	}
	// Reserve a private output before any cluster changes. Never adopt/overwrite
	// resources left by another installation, including a previous partial attempt.
	if err := os.Mkdir(o.OutputDir, 0700); err != nil {
		return err
	}
	write := func(name string, value any) error {
		raw, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return err
		}
		f, err := os.OpenFile(filepath.Join(o.OutputDir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		defer f.Close()
		if _, err := f.Write(raw); err != nil {
			return err
		}
		return f.Sync()
	}
	meta := func(name string) metav1.ObjectMeta {
		return metav1.ObjectMeta{Name: name, Namespace: o.Namespace, Annotations: map[string]string{"celln.sympozium.ai/package": configured.PackageHash}}
	}
	// The native profile has no OCI adapter. Keep image empty instead of naming
	// a fake or incompatible image; OCI Ready must not be asserted for this entry.
	runtime := &api.AgentRuntime{ObjectMeta: meta("celln-native"), Spec: api.AgentRuntimeSpec{Celln: &cat.Worker, SupportOwner: "native-starter-operator"}}
	agent := &api.Agent{ObjectMeta: meta("celln-agent"), Spec: api.AgentSpec{RuntimeRef: runtime.Name}}
	var objects []client.Object
	objects = append(objects, runtime, agent)
	for _, entry := range cat.Tools {
		objects = append(objects, &api.CellnTool{ObjectMeta: meta(entry.Name), Spec: entry.Spec})
	}
	for _, object := range objects {
		if err := store.Create(ctx, object); err != nil {
			return fmt.Errorf("create %s: %w; partial installation retained, no run was submitted", object.GetName(), err)
		}
		if err := store.Get(ctx, client.ObjectKeyFromObject(object), object); err != nil {
			return err
		}
	}
	agentID, err := cellnauthority.IdentifySubject("Agent", agent.ObjectMeta, agent.Spec)
	if err != nil {
		return err
	}
	runtimeID, err := cellnauthority.IdentifySubject("AgentRuntime", runtime.ObjectMeta, runtime.Spec)
	if err != nil {
		return err
	}
	var grants []cellnauthority.Grant
	var selections []cellnauthority.Selection
	var refs []api.CellnCatalogueToolRef
	for _, object := range objects[2:] {
		tool := object.(*api.CellnTool)
		id, err := cellnauthority.Identify(*tool)
		if err != nil {
			return err
		}
		grants = append(grants, cellnauthority.Grant{Tool: id, Limits: *tool.Spec.Limits.DeepCopy()})
		selections = append(selections, cellnauthority.Selection{Name: tool.Name, Revision: tool.Spec.Revision})
		refs = append(refs, api.CellnCatalogueToolRef{Name: tool.Name, Revision: tool.Spec.Revision})
	}
	for _, layer := range []string{"operator", "runtime", "agent"} {
		raw, err := json.Marshal(cellnauthority.GrantDocument{APIVersion: "sympozium.ai/celln-grants-v1", Layer: layer, Agent: agentID, Runtime: runtimeID, Grants: grants})
		if err != nil {
			return err
		}
		if err := store.Create(ctx, &corev1.ConfigMap{ObjectMeta: meta("grant-" + layer), Data: map[string]string{"grants.json": string(raw)}}); err != nil {
			return err
		}
	}
	ref := func(layer string) types.NamespacedName {
		return types.NamespacedName{Namespace: o.Namespace, Name: "grant-" + layer}
	}
	loader := cellnauthority.Loader{Reader: store, OperatorSource: ref("operator"), RuntimeSource: ref("runtime"), AgentSource: ref("agent")}
	snapshot, err := loader.Resolve(ctx, client.ObjectKeyFromObject(agent), selections)
	if err != nil {
		return err
	}
	digest, err := cellnparent.ParentSelectionDigest(*snapshot)
	if err != nil {
		return err
	}
	approvals, journal := filepath.Join(o.StatePath, "approvals"), filepath.Join(o.StatePath, "journal")
	registration := cellnparent.RegistrationConfig{APIVersion: "sympozium.ai/celln-parent-registrations-v1", Journal: journal, Approvals: approvals, OperatorSource: ref("operator"), RuntimeSource: ref("runtime"), AgentSource: ref("agent"), LocalProvisioner: &cellnparent.LocalProvisioner{Binary: "/usr/local/bin/celln", Root: filepath.Join(o.StatePath, "authority"), Journal: journal, Approvals: approvals, Target: o.OwnerTarget, TokenFile: "/etc/sympozium/celln-parent/owner-token", CAFile: "/etc/sympozium/celln-parent/owner-ca.pem"}, HostTemplates: []cellnparent.HostProvisionTemplate{{APIVersion: "sympozium.ai/celln-parent-host-template-v1", SelectionSHA256: digest, Scope: o.Scope, Principal: configured.Principal, Model: configured.Model, HostLimits: configured.HostLimits, Native: native}}}
	if err := write("registrations.json", registration); err != nil {
		return err
	}
	if err := write("preview.json", map[string]any{"apiVersion": "sympozium.ai/celln-permission-preview-v1", "bindings": []any{map[string]any{"agent": client.ObjectKeyFromObject(agent), "operatorSource": ref("operator"), "runtimeSource": ref("runtime"), "agentSource": ref("agent")}}}); err != nil {
		return err
	}
	run := &api.AgentRun{TypeMeta: metav1.TypeMeta{APIVersion: "sympozium.ai/v1alpha1", Kind: "AgentRun"}, ObjectMeta: metav1.ObjectMeta{GenerateName: "celln-starter-", Namespace: o.Namespace}, Spec: api.AgentRunSpec{AgentRef: agent.Name, Backend: "celln", ExecutionLifecycle: "enduring", CellnSelection: &api.CellnCatalogueSelection{RuntimeRef: runtime.Name, ToolRefs: refs}, Model: configured.Model, SystemPrompt: cat.SystemPrompt, Enduring: &configured.HostLimits, Task: api.NewStringTask("Write violet to notes.txt using workspace-write with revision 0.")}}
	if err := write("run.json", run); err != nil {
		return err
	}
	return write("installed.json", map[string]any{"namespace": o.Namespace, "packageHash": configured.PackageHash, "selectionSHA256": digest, "runSubmitted": false, "readiness": "not_established"})
}
