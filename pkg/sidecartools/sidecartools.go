// Package sidecartools holds helpers shared between the controller (which writes
// the native sidecar-tools manifest and the IPC routing target) and the
// agent-runner (which consumes the manifest and dispatches exec requests). It
// exists so the producer and consumer cannot drift on the target-normalization
// contract.
package sidecartools

import "strings"

// NormalizeTarget returns the canonical form of a SkillPack target name used in
// execRequest.Target. Both the producer (controller/agent-runner) and the
// consumer (tool-executor.sh in skill sidecars) must use equivalent
// normalization so that case or surrounding-whitespace differences do not cause
// silent routing misses. SkillPack names are DNS-1123 labels and so never
// contain internal whitespace.
func NormalizeTarget(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
