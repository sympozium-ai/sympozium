package cellnparent

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/sympozium-ai/sympozium/internal/cellnauthority"
)

// These are operator-template consistency tests, not artifact/KVM admission.
func testHostProvisionPlan(t *testing.T, ctx context.Context, loader cellnauthority.Loader, intent ProvisionIntent) {
	t.Helper()
	selection, err := ParentSelectionDigest(intent.Selection.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	request := func(timeout int) json.RawMessage {
		raw, err := json.Marshal(map[string]any{"workload": map[string]any{"caller": "tenant"}, "capabilities": map[string]any{"timeoutMs": timeout, "memoryBytes": 268435456}})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	template := HostProvisionTemplate{
		APIVersion: "sympozium.ai/celln-parent-host-template-v1", SelectionSHA256: selection,
		Scope: "test-cluster", Principal: "tenant", Model: intent.Spec.Model, HostLimits: *intent.Spec.Enduring,
		Native: NativeProvisionConfig{AdmissionWindowMs: 60000, Parent: request(180000), Worker: request(60000),
			Template:     json.RawMessage(`{"contract":"celln.json-tools/v1","task":"","system":"","model":"deepseek-chat","tools":[],"max_turns":1,"max_calls":0}`),
			ModelProfile: "blake3:" + strings.Repeat("a", 64), ReservedMemoryBytes: 1610612736,
			MaxTurns: 4, TurnModelRequests: 3, TurnOutputTokens: 1536, TotalModelRequests: 12, TotalOutputTokens: 4096},
	}
	raw, err := BuildHostProvisionPlan(ctx, loader, intent, template)
	if err != nil {
		t.Fatal(err)
	}
	var plan HostProvisionPlan
	if err := json.Unmarshal(raw, &plan); err != nil {
		t.Fatal(err)
	}
	digest, _ := intent.Digest()
	if plan.APIVersion != "celln.parent-provision-plan/v1" || plan.RunUID != string(intent.Selection.Run.UID) || plan.IntentSHA256 != digest || plan.Scope != template.Scope || !reflect.DeepEqual(plan.NativeProvisionConfig, template.Native) {
		t.Fatal("host wire plan lost authority or native configuration")
	}
	retry, err := BuildHostProvisionPlan(ctx, loader, intent, template)
	if err != nil || string(raw) != string(retry) {
		t.Fatal("host plan retry drifted")
	}
	testLocalProvisioner(t, ctx, loader, intent, template)
	for name, change := range map[string]func(*HostProvisionTemplate){
		"version":   func(p *HostProvisionTemplate) { p.APIVersion = "unknown" },
		"scope":     func(p *HostProvisionTemplate) { p.Scope = "" },
		"principal": func(p *HostProvisionTemplate) { p.Principal = "other" },
		"selection": func(p *HostProvisionTemplate) { p.SelectionSHA256 = "sha256:" + strings.Repeat("b", 64) },
		"model":     func(p *HostProvisionTemplate) { p.Model.Model = "other" },
		"turns":     func(p *HostProvisionTemplate) { p.Native.MaxTurns++ },
		"totals":    func(p *HostProvisionTemplate) { p.Native.TotalModelRequests++ },
		"lease":     func(p *HostProvisionTemplate) { p.Native.Parent = request(180001) },
		"window":    func(p *HostProvisionTemplate) { p.Native.AdmissionWindowMs = 300001 },
		"persona": func(p *HostProvisionTemplate) {
			p.Native.Template = json.RawMessage(`{"contract":"celln.json-tools/v1","model":"deepseek-chat","system":"unapproved"}`)
		},
		"tool-choice": func(p *HostProvisionTemplate) {
			p.Native.Template = json.RawMessage(`{"contract":"celln.json-tools/v1","model":"deepseek-chat","require_tool_call":true}`)
		},
		"task-in-template": func(p *HostProvisionTemplate) {
			p.Native.Template = json.RawMessage(`{"contract":"celln.json-tools/v1","model":"deepseek-chat","task":"silently replace first turn"}`)
		},
	} {
		t.Run("host-plan-"+name, func(t *testing.T) {
			changed := template
			change(&changed)
			if _, err := BuildHostProvisionPlan(ctx, loader, intent, changed); err == nil {
				t.Fatal("mismatched host plan accepted")
			}
		})
	}
}
