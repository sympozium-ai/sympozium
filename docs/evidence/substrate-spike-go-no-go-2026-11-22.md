# Substrate spike: results and go/no-go (2026-11-22)

For [#699](https://github.com/sympozium-ai/sympozium/issues/699), spike 0b of
[epic #692](https://github.com/sympozium-ai/sympozium/issues/692). How the
backend works is in the [design note](../design/substrate-backend.md).

## Recommendation

**Go to phase 1, with three conditions.** An `AgentRun` runs as a Substrate
actor through the real controller, under shared admission, with the Agent's
key never inside the sandbox, in about 0.43 s against about 11 s for a Job on
the same node. Nothing found in the spike says the design is wrong.

The conditions, in order of weight:

1. **Close the clear-text hop.** Actors reach the model gateway over HTTP
   inside the cluster, because Substrate's egress gateway only trusts public
   certificate roots for upstreams. This is the one exception to "the key
   path has no exceptions", and it blocks a release.
2. **Design the tool path.** Tools were not attempted. They need a tool
   gateway outside the sandbox; an actor has no trusted sidecar.
3. **Repeat on Substrate 1.0.1.** The cluster runs `v1.0.0-rc.1`. The client
   is built from the 1.0.1 API, which is wire-identical, but nothing here
   ran against a 1.0.1 server.

## Against the checkpoints

| # | Checkpoint | Result | Evidence |
|---|---|---|---|
| 0 | Environment reachable | Pass, temporarily | The node's address is restored by hand after a reboot |
| 1 | Control API client | Pass | Live lifecycle; `internal/substrate/client_test.go` |
| 2 | Runner inside an actor | Pass | 20 of 20 turns, [raw](substrate-actor-turns-2026-11-22.json) |
| 3 | Key path | Pass, one exception | [raw](substrate-key-path-2026-11-22.json); the clear-text hop |
| 4 | Backend behind the seam | Pass | `test/system/substrate_test.go`; 63 live runs through the real controller |
| 5 | Tools and tool policy | **Not done** | Needs a tool gateway; see the design note |
| 6 | Identity and attribution | Partly | Actor, UID, worker and active interval on status; one-second resolution; one interval only |
| 7 | Measurements | Partly | Substrate and Job measured; Celln quoted from the roadmap; idle cost for Substrate only |
| 8 | Write-up | This note | #699 not yet updated |

### Checkpoint 3 in detail

| Claim | How it was shown |
|---|---|
| The actor reaches only the turn endpoint and the model gateway | Live: a provider hostname and another in-cluster Service failed DNS; the allowed destination by IP address got a 403 |
| No key in the actor | The turn carries a placeholder; the run's result and logs were searched for the key on every live run |
| The gateway adds the Agent's key | Live: a stub provider that answers only the Agent's key answered 63 of 63 runs; three runs against DeepSeek succeeded with a real key |
| Another actor cannot use the key | Live: an actor that called another run's gateway path got a 401 and no provider call was made |
| The key path closes when the run ends | System test: after completion the same actor gets a 401 |

## Latency

One-shot runs, one node, images cached, a stub provider so the model is out
of the timings. Substrate runs went through the real controller and gateway;
the Kubernetes API was a local test API server, so API round trips are
faster than in a cluster.

| Backend | Runs | Created → Succeeded p50 | p95 | Source |
|---|---|---|---|---|
| Substrate | 20 of 20 | **0.43 s** | 0.66 s | [raw](substrate-controller-runs-2026-11-22.json) |
| Job (Sympozium 0.12.2 on the cluster) | 20 of 20 | **11.3 s** | 11.9 s | [raw](job-backend-runs-2026-11-22.txt) |
| Celln | not re-measured | about 2.5 s | | [roadmap](../roadmap.md) |

Where the Substrate time goes (p50): 59 ms from creation to `Running`,
about 360 ms for resume, the turn and the result, 8 ms to mark the run
`Succeeded`. The actor is confirmed deleted 225 ms later.

Other Substrate figures:

| | |
|---|---|
| First run of an image in a new namespace | 6.0 s, once (golden snapshot) |
| With a real model (DeepSeek, 3 runs) | 0.95 s, 1.27 s, 1.35 s end to end ([raw](substrate-controller-runs-real-model-2026-11-22.json)) |
| 40 runs, 20 in flight | 40 of 40; p50 0.99 s, p95 7.6 s ([raw](substrate-controller-runs-parallel-2026-11-22.json)) |

The 7.6 s tail is the worker pool, not the backend. The pool is two workers
that hold six actors of this size between them, so with 20 runs in flight
most wait for a slot, retried every 2 s. No run failed.

## Idle cost

Six actors at a time, each parked in the runner's polling loop, then
suspended. [Raw output](substrate-idle-cost-2026-11-22.txt).

| State | Worker memory | Storage |
|---|---|---|
| Running, idle | about 25 MiB per actor | |
| Suspended (50 actors) | process memory unchanged from an empty worker | about 0.6 MB per actor |

Suspending takes 0.2 to 0.4 s and resuming a suspended actor 0.23 s.

One caveat. With 50 actors suspended, one worker's working set stayed about
110 MiB above its baseline while its process memory did not move. It
returned to baseline when the actors were deleted, so it was file cache from
snapshot files, not memory an actor holds. A Job pod's or a Celln cell's
idle cost was not measured for comparison.

## Against the thresholds set before the spike

| Threshold | Result |
|---|---|
| Warm-turn overhead no worse than Celln's 2.5 s | Met: 0.43 s |
| A suspended actor holds no worker memory | Met for process memory; file cache is held until the actor is deleted |
| Checkpoint 3 with no exceptions | Not met: the clear-text hop |

## Findings that change the plan

1. **Substrate's egress gateway does not trust a private CA upstream.** Its
   upstream TLS validates against the public root store. An in-cluster
   receiver with a private certificate can only be reached over HTTP, and a
   passthrough rule cannot carry the actor JWT. Options: ask Substrate for a
   per-install upstream trust bundle, give the gateway a publicly trusted
   certificate, or put a mesh on that hop.
2. **Worker drain is ours to handle.** Substrate sends `SIGTERM` to the
   actor and gives it 30 minutes to be suspended; it does not suspend it.
   One-shot runs finish in time. Enduring runs need the runner to pass the
   notice on and the controller to suspend.
3. **Capacity shows up as latency.** A full pool makes `ResumeActor` fail
   until a slot frees. Runs wait rather than fail, but they show `Running`
   while they wait.
4. **Templates are per namespace.** Mapping a namespace to an atespace keeps
   Substrate's tenancy boundary where ours is, at the cost of one golden
   snapshot per namespace per runner image.
5. **The Substrate Go module cannot be imported**, so the API is vendored
   and pinned.

## What was not verified

- Nothing ran against a Substrate 1.0.1 server.
- The Substrate runs used a local test API server, not a cluster's.
- The controller and gateway binaries were not deployed; their wiring
  (`cmd/controller`, `cmd/harness-gateway`) compiles and the same code ran
  in one process in `hack/substrate-e2e`.
- **The NetworkPolicy guard on the clear-text hop.** The framework cluster's
  CNI is flannel, which does not enforce NetworkPolicy, and the controller
  and gateway ran out of cluster, so no NetworkPolicy for the clear-text
  listeners was deployed, let alone enforced. Nothing here shows that other
  workloads are kept off those listeners. Acknowledging the clear-text hop
  (`substrate.allowPlaintextGatewayHop`) is only safe on a CNI that enforces
  NetworkPolicy (for example Calico or Cilium): on flannel anything in the
  cluster can reach the listeners, and nothing confines the hop that carries
  the actor JWTs and prompts.
- The runner's drain-signal handling was added after the measured image was
  built and has not run in an actor.
- No chart values exist for any of this.

## What was left on the cluster

In `ate-system`: a `kind-registry` Deployment and Service, labelled
`sympozium.ai/spike: "699"`, used to load a locally built runner image.
A `sympozium-spike` namespace with a Service and EndpointSlice pointing at
the node, a throwaway Secret and an Agent. In Substrate: atespaces
`symp-smoke`, `symp-spike` and `symp-e2e` with one actor template each and
no actors.
