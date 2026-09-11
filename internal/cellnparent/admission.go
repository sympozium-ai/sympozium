package cellnparent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"

	"github.com/sympozium-ai/sympozium/internal/celln"
	"unicode"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// SpecDigest binds the complete typed run spec, including lifecycle, catalogue
// revisions, initial task and limits. This is Go JSON encoding, not RFC JCS.
func SpecDigest(spec api.AgentRunSpec) (string, error) {
	raw, err := json.Marshal(spec)
	if err != nil {
		return "", err
	}
	if len(raw) > 262144 {
		return "", fmt.Errorf("parent run spec exceeds bound")
	}
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("sha256:%x", sum), nil
}

// ValidateAdmission consumes an independently operator-approved launch binding.
// Never construct approval from tenant annotations or reuse a one-shot grant.
func ValidateAdmission(run *api.AgentRun, approval api.CellnParentBinding) error {
	if run.Status.Phase != "" && run.Status.Phase != api.AgentRunPhasePending && run.Status.Phase != api.AgentRunPhaseRunning {
		return fmt.Errorf("parent startup cannot admit a terminal or unrelated lifecycle phase")
	}
	if run.UID == "" || run.DeletionTimestamp != nil || run.Spec.ExecutionLifecycle != "enduring" || run.Spec.ValidateLifecycle() != "" {
		return fmt.Errorf("parent admission requires a live enduring AgentRun")
	}
	digest, err := SpecDigest(run.Spec)
	if err != nil {
		return err
	}
	if approval.RunUID != string(run.UID) || approval.SpecSHA256 != digest || !hashPattern.MatchString(approval.LaunchProfile) || !hashPattern.MatchString(approval.Incarnation) || approval.Principal == "" || len(approval.Principal) > 512 {
		return fmt.Errorf("parent approval does not bind run identity and complete intent")
	}
	for _, ch := range approval.Principal {
		if unicode.IsControl(ch) {
			return fmt.Errorf("invalid parent principal")
		}
	}
	if err := validateOwnerOrigin(approval.Target); err != nil {
		return err
	}
	if run.Status.CellnParent != nil && run.Status.CellnParent.Binding != approval {
		return fmt.Errorf("frozen parent admission changed; reconcile original owner")
	}
	if run.Status.CellnRequest != "" || run.Status.CellnIssuance != nil || run.Status.CellnActionID != "" || run.Status.JobName != "" || run.Status.DeploymentName != "" {
		return fmt.Errorf("parent admission cannot replace another execution path")
	}
	return nil
}

func validateOwnerOrigin(target string) error {
	if len(target) > 2048 {
		return fmt.Errorf("parent approval requires an exact owner origin")
	}
	c, err := celln.New(celln.Config{BaseURL: target, AllowInsecure: os.Getenv("CELLN_ALLOW_INSECURE_HTTP") == "true"})
	if err != nil {
		return fmt.Errorf("parent owner requires protected transport: %w", err)
	}
	c.Close()
	return nil
}

// Prepare saves the immutable binding without network effects. ResourceVersion
// conflicts are returned to the reconciler, never overwritten with a new owner.
func Prepare(ctx context.Context, writer client.Client, reader client.Reader, key types.NamespacedName, approval api.CellnParentBinding) error {
	var run api.AgentRun
	if err := reader.Get(ctx, key, &run); err != nil {
		return err
	}
	if err := ValidateAdmission(&run, approval); err != nil {
		return err
	}
	if run.Status.CellnParent != nil {
		return nil
	}
	run.Status.CellnOnly = true
	run.Status.CellnParent = &api.CellnParentStatus{Binding: approval}
	return writer.Status().Update(ctx, &run)
}

// ClaimCreate persists the at-most-once attempt before a caller may POST. False
// means reconcile only, including after a crash between this write and dispatch.
func ClaimCreate(ctx context.Context, writer client.Client, reader client.Reader, key types.NamespacedName, approval api.CellnParentBinding) (bool, error) {
	var run api.AgentRun
	if err := reader.Get(ctx, key, &run); err != nil {
		return false, err
	}
	if err := ValidateAdmission(&run, approval); err != nil {
		return false, err
	}
	if run.Status.CellnParent == nil {
		return false, fmt.Errorf("parent must be durably prepared before creation")
	}
	if run.Status.CellnParent.CreateAttempted {
		return false, nil
	}
	run.Status.CellnParent.CreateAttempted = true
	if err := writer.Status().Update(ctx, &run); err != nil {
		return false, err
	}
	return true, nil
}
