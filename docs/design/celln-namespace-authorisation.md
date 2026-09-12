# Celln namespace authorisation and cross-repository conformance contract

Status: **v1 contract for #495 / #496; contract only, not runtime implementation.**

This document refines #494. It defines the authority and lifecycle invariants that
#497–#510 must implement. It intentionally does not claim Celln, the controller,
the model gateway or the durable ledger already implement this protocol.

## 1. Target outcome

Install the Celln execution plane once. A workload in any **policy-authorised**
Kubernetes namespace can use direct one-shot, Harness one-shot or enduring native
Harness execution without a per-namespace native install, copied grant ConfigMaps
or prepared parent registrations.

The security model has four separate boundaries:

1. **Resolver/issuer** — reads live Kubernetes intent and operator policy, creates
   an immutable decision, and signs audience-scoped credentials.
2. **Celln gateway/dispatcher** — independently verifies execution credentials,
   host artifact/publisher/ABI constraints, durable ownership and owner affinity.
3. **Dedicated model gateway** — verifies model credentials, revalidates the exact
   namespaced route/credential source, reserves durable allowance, and injects the
   provider key. Provider credentials do not enter the guest or Celln host config.
4. **Durable budget ledger** — preserves run ceilings, per-turn ceilings,
   idempotency and terminal fences across token refreshes and process restarts.

A standing controller transport bearer is not workload execution authority.

## 2. One decision, multiple scoped credentials

A `CellnAuthorisationDecision` represents **one externally requested unit of work**:

- one-shot run (`execution.start`),
- enduring parent creation (`execution.start`, lifecycle `enduring-initial`),
- one enduring turn (`execution.turn`), or
- owner read/cleanup (`execution.read` / `execution.cleanup`).

A model-using start/turn decision can yield two credentials derived from the
**same decision digest**:

- audience `celln-execution`, operation `execution.start` or `execution.turn`;
- audience `sympozium-model-gateway`, operation `model.invoke`.

`model.invoke` is therefore **not a second independent authorisation decision**.
The model gateway receives a narrower credential derived from the admitted
start/turn decision. This avoids two independently evolving authority planes.

Every verifier receives trusted endpoint context: `expectedAudience` and
`expectedOperation`. It never derives the expected endpoint solely from fields
inside the presented credential.

## 3. Decision identity

The normative JSON schema is
`test/fixtures/celln-authorisation/v1/schema/decision.schema.json`.

A decision binds at least:

- cluster identity;
- namespace name + namespace UID;
- AgentRun name + UID + spec digest;
- Agent / AgentRuntime identities and spec digests;
- operator policy identity/digest;
- ordered runtime/tool artifact identities;
- bounded artifact and HTTPS broker permissions;
- lifecycle and parent incarnation / turn identity;
- exact model route and credential-source identity;
- stable run budget ID, aggregate caps, per-turn caps and `maxTurns`;
- fixed parent deadline and turn/work deadline;
- issued/not-before/admission window;
- **digest of the actual external execution/turn request**.

Same-name object recreation is a different identity. A new credential cannot
retarget an existing run to a different namespace UID, ModelConnection, Secret
UID, parent incarnation, runtime or tool revision.

### Actual request binding

`requestDigest` is SHA-256 of the canonical, versioned execution/turn request
received by Celln, not a self-hash of the decision.

For a turn, the bound request contains the run UID, parent incarnation, turn ID
and turn payload. Changing the message while retaining the same turn ID must
fail `AUTH_REQUEST_BINDING_MISMATCH`.

Model request bodies are generated later by the Harness and are **not** predicted
or pre-signed by the controller. Each outbound model attempt instead gets a
host-generated request ID and canonical request-body digest at durable
reservation time.

## 4. Canonical encoding

Decision and request bytes use RFC 8785 JSON Canonicalization Scheme semantics.
This v1 contract deliberately restricts all JSON numbers in the signed structures
to JSON-safe **integers**. Floats, exponent notation and negative zero are invalid.

The Go fixture implementation performs strict I-JSON validation before
canonicalisation:

- duplicate object keys reject;
- trailing JSON rejects;
- invalid UTF-8 rejects;
- lone UTF-16 surrogate escapes reject;
- numbers outside the integer-only profile reject;
- object keys are sorted by UTF-16 code units and strings use the RFC 8785 JSON
  escaping rules.

The shared bundle contains exact canonical bytes and hashes. The future Rust
consumer must reproduce those bytes independently before the contract is treated
as cross-repository implemented. Agreement between generator and verifier alone
is not a cross-language proof.

`decisionDigest = sha256(canonical decision bytes)`.
`requestDigest = sha256(canonical external request bytes)`.
Celln artifact identities continue to use their native `blake3:` identifiers;
these hash families are typed and are never compared as though equivalent.

## 5. Credential contract

Credentials are compact Ed25519 JWS with a fixed issuer and configured `kid`.
The protected header permits only `alg`, `typ`, and `kid`; remote key URLs and
embedded key material are forbidden. Payload parsing rejects duplicate and
unknown fields.

Claims bind:

- issuer and single audience;
- issued/not-before/expiry;
- unique credential `jti`;
- decision digest;
- stable budget ID;
- operation;
- run UID, parent incarnation and turn ID.

A `jti` identifies **a credential**, not an execution, turn, provider request or
budget. Durable operation/request IDs provide idempotency.

Fixture signing keys are explicitly non-production test material.

## 6. Lifecycle-specific verification

### New start / turn admission

Before issuance, the resolver re-reads live policy and pinned Kubernetes object
identities. Failure or contraction refuses **new authority**.

An issued execution permit has a default 60-second admission window and at most
5 seconds verifier skew. It may still be admitted during that bounded window if
policy is removed *after issuance*. This is deliberate bounded revocation, not
instantaneous revocation.

The execution credential expires no later than the admission window. Duplicate
admission of the same operation identity is a **recovery** path, not permission
to execute again.

### Admitted model work

After a turn/start is admitted, model operations use the derived
`sympozium-model-gateway` credential. The original admission window is no longer
checked. Model work is instead bounded by:

- credential expiry;
- the immutable turn/work deadline;
- live route/credential-source identity at the model gateway;
- the durable run and turn allowance;
- closing/cancellation fences.

Thus a model call at T+70 is valid when the execution was admitted before T+60
and the fixed work deadline is T+120.

### Read and cleanup

Read/cleanup authority is owner-scoped and distinct from permission to start new
work. Policy withdrawal or expiry of execution permission cannot strand a known
parent. A fresh, short-lived owner cleanup credential may stop/cancel the exact
recorded owner after policy withdrawal. It cannot create a cell, turn or model
request.

## 7. Enduring lifecycle

An enduring run has one actual parent/incarnation and one active turn at a time.
The initial task counts toward `maxTurns`. Every externally accepted turn has:

- a unique durable turn identity;
- its own decision and external-request digest;
- a fresh execution credential;
- a derived model credential when the route is model-using;
- a fixed turn deadline;
- per-turn allowance that is also constrained by the original run allowance.

New turns do not extend the original parent deadline, change model/runtime/tool
identity or refill aggregate allowance. Parent loss is `ContextLost`; no silent
recreation or transcript-only recovery is authorised.

## 8. Broker permissions

The decision preserves the existing Celln distinction between executable tool
identity and per-run broker authority. Tool limits include:

- CPU/memory/time/input/output ceilings;
- `workspace: none` (no host mount authority);
- bounded run-owned artifact read/write quotas;
- bounded credential-free HTTPS GET destinations/request/body/deadline limits;
- side-effect classification.

`workspace: none` does **not** mean the separately brokered artifact capability
is absent. Artifact and HTTPS authority only shrinks through policy intersection.

## 9. Model route and credential source

A model route binds provider, protocol, model, endpoint origin, ModelConnection
UID/spec digest, auth mode and (for secret-backed routes) Secret UID/name/key.
The resolver does not read Secret contents.

The dedicated gateway pins and revalidates the actual source identity. Secret
data may rotate within the same Secret UID; deleting/recreating the Secret or
changing route identity does not retarget an active run.

No-auth local routes are explicit (`auth: none`) and still require operator
endpoint/model permission. A secret-backed route cannot silently become no-auth.
Initial v1 mediation is non-streaming `openai-chat` and `anthropic-messages`.

## 10. Durable model accounting and idempotency

The trusted issuer registers immutable run/turn ceilings. A model credential
cannot create or increase a budget row.

Before provider contact the gateway atomically reserves:

- one request;
- the permitted output-token ceiling;
- under a host-generated `(turnID, requestID, requestDigest)` identity.

Rules:

- same request ID + same digest => recover original reservation/outcome;
- same request ID + different digest => `AUTH_REQUEST_ID_CONFLICT`;
- a new credential/JTI does not reset allowance;
- a new turn gets a fresh per-turn counter but shares the original run counter;
- committed reservations are non-refundable in v1;
- ambiguous provider outcomes are not automatically replayed.

The shared sequence fixture demonstrates two calls in turn 1, recovery of a
duplicate, conflict on changed content, one call in turn 2, and aggregate-run
exhaustion on the next call.

## 11. Resolver versus verifier responsibilities

| Check | Owner |
| --- | --- |
| live namespace/policy/object identity and policy intersection | resolver/issuer |
| ordered runtime/tool and broker-limit contraction | resolver + independent host ceiling checks |
| signature, trusted `kid`, issuer, expected audience/operation | receiving verifier |
| external execution/turn request digest | Celln admission boundary |
| parent/turn/owner binding | Celln + durable ownership journal |
| live model route/Secret source identity | model gateway |
| model request idempotency and cumulative allowance | durable ledger |
| cleanup ownership after policy withdrawal | Celln owner journal + cleanup verifier |

A verifier does **not** re-run the resolver's live policy test for an already
issued permit. Conversely, the issuer does not make a Celln host trust arbitrary
runtime bytes: Celln continues to verify publisher/artifact/ABI/host ceilings.

## 12. Stable refusal/disposition vocabulary

Relevant contract outcomes include:

- `AUTH_REQUEST_BINDING_MISMATCH`
- `AUTH_AUD_MISMATCH`
- `AUTH_OPERATION_MISMATCH`
- `AUTH_ADMISSION_WINDOW_EXPIRED`
- `AUTH_WORK_DEADLINE_EXPIRED`
- `AUTH_WINDOW_INVALID`
- `AUTH_BUDGET_MISMATCH`
- `AUTH_BUDGET_EXHAUSTED`
- `AUTH_REQUEST_ID_CONFLICT`
- `AUTH_POLICY_WITHDRAWN`
- `AUTH_POLICY_CONTRACTED`
- `AUTH_TOOL_ORDER_MISMATCH`
- `AUTH_ROUTE_MISMATCH`
- `AUTH_NAMESPACE_UID_MISMATCH`
- `AUTH_PARENT_TURN_MISMATCH`
- `AUTH_ADMISSION_REPLAY` (recovery disposition, not second execution)
- `AUTH_STREAMING_UNSUPPORTED`
- `AUTH_PROTOCOL_UNSUPPORTED`
- `AUTH_CONTEXT_LOST`
- `AUTH_CLEANUP_PENDING`

HTTP mapping belongs to the implementing endpoint; the semantic reason is the
stable cross-boundary contract.

## 13. Shared fixture bundle

`test/fixtures/celln-authorisation/v1/` contains:

- `manifest.json` — authoritative, non-empty required vector/sequence inventory;
- `cases.json.gz` — gzip-compressed compact decisions, canonical bytes/digests, signed credentials,
  observed endpoint context, resolver cases and stateful accounting sequences;
- `schema/*.json` — decision and credential claim schemas;
- `signing/*` — deterministic non-production Ed25519 vectors;
- `bundle/SHA256SUMS` — every normative file except the two bundle outputs;
- `bundle/BUNDLE.sha256` — SHA-256 of `SHA256SUMS`, the external bundle pin.

Unlike the first draft, `manifest.json` itself is included in `SHA256SUMS`.
Removing test inventory therefore changes the bundle. The verifier also carries
an explicit required-case list and rejects empty, duplicate or missing coverage.

Generator and verifier:

```bash
go run ./cmd/celln-authorisation-fixture gen \
  -fixtures test/fixtures/celln-authorisation/v1

go run ./cmd/celln-authorisation-fixture verify \
  -fixtures test/fixtures/celln-authorisation/v1
```

Both command forms parse `-fixtures` after the subcommand.

## 14. Current implementation gaps (not resolved by this contract PR)

The contract deliberately records implementation gaps for downstream work:

1. current Celln uses static bearer/token-hash authentication, not scoped JWS;
2. current Sympozium Celln transport has no audience split;
3. current runtime hashes are not this cross-repository canonical decision format;
4. decision/budget SHA-256 and Celln artifact BLAKE3 identities remain distinct;
5. there is no durable PostgreSQL model budget ledger yet;
6. there is no dedicated model gateway yet;
7. current parent admission does not verify this per-turn decision/request binding;
8. live deleted/recreated Secret-UID revalidation is not implemented;
9. explicit no-auth local route semantics are not implemented;
10. fixture keys remain test-only and must never be cluster trust defaults.

These are implementation tasks for #497–#510. They are not reasons to weaken the
contract or claim the current plane already satisfies it.
