package cellninstall

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Model parameters are a bounded JSON object the Celln host merges into every
// provider request of one backend; the guest never sees or changes them. The
// rules below are Celln's own (it validates the plan again on every node):
// refusing here reports a bad object before anything reaches the cluster.

const (
	// MaxModelParametersBytes bounds the serialized object.
	MaxModelParametersBytes = api.MaxModelParametersBytes
	// ModelParametersMinCelln is the newest Celln release whose plans refuse
	// model parameters; nodes need a newer one.
	ModelParametersMinCelln = "v0.5.22"
	// ModelParametersCellnHint names the usual reason a backend with
	// parameters never gets configured.
	ModelParametersCellnHint = "this backend sets model parameters, which need a Celln release newer than " + ModelParametersMinCelln + " on the nodes (an older one refuses the plan: look for \"starter-configure\" in the celln-node-configure logs)"
)

// ReservedModelParameters are the request fields Celln owns; a parameter may
// not set them.
var ReservedModelParameters = api.ReservedModelParameters

// ValidateModelParameters applies Celln's rules for modelConnection.parameters.
// A nil or empty object is valid and means no parameters. The rules live in
// api/v1alpha1, shared with the namespaced ModelConnection.
func ValidateModelParameters(parameters map[string]any) error {
	return api.ValidateModelParameters(parameters)
}

// ParseModelParameters decodes and validates one JSON object. Numbers keep
// their written form. Empty input and {} mean no parameters (nil).
func ParseModelParameters(raw []byte) (map[string]any, error) {
	return api.ParseModelParameters(raw)
}

// ReadModelParametersFile reads the parameters-file of a backend.
func ReadModelParametersFile(path string) (map[string]any, error) {
	info, err := os.Stat(path)
	if !filepath.IsAbs(path) || err != nil || !info.Mode().IsRegular() || info.Size() > 4*MaxModelParametersBytes {
		return nil, fmt.Errorf("model parameters file %q must be a regular absolute file of at most %d bytes", path, 4*MaxModelParametersBytes)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	parameters, err := ParseModelParameters(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return parameters, nil
}

// ModelParametersJSON is the compact form carried through chart values, the
// node's backend list and messages; empty for no parameters.
func ModelParametersJSON(parameters map[string]any) string {
	if len(parameters) == 0 {
		return ""
	}
	raw, err := json.Marshal(parameters)
	if err != nil {
		return ""
	}
	return string(raw)
}

// SameModelParameters compares two objects by JSON value, whatever Go number
// types their decoders chose.
func SameModelParameters(a, b map[string]any) bool {
	if len(a) == 0 || len(b) == 0 {
		return len(a) == 0 && len(b) == 0
	}
	normal := func(parameters map[string]any) any {
		var out any
		raw, err := json.Marshal(parameters)
		if err != nil || json.Unmarshal(raw, &out) != nil {
			return nil
		}
		return out
	}
	left, right := normal(a), normal(b)
	return left != nil && reflect.DeepEqual(left, right)
}

// PublishedModelParameters reads what the fleet published for every backend:
// model.parameters of its configured.json (nil when it has none).
func PublishedModelParameters(data map[string]string) (map[string]map[string]any, error) {
	settings, err := PublishedModelSettings(data)
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]any{}
	for backend, s := range settings {
		out[backend] = s.Parameters
	}
	return out, nil
}

// PublishedModelSetting is what a published backend was configured with and
// keeps for good: model.parameters and model.maxOutputTokens of its
// configured.json (nil and 0 when it has the defaults).
type PublishedModelSetting struct {
	Parameters      map[string]any
	MaxOutputTokens int64
}

// PublishedModelSettings reads them for every published backend.
func PublishedModelSettings(data map[string]string) (map[string]PublishedModelSetting, error) {
	backends, err := PublishedBackends(data)
	if err != nil {
		return nil, err
	}
	out := map[string]PublishedModelSetting{}
	for _, backend := range backends {
		var configured struct {
			Model struct {
				Parameters      map[string]any `json:"parameters"`
				MaxOutputTokens int64          `json:"maxOutputTokens"`
			} `json:"model"`
		}
		decoder := json.NewDecoder(strings.NewReader(data[publishedKey(backend, "configured.json", data)]))
		decoder.UseNumber()
		if err := decoder.Decode(&configured); err != nil {
			return nil, fmt.Errorf("published configuration of backend %s is unreadable: %w", backend, err)
		}
		out[backend] = PublishedModelSetting{Parameters: configured.Model.Parameters, MaxOutputTokens: NormalModelMaxOutputTokens(configured.Model.MaxOutputTokens)}
	}
	return out, nil
}

// CheckPublishedModelParameters refuses an install whose backend parameters
// or output cap per request differ from what the fleet already published for that backend in the same
// scope and package. A node never reconfigures a configured backend and a
// published backend's files are never rewritten, so the change would be
// silently ignored; the refusal says how to get the parameters instead.
func CheckPublishedModelParameters(ctx context.Context, store client.Reader, o FleetOptions) error {
	backends, err := o.ResolvedBackends()
	if err != nil {
		return err
	}
	var published corev1.ConfigMap
	if err := store.Get(ctx, types.NamespacedName{Namespace: fleetNamespace, Name: FleetConfigurationConfigMap}, &published); apierrors.IsNotFound(err) {
		return nil
	} else if err != nil {
		return err
	}
	// A scope moving to another package or scope configures every backend afresh.
	if published.Annotations[packageAnnotation] != o.PackageHash {
		return nil
	}
	if scope := published.Annotations[FleetScopeAnnotation]; scope != "" && scope != o.Scope {
		return nil
	}
	have, err := PublishedModelSettings(published.Data)
	if err != nil {
		return err
	}
	for _, b := range backends {
		current, ok := have[b.Name]
		if !ok {
			continue
		}
		if !SameModelParameters(current.Parameters, b.Model.Parameters) {
			return ModelParametersChangeRefusal(b, current.Parameters)
		}
		if current.MaxOutputTokens != NormalModelMaxOutputTokens(b.Model.MaxOutputTokens) {
			return ModelMaxOutputTokensChangeRefusal(b, current.MaxOutputTokens)
		}
	}
	return nil
}

// ModelParametersChangeRefusal explains why a published backend keeps its
// parameters and the two ways forward.
func ModelParametersChangeRefusal(b FleetBackend, published map[string]any) error {
	show := func(parameters map[string]any) string {
		if raw := ModelParametersJSON(parameters); raw != "" {
			return raw
		}
		return "none"
	}
	return publishedBackendChangeRefusal(b, "model parameters", show(published), show(b.Model.Parameters), true)
}

// ModelMaxOutputTokensChangeRefusal is the same refusal for a backend's output
// cap per request.
func ModelMaxOutputTokensChangeRefusal(b FleetBackend, published int64) error {
	show := func(tokens int64) string {
		if tokens = NormalModelMaxOutputTokens(tokens); tokens != 0 {
			return fmt.Sprintf("max output tokens %d", tokens)
		}
		return fmt.Sprintf("the default max output tokens (%d)", DefaultModelMaxOutputTokens)
	}
	return publishedBackendChangeRefusal(b, "max output tokens per request", show(published), show(b.Model.MaxOutputTokens), len(b.Model.Parameters) != 0)
}

// publishedBackendChangeRefusal names the three ways to get a setting a
// published backend cannot take: the same backend under another name through
// the installer or the API, or a new scope.
func publishedBackendChangeRefusal(b FleetBackend, what, was, asks string, parametersFile bool) error {
	spec := fmt.Sprintf("name=%s-2,provider=%s,model=%s,endpoint=%s,protocol=%s", b.Name, b.Model.Provider, b.Model.Name, b.Model.Endpoint, b.Model.Protocol)
	if b.Model.AllowInsecure {
		spec += ",allow-insecure=true"
	}
	if b.Model.NeedsCredential() {
		spec += ",credential-file=/abs/path/key"
	}
	request := map[string]any{"name": b.Name + "-2", "provider": b.Model.Provider, "model": b.Model.Name, "endpoint": b.Model.Endpoint, "protocol": b.Model.Protocol}
	if parametersFile {
		spec += ",parameters-file=/abs/path/parameters.json"
		request["parameters"] = b.Model.Parameters
	}
	if tokens := NormalModelMaxOutputTokens(b.Model.MaxOutputTokens); tokens != 0 {
		spec += fmt.Sprintf(",max-output-tokens=%d", tokens)
		request["maxOutputTokens"] = tokens
	}
	if b.Model.AllowInsecure {
		request["allowInsecure"] = true
	}
	if b.Model.NeedsCredential() {
		request["credential"] = "<key>"
	}
	body, _ := json.Marshal(request)
	return fmt.Errorf("%s of a published backend cannot change: backend %s was published with %s and this install asks for %s.\n"+
		"  The nodes configured it once and its published configuration is never rewritten. Either\n"+
		"  - keep %s as published and add a backend under another name, then move agents to it:\n"+
		"      --celln-fleet-backend %s\n"+
		"    or POST /api/v1/celln-platform/backends %s\n"+
		"  - or move the fleet to a new scope with --celln-fleet-scope and --celln-fleet-replace-package, which configures every backend afresh and ends every live parent on the fleet",
		what, b.Name, was, asks, b.Name, spec, body)
}

// strvalsEscape makes any text one literal Helm strvals value: a backslash
// before every character strvals gives meaning to (commas, equals signs,
// braces, brackets, dots, backslashes) keeps it as written.
func strvalsEscape(value string) string {
	var out strings.Builder
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r > 127) {
			out.WriteByte('\\')
		}
		out.WriteRune(r)
	}
	return out.String()
}
