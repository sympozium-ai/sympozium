package controller

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/yaml"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// Each skill ConfigMap mounts read-only at /skills/<ref>/ under a writable
// /skills parent. The parent must not be read-only: the container runtime
// creates the /skills/<ref> mountpoints inside it, and on the agent's
// read-only root filesystem a read-only parent fails container start.
func TestAgentVolumeMounts_PerPackSkills(t *testing.T) {
	mounts := agentVolumeMounts([]sympoziumv1alpha1.SkillRef{
		{SkillPackRef: "k8s-ops"},
		{ConfigMapRef: "team-notes"},
		{SkillPackRef: "k8s-ops"}, // duplicate ref mounts once
	})

	var got []string
	for _, m := range mounts {
		if m.MountPath == "/skills" {
			if m.ReadOnly {
				t.Error("/skills parent mount must be writable so per-pack mountpoints can be created")
			}
			continue
		}
		if !strings.HasPrefix(m.MountPath, "/skills/") {
			continue
		}
		if !m.ReadOnly {
			t.Errorf("skill mount %q must be read-only", m.MountPath)
		}
		got = append(got, m.MountPath+"="+m.Name)
	}
	want := []string{
		"/skills/k8s-ops=" + skillVolumeName("k8s-ops"),
		"/skills/team-notes=" + skillVolumeName("team-notes"),
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("skill mounts = %v, want %v", got, want)
	}
}

// Every mount on the agent container must name a volume the pod declares,
// or the API server rejects the pod.
func TestBuildContainers_SkillMountsHaveVolumes(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	run.Spec.Skills = []sympoziumv1alpha1.SkillRef{
		{SkillPackRef: "k8s-ops"},
		{ConfigMapRef: "skills.example.com"},
	}
	containers, _, err := r.buildContainers(run, false, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildContainers: %v", err)
	}
	volumes := map[string]bool{}
	for _, v := range r.buildVolumes(run, false, nil, nil) {
		volumes[v.Name] = true
	}
	for _, m := range containers[0].VolumeMounts {
		if !volumes[m.Name] {
			t.Errorf("agent mount %q at %s has no matching pod volume", m.Name, m.MountPath)
		}
	}
}

// A harness replaces the agent container but keeps its skill mounts, so an
// adapter sees the same /skills/<ref>/ layout as agent-runner.
func TestBuildContainers_HarnessKeepsSkillMounts(t *testing.T) {
	r := &AgentRunReconciler{}
	run := harnessModeRun(nil)
	run.Spec.Skills = []sympoziumv1alpha1.SkillRef{{SkillPackRef: "k8s-ops"}}
	containers, _, err := r.buildContainers(run, false, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildContainers: %v", err)
	}
	for _, m := range containers[0].VolumeMounts {
		if m.MountPath == "/skills/k8s-ops" && m.ReadOnly {
			return
		}
	}
	t.Errorf("harness container lost the /skills/k8s-ops mount: %+v", containers[0].VolumeMounts)
}

func TestSkillVolumeName(t *testing.T) {
	long := strings.Repeat("a", 70)
	refs := []string{
		"k8s-ops",
		"skills.example.com",
		"skills-example-com",
		long,
		long + "b",
		strings.Repeat("x", 47), // exactly fills the 63-character limit
	}
	seen := map[string]string{}
	for _, ref := range refs {
		name := skillVolumeName(ref)
		if errs := validation.IsDNS1123Label(name); len(errs) != 0 {
			t.Errorf("skillVolumeName(%q) = %q is not a valid volume name: %v", ref, name, errs)
		}
		if !strings.HasPrefix(name, skillVolumePrefix) {
			t.Errorf("skillVolumeName(%q) = %q lacks the reserved prefix", ref, name)
		}
		if other, dup := seen[name]; dup {
			t.Errorf("skillVolumeName(%q) and skillVolumeName(%q) collide on %q", ref, other, name)
		}
		seen[name] = ref
		if again := skillVolumeName(ref); again != name {
			t.Errorf("skillVolumeName(%q) is not deterministic: %q then %q", ref, name, again)
		}
	}
	if got := skillVolumeName("k8s-ops"); got != "sympozium-skill-k8s-ops" {
		t.Errorf("a ref that fits should be used as is, got %q", got)
	}
}

func TestIsReservedVolumeName_SkillPrefix(t *testing.T) {
	if !isReservedVolumeName(skillVolumeName("k8s-ops")) {
		t.Error("per-pack skill volume names must be reserved")
	}
	if isReservedVolumeName("skill-cache") {
		t.Error("only the full skill volume prefix is reserved")
	}
}

func TestRenderSkillContent(t *testing.T) {
	t.Run("frontmatter round-trips awkward descriptions", func(t *testing.T) {
		desc := `Use for "pods": crash loops, OOMs; # not a comment` + "\nsecond line"
		out, err := renderSkillContent("k8s-troubleshoot", desc, "## Steps\n\nRun kubectl.")
		if err != nil {
			t.Fatalf("renderSkillContent: %v", err)
		}
		header, body, ok := strings.Cut(strings.TrimPrefix(out, "---\n"), "---\n\n")
		if !strings.HasPrefix(out, "---\n") || !ok {
			t.Fatalf("missing frontmatter fences:\n%s", out)
		}
		var fm skillFrontmatter
		if err := yaml.Unmarshal([]byte(header), &fm); err != nil {
			t.Fatalf("frontmatter is not valid YAML: %v\n%s", err, header)
		}
		if fm.Name != "k8s-troubleshoot" || fm.Description != desc {
			t.Errorf("frontmatter = %+v, want name and description preserved", fm)
		}
		if body != "## Steps\n\nRun kubectl." {
			t.Errorf("body = %q", body)
		}
	})

	t.Run("no description omits the key", func(t *testing.T) {
		out, err := renderSkillContent("notes", "", "body")
		if err != nil {
			t.Fatalf("renderSkillContent: %v", err)
		}
		if strings.Contains(out, "description") {
			t.Errorf("unexpected description key:\n%s", out)
		}
	})

	t.Run("author frontmatter is kept", func(t *testing.T) {
		authored := "---\nname: mine\ndescription: hand-written\n---\nbody"
		out, err := renderSkillContent("ignored", "ignored", authored)
		if err != nil {
			t.Fatalf("renderSkillContent: %v", err)
		}
		if out != authored {
			t.Errorf("author frontmatter was rewritten:\n%s", out)
		}
	})
}
