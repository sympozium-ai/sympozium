# Celln: Agent-bound model access

Status: **proposal** (2026-10-22). Replaces the operator-declared model access
described in `docs/guides/celln-mediated-model-access.md` and the model-backend
half of `docs/guides/celln-fleet-installation.md`.

## The problem

Celln is KVM isolation. It should not care which model an agent talks to. But
installing it today means picking a model up front, then declaring which
providers Agents may use:

- `sympozium install` needs a provider key or `--celln-fleet-backend` before
  the fleet exists. Celln's `starter-configure` writes a model profile per
  backend, defaults to DeepSeek and requires a credential file it never reads.
- Every Agent then borrows one of those per-backend profiles just to get the
  reviewed worker runtime. Its model route is ignored (`profileForRoute`:
  "the profile's own model route is not used").
- A provider an Agent may use must be an operator **route**: provider,
  protocol, exact origin and model. Routes come from three sources
  (`routes`, `defaultRoutes`, `mediateBackends`). They are copied from values to a
  ConfigMap to the policy, append-only, with `apply-routes` and "pending" to
  manage the drift. Anything not on the list is `AUTH_ROUTE_MISMATCH`.
- The installer turns its own key into a "starter Agent" because the fleet
  used to hold that key. Mediation is a switch with an "operator mode", trust
  that only the CLI can bootstrap, and a placeholder credential still mounted
  on every node.

Most of this exists because the fleet used to hold one shared key. Since #650,
every mediated Agent already brings its own key and nothing on a node holds one.
The machinery that decided which shared backend an Agent ran on is still there.

## Goals

1. Celln installs as part of Sympozium. `helm install` / `sympozium install`
   brings it up; every KVM node joins; nothing about models is asked.
2. Nobody declares an AI backend up front.
3. Create an Agent on Celln, pick a provider, give it a key: it works.
4. Celln Agents are permissive by default (within reason) and fully usable
   through the API, not just the console.
5. No assumption about which provider: a list of presets for convenience, any
   OpenAI-compatible or Anthropic-compatible endpoint otherwise.
6. Many teams share one cluster. **The Agent's own configuration is the
   binding between a provider key and that running agent.**

## What we keep (this is the actual security)

These already hold for every mediated run and do not depend on routes or
backends:

| Property | Where |
|---|---|
| One key, one Agent: a Secret is claimed by exactly one Agent (or its Ensemble); another Agent is refused | `internal/modelkey`, resolver grant check, controller claim, gateway `OwnedBy` on every call |
| The key never reaches a node, a guest, a run spec or the controller | gateway adds it per request; Celln's mediated grant refuses a credential file (`celln egress.rs`) |
| A run cannot carry or redirect a key | `modelconnection.Resolve`: no inline credential, no overrides, revision pinned |
| Session binding: a signed permit per run, bound to cluster, namespace UID, run UID, spec digest, route and budget; turns cannot widen it | `internal/cellnscoped`, gateway register/invoke |
| Live re-check on every model call: connection UID/digest/origin, Secret UID, Agent UID, key owner. Revocation bites mid-run | gateway `liveRoute` + `OwnedBy` |
| Network safety: no redirects, private/loopback/link-local refused unless allowed, no streaming, provider error bodies hidden, credential echo refused | `internal/modelgateway/transport.go`, `response.go` |
| Budgets per run and per turn | gateway + PostgreSQL |

Multi-team isolation is the namespace: a team's keys are Secrets in its
namespace, its Agents are there, and namespace RBAC decides who can write them.

## What changes

### 1. Celln is model-free

- **Celln (`starter-configure`)**: a model-free mode, used by Sympozium
  always. No model profile, no `credentialFile`, no DeepSeek default, no
  `native-template.json` model fields. It still publishes the catalogue, tool
  schemas, argv bindings and `scoped-parent-request.json`. The scoped path
  already needs nothing else (it never opens `trusted-parent-models`).
- The per-request output-token bound comes from the signed decision (the
  connection's `maxOutputTokens`), not from operator material.
- **One runtime per scope**, not one per backend. One profile
  (`celln-<scope>`, plus `.toolbox`), one wrapper AgentRuntime per namespace
  (`celln`). Ceilings and the enduring parent request come from that scope,
  not from "the first backend".

Removed: `celln.fleet.backends`, `celln.fleet.model*`, the
`celln-fleet-model-credentials` Secret and mount, `celln-fleet-backends-extra`,
`POST /api/v1/celln-platform/backends`, the backend preflight, per-backend
wrappers, `MediationOnlyLabel`, `PlanMediatedBackends`, the starter Agent.

### 2. The Agent's connection is the route

A run's model route is its Agent's `ModelConnection`: provider (a free label),
protocol (`openai-chat` | `anthropic-messages`), endpoint, model, and optionally
`secretRef`. There is no operator route list.

What routes protected, and what replaces it: routes stopped "anything that can
write a ModelConnection" (a prompt-injected agent with that permission)
pointing a key at a host of its choosing. The replacement binds the key to its
destination when the key is created:

- **Key ↔ origin pin.** When a key Secret is created (API, console, or by hand)
  it records the endpoint origin it is for: annotation
  `sympozium.ai/model-key-origin: https://api.openai.com`. The gateway refuses to
  send the key anywhere else. Changing a connection's endpoint to another
  origin needs a new key, entered by a person. An agent that rewrites its own
  connection gets a refusal, not an exfiltrated key.
- A Secret without the annotation is pinned on first use (TOFU), the same way
  `model-key-owner` is claimed today.
- Agents' own pods get no RBAC on ModelConnections or Secrets (true today for
  run pods; skill sidecars lost Secret access in #650). This stays a hard rule.

Model names are not restricted. A key may be used for any model its provider
serves. The signed decision still binds the run to one concrete model.

Removed: `celln.mediation.routes`, `defaultRoutes`, `mediateBackends`,
`--celln-mediated-route`, `--celln-mediate-backends`, the
`celln-mediated-routes` ConfigMap, `apply-routes`, "pending" routes,
`AUTH_ROUTE_MISMATCH` for own-key runs, `GET /celln-platform/mediation` route
listing, and `host-profile` routes (there are no fleet-held keys left to protect).

An operator can still **narrow** (opt-in, not required): a policy may list
allowed origins and/or denied origins. Empty = any public HTTPS endpoint.

### 3. Any provider

- Presets are a convenience in the console and API only: OpenAI, Anthropic,
  DeepSeek, Google Gemini (OpenAI-compatible), OpenRouter, Groq, Mistral,
  Together, xAI, and local servers (Ollama, llama-server, LM Studio, vLLM).
  Each preset is just `{protocol, endpoint, suggested models}`.
- **Custom**: any name, protocol, endpoint.
- The Secret key name stops being tied to the protocol. A connection names the
  key: `secretRef: {name, key}`; the console writes `API_KEY`. The old
  `OPENAI_API_KEY`/`ANTHROPIC_API_KEY` names keep working.
- Endpoints may carry a port (`https://llm.internal:8443`). Fixes the Azure-
  style gap only partly; Azure's `api-key` header + `api-version` query is a
  later preset with its own protocol variant.

### 4. Private and local endpoints, within reason

Default: the gateway reaches any **public** HTTPS endpoint. A private
destination (RFC 1918, ULA, 100.64/10, a LAN llama-server) needs a one-time
operator approval of that exact origin (`modelGateway.privateOrigins`, as
today), because the gateway sees only an IP and cannot tell a LAN host from a
pod or service range. The change is in the experience, not the rule: a tenant
creates a connection to a private origin, the console and `doctor` show it as
"pending operator approval" with the exact value to add, and the operator
approves once for the cluster. Plain HTTP stays keyless-only. Loopback,
link-local and the `kubernetes` Endpoints addresses are always refused;
NAT64 `64:ff9b::/96` is treated as public.

### 5. Installs as part of Sympozium

- The chart deploys the fleet and the gateway by default
  (`celln.enabled: true`). Nodes the probe finds with `/dev/kvm` and a kernel
  join; a cluster without KVM installs the same thing and simply has no Celln
  capacity (the console says so; nothing fails).
- **The fleet is privileged, and the install says so.** The `celln-node` and
  `celln-node-configure` DaemonSets run `privileged: true` with `/dev/kvm`,
  `/boot`, `/lib/modules` and a hostPath state directory on every KVM node;
  that is what booting micro-VMs needs. It is part of the security envelope,
  not hidden behind a flag: `sympozium install` prints it before it proceeds,
  `helm install` prints it in NOTES.txt, the chart README and the fleet guide
  lead with it, and `--no-celln` / `celln.enabled=false` opts out. The legacy
  `celln-installer` DaemonSet (hostPID + host root + node-held provider keys)
  is deleted; nothing in 0.12 mounts the host root.
- The starter package digest, hash and publisher are pinned in the chart's
  `values.yaml` at release, not only in CLI ldflags, so plain `helm install`
  works.
- Trust is bootstrapped in-cluster: a Helm hook Job (the controller image,
  `cellninstall.PrepareMediationTrust`) creates the issuer key, tokens and
  certs if missing and verifies them otherwise. `clusterId` is the
  `kube-system` UID. No CLI step, no operator mode, no `keyId` value.
- Mediation is not a switch: it is how Celln runs models. `--no-celln-mediation`,
  `celln.mediation.enabled`, the operator-managed path and the
  `ScopedDispatchDisabled` state go away.
- Removed CLI: `--celln-fleet`, `--celln-fleet-*` model and backend flags,
  `--celln-native-approve-starter-tools`, `--celln-starter-namespace`,
  `SYMPOZIUM_CELLN_BACKEND`, the provider prompt. Kept: `--no-celln`,
  `--celln-fleet-https-host`, limits, scope, replace-package.

### 6. Permissive by default

- Every namespace not excluded may run Celln Agents (as today).
- A Celln Agent gets the full starter toolbox: workspace (all six ops), web
  fetch and JSON POST to any public HTTPS host, borrowed commands. Private
  addresses are always refused for tools.
- Generous default limits from the scope's ceilings, chosen server-side; the
  client never computes them.
- An operator narrows with the policy (tools, hosts, limits), never has to
  widen.

### 7. Direct API use

One call creates a working Celln Agent:

```http
POST /api/v1/agents
{
  "name": "researcher",
  "execution": { "backend": "celln" },
  "model": {
    "provider": "openrouter",
    "protocol": "openai-chat",
    "endpoint": "https://openrouter.ai/api/v1/chat/completions",
    "model": "qwen/qwen3-235b",
    "apiKey": "sk-..."            // or "secretRef": {"name": "...", "key": "..."}, or neither for keyless
  }
}
```

The server creates the Secret (owned by this Agent, pinned to that origin),
the ModelConnection, the wrapper and the Agent, with lifecycle, toolbox and
limits defaulted. `kubectl apply` of the same objects works too.

Talking to it:

- `POST /api/v1/agents/{name}/messages` `{ "message": "...", "conversation"?: "<run>", "requestId"?: "..." }`
  continues the Agent's newest live conversation (or the one named) as a turn,
  or starts one from the Agent's own execution defaults. It answers with the
  run and, for a continued conversation, the turn; the answer lands on that
  object's status, which the caller polls or watches as today. (As built in
  0.12: no synchronous `wait`; the OpenAI-compatible endpoint below is where
  request/response semantics belong.)
- `POST /api/v1/agents/{name}/v1/chat/completions` and `GET …/v1/models`
  (built 2026-10-23): the Agent as an OpenAI-compatible endpoint, so any SDK
  pointed at base URL `…/api/v1/agents/{name}/v1` with the Sympozium token as
  its API key talks to it (`X-Sympozium-Namespace` names the namespace, since
  SDKs cannot add a query parameter). Semantics settled as **conversation
  keyed, not stateless**: the newest user message is the message to the Agent
  (a turn of its live conversation, or the first message of a new one, exactly
  as `/messages`); resent history is ignored because the Agent holds its own
  conversation; concurrent requests are serialised by the turn mechanism as
  they are today; a transient turn refusal (parent not admitted yet, earlier
turn still running) is waited out rather than returned. The request waits
for the answer (`?timeout=`, default 3m,
  max 10m; `504` names the run and turn to read, and the conversation goes
  on), streaming it as one SSE content chunk after an immediate role chunk and
  keep-alive comments when `stream` is true. `X-Sympozium-Conversation` names
  a run to continue. The completion's `sympozium` field and the
  `X-Sympozium-Run`/`X-Sympozium-Turn` headers name the objects behind it. A
  Kubernetes Agent on a session runtime (Pi, Hermes `openai-chat`) is handed
  to its running session unchanged, history and streaming included, through
  the existing fixed-target session proxy; any other Agent runs the message as
  a run and waits for its result. Errors are OpenAI error objects.

## Harnesses in a cell (Pi, Hermes): deferred

Celln runs only its native harness in 0.12. Pi and Hermes stay on the
Kubernetes plane and use the same `ModelConnection` and key model, so the
provider choice is identical on both planes.

Why deferred (review, 2026-10-22): the only host↔guest data path is a
port-I/O mailbox that carries one exchange at a time in frames under 61,440
bytes and yields the vCPU on commit (`celln-warden/src/parent_mailbox.rs:9-23`).
A long-lived HTTP server that needs an egress model call in flight while an
ingress request is open, with streamed answers and histories over 32 KiB, needs
a new transport, a new guest lane, a model shim that rewrites each adapter's
request shape, 4× the cell memory and signed closures for node and python
rootfs. That is a Celln project of its own, and the payoff today is small
because both adapters run with their tools off. It gets its own design doc,
built on the model path this one establishes.

What it would take (sized 2026-10-23 against the code). Celln's VMM
(`celln-warden/src/vmm.rs`) emulates an 8250 UART and the port-I/O mailbox
and nothing else: no virtio devices, no vsock, no network; the guest is a
bzImage plus `guest/init/init.c` running the native starter, and its model
path is a credential-free, non-streaming JSON POST broker to one pinned
endpoint (`egress_post.rs`). Pi and Hermes are Node and Python servers that
expect a socket to listen on and a model URL to call. So, in order:

1. **A guest data lane**: a virtio-vsock device in the VMM and the guest
   kernel, with the host end owned by the warden (no network device; the
   guest still has no AF_INET route out).
2. **Egress shim**: a guest-side loopback HTTP listener the adapter is given
   as `OPENAI_BASE_URL`, forwarding over vsock to the host broker, which
   keeps the only credential and the origin pin and now has to stream.
3. **Ingress**: the apiserver's `/v1/chat/completions` request reaches the
   adapter's port over the same lane, through the scoped receiver, replacing
   the single-exchange turn mailbox for these cells and lifting the 2 KiB
   message / 8 KiB answer bounds.
4. **Rootfs closures** for the Node and Python adapters, digest-pinned and
   signed like the toolbox layers, with roughly 4× the cell memory.
5. **Sympozium**: an AgentRuntime session profile that places on a cell, the
   HarnessSession lifecycle driving a cell instead of a Deployment, and the
   gateway as the sole egress. The per-Agent OpenAI endpoint already fronts
   session runtimes, so the URL a client uses would not change.

Several weeks of Celln work plus a Sympozium slice; the isolation gain is
real, the user-visible change is only *where* the harness runs.

## Threat model check

| Threat | Before | After |
|---|---|---|
| Agent B uses Agent A's key | refused (modelkey) | unchanged |
| Prompt-injected agent points its key elsewhere | needs ModelConnection write; refused by route list | needs ModelConnection write; refused by key↔origin pin |
| A team member sends their own key to an arbitrary provider | refused unless operator declared it | allowed: it is their key and their choice |
| Guest prompts reach a host the operator never named | refused for the model route; allowed for web tools | allowed for both, like web tools today; operator may narrow |
| Gateway used to reach cluster internals (SSRF) | private refused unless `privateOrigins` | unchanged; approval is a console step instead of a values edit |
| Key on a node | no (mediated) | no: there is no other path |

The one property given up is an operator allow-list of model destinations by
default. It stays available as an opt-in narrowing.

## Delivery

One PR per slice, unit/render tests per seam, one system journey at the end.
Breaking (0.12); all prior Celln model-access work is disposable. Details and
file references in `celln-agent-bound-model-access-review.md`; what goes in
`celln-removal-inventory.md`.

1. **Celln model-free**: `starter-configure` without a model profile or
   credential file; Sympozium's configuration reader accepts both shapes;
   single configuration directory per scope. Release + pin.
2. **Core**: the connection is the route; required origin pin written at
   create and at claim, enforced at Pin, liveRoute and the API update path;
   `GrantsConnection` fixes the `modelConnectionRef`-only gap; named Secret
   key; HTTPS port allowed; `allowedOrigins`/`deniedOrigins` host globs on the
   policy; server-side `maxOutputTokens` clamp; one profile per scope with the
   old wrapper names kept as aliases; private origins unchanged.
3. **Install**: fleet + gateway + in-cluster trust bootstrap on by default with
   the privileged-fleet disclosure; legacy installer DaemonSet, backends,
   routes, starter Agent, mediation switch and their flags removed; `doctor`
   updated; upgrade notice that live conversations end; fleet-keyed cleanup.
4. **API + console**: one-call `POST /agents` with Agent-owned Secret and
   ModelConnection; `POST /agents/{name}/messages`; wizard presets + Custom;
   "pending operator approval" for private origins; per-namespace gateway
   share and spend ceiling; refusal events on the run.
5. **Docs + journey**: rewrite both guides; two teams, three providers (keyed
   canary, custom OpenAI-compatible, keyless local via an approved private
   origin), refused key reuse, refused origin change, unpinned Secret refused,
   API-only run.
6. **Dead code**: the one-shot router plane, the `--celln-native` single plane
   and the legacy issuer/review stack (~13,000 LOC) are removed in the same
   release.
7. **Later, own doc**: Pi/Hermes in a cell (sized above). The
   OpenAI-compatible per-Agent endpoint landed in 0.12 (conversation keyed).

### Status (2026-10-22, evening)

All slices are built in one working tree and verified end to end on a real
cluster: model-free fleet install with no model input and the privilege
disclosure, 0.11→0.12 migration of a keyed fleet, the one-call
`POST /agents` with an inline connection, `POST /agents/{name}/messages`
starting and continuing a conversation through the gateway (keyless LAN
llama-server answered), a keyed Secret pinned to its origin and a move
refused with 409. Removed: the one-shot router plane, the host installer, the
in-cluster dispatcher, the native-parent plane and parent-only controller,
the legacy issuer/review/catalogue stack, fleet-keyed backends and routes
(~40k LOC with tests). Docs, console and journeys follow the design. Lands as
one PR (Celln + Sympozium). Not in 0.12, tracked as follow-ups: the
per-namespace gateway share/spend ceiling, refusal events on the run,
Pi/Hermes in a cell, a native parent surviving refused tool calls.

Found and fixed 2026-10-23 while exercising the fleet through the new
endpoint: (1) a native **one-shot** run on the fleet never started
(`AUTH_LIFECYCLE_INVALID`): the resolver gave a native one-shot a parent
lease on a decision whose lifecycle said `one-shot`, a mix neither the
capability contract nor the Celln receiver (`tenancy_credentials.rs`)
accepts. Dropping the lease got the run admitted and then the fleet's worker
reported "missing parent context": the starter package's `/worker` is the
turn harness and runs only as a parent's turn. So a one-shot with a model on
a native profile is now issued as what it is on this fleet, a **single-turn
parent**: an `enduring-initial` decision with one turn, the turn's allowance
as the run budget and a lease covering admission plus the turn; the policy
still admits it as `harness-one-shot`, and the controller stops the parent
(cells returned, model allowance fenced) as soon as it has answered, then
records the run as succeeded. The dispatcher derives a parent incarnation for
every scoped run. The strict one-shot rule in the verifier stands. (2) Agents answered time-sensitive questions
from training data: the fleet persona said to use tools "when requested", and
`https-fetch` returned at most 4 KiB of raw body. The Celln starter persona
now tells the model to fetch a live, dated source first (Google News RSS
search, Wikipedia page summaries) and the fetch limit is 16 KiB; both are
bound into the fleet profile at install, so this is a Celln change surfaced
by `sympozium install`. (3) Deploying that fix exposed a third gap: the fleet
node cached its derived configuration by package hash alone and the publish
step refused files that differed for the same package, so a new
`celln-node-configure` image never changed the published persona or limits.
The node now records the Celln binary that derived a configuration
(`celln.sympozium.ai/configured-by`), re-derives when it changes, and a
re-derived configuration replaces the published one; the installer follows a
re-derived catalogue of the same package (profile and tools updated in
place) instead of refusing it as drift. The fetch body ceiling in the shared
Go/Rust authorisation contract (CRD, broker check, decision schema, Celln
receiver) rose from 4 KiB to 64 KiB so the 16 KiB tool limit is admissible;
the conformance bundle was regenerated. (4) With the new persona the model
did call `https-fetch`, and every call failed: the broker refused any body
over the grant outright (`curl --max-filesize`), and a news feed is 130 KB.
A web tool returns text, so the broker now cuts a body to the grant on a
character boundary (downloading at most 1 MiB) and the tool description says
so. With that in place the next run died after the fetch instead: the web
tools' declared result schema still bounded `content` at 4096 characters (the
workspace tools' file bound), so the harness refused its own tool's 16 KiB
result; `https-fetch` and `https-post-json` now declare a result schema sized
to the fetch limit, which also meant raising the tool-schema subset's string
bound in `celln-manifest` from 4096 to 16384 characters (the first attempt
left the node's configure pod crash-looping on "invalid string bounds"). A fetch round on a live conversation then answered from
Wikipedia's live summary (a July 2026 Forbes figure) in 36 s. Observed alongside, for later: four refused tool calls ended the native
parent ("native child provenance is incomplete") and the whole conversation
failed; a refused tool call should be a tool error the model sees, not the
end of the parent.

Also 2026-10-23, **the persona moved to the Agent**. Changing the fleet's
system prompt had required a Celln release, a re-derivation and a republish,
and still applied to every Agent on the fleet: a system prompt is agent
configuration, not reviewed supply-chain material. `execution.systemPrompt`
on the Agent is now inherited by its runs (a run may override it), the
catalogue's prompt is only the fleet default, and the cell's 2048-byte bound
is validated on the Agent, at run creation and in the authority resolver. No
Celln change was needed: the cell already took the run's `systemPrompt`.

Added 2026-10-23: the OpenAI-compatible per-Agent endpoint
(`/api/v1/agents/{name}/v1/chat/completions`, `…/v1/models`), verified on the
same cluster with an unmodified OpenAI client against a Celln Agent; the
console shows each Agent's base URL and a snippet.

Found and fixed 2026-10-24, running two Agents and a one-shot at once on the
fleet (the same web question to all three): the model answered every time
within three minutes, yet most cells ended "missing, ambiguous, or incomplete
pilot execution report" and the run failed as "context unavailable". The
dispatcher now says *which* check rejected a report, with a bounded excerpt,
and that showed the cause: kernel text inside pilot's console frames. A cell
forked from the warm template resumes a guest whose vCPU the host held since
the template parked, and a worker cell sits at a port exit for a minute on
every model request while its clock runs on; in both cases the guest kernel's
lockup watchdog printed a soft-lockup report (an emergency message, which
pilot's console silencing does not cover) onto the serial console, between or
inside the bytes pilot was transmitting one interrupt at a time. The parent
path already told the guest its clock had been stopped (`KVM_KVMCLOCK_CTRL`)
after a park; the fork path and the broker exchange did not. Both do now, the
frame parser tolerates kernel text after a complete marker or frame (and the
init's `CELLN:cell=live` before pilot's handshake), and per-turn audit
records (`parent-audit/`, operator-private) carry the output and denial.
Alongside: an enduring conversation that had answered was marked Failed when
its lease ran out (now Succeeded with `LeaseElapsed`); a one-shot's recorded
success was overturned by a later read of its stopped parent (a terminal
phase is never re-ended); and the template's per-turn tool-call budget rose
from four to five after a conversation died on a refused third fetch. With
all of that deployed, the three concurrent runs (a conversation through the
messages API, one through the OpenAI endpoint, and a one-shot) each answered
from live September and October 2026 sources in 113 to 152 seconds.

### Where the time went, and what was cut (2026-10-25)

A one-second timeline of a solo one-shot, with timing traces in the Celln
dispatcher (`CELLN_TRACE=1`: parent fork, boot to grant, each mailbox
exchange, turn reserve and worker, every broker fetch, every guest `CELLN:`
console marker) and debug-level step timings in the controller, gave this
budget for a 78-second run: 6 to 12 s of controller admission, 6 s inside
the receiver before the parent fork, 0.2 s parent boot, 3.8 s of the worker
guest hashing its 22-tool closure, 3.5 s of the worker building its prompt,
then two model requests (a tool call and the answer) of 20 and 43 s, of
which 9.5 s was re-reading fetched text and 33 s was generating 351 tokens
at 11 tokens a second.

Cut on the host side: the warm template cache held one template, so the
parent and worker templates evicted each other and every start rebooted
both (now up to four, start-to-fork 6.3 s → 0.3 s); the controller
revalidated authority on every pre-start pass and the resolver read each of
the 22 cluster tools with its own uncached API Get, twice per resolution
(now one List, revalidated once before start: 2 s → 30 ms); the AgentRun
controller reconciled one run at a time (now four, so three admissions no
longer queue); the admission ledger took its file lock non-blocking and
reported any contention as `AUTH_CONTEXT_LOST`, which the controller treats
as a lost node and ends the run (now a bounded wait, and contention is
`AUTH_UNAVAILABLE`, a retry). Cut on the model side by the test Agents'
persona alone: fetch live sources with no preamble and in one response, the
Wikipedia plain-text extract instead of the JSON summary, and an answer of
at most 90 words. A solo one-shot now takes about 51 s with a warm cache
and a cached prompt prefix, of which about 45 s is the model; the three
concurrent runs answer in 58 to 85 s (from 113 to 152 s), with the two
conversations continuing as turns on a second ask rather than starting over.

A second shape of the same test, closer to real use (fetch a page, write
its text to a file, write a small parser script, list the workspace, then
report), ran three ways three times: seven of nine cells completed in 99 to
185 s with four or five tool calls each; the other two spent the turn's
five-call budget on a redundant call (a second fetch, a rewrite of the
script) and the guest harness ends the turn when the model asks for more
calls than remain, so the conversation's first turn failed. Two parallel
writes with the same workspace revision are refused and the model retries
with the bumped revision, which is the intended optimistic-concurrency
behaviour. Found and fixed on the way: a turn whose authority no longer
matched the conversation's (the Agent had been replaced) was retried every
five seconds for ever as "PreparationUnconfirmed" (now refused at once as
`RefusedBeforeAdmission`), and a conversation that fails on its first turn
now carries the turn's own reason in its error and through the OpenAI
endpoint, instead of only "cleanup confirmed".

With the provider taken out of the picture (the same tests against DeepSeek:
solo 16.6 s, three-way 18 s, multi-tool 24 s), the guest's share stood out:
pilot took 4.6 s to verify the worker's 24-member closure and the turn
harness another 5.7 s re-reading and re-hashing the same tools before its
first request. A probe in pilot showed why: `read()` on the sealed tool
window (a DAX mount over legacy e820 pmem) runs at four to five megabytes a
second in the guest, steady state, while hashing the same bytes through a
mapping of the file takes a millisecond. Pilot now verifies through a
mapping (24 members in 52 ms), and the harness, told by pilot that the
closure it was exec'd from is verified, skips its own re-read, which the
code already described as diagnostics only. Against DeepSeek a solo
one-shot now takes 8.5 s warm, three concurrent runs 12 s, and the
multi-tool task 13 to 18 s; about 2.5 s of a solo run is Sympozium and
Celln (admission 1.5 s, the rest observation and cleanup), the remainder the
provider and the web. Proven first as a dev starter package on the real
fleet through the same build the release uses, then released as Celln
v0.5.35 and pinned here, so the CI-built starter package carries it.

Left as follow-ups: the per-turn tool-call budget (five calls, six model
requests, a constant of the shared permit contract) is tight for multi-step
work and the guest harness should answer an over-budget request with a tool
error rather than ending the turn; admission could lose most of its 1.5 s by
caching the resolver's reads; the worker hashes every lent tool before it runs (a toolbox profile requires a run to
select all of its tools, so a leaner per-Agent toolbox needs a resolver
design change); the worker spends about 3.5 s building its prompt before the
first request; llama-server runs two slots for three cells and generates at
about 11 tokens a second, so speculative decoding or a third slot on the
operator's server would be the next large saving.

## Decisions (2026-10-22, revised after review)

1. **Pi and Hermes in a cell are deferred** past 0.12 (reversed after review;
   see "Harnesses in a cell").
2. **Private endpoints** stay behind a one-time exact-origin operator approval,
   surfaced in the console as pending (reversed after review).
3. **The privileged fleet installs by default**, with the privilege stated
   plainly at install time and in the docs as part of the security envelope.
   The hostPID/host-root legacy installer is deleted.
4. **Fleet-keyed backends are removed entirely** (breaking, 0.12).
5. **API**: `POST /api/v1/agents/{name}/messages` and the OpenAI-compatible
   per-Agent endpoint, conversation keyed, both in 0.12.
7. **The persona is agent configuration** (`execution.systemPrompt`); the
   fleet catalogue only supplies a default.
6. **All prior Celln model-access work is disposable**, including the legacy
   issuer/review stack.
