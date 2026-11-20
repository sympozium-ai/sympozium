package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSkillPack lays files out the way a ConfigMap volume does: the real
// files live in a hidden ..data directory and each key is a symlink to it.
func writeSkillPack(t *testing.T, root, pack string, files map[string]string) {
	t.Helper()
	data := filepath.Join(root, pack, "..data")
	if err := os.MkdirAll(data, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(data, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join("..data", name), filepath.Join(root, pack, name)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLoadSkillIndex(t *testing.T) {
	root := t.TempDir()
	writeSkillPack(t, root, "k8s-ops", map[string]string{
		"troubleshoot.md": "---\nname: troubleshoot\ndescription: 'Diagnose \"pods\": crash loops'\n---\n\n## Steps\nRun kubectl.",
		"scale.md":        "---\nname: scale\ndescription: \"Scale\\n  workloads\"\n---\nbody",
	})
	writeSkillPack(t, root, "team-notes", map[string]string{
		"legacy.md": "# legacy\n\n> Written by an older controller\n\nbody",
		"notes.txt": "Plain notes without a heading.",
		"empty.md":  "  \n",
	})
	// A file directly under /skills (the flat layout of older controllers).
	if err := os.WriteFile(filepath.Join(root, "top.md"), []byte("# Top\n\nTop-level skill."), 0o644); err != nil {
		t.Fatal(err)
	}

	got := map[string]string{}
	var order []string
	for _, s := range loadSkillIndex(root) {
		got[s.Name] = s.Description
		order = append(order, s.Name)
	}
	want := map[string]string{
		"k8s-ops/scale":        "Scale workloads",
		"k8s-ops/troubleshoot": `Diagnose "pods": crash loops`,
		"team-notes/legacy":    "Written by an older controller",
		"team-notes/notes.txt": "Plain notes without a heading.",
		"top":                  "Top-level skill.",
	}
	if len(got) != len(want) {
		t.Errorf("indexed %v, want %d skills (hidden entries and empty files skipped)", order, len(want))
	}
	for name, desc := range want {
		if got[name] != desc {
			t.Errorf("skill %q description = %q, want %q", name, got[name], desc)
		}
	}
	if strings.Join(order, ",") != "k8s-ops/scale,k8s-ops/troubleshoot,team-notes/legacy,team-notes/notes.txt,top" {
		t.Errorf("skills not sorted by name: %v", order)
	}
}

func TestLoadSkillIndex_MissingDir(t *testing.T) {
	if skills := loadSkillIndex(filepath.Join(t.TempDir(), "absent")); skills != nil {
		t.Errorf("expected no skills, got %v", skills)
	}
}

func TestSplitFrontmatter(t *testing.T) {
	cases := []struct {
		name, in, wantDesc, wantBody string
	}{
		{"none", "# Title\nbody", "", "# Title\nbody"},
		{"crlf", "---\r\nname: a\r\ndescription: d\r\n---\r\nbody", "d", "body"},
		{"unterminated", "---\nname: a\nbody", "", "---\nname: a\nbody"},
		{"not yaml", "---\n: : :\n---\nbody", "", "---\n: : :\n---\nbody"},
		{"horizontal rule only", "---\n", "", "---\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			meta, body := splitFrontmatter(tc.in)
			if meta.Description != tc.wantDesc || body != tc.wantBody {
				t.Errorf("splitFrontmatter(%q) = (%q, %q), want (%q, %q)",
					tc.in, meta.Description, body, tc.wantDesc, tc.wantBody)
			}
		})
	}
}

func TestSkillsToolEnabled(t *testing.T) {
	skills := []skillEntry{{Name: "k8s-ops/troubleshoot"}}
	cases := []struct {
		name         string
		skills       []skillEntry
		toolsEnabled bool
		allow, deny  string
		want         bool
	}{
		{"no policy", skills, true, "", "", true},
		{"tools disabled", skills, false, "", "", false},
		{"no skills", nil, true, "", "", false},
		{"denied", skills, true, "", "skills", false},
		{"deny all", skills, true, "", "*", false},
		{"allow list without skills", skills, true, "read_file", "", false},
		{"allow list with skills", skills, true, "read_file,skills", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := skillsToolEnabled(tc.skills, tc.toolsEnabled, tc.allow, tc.deny); got != tc.want {
				t.Errorf("skillsToolEnabled = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBuildSystemPrompt_Skills(t *testing.T) {
	root := t.TempDir()
	writeSkillPack(t, root, "k8s-ops", map[string]string{
		"troubleshoot.md": "---\nname: troubleshoot\ndescription: Diagnose pods\n---\n\nSECRET-STEP-BODY",
		"plain.md":        "PLAIN-BODY",
	})
	skills := loadSkillIndex(root)

	t.Run("skills tool points at the catalog", func(t *testing.T) {
		prompt := buildSystemPrompt("base", skills, true, true)
		if strings.Contains(prompt, "SECRET-STEP-BODY") || strings.Contains(prompt, "PLAIN-BODY") {
			t.Errorf("skill bodies must not be inlined when the skills tool is offered:\n%s", prompt)
		}
		if !strings.Contains(prompt, "`skills`") {
			t.Errorf("prompt should point the model at the skills tool:\n%s", prompt)
		}
	})

	t.Run("without the tool bodies are inlined", func(t *testing.T) {
		prompt := buildSystemPrompt("base", skills, false, true)
		for _, want := range []string{"# troubleshoot\n\n> Diagnose pods\n\nSECRET-STEP-BODY", "PLAIN-BODY"} {
			if !strings.Contains(prompt, want) {
				t.Errorf("inlined prompt missing %q:\n%s", want, prompt)
			}
		}
		if strings.Contains(prompt, "description:") {
			t.Errorf("frontmatter leaked into the inlined prompt:\n%s", prompt)
		}
	})

	t.Run("no skills adds no section", func(t *testing.T) {
		if prompt := buildSystemPrompt("base", nil, false, false); prompt != "base" {
			t.Errorf("prompt = %q, want just the base", prompt)
		}
	})
}

func TestSkillsTool(t *testing.T) {
	root := t.TempDir()
	writeSkillPack(t, root, "k8s-ops", map[string]string{
		"troubleshoot.md": "---\nname: troubleshoot\ndescription: Diagnose pods\n---\n\n## Steps\nRun kubectl.\n",
	})
	skills := loadSkillIndex(root)
	registerSkills(skills)
	t.Cleanup(func() { registerSkills(nil) })

	def := skillsToolDef(skills)
	if def.Name != ToolSkills || !strings.Contains(def.Description, "- `k8s-ops/troubleshoot`: Diagnose pods") {
		t.Errorf("tool definition missing the catalog entry:\n%s", def.Description)
	}
	enum := def.Parameters["properties"].(map[string]any)["name"].(map[string]any)["enum"].([]string)
	if len(enum) != 1 || enum[0] != "k8s-ops/troubleshoot" {
		t.Errorf("enum = %v", enum)
	}

	call := func(args map[string]any) string {
		raw, _ := json.Marshal(args)
		return executeToolCall(t.Context(), ToolSkills, string(raw))
	}
	if got := call(map[string]any{"name": "k8s-ops/troubleshoot"}); got != "## Steps\nRun kubectl." {
		t.Errorf("skill body = %q, want the body without frontmatter", got)
	}
	if got := call(map[string]any{"name": "k8s-ops/missing"}); !strings.HasPrefix(got, "Error: unknown skill") {
		t.Errorf("unknown skill result = %q", got)
	}
	if got := call(map[string]any{}); !strings.HasPrefix(got, "Error: 'name' is required") {
		t.Errorf("missing name result = %q", got)
	}
}

func TestSkillsToolNameReservedForSidecarTools(t *testing.T) {
	if !sidecarToolNameReserved(ToolSkills) {
		t.Errorf("a sidecar tool named %q would shadow the skills tool", ToolSkills)
	}
}
