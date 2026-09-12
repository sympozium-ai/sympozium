package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// This file generates the shared conformance fixtures. Test keys are
// deterministic and explicitly non-production.

const testNow int64 = 1790000000

const (
	nsName = "celln-tenant-a"
)

func sha(label string) string   { return "sha256:" + hex64(label) }
func blake(label string) string { return "blake3:" + hex64(label) }

func hex64(label string) string {
	sum := sha256.Sum256([]byte(label))
	return hex.EncodeToString(sum[:])
}

func testPrivateKeys() PrivateKeys {
	keys := []struct{ kid, seed string }{
		{"test-key-1", strings.Repeat("11", 32)},
		{"test-key-2", strings.Repeat("22", 32)},
	}
	out := PrivateKeys{Keys: make([]PrivateKey, 0, len(keys))}
	for _, k := range keys {
		out.Keys = append(out.Keys, PrivateKey{
			Kid: k.kid, Alg: "EdDSA", Crv: "Ed25519", SeedHex: k.seed,
			Prod: false, Comment: "NON-PRODUCTION conformance fixture key; never trust in a cluster",
		})
	}
	return out
}

func deriveJWKS(pk PrivateKeys) JWKS {
	out := JWKS{}
	for _, k := range pk.Keys {
		seed, _ := hex.DecodeString(k.SeedHex)
		priv := ed25519.NewKeyFromSeed(seed)
		pub := priv.Public().(ed25519.PublicKey)
		out.Keys = append(out.Keys, JWK{
			Kty: "OKP", Crv: "Ed25519", Kid: k.Kid, Use: "sig", Alg: "EdDSA",
			X: base64.RawURLEncoding.EncodeToString(pub),
		})
	}
	return out
}

func privateKeyFor(pk PrivateKeys, kid string) (ed25519.PrivateKey, error) {
	for _, k := range pk.Keys {
		if k.Kid == kid {
			seed, err := hex.DecodeString(k.SeedHex)
			if err != nil {
				return nil, err
			}
			return ed25519.NewKeyFromSeed(seed), nil
		}
	}
	return nil, fmt.Errorf("unknown test kid %q", kid)
}

type vectorSpec struct {
	name     string
	category string
	decision Decision
	claims   Claims
	observed Observed
	expect   Expect
	mode     string // normal | staleDigest | duplicateJSON | unknownField | unknownAlg | tamperSig | oversized
	kid      string
	binding  string // when set, overrides the recomputed requestBinding after finalize
}

func baseRoute() RouteBinding {
	uid := "mc-uid-0001"
	return RouteBinding{
		ModelConnectionUID:        &uid,
		ModelConnectionSpecSHA256: sha("modelconnection-spec"),
		Provider:                  "openai",
		Protocol:                  "openai-chat",
		Model:                     "gpt-4o-mini",
		EndpointOrigin:            "https://api.openai.com",
		Streaming:                 false,
		CredentialSource: &CredentialSource{
			Kind: "ModelConnection", SecretUID: "secret-uid-0001",
			SecretName: "openai-key", SecretKey: "apiKey",
		},
	}
}

func noModelRoute() RouteBinding {
	return RouteBinding{
		ModelConnectionUID:        nil,
		ModelConnectionSpecSHA256: "",
		Provider:                  "none",
		Protocol:                  "none",
		Model:                     "",
		EndpointOrigin:            "",
		Streaming:                 false,
		CredentialSource:          nil,
	}
}

func tool(name, rev string) ToolBinding {
	return ToolBinding{
		Name: name, Revision: rev, Hash: blake(name + "-" + rev),
		Limits: Limits{TimeoutMillis: 30000, MemoryBytes: 67108864, TaskBytes: 2048, OutputBytes: 65536, Workspace: "none"},
	}
}

func baseDecision(op, lifecycle string, parent *ParentBinding, tools []ToolBinding, route RouteBinding) Decision {
	turn := (*string)(nil)
	if parent != nil {
		turn = parent.TurnID
	}
	_ = turn
	return Decision{
		APIVersion: decisionVer,
		Kind:       "CellnAuthorisationDecision",
		Run: RunBinding{
			Namespace: nsName, NamespaceUID: "ns-uid-aaaa",
			Name: "run-1", UID: "run-uid-0001", SpecSHA256: sha("run-spec"),
		},
		Subject: SubjectBinding{
			Kind: "Agent", Namespace: nsName, Name: "agent-1",
			UID: "agent-uid-0001", SpecSHA256: sha("agent-spec"),
		},
		Operation: op,
		Lifecycle: lifecycle,
		Parent:    parent,
		Runtime: RuntimeBinding{
			Name: "runtime-1", UID: "runtime-uid-0001",
			Revision: "rev-1", SpecSHA256: sha("runtime-spec"),
		},
		Agent: SubjectBinding{
			Kind: "Agent", Namespace: nsName, Name: "agent-1",
			UID: "agent-uid-0001", SpecSHA256: sha("agent-spec"),
		},
		Policy: PolicyBinding{Profile: "prof-1", Revision: "policy-rev-7", Digest: sha("policy")},
		Tools:  tools,
		Route:  route,
		Budget: BudgetBinding{
			BudgetID:     sha("budget"),
			RunCap:       Cap{Requests: 6, OutputTokens: 3072},
			TurnCap:      Cap{Requests: 2, OutputTokens: 512},
			DeadlineUnix: testNow + 123,
		},
		Windows: Windows{
			IssuedAt: testNow, NotBefore: testNow, Expiry: testNow + 123,
			AdmissionDeadline: testNow + 60,
		},
	}
}

func claimsFor(d Decision, kid string) Claims {
	aud := requiredAudience(d.Operation)
	return Claims{
		APIVersion: credentialVer, Iss: issuerName, Aud: aud,
		Iat: d.Windows.IssuedAt, Nbf: d.Windows.NotBefore, Exp: d.Windows.Expiry,
		Jti:       "jti-" + hex64(kid + d.Operation + d.Run.UID)[:32],
		BudgetID:  d.Budget.BudgetID,
		Operation: d.Operation,
		Subject: ClaimSubject{
			RunUID:            d.Run.UID,
			TurnID:            parentTurnID(d.Parent),
			ParentIncarnation: parentIncarnation(d.Parent),
		},
	}
}

func observedFor(d Decision) Observed {
	limits := make([]Limits, 0, len(d.Tools))
	for _, t := range d.Tools {
		limits = append(limits, t.Limits)
	}
	var parent *ParentBinding
	if d.Parent != nil {
		p := *d.Parent
		parent = &p
	}
	route := d.Route
	if d.Route.CredentialSource != nil {
		cs := *d.Route.CredentialSource
		route.CredentialSource = &cs
	}
	return Observed{
		Now:           testNow,
		Namespace:     d.Run.Namespace,
		NamespaceUID:  d.Run.NamespaceUID,
		RunUID:        d.Run.UID,
		RunSpecSHA256: d.Run.SpecSHA256,
		Parent:        parent,
		PolicyPresent: true,
		PolicyDigest:  d.Policy.Digest,
		PolicyLimits:  limits,
		ToolOrder:     append([]ToolBinding{}, d.Tools...),
		Route:         route,
		BudgetID:      d.Budget.BudgetID,
		SeenJti:       false,
	}
}

func finalizeBinding(d *Decision) {
	rb, err := requestBinding(*d)
	if err != nil {
		panic(err)
	}
	d.RequestBinding = rb
}

func buildSpecs() []vectorSpec {
	var specs []vectorSpec

	add := func(s vectorSpec) {
		if s.kid == "" {
			s.kid = "test-key-1"
		}
		finalizeBinding(&s.decision)
		if s.binding != "" {
			s.decision.RequestBinding = s.binding
		}
		if s.claims.APIVersion == "" {
			s.claims = claimsFor(s.decision, s.kid)
		}
		if s.observed.Now == 0 {
			s.observed = observedFor(s.decision)
		}
		specs = append(specs, s)
	}

	// --- positives -------------------------------------------------------
	direct := baseDecision("execution.start", "one-shot", nil, []ToolBinding{}, noModelRoute())
	add(vectorSpec{name: "one-shot-direct-no-model", category: "accept", decision: direct, expect: Expect{Outcome: "accept"}})

	harness := baseDecision("execution.start", "one-shot", nil, []ToolBinding{tool("k8s-read", "rev-3")}, baseRoute())
	add(vectorSpec{name: "harness-one-shot", category: "accept", decision: harness, expect: Expect{Outcome: "accept"}})

	parentCreate := baseDecision("execution.turn", "enduring-initial", &ParentBinding{Incarnation: blake("incarnation"), TurnID: nil}, []ToolBinding{tool("k8s-read", "rev-3")}, baseRoute())
	add(vectorSpec{name: "parent-create", category: "accept", decision: parentCreate, expect: Expect{Outcome: "accept"}})

	t1 := "t1"
	initial := baseDecision("execution.turn", "enduring-turn", &ParentBinding{Incarnation: blake("incarnation"), TurnID: &t1}, []ToolBinding{tool("k8s-read", "rev-3")}, baseRoute())
	add(vectorSpec{name: "enduring-initial-turn", category: "accept", decision: initial, expect: Expect{Outcome: "accept"}})

	t2 := "t2"
	subsequent := baseDecision("execution.turn", "enduring-turn", &ParentBinding{Incarnation: blake("incarnation"), TurnID: &t2}, []ToolBinding{tool("k8s-read", "rev-3")}, baseRoute())
	add(vectorSpec{name: "enduring-subsequent-turn", category: "accept", decision: subsequent, expect: Expect{Outcome: "accept"}})

	read := baseDecision("execution.read", "enduring-turn", &ParentBinding{Incarnation: blake("incarnation"), TurnID: &t1}, []ToolBinding{tool("k8s-read", "rev-3")}, baseRoute())
	add(vectorSpec{name: "owner-read", category: "accept", decision: read, expect: Expect{Outcome: "accept"}})

	cleanup := baseDecision("execution.cleanup", "enduring-turn", &ParentBinding{Incarnation: blake("incarnation"), TurnID: &t1}, []ToolBinding{tool("k8s-read", "rev-3")}, baseRoute())
	add(vectorSpec{name: "owner-cleanup", category: "accept", decision: cleanup, expect: Expect{Outcome: "accept"}})

	modelInvoke := baseDecision("model.invoke", "enduring-turn", &ParentBinding{Incarnation: blake("incarnation"), TurnID: &t1}, []ToolBinding{tool("k8s-read", "rev-3")}, baseRoute())
	add(vectorSpec{name: "model-invoke", category: "accept", decision: modelInvoke, expect: Expect{Outcome: "accept"}})

	// key rotation: a second configured kid is accepted.
	rot := baseDecision("execution.start", "one-shot", nil, []ToolBinding{tool("k8s-read", "rev-3")}, baseRoute())
	add(vectorSpec{name: "rotation-key-2", category: "accept", decision: rot, kid: "test-key-2", expect: Expect{Outcome: "accept"}})

	// --- negatives -------------------------------------------------------
	neg := func(name string, mut func(d *Decision, c *Claims, o *Observed), reason string) {
		d := baseDecision("execution.start", "one-shot", nil, []ToolBinding{tool("k8s-read", "rev-3")}, baseRoute())
		c := claimsFor(d, "test-key-1")
		o := observedFor(d)
		mut(&d, &c, &o)
		add(vectorSpec{name: name, category: "reject", decision: d, claims: c, observed: o,
			expect: Expect{Outcome: "reject", Reason: reason}})
	}

	neg("namespace-uid-mismatch", func(d *Decision, c *Claims, o *Observed) {
		d.Run.NamespaceUID = "ns-uid-other"
	}, ReasonNamespaceMismatch)

	neg("run-uid-mismatch", func(d *Decision, c *Claims, o *Observed) {
		d.Run.UID = "run-uid-other"
		c.Subject.RunUID = "run-uid-other"
	}, ReasonRunMismatch)

	neg("parent-incarnation-mismatch", func(d *Decision, c *Claims, o *Observed) {
		tid := "t1"
		d.Parent = &ParentBinding{Incarnation: blake("other-incarnation"), TurnID: &tid}
		c.Subject.ParentIncarnation = &d.Parent.Incarnation
		c.Subject.TurnID = &tid
	}, ReasonParentTurn)

	neg("turn-id-mismatch", func(d *Decision, c *Claims, o *Observed) {
		tid := "t1"
		other := "t9"
		d.Parent = &ParentBinding{Incarnation: blake("incarnation"), TurnID: &tid}
		c.Subject.ParentIncarnation = &d.Parent.Incarnation
		c.Subject.TurnID = &other
	}, ReasonSubjectMismatch)

	neg("operation-mismatch", func(d *Decision, c *Claims, o *Observed) {
		c.Operation = "execution.read"
	}, ReasonOperationMismatch)

	neg("audience-mismatch", func(d *Decision, c *Claims, o *Observed) {
		c.Aud = audModel
	}, ReasonAudMismatch)

	neg("subject-mismatch", func(d *Decision, c *Claims, o *Observed) {
		c.Subject.RunUID = "run-uid-other"
	}, ReasonSubjectMismatch)

	neg("budget-id-mismatch", func(d *Decision, c *Claims, o *Observed) {
		c.BudgetID = sha("other-budget")
	}, ReasonBudgetMismatch)

	neg("route-mismatch", func(d *Decision, c *Claims, o *Observed) {
		d.Route.Model = "gpt-4o"
	}, ReasonRouteMismatch)

	neg("credential-source-mismatch", func(d *Decision, c *Claims, o *Observed) {
		d.Route.CredentialSource.SecretUID = "secret-uid-other"
	}, ReasonCredSourceMismatch)

	{
		d := baseDecision("execution.start", "one-shot", nil, []ToolBinding{tool("k8s-read", "rev-3")}, baseRoute())
		c := claimsFor(d, "test-key-1")
		o := observedFor(d)
		add(vectorSpec{name: "request-binding-mismatch", category: "reject", decision: d, claims: c, observed: o,
			binding: sha("bad-binding"), expect: Expect{Outcome: "reject", Reason: ReasonRequestBinding}})
	}
	neg("policy-contracted", func(d *Decision, c *Claims, o *Observed) {
		d.Tools[0].Limits.TimeoutMillis = 60000
	}, ReasonPolicyContracted)

	neg("policy-removed", func(d *Decision, c *Claims, o *Observed) {
		o.PolicyPresent = false
	}, ReasonPolicyWithdrawn)

	neg("limit-zero", func(d *Decision, c *Claims, o *Observed) {
		d.Tools[0].Limits.TimeoutMillis = 0
	}, ReasonLimitRange)

	neg("limit-missing", func(d *Decision, c *Claims, o *Observed) {
		d.Tools[0].Limits.OutputBytes = 0
	}, ReasonLimitRange)

	neg("limit-out-of-range", func(d *Decision, c *Claims, o *Observed) {
		d.Tools[0].Limits.MemoryBytes = 1 << 40
		o.PolicyLimits[0].MemoryBytes = 1 << 40
	}, ReasonLimitRange)

	neg("tool-reordered", func(d *Decision, c *Claims, o *Observed) {
		d.Tools = []ToolBinding{tool("k8s-write", "rev-2"), tool("k8s-read", "rev-3")}
		o.ToolOrder = []ToolBinding{tool("k8s-read", "rev-3"), tool("k8s-write", "rev-2")}
		o.PolicyLimits = []Limits{d.Tools[1].Limits, d.Tools[0].Limits}
	}, ReasonToolOrder)

	neg("tool-revision-unknown", func(d *Decision, c *Claims, o *Observed) {
		d.Tools[0].Revision = "rev-9"
	}, ReasonToolUnknown)

	neg("streaming-unsupported", func(d *Decision, c *Claims, o *Observed) {
		d.Route.Streaming = true
		o.Route.Streaming = true
	}, ReasonStreaming)

	neg("protocol-unsupported", func(d *Decision, c *Claims, o *Observed) {
		d.Route.Protocol = "grpc"
		o.Route.Protocol = "grpc"
	}, ReasonProtocol)

	neg("duplicate-decision", func(d *Decision, c *Claims, o *Observed) {
		o.SeenJti = true
	}, ReasonReplay)

	neg("issuer-mismatch", func(d *Decision, c *Claims, o *Observed) {
		c.Iss = "evil-issuer"
	}, ReasonIssMismatch)

	neg("expired-token", func(d *Decision, c *Claims, o *Observed) {
		c.Exp = o.Now - 10
	}, ReasonTimeExpired)

	neg("future-token", func(d *Decision, c *Claims, o *Observed) {
		c.Nbf = o.Now + 100
	}, ReasonTimeNotYetValid)

	neg("unknown-version", func(d *Decision, c *Claims, o *Observed) {
		d.APIVersion = "celln.sympozium.ai/authorisation-decision-v0"
	}, ReasonVersionUnsupported)

	// Altering the signed decision without re-signing -> signature/parse modes.
	negMode := func(name, mode, reason string) {
		d := baseDecision("execution.start", "one-shot", nil, []ToolBinding{tool("k8s-read", "rev-3")}, baseRoute())
		finalizeBinding(&d)
		c := claimsFor(d, "test-key-1")
		o := observedFor(d)
		add(vectorSpec{name: name, category: "reject", decision: d, claims: c, observed: o,
			mode: mode, expect: Expect{Outcome: "reject", Reason: reason}})
	}
	negMode("unknown-algorithm", "unknownAlg", ReasonAlgUnsupported)
	negMode("tampered-signature", "tamperSig", ReasonSigInvalid)
	negMode("stale-decision-digest", "staleDigest", ReasonDigestMismatch)
	negMode("unknown-payload-field", "unknownField", ReasonUnknownField)
	negMode("duplicate-json-key", "duplicateJSON", ReasonDuplicateJSONKey)
	negMode("unknown-kid", "unknownKid", ReasonKidUnknown)
	negMode("oversized-credential", "oversized", ReasonSizeExceeded)

	return specs
}

// ---------------------------------------------------------------------------
// Emission
// ---------------------------------------------------------------------------

func marshalIndent(v any) []byte {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		panic(err)
	}
	return append(b, '\n')
}

func makeCredential(d Decision, c Claims, kid, mode string, pk PrivateKeys) (string, []byte, []byte, string, error) {
	decisionRaw, err := json.Marshal(d)
	if err != nil {
		return "", nil, nil, "", err
	}
	canonical, err := canonicalizeJSON(decisionRaw)
	if err != nil {
		return "", nil, nil, "", err
	}
	digest := sha256Digest(canonical)
	switch mode {
	case "staleDigest":
		c.DecisionDigest = sha("stale")
	default:
		c.DecisionDigest = digest
	}
	claimsRaw, err := json.Marshal(c)
	if err != nil {
		return "", nil, nil, "", err
	}
	payload, err := canonicalizeJSON(claimsRaw)
	if err != nil {
		return "", nil, nil, "", err
	}
	switch mode {
	case "duplicateJSON":
		// Inject a duplicate "iss" key before the closing brace.
		payload = append(payload[:len(payload)-1], []byte(`,"iss":"evil"}`)...)
	case "unknownField":
		payload = append(payload[:len(payload)-1], []byte(`,"zzz":1}`)...)
	}
	alg := "EdDSA"
	if mode == "unknownAlg" {
		alg = "RS256"
	}
	headerKid := kid
	if mode == "unknownKid" {
		headerKid = "no-such-kid"
	}
	header := fmt.Sprintf(`{"alg":%q,"typ":%q,"kid":%q}`, alg, jwsTyp, headerKid)
	headerB64 := base64.RawURLEncoding.EncodeToString([]byte(header))
	payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
	signingInput := headerB64 + "." + payloadB64

	priv, err := privateKeyFor(pk, kid)
	if err != nil {
		return "", nil, nil, "", err
	}
	sig := ed25519.Sign(priv, []byte(signingInput))
	if mode == "tamperSig" {
		sig[0] ^= 0xff
	}
	sigB64 := base64.RawURLEncoding.EncodeToString(sig)
	compact := signingInput + "." + sigB64
	if mode == "oversized" {
		compact = strings.Repeat("A", maxCompactSize+1)
	}
	return compact, canonical, []byte(digest), digest, nil
}

func generateFixtures(dir string) error {
	pk := testPrivateKeys()
	jwks := deriveJWKS(pk)
	if err := os.MkdirAll(filepath.Join(dir, "signing"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "signing", "test-jwks.json"), marshalIndent(jwks), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "signing", "test-private-keys.json"), marshalIndent(pk), 0o600); err != nil {
		return err
	}

	specs := buildSpecs()
	manifest := Manifest{
		APIVersion: "celln.sympozium.ai/conformance-manifest-v1",
		Contract:   "docs/design/celln-namespace-authorisation.md",
	}
	for _, s := range specs {
		vdir := filepath.Join(dir, "vectors", s.name)
		if err := os.MkdirAll(vdir, 0o755); err != nil {
			return err
		}
		compact, canonical, declared, digest, err := makeCredential(s.decision, s.claims, s.kid, s.mode, pk)
		if err != nil {
			return fmt.Errorf("%s: %w", s.name, err)
		}
		if err := os.WriteFile(filepath.Join(vdir, "decision.json"), marshalIndent(s.decision), 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(vdir, "decision.canonical"), canonical, 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(vdir, "decision.digest"), []byte(digest+"\n"), 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(vdir, "credential.jws"), []byte(compact+"\n"), 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(vdir, "observed.json"), marshalIndent(s.observed), 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(vdir, "expect.json"), marshalIndent(s.expect), 0o644); err != nil {
			return err
		}
		_ = declared
		manifest.Vectors = append(manifest.Vectors, ManifestVector{
			Name: s.name, Category: s.category, Outcome: s.expect.Outcome, Reason: s.expect.Reason,
		})
	}
	sort.Slice(manifest.Vectors, func(i, j int) bool { return manifest.Vectors[i].Name < manifest.Vectors[j].Name })

	// Bundle hash first over all fixture files except the manifest and bundle
	// outputs, so it is stable and deterministic.
	sums, err := computeSums(dir, map[string]bool{
		"manifest.json":        true,
		"bundle/SHA256SUMS":    true,
		"bundle/BUNDLE.sha256": true,
	})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, "bundle"), 0o755); err != nil {
		return err
	}
	var sumsBuf bytes.Buffer
	for _, s := range sums {
		fmt.Fprintf(&sumsBuf, "%s  %s\n", s.hash, s.rel)
	}
	if err := os.WriteFile(filepath.Join(dir, "bundle", "SHA256SUMS"), sumsBuf.Bytes(), 0o644); err != nil {
		return err
	}
	bundleSum := sha256.Sum256(sumsBuf.Bytes())
	manifest.BundleHash = "sha256:" + hex.EncodeToString(bundleSum[:])
	if err := os.WriteFile(filepath.Join(dir, "bundle", "BUNDLE.sha256"), []byte(manifest.BundleHash+"\n"), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "manifest.json"), marshalIndent(manifest), 0o644)
}

type sumEntry struct {
	rel  string
	hash string
}

func computeSums(dir string, skip map[string]bool) ([]sumEntry, error) {
	var out []sumEntry
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		rel = filepath.ToSlash(rel)
		if skip[rel] || strings.HasSuffix(rel, ".md") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		out = append(out, sumEntry{rel: rel, hash: hex.EncodeToString(sum[:])})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out, nil
}
