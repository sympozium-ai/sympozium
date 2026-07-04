package webhook

import (
	"testing"

	"github.com/go-logr/logr"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

func runWithSandboxResources(cpuReq, cpuLim, memReq, memLim string) *sympoziumv1alpha1.AgentRun {
	res := &sympoziumv1alpha1.ResourceSpec{
		Requests: map[string]string{},
		Limits:   map[string]string{},
	}
	if cpuReq != "" {
		res.Requests["cpu"] = cpuReq
	}
	if memReq != "" {
		res.Requests["memory"] = memReq
	}
	if cpuLim != "" {
		res.Limits["cpu"] = cpuLim
	}
	if memLim != "" {
		res.Limits["memory"] = memLim
	}
	return &sympoziumv1alpha1.AgentRun{
		Spec: sympoziumv1alpha1.AgentRunSpec{
			Sandbox: &sympoziumv1alpha1.AgentRunSandboxSpec{
				Enabled:   true,
				Resources: res,
			},
		},
	}
}

func policyWithCaps(maxCPU, maxMem string) *sympoziumv1alpha1.SympoziumPolicy {
	return &sympoziumv1alpha1.SympoziumPolicy{
		Spec: sympoziumv1alpha1.SympoziumPolicySpec{
			SandboxPolicy: &sympoziumv1alpha1.SandboxPolicySpec{
				MaxCPU:    maxCPU,
				MaxMemory: maxMem,
			},
		},
	}
}

func TestValidateResources(t *testing.T) {
	pe := &PolicyEnforcer{Log: logr.Discard()}

	cases := []struct {
		name    string
		run     *sympoziumv1alpha1.AgentRun
		policy  *sympoziumv1alpha1.SympoziumPolicy
		wantErr bool
	}{
		{
			name:    "within limits",
			run:     runWithSandboxResources("500m", "1", "1Gi", "2Gi"),
			policy:  policyWithCaps("2", "4Gi"),
			wantErr: false,
		},
		{
			name:    "cpu request exceeds cap",
			run:     runWithSandboxResources("3", "", "", ""),
			policy:  policyWithCaps("2", "4Gi"),
			wantErr: true,
		},
		{
			name:    "cpu limit exceeds cap",
			run:     runWithSandboxResources("", "3", "", ""),
			policy:  policyWithCaps("2", "4Gi"),
			wantErr: true,
		},
		{
			name:    "memory limit exceeds cap",
			run:     runWithSandboxResources("", "", "", "8Gi"),
			policy:  policyWithCaps("2", "4Gi"),
			wantErr: true,
		},
		{
			name:    "exactly at cap is allowed",
			run:     runWithSandboxResources("2", "2", "4Gi", "4Gi"),
			policy:  policyWithCaps("2", "4Gi"),
			wantErr: false,
		},
		{
			name:    "no caps configured allows anything",
			run:     runWithSandboxResources("64", "64", "256Gi", "256Gi"),
			policy:  policyWithCaps("", ""),
			wantErr: false,
		},
		{
			name:    "malformed policy cap is rejected, not ignored",
			run:     runWithSandboxResources("1", "", "", ""),
			policy:  policyWithCaps("not-a-quantity", ""),
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := pe.validateResources(tc.run, tc.policy)
			if tc.wantErr && err == nil {
				t.Errorf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("expected no error, got: %v", err)
			}
		})
	}
}

func TestValidateResources_NoSandbox(t *testing.T) {
	pe := &PolicyEnforcer{Log: logr.Discard()}
	// A run with no sandbox spec must pass regardless of caps.
	run := &sympoziumv1alpha1.AgentRun{}
	if err := pe.validateResources(run, policyWithCaps("1", "1Gi")); err != nil {
		t.Errorf("expected nil for run without sandbox, got: %v", err)
	}
}
