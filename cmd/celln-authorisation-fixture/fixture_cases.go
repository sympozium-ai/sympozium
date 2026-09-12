package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

const testNow int64 = 1790000000

func sha(label string) string {
	s := sha256.Sum256([]byte(label))
	return "sha256:" + hex.EncodeToString(s[:])
}
func blake(label string) string {
	s := sha256.Sum256([]byte(label))
	return "blake3:" + hex.EncodeToString(s[:])
}
func sp(s string) *string { return &s }

func testPrivateKeys() PrivateKeys {
	return PrivateKeys{Keys: []PrivateKey{
		{Kid: "test-key-1", Alg: "EdDSA", Crv: "Ed25519", SeedHex: strings.Repeat("11", 32), Comment: "NON-PRODUCTION conformance fixture key; never trust in a cluster"},
		{Kid: "test-key-2", Alg: "EdDSA", Crv: "Ed25519", SeedHex: strings.Repeat("22", 32), Comment: "NON-PRODUCTION conformance fixture key; never trust in a cluster"},
	}}
}
func deriveJWKS(pk PrivateKeys) JWKS {
	var out JWKS
	for _, k := range pk.Keys {
		seed, _ := hex.DecodeString(k.SeedHex)
		p := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
		out.Keys = append(out.Keys, JWK{Kty: "OKP", Crv: "Ed25519", Kid: k.Kid, Use: "sig", Alg: "EdDSA", X: base64.RawURLEncoding.EncodeToString(p)})
	}
	return out
}
func privateFor(pk PrivateKeys, kid string) ed25519.PrivateKey {
	for _, k := range pk.Keys {
		if k.Kid == kid {
			seed, _ := hex.DecodeString(k.SeedHex)
			return ed25519.NewKeyFromSeed(seed)
		}
	}
	panic("missing fixture key")
}

func baseRoute() RouteBinding {
	return RouteBinding{ModelConnectionUID: sp("mc-uid-1"), ModelConnectionSpecSHA256: sha("mc-spec"), Provider: "openai", Protocol: "openai-chat", Model: "gpt-test", EndpointOrigin: "https://model.example", Auth: "secret", CredentialSource: &CredentialSource{Kind: "Secret", SecretUID: "secret-uid-1", SecretName: "model-key", SecretKey: "apiKey"}}
}
func noModelRoute() RouteBinding {
	return RouteBinding{Provider: "none", Protocol: "none", Auth: "none"}
}
func starterTools() []ToolBinding {
	return []ToolBinding{
		{Name: "workspace-write", Revision: "r1", Hash: blake("workspace-write-r1"), Limits: Limits{TimeoutMillis: 30000, MemoryBytes: 67108864, ArgumentBytes: 2048, OutputBytes: 65536, Workspace: "none", Effects: "external-side-effects", Artifacts: &ArtifactLimits{Operation: "write", MaxOperations: 4, MaxFiles: 8, MaxFileBytes: 4096, MaxTotalBytes: 16384}}},
		{Name: "https-fetch", Revision: "r1", Hash: blake("https-fetch-r1"), Limits: Limits{TimeoutMillis: 30000, MemoryBytes: 67108864, ArgumentBytes: 2048, OutputBytes: 65536, Workspace: "none", Effects: "external-side-effects", HTTPS: &HTTPSLimits{AllowHosts: []string{"docs.example"}, MaxRequests: 2, MaxResponseBytes: 4096, TimeoutMillis: 5000}}},
	}
}

func requestFor(op, runUID string, parent *ParentBinding, text string) (string, string) {
	m := map[string]any{"apiVersion": "celln.sympozium.ai/execution-request-v1", "operation": op, "runUid": runUID, "payload": text}
	if parent != nil {
		m["parentIncarnation"] = parent.Incarnation
		if parent.TurnID != nil {
			m["turnId"] = *parent.TurnID
		}
	}
	raw, _ := json.Marshal(m)
	c, err := canonicalizeJSON(raw)
	if err != nil {
		panic(err)
	}
	return string(c), sha256Digest(c)
}
func baseDecision(op, lifecycle string, parent *ParentBinding, tools []ToolBinding, route RouteBinding, requestDigest string) Decision {
	b := BudgetBinding{BudgetID: sha("budget-run-1"), RunCap: Cap{Requests: 6, OutputTokens: 3072}, TurnCap: Cap{Requests: 2, OutputTokens: 512}, MaxTurns: 1, TurnDeadlineUnix: testNow + 120}
	if lifecycle != "one-shot" {
		b.MaxTurns = 4
		b.ParentDeadlineUnix = testNow + 300
		if lifecycle == "enduring-initial" {
			b.TurnDeadlineUnix = testNow + 300
		}
	}
	if route.Provider == "none" {
		b.RunCap = Cap{}
		b.TurnCap = Cap{}
	}
	return Decision{APIVersion: decisionVer, Kind: "CellnAuthorisationDecision", ClusterID: "cluster-test-1", Run: RunBinding{Namespace: "tenant-a", NamespaceUID: "ns-uid-a", Name: "run-1", UID: "run-uid-1", SpecSHA256: sha("run-spec")}, Subject: SubjectBinding{Kind: "AgentRun", Namespace: "tenant-a", Name: "run-1", UID: "run-uid-1", SpecSHA256: sha("run-spec")}, Operation: op, Lifecycle: lifecycle, Parent: parent, Runtime: RuntimeBinding{Name: "runtime", UID: "runtime-uid", Revision: "r1", SpecSHA256: sha("runtime-spec")}, Agent: SubjectBinding{Kind: "Agent", Namespace: "tenant-a", Name: "agent", UID: "agent-uid", SpecSHA256: sha("agent-spec")}, Policy: PolicyBinding{Profile: "default", Revision: "p1", Digest: sha("policy")}, Tools: tools, Route: route, Budget: b, Windows: Windows{IssuedAt: testNow, NotBefore: testNow, AdmissionDeadline: testNow + 60}, RequestDigest: requestDigest}
}
func canonicalDecision(d Decision) ([]byte, string) {
	r, _ := json.Marshal(d)
	c, err := canonicalizeJSON(r)
	if err != nil {
		panic(err)
	}
	return c, sha256Digest(c)
}
func claimsFor(d Decision, op, aud, kid string, exp int64) Claims {
	_, dig := canonicalDecision(d)
	turn := ""
	if d.Parent != nil && d.Parent.TurnID != nil {
		turn = *d.Parent.TurnID
	}
	j := sha(kid + op + d.Run.UID + turn + fmt.Sprint(exp))
	return Claims{APIVersion: credentialVer, Iss: issuerName, Aud: aud, Iat: testNow, Nbf: testNow, Exp: exp, Jti: "jti-" + j[len("sha256:"):len("sha256:")+32], DecisionDigest: dig, BudgetID: d.Budget.BudgetID, Operation: op, Subject: ClaimSubject{RunUID: d.Run.UID, TurnID: parentTurnID(d.Parent), ParentIncarnation: parentIncarnation(d.Parent)}}
}
func signClaims(c Claims, kid string, pk PrivateKeys, headerKid string, tamper bool) string {
	if headerKid == "" {
		headerKid = kid
	}
	h := fmt.Sprintf(`{"alg":"EdDSA","typ":"%s","kid":"%s"}`, jwsTyp, headerKid)
	p, _ := json.Marshal(c)
	pc, _ := canonicalizeJSON(p)
	hb := base64.RawURLEncoding.EncodeToString([]byte(h))
	pb := base64.RawURLEncoding.EncodeToString(pc)
	input := hb + "." + pb
	sig := ed25519.Sign(privateFor(pk, kid), []byte(input))
	if tamper {
		sig[0] ^= 0xff
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(sig)
}
func contextFor(d Decision, op, aud string, now int64) VerifyContext {
	return VerifyContext{Now: now, ExpectedAudience: aud, ExpectedOperation: op, ClusterID: d.ClusterID, Namespace: d.Run.Namespace, NamespaceUID: d.Run.NamespaceUID, RunUID: d.Run.UID, RunSpecSHA256: d.Run.SpecSHA256, Parent: d.Parent, RequestDigest: d.RequestDigest, Route: d.Route, BudgetID: d.Budget.BudgetID}
}
func resolverFor(d Decision) ResolverObserved {
	limits := make([]Limits, len(d.Tools))
	for i, t := range d.Tools {
		limits[i] = t.Limits
	}
	return ResolverObserved{PolicyPresent: true, PolicyDigest: d.Policy.Digest, ToolOrder: append([]ToolBinding{}, d.Tools...), PolicyLimits: limits}
}

func cloneDecision(in Decision) Decision {
	b, _ := json.Marshal(in)
	var out Decision
	_ = json.Unmarshal(b, &out)
	return out
}
func cloneResolver(in ResolverObserved) ResolverObserved {
	b, _ := json.Marshal(in)
	var out ResolverObserved
	_ = json.Unmarshal(b, &out)
	return out
}
func makeVerifyVector(name string, d Decision, req string, op, aud, kid string, exp, now int64, expect Expect, mut func(*Claims, *VerifyContext)) Vector {
	d = cloneDecision(d)
	pk := testPrivateKeys()
	c := claimsFor(d, op, aud, kid, exp)
	ctx := contextFor(d, op, aud, now)
	if mut != nil {
		mut(&c, &ctx)
	}
	canon, dig := canonicalDecision(d)
	return Vector{Name: name, Evaluator: "verify", Decision: d, DecisionCanonical: string(canon), DecisionDigest: dig, RequestCanonical: req, Credential: signClaims(c, kid, pk, "", false), Verify: &ctx, Expect: expect}
}
func makeResolverVector(name string, d Decision, req string, expect Expect, mut func(*ResolverObserved)) Vector {
	d = cloneDecision(d)
	o := cloneResolver(resolverFor(d))
	if mut != nil {
		mut(&o)
	}
	canon, dig := canonicalDecision(d)
	return Vector{Name: name, Evaluator: "resolver", Decision: d, DecisionCanonical: string(canon), DecisionDigest: dig, RequestCanonical: req, Resolver: &o, Expect: expect}
}

func buildCases() Cases {
	var v []Vector
	reqDirect, rdDirect := requestFor("execution.start", "run-uid-1", nil, "direct-input")
	direct := baseDecision("execution.start", "one-shot", nil, nil, noModelRoute(), rdDirect)
	v = append(v, makeVerifyVector("direct-one-shot", direct, reqDirect, "execution.start", audExecution, "test-key-1", testNow+60, testNow+10, Expect{Outcome: "accept"}, nil))

	reqHarness, rdHarness := requestFor("execution.start", "run-uid-1", nil, "harness-task")
	harness := baseDecision("execution.start", "one-shot", nil, starterTools(), baseRoute(), rdHarness)
	v = append(v, makeVerifyVector("harness-one-shot", harness, reqHarness, "execution.start", audExecution, "test-key-1", testNow+60, testNow+10, Expect{Outcome: "accept"}, nil))
	v = append(v, makeVerifyVector("model-after-admission-window", harness, reqHarness, "model.invoke", audModel, "test-key-1", testNow+120, testNow+70, Expect{Outcome: "accept"}, nil))
	v = append(v, makeVerifyVector("issued-permit-after-policy-withdrawal", harness, reqHarness, "execution.start", audExecution, "test-key-1", testNow+60, testNow+20, Expect{Outcome: "accept"}, nil))

	p0 := &ParentBinding{Incarnation: blake("parent-1")}
	reqParent, rdParent := requestFor("execution.start", "run-uid-1", p0, "create-parent")
	parentCreate := baseDecision("execution.start", "enduring-initial", p0, starterTools(), baseRoute(), rdParent)
	v = append(v, makeVerifyVector("parent-create", parentCreate, reqParent, "execution.start", audExecution, "test-key-1", testNow+60, testNow+10, Expect{Outcome: "accept"}, nil))
	t1 := "turn-1"
	p1 := &ParentBinding{Incarnation: p0.Incarnation, TurnID: &t1}
	reqT1, rdT1 := requestFor("execution.turn", "run-uid-1", p1, "remember violet")
	turn1 := baseDecision("execution.turn", "enduring-turn", p1, starterTools(), baseRoute(), rdT1)
	v = append(v, makeVerifyVector("enduring-turn", turn1, reqT1, "execution.turn", audExecution, "test-key-1", testNow+60, testNow+10, Expect{Outcome: "accept"}, nil))
	v = append(v, makeVerifyVector("enduring-model-after-admission-window", turn1, reqT1, "model.invoke", audModel, "test-key-1", testNow+120, testNow+70, Expect{Outcome: "accept"}, nil))

	reqCleanup, rdCleanup := requestFor("execution.cleanup", "run-uid-1", p0, "stop")
	cleanup := baseDecision("execution.cleanup", "enduring-initial", p0, nil, noModelRoute(), rdCleanup)
	v = append(v, makeVerifyVector("cleanup-after-policy-withdrawal", cleanup, reqCleanup, "execution.cleanup", audExecution, "test-key-1", testNow+200, testNow+150, Expect{Outcome: "accept"}, nil))

	v = append(v, makeVerifyVector("request-content-changed", turn1, reqT1, "execution.turn", audExecution, "test-key-1", testNow+60, testNow+10, Expect{Outcome: "reject", Reason: ReasonRequestBinding}, func(c *Claims, x *VerifyContext) { x.RequestDigest = sha("different-turn-message") }))
	v = append(v, makeVerifyVector("wrong-endpoint-audience", turn1, reqT1, "model.invoke", audModel, "test-key-1", testNow+120, testNow+10, Expect{Outcome: "reject", Reason: ReasonAudMismatch}, func(c *Claims, x *VerifyContext) { x.ExpectedAudience = audExecution }))
	v = append(v, makeVerifyVector("admission-expired", turn1, reqT1, "execution.turn", audExecution, "test-key-1", testNow+65, testNow+66, Expect{Outcome: "reject", Reason: ReasonAdmissionExpired}, nil))
	v = append(v, makeVerifyVector("model-work-deadline-expired", turn1, reqT1, "model.invoke", audModel, "test-key-1", testNow+120, testNow+130, Expect{Outcome: "reject", Reason: ReasonDeadlineExpired}, nil))
	v = append(v, makeVerifyVector("model-token-beyond-work-deadline", turn1, reqT1, "model.invoke", audModel, "test-key-1", testNow+200, testNow+10, Expect{Outcome: "reject", Reason: ReasonWindowInvalid}, nil))
	v = append(v, makeVerifyVector("budget-id-mismatch", turn1, reqT1, "execution.turn", audExecution, "test-key-1", testNow+60, testNow+10, Expect{Outcome: "reject", Reason: ReasonBudgetMismatch}, func(c *Claims, x *VerifyContext) { x.BudgetID = sha("other-budget") }))
	v = append(v, makeVerifyVector("namespace-uid-mismatch", turn1, reqT1, "execution.turn", audExecution, "test-key-1", testNow+60, testNow+10, Expect{Outcome: "reject", Reason: ReasonNamespaceMismatch}, func(c *Claims, x *VerifyContext) { x.NamespaceUID = "other-ns" }))
	v = append(v, makeVerifyVector("parent-turn-mismatch", turn1, reqT1, "execution.turn", audExecution, "test-key-1", testNow+60, testNow+10, Expect{Outcome: "reject", Reason: ReasonParentTurn}, func(c *Claims, x *VerifyContext) {
		q := "turn-x"
		x.Parent = &ParentBinding{Incarnation: p0.Incarnation, TurnID: &q}
	}))
	v = append(v, makeVerifyVector("model-route-mismatch", turn1, reqT1, "model.invoke", audModel, "test-key-1", testNow+120, testNow+10, Expect{Outcome: "reject", Reason: ReasonRouteMismatch}, func(c *Claims, x *VerifyContext) { x.Route.Model = "other" }))
	v = append(v, makeVerifyVector("admission-replay-recovers", turn1, reqT1, "execution.turn", audExecution, "test-key-1", testNow+60, testNow+10, Expect{Outcome: "recover", Reason: ReasonAdmissionReplay}, func(c *Claims, x *VerifyContext) { x.SeenAdmissionJTI = true }))

	badWindow := turn1
	badWindow.Windows.AdmissionDeadline = testNow + 86400
	v = append(v, makeVerifyVector("invalid-admission-window", badWindow, reqT1, "execution.turn", audExecution, "test-key-1", testNow+60, testNow+10, Expect{Outcome: "reject", Reason: ReasonWindowInvalid}, nil))
	badBudget := turn1
	badBudget.Budget.TurnCap.Requests = 7
	v = append(v, makeVerifyVector("turn-cap-exceeds-run-cap", badBudget, reqT1, "execution.turn", audExecution, "test-key-1", testNow+60, testNow+10, Expect{Outcome: "reject", Reason: ReasonBudgetMismatch}, nil))
	badStream := turn1
	badStream.Route.Streaming = true
	v = append(v, makeVerifyVector("streaming-unsupported", badStream, reqT1, "execution.turn", audExecution, "test-key-1", testNow+60, testNow+10, Expect{Outcome: "reject", Reason: ReasonStreaming}, nil))

	v = append(v, makeVerifyVector("rotation-key-2", harness, reqHarness, "execution.start", audExecution, "test-key-2", testNow+60, testNow+10, Expect{Outcome: "accept"}, nil))
	unknown := makeVerifyVector("unknown-kid", harness, reqHarness, "execution.start", audExecution, "test-key-1", testNow+60, testNow+10, Expect{Outcome: "reject", Reason: ReasonKidUnknown}, nil)
	c := claimsFor(harness, "execution.start", audExecution, "test-key-1", testNow+60)
	unknown.Credential = signClaims(c, "test-key-1", testPrivateKeys(), "not-configured", false)
	v = append(v, unknown)
	tampered := makeVerifyVector("tampered-signature", harness, reqHarness, "execution.start", audExecution, "test-key-1", testNow+60, testNow+10, Expect{Outcome: "reject", Reason: ReasonSigInvalid}, nil)
	c = claimsFor(harness, "execution.start", audExecution, "test-key-1", testNow+60)
	tampered.Credential = signClaims(c, "test-key-1", testPrivateKeys(), "", true)
	v = append(v, tampered)

	v = append(v, makeResolverVector("resolver-policy-valid", harness, reqHarness, Expect{Outcome: "accept"}, nil))
	v = append(v, makeResolverVector("resolver-policy-removed", harness, reqHarness, Expect{Outcome: "reject", Reason: ReasonPolicyWithdrawn}, func(o *ResolverObserved) { o.PolicyPresent = false }))
	v = append(v, makeResolverVector("resolver-artifact-contracted", harness, reqHarness, Expect{Outcome: "reject", Reason: ReasonPolicyContracted}, func(o *ResolverObserved) { o.PolicyLimits[0].Artifacts.MaxTotalBytes = 1024 }))
	v = append(v, makeResolverVector("resolver-https-host-contracted", harness, reqHarness, Expect{Outcome: "reject", Reason: ReasonPolicyContracted}, func(o *ResolverObserved) { o.PolicyLimits[1].HTTPS.AllowHosts = []string{"other.example"} }))
	v = append(v, makeResolverVector("resolver-tool-reordered", harness, reqHarness, Expect{Outcome: "reject", Reason: ReasonToolOrder}, func(o *ResolverObserved) { o.ToolOrder[0], o.ToolOrder[1] = o.ToolOrder[1], o.ToolOrder[0] }))

	seq := Sequence{Name: "model-budget-idempotency-across-turns", RunCap: Cap{Requests: 3, OutputTokens: 1536}, TurnCap: Cap{Requests: 2, OutputTokens: 1024}, Actions: []SequenceAction{
		{TurnID: "t1", ID: "a", Digest: sha("a"), ReserveOutput: 512, Expect: "accept"},
		{TurnID: "t1", ID: "b", Digest: sha("b"), ReserveOutput: 512, Expect: "accept"},
		{TurnID: "t1", ID: "a", Digest: sha("a"), ReserveOutput: 512, Expect: "recover"},
		{TurnID: "t1", ID: "a", Digest: sha("altered"), ReserveOutput: 512, Expect: ReasonRequestConflict},
		{TurnID: "t2", ID: "c", Digest: sha("c"), ReserveOutput: 512, Expect: "accept"},
		{TurnID: "t2", ID: "d", Digest: sha("d"), ReserveOutput: 512, Expect: ReasonBudgetExhausted},
	}}
	decisions := map[string]DecisionFixture{}
	for i := range v {
		ref := v[i].DecisionDigest
		decisions[ref] = DecisionFixture{Canonical: v[i].DecisionCanonical, RequestCanonical: v[i].RequestCanonical}
		v[i].DecisionRef = ref
	}
	return Cases{APIVersion: "celln.sympozium.ai/conformance-cases-v2", Decisions: decisions, Vectors: v, Sequences: []Sequence{seq}}
}

func marshalIndent(v any) []byte {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		panic(err)
	}
	return append(b, '\n')
}
