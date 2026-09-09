package cellnparent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/sympozium-ai/sympozium/internal/cellnauthority"
)

func approvalFileName(runUID string) string {
	sum := sha256.Sum256([]byte(runUID))
	return fmt.Sprintf("run-%x.json", sum)
}

// PublishRegisteredParent durably assigns a prepared incarnation before
// publishing its exact per-run approval. Point CELLN_PARENT_CONFIG at the
// approvals directory to consume these records. No shared file is rewritten.
//
// This is an explicit approval action, not a background watcher. Revocation
// must withdraw the registration/grants before an automation calls this again;
// deleting only an approval file does not revoke the underlying registration.
func PublishRegisteredParent(ctx context.Context, loader cellnauthority.Loader, frozen cellnauthority.ParentSelection, registration ParentLaunchRegistration, journal, approvals string) (RunApproval, error) {
	approval, err := ClaimRegisteredParent(ctx, loader, frozen, registration, journal)
	if err != nil {
		return RunApproval{}, err
	}
	raw, err := json.Marshal(ApprovalConfig{APIVersion: "sympozium.ai/celln-parent-controller-v1", Approvals: []RunApproval{approval}})
	if err != nil {
		return RunApproval{}, err
	}
	if err := publishParentRecord(approvals, approvalFileName(approval.Binding.RunUID), raw); err != nil {
		return RunApproval{}, err
	}
	return approval, nil
}
