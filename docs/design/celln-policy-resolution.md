# Celln platform policy resolution

`internal/cellnauthority.PlatformResolver` is the live, secret-free boundary
between tenant workload intent and Celln authorisation. Controllers must give it
their uncached `APIReader`; a cached client is not an acceptable fallback.

For every initial run or enduring turn, the resolver reads the live Namespace,
Agent, namespaced AgentRuntime wrapper, cluster runtime profile, ordered cluster
tool revisions, all applicable CellnExecutionPolicies, AgentRun, optional
AgentRunTurn, and optional namespaced ModelConnection. Tenant resources are
always read from the run namespace. It performs the same load twice and compares
UID, generation, spec digests, protected namespace labels, and the applicable
policy set before returning.

All applicable policies intersect. An absent runtime, tool, lifecycle, route, or
positive model budget denies; omission never means wildcard authority. Numeric
limits take the minimum. Tool ordering remains part of the request binding.
Preview uses this evaluator but returns neither a credential nor an admission or
readiness claim.

The resolver never reads a Secret. For a secret-authenticated model route it
returns only the Secret name/key reference already bound by the ModelConnection
spec digest. The dedicated model gateway must resolve and return the live Secret
UID through `PlatformDecision.FinalizeCredentialSource`; `ReadyForSigning`
refuses the decision until that non-secret UID pin is present. Secret bytes do
not enter the decision.

`PlatformResolution` retains the exact read set and request parameters for
`Revalidate`. Enduring turns must supply the original admitted decision. They
retain its run, runtime, policy, tools, route, budget ID, aggregate ceilings and
parent deadline; a later turn can only receive a shorter turn deadline.

Stable resolver refusals use the #496 vocabulary: `AUTH_POLICY_WITHDRAWN`,
`AUTH_POLICY_CONTRACTED`, `AUTH_TOOL_UNKNOWN`, `AUTH_TOOL_ORDER_MISMATCH`,
`AUTH_ROUTE_MISMATCH`, `AUTH_NAMESPACE_UID_MISMATCH`,
`AUTH_PARENT_TURN_MISMATCH`, `AUTH_LIMIT_OUT_OF_RANGE`, and
`AUTH_LIFECYCLE_INVALID`.
