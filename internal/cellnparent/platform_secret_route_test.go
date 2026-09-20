package cellnparent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnauthority"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// A gateway-mediated decision (a namespace's own Secret, or no credential) is
// never provisioned on the fleet: the plan is refused before anything is
// pinned and the router is never contacted, however the run got here.
func TestPlatformProvisionRefusesGatewayMediatedRoutes(t *testing.T) {
	for _, tc := range []struct {
		name, auth string
		connection func(*api.ModelConnectionSpec)
		agent      func(*api.Agent)
	}{
		{name: "secret", auth: "secret", connection: func(s *api.ModelConnectionSpec) { s.CredentialProfile, s.SecretRef = "", "tenant-key" }, agent: func(a *api.Agent) { a.Spec.AuthRefs = []api.SecretRef{{Provider: "deepseek", Secret: "tenant-key"}} }},
		{name: "none", auth: "none", connection: func(s *api.ModelConnectionSpec) { s.CredentialProfile = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			objects := platformObjects("tenant-a")
			for _, object := range objects {
				switch o := object.(type) {
				case *api.ModelConnection:
					tc.connection(&o.Spec)
				case *api.Agent:
					if tc.agent != nil {
						tc.agent(o)
					}
				case *api.CellnExecutionPolicy:
					// The operator allows the mediated route too: the refusal
					// below is the provision guard, not a missing route.
					o.Spec.Routes = append(o.Spec.Routes, api.CellnExecutionPolicyRoute{Provider: "deepseek", Protocol: "openai-chat", Models: []string{"deepseek-chat"}, EndpointOrigins: []string{"https://api.deepseek.com"}, Auth: tc.auth})
				}
			}
			store := platformStore(t, objects...)
			key := types.NamespacedName{Namespace: "tenant-a", Name: "conversation"}
			incarnation, err := cellnauthority.ScopedParentIncarnation("cluster", "tenant-a-uid", "tenant-a-run")
			if err != nil {
				t.Fatal(err)
			}
			resolution, err := (cellnauthority.PlatformResolver{Reader: store}).Resolve(ctx, key, cellnauthority.PlatformResolveRequest{ClusterID: "cluster", Now: time.Now().UTC(), AdmissionWindow: time.Minute, Operation: "execution.start", ParentIncarnation: incarnation})
			if err != nil || resolution.Decision.Route.Auth != tc.auth {
				t.Fatalf("fixture did not resolve to a %s route: %v", tc.auth, err)
			}
			var run api.AgentRun
			if err := store.Get(ctx, key, &run); err != nil {
				t.Fatal(err)
			}
			scope, _ := cellnauthority.ScopedParentScope("cluster", "tenant-a-uid")
			if plan, _, err := BuildPlatformProvisionPlan(run, *resolution, scope); err == nil || plan != nil || !strings.Contains(err.Error(), "owner-installed model credential route") {
				t.Fatalf("a %s decision produced a provision plan: %v", tc.auth, err)
			}

			var requests atomic.Int32
			router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer router.Close()
			root := t.TempDir()
			token := filepath.Join(root, "token")
			if err := os.WriteFile(token, []byte("platform-admission-test-credential"), 0600); err != nil {
				t.Fatal(err)
			}
			p := PlatformProvisioner{ClusterID: "cluster", Journal: filepath.Join(root, "journal"), Approvals: filepath.Join(root, "approvals"), Target: router.URL, TokenFile: token}
			for _, dir := range []string{p.Journal, p.Approvals} {
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := p.Admit(ctx, client.Reader(store), key); err == nil || !strings.Contains(err.Error(), "owner-installed model credential route") {
				t.Fatalf("a %s run was admitted for provisioning: %v", tc.auth, err)
			}
			if requests.Load() != 0 {
				t.Fatalf("the router received %d requests for a %s route", requests.Load(), tc.auth)
			}
			for _, dir := range []string{p.Journal, p.Approvals} {
				if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
					t.Fatalf("refusal left durable issuance records in %s: %v %v", dir, entries, err)
				}
			}
		})
	}
}
