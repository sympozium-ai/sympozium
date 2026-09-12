package modelgateway

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
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
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestPostgresGatewayTenantCredentialIsolation(t *testing.T) {
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
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()
	g, err := New(Config{ClusterID: "cluster", RegistrationToken: cap.NewToken("issuer-transport-canary"), AllowPrivateOrigins: map[string]bool{provider.URL: true}}, verifier, k8s, budget, authorities)
	if err != nil {
		t.Fatal(err)
	}
	if err = g.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	for _, tenant := range []string{"a", "b"} {
		t.Run(tenant, func(t *testing.T) {
			ns := "tenant-" + tenant
			key := "provider-canary-" + tenant
			uid := types.UID("namespace-" + tenant)
			connUID := types.UID("connection-" + tenant)
			secretUID := types.UID("secret-" + tenant)
			conn := &api.ModelConnection{ObjectMeta: meta.ObjectMeta{Namespace: ns, Name: "model", UID: connUID}, Spec: api.ModelConnectionSpec{Provider: "openai", Protocol: "openai-chat", Endpoint: provider.URL + "/v1/chat/completions", SecretRef: "key", Models: []string{"m"}, AllowInsecure: true}}
			secret := &core.Secret{ObjectMeta: meta.ObjectMeta{Namespace: ns, Name: "key", UID: secretUID}, Data: map[string][]byte{"OPENAI_API_KEY": []byte(key)}}
			for _, obj := range []client.Object{&core.Namespace{ObjectMeta: meta.ObjectMeta{Name: ns, UID: uid}}, conn, secret} {
				if err := k8s.Create(ctx, obj); err != nil {
					t.Fatal(err)
				}
			}
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
			if _, err = g.Invoke(ctx, execution, in); err == nil {
				t.Fatal("execution audience called provider")
			}
			if _, err = g.Invoke(ctx, model, in); err != nil {
				t.Fatal(err)
			}
			if got := <-received; got != "Bearer "+key {
				t.Fatalf("wrong tenant credential %q", got)
			}
			if _, err = g.Invoke(ctx, model, in); err == nil {
				t.Fatal("duplicate replayed")
			}
			secret.Data["OPENAI_API_KEY"] = []byte(key + "-rotated")
			if err = k8s.Update(ctx, secret); err != nil {
				t.Fatal(err)
			}
			in.RequestID = "q2"
			if _, err = g.Invoke(ctx, model, in); err != nil {
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
			if err = k8s.Create(ctx, secret); err != nil {
				t.Fatal(err)
			}
			in.RequestID = "q3"
			if _, err = g.Invoke(ctx, model, in); Reason(err) != ReasonCredentialChanged {
				t.Fatalf("recreated source: %v", err)
			}
			if len(received) != 0 {
				t.Fatal("refused operation contacted provider")
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
