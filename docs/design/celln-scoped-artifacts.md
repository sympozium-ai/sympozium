# Scoped enduring artifacts: paired implementation contract

This implements the additive contract proposed in Celln PR #153, pinned at
`98994ffcdad973b6757d4c4449e3bb728127d642`, for Sympozium #495/#496/#510.
This pin includes scoped read-only capability discovery without dispatcher-wide
execution authority. Recorded two-tenant guest-backed development qualification
and its limitations are in
`test/integration/celln-installed-qualification/artifacts/README.md`.
It does not close those issues or certify A01–A12. This branch is stacked on
Sympozium #635 (`test/celln-enduring-context-0924`), which itself depends on #634.
No CRD, credential audience, or workspace enum changes (v2 below widens the
decision schema's artifact operation enum only).

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

## v2: the full starter toolbox on mediated routes

Mediated model access is the default install, so mediated Agents now get the
same starter toolbox as fleet-keyed ones. Two additive contracts, negotiated
the same fail-closed way, extend the v1 rules above (which still hold for a
decision a v1 node can serve):

- **`celln.scoped-artifacts/v2`** (`scopedArtifactContracts`): `operation` may
  be any of `read`, `write`, `list`, `append`, `search`, `delete`, for
  `one-shot` as well as enduring runs. Effects pair exactly: `write`,
  `append`, `delete` require `external-side-effects`; `read`, `list`, `search`
  require `none`. The node grants exactly the named operations (read never
  implies list/search; write never implies append/delete). Enduring runs keep
  the v1 owner-bound, per-parent store, minimum-intersected caps and pinned
  continuation policy. A one-shot run gets a private, empty store owned by its
  broker and dropped with its cell; nothing persists past the run.
- **`celln.scoped-https/v1`** (`scopedHttpsContracts`): the signed
  `tools[].limits.https` (`allowHosts` exactly `["*"]` or 1–16 lowercase DNS
  names, `maxRequests` 1–16 per turn, `maxResponseBytes` 1–4096,
  `timeoutMillis` 1–30000 and within the tool's timeout, effects
  `external-side-effects`) for one-shot and enduring runs. The tool's signed
  closure entry point selects the broker method (`/https-fetch` GET,
  `/https-post-json` POST, body at most min(argumentBytes, 4096)); signed
  limits may only attenuate the material's. Enforcement is Celln's fleet
  broker egress (`Reach::Tool`): public IPv4 HTTPS on 443, pinned DNS, every
  redirect hop re-authorised, POST never redirected, separate GET/POST budgets
  that never buy model requests. A route's `allowInsecure` never reaches a tool.
  Enduring parents attach these grants per reserved turn after the model-only
  check.

The resolver no longer narrows mediated routes beyond the catalogue and
policy (`validToolLimits`, `intersectBrokerLimits`), except that a model-free
route cannot carry brokered tools. `PreflightArtifacts` requires exactly what
the decision needs (`RequiredContracts`): v1 for enduring read/write only, v2
for any other operation or a one-shot run, and the HTTPS contract for any web
tool. An older node refuses with `AUTH_PROTOCOL_UNSUPPORTED` before gateway
pinning or native preparation.

The authorisation decision schema (`test/fixtures/celln-authorisation/v1`,
regenerated by `go run ./cmd/celln-authorisation-fixture gen`; bundle pin
`sha256:5fb2bd011d3d9451cac80747996c166a811d7fc15b54ebe76da3c9934fad4aa3`)
now admits the six operations; only the schema changed, every case is
byte-identical. Celln vendors the same bundle.

Shared fixtures, byte-identical in Celln and pinned by hash in Go:
`test/fixtures/scoped-artifacts/v2.json`
(`a9742c88087311e1533877863b575651b4af9156862ab97b70ddc8ae54305e49`, 23
vectors) and `test/fixtures/scoped-https/v1.json`
(`f4048c68ab946d0548da2688cd396b28608c1acee29265a0919fa96381aea566`, 19
vectors). v1.json is unchanged.

The installer's starter Agent and the console's Celln Agents select the
scope's lent tools (`cellnplatform.LentTools`) as `clusterToolRefs`, like a
fleet wrapper Agent.
