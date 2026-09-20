package modelgateway

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	cap "github.com/sympozium-ai/sympozium/internal/cellncapability"
	"github.com/sympozium-ai/sympozium/internal/modelbudget"
	core "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const policyGuestBody = `{"model":"m","messages":[{"role":"user","content":"hello"}],"max_tokens":%TOKENS%}`

func guestBody(tokens string) []byte {
	return []byte(strings.Replace(policyGuestBody, "%TOKENS%", tokens, 1))
}

func mustParameters(t *testing.T, raw string) map[string]any {
	t.Helper()
	parameters, err := api.ParseModelParameters([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return parameters
}

func TestRequestOutputBoundComesFromConnection(t *testing.T) {
	for _, tc := range []struct {
		name      string
		bound     int64 // spec.maxOutputTokens; 0 is omitted
		turnCap   int64
		requested string
		refused   string // substring of the wrapped error; empty accepts
	}{
		{"default bound accepts 512", 0, 24576, "512", ""},
		{"default bound refuses 513", 0, 24576, "513", "1..512"},
		{"raised bound accepts 4096", 4096, 24576, "4096", ""},
		{"raised bound accepts 513", 2048, 24576, "513", ""},
		{"raised bound refuses above it", 2048, 24576, "2049", "1..2048"},
		{"maximum bound refuses above it", 4096, 24576, "4097", "1..4096"},
		{"turn cap still applies under a raised bound", 4096, 1024, "2048", "turn's output-token cap 1024"},
		{"zero tokens refuse", 4096, 24576, "0", "1..4096"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := api.ModelConnectionSpec{Provider: "openai", Protocol: "openai-chat", Endpoint: "https://model.example/v1/chat/completions", SecretRef: "key", Models: []string{"m"}, MaxOutputTokens: tc.bound}
			if err := spec.Validate(); err != nil {
				t.Fatal(err)
			}
			policy, err := connectionRequestPolicy(spec)
			if err != nil {
				t.Fatal(err)
			}
			reserved, _, _, _, err := validateProviderRequest("openai-chat", "m", guestBody(tc.requested), tc.turnCap, policy)
			if tc.refused == "" {
				if err != nil {
					t.Fatalf("refused: %v (%v)", err, errors.Unwrap(err))
				}
				if want, _ := json.Number(tc.requested).Int64(); reserved != want {
					t.Fatalf("reserved %d want %d", reserved, want)
				}
				return
			}
			if Reason(err) != ReasonForbidden || errors.Unwrap(err) == nil || !strings.Contains(errors.Unwrap(err).Error(), tc.refused) {
				t.Fatalf("want %s naming %q, got %v (%v)", ReasonForbidden, tc.refused, err, errors.Unwrap(err))
			}
		})
	}
}

func TestConnectionParametersAreHostPinned(t *testing.T) {
	plainDigest, plainCanonical, err := requestDigest(guestBody("256"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		parameters string
		guest      string
		outgoing   string // exact forwarded body; empty means refused
		reason     string
	}{
		{"no parameters forwards the canonical guest body", ``, string(guestBody("256")), string(plainCanonical), ""},
		{"parameters are appended to the guest body", `{"chat_template_kwargs":{"enable_thinking":false},"seed":7}`, string(guestBody("256")),
			`{"max_tokens":256,"messages":[{"content":"hello","role":"user"}],"model":"m","chat_template_kwargs":{"enable_thinking":false},"seed":7}`, ""},
		{"a fractional parameter is forwarded as written", `{"temperature":0.7,"top_p":0.95,"min_p":1e-2}`, string(guestBody("256")),
			`{"max_tokens":256,"messages":[{"content":"hello","role":"user"}],"model":"m","min_p":1e-2,"temperature":0.7,"top_p":0.95}`, ""},
		{"guest may not set a pinned key", `{"temperature":0.7}`, `{"model":"m","messages":[{"role":"user","content":"hello"}],"max_tokens":256,"temperature":1}`, "", ReasonForbidden},
		{"guest may not repeat a pinned key with the same value", `{"seed":7}`, `{"model":"m","messages":[{"role":"user","content":"hello"}],"max_tokens":256,"seed":7}`, "", ReasonForbidden},
		{"guest may not alias a pinned key by case", `{"seed":7}`, `{"model":"m","messages":[{"role":"user","content":"hello"}],"max_tokens":256,"SEED":1}`, "", ReasonMalformed},
		{"guest fractional numbers still refuse", `{"seed":7}`, `{"model":"m","messages":[{"role":"user","content":"hello"}],"max_tokens":256,"temperature":0.7}`, "", ReasonMalformed},
		{"guest unknown fields still refuse", `{"seed":7}`, `{"model":"m","messages":[{"role":"user","content":"hello"}],"max_tokens":256,"chat_template_kwargs":{}}`, "", ReasonMalformed},
		{"guest may not alias max_tokens by case", ``, `{"model":"m","messages":[{"role":"user","content":"hello"}],"MAX_TOKENS":4096,"max_tokens":256}`, "", ReasonMalformed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := requestPolicy{Parameters: mustParameters(t, tc.parameters)}
			_, digest, canonical, outgoing, err := validateProviderRequest("openai-chat", "m", []byte(tc.guest), 3072, policy)
			if tc.outgoing == "" {
				if Reason(err) != tc.reason || err == nil {
					t.Fatalf("want %s, got %v", tc.reason, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if string(outgoing) != tc.outgoing {
				t.Fatalf("forwarded body\n got %s\nwant %s", outgoing, tc.outgoing)
			}
			// The reservation digest is of the guest body alone, exactly as
			// before parameters existed.
			if digest != plainDigest || string(canonical) != string(plainCanonical) {
				t.Fatalf("parameters changed the reserved request digest: %s %s", digest, canonical)
			}
		})
	}
}

func TestMergeRefusesUnvalidatedParameters(t *testing.T) {
	for _, reserved := range api.ReservedModelParameters {
		if _, err := mergeParameters([]byte(`{"model":"m"}`), map[string]any{reserved: json.Number("1")}); err == nil {
			t.Fatalf("merged reserved key %q", reserved)
		}
	}
}

// memoryStores are in-memory BudgetStore and AuthorityStore fakes: enough to
// drive Register and Invoke without PostgreSQL. They prove nothing about
// durable accounting, which the PostgreSQL tier covers.
type memoryStores struct {
	BudgetStore
	mu          sync.Mutex
	authorities map[string]Authority
	requests    map[string]modelbudget.ReservationRequest
}

func (s *memoryStores) RegisterRun(context.Context, modelbudget.RunRegistration) error   { return nil }
func (s *memoryStores) RegisterTurn(context.Context, modelbudget.TurnRegistration) error { return nil }
func (s *memoryStores) CheckReady(context.Context) error                                 { return nil }
func (s *memoryStores) MarkInFlight(context.Context, string, string, string) error       { return nil }
func (s *memoryStores) Reconcile(context.Context, string, string, string, int64, string, string) error {
	return nil
}
func (s *memoryStores) ReconcileUnknown(context.Context, string, string, string, string, string) error {
	return nil
}
func (s *memoryStores) Inspect(context.Context, string, string) (modelbudget.Usage, error) {
	return modelbudget.Usage{}, nil
}
func (s *memoryStores) ReserveBound(_ context.Context, in modelbudget.ReservationRequest, _ modelbudget.ReservationBinding) (modelbudget.Reservation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, existing := s.requests[in.RequestID]
	s.requests[in.RequestID] = in
	return modelbudget.Reservation{RequestID: in.RequestID, RequestDigest: in.RequestDigest, ReservedOutputTokens: in.ReservedOutputTokens, Existing: existing}, nil
}
func (s *memoryStores) RegisterAuthority(_ context.Context, a Authority) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.authorities[a.BudgetID+"/"+a.TurnID] = a
	return nil
}
func (s *memoryStores) Authority(_ context.Context, budgetID, turnID string) (Authority, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.authorities[budgetID+"/"+turnID]
	if !ok {
		return Authority{}, fail(ReasonForbidden, 403, nil)
	}
	return a, nil
}

func TestInvokeAppliesLiveConnectionRequestPolicy(t *testing.T) {
	ctx := context.Background()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := cap.NewIssuer(cap.ControlPlaneIssuer, cap.SigningKey{KeyID: "test", PrivateKey: priv}, nil)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := cap.NewVerifier(cap.ControlPlaneIssuer, []cap.VerificationKey{{KeyID: "test", PublicKey: pub}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	forwarded := make(chan string, 4)
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		forwarded <- string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"fixture result"},"finish_reason":"stop"}],"usage":{"completion_tokens":1}}`))
	}))
	defer provider.Close()
	scheme := runtime.NewScheme()
	_ = core.AddToScheme(scheme)
	_ = api.AddToScheme(scheme)
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()
	stores := &memoryStores{authorities: map[string]Authority{}, requests: map[string]modelbudget.ReservationRequest{}}
	g, err := New(Config{AuthorityReady: func(context.Context) error { return nil }, ClusterID: "cluster", RegistrationToken: cap.NewToken("issuer-transport"),
		AllowPrivateOrigins: map[string]bool{provider.URL: true}, ProviderRootCAs: provider.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs}, verifier, k8s, stores, stores)
	if err != nil {
		t.Fatal(err)
	}
	const ns = "tenant"
	conn := &api.ModelConnection{ObjectMeta: meta.ObjectMeta{Namespace: ns, Name: "model", UID: "connection-uid"}, Spec: api.ModelConnectionSpec{
		Provider: "openai", Protocol: "openai-chat", Endpoint: provider.URL + "/v1/chat/completions", SecretRef: "key", Models: []string{"m"}, AllowInsecure: true,
		Parameters: &apiextensionsv1.JSON{Raw: []byte(`{"temperature": 0.7, "chat_template_kwargs": {"enable_thinking": false}}`)}, MaxOutputTokens: 2048}}
	for _, obj := range []client.Object{
		&core.Namespace{ObjectMeta: meta.ObjectMeta{Name: ns, UID: "namespace-uid"}}, conn,
		&core.Secret{ObjectMeta: meta.ObjectMeta{Namespace: ns, Name: "key", UID: "secret-uid"}, Data: map[string][]byte{"OPENAI_API_KEY": []byte("provider-key")}},
	} {
		if err := k8s.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	digest, err := connectionDigest(conn.Spec)
	if err != nil {
		t.Fatal(err)
	}
	connUID := "connection-uid"
	now := time.Now().Unix()
	d := cap.Decision{APIVersion: cap.DecisionAPIVersion, Kind: "CellnAuthorisationDecision", ClusterID: "cluster", Operation: "execution.start", Lifecycle: "one-shot",
		Run:     cap.RunBinding{Namespace: ns, NamespaceUID: "namespace-uid", UID: "run-uid"},
		Route:   cap.RouteBinding{ModelConnectionUID: &connUID, ModelConnectionSpecSHA256: digest, Provider: "openai", Protocol: "openai-chat", Model: "m", EndpointOrigin: provider.URL, Auth: "secret", CredentialSource: &cap.CredentialSource{Kind: "Secret", SecretUID: "secret-uid", SecretName: "key", SecretKey: "OPENAI_API_KEY"}},
		Budget:  cap.BudgetBinding{MaxTurns: 1, RunCap: cap.Cap{Requests: 6, OutputTokens: 12288}, TurnCap: cap.Cap{Requests: 6, OutputTokens: 12288}, TurnDeadlineUnix: now + 120},
		Windows: cap.Windows{IssuedAt: now, NotBefore: now, AdmissionDeadline: now + 60}}
	completeGatewayDecision(&d)
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
	if err := g.Register(ctx, RegistrationRequest{Decision: raw, ExecutionToken: execution, ConnectionName: "model"}); err != nil {
		t.Fatal(err)
	}
	guestDigest, _, err := requestDigest(guestBody("2048"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, id string
		body     []byte
		reason   string
	}{
		{"above the connection's bound", "q-over", guestBody("2049"), ReasonForbidden},
		{"override of a pinned parameter", "q-override", []byte(`{"model":"m","messages":[{"role":"user","content":"hello"}],"max_tokens":256,"temperature":1}`), ReasonForbidden},
		{"raised bound with merged parameters", "q-ok", guestBody("2048"), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := g.Invoke(ctx, model, InvokeRequest{Decision: raw, RequestID: tc.id, Request: tc.body})
			if tc.reason != "" {
				if Reason(err) != tc.reason || err == nil {
					t.Fatalf("want %s, got %v", tc.reason, err)
				}
				if _, reserved := stores.requests[tc.id]; reserved {
					t.Fatal("a refused request reserved allowance")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := `{"max_tokens":2048,"messages":[{"content":"hello","role":"user"}],"model":"m","chat_template_kwargs":{"enable_thinking":false},"temperature":0.7}`
			if got := <-forwarded; got != want {
				t.Fatalf("forwarded body\n got %s\nwant %s", got, want)
			}
			if got := stores.requests[tc.id]; got.RequestDigest != guestDigest || got.ReservedOutputTokens != 2048 {
				t.Fatalf("reservation is not of the guest body alone: %+v", got)
			}
		})
	}
	// A changed request policy is a changed spec: the registered run refuses
	// instead of silently taking the new parameters or bound.
	for _, tc := range []struct {
		name   string
		change func(*api.ModelConnectionSpec)
	}{
		{"parameters changed", func(s *api.ModelConnectionSpec) {
			s.Parameters = &apiextensionsv1.JSON{Raw: []byte(`{"temperature":0.9,"chat_template_kwargs":{"enable_thinking":false}}`)}
		}},
		{"parameters removed", func(s *api.ModelConnectionSpec) { s.Parameters = nil }},
		{"bound raised", func(s *api.ModelConnectionSpec) { s.MaxOutputTokens = 4096 }},
		{"bound removed", func(s *api.ModelConnectionSpec) { s.MaxOutputTokens = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var live api.ModelConnection
			if err := k8s.Get(ctx, types.NamespacedName{Namespace: ns, Name: "model"}, &live); err != nil {
				t.Fatal(err)
			}
			original := *live.Spec.DeepCopy()
			tc.change(&live.Spec)
			if err := k8s.Update(ctx, &live); err != nil {
				t.Fatal(err)
			}
			_, err := g.Invoke(ctx, model, InvokeRequest{Decision: raw, RequestID: "changed-" + tc.name, Request: guestBody("256")})
			if Reason(err) != ReasonRouteChanged {
				t.Fatalf("want %s, got %v", ReasonRouteChanged, err)
			}
			select {
			case body := <-forwarded:
				t.Fatalf("changed policy reached the provider: %s", body)
			default:
			}
			live.Spec = original
			if err := k8s.Update(ctx, &live); err != nil {
				t.Fatal(err)
			}
		})
	}
	// Restored to the pinned spec, the same run works again.
	if _, err := g.Invoke(ctx, model, InvokeRequest{Decision: raw, RequestID: "q-restored", Request: guestBody("256")}); err != nil {
		t.Fatal(err)
	}
	<-forwarded
}
