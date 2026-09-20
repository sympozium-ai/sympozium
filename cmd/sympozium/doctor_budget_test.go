package main

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

func TestDoctorFleetTurnBudget(t *testing.T) {
	policy := func(turns, requests, tokens int64) *sympoziumv1alpha1.CellnExecutionPolicy {
		return &sympoziumv1alpha1.CellnExecutionPolicy{ObjectMeta: metav1.ObjectMeta{Name: "celln-fleet-starter"}, Spec: sympoziumv1alpha1.CellnExecutionPolicySpec{
			RuntimeProfiles: []sympoziumv1alpha1.CellnExecutionPolicyRuntime{{Ref: sympoziumv1alpha1.CellnRuntimeProfileRef{Name: "celln-native-starter", Revision: "v1"}}},
			Ceilings:        sympoziumv1alpha1.CellnExecutionPolicyCeilings{MaxTurns: turns, MaxModelRequests: requests, MaxOutputTokens: tokens, MaxParentLeaseSeconds: 86400, MaxTurnSeconds: 60}}}
	}
	profile := func(requests, tokens int64) *sympoziumv1alpha1.CellnRuntimeProfile {
		return &sympoziumv1alpha1.CellnRuntimeProfile{ObjectMeta: metav1.ObjectMeta{Name: "celln-native-starter"}, Spec: sympoziumv1alpha1.CellnRuntimeProfileSpec{Revision: "v1",
			Native: &sympoziumv1alpha1.CellnNativeProvisioning{TurnModelRequests: requests, TurnOutputTokens: tokens}}}
	}
	for _, tc := range []struct {
		name    string
		objects []client.Object
		status  string
		want    []string
	}{
		{name: "nothing published", status: statusPass, want: []string{"no fleet configuration"}},
		{name: "no policy yet", objects: []client.Object{publishedFleet(newPackage, "starter")}, status: statusPass, want: []string{"not published yet"}},
		{name: "ceilings sized for the current allowance", objects: []client.Object{publishedFleet(newPackage, "starter"), policy(256, 1536, 786432), profile(6, 3072)}, status: statusPass, want: []string{"256 turns"}},
		{name: "old package keeps its smaller allowance", objects: []client.Object{publishedFleet(installedPackage, "starter"), policy(256, 768, 393216), profile(3, 1536)}, status: statusPass, want: []string{"reserves 3 requests and 1536 tokens"}},
		{name: "old ceilings under the current allowance", objects: []client.Object{publishedFleet(newPackage, "starter"), policy(256, 768, 393216), profile(6, 3072)}, status: statusWarn,
			want: []string{"ends after 128 turns", "3 requests and 1536 tokens per allowed turn", "--celln-fleet-max-model-requests 1536 --celln-fleet-max-output-tokens 786432", "or accept 128 turns"}},
		{name: "too few tokens alone", objects: []client.Object{publishedFleet(newPackage, "starter"), policy(256, 1536, 393216), profile(6, 3072)}, status: statusWarn, want: []string{"ends after 128 turns"}},
		{name: "a profile without an allowance is held to the current one", objects: []client.Object{publishedFleet(newPackage, "starter"), policy(256, 768, 393216)}, status: statusWarn, want: []string{"reserves 6 requests and 3072 tokens", "ends after 128 turns"}},
		{name: "a suggestion never exceeds the maximum", objects: []client.Object{publishedFleet(newPackage, "starter"), policy(1024, 768, 393216), profile(6, 3072)}, status: statusWarn, want: []string{"--celln-fleet-max-model-requests 6144 --celln-fleet-max-output-tokens 3145728"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := testDoctor(t, append([]client.Object{kvmNode("node-a")}, tc.objects...)...)
			f := findingOf(t, d.run(context.Background()), "Fleet turn budget")
			text := f.Summary + "\n" + strings.Join(f.Remedy, "\n")
			if f.Status != tc.status {
				t.Fatalf("got %s: %s", f.Status, text)
			}
			for _, want := range tc.want {
				if !strings.Contains(text, want) {
					t.Fatalf("missing %q in %s", want, text)
				}
			}
		})
	}
}

// Backends of one scope may allow different output tokens per request, so
// each runtime profile is reported with the turns the shared ceilings pay for.
func TestDoctorFleetTurnBudgetIsPerProfile(t *testing.T) {
	policy := func(turns, requests, tokens int64, profiles ...string) *sympoziumv1alpha1.CellnExecutionPolicy {
		refs := make([]sympoziumv1alpha1.CellnExecutionPolicyRuntime, 0, len(profiles))
		for _, name := range profiles {
			refs = append(refs, sympoziumv1alpha1.CellnExecutionPolicyRuntime{Ref: sympoziumv1alpha1.CellnRuntimeProfileRef{Name: name, Revision: "v1"}})
		}
		return &sympoziumv1alpha1.CellnExecutionPolicy{ObjectMeta: metav1.ObjectMeta{Name: "celln-fleet-starter"}, Spec: sympoziumv1alpha1.CellnExecutionPolicySpec{RuntimeProfiles: refs,
			Ceilings: sympoziumv1alpha1.CellnExecutionPolicyCeilings{MaxTurns: turns, MaxModelRequests: requests, MaxOutputTokens: tokens, MaxParentLeaseSeconds: 86400, MaxTurnSeconds: 60}}}
	}
	profile := func(name string, tokens int64) *sympoziumv1alpha1.CellnRuntimeProfile {
		return &sympoziumv1alpha1.CellnRuntimeProfile{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: sympoziumv1alpha1.CellnRuntimeProfileSpec{Revision: "v1",
			Native: &sympoziumv1alpha1.CellnNativeProvisioning{TurnModelRequests: 6, TurnOutputTokens: tokens}}}
	}
	names := []string{"celln-native-starter", "celln-native-starter-thinker", "celln-native-starter-deep"}
	profiles := []client.Object{profile(names[0], 3072), profile(names[1], 12288), profile(names[2], 24576)}
	for _, tc := range []struct {
		name    string
		policy  *sympoziumv1alpha1.CellnExecutionPolicy
		status  string
		want    []string
		without []string
	}{
		{name: "ceilings sized for the costliest backend", policy: policy(256, 1536, 6291456, names...), status: statusPass,
			want: []string{"celln-native-starter 6 requests and 3072 tokens (pays for 256 turns)", "celln-native-starter-deep 6 requests and 24576 tokens (pays for 256 turns)"}},
		{name: "a backend added above what the ceilings were sized for", policy: policy(256, 1536, 786432, names...), status: statusWarn,
			want: []string{"celln-native-starter 6 requests and 3072 tokens (pays for 256 turns)", "celln-native-starter-thinker 6 requests and 12288 tokens (pays for 64 turns)", "celln-native-starter-deep 6 requests and 24576 tokens (pays for 32 turns)",
				"ends early: celln-native-starter-thinker after 64 turns, celln-native-starter-deep after 32 turns", "--celln-fleet-max-model-requests 1536 --celln-fleet-max-output-tokens 6291456", "or accept 32 turns"},
			without: []string{"celln-native-starter after"}},
		{name: "a suggestion never exceeds the raised maximum", policy: policy(1024, 6144, 3145728, names[0], names[2]), status: statusWarn, want: []string{"--celln-fleet-max-output-tokens 25165824", "after 128 turns"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := testDoctor(t, append([]client.Object{kvmNode("node-a"), publishedFleet(newPackage, "starter"), tc.policy}, profiles...)...)
			f := findingOf(t, d.run(context.Background()), "Fleet turn budget")
			text := f.Summary + "\n" + strings.Join(f.Remedy, "\n")
			if f.Status != tc.status {
				t.Fatalf("got %s: %s", f.Status, text)
			}
			for _, want := range tc.want {
				if !strings.Contains(text, want) {
					t.Fatalf("missing %q in %s", want, text)
				}
			}
			for _, without := range tc.without {
				if strings.Contains(text, without) {
					t.Fatalf("unexpected %q in %s", without, text)
				}
			}
		})
	}
}
