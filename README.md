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
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache%202.0-blue" alt="License"></a>
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
sympozium install          # deploys CRDs, controllers, and built-in PersonaPacks
sympozium                  # launch the TUI — go to Personas tab, press Enter to onboard
sympozium serve            # open the web dashboard (port-forwards to the in-cluster UI)
```

### Advanced: Helm Chart

**Prerequisites:** [cert-manager](https://cert-manager.io/) (for webhook TLS):
```bash
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.17.1/cert-manager.yaml
```

Deploy the Sympozium control plane:
```bash
helm install sympozium ./charts/sympozium
```

See [`charts/sympozium/values.yaml`](charts/sympozium/values.yaml) for configuration options.

---

## Why Sympozium?

Sympozium serves **two powerful use cases** on one Kubernetes-native platform:

1. **Orchestrate fleets of AI agents** — customer support, code review, data pipelines, or any domain-specific workflow. Each agent gets its own pod, RBAC, and network policy with proper tenant isolation.
2. **Administer the cluster itself agentically** — point agents inward to diagnose failures, scale deployments, triage alerts, and remediate issues, all with Kubernetes-native isolation, RBAC, and audit trails.

### Isolated Skill Sidecars

**Every skill runs in its own sidecar container** — a separate, isolated process injected into the agent pod at runtime with ephemeral least-privilege RBAC that's garbage-collected when the run finishes.

> _"Give the agent tools, not trust."_

---

## Documentation

| Topic | Link |
|-------|------|
| Getting Started | [deploy.sympozium.ai/docs/getting-started](https://deploy.sympozium.ai/docs/getting-started/) |
| Architecture | [deploy.sympozium.ai/docs/architecture](https://deploy.sympozium.ai/docs/architecture/) |
| Custom Resources | [deploy.sympozium.ai/docs/concepts/custom-resources](https://deploy.sympozium.ai/docs/concepts/custom-resources/) |
| PersonaPacks | [deploy.sympozium.ai/docs/concepts/personapacks](https://deploy.sympozium.ai/docs/concepts/personapacks/) |
| Skills & Sidecars | [deploy.sympozium.ai/docs/concepts/skills](https://deploy.sympozium.ai/docs/concepts/skills/) |
| Channels | [deploy.sympozium.ai/docs/concepts/channels](https://deploy.sympozium.ai/docs/concepts/channels/) |
| Security | [deploy.sympozium.ai/docs/concepts/security](https://deploy.sympozium.ai/docs/concepts/security/) |
| CLI & TUI Reference | [deploy.sympozium.ai/docs/reference/cli](https://deploy.sympozium.ai/docs/reference/cli/) |
| Helm Chart | [deploy.sympozium.ai/docs/reference/helm](https://deploy.sympozium.ai/docs/reference/helm/) |
| Ollama & Local Inference | [deploy.sympozium.ai/docs/guides/ollama](https://deploy.sympozium.ai/docs/guides/ollama/) |
| Writing Skills | [deploy.sympozium.ai/docs/guides/writing-skills](https://deploy.sympozium.ai/docs/guides/writing-skills/) |
| Writing Tools | [deploy.sympozium.ai/docs/guides/writing-tools](https://deploy.sympozium.ai/docs/guides/writing-tools/) |

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
