package cellninstall

import (
	"fmt"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// Output tokens one model request of a backend may produce
// (modelConnection.maxOutputTokens of the starter-configure plan). A turn
// reserves api.TurnModelRequests requests of that size from its parent's
// lifetime totals.
const (
	DefaultModelMaxOutputTokens = api.DefaultRequestOutputTokens
	MinModelMaxOutputTokens     = api.MinRequestOutputTokens
	MaxModelMaxOutputTokens     = api.MaxRequestOutputTokens
	// ModelMaxOutputTokensMinCelln is the newest Celln release whose plans
	// refuse a modelConnection.maxOutputTokens field.
	ModelMaxOutputTokensMinCelln = "v0.5.23"
	// ModelMaxOutputTokensCellnHint names the usual reason a backend with its
	// own output cap never gets configured.
	ModelMaxOutputTokensCellnHint = "this backend sets max output tokens per request, which needs a Celln release newer than " + ModelMaxOutputTokensMinCelln + " on the nodes and a starter package built by it (an older Celln refuses the plan, and a newer one refuses an older package: look for \"starter-configure\" in the celln-node-configure logs; an installed fleet takes a new package with --celln-fleet-replace-package)"
)

// ValidateModelMaxOutputTokens is the one rule for a backend's output cap,
// used by the installer, the chart values and the API: 0 means Celln's default
// (DefaultModelMaxOutputTokens), anything else must be within Celln's range.
func ValidateModelMaxOutputTokens(tokens int64) error {
	return api.ValidateRequestOutputTokens(tokens)
}

// NormalModelMaxOutputTokens folds the default into "unset", so a backend
// asking for exactly the default carries nothing: its values, its entry in
// FLEET_BACKENDS and its plan stay what they were before the field existed.
func NormalModelMaxOutputTokens(tokens int64) int64 {
	if tokens == DefaultModelMaxOutputTokens {
		return 0
	}
	return tokens
}

// EffectiveModelMaxOutputTokens is the cap Celln applies.
func EffectiveModelMaxOutputTokens(tokens int64) int64 {
	return api.EffectiveRequestOutputTokens(tokens)
}

// TurnOutputTokensFor is what one turn of a backend reserves from its parent's
// lifetime output tokens: api.TurnModelRequests requests of the backend's cap.
func TurnOutputTokensFor(maxOutputTokens int64) int64 {
	return api.TurnModelRequests * EffectiveModelMaxOutputTokens(maxOutputTokens)
}

// HintForBackendModel is what to tell an operator whose backend never got
// configured: the Celln release its settings need, or nothing.
func HintForBackendModel(hasParameters bool, maxOutputTokens int64) string {
	switch {
	case NormalModelMaxOutputTokens(maxOutputTokens) != 0:
		// The newer requirement covers parameters too.
		return ModelMaxOutputTokensCellnHint
	case hasParameters:
		return ModelParametersCellnHint
	}
	return ""
}

// MostExpensiveBackend is the backend whose turns reserve the most output
// tokens (the first of equals), and that reservation.
func MostExpensiveBackend(backends []FleetBackend) (FleetBackend, int64) {
	var most FleetBackend
	turnTokens := int64(api.TurnOutputTokens)
	for i, b := range backends {
		if cost := TurnOutputTokensFor(b.Model.MaxOutputTokens); i == 0 || cost > turnTokens {
			most, turnTokens = b, cost
		}
	}
	return most, turnTokens
}

// ResolveFor sizes and checks the scope's one set of ceilings against the
// backends it is installed with. Backends may differ in what a turn costs, so
// the most expensive one decides: an unset lifetime output-token ceiling
// scales so the default turns (DefaultFleetLimits.MaxTurns) stay reachable on it (the note says so), and an
// explicit one must pay for at least one of its turns.
func (l FleetLimits) ResolveFor(backends []FleetBackend) (FleetLimits, string, error) {
	most, turnTokens := MostExpensiveBackend(backends)
	note := ""
	if l.MaxOutputTokens == 0 && turnTokens > api.TurnOutputTokens {
		turns := DefaultFleetLimits.MaxTurns
		l.MaxOutputTokens = CapFleetTotal(turns*turnTokens, MaxFleetOutputTokens)
		note = fmt.Sprintf("Lifetime output-token ceiling sized to %d (%d turns × %d tokens): backend %s allows %d output tokens per request and a turn reserves %d requests", l.MaxOutputTokens, turns, turnTokens, most.Name, EffectiveModelMaxOutputTokens(most.Model.MaxOutputTokens), api.TurnModelRequests)
	}
	resolved, err := l.Resolve()
	if err != nil {
		return resolved, "", err
	}
	if resolved.MaxOutputTokens < turnTokens {
		return resolved, "", fmt.Errorf("fleet limits: %d lifetime output tokens do not pay for one turn of backend %s, which reserves %d (%d requests × %d max output tokens per request); pass --celln-fleet-max-output-tokens %d or more (turns × %d), or lower the backend's max-output-tokens", resolved.MaxOutputTokens, most.Name, turnTokens, api.TurnModelRequests, EffectiveModelMaxOutputTokens(most.Model.MaxOutputTokens), turnTokens, turnTokens)
	}
	return resolved, note, nil
}

// SessionDefaultsFor is SessionDefaults for one runtime profile: its own
// per-turn allowance (spec.native.turnModelRequests/turnOutputTokens, else the
// current starter package's) decides how many turns the ceilings pay for.
func SessionDefaultsFor(ceilings api.EnduringRunSpec, profile *api.CellnRuntimeProfile) *api.EnduringRunSpec {
	turnRequests, turnTokens := ProfileTurnAllowance(profile)
	return SessionDefaultsAt(ceilings, turnRequests, turnTokens)
}

// ProfileTurnAllowance is what one turn on the profile reserves; a profile
// that states none is held to the current starter package's allowance.
func ProfileTurnAllowance(profile *api.CellnRuntimeProfile) (int64, int64) {
	if profile != nil && profile.Spec.Native != nil && profile.Spec.Native.TurnModelRequests > 0 && profile.Spec.Native.TurnOutputTokens > 0 {
		return profile.Spec.Native.TurnModelRequests, profile.Spec.Native.TurnOutputTokens
	}
	return api.TurnModelRequests, api.TurnOutputTokens
}

// FewerSessionTurnsWarning says so when a backend's turns cost more than the
// scope's ceilings were sized for, so a conversation on it gets fewer turns
// than the usual session; empty otherwise.
func FewerSessionTurnsWarning(backend string, maxOutputTokens int64, ceilings api.CellnExecutionPolicyCeilings) string {
	turnTokens := TurnOutputTokensFor(maxOutputTokens)
	afforded := TurnsAfforded(ceilings.MaxModelRequests, ceilings.MaxOutputTokens, api.TurnModelRequests, turnTokens)
	usual := min(int64(sessionTurns), ceilings.MaxTurns)
	if afforded >= usual {
		return ""
	}
	return fmt.Sprintf("a turn on backend %s reserves %d output tokens (%d requests × %d), and this fleet's ceilings (%d model requests, %d output tokens per conversation) pay for %d such turns, fewer than the usual %d. Conversations on it end sooner; 'Restart elsewhere' continues one that ran out, and 'sympozium doctor' shows how to raise the ceilings", backend, turnTokens, api.TurnModelRequests, EffectiveModelMaxOutputTokens(maxOutputTokens), ceilings.MaxModelRequests, ceilings.MaxOutputTokens, afforded, usual)
}
