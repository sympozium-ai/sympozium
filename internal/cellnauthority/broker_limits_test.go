package cellnauthority

import (
	"reflect"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

func brokerFixture(t *testing.T, https bool) ToolRequest {
	t.Helper()
	r := fixture(t)
	r.Catalogue[0].Spec.InvocationABI = "celln.json-stdio/v1"
	l := &r.Catalogue[0].Spec.Limits
	l.Effects = "external-side-effects"
	if https {
		l.HTTPS = &api.CellnHTTPSLimits{AllowHosts: []string{"example.com", "example.org"}, MaxRequests: 4, MaxResponseBytes: 4096, TimeoutMillis: 1000}
	} else {
		l.Artifacts = &api.CellnArtifactLimits{Operation: "write", MaxOperations: 8, MaxFiles: 32, MaxFileBytes: 4096, MaxTotalBytes: 65536}
	}
	id, err := Identify(r.Catalogue[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, grants := range [][]Grant{r.Operator, r.Runtime, r.Agent, r.Selection} {
		grants[0] = Grant{Tool: id, Limits: *l.DeepCopy()}
	}
	return r
}

func TestStarterRequiresEveryExplicitBrokerGrant(t *testing.T) {
	for _, https := range []bool{false, true} {
		for layer := 0; layer < 4; layer++ {
			r := brokerFixture(t, https)
			grants := [][]Grant{r.Operator, r.Runtime, r.Agent, r.Selection}[layer]
			grants[0].Limits.Artifacts = nil
			grants[0].Limits.HTTPS = nil
			if _, err := ResolveTools(r); err == nil {
				t.Fatalf("missing broker permission accepted: https=%v layer=%d", https, layer)
			}
		}
	}
}

func TestArtifactLimitsIntersectWithoutMutatingSources(t *testing.T) {
	r := brokerFixture(t, false)
	r.Operator[0].Limits.Artifacts.MaxOperations = 3
	r.Runtime[0].Limits.Artifacts.MaxFiles = 2
	r.Agent[0].Limits.Artifacts.MaxTotalBytes = 2048
	r.Agent[0].Limits.Artifacts.MaxFileBytes = 2048
	r.Selection[0].Limits.Artifacts.MaxFileBytes = 1024
	got, err := ResolveTools(r)
	if err != nil {
		t.Fatal(err)
	}
	want := &api.CellnArtifactLimits{Operation: "write", MaxOperations: 3, MaxFiles: 2, MaxFileBytes: 1024, MaxTotalBytes: 2048}
	if !reflect.DeepEqual(got[0].Limits.Artifacts, want) {
		t.Fatalf("incorrect intersection: %+v", got[0].Limits.Artifacts)
	}
	if r.Catalogue[0].Spec.Limits.Artifacts.MaxFiles != 32 {
		t.Fatal("catalogue mutated")
	}
	r.Agent[0].Limits.Artifacts.Operation = "read"
	if _, err := ResolveTools(r); err == nil {
		t.Fatal("read grant authorized write")
	}
}

func TestHTTPSLimitsIntersectHostsAndBudgets(t *testing.T) {
	r := brokerFixture(t, true)
	r.Agent[0].Limits.HTTPS.AllowHosts = []string{"example.org"}
	r.Runtime[0].Limits.HTTPS.MaxRequests = 2
	r.Selection[0].Limits.HTTPS.MaxResponseBytes = 512
	r.Operator[0].Limits.TimeoutMillis = 500
	r.Operator[0].Limits.HTTPS.TimeoutMillis = 500
	got, err := ResolveTools(r)
	if err != nil {
		t.Fatal(err)
	}
	want := &api.CellnHTTPSLimits{AllowHosts: []string{"example.org"}, MaxRequests: 2, MaxResponseBytes: 512, TimeoutMillis: 500}
	if !reflect.DeepEqual(got[0].Limits.HTTPS, want) {
		t.Fatalf("incorrect intersection: %+v", got[0].Limits.HTTPS)
	}
	if len(r.Catalogue[0].Spec.Limits.HTTPS.AllowHosts) != 2 {
		t.Fatal("catalogue mutated")
	}
	r.Agent[0].Limits.HTTPS.AllowHosts = []string{"unapproved.example"}
	if _, err := ResolveTools(r); err == nil {
		t.Fatal("disjoint HTTPS authority accepted")
	}
}

func TestBrokerLimitsRejectAmbientAuthorityAndInvalidBounds(t *testing.T) {
	for _, host := range []string{"*", "https://example.com", "example.com:443", "user@example.com", "Example.com", "example.com/secret", "example..com"} {
		r := brokerFixture(t, true)
		r.Agent[0].Limits.HTTPS.AllowHosts = []string{host}
		if _, err := ResolveTools(r); err == nil {
			t.Fatalf("invalid host accepted: %s", host)
		}
	}
	r := brokerFixture(t, false)
	r.Agent[0].Limits.Artifacts.MaxOperations = 0
	if _, err := ResolveTools(r); err == nil {
		t.Fatal("unbounded artifact operations")
	}
}
