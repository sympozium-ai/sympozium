// Package modelkey keeps a model credential Secret to one owner: a single
// Agent, or an Ensemble whose member Agents are one team. Runs of that owner
// (and the sub-agents they spawn or delegate to) may use the key; no other
// Agent may, whatever an AgentRun names.
//
// The owner is recorded on the Secret itself, by the controller, the first
// time an Agent that lists the Secret in spec.authRefs uses it. A claim whose
// owner no longer exists is stale and may be taken over by the next Agent that
// grants the Secret.
package modelkey

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// OwnerAnnotation on a Secret names its owner as "Agent/<name>" or
// "Ensemble/<name>" in the Secret's namespace.
const OwnerAnnotation = "sympozium.ai/model-key-owner"

// ensembleLabel marks an Agent the Ensemble controller created for a persona.
const ensembleLabel = "sympozium.ai/ensemble"

// Owner is the identity that owns the keys an Agent uses: its Ensemble when
// it is an Ensemble member, otherwise the Agent itself. Membership needs both
// the label and the Ensemble's controller reference, so labelling an Agent by
// hand does not join it to another team's key.
func Owner(agent *api.Agent) string {
	if ensemble := agent.Labels[ensembleLabel]; ensemble != "" {
		for _, ref := range agent.OwnerReferences {
			if ref.Controller != nil && *ref.Controller && ref.Kind == "Ensemble" && ref.Name == ensemble && strings.HasPrefix(ref.APIVersion, api.GroupVersion.Group+"/") {
				return "Ensemble/" + ensemble
			}
		}
	}
	return "Agent/" + agent.Name
}

// ConflictError reports a key already owned by another live Agent or Ensemble.
type ConflictError struct {
	Secret, Owner string
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("model key Secret %q belongs to %s; every Agent needs its own key (sub-agents share their parent's)", e.Secret, e.Owner)
}

// NotGrantedError reports a key the Agent neither lists in spec.authRefs nor
// owns through its Ensemble.
type NotGrantedError struct {
	Secret, Agent string
}

func (e *NotGrantedError) Error() string {
	return fmt.Sprintf("model key Secret %q is not declared in Agent %q spec.authRefs", e.Secret, e.Agent)
}

// IsRefusal reports whether err is a permanent ownership refusal, as opposed
// to a transient read or write failure worth retrying.
func IsRefusal(err error) bool {
	switch err.(type) {
	case *ConflictError, *NotGrantedError:
		return true
	}
	return false
}

// Grants reports whether the Agent lists secret in spec.authRefs for provider.
// An AuthRef with an empty provider is a provider-agnostic grant, and an empty
// provider argument asks about the Secret alone.
func Grants(agent *api.Agent, provider, secret string) bool {
	for _, ref := range agent.Spec.AuthRefs {
		if ref.Secret == secret && (provider == "" || ref.Provider == "" || strings.EqualFold(ref.Provider, provider)) {
			return true
		}
	}
	return false
}

// Check is the read-only form of Authorize, for admission: it refuses what
// Authorize would refuse without recording a claim. A missing Secret passes;
// the run fails later on the missing credential as before.
func Check(ctx context.Context, c client.Reader, agent *api.Agent, provider, secret string) error {
	_, err := decide(ctx, c, agent, provider, secret)
	return err
}

// Authorize decides whether runs of agent may use secret, and records the
// claim on the Secret when the Agent takes an unowned or stale key it grants.
// An empty secret (an unauthenticated provider) is always allowed.
func Authorize(ctx context.Context, c client.Client, agent *api.Agent, provider, secret string) error {
	claim, err := decide(ctx, c, agent, provider, secret)
	if err != nil || claim == nil {
		return err
	}
	if claim.Annotations == nil {
		claim.Annotations = map[string]string{}
	}
	claim.Annotations[OwnerAnnotation] = Owner(agent)
	// Update, not patch: the resourceVersion makes two concurrent claimants
	// race on one write, and the loser re-reads and sees the winner.
	return c.Update(ctx, claim)
}

// decide returns the Secret to claim (nil when already owned or absent), or a
// refusal.
func decide(ctx context.Context, c client.Reader, agent *api.Agent, provider, secret string) (*corev1.Secret, error) {
	if strings.TrimSpace(secret) == "" {
		return nil, nil
	}
	var s corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: agent.Namespace, Name: secret}, &s); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	owner := Owner(agent)
	current, err := LiveOwner(ctx, c, &s)
	if err != nil {
		return nil, err
	}
	if current == owner {
		return nil, nil
	}
	if current != "" {
		return nil, &ConflictError{Secret: secret, Owner: current}
	}
	if !Grants(agent, provider, secret) {
		return nil, &NotGrantedError{Secret: secret, Agent: agent.Name}
	}
	return &s, nil
}

// LiveOwner is the Secret's recorded owner while that Agent or Ensemble still
// exists, and "" for an unowned key or a stale claim.
func LiveOwner(ctx context.Context, c client.Reader, secret *corev1.Secret) (string, error) {
	current := secret.Annotations[OwnerAnnotation]
	if current == "" {
		return "", nil
	}
	live, err := ownerExists(ctx, c, secret.Namespace, current)
	if err != nil || !live {
		return "", err
	}
	return current, nil
}

// CheckOwner refuses a key that another live owner holds, for an owner that
// does not exist yet (an Agent or Ensemble about to be created).
func CheckOwner(ctx context.Context, c client.Reader, owner string, secret *corev1.Secret) error {
	current, err := LiveOwner(ctx, c, secret)
	if err != nil {
		return err
	}
	if current != "" && current != owner {
		return &ConflictError{Secret: secret.Name, Owner: current}
	}
	return nil
}

// OwnedBy reports whether the Secret's recorded owner is agent's owner. The
// model gateway uses it on every call, after the controller made the claim.
func OwnedBy(secret *corev1.Secret, agent *api.Agent) bool {
	return secret.Annotations[OwnerAnnotation] == Owner(agent)
}

func ownerExists(ctx context.Context, c client.Reader, namespace, owner string) (bool, error) {
	kind, name, ok := strings.Cut(owner, "/")
	var obj client.Object
	switch {
	case ok && kind == "Agent" && name != "":
		obj = &api.Agent{}
	case ok && kind == "Ensemble" && name != "":
		obj = &api.Ensemble{}
	default:
		// An unreadable owner is nobody's: treat it as stale.
		return false, nil
	}
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, obj); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return obj.GetDeletionTimestamp() == nil, nil
}
