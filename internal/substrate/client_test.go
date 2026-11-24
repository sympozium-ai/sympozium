package substrate

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	ateapi "github.com/sympozium-ai/sympozium/third_party/substrate/ateapi/v1"
)

// fakeControl is an in-memory Control API: enough state to prove the client's
// idempotency and state mapping, none of Substrate's behaviour.
type fakeControl struct {
	ateapi.UnimplementedControlServer
	mu        sync.Mutex
	actors    map[string]*ateapi.Actor
	templates map[string]*ateapi.ActorTemplate
	policies  map[string]*ateapi.EgressPolicy
	creates   int
	tokens    []string
	resumeErr error
}

func newFake() *fakeControl {
	return &fakeControl{actors: map[string]*ateapi.Actor{}, templates: map[string]*ateapi.ActorTemplate{}, policies: map[string]*ateapi.EgressPolicy{}}
}

func key(r *ateapi.ObjectRef) string { return r.GetAtespace() + "/" + r.GetName() }

func (f *fakeControl) auth(ctx context.Context) error {
	md, _ := metadata.FromIncomingContext(ctx)
	got := md.Get("authorization")
	if len(got) != 1 || got[0] != "Bearer good" {
		return status.Error(codes.Unauthenticated, "no credential")
	}
	f.tokens = append(f.tokens, got[0])
	return nil
}

func (f *fakeControl) CreateAtespace(ctx context.Context, _ *ateapi.CreateAtespaceRequest) (*ateapi.Atespace, error) {
	if err := f.auth(ctx); err != nil {
		return nil, err
	}
	return nil, status.Error(codes.AlreadyExists, "exists")
}

func (f *fakeControl) GetActorTemplate(_ context.Context, r *ateapi.GetActorTemplateRequest) (*ateapi.ActorTemplate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t, ok := f.templates[key(r.GetActorTemplate())]; ok {
		return t, nil
	}
	return nil, status.Error(codes.NotFound, "no template")
}

func (f *fakeControl) CreateActorTemplate(_ context.Context, r *ateapi.CreateActorTemplateRequest) (*ateapi.ActorTemplate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := r.GetActorTemplate()
	f.templates[t.GetMetadata().GetAtespace()+"/"+t.GetMetadata().GetName()] = t
	return t, nil
}

func (f *fakeControl) CreateActor(_ context.Context, r *ateapi.CreateActorRequest) (*ateapi.Actor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := r.GetActor().GetMetadata().GetAtespace() + "/" + r.GetActor().GetMetadata().GetName()
	if _, ok := f.actors[k]; ok {
		return nil, status.Error(codes.AlreadyExists, "exists")
	}
	f.creates++
	a := r.GetActor()
	a.Metadata.Uid = "uid-1"
	a.Status = &ateapi.ActorStatus{State: ateapi.ActorState_ACTOR_STATE_SUSPENDED}
	f.actors[k] = a
	return a, nil
}

func (f *fakeControl) GetActor(_ context.Context, r *ateapi.GetActorRequest) (*ateapi.Actor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if a, ok := f.actors[key(r.GetActor())]; ok {
		return a, nil
	}
	return nil, status.Error(codes.NotFound, "no actor")
}

func (f *fakeControl) ResumeActor(_ context.Context, r *ateapi.ResumeActorRequest) (*ateapi.ResumeActorResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.resumeErr != nil {
		return nil, f.resumeErr
	}
	a := f.actors[key(r.GetActor())]
	a.Status = &ateapi.ActorStatus{State: ateapi.ActorState_ACTOR_STATE_RUNNING, WorkerAssignment: &ateapi.WorkerAssignment{WorkerPod: "worker-b", NodeName: "framework"}}
	return &ateapi.ResumeActorResponse{Actor: a, Resumed: true}, nil
}

func (f *fakeControl) DeleteActor(_ context.Context, r *ateapi.DeleteActorRequest) (*ateapi.Actor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.actors[key(r.GetActor())]
	if !ok {
		return nil, status.Error(codes.NotFound, "no actor")
	}
	delete(f.actors, key(r.GetActor()))
	return a, nil
}

func (f *fakeControl) CreateActorEgressPolicy(_ context.Context, r *ateapi.CreateActorEgressPolicyRequest) (*ateapi.EgressPolicy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.policies[key(r.GetActor())]; ok {
		return nil, status.Error(codes.AlreadyExists, "exists")
	}
	f.policies[key(r.GetActor())] = r.GetEgressPolicy()
	return r.GetEgressPolicy(), nil
}

func (f *fakeControl) UpdateActorEgressPolicy(_ context.Context, r *ateapi.UpdateActorEgressPolicyRequest) (*ateapi.EgressPolicy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.policies[key(r.GetActor())] = r.GetEgressPolicy()
	return r.GetEgressPolicy(), nil
}

// serve starts the fake behind TLS on loopback and returns a client for it.
func serve(t *testing.T, f *fakeControl, token string) *Client {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "ate-api"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		DNSNames: []string{"api.ate-system.svc"}, IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: priv}},
	})))
	ateapi.RegisterControlServer(srv, f)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	c, err := Dial(Config{
		Endpoint: lis.Addr().String(), RootCAs: pool, ServerName: "api.ate-system.svc",
		Token: func(context.Context) (string, error) { return token + "\n", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestBearerTokenIsSentAndTrimmed(t *testing.T) {
	f := newFake()
	if err := serve(t, f, "good").EnsureAtespace(context.Background(), "symp-default"); err != nil {
		t.Fatalf("existing atespace must not be an error: %v", err)
	}
	if len(f.tokens) != 1 {
		t.Fatalf("server saw %d authenticated calls, want 1", len(f.tokens))
	}
	if err := serve(t, newFake(), "bad").EnsureAtespace(context.Background(), "x"); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("a wrong token must surface Unauthenticated, got %v", err)
	}
}

func TestDialRequiresACredential(t *testing.T) {
	if _, err := Dial(Config{Endpoint: "x:1"}); err == nil {
		t.Fatal("a client without a token source must not be built")
	}
}

func TestCreateActorIsIdempotentByName(t *testing.T) {
	f := newFake()
	c := serve(t, f, "good")
	ctx := context.Background()

	first, created, err := c.CreateActor(ctx, "symp-default", "run-abc", "runner-deadbeef")
	if err != nil || !created || first.State != StateSuspended {
		t.Fatalf("first create: actor=%+v created=%v err=%v", first, created, err)
	}
	second, created, err := c.CreateActor(ctx, "symp-default", "run-abc", "runner-deadbeef")
	if err != nil || created {
		t.Fatalf("replayed create must return the existing actor: created=%v err=%v", created, err)
	}
	if second.UID != first.UID || f.creates != 1 {
		t.Fatalf("replay made a second execution: uid %q vs %q, creates=%d", second.UID, first.UID, f.creates)
	}
}

func TestResumeReportsWorkerAndUnavailableIsRetryable(t *testing.T) {
	f := newFake()
	c := serve(t, f, "good")
	ctx := context.Background()
	if _, _, err := c.CreateActor(ctx, "a", "run", "tpl"); err != nil {
		t.Fatal(err)
	}

	f.resumeErr = status.Error(codes.ResourceExhausted, "no worker has room for the actor")
	if _, err := c.ResumeActor(ctx, "a", "run"); !IsUnavailable(err) {
		t.Fatalf("pool saturation must be retryable, got %v", err)
	}
	f.resumeErr = nil
	a, err := c.ResumeActor(ctx, "a", "run")
	if err != nil || a.State != StateRunning || a.Worker != "worker-b" || a.Node != "framework" {
		t.Fatalf("resume: %+v err=%v", a, err)
	}
	if IsUnavailable(status.Error(codes.InvalidArgument, "bad")) {
		t.Fatal("a bad request must not be retried")
	}
}

func TestDeleteActorConfirmsAndIsIdempotent(t *testing.T) {
	c := serve(t, newFake(), "good")
	ctx := context.Background()
	if _, _, err := c.CreateActor(ctx, "a", "run", "tpl"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		gone, err := c.DeleteActor(ctx, "a", "run")
		if err != nil || !gone {
			t.Fatalf("delete %d: gone=%v err=%v", i, gone, err)
		}
	}
}

func TestEnsureTemplateRefusesTagsAndReportsGolden(t *testing.T) {
	f := newFake()
	c := serve(t, f, "good")
	ctx := context.Background()

	if _, err := c.EnsureTemplate(ctx, Template{Atespace: "a", Name: "t", Image: "ghcr.io/x/runner:latest"}); err == nil {
		t.Fatal("a tag must be refused before the API is called")
	}
	tpl := Template{Atespace: "a", Name: "t", Image: "ghcr.io/x/runner@sha256:abc", Env: map[string]string{"B": "2", "A": "1"}, WakeupPath: "/healthz", WakeupPort: 8080}
	st, err := c.EnsureTemplate(ctx, tpl)
	if err != nil || st.GoldenReady {
		t.Fatalf("new template has no golden snapshot yet: %+v err=%v", st, err)
	}
	stored := f.templates["a/t"]
	if env := stored.GetContainers()[0].GetEnv(); len(env) != 2 || env[0].GetName() != "A" {
		t.Fatalf("env must be rendered in a stable order: %v", env)
	}
	stored.Status = &ateapi.ActorTemplateStatus{GoldenSnapshotStatus: &ateapi.GoldenSnapshotStatus{GoldenTag: &ateapi.ObjectRef{Atespace: "a", Name: "golden"}}}
	if st, err = c.EnsureTemplate(ctx, tpl); err != nil || !st.GoldenReady {
		t.Fatalf("golden snapshot present: %+v err=%v", st, err)
	}
}

func TestSetEgressIsExactlyTheTargetsWithActorJWT(t *testing.T) {
	f := newFake()
	c := serve(t, f, "good")
	ctx := context.Background()
	targets := []EgressTarget{
		{Hostname: "gateway.sympozium-system.svc.cluster.local", Port: 8443, TLS: true, ActorJWTAudience: "sympozium-model-gateway"},
		{Hostname: "turns.sympozium-system.svc.cluster.local", Port: 8080, ActorJWTAudience: "sympozium-turns"},
	}
	for i := 0; i < 2; i++ { // second call takes the update path
		if err := c.SetEgress(ctx, "a", "run", targets); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	rules := f.policies["a/run"].GetRules()
	if len(rules) != 2 || rules[0].GetHttps() == nil || rules[1].GetHttp() == nil || rules[0].GetTlsPassthrough() != nil {
		t.Fatalf("want one https and one http rule, got %v", rules)
	}
	h := rules[0].GetHttps().GetEffects().GetReplaceHeaders()[0]
	if h.GetCredentialUri() != "" || h.GetActorJwt().GetAudiences()[0] != "sympozium-model-gateway" || h.GetActorJwt().GetExpirationSeconds() != 300 {
		t.Fatalf("the header must carry an actor JWT, never a stored credential: %v", h)
	}
}
