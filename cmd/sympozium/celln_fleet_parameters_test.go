package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"

	"github.com/sympozium-ai/sympozium/internal/cellninstall"
	"github.com/sympozium-ai/sympozium/internal/helmchart"
)

// A parameters-file holding everything a --set value could mangle travels
// from the backend flag through FleetValues, buildHelmValues (the parser
// runInstall uses) and the embedded chart into the configure DaemonSet's
// FLEET_BACKENDS unchanged; a backend without parameters carries no key.
func TestFleetBackendParametersFileReachesTheConfigureDaemonSet(t *testing.T) {
	const nasty = `{"chat_template_kwargs":{"enable_thinking":false,"note":"a,b=c {x} [y] \"q\" \\ back.slash\nline é"},"stop":["</s>",",","a=b"],"temperature":0.7,"top_k":40,"deep":{"er":{"flag":true}}}`
	path := filepath.Join(t.TempDir(), "parameters.json")
	if err := os.WriteFile(path, []byte(nasty), 0600); err != nil {
		t.Fatal(err)
	}
	backends, err := parseFleetBackends([]string{
		"name=native,provider=deepseek",
		"name=local,provider=llama-server,model=qwen.gguf,endpoint=http://10.0.0.5:8080,allow-insecure=true,parameters-file=" + path,
	})
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]any
	if err := json.Unmarshal([]byte(nasty), &want); err != nil {
		t.Fatal(err)
	}
	if backends[0].Model.Parameters != nil || !cellninstall.SameModelParameters(backends[1].Model.Parameters, want) {
		t.Fatalf("parsed parameters: %v", backends)
	}
	fleet, err := cellninstall.FleetValues(cellninstall.FleetOptions{Scope: "starter", Principal: "sympozium:celln", Publisher: "ed25519:operator",
		PackageImage: "registry.example/celln/starter@sha256:" + strings.Repeat("a", 64), PackageHash: "blake3:" + strings.Repeat("b", 64), Backends: backends})
	if err != nil {
		t.Fatal(err)
	}
	set, err := cellnInstallSetValues(context.Background(), "", "", nil, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	vals, err := buildHelmValues("", append(set, fleet...))
	if err != nil {
		t.Fatal(err)
	}
	ch, err := helmchart.Load()
	if err != nil {
		t.Fatal(err)
	}
	objects, err := renderReleaseObjects(ch, vals)
	if err != nil {
		t.Fatal(err)
	}
	var exported string
	for _, obj := range objects {
		if obj.GetKind() != "DaemonSet" || obj.GetName() != cellninstall.FleetConfigureDaemonSet {
			continue
		}
		var ds appsv1.DaemonSet
		raw, _ := json.Marshal(obj.Object)
		if err := json.Unmarshal(raw, &ds); err != nil {
			t.Fatal(err)
		}
		for _, env := range ds.Spec.Template.Spec.Containers[0].Env {
			if env.Name == "FLEET_BACKENDS" {
				exported = env.Value
			}
		}
	}
	var list []cellninstall.ExtraBackend
	if err := json.Unmarshal([]byte(exported), &list); err != nil || len(list) != 2 {
		t.Fatalf("FLEET_BACKENDS: %v: %q", err, exported)
	}
	if list[0].Parameters != nil || strings.Count(exported, `"parameters"`) != 1 {
		t.Fatalf("a backend without parameters exports them: %s", exported)
	}
	if !reflect.DeepEqual(list[1].Parameters, want) {
		t.Fatalf("parameters changed on the way:\n%v\n%v", list[1].Parameters, want)
	}
}

func TestFleetBackendParametersFileIsValidated(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	for path, refusal := range map[string]string{
		write("reserved.json", `{"max_tokens":4096}`): `"max_tokens" is reserved`,
		write("broken.json", `{"a":`):                 "not valid JSON",
		write("list.json", `[1]`):                     "a JSON object is required",
		filepath.Join(dir, "missing.json"):            "must be a regular absolute file",
		"relative.json":                               "must be a regular absolute file",
	} {
		if _, err := parseFleetBackends([]string{"name=local,provider=deepseek,parameters-file=" + path}); err == nil || !strings.Contains(err.Error(), refusal) {
			t.Fatalf("%s: want %q, got %v", path, refusal, err)
		}
	}
	if _, err := parseFleetBackends([]string{"name=local,provider=deepseek,parameters={}"}); err == nil || !strings.Contains(err.Error(), "parameters-file") {
		t.Fatalf("inline parameters: %v", err)
	}
}
