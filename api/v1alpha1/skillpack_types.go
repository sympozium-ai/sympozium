package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SkillPackSpec defines the desired state of SkillPack.
// Skills are Markdown-based instruction bundles mounted into agent pods.
type SkillPackSpec struct {
	// Skills is the list of skills in this pack.
	Skills []Skill `json:"skills"`

	// Category classifies this skill pack (e.g. "kubernetes", "security", "devops").
	// +optional
	Category string `json:"category,omitempty"`

	// Source records where this skill pack was imported from.
	// +optional
	Source string `json:"source,omitempty"`

	// Version is the skill pack version.
	// +optional
	Version string `json:"version,omitempty"`

	// RuntimeRequirements defines container image requirements for this skill pack.
	// +optional
	RuntimeRequirements *RuntimeRequirements `json:"runtimeRequirements,omitempty"`

	// Sidecar defines a container that is injected into the agent pod when this
	// SkillPack is active. The sidecar provides tools (kubectl, helm, etc.) and
	// the controller creates scoped RBAC automatically.
	// +optional
	Sidecar *SkillSidecar `json:"sidecar,omitempty"`
}

// Skill defines a single skill entry.
type Skill struct {
	// Name is the skill identifier.
	Name string `json:"name"`

	// Description describes what this skill does.
	// +optional
	Description string `json:"description,omitempty"`

	// Requires lists runtime requirements (binaries, etc.) for this skill.
	// +optional
	Requires *SkillRequirements `json:"requires,omitempty"`

	// Content is the Markdown content of the skill.
	Content string `json:"content"`
}

// SkillRequirements defines what a skill needs at runtime.
type SkillRequirements struct {
	// Bins lists required binaries.
	Bins []string `json:"bins,omitempty"`

	// Tools lists tools this skill expects the agent to have.
	Tools []string `json:"tools,omitempty"`
}

// RuntimeRequirements defines container-level requirements.
type RuntimeRequirements struct {
	// Image is the container image that satisfies these requirements.
	// +optional
	Image string `json:"image,omitempty"`

	// Sandbox indicates whether this skill requires a sandbox.
	// +optional
	Sandbox bool `json:"sandbox,omitempty"`

	// MinMemory is the minimum memory requirement (e.g. "256Mi").
	// +optional
	MinMemory string `json:"minMemory,omitempty"`

	// MinCPU is the minimum CPU requirement (e.g. "100m").
	// +optional
	MinCPU string `json:"minCPU,omitempty"`
}

// SkillSidecar defines a sidecar container that is injected into the agent pod
// when this SkillPack is active. The sidecar runs alongside the agent and
// provides tools (e.g. kubectl, helm) that the agent executes via the shared
// workspace/IPC volumes.
type SkillSidecar struct {
	// Image is the container image for this skill sidecar.
	Image string `json:"image"`

	// ImagePullPolicy overrides the container image pull policy for this
	// sidecar. Valid values are "Always", "IfNotPresent", or "Never".
	// Defaults to "IfNotPresent" when unset.
	// +kubebuilder:validation:Enum=Always;IfNotPresent;Never
	// +optional
	ImagePullPolicy corev1.PullPolicy `json:"imagePullPolicy,omitempty"`

	// Command overrides the container entrypoint.
	// Defaults to ["sleep", "infinity"] to keep the sidecar alive.
	// +optional
	Command []string `json:"command,omitempty"`

	// Env is a list of environment variables for the sidecar.
	// +optional
	Env []EnvVar `json:"env,omitempty"`

	// MountWorkspace controls whether /workspace is mounted into the sidecar.
	// +kubebuilder:default=true
	// +optional
	MountWorkspace bool `json:"mountWorkspace,omitempty"`

	// Resources for the sidecar container.
	// +optional
	Resources *SidecarResources `json:"resources,omitempty"`

	// RBAC defines Kubernetes RBAC rules that this sidecar needs.
	// The controller creates a Role + RoleBinding scoped to the run namespace.
	// +optional
	RBAC []RBACRule `json:"rbac,omitempty"`

	// ClusterRBAC defines cluster-scoped RBAC rules (ClusterRole + ClusterRoleBinding).
	// Use for read-only cluster-wide access (e.g. listing nodes, namespaces).
	// +optional
	ClusterRBAC []RBACRule `json:"clusterRBAC,omitempty"`

	// SecretRef is the name of a Kubernetes Secret whose keys are mounted as files
	// inside the sidecar at SecretMountPath. Use this to supply credentials such as
	// API tokens that the sidecar needs at runtime (e.g. GH_TOKEN for github-gitops).
	// The Secret must exist in sympozium-system and will be mirrored into the
	// AgentRun namespace automatically.
	// +optional
	SecretRef string `json:"secretRef,omitempty"`

	// SecretMountPath is the directory inside the sidecar where the Secret keys are
	// projected as individual files. Defaults to /secrets/<SecretRef>.
	// +optional
	SecretMountPath string `json:"secretMountPath,omitempty"`

	// HostAccess enables explicit host-level access for this sidecar.
	// This is disabled by default and should only be used when a skill
	// must inspect node-local host resources (for example, hardware probes).
	// +optional
	HostAccess *HostAccessSpec `json:"hostAccess,omitempty"`

	// RequiresServer indicates this sidecar needs a long-running Deployment
	// instead of an ephemeral Job. The AgentRun controller detects this and
	// creates a Deployment + Service.
	// +optional
	RequiresServer bool `json:"requiresServer,omitempty"`

	// Ports exposed by this sidecar, used to create a Service when RequiresServer is true.
	// +optional
	Ports []SidecarPort `json:"ports,omitempty"`

	// Volumes are additional pod-level volumes added when this sidecar is
	// injected. Use this to expose CSI driver volumes (e.g. Vault Secrets
	// Store CSI), PVCs, or projected Secret/ConfigMap volumes to the
	// sidecar. Volumes are appended at the pod level, so multiple sidecars
	// on the same pod must use unique volume names. Names must not collide
	// with reserved volumes (workspace, ipc, skills, tmp, memory, mcp-config)
	// or volumes contributed by other sidecars.
	// +optional
	Volumes []corev1.Volume `json:"volumes,omitempty"`

	// VolumeMounts are additional volume mounts attached to this sidecar
	// container. Names must reference entries in Volumes (or another volume
	// already present on the pod).
	// +optional
	VolumeMounts []corev1.VolumeMount `json:"volumeMounts,omitempty"`
}

// SidecarPort defines a port exposed by a skill sidecar.
type SidecarPort struct {
	// Name is the port name (e.g. "http").
	Name string `json:"name"`

	// ContainerPort is the port number on the container.
	ContainerPort int32 `json:"containerPort"`

	// Protocol defaults to TCP.
	// +optional
	Protocol string `json:"protocol,omitempty"`
}

// HostAccessSpec defines opt-in host-level pod settings and hostPath mounts
// for a skill sidecar.
type HostAccessSpec struct {
	// Enabled controls whether host access settings are applied.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// HostNetwork enables host network namespace for the pod.
	// Applied at pod level when any enabled sidecar requests it.
	// +optional
	HostNetwork bool `json:"hostNetwork,omitempty"`

	// HostPID enables host PID namespace for the pod.
	// Applied at pod level when any enabled sidecar requests it.
	// +optional
	HostPID bool `json:"hostPID,omitempty"`

	// Privileged runs the sidecar in privileged mode.
	// +optional
	Privileged bool `json:"privileged,omitempty"`

	// RunAsRoot runs the sidecar as UID 0.
	// +optional
	RunAsRoot bool `json:"runAsRoot,omitempty"`

	// Mounts are hostPath mounts injected into this sidecar.
	// +optional
	Mounts []HostPathMount `json:"mounts,omitempty"`
}

// HostPathMount defines one hostPath volume mount for a sidecar.
type HostPathMount struct {
	// HostPath is the absolute path on the node host.
	HostPath string `json:"hostPath"`

	// MountPath is the path inside the sidecar container.
	MountPath string `json:"mountPath"`

	// ReadOnly controls whether the mount is read-only.
	// +kubebuilder:default=true
	// +optional
	ReadOnly *bool `json:"readOnly,omitempty"`
}

// EnvVar is a simplified environment variable (name + value).
type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// SidecarResources defines resource requests and limits for a skill sidecar.
type SidecarResources struct {
	// CPU request (e.g. "100m").
	// +optional
	CPU string `json:"cpu,omitempty"`

	// Memory request (e.g. "128Mi").
	// +optional
	Memory string `json:"memory,omitempty"`
}

// RBACRule defines a single Kubernetes RBAC policy rule.
type RBACRule struct {
	// APIGroups is the list of API groups (e.g. "", "apps", "batch").
	APIGroups []string `json:"apiGroups"`

	// Resources is the list of resources (e.g. "pods", "deployments").
	Resources []string `json:"resources"`

	// Verbs is the list of allowed verbs (e.g. "get", "list", "watch").
	Verbs []string `json:"verbs"`
}

// SkillPackStatus defines the observed state of SkillPack.
type SkillPackStatus struct {
	// Phase is the current phase.
	// +optional
	Phase string `json:"phase,omitempty"`

	// ConfigMapName is the name of the generated ConfigMap.
	// +optional
	ConfigMapName string `json:"configMapName,omitempty"`

	// SkillCount is the number of skills in this pack.
	// +optional
	SkillCount int `json:"skillCount,omitempty"`

	// Conditions represent the latest available observations.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Skills",type="integer",JSONPath=".status.skillCount"
// +kubebuilder:printcolumn:name="ConfigMap",type="string",JSONPath=".status.configMapName"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// SkillPack is the Schema for the skillpacks API.
// It bundles portable skills as a CRD that produces ConfigMaps mounted into agent pods.
type SkillPack struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SkillPackSpec   `json:"spec,omitempty"`
	Status SkillPackStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SkillPackList contains a list of SkillPack.
type SkillPackList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SkillPack `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SkillPack{}, &SkillPackList{})
}
