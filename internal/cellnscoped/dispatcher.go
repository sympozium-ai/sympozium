package cellnscoped

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnauthority"
	cap "github.com/sympozium-ai/sympozium/internal/cellncapability"
	"github.com/sympozium-ai/sympozium/internal/modelgateway"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
)

type Dispatcher struct {
	ClusterID string
	Store     cellnauthority.PreparedStore
	Receiver  *NativeClient
	Gateway   *GatewayClient
	Issuer    *cap.Issuer
}

func (d *Dispatcher) Prepare(ctx context.Context, key types.NamespacedName) (*cellnauthority.StoredPreparation, error) {
	if d == nil || d.Receiver == nil || d.Issuer == nil || d.ClusterID == "" {
		return nil, errors.New("scoped dispatcher is not configured")
	}
	request := cellnauthority.PlatformResolveRequest{ClusterID: d.ClusterID, Now: time.Now().UTC(), AdmissionWindow: 60 * time.Second, Operation: "execution.start"}
	var run api.AgentRun
	if err := d.Store.Reader.Get(ctx, key, &run); err != nil {
		return nil, err
	}
	if run.Spec.ExecutionLifecycle == "enduring" {
		var namespace corev1.Namespace
		if err := d.Store.Reader.Get(ctx, types.NamespacedName{Name: key.Namespace}, &namespace); err != nil {
			return nil, err
		}
		incarnation, err := cellnauthority.ScopedParentIncarnation(d.ClusterID, string(namespace.UID), string(run.UID))
		if err != nil {
			return nil, err
		}
		request.ParentIncarnation = incarnation
	}
	return d.Store.Prepare(ctx, key, request)
}

// PrepareTurn captures a fresh turn from its API UID while retaining the
// original final run authority and parent incarnation.
func (d *Dispatcher) PrepareTurn(ctx context.Context, runKey, turnKey types.NamespacedName, original *cellnauthority.FinalizedPreparation, incarnation string) (*cellnauthority.StoredPreparation, error) {
	if d == nil || original == nil || original.Decision.Operation != "execution.start" || original.Decision.Lifecycle != "enduring-initial" || original.Decision.Parent == nil || original.Decision.Parent.Incarnation != incarnation {
		return nil, errors.New("original enduring authority is unavailable")
	}
	root, err := d.Store.Load(ctx, original.PreparationName)
	if err != nil {
		return nil, err
	}
	if root.UID != original.PreparationUID {
		return nil, errors.New("original preparation identity changed")
	}
	decision := original.Decision
	next, err := d.Store.Prepare(ctx, runKey, cellnauthority.PlatformResolveRequest{ClusterID: d.ClusterID, Now: time.Now().UTC(), AdmissionWindow: 60 * time.Second, Operation: "execution.turn", ParentIncarnation: incarnation, TurnKey: &turnKey, Original: &decision})
	if err != nil {
		return nil, err
	}
	before, after := root.Operation.Resolution.Execution, next.Operation.Resolution.Execution
	if before == nil || after == nil || !reflect.DeepEqual(before.Tools, after.Tools) {
		return nil, cellnauthority.Refuse(cellnauthority.ReasonPolicyContracted, "original ordered tool material changed")
	}
	return next, nil
}

func (d *Dispatcher) EnsureFinal(ctx context.Context, prepared *cellnauthority.StoredPreparation) (*cellnauthority.FinalizedPreparation, error) {
	name, err := d.Store.FinalName(prepared)
	if err != nil {
		return nil, err
	}
	if final, err := d.Store.LoadFinal(ctx, name, prepared); err == nil {
		return final, nil
	} else if !apierrors.IsNotFound(err) {
		return nil, err
	}
	base := prepared.Operation.Resolution.Decision
	if err := d.Receiver.PreflightArtifacts(ctx, base); err != nil {
		return nil, err
	}
	secretUID := ""
	if base.Route.Auth == "secret" {
		if d.Gateway == nil || base.Route.CredentialSourceRef == nil {
			return nil, errors.New("secret-auth scoped execution requires the configured model gateway")
		}
		connectionUID := ""
		if base.Route.ModelConnectionUID != nil {
			connectionUID = *base.Route.ModelConnectionUID
		}
		pin, err := d.Gateway.Pin(ctx, modelgateway.PinRequest{Namespace: base.Run.Namespace, ConnectionName: prepared.Operation.Resolution.Execution.ModelConnectionName, ModelConnectionSpecSHA256: base.Route.ModelConnectionSpecSHA256, CredentialSecretName: base.Route.CredentialSourceRef.SecretName, CredentialSecretKey: base.Route.CredentialSourceRef.SecretKey})
		if err != nil {
			return nil, err
		}
		if pin.ModelConnectionUID == "" || pin.ModelConnectionUID != connectionUID || pin.SecretUID == "" {
			return nil, errors.New("model gateway returned a conflicting route pin")
		}
		secretUID = pin.SecretUID
	}
	finalDecision, err := base.FinalizeCredentialSource(secretUID)
	if err != nil {
		return nil, err
	}
	return d.Store.Finalize(ctx, prepared, finalDecision)
}

// EnsureTurnFinal reuses the original gateway-pinned Secret UID. It must not
// ask the gateway to pin a possibly recreated Secret for continuation work.
func (d *Dispatcher) EnsureTurnFinal(ctx context.Context, prepared *cellnauthority.StoredPreparation, original *cellnauthority.FinalizedPreparation) (*cellnauthority.FinalizedPreparation, error) {
	if prepared == nil || original == nil || prepared.Operation.Resolution.Decision.Operation != "execution.turn" {
		return nil, errors.New("turn finalization requires original prepared authority")
	}
	if err := d.Receiver.PreflightArtifacts(ctx, prepared.Operation.Resolution.Decision); err != nil {
		return nil, err
	}
	secretUID := ""
	if original.Decision.Route.CredentialSource != nil {
		secretUID = original.Decision.Route.CredentialSource.SecretUID
	}
	final, err := prepared.Operation.Resolution.Decision.FinalizeCredentialSource(secretUID)
	if err != nil {
		return nil, err
	}
	return d.Store.Finalize(ctx, prepared, final)
}

func CapabilityDecision(decision cellnauthority.PlatformDecision) (cap.Decision, error) {
	raw, err := json.Marshal(decision)
	if err != nil {
		return cap.Decision{}, err
	}
	var out cap.Decision
	if err := cap.StrictDecode(raw, &out); err != nil {
		return cap.Decision{}, err
	}
	return out, nil
}

// RevalidateAdmission checks current policy/catalogue identity without moving
// the frozen clock or granting a replacement allowance. Recovery/cleanup must
// not call this: historical owner authority survives policy withdrawal.
func (d *Dispatcher) RevalidateAdmission(ctx context.Context, prepared *cellnauthority.StoredPreparation) error {
	if d == nil || d.Store.Reader == nil || prepared == nil {
		return errors.New("scoped admission reader is unavailable")
	}
	if err := d.Receiver.PreflightArtifacts(ctx, prepared.Operation.Resolution.Decision); err != nil {
		return err
	}
	frozen := prepared.Operation.Resolution
	frozen.Request = prepared.Operation.ResolveRequest
	return (cellnauthority.PlatformResolver{Reader: d.Store.Reader}).Revalidate(ctx,
		types.NamespacedName{Namespace: frozen.Decision.Run.Namespace, Name: frozen.Decision.Run.Name}, frozen)
}

func (d *Dispatcher) Enroll(ctx context.Context, prepared *cellnauthority.StoredPreparation, final *cellnauthority.FinalizedPreparation) (PrepareResponse, error) {
	decision, err := CapabilityDecision(final.Decision)
	if err != nil {
		return PrepareResponse{}, err
	}
	return d.Receiver.Prepare(ctx, prepared.Operation, decision)
}

func (d *Dispatcher) StartTokens(final *cellnauthority.FinalizedPreparation) (cap.Decision, cap.Token, cap.Token, error) {
	decision, err := CapabilityDecision(final.Decision)
	if err != nil {
		return decision, cap.Token{}, cap.Token{}, err
	}
	execution, err := d.Issuer.Issue(decision, cap.IssueRequest{Audience: cap.AudienceExecution, Operation: decision.Operation})
	if err != nil {
		return decision, cap.Token{}, cap.Token{}, err
	}
	var model cap.Token
	if decision.Route.Provider != "none" {
		model, err = d.Issuer.Issue(decision, cap.IssueRequest{Audience: cap.AudienceModelGateway, Operation: "model.invoke"})
	}
	return decision, execution, model, err
}

func (d *Dispatcher) RegisterGateway(ctx context.Context, final *cellnauthority.FinalizedPreparation, decision cap.Decision, execution cap.Token) error {
	if decision.Route.Provider == "none" {
		return nil
	}
	if d.Gateway == nil {
		return errors.New("model-using scoped execution requires the configured model gateway")
	}
	prepared, err := d.Store.Load(ctx, final.PreparationName)
	if err != nil || prepared.UID != final.PreparationUID {
		return errors.New("final decision lost its protected preparation")
	}
	return d.Gateway.Register(ctx, decision, execution, prepared.Operation.Resolution.Execution.ModelConnectionName)
}

func (d *Dispatcher) Start(ctx context.Context, id, owner string, execution, model cap.Token) (OperationStatus, error) {
	return d.Receiver.Start(ctx, id, owner, execution, model)
}

func ownerDecision(original cap.Decision, operation string, now time.Time) cap.Decision {
	original.Operation = operation
	stamp := now.UTC().Unix()
	original.Windows = cap.Windows{IssuedAt: stamp, NotBefore: stamp, AdmissionDeadline: stamp + 60}
	return original
}

func (d *Dispatcher) Read(ctx context.Context, id string, final *cellnauthority.FinalizedPreparation) (OperationStatus, error) {
	original, err := CapabilityDecision(final.Decision)
	if err != nil {
		return OperationStatus{}, err
	}
	decision := ownerDecision(original, "execution.read", time.Now())
	token, err := d.Issuer.Issue(decision, cap.IssueRequest{Audience: cap.AudienceExecution, Operation: "execution.read"})
	if err != nil {
		return OperationStatus{}, err
	}
	return d.Receiver.Read(ctx, id, decision, token)
}

func (d *Dispatcher) Cleanup(ctx context.Context, id string, final *cellnauthority.FinalizedPreparation, closeGateway bool) (OperationStatus, error) {
	original, err := CapabilityDecision(final.Decision)
	if err != nil {
		return OperationStatus{}, err
	}
	decision := ownerDecision(original, "execution.cleanup", time.Now())
	token, err := d.Issuer.Issue(decision, cap.IssueRequest{Audience: cap.AudienceExecution, Operation: "execution.cleanup"})
	if err != nil {
		return OperationStatus{}, err
	}
	status, err := d.Receiver.Cleanup(ctx, id, decision, token)
	if err != nil || !status.CleanupConfirmed {
		return status, err
	}
	if original.Route.Provider != "none" && closeGateway {
		if d.Gateway == nil {
			return status, errors.New("model gateway cleanup is not configured")
		}
		if err := d.Gateway.Close(ctx, decision, token); err != nil {
			return status, fmt.Errorf("native cleanup confirmed but model gateway close is uncertain: %w", err)
		}
	}
	return status, nil
}
