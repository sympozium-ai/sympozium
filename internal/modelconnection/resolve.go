// Package modelconnection resolves native model intent without reading credentials
// or granting host authority. A resolved run pins the connection UID and spec.
package modelconnection

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func Resolve(ctx context.Context, reader client.Reader, namespace string, model api.ModelSpec) (api.ModelSpec, error) {
	if model.ConnectionRef == "" {
		return model, nil
	}
	var connection api.ModelConnection
	if err := reader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: model.ConnectionRef}, &connection); err != nil {
		return model, fmt.Errorf("model connection %q unavailable: %w", model.ConnectionRef, err)
	}
	if err := connection.Spec.Validate(); err != nil {
		return model, err
	}
	if connection.Spec.Disabled || connection.DeletionTimestamp != nil {
		return model, fmt.Errorf("model connection is disabled or being deleted")
	}
	if !slices.Contains(connection.Spec.Models, model.Model) {
		return model, fmt.Errorf("model %q is not listed in connection %q", model.Model, model.ConnectionRef)
	}
	raw, _ := json.Marshal(struct {
		UID  types.UID
		Spec api.ModelConnectionSpec
	}{connection.UID, connection.Spec})
	revision := fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	if model.ConnectionRevision != "" && model.ConnectionRevision != revision {
		return model, fmt.Errorf("model connection changed; create a new run")
	}
	if model.AuthSecretRef != "" || model.ModelRef != "" || len(model.ProviderHeaders) != 0 || model.ProviderHeadersSecretRef != "" || len(model.NodeSelector) != 0 || (model.Thinking != "" && model.Thinking != "off") {
		return model, fmt.Errorf("native model connections cannot be combined with inline credentials, headers, placement or thinking settings")
	}
	s := connection.Spec
	if s.CredentialProfile == "" || s.SecretRef != "" {
		return model, fmt.Errorf("native connections require a host credential profile")
	}
	if (model.Provider != "" && model.Provider != s.Provider) || (model.BaseURL != "" && model.BaseURL != s.Endpoint) || (model.Protocol != "" && model.Protocol != s.Protocol) || (model.CredentialProfile != "" && model.CredentialProfile != s.CredentialProfile) {
		return model, fmt.Errorf("inline model settings differ from the selected connection")
	}
	if model.AllowInsecure && !s.AllowInsecure {
		return model, fmt.Errorf("inline insecure setting is not authorized by the connection")
	}
	model.Provider, model.BaseURL, model.Protocol, model.CredentialProfile = s.Provider, s.Endpoint, s.Protocol, s.CredentialProfile
	model.AllowInsecure = s.AllowInsecure
	model.ConnectionRevision = revision
	return model, nil
}

// Route returns the host-visible identity. The connection revision is separately
// pinned in the run/selection and must not alter the independent host template.
func Route(model api.ModelSpec) api.ModelSpec {
	model.ConnectionRef, model.ConnectionRevision = "", ""
	return model
}

// ResolveHarness reads a connection for a Kubernetes persistent harness. The
// Secret is referenced only; no credential bytes are fetched by this resolver.
func ResolveHarness(ctx context.Context, reader client.Reader, namespace, name, model string) (*api.AgentRuntimeModel, string, error) {
	var connection api.ModelConnection
	if err := reader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &connection); err != nil {
		return nil, "", err
	}
	if err := connection.Spec.Validate(); err != nil {
		return nil, "", err
	}
	s := connection.Spec
	if s.Disabled || connection.DeletionTimestamp != nil {
		return nil, "", fmt.Errorf("model connection is disabled or being deleted")
	}
	if s.CredentialProfile != "" {
		return nil, "", fmt.Errorf("Kubernetes harnesses require a Kubernetes Secret reference or an unauthenticated connection")
	}
	if !slices.Contains(s.Models, model) {
		return nil, "", fmt.Errorf("model %q is not listed in connection %q", model, name)
	}
	if s.Protocol != "openai-chat" {
		return nil, "", fmt.Errorf("persistent Kubernetes harness connections currently require openai-chat; use an OpenAI compatible gateway for this provider")
	}
	// The maintained Hermes adapter consumes OpenAI-compatible endpoints.
	// Direct Anthropic routes require a harness that supports that protocol.
	base := strings.TrimSuffix(strings.TrimSuffix(strings.TrimRight(s.Endpoint, "/"), "/chat/completions"), "/messages")
	raw, _ := json.Marshal(struct {
		UID   types.UID
		Spec  api.ModelConnectionSpec
		Model string
	}{connection.UID, s, model})
	revision := fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	return &api.AgentRuntimeModel{Provider: s.Provider, Model: model, BaseURL: base, AuthSecretRef: s.SecretRef}, revision, nil
}
