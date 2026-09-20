package cellnauthority

import api "github.com/sympozium-ai/sympozium/api/v1alpha1"

// PlatformExecutionMaterial is the public material needed to construct a native
// request without looking up a potentially newer catalogue revision. It is
// captured in the same double-read epoch as the decision. No credentials or
// transport tokens belong in this structure.
type PlatformExecutionMaterial struct {
	Source              PreparedRunIdentity         `json:"source"`
	WrapperSpec         api.AgentRuntimeSpec        `json:"wrapperSpec"`
	ProfileName         string                      `json:"profileName"`
	ProfileUID          string                      `json:"profileUid"`
	ProfileSpec         api.CellnRuntimeProfileSpec `json:"profileSpec"`
	Tools               []PreparedTool              `json:"tools"`
	RuntimeLimits       api.AgentRuntimeCellnLimits `json:"runtimeLimits"`
	Payload             string                      `json:"payload"`
	SystemPrompt        string                      `json:"systemPrompt"`
	ModelConnectionName string                      `json:"modelConnectionName"`
	CredentialSourceRef *CredentialSourceRef        `json:"credentialSourceRef"`
	// RequestOutputTokens is the output tokens one model request of this
	// operation asks for (the receiver's resolution.execution.requestOutputTokens):
	// a gateway-mediated connection's spec.maxOutputTokens, 256-4096. It is
	// omitted at the default 512, so such an operation is byte-identical to one
	// prepared before the field existed, and it is never set on a host-profile
	// or model-free route. It is not part of any signed digest: the model
	// gateway enforces the connection's bound per request and the signed turn
	// and run caps, sized for it by resolveBudget, bound the total.
	RequestOutputTokens int64 `json:"requestOutputTokens,omitempty"`
	// TurnUID is captured from the persisted AgentRunTurn object. It is data
	// correlated with this prepared operation, never authority supplied by a
	// token or an HTTP caller.
	TurnUID string `json:"turnUid,omitempty"`
}

type PreparedRunIdentity struct {
	ClusterID     string `json:"clusterId"`
	Namespace     string `json:"namespace"`
	NamespaceUID  string `json:"namespaceUid"`
	RunName       string `json:"runName"`
	RunUID        string `json:"runUid"`
	RunSpecSHA256 string `json:"runSpecSha256"`
}

type PreparedTool struct {
	Name string            `json:"name"`
	UID  string            `json:"uid"`
	Spec api.CellnToolSpec `json:"spec"`
}

func executionMaterial(s platformSnapshot, request PlatformResolveRequest, decision PlatformDecision) (*PlatformExecutionMaterial, error) {
	runDigest, err := digestJSON(s.Run.Spec)
	if err != nil {
		return nil, err
	}
	limits := *s.Profile.Spec.Limits.DeepCopy()
	if s.Runtime.Spec.CellnLimits != nil {
		limits, err = intersectRuntimeLimits(limits, *s.Runtime.Spec.CellnLimits)
		if err != nil {
			return nil, err
		}
	}
	for _, policy := range s.Policies {
		for _, allowed := range policy.Spec.RuntimeProfiles {
			if allowed.Ref.Name == s.Profile.Name && allowed.Ref.Revision == s.Profile.Spec.Revision && allowed.Limits != nil {
				limits = minimumRuntimeLimits(limits, *allowed.Limits)
			}
		}
	}
	out := &PlatformExecutionMaterial{
		Source:      PreparedRunIdentity{ClusterID: request.ClusterID, Namespace: s.Namespace.Name, NamespaceUID: string(s.Namespace.UID), RunName: s.Run.Name, RunUID: string(s.Run.UID), RunSpecSHA256: runDigest},
		WrapperSpec: *s.Runtime.Spec.DeepCopy(), ProfileName: s.Profile.Name, ProfileUID: string(s.Profile.UID), ProfileSpec: *s.Profile.Spec.DeepCopy(),
		Tools: make([]PreparedTool, 0, len(s.Tools)), RuntimeLimits: limits, Payload: s.Run.Spec.Task.GetPrompt(),
	}
	out.SystemPrompt = s.Run.Spec.SystemPrompt
	if s.Connection != nil {
		out.ModelConnectionName = s.Connection.Name
		out.RequestOutputTokens = mediatedRequestOutputTokens(s.Connection)
	}
	if s.Turn != nil {
		out.Payload = s.Turn.Spec.Message
		out.TurnUID = string(s.Turn.UID)
	}
	if decision.Route.CredentialSourceRef != nil {
		ref := *decision.Route.CredentialSourceRef
		out.CredentialSourceRef = &ref
	}
	for _, tool := range s.Tools {
		out.Tools = append(out.Tools, PreparedTool{Name: tool.Name, UID: string(tool.UID), Spec: *tool.Spec.DeepCopy()})
	}
	return out, nil
}
