package main

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellninstall"
)

// checkFleetBudget compares the scope's policy ceilings with what its turns
// cost. Every turn reserves the runtime profile's whole per-turn allowance
// from the parent's lifetime totals, so totals sized for a smaller allowance
// (3 requests / 1536 tokens a turn before the current starter package) run
// out before the turn ceiling does: the conversation ends early and the turn
// count the policy states is never reached. Backends of one scope may differ
// in what a turn costs (their max output tokens per request), so the report
// is per runtime profile.
func (d *doctor) checkFleetBudget(ctx context.Context) doctorFinding {
	const check = "Fleet turn budget"
	publication, err := cellninstall.ReadFleetPublication(ctx, d.client)
	if err != nil {
		return failed(check, err)
	}
	if !publication.Exists || publication.Scope == "" {
		return doctorFinding{Check: check, Status: statusPass, Summary: "no fleet configuration is published; an install sizes its own ceilings"}
	}
	_, policyName, _ := cellninstall.PlatformCatalogueNames(publication.Scope)
	var policy sympoziumv1alpha1.CellnExecutionPolicy
	if err := d.client.Get(ctx, types.NamespacedName{Name: policyName}, &policy); apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
		return doctorFinding{Check: check, Status: statusPass, Summary: fmt.Sprintf("policy %s is not published yet; the install sizes its ceilings", policyName)}
	} else if err != nil {
		return failed(check, err)
	}
	// Every backend of the scope is a runtime profile with its own per-turn
	// allowance (a backend may raise its output tokens per request), while the
	// policy's one set of ceilings bounds them all: report each profile. A
	// profile that states no allowance is held to the current starter package's.
	type profileBudget struct {
		name                           string
		turnRequests, turnTokens, buys int64
	}
	c := policy.Spec.Ceilings
	var budgets []profileBudget
	for _, ref := range policy.Spec.RuntimeProfiles {
		var profile sympoziumv1alpha1.CellnRuntimeProfile
		if d.client.Get(ctx, types.NamespacedName{Name: ref.Ref.Name}, &profile) != nil || profile.Spec.Native == nil {
			continue
		}
		requests, tokens := cellninstall.ProfileTurnAllowance(&profile)
		budgets = append(budgets, profileBudget{name: profile.Name, turnRequests: requests, turnTokens: tokens, buys: cellninstall.TurnsAfforded(c.MaxModelRequests, c.MaxOutputTokens, requests, tokens)})
	}
	if len(budgets) == 0 {
		requests, tokens := cellninstall.ProfileTurnAllowance(nil)
		budgets = append(budgets, profileBudget{turnRequests: requests, turnTokens: tokens, buys: cellninstall.TurnsAfforded(c.MaxModelRequests, c.MaxOutputTokens, requests, tokens)})
	}
	// The costliest profile affords the fewest turns and sizes the remedy.
	worst, uniform := budgets[0], true
	for _, b := range budgets[1:] {
		uniform = uniform && b.turnRequests == budgets[0].turnRequests && b.turnTokens == budgets[0].turnTokens
		if b.buys < worst.buys {
			worst = b
		}
	}
	sized := fmt.Sprintf("policy %s allows %d turns with %d model requests and %d output tokens; ", policyName, c.MaxTurns, c.MaxModelRequests, c.MaxOutputTokens)
	if uniform {
		sized += fmt.Sprintf("a turn reserves %d requests and %d tokens", worst.turnRequests, worst.turnTokens)
	} else {
		sized += "a turn reserves, per runtime profile: "
		for i, b := range budgets {
			if i > 0 {
				sized += "; "
			}
			sized += fmt.Sprintf("%s %d requests and %d tokens (pays for %d turns)", b.name, b.turnRequests, b.turnTokens, b.buys)
		}
	}
	if c.MaxTurns < 1 || worst.buys >= c.MaxTurns {
		return doctorFinding{Check: check, Status: statusPass, Summary: sized}
	}
	ends := fmt.Sprintf("so a conversation ends after %d turns", worst.buys)
	if !uniform {
		var short []string
		for _, b := range budgets {
			if b.buys < c.MaxTurns {
				short = append(short, fmt.Sprintf("%s after %d turns", b.name, b.buys))
			}
		}
		ends = "so a conversation ends early: " + strings.Join(short, ", ")
	}
	return doctorFinding{Check: check, Status: statusWarn,
		Summary: fmt.Sprintf("%s, %s (%d requests and %d tokens per allowed turn)", sized, ends, c.MaxModelRequests/c.MaxTurns, c.MaxOutputTokens/c.MaxTurns),
		Remedy: []string{
			fmt.Sprintf("sympozium install --celln-fleet-replace-package --celln-fleet-max-turns %d --celln-fleet-max-model-requests %d --celln-fleet-max-output-tokens %d   # ceilings are set when a scope is configured for a package and rewritten only when the install moves it to another package; live parents are lost", c.MaxTurns, cellninstall.CapFleetTotal(c.MaxTurns*worst.turnRequests, cellninstall.MaxFleetModelRequests), cellninstall.CapFleetTotal(c.MaxTurns*worst.turnTokens, cellninstall.MaxFleetOutputTokens)),
			fmt.Sprintf("or accept %d turns per conversation; 'Restart elsewhere' continues a conversation that ran out", worst.buys),
		}}
}
