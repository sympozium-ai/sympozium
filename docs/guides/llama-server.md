# Using llama-server with Sympozium

Sympozium supports [llama-server](https://github.com/ggerganov/llama.cpp/tree/master/tools/server) (the official llama.cpp HTTP server) as an LLM provider. llama-server exposes an OpenAI-compatible API, so Sympozium treats it identically to any OpenAI-compatible endpoint. This lets you run agents on local GPU hardware with GGUF models.

---

## Prerequisites

- A running Kubernetes cluster (Kind, minikube, etc.)
- Sympozium installed (`sympozium install`)
- llama-server installed on your host machine (`brew install llama.cpp` or build from source)
- A GGUF model downloaded or a Hugging Face model reference

---

## Starting llama-server

```bash
llama-server \
    -hf unsloth/gemma-4-26B-A4B-it-GGUF:Q8_0 \
    --port 8080 \
    --n-gpu-layers 999 \
    --ctx-size 65536 \
    --batch-size 8192 \
    --ubatch-size 2048 \
    --threads 16 \
    --flash-attn on \
    --cont-batching
```

By default llama-server binds to `127.0.0.1:8080`. To allow cluster access,
bind to all interfaces:

```bash
llama-server --host 0.0.0.0 --port 8080 -hf <model>
```

### Verify the server is running

```bash
curl http://localhost:8080/health
curl http://localhost:8080/v1/models
```

### Finding the host gateway IP

Agent pods need a routable IP to reach llama-server on the host.

**Kind:**

```bash
docker exec kind-control-plane ip route | grep default | awk '{print $3}'
```

This typically returns something like `172.18.0.1`.

**minikube:**

```bash
minikube ssh -- ip route | grep default | awk '{print $3}'
```

**Cloud clusters:** llama-server must be reachable from the cluster network. Use
the machine's private IP.

### The base URL

Once you have the host gateway IP, the base URL is:

```
http://<host-gateway-ip>:8080/v1
```

For example: `http://172.18.0.1:8080/v1`

---

## Node discovery

The node-probe DaemonSet automatically discovers llama-server instances running
on port 8080 (via the `llama-cpp` probe target). When llama-server is detected,
the web UI wizard will show the node as available under the "Installed on Node"
inference mode.

No additional configuration is needed — the existing `llama-cpp` node-probe
target covers llama-server since they share the same endpoints (`/health` and
`/v1/models`).

---

## Creating an Agent

llama-server does not require an API key, but the `authRefs` field is mandatory —
create a Secret with a placeholder value.

```bash
kubectl create secret generic llama-server-key \
  --from-literal=OPENAI_API_KEY=not-needed
```

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: Agent
metadata:
  name: llama-server-agent
spec:
  agents:
    default:
      model: unsloth/gemma-4-26B-A4B-it-GGUF
      baseURL: "http://172.18.0.1:8080/v1"
  authRefs:
    - provider: llama-server
      secret: llama-server-key
  skills:
    - skillPackRef: k8s-ops
  policyRef: default-policy
```

---

## Running an AgentRun

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: AgentRun
metadata:
  name: llama-server-test
spec:
  agentRef: llama-server-agent
  agentId: default
  sessionKey: "llama-server-test-001"
  task: "List all pods across every namespace and summarise their status."
  model:
    provider: llama-server
    model: unsloth/gemma-4-26B-A4B-it-GGUF
    baseURL: "http://172.18.0.1:8080/v1"
    authSecretRef: llama-server-key
  skills:
    - skillPackRef: k8s-ops
  timeout: "5m"
```

```bash
kubectl apply -f llama-server-test.yaml
kubectl get agentrun llama-server-test -w
```

The phase transitions: `Pending` -> `Running` -> `Succeeded` (or `Failed`).

---

## Network policies

The default Sympozium network policies may not include egress on port `8080` for
agent pods. If needed, add an egress rule:

```yaml
# Allow llama-server (default port 8080)
- to: []
  ports:
    - protocol: TCP
      port: 8080
```

Add this to both `sympozium-agent-allow-egress` and
`sympozium-agent-server-allow-egress`, then apply:

```bash
kubectl apply -f config/network/policies.yaml
```

> **Sandbox note:** Pods with `sympozium.ai/sandbox: "true"` use the
> `sympozium-sandbox-restricted` policy that only allows DNS and localhost IPC.
> Sandboxed agents cannot reach llama-server directly.

---

## Using with Ensembles

You can point an entire Ensemble at llama-server by setting `baseURL` during
onboarding:

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: Ensemble
metadata:
  name: my-team
spec:
  baseURL: "http://172.18.0.1:8080/v1"
  authRefs:
    - provider: llama-server
      secret: llama-server-key
  agentConfigs:
    - name: assistant
      displayName: "Local Assistant"
      systemPrompt: |
        You are a helpful assistant running on local hardware.
      skills:
        - k8s-ops
      schedule:
        type: heartbeat
        interval: "1h"
        task: "Check cluster health."
```

---

## llama-server vs LM Studio vs Ollama

| Feature | llama-server | LM Studio | Ollama |
|---------|-------------|-----------|--------|
| **Interface** | CLI | Desktop GUI | CLI + API |
| **Default port** | 8080 | 1234 | 11434 |
| **Model format** | GGUF / HF download | GGUF | Ollama-native (GGUF) |
| **GPU support** | CUDA, Metal, Vulkan | Metal, CUDA | Metal, CUDA |
| **Tool calling** | Supported (model dependent) | Supported (model dependent) | Supported (model dependent) |
| **In-cluster deployment** | Container image available | Not supported (desktop app) | Supported (container image) |
| **Node discovery** | Auto-discovery via node-probe | Manual baseURL | Auto-discovery via node-probe |
| **Continuous batching** | Yes | No | Yes |

Use **llama-server** when you want maximum control over inference parameters
(context size, batch size, GPU layers) and are comfortable with CLI tooling.
Use **LM Studio** for a GUI-first experience. Use **Ollama** for the simplest
setup or in-cluster deployment.

---

## Supported models

Any GGUF model or Hugging Face model works with llama-server. Popular choices:

| Model | Parameters | Tool calling | Notes |
|-------|-----------|-------------|-------|
| `Meta-Llama-3-8B-Instruct` | 8B | Yes | Good general-purpose model |
| `Meta-Llama-3-70B-Instruct` | 70B | Yes | Higher quality, needs more VRAM |
| `Qwen2.5-7B-Instruct` | 7B | Yes | Strong tool-calling support |
| `gemma-4-26B-A4B-it` | 26B (4B active) | Yes | Efficient MoE architecture |
| `DeepSeek-R1-Distill` | 7B | No | Reasoning model, no tool use |

> **Tool calling:** Sympozium agents rely on tool calling to execute commands,
> read files, and interact with the cluster. Models without tool-calling
> support can still answer questions but cannot use skills or execute actions.

---

## Local development note

llama-server's default port (8080) may conflict with the Sympozium API server
during local development. The `make dev` target runs the API on port 8081 by
default, leaving port 8080 free for llama-server. If you encounter a port
conflict, either start llama-server on a different port (`--port 8087`) or
override the API port: `API_ADDR=:8082 make dev`.

---

## Troubleshooting

### Agent pod fails to connect to llama-server

**Symptom:** AgentRun fails with a connection refused or timeout error.

**Check the server is running:**

```bash
curl http://172.18.0.1:8080/health
```

If this fails, ensure llama-server is started and bound to `0.0.0.0`.

**Check from inside the cluster:**

```bash
kubectl run -it --rm debug --image=busybox -- wget -qO- http://172.18.0.1:8080/v1/models
```

### Model not loading

**Symptom:** llama-server exits with an error or runs out of memory.

- Use a smaller quantization (e.g. Q4_K_M instead of Q8_0)
- Reduce `--n-gpu-layers` to offload fewer layers to GPU
- Reduce `--ctx-size` to lower memory usage

### Network policy blocking traffic

**Symptom:** Agent pods timeout but llama-server is reachable from the host.

Verify the egress rule for port `8080` is in place:

```bash
kubectl get networkpolicy -A | grep sympozium
```

### Slow responses

- Use a smaller or more quantized model
- Increase the AgentRun timeout: `timeout: "15m"`
- Enable `--flash-attn on` for faster attention computation
- Enable `--cont-batching` for better throughput with concurrent requests
- Ensure GPU offloading is active (`--n-gpu-layers 999`)
