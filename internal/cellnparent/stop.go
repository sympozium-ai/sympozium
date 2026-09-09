package cellnparent

import (
	"context"
	"fmt"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ReconcileStop joins only the frozen owner. Unlike startup, cleanup must remain
// possible after spec drift or deletion. It does not admit changed intent and
// never dispatches work. Success means an explicit host teardown acknowledgement;
// missing owners, historical context loss and transport failures remain uncertain.
func ReconcileStop(ctx context.Context, reader client.Reader, key types.NamespacedName, configPath string) error {
	var run api.AgentRun
	if err := reader.Get(ctx, key, &run); err != nil {
		return err
	}
	if run.Status.CellnParent == nil || !run.Status.CellnParent.CreateAttempted {
		return fmt.Errorf("no attempted parent to confirm; reconcile local admission separately")
	}
	binding, transport, err := loadBinding(configPath, &run, true)
	if err != nil {
		return err
	}
	defer transport.Close()
	return transport.Stop(ctx, binding.Incarnation)
}
