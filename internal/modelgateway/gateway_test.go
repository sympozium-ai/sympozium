package modelgateway

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	cap "github.com/sympozium-ai/sympozium/internal/cellncapability"
	"github.com/sympozium-ai/sympozium/internal/modelbudget"
	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	authclient "k8s.io/client-go/kubernetes/typed/authorization/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestPostgresGatewayTenantCredentialIsolation(t *testing.T) {
	testGatewayTenantCredentialIsolation(t, false)
}

func TestLiveKubernetesGatewayTenantCredentialIsolation(t *testing.T) {
	if os.Getenv("CELLN_GATEWAY_LIVE_KUBERNETES") != "1" {
		t.Skip("set CELLN_GATEWAY_LIVE_KUBERNETES=1 to create temporary namespaces on the configured cluster")
	}
	if os.Getenv("CELLN_MODEL_BUDGET_DATABASE_URL") == "" {
		t.Fatal("live proof requires CELLN_MODEL_BUDGET_DATABASE_URL")
	}
	testGatewayTenantCredentialIsolation(t, true)
}

func testGatewayTenantCredentialIsolation(t *testing.T, live bool) {
	db := os.Getenv("CELLN_MODEL_BUDGET_DATABASE_URL")
	if db == "" {
		t.Skip("explicit PostgreSQL test tier requires CELLN_MODEL_BUDGET_DATABASE_URL")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	for _, name := range []string{"002_celln_model_budget.sql", "003_celln_model_gateway.sql"} {
		raw, err := os.ReadFile("../../migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	budget, err := modelbudget.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	authorities, err := NewPostgresAuthorityStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := cap.NewIssuer("test-issuer", cap.SigningKey{KeyID: "test", PrivateKey: priv}, nil)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := cap.NewVerifier("test-issuer", []cap.VerificationKey{{KeyID: "test", PublicKey: pub}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	received := make(chan string, 10)
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("X-Celln-Execution-Permit") != "" {
			t.Error("forwarded caller header")
		}
		received <- r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[],"usage":{"completion_tokens":1}}`))
	}))
	defer provider.Close()
	scheme := runtime.NewScheme()
	_ = core.AddToScheme(scheme)
	_ = api.AddToScheme(scheme)
	probe := func(context.Context) error { return nil } // Fake-client tier only.
	var k8s client.Client = fake.NewClientBuilder().WithScheme(scheme).Build()
	if live {
		cfg, err := ctrl.GetConfig()
		if err != nil {
			t.Fatal(err)
		}
		cfg.Timeout = 5 * time.Second
		auth, err := authclient.NewForConfig(cfg)
		if err != nil {
			t.Fatal(err)
		}
		probe = AuthorityReadiness(auth.SelfSubjectAccessReviews())
		k8s, err = client.New(cfg, client.Options{Scheme: scheme})
		if err != nil {
			t.Fatal(err)
		}
		t.Log("live Kubernetes authority test: caller uses configured kubeconfig; this is not tenant RBAC isolation proof")
	}
	g, err := New(Config{AuthorityReady: probe, ClusterID: "cluster", RegistrationToken: cap.NewToken("issuer-transport-canary"), AllowPrivateOrigins: map[string]bool{provider.URL: true}}, verifier, k8s, budget, authorities)
	if err != nil {
		t.Fatal(err)
	}
	// Trust only the recorder's generated CA; keep production destination
	// validation and certificate verification enabled (never InsecureSkipVerify).
	g.newClient = func(endpoint string, private bool, timeout time.Duration) (*http.Client, error) {
		out, err := clientForEndpoint(endpoint, private, timeout)
		if err != nil {
			return nil, err
		}
		out.Transport.(*http.Transport).TLSClientConfig.RootCAs = provider.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
		return out, nil
	}
	if err = g.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(g.Handler())
	defer server.Close()
	invoke := func(ctx context.Context, token cap.Token, in InvokeRequest) (InvokeResponse, error) {
		body, err := json.Marshal(in)
		if err != nil {
			return InvokeResponse{}, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/invoke", bytes.NewReader(body))
		if err != nil {
			return InvokeResponse{}, err
		}
		req.Header.Set("Authorization", "Bearer "+token.Bearer())
		req.Header.Set("X-Forwarded-For", "caller-controlled")
		req.Header.Set("X-Celln-Execution-Permit", "must-not-forward")
		resp, err := server.Client().Do(req)
		if err != nil {
			return InvokeResponse{}, err
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			return InvokeResponse{}, err
		}
		if resp.StatusCode != http.StatusOK {
			var refusal struct {
				Reason string `json:"reason"`
			}
			if err := json.Unmarshal(raw, &refusal); err != nil {
				return InvokeResponse{}, err
			}
			return InvokeResponse{}, fail(refusal.Reason, resp.StatusCode, nil)
		}
		return InvokeResponse{StatusCode: resp.StatusCode, Body: raw}, nil
	}
	for _, tenant := range []string{"a", "b"} {
		t.Run(tenant, func(t *testing.T) {
			ns := fmt.Sprintf("celln-gateway-test-%s-%d", tenant, time.Now().UnixNano())
			key := "provider-canary-" + tenant
			uid := types.UID("namespace-" + tenant)
			connUID := types.UID("connection-" + tenant)
			secretUID := types.UID("secret-" + tenant)
			conn := &api.ModelConnection{ObjectMeta: meta.ObjectMeta{Namespace: ns, Name: "model", UID: connUID}, Spec: api.ModelConnectionSpec{Provider: "openai", Protocol: "openai-chat", Endpoint: provider.URL + "/v1/chat/completions", SecretRef: "key", Models: []string{"m"}, AllowInsecure: true}}
			secret := &core.Secret{ObjectMeta: meta.ObjectMeta{Namespace: ns, Name: "key", UID: secretUID}, Data: map[string][]byte{"OPENAI_API_KEY": []byte(key)}}
			namespace := &core.Namespace{ObjectMeta: meta.ObjectMeta{Name: ns, UID: uid, Labels: map[string]string{"sympozium.ai/test": "model-gateway"}}}
			for _, obj := range []client.Object{namespace, conn, secret} {
				if live {
					obj.SetUID("")
				}
				if err := k8s.Create(ctx, obj); err != nil {
					t.Fatal(err)
				}
				if obj == namespace && live {
					t.Cleanup(func() {
						cleanupCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
						defer cancel()
						if err := k8s.Delete(cleanupCtx, namespace); err != nil && !apierrors.IsNotFound(err) {
							t.Error(err)
							return
						}
						err := wait.PollUntilContextCancel(cleanupCtx, time.Second, true, func(ctx context.Context) (bool, error) {
							var remaining core.Namespace
							err := k8s.Get(ctx, client.ObjectKey{Name: ns}, &remaining)
							if apierrors.IsNotFound(err) {
								return true, nil
							}
							return false, err
						})
						if err != nil {
							t.Errorf("namespace cleanup pending for %s: %v", ns, err)
						} else {
							t.Logf("confirmed namespace cleanup: %s", ns)
						}
					})
				}
			}
			uid, connUID, secretUID = namespace.UID, conn.UID, secret.UID
			t.Logf("namespace=%s uid=%s connectionUid=%s secretUid=%s", ns, uid, connUID, secretUID)
			digest, err := connectionDigest(conn.Spec)
			if err != nil {
				t.Fatal(err)
			}
			cuid := string(connUID)
			now := time.Now().Unix()
			run := fmt.Sprintf("run-%s-%d", tenant, time.Now().UnixNano())
			d := cap.Decision{APIVersion: cap.DecisionAPIVersion, Kind: "CellnAuthorisationDecision", ClusterID: "cluster", Operation: "execution.start", Lifecycle: "one-shot", Run: cap.RunBinding{Namespace: ns, NamespaceUID: string(uid), UID: run, SpecSHA256: "spec"}, Route: cap.RouteBinding{ModelConnectionUID: &cuid, ModelConnectionSpecSHA256: digest, Provider: "openai", Protocol: "openai-chat", Model: "m", EndpointOrigin: provider.URL, Auth: "secret", CredentialSource: &cap.CredentialSource{Kind: "Secret", SecretUID: string(secretUID), SecretName: "key", SecretKey: "OPENAI_API_KEY"}}, Budget: cap.BudgetBinding{BudgetID: run, MaxTurns: 1, RunCap: cap.Cap{Requests: 3, OutputTokens: 1536}, TurnCap: cap.Cap{Requests: 3, OutputTokens: 1536}, TurnDeadlineUnix: now + 120}, Windows: cap.Windows{IssuedAt: now, NotBefore: now, AdmissionDeadline: now + 60}, RequestDigest: "request"}
			raw, _, err := cap.CanonicalDecision(d)
			if err != nil {
				t.Fatal(err)
			}
			execution, err := issuer.Issue(d, cap.IssueRequest{Audience: cap.AudienceExecution, Operation: "execution.start"})
			if err != nil {
				t.Fatal(err)
			}
			model, err := issuer.Issue(d, cap.IssueRequest{Audience: cap.AudienceModelGateway, Operation: "model.invoke"})
			if err != nil {
				t.Fatal(err)
			}
			registration := RegistrationRequest{Decision: raw, ExecutionToken: execution, ConnectionName: "model"}
			if err = g.Register(ctx, registration); err != nil {
				t.Fatal(err)
			}
			in := InvokeRequest{Decision: raw, RequestID: "q1", Request: []byte(`{"model":"m","messages":[{"role":"user","content":"hello"}],"max_tokens":512}`)}
			if _, err = invoke(ctx, execution, in); err == nil {
				t.Fatal("execution audience called provider")
			}
			if _, err = invoke(ctx, model, in); err != nil {
				t.Fatal(err)
			}
			if got := <-received; got != "Bearer "+key {
				t.Fatalf("wrong tenant credential %q", got)
			}
			if _, err = invoke(ctx, model, in); err == nil {
				t.Fatal("duplicate replayed")
			}
			secret.Data["OPENAI_API_KEY"] = []byte(key + "-rotated")
			if err = k8s.Update(ctx, secret); err != nil {
				t.Fatal(err)
			}
			in.RequestID = "q2"
			if _, err = invoke(ctx, model, in); err != nil {
				t.Fatal(err)
			}
			if got := <-received; got != "Bearer "+key+"-rotated" {
				t.Fatal("data rotation not used")
			}
			if err = k8s.Delete(ctx, secret); err != nil {
				t.Fatal(err)
			}
			secret.ResourceVersion = ""
			secret.UID = types.UID("replacement-" + tenant)
			if live {
				secret.UID = ""
			}
			if err = k8s.Create(ctx, secret); err != nil {
				t.Fatal(err)
			}
			in.RequestID = "q3"
			if _, err = invoke(ctx, model, in); Reason(err) != ReasonCredentialChanged {
				t.Fatalf("recreated source: %v", err)
			}
			if len(received) != 0 {
				t.Fatal("refused operation contacted provider")
			}
			// Cleanup remains possible even though the pinned Secret no longer
			// exists. Model/start credentials cannot substitute for owner cleanup.
			cleanupDecision := d
			cleanupDecision.Operation = "execution.cleanup"
			cleanupRaw, _, err := cap.CanonicalDecision(cleanupDecision)
			if err != nil {
				t.Fatal(err)
			}
			cleanupToken, err := issuer.Issue(cleanupDecision, cap.IssueRequest{Audience: cap.AudienceExecution, Operation: "execution.cleanup"})
			if err != nil {
				t.Fatal(err)
			}
			for _, permission := range []struct {
				transport, permit string
				want              int
			}{
				{"", cleanupToken.Bearer(), 401},
				{"issuer-transport-canary", "", 401},
				{"issuer-transport-canary", model.Bearer(), 401},
				{"issuer-transport-canary", execution.Bearer(), 401},
				{"issuer-transport-canary", cleanupToken.Bearer(), 204},
			} {
				body, err := json.Marshal(CloseRequest{Decision: cleanupRaw})
				if err != nil {
					t.Fatal(err)
				}
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/internal/close", bytes.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Authorization", "Bearer "+permission.transport)
				req.Header.Set("X-Celln-Execution-Permit", permission.permit)
				resp, err := server.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				resp.Body.Close()
				if resp.StatusCode != permission.want {
					t.Fatalf("cleanup status=%d want=%d", resp.StatusCode, permission.want)
				}
			}
			usage, err := budget.Inspect(ctx, run, run)
			if err != nil {
				t.Fatal(err)
			}
			if !usage.RunClosed || !usage.TurnClosed || usage.RunReservedRequests != 2 || usage.RunReservedOutputTokens != 1024 {
				t.Fatalf("cleanup lost fence or refunded usage: %+v", usage)
			}
		})
	}
	req := httptest.NewRequest("POST", "/internal/pin", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer wrong-canary")
	w := httptest.NewRecorder()
	g.Handler().ServeHTTP(w, req)
	if w.Code != 401 || strings.Contains(w.Body.String(), "canary") {
		t.Fatal("unauthenticated registration or credential disclosure")
	}
}
