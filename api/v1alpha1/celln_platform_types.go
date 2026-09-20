package v1alpha1

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

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

	// Native is the reviewed parent/worker provisioning material an owner
	// issues an enduring parent from. The fleet installer publishes it from
	// the starter configuration; every platform decision binds it through the
	// profile spec digest. It carries no credential.
	// +optional
	Native *CellnNativeProvisioning `json:"native,omitempty"`
}

// CellnNativeProvisioning mirrors the owner-side provision plan fields whose
// values are fixed by the admitted package rather than by the run.
type CellnNativeProvisioning struct {
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=300000
	AdmissionWindowMs int64 `json:"admissionWindowMs"`
	// Parent and Worker are the complete owner-side ExecutionRequest objects;
	// Template is the native JSON-harness configuration bound into the model
	// profile. They are opaque here and verified by the owner at issuance.
	// +kubebuilder:pruning:PreserveUnknownFields
	Parent apiextensionsv1.JSON `json:"parent"`
	// +kubebuilder:pruning:PreserveUnknownFields
	Worker apiextensionsv1.JSON `json:"worker"`
	// +kubebuilder:pruning:PreserveUnknownFields
	Template apiextensionsv1.JSON `json:"template"`
	// +kubebuilder:validation:Pattern=`^blake3:[0-9a-f]{64}$`
	ModelProfile string `json:"modelProfile"`
	// CredentialProfile names the owner-installed model credential; a tenant
	// ModelConnection using this runtime must reference exactly this profile.
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9_-]{1,64}$`
	CredentialProfile string `json:"credentialProfile"`
	// +kubebuilder:validation:Minimum=1
	ReservedMemoryBytes int64 `json:"reservedMemoryBytes"`
	// +kubebuilder:validation:Minimum=1
	TurnModelRequests int64 `json:"turnModelRequests"`
	// +kubebuilder:validation:Minimum=1
	TurnOutputTokens int64 `json:"turnOutputTokens"`
	// SystemPrompt is the persona bound into the model profile; a run using
	// this runtime must carry it exactly.
	// +kubebuilder:validation:MaxLength=8192
	SystemPrompt string `json:"systemPrompt"`
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
// +kubebuilder:validation:XValidation:rule="self.endpointOrigins.all(x, x.startsWith('https://') || (x.startsWith('http://') && self.auth in ['none', 'host-profile'] && has(self.allowInsecure) && self.allowInsecure))",message="Secret routes require https; http requires auth none or host-profile and explicit allowInsecure"
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
	// +kubebuilder:validation:Enum=secret;none;host-profile
	Auth string `json:"auth"`
	// AllowInsecure approves plain-HTTP or private endpoint origins for a
	// keyless or host-profile route (for example a LAN llama-server).
	// A cluster Secret route can never use plain HTTP.
	// +optional
	AllowInsecure bool `json:"allowInsecure,omitempty"`
}

type CellnExecutionPolicyCeilings struct {
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=1024
	MaxTurns int64 `json:"maxTurns"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=6144
	MaxModelRequests int64 `json:"maxModelRequests"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=25165824
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
