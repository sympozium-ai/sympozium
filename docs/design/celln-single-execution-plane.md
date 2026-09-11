# Celln: single execution plane

Status: design proposal (no implementation yet).
Goals: one Kubernetes-managed Celln service, one controller client, one install,
one credential model — serving both **one-shot** and **enduring** runs.

---

## 1. Why this document

Today Celln is reachable through two independent stacks:

| | One-shot | Enduring (native parent) |
|---|---|---|
| Controller entry | `reconcilePendingCelln` | `reconcileCellnParent` |
| Client | `internal/controller/celln_transport.go` + `agentrun_celln.go` | `internal/cellnparent/client.go` |
| Base URL | `CELLN_ROUTER_URL` (router Service) | per-run owner `Target` from an approval file |
| Endpoint | `/v1/executions*` | `/v1/parents*` |
| Credential | `CELLN_TOKEN_FILE` (one bearer) | per-run `RunApproval.TokenFile` + CA |
| Deploy | `celln-dispatcher` + `celln-router` Deployments | `celln-parent-controller` Deployment + host `celln dispatcher` owner |
| Install | `cellnInstallSetValues` | `--celln-native` + `celln.nativeParent.*` values |
| Config env | `CELLN_ROUTER_URL`, `CELLN_TOKEN_FILE` | `CELLN_PARENT_CONFIG`, `CELLN_PARENT_REGISTRATIONS` |

This is packaging debt, not a hardware or capability difference.

## 2. Key finding: the binary is already unified

The Celln CLI runs **one subcommand** for both surfaces. `celln dispatcher`
serves:

```
POST /v1/executions                  (one-shot)
GET  /v1/executions/{id}
POST /v1/executions/{id}/cancel
GET  /v1/executions/{id}/audit
POST /v1/artifacts/prewarm
GET  /v1/capabilities | /v1/health | /v1/node

POST /v1/parents                     (enduring)
GET  /v1/parents/{id}
POST /v1/parents/{id}/turns
POST /v1/parents/{id}/stop
POST /v1/parents/{id}/cancel
POST /v1/parents/{id}/turns/{turn}/cancel
GET  /v1/parents/{id}/turns/{turn}
```

`/v1/parents*` is dispatched to `parents::handle`, which authenticates the
bearer against an operator-owned **token-hash file** under the dispatcher's
`--root` (distinct from the one-shot `--token-file`). The "native owner"
systemd unit (`config/host/sympozium-celln-native-owner.service`) is literally:

```
.../celln --root <authority> dispatcher --listen 127.0.0.1:18787 \
  --token-file /etc/celln-native/owner-token --node-name framework-native \
  --mote-store ... --tool-store ... --max-cells 4 ...
```

i.e. the **same `celln dispatcher`**, just a second invocation with a different
root, token, port, and node name.

**Conclusion:** unification is a routing + controller + packaging change. The
Celln engine does not need to become something new; it needs one deployment that
exposes both surfaces, and a gateway that routes both.

## 3. The one real constraint

The two lifecycles have opposite routing properties and both must survive:

| | One-shot | Enduring |
|---|---|---|
| Placement | load-balanced across dispatchers | **owner-affine** — a parent lives on exactly one dispatcher |
| Idempotency | replay-safe by request `id` (durable ledger) | at-most-once create; per-turn idempotent |
| Failure | retry/reroute to another backend | no reschedule; `ContextLost` if the owner is gone |

A single gateway must therefore route `/v1/executions*` by **balancing** and
`/v1/parents*` by **affinity**. This is the core of the design.

---

## 4. Target architecture

```
                          sympozium-controller (one manager)
                                     │  one base URL + one bearer + one TLS policy
                                     ▼
                 ┌───────────────────────────────────────────┐
                 │  celln-gateway  (celln route, extended)    │
                 │   /v1/executions*  → balanced + idempotent │
                 │   /v1/parents*     → owner-affine          │
                 │   durable ownership + parent-affinity ledger│
                 └───────────────────────────────────────────┘
                          │                    │
             ┌────────────┘                    └────────────┐
             ▼                                              ▼
   celln dispatcher (node A)                     celln dispatcher (node B)
   privileged, /dev/kvm, hostPath state          privileged, /dev/kvm, hostPath state
   serves /v1/executions + /v1/parents           serves /v1/executions + /v1/parents
   --root /var/lib/celln (authority/journal/…)   …
```

* One **dispatcher** Deployment-per-KVM-node (today's `celln-dispatcher` shape),
  now also running with the parent authority root so it serves `/v1/parents`.
* One **gateway** (the existing `celln route`, extended) for `/v1/executions`
  (existing behavior) and `/v1/parents` (new: affinity).
* One **controller**: both reconcilers in one manager, one client, one config.
* One **install**: the chart renders the dispatcher + gateway; `--celln-native`
  flips on the enduring lifecycle instead of deploying a separate stack.

---

## 5. Component changes

### 5.1 Celln gateway (`celln route`) — the main Celln-repo change

`router.rs` already:
* forwards `POST /v1/executions` and `POST /v1/artifacts/prewarm`,
* maintains a **durable ownership ledger** (`ownership::Ledger`) binding
  request-id → backend, enforcing at-most-once and anti-replay,
* forwards `GET/POST /v1/executions/{id}*` to the owning backend.

Extend it to a general gateway:

1. Add a **parent-affinity ledger** `parent_id → backend`, durable, in the same
   `--ownership-dir`.
2. `POST /v1/parents`: choose a backend by health/capacity, forward verbatim,
   and on success durably bind the returned `parent id` → backend (mirroring
   `forward_submission`).
3. `GET|POST /v1/parents/{id}*`: look up affinity; forward to that backend; if
   the binding is unknown return `404`, if the backend is gone return the
   dispatcher's `ContextLost`-equivalent.
4. Keep `/v1/capabilities`, `/v1/health`, `/v1/node` aggregate behavior.
5. Parent-affinity conflicts (same id, different backend) must be refused, like
   the execution ledger's `Claim::Conflict`.

This preserves the two routing properties behind one URL.

> Alternative considered: drop the gateway and expose a headless per-node
> Service, with the controller recording the node in run status. Simpler routing
> but pushes placement/affinity into Sympozium and loses the shared durability
> and anti-replay the ledger already provides. Rejected for v1.

### 5.2 Dispatcher deployment — run both surfaces

The chart's `celln-dispatcher` container already runs `celln dispatcher
--root /var/lib/celln`. Required:

1. Provision the **parent authority root** (`authority/`, `journal/`,
   `approvals/`) under the dispatcher's `--root` (or a sibling path), mounted
   from a Secret/ConfigMap + hostPath.
2. Confirm `parents::handle` authorizes against a principal/token-hash file
   present at that root.
3. **Shared capacity accounting**: `--max-cells` / memory/egress budgets must be
   held across both one-shot cells and parent child-cells (one scheduler, one
   node budget), so a busy parent cannot starve one-shot runs and vice-versa.
4. `--unsafe-non-loopback` + NetworkPolicy, as today (or terminate TLS at the
   gateway).

### 5.3 Controller — one client, one path

1. **One client** (`internal/celln`, replacing the split between
   `celln_transport.go` and `cellnparent/client.go`): one base URL, one token
   file, one timeout, one redirect policy, one TLS policy; methods for
   `/v1/executions*` and `/v1/parents*`.
2. **One reconciler wiring**: register `AgentRun`, `AgentRunTurn` and the
   parent/turn reconcilers in the single manager; delete `--celln-parent-only`
   and `cmd/controller/parent_only.go`.
3. **One gate**: keep the "enduring requires celln + cellnSelection + enduring
   limits" validation in `internal/agentexecution/resolve.go`; it no longer
   implies a different subscription, only a different request builder.
4. **One request builder** (`internal/controller/celln_harness.go` /
   `agentrun_celln.go`): translate a resolved run into either a
   `celln.dev/v1alpha*` execution request or a `celln.parent-create/v1` +
   `celln.parent-context/v1` turn — both posted to the same base URL.
5. Remove the `CELLN_PARENT_CONFIG` / `CELLN_PARENT_REGISTRATIONS` env split;
   the approval/registration data becomes dispatcher-side config.

### 5.4 Contracts / CRDs

* `AgentRuntimeCellnProfile.Lifecycle` is currently fixed to
  `disposable-one-shot` (`api/v1alpha1/agentruntime_celln_types.go`). Change to
  accept both lifecycles, e.g. `lifecycles: [disposable-one-shot, enduring]`
  (or relax the enum), so one approved runtime serves both — matching how
  `celln.json-tools/v1` is already shared.
* Keep `celln.json-tools/v1` as the single adapter/tool contract for both
  lifecycles.
* Keep both wire families (`celln.dev/*`, `celln.parent-*`) — they are
  per-lifecycle payloads, not per-deployment. The point is one **transport and
  one deployment**, not one payload version.

### 5.5 Chart + values + install

* `charts/sympozium/templates/celln.yaml`: render dispatcher(s) + gateway.
* **Delete** `charts/sympozium/templates/celln-native-parent.yaml` (the separate
  parent-only controller) and the host `celln-installer` DaemonSet path for the
  default topology; fold their function into the dispatcher.
* `charts/sympozium/values.yaml`: collapse `celln.dispatcher.*` and
  `celln.nativeParent.*` into one `celln` block; keep `router.*` as the gateway.
* `cmd/sympozium/main.go`: `cellnInstallSetValues` should emit one coherent set
  (dispatcher + gateway + authority) and `--celln-native` should mean "enable the
  enduring lifecycle", not "run a second install path". Keep a
  `--celln-host-systemd` escape hatch only for air-gapped bare metal.
* One RBAC identity with both `agentruns` and `agentrunturns` verbs.
* NetworkPolicy must permit parent traffic to the gateway/dispatcher (today it
  only opens 8788 to the router).

---

## 6. Credentials and trust

* **One control-plane principal** presented by the controller. Today the
  dispatcher authorizes executions by `--token-file` and parents by a
  token-hash file; converge on the gateway terminating the controller bearer
  (as the router already does with `--client-token-file`) and forwarding a
  backend token, with parent principals expressed as scoped entries in the same
  operator config.
* **TLS**: terminate at the gateway (or mTLS controller↔gateway) and keep
  gateway↔dispatcher on the cluster network under NetworkPolicy. Retain the
  explicit `allowInsecure` acknowledgement for plaintext topologies.
* **Approval/authority**: `RunApproval`/`CellnParentBinding` data moves from a
  host-only file to a mounted Secret consumed by the dispatcher; the operator
  flow (`celln parent-provision`, `starter-*`) is unchanged in intent but its
  output is delivered to the in-cluster dispatcher rather than a host owner.

---

## 7. Lifecycle, upgrades, durability

* Parents are **pod-lifetime-scoped**: rolling the dispatcher pod stops live
  parents (same guarantee as a host owner restart). Make the Deployment
  `strategy: Recreate` and add a **preStop drain** that refuses new parents,
  stops running parents, and only then exits.
* Dispatcher state (authority, journal, motes, tools, cells) is durable on
  hostPath/PVC, as today.
* A gateway restart must not lose parent affinity (durable ledger).
* No HA/migration for a parent across nodes; `ContextLost` remains the
  contract, now surfaced through the gateway.

---

## 8. Migration plan

1. **Gateway affinity (Celln repo).** Add `/v1/parents*` routing + parent
   affinity ledger to `celln route`. Ship an image that serves both.
2. **Dispatcher config (chart).** Run the existing dispatcher with the parent
   authority root; prove `/v1/parents` works through the dispatcher Service
   directly (controller talks to it as the "owner").
3. **Controller client (Sympozium repo).** Introduce `internal/celln` as the
   single client; repoint the one-shot path at it (no behavior change), then the
   parent path at the same base URL through the gateway.
4. **Collapse controller wiring.** Fold `--celln-parent-only` into the main
   manager; delete the separate registrations env.
5. **Collapse chart + install + values.** Delete the native-parent Deployment and
   the default host installer; make `--celln-native` enable the enduring
   lifecycle on the unified plane; keep a documented escape hatch for bare metal.
6. **Relax the runtime contract.** Expand `AgentRuntimeCellnProfile.Lifecycle`.

Each step is independently shippable and reversible; steps 1–3 deliver the
"single service, single client" outcome without touching the CRDs.

---

## 9. Risks / open questions

* **Capacity fairness.** One budget for one-shot + parents needs an explicit
  policy (reserve N cells for interactive parents?).
* **Parent affinity durability semantics.** What happens when the bound backend
  is replaced during an upgrade — refuse (`ContextLost`) or attempt exact
  node re-placement? v1 proposal: refuse, matching today.
* **Tool/mote stores.** One-shot motes and parent motes currently live under
  different roots; merging must not break the sealed-members/tool-digest model.
* **Authority in-cluster.** Moving the parent authority from a host-only file to
  a cluster Secret changes the trust boundary; needs an explicit threat-model
  review (the host owner deliberately kept model keys out of the cluster).
* **Air-gapped bare metal.** Some operators may need the host-systemd model; keep
  it as a non-default escape hatch rather than deleting it outright.

## 10. Appendix: endpoint inventory

```
executions (one-shot)   POST /v1/executions
                        GET  /v1/executions/{id}
                        POST /v1/executions/{id}/cancel
                        GET  /v1/executions/{id}/audit
prewarm                 POST /v1/artifacts/prewarm
discovery               GET  /v1/capabilities | /v1/health | /v1/node
parents (enduring)      POST /v1/parents
                        GET  /v1/parents/{id}
                        POST /v1/parents/{id}/turns
                        POST /v1/parents/{id}/stop
                        POST /v1/parents/{id}/cancel
                        POST /v1/parents/{id}/turns/{turn}/cancel
                        GET  /v1/parents/{id}/turns/{turn}
```
All are served by a single `celln dispatcher`; today only `/v1/executions*` is
routed, and `/v1/parents*` is reached by a direct, per-run owner address.
