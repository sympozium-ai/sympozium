# Using LM Studio with Sympozium

Sympozium supports [LM Studio](https://lmstudio.ai) as an LLM provider. LM Studio
exposes an OpenAI-compatible API, so Sympozium treats it identically to any
OpenAI-compatible endpoint. This lets you run agents on local hardware with
models downloaded through LM Studio's model browser.

---

## Prerequisites

- A running Kubernetes cluster (Kind, minikube, etc.)
- Sympozium installed (`sympozium install`)
- LM Studio installed on your host machine
- At least one model downloaded and loaded in LM Studio

---

## Starting the LM Studio server

1. Open LM Studio and download a model (e.g. `lmstudio-community/Meta-Llama-3-8B-Instruct-GGUF`)
2. Go to the **Developer** tab (left sidebar)
3. Load a model and click **Start Server**
4. Note the port — by default LM Studio serves on `http://localhost:1234`

### Binding to all interfaces

By default LM Studio binds to `127.0.0.1`, which is unreachable from
Kubernetes pods. To allow cluster access:

1. In the **Developer** tab, click the server settings (gear icon)
2. Enable **Serve on Local Network** (or set the bind address to `0.0.0.0`)
3. Restart the server

Alternatively, start LM Studio's server from the CLI:

```bash
lms server start --host 0.0.0.0 --port 1234
```

### Finding the host gateway IP

Agent pods need a routable IP to reach LM Studio on the host.

**Kind:**

```bash
docker exec kind-control-plane ip route | grep default | awk '{print $3}'
```

This typically returns something like `172.18.0.1`.

**minikube:**

```bash
minikube ssh -- ip route | grep default | awk '{print $3}'
```

**Cloud clusters:** LM Studio must be reachable from the cluster network. Use
the machine's private IP or consider using Ollama in-cluster instead.

### The base URL

Once you have the host gateway IP, the base URL is:

```
http://<host-gateway-ip>:1234/v1
```

For example: `http://172.18.0.1:1234/v1`

You can verify the server is reachable:

```bash
curl http://172.18.0.1:1234/v1/models
```

---

## Creating an Agent

LM Studio does not require an API key, but the `authRefs` field is mandatory —
create a Secret with a placeholder value.

```bash
kubectl create secret generic lmstudio-key \
  --from-literal=OPENAI_API_KEY=not-needed
```

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: Agent
metadata:
  name: lmstudio-agent
spec:
  agents:
    default:
      model: lmstudio-community/Meta-Llama-3-8B-Instruct-GGUF
      baseURL: "http://172.18.0.1:1234/v1"
  authRefs:
    - provider: openai
      secret: lmstudio-key
  skills:
    - skillPackRef: k8s-ops
  policyRef: default-policy
```

> **Note:** The `model` field should match the model identifier shown in LM
> Studio's Developer tab. LM Studio also accepts the alias of the currently
> loaded model — you can use any string and LM Studio will route to the active
> model.

---

## Running an AgentRun

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: AgentRun
metadata:
  name: lmstudio-test
spec:
  agentRef: lmstudio-agent
  agentId: default
  sessionKey: "lmstudio-test-001"
  task: "List all pods across every namespace and summarise their status."
  model:
    provider: openai
    model: lmstudio-community/Meta-Llama-3-8B-Instruct-GGUF
    baseURL: "http://172.18.0.1:1234/v1"
    authSecretRef: lmstudio-key
  skills:
    - skillPackRef: k8s-ops
  timeout: "5m"
```

```bash
kubectl apply -f lmstudio-test.yaml
kubectl get agentrun lmstudio-test -w
```

The phase transitions: `Pending` -> `Running` -> `Succeeded` (or `Failed`).

---

## Network policies

The default Sympozium network policies allow egress on port `11434` (Ollama)
but **not** port `1234` (LM Studio). You need to add an egress rule for LM
Studio's port.

Add the following to both `sympozium-agent-allow-egress` and
`sympozium-agent-server-allow-egress` in `config/network/policies.yaml`:

```yaml
# Allow LM Studio (default port 1234)
- to: []
  ports:
    - protocol: TCP
      port: 1234
```

Then apply:

```bash
kubectl apply -f config/network/policies.yaml
```

> **Sandbox note:** Pods with `sympozium.ai/sandbox: "true"` use the
> `sympozium-sandbox-restricted` policy that only allows DNS and localhost IPC.
> Sandboxed agents cannot reach LM Studio directly.

---

## Using with Ensembles

You can point an entire Ensemble at LM Studio by setting `baseURL` during
onboarding:

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: Ensemble
metadata:
  name: my-team
spec:
  baseURL: "http://172.18.0.1:1234/v1"
  authRefs:
    - provider: openai
      secret: lmstudio-key
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

## LM Studio vs Ollama

| Feature | LM Studio | Ollama |
|---------|-----------|--------|
| **GUI** | Full desktop app with model browser | CLI-first |
| **Default port** | 1234 | 11434 |
| **Model format** | GGUF | Ollama-native (GGUF under the hood) |
| **Tool calling** | Supported (model dependent) | Supported (model dependent) |
| **In-cluster deployment** | Not supported (desktop app) | Supported (container image) |
| **Node discovery** | Manual baseURL | Auto-discovery via node-probe DaemonSet |

Use **LM Studio** when you want a GUI for model management and are running
agents from a local dev cluster. Use **Ollama** when you need in-cluster
deployment or automatic node discovery.

---

## Supported models

Any model loaded in LM Studio works with Sympozium, as long as it supports the
OpenAI chat completions API format. Popular choices:

| Model | Parameters | Tool calling | Notes |
|-------|-----------|-------------|-------|
| `Meta-Llama-3-8B-Instruct` | 8B | Yes | Good general-purpose model |
| `Meta-Llama-3-70B-Instruct` | 70B | Yes | Higher quality, needs more VRAM |
| `Mistral-7B-Instruct` | 7B | Yes | Fast, good for simple tasks |
| `Qwen2.5-7B-Instruct` | 7B | Yes | Strong tool-calling support |
| `DeepSeek-R1-Distill` | 7B | No | Reasoning model, no tool use |

> **Tool calling:** Sympozium agents rely on tool calling to execute commands,
> read files, and interact with the cluster. Models without tool-calling
> support can still answer questions but cannot use skills or execute actions.

---

## Troubleshooting

### Agent pod fails to connect to LM Studio

**Symptom:** AgentRun fails with a connection refused or timeout error.

**Check the server is running:**

```bash
curl http://172.18.0.1:1234/v1/models
```

If this fails, ensure LM Studio's server is started and bound to `0.0.0.0`.

**Check from inside the cluster:**

```bash
kubectl run -it --rm debug --image=busybox -- wget -qO- http://172.18.0.1:1234/v1/models
```

### Model not loaded

**Symptom:** `model not found` or empty response from the API.

LM Studio requires a model to be **loaded** (not just downloaded). Open the
Developer tab and verify a model is loaded and the server is running.

### Network policy blocking traffic

**Symptom:** Agent pods timeout but LM Studio is reachable from the host.

Verify the egress rule for port `1234` is in place:

```bash
kubectl get networkpolicy -A | grep sympozium
```

### Slow responses

LM Studio runs inference on your local hardware. If responses are slow:

- Use a smaller or more quantized model (e.g. Q4_K_M instead of Q8_0)
- Increase the AgentRun timeout: `timeout: "15m"`
- Ensure your GPU is being utilized (check LM Studio's performance tab)
- Close other applications competing for GPU memory
