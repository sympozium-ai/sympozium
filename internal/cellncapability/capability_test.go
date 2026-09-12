package cellncapability

import (
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type fixtureDecision struct {
	Canonical        string `json:"canonical"`
	RequestCanonical string `json:"requestCanonical"`
}

type fixtureVerifyContext struct {
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

type fixtureVector struct {
	Name        string                `json:"name"`
	Evaluator   string                `json:"evaluator"`
	DecisionRef string                `json:"decisionRef"`
	Credential  string                `json:"credential"`
	Verify      *fixtureVerifyContext `json:"verify"`
	Expect      struct {
		Outcome string `json:"outcome"`
		Reason  string `json:"reason"`
	} `json:"expect"`
}

type fixtureCases struct {
	Decisions map[string]fixtureDecision `json:"decisions"`
	Vectors   []fixtureVector            `json:"vectors"`
}

type fixtureJWKS struct {
	Keys []struct {
		Kid string `json:"kid"`
		X   string `json:"x"`
	} `json:"keys"`
}

func fixtureRoot(t *testing.T) string {
	t.Helper()
	return filepath.Join("..", "..", "test", "fixtures", "celln-authorisation", "v1")
}

func loadFixtureCases(t *testing.T) fixtureCases {
	t.Helper()
	f, err := os.Open(filepath.Join(fixtureRoot(t), "cases.json.gz"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	data, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	var cases fixtureCases
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	return cases
}

func loadFixtureKeys(t *testing.T) []VerificationKey {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixtureRoot(t), "signing", "test-jwks.json"))
	if err != nil {
		t.Fatal(err)
	}
	var jwks fixtureJWKS
	if err := json.Unmarshal(data, &jwks); err != nil {
		t.Fatal(err)
	}
	keys := make([]VerificationKey, 0, len(jwks.Keys))
	for _, key := range jwks.Keys {
		public, err := base64.RawURLEncoding.DecodeString(key.X)
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, VerificationKey{KeyID: key.Kid, PublicKey: ed25519.PublicKey(public)})
	}
	return keys
}

func contextFromFixture(in fixtureVerifyContext) VerifyContext {
	return VerifyContext{
		Now:               in.Now,
		ExpectedAudience:  in.ExpectedAudience,
		ExpectedOperation: in.ExpectedOperation,
		ClusterID:         in.ClusterID,
		Namespace:         in.Namespace,
		NamespaceUID:      in.NamespaceUID,
		RunUID:            in.RunUID,
		RunSpecSHA256:     in.RunSpecSHA256,
		Parent:            in.Parent,
		RequestDigest:     in.RequestDigest,
		Route:             in.Route,
		BudgetID:          in.BudgetID,
		SeenAdmissionJTI:  in.SeenAdmissionJTI,
	}
}

func TestVerifierConsumesContractFixtures(t *testing.T) {
	cases := loadFixtureCases(t)
	verifier, err := NewVerifier(ControlPlaneIssuer, loadFixtureKeys(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, vector := range cases.Vectors {
		if vector.Evaluator != "verify" {
			continue
		}
		seen++
		vector := vector
		t.Run(vector.Name, func(t *testing.T) {
			fixture, ok := cases.Decisions[vector.DecisionRef]
			if !ok {
				t.Fatalf("missing decision %s", vector.DecisionRef)
			}
			if vector.Verify == nil {
				t.Fatal("missing verify context")
			}
			result, err := verifier.Verify(NewToken(vector.Credential), []byte(fixture.Canonical), contextFromFixture(*vector.Verify))
			switch vector.Expect.Outcome {
			case "accept":
				if err != nil {
					t.Fatalf("expected acceptance, got %s", Reason(err))
				}
				if result.Recovered {
					t.Fatal("unexpected recovery result")
				}
			case "recover":
				if err != nil {
					t.Fatalf("expected recovery, got %s", Reason(err))
				}
				if !result.Recovered || result.Reason != vector.Expect.Reason {
					t.Fatalf("expected recovery reason %q, got recovered=%v reason=%q", vector.Expect.Reason, result.Recovered, result.Reason)
				}
			case "reject":
				if err == nil {
					t.Fatal("expected rejection")
				}
				if got := Reason(err); got != vector.Expect.Reason {
					t.Fatalf("expected reason %q, got %q", vector.Expect.Reason, got)
				}
			default:
				t.Fatalf("unknown expected outcome %q", vector.Expect.Outcome)
			}
		})
	}
	if seen < 20 {
		t.Fatalf("expected the production verifier to consume the credential fixture set, saw only %d vectors", seen)
	}
}

func fixtureDecisionByVector(t *testing.T, name string) (Decision, fixtureVector) {
	t.Helper()
	cases := loadFixtureCases(t)
	for _, vector := range cases.Vectors {
		if vector.Name != name {
			continue
		}
		fixture, ok := cases.Decisions[vector.DecisionRef]
		if !ok {
			t.Fatalf("missing decision %s", vector.DecisionRef)
		}
		var decision Decision
		if err := strictDecode([]byte(fixture.Canonical), &decision); err != nil {
			t.Fatal(err)
		}
		return decision, vector
	}
	t.Fatalf("fixture vector %q not found", name)
	return Decision{}, fixtureVector{}
}

func keyFromByte(id string, b byte) SigningKey {
	seed := bytes.Repeat([]byte{b}, ed25519.SeedSize)
	return SigningKey{KeyID: id, PrivateKey: ed25519.NewKeyFromSeed(seed)}
}

func verificationKey(key SigningKey) VerificationKey {
	return VerificationKey{KeyID: key.KeyID, PublicKey: key.PrivateKey.Public().(ed25519.PublicKey)}
}

func TestIssuerAudienceSeparationAndRotation(t *testing.T) {
	decision, vector := fixtureDecisionByVector(t, "harness-one-shot")
	key1 := keyFromByte("prod-test-1", 0x33)
	key2 := keyFromByte("prod-test-2", 0x44)
	current := time.Unix(decision.Windows.IssuedAt+10, 0).UTC()
	clock := ClockFunc(func() time.Time { return current })
	issuer, err := NewIssuer(ControlPlaneIssuer, key1, clock)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewVerifier(ControlPlaneIssuer, []VerificationKey{verificationKey(key1), verificationKey(key2)}, clock)
	if err != nil {
		t.Fatal(err)
	}

	execution, err := issuer.Issue(decision, IssueRequest{Audience: AudienceExecution, Operation: "execution.start", JTI: "issuer-test-execution-0001"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := contextFromFixture(*vector.Verify)
	ctx.Now = current.Unix()
	if _, err := verifier.Verify(execution, mustDecisionJSON(t, decision), ctx); err != nil {
		t.Fatalf("verify execution credential: %v", err)
	}
	modelCtx := ctx
	modelCtx.ExpectedAudience = AudienceModelGateway
	modelCtx.ExpectedOperation = "model.invoke"
	modelCtx.Route = decision.Route
	if _, err := verifier.Verify(execution, mustDecisionJSON(t, decision), modelCtx); Reason(err) != ReasonAudMismatch {
		t.Fatalf("execution credential used at model boundary: got %q", Reason(err))
	}

	current = time.Unix(decision.Windows.AdmissionDeadline+10, 0).UTC()
	model, err := issuer.Issue(decision, IssueRequest{Audience: AudienceModelGateway, Operation: "model.invoke", JTI: "issuer-test-model-0000001"})
	if err != nil {
		t.Fatal(err)
	}
	modelCtx.Now = current.Unix()
	if _, err := verifier.Verify(model, mustDecisionJSON(t, decision), modelCtx); err != nil {
		t.Fatalf("model credential after execution admission window: %v", err)
	}

	if err := issuer.Rotate(key2); err != nil {
		t.Fatal(err)
	}
	current = time.Unix(decision.Windows.IssuedAt+20, 0).UTC()
	rotated, err := issuer.Issue(decision, IssueRequest{Audience: AudienceExecution, Operation: "execution.start", JTI: "issuer-test-rotated-0001"})
	if err != nil {
		t.Fatal(err)
	}
	ctx.Now = current.Unix()
	if _, err := verifier.Verify(rotated, mustDecisionJSON(t, decision), ctx); err != nil {
		t.Fatalf("verify rotated credential during key overlap: %v", err)
	}
	if err := verifier.ReloadKeys([]VerificationKey{verificationKey(key2)}); err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Verify(execution, mustDecisionJSON(t, decision), ctx); Reason(err) != ReasonKidUnknown {
		t.Fatalf("old credential after old key removal: got %q", Reason(err))
	}
}

func TestIssuerRefusesAuthorityExtension(t *testing.T) {
	decision, _ := fixtureDecisionByVector(t, "harness-one-shot")
	key := keyFromByte("prod-test", 0x55)
	clock := ClockFunc(func() time.Time { return time.Unix(decision.Windows.IssuedAt+10, 0).UTC() })
	issuer, err := NewIssuer(ControlPlaneIssuer, key, clock)
	if err != nil {
		t.Fatal(err)
	}
	_, err = issuer.Issue(decision, IssueRequest{
		Audience:  AudienceExecution,
		Operation: "execution.start",
		ExpiresAt: time.Unix(decision.Windows.AdmissionDeadline+30, 0),
		JTI:       "issuer-test-too-long-0001",
	})
	if got := Reason(err); got != ReasonWindowInvalid {
		t.Fatalf("expected %s, got %s", ReasonWindowInvalid, got)
	}
}

func TestTokenRedaction(t *testing.T) {
	token := NewToken("super-secret-bearer")
	if got := token.String(); got == token.Bearer() || bytes.Contains([]byte(got), []byte("super-secret")) {
		t.Fatalf("token string leaked bearer material: %q", got)
	}
	if _, err := token.MarshalText(); err == nil {
		t.Fatal("expected text marshalling to refuse bearer serialization")
	}
}

func TestCanonicalRequestRejectsAmbiguousJSON(t *testing.T) {
	cases := [][]byte{
		[]byte(`{"x":-0}`),
		[]byte(`{"x":1,"x":2}`),
		[]byte(`{"x":"\ud800"}`),
		[]byte(`{"x":1}{"y":2}`),
		[]byte(`{"x":1.5}`),
	}
	for _, input := range cases {
		if _, _, err := CanonicalRequest(input); err == nil {
			t.Fatalf("expected strict canonicalization failure for %s", input)
		}
	}
}

func FuzzVerifierMalformedInput(f *testing.F) {
	f.Add("not-a-token", []byte(`{}`))
	f.Fuzz(func(t *testing.T, compact string, decision []byte) {
		key := keyFromByte("fuzz", 0x66)
		verifier, err := NewVerifier(ControlPlaneIssuer, []VerificationKey{verificationKey(key)}, ClockFunc(func() time.Time { return time.Unix(1790000000, 0) }))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = verifier.Verify(NewToken(compact), decision, VerifyContext{ExpectedAudience: AudienceExecution, ExpectedOperation: "execution.start"})
	})
}

func mustDecisionJSON(t *testing.T, decision Decision) []byte {
	t.Helper()
	data, err := json.Marshal(decision)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
