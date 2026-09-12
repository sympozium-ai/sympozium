package cellncapability

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

type Clock interface {
	Now() time.Time
}

type ClockFunc func() time.Time

func (f ClockFunc) Now() time.Time { return f() }

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

type SigningKey struct {
	KeyID      string
	PrivateKey ed25519.PrivateKey
}

type VerificationKey struct {
	KeyID     string
	PublicKey ed25519.PublicKey
}

type IssueRequest struct {
	Audience  string
	Operation string
	ExpiresAt time.Time
	JTI       string
}

type Issuer struct {
	mu     sync.RWMutex
	issuer string
	clock  Clock
	key    SigningKey
}

func NewIssuer(issuer string, key SigningKey, clock Clock) (*Issuer, error) {
	if issuer == "" {
		return nil, fmt.Errorf("issuer is required")
	}
	if err := validateSigningKey(key); err != nil {
		return nil, err
	}
	if clock == nil {
		clock = realClock{}
	}
	return &Issuer{issuer: issuer, key: cloneSigningKey(key), clock: clock}, nil
}

// Rotate atomically replaces the active signing key. Verifier key overlap is
// managed independently so an old public key can remain trusted until every
// credential it signed has expired.
func (i *Issuer) Rotate(key SigningKey) error {
	if err := validateSigningKey(key); err != nil {
		return err
	}
	i.mu.Lock()
	i.key = cloneSigningKey(key)
	i.mu.Unlock()
	return nil
}

func (i *Issuer) Issue(decision Decision, req IssueRequest) (Token, error) {
	if reason := validateDecision(decision); reason != "" {
		return Token{}, reasonError(reason, nil)
	}
	if requiredAudience(req.Operation) != req.Audience || !allowedByDecision(decision, req.Operation) {
		return Token{}, reasonError(ReasonOperationMismatch, nil)
	}

	now := i.clock.Now().UTC()
	nowUnix := now.Unix()
	maxExpiry, reason := maximumExpiry(decision, req.Operation, nowUnix)
	if reason != "" {
		return Token{}, reasonError(reason, nil)
	}
	expiry := req.ExpiresAt.UTC()
	if req.ExpiresAt.IsZero() {
		expiry = time.Unix(maxExpiry, 0).UTC()
	}
	if expiry.Unix() <= nowUnix-clockSkewSeconds || expiry.Unix() > maxExpiry {
		return Token{}, reasonError(ReasonWindowInvalid, nil)
	}

	jti := req.JTI
	if jti == "" {
		var err error
		jti, err = randomJTI()
		if err != nil {
			return Token{}, fmt.Errorf("generate capability id: %w", err)
		}
	}

	_, decisionDigest, err := CanonicalDecision(decision)
	if err != nil {
		return Token{}, reasonError(ReasonMalformed, err)
	}
	claims := Claims{
		APIVersion:     CredentialAPIVersion,
		Iss:            i.issuer,
		Aud:            req.Audience,
		Iat:            nowUnix,
		Nbf:            nowUnix,
		Exp:            expiry.Unix(),
		Jti:            jti,
		DecisionDigest: decisionDigest,
		BudgetID:       decision.Budget.BudgetID,
		Operation:      req.Operation,
		Subject: ClaimSubject{
			RunUID:            decision.Run.UID,
			TurnID:            parentTurnID(decision.Parent),
			ParentIncarnation: parentIncarnation(decision.Parent),
		},
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return Token{}, err
	}
	canonicalPayload, err := canonicalizeJSON(payload)
	if err != nil {
		return Token{}, reasonError(ReasonMalformed, err)
	}

	i.mu.RLock()
	key := cloneSigningKey(i.key)
	i.mu.RUnlock()
	jwk := jose.JSONWebKey{Key: key.PrivateKey, KeyID: key.KeyID, Algorithm: string(jose.EdDSA), Use: "sig"}
	opts := (&jose.SignerOptions{}).WithType(jose.ContentType(CredentialType))
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.EdDSA, Key: jwk}, opts)
	if err != nil {
		return Token{}, fmt.Errorf("create capability signer: %w", err)
	}
	object, err := signer.Sign(canonicalPayload)
	if err != nil {
		return Token{}, fmt.Errorf("sign capability: %w", err)
	}
	compact, err := object.CompactSerialize()
	if err != nil {
		return Token{}, fmt.Errorf("serialize capability: %w", err)
	}
	if len(compact) > maxCompactSize {
		return Token{}, reasonError(ReasonSizeExceeded, nil)
	}
	return NewToken(compact), nil
}

func validateSigningKey(key SigningKey) error {
	if key.KeyID == "" {
		return fmt.Errorf("signing key id is required")
	}
	if len(key.PrivateKey) != ed25519.PrivateKeySize {
		return fmt.Errorf("signing key %q must be an Ed25519 private key", key.KeyID)
	}
	return nil
}

func cloneSigningKey(key SigningKey) SigningKey {
	copyKey := append(ed25519.PrivateKey(nil), key.PrivateKey...)
	return SigningKey{KeyID: key.KeyID, PrivateKey: copyKey}
}

func randomJTI() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func maximumExpiry(decision Decision, operation string, now int64) (int64, string) {
	switch operation {
	case "execution.start", "execution.turn":
		if now > decision.Windows.AdmissionDeadline+clockSkewSeconds {
			return 0, ReasonAdmissionExpired
		}
		return decision.Windows.AdmissionDeadline + clockSkewSeconds, ""
	case "model.invoke":
		if now > decision.Budget.TurnDeadlineUnix+clockSkewSeconds {
			return 0, ReasonDeadlineExpired
		}
		return decision.Budget.TurnDeadlineUnix + clockSkewSeconds, ""
	case "execution.read", "execution.cleanup":
		return now + ownerCredentialSeconds, ""
	default:
		return 0, ReasonOperationMismatch
	}
}
