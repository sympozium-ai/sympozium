# Skills & Sidecars

Most agent frameworks dump every tool into one shared process. One bad `kubectl delete` and your whole agent environment is toast. Sympozium does this completely differently.

## Isolated Skill Sidecars

**Every skill runs in its own sidecar container** — a separate, isolated process injected into the agent pod at runtime. Use skills to give agents cluster-admin capabilities (`kubectl`, `helm`, scaling) or domain-specific tools — each with ephemeral least-privilege RBAC that's garbage-collected when the run finishes. Toggle a skill on, and the controller automatically:

- Injects a dedicated sidecar container with only the binaries that skill needs (`kubectl`, `helm`, `terraform`, etc.)
- Provisions **ephemeral, least-privilege RBAC** scoped to that single agent run — no standing permissions, no god-roles
- Shares a `/workspace` volume so the agent can coordinate with the sidecar
- **Garbage-collects everything** when the run finishes — containers, roles, bindings, all gone

> _"Give the agent tools, not trust."_ — Skills get exactly the permissions they declare, for exactly as long as the run lasts, and not a second longer.

## How Sidecars Are Injected

```
Agent has skills: [k8s-ops]
  → AgentRun created
    → Controller resolves SkillPack "k8s-ops"
      → Finds sidecar: { image: skill-k8s-ops, rbac: [...] }
      → Injects sidecar container into pod
      → Creates Role + RoleBinding (namespace-scoped)
      → Creates ClusterRole + ClusterRoleBinding (cluster-wide access)
    → Pod runs with kubectl + RBAC available
    → On completion/deletion: all skill RBAC cleaned up
```

## Built-in Tools

Every agent pod has these tools available out of the box (no skill sidecar required for native tools):

| Tool | Type | Description |
|------|------|-------------|
| `execute_command` | IPC (sidecar) | Execute shell commands (`kubectl`, `bash`, `curl`, `jq`, etc.) in the skill sidecar container |
| `read_file` | Native | Read file contents from the pod filesystem |
| `write_file` | Native | Create or overwrite files under `/workspace` or `/tmp` |
| `list_directory` | Native | List directory contents with type, size, and name |
| `fetch_url` | Native | Fetch web pages or API endpoints. HTML is converted to readable plain text |
| `send_channel_message` | IPC (bridge) | Send a message through a connected channel |
| `schedule_task` | IPC (bridge) | Create, update, suspend, resume, or delete recurring `SympoziumSchedule` tasks |

!!! note
    **Native** tools run directly in the agent container. **IPC** tools communicate with sidecars or the IPC bridge via the shared `/ipc` volume. See the [Tool Authoring Guide](../guides/writing-tools.md) for how to add your own.

## Built-in SkillPacks

| SkillPack | Category | Sidecar | Description | Status |
|-----------|----------|---------|-------------|--------|
| `k8s-ops` | Kubernetes | `kubectl`, `curl`, `jq` | Cluster inspection, workload management, troubleshooting, scaling | **Stable** |
| `sre-observability` | SRE | `kubectl`, `curl`, `jq` | Prometheus/Loki/Kubernetes observability workflows | **Alpha** |
| `llmfit` | SRE | `llmfit`, `kubectl`, `jq` | Node-level model placement analysis | **Alpha** |
| `incident-response` | SRE | yes | Structured incident triage — gather context, diagnose root cause, suggest remediation | **Alpha** |
| `code-review` | Development | — | Code review guidelines and best practices | **Alpha** |
| `web-endpoint` | Connectivity | `web-proxy` | Expose agents as HTTP APIs — OpenAI-compatible and MCP | **Alpha** |

## Toggling Skills

```bash
# In the TUI: press 's' on an instance → Space to toggle skills
# Or via kubectl:
kubectl patch agent <name> --type=merge \
  -p '{"spec":{"skills":[{"skillPackRef":"k8s-ops"},{"skillPackRef":"llmfit"}]}}'
```

## Learn More

- [Writing Skills](../guides/writing-skills.md) — full walkthrough of building your own SkillPacks
- [LLMFit Skill](../skills/llmfit.md) — node-level model placement analysis
- [Web Endpoint Skill](../skills/web-endpoint.md) — expose agents as HTTP APIs
- [GitHub GitOps Skill](../skills/github-gitops.md) — GitHub integration for agent workflows
