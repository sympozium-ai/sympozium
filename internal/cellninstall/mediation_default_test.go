package cellninstall

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnplatform"
	"github.com/sympozium-ai/sympozium/internal/modelkey"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// The installer renders the key id into the chart before the trust exists,
// then bootstraps under that id; every later run reads it back and never
// rotates it.
func TestMediationTrustUnderAChosenKeyID(t *testing.T) {
	ctx := context.Background()
	if _, found, err := ExistingMediationKeyID(ctx, extraStore(t), "sympozium-system"); found || err != nil {
		t.Fatalf("a fresh cluster (no namespaces) has trust: %v %v", found, err)
	}
	store := mediationStore(t)
	if _, found, err := ExistingMediationKeyID(ctx, store, "sympozium-system"); found || err != nil {
		t.Fatalf("no trust yet: %v %v", found, err)
	}
	o := mediationOptions()
	o.KeyID = "mediation-chosen-1"
	trust, err := PrepareMediationTrust(ctx, store, o)
	if err != nil || !trust.Created || trust.KeyID != "mediation-chosen-1" {
		t.Fatalf("bootstrap: %+v %v", trust, err)
	}
	keyID, found, err := ExistingMediationKeyID(ctx, store, "sympozium-system")
	if err != nil || !found || keyID != "mediation-chosen-1" {
		t.Fatalf("read back: %q %v %v", keyID, found, err)
	}
	again, err := PrepareMediationTrust(ctx, store, o)
	if err != nil || again.Created || again.KeyID != keyID {
		t.Fatalf("rerun: %+v %v", again, err)
	}
	other := o
	other.KeyID = "mediation-other"
	if _, err := PrepareMediationTrust(ctx, store, other); err == nil || !strings.Contains(err.Error(), "never rotated silently") {
		t.Fatalf("another key id was accepted: %v", err)
	}
	bad := o
	bad.KeyID = "has space"
	if _, err := PrepareMediationTrust(ctx, mediationStore(t), bad); err == nil {
		t.Fatal("an invalid key id was accepted")
	}

	// Partial trust is reported, never completed.
	partial := mediationStore(t, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "sympozium-system", Name: MediationControllerSecret}})
	if _, _, err := ExistingMediationKeyID(ctx, partial, "sympozium-system"); err == nil || !strings.Contains(err.Error(), "partially present") {
		t.Fatalf("partial trust: %v", err)
	}
}

func TestMediationCertificatesLastTenYears(t *testing.T) {
	ctx := context.Background()
	store := mediationStore(t)
	now := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	o := mediationOptions()
	o.Now = func() time.Time { return now }
	if _, err := PrepareMediationTrust(ctx, store, o); err != nil {
		t.Fatal(err)
	}
	var trust corev1.ConfigMap
	var gateway, node corev1.Secret
	for key, object := range map[types.NamespacedName]client.Object{
		{Namespace: "sympozium-system", Name: MediationTrustConfigMap}: &trust,
		{Namespace: "sympozium-system", Name: MediationGatewaySecret}:  &gateway,
		{Namespace: fleetNamespace, Name: MediationNodeSecret}:         &node,
	} {
		if err := store.Get(ctx, key, object); err != nil {
			t.Fatal(err)
		}
	}
	expiry, ok := MediationCertificateExpiry([]byte(trust.Data["ca.crt"]), gateway.Data["tls.crt"], node.Data["tls.crt"])
	if !ok || !expiry.Equal(now.Add(DefaultMediationValidity)) {
		t.Fatalf("expiry %v %v, want %v", expiry, ok, now.Add(DefaultMediationValidity))
	}
	if DefaultMediationValidity < 10*365*24*time.Hour {
		t.Fatal("the default lifetime is shorter than ten years")
	}
	if _, ok := MediationCertificateExpiry([]byte("not a certificate")); ok {
		t.Fatal("garbage parsed as a certificate")
	}
}

func TestAutoMediationValues(t *testing.T) {
	image := "ghcr.io/sympozium-ai/sympozium/model-gateway@sha256:" + strings.Repeat("a", 64)
	values, err := AutoMediationValues("0b9c-uid", "mediation-2026-10-05-01020304", image)
	want := []string{"celln.mediation.enabled=true", "celln.mediation.clusterId=0b9c-uid", "celln.mediation.issuer.keyId=mediation-2026-10-05-01020304", "celln.mediation.mediateBackends=true", "modelGateway.image=" + image}
	if err != nil || !slices.Equal(values, want) {
		t.Fatalf("values = %q %v", values, err)
	}
	for _, tc := range []struct{ cluster, key, image string }{
		{"", "k", image},
		{"a,b", "k", image},
		{"c", "bad key", image},
		{"c", "k", "ghcr.io/sympozium-ai/sympozium/model-gateway:v1"},
	} {
		if _, err := AutoMediationValues(tc.cluster, tc.key, tc.image); err == nil {
			t.Fatalf("accepted %+v", tc)
		}
	}
}

func resolvedBackend(t *testing.T, name string, m FleetModel) FleetBackend {
	t.Helper()
	resolved, err := m.Resolve(name)
	if err != nil {
		t.Fatal(err)
	}
	return FleetBackend{Name: name, Model: resolved}
}

func TestPlanMediatedBackends(t *testing.T) {
	ctx := context.Background()
	backends := []FleetBackend{
		resolvedBackend(t, "native", FleetModel{Provider: ModelProviderDeepSeek}),
		resolvedBackend(t, "claude", FleetModel{Provider: ModelProviderAnthropic, Name: "claude-test"}),
		resolvedBackend(t, "lan", FleetModel{Provider: ModelProviderLlamaServer, Name: "local", Endpoint: "http://10.0.0.7:8080", AllowInsecure: true}),
		resolvedBackend(t, "ported", FleetModel{Provider: "custom", Protocol: "openai-chat", Name: "m", Endpoint: "https://models.example.com:8443/v1/chat/completions", AllowInsecure: true}),
		resolvedBackend(t, "again", FleetModel{Provider: ModelProviderOpenAI, Name: "gpt-test"}),
	}
	store := extraStore(t, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: fleetNamespace, Name: FleetModelCredentialSecret}, Data: map[string][]byte{
		"claude": []byte("an-earlier-install-published-this-key"),
		"again":  []byte(MediatedBackendCredential),
	}})
	plan, err := PlanMediatedBackends(ctx, store, backends)
	if err != nil {
		t.Fatal(err)
	}
	if got := SortedNames(plan.Mediated); !slices.Equal(got, []string{"again", "native"}) {
		t.Fatalf("mediated = %v", got)
	}
	if !slices.Equal(plan.FleetKeyed, []string{"claude"}) || !slices.Equal(plan.Unmediable, []string{"ported"}) {
		t.Fatalf("plan = %+v", plan)
	}
}

// With mediation the fleet Secret carries a marker for a mediated backend;
// the provider key never reaches it.
func TestPublishBackendCredentialsKeepsMediatedKeysOffTheFleet(t *testing.T) {
	ctx := context.Background()
	key := "sk-installer-key-0123456789abcdef"
	file := filepath.Join(t.TempDir(), "native")
	if err := os.WriteFile(file, []byte(key+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	native := resolvedBackend(t, "native", FleetModel{Provider: ModelProviderDeepSeek})
	native.CredentialFile = file
	lan := resolvedBackend(t, "lan", FleetModel{Provider: ModelProviderLlamaServer, Name: "local", Endpoint: "http://10.0.0.7:8080", AllowInsecure: true})
	store := extraStore(t)
	for range 2 {
		if err := PublishBackendCredentials(ctx, store, []FleetBackend{native, lan}, map[string]bool{"native": true}); err != nil {
			t.Fatal(err)
		}
	}
	var secret corev1.Secret
	if err := store.Get(ctx, types.NamespacedName{Namespace: fleetNamespace, Name: FleetModelCredentialSecret}, &secret); err != nil {
		t.Fatal(err)
	}
	if string(secret.Data["native"]) != MediatedBackendCredential || string(secret.Data["lan"]) != fleetModelPlaceholderCredential {
		t.Fatalf("fleet credentials = %q", secret.Data)
	}
	for name, value := range secret.Data {
		if strings.Contains(string(value), key) {
			t.Fatalf("the installer's key reached the fleet under %s", name)
		}
	}
	mediated, err := MediatedOnlyBackends(ctx, store)
	if err != nil || !mediated["native"] || mediated["lan"] {
		t.Fatalf("read back: %v %v", mediated, err)
	}
	// Without mediation (opt-out) the key is published as before.
	plain := extraStore(t)
	if err := PublishBackendCredentials(ctx, plain, []FleetBackend{native}, nil); err != nil {
		t.Fatal(err)
	}
	if err := plain.Get(ctx, types.NamespacedName{Namespace: fleetNamespace, Name: FleetModelCredentialSecret}, &secret); err != nil || string(secret.Data["native"]) != key {
		t.Fatalf("opt-out publication: %q %v", secret.Data, err)
	}
}

// A mediation-only backend is offered only as an auth "secret" route, gets no
// shared Agent or host-profile connection, and its sample run is the starter
// Agent's.
func TestInstallPlatformMediationOnlyBackend(t *testing.T) {
	ctx := context.Background()
	dir, packageHash, principal := starterConfiguration(t)
	store := platformInstallStore(t)
	if err := store.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: fleetNamespace}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: fleetNamespace, Name: FleetModelCredentialSecret}, Data: map[string][]byte{"native": []byte(MediatedBackendCredential), "claude": []byte("a-fleet-key-from-an-earlier-install")}}); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "out")
	o := PlatformOptions{Namespace: "tenant-a", ConfigurationDir: dir, OutputDir: out, Scope: "trial", ClusterID: "cluster-uid", PackageHash: packageHash, Principal: principal, ControllerNamespace: "sympozium-system"}
	for range 2 {
		if err := InstallPlatform(ctx, store, o); err != nil {
			t.Fatal(err)
		}
	}
	_, policyName, _ := PlatformCatalogueNames("trial")
	var policy api.CellnExecutionPolicy
	if err := store.Get(ctx, types.NamespacedName{Name: policyName}, &policy); err != nil {
		t.Fatal(err)
	}
	var hostProfile, secret []string
	for _, r := range policy.Spec.Routes {
		switch r.Auth {
		case "host-profile":
			hostProfile = append(hostProfile, r.Provider)
		case "secret":
			secret = append(secret, r.Provider)
		}
	}
	if !slices.Equal(hostProfile, []string{"anthropic"}) || !slices.Equal(secret, []string{"deepseek"}) {
		t.Fatalf("host-profile %v, secret %v: %+v", hostProfile, secret, policy.Spec.Routes)
	}
	if len(policy.Spec.RuntimeProfiles) != 2 {
		t.Fatalf("the mediation-only backend's runtime is not in the policy: %+v", policy.Spec.RuntimeProfiles)
	}
	var profile api.CellnRuntimeProfile
	if err := store.Get(ctx, types.NamespacedName{Name: PlatformProfileName("trial", "native")}, &profile); err != nil || !cellnplatform.MediationOnly(&profile) {
		t.Fatalf("profile label: %v %v", profile.Labels, err)
	}
	native := cellnplatform.WrapperNames("native")
	var runtime api.AgentRuntime
	if err := store.Get(ctx, types.NamespacedName{Namespace: "tenant-a", Name: native.Runtime}, &runtime); err != nil {
		t.Fatalf("runtime wrapper: %v", err)
	}
	if err := store.Get(ctx, types.NamespacedName{Namespace: "tenant-a", Name: native.Agent}, &api.Agent{}); !apierrors.IsNotFound(err) {
		t.Fatalf("a shared Agent was created for a mediation-only backend: %v", err)
	}
	if err := store.Get(ctx, types.NamespacedName{Namespace: "tenant-a", Name: native.Connection}, &api.ModelConnection{}); !apierrors.IsNotFound(err) {
		t.Fatalf("a host-profile connection was created for a mediation-only backend: %v", err)
	}
	claude := cellnplatform.WrapperNames("claude")
	if err := store.Get(ctx, types.NamespacedName{Namespace: "tenant-a", Name: claude.Agent}, &api.Agent{}); err != nil {
		t.Fatalf("the fleet-keyed backend lost its wrappers: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(out, "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	var run api.AgentRun
	if err := json.Unmarshal(raw, &run); err != nil || run.Spec.AgentRef != StarterAgentName || run.Spec.Model.ConnectionRef != StarterAgentName || len(run.Spec.CellnSelection.ClusterToolRefs) != 0 {
		t.Fatalf("sample run: %+v %v", run.Spec, err)
	}
	// A starter Agent created elsewhere before the profile existed (as the API
	// server creates an added backend's) is bound to it once it is admitted.
	starter := StarterAgentOptions{Namespace: "tenant-b", Backend: resolvedBackend(t, "native", FleetModel{Provider: ModelProviderDeepSeek}), Credential: "sk-starter-0123456789abcdef0123", Runtime: native.Runtime}
	if _, err := EnsureStarterAgent(ctx, store, starter); err != nil {
		t.Fatal(err)
	}
	if bound, err := EnsureStarterRuntimeWrappers(ctx, store, "trial", "native"); err != nil || !slices.Equal(bound, []string{"tenant-b"}) {
		t.Fatalf("starter runtime wrappers: %v %v", bound, err)
	}
	if err := store.Get(ctx, types.NamespacedName{Namespace: "tenant-b", Name: native.Runtime}, &api.AgentRuntime{}); err != nil {
		t.Fatalf("starter Agent's runtime wrapper: %v", err)
	}
	// A namespace asking for the profile's runtime gets it, as for any other.
	if _, err := cellnplatform.EnsureRuntimeWrapper(ctx, store, "tenant-b", profile.Name); err != nil {
		t.Fatal(err)
	}
}

func TestStarterAgentObjects(t *testing.T) {
	key := "sk-starter-0123456789abcdef0123"
	for _, tc := range []struct {
		backend FleetBackend
		keyName string
		names   StarterAgentNames
	}{
		{resolvedBackend(t, "native", FleetModel{Provider: ModelProviderDeepSeek}), "OPENAI_API_KEY", StarterAgentNames{"starter", "starter-model-key", "starter"}},
		{resolvedBackend(t, "claude", FleetModel{Provider: ModelProviderAnthropic, Name: "claude-test", MaxOutputTokens: 2048, Parameters: map[string]any{"temperature": 0.2}}), "ANTHROPIC_API_KEY", StarterAgentNames{"starter-claude", "starter-claude-model-key", "starter-claude"}},
	} {
		secret, connection, agent, err := StarterAgentObjects(StarterAgentOptions{Namespace: "default", Backend: tc.backend, Credential: key, Runtime: "celln-native"})
		if err != nil {
			t.Fatal(err)
		}
		m := tc.backend.Model
		if secret.Name != tc.names.Secret || string(secret.Data[tc.keyName]) != key || len(secret.Data) != 1 || secret.Annotations[modelkey.OwnerAnnotation] != "Agent/"+tc.names.Agent {
			t.Fatalf("secret %+v", secret)
		}
		if connection.Name != tc.names.Connection || connection.Spec.SecretRef != secret.Name || connection.Spec.CredentialProfile != "" || connection.Spec.Provider != m.Provider || connection.Spec.Protocol != m.Protocol || connection.Spec.Endpoint != m.Endpoint || !slices.Equal(connection.Spec.Models, []string{m.Name}) {
			t.Fatalf("connection %+v", connection.Spec)
		}
		if tc.backend.Name == "claude" && (connection.Spec.MaxOutputTokens != 2048 || connection.Spec.Parameters == nil) {
			t.Fatalf("backend parameters not carried: %+v", connection.Spec)
		}
		e := agent.Spec.Execution
		if agent.Name != tc.names.Agent || !modelkey.Grants(agent, m.Provider, secret.Name) || agent.Spec.RuntimeRef != "celln-native" || e == nil || e.Backend != "celln" || e.ModelConnectionRef != connection.Name || e.Model != m.Name || e.CellnSelection == nil || e.CellnSelection.ToolRefs == nil || len(e.CellnSelection.ToolRefs) != 0 || e.Validate() != "" {
			t.Fatalf("agent %+v", agent.Spec)
		}
	}
	native := resolvedBackend(t, "native", FleetModel{Provider: ModelProviderDeepSeek})
	for _, o := range []StarterAgentOptions{
		{Namespace: "default", Backend: native, Credential: "", Runtime: "celln-native"},
		{Namespace: "default", Backend: native, Credential: MediatedBackendCredential, Runtime: "celln-native"},
		{Namespace: "default", Backend: native, Credential: "two words", Runtime: "celln-native"},
		{Namespace: "Not_A_Namespace", Backend: native, Credential: key, Runtime: "celln-native"},
		{Namespace: "default", Backend: resolvedBackend(t, "lan", FleetModel{Provider: ModelProviderLlamaServer, Name: "local", Endpoint: "http://10.0.0.7:8080", AllowInsecure: true}), Credential: key, Runtime: "celln-lan"},
	} {
		if _, _, _, err := StarterAgentObjects(o); err == nil {
			t.Fatalf("accepted %+v", o)
		}
	}
}

func TestEnsureStarterAgentNeverReplacesAKey(t *testing.T) {
	ctx := context.Background()
	key := "sk-starter-0123456789abcdef0123"
	o := StarterAgentOptions{Namespace: "default", Backend: resolvedBackend(t, "native", FleetModel{Provider: ModelProviderDeepSeek}), Credential: key, Runtime: "celln-native"}
	store := extraStore(t, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}})
	created, err := EnsureStarterAgent(ctx, store, o)
	if err != nil || !slices.Equal(created, []string{"starter-model-key", "starter", "starter"}) {
		t.Fatalf("created %v %v", created, err)
	}
	// The owner may change the Agent; a rerun keeps it as it is.
	var agent api.Agent
	if err := store.Get(ctx, types.NamespacedName{Namespace: "default", Name: "starter"}, &agent); err != nil {
		t.Fatal(err)
	}
	agent.Spec.Agents.Default.Model = "edited"
	if err := store.Update(ctx, &agent); err != nil {
		t.Fatal(err)
	}
	if created, err := EnsureStarterAgent(ctx, store, o); err != nil || len(created) != 0 {
		t.Fatalf("rerun: %v %v", created, err)
	}
	if err := store.Get(ctx, types.NamespacedName{Namespace: "default", Name: "starter"}, &agent); err != nil || agent.Spec.Agents.Default.Model != "edited" {
		t.Fatalf("the rerun rewrote the Agent: %v", err)
	}
	if got, err := StarterAgentCredential(ctx, store, "default", o.Backend); err != nil || got != key {
		t.Fatalf("read back %q %v", got, err)
	}
	other := o
	other.Credential = "sk-another-key-0123456789abcdef"
	if _, err := EnsureStarterAgent(ctx, store, other); !errors.Is(err, ErrStarterKeyTaken) || !strings.Contains(err.Error(), "never replaces an Agent's key") {
		t.Fatalf("a different key was accepted: %v", err)
	}
	// A Secret another live Agent owns is never taken.
	taken := extraStore(t,
		&api.Agent{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "someone"}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "starter-model-key", Annotations: map[string]string{modelkey.OwnerAnnotation: "Agent/someone"}}, Data: map[string][]byte{"OPENAI_API_KEY": []byte(key)}})
	if _, err := EnsureStarterAgent(ctx, taken, o); !errors.Is(err, ErrStarterKeyTaken) || !strings.Contains(err.Error(), "belongs to Agent/someone") {
		t.Fatalf("another Agent's key was taken: %v", err)
	}
}
