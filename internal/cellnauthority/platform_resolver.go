package cellnauthority

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	ReasonPolicyWithdrawn  = "AUTH_POLICY_WITHDRAWN"
	ReasonPolicyContracted = "AUTH_POLICY_CONTRACTED"
	ReasonToolUnknown      = "AUTH_TOOL_UNKNOWN"
	ReasonToolOrder        = "AUTH_TOOL_ORDER_MISMATCH"
	ReasonRouteMismatch    = "AUTH_ROUTE_MISMATCH"
	ReasonNamespaceUID     = "AUTH_NAMESPACE_UID_MISMATCH"
	ReasonParentTurn       = "AUTH_PARENT_TURN_MISMATCH"
	ReasonLimitRange       = "AUTH_LIMIT_OUT_OF_RANGE"
	ReasonLifecycle        = "AUTH_LIFECYCLE_INVALID"
)

type PlatformResolutionError struct {
	Reason string
	Detail string
}

func (e *PlatformResolutionError) Error() string { return e.Reason + ": " + e.Detail }

func deny(reason, format string, args ...any) error {
	return &PlatformResolutionError{Reason: reason, Detail: fmt.Sprintf(format, args...)}
}

func PlatformReason(err error) string {
	var target *PlatformResolutionError
	if errors.As(err, &target) {
		return target.Reason
	}
	return ""
}

type PlatformResolveRequest struct {
	ClusterID         string
	Now               time.Time
	AdmissionWindow   time.Duration
	Operation         string
	ParentIncarnation string
	TurnKey           *types.NamespacedName
	Original          *PlatformDecision
}

type platformSnapshot struct {
	Namespace  corev1.Namespace
	Agent      api.Agent
	Runtime    api.AgentRuntime
	Run        api.AgentRun
	Profile    api.CellnRuntimeProfile
	Tools      []api.ClusterCellnTool
	Policies   []api.CellnExecutionPolicy
	Connection *api.ModelConnection
	Turn       *api.AgentRunTurn
}

type PlatformObjectRevision struct {
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace,omitempty"`
	Name       string `json:"name"`
	UID        string `json:"uid"`
	Generation int64  `json:"generation"`
	Digest     string `json:"digest"`
}

type PlatformResolution struct {
	Decision PlatformDecision         `json:"decision"`
	ReadSet  []PlatformObjectRevision `json:"readSet"`
	Request  PlatformResolveRequest   `json:"-"`
}

// PlatformResolver must be constructed with the manager's APIReader, never a
// cached client. All tenant reads are forced into the AgentRun namespace.
type PlatformResolver struct {
	Reader client.Reader
}

func (r PlatformResolver) Resolve(ctx context.Context, runKey types.NamespacedName, request PlatformResolveRequest) (*PlatformResolution, error) {
	if r.Reader == nil || runKey.Namespace == "" || runKey.Name == "" {
		return nil, deny(ReasonPolicyWithdrawn, "uncached reader and namespaced run identity are required")
	}
	if request.Now.IsZero() {
		request.Now = time.Now().UTC()
	}
	request.Now = request.Now.UTC().Truncate(time.Second)
	if request.AdmissionWindow == 0 {
		request.AdmissionWindow = 60 * time.Second
	}
	if request.AdmissionWindow < time.Second || request.AdmissionWindow > 60*time.Second || strings.TrimSpace(request.ClusterID) == "" {
		return nil, deny(ReasonLimitRange, "cluster identity and an admission window of 1-60 seconds are required")
	}

	first, err := r.load(ctx, runKey, request.TurnKey)
	if err != nil {
		return nil, err
	}
	decision, err := evaluatePlatform(first, request)
	if err != nil {
		return nil, err
	}
	readSet, err := snapshotReadSet(first)
	if err != nil {
		return nil, err
	}
	second, err := r.load(ctx, runKey, request.TurnKey)
	if err != nil {
		return nil, err
	}
	secondReadSet, err := snapshotReadSet(second)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(readSet, secondReadSet) {
		return nil, deny(ReasonPolicyContracted, "an authority object changed during resolution")
	}
	return &PlatformResolution{Decision: *decision, ReadSet: readSet, Request: request}, nil
}

func (r PlatformResolver) Revalidate(ctx context.Context, runKey types.NamespacedName, frozen PlatformResolution) error {
	current, err := r.Resolve(ctx, runKey, frozen.Request)
	if err != nil {
		return err
	}
	if current.Decision.Run.NamespaceUID != frozen.Decision.Run.NamespaceUID {
		return deny(ReasonNamespaceUID, "namespace identity changed before admission")
	}
	if !reflect.DeepEqual(current.Decision.Tools, frozen.Decision.Tools) && sameToolSet(current.Decision.Tools, frozen.Decision.Tools) {
		return deny(ReasonToolOrder, "ordered tool selection changed before admission")
	}
	if !sameRouteAuthority(frozen.Decision.Route, current.Decision.Route) {
		return deny(ReasonRouteMismatch, "model route changed before admission")
	}
	if !reflect.DeepEqual(current.Decision.Parent, frozen.Decision.Parent) {
		return deny(ReasonParentTurn, "parent or turn identity changed before admission")
	}
	if !reflect.DeepEqual(current.ReadSet, frozen.ReadSet) || !reflect.DeepEqual(current.Decision, frozen.Decision) {
		return deny(ReasonPolicyContracted, "resolved authority changed before admission")
	}
	return nil
}

func sameToolSet(left, right []DecisionToolBinding) bool {
	if len(left) != len(right) {
		return false
	}
	l := append([]DecisionToolBinding(nil), left...)
	r := append([]DecisionToolBinding(nil), right...)
	sort.Slice(l, func(i, j int) bool { return l[i].Name < l[j].Name })
	sort.Slice(r, func(i, j int) bool { return r[i].Name < r[j].Name })
	return reflect.DeepEqual(l, r)
}

type PlatformPermissionPreview struct {
	DecisionDigest string                `json:"decisionDigest"`
	Lifecycle      string                `json:"lifecycle"`
	Runtime        RuntimeBinding        `json:"runtime"`
	Tools          []DecisionToolBinding `json:"tools"`
	Route          DecisionRouteBinding  `json:"route"`
	Budget         DecisionBudgetBinding `json:"budget"`
	Readiness      string                `json:"readiness"`
}

func (r PlatformResolver) Preview(ctx context.Context, runKey types.NamespacedName, request PlatformResolveRequest) (*PlatformPermissionPreview, error) {
	resolution, err := r.Resolve(ctx, runKey, request)
	if err != nil {
		return nil, err
	}
	digest, err := resolution.Decision.Digest()
	if err != nil {
		return nil, err
	}
	d := resolution.Decision
	return &PlatformPermissionPreview{DecisionDigest: digest, Lifecycle: d.Lifecycle, Runtime: d.Runtime, Tools: d.Tools, Route: d.Route, Budget: d.Budget, Readiness: "not-established"}, nil
}

func (r PlatformResolver) load(ctx context.Context, runKey types.NamespacedName, turnKey *types.NamespacedName) (platformSnapshot, error) {
	var snapshot platformSnapshot
	if err := r.Reader.Get(ctx, types.NamespacedName{Name: runKey.Namespace}, &snapshot.Namespace); err != nil {
		return snapshot, deny(ReasonPolicyWithdrawn, "namespace unavailable: %v", err)
	}
	if !liveMeta(snapshot.Namespace.ObjectMeta, false) {
		return snapshot, deny(ReasonPolicyWithdrawn, "namespace is not live")
	}
	if err := r.Reader.Get(ctx, runKey, &snapshot.Run); err != nil {
		return snapshot, deny(ReasonPolicyWithdrawn, "run unavailable: %v", err)
	}
	if !liveMeta(snapshot.Run.ObjectMeta, true) || snapshot.Run.Namespace != runKey.Namespace || snapshot.Run.Spec.AgentRef == "" {
		return snapshot, deny(ReasonPolicyContracted, "run identity is invalid or deleting")
	}
	if err := r.Reader.Get(ctx, types.NamespacedName{Namespace: runKey.Namespace, Name: snapshot.Run.Spec.AgentRef}, &snapshot.Agent); err != nil {
		return snapshot, deny(ReasonPolicyWithdrawn, "Agent unavailable: %v", err)
	}
	if !liveMeta(snapshot.Agent.ObjectMeta, true) || snapshot.Agent.Namespace != runKey.Namespace {
		return snapshot, deny(ReasonPolicyContracted, "Agent identity is invalid or deleting")
	}
	if snapshot.Run.Spec.CellnSelection == nil || snapshot.Run.Spec.Backend != "celln" || snapshot.Run.Spec.Celln != nil {
		return snapshot, deny(ReasonLifecycle, "run must use the explicit Celln catalogue path")
	}
	runtimeName := snapshot.Run.Spec.CellnSelection.RuntimeRef
	if runtimeName == "" {
		runtimeName = snapshot.Agent.Spec.RuntimeRef
	}
	if runtimeName == "" {
		return snapshot, deny(ReasonPolicyContracted, "run has no namespaced runtime wrapper")
	}
	if err := r.Reader.Get(ctx, types.NamespacedName{Namespace: runKey.Namespace, Name: runtimeName}, &snapshot.Runtime); err != nil {
		return snapshot, deny(ReasonPolicyWithdrawn, "runtime unavailable: %v", err)
	}
	if !liveMeta(snapshot.Runtime.ObjectMeta, true) || snapshot.Runtime.Namespace != runKey.Namespace || snapshot.Runtime.Spec.CellnProfileRef == nil || snapshot.Runtime.Spec.Celln != nil {
		return snapshot, deny(ReasonPolicyContracted, "runtime is not a live shared-profile wrapper")
	}
	if err := r.Reader.Get(ctx, types.NamespacedName{Name: snapshot.Runtime.Spec.CellnProfileRef.Name}, &snapshot.Profile); err != nil {
		return snapshot, deny(ReasonPolicyWithdrawn, "runtime profile unavailable: %v", err)
	}
	if !liveMeta(snapshot.Profile.ObjectMeta, true) || snapshot.Profile.Namespace != "" || snapshot.Profile.Spec.Revision != snapshot.Runtime.Spec.CellnProfileRef.Revision {
		return snapshot, deny(ReasonPolicyContracted, "runtime profile revision changed or is invalid")
	}

	selection := snapshot.Run.Spec.CellnSelection
	if len(selection.ToolRefs) != 0 || len(selection.ClusterToolRefs) > 16 {
		return snapshot, deny(ReasonPolicyContracted, "shared catalogue resolution cannot mix legacy namespaced tools")
	}
	seen := map[string]bool{}
	for _, ref := range selection.ClusterToolRefs {
		if ref.Name == "" || ref.Revision == "" || seen[ref.Name] {
			return snapshot, deny(ReasonToolOrder, "cluster tool refs require unique names and exact revisions")
		}
		seen[ref.Name] = true
		var tool api.ClusterCellnTool
		if err := r.Reader.Get(ctx, types.NamespacedName{Name: ref.Name}, &tool); err != nil {
			return snapshot, deny(ReasonToolUnknown, "cluster tool %q unavailable: %v", ref.Name, err)
		}
		if !liveMeta(tool.ObjectMeta, true) || tool.Namespace != "" || tool.Spec.Revision != ref.Revision {
			return snapshot, deny(ReasonToolUnknown, "cluster tool %q revision changed", ref.Name)
		}
		snapshot.Tools = append(snapshot.Tools, tool)
	}

	var policies api.CellnExecutionPolicyList
	if err := r.Reader.List(ctx, &policies); err != nil {
		return snapshot, deny(ReasonPolicyWithdrawn, "execution policies unavailable: %v", err)
	}
	for i := range policies.Items {
		policy := policies.Items[i]
		if !liveMeta(policy.ObjectMeta, true) || policy.Namespace != "" {
			continue
		}
		selector, err := metav1.LabelSelectorAsSelector(&policy.Spec.NamespaceSelector)
		if err != nil {
			return snapshot, deny(ReasonPolicyContracted, "policy %q has invalid namespace selector", policy.Name)
		}
		if selector.Matches(labels.Set(snapshot.Namespace.Labels)) {
			snapshot.Policies = append(snapshot.Policies, policy)
		}
	}
	sort.Slice(snapshot.Policies, func(i, j int) bool { return snapshot.Policies[i].Name < snapshot.Policies[j].Name })
	if len(snapshot.Policies) == 0 {
		return snapshot, deny(ReasonPolicyWithdrawn, "no execution policy selects the namespace")
	}

	if ref := snapshot.Run.Spec.Model.ConnectionRef; ref != "" {
		var connection api.ModelConnection
		if err := r.Reader.Get(ctx, types.NamespacedName{Namespace: runKey.Namespace, Name: ref}, &connection); err != nil {
			return snapshot, deny(ReasonRouteMismatch, "model connection unavailable: %v", err)
		}
		if !liveMeta(connection.ObjectMeta, true) || connection.Namespace != runKey.Namespace {
			return snapshot, deny(ReasonRouteMismatch, "model connection identity is invalid")
		}
		snapshot.Connection = &connection
	}
	if turnKey != nil {
		if turnKey.Namespace != runKey.Namespace || turnKey.Name == "" {
			return snapshot, deny(ReasonParentTurn, "turn must be in the run namespace")
		}
		var turn api.AgentRunTurn
		if err := r.Reader.Get(ctx, *turnKey, &turn); err != nil {
			return snapshot, deny(ReasonParentTurn, "turn unavailable: %v", err)
		}
		if !liveMeta(turn.ObjectMeta, true) || turn.Namespace != runKey.Namespace || turn.Spec.RunName != snapshot.Run.Name || turn.Spec.RunUID != string(snapshot.Run.UID) {
			return snapshot, deny(ReasonParentTurn, "turn is not bound to this run")
		}
		snapshot.Turn = &turn
	}
	return snapshot, nil
}

func evaluatePlatform(s platformSnapshot, request PlatformResolveRequest) (*PlatformDecision, error) {
	if reason := s.Run.Spec.ValidateLifecycle(); reason != "" {
		return nil, deny(ReasonLifecycle, "%s", reason)
	}
	if len(s.Agent.Spec.Skills) != 0 || len(s.Agent.Spec.MCPServers) != 0 || len(s.Run.Spec.Env) != 0 || s.Run.Spec.Parent != nil || s.Run.Spec.Sandbox != nil || s.Run.Spec.AgentSandbox != nil || len(s.Run.Spec.Skills) != 0 || s.Run.Spec.ToolPolicy != nil || s.Run.Spec.CanaryMode || s.Run.Spec.DryRun || len(s.Run.Spec.ImagePullSecrets) != 0 || s.Run.Spec.Lifecycle != nil || len(s.Run.Spec.Volumes) != 0 || len(s.Run.Spec.VolumeMounts) != 0 || (s.Run.Spec.Mode != "" && s.Run.Spec.Mode != "task") || (s.Run.Spec.UseContext != nil && !*s.Run.Spec.UseContext) || (s.Run.Spec.Cleanup != "" && s.Run.Spec.Cleanup != "delete") {
		return nil, deny(ReasonLifecycle, "run or Agent requests unsupported pod, delegation, lifecycle, or context authority")
	}
	if !s.Run.Spec.Task.IsString() || strings.TrimSpace(s.Run.Spec.Task.GetPrompt()) == "" {
		return nil, deny(ReasonLifecycle, "a bounded string task is required")
	}
	lifecycle := "direct-one-shot"
	modelRequired := s.Connection != nil || s.Run.Spec.Model.Model != "" || s.Run.Spec.Model.Provider != ""
	if s.Run.Spec.ExecutionLifecycle == "enduring" {
		lifecycle = "enduring"
		modelRequired = true
	} else if modelRequired {
		lifecycle = "harness-one-shot"
	}
	profileLifecycle := "disposable-one-shot"
	if lifecycle == "enduring" {
		profileLifecycle = "enduring"
	}
	if err := validateRuntimeProfile(s.Profile, profileLifecycle); err != nil {
		return nil, err
	}
	for _, policy := range s.Policies {
		if !slices.Contains(policy.Spec.Lifecycles, lifecycle) {
			return nil, deny(ReasonPolicyContracted, "policy %q does not permit lifecycle %q", policy.Name, lifecycle)
		}
	}

	runtimeLimits := s.Profile.Spec.Limits
	if s.Runtime.Spec.CellnLimits != nil {
		var err error
		runtimeLimits, err = intersectRuntimeLimits(runtimeLimits, *s.Runtime.Spec.CellnLimits)
		if err != nil {
			return nil, err
		}
	}
	for _, policy := range s.Policies {
		matched := false
		for _, allowed := range policy.Spec.RuntimeProfiles {
			if allowed.Ref.Name == s.Profile.Name && allowed.Ref.Revision == s.Profile.Spec.Revision {
				matched = true
				if allowed.Limits != nil {
					if _, err := intersectRuntimeLimits(s.Profile.Spec.Limits, *allowed.Limits); err != nil {
						return nil, err
					}
					runtimeLimits = minimumRuntimeLimits(runtimeLimits, *allowed.Limits)
				}
				break
			}
		}
		if !matched {
			return nil, deny(ReasonPolicyContracted, "policy %q does not permit runtime revision", policy.Name)
		}
	}
	if int64(len(s.Run.Spec.Task.GetPrompt())) > runtimeLimits.TaskBytes {
		return nil, deny(ReasonLimitRange, "task exceeds effective runtime limit")
	}

	tools := make([]DecisionToolBinding, 0, len(s.Tools))
	for _, tool := range s.Tools {
		if err := validateClusterTool(tool); err != nil {
			return nil, deny(ReasonToolUnknown, "tool %q is not a supported immutable tool-lane artifact", tool.Name)
		}
		limits := *tool.Spec.Limits.DeepCopy()
		for _, policy := range s.Policies {
			matched := false
			for _, allowed := range policy.Spec.Tools {
				if allowed.Ref.Name == tool.Name && allowed.Ref.Revision == tool.Spec.Revision {
					matched = true
					if allowed.Limits != nil {
						if !validToolLimits(*allowed.Limits) {
							return nil, deny(ReasonLimitRange, "policy %q has invalid limits for tool %q", policy.Name, tool.Name)
						}
						limits = intersectToolLimits(limits, *allowed.Limits)
					}
					break
				}
			}
			if !matched {
				return nil, deny(ReasonToolUnknown, "policy %q does not permit tool %q at the selected revision", policy.Name, tool.Name)
			}
		}
		tools = append(tools, decisionTool(tool, limits))
	}

	route, err := resolveDecisionRoute(s, modelRequired)
	if err != nil {
		return nil, err
	}
	policyBinding, err := bindPolicies(s.Policies)
	if err != nil {
		return nil, err
	}
	runDigest, err := digestJSON(s.Run.Spec)
	if err != nil {
		return nil, err
	}
	agentDigest, err := digestJSON(s.Agent.Spec)
	if err != nil {
		return nil, err
	}
	profileDigest, err := digestJSON(s.Profile.Spec)
	if err != nil {
		return nil, err
	}
	runtimeDigest, err := digestJSON(struct {
		WrapperSpec   api.AgentRuntimeSpec `json:"wrapperSpec"`
		ProfileName   string               `json:"profileName"`
		ProfileUID    string               `json:"profileUid"`
		ProfileDigest string               `json:"profileDigest"`
	}{s.Runtime.Spec, s.Profile.Name, string(s.Profile.UID), profileDigest})
	if err != nil {
		return nil, err
	}

	now := request.Now.Unix()
	operation := request.Operation
	if operation == "" {
		operation = "execution.start"
	}
	var parent *ParentBinding
	payload := s.Run.Spec.Task.GetPrompt()
	if s.Turn != nil {
		if operation != "execution.turn" || request.Original == nil || !hashPattern.MatchString(request.ParentIncarnation) {
			return nil, deny(ReasonParentTurn, "turn resolution requires the original decision and parent incarnation")
		}
		turnID := string(s.Turn.UID)
		if turnID == "" {
			turnID = s.Turn.Name
		}
		parent = &ParentBinding{Incarnation: request.ParentIncarnation, TurnID: &turnID}
		payload = s.Turn.Spec.Message
	} else if lifecycle == "enduring" {
		if operation != "execution.start" || !hashPattern.MatchString(request.ParentIncarnation) {
			return nil, deny(ReasonParentTurn, "enduring parent start requires its persisted incarnation")
		}
		parent = &ParentBinding{Incarnation: request.ParentIncarnation}
	} else if operation != "execution.start" {
		return nil, deny(ReasonLifecycle, "one-shot resolution only permits execution.start")
	}

	budget, err := resolveBudget(s, request, modelRequired, runtimeLimits)
	if err != nil {
		return nil, err
	}
	requestBinding := map[string]any{"apiVersion": "celln.sympozium.ai/execution-request-v1", "operation": operation, "payload": payload, "runUid": string(s.Run.UID)}
	if parent != nil {
		requestBinding["parentIncarnation"] = parent.Incarnation
		if parent.TurnID != nil {
			requestBinding["turnId"] = *parent.TurnID
		}
	}
	requestDigest, err := digestJSON(requestBinding)
	if err != nil {
		return nil, err
	}
	decision := &PlatformDecision{
		APIVersion: PlatformDecisionAPIVersion, Kind: "CellnAuthorisationDecision", ClusterID: request.ClusterID,
		Run:       RunBinding{Namespace: s.Run.Namespace, NamespaceUID: string(s.Namespace.UID), Name: s.Run.Name, UID: string(s.Run.UID), SpecSHA256: runDigest},
		Subject:   SubjectBinding{Kind: "AgentRun", Namespace: s.Run.Namespace, Name: s.Run.Name, UID: string(s.Run.UID), SpecSHA256: runDigest},
		Operation: operation, Lifecycle: decisionLifecycle(lifecycle, s.Turn != nil), Parent: parent,
		Runtime: RuntimeBinding{Name: s.Runtime.Name, UID: string(s.Runtime.UID), Revision: s.Profile.Spec.Revision, SpecSHA256: runtimeDigest},
		Agent:   SubjectBinding{Kind: "Agent", Namespace: s.Agent.Namespace, Name: s.Agent.Name, UID: string(s.Agent.UID), SpecSHA256: agentDigest},
		Policy:  policyBinding, Tools: tools, Route: route, Budget: budget,
		Windows: DecisionWindows{IssuedAt: now, NotBefore: now, AdmissionDeadline: now + int64(request.AdmissionWindow/time.Second)}, RequestDigest: requestDigest,
	}
	if request.Original != nil {
		if err := retainOriginalAuthority(*request.Original, *decision); err != nil {
			return nil, err
		}
	}
	return decision, nil
}

func resolveDecisionRoute(s platformSnapshot, required bool) (DecisionRouteBinding, error) {
	empty := DecisionRouteBinding{Provider: "none", Protocol: "none", Auth: "none"}
	if !required {
		if s.Connection != nil || s.Run.Spec.Model.ConnectionRef != "" || s.Run.Spec.Model.Model != "" || s.Run.Spec.Model.Provider != "" {
			return empty, deny(ReasonRouteMismatch, "model-free execution cannot carry a model route")
		}
		return empty, nil
	}
	if s.Connection == nil || s.Run.Spec.Model.ConnectionRef == "" || s.Run.Spec.Model.Model == "" {
		return empty, deny(ReasonRouteMismatch, "model-using execution requires a namespaced ModelConnection and model")
	}
	c := s.Connection
	if err := c.Spec.Validate(); err != nil || c.Spec.Disabled || c.DeletionTimestamp != nil || !slices.Contains(c.Spec.Models, s.Run.Spec.Model.Model) {
		return empty, deny(ReasonRouteMismatch, "model connection is disabled, incompatible, or does not list the model")
	}
	if s.Run.Spec.Model.Provider != "" && s.Run.Spec.Model.Provider != c.Spec.Provider || s.Run.Spec.Model.BaseURL != "" && s.Run.Spec.Model.BaseURL != c.Spec.Endpoint || s.Run.Spec.Model.Protocol != "" && s.Run.Spec.Model.Protocol != c.Spec.Protocol || s.Run.Spec.Model.AuthSecretRef != "" || s.Run.Spec.Model.CredentialProfile != "" || s.Run.Spec.Model.ModelRef != "" || s.Run.Spec.Model.AllowInsecure || len(s.Run.Spec.Model.ProviderHeaders) != 0 || s.Run.Spec.Model.ProviderHeadersSecretRef != "" || len(s.Run.Spec.Model.NodeSelector) != 0 || (s.Run.Spec.Model.Thinking != "" && s.Run.Spec.Model.Thinking != "off") {
		return empty, deny(ReasonRouteMismatch, "inline model authority or route override is forbidden")
	}
	auth := "none"
	if c.Spec.SecretRef != "" {
		auth = "secret"
	}
	if c.Spec.CredentialProfile != "" {
		return empty, deny(ReasonRouteMismatch, "shared namespace execution requires gateway Secret custody, not a host credential profile")
	}
	var origin string
	var err error
	if auth == "secret" {
		origin, err = api.ModelEndpointOrigin(c.Spec.Endpoint)
	} else {
		origin, err = api.ModelEndpointOriginInsecure(c.Spec.Endpoint, c.Spec.AllowInsecure)
	}
	if err != nil {
		return empty, deny(ReasonRouteMismatch, "%v", err)
	}
	for _, policy := range s.Policies {
		allowed := false
		for _, candidate := range policy.Spec.Routes {
			if candidate.Provider == c.Spec.Provider && candidate.Protocol == c.Spec.Protocol && candidate.Auth == auth && slices.Contains(candidate.Models, s.Run.Spec.Model.Model) && slices.Contains(candidate.EndpointOrigins, origin) {
				allowed = true
				break
			}
		}
		if !allowed {
			return empty, deny(ReasonRouteMismatch, "policy %q does not permit the exact model route", policy.Name)
		}
	}
	uid := string(c.UID)
	specDigest, err := digestJSON(c.Spec)
	if err != nil {
		return empty, err
	}
	if revision := s.Run.Spec.Model.ConnectionRevision; revision != "" && revision != specDigest {
		return empty, deny(ReasonRouteMismatch, "pinned model connection revision changed")
	}
	route := DecisionRouteBinding{ModelConnectionUID: &uid, ModelConnectionSpecSHA256: specDigest, Provider: c.Spec.Provider, Protocol: c.Spec.Protocol, Model: s.Run.Spec.Model.Model, EndpointOrigin: origin, Auth: auth}
	if auth == "secret" {
		secretKey := "OPENAI_API_KEY"
		if c.Spec.Protocol == "anthropic-messages" {
			secretKey = "ANTHROPIC_API_KEY"
		}
		route.CredentialSourceRef = &CredentialSourceRef{Kind: "Secret", SecretName: c.Spec.SecretRef, SecretKey: secretKey}
	}
	return route, nil
}

func resolveBudget(s platformSnapshot, request PlatformResolveRequest, modelRequired bool, runtime api.AgentRuntimeCellnLimits) (DecisionBudgetBinding, error) {
	ceilings := s.Policies[0].Spec.Ceilings
	for _, policy := range s.Policies[1:] {
		ceilings.MaxTurns = min(ceilings.MaxTurns, policy.Spec.Ceilings.MaxTurns)
		ceilings.MaxModelRequests = min(ceilings.MaxModelRequests, policy.Spec.Ceilings.MaxModelRequests)
		ceilings.MaxOutputTokens = min(ceilings.MaxOutputTokens, policy.Spec.Ceilings.MaxOutputTokens)
		ceilings.MaxParentLeaseSeconds = min(ceilings.MaxParentLeaseSeconds, policy.Spec.Ceilings.MaxParentLeaseSeconds)
		ceilings.MaxTurnSeconds = min(ceilings.MaxTurnSeconds, policy.Spec.Ceilings.MaxTurnSeconds)
	}
	if ceilings.MaxTurns < 1 || ceilings.MaxParentLeaseSeconds < 1 || ceilings.MaxTurnSeconds < 1 {
		return DecisionBudgetBinding{}, deny(ReasonLimitRange, "policy ceilings are invalid")
	}
	maxTurns := int64(1)
	runCap := DecisionCap{}
	parentDeadline := int64(0)
	if modelRequired {
		runCap = DecisionCap{Requests: ceilings.MaxModelRequests, OutputTokens: ceilings.MaxOutputTokens}
		if runCap.Requests < 1 || runCap.OutputTokens < 1 {
			return DecisionBudgetBinding{}, deny(ReasonLimitRange, "model route has no positive request/output budget")
		}
	}
	if s.Run.Spec.ExecutionLifecycle == "enduring" {
		e := s.Run.Spec.Enduring
		maxTurns = min(int64(e.MaxTurns), ceilings.MaxTurns)
		runCap.Requests = min(int64(e.MaxModelRequests), runCap.Requests)
		runCap.OutputTokens = min(e.MaxOutputTokens, runCap.OutputTokens)
		lease := min(int64(e.LeaseSeconds), ceilings.MaxParentLeaseSeconds)
		if maxTurns != int64(e.MaxTurns) || runCap.Requests != int64(e.MaxModelRequests) || runCap.OutputTokens != e.MaxOutputTokens || lease != int64(e.LeaseSeconds) {
			return DecisionBudgetBinding{}, deny(ReasonLimitRange, "requested enduring limits exceed the policy intersection")
		}
		parentDeadline = request.Now.Unix() + lease
	}
	turnSeconds := min(ceilings.MaxTurnSeconds, max(int64(1), runtime.TimeoutMillis/1000))
	if s.Run.Spec.Timeout != nil {
		turnSeconds = min(turnSeconds, max(int64(1), int64(s.Run.Spec.Timeout.Duration/time.Second)))
	}
	turnDeadline := request.Now.Unix() + turnSeconds
	turnCap := runCap
	if maxTurns > 1 {
		turnCap.Requests = min(turnCap.Requests, max(int64(1), turnCap.Requests/maxTurns))
		turnCap.OutputTokens = min(turnCap.OutputTokens, max(int64(1), turnCap.OutputTokens/maxTurns))
	}
	budgetID, err := digestJSON(map[string]string{"clusterId": request.ClusterID, "namespaceUid": string(s.Namespace.UID), "runUid": string(s.Run.UID)})
	if err != nil {
		return DecisionBudgetBinding{}, err
	}
	if request.Original != nil {
		original := request.Original.Budget
		budgetID, runCap, maxTurns, parentDeadline = original.BudgetID, original.RunCap, original.MaxTurns, original.ParentDeadlineUnix
		if parentDeadline > 0 {
			turnDeadline = min(turnDeadline, parentDeadline)
		}
		turnCap.Requests = min(turnCap.Requests, original.TurnCap.Requests)
		turnCap.OutputTokens = min(turnCap.OutputTokens, original.TurnCap.OutputTokens)
	}
	return DecisionBudgetBinding{BudgetID: budgetID, RunCap: runCap, TurnCap: turnCap, MaxTurns: maxTurns, ParentDeadlineUnix: parentDeadline, TurnDeadlineUnix: turnDeadline}, nil
}

func retainOriginalAuthority(original, current PlatformDecision) error {
	if original.ClusterID != current.ClusterID || original.Run != current.Run || original.Agent != current.Agent || original.Runtime != current.Runtime || original.Policy != current.Policy || !reflect.DeepEqual(original.Tools, current.Tools) || !sameRouteAuthority(original.Route, current.Route) || original.Budget.BudgetID != current.Budget.BudgetID || original.Budget.RunCap != current.Budget.RunCap || original.Budget.MaxTurns != current.Budget.MaxTurns || original.Budget.ParentDeadlineUnix != current.Budget.ParentDeadlineUnix {
		return deny(ReasonPolicyContracted, "turn authority differs from the originally admitted run")
	}
	return nil
}

func sameRouteAuthority(original, current DecisionRouteBinding) bool {
	if !reflect.DeepEqual(original.ModelConnectionUID, current.ModelConnectionUID) || original.ModelConnectionSpecSHA256 != current.ModelConnectionSpecSHA256 || original.Provider != current.Provider || original.Protocol != current.Protocol || original.Model != current.Model || original.EndpointOrigin != current.EndpointOrigin || original.Auth != current.Auth || original.Streaming != current.Streaming {
		return false
	}
	if original.Auth == "none" {
		return original.CredentialSource == nil && original.CredentialSourceRef == nil && current.CredentialSource == nil && current.CredentialSourceRef == nil
	}
	if current.CredentialSourceRef == nil {
		return reflect.DeepEqual(original.CredentialSource, current.CredentialSource)
	}
	if original.CredentialSource != nil {
		return original.CredentialSource.Kind == current.CredentialSourceRef.Kind && original.CredentialSource.SecretName == current.CredentialSourceRef.SecretName && original.CredentialSource.SecretKey == current.CredentialSourceRef.SecretKey
	}
	return reflect.DeepEqual(original.CredentialSourceRef, current.CredentialSourceRef)
}

func bindPolicies(policies []api.CellnExecutionPolicy) (PolicyBinding, error) {
	type bound struct {
		Name string                       `json:"name"`
		UID  string                       `json:"uid"`
		Spec api.CellnExecutionPolicySpec `json:"spec"`
	}
	items := make([]bound, 0, len(policies))
	names := make([]string, 0, len(policies))
	for _, policy := range policies {
		items = append(items, bound{Name: policy.Name, UID: string(policy.UID), Spec: *policy.Spec.DeepCopy()})
		names = append(names, policy.Name)
	}
	digest, err := digestJSON(items)
	if err != nil {
		return PolicyBinding{}, err
	}
	profile := strings.Join(names, "+")
	if len(profile) > 253 {
		profile = "policy-intersection"
	}
	return PolicyBinding{Profile: profile, Revision: "set-v1", Digest: digest}, nil
}

func snapshotReadSet(s platformSnapshot) ([]PlatformObjectRevision, error) {
	items := []struct {
		kind string
		meta metav1.ObjectMeta
		spec any
	}{
		{"Namespace", s.Namespace.ObjectMeta, struct {
			Labels map[string]string `json:"labels"`
		}{s.Namespace.Labels}},
		{"Agent", s.Agent.ObjectMeta, s.Agent.Spec}, {"AgentRuntime", s.Runtime.ObjectMeta, s.Runtime.Spec},
		{"AgentRun", s.Run.ObjectMeta, s.Run.Spec}, {"CellnRuntimeProfile", s.Profile.ObjectMeta, s.Profile.Spec},
	}
	for i := range s.Tools {
		items = append(items, struct {
			kind string
			meta metav1.ObjectMeta
			spec any
		}{"ClusterCellnTool", s.Tools[i].ObjectMeta, s.Tools[i].Spec})
	}
	for i := range s.Policies {
		items = append(items, struct {
			kind string
			meta metav1.ObjectMeta
			spec any
		}{"CellnExecutionPolicy", s.Policies[i].ObjectMeta, s.Policies[i].Spec})
	}
	if s.Connection != nil {
		items = append(items, struct {
			kind string
			meta metav1.ObjectMeta
			spec any
		}{"ModelConnection", s.Connection.ObjectMeta, s.Connection.Spec})
	}
	if s.Turn != nil {
		items = append(items, struct {
			kind string
			meta metav1.ObjectMeta
			spec any
		}{"AgentRunTurn", s.Turn.ObjectMeta, s.Turn.Spec})
	}
	result := make([]PlatformObjectRevision, 0, len(items))
	for _, item := range items {
		digest, err := digestJSON(item.spec)
		if err != nil {
			return nil, err
		}
		result = append(result, PlatformObjectRevision{Kind: item.kind, Namespace: item.meta.Namespace, Name: item.meta.Name, UID: string(item.meta.UID), Generation: item.meta.Generation, Digest: digest})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Kind+"/"+result[i].Namespace+"/"+result[i].Name < result[j].Kind+"/"+result[j].Namespace+"/"+result[j].Name
	})
	return result, nil
}

func liveMeta(meta metav1.ObjectMeta, requireGeneration bool) bool {
	return meta.Name != "" && meta.UID != "" && meta.DeletionTimestamp == nil && (!requireGeneration || meta.Generation > 0)
}

func validateRuntimeProfile(profile api.CellnRuntimeProfile, lifecycle string) error {
	s := profile.Spec
	if s.ContractVersion != "celln.json-tools/v1" || s.Platform != "linux/amd64" || s.Lane != "agent" || !slices.Contains(s.Lifecycles, lifecycle) || s.JSON == nil || s.JSON.MaxTurns < 1 || s.JSON.MaxTurns > 6 || s.JSON.MaxCalls < 0 || s.JSON.MaxCalls > 16 || !hashPattern.MatchString(s.Executable.Hash) || !hashPattern.MatchString(s.Closure.Hash) || !hashPattern.MatchString(s.Mote.Hash) || !publisherPattern.MatchString(s.PublisherKey) || !pathPattern.MatchString(s.EntryPoint) || s.EntryPoint == "/pilot-fetch" || s.Limits.TimeoutMillis < 1 || s.Limits.MemoryBytes < 1 || s.Limits.TaskBytes < 1 || s.Limits.OutputBytes < 1 || s.Limits.Workspace != "none" {
		return deny(ReasonPolicyContracted, "runtime profile is outside the supported Celln JSON contract")
	}
	return nil
}

func validateClusterTool(tool api.ClusterCellnTool) error {
	s := tool.Spec
	if s.Revision == "" || s.InvocationABI != "celln.json-stdio/v1" || s.Lane != "tool" || s.Platform != "linux/amd64" || !hashPattern.MatchString(s.Executable.Hash) || !hashPattern.MatchString(s.Closure.Hash) || !hashPattern.MatchString(s.ArgumentsSchema.Hash) || !hashPattern.MatchString(s.ResultSchema.Hash) || !publisherPattern.MatchString(s.PublisherKey) || !pathPattern.MatchString(s.EntryPoint) || !validToolLimits(s.Limits) {
		return deny(ReasonToolUnknown, "unsupported cluster tool")
	}
	return nil
}

func validToolLimits(limits api.CellnToolLimits) bool {
	if limits.TimeoutMillis < 1 || limits.MemoryBytes < 1 || limits.ArgumentBytes < 1 || limits.OutputBytes < 1 || limits.Workspace != "none" || (limits.Effects != "none" && limits.Effects != "external-side-effects") || len(limits.Egress) != 0 || len(limits.Inputs) != 0 {
		return false
	}
	if limits.Artifacts != nil && (limits.Artifacts.Operation != "read" && limits.Artifacts.Operation != "write" || limits.Artifacts.MaxOperations < 1 || limits.Artifacts.MaxFiles < 1 || limits.Artifacts.MaxFileBytes < 1 || limits.Artifacts.MaxTotalBytes < 1) {
		return false
	}
	if limits.HTTPS != nil && (len(limits.HTTPS.AllowHosts) == 0 || limits.HTTPS.MaxRequests < 1 || limits.HTTPS.MaxResponseBytes < 1 || limits.HTTPS.TimeoutMillis < 1) {
		return false
	}
	return true
}

func intersectRuntimeLimits(base, requested api.AgentRuntimeCellnLimits) (api.AgentRuntimeCellnLimits, error) {
	if requested.TimeoutMillis < 1 || requested.MemoryBytes < 1 || requested.TaskBytes < 1 || requested.OutputBytes < 1 || requested.Workspace != "none" || requested.TimeoutMillis > base.TimeoutMillis || requested.MemoryBytes > base.MemoryBytes || requested.TaskBytes > base.TaskBytes || requested.OutputBytes > base.OutputBytes || base.Workspace != "none" {
		return base, deny(ReasonLimitRange, "runtime attenuation raises or invalidates a profile ceiling")
	}
	return requested, nil
}

func minimumRuntimeLimits(left, right api.AgentRuntimeCellnLimits) api.AgentRuntimeCellnLimits {
	return api.AgentRuntimeCellnLimits{
		TimeoutMillis: min(left.TimeoutMillis, right.TimeoutMillis), MemoryBytes: min(left.MemoryBytes, right.MemoryBytes),
		TaskBytes: min(left.TaskBytes, right.TaskBytes), OutputBytes: min(left.OutputBytes, right.OutputBytes), Workspace: "none",
	}
}

func intersectToolLimits(base, requested api.CellnToolLimits) api.CellnToolLimits {
	result := *base.DeepCopy()
	result.TimeoutMillis = min(base.TimeoutMillis, requested.TimeoutMillis)
	result.MemoryBytes = min(base.MemoryBytes, requested.MemoryBytes)
	result.ArgumentBytes = min(base.ArgumentBytes, requested.ArgumentBytes)
	result.OutputBytes = min(base.OutputBytes, requested.OutputBytes)
	if requested.Workspace != base.Workspace {
		result.Workspace = "none"
	}
	if requested.Effects == "none" {
		result.Effects = "none"
	}
	if requested.Artifacts == nil {
		result.Artifacts = nil
	} else if result.Artifacts != nil && result.Artifacts.Operation == requested.Artifacts.Operation {
		result.Artifacts.MaxOperations = min(result.Artifacts.MaxOperations, requested.Artifacts.MaxOperations)
		result.Artifacts.MaxFiles = min(result.Artifacts.MaxFiles, requested.Artifacts.MaxFiles)
		result.Artifacts.MaxFileBytes = min(result.Artifacts.MaxFileBytes, requested.Artifacts.MaxFileBytes)
		result.Artifacts.MaxTotalBytes = min(result.Artifacts.MaxTotalBytes, requested.Artifacts.MaxTotalBytes)
	} else {
		result.Artifacts = nil
	}
	if requested.HTTPS == nil {
		result.HTTPS = nil
	} else if result.HTTPS != nil {
		allowed := make([]string, 0)
		for _, host := range result.HTTPS.AllowHosts {
			if slices.Contains(requested.HTTPS.AllowHosts, host) {
				allowed = append(allowed, host)
			}
		}
		sort.Strings(allowed)
		result.HTTPS.AllowHosts = allowed
		result.HTTPS.MaxRequests = min(result.HTTPS.MaxRequests, requested.HTTPS.MaxRequests)
		result.HTTPS.MaxResponseBytes = min(result.HTTPS.MaxResponseBytes, requested.HTTPS.MaxResponseBytes)
		result.HTTPS.TimeoutMillis = min(result.HTTPS.TimeoutMillis, requested.HTTPS.TimeoutMillis)
	} else {
		result.HTTPS = nil
	}
	return result
}

func decisionTool(tool api.ClusterCellnTool, limits api.CellnToolLimits) DecisionToolBinding {
	return DecisionToolBinding{Name: tool.Name, Revision: tool.Spec.Revision, Hash: tool.Spec.Executable.Hash, Limits: DecisionToolLimits{TimeoutMillis: limits.TimeoutMillis, MemoryBytes: limits.MemoryBytes, ArgumentBytes: limits.ArgumentBytes, OutputBytes: limits.OutputBytes, Workspace: limits.Workspace, Effects: limits.Effects, Artifacts: limits.Artifacts.DeepCopy(), HTTPS: limits.HTTPS.DeepCopy()}}
}

func decisionLifecycle(lifecycle string, turn bool) string {
	if lifecycle != "enduring" {
		return "one-shot"
	}
	if turn {
		return "enduring-turn"
	}
	return "enduring-initial"
}
