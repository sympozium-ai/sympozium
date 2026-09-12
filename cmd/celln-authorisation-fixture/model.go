// Package main implements the Celln namespace-authorisation v1 conformance
// fixture generator and validator. See docs/design/celln-namespace-authorisation.md.
//
// This is a contract/fixture tool. It contains no runtime enforcement code and
// makes no installed-security claim.
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"unicode/utf16"
)

// ---------------------------------------------------------------------------
// Decision and credential model (mirrors the JSON Schema).
// ---------------------------------------------------------------------------

type RunBinding struct {
	Namespace    string `json:"namespace"`
	NamespaceUID string `json:"namespaceUid"`
	Name         string `json:"name"`
	UID          string `json:"uid"`
	SpecSHA256   string `json:"specSha256"`
}

type SubjectBinding struct {
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
	UID        string `json:"uid"`
	SpecSHA256 string `json:"specSha256"`
}

type ParentBinding struct {
	Incarnation string  `json:"incarnation"`
	TurnID      *string `json:"turnId"`
}

type RuntimeBinding struct {
	Name       string `json:"name"`
	UID        string `json:"uid"`
	Revision   string `json:"revision"`
	SpecSHA256 string `json:"specSha256"`
}

type PolicyBinding struct {
	Profile  string `json:"profile"`
	Revision string `json:"revision"`
	Digest   string `json:"digest"`
}

type Limits struct {
	TimeoutMillis int64  `json:"timeoutMillis"`
	MemoryBytes   int64  `json:"memoryBytes"`
	TaskBytes     int64  `json:"taskBytes"`
	OutputBytes   int64  `json:"outputBytes"`
	Workspace     string `json:"workspace"`
}

type ToolBinding struct {
	Name     string `json:"name"`
	Revision string `json:"revision"`
	Hash     string `json:"hash"`
	Limits   Limits `json:"limits"`
}

type CredentialSource struct {
	Kind       string `json:"kind"`
	SecretUID  string `json:"secretUid"`
	SecretName string `json:"secretName"`
	SecretKey  string `json:"secretKey"`
}

type RouteBinding struct {
	ModelConnectionUID        *string           `json:"modelConnectionUid"`
	ModelConnectionSpecSHA256 string            `json:"modelConnectionSpecSha256"`
	Provider                  string            `json:"provider"`
	Protocol                  string            `json:"protocol"`
	Model                     string            `json:"model"`
	EndpointOrigin            string            `json:"endpointOrigin"`
	Streaming                 bool              `json:"streaming"`
	CredentialSource          *CredentialSource `json:"credentialSource"`
}

type Cap struct {
	Requests     int64 `json:"requests"`
	OutputTokens int64 `json:"outputTokens"`
}

type BudgetBinding struct {
	BudgetID     string `json:"budgetId"`
	RunCap       Cap    `json:"runCap"`
	TurnCap      Cap    `json:"turnCap"`
	DeadlineUnix int64  `json:"deadlineUnix"`
}

type Windows struct {
	IssuedAt          int64 `json:"issuedAt"`
	NotBefore         int64 `json:"notBefore"`
	Expiry            int64 `json:"expiry"`
	AdmissionDeadline int64 `json:"admissionDeadline"`
}

type Decision struct {
	APIVersion     string         `json:"apiVersion"`
	Kind           string         `json:"kind"`
	Run            RunBinding     `json:"run"`
	Subject        SubjectBinding `json:"subject"`
	Operation      string         `json:"operation"`
	Lifecycle      string         `json:"lifecycle"`
	Parent         *ParentBinding `json:"parent"`
	Runtime        RuntimeBinding `json:"runtime"`
	Agent          SubjectBinding `json:"agent"`
	Policy         PolicyBinding  `json:"policy"`
	Tools          []ToolBinding  `json:"tools"`
	Route          RouteBinding   `json:"route"`
	Budget         BudgetBinding  `json:"budget"`
	Windows        Windows        `json:"windows"`
	RequestBinding string         `json:"requestBinding"`
}

type ClaimSubject struct {
	RunUID            string  `json:"runUid"`
	TurnID            *string `json:"turnId"`
	ParentIncarnation *string `json:"parentIncarnation"`
}

type Claims struct {
	APIVersion     string       `json:"apiVersion"`
	Iss            string       `json:"iss"`
	Aud            string       `json:"aud"`
	Iat            int64        `json:"iat"`
	Nbf            int64        `json:"nbf"`
	Exp            int64        `json:"exp"`
	Jti            string       `json:"jti"`
	DecisionDigest string       `json:"decisionDigest"`
	BudgetID       string       `json:"budgetId"`
	Operation      string       `json:"operation"`
	Subject        ClaimSubject `json:"subject"`
}

type JWK struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	X   string `json:"x"`
}

type JWKS struct {
	Keys []JWK `json:"keys"`
}

type PrivateKey struct {
	Kid     string `json:"kid"`
	Alg     string `json:"alg"`
	Crv     string `json:"crv"`
	SeedHex string `json:"seedHex"`
	Prod    bool   `json:"production"`
	Comment string `json:"comment"`
}

type PrivateKeys struct {
	Keys []PrivateKey `json:"keys"`
}

// Observed is the live context a verifier compares the signed decision against.
type Observed struct {
	Now           int64          `json:"now"`
	Namespace     string         `json:"namespace"`
	NamespaceUID  string         `json:"namespaceUid"`
	RunUID        string         `json:"runUid"`
	RunSpecSHA256 string         `json:"runSpecSha256"`
	Parent        *ParentBinding `json:"parent"`
	PolicyPresent bool           `json:"policyPresent"`
	PolicyDigest  string         `json:"policyDigest"`
	// PolicyLimits are the per-tool contracted maxima, parallel to the approved
	// tool order.
	PolicyLimits []Limits `json:"policyLimits"`
	// ToolOrder is the approved ordered tool set the decision must match.
	ToolOrder []ToolBinding `json:"toolOrder"`
	Route     RouteBinding  `json:"route"`
	BudgetID  string        `json:"budgetId"`
	SeenJti   bool          `json:"seenJti"`
}

type Expect struct {
	Outcome string `json:"outcome"`
	Reason  string `json:"reason,omitempty"`
}

type ManifestVector struct {
	Name     string `json:"name"`
	Category string `json:"category"`
	Outcome  string `json:"outcome"`
	Reason   string `json:"reason,omitempty"`
}

type Manifest struct {
	APIVersion string           `json:"apiVersion"`
	Contract   string           `json:"contract"`
	BundleHash string           `json:"bundleHash"`
	Vectors    []ManifestVector `json:"vectors"`
}

// ---------------------------------------------------------------------------
// Canonical encoding (RFC 8785 JCS), restricted to integer numbers.
// ---------------------------------------------------------------------------

const maxSafeInteger = int64(9007199254740991)

func canonicalizeJSON(raw []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, fmt.Errorf("trailing data")
	}
	var buf bytes.Buffer
	if err := appendCanonical(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func appendCanonical(buf *bytes.Buffer, v any) error {
	switch t := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if t {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case string:
		appendCanonicalString(buf, t)
	case json.Number:
		s := t.String()
		if !isCanonicalInteger(s) {
			return fmt.Errorf("non-integer or non-canonical number %q", s)
		}
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil || n > maxSafeInteger || n < -maxSafeInteger {
			return fmt.Errorf("number %q outside JSON-safe integer range", s)
		}
		buf.WriteString(s)
	case []any:
		buf.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := appendCanonical(buf, e); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return lessUTF16(keys[i], keys[j]) })
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			appendCanonicalString(buf, k)
			buf.WriteByte(':')
			if err := appendCanonical(buf, t[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	default:
		return fmt.Errorf("unsupported JSON value %T", v)
	}
	return nil
}

func isCanonicalInteger(s string) bool {
	if s == "" {
		return false
	}
	i := 0
	if s[0] == '-' {
		if len(s) == 1 {
			return false
		}
		i = 1
	}
	if s[i] == '0' && len(s)-i > 1 {
		return false
	}
	for ; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func appendCanonicalString(buf *bytes.Buffer, s string) {
	buf.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			buf.WriteString(`\"`)
		case '\\':
			buf.WriteString(`\\`)
		case '\b':
			buf.WriteString(`\b`)
		case '\f':
			buf.WriteString(`\f`)
		case '\n':
			buf.WriteString(`\n`)
		case '\r':
			buf.WriteString(`\r`)
		case '\t':
			buf.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(buf, `\u%04x`, r)
			} else {
				buf.WriteRune(r)
			}
		}
	}
	buf.WriteByte('"')
}

func lessUTF16(a, b string) bool {
	ua := utf16.Encode([]rune(a))
	ub := utf16.Encode([]rune(b))
	for i := 0; i < len(ua) && i < len(ub); i++ {
		if ua[i] != ub[i] {
			return ua[i] < ub[i]
		}
	}
	return len(ua) < len(ub)
}

func sha256Digest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// requestBinding recomputes the non-self-referential binding over the decision
// with requestBinding cleared. Only that self-reference is excluded.
func requestBinding(d Decision) (string, error) {
	d.RequestBinding = ""
	raw, err := json.Marshal(d)
	if err != nil {
		return "", err
	}
	canon, err := canonicalizeJSON(raw)
	if err != nil {
		return "", err
	}
	return sha256Digest(canon), nil
}

// ---------------------------------------------------------------------------
// Strict JWS parsing and verification.
// ---------------------------------------------------------------------------

var errDuplicateKey = fmt.Errorf("duplicate JSON key")

type parsedJWS struct {
	header       map[string]any
	headerRaw    []byte
	payloadRaw   []byte
	signingInput []byte
	signature    []byte
}

func parseCompact(compact string) (*parsedJWS, error) {
	parts := bytes.Split([]byte(compact), []byte("."))
	if len(parts) != 3 || len(parts[0]) == 0 || len(parts[1]) == 0 || len(parts[2]) == 0 {
		return nil, fmt.Errorf("compact JWS must have exactly three non-empty segments")
	}
	dec := func(b []byte) ([]byte, error) {
		return base64.RawURLEncoding.DecodeString(string(b))
	}
	headerRaw, err := dec(parts[0])
	if err != nil {
		return nil, err
	}
	payloadRaw, err := dec(parts[1])
	if err != nil {
		return nil, err
	}
	sig, err := dec(parts[2])
	if err != nil {
		return nil, err
	}
	signingInput := append(append([]byte{}, parts[0]...), '.')
	signingInput = append(signingInput, parts[1]...)
	// Strict duplicate detection on header and payload.
	if err := checkStrictJSON(headerRaw); err != nil {
		return nil, err
	}
	if err := checkStrictJSON(payloadRaw); err != nil {
		return nil, err
	}
	var header map[string]any
	if err := json.Unmarshal(headerRaw, &header); err != nil {
		return nil, err
	}
	return &parsedJWS{header: header, headerRaw: headerRaw, payloadRaw: payloadRaw, signingInput: signingInput, signature: sig}, nil
}

// checkStrictJSON walks a JSON document and rejects duplicate object keys.
func checkStrictJSON(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := walkStrict(dec); err != nil {
		return err
	}
	return nil
}

func walkStrict(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for dec.More() {
			ktok, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := ktok.(string)
			if !ok {
				return fmt.Errorf("object key is not a string")
			}
			if seen[key] {
				return fmt.Errorf("%w: %q", errDuplicateKey, key)
			}
			seen[key] = true
			if err := walkStrict(dec); err != nil {
				return err
			}
		}
		_, err = dec.Token()
		return err
	case '[':
		for dec.More() {
			if err := walkStrict(dec); err != nil {
				return err
			}
		}
		_, err = dec.Token()
		return err
	default:
		return fmt.Errorf("unexpected delimiter %v", delim)
	}
}

func strictDecode(raw []byte, target any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(target)
}

func verifyEd25519(jwks JWKS, kid string, signingInput, sig []byte) (bool, error) {
	for _, k := range jwks.Keys {
		if k.Kid != kid {
			continue
		}
		pub, err := base64.RawURLEncoding.DecodeString(k.X)
		if err != nil || len(pub) != ed25519.PublicKeySize {
			return false, fmt.Errorf("invalid JWK %q", kid)
		}
		return ed25519.Verify(ed25519.PublicKey(pub), signingInput, sig), nil
	}
	return false, nil
}

// ---------------------------------------------------------------------------
// Reason codes.
// ---------------------------------------------------------------------------

const (
	ReasonMalformed          = "AUTH_CRED_MALFORMED"
	ReasonDuplicateJSONKey   = "AUTH_CRED_DUPLICATE_JSON_KEY"
	ReasonUnknownField       = "AUTH_CRED_UNKNOWN_FIELD"
	ReasonHeaderSize         = "AUTH_CRED_HEADER_SIZE"
	ReasonSizeExceeded       = "AUTH_CRED_SIZE_EXCEEDED"
	ReasonAlgUnsupported     = "AUTH_CRED_ALG_UNSUPPORTED"
	ReasonTypUnsupported     = "AUTH_CRED_TYP_UNSUPPORTED"
	ReasonKidUnknown         = "AUTH_CRED_KID_UNKNOWN"
	ReasonSigInvalid         = "AUTH_CRED_SIG_INVALID"
	ReasonKeyUnavailable     = "AUTH_CRED_KEY_UNAVAILABLE"
	ReasonIssMismatch        = "AUTH_ISS_MISMATCH"
	ReasonAudMismatch        = "AUTH_AUD_MISMATCH"
	ReasonOperationMismatch  = "AUTH_OPERATION_MISMATCH"
	ReasonSubjectMismatch    = "AUTH_SUBJECT_MISMATCH"
	ReasonTimeNotYetValid    = "AUTH_TIME_NOT_YET_VALID"
	ReasonTimeExpired        = "AUTH_TIME_EXPIRED"
	ReasonTimeSkew           = "AUTH_TIME_SKEW_EXCEEDED"
	ReasonAdmissionExpired   = "AUTH_ADMISSION_WINDOW_EXPIRED"
	ReasonDigestMismatch     = "AUTH_DECISION_DIGEST_MISMATCH"
	ReasonVersionUnsupported = "AUTH_VERSION_UNSUPPORTED"
	ReasonDecisionSize       = "AUTH_DECISION_SIZE_EXCEEDED"
	ReasonNamespaceMismatch  = "AUTH_NAMESPACE_UID_MISMATCH"
	ReasonRunMismatch        = "AUTH_RUN_UID_MISMATCH"
	ReasonParentTurn         = "AUTH_PARENT_TURN_MISMATCH"
	ReasonRouteMismatch      = "AUTH_ROUTE_MISMATCH"
	ReasonCredSourceMismatch = "AUTH_CREDENTIAL_SOURCE_MISMATCH"
	ReasonBudgetMismatch     = "AUTH_BUDGET_MISMATCH"
	ReasonPolicyWithdrawn    = "AUTH_POLICY_WITHDRAWN"
	ReasonPolicyContracted   = "AUTH_POLICY_CONTRACTED"
	ReasonToolOrder          = "AUTH_TOOL_ORDER_MISMATCH"
	ReasonToolUnknown        = "AUTH_TOOL_UNKNOWN"
	ReasonLimitRange         = "AUTH_LIMIT_OUT_OF_RANGE"
	ReasonStreaming          = "AUTH_STREAMING_UNSUPPORTED"
	ReasonProtocol           = "AUTH_PROTOCOL_UNSUPPORTED"
	ReasonReplay             = "AUTH_DUPLICATE_DECISION"
	ReasonRequestBinding     = "AUTH_REQUEST_BINDING_MISMATCH"
)

const (
	issuerName     = "sympozium-control-plane"
	decisionVer    = "celln.sympozium.ai/authorisation-decision-v1"
	credentialVer  = "celln.sympozium.ai/authorisation-credential-v1"
	jwsTyp         = "celln-authorisation+jws"
	audExecution   = "celln-execution"
	audModel       = "sympozium-model-gateway"
	maxCompactSize = 32768
	maxDecisionLen = 262144
	maxHeaderLen   = 1024
	skewSeconds    = 5
)

// Evaluate runs the ordered conformance checks for one vector and returns the
// first failing reason, or empty when the vector is accepted.
func Evaluate(compact string, decisionRaw, canonical, declaredDigest []byte, observed Observed) (string, error) {
	// 1. size
	if len(compact) > maxCompactSize || len(canonical) > maxDecisionLen {
		return ReasonSizeExceeded, nil
	}
	if len(compact) == 0 {
		return ReasonMalformed, nil
	}
	// 2. parse
	pj, err := parseCompact(compact)
	if err != nil {
		if contains(err.Error(), "duplicate JSON key") {
			return ReasonDuplicateJSONKey, nil
		}
		return ReasonMalformed, nil
	}
	if len(pj.headerRaw) > maxHeaderLen {
		return ReasonHeaderSize, nil
	}
	// header unknown fields
	var protected struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
		Kid string `json:"kid"`
	}
	if err := strictDecode(pj.headerRaw, &protected); err != nil {
		if contains(err.Error(), "unknown field") {
			return ReasonUnknownField, nil
		}
		return ReasonMalformed, nil
	}
	// 3. alg/typ
	if protected.Alg != "EdDSA" {
		return ReasonAlgUnsupported, nil
	}
	if protected.Typ != jwsTyp {
		return ReasonTypUnsupported, nil
	}
	// 4. decision version
	var d Decision
	if err := json.Unmarshal(decisionRaw, &d); err != nil {
		return ReasonMalformed, nil
	}
	if d.APIVersion != decisionVer {
		return ReasonVersionUnsupported, nil
	}
	// 5. kid
	jwks := loadJWKS()
	known := false
	for _, k := range jwks.Keys {
		if k.Kid == protected.Kid {
			known = true
		}
	}
	if !known {
		return ReasonKidUnknown, nil
	}
	// 6. signature
	ok, err := verifyEd25519(jwks, protected.Kid, pj.signingInput, pj.signature)
	if err != nil {
		return ReasonKeyUnavailable, nil
	}
	if !ok {
		return ReasonSigInvalid, nil
	}
	// 7. digest + canonical decision bytes
	recomputed, err := canonicalizeJSON(decisionRaw)
	if err != nil {
		return ReasonMalformed, nil
	}
	if !bytes.Equal(recomputed, canonical) {
		return ReasonDigestMismatch, nil
	}
	digest := sha256Digest(canonical)
	if string(declaredDigest) != digest {
		return ReasonDigestMismatch, nil
	}
	var claims Claims
	if err := strictDecode(pj.payloadRaw, &claims); err != nil {
		if contains(err.Error(), "unknown field") {
			return ReasonUnknownField, nil
		}
		return ReasonMalformed, nil
	}
	if claims.DecisionDigest != digest {
		return ReasonDigestMismatch, nil
	}
	// 8. issuer
	if claims.Iss != issuerName {
		return ReasonIssMismatch, nil
	}
	// 9. time
	now := observed.now()
	if claims.Nbf > now+skewSeconds || claims.Iat > now+skewSeconds {
		return ReasonTimeNotYetValid, nil
	}
	if claims.Iat > now+skewSeconds {
		return ReasonTimeSkew, nil
	}
	if claims.Exp <= now-skewSeconds {
		return ReasonTimeExpired, nil
	}
	if d.Windows.AdmissionDeadline+skewSeconds < now {
		return ReasonAdmissionExpired, nil
	}
	// 10. audience
	required := requiredAudience(d.Operation)
	if required == "" || claims.Aud != required {
		return ReasonAudMismatch, nil
	}
	// 11. claim binding
	if claims.Operation != d.Operation {
		return ReasonOperationMismatch, nil
	}
	if claims.BudgetID != d.Budget.BudgetID {
		return ReasonBudgetMismatch, nil
	}
	if claims.Subject.RunUID != d.Run.UID {
		return ReasonSubjectMismatch, nil
	}
	if !samePtr(claims.Subject.TurnID, parentTurnID(d.Parent)) {
		return ReasonSubjectMismatch, nil
	}
	if !samePtr(claims.Subject.ParentIncarnation, parentIncarnation(d.Parent)) {
		return ReasonSubjectMismatch, nil
	}
	// 12. request binding
	rb, err := requestBinding(d)
	if err != nil {
		return ReasonMalformed, nil
	}
	if d.RequestBinding != rb {
		return ReasonRequestBinding, nil
	}
	// 13. namespace
	if d.Run.Namespace != observed.Namespace || d.Run.NamespaceUID != observed.NamespaceUID {
		return ReasonNamespaceMismatch, nil
	}
	if d.Subject.Namespace != d.Run.Namespace {
		return ReasonNamespaceMismatch, nil
	}
	// 14. run
	if d.Run.UID != observed.RunUID || d.Run.SpecSHA256 != observed.RunSpecSHA256 {
		return ReasonRunMismatch, nil
	}
	// 15. parent/turn
	if !sameParent(d.Parent, observed.Parent) {
		return ReasonParentTurn, nil
	}
	// 16. route
	if !sameRoute(d.Route, observed.Route) {
		return ReasonRouteMismatch, nil
	}
	// 17. credential source
	if !sameCredSource(d.Route.CredentialSource, observed.Route.CredentialSource) {
		return ReasonCredSourceMismatch, nil
	}
	// 18. policy
	if !observed.PolicyPresent || d.Policy.Digest != observed.PolicyDigest {
		return ReasonPolicyWithdrawn, nil
	}
	// 19. policy contraction
	for i, t := range d.Tools {
		if i < len(observed.PolicyLimits) && !limitsWithin(t.Limits, observed.PolicyLimits[i]) {
			return ReasonPolicyContracted, nil
		}
	}
	// 20. limits range
	for _, t := range d.Tools {
		if !limitInRange(t.Limits) {
			return ReasonLimitRange, nil
		}
	}
	// 21. tools order / unknown
	if !sameToolNames(d.Tools, observed.ToolOrder) {
		return ReasonToolOrder, nil
	}
	for i := range d.Tools {
		if i >= len(observed.ToolOrder) || d.Tools[i].Revision != observed.ToolOrder[i].Revision || d.Tools[i].Hash != observed.ToolOrder[i].Hash {
			return ReasonToolUnknown, nil
		}
	}
	// 22. protocol / streaming (only for model-bearing routes)
	if d.Route.Provider != "none" {
		if d.Route.Streaming {
			return ReasonStreaming, nil
		}
		if d.Route.Protocol != "openai-chat" && d.Route.Protocol != "anthropic-messages" {
			return ReasonProtocol, nil
		}
	}
	// 23. replay
	if observed.SeenJti {
		return ReasonReplay, nil
	}
	return "", nil
}

func (o Observed) now() int64 { return o.Now }

func requiredAudience(op string) string {
	switch op {
	case "execution.start", "execution.turn", "execution.cancel", "execution.stop", "execution.read", "execution.cleanup":
		return audExecution
	case "model.invoke":
		return audModel
	default:
		return ""
	}
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
	return samePtr(a.ModelConnectionUID, b.ModelConnectionUID) &&
		a.ModelConnectionSpecSHA256 == b.ModelConnectionSpecSHA256 &&
		a.Provider == b.Provider && a.Protocol == b.Protocol && a.Model == b.Model &&
		a.EndpointOrigin == b.EndpointOrigin && a.Streaming == b.Streaming
}

func sameCredSource(a, b *CredentialSource) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func sameToolNames(tools []ToolBinding, approved []ToolBinding) bool {
	if len(tools) != len(approved) {
		return false
	}
	for i := range tools {
		if tools[i].Name != approved[i].Name {
			return false
		}
	}
	return true
}

func limitsWithin(a, b Limits) bool {
	return a.TimeoutMillis <= b.TimeoutMillis && a.MemoryBytes <= b.MemoryBytes &&
		a.TaskBytes <= b.TaskBytes && a.OutputBytes <= b.OutputBytes
}

func limitInRange(l Limits) bool {
	if l.TimeoutMillis < 1 || l.TimeoutMillis > 300000 {
		return false
	}
	if l.MemoryBytes < 1 || l.MemoryBytes > 268435456 {
		return false
	}
	if l.TaskBytes < 1 || l.TaskBytes > 2048 {
		return false
	}
	if l.OutputBytes < 1 || l.OutputBytes > 65536 {
		return false
	}
	return l.Workspace == "none"
}

func contains(s, sub string) bool { return bytes.Contains([]byte(s), []byte(sub)) }

// activeJWKS is populated by the CLI from the fixture signing keys before any
// evaluation. Fixture keys are explicitly non-production.
var activeJWKS JWKS

func loadJWKS() JWKS { return activeJWKS }
