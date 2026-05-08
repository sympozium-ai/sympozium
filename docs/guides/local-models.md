# Local Models — Cluster-Local Inference

Run LLMs directly inside your Kubernetes cluster — no API keys, no external providers, no data leaving your network. Sympozium downloads the model weights, deploys a [llama.cpp](https://github.com/ggerganov/llama.cpp) inference server, and exposes an OpenAI-compatible endpoint that your agents use automatically.

## Quickstart: Zero to Local Inference in 5 Minutes

### 1. Deploy a Model

Open the web UI and navigate to **Models** in the sidebar. Click **Deploy Model**.

Pick a preset to get started quickly:

| Preset | Size | Context | Best for |
|--------|------|---------|----------|
| **Qwen3 8B (Q4)** | 5 GB | 8K tokens | General use, good quality |
| **Qwen3.5 9B (Q4)** | 5.7 GB | 8K tokens | Latest Qwen, best quality |
| **Phi-3 Mini 4K (Q4)** | 2.2 GB | 4K tokens | Fast, lightweight |
| **Qwen3 0.6B (Q8)** | 0.6 GB | 4K tokens | Testing, minimal resources |

Click a preset to auto-fill the form, then click **Deploy**.

The model will progress through phases: **Pending** → **Downloading** → **Loading** → **Ready**. Download time depends on model size and network speed.

### 2. Create an Agent

Once the model shows **Ready**, navigate to **Agents** → **Create Agent**.

The provider dropdown now shows your model at the top as **(Local Model)**. Select it — the wizard automatically:
- Skips the API key step (no key needed)
- Skips the model selector step (model is already known)
- Jumps straight to skills configuration

Complete the wizard and your instance is ready to use.

### 3. Run an Agent

From the instance detail page, trigger an ad-hoc run or connect a channel. The agent will use your cluster-local model for all inference — no external API calls.

### 4. (Optional) Deploy a Team

The **local-inference-example** ensemble provides a pre-configured 2-agent team (assistant + coder) that runs entirely on a local model. To use it:

1. Deploy a model named `qwen3-8b-q4` (use the Qwen3 8B preset)
2. Navigate to **Ensembles** → **local-inference-example** → **Activate**
3. The ensemble waits for the model to be Ready, then stamps out instances automatically

## How It Works

A `Model` is a declarative Kubernetes resource. The controller reconciles it into standard K8s primitives:

```
Model CR (you create)
  │
  ├─ PersistentVolumeClaim   (stores downloaded GGUF weights)
  ├─ Job                     (downloads GGUF from URL to PVC)
  ├─ Deployment              (llama-server serving the model)
  └─ Service                 (ClusterIP, OpenAI-compatible API)
```

Agents connect to the model via the Service endpoint (`http://model-<name>.sympozium-system.svc:8080/v1`). The `LLMProvider` interface treats it identically to OpenAI — no code changes needed.

## Deployment Methods

### Web UI

Models → Deploy Model → pick a preset or enter a custom GGUF URL → Deploy.

### kubectl

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: Model
metadata:
  name: qwen3-8b-q4
  namespace: sympozium-system
spec:
  source:
    url: "https://huggingface.co/Qwen/Qwen3-8B-GGUF/resolve/main/Qwen3-8B-Q4_K_M.gguf"
  storage:
    size: "8Gi"
  resources:
    memory: "8Gi"
    cpu: "4"
  inference:
    contextSize: 8192
```

```bash
kubectl apply -f model.yaml
kubectl get models -n sympozium-system -w
```

### Helm

```yaml
# values-local.yaml
models:
  enabled: true
  items:
    - name: qwen3-8b-q4
      url: "https://huggingface.co/Qwen/Qwen3-8B-GGUF/resolve/main/Qwen3-8B-Q4_K_M.gguf"
      storageSize: "8Gi"
      memory: "8Gi"
      contextSize: 8192
```

```bash
helm upgrade sympozium ./charts/sympozium -f values-local.yaml
```

## Connecting Models to Agents

### Single Instance (modelRef)

Reference a Model by name on an AgentRun:

```yaml
spec:
  model:
    modelRef: qwen3-8b-q4   # resolves to the Model's endpoint
```

The controller sets `provider: openai`, `baseURL: <endpoint>`, and requires no auth secret.

### Ensemble (modelRef)

Set `modelRef` at the ensemble level — all personas in the team use the same local model:

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: Ensemble
metadata:
  name: my-local-team
spec:
  modelRef: qwen3-8b-q4    # all personas use this model
  personas:
    - name: researcher
      systemPrompt: "You are a research assistant..."
    - name: writer
      systemPrompt: "You are a technical writer..."
```

The controller waits for the Model to reach Ready before creating instances. If the model isn't deployed yet, the ensemble stays pending and retries every 10 seconds.

### Web UI Auto-Wiring

When creating an Instance or activating an Ensemble via the web UI, Ready models appear automatically at the top of the provider dropdown as **(Local Model)**. No manual endpoint configuration needed.

## Model CRD Reference

### Spec

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `source.url` | string | required | GGUF download URL |
| `source.filename` | string | `model.gguf` | Target filename on PVC |
| `storage.size` | string | `10Gi` | PVC storage size |
| `storage.storageClass` | string | cluster default | PVC storage class |
| `inference.image` | string | `ghcr.io/ggml-org/llama.cpp:server` | Inference server image |
| `inference.port` | int | `8080` | Server listen port |
| `inference.contextSize` | int | `4096` | Max context window (tokens) |
| `inference.args` | []string | `[]` | Extra llama-server args |
| `resources.gpu` | int | `0` | GPU count (`nvidia.com/gpu`). 0 = CPU-only |
| `resources.memory` | string | `16Gi` | Memory request/limit |
| `resources.cpu` | string | `4` | CPU request/limit |
| `nodeSelector` | map | `{}` | Node scheduling constraints |
| `tolerations` | []Toleration | `[]` | Node taint tolerations |

### Status

| Field | Description |
|-------|-------------|
| `phase` | Pending, Downloading, Loading, Ready, or Failed |
| `endpoint` | Cluster-internal OpenAI-compatible URL (set when Ready) |
| `message` | Human-readable status detail |
| `conditions` | Downloaded, ServerReady |

## GPU Configuration

Models default to CPU-only inference (`gpu: 0`). For GPU acceleration:

```yaml
spec:
  resources:
    gpu: 1
  inference:
    contextSize: 32768
    args:
      - "--n-gpu-layers"
      - "99"           # offload all layers to GPU
  nodeSelector:
    gpu-type: a100     # schedule on GPU nodes
  tolerations:
    - key: nvidia.com/gpu
      operator: Exists
      effect: NoSchedule
```

## Context Size

The `contextSize` field controls the maximum token window. Choose based on your model and available memory:

| Context Size | Memory Impact | Use Case |
|-------------|---------------|----------|
| 2048 | Minimal | Short tasks, testing |
| 4096 | ~2 GB extra | Default, most tasks |
| 8192 | ~4 GB extra | Long documents, code |
| 32768 | ~16 GB extra | Full-context analysis (GPU recommended) |

Larger contexts require more memory. If the inference server OOMs, reduce `contextSize` or increase `resources.memory`.

## The "local-inference-example" Ensemble

Sympozium ships with a default ensemble designed for local models:

**Personas:**
- **Local Assistant** — general Q&A with persistent memory
- **Local Coder** — writes code with software-dev tools, delegates from assistant

**Prerequisites:** Deploy a Model named `qwen3-8b-q4`. The ensemble references it via `modelRef` and waits for it to be Ready.

**Activate via UI:** Ensembles → local-inference-example → Activate

**Activate via kubectl:**
```bash
kubectl patch ensemble local-inference-example -n sympozium-system \
  --type merge -p '{"spec":{"enabled":true}}'
```

## Cleanup

Delete a Model CR and all owned resources (PVC, Job, Deployment, Service) are cleaned up automatically:

```bash
kubectl delete model qwen3-8b-q4 -n sympozium-system
```

Or via the web UI: Models → click model → Delete.

## Troubleshooting

| Symptom | Check | Fix |
|---------|-------|-----|
| Stuck in Downloading | `kubectl logs -n sympozium-system job/model-<name>-download` | Check URL, network, PVC size |
| Stuck in Loading | `kubectl logs -n sympozium-system deploy/model-<name>` | Increase memory, check OOM |
| Failed phase | `kubectl describe model <name> -n sympozium-system` | See `status.message` |
| Context size error | Agent run logs show "exceeds available context" | Increase `inference.contextSize` or use a model with larger native context |
| Pod Pending (GPU) | `kubectl describe pod -n sympozium-system -l app.kubernetes.io/instance=<name>` | Set `gpu: 0` for CPU-only, or ensure GPU nodes are available |
| Download 404 | Model phase shows Failed | Update `source.url` — controller retries on spec change |
