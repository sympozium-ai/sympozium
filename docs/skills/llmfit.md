# LLMFit Skill (`llmfit`)

The `llmfit` SkillPack adds node-level model placement analysis to Sympozium.

It uses your `llmfit` project (`github.com/AlexsJones/llmfit`) inside a skill sidecar and lets agents answer questions like:

- "Which node is best for `Qwen/Qwen2.5-Coder-14B-Instruct`?"
- "Show top coding-model placements across all nodes"

---

## What it installs

- SkillPack manifest: `config/skills/llmfit.yaml`
- Sidecar image: `ghcr.io/sympozium-ai/sympozium/skill-llmfit:latest`
- Sidecar build context: `images/skill-llmfit/`
  - `Dockerfile`
  - `tool-executor.sh`
  - `llmfit-probe-json.sh`
  - `llmfit-cluster-fit.sh`

Helm bundled copy:
- `charts/sympozium/files/skills/llmfit.yaml`

---

## Runtime design

### Host access (default for `llmfit`)

The built-in `llmfit` sidecar now enables explicit host access by default so node-level hardware probes can read host information directly:

- `hostPID: true` (pod-level)
- Sidecar runs as `root` (`runAsRoot: true`)
- Read-only host mounts:
  - `/proc` → `/host/proc`
  - `/sys` → `/host/sys`
  - `/dev` → `/host/dev`
  - `/run/udev` → `/host/run/udev`

The sidecar also exports helper environment variables:

- `LLMFIT_HOST_PROC=/host/proc`
- `LLMFIT_HOST_SYS=/host/sys`
- `LLMFIT_HOST_DEV=/host/dev`
- `LLMFIT_HOST_UDEV=/host/run/udev`

This is configured in the SkillPack (`spec.sidecar.hostAccess`) and is not globally enabled for other skills.

### Binary source

The sidecar installs `llmfit` from GitHub releases (v0.5.8+), using architecture-aware assets:

- `x86_64-unknown-linux-musl` for `amd64`
- `aarch64-unknown-linux-musl` for `arm64`

This avoids host-level `brew` dependency and keeps installation deterministic in containers.

### Cluster workflow

The primary command is:

```bash
llmfit-cluster-fit.sh --model "Qwen/Qwen2.5-Coder-14B-Instruct" --use-case coding --min-fit good --limit 10
```

It:
1. Discovers nodes with `kubectl get nodes`
2. Spawns one short-lived probe pod per node (`nodeName` pinned)
3. Runs `llmfit` on each node (`system` + `recommend --json`)
4. Aggregates and ranks results in a single JSON payload

### REST API compatibility

If node-local daemons already run (`llmfit serve`), agent workflows can query:

- `/health`
- `/api/v1/system`
- `/api/v1/models/top`
- `/api/v1/models/{name}`

---

## RBAC

The skill provisions minimal scoped permissions:

- Namespace: `pods`, `pods/log` (`get/list/watch/create/delete`) for probe lifecycle
- Cluster: `nodes` (`get/list/watch`) for node discovery

RBAC controls Kubernetes API access only. Host-level access is configured separately via `spec.sidecar.hostAccess`.

---

## Usage examples

Preflight (recommended before queries):

```bash
which llmfit && llmfit --version && which kubectl && which jq
```

Top models on default settings:

```bash
llmfit-cluster-fit.sh --model "*" --min-fit good --limit 10 | jq '.ranked_nodes[:5]'
```

Top 5 candidate nodes for a coding model:

```bash
llmfit-cluster-fit.sh --model "Qwen2.5" --use-case coding --min-fit good --limit 10 | jq '.ranked_nodes[:5]'
```

Inspect full per-node evidence:

```bash
llmfit-cluster-fit.sh --model "Qwen2.5" | jq '.node_results'
```

If `ranked_nodes` is empty at `min-fit=good`, retry with:

```bash
llmfit-cluster-fit.sh --model "*" --min-fit marginal --limit 10
llmfit-cluster-fit.sh --model "*" --min-fit too_tight --limit 10
```

If preflight fails (`llmfit: not found`), this indicates a stale/mismatched sidecar image in-cluster rather than a query issue.

---

## Persona integration

`platform-team` now enables `llmfit` for the `sre-watchdog` persona so SRE flows can recommend model placement in chat without manual setup.
