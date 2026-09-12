package modelgateway

import (
	"crypto/sha256"
	"fmt"

	cap "github.com/sympozium-ai/sympozium/internal/cellncapability"
	"github.com/zeebo/blake3"
)

func fixtureDigest(text string) string { return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(text))) }
func fixtureIncarnation(text string) string {
	return fmt.Sprintf("blake3:%x", blake3.Sum256([]byte(text)))
}

// These are gateway fixtures, not real AgentRun/runtime admission evidence.
// Their wire shape must nevertheless be acceptable to the independent receiver.
func completeGatewayDecision(d *cap.Decision) {
	d.Run.Name = "run"
	d.Run.SpecSHA256 = fixtureDigest("run-spec")
	d.Subject = cap.SubjectBinding{Kind: "AgentRun", Namespace: d.Run.Namespace, Name: d.Run.Name, UID: d.Run.UID, SpecSHA256: d.Run.SpecSHA256}
	d.Agent = cap.SubjectBinding{Kind: "Agent", Namespace: d.Run.Namespace, Name: "agent", UID: "agent-uid", SpecSHA256: fixtureDigest("agent-spec")}
	d.Runtime = cap.RuntimeBinding{Name: "runtime", UID: "runtime-uid", Revision: "v1", SpecSHA256: fixtureDigest("runtime-spec")}
	d.Policy = cap.PolicyBinding{Profile: "fixture-policy", Revision: "v1", Digest: fixtureDigest("policy")}
	d.Tools = []cap.ToolBinding{}
	d.Budget.BudgetID = fixtureDigest("budget/" + d.Run.UID)
	d.RequestDigest = fixtureDigest("request/" + d.Run.UID)
}
