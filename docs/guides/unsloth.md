# Using Unsloth with Sympozium

Sympozium supports [Unsloth](https://unsloth.ai) as an LLM provider. Unsloth
is primarily a fine-tuning library, but its [Run Tutorials](https://unsloth.ai/docs/models/gemma-4#run-gemma-4-tutorials)
walk you through serving fine-tuned (or stock) models over an OpenAI-compatible
HTTP API via `llama.cpp`'s `llama-server` or vLLM. Sympozium treats Unsloth
exactly like any other OpenAI-compatible endpoint, so you can point an instance
at a locally-running Unsloth model and drive it with skills, channels, and
schedules like any cloud-backed agent.

---

## Prerequisites

- A running Kubernetes cluster (Kind, minikube, etc.)
- Sympozium installed (`sympozium install`)
- Unsloth installed on your host machine (see the
  [Unsloth install docs](https://docs.unsloth.ai/get-started/installing-+-updating))
- A model exported to GGUF (for `llama.cpp`) or served directly (for vLLM)

---

## Starting the Unsloth server

Unsloth itself is a training library — it does not ship its own serve
endpoint. Follow one of Unsloth's run tutorials (e.g.
[Run Gemma 3](https://unsloth.ai/docs/models/gemma-4#run-gemma-4-tutorials))
to serve a model over HTTP. Two common paths:

### Option A — llama.cpp `llama-server` (GGUF)

After exporting your model to GGUF with Unsloth:

```bash
./llama-server \
  --model ./unsloth.Q4_K_M.gguf \
  --host 0.0.0.0 \
  --port 8080 \
  --jinja
```

This exposes an OpenAI-compatible API at `http://localhost:8080/v1`.

### Option B — vLLM

```bash
vllm serve unsloth/gemma-3-12b-it --host 0.0.0.0 --port 8000
```

This exposes an OpenAI-compatible API at `http://localhost:8000/v1`.

> **Bind to `0.0.0.0`:** Agent pods cannot reach `127.0.0.1` on the host —
> always bind the server to `0.0.0.0` (or explicitly to the host gateway IP).

### Finding the host gateway IP

**Kind:**

```bash
docker exec kind-control-plane ip route | grep default | awk '{print $3}'
```

**minikube:**

```bash
minikube ssh -- ip route | grep default | awk '{print $3}'
```

### The base URL

```
http://<host-gateway-ip>:8080/v1    # llama-server
http://<host-gateway-ip>:8000/v1    # vLLM
```

Verify reachability:

```bash
curl http://172.18.0.1:8080/v1/models
```

---

## Creating an Agent

Unsloth-served models do not require an API key, but `authRefs` is mandatory —
create a Secret with a placeholder value.

```bash
kubectl create secret generic unsloth-key \
  --from-literal=OPENAI_API_KEY=not-needed
```

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: Agent
metadata:
  name: unsloth-agent
spec:
  agents:
    default:
      model: unsloth/gemma-3-12b-it
      baseURL: "http://172.18.0.1:8080/v1"
  authRefs:
    - provider: unsloth
      secret: unsloth-key
  skills:
    - skillPackRef: k8s-ops
  policyRef: default-policy
```

> **Note:** The `model` field should match the ID reported by
> `/v1/models` — for `llama-server` this is usually the GGUF filename or the
> alias you passed via `--alias`; for vLLM it is the HuggingFace repo ID you
> loaded.

---

## Running an AgentRun

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: AgentRun
metadata:
  name: unsloth-test
spec:
  agentRef: unsloth-agent
  agentId: default
  sessionKey: "unsloth-test-001"
  task: "List all pods across every namespace and summarise their status."
  model:
    provider: unsloth
    model: unsloth/gemma-3-12b-it
    baseURL: "http://172.18.0.1:8080/v1"
    authSecretRef: unsloth-key
  skills:
    - skillPackRef: k8s-ops
  timeout: "5m"
```

```bash
kubectl apply -f unsloth-test.yaml
kubectl get agentrun unsloth-test -w
```

The phase transitions: `Pending` → `Running` → `Succeeded` (or `Failed`).

Because Unsloth runs locally, Sympozium applies local-provider timeouts
automatically (5 min per request, 30 min per run, 2 retries).

---

## Network policies

The default Sympozium network policies do **not** open egress on 8080 or
8000. You need to add an egress rule for whichever port your Unsloth server
listens on.

Add to both `sympozium-agent-allow-egress` and
`sympozium-agent-server-allow-egress` in `config/network/policies.yaml`:

```yaml
# Allow Unsloth via llama-server (port 8080) or vLLM (port 8000)
- to: []
  ports:
    - protocol: TCP
      port: 8080
    - protocol: TCP
      port: 8000
```

Apply:

```bash
kubectl apply -f config/network/policies.yaml
```

> **Sandbox note:** Pods with `sympozium.ai/sandbox: "true"` use the
> `sympozium-sandbox-restricted` policy that only allows DNS and localhost IPC.
> Sandboxed agents cannot reach Unsloth directly.

---

## Node discovery

Sympozium's node-probe DaemonSet already probes port `8080` under the
`llama-cpp` target name and port `8000` under the `vllm` target — both of
which will detect an Unsloth-served model running on those ports. The
discovered models appear under the corresponding provider annotation on the
node. There is intentionally no separate `unsloth` node-probe target to avoid
port conflicts with those existing targets.

---

## Using with Ensembles

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: Ensemble
metadata:
  name: my-team
spec:
  baseURL: "http://172.18.0.1:8080/v1"
  authRefs:
    - provider: unsloth
      secret: unsloth-key
  agentConfigs:
    - name: assistant
      displayName: "Unsloth Assistant"
      systemPrompt: |
        You are a helpful assistant running on a locally-served Unsloth model.
      skills:
        - k8s-ops
      schedule:
        type: heartbeat
        interval: "1h"
        task: "Check cluster health."
```

---

## Unsloth vs LM Studio vs Ollama

| Feature | Unsloth | LM Studio | Ollama |
|---------|---------|-----------|--------|
| **Primary role** | Fine-tuning + serve via llama.cpp/vLLM | GUI model server | CLI model server |
| **GUI** | None (Python / Jupyter) | Full desktop app | CLI-first |
| **Default port** | 8080 (llama-server) or 8000 (vLLM) | 1234 | 11434 |
| **Model format** | GGUF / HF / vLLM | GGUF | Ollama-native |
| **Tool calling** | Depends on serve layer (`--jinja` for llama-server) | Supported (model dependent) | Supported (model dependent) |
| **In-cluster deployment** | Custom (requires packaging) | Not supported | Supported |
| **Strengths** | Fast fine-tuning of your own LoRA, then serve | Easy model browsing | In-cluster + auto-discovery |

Use **Unsloth** when you've fine-tuned a model with Unsloth and want to run
Sympozium agents against that exact model.

---

## Troubleshooting

### Agent pod fails to connect

**Symptom:** AgentRun fails with connection refused or timeout.

```bash
curl http://172.18.0.1:8080/v1/models
```

If this fails, ensure your Unsloth serve process is running and bound to
`0.0.0.0`.

### Tool calls never arrive

**Symptom:** Agent chats but never invokes skills.

Make sure `llama-server` was started with `--jinja` (for Gemma/Qwen/Llama3
tool-calling templates). Without this flag, tool-call JSON is emitted as
plain text and never parsed into structured `tool_calls`.

### Slow responses

- Use a smaller quant (Q4_K_M instead of Q8_0)
- Increase AgentRun timeout: `timeout: "15m"`
- Verify GPU offload (`--n-gpu-layers` for llama-server)
