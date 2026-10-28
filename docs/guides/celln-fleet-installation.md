# Celln fleet installation

The Celln fleet is KVM isolation for agents: every node labeled
`celln.dev/kvm=true` boots micro-VMs ("cells") for one-shot and enduring
runs from any authorised namespace. It holds **no model**. Nobody declares a
provider or a key at install time; each Agent brings its own
`ModelConnection` and key, and the model gateway adds that key to the
Agent's requests. How that works, and what protects the key, is
[Agent-bound model access](celln-mediated-model-access.md). This guide covers
installing, verifying, upgrading and operating the fleet.

## The fleet is privileged, and the install says so

`sympozium install` prints this before it deploys anything:

```text
  Celln fleet: on every node labeled celln.dev/kvm=true this deploys two privileged DaemonSets
  (celln-node, celln-node-configure) that mount /dev/kvm, /boot, /lib/modules and a state directory
  under /var/lib/sympozium-celln, because booting micro-VMs needs them. Nodes hold no model key:
  every Agent's model requests leave through the model gateway with that Agent's own key.
  Pass --no-celln to install without the fleet.
```

That is the whole host footprint: `privileged: true` on the two DaemonSets,
`/dev/kvm`, a read of `/boot` and `/lib/modules` for the guest kernel, and
`/var/lib/sympozium-celln/<scope>` for node state. Nothing mounts the host
root, nothing uses `hostPID`, and no other component is privileged. The
router, the model gateway and the controller run unprivileged on any node.

## What gets deployed

| Component | Where | Role |
| --- | --- | --- |
| `celln-node-configure` DaemonSet | `celln-system`, every `celln.dev/kvm=true` node | Pulls the digest-pinned starter package, verifies it against the approved hash and publisher, runs `starter-admit` and a model-free `starter-configure`, publishes the result as ConfigMap `celln-fleet-configuration`, reports cells to `celln-fleet-cells` |
| `celln-node` DaemonSet | same nodes | The owner: the dispatcher that boots parent and child cells and serves the scoped receiver every run goes through |
| `celln-router` Deployment | `celln-system`, any node | Discovers owners by DNS, binds each parent to the owner that holds it, and (through the `celln-scoped-receiver` Service and its TLS sidecar) forwards each scoped operation to the node that prepared it |
| `<release>-model-gateway` Deployment | `sympozium-system` | Adds each Agent's own key to its model requests; the only component that reads key Secrets. Its budgets and registrations live in a bundled single-pod PostgreSQL (`<release>-model-gateway-db`) unless you bring your own |
| Controller wiring | `sympozium-system` | Signs one permit per run against the bootstrapped issuer key; the node dispatchers and the gateway verify it against the same public keyset |
| Cluster-scoped catalogue | — | `CellnRuntimeProfile` `celln-native-<scope>` and `celln-native-<scope>.toolbox`, one `ClusterCellnTool` `celln-<scope>-<tool>` per packaged tool, and `CellnExecutionPolicy` `celln-fleet-<scope>` with the scope's ceilings |
| Namespace wrappers | each authorised namespace, on first use | `AgentRuntime` `celln-native` and `celln-native.toolbox`, binding the profiles. Nothing else: no shared Agent, no shared connection |

The installer also deploys [ergoz](https://github.com/sympozium-ai/ergoz)
(accelerator power telemetry, pinned in `config/ergoz/release.json`) into
`ergoz-system`; `--no-ergoz` skips it.

## Prerequisites

1. **A released `sympozium` binary.** A release pins the starter package the
   project built and signed for it (`hack/build-celln-starter.sh`; the
   identity is the `celln-starter.json` release asset) and the model gateway
   image digest. With both pinned, a bare `sympozium install` deploys the
   fleet. A build from source pins neither: it needs `--celln-fleet` with
   `--celln-fleet-package-image`, `--celln-fleet-package-hash` and
   `--celln-fleet-publisher`, plus `--model-gateway-image
   ghcr.io/sympozium-ai/sympozium/model-gateway@sha256:<digest>`, or
   `--no-celln`.
2. **Nodes with KVM.** Each cell is a micro-VM booted from a kernel image on
   the node, so a node needs `/dev/kvm`, a kernel under `/boot` and its
   `/lib/modules`. The node probe (`nodeProbe.labelKVMNodes`, on by default)
   labels such nodes `celln.dev/kvm=true`; a label an operator set (either
   value) is never changed, so `celln.dev/kvm=false` keeps a node out. A
   cluster with no such node installs the same thing and has no Celln
   capacity; `sympozium doctor` says so.
3. **Kind is a development environment only.** On a Linux host, Kind nodes
   see `/dev/kvm` and mount the host's `/lib/modules` but ship no `/boot`, so
   nothing gets labelled and the install waits for a node that never
   qualifies. Copy the host's **running** kernel into each node; the probe
   labels it within seconds:

   ```sh
   docker cp /boot/vmlinuz-$(uname -r) kind-control-plane:/boot/   # and each worker
   ```

   Kind on macOS or Windows has no `/dev/kvm` and cannot run the fleet. The
   installer prints this hint when no node has qualified within the first
   45 seconds of its wait.
4. **A default StorageClass**, for the gateway's bundled PostgreSQL (1 Gi
   PVC). To use your own database instead, see
   [PostgreSQL](celln-mediated-model-access.md#postgresql).
5. **An enforcing CNI** if you rely on the rendered NetworkPolicies.

## Install

```sh
sympozium install
```

Nothing about models is asked. In order, the command:

1. decides the gateway image (`--model-gateway-image`, `--set
   modelGateway.image`, the release's pin, or the one already deployed) and
   reads the existing trust's key id, or chooses a new one;
2. fills the package identity, scope (`starter`) and output directory
   (`~/.sympozium/celln-fleet/<scope>`) from the build's defaults, sizes the
   lifetime output-token ceiling (see [Leases and budgets](#leases-and-budgets))
   and checks that this install would not move an installed fleet to another
   package without `--celln-fleet-replace-package`;
3. prints the privilege notice above and installs the chart with the
   `celln.fleet.*` values, the derived `celln.mediation.*` values and
   `modelGateway.image`;
4. publishes the parent principal (`celln-router-parent` and the hash-only
   client policy every owner installs) and bootstraps or verifies the
   mediation trust (issuer key, two bearer tokens, a private CA and the
   gateway and receiver certificates; cluster id = the `kube-system`
   namespace UID);
5. waits (default 15 minutes, `--celln-fleet-wait`) for the first labeled
   node to admit the package and publish the starter configuration, then
   for the controller rollout;
6. installs the catalogue: the scope's profiles, tools and policy, and the
   runtime wrappers in the `-n` namespace (`default` unless you pass one).

It ends with:

```text
  Model access: every Celln Agent brings its own connection (any OpenAI- or Anthropic-compatible endpoint) and its own key; the key is pinned to that endpoint's origin and never reaches a node. Narrow where keys may go with allowedOrigins/deniedOrigins on the scope's CellnExecutionPolicy; approve a private (LAN) endpoint once with modelGateway.privateOrigins.
  Enabled Celln runs on fleet "starter" for every namespace except the system exclusions and namespaces labeled celln.sympozium.ai/excluded; default carries the wrapper objects and any other namespace gets them on first use. No run submitted.
```

The command is safe to rerun: trust, principal, configuration and
installation records are verified, never replaced, and a rerun with the same
package keeps every live conversation (`Celln fleet package unchanged (…);
live conversations are kept`). If the wait expires, label a node, read the
`celln-node-configure` log in `celln-system`, and rerun the same command.

### Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--no-celln` | `false` | Install without Celln (no fleet, router or gateway) |
| `--celln-fleet` | on when the build pins a package | Deploy the fleet explicitly; needed, with the three package flags, for your own package or a source build |
| `--celln-fleet-package-image`, `--celln-fleet-package-hash`, `--celln-fleet-publisher` | the release's starter package | A reviewed, digest-pinned package (`repository@sha256:…`, the `blake3:` hash and publisher key from `celln starter-inspect`) |
| `--celln-fleet-scope` | `starter` | Stable installation identity; node state lives at `/var/lib/sympozium-celln/<scope>` |
| `--celln-fleet-principal` | `sympozium:celln` | Parent principal every owner authenticates |
| `--celln-fleet-authorise` | `all` | `all`: every namespace except `kube-*`, `cert-manager`, the control-plane namespaces and namespaces labeled `celln.sympozium.ai/excluded`; `labeled`: only namespaces labeled `celln.sympozium.ai/scope=<scope>` (the `-n` namespace is labeled for you) |
| `--celln-fleet-https-host` | any public host | Restrict the `https-fetch`/`https-post-json` tools to these exact lowercase hosts (repeatable); private addresses are always refused |
| `--celln-fleet-max-lease-seconds`, `-max-turns`, `-max-model-requests`, `-max-output-tokens` | 86400 / 256 / 1536 / sized | Scope ceilings for every parent; see [Leases and budgets](#leases-and-budgets) |
| `--model-gateway-image` | the release's pin | Digest-pinned model gateway image; required on a source build |
| `--celln-fleet-replace-package` | `false` | Approve moving an installed scope to another package or scope; ends every live parent |
| `--celln-fleet-output-dir` | `~/.sympozium/celln-fleet/<scope>` | Private directory for the materialized configuration and installation records |
| `--celln-fleet-wait` | `15m` | How long to wait for the first labeled node |
| `--celln-router-image` | `ghcr.io/sympozium-ai/celln:v0.5.35` | Router image (`repo:tag` or `repo@sha256:…`) |
| `--celln-installer-image` | `ghcr.io/sympozium-ai/sympozium/celln-installer:<release>` | The fleet node image (the `celln` binary and its runtime assets); a source build uses the embedded chart's `appVersion`, never `latest` |
| `--celln-router-replicas` | `1` | Router replicas for the generated ownership PVC |

The general install flags (`--image-tag`, `--set`, `--adopt-existing`,
`--no-ergoz`) apply as well; see the [CLI reference](../reference/cli.md).

## Trust

- **Publisher.** The package is signed with an operator seed; its publisher
  key is approved once (the release pins it) and every node writes it into
  `trusted-closures.json` before admission. A package cannot install its own
  trust.
- **Parent principal.** One bearer credential (`celln-router-parent`) for the
  router; only its BLAKE3 hash is published as the client policy every owner
  installs. Rerunning verifies the pair and never rotates it by replacement.
- **Mediation trust.** Four objects, bootstrapped by the installer and never
  rendered from Helm values: Secrets `celln-mediation-controller` and
  `celln-mediation-gateway` in the control-plane namespace,
  `celln-mediation-node` in `celln-system`, and the public ConfigMap
  `celln-mediation-trust` (JWKS and CA certificate) in both. Details and
  rotation are in the
  [model access guide](celln-mediated-model-access.md#how-it-is-wired).
- **Model keys.** None on any node. A key is a Secret in the Agent's
  namespace, owned by that Agent and pinned to one endpoint origin; only the
  gateway reads it, per request.
- **Configuration publication.** Only the configure DaemonSet holds a
  projected ServiceAccount token, scoped to its ConfigMaps in
  `celln-system`. The dispatcher container has no API credential.

## Verify

```sh
kubectl -n celln-system get daemonset celln-node celln-node-configure   # DESIRED = labeled nodes
kubectl -n celln-system get pods -o wide
kubectl -n sympozium-system rollout status deploy/sympozium-model-gateway   # ready = PostgreSQL, RBAC and keys accepted
kubectl get cellnexecutionpolicy celln-fleet-starter -o yaml              # ceilings, allowedOrigins/deniedOrigins
sympozium doctor
```

`sympozium doctor` reads the cluster and prints one `PASS`/`WARN`/`FAIL`
line per check with the command that fixes each problem (`--json` for
scripts; exit 1 on any `FAIL`). Its checks: **Cluster**, **Helm release**,
**Ownership** (objects the chart would create that exist outside the
release), **Terminating** (CRDs or namespaces stuck in deletion and what
holds them), **KVM nodes**, **Fleet pods** (including an init container that
has waited more than two minutes, with its last log line), **Fleet package**
(the published package against the one this CLI installs), **Fleet turn
budget** (whether the ceilings pay for the turns the policy allows) and
**Mediated model access**: the trust objects, the gateway's readiness and
the policy's origin guardrail, for example
`policy celln-fleet-starter lets an Agent's own key go to any origin the
gateway reaches (narrow it with allowedOrigins/deniedOrigins)`.

Then create a connection and an Agent and run it, through the API or
`kubectl`: [Create an Agent through the API](celln-mediated-model-access.md#create-an-agent-through-the-api).

### Seeing cells in the console

The Harnesses page and the topology show `celln ps` for every fleet node
(`GET /api/v1/celln-platform/cells`). The API server first asks the router
for `GET /v1/cells` with its read-only capability token (**source:
gateway**), which also carries each parent's live owner status; a Celln
release without that endpoint answers 404, and the API server then reads the
`celln-fleet-cells` ConfigMap that each `celln-node-configure` pod publishes
every two seconds (**source: node reports**).

## Authorising namespaces

The catalogue is published **once per scope** as cluster-scoped objects.
Which namespaces may run on it is `--celln-fleet-authorise`: by default
every namespace except the system exclusions and any namespace labeled
`celln.sympozium.ai/excluded=true` (fence a namespace off with that label;
nothing else is needed to admit one), or with `labeled` only namespaces
labeled `celln.sympozium.ai/scope=<scope>`.

A namespace's runs select ordinary workload objects: an `AgentRuntime`
wrapper (`celln-native.toolbox` for the starter toolbox, `celln-native` for
chat only) binding the profile, the Agent's own `ModelConnection`, and the
Agent. The wrapper is created on first use, by the console or by
`POST /api/v1/celln-platform/wrappers?namespace=<ns>` with
`{"profile": "celln-native-<scope>.toolbox", "runtimeOnly": true}`; a
namespace that manages its own objects in Git can apply the same
`AgentRuntime` instead (existing objects are never modified). A run in an
excluded namespace is held with `AUTH_POLICY_WITHDRAWN`; nothing is issued.

## The toolbox

Every run borrows tools from the scope's package, and the policy lends
exactly those revisions. Two kinds live side by side:

- **Brokered tools**, Celln's own eight: `workspace-read`, `workspace-write`,
  `workspace-list`, `workspace-append`, `workspace-search` and
  `workspace-delete` over the run's files, and `https-fetch` (GET) and
  `https-post-json` (a JSON object, posted with no credential) over the
  network. They are the only way a cell touches files or the network, through
  host brokers with the quotas the policy shows; every write-like operation
  is an approved effect. The hosts the two HTTPS tools may reach are the
  scope's `--celln-fleet-https-host` list, by default any public HTTPS host;
  private, loopback and link-local addresses, plain HTTP and other ports are
  always refused, whatever a model connection's `allowInsecure` says (that
  applies to the model endpoint only).
- **Borrowed commands**: ordinary programs taken from container images pinned
  by digest in Celln's catalogue (`tools.toml`), for example busybox's grep,
  sed, awk, sort, uniq, wc, cut, head, tail, tr, base64, sha256sum and date,
  and jq. Each command's static executable is extracted from the pinned image
  when the package is built and lent inside the signed worker closure; the
  model calls it through the `celln.argv/v1` binding (validated arguments
  become the command line, no shell). Each cluster tool names the image it
  came from: `kubectl get clustercellntool -o custom-columns=NAME:.metadata.name,SOURCE:.spec.sourceImage`.

An Agent that lends the toolbox runs on the `.toolbox` profile and selects
exactly its tools in catalogue order (`toolboxTools` in
`GET /api/v1/celln-platform/profiles`); a subset or another order is refused
`AUTH_TOOL_ORDER_MISMATCH`. A worker carries at most 24 tools (8 brokered
plus 16 commands).

### Extending the toolbox

No recompilation. On the packaging machine:

1. Pin an image into your own catalogue (`~/.celln/tools.toml` under the
   root you package with): `celln image add ghcr.io/example/tool:1.2`.
2. Declare the commands the model may call, in that entry:

   ```toml
   commands = [
     { name = "csvq", exec = "/usr/bin/csvq", args = ["{query}"], stdin = "csv",
       description = "Run a SQL query over CSV text.",
       params = [ { name = "query", type = "string", required = true, max = 2048 },
                  { name = "csv", type = "string", required = true, max = 4096 } ] },
   ]
   ```

   Arguments are literals, `{field}`, `{field?FLAG}` (FLAG when a boolean is
   true) or `{field:FLAG}` (FLAG then the value when present); `stdin` names
   the string fed to the program. String parameters and a command's output
   are bounded at 4096 characters. The executable must be a static Linux
   amd64 binary.
3. Rebuild the starter package (`celln starter-package … --tool-image NAME`)
   and install it with `--celln-fleet-replace-package` (see
   [Moving to a new package or scope](#moving-to-a-new-package-or-scope)).

## Leases and budgets

A parent lives for its run's `leaseSeconds` and may spend up to its run's
turn, model-request and output-token budget (`spec.enduring`). The scope's
ceilings bound every run and are configured on every node at install time:

| Flag | Default | Range |
| --- | --- | --- |
| `--celln-fleet-max-lease-seconds` | 86400 (24 h) | 60–86400 |
| `--celln-fleet-max-turns` | 256 | 1–1024 |
| `--celln-fleet-max-model-requests` | 1536 (256 × 6) | 6–6144 |
| `--celln-fleet-max-output-tokens` | 6291456 (256 × 6 × 4096) | 24576–25165824 |

One turn may make 6 model requests, and **every turn reserves 6 requests and
6 × the connection's `maxOutputTokens`** from the parent's lifetime totals,
whether or not it spends them: 3072 tokens at the default per-request cap of
512, 24576 at the largest, 4096. Because a model-free node is configured for
the largest cap any connection may set, an unset lifetime output-token
ceiling is sized for 256 such turns and the installer prints the figure; an
explicit one must pay for at least one turn at that cap. Totals that afford
fewer turns than `max-turns` end a conversation early; `sympozium doctor`
reports this as **Fleet turn budget** with the turn it would end at.

When a connection is created through the API, its `maxOutputTokens` is
clamped to what one turn can afford under the namespace's policy
(ceiling ÷ 6), so a first run does not fail on a bound the scope cannot pay
for. A run asking for more than a ceiling is refused
`AUTH_LIMIT_OUT_OF_RANGE`. One message is at most 2048 bytes and one
committed answer at most 8192 bytes. When a lease ends no new turn is
admitted and the parent stops; leases are not extended in place. Every live
parent holds two cells and its declared memory for its whole lease, so long
defaults cost node capacity while conversations sit idle.

`GET /api/v1/celln-platform/profiles` reports the policy's `ceilings` and a
`sessionDefaults` budget a new conversation should ask for within them.

Ceilings are configured on the nodes together with the package and are never
rewritten for an unchanged package; they are rewritten when the scope moves
to another package:

```sh
sympozium install --celln-fleet-replace-package \
  --celln-fleet-max-turns 512 --celln-fleet-max-model-requests 3072 \
  --celln-fleet-max-output-tokens 12582912
```

## Capacity

Each node sizes itself when its dispatcher starts (`celln.fleet.capacity:
auto`, the default): it may reserve `memoryPercent` (75) of the node's memory
(the container's cgroup limit when one is set) for guests, allows one cell
per `cellMemoryBytes` (640 MiB) up to `maxCellsCeiling`, and one broker slot
per cell. The dispatcher logs the result (`celln capacity: node=… cells=…`).
A parent charges two cells, two broker slots and its declared memory
(1.25 GiB for the starter profile). For nominal node sizes:

| Node memory | Cells | Parents per node |
| --- | --- | --- |
| 16 GiB | 19 | 9 |
| 64 GiB | 76 | 38 |
| 256 GiB | 307 | 153 |

Set `capacity: fixed` with `maxCells`, `memoryBytes` and `egressSlots` to pin
exact numbers, or lower `memoryPercent` on nodes that run other workloads.
The router places each new parent on the healthy owner with the most spare
cells (then memory); once provisioned, a parent is bound to its owner.

## Operations

- **Join a node:** label it `celln.dev/kvm=true`. The package is admitted on
  that node only.
- **Leave a node:** remove the label or drain it. The DaemonSet pod's preStop
  closes admission and stops every parent on that owner; those runs end with
  `AUTH_CONTEXT_LOST`. Start new runs; nothing is moved.
- **Rolling updates** replace one node's dispatcher at a time with the same
  semantics. A dispatcher restart loses the live parents on that node.
- **Rotation of the mediation trust:** see
  [Rotation](celln-mediated-model-access.md#rotation).
- **Uninstall** retains `/var/lib/sympozium-celln/<scope>` on each node;
  remove it only after every run has been deleted and cleanup confirmed.

### The catalogue's persona is only a default

The starter catalogue carries one system prompt. It is the fleet's default
persona, used by a run whose Agent names none; an Agent's own
`execution.systemPrompt` replaces it for that Agent's runs (see
[Agent-bound model access](celln-mediated-model-access.md)). Changing the
default means a new starter package or Celln binary, below.

### A new Celln binary re-derives the configuration

The configuration a node publishes (the persona, the tool limits, the
template shape) is derived from the starter package *by the Celln binary in
the `celln-node-configure` image*. The node records which binary derived it
(`celln.sympozium.ai/configured-by` on the ConfigMap, and a marker in its
state directory). A new installer image therefore re-runs `starter-configure`
and replaces the published configuration even when the package is unchanged;
during a rollout the last node to publish wins and the fleet converges once
every node runs the new image. Nodes on the same binary that derive different
files from one package are still refused.

## Moving to a new package or scope

A Sympozium release carries a new starter package only when the package's
inputs changed: the pinned Celln release (`config/celln/release.json`), the
packaging recipe (`hack/build-celln-starter.sh`) or the tool images. Every
other release republishes the previous package unchanged, so upgrading
`sympozium` and rerunning `sympozium install` keeps the fleet's package and
restarts no dispatcher. Compare the `celln-starter.json` asset of two
releases to see whether the package changes (`packageHash` decides):

```console
$ for tag in v0.11.4 v0.12.0; do gh release download "$tag" --repo sympozium-ai/sympozium \
    --pattern celln-starter.json --output - | jq -c '{inputs, packageHash}'; done
```

One scope carries exactly one package at a time. Because a move ends every
live parent on the fleet, the installer checks the published configuration
before it changes anything and refuses unless you approve:

```console
$ sympozium install
Error: the Celln fleet runs package blake3:d365… in scope starter, and this
install would move it to package blake3:e75a… in scope starter.
  Moving a scope ends every live parent on the fleet. To go ahead, rerun with --celln-fleet-replace-package.
```

- **Move the fleet:** rerun with `--celln-fleet-replace-package`. The nodes
  admit the new package next to the old one and publish the new
  configuration; the installer replaces the scope's profiles and tools
  (immutable, so deleted and recreated), rewrites the policy, removes tools
  the new package dropped, and points every namespace's platform-managed
  wrappers at the new profiles. Wrappers a namespace created itself are left
  alone. The owner DaemonSet rolls, so every live parent ends.
- **Keep the installed package:** pin it with `--celln-fleet-package-image`,
  `--celln-fleet-package-hash` and `--celln-fleet-publisher` from the
  `celln-starter.json` asset of the release that installed it.

`sympozium doctor` (**Fleet package**) reports which case you are in.

## Upgrading from 0.11

0.11 fleets carried model backends: the installer asked for a provider key,
configured each backend on every node, held its key in the
`celln-fleet-model-credentials` Secret (a placeholder when mediated), gave
the key to a `starter` Agent, and admitted an Agent's own key only for
providers on an operator route list. 0.12 removes all of it. **Celln holds no
model; the Agent's `ModelConnection` is its route.**

Rerunning `sympozium install` with a 0.12 binary migrates an installed 0.11
fleet by itself. It detects the keyed configuration (the scope's profile
still names a model profile), configures the nodes model-free, and in every
namespace deletes what the fleet-held model left behind:

- the platform-managed Agents (`celln-agent`, `celln-agent-<backend>`,
  `starter`, `starter-<backend>`) and ModelConnections (`celln-native`,
  `celln-<backend>`, `starter*`);
- the runtime wrappers and `CellnRuntimeProfile`s of backends the model-free
  scope no longer configures (only the one model-free profile and its
  toolbox remain, under the old names `celln-native-<scope>` and
  `celln-native-<scope>.toolbox`, so wrapper names and `runtimeRef`s keep
  working);
- the `celln-fleet-model-credentials` Secret in `celln-system`.

The scope's policy is rewritten: host-profile routes go, ceilings are
resized for a model-free node. The package itself is unchanged, so no
`--celln-fleet-replace-package` is needed, but **every live conversation on
the fleet ends** (the `celln-node` pod template and the profiles change).
Start new runs afterwards.

What teams do next: each team creates its own `ModelConnection` (any
OpenAI-compatible or Anthropic-compatible endpoint, with its own key Secret)
and an Agent that references it, then runs it. The 0.11 starter Agent's
`starter-model-key` Secret is not deleted; it stays owned by the deleted
Agent until another Agent in that namespace claims it with a connection to
the same origin. See
[Agent-bound model access](celln-mediated-model-access.md).

Removed, and refused if still set:

| Removed | Replaced by |
| --- | --- |
| Provider keys at install (`DEEPSEEK_API_KEY` and friends in the environment, the terminal prompt, `SYMPOZIUM_CELLN_BACKEND`) | Nothing is asked; each Agent brings a key |
| `--celln-fleet-backend`, `--celln-fleet-model-*`, `--celln-fleet-skip-preflight`, `celln.fleet.backends`, `celln.fleet.model.*`, `celln.fleet.modelCredentialsSecret`, `celln.fleet.backendsExtraRevision`, `POST /api/v1/celln-platform/backends` | The Agent's `ModelConnection` (the chart refuses to render with these values) |
| `celln.mediation.routes`, `defaultRoutes`, `mediateBackends`, `--celln-mediated-route`, `--celln-mediate-backends`, `sympozium celln-mediation apply-routes`, the `celln-mediated-routes` ConfigMap, "pending" routes | No route list: the key is pinned to its connection's origin; `allowedOrigins`/`deniedOrigins` on the policy narrow it (the chart refuses the old values) |
| `--no-celln-mediation` | Mediation is how Celln runs models, not a switch |
| `--celln-starter-namespace`, `--celln-native-approve-starter-tools`, the `starter` Agent | Teams create their own Agents |
| `credentialProfile` on a `ModelConnection`, `auth: host-profile` policy routes | `secretRef` (or no credential) |
| The single in-cluster dispatcher (`celln.dispatcher.*`), the host installer DaemonSet (`celln.installer.*`, `--celln-host-installer`, `--celln-backend`, `celln.openaiApiKey`/`anthropicApiKey`/`deepseekApiKey`), the native parent plane (`celln.nativeParent.*`, `celln.fleet.parentConfigSecret`, controller `--celln-parent-only`, `CELLN_PARENT_*`), `--celln-native` | The fleet, on every KVM node |
| `sympozium celln issuer|withdraw|tool|selection|recover` and the host issuer stack | Nothing; the fleet needs no issuer |

`sympozium upgrade` (chart upgrade with the deployed values) does not run
this migration; use `sympozium install`.

## Troubleshooting

Start with `sympozium doctor`; it only reads. `sympozium install` runs the
same leftover checks before it changes anything and stops with the complete
list rather than failing part-way.

- **`… exists and cannot be imported into the current release: invalid
  ownership metadata`.** Objects of an older, partly removed install are in
  the way. `doctor` (**Ownership**) lists every one with a remedy each.
  `sympozium install --adopt-existing` adopts the safe kinds (Namespace,
  ServiceAccount, ConfigMap, Secret, Service, NetworkPolicy,
  PersistentVolumeClaim and the chart's own `sympozium.ai` resources);
  Deployments, DaemonSets and other kinds are never adopted, because an old
  spec may be incompatible: delete them and the chart recreates them.
- **A CRD or namespace stuck `Terminating`.** Custom resources still hold a
  finalizer and no controller is left to remove it. `doctor`
  (**Terminating**) names each holder and prints its `kubectl patch …
  '{"metadata":{"finalizers":null}}'` command. Clearing a finalizer skips the
  controller's cleanup for that object, so only do it for an install that is
  gone.
- **`celln-node` never starts and the install sits in its wait.** `doctor`
  (**Fleet pods**) reports an init container that has run for more than two
  minutes, or is backing off, with its last log lines. The usual cause is a
  state directory `/var/lib/sympozium-celln/<scope>` left by an older install
  under another uid: `wait-prepared` fails at once with `cannot read the
  Celln state directory …`, and `celln-node-configure` resets the owner to
  root on its next pass. If it does not, move the directory aside on the node
  (`sudo mv /var/lib/sympozium-celln/<scope>{,.old}`) or `sudo chown 0:0` it;
  with SELinux enforcing also `sudo chcon -R -t container_file_t` it.
- **No node is labeled.** `doctor` (**KVM nodes**) says so and prints the
  `kubectl label node <name> celln.dev/kvm=true` to use for a node you know
  has `/dev/kvm`; on Kind, copy the kernel in first.
- **`Mediated model access` FAILs.** The release records no gateway, a trust
  object is missing, or the gateway is not ready. `sympozium install` wires
  the gateway and bootstraps the trust; `kubectl -n sympozium-system logs
  deploy/sympozium-model-gateway` says why it is not ready (ready means
  PostgreSQL, RBAC and keys were accepted).
- **A run is refused before it starts** (`model key Secret … belongs to …`,
  `… is pinned to … and cannot be sent to …`, `AUTH_ROUTE_MISMATCH`,
  `AUTH_LIMIT_OUT_OF_RANGE`) or **a model call fails with `MODEL_…`**: see
  [Refusals](celln-mediated-model-access.md#refusals).
- **A CLI built from source ran an old node image.** A build without a
  release version uses the embedded chart's `appVersion` (`v`-prefixed) for
  the node image and says so; `--celln-installer-image` overrides it.

## Limits

One active turn per parent; no live parent migration (node loss ends the
conversation with `AUTH_CONTEXT_LOST`; start a new run); no live lease
extension; one package per scope. Celln runs only its native harness: Pi and
Hermes stay on the Kubernetes plane and use the same `ModelConnection` and
key model.
