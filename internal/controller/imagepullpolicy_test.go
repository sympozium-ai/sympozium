package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestResolveImagePullPolicy(t *testing.T) {
	tests := []struct {
		name string
		in   corev1.PullPolicy
		want corev1.PullPolicy
	}{
		{"empty falls back", "", FallbackImagePullPolicy},
		{"always", corev1.PullAlways, corev1.PullAlways},
		{"if-not-present", corev1.PullIfNotPresent, corev1.PullIfNotPresent},
		{"never", corev1.PullNever, corev1.PullNever},
		{"unknown falls back", corev1.PullPolicy("Bogus"), FallbackImagePullPolicy},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResolveImagePullPolicy(tt.in); got != tt.want {
				t.Errorf("ResolveImagePullPolicy(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestFallbackImagePullPolicy(t *testing.T) {
	// Sanity check: the fallback must remain IfNotPresent so existing pods
	// don't change behaviour when this field is not set on a CR.
	if FallbackImagePullPolicy != corev1.PullIfNotPresent {
		t.Errorf("FallbackImagePullPolicy = %q, want %q", FallbackImagePullPolicy, corev1.PullIfNotPresent)
	}
}
