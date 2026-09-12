package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// CellnRuntimeProfileRef identifies one immutable operator-published runtime
// revision. Name alone is never sufficient authority.
type CellnRuntimeProfileRef struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$"
	Name string `json:"name"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	Revision string `json:"revision"`
}

// ClusterCellnToolRef is an unambiguous reference to a cluster-scoped tool
// catalogue revision. It is intentionally distinct from CellnCatalogueToolRef,
// which continues to mean the legacy namespaced CellnTool API.
type ClusterCellnToolRef struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$"
	Name string `json:"name"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	Revision string `json:"revision"`
}

// CellnRuntimeProfileSpec is executable platform catalogue authority. A
// revision is immutable; changing any executable identity or constraint requires
// publishing a new object/revision.
// +kubebuilder:validation:XValidation:rule="self.lifecycles.all(x, self.lifecycles.filter(y, y == x).size() == 1)",message="runtime lifecycles must be unique"
// +kubebuilder:validation:XValidation:rule="self.contractVersion == 'celln.json-tools/v1' ? has(self.json) : !has(self.json)",message="JSON loop ceilings must match the selected adapter contract"
type CellnRuntimeProfileSpec struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	Revision string `json:"revision"`
	// +kubebuilder:validation:Enum=celln.reference-functions/v1;celln.json-tools/v1
	ContractVersion string            `json:"contractVersion"`
	Executable      CellnImmutableRef `json:"executable"`
	Closure         CellnImmutableRef `json:"closure"`
	Mote            CellnImmutableRef `json:"mote"`
	// +kubebuilder:validation:Pattern="^[0-9a-f]{64}$"
	// +kubebuilder:validation:MaxLength=64
	PublisherKey string `json:"publisherKey"`
	// +kubebuilder:validation:Pattern="^/([A-Za-z0-9_-][A-Za-z0-9_.+-]*/)*[A-Za-z0-9_-][A-Za-z0-9_.+-]*$"
	// +kubebuilder:validation:MaxLength=256
	EntryPoint string `json:"entryPoint"`
	// +kubebuilder:validation:Enum=linux/amd64
	Platform string `json:"platform"`
	// +kubebuilder:validation:Enum=agent
	Lane string `json:"lane"`
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=2
	// +listType=atomic
	// +kubebuilder:validation:items:Enum=disposable-one-shot;enduring
	Lifecycles []string                `json:"lifecycles"`
	Limits     AgentRuntimeCellnLimits `json:"limits"`
	// +optional
	JSON *CellnHarnessJSONLimits `json:"json,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Revision",type=string,JSONPath=`.spec.revision`
// +kubebuilder:printcolumn:name="Contract",type=string,JSONPath=`.spec.contractVersion`
// +kubebuilder:validation:XValidation:rule="self.spec == oldSelf.spec",message="runtime profile revisions are immutable; publish a new revision"
type CellnRuntimeProfile struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              CellnRuntimeProfileSpec `json:"spec"`
	// +optional
	Status CellnPlatformCatalogueStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type CellnRuntimeProfileList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CellnRuntimeProfile `json:"items"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Revision",type=string,JSONPath=`.spec.revision`
// +kubebuilder:printcolumn:name="Lane",type=string,JSONPath=`.spec.lane`
// +kubebuilder:validation:XValidation:rule="self.spec == oldSelf.spec",message="cluster tool revisions are immutable; publish a new revision"
type ClusterCellnTool struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              CellnToolSpec `json:"spec"`
	// +optional
	Status CellnPlatformCatalogueStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type ClusterCellnToolList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterCellnTool `json:"items"`
}

type CellnPlatformCatalogueStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Conditions may describe publication/distribution observations but never
	// constitute execution admission for a tenant run.
	// +optional
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// CellnExecutionPolicyRuntime permits an exact shared runtime revision and may
// narrow its numeric ceilings. The resolver enforces that the override cannot
// raise the referenced profile's ceilings.
type CellnExecutionPolicyRuntime struct {
	Ref CellnRuntimeProfileRef `json:"ref"`
	// +optional
	Limits *AgentRuntimeCellnLimits `json:"limits,omitempty"`
}

// CellnExecutionPolicyTool permits an exact shared tool revision and may narrow
// that tool's declared constraints.
type CellnExecutionPolicyTool struct {
	Ref ClusterCellnToolRef `json:"ref"`
	// +optional
	Limits *CellnToolLimits `json:"limits,omitempty"`
}

// CellnExecutionPolicyRoute is explicit model endpoint authority. A tenant
// ModelConnection may choose only a route contained by one of these entries.
// +kubebuilder:validation:XValidation:rule="self.endpointOrigins.all(x, x.startsWith('https://') || (self.auth == 'none' && (x.startsWith('http://127.0.0.1') || x.startsWith('http://localhost') || x.startsWith('http://[::1]'))))",message="credential-bearing routes require https; http is restricted to explicit no-auth loopback origins"
type CellnExecutionPolicyRoute struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	Provider string `json:"provider"`
	// +kubebuilder:validation:Enum=openai-chat;anthropic-messages
	Protocol string `json:"protocol"`
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=32
	// +listType=set
	Models []string `json:"models"`
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	// +listType=set
	EndpointOrigins []string `json:"endpointOrigins"`
	// +kubebuilder:validation:Enum=secret;none
	Auth string `json:"auth"`
}

type CellnExecutionPolicyCeilings struct {
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=1024
	MaxTurns int64 `json:"maxTurns"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=6144
	MaxModelRequests int64 `json:"maxModelRequests"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=3145728
	MaxOutputTokens int64 `json:"maxOutputTokens"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=86400
	MaxParentLeaseSeconds int64 `json:"maxParentLeaseSeconds"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=3600
	MaxTurnSeconds int64 `json:"maxTurnSeconds"`
}

// CellnExecutionPolicySpec maps protected namespace labels to an intersection of
// exact catalogue revisions, model routes, lifecycle modes, and hard ceilings.
// Empty tools means no tool authority; it does not make an otherwise valid
// tool-free direct request invalid.
// +kubebuilder:validation:XValidation:rule="self.runtimeProfiles.all(x, self.runtimeProfiles.filter(y, y.ref.name == x.ref.name).size() == 1)",message="runtime profile names must be unique within a policy"
// +kubebuilder:validation:XValidation:rule="self.tools.all(x, self.tools.filter(y, y.ref.name == x.ref.name).size() == 1)",message="cluster tool names must be unique within a policy"
type CellnExecutionPolicySpec struct {
	NamespaceSelector metav1.LabelSelector `json:"namespaceSelector"`
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	// +listType=atomic
	RuntimeProfiles []CellnExecutionPolicyRuntime `json:"runtimeProfiles"`
	// +kubebuilder:validation:MaxItems=64
	// +listType=atomic
	Tools []CellnExecutionPolicyTool `json:"tools"`
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=3
	// +listType=set
	// +kubebuilder:validation:items:Enum=direct-one-shot;harness-one-shot;enduring
	Lifecycles []string `json:"lifecycles"`
	// +kubebuilder:validation:MaxItems=32
	// +listType=atomic
	Routes   []CellnExecutionPolicyRoute  `json:"routes"`
	Ceilings CellnExecutionPolicyCeilings `json:"ceilings"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
type CellnExecutionPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              CellnExecutionPolicySpec `json:"spec"`
	// +optional
	Status CellnExecutionPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type CellnExecutionPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CellnExecutionPolicy `json:"items"`
}

type CellnExecutionPolicyStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

func init() {
	SchemeBuilder.Register(
		&CellnRuntimeProfile{}, &CellnRuntimeProfileList{},
		&ClusterCellnTool{}, &ClusterCellnToolList{},
		&CellnExecutionPolicy{}, &CellnExecutionPolicyList{},
	)
}
