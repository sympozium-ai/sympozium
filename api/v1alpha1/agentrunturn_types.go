package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// AgentRunTurnSpec carries data for an existing enduring run, never executable
// or model authority. A run name cannot be reused to retarget an old turn.
// +kubebuilder:validation:XValidation:rule="self.runName == oldSelf.runName && self.runUID == oldSelf.runUID && self.message == oldSelf.message",message="turn input and parent identity are immutable"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.cancelRequested) || !oldSelf.cancelRequested || (has(self.cancelRequested) && self.cancelRequested)",message="turn cancellation cannot be withdrawn"
type AgentRunTurnSpec struct {
	// CancelRequested signals this turn's sub-cell only, never the parent.
	// Cancellation requires a recorded dispatch attempt and does not imply teardown.
	// +optional
	CancelRequested bool `json:"cancelRequested,omitempty"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	RunName string `json:"runName"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	RunUID string `json:"runUID"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=2048
	Message string `json:"message"`
}

// AgentRunTurnStatus has the same durable attempt/result rules as the initial
// turn. The enclosing AgentRun retains lifetime and tool/model authority.
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.execution) || has(self.execution)",message="turn execution record cannot be removed"
type AgentRunTurnStatus struct {
	// CancelAttempted is persisted before sending cancellation. An uncertain send
	// is reconciled from original turn evidence, never automatically repeated.
	// +optional
	CancelAttempted bool `json:"cancelAttempted,omitempty"`
	// Conditions describe reconciliation observations, not execution authority.
	// A failed observation never permits clearing an attempt or replaying a turn.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// +optional
	Execution *CellnParentTurnStatus `json:"execution,omitempty"`
	// ParentIncarnation is fixed before dispatch, not selected by the message.
	// +kubebuilder:validation:MaxLength=71
	// +kubebuilder:validation:Pattern=`^blake3:[0-9a-f]{64}$`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="turn parent incarnation is immutable"
	// +optional
	ParentIncarnation string `json:"parentIncarnation,omitempty"`
}

// AgentRunTurn is a durable message/result record, not another running agent.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=arturn
// +kubebuilder:validation:XValidation:rule="!has(self.spec.cancelRequested) || !self.spec.cancelRequested || (has(self.status) && has(self.status.execution) && has(self.status.execution.attempted) && self.status.execution.attempted)",message="cancellation requires a recorded turn dispatch attempt"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.status) || !has(oldSelf.status.cancelAttempted) || !oldSelf.status.cancelAttempted || (has(self.status) && has(self.status.cancelAttempted) && self.status.cancelAttempted)",message="cancellation attempt cannot be cleared"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.status) || !has(oldSelf.status.execution) || (has(self.status) && has(self.status.execution))",message="saved turn execution cannot be removed"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.status) || !has(oldSelf.status.parentIncarnation) || (has(self.status) && has(self.status.parentIncarnation))",message="saved turn parent cannot be removed"
type AgentRunTurn struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              AgentRunTurnSpec `json:"spec"`
	// +optional
	Status AgentRunTurnStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type AgentRunTurnList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AgentRunTurn `json:"items"`
}

func init() { SchemeBuilder.Register(&AgentRunTurn{}, &AgentRunTurnList{}) }
