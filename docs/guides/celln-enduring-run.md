# Enduring Celln run contract (development)

This path is under development, not an installed hands-on environment. It uses
a live persistent Harness parent and disposable per-turn child cells. The older
[interactive developer session](celln-interactive-session.md) runs independent
one-shots and does not enable this lifecycle.

AgentRun owns lifecycle. Harness configuration is optional for one-shot work:
direct approved tool invocation does not require a model or conversation. The
initial enduring implementation specifically requires the approved native
parent/turn-worker Harness architecture; arbitrary OCI Harnesses are not supported.

## Explicit intent in YAML

With the new CRDs installed, the intended resource shape is:

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: AgentRun
metadata:
  name: violet-conversation
  namespace: default
spec:
  agentRef: agent
  task: Remember the colour violet for the next turn.
  backend: celln
  executionLifecycle: enduring
  enduring:
    requireToolCall: false
    leaseSeconds: 300
    maxTurns: 4
    maxModelRequests: 12
    maxOutputTokens: 4096
  model:
    provider: deepseek
    model: deepseek-chat
  cellnSelection:
    runtimeRef: approved-parent-runtime
    toolRefs: []
```

The runtime name is illustrative, not a shipped or automatically admitted runtime.
Limits are requested aggregate ceilings, not grants. `maxTurns` includes the
initial message. An empty tool list lends no tools. Initial and subsequent
messages are bounded to 2048 UTF-8 bytes without NUL; context capacity can impose
a further bound. This is live context retention, not crash-resumable persistence.

Optional `enduring.requireToolCall: true` requires a fresh execution of at least
one selected tool on every turn before it can report success. It does not force
a specific tool or require all tools, and does not lend anything implicitly.
Select at least one tool and provide model request/output budgets. The approved
native template and parent registration must agree with this value exactly;
existing optional-tool templates cannot be reused for required-tool runs.
The creation form exposes the same opt-in. Leaving it unchecked preserves
optional tool use, including conversations that need no tools on some turns.

The run-create HTTP API accepts the same `executionLifecycle` and `enduring`
fields alongside its existing `agentRef`, string `task`, `backend`, `provider`,
`model` and `cellnSelection` fields. Its model selection fields are top-level,
unlike the nested `spec.model` in YAML. A 201 response only confirms resource
creation. The browser client does not automatically retry enduring creation on
network failure; check the run list before creating another run.

The API also accepts optional `systemPrompt`, mapped to `spec.systemPrompt`.
Prepared parent registration must match this explicit persona exactly; a host
template's nonempty system prompt is not implicitly substituted for empty intent.

## Creation form (development request)

In **Runs → New Run**, select the Agent, native Harness and Celln backend.
Select **Enduring conversation (development — operator approval required)**
and enter aggregate ceilings. **Request enduring run** submits explicit lifecycle
intent; it does not establish that the selected runtime has an approved parent
implementation. The one-shot permission preview is hidden because it cannot
attest to parent execution. Unchecking the option submits the existing one-shot
request without enduring fields.

The enduring form also accepts an optional Harness system prompt. It is sent as
explicit intent and must exactly match the prepared parent registration. Opting
out omits this parent-specific field along with the enduring lifecycle and limits.

If creation is unconfirmed, the form disables another submission. Inspect the
run list before trying again; this creation guard lasts only for the mounted
page, unlike the per-turn request identity retained in session storage. Durable
idempotency for initial run creation remains future work.

## Admission and conversation

The controller requires `CELLN_PARENT_CONFIG` pointing to trusted operator
configuration. Each approval binds the created run's namespace, name, Kubernetes
UID and exact spec digest to a specific host target, principal, launch profile
and parent incarnation. Neither YAML nor browser input can supply this authority.
Optional `CELLN_PARENT_REGISTRATIONS` connects prepared-registration admission
to controller startup. In this mode `CELLN_PARENT_CONFIG` must be the exact
approval directory configured below. The registration configuration is a bounded
JSON file with version `sympozium.ai/celln-parent-registrations-v1`, absolute
`journal` and `approvals` directory paths, three distinct `operatorSource`,
`runtimeSource` and `agentSource` objects (each with `namespace` and `name`), and
a `registrations` array of operator-prepared `ParentLaunchRegistration` records.
Those records bind the complete resolved selection digest, model, persona, host
limits and one prepared launch/incarnation; they contain credential file paths,
never credential contents. Tenant requests cannot provide this configuration.

All controller replicas consuming a registration pool must share its durable
operator-owned journal and approval directory. These directories must already
exist and support the non-replacing, synced publication primitive. Protect the
configuration, journal, approvals and all grant ConfigMaps from tenant writes.
Admission rereads registrations and live grants for each unbound run, requires
one exact candidate, and pins that choice before publication. Missing, ambiguous,
consumed or changed choices refuse without dispatch. Configuration routing cannot
change in a running dispatcher; restarting must not discard or replace its journal.

Removing a registration prevents new admission; it is not cancellation of an
already-bound parent. Such parents retain their original identity and use the
existing stop/delete path. Automatic preparation of fresh host launch profiles
and permits is still outstanding. Do not reuse the one-shot issuer as if it
grants parent execution.

Once that approval has actually initialized the parent and committed its initial
turn, the run-detail page shows the persistent conversation and enables the next
message. Each submission creates a data-only AgentRunTurn; results appear through
history polling. An unconfirmed submission retains its original request identity
in session storage across reload, disables another send, and is not automatically
resubmitted. Context loss disables sending even when historical answers remain.

Automated admission and a deployed browser journey through the real controller
and Celln host remain unfinished. Intercepted
browser tests and fake-client API tests do not prove that end-to-end path.
