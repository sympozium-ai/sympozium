package cellninstall

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/strvals"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// nastyParameters exercises everything the values path must keep literally:
// commas, quotes, braces, brackets, equals signs, backslashes, dots, spaces,
// newlines, non-ASCII text, nesting and every scalar type.
const nastyParameters = `{"chat_template_kwargs":{"enable_thinking":false,"note":"a,b=c {x} [y] \"q\" \\ back.slash\nline é"},"stop":["</s>",",","a=b"],"temperature":0.7,"top_k":40,"seed":-1,"deep":{"er":{"flag":true}}}`

func TestValidateModelParametersAppliesEveryRule(t *testing.T) {
	object := func(n int) map[string]any {
		out := map[string]any{}
		for i := range n {
			out[fmt.Sprintf("k%d", i)] = true
		}
		return out
	}
	big := map[string]any{}
	for i := range 9 {
		big[fmt.Sprintf("k%d", i)] = strings.Repeat("x", 256)
	}
	cases := []struct {
		name       string
		parameters map[string]any
		refusal    string // empty: accepted
	}{
		{"nil", nil, ""},
		{"empty", map[string]any{}, ""},
		{"reasoning off", map[string]any{"chat_template_kwargs": map[string]any{"enable_thinking": false}}, ""},
		{"every scalar", map[string]any{"a": true, "b": 0.5, "c": json.Number("12"), "d": "text", "e": 3, "f": []any{"x", 1.5, false}}, ""},
		{"16 keys", object(16), ""},
		{"17 keys", object(17), "at most 16 top-level keys"},
		{"depth 3", map[string]any{"a": map[string]any{"b": map[string]any{"c": 1.0}}}, ""},
		{"depth 4", map[string]any{"a": map[string]any{"b": map[string]any{"c": map[string]any{"d": 1.0}}}}, "a.b.c nests deeper than 3 levels"},
		{"upper-case key", map[string]any{"Temperature": 1.0}, `key "Temperature" must match`},
		{"leading digit", map[string]any{"1st": 1.0}, `key "1st" must match`},
		{"dash in key", map[string]any{"top-k": 1.0}, `key "top-k" must match`},
		{"empty key", map[string]any{"": 1.0}, `key "" must match`},
		{"65-byte key", map[string]any{"a" + strings.Repeat("b", 64): 1.0}, "must match"},
		{"64-byte key", map[string]any{"a" + strings.Repeat("b", 63): 1.0}, ""},
		{"nested key", map[string]any{"a": map[string]any{"Bad": 1.0}}, `key "a.Bad" must match`},
		{"null", map[string]any{"a": nil}, "a is null"},
		{"NaN", map[string]any{"a": math.NaN()}, "a must be a finite number"},
		{"Inf", map[string]any{"a": math.Inf(1)}, "a must be a finite number"},
		{"overflowing number", map[string]any{"a": json.Number("1e999")}, "a must be a finite number"},
		{"256-byte string", map[string]any{"a": strings.Repeat("x", 256)}, ""},
		{"257-byte string", map[string]any{"a": strings.Repeat("x", 257)}, "a must be a string of at most 256 bytes"},
		{"NUL in string", map[string]any{"a": "x\x00y"}, "without NUL"},
		{"8 items", map[string]any{"a": []any{1.0, 2.0, 3.0, 4.0, 5.0, 6.0, 7.0, 8.0}}, ""},
		{"9 items", map[string]any{"a": []any{1.0, 2.0, 3.0, 4.0, 5.0, 6.0, 7.0, 8.0, 9.0}}, "a holds 9 items, at most 8"},
		{"object in array", map[string]any{"a": []any{map[string]any{"b": 1.0}}}, "a[0] must be a boolean, number or string"},
		{"array in array", map[string]any{"a": []any{[]any{1.0}}}, "a[0] must be a boolean, number or string"},
		{"null in array", map[string]any{"a": []any{nil}}, "a[0] is null"},
		{"long string in array", map[string]any{"a": []any{strings.Repeat("x", 257)}}, "a[0] must be a string"},
		{"too large", big, "exceeds 2048"},
		{"unsupported type", map[string]any{"a": struct{}{}}, "unsupported type"},
	}
	for _, reserved := range ReservedModelParameters {
		cases = append(cases, struct {
			name       string
			parameters map[string]any
			refusal    string
		}{"reserved " + reserved, map[string]any{reserved: "x"}, fmt.Sprintf("%q is reserved", reserved)})
	}
	if len(ReservedModelParameters) != 14 {
		t.Fatalf("reserved keys: %v", ReservedModelParameters)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateModelParameters(tc.parameters)
			if tc.refusal == "" && err != nil {
				t.Fatalf("refused: %v", err)
			}
			if tc.refusal != "" && (err == nil || !strings.Contains(err.Error(), tc.refusal) || !strings.HasPrefix(err.Error(), "model parameters: ")) {
				t.Fatalf("want %q, got %v", tc.refusal, err)
			}
		})
	}
	// A reserved name is only reserved at the top level.
	if err := ValidateModelParameters(map[string]any{"extra": map[string]any{"model": "x"}}); err != nil {
		t.Fatalf("nested reserved name refused: %v", err)
	}
}

func TestParseModelParametersReadsOneObject(t *testing.T) {
	for raw, refusal := range map[string]string{
		`[1]`:                "a JSON object is required",
		`"x"`:                "a JSON object is required",
		`{"a":1} {"b":2}`:    "one JSON object expected",
		`{"a":`:              "not valid JSON",
		`{"a":NaN}`:          "not valid JSON",
		`{"max_tokens":900}`: `"max_tokens" is reserved`,
	} {
		if _, err := ParseModelParameters([]byte(raw)); err == nil || !strings.Contains(err.Error(), refusal) {
			t.Fatalf("%s: want %q, got %v", raw, refusal, err)
		}
	}
	for _, raw := range []string{"", "  \n", "{}"} {
		if got, err := ParseModelParameters([]byte(raw)); err != nil || got != nil {
			t.Fatalf("%q: %v %v", raw, got, err)
		}
	}
	got, err := ParseModelParameters([]byte(nastyParameters))
	if err != nil || ModelParametersJSON(got) == "" {
		t.Fatalf("nasty object refused: %v", err)
	}
	// Numbers keep their written form.
	precise, err := ParseModelParameters([]byte(`{"seed":12345678901234567}`))
	if err != nil || ModelParametersJSON(precise) != `{"seed":12345678901234567}` {
		t.Fatalf("number rewritten: %s %v", ModelParametersJSON(precise), err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "parameters.json")
	if err := os.WriteFile(path, []byte(nastyParameters), 0600); err != nil {
		t.Fatal(err)
	}
	if fromFile, err := ReadModelParametersFile(path); err != nil || !SameModelParameters(fromFile, got) {
		t.Fatalf("file: %v %v", fromFile, err)
	}
	for _, bad := range []string{"parameters.json", dir, filepath.Join(dir, "missing.json")} {
		if _, err := ReadModelParametersFile(bad); err == nil {
			t.Fatalf("%s accepted", bad)
		}
	}
}

// A backend without parameters renders exactly the values it always did; one
// with parameters adds a single value that survives Helm's strvals parser.
func TestFleetValuesCarryParametersThroughStrvals(t *testing.T) {
	parameters, err := ParseModelParameters([]byte(nastyParameters))
	if err != nil {
		t.Fatal(err)
	}
	o := validFleet()
	o.Backends = []FleetBackend{
		{Name: "native", Model: FleetModel{Provider: "deepseek"}},
		{Name: "local", Model: FleetModel{Provider: "llama-server", Endpoint: "http://10.0.0.1:8080", Name: "qwen.gguf", AllowInsecure: true, Parameters: parameters}},
	}
	values, err := FleetValues(o)
	if err != nil {
		t.Fatal(err)
	}
	parsed := map[string]any{}
	carried := 0
	for _, value := range values {
		if strings.Contains(value, ".parameters=") {
			carried++
		}
		if err := strvals.ParseInto(value, parsed); err != nil {
			t.Fatalf("%s: %v", value, err)
		}
	}
	if carried != 1 {
		t.Fatalf("parameters values: %d in %v", carried, values)
	}
	backends := parsed["celln"].(map[string]any)["fleet"].(map[string]any)["backends"].([]any)
	if _, has := backends[0].(map[string]any)["parameters"]; has {
		t.Fatal("a backend without parameters carries the key")
	}
	raw, ok := backends[1].(map[string]any)["parameters"].(string)
	if !ok {
		t.Fatalf("parameters did not arrive as one string: %#v", backends[1])
	}
	back, err := ParseModelParameters([]byte(raw))
	if err != nil || !SameModelParameters(back, parameters) || raw != ModelParametersJSON(parameters) {
		t.Fatalf("round trip changed the object:\n%s\n%s\n%v", raw, ModelParametersJSON(parameters), err)
	}

	without := validFleet()
	without.Backends = []FleetBackend{{Name: "native", Model: FleetModel{Provider: "deepseek", Parameters: map[string]any{}}}}
	plain, err := FleetValues(without)
	if err != nil || strings.Contains(strings.Join(plain, "\n"), "parameters") {
		t.Fatalf("empty parameters rendered: %v %v", plain, err)
	}
	o.Backends[1].Model.Parameters = map[string]any{"max_tokens": 4096}
	if _, err := FleetValues(o); err == nil || !strings.Contains(err.Error(), `backend local: model parameters: "max_tokens" is reserved`) {
		t.Fatalf("reserved key rendered: %v", err)
	}
}

func TestExtraBackendCarriesParametersOnlyWhenSet(t *testing.T) {
	plain, _ := json.Marshal(ExtraBackendFor(FleetBackend{Name: "a", Model: FleetModel{Provider: "deepseek"}}))
	if strings.Contains(string(plain), "parameters") {
		t.Fatalf("backend without parameters names them: %s", plain)
	}
	with, _ := json.Marshal(ExtraBackendFor(FleetBackend{Name: "a", Model: FleetModel{Provider: "llama-server", Parameters: map[string]any{"chat_template_kwargs": map[string]any{"enable_thinking": false}}}}))
	if !strings.Contains(string(with), `"parameters":{"chat_template_kwargs":{"enable_thinking":false}}`) {
		t.Fatalf("parameters lost: %s", with)
	}
	store := fleetStore()
	if _, err := AppendExtraBackend(context.Background(), store, FleetFacts{}, ExtraBackend{Name: "bad", Parameters: map[string]any{"stream": true}}); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("invalid parameters stored: %v", err)
	}
}

// The probe carries the parameters as the Celln host will send them, for both
// protocols, and a provider that refuses them is reported with them.
func TestPreflightBackendSendsTheParameters(t *testing.T) {
	ctx := context.Background()
	bodies := map[string]map[string]any{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies[r.URL.Path] = body
		if _, bad := body["unknown_knob"]; bad {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"unknown_knob is not supported"}`))
		}
	}))
	defer server.Close()
	parameters := map[string]any{"chat_template_kwargs": map[string]any{"enable_thinking": false}, "temperature": 0.2}
	for path, protocol := range map[string]string{"/v1/chat/completions": "openai-chat", "/v1/messages": "anthropic-messages"} {
		b := FleetBackend{Name: "local", Model: FleetModel{Provider: "custom", Protocol: protocol, Endpoint: server.URL + path, Name: "m", Parameters: parameters}}
		if err := PreflightBackend(ctx, server.Client(), b, "sk-valid-credential-0000000001"); err != nil {
			t.Fatalf("%s: %v", protocol, err)
		}
		got := bodies[path]
		if got["model"] != "m" || got["max_tokens"] != float64(1) || got["temperature"] != 0.2 || !reflect.DeepEqual(got["chat_template_kwargs"], map[string]any{"enable_thinking": false}) || got["messages"] == nil {
			t.Fatalf("%s probe body: %v", protocol, got)
		}
	}
	// No parameters: the probe is exactly the old one.
	plain := FleetBackend{Name: "plain", Model: FleetModel{Provider: "custom", Protocol: "openai-chat", Endpoint: server.URL + "/plain", Name: "m"}}
	if err := PreflightBackend(ctx, server.Client(), plain, "k"); err != nil || len(bodies["/plain"]) != 3 {
		t.Fatalf("plain probe: %v %v", bodies["/plain"], err)
	}
	refused := FleetBackend{Name: "local", Model: FleetModel{Provider: "custom", Protocol: "openai-chat", Endpoint: server.URL + "/refused", Name: "m", Parameters: map[string]any{"unknown_knob": true}}}
	err := PreflightBackend(ctx, server.Client(), refused, "k")
	if err == nil || !strings.Contains(err.Error(), "HTTP 400") || !strings.Contains(err.Error(), `{"unknown_knob":true}`) || !strings.Contains(err.Error(), "unknown_knob is not supported") {
		t.Fatalf("refused parameter not reported: %v", err)
	}
}

// Parameters of a published backend cannot change: the install stops and says
// what to do instead.
func TestPublishedBackendParametersCannotChange(t *testing.T) {
	ctx := context.Background()
	o := validFleet()
	thinkingOff := map[string]any{"chat_template_kwargs": map[string]any{"enable_thinking": false}, "top_k": 40}
	backends := func(native, local map[string]any) []FleetBackend {
		return []FleetBackend{
			{Name: "native", Model: FleetModel{Provider: "deepseek", Parameters: native}},
			{Name: "local", Model: FleetModel{Provider: "llama-server", Endpoint: "http://10.0.0.1:8080", Name: "qwen.gguf", AllowInsecure: true, Parameters: local}},
		}
	}
	data := map[string]string{}
	for _, f := range fleetConfigurationFiles {
		data[f], data["local."+f] = "{}", "{}"
	}
	data["configured.json"] = `{"apiVersion":"celln.native-starter-configured/v1","model":{"provider":"deepseek"}}`
	data["local.configured.json"] = `{"apiVersion":"celln.native-starter-configured/v1","model":{"provider":"llama-server","parameters":{"top_k":40,"chat_template_kwargs":{"enable_thinking":false}}}}`
	published := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: FleetConfigurationConfigMap, Namespace: fleetNamespace, Annotations: map[string]string{packageAnnotation: o.PackageHash, FleetScopeAnnotation: o.Scope}}, Data: data}

	// Nothing published yet: anything goes.
	o.Backends = backends(thinkingOff, nil)
	if err := CheckPublishedModelParameters(ctx, fleetStore(), o); err != nil {
		t.Fatalf("first install refused: %v", err)
	}
	store := fleetStore()
	if err := store.Create(ctx, published); err != nil {
		t.Fatal(err)
	}
	// The same parameters (whatever their key order or number type), and a
	// backend that is not published yet, pass.
	o.Backends = append(backends(nil, thinkingOff), FleetBackend{Name: "new", Model: FleetModel{Provider: "deepseek", Parameters: map[string]any{"temperature": 0.1}}})
	if err := CheckPublishedModelParameters(ctx, store, o); err != nil {
		t.Fatalf("unchanged parameters refused: %v", err)
	}
	for name, change := range map[string][]FleetBackend{
		"changed": backends(nil, map[string]any{"chat_template_kwargs": map[string]any{"enable_thinking": true}, "top_k": 40}),
		"removed": backends(nil, nil),
		"added":   backends(thinkingOff, thinkingOff),
	} {
		o.Backends = change
		err := CheckPublishedModelParameters(ctx, store, o)
		if err == nil {
			t.Fatalf("%s: accepted", name)
		}
		for _, want := range []string{"model parameters of a published backend cannot change", "--celln-fleet-backend name=", "parameters-file=", "POST /api/v1/celln-platform/backends", "--celln-fleet-replace-package"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("%s: refusal lacks %q: %v", name, want, err)
			}
		}
	}
	o.Backends = backends(nil, nil)
	err := CheckPublishedModelParameters(ctx, store, o)
	if err == nil || !strings.Contains(err.Error(), `backend local was published with {"chat_template_kwargs":{"enable_thinking":false},"top_k":40} and this install asks for none`) || !strings.Contains(err.Error(), "name=local-2,provider=llama-server,model=qwen.gguf,endpoint=http://10.0.0.1:8080/v1/chat/completions,protocol=openai-chat,allow-insecure=true,parameters-file=") {
		t.Fatalf("refusal: %v", err)
	}
	// Moving the scope to another package configures every backend afresh.
	o.PackageHash = "blake3:" + strings.Repeat("c", 64)
	if err := CheckPublishedModelParameters(ctx, store, o); err != nil {
		t.Fatalf("replacement refused: %v", err)
	}
}
