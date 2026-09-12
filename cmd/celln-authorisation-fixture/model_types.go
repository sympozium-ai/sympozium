package main

import (
	"encoding/json"
)

type RunBinding struct{ Namespace, NamespaceUID, Name, UID, SpecSHA256 string }

func (r RunBinding) MarshalJSON() ([]byte, error) {
	type x struct {
		Namespace    string `json:"namespace"`
		NamespaceUID string `json:"namespaceUid"`
		Name         string `json:"name"`
		UID          string `json:"uid"`
		SpecSHA256   string `json:"specSha256"`
	}
	return json.Marshal(x{r.Namespace, r.NamespaceUID, r.Name, r.UID, r.SpecSHA256})
}
func (r *RunBinding) UnmarshalJSON(b []byte) error {
	var x struct {
		Namespace    string `json:"namespace"`
		NamespaceUID string `json:"namespaceUid"`
		Name         string `json:"name"`
		UID          string `json:"uid"`
		SpecSHA256   string `json:"specSha256"`
	}
	if err := strictDecode(b, &x); err != nil {
		return err
	}
	*r = RunBinding{x.Namespace, x.NamespaceUID, x.Name, x.UID, x.SpecSHA256}
	return nil
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
type ArtifactLimits struct {
	Operation     string `json:"operation"`
	MaxOperations int64  `json:"maxOperations"`
	MaxFiles      int64  `json:"maxFiles"`
	MaxFileBytes  int64  `json:"maxFileBytes"`
	MaxTotalBytes int64  `json:"maxTotalBytes"`
}
type HTTPSLimits struct {
	AllowHosts       []string `json:"allowHosts"`
	MaxRequests      int64    `json:"maxRequests"`
	MaxResponseBytes int64    `json:"maxResponseBytes"`
	TimeoutMillis    int64    `json:"timeoutMillis"`
}
type Limits struct {
	TimeoutMillis int64           `json:"timeoutMillis"`
	MemoryBytes   int64           `json:"memoryBytes"`
	ArgumentBytes int64           `json:"argumentBytes"`
	OutputBytes   int64           `json:"outputBytes"`
	Workspace     string          `json:"workspace"`
	Effects       string          `json:"effects"`
	Artifacts     *ArtifactLimits `json:"artifacts"`
	HTTPS         *HTTPSLimits    `json:"https"`
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
	Auth                      string            `json:"auth"`
	Streaming                 bool              `json:"streaming"`
	CredentialSource          *CredentialSource `json:"credentialSource"`
}
type Cap struct {
	Requests     int64 `json:"requests"`
	OutputTokens int64 `json:"outputTokens"`
}
type BudgetBinding struct {
	BudgetID           string `json:"budgetId"`
	RunCap             Cap    `json:"runCap"`
	TurnCap            Cap    `json:"turnCap"`
	MaxTurns           int64  `json:"maxTurns"`
	ParentDeadlineUnix int64  `json:"parentDeadlineUnix"`
	TurnDeadlineUnix   int64  `json:"turnDeadlineUnix"`
}
type Windows struct {
	IssuedAt          int64 `json:"issuedAt"`
	NotBefore         int64 `json:"notBefore"`
	AdmissionDeadline int64 `json:"admissionDeadline"`
}
type Decision struct {
	APIVersion    string         `json:"apiVersion"`
	Kind          string         `json:"kind"`
	ClusterID     string         `json:"clusterId"`
	Run           RunBinding     `json:"run"`
	Subject       SubjectBinding `json:"subject"`
	Operation     string         `json:"operation"`
	Lifecycle     string         `json:"lifecycle"`
	Parent        *ParentBinding `json:"parent"`
	Runtime       RuntimeBinding `json:"runtime"`
	Agent         SubjectBinding `json:"agent"`
	Policy        PolicyBinding  `json:"policy"`
	Tools         []ToolBinding  `json:"tools"`
	Route         RouteBinding   `json:"route"`
	Budget        BudgetBinding  `json:"budget"`
	Windows       Windows        `json:"windows"`
	RequestDigest string         `json:"requestDigest"`
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

type VerifyContext struct {
	Now               int64          `json:"now"`
	ExpectedAudience  string         `json:"expectedAudience"`
	ExpectedOperation string         `json:"expectedOperation"`
	ClusterID         string         `json:"clusterId"`
	Namespace         string         `json:"namespace"`
	NamespaceUID      string         `json:"namespaceUid"`
	RunUID            string         `json:"runUid"`
	RunSpecSHA256     string         `json:"runSpecSha256"`
	Parent            *ParentBinding `json:"parent"`
	RequestDigest     string         `json:"requestDigest"`
	Route             RouteBinding   `json:"route"`
	BudgetID          string         `json:"budgetId"`
	SeenAdmissionJTI  bool           `json:"seenAdmissionJti"`
}
type ResolverObserved struct {
	PolicyPresent bool          `json:"policyPresent"`
	PolicyDigest  string        `json:"policyDigest"`
	ToolOrder     []ToolBinding `json:"toolOrder"`
	PolicyLimits  []Limits      `json:"policyLimits"`
}
type Expect struct {
	Outcome string `json:"outcome"`
	Reason  string `json:"reason,omitempty"`
}
type DecisionFixture struct {
	Canonical        string `json:"canonical"`
	RequestCanonical string `json:"requestCanonical"`
}
type Vector struct {
	Name              string            `json:"name"`
	Evaluator         string            `json:"evaluator"`
	Decision          Decision          `json:"-"`
	DecisionRef       string            `json:"decisionRef"`
	DecisionCanonical string            `json:"-"`
	DecisionDigest    string            `json:"-"`
	RequestCanonical  string            `json:"-"`
	Credential        string            `json:"credential,omitempty"`
	Verify            *VerifyContext    `json:"verify,omitempty"`
	Resolver          *ResolverObserved `json:"resolver,omitempty"`
	Expect            Expect            `json:"expect"`
}
type SequenceAction struct {
	TurnID        string `json:"turnId"`
	ID            string `json:"id"`
	Digest        string `json:"digest"`
	ReserveOutput int64  `json:"reserveOutput"`
	Expect        string `json:"expect"`
}
type Sequence struct {
	Name    string           `json:"name"`
	RunCap  Cap              `json:"runCap"`
	TurnCap Cap              `json:"turnCap"`
	Actions []SequenceAction `json:"actions"`
}
type Cases struct {
	APIVersion string                     `json:"apiVersion"`
	Decisions  map[string]DecisionFixture `json:"decisions"`
	Vectors    []Vector                   `json:"vectors"`
	Sequences  []Sequence                 `json:"sequences"`
}
type Manifest struct {
	APIVersion    string   `json:"apiVersion"`
	Contract      string   `json:"contract"`
	VectorNames   []string `json:"vectorNames"`
	SequenceNames []string `json:"sequenceNames"`
}

const (
	issuerName                        = "sympozium-control-plane"
	decisionVer                       = "celln.sympozium.ai/authorisation-decision-v1"
	credentialVer                     = "celln.sympozium.ai/authorisation-credential-v1"
	jwsTyp                            = "celln-authorisation+jws"
	audExecution                      = "celln-execution"
	audModel                          = "sympozium-model-gateway"
	maxCompactSize                    = 32768
	maxDecisionLen                    = 262144
	maxHeaderLen                      = 1024
	skewSeconds                 int64 = 5
	admissionWindowSeconds      int64 = 60
	cleanupCredentialMaxSeconds int64 = 300
)

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
	ReasonIssMismatch        = "AUTH_ISS_MISMATCH"
	ReasonAudMismatch        = "AUTH_AUD_MISMATCH"
	ReasonOperationMismatch  = "AUTH_OPERATION_MISMATCH"
	ReasonSubjectMismatch    = "AUTH_SUBJECT_MISMATCH"
	ReasonTimeNotYetValid    = "AUTH_TIME_NOT_YET_VALID"
	ReasonTimeExpired        = "AUTH_TIME_EXPIRED"
	ReasonAdmissionExpired   = "AUTH_ADMISSION_WINDOW_EXPIRED"
	ReasonDeadlineExpired    = "AUTH_WORK_DEADLINE_EXPIRED"
	ReasonWindowInvalid      = "AUTH_WINDOW_INVALID"
	ReasonDigestMismatch     = "AUTH_DECISION_DIGEST_MISMATCH"
	ReasonVersionUnsupported = "AUTH_VERSION_UNSUPPORTED"
	ReasonNamespaceMismatch  = "AUTH_NAMESPACE_UID_MISMATCH"
	ReasonRunMismatch        = "AUTH_RUN_UID_MISMATCH"
	ReasonParentTurn         = "AUTH_PARENT_TURN_MISMATCH"
	ReasonRouteMismatch      = "AUTH_ROUTE_MISMATCH"
	ReasonBudgetMismatch     = "AUTH_BUDGET_MISMATCH"
	ReasonPolicyWithdrawn    = "AUTH_POLICY_WITHDRAWN"
	ReasonPolicyContracted   = "AUTH_POLICY_CONTRACTED"
	ReasonToolOrder          = "AUTH_TOOL_ORDER_MISMATCH"
	ReasonToolUnknown        = "AUTH_TOOL_UNKNOWN"
	ReasonLimitRange         = "AUTH_LIMIT_OUT_OF_RANGE"
	ReasonStreaming          = "AUTH_STREAMING_UNSUPPORTED"
	ReasonProtocol           = "AUTH_PROTOCOL_UNSUPPORTED"
	ReasonAdmissionReplay    = "AUTH_ADMISSION_REPLAY"
	ReasonRequestBinding     = "AUTH_REQUEST_BINDING_MISMATCH"
	ReasonBudgetExhausted    = "AUTH_BUDGET_EXHAUSTED"
	ReasonRequestConflict    = "AUTH_REQUEST_ID_CONFLICT"
	ReasonLifecycleInvalid   = "AUTH_LIFECYCLE_INVALID"
)

var activeJWKS JWKS
