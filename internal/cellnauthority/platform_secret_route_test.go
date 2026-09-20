package cellnauthority

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/types"
)

// secretRouteFixture is an Agent that owns its model backend (a Secret-backed
// ModelConnection) running enduring on a FLEET runtime profile: the profile
// carries another credential's native material and the policy admits both the
// fleet's host-profile route and an operator-declared secret route.
func secretRouteFixture(t *testing.T, mutate func(ctx context.Context, f platformFixture)) (platformFixture, PlatformResolveRequest) {
	t.Helper()
	f := newPlatformFixture(t, "tenant", true)
	ctx := t.Context()
	raw := func(s string) apiextensionsv1.JSON { return apiextensionsv1.JSON{Raw: []byte(s)} }
	var profile api.CellnRuntimeProfile
	if err := f.client.Get(ctx, types.NamespacedName{Name: "json-agent-v1"}, &profile); err != nil {
		t.Fatal(err)
	}
	profile.Spec.Native = &api.CellnNativeProvisioning{AdmissionWindowMs: 60000, Parent: raw(`{"workload":{"caller":"sympozium:celln"}}`), Worker: raw(`{"workload":{"caller":"sympozium:celln"}}`), Template: raw(`{"contract":"celln.json-tools/v1"}`), ModelProfile: "blake3:" + strings.Repeat("c", 64), CredentialProfile: "fleet-key", ReservedMemoryBytes: 1, TurnModelRequests: api.TurnModelRequests, TurnOutputTokens: api.TurnOutputTokens}
	if err := f.client.Update(ctx, &profile); err != nil {
		t.Fatal(err)
	}
	var policies api.CellnExecutionPolicyList
	if err := f.client.List(ctx, &policies); err != nil {
		t.Fatal(err)
	}
	for i := range policies.Items {
		p := &policies.Items[i]
		p.Spec.Routes = append([]api.CellnExecutionPolicyRoute{{Provider: "llama", Protocol: "openai-chat", Models: []string{"fleet-model"}, EndpointOrigins: []string{"https://fleet.example"}, Auth: "host-profile"}}, p.Spec.Routes...)
		p.Spec.Ceilings = api.CellnExecutionPolicyCeilings{MaxTurns: 64, MaxModelRequests: 384, MaxOutputTokens: 1 << 20, MaxParentLeaseSeconds: 14400, MaxTurnSeconds: 60}
		if err := f.client.Update(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	var run api.AgentRun
	if err := f.client.Get(ctx, f.runKey, &run); err != nil {
		t.Fatal(err)
	}
	run.Spec.ExecutionLifecycle = "enduring"
	run.Spec.Enduring = &api.EnduringRunSpec{LeaseSeconds: 240, MaxTurns: 4, MaxModelRequests: 24, MaxOutputTokens: 98304}
	if err := f.client.Update(ctx, &run); err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(ctx, f)
	}
	request := platformRequest(f)
	request.ParentIncarnation = "blake3:" + strings.Repeat("f", 64)
	return f, request
}

func updateConnection(t *testing.T, ctx context.Context, f platformFixture, mutate func(*api.ModelConnection)) {
	t.Helper()
	var connection api.ModelConnection
	if err := f.client.Get(ctx, types.NamespacedName{Namespace: f.runKey.Namespace, Name: "model"}, &connection); err != nil {
		t.Fatal(err)
	}
	mutate(&connection)
	if err := f.client.Update(ctx, &connection); err != nil {
		t.Fatal(err)
	}
}

func updateAgent(t *testing.T, ctx context.Context, f platformFixture, mutate func(*api.Agent)) {
	t.Helper()
	var agent api.Agent
	if err := f.client.Get(ctx, types.NamespacedName{Namespace: f.runKey.Namespace, Name: "agent"}, &agent); err != nil {
		t.Fatal(err)
	}
	mutate(&agent)
	if err := f.client.Update(ctx, &agent); err != nil {
		t.Fatal(err)
	}
}

// The fleet profile's native credential profile binds host-profile connections
// only: a Secret-backed connection may run on the same reviewed worker.
func TestSecretRouteRunsOnFleetRuntimeProfile(t *testing.T) {
	f, request := secretRouteFixture(t, nil)
	resolution, err := f.resolver.Resolve(t.Context(), f.runKey, request)
	if err != nil {
		t.Fatal(err)
	}
	route := resolution.Decision.Route
	want := CredentialSourceRef{Kind: "Secret", SecretName: "model-secret", SecretKey: "OPENAI_API_KEY"}
	if route.Auth != "secret" || route.CredentialSourceRef == nil || *route.CredentialSourceRef != want || route.CredentialSource != nil || route.EndpointOrigin != "https://model.example" || route.Model != "gpt-test" {
		t.Fatalf("secret route not bound: %+v", route)
	}
	if resolution.Execution.CredentialSourceRef == nil || *resolution.Execution.CredentialSourceRef != want || resolution.Execution.ProfileSpec.Native == nil {
		t.Fatalf("execution material lost the credential reference or the fleet profile: %+v", resolution.Execution)
	}
	if f.spy.secretReads != 0 {
		t.Fatalf("resolver read %d Secrets", f.spy.secretReads)
	}
	if resolution.Decision.Lifecycle != "enduring-initial" {
		t.Fatalf("lifecycle = %q", resolution.Decision.Lifecycle)
	}
	finalized, err := resolution.Decision.FinalizeCredentialSource("secret-uid")
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := finalized.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	validateDecisionSchema(t, canonical)
	if err := f.resolver.Revalidate(t.Context(), f.runKey, *resolution); err != nil {
		t.Fatalf("stable secret route failed revalidation: %v", err)
	}
	// An Anthropic-protocol connection names the Anthropic key of its Secret.
	f, request = secretRouteFixture(t, func(ctx context.Context, f platformFixture) {
		updateConnection(t, ctx, f, func(c *api.ModelConnection) { c.Spec.Protocol = "anthropic-messages" })
		var policies api.CellnExecutionPolicyList
		if err := f.client.List(ctx, &policies); err != nil {
			t.Fatal(err)
		}
		for i := range policies.Items {
			policies.Items[i].Spec.Routes[1].Protocol = "anthropic-messages"
			if err := f.client.Update(ctx, &policies.Items[i]); err != nil {
				t.Fatal(err)
			}
		}
	})
	resolution, err = f.resolver.Resolve(t.Context(), f.runKey, request)
	if err != nil || resolution.Decision.Route.CredentialSourceRef.SecretKey != "ANTHROPIC_API_KEY" {
		t.Fatalf("anthropic secret key not selected: %v", err)
	}
}

func TestSecretRouteRefusals(t *testing.T) {
	anyProvider := func(ctx context.Context, t *testing.T, f platformFixture) {
		updateAgent(t, ctx, f, func(a *api.Agent) { a.Spec.AuthRefs = []api.SecretRef{{Secret: "model-secret"}} })
	}
	for _, tc := range []struct {
		name   string
		reason string
		detail string
		change func(ctx context.Context, t *testing.T, f platformFixture)
	}{
		// The host-profile binding to the runtime profile is not relaxed.
		{name: "host profile other than the runtime profile's", reason: ReasonRouteMismatch, detail: "installed model credential", change: func(ctx context.Context, t *testing.T, f platformFixture) {
			updateConnection(t, ctx, f, func(c *api.ModelConnection) {
				c.Spec = api.ModelConnectionSpec{Provider: "llama", Protocol: "openai-chat", Endpoint: "https://fleet.example/v1/chat/completions", CredentialProfile: "someone-elses-key", Models: []string{"gpt-test"}}
			})
		}},
		// Policy routes stay the operator's allow-list; a tenant-authored
		// connection is never its own authorisation.
		{name: "provider outside every route", reason: ReasonRouteMismatch, detail: "exact model route", change: func(ctx context.Context, t *testing.T, f platformFixture) {
			anyProvider(ctx, t, f)
			updateConnection(t, ctx, f, func(c *api.ModelConnection) { c.Spec.Provider = "anthropic" })
		}},
		{name: "origin outside every route", reason: ReasonRouteMismatch, detail: "exact model route", change: func(ctx context.Context, t *testing.T, f platformFixture) {
			updateConnection(t, ctx, f, func(c *api.ModelConnection) { c.Spec.Endpoint = "https://attacker.example/v1/chat/completions" })
		}},
		{name: "model outside every route", reason: ReasonRouteMismatch, detail: "exact model route", change: func(ctx context.Context, t *testing.T, f platformFixture) {
			updateConnection(t, ctx, f, func(c *api.ModelConnection) { c.Spec.Models = []string{"gpt-test", "unlisted"} })
			var run api.AgentRun
			if err := f.client.Get(ctx, f.runKey, &run); err != nil {
				t.Fatal(err)
			}
			run.Spec.Model.Model = "unlisted"
			if err := f.client.Update(ctx, &run); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "fleet origin is a host-profile route only", reason: ReasonRouteMismatch, detail: "exact model route", change: func(ctx context.Context, t *testing.T, f platformFixture) {
			anyProvider(ctx, t, f)
			updateConnection(t, ctx, f, func(c *api.ModelConnection) {
				c.Spec.Provider, c.Spec.Endpoint, c.Spec.Models = "llama", "https://fleet.example/v1/chat/completions", []string{"fleet-model", "gpt-test"}
			})
		}},
		// The Agent owner, not the run author, grants a Secret.
		{name: "Agent grants no Secret", reason: ReasonRouteMismatch, detail: "does not grant", change: func(ctx context.Context, t *testing.T, f platformFixture) {
			updateAgent(t, ctx, f, func(a *api.Agent) { a.Spec.AuthRefs = nil })
		}},
		{name: "Agent grants another Secret", reason: ReasonRouteMismatch, detail: "does not grant", change: func(ctx context.Context, t *testing.T, f platformFixture) {
			updateAgent(t, ctx, f, func(a *api.Agent) { a.Spec.AuthRefs = []api.SecretRef{{Provider: "openai", Secret: "other-secret"}} })
		}},
		{name: "Agent grants the Secret for another provider", reason: ReasonRouteMismatch, detail: "does not grant", change: func(ctx context.Context, t *testing.T, f platformFixture) {
			updateAgent(t, ctx, f, func(a *api.Agent) { a.Spec.AuthRefs = []api.SecretRef{{Provider: "anthropic", Secret: "model-secret"}} })
		}},
		{name: "Agent selects another connection", reason: ReasonRouteMismatch, detail: "does not grant", change: func(ctx context.Context, t *testing.T, f platformFixture) {
			updateAgent(t, ctx, f, func(a *api.Agent) {
				a.Spec.AuthRefs = nil
				a.Spec.Execution = &api.AgentExecutionDefaults{ModelConnectionRef: "other"}
			})
		}},
		{name: "Agent grants the Secret for any provider", change: anyProvider},
		{name: "Agent selects this connection", change: func(ctx context.Context, t *testing.T, f platformFixture) {
			updateAgent(t, ctx, f, func(a *api.Agent) {
				a.Spec.AuthRefs = nil
				a.Spec.Execution = &api.AgentExecutionDefaults{ModelConnectionRef: "model"}
			})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, request := secretRouteFixture(t, func(ctx context.Context, f platformFixture) { tc.change(ctx, t, f) })
			_, err := f.resolver.Resolve(t.Context(), f.runKey, request)
			if PlatformReason(err) != tc.reason {
				t.Fatalf("reason = %q, want %q; error %v", PlatformReason(err), tc.reason, err)
			}
			if err != nil && !strings.Contains(err.Error(), tc.detail) {
				t.Fatalf("refusal %q does not mention %q", err, tc.detail)
			}
		})
	}
}

// A withdrawn grant is a change of authority between resolution and admission.
func TestSecretRouteGrantIsRevalidated(t *testing.T) {
	f, request := secretRouteFixture(t, nil)
	resolution, err := f.resolver.Resolve(t.Context(), f.runKey, request)
	if err != nil {
		t.Fatal(err)
	}
	updateAgent(t, t.Context(), f, func(a *api.Agent) { a.Spec.AuthRefs = nil })
	if err := f.resolver.Revalidate(t.Context(), f.runKey, *resolution); PlatformReason(err) != ReasonRouteMismatch {
		t.Fatalf("withdrawn Secret grant survived revalidation: %v", err)
	}
}

func TestMediatedTurnBudgetFollowsConnectionOutputBound(t *testing.T) {
	for _, tc := range []struct {
		name            string
		maxOutputTokens int64
		enduring        api.EnduringRunSpec
		policyTokens    int64
		wantTurn        DecisionCap
		wantHint        int64
		reason          string
		detail          []string
	}{
		// Unset keeps the profile's reviewed allowance, exactly as before.
		{name: "connection without its own bound", enduring: api.EnduringRunSpec{LeaseSeconds: 240, MaxTurns: 4, MaxModelRequests: 24, MaxOutputTokens: 98304}, policyTokens: 1 << 20, wantTurn: DecisionCap{Requests: api.TurnModelRequests, OutputTokens: api.TurnOutputTokens}},
		// The default spelled out is still the default: nothing is sent to the
		// receiver, so the prepared operation is what it was before the field.
		{name: "connection stating the default", maxOutputTokens: api.DefaultRequestOutputTokens, enduring: api.EnduringRunSpec{LeaseSeconds: 240, MaxTurns: 4, MaxModelRequests: 24, MaxOutputTokens: 98304}, policyTokens: 1 << 20, wantTurn: DecisionCap{Requests: api.TurnModelRequests, OutputTokens: api.TurnOutputTokens}},
		{name: "largest bound", maxOutputTokens: api.MaxRequestOutputTokens, enduring: api.EnduringRunSpec{LeaseSeconds: 240, MaxTurns: 4, MaxModelRequests: 24, MaxOutputTokens: 98304}, policyTokens: 1 << 20, wantTurn: DecisionCap{Requests: 6, OutputTokens: 24576}, wantHint: 4096},
		{name: "smaller than the fleet backend's", maxOutputTokens: api.MinRequestOutputTokens, enduring: api.EnduringRunSpec{LeaseSeconds: 240, MaxTurns: 4, MaxModelRequests: 24, MaxOutputTokens: 98304}, policyTokens: 1 << 20, wantTurn: DecisionCap{Requests: 6, OutputTokens: 1536}, wantHint: 256},
		{name: "run budget exactly one turn", maxOutputTokens: 4096, enduring: api.EnduringRunSpec{LeaseSeconds: 240, MaxTurns: 1, MaxModelRequests: 6, MaxOutputTokens: 24576}, policyTokens: 1 << 20, wantTurn: DecisionCap{Requests: 6, OutputTokens: 24576}, wantHint: 4096},
		{name: "run output tokens cannot pay for a turn", maxOutputTokens: 4096, enduring: api.EnduringRunSpec{LeaseSeconds: 240, MaxTurns: 4, MaxModelRequests: 24, MaxOutputTokens: 24575}, policyTokens: 1 << 20, reason: ReasonLimitRange, detail: []string{"24576 output tokens", "6 requests x 4096", "allows 24 requests and 24575 output tokens"}},
		{name: "run model requests cannot pay for a turn", maxOutputTokens: 4096, enduring: api.EnduringRunSpec{LeaseSeconds: 240, MaxTurns: 4, MaxModelRequests: 5, MaxOutputTokens: 98304}, policyTokens: 1 << 20, reason: ReasonLimitRange, detail: []string{"needs 6 model requests", "allows 5 requests"}},
		{name: "policy ceiling cannot pay for a turn", maxOutputTokens: 4096, enduring: api.EnduringRunSpec{LeaseSeconds: 240, MaxTurns: 4, MaxModelRequests: 24, MaxOutputTokens: 12288}, policyTokens: 12288, reason: ReasonLimitRange, detail: []string{"24576 output tokens", "12288 output tokens"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, request := secretRouteFixture(t, func(ctx context.Context, f platformFixture) {
				updateConnection(t, ctx, f, func(c *api.ModelConnection) { c.Spec.MaxOutputTokens = tc.maxOutputTokens })
				var policies api.CellnExecutionPolicyList
				if err := f.client.List(ctx, &policies); err != nil {
					t.Fatal(err)
				}
				for i := range policies.Items {
					policies.Items[i].Spec.Ceilings.MaxOutputTokens = tc.policyTokens
					if err := f.client.Update(ctx, &policies.Items[i]); err != nil {
						t.Fatal(err)
					}
				}
				var run api.AgentRun
				if err := f.client.Get(ctx, f.runKey, &run); err != nil {
					t.Fatal(err)
				}
				run.Spec.Enduring = tc.enduring.DeepCopy()
				if err := f.client.Update(ctx, &run); err != nil {
					t.Fatal(err)
				}
			})
			resolution, err := f.resolver.Resolve(t.Context(), f.runKey, request)
			if PlatformReason(err) != tc.reason {
				t.Fatalf("reason = %q, want %q; error %v", PlatformReason(err), tc.reason, err)
			}
			if err != nil {
				// The numbers must survive into tenant status.
				shown := PlatformDetail(err)
				for _, want := range tc.detail {
					if !strings.Contains(shown, want) {
						t.Fatalf("tenant-visible refusal %q does not name %q", shown, want)
					}
				}
				return
			}
			budget := resolution.Decision.Budget
			if budget.TurnCap != tc.wantTurn || budget.RunCap != (DecisionCap{Requests: int64(tc.enduring.MaxModelRequests), OutputTokens: tc.enduring.MaxOutputTokens}) || budget.MaxTurns != int64(tc.enduring.MaxTurns) {
				t.Fatalf("budget = %+v, want turn cap %+v", budget, tc.wantTurn)
			}
			if resolution.Execution.RequestOutputTokens != tc.wantHint {
				t.Fatalf("request output tokens hint = %d, want %d", resolution.Execution.RequestOutputTokens, tc.wantHint)
			}
			// The receiver refuses a null, a string or an out-of-range value, and
			// requires the worker's whole loop at the bound to fit turn and run.
			material, err := json.Marshal(resolution.Execution)
			if err != nil {
				t.Fatal(err)
			}
			if present := strings.Contains(string(material), `"requestOutputTokens"`); present != (tc.wantHint != 0) {
				t.Fatalf("requestOutputTokens present = %v for hint %d: %s", present, tc.wantHint, material)
			}
			if tc.wantHint != 0 {
				if !strings.Contains(string(material), fmt.Sprintf(`"requestOutputTokens":%d`, tc.wantHint)) || tc.wantHint < api.MinRequestOutputTokens || tc.wantHint > api.MaxRequestOutputTokens {
					t.Fatalf("requestOutputTokens is not a bounded integer: %s", material)
				}
				loop := min(resolution.Execution.ProfileSpec.JSON.MaxTurns, budget.TurnCap.Requests)
				if loop*tc.wantHint > budget.TurnCap.OutputTokens || loop*tc.wantHint > budget.RunCap.OutputTokens {
					t.Fatalf("the worker loop (%d x %d) does not fit the budget %+v", loop, tc.wantHint, budget)
				}
			}
			finalized, err := resolution.Decision.FinalizeCredentialSource("secret-uid")
			if err != nil {
				t.Fatal(err)
			}
			canonical, err := finalized.Canonical()
			if err != nil {
				t.Fatal(err)
			}
			validateDecisionSchema(t, canonical)
		})
	}
}

// A follow-up turn keeps the turn allowance its parent was admitted with.
func TestMediatedTurnBudgetIsRetainedByFollowUpTurns(t *testing.T) {
	f, request := secretRouteFixture(t, func(ctx context.Context, f platformFixture) {
		updateConnection(t, ctx, f, func(c *api.ModelConnection) { c.Spec.MaxOutputTokens = 2048 })
	})
	initial, err := f.resolver.Resolve(t.Context(), f.runKey, request)
	if err != nil {
		t.Fatal(err)
	}
	original, err := initial.Decision.FinalizeCredentialSource("secret-uid")
	if err != nil {
		t.Fatal(err)
	}
	turn := &api.AgentRunTurn{Spec: api.AgentRunTurnSpec{RunName: "run", RunUID: "tenant-run", Message: "continue"}}
	turn.Namespace, turn.Name, turn.UID, turn.Generation = "tenant", "turn-1", "turn-uid", 1
	if err := f.client.Create(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	request.Operation, request.TurnKey, request.Original = "execution.turn", &types.NamespacedName{Namespace: "tenant", Name: "turn-1"}, &original
	next, err := f.resolver.Resolve(t.Context(), f.runKey, request)
	if err != nil {
		t.Fatal(err)
	}
	if next.Decision.Budget.TurnCap != (DecisionCap{Requests: 6, OutputTokens: 12288}) || next.Decision.Budget.RunCap != initial.Decision.Budget.RunCap || next.Execution.RequestOutputTokens != 2048 {
		t.Fatalf("turn budget = %+v", next.Decision.Budget)
	}
}

// The receiver refuses the field on a model-free route, and a host-profile
// route is served by the native host broker, which never reads it.
func TestRequestOutputTokensIsOnlySentOnGatewayMediatedRoutes(t *testing.T) {
	direct := newPlatformFixture(t, "tenant", false)
	resolution, err := direct.resolver.Resolve(t.Context(), direct.runKey, platformRequest(direct))
	if err != nil || resolution.Decision.Route.Provider != "none" || resolution.Execution.RequestOutputTokens != 0 {
		t.Fatalf("model-free operation: %v %+v", err, resolution)
	}
	if got := mediatedRequestOutputTokens(&api.ModelConnection{Spec: api.ModelConnectionSpec{CredentialProfile: "fleet-key", MaxOutputTokens: 4096}}); got != 0 {
		t.Fatalf("host-profile connection carries a request bound: %d", got)
	}
	if got := mediatedRequestOutputTokens(&api.ModelConnection{Spec: api.ModelConnectionSpec{MaxOutputTokens: 2048}}); got != 2048 {
		t.Fatalf("credential-free mediated connection bound = %d", got)
	}
}
