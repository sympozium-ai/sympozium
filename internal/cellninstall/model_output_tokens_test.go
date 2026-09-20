package cellninstall

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestValidateModelMaxOutputTokens(t *testing.T) {
	for _, tokens := range []int64{0, 256, 512, 2048, 4096} {
		if err := ValidateModelMaxOutputTokens(tokens); err != nil {
			t.Fatalf("%d refused: %v", tokens, err)
		}
	}
	for _, tokens := range []int64{-1, 1, 255, 4097, 1 << 40} {
		if err := ValidateModelMaxOutputTokens(tokens); err == nil || !strings.Contains(err.Error(), "must be 256–4096 (omit it for the default 512)") {
			t.Fatalf("%d accepted: %v", tokens, err)
		}
	}
	if NormalModelMaxOutputTokens(512) != 0 || NormalModelMaxOutputTokens(2048) != 2048 || EffectiveModelMaxOutputTokens(0) != 512 || EffectiveModelMaxOutputTokens(256) != 256 {
		t.Fatal("the default must fold into unset and back")
	}
	if TurnOutputTokensFor(0) != api.TurnOutputTokens || TurnOutputTokensFor(4096) != api.MaxTurnOutputTokens || api.MaxLifetimeOutputTokens != 25165824 || MaxFleetOutputTokens != 25165824 {
		t.Fatal("a turn reserves 6 requests of the cap; the lifetime maximum is 1024 such turns at 4096")
	}
	// Every path resolves a route through the same rule.
	if _, err := (FleetModel{Provider: "deepseek", MaxOutputTokens: 100}).Resolve("starter"); err == nil || !strings.Contains(err.Error(), "256–4096") {
		t.Fatalf("route: %v", err)
	}
	if m, err := (FleetModel{Provider: "deepseek", MaxOutputTokens: 512}).Resolve("starter"); err != nil || m.MaxOutputTokens != 0 {
		t.Fatalf("the default stated: %+v %v", m, err)
	}
}

// A backend with the default cap (absent, 0 or 512) renders exactly the
// values it always did; a non-default one adds a single value.
func TestFleetValuesCarryMaxOutputTokensOnlyWhenNotDefault(t *testing.T) {
	before := validFleet()
	before.Backends = []FleetBackend{{Name: "native", Model: FleetModel{Provider: "deepseek"}}, {Name: "local", Model: FleetModel{Provider: "llama-server", Endpoint: "http://10.0.0.1:8080", Name: "qwen.gguf", AllowInsecure: true}}}
	want, err := FleetValues(before)
	if err != nil {
		t.Fatal(err)
	}
	stated := validFleet()
	stated.Backends = []FleetBackend{{Name: "native", Model: FleetModel{Provider: "deepseek", MaxOutputTokens: 512}}, {Name: "local", Model: FleetModel{Provider: "llama-server", Endpoint: "http://10.0.0.1:8080", Name: "qwen.gguf", AllowInsecure: true}}}
	if got, err := FleetValues(stated); err != nil || !reflect.DeepEqual(got, want) || strings.Contains(strings.Join(got, "\n"), "maxOutputTokens=5") {
		t.Fatalf("default backends changed their values: %v %v", got, err)
	}
	stated.Backends[1].Model.MaxOutputTokens = 2048
	got, err := FleetValues(stated)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "celln.fleet.backends[1].maxOutputTokens=2048") || strings.Contains(joined, "backends[0].maxOutputTokens") {
		t.Fatalf("values: %s", joined)
	}
	// The unset lifetime ceiling follows the most expensive backend:
	// 256 turns × 6 × 2048.
	if !strings.Contains(joined, "celln.fleet.limits.maxOutputTokens=3145728") || !strings.Contains(joined, "celln.fleet.limits.maxModelRequests=1536") || !strings.Contains(joined, "celln.fleet.limits.maxTurns=256") {
		t.Fatalf("limits: %s", joined)
	}
	stated.Backends[1].Model.MaxOutputTokens = 4097
	if _, err := FleetValues(stated); err == nil || !strings.Contains(err.Error(), "backend local: max output tokens per request must be 256–4096") {
		t.Fatalf("out of range rendered: %v", err)
	}
}

// One policy bounds every backend, so its ceilings are sized and checked for
// the backend whose turns cost most.
func TestFleetLimitsFollowTheMostExpensiveBackend(t *testing.T) {
	backends := func(caps ...int64) []FleetBackend {
		out := make([]FleetBackend, len(caps))
		for i, c := range caps {
			out[i] = FleetBackend{Name: string(rune('a' + i)), Model: FleetModel{MaxOutputTokens: c}}
		}
		return out
	}
	// Default backends: the defaults, and nothing to say.
	got, note, err := FleetLimits{}.ResolveFor(backends(0, 0))
	if err != nil || note != "" || got != DefaultFleetLimits {
		t.Fatalf("default backends: %+v %q %v", got, note, err)
	}
	// A cheaper backend changes nothing either.
	if got, note, err = (FleetLimits{}).ResolveFor(backends(256)); err != nil || note != "" || got != DefaultFleetLimits {
		t.Fatalf("cheaper backend: %+v %q %v", got, note, err)
	}
	for _, tc := range []struct {
		caps   []int64
		tokens int64
		names  string
	}{
		{[]int64{0, 1024}, 256 * 6 * 1024, "backend b allows 1024"},
		{[]int64{2048, 0, 1024}, 256 * 6 * 2048, "backend a allows 2048"},
		{[]int64{0, 4096}, 256 * 6 * 4096, "backend b allows 4096"},
	} {
		got, note, err := FleetLimits{}.ResolveFor(backends(tc.caps...))
		if err != nil || got.MaxOutputTokens != tc.tokens || got.MaxTurns != 256 || got.MaxModelRequests != 1536 {
			t.Fatalf("%v: %+v %v", tc.caps, got, err)
		}
		_, turn := MostExpensiveBackend(backends(tc.caps...))
		if TurnsAfforded(got.MaxModelRequests, got.MaxOutputTokens, api.TurnModelRequests, turn) != 256 || !strings.Contains(note, tc.names) || !strings.Contains(note, "256 turns") {
			t.Fatalf("%v: default turns unreachable or unexplained: %+v %q", tc.caps, got, note)
		}
	}
	// Explicit ceilings are kept, and must pay for one turn of that backend.
	if got, note, err = (FleetLimits{MaxOutputTokens: 24576}).ResolveFor(backends(0, 4096)); err != nil || note != "" || got.MaxOutputTokens != 24576 {
		t.Fatalf("one exact turn: %+v %q %v", got, note, err)
	}
	_, _, err = FleetLimits{MaxOutputTokens: 24575}.ResolveFor(backends(0, 4096))
	for _, want := range []string{"24575 lifetime output tokens do not pay for one turn of backend b", "reserves 24576 (6 requests × 4096 max output tokens per request)", "--celln-fleet-max-output-tokens 24576"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal lacks %q: %v", want, err)
		}
	}
	// The maximum is 1024 turns of the largest allowance.
	if got, _, err = (FleetLimits{MaxTurns: 1024, MaxModelRequests: 6144, MaxOutputTokens: 25165824}).ResolveFor(backends(4096)); err != nil || TurnsAfforded(got.MaxModelRequests, got.MaxOutputTokens, 6, 24576) != 1024 {
		t.Fatalf("maximum: %+v %v", got, err)
	}
	if _, _, err = (FleetLimits{MaxOutputTokens: 25165825}).ResolveFor(backends(4096)); err == nil || !strings.Contains(err.Error(), "output tokens 3072–25165824") {
		t.Fatalf("above the maximum: %v", err)
	}
}

// Session defaults are the largest turn count up to 64 that the ceilings pay
// for at the profile's own per-turn allowance.
func TestSessionDefaultsFollowTheProfileAllowance(t *testing.T) {
	ceilings := api.EnduringRunSpec{LeaseSeconds: 86400, MaxTurns: 256, MaxModelRequests: 1536, MaxOutputTokens: 786432}
	profile := func(requests, tokens int64) *api.CellnRuntimeProfile {
		return &api.CellnRuntimeProfile{Spec: api.CellnRuntimeProfileSpec{Native: &api.CellnNativeProvisioning{TurnModelRequests: requests, TurnOutputTokens: tokens}}}
	}
	for name, tc := range map[string]struct {
		profile                 *api.CellnRuntimeProfile
		turns, requests, tokens int64
	}{
		"default allowance":       {profile(6, 3072), 64, 384, 196608},
		"no profile":              {nil, 64, 384, 196608},
		"no allowance stated":     {&api.CellnRuntimeProfile{}, 64, 384, 196608},
		"2048 per request":        {profile(6, 12288), 64, 384, 786432},
		"4096 per request":        {profile(6, 24576), 32, 192, 786432},
		"3000 per request":        {profile(6, 18000), 43, 258, 774000},
		"the old, smaller one":    {profile(3, 1536), 64, 192, 98304},
		"requests are the bottle": {profile(48, 3072), 32, 1536, 98304},
	} {
		got := SessionDefaultsFor(ceilings, tc.profile)
		if int64(got.MaxTurns) != tc.turns || int64(got.MaxModelRequests) != tc.requests || got.MaxOutputTokens != tc.tokens || got.LeaseSeconds != 14400 {
			t.Fatalf("%s: %+v", name, got)
		}
		requests, tokens := ProfileTurnAllowance(tc.profile)
		if TurnsAfforded(int64(got.MaxModelRequests), got.MaxOutputTokens, requests, tokens) != int64(got.MaxTurns) {
			t.Fatalf("%s: the defaults do not pay for their own turns: %+v", name, got)
		}
	}
	// Ceilings below one turn still yield a bounded one-turn request.
	if got := SessionDefaultsFor(api.EnduringRunSpec{LeaseSeconds: 600, MaxTurns: 4, MaxModelRequests: 6, MaxOutputTokens: 3072}, profile(6, 24576)); got.MaxTurns != 1 || got.MaxModelRequests != 6 || got.MaxOutputTokens != 3072 {
		t.Fatalf("below one turn: %+v", got)
	}
}

func TestFewerSessionTurnsWarning(t *testing.T) {
	ceilings := api.CellnExecutionPolicyCeilings{MaxTurns: 256, MaxModelRequests: 1536, MaxOutputTokens: 786432}
	for _, tokens := range []int64{0, 512, 1024, 2048} {
		if w := FewerSessionTurnsWarning("b", tokens, ceilings); w != "" {
			t.Fatalf("%d: %s", tokens, w)
		}
	}
	if w := FewerSessionTurnsWarning("b", 2049, ceilings); !strings.Contains(w, "pay for 63 such turns, fewer than the usual 64") {
		t.Fatalf("2049: %s", w)
	}
	// A scope limited to fewer turns than a session is not warned about them.
	if w := FewerSessionTurnsWarning("b", 4096, api.CellnExecutionPolicyCeilings{MaxTurns: 8, MaxModelRequests: 1536, MaxOutputTokens: 786432}); w != "" {
		t.Fatalf("small scope: %s", w)
	}
}

func TestExtraBackendCarriesMaxOutputTokensOnlyWhenNotDefault(t *testing.T) {
	before, _ := json.Marshal(ExtraBackendFor(FleetBackend{Name: "a", Model: FleetModel{Provider: "deepseek"}}))
	stated, _ := json.Marshal(ExtraBackendFor(FleetBackend{Name: "a", Model: FleetModel{Provider: "deepseek", MaxOutputTokens: 512}}))
	if string(before) != string(stated) || strings.Contains(string(before), "maxOutputTokens") {
		t.Fatalf("default backend names the cap: %s %s", before, stated)
	}
	with, _ := json.Marshal(ExtraBackendFor(FleetBackend{Name: "a", Model: FleetModel{Provider: "deepseek", MaxOutputTokens: 2048}}))
	if !strings.Contains(string(with), `"maxOutputTokens":2048`) {
		t.Fatalf("cap lost: %s", with)
	}
	if _, err := AppendExtraBackend(context.Background(), fleetStore(), FleetFacts{}, ExtraBackend{Name: "a", MaxOutputTokens: 5000}); err == nil || !strings.Contains(err.Error(), "256–4096") {
		t.Fatalf("out of range stored: %v", err)
	}
}

// The probe stays a one-token request whatever the backend's cap.
func TestPreflightBackendKeepsItsOneTokenProbe(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}))
	defer server.Close()
	for _, protocol := range []string{"openai-chat", "anthropic-messages"} {
		body = nil
		b := FleetBackend{Name: "deep", Model: FleetModel{Provider: "custom", Protocol: protocol, Endpoint: server.URL, Name: "m", MaxOutputTokens: 4096}}
		if err := PreflightBackend(context.Background(), server.Client(), b, "k"); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(body)
		if body["max_tokens"] != float64(1) || len(body) != 3 || strings.Contains(string(raw), "4096") || strings.Contains(strings.ToLower(string(raw)), "maxoutputtokens") {
			t.Fatalf("%s probe: %s", protocol, raw)
		}
	}
}

// The output cap of a published backend cannot change: the install stops with
// the same ways forward as for parameters.
func TestPublishedBackendMaxOutputTokensCannotChange(t *testing.T) {
	ctx := context.Background()
	o := validFleet()
	backends := func(native, local int64) []FleetBackend {
		return []FleetBackend{
			{Name: "native", Model: FleetModel{Provider: "deepseek", MaxOutputTokens: native}},
			{Name: "local", Model: FleetModel{Provider: "llama-server", Endpoint: "http://10.0.0.1:8080", Name: "qwen.gguf", AllowInsecure: true, MaxOutputTokens: local}},
		}
	}
	data := map[string]string{}
	for _, f := range fleetConfigurationFiles {
		data[f], data["local."+f] = "{}", "{}"
	}
	// configured.json names the cap only when it is not 512.
	data["configured.json"] = `{"apiVersion":"celln.native-starter-configured/v1","model":{"provider":"deepseek"}}`
	data["local.configured.json"] = `{"apiVersion":"celln.native-starter-configured/v1","model":{"provider":"llama-server","maxOutputTokens":2048}}`
	published := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: FleetConfigurationConfigMap, Namespace: fleetNamespace, Annotations: map[string]string{packageAnnotation: o.PackageHash, FleetScopeAnnotation: o.Scope}}, Data: data}
	settings, err := PublishedModelSettings(data)
	if err != nil || settings["native"].MaxOutputTokens != 0 || settings["local"].MaxOutputTokens != 2048 {
		t.Fatalf("published settings: %+v %v", settings, err)
	}

	o.Backends = backends(4096, 0)
	if err := CheckPublishedModelParameters(ctx, fleetStore(), o); err != nil {
		t.Fatalf("first install refused: %v", err)
	}
	store := fleetStore()
	if err := store.Create(ctx, published); err != nil {
		t.Fatal(err)
	}
	// Unchanged (the default stated or not), and a backend not published yet.
	for _, same := range [][]FleetBackend{backends(0, 2048), backends(512, 2048), append(backends(0, 2048), FleetBackend{Name: "new", Model: FleetModel{Provider: "deepseek", MaxOutputTokens: 4096}})} {
		o.Backends = same
		if err := CheckPublishedModelParameters(ctx, store, o); err != nil {
			t.Fatalf("unchanged cap refused: %v", err)
		}
	}
	for name, change := range map[string][]FleetBackend{"raised": backends(0, 4096), "removed": backends(0, 0), "added": backends(1024, 2048)} {
		o.Backends = change
		err := CheckPublishedModelParameters(ctx, store, o)
		if err == nil {
			t.Fatalf("%s: accepted", name)
		}
		for _, want := range []string{"max output tokens per request of a published backend cannot change", "--celln-fleet-backend name=", "POST /api/v1/celln-platform/backends", "--celln-fleet-replace-package"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("%s: refusal lacks %q: %v", name, want, err)
			}
		}
	}
	o.Backends = backends(0, 4096)
	err = CheckPublishedModelParameters(ctx, store, o)
	if err == nil || !strings.Contains(err.Error(), "backend local was published with max output tokens 2048 and this install asks for max output tokens 4096") ||
		!strings.Contains(err.Error(), "name=local-2,provider=llama-server,model=qwen.gguf,endpoint=http://10.0.0.1:8080/v1/chat/completions,protocol=openai-chat,allow-insecure=true,max-output-tokens=4096\n") ||
		!strings.Contains(err.Error(), `"maxOutputTokens":4096`) || strings.Contains(err.Error(), "parameters") {
		t.Fatalf("refusal: %v", err)
	}
	o.Backends = backends(0, 0)
	if err = CheckPublishedModelParameters(ctx, store, o); err == nil || !strings.Contains(err.Error(), "asks for the default max output tokens (512)") {
		t.Fatalf("refusal of the default: %v", err)
	}
	// Moving the scope to another package configures every backend afresh.
	o.Backends = backends(0, 4096)
	o.PackageHash = "blake3:" + strings.Repeat("c", 64)
	if err := CheckPublishedModelParameters(ctx, store, o); err != nil {
		t.Fatalf("replacement refused: %v", err)
	}
}
