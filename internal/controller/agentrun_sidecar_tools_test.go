package controller

import (
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

func TestSidecarsHaveTools(t *testing.T) {
	none := []resolvedSidecar{
		{skillPackName: "a", sidecar: sympoziumv1alpha1.SkillSidecar{}},
	}
	if sidecarsHaveTools(none) {
		t.Error("expected false when no sidecar declares tools")
	}

	some := []resolvedSidecar{
		{skillPackName: "a", sidecar: sympoziumv1alpha1.SkillSidecar{}},
		{skillPackName: "b", sidecar: sympoziumv1alpha1.SkillSidecar{
			Tools: []sympoziumv1alpha1.SidecarTool{{Name: "t", Exec: []string{"true"}}},
		}},
	}
	if !sidecarsHaveTools(some) {
		t.Error("expected true when a sidecar declares tools")
	}
}

func TestBuildSidecarToolsManifest(t *testing.T) {
	params := apiextensionsv1.JSON{Raw: []byte(`{"type":"object","properties":{"serviceIdentifier":{"type":"string"}}}`)}

	sidecars := []resolvedSidecar{
		{
			skillPackName: "Service-Discovery", // mixed case to verify normalization
			sidecar: sympoziumv1alpha1.SkillSidecar{
				Tools: []sympoziumv1alpha1.SidecarTool{
					{
						Name:           "sd_evaluate",
						Description:    "Evaluate changes",
						Exec:           []string{"node", "/app/dist/cli.js"},
						Subcommand:     "evaluate-changes",
						InputMode:      "stdin",
						PositionalArgs: []string{"serviceIdentifier"},
						Parameters:     &params,
					},
					{
						Name: "sd_list", // no InputMode / no Parameters -> defaults applied
						Exec: []string{"node", "/app/dist/cli.js"},
					},
				},
			},
		},
	}

	out, err := buildSidecarToolsManifest(sidecars)
	if err != nil {
		t.Fatalf("buildSidecarToolsManifest: %v", err)
	}

	var manifest struct {
		Tools []struct {
			Name           string          `json:"name"`
			Target         string          `json:"target"`
			Exec           []string        `json:"exec"`
			Subcommand     string          `json:"subcommand"`
			InputMode      string          `json:"inputMode"`
			PositionalArgs []string        `json:"positionalArgs"`
			Parameters     json.RawMessage `json:"parameters"`
		} `json:"tools"`
	}
	if err := json.Unmarshal([]byte(out), &manifest); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}

	if len(manifest.Tools) != 2 {
		t.Fatalf("got %d tools, want 2", len(manifest.Tools))
	}

	first := manifest.Tools[0]
	// Target is controller-derived from the SkillPack name (normalized), never
	// taken from the manifest input — this is the trust boundary.
	if first.Target != "service-discovery" {
		t.Errorf("target = %q, want service-discovery (normalized from SkillPack name)", first.Target)
	}
	if first.InputMode != "stdin" {
		t.Errorf("inputMode = %q, want stdin", first.InputMode)
	}
	if len(first.Exec) != 2 || first.Exec[0] != "node" {
		t.Errorf("exec = %v, want [node /app/dist/cli.js]", first.Exec)
	}

	second := manifest.Tools[1]
	if second.InputMode != "args" {
		t.Errorf("default inputMode = %q, want args", second.InputMode)
	}
	// A tool with no Parameters gets a default empty object schema, never null.
	var schema map[string]any
	if err := json.Unmarshal(second.Parameters, &schema); err != nil {
		t.Fatalf("default parameters not valid JSON: %v", err)
	}
	if schema["type"] != "object" {
		t.Errorf("default parameters = %v, want an object schema", schema)
	}
}

func TestBuildSidecarToolsManifest_Empty(t *testing.T) {
	out, err := buildSidecarToolsManifest(nil)
	if err != nil {
		t.Fatalf("buildSidecarToolsManifest(nil): %v", err)
	}
	if out != `{"tools":[]}` {
		t.Errorf("empty manifest = %q, want an empty tools array", out)
	}
}

// TestSidecarTools_PodSpecWiring verifies that when a resolved sidecar declares
// native tools, the agent pod gets the read-only sidecar-tools volume, the agent
// container mounts it read-only, and the manifest-path env is set. With no tools
// declared, none of that wiring appears.
func TestSidecarTools_PodSpecWiring(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()

	withTools := []resolvedSidecar{{
		skillPackName: "svc",
		sidecar: sympoziumv1alpha1.SkillSidecar{
			Image: "example/sidecar:latest",
			Tools: []sympoziumv1alpha1.SidecarTool{{Name: "svc_eval", Exec: []string{"true"}}},
		},
	}}

	// Volume present and references the controller-written ConfigMap.
	vols := r.buildVolumes(run, false, withTools, nil)
	var vol *corev1.Volume
	for i := range vols {
		if vols[i].Name == "sidecar-tools" {
			vol = &vols[i]
		}
	}
	if vol == nil {
		t.Fatal("expected a sidecar-tools volume")
	}
	if vol.ConfigMap == nil || vol.ConfigMap.Name != run.Name+"-sidecar-tools" {
		t.Errorf("sidecar-tools volume should source ConfigMap %q, got %+v", run.Name+"-sidecar-tools", vol.VolumeSource)
	}

	// Agent container gets the read-only mount + manifest-path env.
	cs, _ := r.buildContainers(run, false, nil, withTools, nil, nil)
	agent := cs[0]
	var mountOK bool
	for _, m := range agent.VolumeMounts {
		if m.Name == "sidecar-tools" {
			if m.MountPath != "/config/sidecar-tools" || !m.ReadOnly {
				t.Errorf("sidecar-tools mount must be read-only at /config/sidecar-tools, got %+v", m)
			}
			mountOK = true
		}
	}
	if !mountOK {
		t.Error("agent container is missing the sidecar-tools mount")
	}
	var envOK bool
	for _, e := range agent.Env {
		if e.Name == "SIDECAR_TOOLS_MANIFEST_PATH" {
			if e.Value != "/config/sidecar-tools/sidecar-tools.json" {
				t.Errorf("SIDECAR_TOOLS_MANIFEST_PATH = %q", e.Value)
			}
			envOK = true
		}
	}
	if !envOK {
		t.Error("agent container is missing SIDECAR_TOOLS_MANIFEST_PATH env")
	}

	// No tools -> no wiring.
	noTools := []resolvedSidecar{{skillPackName: "svc", sidecar: sympoziumv1alpha1.SkillSidecar{Image: "x"}}}
	for _, v := range r.buildVolumes(run, false, noTools, nil) {
		if v.Name == "sidecar-tools" {
			t.Error("sidecar-tools volume must not appear when no tools are declared")
		}
	}
}
