package charts

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/sympozium-ai/sympozium/internal/cellninstall"
)

// nastyFleetParameters holds everything a values path could mangle: commas,
// quotes, braces, brackets, equals signs, backslashes, dots and nesting.
const nastyFleetParameters = `{"chat_template_kwargs":{"enable_thinking":false,"note":"a,b=c {x} [y] \"q\" \\ back.slash\nline é"},"stop":["</s>",",","a=b"],"temperature":0.7,"top_k":40,"deep":{"er":{"flag":true}}}`

// plan runs the node's plan builder (files/celln/fleet-plan.py) for one
// backend of a FLEET_BACKENDS list and returns its output.
func plan(t *testing.T, backends, backend string, extraEnv ...string) (string, error) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 required for the plan builder")
	}
	cmd := exec.Command("python3", "sympozium/files/celln/fleet-plan.py", "/state/package-abc", "blake3:abc", "sympozium:celln", backend, "/state/.configure-abc")
	cmd.Env = append(append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1", "FLEET_SCOPE=starter", "FLEET_BACKENDS="+backends), extraEnv...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return stderr.String(), err
	}
	return strings.TrimSuffix(string(out), "\n"), nil
}

// A backend without parameters gets byte for byte the plan it always got (an
// older Celln refuses a plan naming parameters); one with parameters carries
// them under modelConnection.
func TestFleetPlanCarriesParametersOnlyWhenConfigured(t *testing.T) {
	backends := `[{"name":"native","provider":"deepseek","protocol":"openai-chat","endpoint":"https://api.deepseek.com/chat/completions","model":"deepseek-chat","allowInsecure":false,"credentialFile":"/etc/celln-native/credentials/native"},` +
		`{"name":"empty","provider":"deepseek","protocol":"openai-chat","endpoint":"https://api.deepseek.com/chat/completions","model":"deepseek-chat","allowInsecure":false,"credentialFile":"/etc/celln-native/credentials/empty","parameters":{}},` +
		`{"name":"local","provider":"llama-server","protocol":"openai-chat","endpoint":"http://10.0.0.1:8080/v1/chat/completions","model":"qwen.gguf","allowInsecure":true,"credentialFile":"/etc/celln-native/credentials/local","parameters":` + nastyFleetParameters + `},` +
		`{"name":"reviewed","credentialFile":"/etc/celln-native/credentials/reviewed"},` +
		`{"name":"stray","credentialFile":"/etc/celln-native/credentials/stray","parameters":{"temperature":1}},` +
		`{"name":"list","provider":"x","protocol":"openai-chat","endpoint":"https://x.example/v1/chat/completions","model":"m","credentialFile":"/c","parameters":[1]}]`
	got, err := plan(t, backends, "native", "FLEET_LIMIT_MAX_TURNS=256", `FLEET_HTTPS_HOSTS=["example.org"]`)
	// The exact bytes the inline builder printed before parameters existed.
	const before = `{"apiVersion": "celln.native-starter-config/v1", "package": "/state/package-abc", "packageHash": "blake3:abc", "principal": "sympozium:celln", "credentialFile": "/etc/celln-native/credentials/native", "output": "/state/.configure-abc", "modelConnection": {"provider": "deepseek", "protocol": "openai-chat", "endpoint": "https://api.deepseek.com/chat/completions", "model": "deepseek-chat", "credentialProfile": "starter", "allowInsecure": false}, "hostLimits": {"maxTurns": 256}, "httpsHosts": ["example.org"]}`
	if err != nil || got != before {
		t.Fatalf("plan without parameters changed:\n%s\n%s\n%v", got, before, err)
	}
	if got, err := plan(t, backends, "empty"); err != nil || strings.Contains(got, "parameters") {
		t.Fatalf("empty parameters reached the plan: %s %v", got, err)
	}
	if got, err := plan(t, backends, "reviewed"); err != nil || strings.Contains(got, "modelConnection") {
		t.Fatalf("reviewed default route: %s %v", got, err)
	}
	got, err = plan(t, backends, "local")
	if err != nil {
		t.Fatalf("plan: %v: %s", err, got)
	}
	var built struct {
		ModelConnection map[string]any `json:"modelConnection"`
	}
	var want map[string]any
	if err := json.Unmarshal([]byte(got), &built); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal([]byte(nastyFleetParameters), &want)
	if !reflect.DeepEqual(built.ModelConnection["parameters"], want) || built.ModelConnection["credentialProfile"] != "starter-local" || built.ModelConnection["allowInsecure"] != true {
		t.Fatalf("plan model connection: %v", built.ModelConnection)
	}
	for _, refused := range []string{"stray", "list"} {
		if out, err := plan(t, backends, refused); err == nil || !strings.Contains(out, "parameters") {
			t.Fatalf("%s: plan built: %s", refused, out)
		}
	}
}

// The installer's values for a nasty parameters object pass through Helm's
// --set parser and the chart into the configure DaemonSet's FLEET_BACKENDS,
// and from there into the plan, unchanged; every other backend is untouched.
func TestFleetBackendParametersReachTheNodeUnchanged(t *testing.T) {
	var parameters map[string]any
	if err := json.Unmarshal([]byte(nastyFleetParameters), &parameters); err != nil {
		t.Fatal(err)
	}
	options := func(local map[string]any) cellninstall.FleetOptions {
		return cellninstall.FleetOptions{Scope: "starter", Principal: "sympozium:celln", Publisher: "ed25519:operator",
			PackageImage: "registry.example/celln/starter@sha256:" + strings.Repeat("a", 64), PackageHash: "blake3:" + strings.Repeat("b", 64),
			Backends: []cellninstall.FleetBackend{
				{Name: "native", Model: cellninstall.FleetModel{Provider: "deepseek"}},
				{Name: "local", Model: cellninstall.FleetModel{Provider: "llama-server", Endpoint: "http://10.0.0.1:8080", Name: "qwen.gguf", AllowInsecure: true, Parameters: local}},
			}}
	}
	render := func(local map[string]any) (string, string) {
		t.Helper()
		values, err := cellninstall.FleetValues(options(local))
		if err != nil {
			t.Fatal(err)
		}
		raw, err := renderNativeParent(t, append(fleetValues(), values...))
		if err != nil {
			t.Fatalf("render: %v: %s", err, raw)
		}
		template := decodeFleet(t, raw).daemonSets["celln-node-configure"].Spec.Template
		env, _ := prepareBackends(t, template.Spec)
		return env["FLEET_BACKENDS"], template.Annotations["celln.sympozium.ai/backends"]
	}
	exported, checksum := render(parameters)
	var list []map[string]any
	if err := json.Unmarshal([]byte(exported), &list); err != nil || len(list) != 2 {
		t.Fatalf("FLEET_BACKENDS: %v: %s", err, exported)
	}
	if _, has := list[0]["parameters"]; has {
		t.Fatalf("backend without parameters exports the key: %s", exported)
	}
	if !reflect.DeepEqual(list[1]["parameters"], parameters) {
		t.Fatalf("parameters changed on the way:\n%v\n%v", list[1]["parameters"], parameters)
	}
	built, err := plan(t, exported, "local")
	if err != nil {
		t.Fatalf("plan: %v: %s", err, built)
	}
	var got struct {
		ModelConnection struct {
			Parameters map[string]any `json:"parameters"`
		} `json:"modelConnection"`
	}
	if err := json.Unmarshal([]byte(built), &got); err != nil || !reflect.DeepEqual(got.ModelConnection.Parameters, parameters) {
		t.Fatalf("plan parameters: %v %v", got.ModelConnection.Parameters, err)
	}
	// Without parameters the export, and with it the pod template's checksum,
	// is what it was before parameters existed; with them the nodes roll.
	plainExport, plainChecksum := render(nil)
	if strings.Contains(plainExport, "parameters") || plainChecksum == checksum {
		t.Fatalf("plain export %s, checksums %s %s", plainExport, plainChecksum, checksum)
	}
}

// A values file may give the object itself; anything else is refused.
func TestFleetBackendParametersAsValuesObject(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm required for chart rendering")
	}
	template := func(setJSON string) ([]byte, error) {
		args := []string{"template", "test", "sympozium"}
		for _, value := range twoBackendValues() {
			args = append(args, "--set", value)
		}
		return exec.Command("helm", append(args, "--set-json", setJSON)...).CombinedOutput()
	}
	raw, err := template(`celln.fleet.backends[1].parameters=` + nastyFleetParameters)
	if err != nil {
		t.Fatalf("render: %v: %s", err, raw)
	}
	env, _ := prepareBackends(t, decodeFleet(t, raw).daemonSets["celln-node-configure"].Spec.Template.Spec)
	var list []map[string]any
	var want map[string]any
	_ = json.Unmarshal([]byte(nastyFleetParameters), &want)
	if err := json.Unmarshal([]byte(env["FLEET_BACKENDS"]), &list); err != nil || !reflect.DeepEqual(list[1]["parameters"], want) {
		t.Fatalf("object parameters: %v %s", err, env["FLEET_BACKENDS"])
	}
	for value, refusal := range map[string]string{
		`celln.fleet.backends[1].parameters=[1,2]`:     "must be an object",
		`celln.fleet.backends[1].parameters="{broken"`: "is not a JSON object",
		`celln.fleet.backends[1].parameters="[1]"`:     "is not a JSON object",
	} {
		if out, err := template(value); err == nil || !strings.Contains(string(out), refusal) {
			t.Fatalf("%s: want %q: %s", value, refusal, out)
		}
	}
}

// The configure pods carry the plan builder next to prepare.sh, and
// prepare.sh uses it.
func TestFleetShipsThePlanBuilder(t *testing.T) {
	raw, err := renderNativeParent(t, fleetValues())
	if err != nil {
		t.Fatalf("render: %v: %s", err, raw)
	}
	if !strings.Contains(string(raw), "fleet-plan.py: |") || !strings.Contains(string(raw), "def build(package, package_hash, principal, name, output, environ)") {
		t.Fatal("celln-fleet-prepare does not carry fleet-plan.py")
	}
	mounted := false
	for _, m := range decodeFleet(t, raw).daemonSets["celln-node-configure"].Spec.Template.Spec.Containers[0].VolumeMounts {
		mounted = mounted || (m.MountPath == "/etc/celln-fleet/fleet-plan.py" && m.SubPath == "fleet-plan.py" && m.ReadOnly)
	}
	if !mounted {
		t.Fatal("fleet-plan.py is not mounted into the configure pod")
	}
	script, err := os.ReadFile("sympozium/files/celln/fleet-prepare.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), `python3 /etc/celln-fleet/fleet-plan.py "$package"`) || !strings.Contains(string(script), "newer than v0.5.22") {
		t.Fatal("prepare.sh does not build the plan with fleet-plan.py or explain a refused parameters plan")
	}
}
