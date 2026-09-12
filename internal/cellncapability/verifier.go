package cellncapability

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"

	jose "github.com/go-jose/go-jose/v4"
)

type Verifier struct {
	mu     sync.RWMutex
	issuer string
	clock  Clock
	keys   map[string]ed25519.PublicKey
}

func NewVerifier(issuer string, keys []VerificationKey, clock Clock) (*Verifier, error) {
	if issuer == "" {
		return nil, fmt.Errorf("issuer is required")
	}
	if clock == nil {
		clock = realClock{}
	}
	v := &Verifier{issuer: issuer, clock: clock}
	if err := v.ReloadKeys(keys); err != nil {
		return nil, err
	}
	return v, nil
}

func (v *Verifier) ReloadKeys(keys []VerificationKey) error {
	if len(keys) == 0 {
		return fmt.Errorf("at least one verification key is required")
	}
	next := make(map[string]ed25519.PublicKey, len(keys))
	for _, key := range keys {
		if key.KeyID == "" || len(key.PublicKey) != ed25519.PublicKeySize {
			return fmt.Errorf("invalid Ed25519 verification key %q", key.KeyID)
		}
		if _, exists := next[key.KeyID]; exists {
			return fmt.Errorf("duplicate verification key id %q", key.KeyID)
		}
		next[key.KeyID] = append(ed25519.PublicKey(nil), key.PublicKey...)
	}
	v.mu.Lock()
	v.keys = next
	v.mu.Unlock()
	return nil
}

func (v *Verifier) Verify(token Token, decisionRaw []byte, ctx VerifyContext) (Verified, error) {
	compact := token.Bearer()
	if compact == "" || len(compact) > maxCompactSize {
		return Verified{}, reasonError(ReasonSizeExceeded, nil)
	}
	if len(decisionRaw) == 0 || len(decisionRaw) > maxDecisionSize {
		return Verified{}, reasonError(ReasonSizeExceeded, nil)
	}

	headerRaw, _, err := compactParts(compact)
	if err != nil {
		return Verified{}, classifyStrictError(err)
	}
	if len(headerRaw) > maxHeaderSize {
		return Verified{}, reasonError(ReasonHeaderSize, nil)
	}
	var header struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
		Kid string `json:"kid"`
	}
	if err := strictDecode(headerRaw, &header); err != nil {
		return Verified{}, classifyStrictError(err)
	}
	if header.Alg != string(jose.EdDSA) {
		return Verified{}, reasonError(ReasonAlgUnsupported, nil)
	}
	if header.Typ != CredentialType {
		return Verified{}, reasonError(ReasonTypUnsupported, nil)
	}

	v.mu.RLock()
	publicKey, known := v.keys[header.Kid]
	if known {
		publicKey = append(ed25519.PublicKey(nil), publicKey...)
	}
	v.mu.RUnlock()
	if !known {
		return Verified{}, reasonError(ReasonKidUnknown, nil)
	}

	jws, err := jose.ParseSignedCompact(compact, []jose.SignatureAlgorithm{jose.EdDSA})
	if err != nil {
		return Verified{}, reasonError(ReasonMalformed, err)
	}
	payload, err := jws.Verify(publicKey)
	if err != nil {
		return Verified{}, reasonError(ReasonSigInvalid, err)
	}

	var decision Decision
	if err := strictDecode(decisionRaw, &decision); err != nil {
		return Verified{}, reasonError(ReasonMalformed, err)
	}
	if reason := validateDecision(decision); reason != "" {
		return Verified{}, reasonError(reason, nil)
	}
	_, decisionDigest, err := CanonicalDecision(decision)
	if err != nil {
		return Verified{}, reasonError(ReasonMalformed, err)
	}

	var claims Claims
	if err := strictDecode(payload, &claims); err != nil {
		return Verified{}, classifyStrictError(err)
	}
	if claims.APIVersion != CredentialAPIVersion {
		return Verified{}, reasonError(ReasonVersionUnsupported, nil)
	}
	if claims.DecisionDigest != decisionDigest {
		return Verified{}, reasonError(ReasonDigestMismatch, nil)
	}
	if claims.Iss != v.issuer {
		return Verified{}, reasonError(ReasonIssMismatch, nil)
	}
	if claims.Aud != ctx.ExpectedAudience || requiredAudience(ctx.ExpectedOperation) != ctx.ExpectedAudience {
		return Verified{}, reasonError(ReasonAudMismatch, nil)
	}
	if claims.Operation != ctx.ExpectedOperation || !allowedByDecision(decision, claims.Operation) {
		return Verified{}, reasonError(ReasonOperationMismatch, nil)
	}
	if claims.BudgetID != decision.Budget.BudgetID || ctx.BudgetID != decision.Budget.BudgetID {
		return Verified{}, reasonError(ReasonBudgetMismatch, nil)
	}
	if claims.Subject.RunUID != decision.Run.UID ||
		!sameStringPtr(claims.Subject.TurnID, parentTurnID(decision.Parent)) ||
		!sameStringPtr(claims.Subject.ParentIncarnation, parentIncarnation(decision.Parent)) {
		return Verified{}, reasonError(ReasonSubjectMismatch, nil)
	}

	now := ctx.Now
	if now == 0 {
		now = v.clock.Now().UTC().Unix()
	}
	if claims.Nbf > now+clockSkewSeconds || claims.Iat > now+clockSkewSeconds {
		return Verified{}, reasonError(ReasonTimeNotYetValid, nil)
	}
	if claims.Exp <= now-clockSkewSeconds {
		return Verified{}, reasonError(ReasonTimeExpired, nil)
	}
	if claims.Iat < decision.Windows.IssuedAt-clockSkewSeconds {
		return Verified{}, reasonError(ReasonWindowInvalid, nil)
	}

	if decision.ClusterID != ctx.ClusterID {
		return Verified{}, reasonError(ReasonSubjectMismatch, nil)
	}
	if decision.Run.Namespace != ctx.Namespace || decision.Run.NamespaceUID != ctx.NamespaceUID {
		return Verified{}, reasonError(ReasonNamespaceMismatch, nil)
	}
	if decision.Run.UID != ctx.RunUID || decision.Run.SpecSHA256 != ctx.RunSpecSHA256 {
		return Verified{}, reasonError(ReasonRunMismatch, nil)
	}
	if !sameParent(decision.Parent, ctx.Parent) {
		return Verified{}, reasonError(ReasonParentTurn, nil)
	}

	result := Verified{Claims: claims, DecisionDigest: decisionDigest}
	switch ctx.ExpectedOperation {
	case "execution.start", "execution.turn":
		if now > decision.Windows.AdmissionDeadline+clockSkewSeconds {
			return Verified{}, reasonError(ReasonAdmissionExpired, nil)
		}
		if claims.Exp > decision.Windows.AdmissionDeadline+clockSkewSeconds {
			return Verified{}, reasonError(ReasonWindowInvalid, nil)
		}
		if decision.RequestDigest != ctx.RequestDigest {
			return Verified{}, reasonError(ReasonRequestBinding, nil)
		}
		if ctx.SeenAdmissionJTI {
			result.Recovered = true
			result.Reason = ReasonAdmissionReplay
		}
	case "model.invoke":
		if now > decision.Budget.TurnDeadlineUnix+clockSkewSeconds {
			return Verified{}, reasonError(ReasonDeadlineExpired, nil)
		}
		if claims.Exp > decision.Budget.TurnDeadlineUnix+clockSkewSeconds {
			return Verified{}, reasonError(ReasonWindowInvalid, nil)
		}
		if !sameRoute(decision.Route, ctx.Route) {
			return Verified{}, reasonError(ReasonRouteMismatch, nil)
		}
	case "execution.read", "execution.cleanup":
		if claims.Exp-claims.Iat > ownerCredentialSeconds {
			return Verified{}, reasonError(ReasonWindowInvalid, nil)
		}
	default:
		return Verified{}, reasonError(ReasonOperationMismatch, nil)
	}
	return result, nil
}

func compactParts(compact string) (header, payload []byte, err error) {
	parts := strings.Split(compact, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return nil, nil, fmt.Errorf("compact JWS must contain three non-empty segments")
	}
	header, err = base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, nil, err
	}
	payload, err = base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, nil, err
	}
	if _, err := base64.RawURLEncoding.DecodeString(parts[2]); err != nil {
		return nil, nil, err
	}
	if err := checkStrictJSON(header); err != nil {
		return nil, nil, err
	}
	if err := checkStrictJSON(payload); err != nil {
		return nil, nil, err
	}
	return header, payload, nil
}

func classifyStrictError(err error) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	if strings.Contains(message, "duplicate JSON key") {
		return reasonError(ReasonDuplicateJSONKey, err)
	}
	if strings.Contains(message, "unknown field") {
		return reasonError(ReasonUnknownField, err)
	}
	return reasonError(ReasonMalformed, err)
}

func requiredAudience(operation string) string {
	if operation == "model.invoke" {
		return AudienceModelGateway
	}
	switch operation {
	case "execution.start", "execution.turn", "execution.read", "execution.cleanup":
		return AudienceExecution
	default:
		return ""
	}
}

func allowedByDecision(decision Decision, operation string) bool {
	if operation == decision.Operation {
		return true
	}
	return operation == "model.invoke" &&
		(decision.Operation == "execution.start" || decision.Operation == "execution.turn") &&
		decision.Route.Provider != "none"
}

func validateDecision(decision Decision) string {
	if decision.APIVersion != DecisionAPIVersion {
		return ReasonVersionUnsupported
	}
	if decision.ClusterID == "" {
		return ReasonLifecycleInvalid
	}
	if decision.Windows.AdmissionDeadline < decision.Windows.IssuedAt ||
		decision.Windows.AdmissionDeadline-decision.Windows.IssuedAt > admissionWindowSeconds {
		return ReasonWindowInvalid
	}
	if decision.Windows.NotBefore < decision.Windows.IssuedAt-clockSkewSeconds ||
		decision.Windows.NotBefore > decision.Windows.AdmissionDeadline {
		return ReasonWindowInvalid
	}
	if decision.Budget.TurnCap.Requests > decision.Budget.RunCap.Requests ||
		decision.Budget.TurnCap.OutputTokens > decision.Budget.RunCap.OutputTokens {
		return ReasonBudgetMismatch
	}
	if decision.Lifecycle == "one-shot" {
		if decision.Budget.MaxTurns != 1 || decision.Budget.ParentDeadlineUnix != 0 {
			return ReasonLifecycleInvalid
		}
	} else {
		if decision.Budget.MaxTurns < 1 || decision.Budget.ParentDeadlineUnix < decision.Budget.TurnDeadlineUnix {
			return ReasonLifecycleInvalid
		}
	}
	if decision.Budget.TurnDeadlineUnix <= decision.Windows.IssuedAt {
		return ReasonLifecycleInvalid
	}
	for _, tool := range decision.Tools {
		if !validLimits(tool.Limits) {
			return ReasonLimitRange
		}
	}
	if decision.Route.Provider == "none" {
		if decision.Route.Auth != "none" || decision.Route.CredentialSource != nil || decision.Route.ModelConnectionUID != nil {
			return ReasonRouteMismatch
		}
		return ""
	}
	if decision.Route.Streaming {
		return ReasonStreaming
	}
	if decision.Route.Protocol != "openai-chat" && decision.Route.Protocol != "anthropic-messages" {
		return ReasonProtocol
	}
	switch decision.Route.Auth {
	case "secret":
		if decision.Route.CredentialSource == nil {
			return ReasonRouteMismatch
		}
	case "none":
		if decision.Route.CredentialSource != nil {
			return ReasonRouteMismatch
		}
	default:
		return ReasonRouteMismatch
	}
	return ""
}

func validLimits(limits Limits) bool {
	if limits.TimeoutMillis < 1 || limits.TimeoutMillis > 300000 ||
		limits.MemoryBytes < 1 || limits.MemoryBytes > 268435456 ||
		limits.ArgumentBytes < 1 || limits.ArgumentBytes > 65536 ||
		limits.OutputBytes < 1 || limits.OutputBytes > 65536 ||
		limits.Workspace != "none" ||
		(limits.Effects != "none" && limits.Effects != "external-side-effects") {
		return false
	}
	if limits.Artifacts != nil && (limits.Artifacts.MaxOperations < 1 || limits.Artifacts.MaxFiles < 1 ||
		limits.Artifacts.MaxFileBytes < 1 || limits.Artifacts.MaxTotalBytes < 1) {
		return false
	}
	if limits.HTTPS != nil && (len(limits.HTTPS.AllowHosts) == 0 || limits.HTTPS.MaxRequests < 1 ||
		limits.HTTPS.MaxResponseBytes < 1 || limits.HTTPS.TimeoutMillis < 1) {
		return false
	}
	return true
}

func sameStringPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
func parentTurnID(parent *ParentBinding) *string {
	if parent == nil {
		return nil
	}
	return parent.TurnID
}
func parentIncarnation(parent *ParentBinding) *string {
	if parent == nil {
		return nil
	}
	return &parent.Incarnation
}
func sameParent(a, b *ParentBinding) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Incarnation == b.Incarnation && sameStringPtr(a.TurnID, b.TurnID)
}
func sameRoute(a, b RouteBinding) bool {
	return sameStringPtr(a.ModelConnectionUID, b.ModelConnectionUID) &&
		a.ModelConnectionSpecSHA256 == b.ModelConnectionSpecSHA256 &&
		a.Provider == b.Provider && a.Protocol == b.Protocol && a.Model == b.Model &&
		a.EndpointOrigin == b.EndpointOrigin && a.Auth == b.Auth &&
		a.Streaming == b.Streaming && sameCredentialSource(a.CredentialSource, b.CredentialSource)
}
func sameCredentialSource(a, b *CredentialSource) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
