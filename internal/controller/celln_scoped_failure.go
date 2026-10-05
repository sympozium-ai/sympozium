package controller

import "strings"

// Only a closed set of public reason codes is copied to tenant-visible status.
// Arbitrary native diagnostics can include topology or guest-controlled text.
func scopedFailureSummary(phase, reason string) string {
	switch reason {
	case "AUTH_CAPACITY":
		return "AUTH_CAPACITY: native cell, memory or broker capacity is currently reserved. Stop another conversation and wait for confirmed cleanup, or ask the operator to increase capacity. This failed run will not restart automatically."
	case "AUTH_WORK_DEADLINE_EXPIRED":
		return "AUTH_WORK_DEADLINE_EXPIRED: the original execution deadline expired. Inspect cleanup before starting a fresh run; the old deadline cannot be extended."
	case "AUTH_BUDGET_EXHAUSTED":
		return "AUTH_BUDGET_EXHAUSTED: the original shared allowance is exhausted. Refreshing does not restore it."
	case "AUTH_POLICY_WITHDRAWN":
		return "AUTH_POLICY_WITHDRAWN: the original execution is no longer approved by operator policy."
	}
	return "Celln scoped execution " + phase
}

// scopedEndedSummary explains a run whose original owner kept reporting its
// parent context unavailable. Native reasons are free text, so only a
// recognised cause is mapped to its public code; nothing else is exposed.
func scopedEndedSummary(reason string) string {
	if strings.Contains(strings.ToLower(reason), "budget exhausted") {
		return "Celln parent ended: " + scopedFailureSummary("Failed", "AUTH_BUDGET_EXHAUSTED") + " Raise spec.enduring.maxOutputTokens or maxModelRequests for longer conversations, and start a new run."
	}
	return "Celln parent ended: its node reports the conversation's context unavailable. No replacement execution is permitted; start a new run."
}
