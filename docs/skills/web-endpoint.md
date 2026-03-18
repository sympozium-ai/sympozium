# Web Endpoint Skill (`web-endpoint`)

The `web-endpoint` SkillPack exposes a Sympozium agent as an HTTP API with OpenAI-compatible and MCP endpoints.

It deploys a web-proxy sidecar as a long-running Deployment that accepts incoming HTTP requests and creates per-request AgentRun Jobs — turning any agent into a callable API.

---

## What it installs

- SkillPack manifest: `config/skills/web-endpoint.yaml`
- Sidecar image: `ghcr.io/sympozium-ai/sympozium/web-proxy:latest`
- Sidecar source: `cmd/web-proxy/` + `internal/webproxy/`

Helm bundled copy:
- `charts/sympozium/files/skills/web-endpoint.yaml`

---

## How it works

Unlike other skills that run inside ephemeral Job pods, the web-endpoint skill triggers **serving mode** — a long-running Deployment instead of a one-shot Job. The controller detects `requiresServer: true` on the sidecar and automatically:

1. Creates a **Deployment** with the web-proxy container
2. Creates a **ClusterIP Service** exposing port 8080
3. Provisions an **API key Secret** (`sk-<hex>`) for authentication
4. Creates an **HTTPRoute** (when Kubernetes Gateway API is configured) for external access
5. Provisions **ephemeral RBAC** so the proxy can create AgentRun CRs and read ConfigMaps/Secrets

When a request arrives, the web-proxy creates a new AgentRun Job to handle it, waits for the result, and returns it as an HTTP response.

---

## Endpoints

### OpenAI-Compatible Chat Completions

```
POST /v1/chat/completions
Authorization: Bearer <api-key>
Content-Type: application/json

{
  "model": "default",
  "messages": [
    {"role": "user", "content": "Hello, agent!"}
  ]
}
```

### MCP (Model Context Protocol)

```
POST /v1/mcp
Authorization: Bearer <api-key>
Content-Type: application/json
```

### Health Check

```
GET /healthz
```

No authentication required.

---

## Authentication

All requests (except `/healthz`) require a Bearer token. The API key is auto-generated as `sk-<hex>` and stored in a Kubernetes Secret named `<agentrun>-web-api-key`. You can provide your own Secret via the `auth_secret_ref` skill parameter.

---

## Rate limiting

Rate limiting is enforced by the web-proxy sidecar using a token-bucket algorithm:

- `rate_limit_rpm`: Maximum requests per minute (default: 60)
- `rate_limit_burst`: Burst size above the sustained rate (default: 10)

Configure via skill parameters on the SympoziumInstance:

```yaml
spec:
  skills:
    - skillPackRef: web-endpoint
      params:
        rate_limit_rpm: "120"
        rate_limit_burst: "20"
```

---

## Networking

- **Without a gateway**: The Service is ClusterIP-only, accessible from within the cluster.
- **With a gateway**: An HTTPRoute is created automatically once the `SympoziumConfig` gateway is ready, enabling external access. The hostname is derived from `<instance-name>.<baseDomain>` or can be set explicitly via the `hostname` parameter.

---

## Configuration parameters

| Parameter | Description | Default |
|-----------|-------------|---------|
| `rate_limit_rpm` | Max requests per minute | `60` |
| `rate_limit_burst` | Burst size above rate limit | `10` |
| `hostname` | Custom hostname for HTTPRoute | `<instance>.<baseDomain>` |
| `auth_secret_ref` | Name of existing Secret with `api-key` key | auto-generated |

---

## RBAC

The skill provisions scoped permissions:

- `sympozium.ai/agentruns`: `get`, `list`, `watch`, `create` — to dispatch per-request AgentRun Jobs
- `core/configmaps`, `core/secrets`: `get`, `list`, `watch` — to read instance configuration

---

## Enabling via TUI

Press `s` on an instance → `Space` to toggle `web-endpoint`.

## Enabling via kubectl

```bash
kubectl patch sympoziuminstance <name> --type=merge -p '{
  "spec": {
    "skills": [{"skillPackRef": "web-endpoint"}]
  }
}'
```

## Enabling via API

```bash
curl -X PATCH http://localhost:8080/api/v1/instances/<name>?namespace=default \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <token>" \
  -d '{"webEndpoint": {"enabled": true}}'
```

## Checking status via API

```bash
curl http://localhost:8080/api/v1/instances/<name>/web-endpoint?namespace=default \
  -H "Authorization: Bearer <token>"
```

Returns:

```json
{
  "enabled": true,
  "deploymentName": "<run>-server",
  "serviceName": "<run>-server",
  "gatewayReady": false,
  "authSecretName": "<run>-web-api-key"
}
```

---

## Web UI

The instance detail page includes a **Web Endpoint** tab that shows:

- Enable/disable toggle
- Rate limit configuration
- Hostname configuration
- Link to Runs page filtered by "Serving" phase
