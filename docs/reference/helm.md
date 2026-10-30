# Helm Chart

For production and GitOps workflows, deploy the control plane using Helm.

## Prerequisites

[cert-manager](https://cert-manager.io/) is required for webhook TLS:

```bash
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.17.1/cert-manager.yaml
```

## Install

Sympozium ships as two charts: `sympozium-crds` (the CRDs) and `sympozium` (the control plane). The CRDs live in their own chart so `helm upgrade` can roll schema changes forward — Helm 3 [never touches files under a chart's `crds/` directory on upgrade](https://helm.sh/docs/chart_best_practices/custom_resource_definitions/). Install the CRDs first, then the control plane:

```bash
helm upgrade --install sympozium-crds ./charts/sympozium-crds \
  --namespace sympozium-system --create-namespace

helm upgrade --install sympozium ./charts/sympozium \
  --namespace sympozium-system \
  --skip-crds --set createNamespace=false
```

Both charts are kept in lockstep. Always upgrade `sympozium-crds` before `sympozium`.

`--skip-crds` on the second command assumes the `sympozium-crds` release is installed. If you choose to use only the `sympozium` chart, omit `--skip-crds` so the CRDs bundled in that chart are applied — but you will then forfeit the ability to roll CRD schema changes forward via `helm upgrade`.

> **Memory upgrade.** Upgrading to the release with versioned memory (update/forget) copies every memory database to a new file (`memory.v2.db`) and leaves the old `memory.db` unchanged, so `helm rollback` needs no restore. Memory written after the upgrade is not visible to the older release. Before upgrading again after a rollback, see [Persistent Memory — Upgrading and rolling back](../concepts/persistent-memory.md#upgrading-and-rolling-back).

> **Uninstall ordering.** Removing `sympozium-crds` cascade-deletes every Agent, AgentRun, SkillPack, Ensemble, SympoziumPolicy, etc. across **all** namespaces. Always `helm uninstall sympozium` first, then `helm uninstall sympozium-crds`.

> The legacy single-chart install (`helm install sympozium ./charts/sympozium`) still works for fresh clusters that will never need a CRD upgrade.

See [`charts/sympozium/values.yaml`](https://github.com/sympozium-ai/sympozium/blob/main/charts/sympozium/values.yaml) for all configuration options. The `sympozium-crds` chart has no configurable values.

### Celln (hardware-isolated execution)

The raw chart default is `celln.enabled: false`, and so is `sympozium
install`'s: `sympozium install --celln-fleet` turns it on and sets every
value below (a release that already runs the fleet keeps it on a bare rerun). Celln's two
DaemonSets are **privileged** (they mount `/dev/kvm`, `/boot`, `/lib/modules`
and a state directory to boot micro-VMs) and schedule only on nodes labelled
`celln.dev/kvm=true`; the node probe applies that label to nodes with
`/dev/kvm` and a kernel under `/boot` (`nodeProbe.labelKVMNodes`). The fleet
holds no model: every Agent brings its own `ModelConnection` and key, which
the model gateway adds to its requests. See
[Celln Backend](../concepts/celln-backend.md).

- **`celln.fleet.*`** runs one owner per labeled node: `celln-node-configure`
  prepares each node from a digest-pinned, signed starter package
  (`scope`, `package.image`, `package.hash`, `publisher`, `principal`) and
  `celln-node` serves one-shot and enduring parents. `limits` are the scope's
  per-parent ceilings, `httpsHosts` the hosts the HTTPS tools may reach, and
  `capacity`/`memoryPercent`/`cellMemoryBytes`/`maxCellsCeiling` size each
  node. The 0.11 values `backends`, `model`, `modelCredentialsSecret`,
  `modelCredential` and `backendsExtraRevision` are refused.
- **`celln.mediation.*`** wires the controller, the fleet dispatchers and the
  model gateway to one issuer, keyset, CA and token set. It requires the
  fleet and `modelGateway.image`, and `clusterId`, `issuer.keyId` and the
  four trust objects (`controllerSecret`, `gatewaySecret`, `nodeSecret`,
  `trustConfigMap`), which the installer bootstraps and which a Helm-only
  install gets from `sympozium celln-mediation bootstrap`. The 0.11 values
  `routes`, `defaultRoutes` and `mediateBackends` are refused: an Agent's
  `ModelConnection` is its route; narrow it with `allowedOrigins` and
  `deniedOrigins` on the scope's `CellnExecutionPolicy`.
- **`celln.router.*`, `celln.bootstrap.*`, `celln.tokenSecret`,
  `celln.capabilityTokenSecret`, `celln.image.*`** are the router and the
  generated credentials the installer sets; the router needs the fleet (the
  single in-cluster dispatcher and the host installer were removed in 0.12).

Driving the fleet by plain Helm is possible but the installer derives a dozen
values, publishes trust the chart never renders and installs the scope's
catalogue; prefer `sympozium install`. See
[Celln Fleet Installation](../guides/celln-fleet-installation.md).

Disable all Celln resources with `--set celln.enabled=false`.

### Energy collector (ergoz)

```yaml
energyCollector:
  enabled: true
  discovery:
    namespaces: [sympozium-system, ergoz-system]
```

Any Service labelled `sympozium.ai/collector=energy` in one of the listed
namespaces is used for accelerator power draw (`GET /api/v1/power`, shown on
the density and topology views). The namespace list is a security
allowlist: only list namespaces an administrator controls. `sympozium
install` deploys the pinned [ergoz](https://github.com/sympozium-ai/ergoz)
release (`config/ergoz/release.json`) into `ergoz-system` as a separate Helm
release; `--no-ergoz` skips it, and a failed ergoz install never fails the
Sympozium install. With plain Helm, install ergoz yourself if you want it.

### Pricing and memory

```yaml
pricing:
  enabled: true        # ship the sympozium-model-pricing ConfigMap; cost estimates on AgentRuns
  extraEntries: []     # extra/overriding price entries (provider, match, inputPerMTokMicro, outputPerMTokMicro)
memory:
  adminDelete:
    enabled: true      # admin-only DELETE /delete on memory servers, bearer token never given to agents
```

Cost estimates are display-only and never gate runs; local providers are
always exempt.

### Model gateway

The model gateway is how every Celln Agent's model requests leave a cell: it
adds that Agent's own key, from the Secret its `ModelConnection` names, and
no node holds one. `celln.mediation.enabled` deploys it from Secret/ConfigMap
volumes; `modelGateway.image` must be an immutable `repository@sha256:…`
digest (the installer pins the release's). Operator inputs:

```yaml
modelGateway:
  image: ghcr.io/sympozium-ai/sympozium/model-gateway@sha256:<digest>
  egress: []              # NetworkPolicy egress; empty allows any provider, a list restricts it
  namespaces: []          # tenant namespaces the gateway may read; empty keeps the cluster-wide get
  privateOrigins: []      # exact origins on private addresses the gateway may reach, e.g. http://192.168.1.237:8080
  providerCA: {configMap: "", key: ca.crt}   # extra CA, trusted only for privateOrigins
  database:
    secretName: ""        # your PostgreSQL URL Secret (key database-url); empty runs the bundled single-pod PostgreSQL
    bundled: {storage: 1Gi, storageClassName: ""}
```

See [Agent-bound model access](../guides/celln-mediated-model-access.md).

### AgentHarness examples

The chart installs only the maintained, experimental **persistent** Pi and
Hermes adapter catalog entries by default, alongside the built-in Ensemble
catalog. They appear in
**Agents → Harnesses** when the UI is set to the chart namespace (normally
`sympozium-system`). Each is pinned to an immutable digest and carries its
conformance reference.

The chart also creates a `harness-examples` policy that permits only these two
images. It does **not** create or modify an Agent, so no external harness runs
until an operator explicitly selects both that policy and a runtime on an
Agent. The session adapters persist their own conversation state on a
per-session PVC and are currently unmetered; they do not support MCP/SkillPack
tools, native tool filtering, personas, or subagents. One-shot Pi and Hermes
runtimes remain available in values as explicit opt-in examples and are hidden
from the default interactive catalog.

Disable the catalog, or select only one adapter, with values such as:

```yaml
harnessExamples:
  enabled: false
  # Or retain the persistent catalog but install Pi only:
  piSession:
    enabled: true
  hermesSession:
    enabled: false
```

## Observability

The Helm chart deploys a built-in OpenTelemetry collector by default:

```yaml
observability:
  enabled: true
  collector:
    service:
      otlpGrpcPort: 4317
      otlpHttpPort: 4318
      metricsPort: 8889
```

Disable it if you already run a shared collector:

```yaml
observability:
  enabled: false
```

## Web UI

```yaml
apiserver:
  webUI:
    enabled: true       # Serve the embedded web dashboard (default: true)
    token: ""           # Explicit token; leave blank to auto-generate a Secret
```

If `token` is left empty, Helm creates a `<release>-ui-token` Secret with a random 32-character token.

## Node Probe (inference provider discovery)

The node-probe DaemonSet discovers inference providers (Ollama, vLLM, llama-cpp, LM Studio) installed directly on cluster nodes. It probes localhost ports and annotates nodes so the web wizard can offer model selection and node pinning. It also labels KVM-capable nodes `celln.dev/kvm=true` for Celln; a label an operator set (either value) is never changed.

```yaml
nodeProbe:
  enabled: true           # Deployed by default on all nodes
  labelKVMNodes: true     # Label nodes with /dev/kvm and a /boot kernel celln.dev/kvm=true
  config:
    probeInterval: 30s
    targets:
      - name: ollama
        port: 11434
        healthPath: /api/tags
        modelsPath: /api/tags
      - name: vllm
        port: 8000
        healthPath: /health
        modelsPath: /v1/models
      - name: llama-cpp
        port: 8080
        healthPath: /health
        modelsPath: /v1/models
      - name: lm-studio
        port: 1234
        healthPath: /v1/models
        modelsPath: /v1/models
  resources:
    requests:
      cpu: 50m
      memory: 32Mi
    limits:
      cpu: 100m
      memory: 64Mi
  tolerations:
    - operator: Exists    # Run on all nodes including GPU-tainted nodes
  nodeSelector: {}        # Limit which nodes run the probe
```

The DaemonSet uses `hostNetwork: true` to probe localhost on the host. It runs as non-root with a read-only filesystem. Its RBAC is minimal: `nodes: [get, patch]`.

See the [Ollama guide](../guides/ollama.md) for full details on node-based inference.

## llmfit DaemonSet (hardware density telemetry)

The llmfit DaemonSet runs on every node, continuously reporting hardware specs and model density scores. The controller and API server poll each pod to build a cluster-wide FitnessCache that powers instant model placement, the Model Density UI, Prometheus metrics, and the density API.

```yaml
llmfit:
  daemonset:
    enabled: true           # Deployed by default
    eventInterval: 60       # Seconds between fitness polls
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
    enabled: false          # Re-place models when fitness degrades
    checkInterval: 30s
    degradeThreshold: 0.3
  webhook:
    preflightValidation: false  # Reject Models that won't fit
```

The DaemonSet mounts `/proc`, `/sys`, `/dev`, `/run/udev` read-only for hardware detection. It runs as non-root with `readOnlyRootFilesystem: true` and `SYS_PTRACE` for `/proc` access. RBAC is minimal: `nodes: [get]`.

When `enabled: true` (default), Model CRs with `placement.mode: auto` use cached density data for instant placement instead of spawning probe pods.

See the [llmfit skill docs](../skills/llmfit.md) for full details on the density API, web UI, and Prometheus metrics.

## Agent Sandbox (Kubernetes CRD)

Integrates with [kubernetes-sigs/agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox) for kernel-level isolation (gVisor/Kata), warm pools, and suspend/resume lifecycle. See the [Agent Sandbox concept doc](../concepts/agent-sandbox.md) for details.

```yaml
agentSandbox:
  enabled: false                   # Master switch — requires agent-sandbox CRDs installed
  defaultRuntimeClass: "gvisor"    # Default runtimeClassName for Sandbox CRs
  rbac: true                       # Grant controller RBAC for Sandbox/SandboxClaim/SandboxWarmPool
```

When `enabled: true`, the controller:
- Creates RBAC rules for `agents.x-k8s.io` resources (Sandbox, SandboxClaim, SandboxWarmPool)
- Checks for agent-sandbox CRDs at startup and enables the feature if found
- Routes AgentRuns with `agentSandbox.enabled: true` to Sandbox CRs instead of Jobs

## Network Policies

```yaml
networkPolicies:
  enabled: true
  extraEgressPorts: []    # add non-standard API server ports here (e.g. [6444] for k3s)
```
