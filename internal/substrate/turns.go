package substrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// The turn endpoint is how a run's work reaches the runner inside an actor
// and how the result comes back (spike 0b, #699). The actor calls out; nothing
// calls in. The only thing that says which actor is calling is the JWT that
// Substrate's egress gateway puts on the request after it has left the
// sandbox, so the actor holds no credential and cannot claim to be another.
//
// Everything in a request body comes from an LLM-driven process and is treated
// as hostile: it is bounded in size, and a result is accepted only for the turn
// that was handed to that same actor.

const (
	TurnPath   = "/v1/actor/turn"
	ResultPath = "/v1/actor/turn/result"

	maxResultBody = 5 << 20
)

// ActorRef names an actor.
type ActorRef struct {
	Atespace string
	Name     string
	// UID is empty when the token did not carry one.
	UID string
}

func (a ActorRef) String() string { return a.Atespace + "/" + a.Name }

// Turn is one unit of work for an actor: an identifier and the environment
// the runner is started with. It must never contain a model key.
type Turn struct {
	ID  string            `json:"id"`
	Env map[string]string `json:"env"`
}

// TurnResult is what an actor reported for a turn.
type TurnResult struct {
	ID       string          `json:"id"`
	ExitCode int             `json:"exitCode"`
	Result   json.RawMessage `json:"result,omitempty"`
	Logs     string          `json:"logs,omitempty"`
}

// ErrNoSuchTurn is returned by Complete when the actor was not handed that turn.
var ErrNoSuchTurn = errors.New("substrate: no such turn for this actor")

// ErrTurnCompleted is returned by Complete when the turn already has a
// result. A result is recorded once; a second post changes nothing.
var ErrTurnCompleted = errors.New("substrate: the turn already has a result")

// TurnStore is where turns wait and results land. The controller backs it
// with AgentRun status; the spike driver uses the in-memory one below.
type TurnStore interface {
	// Claim returns the turn waiting for the actor, if there is one. It is
	// safe to call repeatedly: the same turn is returned until it completes.
	Claim(ctx context.Context, actor ActorRef) (Turn, bool, error)
	// Complete records the result of a turn previously claimed by the actor.
	// It records one result per turn: ErrTurnCompleted for any later post.
	Complete(ctx context.Context, actor ActorRef, result TurnResult) error
}

// Verifier turns a bearer token into the actor it names.
type Verifier interface {
	Verify(ctx context.Context, token string) (ActorRef, error)
}

// JWTVerifier checks actor JWTs minted by Substrate's ate-api-server against
// the keys its identity provider publishes.
type JWTVerifier struct {
	// Issuer and Audience must match the token exactly.
	Issuer   string
	Audience string
	// JWKSURL is the issuer's key set; Client fetches it.
	JWKSURL string
	Client  *http.Client

	mu      sync.Mutex
	keys    *jose.JSONWebKeySet
	fetched time.Time
}

var actorJWTAlgorithms = []jose.SignatureAlgorithm{jose.ES256, jose.ES384, jose.RS256, jose.PS256, jose.EdDSA}

func (v *JWTVerifier) keySet(ctx context.Context, refresh bool) (*jose.JSONWebKeySet, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	// A refresh is how a rotated key is picked up; rate-limit it so tokens
	// with unknown key IDs cannot make us hammer the issuer.
	if v.keys != nil && (!refresh || time.Since(v.fetched) < 10*time.Second) {
		return v.keys, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.JWKSURL, nil)
	if err != nil {
		return nil, err
	}
	client := v.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch actor JWT keys: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch actor JWT keys: status %d", resp.StatusCode)
	}
	var set jose.JSONWebKeySet
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&set); err != nil {
		return nil, fmt.Errorf("decode actor JWT keys: %w", err)
	}
	v.keys, v.fetched = &set, time.Now()
	return v.keys, nil
}

// Verify checks signature, issuer, audience and lifetime, and reads the actor
// from the subject, which Substrate sets to actor/<atespace>/<name>.
func (v *JWTVerifier) Verify(ctx context.Context, token string) (ActorRef, error) {
	// The claims check below skips an expectation that is empty, so an
	// unset issuer or audience would accept any token the keys verify.
	if v.Issuer == "" || v.Audience == "" {
		return ActorRef{}, errors.New("actor JWT verifier has no issuer or audience")
	}
	tok, err := jwt.ParseSigned(token, actorJWTAlgorithms)
	if err != nil {
		return ActorRef{}, fmt.Errorf("not an actor JWT: %w", err)
	}
	var claims jwt.Claims
	// Substrate repeats the actor in its own claim, with the UID that tells
	// two actors of the same name apart.
	var extra struct {
		Actor struct {
			Atespace string `json:"atespace"`
			Name     string `json:"actorName"`
			UID      string `json:"actorUID"`
		} `json:"actor.ate.dev"`
	}
	verify := func(refresh bool) error {
		keys, err := v.keySet(ctx, refresh)
		if err != nil {
			return err
		}
		return tok.Claims(keys, &claims, &extra)
	}
	if err := verify(false); err != nil {
		if err = verify(true); err != nil {
			return ActorRef{}, fmt.Errorf("actor JWT signature: %w", err)
		}
	}
	if err := claims.ValidateWithLeeway(jwt.Expected{Issuer: v.Issuer, AnyAudience: jwt.Audience{v.Audience}, Time: time.Now()}, 30*time.Second); err != nil {
		return ActorRef{}, fmt.Errorf("actor JWT claims: %w", err)
	}
	// A missing exp is not an error to the check above. An actor JWT is
	// short-lived by contract; one that never expires is refused.
	if claims.Expiry == nil {
		return ActorRef{}, errors.New("actor JWT has no expiry")
	}
	parts := strings.Split(claims.Subject, "/")
	if len(parts) != 3 || parts[0] != "actor" || parts[1] == "" || parts[2] == "" {
		return ActorRef{}, fmt.Errorf("actor JWT subject %q is not actor/<atespace>/<name>", claims.Subject)
	}
	if a := extra.Actor; (a.Atespace != "" || a.Name != "") && (a.Atespace != parts[1] || a.Name != parts[2]) {
		return ActorRef{}, fmt.Errorf("actor JWT subject %q does not match its actor claim", claims.Subject)
	}
	return ActorRef{Atespace: parts[1], Name: parts[2], UID: extra.Actor.UID}, nil
}

// VerifyActor is Verify for the harness gateway, which compares the actor's
// name and UID with the actor a run is bound to.
func (v *JWTVerifier) VerifyActor(ctx context.Context, token string) (actor, uid string, err error) {
	ref, err := v.Verify(ctx, token)
	if err != nil {
		return "", "", err
	}
	return ref.String(), ref.UID, nil
}

// TurnHandler serves the turn endpoint.
type TurnHandler struct {
	Verifier Verifier
	Store    TurnStore
}

func (h TurnHandler) actor(w http.ResponseWriter, r *http.Request) (ActorRef, bool) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		http.Error(w, "missing actor credential", http.StatusUnauthorized)
		return ActorRef{}, false
	}
	actor, err := h.Verifier.Verify(r.Context(), token)
	if err != nil {
		// The reason stays in our logs; the caller is the sandbox.
		http.Error(w, "actor credential refused", http.StatusUnauthorized)
		return ActorRef{}, false
	}
	return actor, true
}

func (h TurnHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == TurnPath:
		actor, ok := h.actor(w, r)
		if !ok {
			return
		}
		turn, found, err := h.Store.Claim(r.Context(), actor)
		if err != nil {
			http.Error(w, "turn unavailable", http.StatusServiceUnavailable)
			return
		}
		if !found {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(turn)
	case r.Method == http.MethodPost && r.URL.Path == ResultPath:
		actor, ok := h.actor(w, r)
		if !ok {
			return
		}
		var res TurnResult
		body := http.MaxBytesReader(w, r.Body, maxResultBody)
		if err := json.NewDecoder(body).Decode(&res); err != nil || res.ID == "" {
			http.Error(w, "malformed result", http.StatusBadRequest)
			return
		}
		switch err := h.Store.Complete(r.Context(), actor, res); {
		case errors.Is(err, ErrNoSuchTurn):
			http.Error(w, "no such turn", http.StatusNotFound)
		case errors.Is(err, ErrTurnCompleted):
			// Not retryable: the runner drops the result on a 409.
			http.Error(w, "turn already has a result", http.StatusConflict)
		case err != nil:
			http.Error(w, "result not recorded", http.StatusServiceUnavailable)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	default:
		http.NotFound(w, r)
	}
}

// MemoryTurnStore is a TurnStore for tests and the spike driver.
type MemoryTurnStore struct {
	mu      sync.Mutex
	pending map[string]Turn
	done    map[string]chan TurnResult
	claimed map[string]time.Time
}

// NewMemoryTurnStore returns an empty store.
func NewMemoryTurnStore() *MemoryTurnStore {
	return &MemoryTurnStore{pending: map[string]Turn{}, done: map[string]chan TurnResult{}, claimed: map[string]time.Time{}}
}

// Offer makes a turn available to the actor and returns where its result
// will be delivered.
func (s *MemoryTurnStore) Offer(actor ActorRef, turn Turn) <-chan TurnResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := make(chan TurnResult, 1)
	s.pending[actor.String()] = turn
	s.done[actor.String()] = ch
	return ch
}

// ClaimedAt reports when the actor first asked for its turn.
func (s *MemoryTurnStore) ClaimedAt(actor ActorRef) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.claimed[actor.String()]
}

func (s *MemoryTurnStore) Claim(_ context.Context, actor ActorRef) (Turn, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	turn, ok := s.pending[actor.String()]
	if ok {
		if _, seen := s.claimed[actor.String()]; !seen {
			s.claimed[actor.String()] = time.Now()
		}
	}
	return turn, ok, nil
}

func (s *MemoryTurnStore) Complete(_ context.Context, actor ActorRef, res TurnResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	turn, ok := s.pending[actor.String()]
	if !ok || turn.ID != res.ID {
		return ErrNoSuchTurn
	}
	delete(s.pending, actor.String())
	s.done[actor.String()] <- res
	return nil
}
