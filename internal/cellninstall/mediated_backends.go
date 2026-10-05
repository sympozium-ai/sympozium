package cellninstall

import (
	"context"
	"fmt"
	"sort"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// With mediated model access, a backend's provider key is not the fleet's:
// it belongs to one Agent (the installer's starter Agent) as a Secret in that
// Agent's namespace, and the model gateway adds it to that Agent's requests.
// The nodes still configure the backend, because its starter configuration
// is the runtime every mediated run executes on, but they hold this
// placeholder instead of a key, and the scope's policy offers the backend
// only as an auth "secret" route. Nothing can run on it with a fleet-wide key.

// MediatedBackendCredential is the fleet credential of a mediation-only
// backend. It is a marker, not a key: InstallPlatform reads it back to know
// which backends carry no host-profile route.
const MediatedBackendCredential = "sympozium-mediated-backend-holds-no-key"

// MediatedBackendPlan says, per backend, where its key goes when mediation is
// on.
type MediatedBackendPlan struct {
	// Mediated are the backends whose key goes to a starter Agent's Secret;
	// the fleet gets MediatedBackendCredential for them.
	Mediated map[string]bool
	// FleetKeyed are keyed backends whose key already sits in the fleet
	// Secret from an earlier, unmediated install. It is kept (never removed
	// or rotated by an install) and they stay host-profile backends.
	FleetKeyed []string
	// Unmediable are keyed backends the gateway cannot carry a Secret to
	// (plain HTTP or an explicit port); they keep the fleet path.
	Unmediable []string
}

// MediatedRouteFor is the auth "secret" route that admits a resolved backend's
// own model through the gateway, or ok=false when a Secret may not travel to
// its endpoint (plain HTTP or a port).
func MediatedRouteFor(m FleetModel) (api.CellnExecutionPolicyRoute, bool) {
	if m.AllowInsecure {
		return api.CellnExecutionPolicyRoute{}, false
	}
	origin, err := api.ModelEndpointOriginInsecure(m.Endpoint, false)
	if err != nil {
		return api.CellnExecutionPolicyRoute{}, false
	}
	route, err := MediatedRoute{Provider: m.Provider, Protocol: m.Protocol, Models: []string{m.Name}, EndpointOrigins: []string{origin}}.PolicyRoute()
	return route, err == nil
}

// PlanMediatedBackends decides which of the resolved backends become
// mediation-only. Keyless backends (llama-server) have no key to isolate and
// keep the fleet path, as do the backends PlanMediatedBackends lists as
// FleetKeyed or Unmediable.
func PlanMediatedBackends(ctx context.Context, store client.Reader, backends []FleetBackend) (MediatedBackendPlan, error) {
	plan := MediatedBackendPlan{Mediated: map[string]bool{}}
	published, err := fleetCredentials(ctx, store)
	if err != nil {
		return plan, err
	}
	for _, b := range backends {
		if !b.Model.NeedsCredential() {
			continue
		}
		if present, ok := published[b.Name]; ok && len(present) != 0 && string(present) != MediatedBackendCredential {
			plan.FleetKeyed = append(plan.FleetKeyed, b.Name)
			continue
		}
		if _, ok := MediatedRouteFor(b.Model); !ok {
			plan.Unmediable = append(plan.Unmediable, b.Name)
			continue
		}
		plan.Mediated[b.Name] = true
	}
	return plan, nil
}

// PublishMediatedBackendPlaceholder publishes the marker for a mediation-only
// backend in the fleet credential Secret, so the nodes can configure it
// without a key. An entry holding a real key is never overwritten.
func PublishMediatedBackendPlaceholder(ctx context.Context, store client.Client, b FleetBackend) error {
	if err := PublishFleetBackendCredentialValue(ctx, store, b.Name, b.Model, MediatedBackendCredential); err != nil {
		return fmt.Errorf("backend %s: %w", b.Name, err)
	}
	return nil
}

// PublishBackendCredentials publishes every backend's fleet credential: the
// mediation marker for a mediated backend (whose key the fleet never sees),
// the backend's key or keyless placeholder for the others.
func PublishBackendCredentials(ctx context.Context, store client.Client, backends []FleetBackend, mediated map[string]bool) error {
	for _, b := range backends {
		if mediated[b.Name] {
			if err := PublishMediatedBackendPlaceholder(ctx, store, b); err != nil {
				return err
			}
			continue
		}
		if err := PublishFleetBackendCredential(ctx, store, b); err != nil {
			return fmt.Errorf("backend %s: %w", b.Name, err)
		}
	}
	return nil
}

// MediatedOnlyBackends lists the backends whose fleet credential is the
// mediation marker.
func MediatedOnlyBackends(ctx context.Context, store client.Reader) (map[string]bool, error) {
	published, err := fleetCredentials(ctx, store)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for name, value := range published {
		if string(value) == MediatedBackendCredential {
			out[name] = true
		}
	}
	return out, nil
}

// SortedNames lists a set's names in order, for messages.
func SortedNames(set map[string]bool) []string {
	names := make([]string, 0, len(set))
	for name, on := range set {
		if on {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func fleetCredentials(ctx context.Context, store client.Reader) (map[string][]byte, error) {
	var secret corev1.Secret
	if err := store.Get(ctx, types.NamespacedName{Namespace: fleetNamespace, Name: FleetModelCredentialSecret}, &secret); apierrors.IsNotFound(err) {
		return map[string][]byte{}, nil
	} else if err != nil {
		return nil, err
	}
	return secret.Data, nil
}
