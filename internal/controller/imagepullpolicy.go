package controller

import (
	corev1 "k8s.io/api/core/v1"
)

// FallbackImagePullPolicy is the policy used when a CR does not specify one.
// Matches the behavior the controller had before per-CR pull policies were
// configurable.
const FallbackImagePullPolicy = corev1.PullIfNotPresent

// ResolveImagePullPolicy returns the CR-level pull policy when it's a valid
// value, otherwise FallbackImagePullPolicy. Used everywhere the controller
// builds pod specs from user-supplied images (skill sidecars, lifecycle
// hooks, sandbox image, MCP server, inference server, etc.).
func ResolveImagePullPolicy(p corev1.PullPolicy) corev1.PullPolicy {
	switch p {
	case corev1.PullAlways, corev1.PullIfNotPresent, corev1.PullNever:
		return p
	}
	return FallbackImagePullPolicy
}
