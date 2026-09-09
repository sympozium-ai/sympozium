package cellnparent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sympozium-ai/sympozium/internal/cellnauthority"
)

// ClaimRegisteredParent binds live intent, then durably claims one prepared
// incarnation in an operator-owned shared journal. Every issuer for this pool
// must use the same journal. It publishes no controller config and dispatches
// nothing. Removing records is not a supported way to replenish the pool.
func ClaimRegisteredParent(ctx context.Context, loader cellnauthority.Loader, frozen cellnauthority.ParentSelection, registration ParentLaunchRegistration, journal string) (RunApproval, error) {
	approval, err := BindRegisteredParent(ctx, loader, frozen, registration)
	if err != nil {
		return RunApproval{}, err
	}
	raw, err := json.Marshal(registration)
	if err != nil {
		return RunApproval{}, err
	}
	hash := sha256.Sum256(raw)
	if err := claimParentAssignment(journal, approval, fmt.Sprintf("sha256:%x", hash)); err != nil {
		return RunApproval{}, err
	}
	// Assignment may consume a slot during concurrent revocation. Do not refund
	// it or publish stale authority if the live sources changed while syncing.
	if err := loader.RevalidateParentSelection(ctx, frozen); err != nil {
		return RunApproval{}, err
	}
	return approval, nil
}

func claimParentAssignment(journal string, approval RunApproval, registrationHash string) error {
	if !filepath.IsAbs(journal) || !hashPattern.MatchString(approval.Binding.Incarnation) || approval.Binding.RunUID == "" {
		return fmt.Errorf("absolute operator journal and bound incarnation required")
	}
	raw, err := json.Marshal(struct {
		Version      string      `json:"apiVersion"`
		Registration string      `json:"registrationSHA256"`
		Approval     RunApproval `json:"approval"`
	}{"sympozium.ai/celln-parent-assignment-v1", registrationHash, approval})
	if err != nil || len(raw) > 16384 {
		return fmt.Errorf("parent assignment exceeds bound")
	}
	return publishParentRecord(journal, approval.Binding.Incarnation[7:]+".json", raw)
}

// Both assignment and approval publication use the same non-replacing durable
// primitive. Inputs come only from validated, operator-scoped callers.
func publishParentRecord(journal, name string, raw []byte) error {
	if !filepath.IsAbs(journal) || filepath.Base(name) != name || name == "." || name == ".." || len(raw) > 16384 {
		return fmt.Errorf("invalid bounded parent record location")
	}
	// Require a pre-existing operator directory. Never create arbitrary ancestors.
	dir, err := os.Open(journal)
	if err != nil {
		return fmt.Errorf("parent record directory unavailable")
	}
	defer dir.Close()
	info, err := dir.Stat()
	if err != nil || !info.IsDir() {
		return fmt.Errorf("parent record location is not a directory")
	}
	path := filepath.Join(journal, name)
	// Fully write and sync before atomically publishing without replacement.
	// Filesystems lacking hard-link or directory-sync semantics must refuse.
	tmp, err := os.CreateTemp(journal, ".parent-assignment-")
	if err != nil {
		return fmt.Errorf("cannot prepare assignment")
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(raw); err != nil {
		tmp.Close()
		return fmt.Errorf("assignment write failed")
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("assignment sync failed")
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("assignment close failed")
	}
	if err = os.Link(tmp.Name(), path); err != nil {
		if !os.IsExist(err) {
			return fmt.Errorf("atomic assignment publication unsupported or failed")
		}
		existing, readErr := boundedFile(path, 16384)
		if readErr != nil || !bytes.Equal(existing, raw) {
			return fmt.Errorf("parent incarnation already assigned differently or journal uncertain")
		}
	}
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("assignment directory sync failed; preserve original claim")
	}
	return nil
}
