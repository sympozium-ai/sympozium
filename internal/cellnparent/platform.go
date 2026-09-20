package cellnparent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnauthority"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// PlatformProvisioner admits an enduring run from the platform decision
// (policy-selected namespace, cluster-scoped runtime profile and tools)
// instead of one namespace's grant set, and issues its parent through the
// gateway. It is the fleet's admission mode: a tenant namespace needs only its
// wrapper objects. Journal and Approvals stay controller-durable operator state.
type PlatformProvisioner struct {
	ClusterID string `json:"clusterId"`
	Journal   string `json:"journal"`
	Approvals string `json:"approvals"`
	Target    string `json:"target"`
	TokenFile string `json:"tokenFile"`
	CAFile    string `json:"caFile,omitempty"`
}

const (
	platformAdmissionWindow = 60 * time.Second
	platformChoiceVersion   = "sympozium.ai/celln-parent-platform-choice-v2"
	platformRecordLimit     = 262144
)

// platformChoice is the durable record pinned before the owner is contacted:
// the host that decided, the digests it committed to, and the frozen
// resolution a retry must revalidate instead of resolving afresh.
type platformChoice struct {
	APIVersion     string                  `json:"apiVersion"`
	Host           PlatformProvisioner     `json:"host"`
	DecisionSHA256 string                  `json:"decisionSHA256"`
	PlanSHA256     string                  `json:"planSHA256"`
	Frozen         frozenPlatformAuthority `json:"frozen"`
}

// platformRecordName is the frozen resolution retained beside the approval so
// every later turn revalidates the same authority the parent was issued from.
func platformRecordName(runUID string) string {
	sum := sha256.Sum256([]byte(runUID))
	return fmt.Sprintf("platform-%x.json", sum)
}

// Admit publishes approval for an unbound run. It reads only through the
// uncached reader, pins the decision and plan before contacting the owner,
// revalidates live authority around issuance, and never replaces a durable
// choice on retry.
func (p PlatformProvisioner) Admit(ctx context.Context, reader client.Reader, key types.NamespacedName) error {
	var run api.AgentRun
	if err := reader.Get(ctx, key, &run); err != nil {
		return err
	}
	if run.Status.CellnParent != nil {
		return nil
	}
	if p.ClusterID == "" {
		return fmt.Errorf("platform admission requires the cluster identity")
	}
	owner := ownerIssuer{Journal: p.Journal, Approvals: p.Approvals, Target: p.Target, TokenFile: p.TokenFile, CAFile: p.CAFile}
	if err := owner.validate(); err != nil {
		return err
	}
	var namespace corev1.Namespace
	if err := reader.Get(ctx, types.NamespacedName{Name: key.Namespace}, &namespace); err != nil {
		return err
	}
	scope, err := cellnauthority.ScopedParentScope(p.ClusterID, string(namespace.UID))
	if err != nil {
		return err
	}
	incarnation, err := cellnauthority.ScopedParentIncarnation(p.ClusterID, string(namespace.UID), string(run.UID))
	if err != nil {
		return err
	}
	resolver := cellnauthority.PlatformResolver{Reader: reader}
	// The first attempt resolves and pins; every retry revalidates the pinned
	// resolution and re-sends the identical plan, so the gateway's owner
	// affinity and the owner's issuance ledger see one plan per incarnation.
	choiceName := "provision-" + approvalFileName(string(run.UID))
	var resolution *cellnauthority.PlatformResolution
	var choice []byte
	var pinned platformChoice
	if existing, err := boundedFile(filepath.Join(p.Journal, choiceName), platformRecordLimit); err == nil {
		if json.Unmarshal(existing, &pinned) != nil || pinned.APIVersion != platformChoiceVersion || pinned.Host != p || pinned.Frozen.Resolution.Decision.Run.UID != string(run.UID) || pinned.Frozen.Request.ClusterID != p.ClusterID {
			return fmt.Errorf("pinned platform choice is unreadable or foreign; preserve issuance state")
		}
		pinned.Frozen.Resolution.Request = pinned.Frozen.Request
		if err := resolver.Revalidate(ctx, key, pinned.Frozen.Resolution); err != nil {
			return err
		}
		resolution, choice = &pinned.Frozen.Resolution, existing
	} else if !strings.Contains(err.Error(), "unavailable") {
		return fmt.Errorf("pinned platform choice unreadable: %w", err)
	} else {
		request := cellnauthority.PlatformResolveRequest{ClusterID: p.ClusterID, Now: time.Now().UTC(), AdmissionWindow: platformAdmissionWindow, Operation: "execution.start", ParentIncarnation: incarnation}
		if resolution, err = resolver.Resolve(ctx, key, request); err != nil {
			return err
		}
	}
	plan, principal, err := BuildPlatformProvisionPlan(run, *resolution, scope)
	if err != nil {
		return err
	}
	planSum := fmt.Sprintf("sha256:%x", sha256.Sum256(plan))
	if choice == nil {
		decisionDigest, err := resolution.Decision.Digest()
		if err != nil {
			return err
		}
		if choice, err = json.Marshal(platformChoice{APIVersion: platformChoiceVersion, Host: p, DecisionSHA256: decisionDigest, PlanSHA256: planSum, Frozen: frozenPlatformAuthority{Resolution: *resolution, Request: resolution.Request}}); err != nil {
			return err
		}
		if err := publishBoundedRecord(p.Journal, choiceName, choice, platformRecordLimit); err != nil {
			return fmt.Errorf("preserve original provision host/plan: %w", err)
		}
	} else if pinned.PlanSHA256 != planSum {
		return fmt.Errorf("pinned provision plan no longer reproducible; preserve issuance state")
	}
	remote := RemoteProvisioner{Journal: p.Journal, Approvals: p.Approvals, Target: p.Target, TokenFile: p.TokenFile, CAFile: p.CAFile}
	issued, err := remote.issue(ctx, plan, principal, incarnation)
	if err != nil {
		return err
	}
	if issued.Incarnation != incarnation {
		return fmt.Errorf("issuer returned another run incarnation")
	}
	if err := resolver.Revalidate(ctx, key, *resolution); err != nil {
		return err
	}
	// The resolution's request is not part of its wire form; retain it so a
	// later turn revalidates the exact same clock and identity.
	frozen, err := json.Marshal(frozenPlatformAuthority{Resolution: *resolution, Request: resolution.Request})
	if err != nil {
		return err
	}
	if err := publishBoundedRecord(p.Approvals, platformRecordName(string(run.UID)), frozen, platformRecordLimit); err != nil {
		return fmt.Errorf("preserve frozen platform authority: %w", err)
	}
	digest, err := SpecDigest(run.Spec)
	if err != nil {
		return err
	}
	approval := RunApproval{Namespace: run.Namespace, Name: run.Name, TokenFile: p.TokenFile, CAFile: p.CAFile, Binding: api.CellnParentBinding{Target: p.Target, Principal: principal, RunUID: string(run.UID), SpecSHA256: digest, LaunchProfile: issued.LaunchProfile, Incarnation: incarnation}}
	if err := ValidateAdmission(&run, approval.Binding); err != nil {
		return err
	}
	choiceSum := sha256.Sum256(choice)
	if err := claimParentAssignment(p.Journal, approval, fmt.Sprintf("sha256:%x", choiceSum)); err != nil {
		return err
	}
	raw, err := json.Marshal(ApprovalConfig{APIVersion: "sympozium.ai/celln-parent-controller-v1", Approvals: []RunApproval{approval}})
	if err != nil {
		return err
	}
	return publishParentRecord(p.Approvals, approvalFileName(string(run.UID)), raw)
}

// BuildPlatformProvisionPlan turns a frozen platform decision and the runtime
// profile's native material into the owner's provision plan. Everything the
// package fixed (worker, template, model profile, persona) is passed through
// untouched so the owner's bindings still verify; the parent lease and the
// aggregate ceilings come from the policy-capped decision. It returns the
// parent principal the material was issued for.
func BuildPlatformProvisionPlan(run api.AgentRun, resolution cellnauthority.PlatformResolution, scope string) ([]byte, string, error) {
	d := resolution.Decision
	m := resolution.Execution
	// An enduring run brings its own lease and turn count; a one-shot is a
	// single-turn parent whose lease the resolver derived from one turn.
	enduring := d.Lifecycle == "enduring-initial" && d.Parent != nil && run.Spec.Enduring != nil && run.Spec.ExecutionLifecycle == "enduring"
	oneShot := d.Lifecycle == "one-shot" && run.Spec.PlatformOneShotShape() && d.Budget.ParentDeadlineUnix > resolution.Request.Now.Unix()
	if m == nil || string(run.UID) != d.Run.UID || (!enduring && !oneShot) {
		return nil, "", fmt.Errorf("platform plan requires an enduring-initial or one-shot decision for this run")
	}
	leaseSeconds, requireToolCall := d.Budget.ParentDeadlineUnix-resolution.Request.Now.Unix(), false
	if enduring {
		leaseSeconds, requireToolCall = int64(run.Spec.Enduring.LeaseSeconds), run.Spec.Enduring.RequireToolCall
	}
	native := m.ProfileSpec.Native
	if native == nil {
		return nil, "", fmt.Errorf("runtime profile carries no native provisioning material")
	}
	if d.Route.Auth != "host-profile" || d.Route.Model == "" {
		return nil, "", fmt.Errorf("native parents currently require an owner-installed model credential route")
	}
	var harness struct {
		Contract        string `json:"contract"`
		Task            string `json:"task"`
		System          string `json:"system"`
		Model           string `json:"model"`
		URL             string `json:"url"`
		RequireToolCall bool   `json:"require_tool_call"`
	}
	if json.Unmarshal(native.Template.Raw, &harness) != nil || harness.Contract != "celln.json-tools/v1" || harness.Task != "" {
		return nil, "", fmt.Errorf("runtime profile native template is not a supported JSON harness")
	}
	if harness.System != native.SystemPrompt || run.Spec.SystemPrompt != native.SystemPrompt {
		return nil, "", cellnauthority.Refuse(cellnauthority.ReasonPolicyContracted, "run persona differs from the runtime profile's bound persona; send the profile's systemPrompt verbatim")
	}
	origin, err := api.ModelEndpointOriginInsecure(harness.URL, true)
	if err != nil || harness.Model != d.Route.Model || origin != d.Route.EndpointOrigin {
		return nil, "", cellnauthority.Refuse(cellnauthority.ReasonPolicyContracted, "runtime profile model route differs from the resolved route")
	}
	if harness.RequireToolCall != requireToolCall {
		return nil, "", cellnauthority.Refuse(cellnauthority.ReasonPolicyContracted, "runtime profile tool-call requirement differs from run intent")
	}
	if !hashPattern.MatchString(native.ModelProfile) || native.ReservedMemoryBytes < 1 || native.AdmissionWindowMs < 1 || native.TurnModelRequests < 1 || native.TurnOutputTokens < 1 {
		return nil, "", fmt.Errorf("runtime profile native material is incomplete")
	}
	principal, err := requestCaller(native.Worker.Raw)
	if err != nil {
		return nil, "", err
	}
	parent, err := parentRequestWithLease(native.Parent.Raw, principal, leaseSeconds)
	if err != nil {
		return nil, "", err
	}
	expectedTurns := int64(1)
	if enduring {
		expectedTurns = int64(run.Spec.Enduring.MaxTurns)
	}
	if d.Budget.MaxTurns < 1 || d.Budget.RunCap.Requests < 1 || d.Budget.RunCap.OutputTokens < 1 || expectedTurns != d.Budget.MaxTurns {
		return nil, "", fmt.Errorf("decision budget does not bound the parent's turns")
	}
	// The owner admits a parent only when every turn may spend the model
	// profile's reviewed allowance; a run whose budget affords less is refused
	// here with a tenant-visible reason rather than by an opaque owner 409.
	if d.Budget.TurnCap.Requests < native.TurnModelRequests || d.Budget.TurnCap.OutputTokens < native.TurnOutputTokens {
		return nil, "", cellnauthority.Refuse(cellnauthority.ReasonLimitRange, "run budget affords less than one turn of the runtime profile's model allowance (%d requests, %d output tokens)", native.TurnModelRequests, native.TurnOutputTokens)
	}
	plan := HostProvisionPlan{APIVersion: "celln.parent-provision-plan/v1", Scope: scope, RunUID: string(run.UID), IntentSHA256: mustDecisionDigest(d), NativeProvisionConfig: NativeProvisionConfig{
		AdmissionWindowMs:   uint64(native.AdmissionWindowMs),
		Parent:              parent,
		Worker:              json.RawMessage(native.Worker.Raw),
		Template:            json.RawMessage(native.Template.Raw),
		ModelProfile:        native.ModelProfile,
		ReservedMemoryBytes: uint64(native.ReservedMemoryBytes),
		MaxTurns:            uint64(d.Budget.MaxTurns),
		TurnModelRequests:   uint64(d.Budget.TurnCap.Requests),
		TurnOutputTokens:    uint64(d.Budget.TurnCap.OutputTokens),
		TotalModelRequests:  uint64(d.Budget.RunCap.Requests),
		TotalOutputTokens:   uint64(d.Budget.RunCap.OutputTokens),
	}}
	if plan.IntentSHA256 == "" {
		return nil, "", fmt.Errorf("decision digest unavailable")
	}
	// A continued conversation: the new parent starts with the recorded
	// exchanges. The decision digest already binds the run's spec, seed
	// included, so the plan carries it verbatim.
	if enduring && run.Spec.Conversation != nil && len(run.Spec.Conversation.Seed) != 0 {
		if !SeedFits(run.Spec.Conversation.Seed, SeedBudget(m.ProfileSpec.Limits.TaskBytes)) {
			return nil, "", fmt.Errorf("conversation seed exceeds the parent's context bound")
		}
		plan.History = run.Spec.Conversation.Seed
	}
	raw, err := json.Marshal(plan)
	if err != nil || len(raw) > 65536 {
		return nil, "", fmt.Errorf("bounded platform provision plan required")
	}
	return raw, principal, nil
}

func mustDecisionDigest(d cellnauthority.PlatformDecision) string {
	digest, err := d.Digest()
	if err != nil {
		return ""
	}
	return digest
}

func requestCaller(raw []byte) (string, error) {
	var request struct {
		Workload struct {
			Caller string `json:"caller"`
		} `json:"workload"`
	}
	if json.Unmarshal(raw, &request) != nil || !boundedIdentity(request.Workload.Caller, 512) {
		return "", fmt.Errorf("native request lacks a bounded caller principal")
	}
	return request.Workload.Caller, nil
}

// The parent's lifetime is the only run-specific value inside the owner-side
// request; everything else stays exactly as the package reviewed it.
func parentRequestWithLease(raw []byte, principal string, leaseSeconds int64) (json.RawMessage, error) {
	caller, err := requestCaller(raw)
	if err != nil || caller != principal {
		return nil, fmt.Errorf("native parent and worker requests must share one principal")
	}
	var request map[string]any
	if json.Unmarshal(raw, &request) != nil {
		return nil, fmt.Errorf("native parent request is not an object")
	}
	capabilities, ok := request["capabilities"].(map[string]any)
	if !ok || leaseSeconds < 1 {
		return nil, fmt.Errorf("native parent request lacks capabilities")
	}
	capabilities["timeoutMs"] = leaseSeconds * 1000
	out, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// revalidatePlatformAuthority refuses further work on a platform-admitted run
// whose namespace, policy, catalogue or route authority has changed since the
// parent was issued. Runs admitted through prepared registrations have no
// frozen record and are unaffected.
func revalidatePlatformAuthority(ctx context.Context, reader client.Reader, config string, run *api.AgentRun) error {
	info, err := os.Stat(config)
	if err != nil || !info.IsDir() || !filepath.IsAbs(config) {
		return nil
	}
	raw, err := boundedFile(filepath.Join(config, platformRecordName(string(run.UID))), platformRecordLimit)
	if err != nil {
		if strings.Contains(err.Error(), "unavailable") {
			return nil
		}
		return err
	}
	var frozen frozenPlatformAuthority
	if json.Unmarshal(raw, &frozen) != nil || frozen.Resolution.Decision.Run.UID != string(run.UID) || frozen.Request.ClusterID == "" {
		return fmt.Errorf("frozen platform authority is unreadable; refusing new work")
	}
	frozen.Resolution.Request = frozen.Request
	resolver := cellnauthority.PlatformResolver{Reader: reader}
	return resolver.Revalidate(ctx, client.ObjectKeyFromObject(run), frozen.Resolution)
}

type frozenPlatformAuthority struct {
	Resolution cellnauthority.PlatformResolution     `json:"resolution"`
	Request    cellnauthority.PlatformResolveRequest `json:"request"`
}
