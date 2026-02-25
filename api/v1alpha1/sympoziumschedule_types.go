package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SympoziumScheduleSpec defines a recurring task for a SympoziumInstance.
type SympoziumScheduleSpec struct {
	// InstanceRef is the name of the SympoziumInstance this schedule belongs to.
	InstanceRef string `json:"instanceRef"`

	// Schedule is a cron expression (e.g. "0 * * * *").
	Schedule string `json:"schedule"`

	// Task is the task description sent to the agent on each trigger.
	Task string `json:"task"`

	// Type categorises the schedule: heartbeat, scheduled, or sweep.
	// +kubebuilder:validation:Enum=heartbeat;scheduled;sweep
	// +kubebuilder:default="scheduled"
	Type string `json:"type,omitempty"`

	// Suspend pauses scheduling when true.
	// +optional
	Suspend bool `json:"suspend,omitempty"`

	// ConcurrencyPolicy controls what happens when a trigger fires while
	// the previous run is still active.
	// +kubebuilder:validation:Enum=Forbid;Allow;Replace
	// +kubebuilder:default="Forbid"
	ConcurrencyPolicy string `json:"concurrencyPolicy,omitempty"`

	// IncludeMemory injects the instance's MEMORY.md as context for each run.
	// +kubebuilder:default=true
	IncludeMemory bool `json:"includeMemory,omitempty"`
}

// SympoziumScheduleStatus defines the observed state of a SympoziumSchedule.
type SympoziumScheduleStatus struct {
	// Phase is the current phase (Active, Suspended, Error).
	// +optional
	Phase string `json:"phase,omitempty"`

	// LastRunTime is when the last AgentRun was triggered.
	// +optional
	LastRunTime *metav1.Time `json:"lastRunTime,omitempty"`

	// NextRunTime is the computed next trigger time.
	// +optional
	NextRunTime *metav1.Time `json:"nextRunTime,omitempty"`

	// LastRunName is the name of the most recently created AgentRun.
	// +optional
	LastRunName string `json:"lastRunName,omitempty"`

	// TotalRuns is the total number of runs triggered by this schedule.
	// +optional
	TotalRuns int64 `json:"totalRuns,omitempty"`

	// Conditions represent the latest available observations.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Instance",type="string",JSONPath=".spec.instanceRef"
// +kubebuilder:printcolumn:name="Schedule",type="string",JSONPath=".spec.schedule"
// +kubebuilder:printcolumn:name="Type",type="string",JSONPath=".spec.type"
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Last Run",type="date",JSONPath=".status.lastRunTime"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// SympoziumSchedule is the Schema for the sympoziumschedules API.
// It defines recurring tasks (heartbeats, scheduled jobs, sweeps) for a SympoziumInstance.
type SympoziumSchedule struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SympoziumScheduleSpec   `json:"spec,omitempty"`
	Status SympoziumScheduleStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SympoziumScheduleList contains a list of SympoziumSchedule.
type SympoziumScheduleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SympoziumSchedule `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SympoziumSchedule{}, &SympoziumScheduleList{})
}
