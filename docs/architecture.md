# Architecture

## System Overview

### Control Plane

```mermaid
graph TB
    ADMIN(["Operator / SRE"]) -- "TUI · Web UI · kubectl" --> CP

    subgraph CP["Control Plane"]
        CM["Controller Manager<br/><small>Agent · AgentRun<br/>Ensemble · SkillPack · Model<br/>SympoziumPolicy · MCPServer</small>"]
        API["API Server<br/><small>HTTP + WebSocket</small>"]
        WH["Admission Webhook<br/><small>Policy enforcement</small>"]
        NATS[("NATS JetStream<br/><small>Event bus</small>")]
        CM --- NATS
        API --- NATS
        WH -.- CM
    end

    subgraph DATA["Data Layer"]
        ETCD[("etcd<br/><small>CRDs, state</small>")]
        PG[("PostgreSQL<br/><small>sessions, history</small>")]
    end

    CM -- "reconciles CRDs" --> ETCD
    API -- "reads node annotations" --> ETCD
    API -- "sessions, history" --> PG

    subgraph SCHED["Scheduled Tasks"]
        CS["SympoziumSchedule Controller"]
        SROUTER["Schedule Router"]
    end

    CS -- "creates AgentRuns on schedule" --> CM
    NATS -- "schedule.upsert" --> SROUTER
    SROUTER -- "creates SympoziumSchedule CRDs" --> CS

    style CP stroke:#e94560,stroke-width:2px
    style DATA stroke:#30363d,stroke-width:2px
    style SCHED stroke:#f5a623,stroke-width:2px
```

### Agent Pod Lifecycle

```mermaid
graph LR
    CM["Controller Manager"] -- "creates Job or<br/>Sandbox CR" --> AP

    subgraph LCH["Lifecycle Hooks"]
        direction TB
        PRE["PreRun Init Containers<br/><small>fetch context, clone repos</small>"]
        POST["PostRun Job<br/><small>upload artifacts, notify</small>"]
    end

    PRE -- "runs before" --> AP
    AP -- "runs before" --> POST

    subgraph AP["Agent Pod"]
        direction TB
        A1["Agent Container<br/><small>LLM provider agnostic</small>"]
        IPC["IPC Bridge<br/><small>fsnotify → NATS</small>"]
        SKS["Skill Sidecars<br/><small>kubectl, helm, etc.</small>"]
        SB["Sandbox<br/><small>optional</small>"]
        A1 -. "/ipc volume" .- IPC
        A1 -. "/workspace" .- SKS
        A1 -. "optional" .- SB
    end

    subgraph MEM["Persistent Memory"]
        MSCAR["Memory Sidecar<br/><small>SQLite + FTS5</small>"]
        PVC[("PersistentVolume<br/><small>per-instance</small>")]
    end

    subgraph SMEM["Shared Workflow Memory"]
        SMSRV["Shared Memory Server<br/><small>SQLite + FTS5</small>"]
        SPVC[("PersistentVolume<br/><small>per-pack</small>")]
    end

    A1 -. "memory_search<br/>memory_store" .- MSCAR
    MSCAR -- "reads / writes" --> PVC
    A1 -. "workflow_memory_search<br/>workflow_memory_store" .- SMSRV
    SMSRV -- "reads / writes" --> SPVC

    subgraph SEC["Skill & Lifecycle RBAC"]
        SR["Role + RoleBinding<br/><small>namespace-scoped</small>"]
        SCR["ClusterRole + Binding<br/><small>cluster-scoped</small>"]
    end

    SKS -- "uses" --> SR
    SKS -- "uses" --> SCR
    LCH -. "uses" .-> SR
    CM -- "creates / deletes" --> SEC

    subgraph MCP["MCP Servers"]
        MCPB["MCP Bridge Sidecar"]
    end

    MCPB -. "tools" .- A1
    CM -- "reconciles" --> MCP

    IPC -- "results" --> NATS[("NATS")]
    NATS -- "tasks" --> IPC

    style AP stroke:#53354a,stroke-width:2px
    style MEM stroke:#7c3aed,stroke-width:2px
    style SEC stroke:#238636,stroke-width:2px
    style MCP stroke:#0ea5e9,stroke-width:2px
    style LCH stroke:#e67e22,stroke-width:2px
```

### Channels & Web Endpoints

```mermaid
graph LR
    USER(["User / Chat Client"]) -- "Telegram · Slack<br/>Discord · WhatsApp" --> CH
    HTTPUSER(["HTTP / API Client"]) -- "REST · MCP<br/>OpenAI-compat" --> GW

    subgraph CH["Channel Pods"]
        TG["Telegram"]
        SL["Slack"]
        DC["Discord"]
        WA["WhatsApp"]
    end

    subgraph WE["Web Endpoints"]
        GW["Envoy Gateway<br/><small>HTTPRoute per instance</small>"]
        WP["Web Proxy<br/><small>OpenAI-compat + MCP</small>"]
        GW -- "routes" --> WP
    end

    TG & SL & DC & WA -- "messages" --> NATS[("NATS")]
    WP -- "creates per-request AgentRuns" --> CM["Controller Manager"]
    WP --- NATS
    CM -- "creates Deployment<br/>+ Service + HTTPRoute" --> WE

    subgraph NP["Node Probe · DaemonSet"]
        NPD["Node Probe<br/><small>discovers Ollama, vLLM,<br/>LM Studio on nodes</small>"]
    end

    NPD -- "annotates nodes<br/>sympozium.ai/inference-*" --> ETCD[("etcd")]

    style CH stroke:#0f3460,stroke-width:2px
    style WE stroke:#f5a623,stroke-width:2px
    style NP stroke:#f5a623,stroke-width:2px
```

### Cluster-Local Model Inference

```mermaid
graph TB
    USER(["Operator"]) -- "kubectl apply / Web UI" --> MODEL

    subgraph MODEL["Model CRD Lifecycle"]
        direction LR
        MCRD["Model CR<br/><small>url, gpu, memory</small>"]
        PVC[("PVC<br/><small>model.gguf</small>")]
        DL["Download Job<br/><small>curl → PVC</small>"]
        DEP["Deployment<br/><small>llama-server</small>"]
        SVC["Service<br/><small>ClusterIP :8080</small>"]

        MCRD -- "creates" --> PVC
        MCRD -- "creates" --> DL
        DL -- "writes to" --> PVC
        MCRD -- "creates" --> DEP
        DEP -- "reads from" --> PVC
        MCRD -- "creates" --> SVC
        SVC -- "routes to" --> DEP
    end

    subgraph AP["Agent Pod"]
        A1["Agent Container"]
    end

    A1 -- "MODEL_BASE_URL<br/>http://model-*.svc:8080/v1" --> SVC
    CM["Controller Manager"] -- "reconciles phases:<br/>Pending → Downloading → Loading → Ready" --> MCRD

    style MODEL stroke:#059669,stroke-width:2px
    style AP stroke:#53354a,stroke-width:2px
```

Model CRDs declare GGUF models as Kubernetes resources. The controller downloads weights to a PVC, deploys a llama-server, and exposes an OpenAI-compatible endpoint. AgentRuns reference models by name via `modelRef` — the controller resolves it to the cluster-internal endpoint automatically.

### Persona Delegation Flow

```mermaid
graph TB
    subgraph PACK["Ensemble 'research-delegation-example'"]
        direction TB
        LEAD["Lead<br/><small>coordinates team</small>"]
        RES["Researcher<br/><small>gathers findings</small>"]
        WRI["Writer<br/><small>produces reports</small>"]
        REV["Reviewer<br/><small>quality check</small>"]
        LEAD -- "delegation" --> RES
        RES -- "delegation" --> WRI
        WRI -- "sequential" --> REV
        LEAD -. "supervision" .-> WRI
        LEAD -. "supervision" .-> REV
    end

    subgraph RT["Runtime"]
        AR1["AgentRun (Lead)<br/><small>delegate_to_persona blocks</small>"]
        AR2["AgentRun (Researcher)"]
        AR3["AgentRun (Writer)"]
        IPC["/ipc/spawn/<br/><small>request-*.json</small>"]
        SR["SpawnRouter<br/><small>NATS subscriber</small>"]
        SPAWNER["Spawner<br/><small>resolvePersonaTarget()</small>"]
        RESULT["/ipc/spawn/<br/><small>result-*.json</small>"]
    end

    AR1 -- "1. writes spawn request" --> IPC
    IPC -- "2. NATS event" --> SR
    SR -- "3. calls Spawn()" --> SPAWNER
    SPAWNER -- "4. creates child AgentRun<br/>validates relationship edge" --> AR2
    SR -- "5. patches parent → AwaitingDelegate" --> AR1
    AR2 -- "6. child completes → NATS event" --> SR
    SR -- "7. publishes delegate result" --> RESULT
    RESULT -- "8. tool reads result, unblocks" --> AR1

    style PACK stroke:#7c3aed,stroke-width:2px
    style RT stroke:#e94560,stroke-width:2px
```

Agents in a Ensemble can delegate tasks to other personas using the `delegate_to_persona` tool. The tool **blocks** until the child completes: the SpawnRouter subscribes to spawn requests, creates child AgentRuns via the Spawner (resolving persona names and validating relationship edges), and delivers the child's result back to the parent through NATS. The parent's `DelegateStatus` tracks in-flight delegations.

## How It Works

1. **A message arrives** via a channel pod (Telegram, Slack, etc.) and is published to the NATS event bus.
2. **The controller creates an AgentRun CR**, which reconciles into an ephemeral K8s Job — optional preRun lifecycle init containers, then an agent container + IPC bridge sidecar + optional sandbox + skill sidecars (with auto-provisioned RBAC). PostRun lifecycle hooks execute in a follow-up Job after the agent completes.
3. **The agent container** calls the configured LLM provider (OpenAI, Anthropic, Azure, Ollama, LM Studio, Unsloth, or any OpenAI-compatible endpoint), with skills mounted as files, persistent memory provided by the memory sidecar (SQLite + FTS5 on a PersistentVolume), and tool sidecars providing runtime capabilities like `kubectl`. A legacy ConfigMap-based memory path is preserved as a fallback.
4. **Results flow back** through the IPC bridge → NATS → channel pod → user. The controller extracts structured results and memory updates from pod logs.
5. **Web endpoints** expose agents as HTTP APIs. When an instance has the `web-endpoint` skill, the controller creates a long-lived Deployment (serving mode) with a web-proxy sidecar. The proxy accepts OpenAI-compatible (`/v1/chat/completions`) and MCP (`/sse`, `/message`) requests, creating per-request AgentRun Jobs. An Envoy Gateway with per-instance HTTPRoutes provides external access with TLS.
6. **MCP server integration** — `MCPServer` CRDs define external tool providers using the Model Context Protocol. The controller deploys managed servers (from container images) or connects to external ones, probes them for available tools, and records discovered tools in the resource status. Agent pods access MCP tools through the `mcp-bridge` skill sidecar, which translates between the agent's tool interface and MCP's SSE/stdio transport. Tool names are prefixed to avoid collisions when multiple MCP servers are active. The web UI and CLI provide full CRUD management.
7. **Node-based inference discovery** — for local inference providers (Ollama, vLLM, llama-cpp) installed directly on host nodes, an optional node-probe DaemonSet probes localhost ports and annotates each node with discovered providers and models (`sympozium.ai/inference-*`). The API server reads these annotations, and the web wizard lets users select a node to pin their agent pods to via `nodeSelector`.
8. **Cluster-local model inference** — `Model` CRDs declare GGUF models as Kubernetes resources. The controller downloads weights to a PVC, deploys a llama-server (OpenAI-compatible), and exposes a ClusterIP Service. AgentRuns reference models by name via `spec.model.modelRef` — no API key needed. The web UI auto-wires Ready models as provider options during instance creation.
9. **Everything is a Kubernetes resource** — instances, runs, policies, skills, models, and schedules are all CRDs. Lifecycle is managed by controllers. Access is gated by admission webhooks. Network isolation is enforced by NetworkPolicy. The TUI and web dashboard give you full visibility into the entire system.

---

## How It Compares

| Concern | In-process frameworks | Sympozium (Kubernetes-native) |
|---------|----------------------|----------------------------|
| **Agent execution** | Shared memory, single process | Ephemeral **Pod** per invocation (K8s Job) |
| **Orchestration** | In-process registry + lane queue | **CRD-based** registry with controller reconciliation |
| **Sandbox isolation** | Long-lived Docker sidecar | Pod **SecurityContext** + PodSecurity admission |
| **IPC** | In-process EventEmitter | Filesystem sidecar + **NATS JetStream** |
| **Tool/feature gating** | In-process pipeline | **Admission webhooks** + `SympoziumPolicy` CRD |
| **Persistent memory** | Files on disk | **SQLite + FTS5** on PersistentVolume via memory sidecar (ConfigMap legacy fallback) |
| **Scheduled tasks** | Cron jobs / external scripts | **SympoziumSchedule CRD** with cron controller |
| **State** | SQLite + flat files | **etcd** (CRDs) + PostgreSQL + object storage |
| **Multi-tenancy** | Single-instance file lock | **Namespaced CRDs**, RBAC, NetworkPolicy |
| **Scaling** | Vertical only | **Horizontal** — stateless control plane, HPA |
| **Channel connections** | In-process per channel | Dedicated **Deployment** per channel type |
| **External tools** | Plugin SDKs, in-process registries | **MCPServer CRD** — managed deployments or external endpoints, auto-discovery, prefixed tool namespacing |
| **Observability** | Application logs | `kubectl logs`, events, conditions, **OpenTelemetry traces/metrics**, **k9s-style TUI**, **web dashboard** |

---

## Key Design Decisions

| Decision | Kubernetes Primitive | Rationale |
|----------|---------------------|-----------|
| **One Pod per agent run** | Job | Blast-radius isolation, resource limits, automatic cleanup — each agent is as ephemeral as a CronJob pod |
| **Filesystem IPC** | emptyDir volume | Agent writes to `/ipc/`, bridge sidecar watches via fsnotify and publishes to NATS — language-agnostic, zero dependencies in agent container |
| **NATS JetStream** | StatefulSet | Durable pub/sub with replay — channels and control plane communicate without direct coupling |
| **NetworkPolicy isolation** | NetworkPolicy | Agent pods get deny-all egress; only the IPC bridge connects to the event bus — agents cannot reach the internet or other pods |
| **Policy-as-CRD** | Admission Webhook | `SympoziumPolicy` resources gate tools, sandboxes, and features — enforced at admission time, not at runtime |
| **Memory-as-SQLite** | PersistentVolume + sidecar | Persistent agent memory uses SQLite with FTS5 full-text search on a PVC — supports semantic search via `memory_search`, tagging via `memory_store`, and is upgradeable to vector search. Legacy ConfigMap fallback preserved for migration |
| **Shared Workflow Memory** | PVC + Deployment + Service per Ensemble | Pack-level shared memory pool enables cross-persona knowledge sharing. Same `skill-memory` binary, separate PVC. Per-persona access control (read-write / read-only) enforced client-side. Auto-tagged with source persona for attribution |
| **Schedule-as-CRD** | CronJob analogy | `SympoziumSchedule` resources define recurring tasks with cron expressions — the controller creates AgentRuns, not the user |
| **Skills-as-ConfigMap** | ConfigMap volume | SkillPacks generate ConfigMaps mounted into agent pods — portable, versionable, namespace-scoped |
| **Skill sidecars with auto-RBAC** | Role / ClusterRole | SkillPacks can declare sidecar containers with RBAC rules — the controller injects the container and provisions ephemeral, least-privilege RBAC per run |
| **Ensembles** | Operator Bundle | Pre-configured agent bundles — the controller stamps out Agents, Schedules, and memory ConfigMaps. Activating a pack is a single TUI action |
| **MCP servers as CRD** | Deployment + Service | `MCPServer` resources declare external tool providers — the controller manages deployment lifecycle, probes for tools, and the bridge sidecar translates MCP protocol to agent tool calls. Prefixed tool names prevent collisions across providers |
| **Node probe DaemonSet** | DaemonSet | Discovers host-installed inference providers (Ollama, vLLM) by probing localhost ports — annotates nodes so the control plane can offer model selection and node pinning without manual configuration |
| **llmfit DaemonSet** | DaemonSet | Runs on every node, continuously reporting hardware specs (RAM, CPU, GPU VRAM) and model fitness scores. The controller and API server poll each pod to build a FitnessCache that powers instant model placement, the Cluster Fitness UI, Prometheus metrics, GPU-aware scheduling, and fitness API endpoints |

---

## Project Structure

```
sympozium/
├── api/v1alpha1/           # CRD type definitions
├── cmd/                    # Binary entry points
│   ├── agent-runner/       # LLM agent runner (runs inside agent pods)
│   ├── controller/         # Controller manager (reconciles all CRDs)
│   ├── apiserver/          # HTTP + WebSocket API server (+ embedded web UI)
│   ├── ipc-bridge/         # IPC bridge sidecar (fsnotify → NATS)
│   ├── memory-server/      # Memory sidecar (SQLite + FTS5 persistent memory)
│   ├── web-proxy/          # Web proxy (OpenAI-compat API + MCP gateway)
│   ├── webhook/            # Admission webhook (policy enforcement)
│   ├── node-probe/         # Node probe DaemonSet (inference provider discovery)
│   └── sympozium/          # CLI + interactive TUI
├── images/
│   ├── llmfit-daemon/      # llmfit DaemonSet (hardware fitness telemetry)
├── web/                    # Web dashboard (React + TypeScript + Vite)
├── internal/               # Internal packages
│   ├── controller/         # Kubernetes controllers (6 reconcilers)
│   ├── orchestrator/       # Agent pod builder & spawner
│   ├── apiserver/          # API server handlers
│   ├── mcpbridge/          # MCP bridge sidecar (SSE/stdio adapter)
│   ├── eventbus/           # NATS JetStream event bus
│   ├── ipc/                # IPC bridge (fsnotify + NATS)
│   ├── webhook/            # Policy enforcement webhooks
│   ├── webproxy/           # Web proxy handlers (OpenAI, MCP, rate limiting)
│   ├── session/            # Session persistence (PostgreSQL)
│   └── channel/            # Channel base types
├── channels/               # Channel pod implementations
├── images/                 # Dockerfiles for all components
├── config/                 # Kubernetes manifests
│   ├── crd/bases/          # CRD YAML definitions
│   ├── manager/            # Controller deployment
│   ├── rbac/               # ClusterRole, bindings
│   ├── webhook/            # Webhook configuration
│   ├── network/            # NetworkPolicy for agent isolation
│   ├── nats/               # NATS JetStream deployment
│   ├── cert/               # TLS certificate resources
│   ├── personas/           # Built-in Ensemble definitions
│   ├── skills/             # Built-in SkillPack definitions
│   ├── policies/           # Default SympoziumPolicy presets
│   └── samples/            # Example CRs
├── migrations/             # PostgreSQL schema migrations
├── docs/                   # Documentation (this site)
├── Makefile
└── README.md
```
