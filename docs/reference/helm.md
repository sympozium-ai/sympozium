# Helm Chart

For production and GitOps workflows, deploy the control plane using Helm.

## Prerequisites

[cert-manager](https://cert-manager.io/) is required for webhook TLS:

```bash
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.17.1/cert-manager.yaml
```

## Install

```bash
helm install sympozium ./charts/sympozium
```

See [`charts/sympozium/values.yaml`](https://github.com/sympozium-ai/sympozium/blob/main/charts/sympozium/values.yaml) for all configuration options (replicas, resources, external NATS, network policies, etc.).

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

The node-probe DaemonSet discovers inference providers (Ollama, vLLM, llama-cpp) installed directly on cluster nodes. It probes localhost ports and annotates nodes so the web wizard can offer model selection and node pinning.

```yaml
nodeProbe:
  enabled: false          # Opt-in: deploys a DaemonSet on all nodes
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

## Network Policies

```yaml
networkPolicies:
  enabled: true
  extraEgressPorts: []    # add non-standard API server ports here (e.g. [6444] for k3s)
```
