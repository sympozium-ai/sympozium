package cellnparent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnauthority"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const platformPrincipal = "sympozium:celln"

// platformObjects is one authorised tenant namespace on a platform catalogue:
// the wrapper objects a tenant creates plus the operator's cluster-scoped
// profile, tool and policy.
func platformObjects(namespace string) []client.Object {
	hash := func(ch string) api.CellnImmutableRef {
		return api.CellnImmutableRef{Hash: "blake3:" + strings.Repeat(ch, 64)}
	}
	raw := func(s string) apiextensionsv1.JSON { return apiextensionsv1.JSON{Raw: []byte(s)} }
	request := `{"apiVersion":"celln.dev/v1alpha1","id":"native","workload":{"id":"native","caller":"` + platformPrincipal + `"},"capabilities":{"workspace":"none","timeoutMs":60000,"memoryBytes":268435456,"outputBytes":65536}}`
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace, UID: types.UID(namespace + "-uid"), Labels: map[string]string{"celln.sympozium.ai/scope": "trial"}}}
	agent := &api.Agent{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "celln-agent", UID: types.UID(namespace + "-agent"), Generation: 1}, Spec: api.AgentSpec{RuntimeRef: "celln-native"}}
	wrapper := &api.AgentRuntime{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "celln-native", UID: types.UID(namespace + "-runtime"), Generation: 1}, Spec: api.AgentRuntimeSpec{CellnProfileRef: &api.CellnRuntimeProfileRef{Name: "celln-native-trial", Revision: "v1"}}}
	profile := &api.CellnRuntimeProfile{ObjectMeta: metav1.ObjectMeta{Name: "celln-native-trial", UID: "profile-uid", Generation: 1}, Spec: api.CellnRuntimeProfileSpec{
		Revision: "v1", ContractVersion: "celln.json-tools/v1", Executable: hash("1"), Closure: hash("2"), Mote: hash("3"), PublisherKey: strings.Repeat("a", 64), EntryPoint: "/worker", Platform: "linux/amd64", Lane: "agent", Lifecycles: []string{"disposable-one-shot", "enduring"},
		Limits: api.AgentRuntimeCellnLimits{TimeoutMillis: 60000, MemoryBytes: 268435456, TaskBytes: 2048, OutputBytes: 65536, Workspace: "none"}, JSON: &api.CellnHarnessJSONLimits{MaxTurns: 3, MaxCalls: 1},
		Native: &api.CellnNativeProvisioning{AdmissionWindowMs: 120000, Parent: raw(request), Worker: raw(request), Template: raw(`{"contract":"celln.json-tools/v1","task":"","system":"host persona","model":"deepseek-chat","url":"https://api.deepseek.com/chat/completions","tools":[],"max_turns":3,"max_calls":1}`), ModelProfile: "blake3:" + strings.Repeat("d", 64), CredentialProfile: "starter", ReservedMemoryBytes: 1342177280, TurnModelRequests: 3, TurnOutputTokens: 1536, SystemPrompt: "host persona"},
	}}
	tool := &api.ClusterCellnTool{ObjectMeta: metav1.ObjectMeta{Name: "workspace-write", UID: "tool-uid", Generation: 1}, Spec: api.CellnToolSpec{
		Revision: "v1", Description: "write", SupportOwner: "platform", PublisherKey: strings.Repeat("b", 64), Executable: hash("4"), Closure: hash("5"), EntryPoint: "/workspace-write", InvocationABI: "celln.json-stdio/v1", ArgumentsSchema: hash("6"), ResultSchema: hash("7"), Platform: "linux/amd64", Lane: "tool",
		Limits: api.CellnToolLimits{TimeoutMillis: 30000, MemoryBytes: 268435456, ArgumentBytes: 8192, OutputBytes: 32768, Workspace: "none", Effects: "external-side-effects", Artifacts: &api.CellnArtifactLimits{Operation: "write", MaxOperations: 4, MaxFiles: 8, MaxFileBytes: 4096, MaxTotalBytes: 16384}},
	}}
	policy := &api.CellnExecutionPolicy{ObjectMeta: metav1.ObjectMeta{Name: "celln-fleet-trial", UID: "policy-uid", Generation: 1}, Spec: api.CellnExecutionPolicySpec{
		NamespaceSelector: metav1.LabelSelector{MatchLabels: map[string]string{"celln.sympozium.ai/scope": "trial"}},
		RuntimeProfiles:   []api.CellnExecutionPolicyRuntime{{Ref: api.CellnRuntimeProfileRef{Name: profile.Name, Revision: "v1"}}},
		Tools:             []api.CellnExecutionPolicyTool{{Ref: api.ClusterCellnToolRef{Name: tool.Name, Revision: "v1"}}},
		Lifecycles:        []string{"direct-one-shot", "harness-one-shot", "enduring"},
		Routes:            []api.CellnExecutionPolicyRoute{{Provider: "deepseek", Protocol: "openai-chat", Models: []string{"deepseek-chat"}, EndpointOrigins: []string{"https://api.deepseek.com"}, Auth: "host-profile"}},
		Ceilings:          api.CellnExecutionPolicyCeilings{MaxTurns: 12, MaxModelRequests: 36, MaxOutputTokens: 18432, MaxParentLeaseSeconds: 3600, MaxTurnSeconds: 60},
	}}
	connection := &api.ModelConnection{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "celln-native", UID: types.UID(namespace + "-connection"), Generation: 1}, Spec: api.ModelConnectionSpec{Provider: "deepseek", Protocol: "openai-chat", Endpoint: "https://api.deepseek.com/chat/completions", CredentialProfile: "starter", Models: []string{"deepseek-chat"}}}
	run := &api.AgentRun{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "conversation", UID: types.UID(namespace + "-run"), Generation: 1}, Spec: api.AgentRunSpec{
		AgentRef: "celln-agent", AgentID: "celln-agent", SessionKey: "session", Task: api.NewStringTask("Write violet to notes.txt"), Backend: "celln", ExecutionLifecycle: "enduring", SystemPrompt: "host persona", Cleanup: "delete",
		Model: api.ModelSpec{ConnectionRef: "celln-native", Model: "deepseek-chat"}, CellnSelection: &api.CellnCatalogueSelection{RuntimeRef: "celln-native", ToolRefs: []api.CellnCatalogueToolRef{}, ClusterToolRefs: []api.ClusterCellnToolRef{{Name: tool.Name, Revision: "v1"}}},
		Enduring: &api.EnduringRunSpec{LeaseSeconds: 600, MaxTurns: 8, MaxModelRequests: 24, MaxOutputTokens: 8192},
	}}
	return []client.Object{ns, agent, wrapper, profile, tool, policy, connection, run}
}

func platformStore(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&api.AgentRun{}).WithObjects(objects...).Build()
}

func TestPlatformAdmissionIssuesParentFromDecisionWithoutNamespaceGrants(t *testing.T) {
	ctx := context.Background()
	store := platformStore(t, platformObjects("tenant-a")...)
	key := types.NamespacedName{Namespace: "tenant-a", Name: "conversation"}
	expected, err := cellnauthority.ScopedParentIncarnation("cluster", "tenant-a-uid", "tenant-a-run")
	if err != nil {
		t.Fatal(err)
	}
	scope, _ := cellnauthority.ScopedParentScope("cluster", "tenant-a-uid")
	launch := "blake3:" + strings.Repeat("e", 64)
	var issued atomic.Int32
	var refuse atomic.Bool
	var lastPlan HostProvisionPlan
	var bodies []string
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 65537))
		if r.URL.Path != "/v1/parents/provision" || r.Header.Get("X-Celln-Parent-Incarnation") != expected || json.Unmarshal(body, &lastPlan) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		bodies = append(bodies, string(body))
		if refuse.Load() {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"parent provisioning refused; preserve issuance state","retryAuthorized":false}`))
			return
		}
		issued.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]string{"apiVersion": "celln.parent-provisioned/v1", "launchProfile": launch, "incarnation": expected})
	}))
	defer owner.Close()
	root := t.TempDir()
	token := filepath.Join(root, "token")
	if err := os.WriteFile(token, []byte("platform-admission-test-credential"), 0600); err != nil {
		t.Fatal(err)
	}
	p := PlatformProvisioner{ClusterID: "cluster", Journal: filepath.Join(root, "journal"), Approvals: filepath.Join(root, "approvals"), Target: owner.URL, TokenFile: token}
	for _, dir := range []string{p.Journal, p.Approvals} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	// A refused issuance leaves the pinned choice; the retry re-sends the same
	// bytes instead of resolving a new decision under a new clock.
	refuse.Store(true)
	if err := p.Admit(ctx, store, key); err == nil || !strings.Contains(err.Error(), "owner answered 409") {
		t.Fatalf("owner refusal not surfaced with its status: %v", err)
	}
	refuse.Store(false)
	if err := p.Admit(ctx, store, key); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 || bodies[0] != bodies[1] {
		t.Fatalf("retry after refusal did not re-send the pinned plan (%d bodies)", len(bodies))
	}
	if issued.Load() != 1 || lastPlan.Scope != scope || lastPlan.RunUID != "tenant-a-run" || lastPlan.MaxTurns != 8 || lastPlan.TotalModelRequests != 24 || lastPlan.TotalOutputTokens != 8192 || lastPlan.TurnModelRequests != 3 || lastPlan.TurnOutputTokens != 1536 || lastPlan.ModelProfile != "blake3:"+strings.Repeat("d", 64) || !strings.HasPrefix(lastPlan.IntentSHA256, "sha256:") {
		t.Fatalf("plan did not carry the policy-capped decision: %+v", lastPlan)
	}
	var parent struct {
		Workload     struct{ Caller string }
		Capabilities struct{ TimeoutMs int64 }
	}
	if json.Unmarshal(lastPlan.Parent, &parent) != nil || parent.Workload.Caller != platformPrincipal || parent.Capabilities.TimeoutMs != 600000 {
		t.Fatalf("parent request lost its principal or lease: %s", lastPlan.Parent)
	}
	var run api.AgentRun
	if err := store.Get(ctx, key, &run); err != nil {
		t.Fatal(err)
	}
	binding, transport, err := LoadApproval(p.Approvals, &run)
	if err != nil {
		t.Fatal(err)
	}
	transport.Close()
	if binding.Incarnation != expected || binding.LaunchProfile != launch || binding.Principal != platformPrincipal || binding.Target != owner.URL {
		t.Fatalf("approval not bound to the issued parent: %+v", binding)
	}
	if err := revalidatePlatformAuthority(ctx, store, p.Approvals, &run); err != nil {
		t.Fatalf("frozen authority did not revalidate: %v", err)
	}
	if err := p.Admit(ctx, store, key); err != nil || issued.Load() != 2 || bodies[2] != bodies[0] {
		t.Fatalf("identical retry not idempotent: %v issued=%d", err, issued.Load())
	}
	// Policy withdrawal after issuance stops new turns without touching the parent.
	var policy api.CellnExecutionPolicy
	if err := store.Get(ctx, types.NamespacedName{Name: "celln-fleet-trial"}, &policy); err != nil {
		t.Fatal(err)
	}
	policy.Spec.Ceilings.MaxTurns = 4
	if err := store.Update(ctx, &policy); err != nil {
		t.Fatal(err)
	}
	if err := revalidatePlatformAuthority(ctx, store, p.Approvals, &run); err == nil {
		t.Fatal("changed policy still authorised new turns")
	}
	if err := revalidatePlatformAuthority(ctx, store, filepath.Join(root, "no-such-dir"), &run); err != nil {
		t.Fatalf("legacy approval path must not be gated: %v", err)
	}
}

func TestPlatformAdmissionRefusesUnauthorisedNamespacePersonaAndConfig(t *testing.T) {
	ctx := context.Background()
	objects := platformObjects("tenant-b")
	objects[0].SetLabels(nil) // no policy selects this namespace
	store := platformStore(t, objects...)
	root := t.TempDir()
	token := filepath.Join(root, "token")
	if err := os.WriteFile(token, []byte("platform-admission-test-credential"), 0600); err != nil {
		t.Fatal(err)
	}
	p := PlatformProvisioner{ClusterID: "cluster", Journal: root, Approvals: root, Target: "http://127.0.0.1:1", TokenFile: token}
	err := p.Admit(ctx, store, types.NamespacedName{Namespace: "tenant-b", Name: "conversation"})
	if cellnauthority.PlatformReason(err) != cellnauthority.ReasonPolicyWithdrawn {
		t.Fatalf("unselected namespace admitted: %v", err)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 1 {
		t.Fatal("refusal left durable records")
	}
	authorised := platformObjects("tenant-c")
	run := authorised[len(authorised)-1].(*api.AgentRun)
	run.Spec.SystemPrompt = "another persona"
	store = platformStore(t, authorised...)
	if err := p.Admit(ctx, store, types.NamespacedName{Namespace: "tenant-c", Name: "conversation"}); err == nil || !strings.Contains(err.Error(), "persona") {
		t.Fatalf("foreign persona reached issuance: %v", err)
	}
	// A budget below one turn of the profile's model allowance is refused with
	// a tenant-visible reason before anything is pinned.
	starved := platformObjects("tenant-d")
	starved[len(starved)-1].(*api.AgentRun).Spec.Enduring.MaxOutputTokens = 1000
	store = platformStore(t, starved...)
	if err := p.Admit(ctx, store, types.NamespacedName{Namespace: "tenant-d", Name: "conversation"}); cellnauthority.PlatformReason(err) != cellnauthority.ReasonLimitRange {
		t.Fatalf("starved run budget reached issuance: %v", err)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 1 {
		t.Fatal("budget refusal left durable records")
	}
	config := RegistrationConfig{APIVersion: "sympozium.ai/celln-parent-registrations-v1", Journal: root, Approvals: root, Platform: &p}
	path := filepath.Join(t.TempDir(), "platform.json")
	write := func(c RegistrationConfig) {
		t.Helper()
		raw, _ := json.Marshal(c)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(config)
	dispatcher, err := LoadRegistrationDispatcher(path, root, store)
	if err != nil || !dispatcher.SupportsPlatform() {
		t.Fatalf("platform configuration refused: %v", err)
	}
	mixed := config
	mixed.LocalProvisioner = &LocalProvisioner{Binary: "/usr/local/bin/celln", Root: root, Journal: root, Approvals: root, Target: p.Target, TokenFile: token}
	write(mixed)
	if _, err := LoadRegistrationDispatcher(path, root, store); err == nil {
		t.Fatal("platform and prepared modes combined")
	}
	missing := config
	missing.Platform = &PlatformProvisioner{Journal: root, Approvals: root, Target: p.Target, TokenFile: token}
	write(missing)
	if _, err := LoadRegistrationDispatcher(path, root, store); err == nil {
		t.Fatal("platform mode without cluster identity accepted")
	}
}

// A one-shot on the platform is a single-turn parent: the same admission,
// plan and owner protocol as an enduring run, with the lease derived from one
// turn plus the admission grace and exactly one turn funded.
func TestPlatformAdmissionIssuesSingleTurnParentForOneShot(t *testing.T) {
	ctx := context.Background()
	objects := platformObjects("tenant-a")
	run := objects[len(objects)-1].(*api.AgentRun)
	run.Spec.ExecutionLifecycle, run.Spec.Enduring = "", nil
	if !run.Spec.PlatformOneShotShape() {
		t.Fatal("fixture is not a platform one-shot")
	}
	store := platformStore(t, objects...)
	key := types.NamespacedName{Namespace: "tenant-a", Name: "conversation"}
	expected, _ := cellnauthority.ScopedParentIncarnation("cluster", "tenant-a-uid", "tenant-a-run")
	launch := "blake3:" + strings.Repeat("e", 64)
	var lastPlan HostProvisionPlan
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 65537))
		if r.URL.Path != "/v1/parents/provision" || json.Unmarshal(body, &lastPlan) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"apiVersion": "celln.parent-provisioned/v1", "launchProfile": launch, "incarnation": expected})
	}))
	defer owner.Close()
	root := t.TempDir()
	token := filepath.Join(root, "token")
	if err := os.WriteFile(token, []byte("platform-admission-test-credential"), 0600); err != nil {
		t.Fatal(err)
	}
	p := PlatformProvisioner{ClusterID: "cluster", Journal: filepath.Join(root, "journal"), Approvals: filepath.Join(root, "approvals"), Target: owner.URL, TokenFile: token}
	for _, dir := range []string{p.Journal, p.Approvals} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.Admit(ctx, store, key); err != nil {
		t.Fatal(err)
	}
	// One turn of the profile's allowance, the policy run cap as the total.
	if lastPlan.MaxTurns != 1 || lastPlan.TurnModelRequests != 3 || lastPlan.TurnOutputTokens != 1536 || lastPlan.TotalModelRequests != 36 || lastPlan.TotalOutputTokens != 18432 {
		t.Fatalf("one-shot plan did not fund exactly one turn: %+v", lastPlan)
	}
	var parent struct {
		Capabilities struct{ TimeoutMs int64 }
	}
	// The profile allows 60 s turns; the lease adds the admission grace.
	if json.Unmarshal(lastPlan.Parent, &parent) != nil || parent.Capabilities.TimeoutMs != (60+cellnauthority.OneShotParentGraceSeconds)*1000 {
		t.Fatalf("one-shot lease does not cover one turn plus admission: %s", lastPlan.Parent)
	}
	var admitted api.AgentRun
	if err := store.Get(ctx, key, &admitted); err != nil {
		t.Fatal(err)
	}
	binding, transport, err := LoadApproval(p.Approvals, &admitted)
	if err != nil {
		t.Fatal(err)
	}
	transport.Close()
	if err := ValidateAdmission(&admitted, binding); err != nil {
		t.Fatalf("one-shot admission rejected: %v", err)
	}
	// A one-shot parent never takes a follow-up turn.
	admitted.Status.Phase = api.AgentRunPhaseRunning
	admitted.Status.CellnParent = &api.CellnParentStatus{Binding: binding, CreateAttempted: true, InitialTurn: &api.CellnParentTurnStatus{ID: "initial", Message: "x", Child: launch, Attempted: true, Result: &api.CellnParentTurnResult{Succeeded: true, Answer: "done"}}}
	if err := store.Status().Update(ctx, &admitted); err != nil {
		t.Fatal(err)
	}
	if _, err := NewTurn(&admitted, "follow-up", "again"); err == nil {
		t.Fatal("one-shot parent accepted a follow-up turn")
	}
	if err := ClaimTurnSlot(ctx, store, store, types.NamespacedName{Namespace: "tenant-a", Name: "follow-up"}, p.Approvals, time.Now()); err == nil || strings.Contains(err.Error(), "Enduring") {
		t.Fatalf("one-shot slot claim must refuse without dereferencing enduring limits: %v", err)
	}
}

func TestPlatformPlanCarriesAContinuedConversationsSeed(t *testing.T) {
	ctx := context.Background()
	store := platformStore(t, platformObjects("tenant-a")...)
	key := types.NamespacedName{Namespace: "tenant-a", Name: "conversation"}
	var run api.AgentRun
	if err := store.Get(ctx, key, &run); err != nil {
		t.Fatal(err)
	}
	run.Spec.Conversation = &api.ConversationSpec{Continuation: "automatic", ContinuesFrom: "earlier", Depth: 1, Seed: []api.ConversationExchange{{User: "Remember the word saffron.", Assistant: "Noted: saffron."}}}
	if err := store.Update(ctx, &run); err != nil {
		t.Fatal(err)
	}
	expected, err := cellnauthority.ScopedParentIncarnation("cluster", "tenant-a-uid", "tenant-a-run")
	if err != nil {
		t.Fatal(err)
	}
	var lastPlan HostProvisionPlan
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 65537))
		if json.Unmarshal(body, &lastPlan) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"apiVersion": "celln.parent-provisioned/v1", "launchProfile": "blake3:" + strings.Repeat("e", 64), "incarnation": expected})
	}))
	defer owner.Close()
	root := t.TempDir()
	token := filepath.Join(root, "token")
	if err := os.WriteFile(token, []byte("platform-admission-test-credential"), 0600); err != nil {
		t.Fatal(err)
	}
	p := PlatformProvisioner{ClusterID: "cluster", Journal: filepath.Join(root, "journal"), Approvals: filepath.Join(root, "approvals"), Target: owner.URL, TokenFile: token}
	for _, dir := range []string{p.Journal, p.Approvals} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.Admit(ctx, store, key); err != nil {
		t.Fatal(err)
	}
	if len(lastPlan.History) != 1 || lastPlan.History[0].User != "Remember the word saffron." || lastPlan.History[0].Assistant != "Noted: saffron." {
		t.Fatalf("plan must carry the seed as history: %+v", lastPlan.History)
	}
}

// The fleet's host-profile routes run on the node's own broker, so every
// starter workspace operation stays admissible for one-shot and enduring runs;
// the narrower scoped artifact contract applies only to mediated routes.
func TestPlatformHostProfileKeepsEveryWorkspaceOperation(t *testing.T) {
	for _, op := range []string{"read", "write", "list", "append", "search", "delete"} {
		for _, oneShot := range []bool{false, true} {
			objects := platformObjects("tenant-a")
			tool := objects[4].(*api.ClusterCellnTool)
			tool.Spec.Limits.Artifacts.Operation = op
			if !cellnauthority.ArtifactWrites[op] {
				tool.Spec.Limits.Effects = "none"
			}
			run := objects[len(objects)-1].(*api.AgentRun)
			if oneShot {
				run.Spec.ExecutionLifecycle, run.Spec.Enduring = "", nil
			}
			store := platformStore(t, objects...)
			incarnation, _ := cellnauthority.ScopedParentIncarnation("cluster", "tenant-a-uid", "tenant-a-run")
			request := cellnauthority.PlatformResolveRequest{ClusterID: "cluster", Now: time.Now().UTC(), AdmissionWindow: platformAdmissionWindow, Operation: "execution.start", ParentIncarnation: incarnation}
			if _, err := (cellnauthority.PlatformResolver{Reader: store}).Resolve(context.Background(), types.NamespacedName{Namespace: "tenant-a", Name: "conversation"}, request); err != nil {
				t.Errorf("%s (one-shot=%v) refused on a host-profile route: %v", op, oneShot, err)
			}
		}
	}
}
