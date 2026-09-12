package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
)

type parsedJWS struct{ headerRaw, payloadRaw, signingInput, signature []byte }

func parseCompact(compact string) (*parsedJWS, error) {
	p := bytes.Split([]byte(compact), []byte("."))
	if len(p) != 3 || len(p[0]) == 0 || len(p[1]) == 0 || len(p[2]) == 0 {
		return nil, fmt.Errorf("bad compact JWS")
	}
	dec := func(b []byte) ([]byte, error) { return base64.RawURLEncoding.DecodeString(string(b)) }
	h, e := dec(p[0])
	if e != nil {
		return nil, e
	}
	pl, e := dec(p[1])
	if e != nil {
		return nil, e
	}
	s, e := dec(p[2])
	if e != nil {
		return nil, e
	}
	if e = checkStrictJSON(h); e != nil {
		return nil, e
	}
	if e = checkStrictJSON(pl); e != nil {
		return nil, e
	}
	si := append(append(append([]byte{}, p[0]...), '.'), p[1]...)
	return &parsedJWS{h, pl, si, s}, nil
}
func verifyEd25519(kid string, input, sig []byte) (bool, error) {
	for _, k := range activeJWKS.Keys {
		if k.Kid != kid {
			continue
		}
		pub, err := base64.RawURLEncoding.DecodeString(k.X)
		if err != nil || len(pub) != ed25519.PublicKeySize {
			return false, fmt.Errorf("bad jwk")
		}
		return ed25519.Verify(ed25519.PublicKey(pub), input, sig), nil
	}
	return false, nil
}

func requiredAudience(op string) string {
	if op == "model.invoke" {
		return audModel
	}
	switch op {
	case "execution.start", "execution.turn", "execution.read", "execution.cleanup":
		return audExecution
	}
	return ""
}
func allowedByDecision(d Decision, op string) bool {
	switch op {
	case d.Operation:
		return true
	case "model.invoke":
		return (d.Operation == "execution.start" || d.Operation == "execution.turn") && d.Route.Provider != "none"
	}
	return false
}
func parentTurnID(p *ParentBinding) *string {
	if p == nil {
		return nil
	}
	return p.TurnID
}
func parentIncarnation(p *ParentBinding) *string {
	if p == nil {
		return nil
	}
	return &p.Incarnation
}
func samePtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
func sameParent(a, b *ParentBinding) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Incarnation == b.Incarnation && samePtr(a.TurnID, b.TurnID)
}
func sameRoute(a, b RouteBinding) bool {
	return samePtr(a.ModelConnectionUID, b.ModelConnectionUID) && a.ModelConnectionSpecSHA256 == b.ModelConnectionSpecSHA256 && a.Provider == b.Provider && a.Protocol == b.Protocol && a.Model == b.Model && a.EndpointOrigin == b.EndpointOrigin && a.Auth == b.Auth && a.Streaming == b.Streaming && sameCredSource(a.CredentialSource, b.CredentialSource)
}
func sameCredSource(a, b *CredentialSource) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
func limitsWithin(a, b Limits) bool {
	return a.TimeoutMillis <= b.TimeoutMillis && a.MemoryBytes <= b.MemoryBytes && a.ArgumentBytes <= b.ArgumentBytes && a.OutputBytes <= b.OutputBytes && a.Workspace == b.Workspace && a.Effects == b.Effects && artifactWithin(a.Artifacts, b.Artifacts) && httpsWithin(a.HTTPS, b.HTTPS)
}
func artifactWithin(a, b *ArtifactLimits) bool {
	if a == nil {
		return true
	}
	if b == nil || a.Operation != b.Operation {
		return false
	}
	return a.MaxOperations <= b.MaxOperations && a.MaxFiles <= b.MaxFiles && a.MaxFileBytes <= b.MaxFileBytes && a.MaxTotalBytes <= b.MaxTotalBytes
}
func httpsWithin(a, b *HTTPSLimits) bool {
	if a == nil {
		return true
	}
	if b == nil || a.MaxRequests > b.MaxRequests || a.MaxResponseBytes > b.MaxResponseBytes || a.TimeoutMillis > b.TimeoutMillis {
		return false
	}
	allowed := map[string]bool{}
	for _, h := range b.AllowHosts {
		allowed[h] = true
	}
	for _, h := range a.AllowHosts {
		if !allowed[h] {
			return false
		}
	}
	return true
}
func validLimits(l Limits) bool {
	if l.TimeoutMillis < 1 || l.TimeoutMillis > 300000 || l.MemoryBytes < 1 || l.MemoryBytes > 268435456 || l.ArgumentBytes < 1 || l.ArgumentBytes > 65536 || l.OutputBytes < 1 || l.OutputBytes > 65536 || l.Workspace != "none" || (l.Effects != "none" && l.Effects != "external-side-effects") {
		return false
	}
	if l.Artifacts != nil && (l.Artifacts.MaxOperations < 1 || l.Artifacts.MaxFiles < 1 || l.Artifacts.MaxFileBytes < 1 || l.Artifacts.MaxTotalBytes < 1) {
		return false
	}
	if l.HTTPS != nil && (len(l.HTTPS.AllowHosts) == 0 || l.HTTPS.MaxRequests < 1 || l.HTTPS.MaxResponseBytes < 1 || l.HTTPS.TimeoutMillis < 1) {
		return false
	}
	return true
}

func validateDecision(d Decision) string {
	if d.APIVersion != decisionVer {
		return ReasonVersionUnsupported
	}
	if d.ClusterID == "" {
		return ReasonLifecycleInvalid
	}
	if d.Windows.AdmissionDeadline < d.Windows.IssuedAt || d.Windows.AdmissionDeadline-d.Windows.IssuedAt > admissionWindowSeconds {
		return ReasonWindowInvalid
	}
	if d.Windows.NotBefore < d.Windows.IssuedAt-skewSeconds || d.Windows.NotBefore > d.Windows.AdmissionDeadline {
		return ReasonWindowInvalid
	}
	if d.Budget.TurnCap.Requests > d.Budget.RunCap.Requests || d.Budget.TurnCap.OutputTokens > d.Budget.RunCap.OutputTokens {
		return ReasonBudgetMismatch
	}
	if d.Lifecycle == "one-shot" {
		if d.Budget.MaxTurns != 1 || d.Budget.ParentDeadlineUnix != 0 {
			return ReasonLifecycleInvalid
		}
	} else {
		if d.Budget.MaxTurns < 1 || d.Budget.ParentDeadlineUnix < d.Budget.TurnDeadlineUnix {
			return ReasonLifecycleInvalid
		}
	}
	if d.Budget.TurnDeadlineUnix <= d.Windows.IssuedAt {
		return ReasonLifecycleInvalid
	}
	for _, t := range d.Tools {
		if !validLimits(t.Limits) {
			return ReasonLimitRange
		}
	}
	if d.Route.Provider == "none" {
		if d.Route.Auth != "none" || d.Route.CredentialSource != nil || d.Route.ModelConnectionUID != nil {
			return ReasonRouteMismatch
		}
	} else {
		if d.Route.Streaming {
			return ReasonStreaming
		}
		if d.Route.Protocol != "openai-chat" && d.Route.Protocol != "anthropic-messages" {
			return ReasonProtocol
		}
		if d.Route.Auth == "secret" {
			if d.Route.CredentialSource == nil {
				return ReasonRouteMismatch
			}
		} else if d.Route.Auth == "none" {
			if d.Route.CredentialSource != nil {
				return ReasonRouteMismatch
			}
		} else {
			return ReasonRouteMismatch
		}
	}
	return ""
}

func Verify(compact string, decisionRaw []byte, ctx VerifyContext) (string, string, error) {
	if len(compact) == 0 || len(compact) > maxCompactSize {
		return "reject", ReasonSizeExceeded, nil
	}
	if len(decisionRaw) > maxDecisionLen {
		return "reject", ReasonSizeExceeded, nil
	}
	pj, err := parseCompact(compact)
	if err != nil {
		if bytes.Contains([]byte(err.Error()), []byte("duplicate JSON key")) {
			return "reject", ReasonDuplicateJSONKey, nil
		}
		return "reject", ReasonMalformed, nil
	}
	if len(pj.headerRaw) > maxHeaderLen {
		return "reject", ReasonHeaderSize, nil
	}
	var h struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
		Kid string `json:"kid"`
	}
	if err := strictDecode(pj.headerRaw, &h); err != nil {
		if bytes.Contains([]byte(err.Error()), []byte("unknown field")) {
			return "reject", ReasonUnknownField, nil
		}
		return "reject", ReasonMalformed, nil
	}
	if h.Alg != "EdDSA" {
		return "reject", ReasonAlgUnsupported, nil
	}
	if h.Typ != jwsTyp {
		return "reject", ReasonTypUnsupported, nil
	}
	known := false
	for _, k := range activeJWKS.Keys {
		if k.Kid == h.Kid {
			known = true
		}
	}
	if !known {
		return "reject", ReasonKidUnknown, nil
	}
	ok, err := verifyEd25519(h.Kid, pj.signingInput, pj.signature)
	if err != nil || !ok {
		return "reject", ReasonSigInvalid, nil
	}
	var d Decision
	if err := strictDecode(decisionRaw, &d); err != nil {
		return "reject", ReasonMalformed, nil
	}
	if r := validateDecision(d); r != "" {
		return "reject", r, nil
	}
	canon, err := canonicalizeJSON(decisionRaw)
	if err != nil {
		return "reject", ReasonMalformed, nil
	}
	digest := sha256Digest(canon)
	var c Claims
	if err := strictDecode(pj.payloadRaw, &c); err != nil {
		if bytes.Contains([]byte(err.Error()), []byte("unknown field")) {
			return "reject", ReasonUnknownField, nil
		}
		return "reject", ReasonMalformed, nil
	}
	if c.APIVersion != credentialVer {
		return "reject", ReasonVersionUnsupported, nil
	}
	if c.DecisionDigest != digest {
		return "reject", ReasonDigestMismatch, nil
	}
	if c.Iss != issuerName {
		return "reject", ReasonIssMismatch, nil
	}
	if c.Aud != ctx.ExpectedAudience || requiredAudience(ctx.ExpectedOperation) != ctx.ExpectedAudience {
		return "reject", ReasonAudMismatch, nil
	}
	if c.Operation != ctx.ExpectedOperation || !allowedByDecision(d, c.Operation) {
		return "reject", ReasonOperationMismatch, nil
	}
	if c.BudgetID != d.Budget.BudgetID || ctx.BudgetID != d.Budget.BudgetID {
		return "reject", ReasonBudgetMismatch, nil
	}
	if c.Subject.RunUID != d.Run.UID || !samePtr(c.Subject.TurnID, parentTurnID(d.Parent)) || !samePtr(c.Subject.ParentIncarnation, parentIncarnation(d.Parent)) {
		return "reject", ReasonSubjectMismatch, nil
	}
	now := ctx.Now
	if ctx.ExpectedOperation == "model.invoke" && now > d.Budget.TurnDeadlineUnix+skewSeconds {
		return "reject", ReasonDeadlineExpired, nil
	}
	if c.Nbf > now+skewSeconds || c.Iat > now+skewSeconds {
		return "reject", ReasonTimeNotYetValid, nil
	}
	if c.Exp <= now-skewSeconds {
		return "reject", ReasonTimeExpired, nil
	}
	if c.Iat < d.Windows.IssuedAt-skewSeconds {
		return "reject", ReasonWindowInvalid, nil
	}
	if d.ClusterID != ctx.ClusterID {
		return "reject", ReasonSubjectMismatch, nil
	}
	if d.Run.Namespace != ctx.Namespace || d.Run.NamespaceUID != ctx.NamespaceUID {
		return "reject", ReasonNamespaceMismatch, nil
	}
	if d.Run.UID != ctx.RunUID || d.Run.SpecSHA256 != ctx.RunSpecSHA256 {
		return "reject", ReasonRunMismatch, nil
	}
	if !sameParent(d.Parent, ctx.Parent) {
		return "reject", ReasonParentTurn, nil
	}
	switch ctx.ExpectedOperation {
	case "execution.start", "execution.turn":
		if now > d.Windows.AdmissionDeadline+skewSeconds {
			return "reject", ReasonAdmissionExpired, nil
		}
		if c.Exp > d.Windows.AdmissionDeadline+skewSeconds {
			return "reject", ReasonWindowInvalid, nil
		}
		if d.RequestDigest != ctx.RequestDigest {
			return "reject", ReasonRequestBinding, nil
		}
		if ctx.SeenAdmissionJTI {
			return "recover", ReasonAdmissionReplay, nil
		}
	case "model.invoke":
		if c.Exp > d.Budget.TurnDeadlineUnix+skewSeconds {
			return "reject", ReasonWindowInvalid, nil
		}
		if !sameRoute(d.Route, ctx.Route) {
			return "reject", ReasonRouteMismatch, nil
		}
	case "execution.read", "execution.cleanup":
		if c.Exp-c.Iat > cleanupCredentialMaxSeconds {
			return "reject", ReasonWindowInvalid, nil
		}
	}
	return "accept", "", nil
}

func ResolveCheck(d Decision, o ResolverObserved) (string, string) {
	if !o.PolicyPresent || d.Policy.Digest != o.PolicyDigest {
		return "reject", ReasonPolicyWithdrawn
	}
	if len(d.Tools) != len(o.ToolOrder) {
		return "reject", ReasonToolOrder
	}
	for i, t := range d.Tools {
		if t.Name != o.ToolOrder[i].Name {
			return "reject", ReasonToolOrder
		}
		if t.Revision != o.ToolOrder[i].Revision || t.Hash != o.ToolOrder[i].Hash {
			return "reject", ReasonToolUnknown
		}
		if i >= len(o.PolicyLimits) || !limitsWithin(t.Limits, o.PolicyLimits[i]) {
			return "reject", ReasonPolicyContracted
		}
	}
	return "accept", ""
}
