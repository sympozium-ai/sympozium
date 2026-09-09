package cellnparent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnauthority"
	"k8s.io/apimachinery/pkg/types"
)

// ParentLaunchRegistration is trusted operator input for one already prepared
// host parent/worker launch. It is not supplied by an AgentRun or HTTP tenant.
// The host must independently validate the referenced launch and permit. This
// record does not verify bytes or renew an expired/consumed host incarnation.
type ParentLaunchRegistration struct {
	APIVersion      string              `json:"apiVersion"`
	SelectionSHA256 string              `json:"selectionSHA256"`
	Target          string              `json:"target"`
	Principal       string              `json:"principal"`
	LaunchProfile   string              `json:"launchProfile"`
	Incarnation     string              `json:"incarnation"`
	TokenFile       string              `json:"tokenFile"`
	CAFile          string              `json:"caFile,omitempty"`
	Model           api.ModelSpec       `json:"model"`
	SystemPrompt    string              `json:"systemPrompt"`
	HostLimits      api.EnduringRunSpec `json:"hostLimits"`
}

// ParentSelectionDigest binds the complete resolved Agent/runtime/tool/grant
// snapshot, not a mutable runtime name or merely the list of tool names.
func ParentSelectionDigest(selection cellnauthority.SelectionSnapshot) (string, error) {
	raw, err := json.Marshal(selection)
	if err != nil || len(raw) > 262144 {
		return "", fmt.Errorf("parent registration selection exceeds bound")
	}
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("sha256:%x", sum), nil
}

// BindRegisteredParent revalidates live selection and binds the exact current
// run to an operator-prepared launch. It performs no writes, HTTP, allocation or
// secret reads. A caller must durably reserve the one-use registration before
// publishing this approval; the host journal remains the final replay barrier.
func BindRegisteredParent(ctx context.Context, loader cellnauthority.Loader, frozen cellnauthority.ParentSelection, registration ParentLaunchRegistration) (RunApproval, error) {
	var zero RunApproval
	if registration.APIVersion != "sympozium.ai/celln-parent-registration-v1" || !filepath.IsAbs(registration.TokenFile) || (registration.CAFile != "" && !filepath.IsAbs(registration.CAFile)) {
		return zero, fmt.Errorf("trusted versioned parent registration and credential paths required")
	}
	digest, err := ParentSelectionDigest(frozen.Snapshot)
	if err != nil || digest != registration.SelectionSHA256 {
		return zero, fmt.Errorf("parent registration does not match selected subjects and grants")
	}
	if err := loader.RevalidateParentSelection(ctx, frozen); err != nil {
		return zero, err
	}
	var run api.AgentRun
	if err := loader.Reader.Get(ctx, types.NamespacedName{Namespace: frozen.Run.Namespace, Name: frozen.Run.Name}, &run); err != nil {
		return zero, err
	}
	current, err := cellnauthority.IdentifySubject("AgentRun", run.ObjectMeta, run.Spec)
	if err != nil || current != frozen.Run {
		return zero, fmt.Errorf("run changed before parent registration binding")
	}
	s := run.Spec
	if err := validateParentConfiguration(s, registration.Model, registration.SystemPrompt, registration.HostLimits); err != nil {
		return zero, err
	}
	approval := RunApproval{Namespace: run.Namespace, Name: run.Name, TokenFile: registration.TokenFile, CAFile: registration.CAFile, Binding: api.CellnParentBinding{Target: registration.Target, Principal: registration.Principal, RunUID: string(run.UID), SpecSHA256: current.SpecSHA256, LaunchProfile: registration.LaunchProfile, Incarnation: registration.Incarnation}}
	if err := ValidateAdmission(&run, approval.Binding); err != nil {
		return zero, err
	}
	return approval, nil
}

func validateParentConfiguration(s api.AgentRunSpec, model api.ModelSpec, prompt string, limits api.EnduringRunSpec) error {
	if s.Enduring == nil || s.ExecutionLifecycle != "enduring" || s.ValidateLifecycle() != "" {
		return fmt.Errorf("valid enduring intent required")
	}
	// These options cannot be silently ignored or reinterpreted by this native
	// parent/worker path. Registration is not an escape hatch into pod semantics.
	if err := validateNativeParentSpec(s); err != nil {
		return err
	}
	if !reflect.DeepEqual(s.Model, model) || s.SystemPrompt != prompt {
		return fmt.Errorf("registered parent model/persona differs from run intent")
	}
	if limits.RequireToolCall != s.Enduring.RequireToolCall {
		return fmt.Errorf("registered tool execution requirement differs from run intent")
	}
	valid := api.AgentRunSpec{Backend: "celln", ExecutionLifecycle: "enduring", CellnSelection: s.CellnSelection, Enduring: &limits}
	if valid.ValidateLifecycle() != "" || limits.LeaseSeconds > s.Enduring.LeaseSeconds || limits.MaxTurns > s.Enduring.MaxTurns || limits.MaxModelRequests > s.Enduring.MaxModelRequests || limits.MaxOutputTokens > s.Enduring.MaxOutputTokens {
		return fmt.Errorf("registered host ceilings exceed requested parent limits")
	}
	if s.Timeout != nil && s.Timeout.Duration.Milliseconds() < int64(limits.LeaseSeconds)*1000 {
		return fmt.Errorf("registered lease exceeds explicit run timeout")
	}
	return nil
}
