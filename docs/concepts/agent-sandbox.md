# Agent Sandbox (Kubernetes CRD)

Sympozium optionally integrates with [Kubernetes Agent Sandbox](https://github.com/kubernetes-sigs/agent-sandbox) (SIG Apps) to provide kernel-level isolation for AI agent workloads. When enabled, agent runs create `Sandbox` CRs instead of `Job` CRs, unlocking gVisor/Kata isolation, warm pools, and suspend/resume lifecycle.

## Why Agent Sandbox?

Sympozium's default execution model creates a Kubernetes Job per agent run with container-level security (read-only root filesystem, dropped capabilities, non-root user, seccomp). This is solid baseline isolation, but the agent and all its sidecars still share the host kernel.

Agent Sandbox adds an additional layer:

| Feature | Default (Job) | Agent Sandbox |
|---------|---------------|---------------|
| Isolation | Container (cgroups, namespaces) | Kernel-level (gVisor user-space kernel or Kata lightweight VMs) |
| Cold start | New Job + pod scheduling per run | Pre-warmed pools (SandboxWarmPool) for near-instant starts |
| Lifecycle | Run-to-completion, then deleted | Suspend/resume without losing state |
| Identity | Ephemeral pod name | Stable hostname and network identity |
| Overhead | Low | Slightly higher (gVisor ~5-10%, Kata ~VM overhead) |

## How It Works

```
AgentRun (agentSandbox.enabled: true)
  │
  ├─ Controller creates Sandbox CR (agents.x-k8s.io/v1alpha1)
  │   └─ spec.podTemplate.spec.runtimeClassName: gvisor
  │   └─ spec.podTemplate: same pod spec as a Job (agent + ipc-bridge + sidecars)
  │   └─ ownerReference → AgentRun (garbage-collected on deletion)
  │
  └─ OR (if warmPoolRef is set)
      └─ Controller creates SandboxClaim CR
          └─ Claims a pre-warmed sandbox from a SandboxWarmPool
```

The controller reuses the same container specs, volumes, env vars, and security contexts as the Job path. The only difference is the wrapper resource (Sandbox CR vs Job).

## Enabling Agent Sandbox

### 1. Install Agent Sandbox CRDs

```bash
# From the agent-sandbox release page:
export VERSION="v0.1.0"
kubectl apply -f https://github.com/kubernetes-sigs/agent-sandbox/releases/download/${VERSION}/manifest.yaml

# Or for testing, use the bundled minimal CRDs:
kubectl apply -f hack/agent-sandbox-crds.yaml
```

### 2. Configure Helm Values

```yaml
# values.yaml
agentSandbox:
  enabled: true                    # Master switch
  defaultRuntimeClass: "gvisor"    # Default for runs that don't specify one
  rbac: true                       # Grant controller RBAC for Sandbox CRDs
```

### 3. Install a Runtime Class (Production)

For actual kernel isolation, install gVisor or Kata on your nodes:

```bash
# gVisor (recommended for most use cases)
# See: https://gvisor.dev/docs/user_guide/quick_start/kubernetes/

# Kata Containers (for VM-level isolation)
# See: https://katacontainers.io/docs/install/
```

On KIND clusters, you can test the CRD integration without a runtime — the Sandbox CRs are created and managed correctly, but pods won't have kernel-level isolation.

## Configuration

### Per-Run (AgentRun)

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: AgentRun
metadata:
  name: my-secure-run
spec:
  agentRef: my-agent
  agentId: default
  sessionKey: "secure-run-001"
  task: "Analyze cluster security"
  model:
    provider: anthropic
    model: claude-sonnet-4-20250514
    authSecretRef: my-key
  agentSandbox:
    enabled: true
    runtimeClass: gvisor      # "gvisor", "kata", or custom
    warmPoolRef: wp-my-agent   # Optional: claim from a warm pool
```

### Per-Instance (Agent)

Set defaults for all runs from this instance:

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: Agent
metadata:
  name: secure-agent
spec:
  agents:
    default:
      model: claude-sonnet-4-20250514
      agentSandbox:
        enabled: true
        runtimeClass: gvisor
        warmPool:
          size: 3              # Pre-warm 3 sandboxes
          runtimeClass: gvisor
```

### Per-Policy (SympoziumPolicy)

Enforce agent-sandbox usage and restrict runtime classes:

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: SympoziumPolicy
metadata:
  name: hardened
spec:
  sandboxPolicy:
    required: true
    agentSandboxPolicy:
      required: true                        # All runs MUST use agent-sandbox
      defaultRuntimeClass: gvisor           # Injected when not specified
      allowedRuntimeClasses: [gvisor, kata] # Block unknown runtimes
```

### TUI

Both the onboard wizard and ensemble wizard include an "Agent Sandbox" step:

```
Step 7.5/9 — Agent Sandbox (K8s CRD)
  Uses kubernetes-sigs/agent-sandbox for kernel-level isolation (gVisor/Kata).
  Runs agents in Sandbox CRs instead of Jobs — provides stronger security,
  warm pools for fast cold starts, and suspend/resume lifecycle.
  Requires: agent-sandbox CRDs installed + gVisor/Kata runtime on nodes.
  Enable Agent Sandbox isolation? [y/N]
```

## Warm Pools

SandboxWarmPool maintains a pool of pre-provisioned sandboxes to eliminate cold starts:

```
Without warm pool:
  AgentRun created → Sandbox CR created → Pod scheduled → Container pulled → Ready
  (~5-30 seconds depending on image size and cluster load)

With warm pool:
  AgentRun created → SandboxClaim → Pre-warmed sandbox handed over → Ready
  (~1 second)
```

When configured on an Agent, the controller automatically creates and manages a `SandboxWarmPool` CR. Runs with `warmPoolRef` set create a `SandboxClaim` instead of a bare `Sandbox`.

## Relationship to Existing Safeguards

Agent Sandbox **complements** existing security layers — it does not replace them:

| Layer | Still active with Agent Sandbox? |
|-------|----------------------------------|
| NetworkPolicy deny-all | Yes — applied to the Sandbox pod |
| Pod SecurityContext (non-root, read-only, drop ALL) | Yes — embedded in the Sandbox CR's pod template |
| SympoziumPolicy admission webhook | Yes — validates before Sandbox CR creation |
| Ephemeral skill RBAC | Yes — same Role/ClusterRole lifecycle |
| Seccomp profile | Yes — RuntimeDefault applied to pod |
| **+ Kernel isolation (new)** | gVisor/Kata provides an additional layer between the container and host kernel |

## Mutual Exclusivity

The existing `sandbox` field (sidecar container sandbox) and `agentSandbox` (CRD execution backend) are separate concepts:

- `sandbox.enabled: true` — adds a sandbox sidecar container to the Job pod
- `agentSandbox.enabled: true` — creates a Sandbox CR instead of a Job

When both are set, `agentSandbox` takes priority. When a `SympoziumPolicy` is bound, the webhook enforces mutual exclusivity and denies the run.

## Graceful Degradation

| Scenario | Behavior |
|----------|----------|
| `agentSandbox.enabled=false` in Helm | No agent-sandbox code paths are active. No RBAC rules created. Zero overhead. |
| `agentSandbox.enabled=true` but CRDs not installed | Controller logs a warning and disables the feature. Runs with `agentSandbox.enabled` will fail with a clear error. |
| `agentSandbox.enabled=true`, CRDs installed, no gVisor/Kata | Sandbox CRs are created and work correctly. Pods run with standard container isolation (no kernel-level isolation). |

## Integration Test

Run the full integration test suite:

```bash
bash test/integration/test-agent-sandbox.sh
```

This verifies: CRD installation, controller detection, Sandbox CR creation, backward compatibility, metadata correctness, warm pool claims, garbage collection, and CRD schema validation.
