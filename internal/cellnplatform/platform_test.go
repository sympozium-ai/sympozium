package cellnplatform

import (
	"context"
	"strings"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func store(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = api.AddToScheme(scheme)
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}

func catalogue(selector metav1.LabelSelector) (*api.CellnRuntimeProfile, *api.CellnExecutionPolicy) {
	raw := func(s string) apiextensionsv1.JSON { return apiextensionsv1.JSON{Raw: []byte(s)} }
	profile := &api.CellnRuntimeProfile{ObjectMeta: metav1.ObjectMeta{Name: "celln-native-trial", UID: "profile"}, Spec: api.CellnRuntimeProfileSpec{Revision: "v1", Native: &api.CellnNativeProvisioning{CredentialProfile: "trial", Template: raw(`{"contract":"celln.json-tools/v1","model":"deepseek-chat","url":"https://api.deepseek.com/chat/completions"}`)}}}
	policy := &api.CellnExecutionPolicy{ObjectMeta: metav1.ObjectMeta{Name: "celln-fleet-trial", UID: "policy"}, Spec: api.CellnExecutionPolicySpec{
		NamespaceSelector: selector,
		RuntimeProfiles:   []api.CellnExecutionPolicyRuntime{{Ref: api.CellnRuntimeProfileRef{Name: profile.Name, Revision: "v1"}}},
		Routes:            []api.CellnExecutionPolicyRoute{{Provider: "deepseek", Protocol: "openai-chat", Models: []string{"deepseek-chat"}, EndpointOrigins: []string{"https://api.deepseek.com"}, Auth: "host-profile"}},
	}}
	return profile, policy
}

func namespace(name string, labels map[string]string) *corev1.Namespace {
	if labels == nil {
		labels = map[string]string{}
	}
	labels[NamespaceNameLabel] = name
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(name + "-uid"), Labels: labels}}
}

func TestOpenSelectorAdmitsOrdinaryNamespacesAndRefusesSystemAndExcluded(t *testing.T) {
	ctx := context.Background()
	profile, policy := catalogue(OpenSelector(SystemNamespaces("sympozium-system", "celln-system")))
	c := store(t, profile, policy,
		namespace("team-a", nil),
		namespace("kube-system", nil),
		namespace("sympozium-system", nil),
		namespace("fenced", map[string]string{ExcludedLabel: "true"}),
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "unstamped", UID: "u"}}, // no metadata.name label
	)
	for name, want := range map[string]int{"team-a": 1, "unstamped": 1, "kube-system": 0, "sympozium-system": 0, "fenced": 0} {
		got, err := AuthorisedProfiles(ctx, c, name)
		if err != nil || len(got) != want {
			t.Fatalf("%s: %d profiles (want %d) %v", name, len(got), want, err)
		}
	}
	if _, err := Selector("bogus", "trial", nil); err == nil {
		t.Fatal("unknown authorisation mode accepted")
	}
	strict, _ := Selector(AuthoriseLabeled, "trial", nil)
	if strict.MatchLabels[ScopeLabel] != "trial" {
		t.Fatalf("strict selector: %+v", strict)
	}
}

func TestEnsureWrappersCreatesMissingObjectsOnceAndNeverOverwrites(t *testing.T) {
	ctx := context.Background()
	profile, policy := catalogue(OpenSelector(SystemNamespaces()))
	existing := &api.ModelConnection{ObjectMeta: metav1.ObjectMeta{Name: WrapperConnectionName, Namespace: "team-a"}, Spec: api.ModelConnectionSpec{Provider: "custom", Protocol: "openai-chat", Endpoint: "https://tenant.example/v1", CredentialProfile: "tenant", Models: []string{"deepseek-chat"}}}
	c := store(t, profile, policy, namespace("team-a", nil), existing)
	got, err := EnsureWrappers(ctx, c, "team-a", profile.Name)
	if err != nil || got.Runtime != WrapperRuntimeName || got.Agent != WrapperAgentName || got.Connection != WrapperConnectionName || strings.Join(got.Created, ",") != "celln-native,celln-agent" {
		t.Fatalf("first ensure: %+v %v", got, err)
	}
	var runtime api.AgentRuntime
	if err := c.Get(ctx, types.NamespacedName{Namespace: "team-a", Name: WrapperRuntimeName}, &runtime); err != nil || runtime.Spec.CellnProfileRef == nil || runtime.Spec.CellnProfileRef.Name != profile.Name || runtime.Labels[ManagedByLabel] != ManagedByValue {
		t.Fatalf("runtime wrapper: %+v %v", runtime.Spec, err)
	}
	var connection api.ModelConnection
	if err := c.Get(ctx, types.NamespacedName{Namespace: "team-a", Name: WrapperConnectionName}, &connection); err != nil || connection.Spec.CredentialProfile != "tenant" {
		t.Fatalf("tenant's own connection was overwritten: %+v", connection.Spec)
	}
	again, err := EnsureWrappers(ctx, c, "team-a", profile.Name)
	if err != nil || len(again.Created) != 0 {
		t.Fatalf("second ensure not idempotent: %+v %v", again, err)
	}
	if _, err := EnsureWrappers(ctx, c, "team-a", "other-profile"); err == nil {
		t.Fatal("unauthorised profile prepared")
	}
	if _, err := EnsureWrappers(ctx, c, "kube-system", profile.Name); err == nil {
		t.Fatal("system namespace prepared")
	}
	// A fresh namespace gets the connection bound to the policy's route.
	if err := c.Create(ctx, namespace("team-b", nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureWrappers(ctx, c, "team-b", profile.Name); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, types.NamespacedName{Namespace: "team-b", Name: WrapperConnectionName}, &connection); err != nil || connection.Spec.Provider != "deepseek" || connection.Spec.CredentialProfile != "trial" || connection.Spec.Endpoint != "https://api.deepseek.com/chat/completions" || connection.Spec.Validate() != nil {
		t.Fatalf("connection not bound to the policy route: %+v %v", connection.Spec, err)
	}
}

// An operator-approved insecure profile yields a tenant connection that
// carries the approval and binds the policy's plain-HTTP route.
func TestTenantWrappersCarryInsecureApprovalFromProfile(t *testing.T) {
	raw := func(s string) apiextensionsv1.JSON { return apiextensionsv1.JSON{Raw: []byte(s)} }
	profile := &api.CellnRuntimeProfile{ObjectMeta: metav1.ObjectMeta{Name: "celln-native-local"}, Spec: api.CellnRuntimeProfileSpec{Revision: "v1", Native: &api.CellnNativeProvisioning{CredentialProfile: "local", Template: raw(`{"model":"qwen.gguf","url":"http://100.81.163.75:8080/v1/chat/completions","allow_insecure":true}`)}}}
	policy := &api.CellnExecutionPolicy{ObjectMeta: metav1.ObjectMeta{Name: "celln-fleet-local"}, Spec: api.CellnExecutionPolicySpec{Routes: []api.CellnExecutionPolicyRoute{{Provider: "llama-server", Protocol: "openai-chat", Models: []string{"qwen.gguf"}, EndpointOrigins: []string{"http://100.81.163.75:8080"}, Auth: "host-profile", AllowInsecure: true}}}}
	objects, err := TenantWrappers("team-a", profile, policy)
	if err != nil {
		t.Fatal(err)
	}
	connection := objects[2].(*api.ModelConnection)
	if !connection.Spec.AllowInsecure || connection.Spec.Provider != "llama-server" || connection.Spec.Validate() != nil {
		t.Fatalf("insecure approval not carried: %+v %v", connection.Spec, connection.Spec.Validate())
	}
	policy.Spec.Routes[0].AllowInsecure = false
	if _, err := TenantWrappers("team-a", profile, policy); err == nil {
		t.Fatal("policy route without insecure approval matched a plain-HTTP profile")
	}
	policy.Spec.Routes[0].AllowInsecure = true
	profile.Spec.Native.Template = raw(`{"model":"qwen.gguf","url":"http://100.81.163.75:8080/v1/chat/completions"}`)
	if _, err := TenantWrappers("team-a", profile, policy); err == nil {
		t.Fatal("plain-HTTP profile without operator approval produced a connection")
	}
}

// Each backend's wrappers are named after it, so a namespace holds one set per
// backend; the default backend keeps the original names.
func TestWrappersAreNamedPerBackend(t *testing.T) {
	ctx := context.Background()
	native, policy := catalogue(OpenSelector(SystemNamespaces()))
	raw := func(s string) apiextensionsv1.JSON { return apiextensionsv1.JSON{Raw: []byte(s)} }
	claude := &api.CellnRuntimeProfile{ObjectMeta: metav1.ObjectMeta{Name: "celln-native-trial-claude", UID: "profile-claude", Labels: map[string]string{BackendLabel: "claude"}}, Spec: api.CellnRuntimeProfileSpec{Revision: "v1", Native: &api.CellnNativeProvisioning{CredentialProfile: "trial-claude", Template: raw(`{"contract":"celln.json-tools/v1","model":"claude-test","url":"https://api.anthropic.com/v1/messages"}`)}}}
	policy.Spec.RuntimeProfiles = append(policy.Spec.RuntimeProfiles, api.CellnExecutionPolicyRuntime{Ref: api.CellnRuntimeProfileRef{Name: claude.Name, Revision: "v1"}})
	policy.Spec.Routes = append(policy.Spec.Routes, api.CellnExecutionPolicyRoute{Provider: "anthropic", Protocol: "anthropic-messages", Models: []string{"claude-test"}, EndpointOrigins: []string{"https://api.anthropic.com"}, Auth: "host-profile"})
	c := store(t, native, claude, policy, namespace("team-a", nil))
	offered, err := AuthorisedProfiles(ctx, c, "team-a")
	if err != nil || len(offered) != 2 {
		t.Fatalf("two backends not offered: %d %v", len(offered), err)
	}
	first, err := EnsureWrappers(ctx, c, "team-a", native.Name)
	if err != nil || first.Backend != DefaultBackend || first.Runtime != "celln-native" || first.Agent != "celln-agent" {
		t.Fatalf("default backend wrappers: %+v %v", first, err)
	}
	second, err := EnsureWrappers(ctx, c, "team-a", claude.Name)
	if err != nil || second.Backend != "claude" || second.Runtime != "celln-claude" || second.Agent != "celln-agent-claude" || second.Connection != "celln-claude" || len(second.Created) != 3 {
		t.Fatalf("named backend wrappers: %+v %v", second, err)
	}
	var connection api.ModelConnection
	if err := c.Get(ctx, types.NamespacedName{Namespace: "team-a", Name: "celln-claude"}, &connection); err != nil || connection.Spec.Provider != "anthropic" || connection.Spec.CredentialProfile != "trial-claude" || connection.Labels[BackendLabel] != "claude" {
		t.Fatalf("claude connection: %+v %v", connection, err)
	}
	var runtime api.AgentRuntime
	if err := c.Get(ctx, types.NamespacedName{Namespace: "team-a", Name: "celln-native"}, &runtime); err != nil || runtime.Spec.CellnProfileRef.Name != native.Name {
		t.Fatalf("default runtime wrapper still points at its profile: %v", err)
	}
}

func TestWrappersBindTheRouteOfTheProfilesProtocol(t *testing.T) {
	// Two backends on one llama-server: the same origin and model over
	// openai-chat and over anthropic-messages. Each profile names its
	// protocol and gets the route speaking it.
	_, policy := catalogue(OpenSelector(SystemNamespaces()))
	raw := func(s string) apiextensionsv1.JSON { return apiextensionsv1.JSON{Raw: []byte(s)} }
	template := raw(`{"contract":"celln.json-tools/v1","model":"qwen.gguf","url":"http://100.81.163.75:8080/v1/chat/completions","allow_insecure":true}`)
	chat := &api.CellnRuntimeProfile{ObjectMeta: metav1.ObjectMeta{Name: "celln-native-trial", Labels: map[string]string{BackendLabel: DefaultBackend}, Annotations: map[string]string{ProtocolAnnotation: "openai-chat"}}, Spec: api.CellnRuntimeProfileSpec{Revision: "v1", Native: &api.CellnNativeProvisioning{CredentialProfile: "trial", Template: template}}}
	messages := chat.DeepCopy()
	messages.Name, messages.Labels[BackendLabel], messages.Annotations[ProtocolAnnotation], messages.Spec.Native.CredentialProfile = "celln-native-trial-messages", "messages", "anthropic-messages", "trial-messages"
	messages.Spec.Native.Template = raw(`{"contract":"celln.json-tools/v1","model":"qwen.gguf","url":"http://100.81.163.75:8080/v1/messages","allow_insecure":true}`)
	policy.Spec.Routes = []api.CellnExecutionPolicyRoute{
		{Provider: "llama-server", Protocol: "openai-chat", Models: []string{"qwen.gguf"}, EndpointOrigins: []string{"http://100.81.163.75:8080"}, Auth: "host-profile", AllowInsecure: true},
		{Provider: "llama-server", Protocol: "anthropic-messages", Models: []string{"qwen.gguf"}, EndpointOrigins: []string{"http://100.81.163.75:8080"}, Auth: "host-profile", AllowInsecure: true},
	}
	for _, tc := range []struct {
		profile  *api.CellnRuntimeProfile
		protocol string
	}{{chat, "openai-chat"}, {messages, "anthropic-messages"}} {
		objects, err := TenantWrappers("team-a", tc.profile, policy)
		if err != nil {
			t.Fatal(err)
		}
		if c := objects[2].(*api.ModelConnection); c.Spec.Protocol != tc.protocol || c.Spec.CredentialProfile != tc.profile.Spec.Native.CredentialProfile {
			t.Fatalf("%s bound %s, want %s", tc.profile.Name, c.Spec.Protocol, tc.protocol)
		}
	}
	unannotated := chat.DeepCopy()
	unannotated.Annotations = nil
	objects, err := TenantWrappers("team-a", unannotated, policy)
	if err != nil || objects[2].(*api.ModelConnection).Spec.Protocol != "openai-chat" {
		t.Fatalf("an unannotated profile must keep the first matching route: %v", err)
	}
}

// An Agent that owns its backend needs only the profile's runtime wrapper:
// no shared Agent, no host-profile connection, and its own Secret-backed
// connection is left exactly as the tenant wrote it.
func TestEnsureRuntimeWrapperLeavesAnAgentsOwnConnectionAlone(t *testing.T) {
	ctx := context.Background()
	profile, policy := catalogue(OpenSelector(SystemNamespaces()))
	own := &api.ModelConnection{ObjectMeta: metav1.ObjectMeta{Name: "my-anthropic", Namespace: "team-a"}, Spec: api.ModelConnectionSpec{Provider: "anthropic", Protocol: "anthropic-messages", Endpoint: "https://api.anthropic.com/v1/messages", SecretRef: "my-key", Models: []string{"claude"}, MaxOutputTokens: 4096}}
	c := store(t, profile, policy, namespace("team-a", nil), own)
	for attempt := range 2 {
		name, err := EnsureRuntimeWrapper(ctx, c, "team-a", profile.Name)
		if err != nil || name != WrapperRuntimeName {
			t.Fatalf("attempt %d: %q %v", attempt, name, err)
		}
	}
	var wrapper api.AgentRuntime
	if err := c.Get(ctx, types.NamespacedName{Namespace: "team-a", Name: WrapperRuntimeName}, &wrapper); err != nil || wrapper.Spec.CellnProfileRef == nil || *wrapper.Spec.CellnProfileRef != (api.CellnRuntimeProfileRef{Name: profile.Name, Revision: "v1"}) || wrapper.Spec.Celln != nil || wrapper.Spec.Model != nil {
		t.Fatalf("runtime wrapper: %+v %v", wrapper.Spec, err)
	}
	var connections api.ModelConnectionList
	if err := c.List(ctx, &connections, client.InNamespace("team-a")); err != nil || len(connections.Items) != 1 || connections.Items[0].Name != "my-anthropic" || connections.Items[0].Spec.SecretRef != "my-key" || connections.Items[0].Spec.CredentialProfile != "" {
		t.Fatalf("connections after ensuring the runtime wrapper: %+v %v", connections.Items, err)
	}
	var agents api.AgentList
	if err := c.List(ctx, &agents, client.InNamespace("team-a")); err != nil || len(agents.Items) != 0 {
		t.Fatalf("a shared Agent was created: %v", err)
	}
	// The backend's own wrappers may still be added later; they reuse the
	// runtime and never touch the Agent's connection.
	got, err := EnsureWrappers(ctx, c, "team-a", profile.Name)
	if err != nil || strings.Join(got.Created, ",") != "celln-agent,celln-native" {
		t.Fatalf("backend wrappers after the runtime wrapper: %+v %v", got, err)
	}
	var kept api.ModelConnection
	if err := c.Get(ctx, types.NamespacedName{Namespace: "team-a", Name: "my-anthropic"}, &kept); err != nil || kept.Spec.SecretRef != "my-key" || kept.Spec.MaxOutputTokens != 4096 {
		t.Fatalf("the Agent's own connection changed: %+v %v", kept.Spec, err)
	}
	for _, refused := range []struct{ namespace, profile string }{{"team-a", "other-profile"}, {"kube-system", profile.Name}} {
		if _, err := EnsureRuntimeWrapper(ctx, c, refused.namespace, refused.profile); err == nil {
			t.Fatalf("runtime wrapper prepared for %+v", refused)
		}
	}
}
