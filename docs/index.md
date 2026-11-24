# Sympozium

**A coordination layer for multi-agent AI systems on Kubernetes**

Sympozium does not run agents itself. It sits above an execution plane —
[Agent Substrate](concepts/substrate-backend.md) by default — and adds what an
execution plane does not have: each Agent's identity and keys, policy,
coordination between agents, reach through channels, schedules and an API,
and operations as Kubernetes resources. Every policy is a CRD.

Sympozium decides what agents *do*. How agents run belongs to the execution
plane: Agent Substrate by default, with [Celln](concepts/celln-backend.md)
microVMs or a Kubernetes Job as alternatives. Where compute happens belongs to
[llmfit-dra](https://github.com/sympozium-ai/llmfit-dra); how tokens move belongs
to the serving engine. See [Positioning](positioning.md) for the boundary.

<!-- STATUS:substrate -->
!!! warning "State of the Substrate backend, 2026-11-24"
    Agent Substrate is the default execution plane by decision of
    2026-11-24. The implementation is on a branch and not released. Where
    Substrate is installed and enabled (chart value `substrate.enabled`), a
    run that names no backend runs as an actor when it can: a one-shot
    string task with a model key, with channel messages and schedules served
    outside the sandbox under the run's tool policy. A run that needs
    skills, MCP servers, memory, delegation or an enduring conversation runs
    as a Kubernetes Job instead, and its `DefaultExecutionPlane` condition
    says why. A plain `sympozium install` does not install Substrate, so
    there every run is a Job. One in-cluster hop, from Substrate's egress
    gateway to the model gateway, is unencrypted. See [Substrate
    backend](concepts/substrate-backend.md).

<p align="center">
  <img src="assets/demo.gif" alt="Sympozium TUI demo" width="800px">
</p>

---

## Quick Install

=== "Homebrew"

    ```bash
    brew tap sympozium-ai/sympozium
    brew install sympozium
    ```

=== "Shell Installer"

    ```bash
    curl -fsSL https://deploy.sympozium.ai/install.sh | sh
    ```

Then deploy to your cluster and activate your first agents:

```bash
sympozium install          # deploys CRDs, the control plane, built-in Ensembles and ergoz;
                           # nothing about models is asked. Agents run as Kubernetes Jobs
sympozium install --celln-fleet   # also the Celln fleet on every KVM node and the model gateway
sympozium                  # launch the TUI — go to the Ensembles tab, press Enter to onboard
sympozium serve            # open the web dashboard (port-forwards to the in-cluster UI)
```

!!! tip "New here?"
    See the [Getting Started guide](getting-started.md) — install, deploy, onboard your first agent, and learn the TUI, web UI, and CLI commands.

---

## Why Sympozium?

Sympozium is a **Kubernetes-native coordination layer for AI agent teams**. Deploy agents for customer support, code review, data pipelines, incident response, or any domain-specific workflow. Each run executes in its own sandbox, and what it may do is decided outside that sandbox.

What Sympozium adds above an execution plane:

| | |
|---|---|
| **Identity and keys** | Each Agent owns its provider, model and key. On Substrate and Celln the key is added per call by a gateway and never enters the sandbox |
| **Policy** | `SympoziumPolicy`, tool allow and deny, and admission checks, never enforced by the agent itself |
| **Coordination** | Ensembles, delegation and shared memory |
| **Reach** | Channels, schedules and an OpenAI-compatible Agent API |
| **Operations** | CRDs under your RBAC and GitOps; console, TUI and CLI; token and cost tracking |

Bundle agents into **Ensembles** with delegation, sequential pipelines, and supervision relationships. Give them persistent memory, external tools via MCP servers, and cron schedules — all declared as CRDs and reconciled by controllers.

Every concept that traditional agent frameworks manage in application code, Sympozium expresses as a Kubernetes resource — declarative, reconcilable, observable, and scalable.

---

## Key Features

- **A sandbox per run, on the execution plane you choose** — an [Agent Substrate](concepts/substrate-backend.md) actor by default, a Celln microVM, or a Kubernetes Job with its own security context
- **Hardware-isolated agents (Celln)** — the self-hosted alternative: one-shot and long-running agents in KVM microVMs on every labelled node; each Agent brings its own model connection and key, which a gateway adds per call and no node ever holds
- **AgentHarness** — run approved external harnesses (Pi, Hermes) as one-shot runs or persistent chat sessions
- **Skill sidecars** — on the Job backend, every skill runs in its own container with auto-provisioned, least-privilege RBAC
- **Ensembles** — pre-configured bundles of agents that activate with a few keypresses
- **Multiple interfaces** — k9s-style TUI, full web dashboard, or CLI
- **Channel integrations** — Telegram, Slack, Discord, WhatsApp
- **Persistent memory** — agents retain context across runs via a SQLite + FTS5 memory server on a PersistentVolume
- **Policy-as-CRD** — feature and tool gating enforced at admission time
- **OpenTelemetry** — built-in observability with traces, metrics, token usage and cost estimates; accelerator power via ergoz
- **Web endpoints** — expose agents as OpenAI-compatible APIs and MCP servers
- **Scheduled tasks** — cron-based recurring agent runs
- **Local inference discovery** — node-probe DaemonSet discovers Ollama/vLLM/llama-cpp on host nodes with automatic model listing and node pinning

---

## Learn More

| Topic | Description |
|-------|-------------|
| [Getting Started](getting-started.md) | Install, deploy, and onboard your first agent |
| [Architecture](architecture.md) | System design and how it all fits together |
| [Custom Resources](concepts/custom-resources.md) | The CRDs that model every agentic concept |
| [Ensembles](concepts/ensembles.md) | Pre-configured agent bundles |
| [Skills & Sidecars](concepts/skills.md) | Isolated tool containers with ephemeral RBAC |
| [Lifecycle Hooks](concepts/lifecycle-hooks.md) | PreRun and postRun containers for setup and teardown |
| [Positioning](positioning.md) | What Sympozium adds above an execution plane, and what it is not |
| [Substrate Backend](concepts/substrate-backend.md) | The default execution plane: a run as an Agent Substrate actor |
| [Celln Backend](concepts/celln-backend.md) | The self-hosted alternative: hardware-isolated one-shot and enduring agents |
| [Celln Fleet Installation](guides/celln-fleet-installation.md) | Install, verify, upgrade and operate the fleet; toolbox and capacity |
| [Agent-bound model access](guides/celln-mediated-model-access.md) | Connections, keys, origin pins and the model gateway |
| [AgentHarness](guides/agentharness.md) | Approved external harnesses and persistent chat sessions |
| [Security](concepts/security.md) | Defence-in-depth at every layer |
| [Writing Skills](guides/writing-skills.md) | Build your own SkillPacks |
| [Writing Tools](guides/writing-tools.md) | Add new tools to the agent runner |
| [Model Connections](guides/model-connections.md) | Reusable model routes for persistent Hermes and native Celln harnesses |
| [Ollama & Local Inference](guides/ollama.md) | Node-based and in-cluster Ollama setup with auto-discovery |
| [LM Studio](guides/lm-studio.md) | Local GGUF model serving with desktop GUI |
| [llama-server](guides/llama-server.md) | llama.cpp server with full GPU control and node auto-discovery |
| [Unsloth](guides/unsloth.md) | Fine-tuned models served via llama.cpp or vLLM |
| [AWS Bedrock](guides/aws-bedrock.md) | Amazon Bedrock setup with Claude, Nova, and other foundation models |

---

## Project Links

- [GitHub Repository](https://github.com/sympozium-ai/sympozium)
- [Releases](https://github.com/sympozium-ai/sympozium/releases)
- [License](https://github.com/sympozium-ai/sympozium/blob/main/LICENSE) — MIT
