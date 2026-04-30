<p align="center">
  <img src="logo.png" alt="sympozium.ai logo" width="600px;">
</p>

<p align="center">

  <em>
  Every agent is an ephemeral Pod.<br>Every policy is a CRD. Every execution is a Job.<br>
  Orchestrate multi-agent workflows <b>and</b> let agents diagnose, scale, and remediate your infrastructure.<br>
  Multi-tenant. Horizontally scalable. Safe by design.</em><br><br>
  From the creator of <a href="https://github.com/k8sgpt-ai/k8sgpt">k8sgpt</a> and <a href="https://github.com/AlexsJones/llmfit">llmfit</a>
</p>

<p align="center">
  <b>
  This project is under active development. API's will change, things will be break. Be brave.
  <b />
</p>
<p align="center">
  <a href="https://github.com/sympozium-ai/sympozium/actions"><img src="https://github.com/sympozium-ai/sympozium/actions/workflows/build.yaml/badge.svg" alt="Build"></a>
  <a href="https://github.com/sympozium-ai/sympozium/releases/latest"><img src="https://img.shields.io/github/v/release/sympozium-ai/sympozium" alt="Release"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue" alt="License"></a>
</p>

<p align="center">
  <img src="demo.gif" alt="Sympozium TUI demo" width="800px;">
</p>

---

> **Full documentation:** [deploy.sympozium.ai/docs](https://deploy.sympozium.ai/docs/)

---

### Quick Install (macOS / Linux)

**Homebrew:**
```bash
brew tap sympozium-ai/sympozium
brew install sympozium
```

**Shell installer:**
```bash
curl -fsSL https://deploy.sympozium.ai/install.sh | sh
```

Then deploy to your cluster and activate your first agents:

```bash
sympozium install          # deploys CRDs, controllers, and built-in Ensembles
sympozium                  # launch the TUI — go to Personas tab, press Enter to onboard
sympozium serve            # open the web dashboard (port-forwards to the in-cluster UI)
```

### Advanced: Helm Chart

**Prerequisites:** [cert-manager](https://cert-manager.io/) (for webhook TLS):
```bash
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.17.1/cert-manager.yaml
```

Sympozium can be installed as two charts: `sympozium-crds` (the CRDs, so they can be upgraded) and `sympozium` (the control plane). Install the CRDs first, then the control plane:

```bash
helm repo add sympozium https://deploy.sympozium.ai/charts
helm repo update

helm upgrade --install sympozium-crds sympozium/sympozium-crds \
  --namespace sympozium-system --create-namespace

helm upgrade --install sympozium sympozium/sympozium \
  --namespace sympozium-system \
  --skip-crds --set createNamespace=false
```

> `--skip-crds` on the second command assumes you installed `sympozium-crds`
> first. If you skip the CRDs chart, drop `--skip-crds` so the bundled CRDs
> in the `sympozium` chart are applied instead.

See [`charts/sympozium/values.yaml`](charts/sympozium/values.yaml) for configuration options, or the [Helm Chart docs](https://deploy.sympozium.ai/docs/reference/helm/) for the full guide.

---

## Why Sympozium?

Sympozium serves **two powerful use cases** on one Kubernetes-native platform:

1. **Orchestrate fleets of AI agents** — customer support, code review, data pipelines, or any domain-specific workflow. Each agent gets its own pod, RBAC, and network policy with proper tenant isolation.
2. **Administer the cluster itself agentically** — point agents inward to diagnose failures, scale deployments, triage alerts, and remediate issues, all with Kubernetes-native isolation, RBAC, and audit trails.

### Key Features

| | |
|---|---|
| **Local Model Inference** | Declare GGUF models as CRDs — weights are downloaded, llama-server deployed, and OpenAI-compatible endpoints exposed. No API keys required |
| **Ensembles** | Helm-like bundles for AI agent teams — activate a pack and the controller stamps out instances, schedules, and memory |
| **Agent Workflows** | Delegation, sequential pipelines, and supervision relationships between personas — visualised on an interactive canvas |
| **Shared Workflow Memory** | Pack-level SQLite memory pool for cross-persona knowledge sharing with per-persona access control |
| **Skill Sidecars** | Every skill runs in its own sidecar with ephemeral least-privilege RBAC, garbage-collected on completion |
| **Multi-Channel** | Telegram, Slack, Discord, WhatsApp — each channel is a dedicated Deployment backed by NATS JetStream |
| **Persistent Memory** | SQLite + FTS5 on a PersistentVolume — memories survive across ephemeral pod runs |
| **Scheduled Heartbeats** | Cron-based recurring agent runs for health checks, alert triage, and resource right-sizing |
| **Agent Sandbox** | Kernel-level isolation via [kubernetes-sigs/agent-sandbox](https://deploy.sympozium.ai/docs/concepts/agent-sandbox/) — gVisor or Kata with warm pools for instant starts |
| **MCP Servers** | External tool providers via Model Context Protocol with auto-discovery and allow/deny filtering |
| **TUI & Web UI** | Terminal and browser dashboards with live workflow canvas, or skip the UI entirely with Helm and kubectl |
| **Any AI Provider** | OpenAI, Anthropic, Azure, Ollama, or any compatible endpoint — no vendor lock-in |

---

## Documentation

| Topic | Link |
|-------|------|
| Getting Started | [deploy.sympozium.ai/docs/getting-started](https://deploy.sympozium.ai/docs/getting-started/) |
| Architecture | [deploy.sympozium.ai/docs/architecture](https://deploy.sympozium.ai/docs/architecture/) |
| Custom Resources | [deploy.sympozium.ai/docs/concepts/custom-resources](https://deploy.sympozium.ai/docs/concepts/custom-resources/) |
| Ensembles | [deploy.sympozium.ai/docs/concepts/ensembles](https://deploy.sympozium.ai/docs/concepts/ensembles/) |
| Skills & Sidecars | [deploy.sympozium.ai/docs/concepts/skills](https://deploy.sympozium.ai/docs/concepts/skills/) |
| Persistent Memory | [deploy.sympozium.ai/docs/concepts/persistent-memory](https://deploy.sympozium.ai/docs/concepts/persistent-memory/) |
| Channels | [deploy.sympozium.ai/docs/concepts/channels](https://deploy.sympozium.ai/docs/concepts/channels/) |
| Agent Sandboxing | [deploy.sympozium.ai/docs/concepts/agent-sandbox](https://deploy.sympozium.ai/docs/concepts/agent-sandbox/) |
| Security | [deploy.sympozium.ai/docs/concepts/security](https://deploy.sympozium.ai/docs/concepts/security/) |
| CLI & TUI Reference | [deploy.sympozium.ai/docs/reference/cli](https://deploy.sympozium.ai/docs/reference/cli/) |
| Helm Chart | [deploy.sympozium.ai/docs/reference/helm](https://deploy.sympozium.ai/docs/reference/helm/) |
| Local Models | [deploy.sympozium.ai/docs/guides/local-models](https://deploy.sympozium.ai/docs/guides/local-models/) |
| Ollama & Local Inference | [deploy.sympozium.ai/docs/guides/ollama](https://deploy.sympozium.ai/docs/guides/ollama/) |
| Writing Skills | [deploy.sympozium.ai/docs/guides/writing-skills](https://deploy.sympozium.ai/docs/guides/writing-skills/) |
| Writing Tools | [deploy.sympozium.ai/docs/guides/writing-tools](https://deploy.sympozium.ai/docs/guides/writing-tools/) |
| LM Studio & Local Inference | [deploy.sympozium.ai/docs/guides/lm-studio](https://deploy.sympozium.ai/docs/guides/lm-studio/) |
| llama-server | [deploy.sympozium.ai/docs/guides/llama-server](https://deploy.sympozium.ai/docs/guides/llama-server/) |
| Unsloth | [deploy.sympozium.ai/docs/guides/unsloth](https://deploy.sympozium.ai/docs/guides/unsloth/) |
| Writing Ensembles | [deploy.sympozium.ai/docs/guides/writing-ensembles](https://deploy.sympozium.ai/docs/guides/writing-ensembles/) |
| Your First AgentRun | [deploy.sympozium.ai/docs/guides/first-agentrun](https://deploy.sympozium.ai/docs/guides/first-agentrun/) |

---

## Development

```bash
make test        # run tests
make lint        # run linter
make manifests   # generate CRD manifests
make run         # run controller locally (needs kubeconfig)
```

## License

Apache License 2.0
