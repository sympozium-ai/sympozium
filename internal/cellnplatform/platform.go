// Package cellnplatform is the tenant-facing shape of an installed Celln
// platform: which namespaces a scope's policy admits, and the three ordinary
// workload objects a namespace needs before its first run. The installer and
// the API server build the same objects from the same catalogue, so a
// namespace prepared on demand is indistinguishable from the install namespace.
package cellnplatform

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// ScopeLabel opts a namespace into a scope in strict ("labeled") mode.
	ScopeLabel = "celln.sympozium.ai/scope"
	// ExcludedLabel opts a namespace out of a default-open scope.
	ExcludedLabel = "celln.sympozium.ai/excluded"
	// NamespaceNameLabel is stamped on every namespace by Kubernetes.
	NamespaceNameLabel = "kubernetes.io/metadata.name"
	// ManagedByLabel marks wrappers the platform created on demand.
	ManagedByLabel = "sympozium.ai/managed-by"
	ManagedByValue = "celln-platform"

	// BackendLabel names the model backend a runtime profile serves. A scope
	// publishes one profile per backend; wrappers are named after it.
	BackendLabel = "celln.sympozium.ai/backend"
	// DefaultBackend is the backend of a scope installed with a single model
	// route; its wrappers keep the original names.
	DefaultBackend = "native"
	// ProtocolAnnotation records the wire protocol a runtime profile's model
	// route speaks, so a policy carrying several routes to the same origin
	// and model (one per protocol) binds each backend to its own route.
	ProtocolAnnotation = "celln.sympozium.ai/protocol"

	// The wrapper names of the default backend; a run selects them by name.
	WrapperRuntimeName    = "celln-native"
	WrapperAgentName      = "celln-agent"
	WrapperConnectionName = "celln-native"

	AuthoriseAll     = "all"
	AuthoriseLabeled = "labeled"
)

// SystemNamespaces are never admitted by a default-open policy. Extra names
// (the control-plane namespaces of this installation) are merged in.
func SystemNamespaces(extra ...string) []string {
	names := append([]string{"kube-system", "kube-public", "kube-node-lease", "cert-manager"}, extra...)
	sort.Strings(names)
	return slices.Compact(slices.DeleteFunc(names, func(s string) bool { return s == "" }))
}

// OpenSelector admits every namespace except the exclusions and any namespace
// that opted out with ExcludedLabel.
func OpenSelector(exclusions []string) metav1.LabelSelector {
	return metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{
		{Key: NamespaceNameLabel, Operator: metav1.LabelSelectorOpNotIn, Values: exclusions},
		{Key: ExcludedLabel, Operator: metav1.LabelSelectorOpDoesNotExist},
	}}
}

// StrictSelector admits only namespaces carrying the scope label.
func StrictSelector(scope string) metav1.LabelSelector {
	return metav1.LabelSelector{MatchLabels: map[string]string{ScopeLabel: scope}}
}

// Selector returns the namespace selector for an authorisation mode.
func Selector(mode, scope string, exclusions []string) (metav1.LabelSelector, error) {
	switch mode {
	case "", AuthoriseAll:
		return OpenSelector(exclusions), nil
	case AuthoriseLabeled:
		return StrictSelector(scope), nil
	}
	return metav1.LabelSelector{}, fmt.Errorf("authorisation mode must be %q or %q", AuthoriseAll, AuthoriseLabeled)
}

// Wrappers are the names a namespace's runs select.
type Wrappers struct {
	Backend    string   `json:"backend"`
	Runtime    string   `json:"runtime"`
	Agent      string   `json:"agent"`
	Connection string   `json:"connection"`
	Created    []string `json:"created"`
}

// Backend is the model backend a profile serves (DefaultBackend when unlabeled).
func Backend(profile *api.CellnRuntimeProfile) string {
	if b := profile.Labels[BackendLabel]; b != "" {
		return b
	}
	return DefaultBackend
}

// WrapperNames are the wrapper objects a namespace holds for one backend: the
// default backend keeps the original names, every other backend carries its
// name so several backends coexist in one namespace.
func WrapperNames(backend string) Wrappers {
	if backend == "" || backend == DefaultBackend {
		return Wrappers{Backend: DefaultBackend, Runtime: WrapperRuntimeName, Agent: WrapperAgentName, Connection: WrapperConnectionName}
	}
	return Wrappers{Backend: backend, Runtime: "celln-" + backend, Agent: "celln-agent-" + backend, Connection: "celln-" + backend}
}

// Authorised is one profile a namespace may run and the policy admitting it.
type Authorised struct {
	Profile api.CellnRuntimeProfile
	Policy  api.CellnExecutionPolicy
}

// AuthorisedProfiles lists the runtime profiles a namespace's policies admit,
// evaluated exactly as the resolver evaluates them: live cluster-scoped
// policies whose selector matches the namespace's labels.
func AuthorisedProfiles(ctx context.Context, reader client.Reader, namespace string) ([]Authorised, error) {
	var ns corev1.Namespace
	if err := reader.Get(ctx, types.NamespacedName{Name: namespace}, &ns); err != nil {
		return nil, err
	}
	var policies api.CellnExecutionPolicyList
	if err := reader.List(ctx, &policies); err != nil {
		return nil, err
	}
	set := labels.Set(ns.Labels)
	if set == nil {
		set = labels.Set{}
	}
	if _, ok := set[NamespaceNameLabel]; !ok {
		// Older clusters and fakes may lack the stamped label; the name is authoritative.
		set = labels.Merge(set, labels.Set{NamespaceNameLabel: ns.Name})
	}
	var out []Authorised
	seen := map[string]bool{}
	for i := range policies.Items {
		policy := policies.Items[i]
		if policy.Name == "" || policy.DeletionTimestamp != nil {
			continue
		}
		selector, err := metav1.LabelSelectorAsSelector(&policy.Spec.NamespaceSelector)
		if err != nil || !selector.Matches(set) {
			continue
		}
		for _, ref := range policy.Spec.RuntimeProfiles {
			if seen[ref.Ref.Name] {
				continue
			}
			var profile api.CellnRuntimeProfile
			if err := reader.Get(ctx, types.NamespacedName{Name: ref.Ref.Name}, &profile); err != nil {
				if apierrors.IsNotFound(err) {
					continue
				}
				return nil, err
			}
			if profile.Spec.Revision != ref.Ref.Revision || profile.Spec.Native == nil {
				continue
			}
			seen[ref.Ref.Name] = true
			out = append(out, Authorised{Profile: profile, Policy: policy})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Profile.Name < out[j].Profile.Name })
	return out, nil
}

// RuntimeWrapper is the one object a namespace needs to run an authorised
// profile's reviewed worker: an AgentRuntime naming the profile at its exact
// revision. It carries no model route and no credential, so it serves an Agent
// that owns its backend (its own Secret-backed ModelConnection, executed
// gateway-mediated) exactly as it serves the backend's own wrappers.
func RuntimeWrapper(namespace string, profile *api.CellnRuntimeProfile) *api.AgentRuntime {
	names := WrapperNames(Backend(profile))
	return &api.AgentRuntime{
		ObjectMeta: metav1.ObjectMeta{Name: names.Runtime, Namespace: namespace, Labels: map[string]string{ManagedByLabel: ManagedByValue, BackendLabel: names.Backend}},
		Spec:       api.AgentRuntimeSpec{CellnProfileRef: &api.CellnRuntimeProfileRef{Name: profile.Name, Revision: profile.Spec.Revision}, SupportOwner: "celln-platform"},
	}
}

// EnsureRuntimeWrapper creates the runtime wrapper a namespace lacks for an
// authorised profile and returns its name. It creates neither the backend's
// shared Agent nor its host-profile ModelConnection: an Agent with its own
// ModelConnection needs neither, and its connection is never touched. An
// existing wrapper is kept as it is.
func EnsureRuntimeWrapper(ctx context.Context, c client.Client, namespace, profileName string) (string, error) {
	authorised, err := AuthorisedProfiles(ctx, c, namespace)
	if err != nil {
		return "", err
	}
	index := slices.IndexFunc(authorised, func(a Authorised) bool { return a.Profile.Name == profileName })
	if index < 0 {
		return "", fmt.Errorf("no execution policy admits profile %q in namespace %q", profileName, namespace)
	}
	wrapper := RuntimeWrapper(namespace, &authorised[index].Profile)
	if err := c.Create(ctx, wrapper); err != nil && !apierrors.IsAlreadyExists(err) {
		return "", fmt.Errorf("create AgentRuntime %s: %w", wrapper.Name, err)
	}
	return wrapper.Name, nil
}

// TenantWrappers builds the three objects a namespace needs to run a native
// profile: a runtime wrapper, an agent and the host-profile model connection
// bound to the policy's route for the profile's model.
func TenantWrappers(namespace string, profile *api.CellnRuntimeProfile, policy *api.CellnExecutionPolicy) ([]client.Object, error) {
	native := profile.Spec.Native
	if native == nil || native.CredentialProfile == "" {
		return nil, fmt.Errorf("runtime profile %q carries no native credential profile", profile.Name)
	}
	var harness struct {
		Model         string `json:"model"`
		URL           string `json:"url"`
		AllowInsecure bool   `json:"allow_insecure"`
	}
	if json.Unmarshal(native.Template.Raw, &harness) != nil || harness.Model == "" || harness.URL == "" {
		return nil, fmt.Errorf("runtime profile %q template names no model route", profile.Name)
	}
	origin, err := api.ModelEndpointOriginInsecure(harness.URL, harness.AllowInsecure)
	if err != nil {
		return nil, err
	}
	// The first matching route wins unless the profile names its protocol,
	// in which case the route speaking that protocol does.
	var route *api.CellnExecutionPolicyRoute
	protocol := profile.Annotations[ProtocolAnnotation]
	for i := range policy.Spec.Routes {
		r := &policy.Spec.Routes[i]
		if r.Auth != "host-profile" || (harness.AllowInsecure && !r.AllowInsecure) || !slices.Contains(r.Models, harness.Model) || !slices.Contains(r.EndpointOrigins, origin) {
			continue
		}
		if protocol == "" || r.Protocol == protocol {
			route = r
			break
		}
		if route == nil {
			route = r
		}
	}
	if route == nil {
		return nil, fmt.Errorf("policy %q has no host-profile route for %s at %s", policy.Name, harness.Model, origin)
	}
	names := WrapperNames(Backend(profile))
	meta := func(name string) metav1.ObjectMeta {
		return metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: map[string]string{ManagedByLabel: ManagedByValue, BackendLabel: names.Backend}}
	}
	return []client.Object{
		RuntimeWrapper(namespace, profile),
		&api.Agent{ObjectMeta: meta(names.Agent), Spec: api.AgentSpec{RuntimeRef: names.Runtime}},
		&api.ModelConnection{ObjectMeta: meta(names.Connection), Spec: api.ModelConnectionSpec{Provider: route.Provider, Protocol: route.Protocol, Endpoint: harness.URL, CredentialProfile: native.CredentialProfile, Models: []string{harness.Model}, AllowInsecure: harness.AllowInsecure}},
	}, nil
}

// EnsureWrappers creates the wrappers a namespace lacks for an authorised
// profile. Existing objects are never modified: a namespace that already
// prepared its own wrappers keeps them, whatever they say.
func EnsureWrappers(ctx context.Context, c client.Client, namespace, profileName string) (Wrappers, error) {
	authorised, err := AuthorisedProfiles(ctx, c, namespace)
	if err != nil {
		return Wrappers{}, err
	}
	index := slices.IndexFunc(authorised, func(a Authorised) bool { return a.Profile.Name == profileName })
	if index < 0 {
		return Wrappers{}, fmt.Errorf("no execution policy admits profile %q in namespace %q", profileName, namespace)
	}
	objects, err := TenantWrappers(namespace, &authorised[index].Profile, &authorised[index].Policy)
	if err != nil {
		return Wrappers{}, err
	}
	result := WrapperNames(Backend(&authorised[index].Profile))
	result.Created = []string{}
	for _, object := range objects {
		if err := c.Create(ctx, object); err != nil {
			if apierrors.IsAlreadyExists(err) {
				continue
			}
			return Wrappers{}, fmt.Errorf("create %s %s: %w", object.GetObjectKind().GroupVersionKind().Kind, object.GetName(), err)
		}
		result.Created = append(result.Created, object.GetName())
	}
	return result, nil
}
