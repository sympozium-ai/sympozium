package cellnparent

import (
	"context"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// StartObservation describes startup only, not turn execution or completion.
type StartObservation struct {
	Prepared         bool
	CreationAccepted bool
	Owner            *Status
}

// ReconcileStart performs one durable startup step. First reconciliation saves
// approval without dispatch; the next claims the attempt before POST. Subsequent
// reconciliations only GET the frozen incarnation, including after a lost reply.
// A missing owner after an attempted create is uncertainty, never retry authority.
func ReconcileStart(ctx context.Context, writer client.Client, reader client.Reader, key types.NamespacedName, configPath string) (StartObservation, error) {
	var run api.AgentRun
	if err := reader.Get(ctx, key, &run); err != nil {
		return StartObservation{}, err
	}
	approval, transport, err := LoadApproval(configPath, &run)
	if err != nil {
		return StartObservation{}, err
	}
	defer transport.Close()
	if run.Status.CellnParent == nil {
		if err := Prepare(ctx, writer, reader, key, approval); err != nil {
			return StartObservation{}, err
		}
		return StartObservation{Prepared: true}, nil
	}
	claimed, err := ClaimCreate(ctx, writer, reader, key, approval)
	if err != nil {
		return StartObservation{}, err
	}
	if claimed {
		if err := transport.Create(ctx, approval.LaunchProfile, approval.Incarnation); err != nil {
			return StartObservation{}, err
		}
		return StartObservation{Prepared: true, CreationAccepted: true}, nil
	}
	owner, err := transport.Status(ctx, approval.Incarnation)
	if err != nil {
		return StartObservation{}, err
	}
	return StartObservation{Prepared: true, Owner: &owner}, nil
}
