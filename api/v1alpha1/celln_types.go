package v1alpha1

// CellnCatalogueSelection keeps tenant intent separate from explicit artifacts
// and operator grant/route configuration. Ordered tools are explicitly lent;
// an empty list lends none, never all installed tools.
//
// toolRefs always means the legacy namespaced CellnTool API. clusterToolRefs
// always means the shared ClusterCellnTool catalogue. The two scopes are never
// inferred from object existence and a friendly name may not appear in both
// lists in one selection.
// +kubebuilder:validation:XValidation:rule="self.toolRefs.all(t, self.toolRefs.filter(x, x.name == t.name).size() == 1)",message="catalogue tool names must be unique"
// +kubebuilder:validation:XValidation:rule="!has(self.clusterToolRefs) || self.clusterToolRefs.all(t, self.clusterToolRefs.filter(x, x.name == t.name).size() == 1)",message="cluster catalogue tool names must be unique"
// +kubebuilder:validation:XValidation:rule="!has(self.clusterToolRefs) || self.clusterToolRefs.all(t, self.toolRefs.filter(x, x.name == t.name).size() == 0)",message="a tool name cannot resolve through both namespaced and cluster catalogues"
type CellnCatalogueSelection struct {
	// RuntimeRef preserves the legacy namespaced AgentRuntime reference. It is
	// never reinterpreted as a CellnRuntimeProfile name.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$"
	// +optional
	RuntimeRef string `json:"runtimeRef,omitempty"`
	// +kubebuilder:validation:MaxItems=16
	// +listType=atomic
	ToolRefs []CellnCatalogueToolRef `json:"toolRefs"`
	// ClusterToolRefs selects exact shared catalogue revisions. Empty means no
	// shared tools, not every installed tool.
	// +optional
	// +kubebuilder:validation:MaxItems=16
	// +listType=atomic
	ClusterToolRefs []ClusterCellnToolRef `json:"clusterToolRefs,omitempty"`
}

type CellnCatalogueToolRef struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$"
	Name string `json:"name"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	Revision string `json:"revision"`
}

// CellnExecutionSpec names immutable sources for explicit native Celln requests.
// Invocation aliases are lookups into Tools, never host filesystem paths.
type CellnExecutionSpec struct {
	// Harness opts into an explicitly versioned sealed native runtime contract.
	// This is not the container-replacing task.mode=harness adapter.
	// +optional
	Harness *CellnHarnessSpec `json:"harness,omitempty"`
	Mote    CellnImmutableRef `json:"mote"`
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	Tools []CellnToolRef `json:"tools"`
	// +kubebuilder:validation:MaxItems=16
	// +optional
	Inputs       []CellnInput      `json:"inputs,omitempty"`
	Invocation   CellnInvocation   `json:"invocation"`
	Capabilities CellnCapabilities `json:"capabilities"`
	// Lane never promotes an agent-authored artifact to tool authority.
	// +kubebuilder:validation:Enum=agent;tool
	Lane string `json:"lane"`
}

// CellnHarnessSpec names operator-approved model authority and lent artifacts.
// Task and model are taken from AgentRun.spec and frozen at first dispatch.
// JSON persona comes from AgentRun.spec.systemPrompt. No credential path or
// secret value crosses this API.
// +kubebuilder:validation:XValidation:rule="self.contractVersion == 'celln.json-tools/v1' ? has(self.json) && self.borrowedTools.all(t, has(t.jsonStdio)) : !has(self.json) && size(self.borrowedTools) == 2 && self.borrowedTools.all(t, !has(t.jsonStdio))",message="JSON and reference Harness fields must match the selected contract"
type CellnHarnessSpec struct {
	// +kubebuilder:validation:Enum=celln.reference-functions/v1;celln.json-tools/v1
	ContractVersion string            `json:"contractVersion"`
	ModelGrant      CellnImmutableRef `json:"modelGrant"`
	// +kubebuilder:validation:MinItems=0
	// +kubebuilder:validation:MaxItems=16
	BorrowedTools []CellnBorrowedTool `json:"borrowedTools"`
	// +optional
	JSON *CellnHarnessJSONLimits `json:"json,omitempty"`
}

// CellnHarnessJSONLimits are explicit bounded loop ceilings, not model grants.
type CellnHarnessJSONLimits struct {
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=6
	MaxTurns int64 `json:"maxTurns"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=16
	MaxCalls int64 `json:"maxCalls"`
}

// CellnJSONToolIO names immutable schema data and the separate JSON stdio ABI.
type CellnJSONToolIO struct {
	// +kubebuilder:validation:Enum=celln.json-stdio/v1
	ABI string `json:"abi"`
	// +kubebuilder:validation:Pattern="^blake3:[0-9a-f]{64}$"
	// +kubebuilder:validation:MaxLength=71
	InputSchema string `json:"inputSchema"`
	// +kubebuilder:validation:Pattern="^blake3:[0-9a-f]{64}$"
	// +kubebuilder:validation:MaxLength=71
	OutputSchema string `json:"outputSchema"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65536
	InputBytes int64 `json:"inputBytes"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65536
	OutputBytes int64 `json:"outputBytes"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=30000
	TimeoutMs int64 `json:"timeoutMs"`
}

type CellnBorrowedTool struct {
	// +kubebuilder:validation:Pattern="^[a-zA-Z0-9_-]{1,64}$"
	Name string `json:"name"`
	// +kubebuilder:validation:MaxLength=256
	// +kubebuilder:validation:Pattern="^/"
	Path string `json:"path"`
	// +kubebuilder:validation:Pattern="^blake3:[0-9a-f]{64}$"
	Hash string `json:"hash"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=512
	Description string `json:"description"`
	// +optional
	JSONStdio *CellnJSONToolIO `json:"jsonStdio,omitempty"`
}

type CellnImmutableRef struct {
	// +kubebuilder:validation:Pattern="^blake3:[0-9a-f]{64}$"
	// +kubebuilder:validation:MaxLength=71
	Hash string `json:"hash"`
}

type CellnToolRef struct {
	// +kubebuilder:validation:Pattern="^/"
	Alias string `json:"alias"`
	// +kubebuilder:validation:Pattern="^blake3:[0-9a-f]{64}$"
	Hash string `json:"hash"`
	// +optional
	Closure *CellnImmutableRef `json:"closure,omitempty"`
}

type CellnInput struct {
	// +kubebuilder:validation:Pattern="^[a-z0-9._-]{1,64}$"
	Name string `json:"name"`
	// +kubebuilder:validation:Pattern="^blake3:[0-9a-f]{64}$"
	Hash string `json:"hash"`
	// +kubebuilder:validation:MinLength=1
	MediaType string `json:"mediaType"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65536
	Bytes int64 `json:"bytes"`
}

type CellnInvocation struct {
	Alias string `json:"alias"`
	// +kubebuilder:validation:MaxItems=128
	// +optional
	Args []string `json:"args,omitempty"`
}

type CellnCapabilities struct {
	// +kubebuilder:validation:Enum=none;read-only;read-write
	Workspace string `json:"workspace"`
	// +kubebuilder:validation:MaxItems=32
	// +optional
	Egress []string `json:"egress,omitempty"`
	// Timeout is supplied by AgentRun.spec.timeout, not this object.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=268435456
	MemoryBytes int64 `json:"memoryBytes"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65536
	OutputBytes int64 `json:"outputBytes"`
}
