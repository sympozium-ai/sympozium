package cellnauthority

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type noSecretReader struct {
	client.Reader
	secretReads int
}

type changingPlatformReader struct {
	client.Reader
	agentReads int
}

func (r *changingPlatformReader) Get(ctx context.Context, key client.ObjectKey, object client.Object, options ...client.GetOption) error {
	if err := r.Reader.Get(ctx, key, object, options...); err != nil {
		return err
	}
	if agent, ok := object.(*api.Agent); ok {
		r.agentReads++
		if r.agentReads > 1 {
			agent.Spec.DisplayName = "changed-between-reads"
		}
	}
	return nil
}

func (r *noSecretReader) Get(ctx context.Context, key client.ObjectKey, object client.Object, options ...client.GetOption) error {
	if _, ok := object.(*corev1.Secret); ok {
		r.secretReads++
		return fmt.Errorf("resolver attempted forbidden Secret read")
	}
	return r.Reader.Get(ctx, key, object, options...)
}

type platformFixture struct {
	resolver PlatformResolver
	client   client.Client
	spy      *noSecretReader
	runKey   types.NamespacedName
	now      time.Time
}

func newPlatformFixture(t *testing.T, namespace string, model bool) platformFixture {
	t.Helper()
	hash := func(ch string) api.CellnImmutableRef {
		return api.CellnImmutableRef{Hash: "blake3:" + strings.Repeat(ch, 64)}
	}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace, UID: types.UID(namespace + "-uid"), Labels: map[string]string{"sympozium.ai/celln-tenant": "enabled"}}}
	agent := &api.Agent{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "agent", UID: types.UID(namespace + "-agent"), Generation: 1}, Spec: api.AgentSpec{RuntimeRef: "runtime"}}
	wrapperLimits := &api.AgentRuntimeCellnLimits{TimeoutMillis: 90000, MemoryBytes: 96 << 20, TaskBytes: 1024, OutputBytes: 32768, Workspace: "none"}
	runtimeWrapper := &api.AgentRuntime{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "runtime", UID: types.UID(namespace + "-runtime"), Generation: 1}, Spec: api.AgentRuntimeSpec{CellnProfileRef: &api.CellnRuntimeProfileRef{Name: "json-agent-v1", Revision: "v1"}, CellnLimits: wrapperLimits}}
	profile := &api.CellnRuntimeProfile{ObjectMeta: metav1.ObjectMeta{Name: "json-agent-v1", UID: "profile-uid", Generation: 1}, Spec: api.CellnRuntimeProfileSpec{
		Revision: "v1", ContractVersion: "celln.json-tools/v1", Executable: hash("1"), Closure: hash("2"), Mote: hash("3"), PublisherKey: strings.Repeat("a", 64), EntryPoint: "/bin/agent", Platform: "linux/amd64", Lane: "agent", Lifecycles: []string{"disposable-one-shot", "enduring"},
		Limits: api.AgentRuntimeCellnLimits{TimeoutMillis: 120000, MemoryBytes: 128 << 20, TaskBytes: 2048, OutputBytes: 65536, Workspace: "none"}, JSON: &api.CellnHarnessJSONLimits{MaxTurns: 6, MaxCalls: 16},
	}}
	tool := &api.ClusterCellnTool{ObjectMeta: metav1.ObjectMeta{Name: "workspace-write-v1", UID: "tool-uid", Generation: 1}, Spec: api.CellnToolSpec{
		Revision: "v1", Description: "write", SupportOwner: "platform", PublisherKey: strings.Repeat("b", 64), Executable: hash("4"), Closure: hash("5"), EntryPoint: "/bin/write", InvocationABI: "celln.json-stdio/v1", ArgumentsSchema: hash("6"), ResultSchema: hash("7"), Platform: "linux/amd64", Lane: "tool",
		Limits: api.CellnToolLimits{TimeoutMillis: 30000, MemoryBytes: 64 << 20, ArgumentBytes: 2048, OutputBytes: 4096, Workspace: "none", Effects: "none", Artifacts: &api.CellnArtifactLimits{Operation: "write", MaxOperations: 8, MaxFiles: 8, MaxFileBytes: 4096, MaxTotalBytes: 32768}},
	}}
	selection := &api.CellnCatalogueSelection{ToolRefs: []api.CellnCatalogueToolRef{}, ClusterToolRefs: []api.ClusterCellnToolRef{{Name: tool.Name, Revision: "v1"}}}
	run := &api.AgentRun{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "run", UID: types.UID(namespace + "-run"), Generation: 1}, Spec: api.AgentRunSpec{AgentRef: "agent", AgentID: "agent", SessionKey: "session", Task: api.NewStringTask("write report"), Backend: "celln", ExecutionLifecycle: "one-shot", CellnSelection: selection, Cleanup: "delete"}}
	connection := &api.ModelConnection{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "model", UID: types.UID(namespace + "-connection"), Generation: 1}, Spec: api.ModelConnectionSpec{Provider: "openai", Protocol: "openai-chat", Endpoint: "https://model.example/v1/chat/completions", SecretRef: "model-secret", Models: []string{"gpt-test"}}}
	if model {
		run.Spec.Model = api.ModelSpec{ConnectionRef: connection.Name, Model: "gpt-test"}
	}
	runtimePolicyLimits := &api.AgentRuntimeCellnLimits{TimeoutMillis: 80000, MemoryBytes: 80 << 20, TaskBytes: 768, OutputBytes: 24576, Workspace: "none"}
	toolPolicyLimits := tool.Spec.Limits.DeepCopy()
	toolPolicyLimits.TimeoutMillis = 20000
	policy := func(name string, timeout int64) *api.CellnExecutionPolicy {
		limits := runtimePolicyLimits.DeepCopy()
		limits.TimeoutMillis = timeout
		return &api.CellnExecutionPolicy{ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(name + "-uid"), Generation: 1}, Spec: api.CellnExecutionPolicySpec{
			NamespaceSelector: metav1.LabelSelector{MatchLabels: map[string]string{"sympozium.ai/celln-tenant": "enabled"}},
			RuntimeProfiles:   []api.CellnExecutionPolicyRuntime{{Ref: api.CellnRuntimeProfileRef{Name: profile.Name, Revision: "v1"}, Limits: limits}},
			Tools:             []api.CellnExecutionPolicyTool{{Ref: api.ClusterCellnToolRef{Name: tool.Name, Revision: "v1"}, Limits: toolPolicyLimits}},
			Lifecycles:        []string{"direct-one-shot", "harness-one-shot", "enduring"},
			Routes:            []api.CellnExecutionPolicyRoute{{Provider: "openai", Protocol: "openai-chat", Models: []string{"gpt-test"}, EndpointOrigins: []string{"https://model.example"}, Auth: "secret"}},
			Ceilings:          api.CellnExecutionPolicyCeilings{MaxTurns: 4, MaxModelRequests: 8, MaxOutputTokens: 4096, MaxParentLeaseSeconds: 300, MaxTurnSeconds: 60},
		}}
	}
	objects := []client.Object{ns, agent, runtimeWrapper, profile, tool, run, policy("z-policy", 70000), policy("a-policy", 75000)}
	if model {
		objects = append(objects, connection)
	}
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	spy := &noSecretReader{Reader: c}
	return platformFixture{resolver: PlatformResolver{Reader: spy}, client: c, spy: spy, runKey: client.ObjectKeyFromObject(run), now: time.Unix(1790000000, 0).UTC()}
}

func platformRequest(f platformFixture) PlatformResolveRequest {
	return PlatformResolveRequest{ClusterID: "cluster-test", Now: f.now, AdmissionWindow: 60 * time.Second}
}

func TestPlatformResolverBindsNamespacePolicyToolsAndRouteWithoutSecrets(t *testing.T) {
	f := newPlatformFixture(t, "tenant-a", true)
	resolution, err := f.resolver.Resolve(t.Context(), f.runKey, platformRequest(f))
	if err != nil {
		t.Fatal(err)
	}
	d := resolution.Decision
	if d.Run.NamespaceUID != "tenant-a-uid" || d.Run.UID != "tenant-a-run" || d.Route.ModelConnectionUID == nil || *d.Route.ModelConnectionUID != "tenant-a-connection" || d.Route.CredentialSourceRef == nil || d.Route.CredentialSourceRef.SecretName != "model-secret" {
		t.Fatalf("identity or route was not bound: %+v", d)
	}
	if f.spy.secretReads != 0 {
		t.Fatalf("resolver read %d Secrets", f.spy.secretReads)
	}
	if len(d.Tools) != 1 || d.Tools[0].Limits.TimeoutMillis != 20000 || d.Windows.AdmissionDeadline-d.Windows.IssuedAt != 60 {
		t.Fatalf("tool/policy/window intersection is wrong: %+v", d)
	}
	canonical, err := d.Canonical()
	if err != nil || !strings.Contains(string(canonical), `"credentialSource":null`) || strings.Contains(string(canonical), "model-secret") {
		t.Fatalf("unfinalized decision leaked or violated the contract shape: %s, %v", canonical, err)
	}
	if err := d.ReadyForSigning(); err == nil {
		t.Fatal("secret-auth decision was signable before gateway UID pinning")
	}
	finalized, err := d.FinalizeCredentialSource("secret-uid")
	if err != nil || finalized.ReadyForSigning() != nil {
		t.Fatalf("gateway metadata did not finalize the decision: %v", err)
	}
	canonical, err = finalized.Canonical()
	if err != nil || !strings.Contains(string(canonical), `"credentialSource":{"kind":"Secret","secretKey":"OPENAI_API_KEY","secretName":"model-secret","secretUid":"secret-uid"}`) {
		t.Fatalf("final decision does not carry the exact non-secret source pin: %s, %v", canonical, err)
	}
	validateDecisionSchema(t, canonical)
	if err := f.resolver.Revalidate(t.Context(), f.runKey, *resolution); err != nil {
		t.Fatalf("unchanged resolution did not revalidate: %v", err)
	}
}

func TestPlatformResolverSeparatesSameNamesAcrossNamespaces(t *testing.T) {
	a := newPlatformFixture(t, "tenant-a", true)
	b := newPlatformFixture(t, "tenant-b", true)
	ad, err := a.resolver.Resolve(t.Context(), a.runKey, platformRequest(a))
	if err != nil {
		t.Fatal(err)
	}
	bd, err := b.resolver.Resolve(t.Context(), b.runKey, platformRequest(b))
	if err != nil {
		t.Fatal(err)
	}
	if ad.Decision.Run.NamespaceUID == bd.Decision.Run.NamespaceUID || ad.Decision.Run.UID == bd.Decision.Run.UID || *ad.Decision.Route.ModelConnectionUID == *bd.Decision.Route.ModelConnectionUID || ad.Decision.Budget.BudgetID == bd.Decision.Budget.BudgetID {
		t.Fatal("same names in distinct namespaces produced shared authority")
	}
}

func TestPlatformResolverAllowsModelFreeDirectAndDeniesMissingRoute(t *testing.T) {
	f := newPlatformFixture(t, "tenant", false)
	resolution, err := f.resolver.Resolve(t.Context(), f.runKey, platformRequest(f))
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Decision.Route.Protocol != "none" || resolution.Decision.Budget.RunCap != (DecisionCap{}) || resolution.Decision.Lifecycle != "one-shot" {
		t.Fatalf("unexpected direct decision: %+v", resolution.Decision)
	}
	if err := resolution.Decision.ReadyForSigning(); err != nil {
		t.Fatalf("model-free decision should be contract-ready without gateway metadata: %v", err)
	}
	canonical, err := resolution.Decision.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	validateDecisionSchema(t, canonical)
	var run api.AgentRun
	if err := f.client.Get(t.Context(), f.runKey, &run); err != nil {
		t.Fatal(err)
	}
	run.Spec.Model.Model = "gpt-test"
	if err := f.client.Update(t.Context(), &run); err != nil {
		t.Fatal(err)
	}
	if _, err := f.resolver.Resolve(t.Context(), f.runKey, platformRequest(f)); PlatformReason(err) != ReasonRouteMismatch {
		t.Fatalf("missing model route reason = %q, error %v", PlatformReason(err), err)
	}
}

func TestPlatformResolverPolicyIntersectionIsOrderIndependent(t *testing.T) {
	f := newPlatformFixture(t, "tenant", true)
	first, err := f.resolver.Resolve(t.Context(), f.runKey, platformRequest(f))
	if err != nil {
		t.Fatal(err)
	}
	var policies api.CellnExecutionPolicyList
	if err := f.client.List(t.Context(), &policies); err != nil {
		t.Fatal(err)
	}
	for i := range policies.Items {
		policy := policies.Items[i]
		if err := f.client.Delete(t.Context(), &policy); err != nil {
			t.Fatal(err)
		}
	}
	for i := len(policies.Items) - 1; i >= 0; i-- {
		policy := policies.Items[i]
		policy.ResourceVersion = ""
		if err := f.client.Create(t.Context(), &policy); err != nil {
			t.Fatal(err)
		}
	}
	second, err := f.resolver.Resolve(t.Context(), f.runKey, platformRequest(f))
	if err != nil {
		t.Fatal(err)
	}
	if first.Decision.Policy != second.Decision.Policy || !reflectDecisionAuthority(first.Decision, second.Decision) {
		t.Fatalf("policy list ordering changed the effective decision:\nfirst=%+v\nsecond=%+v", first.Decision, second.Decision)
	}
}

func TestPlatformResolverRevalidationDetectsSpecPolicyAndNamespaceChangesButNotStatus(t *testing.T) {
	for _, change := range []string{"status", "run-spec", "policy", "namespace-label"} {
		t.Run(change, func(t *testing.T) {
			f := newPlatformFixture(t, "tenant", true)
			frozen, err := f.resolver.Resolve(t.Context(), f.runKey, platformRequest(f))
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "status":
				var agent api.Agent
				if err := f.client.Get(t.Context(), types.NamespacedName{Namespace: "tenant", Name: "agent"}, &agent); err != nil {
					t.Fatal(err)
				}
				agent.Status.Phase = "Ready"
				if err := f.client.Status().Update(t.Context(), &agent); err != nil {
					// Fake clients without a status subresource may require a normal update.
					if err := f.client.Update(t.Context(), &agent); err != nil {
						t.Fatal(err)
					}
				}
			case "run-spec":
				var run api.AgentRun
				_ = f.client.Get(t.Context(), f.runKey, &run)
				run.Spec.SystemPrompt = "changed"
				_ = f.client.Update(t.Context(), &run)
			case "policy":
				var policy api.CellnExecutionPolicy
				_ = f.client.Get(t.Context(), types.NamespacedName{Name: "a-policy"}, &policy)
				policy.Spec.Ceilings.MaxOutputTokens--
				_ = f.client.Update(t.Context(), &policy)
			case "namespace-label":
				var ns corev1.Namespace
				_ = f.client.Get(t.Context(), types.NamespacedName{Name: "tenant"}, &ns)
				ns.Labels["sympozium.ai/celln-tenant"] = "disabled"
				_ = f.client.Update(t.Context(), &ns)
			}
			err = f.resolver.Revalidate(t.Context(), f.runKey, *frozen)
			if change == "status" && err != nil {
				t.Fatalf("status-only update invalidated authority: %v", err)
			}
			if change != "status" && err == nil {
				t.Fatalf("%s did not invalidate authority", change)
			}
		})
	}
}

func TestPlatformResolverTurnRetainsOriginalIdentityAndDeadlines(t *testing.T) {
	f := newPlatformFixture(t, "tenant", true)
	var run api.AgentRun
	if err := f.client.Get(t.Context(), f.runKey, &run); err != nil {
		t.Fatal(err)
	}
	run.Spec.ExecutionLifecycle = "enduring"
	run.Spec.Enduring = &api.EnduringRunSpec{LeaseSeconds: 240, MaxTurns: 4, MaxModelRequests: 8, MaxOutputTokens: 4096}
	if err := f.client.Update(t.Context(), &run); err != nil {
		t.Fatal(err)
	}
	incarnation := "blake3:" + strings.Repeat("f", 64)
	request := platformRequest(f)
	request.ParentIncarnation = incarnation
	initial, err := f.resolver.Resolve(t.Context(), f.runKey, request)
	if err != nil {
		t.Fatal(err)
	}
	original, err := initial.Decision.FinalizeCredentialSource("secret-uid")
	if err != nil {
		t.Fatal(err)
	}
	turn := &api.AgentRunTurn{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "turn-1", UID: "turn-uid", Generation: 1}, Spec: api.AgentRunTurnSpec{RunName: "run", RunUID: "tenant-run", Message: "continue"}}
	if err := f.client.Create(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	turnKey := client.ObjectKeyFromObject(turn)
	turnRequest := platformRequest(f)
	turnRequest.Now = f.now.Add(30 * time.Second)
	turnRequest.Operation = "execution.turn"
	turnRequest.ParentIncarnation = incarnation
	turnRequest.TurnKey = &turnKey
	turnRequest.Original = &original
	resolvedTurn, err := f.resolver.Resolve(t.Context(), f.runKey, turnRequest)
	if err != nil {
		t.Fatal(err)
	}
	if resolvedTurn.Decision.Budget.BudgetID != initial.Decision.Budget.BudgetID || resolvedTurn.Decision.Budget.RunCap != initial.Decision.Budget.RunCap || resolvedTurn.Decision.Budget.ParentDeadlineUnix != initial.Decision.Budget.ParentDeadlineUnix || resolvedTurn.Decision.Budget.TurnDeadlineUnix > initial.Decision.Budget.ParentDeadlineUnix || resolvedTurn.Decision.Parent == nil || resolvedTurn.Decision.Parent.TurnID == nil || *resolvedTurn.Decision.Parent.TurnID != "turn-uid" {
		t.Fatalf("turn widened or lost original authority: initial=%+v turn=%+v", initial.Decision.Budget, resolvedTurn.Decision)
	}
}

func TestPlatformResolverDetectsChangeBetweenUncachedReads(t *testing.T) {
	f := newPlatformFixture(t, "tenant", true)
	f.resolver.Reader = &changingPlatformReader{Reader: f.client}
	if _, err := f.resolver.Resolve(t.Context(), f.runKey, platformRequest(f)); PlatformReason(err) != ReasonPolicyContracted {
		t.Fatalf("between-read mutation reason = %q, error %v", PlatformReason(err), err)
	}
}

func TestPlatformResolverStableRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reason string
		change func(context.Context, platformFixture)
	}{
		{name: "unknown-tool", reason: ReasonToolUnknown, change: func(ctx context.Context, f platformFixture) {
			var run api.AgentRun
			_ = f.client.Get(ctx, f.runKey, &run)
			run.Spec.CellnSelection.ClusterToolRefs[0].Revision = "unknown"
			_ = f.client.Update(ctx, &run)
		}},
		{name: "endpoint-override", reason: ReasonRouteMismatch, change: func(ctx context.Context, f platformFixture) {
			var run api.AgentRun
			_ = f.client.Get(ctx, f.runKey, &run)
			run.Spec.Model.BaseURL = "https://attacker.example/v1/chat/completions"
			_ = f.client.Update(ctx, &run)
		}},
		{name: "policy-withdrawal", reason: ReasonPolicyWithdrawn, change: func(ctx context.Context, f platformFixture) {
			var ns corev1.Namespace
			_ = f.client.Get(ctx, types.NamespacedName{Name: "tenant"}, &ns)
			ns.Labels = map[string]string{}
			_ = f.client.Update(ctx, &ns)
		}},
		{name: "runtime-expansion", reason: ReasonLimitRange, change: func(ctx context.Context, f platformFixture) {
			var wrapper api.AgentRuntime
			_ = f.client.Get(ctx, types.NamespacedName{Namespace: "tenant", Name: "runtime"}, &wrapper)
			wrapper.Spec.CellnLimits.TimeoutMillis = 130000
			_ = f.client.Update(ctx, &wrapper)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPlatformFixture(t, "tenant", true)
			tc.change(t.Context(), f)
			if _, err := f.resolver.Resolve(t.Context(), f.runKey, platformRequest(f)); PlatformReason(err) != tc.reason {
				t.Fatalf("reason = %q, want %q; error %v", PlatformReason(err), tc.reason, err)
			}
		})
	}
}

func reflectDecisionAuthority(a, b PlatformDecision) bool {
	return a.ClusterID == b.ClusterID && a.Run == b.Run && a.Runtime == b.Runtime && a.Agent == b.Agent && a.Budget == b.Budget && a.RequestDigest == b.RequestDigest && reflect.DeepEqual(a.Tools, b.Tools) && reflect.DeepEqual(a.Route, b.Route)
}

func validateDecisionSchema(t *testing.T, canonical []byte) {
	t.Helper()
	schemaFile, err := os.Open("../../test/fixtures/celln-authorisation/v1/schema/decision.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	defer schemaFile.Close()
	var schemaDocument any
	if err := json.NewDecoder(schemaFile).Decode(&schemaDocument); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	const schemaURL = "urn:sympozium:celln-authorisation-decision-v1"
	if err := compiler.AddResource(schemaURL, schemaDocument); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile(schemaURL)
	if err != nil {
		t.Fatal(err)
	}
	var value any
	if err := json.Unmarshal(canonical, &value); err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(value); err != nil {
		t.Fatalf("decision does not satisfy #496 schema: %v\n%s", err, canonical)
	}
}

func TestPlatformResolverReasonVocabularyMatchesSharedFixtures(t *testing.T) {
	file, err := os.Open("../../test/fixtures/celln-authorisation/v1/cases.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer compressed.Close()
	var cases struct {
		Vectors []struct {
			Evaluator string `json:"evaluator"`
			Expect    struct {
				Outcome string `json:"outcome"`
				Reason  string `json:"reason"`
			} `json:"expect"`
		} `json:"vectors"`
	}
	if err := json.NewDecoder(compressed).Decode(&cases); err != nil {
		t.Fatal(err)
	}
	implemented := map[string]bool{ReasonPolicyWithdrawn: true, ReasonPolicyContracted: true, ReasonToolOrder: true}
	seen := 0
	for _, vector := range cases.Vectors {
		if vector.Evaluator != "resolver" || vector.Expect.Outcome != "reject" {
			continue
		}
		seen++
		if !implemented[vector.Expect.Reason] {
			t.Fatalf("shared resolver reason %q has no production mapping", vector.Expect.Reason)
		}
	}
	if seen != 4 {
		t.Fatalf("consumed %d resolver refusal fixtures, want 4", seen)
	}
}
