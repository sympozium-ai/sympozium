package charts

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/sympozium-ai/sympozium/internal/cellninstall"
)

// A backend with Celln's default output cap (absent, 0 or 512) gets byte for
// byte the plan it always got: a Celln up to v0.5.23 refuses a plan naming
// modelConnection.maxOutputTokens. Any other value in range joins the plan.
func TestFleetPlanCarriesMaxOutputTokensOnlyWhenNotDefault(t *testing.T) {
	backend := func(name, extra string) string {
		return `{"name":"` + name + `","provider":"deepseek","protocol":"openai-chat","endpoint":"https://api.deepseek.com/chat/completions","model":"deepseek-chat","allowInsecure":false,"credentialFile":"/etc/celln-native/credentials/shared"` + extra + `}`
	}
	backends := "[" + strings.Join([]string{
		backend("absent", ""), backend("zero", `,"maxOutputTokens":0`), backend("stated", `,"maxOutputTokens":512`),
		backend("low", `,"maxOutputTokens":256`), backend("high", `,"maxOutputTokens":4096`),
		backend("both", `,"maxOutputTokens":2048,"parameters":{"chat_template_kwargs":{"enable_thinking":true}}`),
		backend("small", `,"maxOutputTokens":255`), backend("big", `,"maxOutputTokens":4097`), backend("text", `,"maxOutputTokens":"2048"`), backend("fraction", `,"maxOutputTokens":1024.5`), backend("flag", `,"maxOutputTokens":true`),
		`{"name":"stray","credentialFile":"/etc/celln-native/credentials/stray","maxOutputTokens":2048}`,
	}, ",") + "]"
	limits := []string{"FLEET_LIMIT_MAX_TURNS=256", "FLEET_LIMIT_MAX_OUTPUT_TOKENS=6291456"}
	// Everything but the credential profile is shared, so the plans of the
	// default backends must be the same bytes once it is.
	normal := func(name string) string {
		t.Helper()
		got, err := plan(t, backends, name, limits...)
		if err != nil {
			t.Fatalf("%s: %v: %s", name, err, got)
		}
		return strings.ReplaceAll(got, `"starter-`+name+`"`, `"starter-X"`)
	}
	before := normal("absent")
	if strings.Contains(before, "maxOutputTokens\": 5") || strings.Count(before, "maxOutputTokens") != 1 {
		t.Fatalf("default plan names the cap (hostLimits.maxOutputTokens aside): %s", before)
	}
	for _, name := range []string{"zero", "stated"} {
		if got := normal(name); got != before {
			t.Fatalf("%s: a default backend's plan changed:\n%s\n%s", name, got, before)
		}
	}
	for name, want := range map[string]float64{"low": 256, "high": 4096, "both": 2048} {
		var built struct {
			ModelConnection map[string]any   `json:"modelConnection"`
			HostLimits      map[string]int64 `json:"hostLimits"`
		}
		if err := json.Unmarshal([]byte(normal(name)), &built); err != nil {
			t.Fatal(err)
		}
		if built.ModelConnection["maxOutputTokens"] != want || built.HostLimits["maxOutputTokens"] != 6291456 {
			t.Fatalf("%s: %v %v", name, built.ModelConnection, built.HostLimits)
		}
		if _, has := built.ModelConnection["parameters"]; has != (name == "both") {
			t.Fatalf("%s: parameters: %v", name, built.ModelConnection)
		}
	}
	for _, refused := range []string{"small", "big", "text", "fraction", "flag", "stray"} {
		if out, err := plan(t, backends, refused); err == nil || !strings.Contains(out, "maxOutputTokens") {
			t.Fatalf("%s: plan built: %s", refused, out)
		}
	}
	// prepare.sh asks the builder whether a refused plan carried the cap.
	for name, want := range map[string]bool{"absent": false, "stated": false, "high": true} {
		cmd := exec.Command("python3", "sympozium/files/celln/fleet-plan.py", "--has-max-output-tokens", name)
		cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1", "FLEET_BACKENDS="+backends)
		if err := cmd.Run(); (err == nil) != want {
			t.Fatalf("--has-max-output-tokens %s: %v", name, err)
		}
	}
	script, err := os.ReadFile("sympozium/files/celln/fleet-prepare.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), `fleet-plan.py --has-max-output-tokens "$backend"`) || !strings.Contains(string(script), "newer than v0.5.23") {
		t.Fatal("prepare.sh does not explain a refused plan of a backend with its own output cap")
	}
}

// The installer's values reach the configure DaemonSet's FLEET_BACKENDS: a
// default backend exports exactly what it did before the field existed (so
// the pod template's checksum, and with it the nodes, stay put), and a
// non-default cap is exported as an integer and sizes the lifetime ceiling.
func TestFleetBackendMaxOutputTokensReachTheNode(t *testing.T) {
	options := func(native, local int64) cellninstall.FleetOptions {
		return cellninstall.FleetOptions{Scope: "starter", Principal: "sympozium:celln", Publisher: "ed25519:operator",
			PackageImage: "registry.example/celln/starter@sha256:" + strings.Repeat("a", 64), PackageHash: "blake3:" + strings.Repeat("b", 64),
			Backends: []cellninstall.FleetBackend{
				{Name: "native", Model: cellninstall.FleetModel{Provider: "deepseek", MaxOutputTokens: native}},
				{Name: "local", Model: cellninstall.FleetModel{Provider: "llama-server", Endpoint: "http://10.0.0.1:8080", Name: "qwen.gguf", AllowInsecure: true, MaxOutputTokens: local}},
			}}
	}
	render := func(native, local int64) (map[string]string, string) {
		t.Helper()
		values, err := cellninstall.FleetValues(options(native, local))
		if err != nil {
			t.Fatal(err)
		}
		raw, err := renderNativeParent(t, append(fleetValues(), values...))
		if err != nil {
			t.Fatalf("render: %v: %s", err, raw)
		}
		template := decodeFleet(t, raw).daemonSets["celln-node-configure"].Spec.Template
		env, _ := prepareBackends(t, template.Spec)
		return env, template.Annotations["celln.sympozium.ai/backends"]
	}
	// What the chart exported before the field existed.
	const before = `[{"allowInsecure":false,"credentialFile":"/etc/celln-native/credentials/native","endpoint":"https://api.deepseek.com/chat/completions","model":"deepseek-chat","name":"native","protocol":"openai-chat","provider":"deepseek"},{"allowInsecure":true,"credentialFile":"/etc/celln-native/credentials/local","endpoint":"http://10.0.0.1:8080/v1/chat/completions","model":"qwen.gguf","name":"local","protocol":"openai-chat","provider":"llama-server"}]`
	plain, plainChecksum := render(0, 0)
	if plain["FLEET_BACKENDS"] != before || plain["FLEET_LIMIT_MAX_OUTPUT_TOKENS"] != "786432" {
		t.Fatalf("default export changed:\n%s\n%s\n%s", plain["FLEET_BACKENDS"], before, plain["FLEET_LIMIT_MAX_OUTPUT_TOKENS"])
	}
	stated, statedChecksum := render(512, 512)
	if stated["FLEET_BACKENDS"] != before || statedChecksum != plainChecksum {
		t.Fatalf("the default, stated, changed the export or rolls the nodes:\n%s", stated["FLEET_BACKENDS"])
	}
	raised, raisedChecksum := render(0, 2048)
	var list []map[string]any
	if err := json.Unmarshal([]byte(raised["FLEET_BACKENDS"]), &list); err != nil || len(list) != 2 {
		t.Fatalf("FLEET_BACKENDS: %v: %s", err, raised["FLEET_BACKENDS"])
	}
	if _, has := list[0]["maxOutputTokens"]; has || !strings.Contains(raised["FLEET_BACKENDS"], `"maxOutputTokens":2048,`) || raisedChecksum == plainChecksum {
		t.Fatalf("raised export: %s", raised["FLEET_BACKENDS"])
	}
	// 256 turns × 6 requests × 2048 tokens.
	if raised["FLEET_LIMIT_MAX_OUTPUT_TOKENS"] != "3145728" || raised["FLEET_LIMIT_MAX_MODEL_REQUESTS"] != "1536" {
		t.Fatalf("limits: %v", raised)
	}
	built, err := plan(t, raised["FLEET_BACKENDS"], "local", "FLEET_LIMIT_MAX_OUTPUT_TOKENS="+raised["FLEET_LIMIT_MAX_OUTPUT_TOKENS"])
	if err != nil || !strings.Contains(built, `"maxOutputTokens": 2048`) || !strings.Contains(built, `"hostLimits": {"maxOutputTokens": 3145728}`) {
		t.Fatalf("plan: %v: %s", err, built)
	}
}

// A values file may set the field directly; the chart applies Celln's range
// and the 6× rule against the lifetime ceiling.
func TestFleetBackendMaxOutputTokensAsValues(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm required for chart rendering")
	}
	template := func(sets ...string) ([]byte, error) {
		args := []string{"template", "test", "sympozium"}
		for _, value := range append(twoBackendValues(), sets...) {
			args = append(args, "--set", value)
		}
		return exec.Command("helm", args...).CombinedOutput()
	}
	raw, err := template("celln.fleet.backends[1].maxOutputTokens=4096", "celln.fleet.limits.maxOutputTokens=25165824")
	if err != nil {
		t.Fatalf("render: %v: %s", err, raw)
	}
	env, _ := prepareBackends(t, decodeFleet(t, raw).daemonSets["celln-node-configure"].Spec.Template.Spec)
	if !strings.Contains(env["FLEET_BACKENDS"], `"maxOutputTokens":4096,`) || strings.Count(env["FLEET_BACKENDS"], "maxOutputTokens") != 1 || env["FLEET_LIMIT_MAX_OUTPUT_TOKENS"] != "25165824" {
		t.Fatalf("export: %s %s", env["FLEET_BACKENDS"], env["FLEET_LIMIT_MAX_OUTPUT_TOKENS"])
	}
	for _, refused := range []struct {
		sets []string
		want string
	}{
		{[]string{"celln.fleet.backends[1].maxOutputTokens=255"}, "maxOutputTokens must be 256-4096"},
		{[]string{"celln.fleet.backends[1].maxOutputTokens=4097"}, "maxOutputTokens must be 256-4096"},
		{[]string{"celln.fleet.backends[1].maxOutputTokens=4096", "celln.fleet.limits.maxOutputTokens=24575"}, "does not pay for one turn of backend"},
	} {
		if out, err := template(refused.sets...); err == nil || !strings.Contains(string(out), refused.want) {
			t.Fatalf("%v: want %q: %s", refused.sets, refused.want, out)
		}
	}
	// Exactly one turn of the costliest backend is accepted.
	if out, err := template("celln.fleet.backends[1].maxOutputTokens=4096", "celln.fleet.limits.maxOutputTokens=24576"); err != nil {
		t.Fatalf("one turn refused: %s", out)
	}
}
