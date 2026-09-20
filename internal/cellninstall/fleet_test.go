package cellninstall

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnparent"
	"github.com/zeebo/blake3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func fleetStore() client.Client {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	return fake.NewClientBuilder().WithScheme(scheme).Build()
}

func validFleet() FleetOptions {
	return FleetOptions{Scope: "starter", Principal: "sympozium:celln", Publisher: "ed25519:operator", PackageImage: "registry.example/celln/starter@sha256:" + strings.Repeat("a", 64), PackageHash: "blake3:" + strings.Repeat("b", 64)}
}

func TestFleetValuesRefuseAmbiguousOrUnpinnedInputs(t *testing.T) {
	values, err := FleetValues(validFleet())
	if err != nil || !strings.Contains(strings.Join(values, "\n"), "celln.fleet.scope=starter") || !strings.Contains(strings.Join(values, "\n"), "celln.router.backends=null") {
		t.Fatalf("valid fleet refused: %v %v", err, values)
	}
	for name, change := range map[string]func(*FleetOptions){
		"scope":             func(o *FleetOptions) { o.Scope = "Starter" },
		"tag":               func(o *FleetOptions) { o.PackageImage = "registry.example/celln/starter:latest" },
		"hash":              func(o *FleetOptions) { o.PackageHash = "sha256:" + strings.Repeat("b", 64) },
		"publisher":         func(o *FleetOptions) { o.Publisher = "" },
		"comma":             func(o *FleetOptions) { o.Publisher = "a,b" },
		"principal":         func(o *FleetOptions) { o.Principal = "sympozium celln" },
		"relative key file": func(o *FleetOptions) { o.ModelCredentialFile = "etc/token" },
		"bad backend name": func(o *FleetOptions) {
			o.Backends = []FleetBackend{{Name: "Claude", Model: FleetModel{Provider: "anthropic", Name: "m"}}}
		},
		"duplicate backends": func(o *FleetOptions) {
			o.Backends = []FleetBackend{{Name: "a", Model: FleetModel{}}, {Name: "a", Model: FleetModel{}}}
		},
		"backend without model": func(o *FleetOptions) {
			o.Backends = []FleetBackend{{Name: "openai", Model: FleetModel{Provider: "openai"}}}
		},
		"uppercase https host": func(o *FleetOptions) { o.HTTPSHosts = []string{"Hooks.Example"} },
		"bare https host":      func(o *FleetOptions) { o.HTTPSHosts = []string{"localhost"} },
		"https host with port": func(o *FleetOptions) { o.HTTPSHosts = []string{"hooks.example:8080"} },
	} {
		o := validFleet()
		change(&o)
		if _, err := FleetValues(o); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestFleetValuesCarryTheHTTPSHostsInOrder(t *testing.T) {
	o := validFleet()
	o.HTTPSHosts = []string{"hooks.example", "echo.tenant.svc.cluster.local"}
	values, err := FleetValues(o)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(values, "\n")
	if !strings.Contains(joined, "celln.fleet.httpsHosts[0]=hooks.example") || !strings.Contains(joined, "celln.fleet.httpsHosts[1]=echo.tenant.svc.cluster.local") {
		t.Fatalf("hosts not rendered: %v", values)
	}
	if values, err := FleetValues(validFleet()); err != nil || strings.Contains(strings.Join(values, "\n"), "httpsHosts") {
		t.Fatalf("empty hosts must leave the reviewed default: %v %v", err, values)
	}
}

func TestPrepareFleetTrustPublishesHashOnlyPolicyOnce(t *testing.T) {
	ctx := context.Background()
	store := fleetStore()
	if err := PrepareFleetTrust(ctx, store, "sympozium:celln"); err != nil {
		t.Fatal(err)
	}
	var secret corev1.Secret
	var policy corev1.ConfigMap
	if err := store.Get(ctx, types.NamespacedName{Namespace: "celln-system", Name: FleetParentTokenSecret}, &secret); err != nil {
		t.Fatal(err)
	}
	if err := store.Get(ctx, types.NamespacedName{Namespace: "celln-system", Name: FleetParentClientsConfigMap}, &policy); err != nil {
		t.Fatal(err)
	}
	token := string(secret.Data["token"])
	document := policy.Data["trusted-parent-clients.json"]
	if len(token) < 24 || strings.Contains(document, token) || !strings.Contains(document, fmt.Sprintf("blake3:%x", blake3.Sum256([]byte(token)))) || !strings.Contains(document, `"principal":"sympozium:celln"`) {
		t.Fatalf("policy must carry only the credential hash: %s", document)
	}
	if err := PrepareFleetTrust(ctx, store, "sympozium:celln"); err != nil {
		t.Fatalf("matching existing trust refused: %v", err)
	}
	if err := PrepareFleetTrust(ctx, store, "another:principal"); err == nil {
		t.Fatal("principal silently rotated")
	}
	if err := store.Delete(ctx, &policy); err != nil {
		t.Fatal(err)
	}
	if err := PrepareFleetTrust(ctx, store, "sympozium:celln"); err == nil {
		t.Fatal("credential without policy accepted")
	}
}

func TestPublishFleetModelCredentialKeepsExistingSecret(t *testing.T) {
	ctx := context.Background()
	store := fleetStore()
	path := filepath.Join(t.TempDir(), "model-token")
	if err := PublishFleetModelCredential(ctx, store, path, FleetModel{}); err == nil {
		t.Fatal("missing file accepted")
	}
	if err := os.WriteFile(path, []byte("sk-test-model-credential-0001\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := PublishFleetModelCredential(ctx, store, path, FleetModel{}); err != nil {
		t.Fatal(err)
	}
	var secret corev1.Secret
	if err := store.Get(ctx, types.NamespacedName{Namespace: "celln-system", Name: FleetModelCredentialSecret}, &secret); err != nil || string(secret.Data["native"]) != "sk-test-model-credential-0001" {
		t.Fatalf("credential not published verbatim under the backend's name: %v %q", err, secret.Data)
	}
	if err := PublishFleetModelCredential(ctx, store, path, FleetModel{}); err != nil {
		t.Fatalf("identical rerun refused: %v", err)
	}
	if err := os.WriteFile(path, []byte("sk-rotated-model-credential-01\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := PublishFleetModelCredential(ctx, store, path, FleetModel{}); err == nil {
		t.Fatal("existing credential replaced")
	}
	if err := PublishFleetModelCredential(ctx, store, "", FleetModel{}); err != nil {
		t.Fatalf("existing credential not kept: %v", err)
	}
}

func TestReadFleetConfigurationWaitsForNodesAndRefusesDrift(t *testing.T) {
	ctx := context.Background()
	store := fleetStore()
	dir := filepath.Join(t.TempDir(), "configuration")
	if published, err := ReadFleetConfiguration(ctx, store, dir); err != nil || published {
		t.Fatalf("unpublished configuration reported: %v %v", published, err)
	}
	data := map[string]string{"catalogue.json": `{"tools":[]}`, "configured.json": `{"apiVersion":"celln.native-starter-configured/v1"}`, "native-template.json": `{"maxTurns":12}`}
	if err := store.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: FleetConfigurationConfigMap, Namespace: "celln-system"}, Data: map[string]string{"catalogue.json": data["catalogue.json"]}}); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFleetConfiguration(ctx, store, dir); err == nil {
		t.Fatal("incomplete publication accepted")
	}
	var published corev1.ConfigMap
	_ = store.Get(ctx, types.NamespacedName{Namespace: "celln-system", Name: FleetConfigurationConfigMap}, &published)
	published.Data = data
	if err := store.Update(ctx, &published); err != nil {
		t.Fatal(err)
	}
	if ok, err := ReadFleetConfiguration(ctx, store, dir); err != nil || !ok {
		t.Fatalf("published configuration not materialized: %v", err)
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "native", "native-template.json")); err != nil || string(raw) != data["native-template.json"] {
		t.Fatal("materialized bytes differ")
	}
	if ok, err := ReadFleetConfiguration(ctx, store, dir); err != nil || !ok {
		t.Fatalf("identical rerun refused: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "native", "catalogue.json"), []byte(`{"tools":["drift"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFleetConfiguration(ctx, store, dir); err == nil {
		t.Fatal("drifted local configuration accepted")
	}
}

func TestConfigureFleetRebindsIssuanceToGateway(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	o := Options{OutputDir: dir, ControllerNamespace: "sympozium-system", OwnerTarget: ManagedRouterURL, StatePath: "/var/lib/sympozium-celln/starter"}
	registration := cellnparent.RegistrationConfig{APIVersion: "sympozium.ai/celln-parent-registrations-v1", Journal: "/var/lib/sympozium-celln/starter/journal", Approvals: "/var/lib/sympozium-celln/starter/approvals", LocalProvisioner: &cellnparent.LocalProvisioner{Binary: "/usr/local/bin/celln", Root: "/var/lib/sympozium-celln/starter/authority", Journal: "/var/lib/sympozium-celln/starter/journal", Approvals: "/var/lib/sympozium-celln/starter/approvals", Target: ManagedRouterURL, TokenFile: "/etc/sympozium/celln-parent/owner-token"}, HostTemplates: []cellnparent.HostProvisionTemplate{{Principal: "sympozium:celln"}}}
	write := func() {
		t.Helper()
		raw, _ := json.Marshal(registration)
		if err := os.WriteFile(filepath.Join(dir, "registrations.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	store := fleetStore()
	different := o
	different.OwnerTarget = "http://another-owner:8787"
	if _, err := ConfigureFleet(ctx, store, different); err == nil {
		t.Fatal("non-gateway owner accepted")
	}
	values, err := ConfigureFleet(ctx, store, o)
	if err != nil || len(values) != 1 || values[0] != "celln.fleet.parentConfigSecret="+FleetParentConfigSecret {
		t.Fatalf("fleet wiring: %v %v", err, values)
	}
	var secret corev1.Secret
	if err := store.Get(ctx, types.NamespacedName{Namespace: o.ControllerNamespace, Name: FleetParentConfigSecret}, &secret); err != nil {
		t.Fatal(err)
	}
	var published cellnparent.RegistrationConfig
	if err := json.Unmarshal(secret.Data["registrations.json"], &published); err != nil {
		t.Fatal(err)
	}
	remote := published.RemoteProvisioner
	if published.LocalProvisioner != nil || remote == nil || remote.Target != ManagedRouterURL || remote.TokenFile != "/etc/sympozium/celln/token" || remote.CAFile != "" || remote.Journal != FleetJournalRoot+"/journal" || published.Journal != remote.Journal || published.Approvals != FleetJournalRoot+"/approvals" || remote.Approvals != published.Approvals || len(published.HostTemplates) != 1 {
		t.Fatalf("controller wiring did not move issuance to the gateway: %+v", published)
	}
	// A rerun keeps identical wiring; different wiring is never replaced.
	if again, err := ConfigureFleet(ctx, store, o); err != nil || strings.Join(again, ",") != strings.Join(values, ",") {
		t.Fatalf("identical rerun did not keep the wiring: %v %v", err, again)
	}
	if wired, err := ExistingFleetWiring(ctx, store, o.ControllerNamespace); err != nil || !wired {
		t.Fatalf("existing wiring not detected: %v", err)
	}
	secret.Data["registrations.json"] = []byte(`{"apiVersion":"other"}`)
	if err := store.Update(ctx, &secret); err != nil {
		t.Fatal(err)
	}
	if _, err := ConfigureFleet(ctx, store, o); err == nil {
		t.Fatal("existing wiring replaced")
	}
	registration.LocalProvisioner = nil
	write()
	if _, err := ConfigureFleet(ctx, fleetStore(), o); err == nil {
		t.Fatal("registration without local starter issuance accepted")
	}
}

func TestFleetModelPresetsAndRefusals(t *testing.T) {
	ok := map[string]FleetModel{
		"deepseek default": {},
		"openai":           {Provider: ModelProviderOpenAI, Name: "gpt-test"},
		"anthropic":        {Provider: ModelProviderAnthropic, Name: "claude-test"},
		"llama-server":     {Provider: ModelProviderLlamaServer, Name: "qwen.gguf", Endpoint: "http://100.81.163.75:8080/v1/chat/completions", AllowInsecure: true},
		"custom gateway":   {Provider: "litellm", Name: "m", Endpoint: "https://gateway.example/v1/chat/completions", Protocol: "openai-chat"},
	}
	want := map[string][3]string{
		"deepseek default": {"deepseek", "openai-chat", "https://api.deepseek.com/chat/completions"},
		"openai":           {"openai", "openai-chat", "https://api.openai.com/v1/chat/completions"},
		"anthropic":        {"anthropic", "anthropic-messages", "https://api.anthropic.com/v1/messages"},
		"llama-server":     {"llama-server", "openai-chat", "http://100.81.163.75:8080/v1/chat/completions"},
		"custom gateway":   {"litellm", "openai-chat", "https://gateway.example/v1/chat/completions"},
	}
	for name, m := range ok {
		got, err := m.Resolve("trial")
		if err != nil || [3]string{got.Provider, got.Protocol, got.Endpoint} != want[name] || got.Name == "" {
			t.Fatalf("%s: %+v %v", name, got, err)
		}
	}
	for name, m := range map[string]FleetModel{
		"openai without model":      {Provider: ModelProviderOpenAI},
		"llama without endpoint":    {Provider: ModelProviderLlamaServer, Name: "q"},
		"plain http unapproved":     {Provider: ModelProviderLlamaServer, Name: "q", Endpoint: "http://10.0.0.5:8080/v1/chat/completions"},
		"custom without protocol":   {Provider: "litellm", Name: "m", Endpoint: "https://gateway.example/v1/chat/completions"},
		"unsupported protocol":      {Provider: "litellm", Name: "m", Endpoint: "https://gateway.example/v1/x", Protocol: "gemini"},
		"endpoint with credentials": {Provider: ModelProviderOpenAI, Name: "m", Endpoint: "https://user:pw@api.openai.com/v1/chat/completions"},
		"comma breaks helm --set":   {Provider: ModelProviderOpenAI, Name: "a,b"},
	} {
		if _, err := m.Resolve("trial"); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	o := validFleet()
	o.Model = ok["llama-server"]
	values, err := FleetValues(o)
	joined := strings.Join(values, "\n")
	if err != nil || !strings.Contains(joined, "celln.fleet.backends[0].endpoint=http://100.81.163.75:8080/v1/chat/completions") || !strings.Contains(joined, "celln.fleet.backends[0].allowInsecure=true") || !strings.Contains(joined, "celln.fleet.backends[0].model=qwen.gguf") || !strings.Contains(joined, "celln.fleet.backends[0].name=native") || strings.Contains(joined, "credential") {
		t.Fatalf("model route not rendered into values: %v %s", err, joined)
	}
	o.Model = FleetModel{Provider: ModelProviderOpenAI}
	if _, err := FleetValues(o); err == nil {
		t.Fatal("fleet values rendered without a model name")
	}
}

func TestKeylessModelPublishesPlaceholderAndShortKeysAreRefused(t *testing.T) {
	ctx := context.Background()
	store := fleetStore()
	llama := FleetModel{Provider: ModelProviderLlamaServer}
	if err := PublishFleetModelCredential(ctx, store, "", FleetModel{Provider: ModelProviderOpenAI}); err == nil {
		t.Fatal("a provider that needs a key was installed without one")
	}
	if err := PublishFleetModelCredential(ctx, store, "", llama); err != nil {
		t.Fatal(err)
	}
	var secret corev1.Secret
	if err := store.Get(ctx, types.NamespacedName{Namespace: "celln-system", Name: FleetModelCredentialSecret}, &secret); err != nil || len(secret.Data["native"]) < 24 {
		t.Fatalf("keyless backend placeholder missing or too short for Celln: %v %q", err, secret.Data)
	}
	short := filepath.Join(t.TempDir(), "short")
	if err := os.WriteFile(short, []byte("sk-short\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := PublishFleetModelCredential(ctx, fleetStore(), short, FleetModel{}); err == nil {
		t.Fatal("credential Celln would refuse was published")
	}
}

func TestFleetLimitsDefaultToLongRunningAndAreBounded(t *testing.T) {
	got, err := FleetLimits{}.Resolve()
	if err != nil || got != DefaultFleetLimits || got.LeaseSeconds != 86400 {
		t.Fatalf("defaults: %+v %v", got, err)
	}
	got, err = FleetLimits{LeaseSeconds: 3600}.Resolve()
	if err != nil || got.LeaseSeconds != 3600 || got.MaxTurns != DefaultFleetLimits.MaxTurns {
		t.Fatalf("partial override: %+v %v", got, err)
	}
	// Every turn reserves 6 requests and 3072 tokens, so the defaults afford
	// exactly their 256 turns.
	if got.MaxTurns != 256 || got.MaxModelRequests != 1536 || got.MaxOutputTokens != 786432 || TurnsAfforded(got.MaxModelRequests, got.MaxOutputTokens, api.TurnModelRequests, api.TurnOutputTokens) != got.MaxTurns {
		t.Fatalf("defaults do not afford their turns: %+v", got)
	}
	for name, l := range map[string]FleetLimits{"lease too long": {LeaseSeconds: 86401}, "lease too short": {LeaseSeconds: 30}, "no turns": {MaxTurns: -1}, "tokens below one turn": {MaxOutputTokens: 3071}, "requests below one turn": {MaxModelRequests: 5}, "the old one-turn minimum": {MaxModelRequests: 3, MaxOutputTokens: 1536}, "too many requests": {MaxModelRequests: 6145}, "too many tokens": {MaxOutputTokens: 25165825}} {
		if _, err := l.Resolve(); err == nil || !strings.Contains(err.Error(), "model requests 6–6144, output tokens 3072–25165824") {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if one, err := (FleetLimits{MaxTurns: 1, MaxModelRequests: 6, MaxOutputTokens: 3072}).Resolve(); err != nil || one.MaxModelRequests != 6 {
		t.Fatalf("one turn of the allowance refused: %+v %v", one, err)
	}
	o := validFleet()
	o.Limits = FleetLimits{LeaseSeconds: 7200}
	values, err := FleetValues(o)
	joined := strings.Join(values, "\n")
	if err != nil || !strings.Contains(joined, "celln.fleet.limits.leaseSeconds=7200") || !strings.Contains(joined, "celln.fleet.limits.maxTurns=256") {
		t.Fatalf("limits not rendered: %v %s", err, joined)
	}
}

// A scope may carry several backends: each gets its own values entry, entry
// in the shared credentials Secret, credential path and credential profile;
// the default backend keeps the original names.
func TestFleetBackendsRenderSeparately(t *testing.T) {
	o := validFleet()
	o.Backends = []FleetBackend{
		{Name: "native", Model: FleetModel{}, CredentialFile: "/keys/deepseek"},
		{Name: "local-qwen", Model: FleetModel{Provider: ModelProviderLlamaServer, Name: "qwen.gguf", Endpoint: "http://100.81.163.75:8080/v1/chat/completions", AllowInsecure: true}},
		{Name: "claude", Model: FleetModel{Provider: ModelProviderAnthropic, Name: "claude-test"}, CredentialFile: "/keys/anthropic"},
	}
	backends, err := o.ResolvedBackends()
	if err != nil || len(backends) != 3 || backends[1].Model.Protocol != "openai-chat" || backends[2].Model.Protocol != "anthropic-messages" {
		t.Fatalf("resolved backends: %+v %v", backends, err)
	}
	values, err := FleetValues(o)
	joined := strings.Join(values, "\n")
	for _, want := range []string{
		"celln.fleet.backends[0].name=native", "celln.fleet.backends[0].provider=deepseek",
		"celln.fleet.backends[1].name=local-qwen", "celln.fleet.backends[1].allowInsecure=true",
		"celln.fleet.backends[2].name=claude", "celln.fleet.backends[2].protocol=anthropic-messages", "celln.fleet.backends[2].endpoint=https://api.anthropic.com/v1/messages",
	} {
		if err != nil || !strings.Contains(joined, want) {
			t.Fatalf("values lack %q: %v\n%s", want, err, joined)
		}
	}
	if CredentialProfileFor("starter", "native") != "starter" || CredentialProfileFor("starter", "claude") != "starter-claude" || BackendCredentialPath("native") != "/etc/celln-native/credentials/native" || BackendCredentialPath("claude") != "/etc/celln-native/credentials/claude" {
		t.Fatal("credential profiles and paths must distinguish backends")
	}
	ctx := context.Background()
	store := fleetStore()
	key := filepath.Join(t.TempDir(), "anthropic")
	if err := os.WriteFile(key, []byte("sk-ant-test-credential-0000000001\n"), 0600); err != nil {
		t.Fatal(err)
	}
	backends[2].CredentialFile = key
	for _, b := range backends[1:] {
		if err := PublishFleetBackendCredential(ctx, store, b); err != nil {
			t.Fatalf("%s: %v", b.Name, err)
		}
	}
	var secret corev1.Secret
	if err := store.Get(ctx, types.NamespacedName{Namespace: "celln-system", Name: FleetModelCredentialSecret}, &secret); err != nil || len(secret.Data["local-qwen"]) < 24 || string(secret.Data["claude"]) != "sk-ant-test-credential-0000000001" || len(secret.Data) != 2 {
		t.Fatalf("backend credentials are one entry each in the shared Secret: %v %q", err, secret.Data)
	}
	// Adding a backend later adds its entry and leaves the others untouched.
	if err := PublishFleetBackendCredential(ctx, store, FleetBackend{Name: "native", Model: backends[0].Model, CredentialFile: key}); err != nil {
		t.Fatal(err)
	}
	if err := store.Get(ctx, types.NamespacedName{Namespace: "celln-system", Name: FleetModelCredentialSecret}, &secret); err != nil || len(secret.Data) != 3 || string(secret.Data["claude"]) != "sk-ant-test-credential-0000000001" {
		t.Fatalf("adding a backend disturbed the others: %v %q", err, secret.Data)
	}
	if err := PublishFleetBackendCredential(ctx, store, FleetBackend{Name: "claude", Model: backends[2].Model, CredentialFile: filepath.Join(t.TempDir(), "missing")}); err == nil {
		t.Fatal("unreadable replacement accepted")
	}
}

// Nodes publish one configuration per backend; the installer materializes
// each under its own directory and accepts the legacy single-backend keys.
func TestReadFleetConfigurationMaterializesEveryBackend(t *testing.T) {
	ctx := context.Background()
	store := fleetStore()
	data := map[string]string{}
	for _, f := range fleetConfigurationFiles {
		data[f] = "{\"default\":\"" + f + "\"}"
		data["claude."+f] = "{\"claude\":\"" + f + "\"}"
	}
	backends, err := PublishedBackends(data)
	if err != nil || strings.Join(backends, ",") != "claude,native" {
		t.Fatalf("published backends: %v %v", backends, err)
	}
	delete(data, "claude.configured.json")
	if _, err := PublishedBackends(data); err == nil {
		t.Fatal("incomplete backend accepted")
	}
	data["claude.configured.json"] = "{}"
	data["stray.txt"] = "x"
	if _, err := PublishedBackends(data); err == nil {
		t.Fatal("unknown key accepted")
	}
	delete(data, "stray.txt")
	if err := store.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: FleetConfigurationConfigMap, Namespace: "celln-system"}, Data: data}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "configuration")
	if ok, err := ReadFleetConfiguration(ctx, store, dir); err != nil || !ok {
		t.Fatalf("read: %v %v", ok, err)
	}
	got, err := ConfigurationBackends(dir)
	if err != nil || strings.Join(got, ",") != "claude,native" {
		t.Fatalf("materialized backends: %v %v", got, err)
	}
	if raw, _ := os.ReadFile(filepath.Join(dir, "native", "catalogue.json")); string(raw) != data["catalogue.json"] {
		t.Fatalf("default backend file: %s", raw)
	}
	if raw, _ := os.ReadFile(filepath.Join(dir, "claude", "configured.json")); string(raw) != "{}" {
		t.Fatalf("claude backend file: %s", raw)
	}
}
