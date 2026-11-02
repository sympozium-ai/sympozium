# Model connections for persistent harnesses

`ModelConnection` is a namespaced, reusable model route. An Agent references it
through `spec.execution.modelConnectionRef`; the wizard writes the same field.
Provider selection is separate from runtime and execution lifecycle selection.
Creating a connection describes intent. Native Celln still requires independent
host model/tool/runtime admission.

## Kubernetes: Hermes and Pi

Use `protocol: openai-chat` with an OpenAI compatible provider or gateway. The
maintained Hermes adapter consumes this protocol. Direct Anthropic Messages is
supported by the companion native Celln host adapter, not by this Hermes adapter.
Other provider protocols need a compatible gateway or a corresponding harness
adapter. A provider label alone is not a protocol adapter.

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: ModelConnection
metadata:
  name: framework-llama
  namespace: default
spec:
  provider: llama-server
  protocol: openai-chat
  endpoint: http://192.168.1.237:8080/v1/chat/completions
  models: [Qwen3.8-27B-UD-Q4_K_XL.gguf]
```

The endpoint may also be the API base URL, such as `http://host:8080/v1`.
The controller removes a trailing `/chat/completions` before passing the base URL
to the harness. Kubernetes network policy must permit the chosen endpoint port;
the current session policy permits 443, 8080, and 9473.

For an authenticated endpoint, the wizard's Auth step accepts an API key and
writes a Kubernetes Secret, then records its name in `spec.secretRef`. Use the
credential environment keys required by the selected adapter (Hermes uses
`OPENAI_API_KEY`). The connection contains no credential values. For an
unauthenticated endpoint, leave the key blank. The controller supplies an inert
`OPENAI_API_KEY` placeholder because the SDK/adapter requires a nonempty setting
even when the server does not authenticate. It creates no Secret.

See [the complete Hermes example](https://github.com/sympozium-ai/sympozium/blob/main/config/samples/hermes-framework-connection.yaml)
for the Agent and HarnessSession manifests. API/wizard Agent creation creates
`<agent>-chat` automatically; declarative creation also requires a HarnessSession.

In the wizard choose **Kubernetes → Hermes**, then use the ordinary
**Provider → Auth → Model** steps. The wizard persists that selection as a
`<agent>-connection` ModelConnection, so creating a route is identical to
creating a one-shot run. Providers that are not OpenAI-compatible are hidden for
a Kubernetes harness; connect them through a gateway. A run against an Agent
with a connection inherits the connection's model route.

The same steps apply to **Celln**, with the same provider list as the run/agent
flow: the Auth step takes the Agent's own API key, which becomes a Secret
owned by that Agent and pinned to the endpoint's origin. A keyed Celln
connection needs an HTTPS endpoint speaking `openai-chat` or
`anthropic-messages`; a keyless local server may use HTTP on a private
address the operator has approved. See [Native Celln](#native-celln).

The API exposes `GET /api/v1/model-connections` and
`POST /api/v1/model-connections` (body `{name, spec, apiKey?}`). When `apiKey`
is supplied the server creates the Secret and sets `spec.secretRef`; re-posting
the same name updates the connection. Requests use the standard namespace query
parameter and API authentication. Connections can be changed or disabled
through the Kubernetes API. The listing reports configuration, not a claim of
provider reachability or model/tool compatibility.

## Session identity and changes

Before starting a persistent Kubernetes session, the controller pins the
connection UID, complete spec, and selected model in a session annotation.
Changing or deleting that connection stops the session workload on dependency
reconciliation; its PVC remains. A different route requires a new session, so a
conversation is not silently redirected to another provider. Disabling a
connection also prevents new resolution. Connection updates do not rewrite
admin-owned AgentRuntime objects.

Secret values can rotate under the same reference. Existing pod environment
variables are snapshots, so restart the session workload to pick up a rotated
Secret. Revoking a connection and rotating a Secret are different operations.

## Native Celln

On Celln the connection **is** the model route: there is no operator route
list and no fleet-held credential. A keyed connection names a Secret in the
Agent's namespace; a keyless one names none.

```yaml
spec:
  provider: anthropic
  protocol: anthropic-messages
  endpoint: https://api.anthropic.com/v1/messages
  secretRef: my-anthropic-key     # key ANTHROPIC_API_KEY, or the key named by secretKey
  models: [your-model-id]
  maxOutputTokens: 4096           # optional, 256-4096 per request; 512 when omitted
```

A run is never provisioned with the key. The controller signs a permit that
binds the connection (UID, spec digest, endpoint origin, model), and the model
gateway adds the key from the Secret on every call after re-checking the live
connection, Secret, Agent, owner and origin pin. Three things must hold:

- **The Agent grants the Secret**: it selects the connection in
  `spec.execution.modelConnectionRef`, or lists the Secret in `spec.authRefs`
  (an empty `provider` grants it for any provider). The Secret is owned by
  exactly one Agent (`sympozium.ai/model-key-owner`) and pinned to one
  endpoint origin (`sympozium.ai/model-key-origin`); another Agent, or a
  connection to another origin, is refused (`MODEL_KEY_NOT_GRANTED`,
  `MODEL_KEY_CONFLICT`, `MODEL_KEY_ORIGIN_MISMATCH`; see
  [Refusals](celln-mediated-model-access.md#refusals)).
- **The policy admits the origin**: the scope's `CellnExecutionPolicy` may
  list `allowedOrigins`/`deniedOrigins` host patterns; empty admits any
  origin the gateway reaches. A keyed endpoint must be HTTPS; a private
  address needs `modelGateway.privateOrigins`. Refusals are
  `AUTH_ROUTE_MISMATCH`.
- **The run's budget pays for a turn**: a turn reserves 6 model requests
  times `maxOutputTokens` output tokens, so `spec.enduring.maxOutputTokens`
  and the policy ceiling must be at least that; otherwise admission is
  refused with the numbers (`AUTH_LIMIT_OUT_OF_RANGE`). A connection created
  through the API is clamped to what the namespace's policy can pay for.

The Agent's `execution` defaults carry `modelConnectionRef`, `model`, the
Celln selection, lifecycle and the enduring budget. Runs may select
`spec.model.connectionRef` and `spec.model.model` explicitly. Resolution pins
the connection revision and fills the model route before freezing host
admission; an unresolved or changed connection refuses admission. Connections
cannot be combined with inline credentials, headers, placement or thinking
settings.

The full flow, through the API and `kubectl`, is in
[Agent-bound model access](celln-mediated-model-access.md).

## Live Hermes acceptance test

Deploy the updated CRDs, controller, API server/UI, and modelconnection RBAC.
Then run:

```bash
LLAMA_SERVER_URL=http://192.168.1.237:8080/v1 \
KEEP_RESOURCES=1 \
make test-hermes-model-connection
```

The test creates its own persistent Hermes runtime and connection, creates an
Agent through the API, checks actual deployment model wiring, sends a real
prompt, replaces the pod, checks the same PVC and conversation recall, and
repeats creation/prompt through declarative resources. It writes JSON evidence
to `/tmp/<test-name>-evidence`. No mocked model or fabricated response is used.
Override `TEST_NAMESPACE`, `TEST_MODEL`, `TEST_NAME`, `TEST_TIMEOUT`, or
`TEST_EVIDENCE_DIR` as needed. Resources are cleaned by default; `KEEP_RESOURCES=1`
leaves them available for manual chat.
