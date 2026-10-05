package cellninstall

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnplatform"
	"github.com/sympozium-ai/sympozium/internal/modelkey"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// With mediated model access the installer's provider key becomes one
// Agent's own key: a Secret in the starter namespace, a ModelConnection that
// names it, and a starter Agent that grants it (spec.authRefs) and runs on
// the backend's runtime through the model gateway. No other Agent may use
// it (internal/modelkey), and no Celln node ever holds it.

// StarterAgentName is the default backend's starter Agent; another backend's
// is StarterAgentName-<backend>.
const StarterAgentName = "starter"

// StarterAgentLabel marks the objects the installer created for a starter
// Agent, with the Agent's name as its value.
const StarterAgentLabel = "sympozium.ai/starter-agent"

// ErrStarterKeyTaken marks EnsureStarterAgent's refusal to touch a starter
// Secret that is another Agent's key or already holds a different key.
var ErrStarterKeyTaken = errors.New("starter Agent's Secret is taken")

// StarterAgentNames are the three objects of one backend's starter Agent.
type StarterAgentNames struct {
	Agent, Secret, Connection string
}

// StarterAgentNamesFor names a backend's starter objects.
func StarterAgentNamesFor(backend string) StarterAgentNames {
	name := StarterAgentName
	if backend != "" && backend != cellnplatform.DefaultBackend {
		name += "-" + backend
	}
	return StarterAgentNames{Agent: name, Secret: name + "-model-key", Connection: name}
}

// ProviderKeyName is the Secret key the model gateway reads a connection's
// provider key from: fixed by the connection's protocol, whatever the
// provider is called.
func ProviderKeyName(protocol string) string {
	if protocol == "anthropic-messages" {
		return "ANTHROPIC_API_KEY"
	}
	return "OPENAI_API_KEY"
}

// StarterAgentOptions describe one starter Agent.
type StarterAgentOptions struct {
	Namespace string
	// Backend is the resolved backend whose route the Agent uses.
	Backend FleetBackend
	// Credential is the provider key, held in memory only until it is
	// written to the Agent's Secret.
	Credential string
	// Runtime is the runtime wrapper (AgentRuntime) in Namespace binding the
	// backend's runtime profile: its toolbox profile when it lends Tools.
	Runtime string
	// Tools are what an Agent with its own key lends on that runtime
	// (cellnplatform.OwnKeySelection): the backend's toolbox tools, exactly
	// and in closure order, which the mediated path serves (workspace
	// operations, public-only web tools and borrowed commands); none on the
	// backend's tool-free runtime.
	Tools []api.ClusterCellnToolRef
}

// StarterAgentObjects builds the Secret, ModelConnection and Agent.
func StarterAgentObjects(o StarterAgentOptions) (*corev1.Secret, *api.ModelConnection, *api.Agent, error) {
	if len(validation.IsDNS1123Label(o.Namespace)) != 0 || o.Runtime == "" {
		return nil, nil, nil, fmt.Errorf("starter Agent: a namespace and a runtime wrapper are required")
	}
	credential := strings.TrimSpace(o.Credential)
	if credential == "" || len(credential) > 4096 || strings.ContainsAny(credential, " \t\r\n") || credential == MediatedBackendCredential {
		return nil, nil, nil, fmt.Errorf("starter Agent: a provider key of one printable line is required")
	}
	m := o.Backend.Model
	names := StarterAgentNamesFor(o.Backend.Name)
	labels := map[string]string{"app.kubernetes.io/part-of": "sympozium", StarterAgentLabel: names.Agent}
	meta := func(name string) metav1.ObjectMeta {
		return metav1.ObjectMeta{Name: name, Namespace: o.Namespace, Labels: labels}
	}
	agent := &api.Agent{ObjectMeta: meta(names.Agent), Spec: api.AgentSpec{
		Agents:     api.AgentsSpec{Default: api.AgentConfig{Model: m.Name}},
		RuntimeRef: o.Runtime,
		AuthRefs:   []api.SecretRef{{Provider: m.Provider, Secret: names.Secret}},
		Execution: &api.AgentExecutionDefaults{
			Backend:            "celln",
			ModelConnectionRef: names.Connection,
			Model:              m.Name,
			// The starter toolbox on the backend's toolbox runtime: the
			// mediated path serves it through the node's broker.
			CellnSelection: &api.CellnCatalogueSelection{RuntimeRef: o.Runtime, ToolRefs: []api.CellnCatalogueToolRef{}, ClusterToolRefs: slices.Clone(o.Tools)},
		},
	}}
	secretMeta := meta(names.Secret)
	// The key is this Agent's from the start; the controller would record
	// the same claim on the Agent's first run.
	secretMeta.Annotations = map[string]string{modelkey.OwnerAnnotation: modelkey.Owner(agent)}
	secret := &corev1.Secret{ObjectMeta: secretMeta, Type: corev1.SecretTypeOpaque, Data: map[string][]byte{ProviderKeyName(m.Protocol): []byte(credential)}}
	connection := &api.ModelConnection{ObjectMeta: meta(names.Connection), Spec: api.ModelConnectionSpec{
		Provider: m.Provider, Protocol: m.Protocol, Endpoint: m.Endpoint, SecretRef: names.Secret, Models: []string{m.Name},
		MaxOutputTokens: NormalModelMaxOutputTokens(m.MaxOutputTokens),
	}}
	if parameters := ModelParametersJSON(m.Parameters); parameters != "" {
		connection.Spec.Parameters = &apiextensionsv1.JSON{Raw: []byte(parameters)}
	}
	if err := connection.Spec.Validate(); err != nil {
		return nil, nil, nil, fmt.Errorf("starter Agent connection: %w", err)
	}
	if _, ok := MediatedRouteFor(m); !ok {
		return nil, nil, nil, fmt.Errorf("starter Agent: backend %s's endpoint %s cannot carry a Secret (plain HTTP or a port)", o.Backend.Name, m.Endpoint)
	}
	return secret, connection, agent, nil
}

// EnsureStarterAgent creates the starter Agent's objects that do not exist
// and returns the names it created. It never modifies an existing object: a
// Secret already holding another key, or owned by another Agent, is refused
// rather than overwritten, because it is that Agent's key now.
func EnsureStarterAgent(ctx context.Context, store client.Client, o StarterAgentOptions) ([]string, error) {
	secret, connection, agent, err := StarterAgentObjects(o)
	if err != nil {
		return nil, err
	}
	var existing corev1.Secret
	switch err := store.Get(ctx, types.NamespacedName{Namespace: secret.Namespace, Name: secret.Name}, &existing); {
	case apierrors.IsNotFound(err):
	case err != nil:
		return nil, err
	default:
		key := ProviderKeyName(o.Backend.Model.Protocol)
		if owner, err := modelkey.LiveOwner(ctx, store, &existing); err != nil {
			return nil, err
		} else if owner != "" && owner != modelkey.Owner(agent) {
			return nil, fmt.Errorf("%w: Secret %s/%s belongs to %s; the installer never takes another Agent's key: choose another starter namespace (--celln-starter-namespace) or remove it", ErrStarterKeyTaken, secret.Namespace, secret.Name, owner)
		}
		if string(existing.Data[key]) != string(secret.Data[key]) {
			return nil, fmt.Errorf("%w: Secret %s/%s already holds a different %s; the installer never replaces an Agent's key: update the Secret yourself, or delete it and install again", ErrStarterKeyTaken, secret.Namespace, secret.Name, key)
		}
	}
	var created []string
	for _, object := range []client.Object{secret, connection, agent} {
		if err := store.Create(ctx, object); err != nil {
			if apierrors.IsAlreadyExists(err) {
				continue
			}
			return created, fmt.Errorf("create %s/%s: %w", object.GetNamespace(), object.GetName(), err)
		}
		created = append(created, object.GetName())
	}
	return created, nil
}

// StarterAgentCredential reads back a starter Agent's key, for the
// preflight probe of a rerun that was given no key: "" when there is none.
func StarterAgentCredential(ctx context.Context, store client.Reader, namespace string, b FleetBackend) (string, error) {
	var secret corev1.Secret
	names := StarterAgentNamesFor(b.Name)
	if err := store.Get(ctx, types.NamespacedName{Namespace: namespace, Name: names.Secret}, &secret); apierrors.IsNotFound(err) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(secret.Data[ProviderKeyName(b.Model.Protocol)])), nil
}

// StarterAgentsFor lists the starter Agents of one backend, in whichever
// namespaces the installer or the API server created them.
func StarterAgentsFor(ctx context.Context, store client.Reader, backend string) ([]api.Agent, error) {
	var agents api.AgentList
	if err := store.List(ctx, &agents, client.MatchingLabels{StarterAgentLabel: StarterAgentNamesFor(backend).Agent}); err != nil {
		return nil, err
	}
	return agents.Items, nil
}

// EnsureStarterRuntimeWrappers binds every starter Agent of a mediation-only
// backend to the backend's runtime profile, once the scope's policy admits
// it: the runtime wrapper its Agent names, in the Agent's namespace. The API
// server creates an added backend's starter Agent before the nodes have
// configured the backend, so the wrapper follows here. It returns the
// namespaces it bound.
func EnsureStarterRuntimeWrappers(ctx context.Context, store client.Client, scope, backend string) ([]string, error) {
	agents, err := StarterAgentsFor(ctx, store, backend)
	if err != nil {
		return nil, err
	}
	var bound []string
	for _, agent := range agents {
		if _, err := cellnplatform.EnsureRuntimeWrapper(ctx, store, agent.Namespace, PlatformProfileName(scope, backend)); err != nil {
			return bound, fmt.Errorf("starter Agent %s/%s: %w", agent.Namespace, agent.Name, err)
		}
		bound = append(bound, agent.Namespace)
	}
	return bound, nil
}
