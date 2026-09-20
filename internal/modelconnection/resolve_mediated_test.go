package modelconnection

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// A Secret-backed connection is resolvable only for the gateway-mediated path,
// and the Secret never becomes part of the run's model spec.
func TestResolveMediated(t *testing.T) {
	const secretName = "anthropic-tenant-key"
	spec := func(mutate func(*api.ModelConnectionSpec)) api.ModelConnectionSpec {
		s := api.ModelConnectionSpec{Provider: "anthropic", Protocol: "anthropic-messages", Endpoint: "https://api.anthropic.com/v1/messages", Models: []string{"claude"}}
		mutate(&s)
		return s
	}
	cases := []struct {
		name               string
		spec               api.ModelConnectionSpec
		strict, mediated   bool // whether Resolve / ResolveMediated accept it
		wantProfile        string
		mediatedErrContain string
	}{
		{name: "secretRef", spec: spec(func(s *api.ModelConnectionSpec) { s.SecretRef = secretName; s.MaxOutputTokens = 4096 }), strict: false, mediated: true},
		{name: "host profile is unchanged", spec: spec(func(s *api.ModelConnectionSpec) { s.CredentialProfile = "fleet-key" }), strict: true, mediated: true, wantProfile: "fleet-key"},
		{name: "no credential", spec: spec(func(*api.ModelConnectionSpec) {}), strict: false, mediated: true},
		{name: "both set", spec: spec(func(s *api.ModelConnectionSpec) { s.SecretRef = secretName; s.CredentialProfile = "fleet-key" }), strict: false, mediated: false, mediatedErrContain: "not both"},
		{name: "secret over plain HTTP", spec: spec(func(s *api.ModelConnectionSpec) {
			s.SecretRef = secretName
			s.Endpoint = "http://10.0.0.5:8080/v1/messages"
			s.AllowInsecure = true
		}), strict: false, mediated: false, mediatedErrContain: "HTTPS"},
		{name: "secret on a port without allowInsecure", spec: spec(func(s *api.ModelConnectionSpec) {
			s.SecretRef = secretName
			s.Endpoint = "https://api.anthropic.com:8443/v1/messages"
		}), strict: false, mediated: false},
	}
	scheme := runtime.NewScheme()
	_ = api.AddToScheme(scheme)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			connection := &api.ModelConnection{ObjectMeta: metav1.ObjectMeta{Name: "own", Namespace: "tenant", UID: "uid"}, Spec: tc.spec}
			store := fake.NewClientBuilder().WithScheme(scheme).WithObjects(connection).Build()
			ctx := context.Background()
			intent := api.ModelSpec{ConnectionRef: "own", Model: "claude"}
			if _, err := Resolve(ctx, store, "tenant", intent); (err == nil) != tc.strict {
				t.Fatalf("Resolve accepted=%v, want %v (%v)", err == nil, tc.strict, err)
			}
			resolved, err := ResolveMediated(ctx, store, "tenant", intent)
			if (err == nil) != tc.mediated {
				t.Fatalf("ResolveMediated accepted=%v, want %v (%v)", err == nil, tc.mediated, err)
			}
			if err != nil {
				if tc.mediatedErrContain != "" && !strings.Contains(err.Error(), tc.mediatedErrContain) {
					t.Fatalf("refusal %q does not mention %q", err, tc.mediatedErrContain)
				}
				return
			}
			if resolved.Provider != tc.spec.Provider || resolved.BaseURL != tc.spec.Endpoint || resolved.Protocol != tc.spec.Protocol || resolved.ConnectionRevision != Revision(connection) || resolved.CredentialProfile != tc.wantProfile {
				t.Fatalf("route not frozen from the connection: %+v", resolved)
			}
			// Nothing a pod or guest path injects as environment may name the Secret.
			if resolved.AuthSecretRef != "" || resolved.ProviderHeadersSecretRef != "" || len(resolved.ProviderHeaders) != 0 {
				t.Fatalf("credential reference leaked into the run model: %+v", resolved)
			}
			raw, err := json.Marshal(resolved)
			if err != nil {
				t.Fatal(err)
			}
			if tc.spec.SecretRef != "" && strings.Contains(string(raw), tc.spec.SecretRef) {
				t.Fatalf("Secret name appears in the resolved model spec: %s", raw)
			}
			// A frozen spec resolves to itself; an inline credential is still refused.
			if again, err := ResolveMediated(ctx, store, "tenant", resolved); err != nil || again.ConnectionRevision != resolved.ConnectionRevision {
				t.Fatalf("frozen mediated spec did not re-resolve: %+v %v", again, err)
			}
			withKey := resolved
			withKey.AuthSecretRef = secretName
			if _, err := ResolveMediated(ctx, store, "tenant", withKey); err == nil {
				t.Fatal("mediated resolution accepted an inline credential Secret")
			}
		})
	}
}
