// Package substrate is the controller's client for Agent Substrate's public
// Control API (spike 0b, #699). It speaks the vendored proto in
// third_party/substrate directly: the controller cannot import Substrate's Go
// module (k8s.io v0.37, controller-runtime v0.24, Go 1.27).
//
// The surface is deliberately the one-shot run lifecycle and nothing else:
// atespace, template, actor create/resume/suspend/delete, egress policy.
// Every mutating call is idempotent by name, because Substrate resources carry
// no labels and the run's identity is encoded in the actor name.
package substrate

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	ateapi "github.com/sympozium-ai/sympozium/third_party/substrate/ateapi/v1"
)

// TokenSource returns the bearer token for one RPC. The controller passes a
// function that re-reads its projected ServiceAccount token (audience
// api.ate-system.svc), so kubelet rotation is picked up without a reconnect.
type TokenSource func(ctx context.Context) (string, error)

// Config is how to reach ate-api-server.
type Config struct {
	// Endpoint is host:port of the ate-api-server Service.
	Endpoint string
	// RootCAs verifies the serving certificate; in a cluster it is the
	// ClusterTrustBundle Substrate's pod-certificate signer publishes.
	RootCAs *x509.CertPool
	// ServerName overrides the name checked in the serving certificate, for
	// callers that reach the server through a port-forward.
	ServerName string
	// Token supplies the bearer token. Required: a call without a credential
	// is refused as Unauthenticated.
	Token TokenSource
}

// Client is a connection to the Control API.
type Client struct {
	conn *grpc.ClientConn
	api  ateapi.ControlClient
}

type bearer struct{ token TokenSource }

func (b bearer) GetRequestMetadata(ctx context.Context, _ ...string) (map[string]string, error) {
	t, err := b.token(ctx)
	if err != nil {
		return nil, fmt.Errorf("substrate token: %w", err)
	}
	return map[string]string{"authorization": "Bearer " + strings.TrimSpace(t)}, nil
}

// RequireTransportSecurity keeps the token off plaintext connections.
func (bearer) RequireTransportSecurity() bool { return true }

// Dial opens a client. It does not block on connectivity; the first RPC does.
func Dial(cfg Config, extra ...grpc.DialOption) (*Client, error) {
	if cfg.Endpoint == "" {
		return nil, errors.New("substrate: endpoint is required")
	}
	if cfg.Token == nil {
		return nil, errors.New("substrate: token source is required")
	}
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: cfg.RootCAs, ServerName: cfg.ServerName}
	opts := append([]grpc.DialOption{
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
		grpc.WithPerRPCCredentials(bearer{cfg.Token}),
	}, extra...)
	conn, err := grpc.NewClient(cfg.Endpoint, opts...)
	if err != nil {
		return nil, fmt.Errorf("substrate: dial %s: %w", cfg.Endpoint, err)
	}
	return &Client{conn: conn, api: ateapi.NewControlClient(conn)}, nil
}

// Close releases the connection.
func (c *Client) Close() error { return c.conn.Close() }

// IsUnavailable reports whether err means "try again later" rather than "this
// request is wrong": the control plane is restarting, or no worker has room.
// A backend maps it to a requeue, never to a failed run.
func IsUnavailable(err error) bool {
	switch status.Code(err) {
	case codes.Unavailable, codes.ResourceExhausted, codes.Aborted, codes.DeadlineExceeded:
		return true
	}
	return false
}

// IsNotFound reports whether the named resource does not exist.
func IsNotFound(err error) bool { return status.Code(err) == codes.NotFound }

func ref(atespace, name string) *ateapi.ObjectRef {
	return &ateapi.ObjectRef{Atespace: atespace, Name: name}
}

// EnsureAtespace creates the atespace if it is missing.
func (c *Client) EnsureAtespace(ctx context.Context, name string) error {
	_, err := c.api.CreateAtespace(ctx, &ateapi.CreateAtespaceRequest{
		Atespace: &ateapi.Atespace{Metadata: &ateapi.ResourceMetadata{Name: name}},
	})
	if status.Code(err) == codes.AlreadyExists {
		return nil
	}
	return err
}

// Template describes the ActorTemplate one runtime image digest maps to.
// Templates are immutable and shared by every run of that digest, so nothing
// run-specific (task, model, tools, tokens) may appear here.
type Template struct {
	Atespace string
	Name     string
	// Image must be pinned by digest; Substrate refuses a tag because a
	// changed image would invalidate the golden snapshot.
	Image          string
	Command        []string
	Args           []string
	Env            map[string]string
	WakeupPath     string
	WakeupPort     int32
	CPUMillicores  int64
	MemoryBytes    int64
	WorkerSelector map[string]string
}

// TemplateStatus is what a caller needs to know before creating actors.
type TemplateStatus struct {
	// GoldenReady is true once the golden snapshot exists; actors created
	// before that cannot resume.
	GoldenReady bool
	// GoldenError is Substrate's message when the golden snapshot failed.
	GoldenError string
}

// EnsureTemplate creates the template if it is missing and reports the state
// of its golden snapshot. An existing template is never compared or updated:
// the name is derived from the image digest, so same name means same content.
func (c *Client) EnsureTemplate(ctx context.Context, t Template) (TemplateStatus, error) {
	if !strings.Contains(t.Image, "@sha256:") {
		return TemplateStatus{}, fmt.Errorf("substrate: template image %q is not pinned by digest", t.Image)
	}
	got, err := c.api.GetActorTemplate(ctx, &ateapi.GetActorTemplateRequest{ActorTemplate: ref(t.Atespace, t.Name)})
	if IsNotFound(err) {
		got, err = c.api.CreateActorTemplate(ctx, &ateapi.CreateActorTemplateRequest{ActorTemplate: t.proto()})
		if status.Code(err) == codes.AlreadyExists {
			got, err = c.api.GetActorTemplate(ctx, &ateapi.GetActorTemplateRequest{ActorTemplate: ref(t.Atespace, t.Name)})
		}
	}
	if err != nil {
		return TemplateStatus{}, err
	}
	g := got.GetStatus().GetGoldenSnapshotStatus()
	return TemplateStatus{
		GoldenReady: g.GetGoldenTag().GetName() != "" && g.GetErrorMessage() == "",
		GoldenError: g.GetErrorMessage(),
	}, nil
}

func (t Template) proto() *ateapi.ActorTemplate {
	container := &ateapi.Container{Name: "runner", Image: t.Image, Command: t.Command, Args: t.Args}
	for _, k := range sortedKeys(t.Env) {
		container.Env = append(container.Env, &ateapi.EnvVar{Name: k, Value: t.Env[k]})
	}
	if t.WakeupPort != 0 {
		container.WakeupProbe = &ateapi.WakeupProbe{HttpGet: &ateapi.HttpGetAction{Path: t.WakeupPath, Port: t.WakeupPort}}
	}
	out := &ateapi.ActorTemplate{
		Metadata:   &ateapi.ResourceMetadata{Atespace: t.Atespace, Name: t.Name},
		Containers: []*ateapi.Container{container},
	}
	if len(t.WorkerSelector) > 0 {
		out.WorkerSelector = &ateapi.Selector{MatchLabels: t.WorkerSelector}
	}
	if t.CPUMillicores != 0 || t.MemoryBytes != 0 {
		out.Resources = &ateapi.ResourceRequirements{Limits: &ateapi.Resources{CpuMillicores: t.CPUMillicores, MemoryBytes: t.MemoryBytes}}
	}
	return out
}

// Actor is the backend-neutral view of one actor.
type Actor struct {
	Atespace string
	Name     string
	UID      string
	State    State
	// Worker is the pod currently holding the actor; empty while suspended.
	// It changes between resumes, so attribution must key on the actor.
	Worker       string
	Node         string
	CrashMessage string
}

// State is the actor lifecycle state, reduced to what a backend acts on.
type State string

const (
	StateSuspended State = "Suspended"
	StateRunning   State = "Running"
	// StateTransition covers resuming, suspending, reverting and deleting:
	// wait and look again.
	StateTransition State = "Transition"
	StateCrashed    State = "Crashed"
	StateUnknown    State = "Unknown"
)

func actorFrom(a *ateapi.Actor) Actor {
	out := Actor{
		Atespace:     a.GetMetadata().GetAtespace(),
		Name:         a.GetMetadata().GetName(),
		UID:          a.GetMetadata().GetUid(),
		Worker:       a.GetStatus().GetWorkerAssignment().GetWorkerPod(),
		Node:         a.GetStatus().GetWorkerAssignment().GetNodeName(),
		CrashMessage: a.GetStatus().GetCrash().GetMessage(),
	}
	switch a.GetStatus().GetState() {
	case ateapi.ActorState_ACTOR_STATE_SUSPENDED:
		out.State = StateSuspended
	case ateapi.ActorState_ACTOR_STATE_RUNNING:
		out.State = StateRunning
	case ateapi.ActorState_ACTOR_STATE_CRASHED:
		out.State = StateCrashed
	case ateapi.ActorState_ACTOR_STATE_RESUMING, ateapi.ActorState_ACTOR_STATE_SUSPENDING,
		ateapi.ActorState_ACTOR_STATE_REVERTING, ateapi.ActorState_ACTOR_STATE_DELETING:
		out.State = StateTransition
	default:
		out.State = StateUnknown
	}
	return out
}

// CreateActor creates the named actor from a template, or returns the
// existing one. The name is the idempotency key: a replayed reconcile gets the
// actor the first attempt created and never a second execution.
func (c *Client) CreateActor(ctx context.Context, atespace, name, template string) (Actor, bool, error) {
	a, err := c.api.CreateActor(ctx, &ateapi.CreateActorRequest{Actor: &ateapi.Actor{
		Metadata:      &ateapi.ResourceMetadata{Atespace: atespace, Name: name},
		ActorTemplate: ref(atespace, template),
	}})
	if status.Code(err) == codes.AlreadyExists {
		existing, gerr := c.GetActor(ctx, atespace, name)
		return existing, false, gerr
	}
	if err != nil {
		return Actor{}, false, err
	}
	return actorFrom(a), true, nil
}

// GetActor reads one actor.
func (c *Client) GetActor(ctx context.Context, atespace, name string) (Actor, error) {
	a, err := c.api.GetActor(ctx, &ateapi.GetActorRequest{Actor: ref(atespace, name)})
	if err != nil {
		return Actor{}, err
	}
	return actorFrom(a), nil
}

// ListActors returns every actor in an atespace. One call covers all of a
// namespace's runs, which is how a backend observes thousands of actors
// without one request per run.
func (c *Client) ListActors(ctx context.Context, atespace string) ([]Actor, error) {
	var out []Actor
	opts := &ateapi.ListOptions{PageSize: 500}
	for {
		page, err := c.api.ListActors(ctx, &ateapi.ListActorsRequest{Atespace: atespace, Options: opts})
		if err != nil {
			return nil, err
		}
		for _, a := range page.GetActors() {
			out = append(out, actorFrom(a))
		}
		if page.GetNextPageToken() == "" {
			return out, nil
		}
		opts = &ateapi.ListOptions{PageSize: 500, PageToken: page.GetNextPageToken()}
	}
}

// ResumeActor assigns the actor a worker and restores it. Resuming a running
// actor is a no-op.
func (c *Client) ResumeActor(ctx context.Context, atespace, name string) (Actor, error) {
	r, err := c.api.ResumeActor(ctx, &ateapi.ResumeActorRequest{Actor: ref(atespace, name)})
	if err != nil {
		return Actor{}, err
	}
	return actorFrom(r.GetActor()), nil
}

// SuspendActor snapshots the actor and frees its worker. With durable set it
// returns only once the snapshot is in storage.
func (c *Client) SuspendActor(ctx context.Context, atespace, name string, durable bool) (Actor, error) {
	mode := ateapi.DurabilityMode_DURABILITY_MODE_ASYNCHRONOUS
	if durable {
		mode = ateapi.DurabilityMode_DURABILITY_MODE_SYNCHRONOUS
	}
	r, err := c.api.SuspendActor(ctx, &ateapi.SuspendActorRequest{Actor: ref(atespace, name), Options: &ateapi.SuspendOptions{Mode: mode}})
	if err != nil {
		return Actor{}, err
	}
	return actorFrom(r.GetActor()), nil
}

// DeleteActor removes the actor. It reports true once the actor is gone, so
// a caller can use it as a confirmed, idempotent release.
func (c *Client) DeleteActor(ctx context.Context, atespace, name string) (bool, error) {
	_, err := c.api.DeleteActor(ctx, &ateapi.DeleteActorRequest{Actor: ref(atespace, name)})
	if IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := c.GetActor(ctx, atespace, name); IsNotFound(err) {
		return true, nil
	}
	return false, nil
}

// EgressTarget is one place an actor may reach.
type EgressTarget struct {
	Hostname string
	Port     int32
	// TLS selects an https rule; false is cleartext HTTP, which is what
	// in-cluster Services speak.
	TLS bool
	// ActorJWTAudience, when set, makes the egress gateway replace the
	// request's Authorization header with a short-lived JWT naming the actor,
	// minted for this audience. The receiver learns which run is calling and
	// no credential lives inside the sandbox.
	ActorJWTAudience string
	// JWTHeaders are the request headers that get the JWT. Empty means
	// Authorization with a Bearer prefix. A header is only replaced on a
	// request that already carries it.
	JWTHeaders []JWTHeader
}

// JWTHeader is one header the egress gateway replaces with the actor's JWT.
type JWTHeader struct {
	Name   string
	Prefix string
}

// actorJWTLifetimeSeconds is the shortest lifetime Substrate accepts.
const actorJWTLifetimeSeconds = 300

// SetEgress replaces the actor's egress policy with exactly these targets.
// Egress is default-deny, so this is the complete set of places the actor can
// reach. It must be called before the actor is first resumed.
func (c *Client) SetEgress(ctx context.Context, atespace, actor string, targets []EgressTarget) error {
	// An actor has one policy; Substrate requires it to be named "default".
	policy := &ateapi.EgressPolicy{Metadata: &ateapi.ResourceMetadata{Atespace: atespace, Name: "default"}}
	for _, t := range targets {
		var effects *ateapi.HttpRuleEffects
		if t.ActorJWTAudience != "" {
			headers := t.JWTHeaders
			if len(headers) == 0 {
				headers = []JWTHeader{{Name: "Authorization", Prefix: "Bearer "}}
			}
			effects = &ateapi.HttpRuleEffects{}
			seen := map[string]bool{}
			for _, h := range headers {
				// Substrate refuses duplicate header names, compared without case.
				if key := strings.ToLower(h.Name); !seen[key] {
					seen[key] = true
					effects.ReplaceHeaders = append(effects.ReplaceHeaders, &ateapi.CredentialHeader{
						Header: h.Name, Prefix: h.Prefix,
						ActorJwt: &ateapi.ActorJwtSource{Audiences: []string{t.ActorJWTAudience}, ExpirationSeconds: actorJWTLifetimeSeconds},
					})
				}
			}
		}
		hosts, ports := []string{t.Hostname}, &ateapi.Ports{Numbers: []int32{t.Port}}
		if t.TLS {
			policy.Rules = append(policy.Rules, &ateapi.EgressRule{Https: &ateapi.HttpsRule{Hostnames: hosts, Ports: ports, Effects: effects}})
		} else {
			policy.Rules = append(policy.Rules, &ateapi.EgressRule{Http: &ateapi.HttpRule{Hostnames: hosts, Ports: ports, Effects: effects}})
		}
	}
	_, err := c.api.CreateActorEgressPolicy(ctx, &ateapi.CreateActorEgressPolicyRequest{Actor: ref(atespace, actor), EgressPolicy: policy})
	if status.Code(err) == codes.AlreadyExists {
		_, err = c.api.UpdateActorEgressPolicy(ctx, &ateapi.UpdateActorEgressPolicyRequest{Actor: ref(atespace, actor), EgressPolicy: policy})
	}
	return err
}
