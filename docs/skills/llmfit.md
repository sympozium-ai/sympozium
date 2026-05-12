# LLMFit — Hardware Fitness & Model Placement

Sympozium integrates [llmfit](https://github.com/AlexsJones/llmfit) (v0.9.24) in two ways:

1. **DaemonSet** — runs on every node, continuously reports hardware specs and model fitness scores. Powers instant model placement, the Cluster Fitness UI, and Prometheus metrics. Deployed by default.
2. **SkillPack sidecar** — gives agents interactive access to llmfit's MCP tools and cluster probe scripts for ad-hoc queries.

---

## DaemonSet (always-on fitness telemetry)

### What it does

The `sympozium-llmfit-daemon` DaemonSet runs `llmfit serve` on every node, exposing a REST API on port 8787. The controller and API server poll each pod every 60 seconds to build a cluster-wide **FitnessCache** containing:

- Per-node hardware specs (RAM, CPU, GPU, VRAM, backend)
- Model fitness scores (which models fit on which nodes, at what quality)
- Installed runtimes (Ollama, vLLM, llama.cpp, etc.)

### Instant model placement

When a Model CR has `placement.mode: auto`, the controller checks the FitnessCache first. If fresh data exists, placement is instant (milliseconds). If the cache is empty — DaemonSet not deployed or still warming up — it falls back to the original probe-pod approach (~3 minutes).

### Helm configuration

```yaml
llmfit:
  daemonset:
    enabled: true           # Deployed by default with Sympozium
    eventInterval: 60       # Seconds between fitness publications
    resources:
      requests:
        cpu: 100m
        memory: 128Mi
      limits:
        cpu: 200m
        memory: 256Mi
    tolerations:
      - operator: Exists    # Run on all nodes including GPU-tainted
    nodeSelector: {}
  liveEviction:
    enabled: false          # Re-place models when fitness degrades (env: LLMFIT_LIVE_EVICTION=true)
    checkInterval: 30s
    degradeThreshold: 0.3   # 30% score drop triggers re-placement
  webhook:
    preflightValidation: false  # Reject Model CRs that won't fit (env: LLMFIT_PREFLIGHT_VALIDATION=true)
```

### Security

- Read-only root filesystem
- Host path mounts (read-only): `/proc`, `/sys`, `/dev`, `/run/udev` at `/host/*` paths
- `SYS_PTRACE` capability for `/proc` access
- Minimal RBAC: `nodes: [get]`

### Prometheus metrics

The controller exposes fitness metrics on its `/metrics` endpoint:

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `sympozium_fitness_node_score` | Gauge | `node` | Highest model fitness score for a node |
| `sympozium_fitness_node_stale` | Gauge | `node` | 1 if node stopped reporting |
| `sympozium_fitness_node_ram_total_gb` | Gauge | `node` | Total RAM |
| `sympozium_fitness_node_ram_available_gb` | Gauge | `node` | Available RAM |
| `sympozium_fitness_node_gpu_vram_gb` | Gauge | `node` | GPU VRAM |
| `sympozium_fitness_node_gpu_count` | Gauge | `node` | Number of GPUs |
| `sympozium_fitness_node_model_count` | Gauge | `node` | Models that fit |
| `sympozium_fitness_cluster_nodes_total` | Gauge | — | Nodes reporting fitness |
| `sympozium_fitness_cluster_nodes_stale` | Gauge | — | Nodes with stale data |

---

## Fitness API endpoints

The API server exposes fitness data for the web UI and agent queries:

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/fitness/nodes` | All nodes with hardware specs and model fit counts |
| `GET` | `/api/v1/fitness/nodes/{name}` | Single node detail with full model fit list |
| `GET` | `/api/v1/fitness/runtimes` | Installed inference runtimes per node |
| `GET` | `/api/v1/fitness/installed-models` | Models downloaded in local runtimes per node |
| `GET` | `/api/v1/fitness/query?model={q}` | Ranked nodes for a model search query |
| `GET` | `/api/v1/catalog` | Alphabetized catalog of all models the cluster can run |
| `POST` | `/api/v1/fitness/simulate` | Simulate deploying a model — shows per-node capacity impact |
| `GET` | `/api/v1/fitness/cost` | Per-model and per-namespace resource attribution |

---

## Web UI

### Cluster Fitness page

Navigate to **Infrastructure > Cluster Fitness** in the sidebar. Three tabs:

- **Nodes** — card per node showing CPU, RAM, GPU, backend, model fit count, stale indicator
- **Model Catalog** — alphabetized table of all models that fit on the cluster with scores and fit levels
- **Query** — live search for specific models with per-node scores, TPS estimates, and memory requirements

### Model deploy dialog

When deploying a model with auto placement, the dialog shows a **fitness preview** with the top 3 nodes ranked by score, color-coded fit levels, and a "recommended" badge.

### Topology page

K8s node cards on the topology canvas show RAM, CPU cores, GPU info, backend, and model fit count from the fitness cache.

---

## SkillPack sidecar (agent-facing)

The `llmfit` SkillPack (v0.2.0) gives agents four skills:

### `llmfit-cluster-placement`

Probe-based cluster placement using `llmfit-cluster-fit.sh`:

```bash
llmfit-cluster-fit.sh --model "Qwen/Qwen2.5-Coder-14B-Instruct" --use-case coding --min-fit good --limit 10
```

### `llmfit-rest-api-usage`

Query node-local llmfit REST endpoints when daemons are available.

### `llmfit-mcp-tools`

Structured MCP tools (v0.9.24+) available via `llmfit serve --mcp`:

| Tool | Purpose |
|------|---------|
| `get_system_specs` | Node hardware (RAM, GPU, CPU) |
| `recommend_models` | Ranked models with filters |
| `search_models` | Free-text model search |
| `plan_hardware` | Memory/quant/TPS estimates |
| `get_runtimes` | Installed inference runtimes |
| `get_installed_models` | Downloaded models |

### `llmfit-fitness-cache`

Query the fitness cache API from agent workflows:

```bash
curl -s http://sympozium-apiserver:8080/api/v1/fitness/nodes | jq .
curl -s "http://sympozium-apiserver:8080/api/v1/fitness/query?model=Qwen2.5" | jq .
curl -s http://sympozium-apiserver:8080/api/v1/catalog | jq .
```

---

## Architecture

```
llmfit DaemonSet (per node)          SkillPack sidecar (per agent)
┌──────────────────────┐             ┌──────────────────────┐
│ llmfit serve         │             │ llmfit serve --mcp   │
│ REST API :8787       │             │ 6 MCP tools (stdio)  │
│ /api/v1/system       │             │ + probe scripts      │
│ /api/v1/models       │             └──────────────────────┘
└──────────┬───────────┘                     │
           │ polled every 60s                │ agent tool calls
           ▼                                 ▼
┌──────────────────────┐             ┌──────────────────────┐
│ FitnessCache         │             │ Agent pod            │
│ (controller +        │             │ (ad-hoc queries)     │
│  apiserver)          │             └──────────────────────┘
└──────────┬───────────┘
           │
     ┌─────┼──────────┐
     ▼     ▼          ▼
  Instant   Fitness   Prometheus
  placement API       metrics
```

---

## Persona integration

The `platform-team` ensemble enables `llmfit` for the `sre-watchdog` agent. Its heartbeat task queries the fitness API and includes a `## Fitness` section reporting per-node scores, stale nodes, and degradation alerts.
