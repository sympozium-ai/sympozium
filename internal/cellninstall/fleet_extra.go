package cellninstall

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnplatform"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Backends added after the install. The chart renders the install-time list
// into the configure DaemonSet; anything added later lives in one ConfigMap
// the API server appends to and the node script merges in. Rolling the
// DaemonSet's pod template makes every node configure the new backend from
// the same admitted package; owners and their conversations are untouched.

const (
	// FleetExtraBackendsConfigMap holds backends.json, the list the node
	// script merges after the chart's own list.
	FleetExtraBackendsConfigMap = "celln-fleet-backends-extra"
	// FleetConfigureDaemonSet is rolled when the extra list changes.
	FleetConfigureDaemonSet = "celln-node-configure"
	// extraRevisionAnnotation is the pod-template annotation that rolls it.
	extraRevisionAnnotation = "celln.sympozium.ai/backends-extra"
	// extraStateAnnotationPrefix records, per added backend, how far the API
	// server got in making it available.
	extraStateAnnotationPrefix = "celln.sympozium.ai/backend-"
)

// ExtraBackend is one added backend in the shape the node script reads,
// the same shape the chart exports for install-time backends.
type ExtraBackend struct {
	Name           string `json:"name"`
	Provider       string `json:"provider"`
	Protocol       string `json:"protocol"`
	Endpoint       string `json:"endpoint"`
	Model          string `json:"model"`
	AllowInsecure  bool   `json:"allowInsecure"`
	CredentialFile string `json:"credentialFile"`
	// Parameters are the backend's model parameters; absent when it has none,
	// so the node builds the same plan as before for such a backend.
	Parameters map[string]any `json:"parameters,omitempty"`
	// MaxOutputTokens is the backend's output cap per model request; absent
	// when it is Celln's default, for the same reason.
	MaxOutputTokens int64 `json:"maxOutputTokens,omitempty"`
}

// ExtraBackendFor renders a resolved backend the way the node script reads it.
func ExtraBackendFor(b FleetBackend) ExtraBackend {
	return ExtraBackend{Name: b.Name, Provider: b.Model.Provider, Protocol: b.Model.Protocol, Endpoint: b.Model.Endpoint, Model: b.Model.Name, AllowInsecure: b.Model.AllowInsecure, CredentialFile: "/etc/celln-native/credentials/" + b.Name, Parameters: b.Model.Parameters, MaxOutputTokens: NormalModelMaxOutputTokens(b.Model.MaxOutputTokens)}
}

// FleetFacts are the scope's identities the configure DaemonSet carries, so
// the API server can act on a fleet it did not install.
type FleetFacts struct {
	Scope, Principal, PackageHash string
	// Backends is the install-time list, by name.
	Backends []string
	// InstallBackends is the install-time list as the chart exported it.
	InstallBackends []ExtraBackend
}

// ReadFleetFacts reads them from the configure DaemonSet's environment.
func ReadFleetFacts(ctx context.Context, store client.Reader) (FleetFacts, error) {
	var ds appsv1.DaemonSet
	if err := store.Get(ctx, types.NamespacedName{Namespace: fleetNamespace, Name: FleetConfigureDaemonSet}, &ds); err != nil {
		return FleetFacts{}, fmt.Errorf("no Celln fleet is installed: %w", err)
	}
	var facts FleetFacts
	for _, c := range ds.Spec.Template.Spec.Containers {
		for _, env := range c.Env {
			switch env.Name {
			case "FLEET_SCOPE":
				facts.Scope = env.Value
			case "FLEET_PRINCIPAL":
				facts.Principal = env.Value
			case "FLEET_PACKAGE_HASH":
				facts.PackageHash = env.Value
			case "FLEET_BACKENDS":
				var list []ExtraBackend
				if err := json.Unmarshal([]byte(env.Value), &list); err != nil {
					return FleetFacts{}, fmt.Errorf("configure DaemonSet carries an unreadable backend list: %w", err)
				}
				facts.InstallBackends = list
				for _, b := range list {
					facts.Backends = append(facts.Backends, b.Name)
				}
			}
		}
	}
	if facts.Scope == "" || facts.Principal == "" || facts.PackageHash == "" {
		return FleetFacts{}, fmt.Errorf("configure DaemonSet does not carry the fleet's scope, principal and package hash")
	}
	return facts, nil
}

// ReadExtraBackends returns the added backends, in the order they were added.
func ReadExtraBackends(ctx context.Context, store client.Reader) ([]ExtraBackend, map[string]string, error) {
	var cm corev1.ConfigMap
	if err := store.Get(ctx, types.NamespacedName{Namespace: fleetNamespace, Name: FleetExtraBackendsConfigMap}, &cm); apierrors.IsNotFound(err) {
		return nil, map[string]string{}, nil
	} else if err != nil {
		return nil, nil, err
	}
	var list []ExtraBackend
	if raw := cm.Data["backends.json"]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &list); err != nil {
			return nil, nil, fmt.Errorf("extra backends list is unreadable: %w", err)
		}
	}
	states := map[string]string{}
	for key, value := range cm.Annotations {
		if name, ok := strings.CutPrefix(key, extraStateAnnotationPrefix); ok {
			states[name] = value
		}
	}
	return list, states, nil
}

// AppendExtraBackend adds one backend to the list (creating the ConfigMap on
// first use) and returns the list's new revision for the rollout. A name
// already present, installed or added, is refused.
func AppendExtraBackend(ctx context.Context, store client.Client, facts FleetFacts, b ExtraBackend) (string, error) {
	if !backendNamePattern.MatchString(b.Name) {
		return "", fmt.Errorf("backend name must be a DNS label of at most 32 characters")
	}
	if err := ValidateModelParameters(b.Parameters); err != nil {
		return "", err
	}
	if err := ValidateModelMaxOutputTokens(b.MaxOutputTokens); err != nil {
		return "", err
	}
	b.MaxOutputTokens = NormalModelMaxOutputTokens(b.MaxOutputTokens)
	for _, name := range facts.Backends {
		if name == b.Name {
			return "", fmt.Errorf("backend %s was configured at install", b.Name)
		}
	}
	list, _, err := ReadExtraBackends(ctx, store)
	if err != nil {
		return "", err
	}
	for _, existing := range list {
		if existing.Name == b.Name {
			return "", fmt.Errorf("backend %s already added", b.Name)
		}
	}
	list = append(list, b)
	raw, err := json.Marshal(list)
	if err != nil {
		return "", err
	}
	revision := fmt.Sprintf("%x", sha256.Sum256(raw))[:16]
	var cm corev1.ConfigMap
	err = store.Get(ctx, types.NamespacedName{Namespace: fleetNamespace, Name: FleetExtraBackendsConfigMap}, &cm)
	if apierrors.IsNotFound(err) {
		cm = corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: FleetExtraBackendsConfigMap, Namespace: fleetNamespace, Labels: fleetLabels(), Annotations: map[string]string{}}}
		cm.Data = map[string]string{"backends.json": string(raw)}
		cm.Annotations[extraStateAnnotationPrefix+b.Name] = "pending: waiting for the nodes to configure it"
		return revision, store.Create(ctx, &cm)
	}
	if err != nil {
		return "", err
	}
	patch := client.MergeFrom(cm.DeepCopy())
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	if cm.Annotations == nil {
		cm.Annotations = map[string]string{}
	}
	cm.Data["backends.json"] = string(raw)
	cm.Annotations[extraStateAnnotationPrefix+b.Name] = "pending: waiting for the nodes to configure it"
	return revision, store.Patch(ctx, &cm, patch)
}

// RecordExtraBackendState notes how far an added backend got, for the API
// and UI to show; it is progress, not authority.
func RecordExtraBackendState(ctx context.Context, store client.Client, name, state string) error {
	var cm corev1.ConfigMap
	if err := store.Get(ctx, types.NamespacedName{Namespace: fleetNamespace, Name: FleetExtraBackendsConfigMap}, &cm); err != nil {
		return err
	}
	patch := client.MergeFrom(cm.DeepCopy())
	if cm.Annotations == nil {
		cm.Annotations = map[string]string{}
	}
	cm.Annotations[extraStateAnnotationPrefix+name] = state
	return store.Patch(ctx, &cm, patch)
}

// RolloutConfigure rolls the configure DaemonSet so every node reads the
// extra list; the owner DaemonSet is not touched.
func RolloutConfigure(ctx context.Context, store client.Client, revision string) error {
	var ds appsv1.DaemonSet
	if err := store.Get(ctx, types.NamespacedName{Namespace: fleetNamespace, Name: FleetConfigureDaemonSet}, &ds); err != nil {
		return err
	}
	patch := client.MergeFrom(ds.DeepCopy())
	if ds.Spec.Template.Annotations == nil {
		ds.Spec.Template.Annotations = map[string]string{}
	}
	ds.Spec.Template.Annotations[extraRevisionAnnotation] = revision
	return store.Patch(ctx, &ds, patch)
}

// PublishFleetBackendCredentialValue publishes a key given as a value (the
// API's case) under the same rules as the file form: never replaced with
// different content, a placeholder for keyless backends.
func PublishFleetBackendCredentialValue(ctx context.Context, store client.Client, name string, model FleetModel, credential string) error {
	credential = strings.TrimSpace(credential)
	if model.NeedsCredential() {
		if len(credential) < 24 || strings.ContainsAny(credential, "\r\n\t ") {
			return fmt.Errorf("a model credential of at least 24 printable characters is required for %s", model.Provider)
		}
	} else if credential == "" {
		credential = fleetModelPlaceholderCredential
	}
	var existing corev1.Secret
	err := store.Get(ctx, types.NamespacedName{Namespace: fleetNamespace, Name: FleetModelCredentialSecret}, &existing)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if apierrors.IsNotFound(err) {
		return store.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: FleetModelCredentialSecret, Namespace: fleetNamespace, Labels: fleetLabels()}, Data: map[string][]byte{name: []byte(credential)}})
	}
	if present := existing.Data[name]; len(present) != 0 {
		if string(present) != credential {
			return fmt.Errorf("model credential for backend %s already exists with different content", name)
		}
		return nil
	}
	patch := client.MergeFrom(existing.DeepCopy())
	if existing.Data == nil {
		existing.Data = map[string][]byte{}
	}
	existing.Data[name] = []byte(credential)
	return store.Patch(ctx, &existing, patch)
}

// InstallNamespaceFor finds the namespace whose wrappers the installer
// created for this scope (the oldest AgentRuntime bound to one of its
// profiles); the API server installs an added backend's wrappers there too.
// Without one, the platform still offers the backend to every authorised
// namespace on first use.
func InstallNamespaceFor(ctx context.Context, store client.Reader, scope string) (string, error) {
	var runtimes api.AgentRuntimeList
	if err := store.List(ctx, &runtimes); err != nil {
		return "", err
	}
	prefix := PlatformProfileName(scope, "")
	var candidates []api.AgentRuntime
	for _, rt := range runtimes.Items {
		if rt.Spec.CellnProfileRef != nil && strings.HasPrefix(rt.Spec.CellnProfileRef.Name, prefix) || (rt.Spec.CellnProfileRef != nil && rt.Spec.CellnProfileRef.Name == strings.TrimSuffix(prefix, "-")) {
			candidates = append(candidates, rt)
		}
	}
	if len(candidates) == 0 {
		return "", nil
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].CreationTimestamp.Before(&candidates[j].CreationTimestamp) })
	return candidates[0].Namespace, nil
}

// AuthoriseModeOf reads back how the scope's policy admits namespaces.
func AuthoriseModeOf(policy *api.CellnExecutionPolicy) string {
	if policy != nil && policy.Spec.NamespaceSelector.MatchLabels[ScopeLabel] != "" {
		return cellnplatform.AuthoriseLabeled
	}
	return cellnplatform.AuthoriseAll
}
