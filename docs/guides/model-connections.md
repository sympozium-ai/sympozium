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

See [the complete Hermes example](../../config/samples/hermes-framework-connection.yaml)
for the Agent and HarnessSession manifests. API/wizard Agent creation creates
`<agent>-chat` automatically; declarative creation also requires a HarnessSession.

In the wizard choose **Kubernetes → Hermes**, then use the ordinary
**Provider → Auth → Model** steps. The wizard persists that selection as a
`<agent>-connection` ModelConnection, so creating a route is identical to
creating a one-shot run. Providers that are not OpenAI-compatible are hidden for
a Kubernetes harness; connect them through a gateway. A run against an Agent
with a connection inherits the connection's model route.

The same steps apply to **Celln**, with the same provider list as the run/agent
flow. The Auth step collects a host credential profile instead of an API key.
Native Celln requires an HTTPS endpoint speaking `openai-chat` or
`anthropic-messages`; point a local provider at an HTTPS gateway, or use a
custom HTTPS endpoint. Declarative Agents can still use the legacy DeepSeek host
route by omitting a connection.

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

Native connections served by the fleet's own credential require a full HTTPS
endpoint, an opaque `credentialProfile`, and either `openai-chat` or
`anthropic-messages`. They cannot use the HTTP/private-network llama-server route
above: the native host transport retains its public HTTPS boundary.

```yaml
spec:
  provider: anthropic
  protocol: anthropic-messages
  endpoint: https://api.anthropic.com/v1/messages
  credentialProfile: team-anthropic
  models: [your-model-id]
```

### An Agent's own key (gateway-mediated)

A shared-catalogue run (a platform wrapper runtime, cluster tools) may instead
select a connection that names the namespace's own Secret:

```yaml
spec:
  provider: anthropic
  protocol: anthropic-messages
  endpoint: https://api.anthropic.com/v1/messages
  secretRef: my-anthropic-key
  models: [your-model-id]
  maxOutputTokens: 4096   # optional, 256-4096 per request; 512 when omitted
```

Such a run is never provisioned with the key. The controller picks the path from
the connection: `credentialProfile` is provisioned on the fleet as before, while
`secretRef` (or no credential) is executed by the scoped receiver, and the model
gateway injects the key from the Secret it pinned. Without a configured scoped
receiver the run is held with condition `CellnScopedExecution` /
`ScopedDispatchDisabled`; it never falls back. Three things must all hold:

- The operator's `CellnExecutionPolicy` lists an `auth: secret` route with the
  connection's exact provider, protocol, HTTPS origin and model. Routes are the
  operator's allow-list and are matched exactly (no wildcards); a connection is
  never its own authorisation.
- The Agent grants the Secret: it is listed in the Agent's `spec.authRefs` (an
  empty `provider` grants it for any provider), or the Agent's
  `spec.execution.modelConnectionRef` names this connection. A run cannot borrow
  another Secret-backed connection in its namespace.
- The run's budget pays for a turn. With `maxOutputTokens: N` a turn reserves
  the worker's model requests (6 on the starter package) times N output tokens,
  so `spec.enduring.maxOutputTokens` and the policy ceiling must be at least
  that; otherwise admission is refused with the numbers (`AUTH_LIMIT_OUT_OF_RANGE`).

The run may use the same runtime wrapper as the fleet's backend: only a
`credentialProfile` connection is bound to the runtime profile's installed
credential.

The Agent's native `execution` defaults contain `modelConnectionRef`, `model`,
the Celln selection, lifecycle, and existing bounded enduring settings. Runs
may select `spec.model.connectionRef` and `spec.model.model` explicitly. Resolution
pins the connection revision and fills the model route before freezing host
admission. An unresolved or changed connection refuses admission.

The host must support the matching protocol. The companion Celln change is in
the `feat/model-connections` branch based on native starter release `f654f23`.
Its model profiles carry `protocol`; old profiles default to `openai-chat`.
Anthropic translation runs after the existing bounded chat request validator,
retains tool IDs/results, and normalizes the reply into the unchanged guest
format. Credentials, output budgets, redirect refusal, and host egress checks
remain host-owned.

`celln starter-configure` plans can include:

```json
"modelConnection": {
  "provider": "anthropic",
  "protocol": "anthropic-messages",
  "endpoint": "https://api.anthropic.com/v1/messages",
  "model": "your-model-id",
  "credentialProfile": "team-anthropic"
}
```

The plan's existing operator `credentialFile` supplies the actual local key.
The generated receipt records the matching route for Sympozium host templates.
For one-shot catalogue issuance, the separate operator model policy also needs
the same provider/protocol/URL/model/credential profile. Legacy DeepSeek plans
and manifests remain supported. Existing native parents keep their content-pinned
host profiles; withdraw that profile to revoke live use. Disabling a Kubernetes
connection alone is not a substitute for withdrawing an already issued native
host profile.

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
