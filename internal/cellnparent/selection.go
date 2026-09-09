package cellnparent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnauthority"
	"k8s.io/apimachinery/pkg/types"
)

// SelectRegisteredParent selects exactly one operator-prepared launch. It is
// not a pool allocator: ambiguity, exhaustion and changed choices fail closed.
// The shared journal pins the run's choice before claiming an incarnation, so
// interrupted publication cannot cause a later reconciliation to switch parents.
// No host request or credential read occurs here.
func SelectRegisteredParent(ctx context.Context, loader cellnauthority.Loader, key types.NamespacedName, registrations []ParentLaunchRegistration, journal, approvals string) (RunApproval, error) {
	var zero RunApproval
	if len(registrations) == 0 || len(registrations) > 1024 {
		return zero, fmt.Errorf("bounded prepared parent registrations required")
	}
	frozen, err := loader.FreezeParentRun(ctx, key)
	if err != nil {
		return zero, err
	}
	digest, err := ParentSelectionDigest(frozen.Snapshot)
	if err != nil {
		return zero, err
	}
	var run api.AgentRun
	if err := loader.Reader.Get(ctx, key, &run); err != nil {
		return zero, err
	}
	var selected *ParentLaunchRegistration
	for i := range registrations {
		entry := &registrations[i]
		if entry.SelectionSHA256 != digest || !reflect.DeepEqual(entry.Model, run.Spec.Model) || entry.SystemPrompt != run.Spec.SystemPrompt {
			continue
		}
		if selected != nil {
			return zero, fmt.Errorf("ambiguous prepared parent registrations")
		}
		selected = entry
	}
	if selected == nil {
		return zero, fmt.Errorf("no matching prepared parent registration")
	}
	approval, err := BindRegisteredParent(ctx, loader, *frozen, *selected)
	if err != nil {
		return zero, err
	}
	registrationJSON, err := json.Marshal(selected)
	if err != nil {
		return zero, err
	}
	registrationHash := sha256.Sum256(registrationJSON)
	choice, err := json.Marshal(struct {
		Version      string      `json:"apiVersion"`
		Registration string      `json:"registrationSHA256"`
		Approval     RunApproval `json:"approval"`
	}{"sympozium.ai/celln-parent-choice-v1", fmt.Sprintf("sha256:%x", registrationHash), approval})
	if err != nil {
		return zero, err
	}
	if err := publishParentRecord(journal, "choice-"+approvalFileName(approval.Binding.RunUID), choice); err != nil {
		return zero, fmt.Errorf("preserve original parent choice: %w", err)
	}
	return PublishRegisteredParent(ctx, loader, *frozen, *selected, journal, approvals)
}
