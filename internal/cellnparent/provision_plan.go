package cellnparent

import (
	"context"
	"encoding/json"
	"fmt"
	"unicode"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnauthority"
)

// NativeProvisionConfig is an operator-prepared parent/worker architecture.
// Nested Celln requests/configuration remain intact for Celln's strict parser
// and independent artifact/model admission; this is not tenant input.
type NativeProvisionConfig struct {
	AdmissionWindowMs   uint64          `json:"admissionWindowMs"`
	Parent              json.RawMessage `json:"parent"`
	Worker              json.RawMessage `json:"worker"`
	Template            json.RawMessage `json:"template"`
	ModelProfile        string          `json:"modelProfile"`
	ReservedMemoryBytes uint64          `json:"reservedMemoryBytes"`
	MaxTurns            uint64          `json:"maxTurns"`
	TurnModelRequests   uint64          `json:"turnModelRequests"`
	TurnOutputTokens    uint64          `json:"turnOutputTokens"`
	TotalModelRequests  uint64          `json:"totalModelRequests"`
	TotalOutputTokens   uint64          `json:"totalOutputTokens"`
}

// HostProvisionTemplate is a trusted operator assertion that this native
// architecture implements exactly this catalogue snapshot. It is not a proof
// of artifact compatibility; the host must still independently admit it.
type HostProvisionTemplate struct {
	APIVersion      string                `json:"apiVersion"`
	SelectionSHA256 string                `json:"selectionSHA256"`
	Scope           string                `json:"scope"`
	Principal       string                `json:"principal"`
	Model           api.ModelSpec         `json:"model"`
	HostLimits      api.EnduringRunSpec   `json:"hostLimits"`
	Native          NativeProvisionConfig `json:"native"`
}

type HostProvisionPlan struct {
	APIVersion   string `json:"apiVersion"`
	Scope        string `json:"scope"`
	RunUID       string `json:"runUid"`
	IntentSHA256 string `json:"intentSHA256"`
	NativeProvisionConfig
}

// BuildHostProvisionPlan performs no writes or host execution. A trusted caller
// must durably pin its template choice and revalidate intent around issuance.
func BuildHostProvisionPlan(ctx context.Context, loader cellnauthority.Loader, intent ProvisionIntent, template HostProvisionTemplate) ([]byte, error) {
	if template.APIVersion != "sympozium.ai/celln-parent-host-template-v1" || !boundedIdentity(template.Scope, 512) || !boundedIdentity(template.Principal, 512) {
		return nil, fmt.Errorf("versioned operator host template and identities required")
	}
	if err := intent.Revalidate(ctx, loader); err != nil {
		return nil, err
	}
	selection, err := ParentSelectionDigest(intent.Selection.Snapshot)
	if err != nil || selection != template.SelectionSHA256 {
		return nil, fmt.Errorf("host template differs from authorized selection")
	}
	n := template.Native
	var harness struct {
		Contract        string `json:"contract"`
		Task            string `json:"task"`
		System          string `json:"system"`
		Model           string `json:"model"`
		RequireToolCall bool   `json:"require_tool_call"`
	}
	if json.Unmarshal(n.Template, &harness) != nil || harness.Contract != "celln.json-tools/v1" || harness.Task != "" || harness.Model != template.Model.Model || harness.RequireToolCall != template.HostLimits.RequireToolCall {
		return nil, fmt.Errorf("host native harness policy mismatch")
	}
	if err := validateParentConfiguration(intent.Spec, template.Model, harness.System, template.HostLimits); err != nil {
		return nil, err
	}
	limits := template.HostLimits
	if n.MaxTurns != uint64(limits.MaxTurns) || n.TotalModelRequests != uint64(limits.MaxModelRequests) || n.TotalOutputTokens != uint64(limits.MaxOutputTokens) || n.AdmissionWindowMs < 1 || n.AdmissionWindowMs > 300000 || !hashPattern.MatchString(n.ModelProfile) || n.ReservedMemoryBytes == 0 {
		return nil, fmt.Errorf("host plan differs from registered limits")
	}
	for index, raw := range []json.RawMessage{n.Parent, n.Worker} {
		var request struct {
			Workload struct {
				Caller string `json:"caller"`
			} `json:"workload"`
			Capabilities struct {
				TimeoutMs   uint64 `json:"timeoutMs"`
				MemoryBytes uint64 `json:"memoryBytes"`
			} `json:"capabilities"`
		}
		if json.Unmarshal(raw, &request) != nil || request.Workload.Caller != template.Principal || request.Capabilities.MemoryBytes == 0 || request.Capabilities.TimeoutMs == 0 || (index == 0 && request.Capabilities.TimeoutMs != uint64(limits.LeaseSeconds)*1000) || (index == 1 && request.Capabilities.TimeoutMs > uint64(limits.LeaseSeconds)*1000) {
			return nil, fmt.Errorf("host request principal or lifetime mismatch")
		}
	}
	digest, err := intent.Digest()
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(HostProvisionPlan{APIVersion: "celln.parent-provision-plan/v1", Scope: template.Scope, RunUID: string(intent.Selection.Run.UID), IntentSHA256: digest, NativeProvisionConfig: n})
	if err != nil || len(raw) > 65536 {
		return nil, fmt.Errorf("bounded host provision plan required")
	}
	return raw, nil
}

func boundedIdentity(value string, limit int) bool {
	if len(value) == 0 || len(value) > limit {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
