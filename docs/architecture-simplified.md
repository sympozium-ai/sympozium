# Sympozium — Simplified Architecture

```mermaid
flowchart TB
    subgraph social["Social Media Channels"]
        direction LR
        discord["Discord"]
        slack["Slack"]
        telegram["Telegram"]
        whatsapp["WhatsApp"]
    end

    subgraph k8s["Kubernetes Cluster"]

        subgraph ctrl["Control Plane"]
            cm["Controller Manager"]
            router["Channel Router"]
        end

        nats["NATS JetStream<br/>Event Bus"]

        subgraph channels["Channel Pods"]
            dp["discord-pod"]
            sp["slack-pod"]
            tp["telegram-pod"]
            wp["whatsapp-pod"]
        end

        subgraph crds["Custom Resources (CRDs)"]
            ensemble["Ensemble<br/><i>multi-agent team</i>"]
            instance["SympoziumInstance<br/><i>agent deployment</i>"]
            agentrun["AgentRun<br/><i>single execution</i>"]
            policy["SympoziumPolicy<br/><i>governance</i>"]
            skillpack["SkillPack<br/><i>tool bundles</i>"]
            schedule["SympoziumSchedule<br/><i>cron triggers</i>"]
            mcpserver["MCPServer<br/><i>tool provider</i>"]
            model["Model<br/><i>cluster-local inference</i>"]
        end

        subgraph localinf["Local Model Inference"]
            llamasrv["llama-server<br/><i>OpenAI-compatible API</i>"]
            modelpvc[("PVC<br/><i>GGUF weights</i>")]
            llamasrv --- modelpvc
        end

        subgraph exec["Agent Execution Pod"]
            runner["Agent Runner<br/><i>LLM provider ↔ tool loop</i>"]
            sidecars["Skill Sidecars<br/><i>kubectl, gh, custom</i>"]
            mcp["MCP Bridge<br/><i>JSON-RPC 2.0</i>"]
            memory["Persistent Memory<br/><i>SQLite + FTS5</i>"]
        end

        subgraph security["K8s-Native Security"]
            rbac["Ephemeral RBAC<br/><i>per-run least-privilege</i>"]
            netpol["NetworkPolicy<br/><i>deny-all + allow-list</i>"]
            sandbox["Agent Sandbox<br/><i>gVisor / Kata</i>"]
            secrets["K8s Secrets<br/><i>auth credentials</i>"]
            secctx["SecurityContext<br/><i>non-root, read-only fs</i>"]
        end

        subgraph workflow["Multi-Agent Workflows"]
            spawner["Spawner<br/><i>orchestrator</i>"]
            delegation["Delegation<br/><i>request + await</i>"]
            sequential["Sequential<br/><i>pipeline</i>"]
            autonomous["Autonomous<br/><i>scheduled</i>"]
        end
    end

    subgraph providers["Bring Your Own Provider"]
        direction LR
        openai["OpenAI"]
        anthropic["Anthropic"]
        azure["Azure OpenAI"]
        bedrock["AWS Bedrock"]
        local["Local<br/><i>Ollama / LM Studio / vLLM</i>"]
    end

    %% Social → Channel Pods
    discord --- dp
    slack --- sp
    telegram --- tp
    whatsapp --- wp

    %% Channel Pods ↔ Event Bus
    channels <-->|"messages"| nats

    %% Control Plane ↔ Event Bus
    router <-->|"route messages<br/>↔ AgentRuns"| nats
    cm -->|"reconcile CRDs"| crds

    %% CRDs drive execution
    ensemble -->|"stamps out"| instance
    instance -->|"creates"| agentrun
    schedule -->|"triggers"| agentrun
    agentrun -->|"launches"| exec
    policy -->|"enforces"| exec
    skillpack -->|"injects"| sidecars

    %% Execution ↔ Event Bus
    runner <-->|"events + streaming"| nats

    %% Multi-agent
    spawner <-->|"spawn requests"| nats
    ensemble -->|"persona<br/>relationships"| workflow

    %% Security applied to execution
    security -.->|"applied to"| exec

    %% Provider connection
    runner <-->|"LLMProvider<br/>interface"| providers

    %% MCP
    mcpserver -->|"registers"| mcp

    %% Local Model Inference
    model -->|"deploys"| localinf
    runner <-->|"modelRef<br/>OpenAI-compat"| llamasrv

    %% Styling
    classDef social fill:#7289da,stroke:#5b6eae,color:#fff
    classDef k8native fill:#326ce5,stroke:#2457b5,color:#fff
    classDef provider fill:#10a37f,stroke:#0d8a6a,color:#fff
    classDef security fill:#e8553d,stroke:#c4432e,color:#fff
    classDef workflow fill:#9b59b6,stroke:#7d3c98,color:#fff
    classDef exec fill:#f39c12,stroke:#d68910,color:#fff

    class discord,slack,telegram,whatsapp social
    class dp,sp,tp,wp social
    classDef localmodel fill:#059669,stroke:#047857,color:#fff
    class cm,router,nats,ensemble,instance,agentrun,policy,skillpack,schedule,mcpserver,model k8native
    class llamasrv,modelpvc localmodel
    class openai,anthropic,azure,bedrock,local provider
    class rbac,netpol,sandbox,secrets,secctx security
    class spawner,delegation,sequential,autonomous workflow
    class runner,sidecars,mcp,memory exec
```

## Key Callouts

### Multi-Agent Workflows
Ensembles define **persona relationships** — directed edges between agents with three workflow types:
- **Delegation** — agent A spawns agent B, awaits result
- **Sequential** — ordered pipeline execution across personas
- **Autonomous** — independent cron-scheduled execution

The **Spawner** orchestrates runtime delegation via the NATS event bus, validating relationships before allowing cross-agent calls.

### Kubernetes-Native Primitives & Security
Every component is a **CRD** reconciled by standard controllers:
- **Ephemeral RBAC** — per-run Role/RoleBinding with least-privilege, auto-deleted on completion
- **NetworkPolicy** — deny-all default + explicit allow-list for DNS, event bus, and external APIs
- **Agent Sandbox** — optional gVisor/Kata kernel isolation via the `agent-sandbox` CRD
- **SecurityContext** — non-root, read-only root filesystem, dropped capabilities
- **K8s Secrets** — auth credentials mounted as volumes, never embedded in CRD specs

### Bring Your Own Provider
A single **`LLMProvider` interface** (`Chat`, `AddToolResults`, `Name`, `Model`) abstracts all backends:
- Cloud: OpenAI, Anthropic, Azure OpenAI, AWS Bedrock
- Local: Ollama, LM Studio, vLLM, llama-server

Configured per-agent via `ModelSpec` — just set `provider`, `model`, and point an `authSecretRef` at a K8s Secret.

### Cluster-Local Model Inference
The **`Model` CRD** makes local inference declarative — apply a Model and the controller handles everything:
- Downloads GGUF weights to a PVC
- Deploys a llama-server with GPU resources
- Exposes an OpenAI-compatible endpoint as a ClusterIP Service
- AgentRuns reference models via `modelRef` — no API key needed

Models appear automatically as provider options in the web UI onboarding wizard. Deploy via `kubectl apply`, the web UI, or Helm values.
