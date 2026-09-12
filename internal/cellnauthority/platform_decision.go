package cellnauthority

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"unicode/utf16"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

const PlatformDecisionAPIVersion = "celln.sympozium.ai/authorisation-decision-v1"

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

type DecisionToolLimits struct {
	TimeoutMillis int64                    `json:"timeoutMillis"`
	MemoryBytes   int64                    `json:"memoryBytes"`
	ArgumentBytes int64                    `json:"argumentBytes"`
	OutputBytes   int64                    `json:"outputBytes"`
	Workspace     string                   `json:"workspace"`
	Effects       string                   `json:"effects"`
	Artifacts     *api.CellnArtifactLimits `json:"artifacts"`
	HTTPS         *api.CellnHTTPSLimits    `json:"https"`
}

type DecisionToolBinding struct {
	Name     string             `json:"name"`
	Revision string             `json:"revision"`
	Hash     string             `json:"hash"`
	Limits   DecisionToolLimits `json:"limits"`
}

// CredentialSourceRef deliberately omits a Secret UID. Resolution binds the
// tenant-authored reference without reading Secret objects; the model gateway
// supplies and durably pins the UID during authenticated route registration.
type CredentialSourceRef struct {
	Kind       string `json:"kind"`
	SecretName string `json:"secretName"`
	SecretKey  string `json:"secretKey"`
}

type CredentialSource struct {
	Kind       string `json:"kind"`
	SecretUID  string `json:"secretUid"`
	SecretName string `json:"secretName"`
	SecretKey  string `json:"secretKey"`
}

type DecisionRouteBinding struct {
	ModelConnectionUID        *string              `json:"modelConnectionUid"`
	ModelConnectionSpecSHA256 string               `json:"modelConnectionSpecSha256"`
	Provider                  string               `json:"provider"`
	Protocol                  string               `json:"protocol"`
	Model                     string               `json:"model"`
	EndpointOrigin            string               `json:"endpointOrigin"`
	Auth                      string               `json:"auth"`
	Streaming                 bool                 `json:"streaming"`
	CredentialSource          *CredentialSource    `json:"credentialSource"`
	CredentialSourceRef       *CredentialSourceRef `json:"-"`
}

type DecisionCap struct {
	Requests     int64 `json:"requests"`
	OutputTokens int64 `json:"outputTokens"`
}

type DecisionBudgetBinding struct {
	BudgetID           string      `json:"budgetId"`
	RunCap             DecisionCap `json:"runCap"`
	TurnCap            DecisionCap `json:"turnCap"`
	MaxTurns           int64       `json:"maxTurns"`
	ParentDeadlineUnix int64       `json:"parentDeadlineUnix"`
	TurnDeadlineUnix   int64       `json:"turnDeadlineUnix"`
}

type DecisionWindows struct {
	IssuedAt          int64 `json:"issuedAt"`
	NotBefore         int64 `json:"notBefore"`
	AdmissionDeadline int64 `json:"admissionDeadline"`
}

// PlatformDecision is secret-free and is the resolver-to-issuer handoff. The
// gateway later replaces CredentialSourceRef with its Secret UID pin before a
// model credential is issued; it may not change any route or budget field.
type PlatformDecision struct {
	APIVersion    string                `json:"apiVersion"`
	Kind          string                `json:"kind"`
	ClusterID     string                `json:"clusterId"`
	Run           RunBinding            `json:"run"`
	Subject       SubjectBinding        `json:"subject"`
	Operation     string                `json:"operation"`
	Lifecycle     string                `json:"lifecycle"`
	Parent        *ParentBinding        `json:"parent"`
	Runtime       RuntimeBinding        `json:"runtime"`
	Agent         SubjectBinding        `json:"agent"`
	Policy        PolicyBinding         `json:"policy"`
	Tools         []DecisionToolBinding `json:"tools"`
	Route         DecisionRouteBinding  `json:"route"`
	Budget        DecisionBudgetBinding `json:"budget"`
	Windows       DecisionWindows       `json:"windows"`
	RequestDigest string                `json:"requestDigest"`
}

func canonicalJSON(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var decoded any
	if err := dec.Decode(&decoded); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := appendCanonical(&out, decoded); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func appendCanonical(out *bytes.Buffer, value any) error {
	switch v := value.(type) {
	case nil:
		out.WriteString("null")
	case bool:
		out.WriteString(strconv.FormatBool(v))
	case string:
		appendCanonicalString(out, v)
	case json.Number:
		number, err := strconv.ParseInt(v.String(), 10, 64)
		if err != nil || number > 9007199254740991 || number < -9007199254740991 {
			return fmt.Errorf("canonical decisions require safe bounded integers: %q", v.String())
		}
		out.WriteString(v.String())
	case []any:
		out.WriteByte('[')
		for i := range v {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := appendCanonical(out, v[i]); err != nil {
				return err
			}
		}
		out.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool { return lessUTF16(keys[i], keys[j]) })
		out.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := appendCanonical(out, key); err != nil {
				return err
			}
			out.WriteByte(':')
			if err := appendCanonical(out, v[key]); err != nil {
				return err
			}
		}
		out.WriteByte('}')
	default:
		return fmt.Errorf("unsupported canonical JSON type %T", value)
	}
	return nil
}

func appendCanonicalString(out *bytes.Buffer, value string) {
	out.WriteByte('"')
	for _, r := range value {
		switch r {
		case '"':
			out.WriteString(`\"`)
		case '\\':
			out.WriteString(`\\`)
		case '\b':
			out.WriteString(`\b`)
		case '\f':
			out.WriteString(`\f`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(out, `\u%04x`, r)
			} else {
				out.WriteRune(r)
			}
		}
	}
	out.WriteByte('"')
}

func lessUTF16(left, right string) bool {
	l := utf16.Encode([]rune(left))
	r := utf16.Encode([]rune(right))
	for i := 0; i < len(l) && i < len(r); i++ {
		if l[i] != r[i] {
			return l[i] < r[i]
		}
	}
	return len(l) < len(r)
}

func digestJSON(value any) (string, error) {
	raw, err := canonicalJSON(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func (d PlatformDecision) Canonical() ([]byte, error) { return canonicalJSON(d) }

func (d PlatformDecision) Digest() (string, error) { return digestJSON(d) }

// FinalizeCredentialSource applies only the non-secret UID pin returned by the
// model gateway. Until this succeeds, a secret-auth decision is not signable.
func (d PlatformDecision) FinalizeCredentialSource(secretUID string) (PlatformDecision, error) {
	if d.Route.Auth == "none" {
		if d.Route.CredentialSourceRef != nil || d.Route.CredentialSource != nil || secretUID != "" {
			return PlatformDecision{}, fmt.Errorf("model-free decision cannot carry a credential source")
		}
		return d, nil
	}
	if d.Route.Auth != "secret" || d.Route.CredentialSourceRef == nil || d.Route.CredentialSource != nil || secretUID == "" {
		return PlatformDecision{}, fmt.Errorf("secret-auth decision requires the gateway's exact Secret UID pin")
	}
	ref := d.Route.CredentialSourceRef
	d.Route.CredentialSource = &CredentialSource{Kind: ref.Kind, SecretUID: secretUID, SecretName: ref.SecretName, SecretKey: ref.SecretKey}
	d.Route.CredentialSourceRef = nil
	return d, nil
}

func (d PlatformDecision) ReadyForSigning() error {
	if d.APIVersion != PlatformDecisionAPIVersion || d.Kind != "CellnAuthorisationDecision" {
		return fmt.Errorf("unsupported decision contract")
	}
	if d.Route.Auth == "secret" && (d.Route.CredentialSource == nil || d.Route.CredentialSource.SecretUID == "") {
		return fmt.Errorf("model gateway has not pinned the credential source UID")
	}
	if d.Route.Auth == "none" && (d.Route.CredentialSource != nil || d.Route.CredentialSourceRef != nil) {
		return fmt.Errorf("no-auth decision carries a credential source")
	}
	return nil
}
