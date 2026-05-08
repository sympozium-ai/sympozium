package controller

import (
	"strings"
	"testing"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestBuildInstance_ChannelAccessControlPrecedence(t *testing.T) {
	tests := []struct {
		name           string
		packAC         map[string]*sympoziumv1alpha1.ChannelAccessControl
		personaAC      map[string]*sympoziumv1alpha1.ChannelAccessControl
		wantAllowChats []string
	}{
		{
			name: "persona override wins over ensemble-level",
			packAC: map[string]*sympoziumv1alpha1.ChannelAccessControl{
				"discord": {AllowedChats: []string{"ensemble-channel"}},
			},
			personaAC: map[string]*sympoziumv1alpha1.ChannelAccessControl{
				"discord": {AllowedChats: []string{"persona-channel"}},
			},
			wantAllowChats: []string{"persona-channel"},
		},
		{
			name: "fallback to ensemble-level when persona has none",
			packAC: map[string]*sympoziumv1alpha1.ChannelAccessControl{
				"discord": {AllowedChats: []string{"ensemble-channel"}},
			},
			personaAC:      nil,
			wantAllowChats: []string{"ensemble-channel"},
		},
		{
			name:           "no access control at either level",
			packAC:         nil,
			personaAC:      nil,
			wantAllowChats: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &EnsembleReconciler{}
			pack := &sympoziumv1alpha1.Ensemble{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pack",
					Namespace: "default",
				},
				Spec: sympoziumv1alpha1.EnsembleSpec{
					ChannelConfigs:       map[string]string{"discord": "my-discord-secret"},
					ChannelAccessControl: tt.packAC,
				},
			}
			persona := &sympoziumv1alpha1.AgentConfigSpec{
				Name:                 "tech-lead",
				SystemPrompt:         "You are a tech lead.",
				Channels:             []string{"discord"},
				ChannelAccessControl: tt.personaAC,
			}

			inst := r.buildAgent(pack, persona, "test-pack-tech-lead", "")

			if len(inst.Spec.Channels) != 1 {
				t.Fatalf("expected 1 channel, got %d", len(inst.Spec.Channels))
			}
			ch := inst.Spec.Channels[0]
			if ch.Type != "discord" {
				t.Fatalf("expected channel type discord, got %s", ch.Type)
			}

			if tt.wantAllowChats == nil {
				if ch.AccessControl != nil {
					t.Errorf("expected nil AccessControl, got %+v", ch.AccessControl)
				}
				return
			}

			if ch.AccessControl == nil {
				t.Fatal("expected non-nil AccessControl")
			}
			if len(ch.AccessControl.AllowedChats) != len(tt.wantAllowChats) {
				t.Fatalf("AllowedChats = %v, want %v", ch.AccessControl.AllowedChats, tt.wantAllowChats)
			}
			for i, want := range tt.wantAllowChats {
				if ch.AccessControl.AllowedChats[i] != want {
					t.Errorf("AllowedChats[%d] = %q, want %q", i, ch.AccessControl.AllowedChats[i], want)
				}
			}
		})
	}
}

func TestBuildInstance_SubagentsPropagated(t *testing.T) {
	r := &EnsembleReconciler{}
	pack := &sympoziumv1alpha1.Ensemble{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pack",
			Namespace: "default",
		},
		Spec: sympoziumv1alpha1.EnsembleSpec{},
	}
	persona := &sympoziumv1alpha1.AgentConfigSpec{
		Name:         "lead-analyst",
		SystemPrompt: "You are the lead analyst.",
		Subagents: &sympoziumv1alpha1.SubagentsSpec{
			MaxDepth:            3,
			MaxConcurrent:       8,
			MaxChildrenPerAgent: 5,
		},
	}

	inst := r.buildAgent(pack, persona, "test-pack-lead-analyst", "")

	sub := inst.Spec.Agents.Default.Subagents
	if sub == nil {
		t.Fatal("expected Subagents to be propagated, got nil")
	}
	if sub.MaxDepth != 3 {
		t.Errorf("MaxDepth = %d, want 3", sub.MaxDepth)
	}
	if sub.MaxConcurrent != 8 {
		t.Errorf("MaxConcurrent = %d, want 8", sub.MaxConcurrent)
	}
	if sub.MaxChildrenPerAgent != 5 {
		t.Errorf("MaxChildrenPerAgent = %d, want 5", sub.MaxChildrenPerAgent)
	}
}

func TestBuildInstance_SubagentsNilWhenUnset(t *testing.T) {
	r := &EnsembleReconciler{}
	pack := &sympoziumv1alpha1.Ensemble{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pack",
			Namespace: "default",
		},
		Spec: sympoziumv1alpha1.EnsembleSpec{},
	}
	persona := &sympoziumv1alpha1.AgentConfigSpec{
		Name:         "worker",
		SystemPrompt: "You are a worker.",
	}

	inst := r.buildAgent(pack, persona, "test-pack-worker", "")

	if inst.Spec.Agents.Default.Subagents != nil {
		t.Errorf("expected Subagents to be nil for persona without subagents config, got %+v",
			inst.Spec.Agents.Default.Subagents)
	}
}

// ── Relationship graph validation tests ────────────────────────────────────

func testPersonas(names ...string) []sympoziumv1alpha1.AgentConfigSpec {
	out := make([]sympoziumv1alpha1.AgentConfigSpec, len(names))
	for i, n := range names {
		out[i] = sympoziumv1alpha1.AgentConfigSpec{Name: n}
	}
	return out
}

func TestValidateRelationshipGraph_NoCycle(t *testing.T) {
	personas := testPersonas("a", "b", "c")
	rels := []sympoziumv1alpha1.AgentConfigRelationship{
		{Source: "a", Target: "b", Type: "sequential"},
		{Source: "b", Target: "c", Type: "sequential"},
	}
	if err := validateRelationshipGraph(personas, rels, nil); err != nil {
		t.Errorf("expected no error for linear pipeline, got: %v", err)
	}
}

func TestValidateRelationshipGraph_Cycle(t *testing.T) {
	personas := testPersonas("a", "b", "c")
	rels := []sympoziumv1alpha1.AgentConfigRelationship{
		{Source: "a", Target: "b", Type: "sequential"},
		{Source: "b", Target: "c", Type: "sequential"},
		{Source: "c", Target: "a", Type: "sequential"},
	}
	err := validateRelationshipGraph(personas, rels, nil)
	if err == nil {
		t.Fatal("expected cycle error")
	}
	if !strings.Contains(err.Error(), "cycle detected") {
		t.Errorf("error should mention cycle, got: %v", err)
	}
}

func TestValidateRelationshipGraph_SelfLoop(t *testing.T) {
	personas := testPersonas("a")
	rels := []sympoziumv1alpha1.AgentConfigRelationship{
		{Source: "a", Target: "a", Type: "sequential"},
	}
	err := validateRelationshipGraph(personas, rels, nil)
	if err == nil {
		t.Fatal("expected cycle error for self-loop")
	}
}

func TestValidateRelationshipGraph_DanglingRef(t *testing.T) {
	personas := testPersonas("a", "b")
	rels := []sympoziumv1alpha1.AgentConfigRelationship{
		{Source: "a", Target: "nonexistent", Type: "sequential"},
	}
	err := validateRelationshipGraph(personas, rels, nil)
	if err == nil {
		t.Fatal("expected error for dangling reference")
	}
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("error should mention missing persona, got: %v", err)
	}
}

func TestValidateRelationshipGraph_IgnoresNonSequential(t *testing.T) {
	personas := testPersonas("a", "b")
	rels := []sympoziumv1alpha1.AgentConfigRelationship{
		{Source: "a", Target: "b", Type: "delegation"},
		{Source: "b", Target: "a", Type: "supervision"},
	}
	if err := validateRelationshipGraph(personas, rels, nil); err != nil {
		t.Errorf("non-sequential edges should not trigger cycle detection, got: %v", err)
	}
}

func TestValidateRelationshipGraph_EmptyRelationships(t *testing.T) {
	personas := testPersonas("a", "b")
	if err := validateRelationshipGraph(personas, nil, nil); err != nil {
		t.Errorf("empty relationships should pass, got: %v", err)
	}
}

// ── Stimulus validation tests ────────────────────────────────────────────────

func TestValidateRelationshipGraph_StimulusValid(t *testing.T) {
	personas := testPersonas("lead", "worker")
	stimulus := &sympoziumv1alpha1.StimulusSpec{
		Name:   "kickoff",
		Prompt: "Begin the research workflow",
	}
	rels := []sympoziumv1alpha1.AgentConfigRelationship{
		{Source: "kickoff", Target: "lead", Type: "stimulus"},
		{Source: "lead", Target: "worker", Type: "sequential"},
	}
	if err := validateRelationshipGraph(personas, rels, stimulus); err != nil {
		t.Errorf("expected valid stimulus config, got: %v", err)
	}
}

func TestValidateRelationshipGraph_StimulusNoSpec(t *testing.T) {
	personas := testPersonas("lead")
	rels := []sympoziumv1alpha1.AgentConfigRelationship{
		{Source: "kickoff", Target: "lead", Type: "stimulus"},
	}
	err := validateRelationshipGraph(personas, rels, nil)
	if err == nil {
		t.Fatal("expected error when stimulus relationship exists without spec")
	}
	if !strings.Contains(err.Error(), "no stimulus spec") {
		t.Errorf("error should mention missing spec, got: %v", err)
	}
}

func TestValidateRelationshipGraph_StimulusNoRelationship(t *testing.T) {
	personas := testPersonas("lead")
	stimulus := &sympoziumv1alpha1.StimulusSpec{
		Name:   "kickoff",
		Prompt: "Start",
	}
	rels := []sympoziumv1alpha1.AgentConfigRelationship{
		{Source: "lead", Target: "lead", Type: "delegation"},
	}
	err := validateRelationshipGraph(personas, rels, stimulus)
	if err == nil {
		t.Fatal("expected error when stimulus spec exists without relationship")
	}
	if !strings.Contains(err.Error(), "no stimulus relationship") {
		t.Errorf("error should mention missing relationship, got: %v", err)
	}
}

func TestValidateRelationshipGraph_StimulusEmptyPrompt(t *testing.T) {
	personas := testPersonas("lead")
	stimulus := &sympoziumv1alpha1.StimulusSpec{
		Name:   "kickoff",
		Prompt: "   ",
	}
	rels := []sympoziumv1alpha1.AgentConfigRelationship{
		{Source: "kickoff", Target: "lead", Type: "stimulus"},
	}
	err := validateRelationshipGraph(personas, rels, stimulus)
	if err == nil {
		t.Fatal("expected error for empty stimulus prompt")
	}
	if !strings.Contains(err.Error(), "prompt must not be empty") {
		t.Errorf("error should mention empty prompt, got: %v", err)
	}
}

func TestValidateRelationshipGraph_StimulusSourceMismatch(t *testing.T) {
	personas := testPersonas("lead")
	stimulus := &sympoziumv1alpha1.StimulusSpec{
		Name:   "kickoff",
		Prompt: "Begin",
	}
	rels := []sympoziumv1alpha1.AgentConfigRelationship{
		{Source: "wrong-name", Target: "lead", Type: "stimulus"},
	}
	err := validateRelationshipGraph(personas, rels, stimulus)
	if err == nil {
		t.Fatal("expected error when stimulus source doesn't match name")
	}
	if !strings.Contains(err.Error(), "must match stimulus name") {
		t.Errorf("error should mention name mismatch, got: %v", err)
	}
}

func TestValidateRelationshipGraph_StimulusDanglingTarget(t *testing.T) {
	personas := testPersonas("lead")
	stimulus := &sympoziumv1alpha1.StimulusSpec{
		Name:   "kickoff",
		Prompt: "Begin",
	}
	rels := []sympoziumv1alpha1.AgentConfigRelationship{
		{Source: "kickoff", Target: "nonexistent", Type: "stimulus"},
	}
	err := validateRelationshipGraph(personas, rels, stimulus)
	if err == nil {
		t.Fatal("expected error for dangling stimulus target")
	}
	if !strings.Contains(err.Error(), "unknown persona") {
		t.Errorf("error should mention unknown target, got: %v", err)
	}
}

func TestValidateRelationshipGraph_MultipleStimulusRelationships(t *testing.T) {
	personas := testPersonas("lead", "worker")
	stimulus := &sympoziumv1alpha1.StimulusSpec{
		Name:   "kickoff",
		Prompt: "Begin",
	}
	rels := []sympoziumv1alpha1.AgentConfigRelationship{
		{Source: "kickoff", Target: "lead", Type: "stimulus"},
		{Source: "kickoff", Target: "worker", Type: "stimulus"},
	}
	err := validateRelationshipGraph(personas, rels, stimulus)
	if err == nil {
		t.Fatal("expected error for multiple stimulus relationships")
	}
	if !strings.Contains(err.Error(), "at most one stimulus") {
		t.Errorf("error should mention multiple stimulus, got: %v", err)
	}
}

// ── Skill reconciliation tests ────────────────────────────────────────────────

func TestBuildDesiredSkills_Basic(t *testing.T) {
	pack := &sympoziumv1alpha1.Ensemble{
		Spec: sympoziumv1alpha1.EnsembleSpec{
			SkillParams: map[string]map[string]string{
				"github-gitops": {"repo": "my-org/my-repo"},
			},
		},
	}
	persona := &sympoziumv1alpha1.AgentConfigSpec{
		Skills: []string{"k8s-ops", "github-gitops"},
	}

	got := buildDesiredSkills(pack, persona)

	// Should have k8s-ops, github-gitops (with params), and memory (auto-added).
	if len(got) != 3 {
		t.Fatalf("expected 3 skills, got %d: %+v", len(got), got)
	}
	if got[0].SkillPackRef != "k8s-ops" {
		t.Errorf("expected first skill k8s-ops, got %s", got[0].SkillPackRef)
	}
	if got[1].SkillPackRef != "github-gitops" {
		t.Errorf("expected second skill github-gitops, got %s", got[1].SkillPackRef)
	}
	if got[1].Params["repo"] != "my-org/my-repo" {
		t.Errorf("expected github-gitops repo param, got %v", got[1].Params)
	}
	if got[2].SkillPackRef != "memory" {
		t.Errorf("expected memory skill auto-added, got %s", got[2].SkillPackRef)
	}
}

func TestBuildDesiredSkills_SkipsMCPBridge(t *testing.T) {
	pack := &sympoziumv1alpha1.Ensemble{}
	persona := &sympoziumv1alpha1.AgentConfigSpec{
		Skills: []string{"mcp-bridge", "k8s-ops"},
	}

	got := buildDesiredSkills(pack, persona)

	for _, s := range got {
		if s.SkillPackRef == "mcp-bridge" {
			t.Error("mcp-bridge should be filtered out")
		}
	}
}

func TestBuildDesiredSkills_MemoryNotDuplicated(t *testing.T) {
	pack := &sympoziumv1alpha1.Ensemble{}
	persona := &sympoziumv1alpha1.AgentConfigSpec{
		Skills: []string{"memory", "k8s-ops"},
	}

	got := buildDesiredSkills(pack, persona)

	memoryCount := 0
	for _, s := range got {
		if s.SkillPackRef == "memory" {
			memoryCount++
		}
	}
	if memoryCount != 1 {
		t.Errorf("expected exactly 1 memory skill, got %d", memoryCount)
	}
}

func TestBuildDesiredSkills_WebEndpoint(t *testing.T) {
	pack := &sympoziumv1alpha1.Ensemble{}
	persona := &sympoziumv1alpha1.AgentConfigSpec{
		Skills: []string{"k8s-ops"},
		WebEndpoint: &sympoziumv1alpha1.AgentConfigWebEndpoint{
			Enabled:  true,
			Hostname: "my-agent.example.com",
		},
	}

	got := buildDesiredSkills(pack, persona)

	var found bool
	for _, s := range got {
		if s.SkillPackRef == "web-endpoint" {
			found = true
			if s.Params["hostname"] != "my-agent.example.com" {
				t.Errorf("expected hostname param, got %v", s.Params)
			}
		}
	}
	if !found {
		t.Error("expected web-endpoint skill to be included")
	}
}

func TestSkillRefsEqual(t *testing.T) {
	tests := []struct {
		name string
		a, b []sympoziumv1alpha1.SkillRef
		want bool
	}{
		{
			name: "both nil",
			a:    nil,
			b:    nil,
			want: true,
		},
		{
			name: "equal with params",
			a: []sympoziumv1alpha1.SkillRef{
				{SkillPackRef: "k8s-ops"},
				{SkillPackRef: "github-gitops", Params: map[string]string{"repo": "org/repo"}},
			},
			b: []sympoziumv1alpha1.SkillRef{
				{SkillPackRef: "k8s-ops"},
				{SkillPackRef: "github-gitops", Params: map[string]string{"repo": "org/repo"}},
			},
			want: true,
		},
		{
			name: "different length",
			a:    []sympoziumv1alpha1.SkillRef{{SkillPackRef: "k8s-ops"}},
			b:    []sympoziumv1alpha1.SkillRef{{SkillPackRef: "k8s-ops"}, {SkillPackRef: "memory"}},
			want: false,
		},
		{
			name: "different skill",
			a:    []sympoziumv1alpha1.SkillRef{{SkillPackRef: "k8s-ops"}},
			b:    []sympoziumv1alpha1.SkillRef{{SkillPackRef: "github-gitops"}},
			want: false,
		},
		{
			name: "different params",
			a:    []sympoziumv1alpha1.SkillRef{{SkillPackRef: "x", Params: map[string]string{"a": "1"}}},
			b:    []sympoziumv1alpha1.SkillRef{{SkillPackRef: "x", Params: map[string]string{"a": "2"}}},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := skillRefsEqual(tt.a, tt.b); got != tt.want {
				t.Errorf("skillRefsEqual() = %v, want %v", got, tt.want)
			}
		})
	}
}
