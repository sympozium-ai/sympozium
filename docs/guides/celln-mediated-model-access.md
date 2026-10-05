# Mediated model access for native Celln Agents

With mediation on, a native Celln Agent's model requests leave the node for the
**model gateway**, which adds that Agent's own provider key from the Secret its
`ModelConnection` names in the Agent's namespace. Celln nodes hold no provider
key for such runs. The controller signs a short-lived permit per run; the node
dispatcher's *scoped receiver* and the gateway both verify it against the same
public keyset.

It is **on by default** for every fleet installed by `sympozium install`:
the installer bootstraps the trust, sets every value below, and turns the key
you install with into one Agent's own key (the starter Agent, see
[the default install](#the-default-install)). Opt out with
`--no-celln-mediation`. The chart's own default stays
`celln.mediation.enabled=false`, and then it renders exactly what it rendered
before this feature existed.

## The default install

```sh
export OPENAI_API_KEY=...     # or DEEPSEEK_API_KEY / ANTHROPIC_API_KEY
sympozium install
```

With a released binary (which pins the model gateway image digest) this:

- mints the trust described in [section 2](#2-bootstrap-the-trust) right
  after the chart creates its namespaces, under a key id the installer chose
  and rendered into the values beforehand; the cluster id is the
  `kube-system` namespace UID and the certificate names follow the release's
  actual full name and namespace;
- sets `celln.mediation.enabled`, `clusterId`, `issuer.keyId`,
  `mediateBackends=true` and `modelGateway.image` (the bundled PostgreSQL
  needs a default StorageClass);
- gives the key to a **starter Agent** in the `-n` namespace (default
  `default`, or `--celln-starter-namespace`): Secret `starter-model-key`
  (key `OPENAI_API_KEY` or `ANTHROPIC_API_KEY`, owner-annotated for Agent
  `starter`), ModelConnection `starter`, the backend's runtime wrapper and
  Agent `starter` with that Secret in `spec.authRefs`, exactly the objects
  of [section 6](#6-give-an-agent-its-own-key). A second keyed backend gets
  `starter-<backend>`;
- publishes only a placeholder for that backend in
  `celln-fleet-model-credentials`, so no Celln node holds the key; the
  backend's profile is labelled `celln.sympozium.ai/mediation-only`, its
  route is `auth: secret` only, and no namespace gets a shared Agent for it.

Every rerun reads the existing trust's key id back (it is verified, never
rotated) and derives the same values again, so a rerun never switches
mediation off; a rerun from a build that knows no gateway image keeps the
deployed one, or stops if there is none. A source build without
`--model-gateway-image` installs without mediation and says so. Values passed
with `--set celln.mediation.enabled=true` select the operator-managed path
below, in which the installer bootstraps and derives nothing.

The rest of this guide is the operator-managed path, and the reference for
what the default sets up.

Mediation leaves the fleet's own configuration alone: `celln.fleet.backends`,
the host profiles and `celln.fleet.modelCredentialsSecret` are rendered and
configured on every node exactly as before, and the two paths run side by side
on one controller. Each run takes the path of its resolved model route
(`internal/controller/celln_scoped.go`):

- a connection with a host `credentialProfile` (a fleet backend, policy auth
  `host-profile`) starts as a fleet parent exactly as it does today, and its
  follow-up turns go to that parent;
- an Agent's own connection with a `secretRef` (policy auth `secret`), or a
  credential-free one, goes through the scoped receiver and the gateway.

A `secret` run on a controller without mediation is held with the reason
`ScopedDispatchDisabled`; it is never sent to a fleet parent.

## What the one switch wires

`celln.mediation.enabled=true` (requires `celln.fleet.enabled`) configures four
things against the same issuer, keyset, CA and tokens, or refuses to render:

| Component | What changes |
|---|---|
| Controller | `CELLN_SCOPED_CONFIG` points at a chart-rendered `config.json` (paths and names only). The issuer signing key and both transport tokens come from `controllerSecret`. |
| Fleet dispatchers (`celln-node`) | `--scoped-operator-token-file`, `--scoped-jwks-file`, `--scoped-issuer`, `--scoped-gateway-origin`, `--scoped-gateway-ca`, and `--scoped-parent-request-file` when the node has one. |
| Router (`celln-router`) | `--scoped-token-file` (the dispatchers' operator token): it forwards each `/v1/scoped/*` request to the node that prepared that operation. A small unprivileged `scoped-receiver` sidecar terminates TLS in front of it, behind the `celln-scoped-receiver` Service. |
| Model gateway | Deployed from Secret/ConfigMap volumes; no configuration PVC. |

Why a TLS sidecar: the router speaks plaintext HTTP, and the controller only
talks to an HTTPS receiver with an explicit CA. The sidecar (`/celln-parent-proxy
--scoped-receiver`, shipped in the controller image) forwards nothing but
`POST /v1/scoped/{prepare,start,read,cleanup}` to the router on loopback. The
operator bearer and the signed permits remain the authority, checked by the
dispatcher on the owning node.

**Any number of nodes.** A prepared operation lives on the node that prepared
it. The router places each new operation on the node with the most spare
capacity (an enduring conversation's turns follow its parent) and records that
binding before forwarding, so an operation never reaches two nodes. If that
node leaves the cluster, the router answers `AUTH_CONTEXT_LOST`. The controller
then ends the run, closes its gateway registration, and never re-creates it on
another node; start a new run. A node that is briefly not ready stays listed,
so a readiness blip does not end its conversations.

## 1. PostgreSQL

The gateway keeps budgets and registrations in PostgreSQL and refuses to start
without it.

**Bundled (default).** With `modelGateway.database.secretName` left empty, the
chart runs its own single-pod PostgreSQL (`<release>-model-gateway-db`, a
StatefulSet on a 1 Gi PVC, digest-pinned `postgres:17-alpine`). Its password
is generated on first install and kept across upgrades; the Secret is kept on
uninstall because the PVC is. Before each gateway start, a `migrate` init
container waits for the database and applies migrations 002 and 003, which
are idempotent. A NetworkPolicy admits only the gateway, and the connection
stays in-cluster without TLS. It holds accounting, never provider keys. Size
and storage class are under `modelGateway.database.bundled`.

**Your own, for production.** Apply `migrations/002_celln_model_budget.sql`
then `migrations/003_celln_model_gateway.sql`, publish the connection URL, and
set `modelGateway.database.secretName=model-gateway-database`. The chart then
renders no database and runs no migrations. If you restrict
`modelGateway.egress`, include your database's address.

```bash
kubectl -n sympozium-system create secret generic model-gateway-database \
  --from-literal=database-url='postgres://gateway:...@postgres.databases.svc:5432/gateway?sslmode=require'
```

## 2. Bootstrap the trust

Install the fleet first (`sympozium install --celln-fleet`, see
[Celln Fleet Installation](celln-fleet-installation.md)); then:

```bash
sympozium celln-mediation bootstrap --cluster-id my-cluster --values-out mediation-values.yaml
```

It mints, once, an Ed25519 issuer key and its public JWKS, two independent
bearer tokens, and a private CA with one server certificate each for the
gateway and the receiver (the CA key is discarded). It publishes:

| Object | Namespace | Keys | Read by |
|---|---|---|---|
| Secret `celln-mediation-controller` | control plane | `issuer.key` (PKCS#8 Ed25519 PEM), `receiver-token`, `gateway-token` | controller |
| Secret `celln-mediation-gateway` | control plane | `tls.crt`, `tls.key`, `registration-token` (= `gateway-token`) | gateway |
| Secret `celln-mediation-node` | `celln-system` | `operator-token` (= `receiver-token`), `tls.crt`, `tls.key` | dispatcher, receiver sidecar |
| ConfigMap `celln-mediation-trust` | both | `jwks.json`, `ca.crt` | all three |

The JWKS and CA certificate are public, so they live in a ConfigMap: they can be
inspected and rotated without Secret access, and the dispatcher and gateway
re-read the keyset (the gateway on `SIGHUP`). Nothing private is ever rendered
from Helm values, and the chart generates no keys.

A rerun verifies the five objects still belong together and changes nothing. It
never repairs or rotates by replacement: to rotate, delete all five
deliberately, bootstrap again (or rerun `sympozium install`) and restart the
three components. Certificates last `--validity`, by default **ten years**:
the CA key is discarded after signing, so nothing renews them in place, and
rotation is the renewal. `sympozium doctor` warns in the last 60 days and
fails once they have expired. If your release is not named `sympozium`,
pass `--release-fullname`; if you set `celln.mediation.receiver.url`, pass its
host with `--receiver-host`.

You can also create these objects yourself; the table is the whole contract.
The JWKS is `{"keys":[{"kty":"OKP","crv":"Ed25519","use":"sig","alg":"EdDSA","kid":"...","x":"..."}]}`.

## 3. Enable it

`mediation-values.yaml` from the bootstrap carries `clusterId`, `issuer.keyId`
and the object names. Add the gateway's operator inputs
(`charts/testdata/celln-mediation-values.yaml` is a complete sample):

```yaml
modelGateway:
  image: ghcr.io/sympozium-ai/sympozium/model-gateway@sha256:<digest>   # digest-pinned
  # database: {secretName: model-gateway-database}   # omit for the bundled PostgreSQL
  namespaces: [team-a]        # optional, see "Gateway RBAC"
  # egress: [...]             # optional: omit to allow any provider; a list restricts it
```

```bash
helm upgrade sympozium charts/sympozium -n sympozium-system --reuse-values \
  -f mediation-values.yaml -f gateway-values.yaml
```

Rendering fails, naming the value, when anything is missing or contradictory:
no fleet, no `clusterId`/`issuer.keyId`, another issuer name, an empty object
name, an unpinned gateway or bundled database image, a configuration
claim as well, or a receiver URL that is not an HTTPS origin.

Enabling (or disabling) mediation changes the `celln-node` pod template, so the
owners roll one node at a time and live native parents on each node end.

## 4. Verify

```bash
kubectl -n sympozium-system rollout status deploy/sympozium-model-gateway      # ready = PostgreSQL, RBAC and keys accepted
kubectl -n sympozium-system logs deploy/sympozium-controller-manager | grep -i "Scoped Celln"
kubectl -n celln-system logs ds/celln-node -c dispatcher | grep -i scoped
```

`/v1/scoped/*` no longer answers `404 scoped receiver disabled`. From the
controller's network identity, an unauthenticated request to the receiver now
gets `401` (run it from a debug pod labelled like the controller, or temporarily
admit your pod in the `celln-node-ingress` NetworkPolicy):

```bash
curl --cacert ca.crt -X POST https://celln-scoped-receiver.celln-system.svc:9443/v1/scoped/read -d '{}'
```

## 5. Declare which providers Agents may bring a key for

The resolver matches an Agent's `ModelConnection` against the `auth: secret`
routes of the scope's `CellnExecutionPolicy`, and refuses everything else with
`AUTH_ROUTE_MISMATCH`.

**Built-in routes.** When mediation is enabled and you declare no route, the
chart records three, so an Agent can bring its own key for any model of a
well-known hosted provider without further setup:

| provider    | protocol             | endpoint origin             | models |
|-------------|----------------------|-----------------------------|--------|
| `openai`    | `openai-chat`        | `https://api.openai.com`    | `["*"]` (any) |
| `anthropic` | `anthropic-messages` | `https://api.anthropic.com` | `["*"]` (any) |
| `deepseek`  | `openai-chat`        | `https://api.deepseek.com`  | `["*"]` (any) |

They grant no key of the cluster's: each Agent still brings its own Secret, and
that Secret only ever goes to the listed origin. Declaring **any** route
(`celln.mediation.routes` or `--celln-mediated-route`) replaces the built-in
routes entirely, so declaring one provider is also how you restrict to it. Set
`celln.mediation.defaultRoutes=false` to record no route at all until you
declare one. Like every route, they reach the policy on the next
`sympozium install --celln-fleet`, added backend or
`sympozium celln-mediation apply-routes`.

The default install also sets `mediateBackends`, which admits each HTTPS fleet
backend's provider and exact model (the starter Agent's among them).

Why the operator, and not the Agent's owner: a `ModelConnection` is written by a
tenant, and a tenant-authored endpoint is not authorisation. If the connection
alone decided where a namespace's Secret may be sent, anything that can write a
`ModelConnection` (including a prompt-injected agent with that permission) could
point a key, or the gateway's egress, at a host of its choosing. The route list
is the operator's allow-list of destinations; the tenant only picks from it.

Matching is **exact** on provider, protocol and endpoint origin: there is no
wildcard origin or origin prefix, so a key is only ever sent to an origin the
operator named. Models are exact names, or the **any-model token** `["*"]`,
which admits any model name of that provider at those origins. `*` must be the
route's only model: `[gpt-5, "*"]`, `gpt-*` and other patterns are refused. To
restrict Agents to particular models, list them instead of `*`. Whatever the
route, the signed decision binds the run's concrete model, and the gateway
refuses a request for any other. Secret routes use `https://host` without a
port: a cluster Secret never crosses plain HTTP. Explicitly keyless local
routes can approve HTTP and ports as described below.

With the installer (the flags only declare routes; mediation itself must be
enabled by this install's values, or they are refused before anything changes):

```bash
sympozium install --celln-fleet ... \
  --set celln.mediation.enabled=true --set celln.mediation.clusterId=my-cluster ... \
  --celln-mediated-route provider=anthropic,protocol=anthropic-messages,origin=https://api.anthropic.com,models=claude-sonnet-5+claude-opus-5 \
  --celln-mediated-route provider=openai,protocol=openai-chat,origin=https://api.openai.com,models=*
```

`models` and `origin` take several values joined with `+` (or repeat the key);
`models=*` alone admits any model (quote it in a shell that globs).
`--celln-mediate-backends` additionally offers every HTTPS backend of the fleet
(its provider, protocol, origin and model) to an Agent's own key; plain-HTTP and
port-bearing backends are never offered. A model name that itself contains `+`
or `,` can only be declared through values.

With values (`celln.mediation.routes` is refused unless `celln.mediation.enabled`
is true: a route declared while mediation is off would admit nothing and only
look like it does):

```yaml
celln:
  mediation:
    mediateBackends: false
    routes:
      - provider: anthropic
        protocol: anthropic-messages     # or openai-chat
        models: [claude-sonnet-5, claude-opus-5]   # exact names, or ["*"] alone for any model
        endpointOrigins: [https://api.anthropic.com]
      - provider: openai
        protocol: openai-chat
        models: ["*"]
        endpointOrigins: [https://api.openai.com]
```

```bash
helm upgrade sympozium charts/sympozium -n sympozium-system --reuse-values -f routes-values.yaml
sympozium celln-mediation apply-routes
```

The chart validates every route the way the installer does and records the
declaration in ConfigMap `celln-system/celln-mediated-routes` (names and public
origins only). That record is the single source every platform installer reads:
`sympozium install --celln-fleet`, the API server when it completes an added
backend, and `sympozium celln-mediation apply-routes`, which does nothing but
publish the record into the installed scope's policy. After a Helm-only change
run `apply-routes`; the installer does the same step itself.

A policy only ever grows. Removing a route from the values stops later installs
from adding it, but never removes a published route from under running Agents;
to withdraw one, edit the `CellnExecutionPolicy` deliberately. Note that
`sympozium install` does not reuse the previous release's values: by default
it derives the mediation values again on every run; on the operator-managed
path pass them (`--set`) again on a rerun, or the default takes over.

`sympozium doctor` reports the state ("Mediated model access"): disabled, or
enabled with the five bootstrap objects present, the gateway ready and the
routes the policy carries; it warns when no route is published or a declared
one is not in the policy yet. The console reads the same through
`GET /api/v1/celln-platform/mediation?namespace=<ns>`:

```json
{"enabled": true, "mediateBackends": false,
 "routes":  [{"provider": "anthropic", "protocol": "anthropic-messages", "models": ["claude-opus-5", "claude-sonnet-5"],
              "endpointOrigins": ["https://api.anthropic.com"], "policy": "celln-fleet-starter", "secretKey": "ANTHROPIC_API_KEY"}],
 "pending": []}
```

`routes` are the ones the namespace's policies carry now (what a run is matched
against); `pending` are declared routes no policy carries yet. A route with
`models: ["*"]` also carries `"anyModel": true`, and the console then asks for
the model name (prefilled with a well-known one) instead of offering a list.

## 6. Give an Agent its own key

Everything below lives in the Agent's namespace (`team-a` here), which must be
one the scope's policy admits and, with `modelGateway.namespaces` set, one the
gateway may read.

**The Secret.** The key name is fixed by the connection's protocol:
`ANTHROPIC_API_KEY` for `anthropic-messages`, `OPENAI_API_KEY` for `openai-chat`
(whatever the provider is called). Only the gateway reads it.

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: my-anthropic-key
  namespace: team-a
stringData:
  ANTHROPIC_API_KEY: "<your key>"
```

**The ModelConnection.** Provider, protocol and the endpoint's origin must
match one declared route exactly, and every model you intend to run must be one
of its models (any model, on a `["*"]` route). `parameters`
and `maxOutputTokens` (256-4096 per request, default 512) are optional.

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: ModelConnection
metadata:
  name: my-anthropic
  namespace: team-a
spec:
  provider: anthropic
  protocol: anthropic-messages
  endpoint: https://api.anthropic.com/v1/messages
  secretRef: my-anthropic-key
  models: [claude-sonnet-5]
  maxOutputTokens: 2048
```

**The runtime wrapper.** The Agent's `runtimeRef` names an `AgentRuntime` in its
namespace that binds a fleet runtime profile by `cellnProfileRef`. A namespace
that has used a fleet backend before already has one (`celln-native` for the
default backend, `celln-<backend>` otherwise). Otherwise ask the API server for
the runtime alone; it is created only for a profile the namespace's policy
admits, and neither the backend's shared Agent nor its host-profile connection
is added:

```bash
curl -X POST "$SYMPOZIUM_API/api/v1/celln-platform/wrappers?namespace=team-a" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"profile": "celln-native-starter", "runtimeOnly": true}'
# {"backend":"native","runtime":"celln-native","agent":"","connection":"","created":["celln-native"]}
```

`GET /api/v1/celln-platform/profiles?namespace=team-a` lists the profile names.
What it creates is this object (shown for reference; the revision must be the
profile's exact one, so prefer the API):

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: AgentRuntime
metadata:
  name: celln-native
  namespace: team-a
spec:
  image: ""                     # required by the API; a fleet profile supplies the executable
  cellnProfileRef:
    name: celln-native-starter
    revision: "<the profile's spec.revision>"
  supportOwner: celln-platform
```

**The Agent.** `authRefs` is the Agent owner's grant that this Secret may be
used for this Agent (selecting the connection in `spec.execution` grants it
too); `spec.execution.modelConnectionRef` makes its runs use the connection.
The mediated path is chat only, so the selection lends no tools.

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: Agent
metadata:
  name: my-agent
  namespace: team-a
spec:
  agents:
    default:
      model: claude-sonnet-5    # required by the API; the run's model is spec.execution.model
  runtimeRef: celln-native
  authRefs:
    - provider: anthropic
      secret: my-anthropic-key
  execution:
    backend: celln
    modelConnectionRef: my-anthropic
    model: claude-sonnet-5
    cellnSelection:
      runtimeRef: celln-native
      toolRefs: []
```

A run of this Agent is resolved against the policy's `secret` routes. If it is
refused `AUTH_ROUTE_MISMATCH`, compare the connection's provider, protocol,
endpoint origin and the run's model with the routes `sympozium doctor` lists:
all four must match one route.

## Keyless local models

For a local OpenAI-compatible server, declare `auth: none` explicitly. HTTP
also requires `allowInsecure: true` on both the operator route and the tenant
connection, and the exact origin in `modelGateway.privateOrigins`:

```yaml
celln:
  mediation:
    enabled: true
    routes:
      - provider: llama-server
        protocol: openai-chat
        auth: none
        allowInsecure: true
        models: [local-model]
        endpointOrigins: [http://192.168.1.237:8080]
modelGateway:
  privateOrigins: [http://192.168.1.237:8080]
```

Merge these settings into the existing mediation values, upgrade with
`helm upgrade --reuse-values`, then run `sympozium celln-mediation apply-routes`.
Keep the existing routes in the values list if they are still needed. The
equivalent installer route is
`--celln-mediated-route provider=llama-server,protocol=openai-chat,auth=none,allowInsecure=true,origin=http://192.168.1.237:8080,models=local-model`;
the gateway allow-list (`modelGateway.privateOrigins`) must also permit that destination, and so must `modelGateway.egress` if you restrict it.

Create Agent → Celln offers the declared local provider and explains that no
key is required. Its ModelConnection sets `allowInsecure: true` and has neither
`secretRef` nor `credentialProfile`. No compatibility Secret is created. The
gateway still enforces budgets and model parameters, but sends no credentials.

HTTP DNS answers must all be loopback or private addresses (RFC 1918, IPv6 ULA,
or the 100.64.0.0/10 shared space Tailscale uses); public, link-local,
and mixed public/private answers are refused. Use an explicit LAN IP when the
host name resolves to several address classes. Redirects remain disabled.
Secret-backed HTTP routes remain forbidden.

## The enduring parent request

Enduring scoped runs need `--scoped-parent-request-file`. Celln's
`starter-configure` writes `scoped-parent-request.json` beside
`native-template.json`, and the chart defaults `celln.mediation.parentRequestFile`
to that file in the first backend's configuration on the node
(`/var/lib/sympozium-celln/<scope>/configuration-<package>-<backend>/`).

Celln refuses to start when the flag names a missing file, so the owner pod
waits for that configuration to exist and passes the flag **only if the file is
there**. Without it the dispatcher logs that enduring scoped runs are disabled
and serves one-shot scoped dispatch. The file is read once at start: after it
appears (a newer starter package, or a reconfigured backend), restart the
`celln-node` pod on that node.

## Gateway RBAC

By default the gateway keeps its existing cluster-wide `get` on Secrets,
ModelConnections and Namespaces. Set `modelGateway.namespaces` to replace that
with one Role/RoleBinding per listed namespace plus a ClusterRole limited to
reading those Namespace objects; the chart then also sets the gateway's
`readinessNamespaces`, so readiness probes exactly that access. Add a namespace
to the list before its Agents use mediation.

## Current limits

- **Node loss ends a mediated run.** Its native state lived on that node;
  there is no automatic continuation on the mediated path yet.
- **Chat only.** Borrowed workspace and HTTPS tools are refused on the mediated
  path.
- **A dispatcher restart loses live scoped parents**, including the roll caused
  by toggling mediation or upgrading the fleet package.
- **Rotation is manual** (delete, bootstrap or rerun the install, restart);
  there is no overlap window tooling yet, although the verifiers accept a
  multi-key JWKS. The default ten-year certificates make this rare.
- **A backend added from the API or the console** still publishes its key to
  the fleet (`auth: host-profile`); only installer backends are mediated by
  default.
- Secret volumes cannot be owned by a non-root user, and the controller and
  gateway refuse key or token files that are not owner-only. A non-root init
  step in each pod therefore copies the operator's files into an in-memory
  volume with mode `0400`; a changed Secret takes effect on the next pod restart.
- NetworkPolicies need an enforcing CNI. The gateway admits the controller and
  the `celln-node` pods on 8443; `celln-node` admits the controller on the
  receiver port only, and its plaintext 8787 stays router-only.
