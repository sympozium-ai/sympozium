package cellninstall

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnparent"
	"github.com/sympozium-ai/sympozium/internal/cellnplatform"
	"github.com/zeebo/blake3"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// starterConfiguration materializes the reviewed testdata with a consistent
// receipt, as the fleet nodes publish it.
type testBackend struct{ name, provider, protocol, model, url, credentialProfile string }

func starterConfiguration(t *testing.T) (dir, packageHash, principal string) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "configuration")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	// Two backends of one package: the reviewed default (DeepSeek) and an
	// Anthropic route named "claude" sharing every other byte.
	for _, backend := range []testBackend{
		{"native", "", "", "", "", ""},
		{"claude", "anthropic", "anthropic-messages", "claude-test", "https://api.anthropic.com/v1/messages", "trial-claude"},
	} {
		packageHash, principal = writeBackendConfiguration(t, dir, backend)
	}
	return dir, packageHash, principal
}

// writeBackendConfiguration materializes one backend's node configuration
// from the testdata package, as a node would publish it.
func writeBackendConfiguration(t *testing.T, dir string, backend testBackend) (packageHash, principal string) {
	t.Helper()
	sub := filepath.Join(dir, backend.name)
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	var metadata receipt
	for _, name := range []string{"catalogue.json", "native-template.json", "configured.json"} {
		raw, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		raw = []byte(strings.TrimSuffix(string(raw), "\n"))
		if name == "native-template.json" && backend.provider != "" {
			var native map[string]any
			if err := json.Unmarshal(raw, &native); err != nil {
				t.Fatal(err)
			}
			template := native["template"].(map[string]any)
			template["model"], template["url"] = backend.model, backend.url
			native["modelProfile"] = "blake3:" + strings.Repeat("e", 64)
			raw, _ = json.Marshal(native)
		}
		if name == "configured.json" {
			if err := json.Unmarshal(raw, &metadata); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(sub, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if backend.provider != "" {
		metadata.Model = api.ModelSpec{Provider: backend.provider, Protocol: backend.protocol, Model: backend.model, BaseURL: backend.url, CredentialProfile: backend.credentialProfile}
		metadata.ModelProfile = "blake3:" + strings.Repeat("e", 64)
	}
	for file, field := range map[string]*string{"catalogue.json": &metadata.CatalogueHash, "native-template.json": &metadata.NativeTemplateHash} {
		raw, _ := os.ReadFile(filepath.Join(sub, file))
		*field = fmt.Sprintf("blake3:%x", blake3.Sum256(raw))
	}
	raw, _ := json.Marshal(metadata)
	if err := os.WriteFile(filepath.Join(sub, "configured.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	return metadata.PackageHash, metadata.Principal
}

func platformInstallStore(t *testing.T) client.Client {
	t.Helper()
	replicas := int32(1)
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "sympozium-controller-manager", Namespace: "sympozium-system", Generation: 1}, Spec: appsv1.DeploymentSpec{Replicas: &replicas, Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "manager"}}}}}, Status: appsv1.DeploymentStatus{ObservedGeneration: 1, Replicas: 1, UpdatedReplicas: 1, AvailableReplicas: 1}}
	scheme := runtime.NewScheme()
	_ = api.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(deployment, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: "cluster-uid"}}, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-a"}}, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-b"}}).Build()
}

func TestInstallPlatformPublishesCatalogueOncePerScopeAndWrapsNamespaces(t *testing.T) {
	ctx := context.Background()
	dir, packageHash, principal := starterConfiguration(t)
	store := platformInstallStore(t)
	clusterID, err := ClusterIdentity(ctx, store)
	if err != nil || clusterID != "cluster-uid" {
		t.Fatalf("cluster identity: %q %v", clusterID, err)
	}
	options := func(namespace string) PlatformOptions {
		return PlatformOptions{Namespace: namespace, ConfigurationDir: dir, OutputDir: filepath.Join(t.TempDir(), "out-"+namespace), Scope: "trial", ClusterID: clusterID, PackageHash: packageHash, Principal: principal, ControllerNamespace: "sympozium-system"}
	}
	first := options("tenant-a")
	if err := InstallPlatform(ctx, store, first); err != nil {
		t.Fatal(err)
	}
	profileName, policyName, toolName := PlatformCatalogueNames("trial")
	var profile api.CellnRuntimeProfile
	if err := store.Get(ctx, types.NamespacedName{Name: profileName}, &profile); err != nil {
		t.Fatal(err)
	}
	// The second backend has its own profile, credential profile and wrappers.
	var claude api.CellnRuntimeProfile
	if err := store.Get(ctx, types.NamespacedName{Name: PlatformProfileName("trial", "claude")}, &claude); err != nil || claude.Labels[cellnplatform.BackendLabel] != "claude" || claude.Annotations[cellnplatform.ProtocolAnnotation] != "anthropic-messages" || claude.Spec.Native.CredentialProfile != "trial-claude" || claude.Spec.Native.ModelProfile != "blake3:"+strings.Repeat("e", 64) {
		t.Fatalf("claude profile: %v %+v", err, claude)
	}
	var claudeConnection api.ModelConnection
	if err := store.Get(ctx, types.NamespacedName{Namespace: "tenant-a", Name: "celln-claude"}, &claudeConnection); err != nil || claudeConnection.Spec.Provider != "anthropic" || claudeConnection.Spec.Protocol != "anthropic-messages" || claudeConnection.Spec.CredentialProfile != "trial-claude" || claudeConnection.Spec.Validate() != nil {
		t.Fatalf("claude wrapper connection: %v %+v", err, claudeConnection.Spec)
	}
	native := profile.Spec.Native
	if native == nil || native.CredentialProfile != "trial" || native.ModelProfile == "" || native.SystemPrompt == "" || len(native.Parent.Raw) == 0 || len(native.Template.Raw) == 0 || profile.Spec.Lifecycles[1] != "enduring" || profile.Annotations[packageAnnotation] != packageHash {
		t.Fatalf("profile lacks native provisioning material: %+v", profile.Spec)
	}
	var policy api.CellnExecutionPolicy
	if err := store.Get(ctx, types.NamespacedName{Name: policyName}, &policy); err != nil {
		t.Fatal(err)
	}
	route := policy.Spec.Routes[0]
	exclusions := policy.Spec.NamespaceSelector.MatchExpressions
	if len(exclusions) != 2 || exclusions[0].Key != cellnplatform.NamespaceNameLabel || exclusions[0].Operator != metav1.LabelSelectorOpNotIn || !slices.Contains(exclusions[0].Values, "kube-system") || !slices.Contains(exclusions[0].Values, "sympozium-system") || !slices.Contains(exclusions[0].Values, "celln-system") || exclusions[1].Key != cellnplatform.ExcludedLabel || exclusions[1].Operator != metav1.LabelSelectorOpDoesNotExist {
		t.Fatalf("default policy is not open with system exclusions: %+v", policy.Spec.NamespaceSelector)
	}
	if len(policy.Spec.RuntimeProfiles) != 2 || len(policy.Spec.Routes) != 2 || policy.Spec.Routes[0].Provider != "anthropic" || policy.Spec.Routes[1].Provider != "deepseek" {
		t.Fatalf("policy must carry every backend's profile and route: %+v", policy.Spec)
	}
	route = policy.Spec.Routes[1]
	if len(policy.Spec.Tools) != 3 || route.Auth != "host-profile" || route.Provider != "deepseek" || route.EndpointOrigins[0] != "https://api.deepseek.com" || policy.Spec.Ceilings.MaxTurns != 12 || policy.Spec.Ceilings.MaxParentLeaseSeconds != 3600 || policy.Spec.Ceilings.MaxTurnSeconds != 60 {
		t.Fatalf("policy does not bind the reviewed route and ceilings: %+v", policy.Spec)
	}
	var tool api.ClusterCellnTool
	if err := store.Get(ctx, types.NamespacedName{Name: toolName("https-fetch")}, &tool); err != nil || tool.Spec.Revision != "v1" {
		t.Fatalf("cluster tool missing: %v", err)
	}
	var namespace corev1.Namespace
	if err := store.Get(ctx, types.NamespacedName{Name: "tenant-a"}, &namespace); err != nil || namespace.Labels[ScopeLabel] != "" {
		t.Fatalf("default mode labeled the namespace: %v %v", err, namespace.Labels)
	}
	var wrapper api.AgentRuntime
	var connection api.ModelConnection
	if err := store.Get(ctx, types.NamespacedName{Namespace: "tenant-a", Name: "celln-native"}, &wrapper); err != nil || wrapper.Spec.CellnProfileRef == nil || wrapper.Spec.CellnProfileRef.Name != profileName || wrapper.Spec.Celln != nil {
		t.Fatalf("tenant wrapper: %v %+v", err, wrapper.Spec)
	}
	if err := store.Get(ctx, types.NamespacedName{Namespace: "tenant-a", Name: "celln-native"}, &connection); err != nil || connection.Spec.CredentialProfile != "trial" || connection.Spec.SecretRef != "" || connection.Spec.Validate() != nil {
		t.Fatalf("tenant model connection: %v %+v", err, connection.Spec)
	}
	var grants corev1.ConfigMapList
	if err := store.List(ctx, &grants, client.InNamespace("tenant-a")); err != nil || len(grants.Items) != 0 {
		t.Fatal("platform installation published namespace grant ConfigMaps")
	}
	var tools api.CellnToolList
	if err := store.List(ctx, &tools, client.InNamespace("tenant-a")); err != nil || len(tools.Items) != 0 {
		t.Fatal("platform installation copied namespaced tools")
	}
	out := first.OutputDir
	raw, err := os.ReadFile(filepath.Join(out, "registrations.json"))
	if err != nil {
		t.Fatal(err)
	}
	var registration cellnparent.RegistrationConfig
	if err := json.Unmarshal(raw, &registration); err != nil || registration.Platform == nil || registration.Platform.ClusterID != clusterID || registration.Platform.Target != ManagedRouterURL || registration.LocalProvisioner != nil || registration.Journal != FleetJournalRoot+"/journal" {
		t.Fatalf("registrations are not platform-mode: %v %+v", err, registration)
	}
	raw, _ = os.ReadFile(filepath.Join(out, "run.json"))
	var run api.AgentRun
	if err := json.Unmarshal(raw, &run); err != nil || run.Spec.Model.ConnectionRef != "celln-native" || len(run.Spec.CellnSelection.ClusterToolRefs) != 3 || len(run.Spec.CellnSelection.ToolRefs) != 0 || run.Spec.SystemPrompt != native.SystemPrompt || run.Spec.Enduring.MaxTurns != 12 || run.Spec.Enduring.LeaseSeconds != 3600 || run.Spec.Enduring.MaxOutputTokens != 18432 || run.Spec.ExecutionLifecycle != "enduring" {
		t.Fatalf("sample run is not a platform run: %v %+v", err, run.Spec)
	}
	values, err := ConfigureFleet(ctx, store, Options{OutputDir: out, ControllerNamespace: "sympozium-system", OwnerTarget: ManagedRouterURL})
	if err != nil || values[0] != "celln.fleet.parentConfigSecret="+FleetParentConfigSecret {
		t.Fatalf("fleet wiring from platform registration: %v %v", err, values)
	}
	// A second namespace reuses the scope's catalogue and gets only wrappers.
	if err := InstallPlatform(ctx, store, options("tenant-b")); err != nil {
		t.Fatalf("second namespace on the same scope: %v", err)
	}
	if err := store.Get(ctx, types.NamespacedName{Namespace: "tenant-b", Name: "celln-native"}, &wrapper); err != nil {
		t.Fatal(err)
	}
	// Strict mode publishes a scope-label selector and labels the namespace.
	strict := options("tenant-b")
	strict.Scope, strict.Authorise, strict.OutputDir = "strict", "labeled", filepath.Join(t.TempDir(), "strict")
	if err := InstallPlatform(ctx, store, strict); err != nil {
		t.Fatal(err)
	}
	_, strictPolicy, _ := PlatformCatalogueNames("strict")
	if err := store.Get(ctx, types.NamespacedName{Name: strictPolicy}, &policy); err != nil || policy.Spec.NamespaceSelector.MatchLabels[ScopeLabel] != "strict" {
		t.Fatalf("strict policy: %v %+v", err, policy.Spec.NamespaceSelector)
	}
	if err := store.Get(ctx, types.NamespacedName{Name: "tenant-b"}, &namespace); err != nil || namespace.Labels[ScopeLabel] != "strict" {
		t.Fatalf("strict mode did not label the namespace: %v %v", err, namespace.Labels)
	}
	bogus := options("tenant-b")
	bogus.Authorise, bogus.OutputDir = "sometimes", filepath.Join(t.TempDir(), "bogus")
	if err := InstallPlatform(ctx, store, bogus); err == nil {
		t.Fatal("unknown authorisation mode accepted")
	}
	// A different package under the same scope is refused before tenant changes.
	mismatch := options("tenant-b")
	mismatch.Principal = "someone-else"
	if err := InstallPlatform(ctx, store, mismatch); err == nil {
		t.Fatal("principal differing from the reviewed configuration accepted")
	}
}

func TestSessionDefaultsStayInsideCeilings(t *testing.T) {
	wide := SessionDefaults(api.EnduringRunSpec{LeaseSeconds: 86400, MaxTurns: 256, MaxModelRequests: 1536, MaxOutputTokens: 786432})
	if wide.LeaseSeconds != 14400 || wide.MaxTurns != 64 || wide.MaxModelRequests != 384 || wide.MaxOutputTokens != 196608 {
		t.Fatalf("session defaults: %+v", wide)
	}
	// Ceilings that pay for two turns yield two, not the four the policy names.
	narrow := SessionDefaults(api.EnduringRunSpec{LeaseSeconds: 600, MaxTurns: 4, MaxModelRequests: 12, MaxOutputTokens: 6144})
	if narrow.LeaseSeconds != 600 || narrow.MaxTurns != 2 || narrow.MaxModelRequests != 12 || narrow.MaxOutputTokens != 6144 {
		t.Fatalf("session defaults exceed narrow ceilings: %+v", narrow)
	}
}

// A rerun with one more backend configured by the nodes adds that backend's
// profile, route and wrappers to the scope and rewrites the installation
// record; the first installation's registration and sample run are kept.
func TestInstallPlatformAddsBackendToRunningScope(t *testing.T) {
	ctx := context.Background()
	dir, packageHash, principal := starterConfiguration(t)
	store := platformInstallStore(t)
	o := PlatformOptions{Namespace: "tenant-a", ConfigurationDir: dir, OutputDir: filepath.Join(t.TempDir(), "out"), Scope: "trial", ClusterID: "cluster-uid", PackageHash: packageHash, Principal: principal, ControllerNamespace: "sympozium-system"}
	if err := InstallPlatform(ctx, store, o); err != nil {
		t.Fatal(err)
	}
	runBefore, err := os.ReadFile(filepath.Join(o.OutputDir, "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	writeBackendConfiguration(t, dir, testBackend{"spare", "spare-provider", "openai-chat", "spare.gguf", "https://spare.example/v1/chat/completions", "trial-spare"})
	if err := InstallPlatform(ctx, store, o); err != nil {
		t.Fatalf("rerun with an added backend refused: %v", err)
	}
	_, policyName, _ := PlatformCatalogueNames("trial")
	var policy api.CellnExecutionPolicy
	if err := store.Get(ctx, types.NamespacedName{Name: policyName}, &policy); err != nil {
		t.Fatal(err)
	}
	if len(policy.Spec.RuntimeProfiles) != 3 || len(policy.Spec.Routes) != 3 || !slices.ContainsFunc(policy.Spec.Routes, func(r api.CellnExecutionPolicyRoute) bool {
		return r.Provider == "spare-provider" && r.EndpointOrigins[0] == "https://spare.example"
	}) {
		t.Fatalf("policy did not grow by the added backend only: %+v", policy.Spec)
	}
	var spare api.CellnRuntimeProfile
	if err := store.Get(ctx, types.NamespacedName{Name: PlatformProfileName("trial", "spare")}, &spare); err != nil || spare.Spec.Native.CredentialProfile != "trial-spare" {
		t.Fatalf("added backend profile: %v", err)
	}
	var connection api.ModelConnection
	if err := store.Get(ctx, types.NamespacedName{Namespace: "tenant-a", Name: "celln-spare"}, &connection); err != nil || connection.Spec.Provider != "spare-provider" {
		t.Fatalf("added backend wrappers: %v", err)
	}
	var installed struct {
		Backends []string `json:"backends"`
	}
	raw, err := os.ReadFile(filepath.Join(o.OutputDir, "installed.json"))
	if err != nil || json.Unmarshal(raw, &installed) != nil || len(installed.Backends) != 3 {
		t.Fatalf("installation record not rewritten with every backend: %s %v", raw, err)
	}
	runAfter, _ := os.ReadFile(filepath.Join(o.OutputDir, "run.json"))
	if string(runAfter) != string(runBefore) {
		t.Fatal("sample run of the first installation was rewritten")
	}
	// The same rerun again changes nothing.
	if err := InstallPlatform(ctx, store, o); err != nil {
		t.Fatalf("idempotent rerun refused: %v", err)
	}
}

// repackage turns a materialized configuration into the one a newer package
// publishes: another package hash, a new worker revision and its last tool
// renamed (the old name is dropped, a new one added).
func repackage(t *testing.T, dir, packageHash string) {
	t.Helper()
	backends, err := ConfigurationBackends(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, backend := range backends {
		sub := filepath.Join(dir, backend)
		var cat map[string]any
		raw, _ := os.ReadFile(filepath.Join(sub, "catalogue.json"))
		if err := json.Unmarshal(raw, &cat); err != nil {
			t.Fatal(err)
		}
		tools := cat["tools"].([]any)
		last := tools[len(tools)-1].(map[string]any)
		last["name"] = fmt.Sprint(last["name"]) + "-next"
		worker := cat["worker"].(map[string]any)
		worker["revision"] = fmt.Sprint(worker["revision"]) + "-next"
		raw, _ = json.Marshal(cat)
		if err := os.WriteFile(filepath.Join(sub, "catalogue.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
		var configured map[string]any
		receiptRaw, _ := os.ReadFile(filepath.Join(sub, "configured.json"))
		if err := json.Unmarshal(receiptRaw, &configured); err != nil {
			t.Fatal(err)
		}
		configured["packageHash"] = packageHash
		configured["catalogueHash"] = fmt.Sprintf("blake3:%x", blake3.Sum256(raw))
		receiptRaw, _ = json.Marshal(configured)
		if err := os.WriteFile(filepath.Join(sub, "configured.json"), receiptRaw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

// Moving a scope to a new package is refused unless approved; approved, the
// catalogue is replaced, dropped tools are retired and every namespace's
// platform wrappers follow the new profiles.
func TestInstallPlatformReplacesPackageOnlyWhenApproved(t *testing.T) {
	ctx := context.Background()
	dir, oldPackage, principal := starterConfiguration(t)
	store := platformInstallStore(t)
	o := PlatformOptions{Namespace: "tenant-a", ConfigurationDir: dir, OutputDir: filepath.Join(t.TempDir(), "out"), Scope: "trial", ClusterID: "cluster-uid", PackageHash: oldPackage, Principal: principal, ControllerNamespace: "sympozium-system"}
	if err := InstallPlatform(ctx, store, o); err != nil {
		t.Fatal(err)
	}
	if _, err := cellnplatform.EnsureWrappers(ctx, store, "tenant-b", PlatformProfileName("trial", "claude")); err != nil {
		t.Fatalf("on-demand wrappers: %v", err)
	}
	var oldCat catalogue
	if _, err := read(filepath.Join(dir, "native", "catalogue.json"), &oldCat); err != nil {
		t.Fatal(err)
	}
	_, policyName, toolName := PlatformCatalogueNames("trial")
	dropped := toolName(oldCat.Tools[len(oldCat.Tools)-1].Name)

	newPackage := "blake3:" + strings.Repeat("b", 64)
	repackage(t, dir, newPackage)
	o.PackageHash = newPackage
	if err := InstallPlatform(ctx, store, o); err == nil || !strings.Contains(err.Error(), "another package") {
		t.Fatalf("unapproved package change accepted: %v", err)
	}
	o.Replacing = FleetPublication{Exists: true, Package: oldPackage, Scope: "trial"}
	if err := InstallPlatform(ctx, store, o); err != nil {
		t.Fatalf("approved replacement refused: %v", err)
	}

	var profile api.CellnRuntimeProfile
	if err := store.Get(ctx, types.NamespacedName{Name: PlatformProfileName("trial", "claude")}, &profile); err != nil || profile.Annotations[packageAnnotation] != newPackage || !strings.HasSuffix(profile.Spec.Revision, "-next") {
		t.Fatalf("profile not replaced: %v %+v", err, profile.ObjectMeta)
	}
	var tool api.ClusterCellnTool
	if err := store.Get(ctx, types.NamespacedName{Name: dropped}, &tool); err == nil {
		t.Fatalf("tool %s dropped by the new package was kept", dropped)
	}
	var policy api.CellnExecutionPolicy
	if err := store.Get(ctx, types.NamespacedName{Name: policyName}, &policy); err != nil || policy.Annotations[packageAnnotation] != newPackage || len(policy.Spec.Tools) != len(oldCat.Tools) || !slices.ContainsFunc(policy.Spec.Tools, func(t api.CellnExecutionPolicyTool) bool { return t.Ref.Name == dropped+"-next" }) {
		t.Fatalf("policy not replaced: %v %+v", err, policy.Spec.Tools)
	}
	for _, ref := range policy.Spec.RuntimeProfiles {
		if !strings.HasSuffix(ref.Ref.Revision, "-next") {
			t.Fatalf("policy still names the old profile revision: %+v", ref)
		}
	}
	for _, key := range []types.NamespacedName{{Namespace: "tenant-a", Name: "celln-claude"}, {Namespace: "tenant-b", Name: "celln-claude"}} {
		var runtime api.AgentRuntime
		if err := store.Get(ctx, key, &runtime); err != nil || runtime.Spec.CellnProfileRef.Revision != profile.Spec.Revision {
			t.Fatalf("%s wrapper not rebound: %v %+v", key, err, runtime.Spec.CellnProfileRef)
		}
	}
	// A rerun of the new package is an ordinary idempotent install.
	o.Replacing = FleetPublication{}
	if err := InstallPlatform(ctx, store, o); err != nil {
		t.Fatalf("rerun after replacement refused: %v", err)
	}
}

func TestFleetPublicationReplaces(t *testing.T) {
	p := FleetPublication{Exists: true, Package: "blake3:a", Scope: "starter"}
	for _, c := range []struct {
		scope, pkg string
		want       bool
	}{{"starter", "blake3:a", false}, {"starter", "blake3:b", true}, {"other", "blake3:a", true}} {
		if got := p.Replaces(c.scope, c.pkg); got != c.want {
			t.Fatalf("%+v: got %v", c, got)
		}
	}
	if (FleetPublication{}).Replaces("starter", "blake3:b") {
		t.Fatal("a fresh cluster replaces nothing")
	}
	if (FleetPublication{Exists: true, Package: "blake3:a"}).Replaces("other", "blake3:a") {
		t.Fatal("an unrecorded scope with the same package is not a replacement")
	}
	if err := p.ReplacementRefusal("starter", "blake3:b"); !strings.Contains(err.Error(), "--celln-fleet-replace-package") {
		t.Fatalf("refusal does not name the approval flag: %v", err)
	}
}
