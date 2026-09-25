# Scoped enduring artifacts: paired implementation contract

This implements the additive contract proposed in Celln PR #153, pinned at
`98994ffcdad973b6757d4c4449e3bb728127d642`, for Sympozium #495/#496/#510.
This pin includes scoped read-only capability discovery without dispatcher-wide
execution authority. Recorded two-tenant guest-backed development qualification
and its limitations are in
`test/integration/celln-installed-qualification/artifacts/README.md`.
It does not close those issues or certify A01–A12. This branch is stacked on
Sympozium #635 (`test/celln-enduring-context-0924`), which itself depends on #634.
No CRD, decision wire schema, credential audience, or workspace enum changes.

## Authority and supported scope

Only mediated `enduring-initial`/`enduring-turn` execution supports this feature.
Native runtime/profile/tool workspace remains `none`. A logical artifact store
is owned by one live native parent, not mounted into the guest and not a host
filesystem. Persistence is across that parent's turns, **not process restart**.
Owner loss remains ContextLost. There is no arbitrary path, executable write,
HTTPS capability, restart recovery, or legacy broker fallback implied here.

Explicitly select the signed minimal `/worker` runtime source plus independently
reviewed `workspace-read` and `workspace-write` source closures. Do not lend the
monolithic legacy worker bundle. Ordered tool identity includes the original
name/revision, UID, executable, closure, schema, ABI, publisher and limits.
Continuation preparation reads the original protected preparation and refuses
changed ordered tool material, including changes not visible in executable hash
alone. Original owner, policy, runtime, route, deadlines and budgets remain pinned.

The existing `limits.artifacts` object contains exactly:

| Field | Constraint |
|---|---|
| operation | `read` or `write` |
| maxOperations | 1–64, aggregate attempts per turn across both tools |
| maxFiles | 1–256, parent-wide retained files |
| maxFileBytes | 1–4096 UTF-8 bytes |
| maxTotalBytes | maxFileBytes–1048576, parent-wide retained bytes |

Read effects must be `none`; write effects must be `external-side-effects`.
Broker tools require `celln.json-stdio/v1`; artifact and HTTPS declarations are
mutually exclusive. No implicit write grant follows from read authority.
Celln verifies signed ceilings do not exceed material ceilings and takes the
minimum of **all selected artifact tools**, not their sum. The capability is
cell-wide; it is not per-process isolation between tools inside one cell.

## Policy omission versus attenuation

A policy must explicitly select the exact catalogue tool name and revision.
Its **nil overall `limits`** retains that immutable catalogue revision's ceiling,
matching existing policy semantics. This does not select an omitted tool.
An **explicit `limits` object** must retain the same declared broker operation;
an absent artifact capability or changed operation refuses the request rather
than silently dropping the capability and admitting an unusable tool. Numeric
ceilings intersect by minimum; catalogue material is never mutated. Removing a
selected tool or changing the effective pinned policy requires a new run.

## Fail-closed negotiation

The scoped dispatcher queries authenticated `GET /v1/capabilities` and requires
`scopedArtifactContracts` to contain exactly the supported value
`celln.scoped-artifacts/v1`. A missing/invalid response or unsupported lifecycle
returns `AUTH_PROTOCOL_UNSUPPORTED` before model-gateway credential pinning or
native preparation. The check is repeated for fresh admission and continuation;
it is not a cached readiness claim. Non-artifact requests retain their existing
path. Native admission independently enforces configuration, signed authority,
source composition, KVM and resource constraints after negotiation.

The response is bounded and rejects malformed/duplicate JSON, while allowing
unrelated additive capability fields. Cleanup/read remain separate owner-bound
operations and do not depend on a fresh artifact capability advertisement.

## Shared fixture and verification

`test/fixtures/scoped-artifacts/v1.json` is copied byte-for-byte from Celln's
`tests/fixtures/scoped-artifacts/v1.json`. SHA-256:
`d8d8dc6a3505d517d515bc492d86996cd5e3f907e8c9718d3195ecf20c14c800`.
The Go consumer pins the hash and executes all 15 signed/material vectors,
including widening, wrong operation, invalid ranges/types and unknown fields.
Resolver tests independently exercise nil policy ceilings, explicit attenuation,
omission refusal, ABI/effects and one-shot refusal.

```sh
GOMAXPROCS=2 go test -mod=readonly -p 2 -race \
  ./internal/cellnauthority ./internal/cellnscoped ./internal/cellncapability \
  ./internal/controller ./test/integration/celln-installed-qualification/...
```

The preexisting generic resolver fixture previously declared a write artifact
with `effects:none`, even in model-free one-shot tests. It now uses a broker-free
tool so those unrelated route/identity tests remain valid. Dedicated tests cover
the actual artifact authority; the rejection rule was not weakened.

Installed qualification is a separate deterministic development tier. It must
assert genuine guest tool replies, two tenant sentinels, one stable parent each,
three distinct children/cells, independent provider counters, closed ledgers and
native teardown. Scripted assistant text alone is insufficient. Browser,
real-provider, normal operator installation, migration and the full adversarial
release matrix remain separate gates; keep `installedAcceptance:false`.
