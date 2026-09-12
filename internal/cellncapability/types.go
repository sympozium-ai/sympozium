package cellncapability

import "fmt"

const (
	DecisionAPIVersion   = "celln.sympozium.ai/authorisation-decision-v1"
	CredentialAPIVersion = "celln.sympozium.ai/authorisation-credential-v1"
	CredentialType       = "celln-authorisation+jws"
	ControlPlaneIssuer   = "sympozium-control-plane"
	AudienceExecution    = "celln-execution"
	AudienceModelGateway = "sympozium-model-gateway"
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
	ReasonKeyUnavailable     = "AUTH_CRED_KEY_UNAVAILABLE"
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
	ReasonStreaming          = "AUTH_STREAMING_UNSUPPORTED"
	ReasonProtocol           = "AUTH_PROTOCOL_UNSUPPORTED"
	ReasonLimitRange         = "AUTH_LIMIT_OUT_OF_RANGE"
	ReasonAdmissionReplay    = "AUTH_ADMISSION_REPLAY"
	ReasonRequestBinding     = "AUTH_REQUEST_BINDING_MISMATCH"
	ReasonLifecycleInvalid   = "AUTH_LIFECYCLE_INVALID"
)

const (
	maxCompactSize               = 32768
	maxDecisionSize              = 262144
	maxHeaderSize                = 1024
	clockSkewSeconds       int64 = 5
	admissionWindowSeconds int64 = 60
	ownerCredentialSeconds int64 = 300
)

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

type VerifyContext struct {
	Now               int64
	ExpectedAudience  string
	ExpectedOperation string
	ClusterID         string
	Namespace         string
	NamespaceUID      string
	RunUID            string
	RunSpecSHA256     string
	Parent            *ParentBinding
	RequestDigest     string
	Route             RouteBinding
	BudgetID          string
	SeenAdmissionJTI  bool
}

type Verified struct {
	Claims         Claims
	DecisionDigest string
	Recovered      bool
	Reason         string
}

type Error struct {
	Reason string
	cause  error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Reason
}
func (e *Error) Unwrap() error { return e.cause }
func reasonError(reason string, cause error) error { return &Error{Reason: reason, cause: cause} }

func Reason(err error) string {
	if err == nil {
		return ""
	}
	if e, ok := err.(*Error); ok {
		return e.Reason
	}
	return ReasonMalformed
}

type Token struct{ raw string }

func NewToken(raw string) Token { return Token{raw: raw} }
func (t Token) Bearer() string  { return t.raw }
func (Token) String() string    { return "[REDACTED celln capability]" }
func (Token) GoString() string  { return "[REDACTED celln capability]" }
func (t Token) Empty() bool     { return t.raw == "" }
func (Token) MarshalText() ([]byte, error) {
	return nil, fmt.Errorf("celln capability tokens are bearer credentials and cannot be marshalled")
}
