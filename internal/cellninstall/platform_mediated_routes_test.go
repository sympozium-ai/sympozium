package cellninstall

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/zeebo/blake3"
	"k8s.io/apimachinery/pkg/types"
)

func TestMediatedRoutePolicyRoute(t *testing.T) {
	valid := MediatedRoute{Provider: "anthropic", Protocol: "anthropic-messages", Models: []string{"claude-b", "claude-a"}, EndpointOrigins: []string{"https://api.anthropic.com"}}
	for _, tc := range []struct {
		name   string
		mutate func(*MediatedRoute)
		refuse string
	}{
		{name: "valid"},
		{name: "plain HTTP origin", mutate: func(r *MediatedRoute) { r.EndpointOrigins = []string{"http://api.anthropic.com"} }, refuse: "plain HTTP"},
		{name: "loopback HTTP origin", mutate: func(r *MediatedRoute) { r.EndpointOrigins = []string{"http://127.0.0.1"} }, refuse: "plain HTTP"},
		{name: "origin with a port", mutate: func(r *MediatedRoute) { r.EndpointOrigins = []string{"https://api.anthropic.com:8443"} }, refuse: "origin"},
		{name: "origin with a path", mutate: func(r *MediatedRoute) { r.EndpointOrigins = []string{"https://api.anthropic.com/v1"} }, refuse: "origin"},
		{name: "origin with a trailing slash", mutate: func(r *MediatedRoute) { r.EndpointOrigins = []string{"https://api.anthropic.com/"} }, refuse: "origin"},
		{name: "origin with credentials", mutate: func(r *MediatedRoute) { r.EndpointOrigins = []string{"https://key@api.anthropic.com"} }, refuse: "origin"},
		{name: "wildcard origin", mutate: func(r *MediatedRoute) { r.EndpointOrigins = []string{"*"} }, refuse: "origin"},
		{name: "duplicate origin", mutate: func(r *MediatedRoute) {
			r.EndpointOrigins = []string{"https://api.anthropic.com", "https://api.anthropic.com"}
		}, refuse: "unique"},
		{name: "no origin", mutate: func(r *MediatedRoute) { r.EndpointOrigins = nil }, refuse: "required"},
		{name: "no model", mutate: func(r *MediatedRoute) { r.Models = nil }, refuse: "required"},
		{name: "wildcard model", mutate: func(r *MediatedRoute) { r.Models = []string{"*"} }, refuse: "no wildcard"},
		{name: "prefix wildcard model", mutate: func(r *MediatedRoute) { r.Models = []string{"claude-*"} }, refuse: "no wildcard"},
		{name: "duplicate model", mutate: func(r *MediatedRoute) { r.Models = []string{"claude-a", "claude-a"} }, refuse: "unique"},
		{name: "unknown protocol", mutate: func(r *MediatedRoute) { r.Protocol = "gemini" }, refuse: "protocol"},
		{name: "empty provider", mutate: func(r *MediatedRoute) { r.Provider = "" }, refuse: "provider"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			route := valid
			route.Models, route.EndpointOrigins = append([]string(nil), valid.Models...), append([]string(nil), valid.EndpointOrigins...)
			if tc.mutate != nil {
				tc.mutate(&route)
			}
			got, err := route.PolicyRoute()
			if tc.refuse != "" {
				if err == nil || !strings.Contains(err.Error(), tc.refuse) {
					t.Fatalf("refusal = %v, want one mentioning %q", err, tc.refuse)
				}
				return
			}
			want := api.CellnExecutionPolicyRoute{Provider: "anthropic", Protocol: "anthropic-messages", Models: []string{"claude-a", "claude-b"}, EndpointOrigins: []string{"https://api.anthropic.com"}, Auth: "secret"}
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("route = %+v %v", got, err)
			}
			if !reflect.DeepEqual(valid.Models, []string{"claude-b", "claude-a"}) {
				t.Fatal("the operator's route was reordered in place")
			}
		})
	}
}

func TestKeylessMediatedRouteRequiresExplicitInsecureApproval(t *testing.T) {
	route := MediatedRoute{Provider: "llama-server", Protocol: "openai-chat", Models: []string{"local"}, EndpointOrigins: []string{"http://framework:8080"}, Auth: "none"}
	if _, err := route.PolicyRoute(); err == nil {
		t.Fatal("HTTP accepted without explicit approval")
	}
	route.AllowInsecure = true
	got, err := route.PolicyRoute()
	if err != nil || got.Auth != "none" || !got.AllowInsecure || got.EndpointOrigins[0] != "http://framework:8080" {
		t.Fatalf("keyless route: %+v %v", got, err)
	}
	for _, auth := range []string{"secret", "host-profile", "unknown"} {
		route.Auth = auth
		if _, err := route.PolicyRoute(); err == nil {
			t.Fatalf("insecure route accepted with auth %s", auth)
		}
	}
}

func TestInstallPlatformMediatedRoutes(t *testing.T) {
	ctx := context.Background()
	dir, packageHash, principal := starterConfiguration(t)
	// A LAN backend on plain HTTP: a host-profile route, never a Secret route.
	writeBackendConfigurationInsecure(t, dir, testBackend{"lan", "llama", "openai-chat", "lan.gguf", "http://10.0.0.7:8080/v1/chat/completions", "trial-lan"})
	store := platformInstallStore(t)
	o := PlatformOptions{Namespace: "tenant-a", ConfigurationDir: dir, OutputDir: filepath.Join(t.TempDir(), "out"), Scope: "trial", ClusterID: "cluster-uid", PackageHash: packageHash, Principal: principal, ControllerNamespace: "sympozium-system"}
	_, policyName, _ := PlatformCatalogueNames("trial")
	policy := func() api.CellnExecutionPolicy {
		t.Helper()
		var p api.CellnExecutionPolicy
		if err := store.Get(ctx, types.NamespacedName{Name: policyName}, &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	secretRoutes := func(p api.CellnExecutionPolicy) (out []api.CellnExecutionPolicyRoute) {
		for _, r := range p.Spec.Routes {
			if r.Auth == "secret" {
				out = append(out, r)
			}
		}
		return out
	}

	// A refused declaration changes nothing in the cluster.
	bad := o
	bad.MediatedRoutes = []MediatedRoute{{Provider: "openai", Protocol: "openai-chat", Models: []string{"gpt"}, EndpointOrigins: []string{"http://api.openai.com"}}}
	if err := InstallPlatform(ctx, store, bad); err == nil || !strings.Contains(err.Error(), "plain HTTP") {
		t.Fatalf("HTTP mediated origin accepted: %v", err)
	}
	var none api.CellnExecutionPolicyList
	if err := store.List(ctx, &none); err != nil || len(none.Items) != 0 {
		t.Fatalf("refused installation wrote a policy: %v", err)
	}

	// Not requested: exactly the backends' host-profile routes, as always.
	if err := InstallPlatform(ctx, store, o); err != nil {
		t.Fatal(err)
	}
	before := policy()
	if len(before.Spec.Routes) != 3 || len(secretRoutes(before)) != 0 {
		t.Fatalf("mediated routes emitted without being requested: %+v", before.Spec.Routes)
	}

	// Requested on a running scope: the policy grows and nothing is rewritten.
	o.MediateBackends = true
	o.MediatedRoutes = []MediatedRoute{{Provider: "openai", Protocol: "openai-chat", Models: []string{"gpt-b", "gpt-a"}, EndpointOrigins: []string{"https://api.openai.com"}}}
	if err := InstallPlatform(ctx, store, o); err != nil {
		t.Fatal(err)
	}
	grown := policy()
	if !reflect.DeepEqual(grown.Spec.Routes[:3], before.Spec.Routes) || !reflect.DeepEqual(grown.Spec.RuntimeProfiles, before.Spec.RuntimeProfiles) || !reflect.DeepEqual(grown.Spec.Ceilings, before.Spec.Ceilings) || !reflect.DeepEqual(grown.Spec.NamespaceSelector, before.Spec.NamespaceSelector) || !reflect.DeepEqual(grown.Spec.Tools, before.Spec.Tools) {
		t.Fatalf("existing policy entries were rewritten: %+v", grown.Spec)
	}
	want := []api.CellnExecutionPolicyRoute{
		{Provider: "anthropic", Protocol: "anthropic-messages", Models: []string{"claude-test"}, EndpointOrigins: []string{"https://api.anthropic.com"}, Auth: "secret"},
		{Provider: "deepseek", Protocol: "openai-chat", Models: []string{"deepseek-chat"}, EndpointOrigins: []string{"https://api.deepseek.com"}, Auth: "secret"},
		{Provider: "openai", Protocol: "openai-chat", Models: []string{"gpt-a", "gpt-b"}, EndpointOrigins: []string{"https://api.openai.com"}, Auth: "secret"},
	}
	if got := secretRoutes(grown); !reflect.DeepEqual(got, want) {
		t.Fatalf("secret routes = %+v", got)
	}
	for _, r := range grown.Spec.Routes {
		for _, origin := range r.EndpointOrigins {
			if r.Auth == "secret" && (!strings.HasPrefix(origin, "https://") || r.AllowInsecure) {
				t.Fatalf("a Secret route may cross plain HTTP: %+v", r)
			}
		}
	}

	// The same options again, and the declaration in another order, change nothing.
	o.MediatedRoutes[0].Models = []string{"gpt-a", "gpt-b"}
	for range 2 {
		if err := InstallPlatform(ctx, store, o); err != nil {
			t.Fatalf("idempotent rerun refused: %v", err)
		}
	}
	if again := policy(); !reflect.DeepEqual(again.Spec, grown.Spec) || again.ResourceVersion != grown.ResourceVersion {
		t.Fatalf("idempotent rerun rewrote the policy: %+v", again.Spec.Routes)
	}

	// A rerun without the option never removes what running agents rely on.
	o.MediateBackends, o.MediatedRoutes = false, nil
	if err := InstallPlatform(ctx, store, o); err != nil {
		t.Fatal(err)
	}
	if kept := policy(); !reflect.DeepEqual(kept.Spec, grown.Spec) {
		t.Fatal("a rerun without mediated routes contracted the policy")
	}
}

// writeBackendConfigurationInsecure is writeBackendConfiguration for a backend
// the operator approved on a plain-HTTP or private endpoint: its template says
// allow_insecure and the receipt binds the rewritten template.
func writeBackendConfigurationInsecure(t *testing.T, dir string, backend testBackend) {
	t.Helper()
	writeBackendConfiguration(t, dir, backend)
	templatePath, receiptPath := filepath.Join(dir, backend.name, "native-template.json"), filepath.Join(dir, backend.name, "configured.json")
	raw, err := os.ReadFile(templatePath)
	if err != nil {
		t.Fatal(err)
	}
	var native map[string]any
	if err := json.Unmarshal(raw, &native); err != nil {
		t.Fatal(err)
	}
	native["template"].(map[string]any)["allow_insecure"] = true
	if raw, err = json.Marshal(native); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(templatePath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var metadata receipt
	receiptRaw, err := os.ReadFile(receiptPath)
	if err != nil || json.Unmarshal(receiptRaw, &metadata) != nil {
		t.Fatalf("receipt: %v", err)
	}
	metadata.NativeTemplateHash = fmt.Sprintf("blake3:%x", blake3.Sum256(raw))
	if receiptRaw, err = json.Marshal(metadata); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(receiptPath, receiptRaw, 0600); err != nil {
		t.Fatal(err)
	}
}
