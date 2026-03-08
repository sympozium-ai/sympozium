# Using Ollama with Sympozium

Sympozium has first-class support for [Ollama](https://ollama.com) as an LLM
provider. Ollama exposes an OpenAI-compatible API, so Sympozium treats it as an
OpenAI-compatible endpoint with the provider set to `ollama`. This lets you run
agents entirely on local hardware with no cloud API keys required.

---

## Prerequisites

- A running Kubernetes cluster (Kind, minikube, etc.)
- Sympozium installed (`sympozium install`)
- Ollama installed on your host machine (or deployed in-cluster)
- At least one model pulled (`ollama pull llama3`)

---

## Configuring Ollama for Kubernetes access

By default Ollama binds to `127.0.0.1:11434`, which means only processes on
the same machine can reach it. Kubernetes pods need to connect over the network,
so you must change the bind address to `0.0.0.0`.

### systemd (Linux)

Create an override file:

```bash
sudo systemctl edit ollama
```

Add the following:

```ini
[Service]
Environment="OLLAMA_HOST=0.0.0.0"
```

Then reload and restart:

```bash
sudo systemctl daemon-reload
sudo systemctl restart ollama
```

### macOS / manual

Export the variable before starting Ollama:

```bash
OLLAMA_HOST=0.0.0.0 ollama serve
```

### Finding the host gateway IP

Agent pods running inside the cluster need a routable IP to reach the host.
The method depends on your cluster type.

**Kind:**

```bash
docker exec kind-control-plane ip route | grep default | awk '{print $3}'
```

This typically returns something like `172.18.0.1`.

**minikube:**

```bash
minikube ssh -- ip route | grep default | awk '{print $3}'
```

**Cloud clusters:** Ollama must be reachable from the cluster network. Use
the machine's private IP or deploy Ollama in-cluster (see below).

### The base URL

Once you have the host gateway IP, the base URL for Ollama is:

```
http://<host-gateway-ip>:11434/v1
```

For example: `http://172.18.0.1:11434/v1`

---

## Creating a SympoziumInstance

Create a SympoziumInstance that points at your Ollama server. Ollama does not
require an API key, but the `authRefs` field is mandatory -- create a Secret
with a placeholder value.

```bash
kubectl create secret generic ollama-key \
  --from-literal=OPENAI_API_KEY=not-needed
```

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: SympoziumInstance
metadata:
  name: ollama-agent
spec:
  agents:
    default:
      model: llama3
      baseURL: "http://172.18.0.1:11434/v1"
  authRefs:
    - provider: ollama
      secret: ollama-key
  skills:
    - skillPackRef: k8s-ops
  policyRef: default-policy
```

> **Note:** If you omit `baseURL` and set the provider to `ollama`, the
> agent-runner defaults to `http://ollama.default.svc:11434/v1`. This is
> useful when Ollama is deployed in-cluster in the `default` namespace.

---

## Running an AgentRun

You can run a one-off task against your Ollama instance:

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: AgentRun
metadata:
  name: ollama-test
spec:
  instanceRef: ollama-agent
  task: "List all pods across every namespace and summarise their status."
  model:
    provider: ollama
    model: llama3
    baseURL: "http://172.18.0.1:11434/v1"
    authSecretRef: ollama-key
  skills:
    - k8s-ops
  timeout: "5m"
```

```bash
kubectl apply -f ollama-test.yaml
kubectl get agentrun ollama-test -w
```

The phase transitions: `Pending` -> `Running` -> `Succeeded` (or `Failed`).

---

## Serving mode (web endpoint)

You can expose an Ollama-backed agent as an HTTP API using the `web-endpoint`
skill. This deploys a long-lived web-proxy sidecar with OpenAI-compatible
chat completions and MCP protocol support.

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: SympoziumInstance
metadata:
  name: ollama-server
spec:
  agents:
    default:
      model: llama3
      baseURL: "http://172.18.0.1:11434/v1"
  authRefs:
    - provider: ollama
      secret: ollama-key
  skills:
    - skillPackRef: k8s-ops
    - skillPackRef: web-endpoint
  policyRef: default-policy
```

Once the instance is running, the web-proxy creates a Service and (optionally)
an HTTPRoute. See [Web Endpoint Skill](skill-web-endpoint.md) for full details
on authentication, rate limiting, and routing.

---

## Network policies

The default network policies in `config/network/policies.yaml` already include
egress rules for port `11434` (Ollama's default port). This applies to both
task-mode (`sympozium-agent-allow-egress`) and server-mode
(`sympozium-agent-server-allow-egress`) agent pods:

```yaml
# Allow Ollama / local LLM provider (default port 11434)
- to: []
  ports:
    - protocol: TCP
      port: 11434
```

No additional network policy configuration is needed for Ollama. If you run
Ollama on a non-standard port, add a matching egress rule to both the
`sympozium-agent-allow-egress` and `sympozium-agent-server-allow-egress`
policies.

> **Sandbox note:** Pods with `sympozium.ai/sandbox: "true"` use the
> `sympozium-sandbox-restricted` policy that only allows DNS and localhost IPC.
> Sandboxed agents cannot reach Ollama directly.

---

## In-cluster Ollama

For production setups or GPU-equipped clusters, deploy Ollama as a Kubernetes
Deployment:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ollama
  namespace: ollama
spec:
  replicas: 1
  selector:
    matchLabels:
      app: ollama
  template:
    metadata:
      labels:
        app: ollama
    spec:
      containers:
        - name: ollama
          image: ollama/ollama:latest
          ports:
            - containerPort: 11434
          resources:
            limits:
              nvidia.com/gpu: "1"   # remove if CPU-only
---
apiVersion: v1
kind: Service
metadata:
  name: ollama
  namespace: ollama
spec:
  selector:
    app: ollama
  ports:
    - port: 11434
      targetPort: 11434
```

The base URL becomes the in-cluster DNS name:

```
http://ollama.ollama.svc:11434/v1
```

Update your SympoziumInstance accordingly:

```yaml
agents:
  default:
    model: llama3
    baseURL: "http://ollama.ollama.svc:11434/v1"
```

If you deploy Ollama in the `default` namespace with a Service named `ollama`,
you can omit the `baseURL` entirely -- the agent-runner defaults to
`http://ollama.default.svc:11434/v1` when the provider is `ollama`.

---

## Supported models

Any model available through Ollama works with Sympozium. Pull a model with
`ollama pull <model>` and reference it by name in the `model` field.

Popular choices:

| Model | Parameters | Tool calling | Notes |
|-------|-----------|-------------|-------|
| `llama3` | 8B | Yes | Good general-purpose model |
| `llama3:70b` | 70B | Yes | Higher quality, needs more RAM/VRAM |
| `mistral` | 7B | Yes | Fast, good for simple tasks |
| `qwen2.5` | 7B | Yes | Strong tool-calling support |
| `deepseek-r1` | 7B | No | Reasoning model, no tool use |
| `codellama` | 7B | No | Code-focused, no tool calling |

> **Tool calling:** Sympozium agents rely on tool calling to execute commands,
> read files, and interact with the cluster. Models without tool-calling
> support can still answer questions but cannot use skills or execute actions.

---

## Troubleshooting

### Agent pod fails to connect to Ollama

**Symptom:** AgentRun fails with a connection refused or timeout error.

**Check the bind address:**

```bash
# Verify Ollama is listening on all interfaces
ss -tlnp | grep 11434
# Should show 0.0.0.0:11434, not 127.0.0.1:11434
```

**Check the host gateway IP:**

```bash
# From inside a cluster pod
kubectl run -it --rm debug --image=busybox -- wget -qO- http://172.18.0.1:11434/v1/models
```

### Model not found

**Symptom:** `model 'xyz' not found` error in agent logs.

Make sure the model is pulled on the Ollama host:

```bash
ollama list          # see available models
ollama pull llama3   # pull if missing
```

The model name in your SympoziumInstance must match exactly (e.g. `llama3`,
not `meta-llama/llama3`).

### Network policy blocking traffic

**Symptom:** Agent pods timeout but Ollama is reachable from the host.

Verify the network policies are applied:

```bash
kubectl get networkpolicy -A | grep sympozium
```

Ensure the `sympozium-agent-allow-egress` policy includes port `11434`. If you
customised the policies, re-apply the defaults:

```bash
kubectl apply -f config/network/policies.yaml
```

### Slow responses with large models

Ollama runs inference on your hardware. If responses are slow or time out:

- Use a smaller model (e.g. `llama3` 8B instead of 70B)
- Increase the AgentRun timeout: `timeout: "15m"`
- Ensure Ollama has GPU access (`nvidia-smi` to verify)
- For in-cluster deployments, request GPU resources in the Deployment spec
