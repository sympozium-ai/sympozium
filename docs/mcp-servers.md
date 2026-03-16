# MCP Servers

Sympozium integrates with [Model Context Protocol (MCP)](https://modelcontextprotocol.io/) servers to extend agent capabilities with specialized tools. MCP servers provide domain-specific functionality — Kubernetes diagnostics, observability, databases, and more — that agents can invoke during task execution.

## Architecture Overview

```
┌─────────────────────────────────────────────────┐
│ Cluster                                          │
│                                                  │
│  MCPServer CRs (shared, one per cluster)        │
│  ┌──────────────┐  ┌──────────────┐             │
│  │ dynatrace-mcp│  │ k8s-net-mcp  │             │
│  │ (stdio)      │  │ (http)       │             │
│  └──────┬───────┘  └──────┬───────┘             │
│         ▼                  ▼                     │
│  ┌──────────────┐  ┌──────────────┐             │
│  │ Deployment + │  │ Deployment + │             │
│  │ Service      │  │ Service      │             │
│  └──────┬───────┘  └──────┬───────┘             │
│         │                  │                     │
│         └────────┬─────────┘                     │
│                  ▼                                │
│  ┌────────────────────────────────┐              │
│  │ Agent Pod                      │              │
│  │  ├─ init: mcp-discover        │              │
│  │  ├─ agent-runner              │              │
│  │  ├─ mcp-bridge ──→ HTTP ──→ MCPServers      │
│  │  └─ ipc-bridge                │              │
│  └────────────────────────────────┘              │
└─────────────────────────────────────────────────┘
```

MCP servers are deployed as **shared services** in the cluster. All agents connect to them via HTTP through the MCP bridge sidecar. This avoids deploying one MCP server per agent pod.

## MCPServer Custom Resource

The `MCPServer` CRD manages the full lifecycle of MCP servers. It supports three deployment modes:

### Stdio Transport (e.g., Dynatrace, GitHub)

For MCP servers that communicate via stdin/stdout. Sympozium wraps them with an HTTP adapter automatically.

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: MCPServer
metadata:
  name: dynatrace-mcp
  namespace: sympozium-system
spec:
  transportType: stdio
  toolsPrefix: dt
  timeout: 30
  deployment:
    image: mcp/dynatrace-mcp-server:latest
    cmd: "node"
    args: ["/app/dist/index.js"]
    env:
      DT_GRAIL_QUERY_BUDGET_GB: "100"
      DT_MCP_DISABLE_TELEMETRY: "true"
    secretRefs:
      - name: dynatrace-mcp-secret
    resources:
      requests:
        cpu: 100m
        memory: 128Mi
      limits:
        cpu: 500m
        memory: 512Mi
```

**What happens:** The controller creates a Deployment with the HTTP-to-stdio adapter (`mcp-bridge --stdio-adapter`), which spawns the MCP server process and translates HTTP ↔ stdin/stdout. A Service is created at `dynatrace-mcp.sympozium-system.svc:8080`.

### HTTP Transport (e.g., k8s-networking-mcp, otel-collector-mcp)

For MCP servers that already expose an HTTP endpoint (Streamable HTTP / JSON-RPC).

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: MCPServer
metadata:
  name: k8s-networking-mcp
  namespace: sympozium-system
spec:
  transportType: http
  toolsPrefix: k8s_net
  timeout: 30
  deployment:
    image: ghcr.io/henrikrexed/k8s-networking-mcp:latest
    port: 8080
    env:
      LOG_LEVEL: "info"
    serviceAccountName: k8s-networking-mcp
    resources:
      requests:
        cpu: 100m
        memory: 128Mi
      limits:
        cpu: 500m
        memory: 256Mi
```

**What happens:** The controller creates a Deployment with the user's image directly (no adapter needed) and a Service.

> **Note:** HTTP MCP servers that need Kubernetes API access (like k8s-networking-mcp) require a ServiceAccount with appropriate RBAC. See [RBAC Configuration](#rbac-configuration) below.

### External (Pre-existing Servers)

For MCP servers already running in your cluster or externally.

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: MCPServer
metadata:
  name: external-mcp
  namespace: sympozium-system
spec:
  transportType: http
  toolsPrefix: ext
  url: http://existing-mcp.monitoring.svc:8080
```

**What happens:** No Deployment is created. The controller just validates the URL and exposes it in `status.url` for agents to use.

## Tool Filtering

MCP servers can expose dozens of tools. For example, `k8s-networking-mcp` exposes 42 tools, each costing ~300 tokens in the agent's context window. If your agent only needs a subset, you can filter tools with `toolsAllow` and `toolsDeny` to reduce token overhead and keep the agent focused.

### How It Works

- **`toolsAllow`** — allowlist. If set, only these tools are registered. Tool names are specified **without** the `toolsPrefix`.
- **`toolsDeny`** — denylist. Applied **after** `toolsAllow`. Removes matching tools from the final set.
- You can use either field alone, or both together.

### Filtering on MCPServer CRD

Set `toolsAllow`/`toolsDeny` on the MCPServer spec to define cluster-wide defaults for that server:

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: MCPServer
metadata:
  name: k8s-networking-mcp
  namespace: sympozium-system
spec:
  transportType: http
  toolsPrefix: k8s_net
  toolsAllow:
    - get_pods
    - get_services
    - get_endpoints
    - get_ingresses
    - get_network_policies
    - describe_pod
    - describe_service
    - get_pod_logs
    - check_connectivity
    - get_gateway_routes
    - get_virtual_services
    - diagnose_service
  deployment:
    image: ghcr.io/henrikrexed/k8s-networking-mcp:latest
    port: 8080
    serviceAccountName: k8s-networking-mcp
```

This reduces the exposed tools from 42 to 12, saving ~9K tokens per agent run.

### Filtering on MCPServerRef (Per-Instance)

You can also set `toolsAllow`/`toolsDeny` on individual `mcpServers` entries in a `SympoziumInstance`. This lets different agents use different subsets of the same MCP server:

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: SympoziumInstance
metadata:
  name: network-triage-agent
spec:
  mcpServers:
    - name: k8s-networking-mcp
      toolsAllow:
        - get_pods
        - get_services
        - describe_pod
        - get_pod_logs
        - diagnose_service
    - name: dynatrace-mcp
      toolsDeny:
        - delete_dashboard
        - update_settings
```

### Inheritance Behavior

If an `MCPServerRef` in your `SympoziumInstance` does **not** set `toolsAllow` or `toolsDeny`, the values from the `MCPServer` CRD spec are inherited automatically. If the ref sets its own values, they take precedence (no merging — the ref's list fully replaces the CRD default).

| MCPServer CRD | MCPServerRef | Result |
|---------------|-------------|--------|
| `toolsAllow: [a, b, c]` | _(not set)_ | `[a, b, c]` inherited |
| `toolsAllow: [a, b, c]` | `toolsAllow: [a, b]` | `[a, b]` (ref wins) |
| _(not set)_ | `toolsAllow: [a, b]` | `[a, b]` |
| _(not set)_ | _(not set)_ | All tools exposed |

The same logic applies to `toolsDeny`.

## Connecting Agents to MCP Servers

In your `SympoziumInstance`, reference MCP servers by name:

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: SympoziumInstance
metadata:
  name: my-agent
spec:
  # ... other config ...
  mcpServers:
    - name: dynatrace-mcp
    - name: k8s-networking-mcp
    - name: otel-collector-mcp
```

The controller automatically resolves each name to the MCPServer's Service URL from `status.url`. No manual URL configuration needed.

### Inline URL (Legacy / Simple)

You can also specify URLs directly without an MCPServer CR:

```yaml
mcpServers:
  - name: my-mcp
    url: http://my-mcp-service.namespace.svc:8080
    toolsPrefix: my
    timeout: 30
```

This is useful for quick testing or external services you don't want to manage via CRD.

## MCPServer Status

Check the status of your MCP servers:

```bash
kubectl get mcpservers -n sympozium-system
```

```
NAME                  TRANSPORT   READY   URL                                            TOOLS   AGE
dynatrace-mcp         stdio       True    http://dynatrace-mcp.sympozium-system.svc:8080   15    5m
k8s-networking-mcp    http        True    http://k8s-networking-mcp.sympozium-system.svc:8080  42  5m
otel-collector-mcp    http        True    http://otel-collector-mcp.sympozium-system.svc:8080   7  5m
```

Detailed status:

```bash
kubectl describe mcpserver dynatrace-mcp -n sympozium-system
```

Shows:
- `status.ready` — whether the MCP server is serving requests
- `status.url` — resolved Service URL
- `status.toolCount` — number of tools discovered
- `status.tools` — list of tool names
- `status.conditions` — Deployed, Ready, ToolsDiscovered

## Configuration Reference

### MCPServer Spec

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `transportType` | string | Yes | - | `stdio` or `http` |
| `url` | string | No | - | URL for external servers (no deployment) |
| `toolsPrefix` | string | Yes | - | Prefix for tool names (e.g., `dt`, `k8s_net`) |
| `timeout` | int | No | 30 | Per-request timeout in seconds |
| `toolsAllow` | []string | No | - | Tool names (without prefix) to expose. If set, only these are registered |
| `toolsDeny` | []string | No | - | Tool names (without prefix) to hide. Applied after `toolsAllow` |
| `replicas` | int | No | 1 | Number of replicas |
| `deployment` | object | No | - | Deployment spec (see below) |

### Deployment Spec

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `image` | string | Yes | - | Container image |
| `cmd` | string | No | - | Command override (stdio: the MCP server binary) |
| `args` | []string | No | - | Command arguments |
| `port` | int | No | 8080 | HTTP port (http transport only) |
| `env` | map | No | - | Environment variables |
| `secretRefs` | []SecretRef | No | - | Kubernetes Secrets to mount as env vars |
| `resources` | ResourceRequirements | No | - | CPU/memory requests and limits |
| `serviceAccountName` | string | No | - | ServiceAccount for RBAC |

### SecretRef

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Name of the Kubernetes Secret |

All keys from the Secret are injected as environment variables.

## RBAC Configuration

MCP servers that interact with the Kubernetes API need a ServiceAccount with appropriate permissions.

Example for k8s-networking-mcp (read-only cluster access):

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: k8s-networking-mcp
  namespace: sympozium-system
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: k8s-networking-mcp
rules:
  - apiGroups: ["", "apps", "networking.k8s.io", "gateway.networking.k8s.io"]
    resources: ["*"]
    verbs: ["get", "list", "watch"]
  - apiGroups: ["networking.istio.io", "security.istio.io"]
    resources: ["*"]
    verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: k8s-networking-mcp
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: k8s-networking-mcp
subjects:
  - kind: ServiceAccount
    name: k8s-networking-mcp
    namespace: sympozium-system
```

## Secrets Management

For MCP servers that need API tokens or credentials:

```bash
# Create secret for Dynatrace
kubectl create secret generic dynatrace-mcp-secret \
  -n sympozium-system \
  --from-literal=DT_URL=https://your-env.live.dynatrace.com \
  --from-literal=DT_TOKEN=dt0c01.xxxx
```

Reference it in the MCPServer CR:

```yaml
spec:
  deployment:
    secretRefs:
      - name: dynatrace-mcp-secret
```

All keys from the Secret are injected as environment variables into the MCP server container.

## Observability

### Traces

All MCP tool calls are traced end-to-end:

```
agent.run → tool.dispatch → mcp.bridge.tool_call → mcp.server.call
```

- **agent.run**: The overall agent execution span
- **tool.dispatch**: Agent-runner dispatching a tool call
- **mcp.bridge.tool_call**: MCP bridge forwarding to the server
- **mcp.server.call**: (stdio adapter only) The adapter wrapping the stdio call

Trace context (`traceparent`) is propagated through the entire chain.

### Metrics

The MCP bridge emits:
- `mcp.bridge.tool_calls` — counter by server, tool
- `mcp.bridge.tool_errors` — counter by server, tool
- `mcp.bridge.tool_duration_ms` — histogram by server, tool

The stdio adapter additionally emits:
- `mcp.server.requests` — counter by server, tool, status
- `mcp.server.errors` — counter by server, tool, error_type
- `mcp.server.duration` — histogram by server, tool

### Health Checks

Each MCPServer Deployment has readiness and liveness probes:
- Liveness: `/healthz` — is the process alive?
- Readiness: `/readyz` — is the process alive AND tools discovered?

## SkillPacks for MCP Tools

MCP tools alone aren't enough — agents need **diagnostic methodology** to use them effectively. SkillPacks provide this guidance.

See [Writing SkillPacks](writing-skills.md) for the general SkillPack guide. For MCP-specific SkillPacks:

### Creating an MCP SkillPack

A good MCP SkillPack includes:
1. **When to use each tool** — which tool for which symptom
2. **Diagnostic methodology** — step-by-step investigation flow
3. **Protocol-specific guidance** — e.g., gRPC ≠ HTTP routing
4. **Common issues table** — symptom → tool → what to look for → fix

Example structure:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: skillpack-dynatrace-diagnostics
  namespace: sympozium-system
  labels:
    sympozium.ai/skillpack: "true"
data:
  dynatrace-diagnostics.md: |
    # Dynatrace Diagnostics

    ## Available Tools
    - `dt_query_logs` — Query Grail logs with DQL
    - `dt_query_metrics` — Query metrics with DQL
    - `dt_get_problems` — Get active problems from Davis AI
    - `dt_get_entities` — List monitored entities

    ## Diagnostic Methodology
    1. Start with `dt_get_problems` to see if Davis already detected the issue
    2. Use `dt_query_logs` with DQL to investigate specific services
    3. Use `dt_query_metrics` for performance trends
    ...
```

Enable it in values.yaml or per-instance.

## Troubleshooting

### MCP Server Not Ready

```bash
kubectl describe mcpserver <name> -n sympozium-system
kubectl logs deploy/<name> -n sympozium-system
```

Common causes:
- Image pull failure (check image name and registry access)
- Secret not found (check secretRefs names)
- Process crash (stdio: check cmd/args, view adapter logs)

### Agent Not Using MCP Tools

Check the agent-runner logs for:
```
Loaded X MCP tool(s) from manifest
tools enabled: Y tool(s) registered
```

If `X = 0`:
- MCP bridge couldn't reach the MCPServer Service
- Init container timed out (check init container logs)

If tools load but agent doesn't use them:
- Enable a SkillPack with diagnostic methodology
- Check the system prompt includes MCP tool guidance

### Stdio Server Crashing

Check adapter logs:
```bash
kubectl logs deploy/<name> -n sympozium-system
```

The adapter restarts the stdio process automatically with backoff. Look for:
- `stdio process exited with code X` — the MCP server binary is crashing
- `restart backoff: Xs` — adapter is throttling restarts

### Tool Calls Timing Out

Default timeout is 30s. Increase per-server:
```yaml
spec:
  timeout: 60  # seconds
```

Or check if the MCP server is overloaded (add more replicas):
```yaml
spec:
  replicas: 2
```

## Examples

### Complete Dynatrace Setup

```bash
# 1. Create secret
kubectl create secret generic dynatrace-mcp-secret \
  -n sympozium-system \
  --from-literal=DT_URL=https://abc12345.live.dynatrace.com \
  --from-literal=DT_TOKEN=dt0c01.sample.secret

# 2. Deploy MCPServer
kubectl apply -f - <<EOF
apiVersion: sympozium.ai/v1alpha1
kind: MCPServer
metadata:
  name: dynatrace-mcp
  namespace: sympozium-system
spec:
  transportType: stdio
  toolsPrefix: dt
  timeout: 30
  deployment:
    image: mcp/dynatrace-mcp-server:latest
    cmd: "node"
    args: ["/app/dist/index.js"]
    env:
      DT_GRAIL_QUERY_BUDGET_GB: "100"
      DT_MCP_DISABLE_TELEMETRY: "true"
    secretRefs:
      - name: dynatrace-mcp-secret
    resources:
      requests:
        cpu: 100m
        memory: 128Mi
EOF

# 3. Verify
kubectl get mcpservers -n sympozium-system

# 4. Reference in your instance
kubectl patch sympoziuminstance my-agent --type merge -p '
spec:
  mcpServers:
    - name: dynatrace-mcp
'
```

### Complete k8s-networking Setup

```bash
# 1. Create RBAC
kubectl apply -f rbac/k8s-networking-mcp.yaml

# 2. Deploy MCPServer
kubectl apply -f - <<EOF
apiVersion: sympozium.ai/v1alpha1
kind: MCPServer
metadata:
  name: k8s-networking-mcp
  namespace: sympozium-system
spec:
  transportType: http
  toolsPrefix: k8s_net
  deployment:
    image: ghcr.io/henrikrexed/k8s-networking-mcp:latest
    port: 8080
    serviceAccountName: k8s-networking-mcp
EOF
```
