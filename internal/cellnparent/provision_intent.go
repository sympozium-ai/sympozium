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

// ProvisionIntent is a read-only snapshot for constructing an operator-local
// Celln provisioning plan. It binds the complete run spec and all resolved
// catalogue/grant identities. It grants no model/host authority by itself.
type ProvisionIntent struct {
	APIVersion string                         `json:"apiVersion"`
	Selection  cellnauthority.ParentSelection `json:"selection"`
	Spec       api.AgentRunSpec               `json:"spec"`
}

// PrepareProvisionIntent requires an uncached loader with operator-configured
// grant sources. Revalidate immediately before issuing host authority and again
// before publishing registration; a snapshot is not continuous authorization.
func PrepareProvisionIntent(ctx context.Context, loader cellnauthority.Loader, key types.NamespacedName) (*ProvisionIntent, error) {
	frozen, err := loader.FreezeParentRun(ctx, key)
	if err != nil {
		return nil, err
	}
	var run api.AgentRun
	if err := loader.Reader.Get(ctx, key, &run); err != nil {
		return nil, err
	}
	current, err := cellnauthority.IdentifySubject("AgentRun", run.ObjectMeta, run.Spec)
	if err != nil || current != frozen.Run {
		return nil, fmt.Errorf("run changed before parent provision intent")
	}
	if run.Status.CellnParent != nil || (run.Status.Phase != "" && run.Status.Phase != api.AgentRunPhasePending) {
		return nil, fmt.Errorf("only an unbound pending run may request parent provisioning")
	}
	if err := validateNativeParentSpec(run.Spec); err != nil {
		return nil, err
	}
	if err := loader.RevalidateParentSelection(ctx, *frozen); err != nil {
		return nil, err
	}
	intent := &ProvisionIntent{APIVersion: "sympozium.ai/celln-parent-provision-intent-v1", Selection: *frozen, Spec: *run.Spec.DeepCopy()}
	if _, err := intent.Digest(); err != nil {
		return nil, err
	}
	return intent, nil
}

// Digest is the opaque intentSHA256 consumed by Celln parent-provision. The
// host pins this identity plus its entire local plan; Go/Rust JSON encoders do
// not have to agree on encoding this snapshot. Digest alone does not authorize.
func (p ProvisionIntent) Digest() (string, error) {
	if p.APIVersion != "sympozium.ai/celln-parent-provision-intent-v1" {
		return "", fmt.Errorf("invalid parent provision intent version")
	}
	raw, err := json.Marshal(p)
	if err != nil || len(raw) > 524288 {
		return "", fmt.Errorf("parent provision intent exceeds bound")
	}
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("sha256:%x", sum), nil
}

func (p ProvisionIntent) Revalidate(ctx context.Context, loader cellnauthority.Loader) error {
	fresh, err := PrepareProvisionIntent(ctx, loader, types.NamespacedName{Namespace: p.Selection.Run.Namespace, Name: p.Selection.Run.Name})
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(p, *fresh) {
		return fmt.Errorf("parent provision intent or authority changed")
	}
	return nil
}

func validateNativeParentSpec(s api.AgentRunSpec) error {
	if s.Parent != nil || len(s.Env) != 0 || s.Sandbox != nil || s.AgentSandbox != nil || len(s.Skills) != 0 || s.ToolPolicy != nil || s.CanaryMode || s.DryRun || len(s.ImagePullSecrets) != 0 || s.Lifecycle != nil || len(s.Volumes) != 0 || len(s.VolumeMounts) != 0 || (s.UseContext != nil && !*s.UseContext) || (s.Cleanup != "" && s.Cleanup != "delete") {
		return fmt.Errorf("unsupported native parent execution semantics")
	}
	return nil
}
