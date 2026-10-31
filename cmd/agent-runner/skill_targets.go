package main

import (
	"fmt"
	"os"
	"strings"
)

// skillTargets is the inventory of skill sidecars the model may address through
// execute_command's `target` argument: the SkillPack names the controller lists
// in SYMPOZIUM_SKILL_TARGETS, which are the same values each sidecar's
// tool-executor matches against SYMPOZIUM_SKILL_PACK. Empty when no skill
// sidecar is attached.
var skillTargets []string

// loadSkillTargets reads SYMPOZIUM_SKILL_TARGETS (comma-separated SkillPack
// names) into skillTargets.
func loadSkillTargets() {
	skillTargets = parseSkillTargets(os.Getenv("SYMPOZIUM_SKILL_TARGETS"))
}

func parseSkillTargets(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := normalizeSidecarTarget(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// resolveSkillTarget maps a model-supplied target to the SkillPack name a
// sidecar will claim, using valid as the inventory of attached sidecars. Without
// it, a target that matches no sidecar is claimed by nobody and the call waits
// out its full timeout.
//
//   - Empty input → "" (any sidecar may claim the request).
//   - Empty inventory → the input, normalized (no inventory to check against).
//   - Exact match → that name.
//   - A short name that is the unique "-<input>" suffix of one name (e.g.
//     "github-gitops" for a SkillPack installed as "<release>-github-gitops")
//     → that name.
//   - Otherwise an error listing the attached targets.
func resolveSkillTarget(input string, valid []string) (string, error) {
	in := normalizeSidecarTarget(input)
	if in == "" {
		return "", nil
	}
	if len(valid) == 0 {
		return in, nil
	}
	for _, t := range valid {
		if t == in {
			return t, nil
		}
	}
	var matches []string
	for _, t := range valid {
		if strings.HasSuffix(t, "-"+in) {
			matches = append(matches, t)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return "", fmt.Errorf("unknown target %q. Available targets: %s", input, strings.Join(valid, ", "))
	default:
		return "", fmt.Errorf("ambiguous target %q matches multiple sidecars: %s — use the full SkillPack name",
			input, strings.Join(matches, ", "))
	}
}
