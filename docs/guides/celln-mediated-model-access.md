# Agent-bound model access

A Celln Agent's model requests leave its cell for the **model gateway**,
which adds that Agent's own provider key and forwards the request to the
endpoint the Agent's `ModelConnection` names. Celln nodes, guests, run specs
and the controller never see the key. There is no operator route list and no
install-time provider: **the Agent's configuration is the binding between a
key and the running agent**. Any OpenAI-compatible or Anthropic-compatible
endpoint works.

This is how every Celln run gets its model; it is not optional. The
[fleet guide](celln-fleet-installation.md) covers installing the fleet and
the gateway. This guide covers connections, keys, what protects them, and
the operator's controls.

## What protects the key

| Property | Where it is enforced |
| --- | --- |
| **One key, one Agent.** A key Secret is owned by exactly one Agent (or its Ensemble; sub-agents share their parent's). Another Agent is refused | `sympozium.ai/model-key-owner` annotation; the controller's claim before any work, the admission webhook, the API (`409`), and the gateway on every call |
| **Pinned to one origin.** A key records the endpoint origin it is for and is never sent anywhere else. Moving a connection to another origin is refused; a new origin needs a new key entered by a person | `sympozium.ai/model-key-origin` annotation; written when the key is created through the API or first claimed, checked by the API, the controller and the gateway |
| **Never on a node.** The gateway adds the key per request; no node, guest, run spec or controller holds it. Agent pods have no RBAC on Secrets or ModelConnections | gateway; chart RBAC |
| **A run cannot carry or redirect a key.** No inline credential, header, placement or protocol override with a connection; the connection revision is pinned | `modelconnection.Resolve` |
| **Session binding.** One signed permit per run, bound to the cluster, namespace UID, run UID, spec digest, route and budget; turns cannot widen it | controller (issuer), node receiver, gateway |
| **Live re-check on every call.** Namespace UID, connection UID and spec digest, endpoint origin, Secret UID and key, Agent UID, owner and pin. Revocation bites mid-run | gateway `liveRoute` |
| **Network safety.** HTTPS to public addresses by default; private, loopback, link-local, multicast and 100.64/10 refused unless the exact origin is approved; a key never crosses plain HTTP; no redirects; DNS answers validated before connecting; no streaming; provider error bodies hidden; a response that echoes the key is refused | gateway transport and response validation |
| **Budgets** per run and per turn, reserved before each request | gateway + PostgreSQL |

Multi-team isolation is the namespace: a team's keys are Secrets in its
namespace, its Agents are there, and namespace RBAC decides who may write
them. An operator may narrow where any key may go
([Narrowing origins](#narrowing-origins-operator)).

## Connections

A `ModelConnection` is a namespaced route: provider, protocol, endpoint,
models and, optionally, the key Secret.

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: ModelConnection
metadata:
  name: cloud
  namespace: team-a
spec:
  provider: openrouter                 # a free label, 1-64 letters, digits, _ or -
  protocol: openai-chat                # or anthropic-messages
  endpoint: https://openrouter.ai/api/v1/chat/completions   # full HTTPS request URL; a port is fine
  secretRef: cloud-openrouter-key      # the key Secret in this namespace; omit for a keyless endpoint
  # secretKey: API_KEY                 # key inside the Secret; default OPENAI_API_KEY (openai-chat) or ANTHROPIC_API_KEY (anthropic-messages)
  models: [qwen/qwen3-235b-a22b]       # exact model identifiers runs may select (1-128)
  maxOutputTokens: 2048                # optional, 256-4096 per request; 512 when omitted
  # parameters: {chat_template_kwargs: {enable_thinking: false}}   # merged into every request by the gateway
  # allowInsecure: true                # plain HTTP or a private CA, private addresses only, keyless only
```

- `provider` is descriptive. Nothing matches it against a list.
- `protocol` selects the adapter: `openai-chat` sends `Authorization:
  Bearer <key>`; `anthropic-messages` sends `x-api-key` and
  `anthropic-version: 2023-06-01`. Any server speaking either works: OpenAI,
  Anthropic, DeepSeek (`https://api.deepseek.com/chat/completions`),
  OpenRouter, Groq, Mistral, Together, xAI, Gemini's OpenAI-compatible
  endpoint, or a local llama-server, Ollama, LM Studio or vLLM.
- `endpoint` is the complete request URL, HTTPS, with no credentials, query
  or fragment. Its origin (`scheme://host[:port]`) is what the key is pinned
  to.
- `models` are exact names. A key may be used for any model its provider
  serves; the signed decision still binds each run to one concrete model.
- `parameters` is pinned on the host side: the guest never sees it, a guest
  request that sets one of its keys is refused, and the fields the gateway
  owns (`model`, `messages`, `max_tokens`, `stream`, `tools`, …) cannot be
  set. At most 16 top-level keys matching `^[a-z][a-z0-9_]{0,63}$`, 3 levels
  deep, 2048 bytes. Use it for provider switches such as turning a reasoning
  phase off.
- `maxOutputTokens` bounds what one request may ask for; the gateway refuses a
  request above it. A turn reserves 6 × this value from the run's budget, so
  a connection created through the API is clamped to what the namespace's
  policy ceilings can pay for.

## Keys

The key is a Secret in the Agent's namespace, under the connection's
`secretKey` (default by protocol: `OPENAI_API_KEY` or `ANTHROPIC_API_KEY`;
`API_KEY` also works when named). Only the gateway reads it.

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: cloud-openrouter-key
  namespace: team-a
stringData:
  OPENAI_API_KEY: "<your key>"
```

Two annotations on the Secret carry the binding:

| Annotation | Value | Written |
| --- | --- | --- |
| `sympozium.ai/model-key-owner` | `Agent/<name>` or `Ensemble/<name>` | by the controller the first time a run of an Agent that grants the Secret uses it (the claim) |
| `sympozium.ai/model-key-origin` | the endpoint origin, e.g. `https://openrouter.ai` | by the API when it creates or re-saves a connection naming the Secret, or by the controller at the claim (a Secret made by hand is pinned on first use) |

Rules:

- **Grant.** An Agent grants a connection's Secret by selecting the
  connection in `spec.execution.modelConnectionRef`, or by listing the Secret
  in `spec.authRefs` (with the connection's `provider`, or an empty provider
  for any). A run that names a Secret the Agent neither grants nor owns is
  refused: `model key Secret "x" is not declared in Agent "a" spec.authRefs`.
- **One owner.** An Agent whose connection names a Secret another live Agent
  or Ensemble owns is refused: `model key Secret "x" belongs to Agent/other;
  every Agent needs its own key (sub-agents share their parent's)`. The
  console does not offer such a Secret, and
  `GET /api/v1/celln-platform/key-secrets?namespace=<ns>&key=OPENAI_API_KEY`
  lists only unowned Secrets that hold that key name (never a value). When
  the owner is deleted its claim goes stale and the next Agent that grants
  the Secret takes it over.
- **One origin.** A Secret pinned to one origin and named by a connection to
  another is refused whoever owns it: `model key Secret "x" is pinned to
  https://openrouter.ai and cannot be sent to https://attacker.example; a
  connection to another provider needs its own key`. The API answers `409
  Conflict` when a connection is saved that way; the controller fails the
  run before anything starts; the gateway refuses the call
  (`MODEL_CREDENTIAL_SOURCE_CHANGED`) if the pin and the live connection
  disagree. The pin outlives the owner; to re-use a Secret for another
  provider on purpose, a person with write access to the Secret removes the
  annotation (or, better, creates a new Secret).
- **HTTPS only.** A connection with a `secretRef` must have an `https://`
  endpoint: the resolver refuses `credential-bearing model endpoint must use
  HTTPS` and the gateway refuses `MODEL_DESTINATION_FORBIDDEN`. Plain HTTP
  is for keyless local endpoints only.

## Create an Agent through the API

Everything below ran against a real cluster
(`test/integration` keeps the journey; the shapes are the API server's). The
namespace must be one the fleet admits (any ordinary namespace by default).

```sh
kubectl -n sympozium-system port-forward svc/sympozium-apiserver 18080:8080 &
API=http://localhost:18080
TOKEN="$(sympozium token)"
H=(-H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json")
NS=team-a
```

**The short way: one call.** `modelConnection` on the Agent create carries the
connection spec and, when the provider wants one, the key. The server writes
the Secret (pinned to the endpoint's origin and owned by this Agent from the
moment it exists), the `ModelConnection` (named after the Agent unless
`modelConnection.name` says otherwise), picks the namespace's fleet profile,
creates its toolbox runtime wrapper and sizes the conversation limits from
the policy's session defaults. Anything you set explicitly (`runtimeRef`,
`execution.cellnSelection`, `execution.enduring`, `model`) is kept.

```sh
curl -sf "${H[@]}" -X POST "$API/api/v1/agents?namespace=$NS" -d '{
  "name": "researcher",
  "execution": {"backend": "celln"},
  "modelConnection": {
    "apiKey": "sk-or-...",
    "spec": {"provider": "openrouter", "protocol": "openai-chat",
             "endpoint": "https://openrouter.ai/api/v1/chat/completions",
             "models": ["qwen/qwen3-235b-a22b"]}}}'
```

Then talk to it. `POST /api/v1/agents/{name}/messages` continues the Agent's
newest live conversation (or the run named by `conversation`) as a turn, and
starts one when there is none; it answers `201` with the new run or `202`
with the turn (`200` for a repeated `requestId`). The answer lands on that
object's status (`.status.result` on the run,
`.status.execution.result.answer` on the turn), which you poll or watch as
for any run.

```sh
curl -sf "${H[@]}" -X POST "$API/api/v1/agents/researcher/messages?namespace=$NS" \
  -d '{"message": "Reply with the single word pong."}'
# {"run": {...}, "started": true}
curl -sf "${H[@]}" -X POST "$API/api/v1/agents/researcher/messages?namespace=$NS" \
  -d '{"message": "And now in French.", "requestId": "fr-1"}'
# {"run": {...}, "turn": {...}, "started": false}
```

A namespace no fleet serves gets `409 no Celln fleet profile is offered …`;
a key another Agent already owns gets `409`; a key over plain HTTP, `400`.

**The persona is the Agent's.** `execution.systemPrompt` on the Agent is the
system prompt every run it starts carries into its cell; a run may name its
own instead, and an Agent that names none gets the fleet's default persona
(the one the starter catalogue ships, which tells the model to fetch a live,
dated source before answering anything time-sensitive). It is agent
configuration, editable on the Agent (console: Execution defaults → Persona,
or `PATCH /api/v1/agents/{name}` with `execution`), not fleet material. A cell
accepts at most 2048 bytes of system prompt; a longer one is refused with
that bound named.

```sh
curl -sf "${H[@]}" -X POST "$API/api/v1/agents?namespace=$NS" -d '{
  "name": "analyst",
  "execution": {"backend": "celln",
                "systemPrompt": "You are a market analyst. Fetch https://news.google.com/rss/search?q=<topic> before answering about recent events and cite dates."},
  "modelConnection": {"spec": {"provider": "openrouter", "protocol": "openai-chat",
                               "endpoint": "https://openrouter.ai/api/v1/chat/completions",
                               "models": ["qwen/qwen3-235b-a22b"]}, "apiKey": "sk-or-..."}}'
```

**Any OpenAI client.** The same Agent is an OpenAI-compatible endpoint at
`/api/v1/agents/{name}/v1`. `POST …/chat/completions` sends the newest user
message to the Agent (as above: a turn of its live conversation, or a new
one) and waits for the answer, returned as an ordinary completion, or as SSE
chunks with `stream: true`; `GET …/models` lists the Agent as its one model.
The Sympozium token is the API key, and the `X-Sympozium-Namespace` header
names the namespace, since SDKs cannot add a query parameter (the header
works on every API route; `?namespace=` wins when both are given). Earlier
messages in the request are not replayed: the Agent holds its own
conversation. A conversation that is still starting, or answering an
earlier message, takes the message when it is ready. `?timeout=` (default
`3m`, at most `10m`) bounds the whole wait; past it the reply is `504` naming
the run and turn to read, and the conversation continues. `X-Sympozium-Conversation: <run>` continues a named
run instead of the newest. The completion's `sympozium` field and the
`X-Sympozium-Run` / `X-Sympozium-Turn` headers name the objects behind it.

```python
from openai import OpenAI
client = OpenAI(base_url=f"{API}/api/v1/agents/researcher/v1", api_key=TOKEN,
                default_headers={"X-Sympozium-Namespace": NS})
reply = client.chat.completions.create(model="researcher",
    messages=[{"role": "user", "content": "Reply with the single word pong."}])
print(reply.choices[0].message.content)   # pong
```

A Kubernetes Agent on a session runtime (Pi, Hermes) has the same endpoint;
its request is handed to the running session, history and streaming
included. The console shows the base URL and a snippet on each Agent.

**The long way** is what that call does, step by step.

**1. The connection, with its key.** `apiKey` makes the server write the
Secret (`<connection>-<provider>-key`, labelled
`sympozium.ai/model-connection: <connection>`) and pin it to the endpoint's
origin; or pass `spec.secretRef` to an existing Secret, which is pinned the
same way. Re-posting the same name updates the connection.

```sh
curl -sf "${H[@]}" -X POST "$API/api/v1/model-connections?namespace=$NS" -d '{
  "name": "cloud",
  "apiKey": "sk-or-...",
  "spec": {"provider": "openrouter", "protocol": "openai-chat",
           "endpoint": "https://openrouter.ai/api/v1/chat/completions",
           "models": ["qwen/qwen3-235b-a22b"]}}'
kubectl -n $NS get secret cloud-openrouter-key -o jsonpath='{.metadata.annotations}'
# {"sympozium.ai/model-key-origin":"https://openrouter.ai"}
```

Moving it to another origin is refused:

```sh
curl -s -o /dev/null -w '%{http_code}\n' "${H[@]}" -X POST "$API/api/v1/model-connections?namespace=$NS" -d '{
  "name": "cloud",
  "spec": {"provider": "openrouter", "protocol": "openai-chat",
           "endpoint": "https://attacker.example/v1/chat/completions",
           "models": ["qwen/qwen3-235b-a22b"], "secretRef": "cloud-openrouter-key"}}'
# 409
```

**2. The runtime.** The fleet offers one profile per scope; ask the namespace
what it may use and have the runtime wrapper created on first use
(`runtimeOnly: true`; existing objects are never modified). The `.toolbox`
profile lends the starter toolbox; the plain profile is chat only.

```sh
curl -sf "${H[@]}" "$API/api/v1/celln-platform/profiles?namespace=$NS"
# [{"name":"celln-native-starter","wrapper":"celln-native","mediationOnly":true,
#   "tools":[...],"ceilings":{...},"sessionDefaults":{...},
#   "toolboxProfile":"celln-native-starter.toolbox","toolboxWrapper":"celln-native.toolbox","toolboxTools":[...]}]
curl -sf "${H[@]}" -X POST "$API/api/v1/celln-platform/wrappers?namespace=$NS" \
  -d '{"profile": "celln-native-starter.toolbox", "runtimeOnly": true}'
# {"backend":"native","runtime":"celln-native.toolbox","agent":"","connection":"","created":["celln-native.toolbox"]}
```

**3. The Agent.** `execution.modelConnectionRef` is the grant: the server
resolves the connection, adds its Secret to the Agent's `authRefs`, and
refuses `apiKey`, `secretName` or `baseURL` alongside it. `cellnSelection`
selects the wrapper and exactly the toolbox's tools in its order (copy
`toolboxTools` from the profile; omit them, on `celln-native`, for a chat-only
Agent). `enduring` is the budget a conversation asks for, within the
profile's `ceilings`; `sessionDefaults` is a sensible one.

```sh
TOOLS="$(curl -sf "${H[@]}" "$API/api/v1/celln-platform/profiles?namespace=$NS" \
  | python3 -c 'import sys,json; print(json.dumps(json.load(sys.stdin)[0]["toolboxTools"]))')"
curl -sf "${H[@]}" -X POST "$API/api/v1/agents?namespace=$NS" -d "{
  \"name\": \"researcher\", \"provider\": \"openrouter\", \"model\": \"qwen/qwen3-235b-a22b\",
  \"runtimeRef\": \"celln-native.toolbox\",
  \"execution\": {\"backend\": \"celln\", \"executionLifecycle\": \"enduring\",
                  \"modelConnectionRef\": \"cloud\", \"model\": \"qwen/qwen3-235b-a22b\",
                  \"cellnSelection\": {\"runtimeRef\": \"celln-native.toolbox\", \"toolRefs\": [], \"clusterToolRefs\": $TOOLS},
                  \"enduring\": {\"leaseSeconds\": 3600, \"maxTurns\": 8, \"maxModelRequests\": 48, \"maxOutputTokens\": 196608}}}"
```

**4. A run, and its turns.**

```sh
RUN="$(curl -sf "${H[@]}" -X POST "$API/api/v1/runs?namespace=$NS" -d "{
  \"agentRef\": \"researcher\", \"task\": \"Reply with the single word pong.\",
  \"backend\": \"celln\", \"executionLifecycle\": \"enduring\",
  \"modelConnectionRef\": \"cloud\", \"model\": \"qwen/qwen3-235b-a22b\",
  \"cellnSelection\": {\"runtimeRef\": \"celln-native.toolbox\", \"toolRefs\": [], \"clusterToolRefs\": $TOOLS},
  \"enduring\": {\"leaseSeconds\": 3600, \"maxTurns\": 8, \"maxModelRequests\": 48, \"maxOutputTokens\": 196608}}" \
  | python3 -c 'import sys,json; print(json.load(sys.stdin)["metadata"]["name"])')"
kubectl -n $NS get agentrun "$RUN" -o jsonpath='{.status.phase} {.status.result}{"\n"}'
UID="$(kubectl -n $NS get agentrun "$RUN" -o jsonpath='{.metadata.uid}')"
curl -sf "${H[@]}" -X POST "$API/api/v1/runs/$RUN/turns?namespace=$NS" \
  -d "{\"message\": \"And now in French.\", \"requestId\": \"fr-1\", \"runUID\": \"$UID\"}"
curl -sf "${H[@]}" "$API/api/v1/runs/$RUN/turns?namespace=$NS"
```

`executionLifecycle: one-shot` answers the task once and stops the parent;
the console's **Answer once** does the same. `kubectl apply` of the same
objects works too: the Secret and `ModelConnection` above, the `AgentRuntime`
the wrapper call created (copy it with `kubectl -n $NS get agentruntime
celln-native.toolbox -o yaml`), and an `Agent` with `spec.runtimeRef`,
`spec.authRefs: [{provider: openrouter, secret: cloud-openrouter-key}]` and
the same `spec.execution`. The Secret is then pinned and claimed by the
controller on the first run.

### Keyless local models

A local OpenAI-compatible server needs no key. Its address is private, so
the operator approves the exact origin once
([Private and local endpoints](#private-and-local-endpoints)); the
connection then needs no `secretRef` and, for plain HTTP, `allowInsecure`:

```sh
curl -sf "${H[@]}" -X POST "$API/api/v1/model-connections?namespace=$NS" -d '{
  "name": "local",
  "spec": {"provider": "llama-server", "protocol": "openai-chat",
           "endpoint": "http://192.168.1.237:8080/v1/chat/completions",
           "models": ["Qwen3.8-27B-UD-Q4_K_XL.gguf"], "allowInsecure": true,
           "parameters": {"chat_template_kwargs": {"enable_thinking": false}}}}'
```

The gateway still binds the session, enforces budgets and parameters and
validates the response; it just sends no credential. Reasoning models served
by llama-server can spend a whole request thinking and return an empty
answer; the `parameters` above turn that off, or raise `maxOutputTokens`.

## Narrowing origins (operator)

By default an Agent's own key may go to any public HTTPS origin the gateway
reaches. The scope's `CellnExecutionPolicy` (`celln-fleet-<scope>`) may
narrow that with host patterns:

```yaml
spec:
  allowedOrigins: ["api.openai.com", "*.anthropic.com", "llm.internal:8443"]   # empty: any origin
  deniedOrigins: ["*.example.net"]                                              # refused even if allowed
```

A pattern is an exact lowercase host with an optional port, or `*.` over
subdomains (`*.example.com` matches `api.example.com` and `example.com`,
never `notexample.com`); at most 64 each. The resolver checks the
connection's origin against every policy that selects the namespace when a
run is admitted and again before each turn, and refuses with
`AUTH_ROUTE_MISMATCH`: `policy "celln-fleet-starter" denies model requests
to https://x (pattern)` or `policy "…" allows model requests only to a, b,
not https://x`.

`sympozium doctor` (**Mediated model access**) summarises the guardrail, and
the console reads it through the mediation endpoint:

```sh
curl -sf "${H[@]}" "$API/api/v1/celln-platform/mediation?namespace=$NS"
# {"enabled":true,"allowedOrigins":[],"deniedOrigins":[],"policies":["celln-fleet-starter"],"routes":[],"pending":[]}
```

`routes` and `pending` are always empty; they remain for 0.11 clients.

## Private and local endpoints

The gateway sees only an IP, so it cannot tell a LAN model server from a
pod or Service address. The rules:

- **Public HTTPS** (any address that is not private, loopback, link-local,
  multicast or 100.64/10): reachable by default. Every DNS answer is checked
  before connecting; mixed public/private answers fail closed; redirects are
  refused.
- **A private destination** (RFC 1918, IPv6 ULA, 100.64/10, loopback) needs
  its exact origin in `modelGateway.privateOrigins`, once for the cluster:

  ```sh
  sympozium install --set 'modelGateway.privateOrigins[0]=http://192.168.1.237:8080'
  ```

  Until then a run to it fails at the gateway with
  `MODEL_DESTINATION_FORBIDDEN`. A listed HTTPS origin is reachable without
  the connection setting `allowInsecure`.
- **Plain HTTP** to a listed origin additionally needs `allowInsecure: true`
  on the connection, and works only for keyless connections (a Secret never
  crosses HTTP). Its DNS answers must all be loopback, private or 100.64/10
  addresses.
- **A private CA.** For an in-house HTTPS server, put the CA in a ConfigMap
  in the gateway's namespace and name it in `modelGateway.providerCA`
  (`configMap`, `key: ca.crt`); it is trusted only for listed origins, and
  public providers never trust it. If you restrict `modelGateway.egress`,
  include the server's address.

Celln's own tools (`https-fetch`, `https-post-json`) never reach a private
address, whatever the model connection allows.

## Refusals

Every refusal names a stable reason. Where a run is refused before it starts,
the reason is on the `AgentRun`'s conditions and the run fails; where a model
call is refused, the gateway logs one line (no credential, route or request
content) and the turn fails:

```text
model-gateway: refused status=403 reason=MODEL_CREDENTIAL_SOURCE_CHANGED op=invoke claimed_run=team-a/researcher-x7k2p claimed_agent=researcher claimed_turn=researcher-x7k2p-recall
```

The controller refuses a run's model key the same way on every backend (Job,
Agent Sandbox, Celln), before any work: the run fails with the message as
`status.error` and the `ModelKeyAuthorized` condition set to `False` with one
of the `MODEL_KEY_*` codes below as its reason. The API and webhook refuse
the same cases with `409` and the same message.

| Reason | Meaning |
| --- | --- |
| `MODEL_KEY_NOT_GRANTED` | The run names a key (or a harness model credential) its Agent does not declare in `spec.authRefs`: `model key Secret … is not declared in Agent … spec.authRefs` |
| `MODEL_KEY_CONFLICT` | The key belongs to another Agent: `model key Secret … belongs to …` |
| `MODEL_KEY_ORIGIN_MISMATCH` | The key is pinned to another endpoint origin: `model key Secret … is pinned to … and cannot be sent to …` |
| `AUTH_ROUTE_MISMATCH` | The connection cannot be the route: a policy's `allowedOrigins`/`deniedOrigins` refuses its origin, a keyed endpoint is not HTTPS, the connection is disabled or malformed, or the run's model is not among its `models` |
| `AUTH_LIMIT_OUT_OF_RANGE` | `spec.enduring` asks for more than the policy's ceilings, or cannot pay for one turn at the connection's `maxOutputTokens` |
| `AUTH_TOOL_ORDER_MISMATCH`, `AUTH_TOOL_UNKNOWN` | `clusterToolRefs` is not exactly the toolbox's tools in its order |
| `AUTH_POLICY_WITHDRAWN` | The namespace is not admitted by any policy |
| `AUTH_PROTOCOL_UNSUPPORTED` | The node runs a Celln release without a contract the run needs |
| `AUTH_CONTEXT_LOST` | The node holding the run's native state is gone; the run ends and nothing is re-placed |
| `MODEL_AUTH_UNAUTHORIZED`, `MODEL_AUTH_FORBIDDEN` | The permit does not verify, or the request widens it (a pinned parameter, an unregistered run, output tokens outside the connection's bound or the turn's cap) |
| `MODEL_ROUTE_CHANGED` | The live connection no longer matches the decision: UID, digest, endpoint origin, provider, protocol, model list, or the namespace changed or is being deleted |
| `MODEL_CREDENTIAL_SOURCE_CHANGED` | The Secret, its key, its UID, its owner or its pin no longer match; or the Agent is gone or has another UID |
| `MODEL_DESTINATION_FORBIDDEN` | Private, loopback, link-local or unlisted private address; plain HTTP with a key; a redirect |
| `MODEL_STREAMING_UNSUPPORTED`, `MODEL_PROTOCOL_UNSUPPORTED`, `MODEL_REQUEST_MALFORMED` | Request shape the gateway does not forward |
| `MODEL_REQUEST_CONFLICT`, `MODEL_RESPONSE_TOO_LARGE`, `MODEL_PROVIDER_UNAVAILABLE`, `MODEL_GATEWAY_UNAVAILABLE` | Duplicate request id, oversized or invalid provider response, provider unreachable, gateway or its database not ready |
| `AUTH_BUDGET_EXHAUSTED`, `AUTH_BUDGET_CLOSED`, `AUTH_WORK_DEADLINE_EXPIRED`, `AUTH_BUDGET_NOT_REGISTERED`, `AUTH_PROVIDER_USAGE_EXCEEDS_RESERVATION` | Budget accounting: the run or turn has no reservation left, is closed, past its deadline, unregistered, or the provider reported more usage than was reserved |

A run whose node receiver rejects the controller's token reports
`ReceiverUnavailable` (`RECEIVER_AUTH_REJECTED`); one that cannot reach it
over trusted TLS reports `RECEIVER_UNREACHABLE`. Both wait rather than fail,
because nothing was submitted.

## Budgets

A run's `spec.enduring` (`leaseSeconds`, `maxTurns`, `maxModelRequests`,
`maxOutputTokens`) is admitted within the scope's policy ceilings. Every turn
reserves 6 model requests and 6 × the connection's `maxOutputTokens` output
tokens from the run's totals before its first request, and the gateway
reserves each request's full output bound before forwarding it; unused
reservation is reconciled from the provider's reported usage. A run whose
output tokens run out on a healthy node reports `Uncertain` and does not end
by itself; size `spec.enduring` for the turns you expect
(`sessionDefaults` in the profiles API is computed from the ceilings). The
ceilings themselves are set at install
([Leases and budgets](celln-fleet-installation.md#leases-and-budgets)).

## Tools on this path

The toolbox does not depend on the model route. An Agent on the `.toolbox`
profile gets the workspace tools (read, write, list, append, search, delete;
files live with the parent and end with it), `https-fetch` and
`https-post-json` to any public HTTPS host (or the scope's
`--celln-fleet-https-host` list), and the borrowed commands, each with the
limits the signed decision carries. See
[The toolbox](celln-fleet-installation.md#the-toolbox).

## How it is wired

`sympozium install` sets `celln.mediation.enabled`, `clusterId` (the
`kube-system` namespace UID), `issuer.keyId` and `modelGateway.image` on
every run, and bootstraps the trust right after the chart creates its
namespaces. The trust is never rendered from Helm values:

| Object | Namespace | Keys | Read by |
|---|---|---|---|
| Secret `celln-mediation-controller` | control plane | `issuer.key` (PKCS#8 Ed25519 PEM), `receiver-token`, `gateway-token` | controller |
| Secret `celln-mediation-gateway` | control plane | `tls.crt`, `tls.key`, `registration-token` (= `gateway-token`) | gateway |
| Secret `celln-mediation-node` | `celln-system` | `operator-token` (= `receiver-token`), `tls.crt`, `tls.key` | dispatcher, receiver sidecar |
| ConfigMap `celln-mediation-trust` | both | `jwks.json`, `ca.crt` | all three |

Against the same issuer (`sympozium-control-plane`), keyset, CA and tokens:
the controller signs one permit per run and talks to the
`celln-scoped-receiver` Service (the router's TLS sidecar, which forwards
each `/v1/scoped/*` operation to the node that prepared it); the `celln-node`
dispatchers verify the permit and reach the gateway on 8443; the gateway
verifies it again, registers the run and serves its model calls. A rerun of
the installer reads the existing key id back, verifies the objects and
changes nothing. The chart installs the record ConfigMap
`celln-system/celln-mediation`, which `doctor` and the API read.

For a Helm-only install the same objects come from
`sympozium celln-mediation bootstrap --cluster-id <id> --values-out
mediation-values.yaml` (flags: `--system-namespace`, `--release-fullname`,
`--gateway-host`, `--receiver-host`, `--validity`), whose output carries the
credential-free values to pass to `helm upgrade`.

### Rotation

Certificates last ten years (`--validity`) and the CA key is discarded after
signing, so nothing renews them in place: rotation is the renewal.
`sympozium doctor` warns in the last 60 days and fails once they have
expired. To rotate, delete the five objects (the four above; the trust
ConfigMap exists in both namespaces) deliberately, run `sympozium install`
(or `celln-mediation bootstrap`), and restart the controller, the model
gateway and the `celln-node` pods. The controller and gateway pods carry a
`checksum/mediation-trust` annotation, so the next `helm upgrade` or
`sympozium upgrade` rolls them onto the new trust; trust recreated outside
Helm needs `kubectl -n sympozium-system rollout restart
deploy/sympozium-controller-manager deploy/sympozium-model-gateway`. Until
then runs report `RECEIVER_AUTH_REJECTED`. Live conversations end with the
`celln-node` restart.

### PostgreSQL

The gateway keeps budgets and registrations in PostgreSQL and refuses to
start without it. With `modelGateway.database.secretName` empty the chart
runs its own single-pod PostgreSQL (`<release>-model-gateway-db`, a
StatefulSet on a 1 Gi PVC, digest-pinned `postgres:17-alpine`; password
generated once and kept across upgrades; a NetworkPolicy admits only the
gateway). It holds accounting, never provider keys. For production, apply
`migrations/002_celln_model_budget.sql` and
`migrations/003_celln_model_gateway.sql` to your own database and set
`modelGateway.database.secretName` to a Secret holding the URL under
`database-url`; the chart then renders no database. If you restrict
`modelGateway.egress`, include the database's address.

### Gateway RBAC and egress

By default the gateway keeps a cluster-wide `get` on Secrets,
ModelConnections and Namespaces. `modelGateway.namespaces` replaces that
with one Role/RoleBinding per listed namespace (and sets the gateway's
readiness probe to exactly that access); add a namespace before its Agents
run. `modelGateway.egress` is the gateway's NetworkPolicy egress: empty
allows any destination (an Agent may use any provider); a list restricts it.

### Current limits

- **Node loss ends a run** (`AUTH_CONTEXT_LOST`); nothing is re-placed on
  another node. Workspace files live with the parent and end with it.
- **A dispatcher restart loses live parents**, including the roll caused by
  a fleet package move or a trust rotation.
- **No streaming.** The gateway forwards complete request and response
  envelopes.
- Secret volumes cannot be owned by a non-root user, so a non-root init step
  in the controller and gateway pods copies the operator's files into an
  in-memory volume with mode `0400`; a changed Secret takes effect on the
  next pod restart.
- NetworkPolicies need an enforcing CNI. The gateway admits the controller
  and the `celln-node` pods on 8443; `celln-node` admits the controller on
  the receiver port only, and its plaintext 8787 stays router-only.
