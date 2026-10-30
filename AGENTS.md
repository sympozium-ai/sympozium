# AGENTS.md — Contributor Guide for AI Agents

This file helps AI coding agents (Copilot, Cursor, Cline, etc.) understand the Sympozium project structure and development workflow.

---

## Project Overview

Sympozium is a **Kubernetes-native agent orchestration platform** written in Go. An AI agent runs either as an ephemeral Kubernetes pod (Job — the default `job` backend) or in a hardware-isolated Celln KVM microVM (`backend: celln`, one-shot or enduring), with policy enforcement via CRDs, admission webhooks, and RBAC. Pod-path communication flows through NATS JetStream and a filesystem-based IPC bridge.

`CLAUDE.md` lists the conventions that most often go wrong (naming, secrets, pod security, generated files).

- **Language:** Go 1.25+
- **Module:** `github.com/sympozium-ai/sympozium`
- **K8s API version:** `sympozium.ai/v1alpha1`

---

## Repository Layout

```
api/v1alpha1/           # CRD type definitions (see Key CRDs below)
cmd/
  agent-runner/         # Agent container — LLM loop + tool execution
  apiserver/            # HTTP + WebSocket API server (+ embedded web UI)
  controller/           # Controller manager (all reconcilers + routers)
  webhook/              # Admission webhook server (validation only)
  ipc-bridge/           # IPC bridge sidecar (fsnotify → NATS)
  memory-server/        # Per-Agent memory server (SQLite + FTS5)
  mcp-bridge/           # MCP bridge sidecar
  web-proxy/            # Web proxy (OpenAI-compat API + MCP gateway)
  node-probe/           # Node probe DaemonSet (inference discovery, celln.dev/kvm labels)
  model-gateway/        # Model gateway: the only model path for Celln runs (default on, digest-pinned)
  celln-*/              # Celln scoped receiver and authorisation fixture
  sympozium/            # CLI + TUI (Bubble Tea), incl. `install` and the Celln fleet installer
channels/               # Channel pods — telegram, slack, discord, whatsapp
charts/
  sympozium/            # Control-plane chart (files/skills, files/agent-configs are the built-ins)
  sympozium-crds/       # CRD-only chart (synced by `make manifests`)
  ergoz/                # Vendored ergoz chart installed by `sympozium install`
config/
  crd/bases/            # Generated CRD YAML manifests
  agent-configs/        # Built-in Ensemble YAML definitions
  skills/               # Built-in SkillPack YAML definitions
  policies/             # Built-in SympoziumPolicy presets
  samples/              # Sample CR YAML files
  celln/release.json    # Pinned Celln release
  ergoz/release.json    # Pinned ergoz release
hack/                   # Build helpers (e.g. build-celln-starter.sh)
images/                 # Dockerfiles for all components
internal/
  apiserver/            # API server implementation
  controller/           # Reconcilers (Agent, AgentRun, AgentRunTurn, AgentRuntime, HarnessSession, WorkspaceSession, Ensemble, SkillPack, SympoziumPolicy, SympoziumSchedule, SympoziumConfig, MCPServer, Model) + routers (channel, schedule, spawn)
  orchestrator/         # Pod builder + spawner for agent Jobs
  celln/, cellnparent/, cellnplatform/, cellnauthority/, cellninstall/, …  # Celln clients, admission, install
  modelconnection/      # ModelConnection resolution
  collector/            # Energy collector discovery (ergoz)
  eventbus/             # NATS JetStream client + topic constants
  ipc/                  # IPC bridge (fsnotify watcher, protocol, file handlers)
  session/              # Session store
  webhook/              # Policy enforcer
  webproxy/             # Web proxy handlers (OpenAI, MCP, rate limiting)
migrations/             # PostgreSQL schema migrations
test/integration/       # Integration journeys (shell + Go)
web/                    # Web dashboard (React + Vite), Cypress specs in web/cypress
docs/                   # User and design documentation (mkdocs)
```

---

## Key CRDs

| CRD | Purpose |
|-----|---------|
| `Agent` | An agent identity — provider config, model, enabled skills, channel bindings |
| `AgentRun` | A single agent invocation — task, result, phase lifecycle |
| `SympoziumPolicy` | Policy rules enforced by the admission webhook |
| `SkillPack` | Bundled skills (Markdown instructions) + optional sidecar container + RBAC |
| `SympoziumSchedule` | Cron-based recurring AgentRun creation (heartbeat, scheduled, sweep) |
| `Ensemble` | Pre-configured agent bundles — stamps out Agents, Schedules, and memory automatically |
| `Model` | Cluster-local inference — GGUF/HuggingFace source, llama-cpp/vllm/tgi backend, OpenAI-compatible endpoint |
| `MCPServer` | Managed MCP server lifecycle — stdio or HTTP transport, tool discovery, allow/deny filtering |
| `SympoziumConfig` | Platform-wide singleton — gateway, canary, and pricing settings |
| `ModelConnection` | Reusable namespaced model route for persistent harnesses and native Celln runs |
| `AgentRuntime` | Admin-approved, digest-pinned harness replacing `agent-runner` (or a Celln wrapper runtime) |
| `HarnessSession` | Persistent Agent-owned harness process (Deployment + PVC) for chat |
| `AgentRunTurn` | A follow-up message/result within an enduring Celln run |
| `WorkspaceSession` | Persistent `/workspace` PVC for one (Agent, sessionKey) |
| `CellnRuntimeProfile`, `CellnExecutionPolicy`, `ClusterCellnTool` | Cluster-scoped Celln runtime, policy and tool catalogue |
| `CellnTool`, `CellnToolSubmission` | Legacy namespaced Celln tool catalogue and untrusted submissions |

Type definitions live in `api/v1alpha1/`. After modifying types, regenerate with:

```bash
make generate    # deepcopy + CRD manifests
make manifests   # CRD YAML + sync both chart copies
```

Never hand-edit `config/crd/bases/`, the chart CRD copies or `zz_generated.deepcopy.go`.

---

## Development Environment Setup

### Prerequisites

- Go 1.25+
- Docker
- [Kind](https://kind.sigs.k8s.io/) (Kubernetes in Docker)
- kubectl
- An LLM API key (e.g. `OPENAI_API_KEY`)

### Create a Kind Cluster & Install Sympozium

```bash
kind create cluster --name kind

# Build all images and load them into Kind
make docker-build TAG=dev
make kind-load TAG=dev

# Install CRDs + control plane from the local chart
make install TAG=dev
```

`sympozium install` (the released path) does the same from published images
and also sets up the Celln fleet and ergoz. For Celln on Kind, the host needs
`/dev/kvm` and each Kind node a kernel image:
`docker cp /boot/vmlinuz-$(uname -r) kind-control-plane:/boot/`.

### Build & Test Cycle

After code changes:

```bash
# Build everything
make build

# Run unit tests
make test

# Build specific image + reload into Kind
make docker-build-agent-runner TAG=dev
make kind-load-agent-runner TAG=dev

# Restart the controller to pick up new images
kubectl rollout restart deployment sympozium-controller-manager -n sympozium-system
```

### Common Build Targets

```bash
make build              # Build all binaries
make test               # Run unit tests with race detector (the bar)
make test-short         # Run short tests only
make test-system        # envtest controller tests (no cluster)
make test-integration   # Run all integration tests (requires Kind + API key)
(cd web && npm run test:stubbed)  # Console Cypress specs that stub every API call (no cluster; needs `npx vite` running; CI runs these on web/** PRs)
make vet                # go vet
make fmt                # gofmt
make tidy               # go mod tidy
make docker-build       # Build all Docker images
make docker-build-<name> TAG=dev   # Build a specific image
make kind-reload        # Build all, load into Kind, restart the controller
make generate           # Regenerate deepcopy + CRD manifests
make manifests          # Regenerate CRD YAML and sync chart copies
make helm-sync-check    # CI drift check for chart copies
make ux-tests           # Cypress UX tests
make clean              # Remove build artifacts
```

---

## Integration Tests

Integration tests live in `test/integration/` and run against a real Kind cluster with a real LLM.

### Running Tests

```bash
# All integration tests
make test-integration

# Single test
./test/integration/test-write-file.sh

# Override model or timeout
TEST_MODEL=gpt-5.2 TEST_TIMEOUT=180 ./test/integration/test-write-file.sh
```

### Existing Tests

| Test | What it validates |
|------|-------------------|
| `test-write-file.sh` | `write_file` tool — agent writes a file, script verifies content |
| `test-anthropic-write-file.sh` | `write_file` tool using Anthropic provider — validates provider parity |
| `test-k8s-ops-nodes.sh` | `k8s-ops` skill — agent runs kubectl via sidecar |
| `test-llmfit-cluster-fit.sh` | `llmfit` skill — agent runs node-level llmfit placement probe workflow |
| `test-telegram-channel.sh` | Telegram channel deployment + message flow |
| `test-slack-channel.sh` | Slack channel deployment (Socket Mode) |
| `test-web-proxy-api.sh` | Web proxy API — healthz, auth, models, chat completions (blocking + streaming), MCP SSE |
| `test-persistent-harness-session.sh` | Persistent AgentHarness session lifecycle |
| `test-celln-fleet.sh` | Model-free Celln fleet on multi-node Kind: install with no model input, two teams' own connections and keys through the API, key canary (each Agent's key and no other), refused key reuse and origin move, package gate, node loss |
| `test-celln-oneliner.sh` | Bare `sympozium install` brings up the fleet and gateway; `doctor` passes |

`test/integration/` holds many more (API smoke, ensembles, memory, MCP, sandbox, Celln contracts); `make integration-tests` runs the API suite.

### Writing New Tests

See `docs/guides/writing-integration-tests.md` for the full guide and template. Tests follow this pattern:

1. Create an `Agent` + `AgentRun` with a deterministic task
2. Poll `status.phase` until `Succeeded` or `Failed`
3. Validate results (pod logs, status, filesystem)
4. Clean up all test resources

Add new tests to the `test-integration` target in the `Makefile`.

---

## Agent Tools

The agent-runner has 8 always-on tools defined in `cmd/agent-runner/tools.go` (plus `delegate_to_persona`, `spawn_subagents`, the memory tools and MCP tools when enabled). Celln runs do not use these; cells borrow `ClusterCellnTool`s instead.

| Tool | Category | Description |
|------|----------|-------------|
| `execute_command` | IPC (sidecar) | Run shell commands in the skill sidecar |
| `read_file` | Native | Read file contents |
| `write_file` | Native | Write/create files |
| `edit_file` | Native | Apply one or more exact-string (unique-match) replacements to a file (atomic, all-or-nothing) |
| `list_directory` | Native | List directory contents |
| `send_channel_message` | IPC (bridge) | Send messages to Telegram/Slack/Discord/WhatsApp |
| `fetch_url` | Native | HTTP GET a URL and return the body |
| `schedule_task` | IPC (bridge) | Create/update/suspend/resume/delete SympoziumSchedule CRDs |

See `docs/guides/writing-tools.md` for the full guide on adding new tools.

---

## Key Architecture Patterns

### IPC Flow (Agent ↔ Control Plane)

```
Agent tool writes JSON → /ipc/<dir>/*.json → fsnotify watcher → NATS publish → Controller handles
```

Directories: `/ipc/tools/` (sidecar exec), `/ipc/messages/` (channel messages), `/ipc/schedules/` (schedule requests).

### Event Bus (NATS Topics)

Key topics in `internal/eventbus/types.go`:

- `agent.run.requested/started/completed/failed` — AgentRun lifecycle
- `channel.message.received/send` — Channel message flow
- `schedule.upsert` — Agent self-scheduling requests
- `tool.exec.request/result` — Sidecar tool execution

### Memory

With the `memory` SkillPack, the controller runs a per-Agent memory server (`<agent>-memory` Deployment + Service, SQLite + FTS5 on the `<agent>-memory-db` PVC); agent containers reach it via `MEMORY_SERVER_URL`. Ensembles add a shared `<pack>-shared-memory` server (`WORKFLOW_MEMORY_SERVER_URL`). The legacy ConfigMap (`<name>-memory`, mounted at `/memory/MEMORY.md`, updated from `__SYMPOZIUM_MEMORY__` markers) remains a fallback.

### Skills

SkillPacks are CRDs containing Markdown instructions + optional sidecar definitions. When enabled on an Agent, skills are mounted at `/skills/` and sidecars are injected into agent pods. See `docs/guides/writing-skills.md`.

---

## Documentation Index

| Document | Location | Content |
|----------|----------|---------|
| Architecture | `docs/architecture.md` | Components, execution planes, data flow |
| Custom resources | `docs/concepts/custom-resources.md` | Every CRD and how they relate |
| Celln | `docs/concepts/celln-backend.md`, `docs/guides/celln-fleet-installation.md` | Hardware-isolated execution and the fleet |
| Harness mode | `docs/modes/harness.md`, `docs/guides/agentharness.md` | External harnesses and persistent sessions |
| Writing tools | `docs/guides/writing-tools.md` | How to add new agent tools |
| Writing skills | `docs/guides/writing-skills.md` | How to create SkillPack CRDs |
| Writing integration tests | `docs/guides/writing-integration-tests.md` | Test patterns and templates |
| Writing UX tests | `docs/guides/writing-ux-tests.md` | Cypress specs |
| Web endpoint skill | `docs/skills/web-endpoint.md` | How to expose agents as HTTP APIs (OpenAI-compat + MCP) |
| Serving mode | `docs/guides/serving-mode.md` | How serving mode works for long-lived agent deployments |
| Design notes | `docs/design/index.md` | Design records, qualification evidence and Celln milestone history (`docs/design/history/`) |
| Historical design | `docs/design.md` | Original February 2026 design draft |
| Sample CRs | `config/samples/` | Example Agent, AgentRun, AgentRuntime, HarnessSession, policy, schedule, SkillPack, Celln catalogue |
| CRD definitions | `api/v1alpha1/` | Go type definitions for all CRDs |
| Built-in Ensembles | `config/agent-configs/` (chart copy `charts/sympozium/files/agent-configs/`) | Pre-configured agent bundles |

---

## Common Tasks for Agents

### Adding a new tool
1. Add constant + definition + handler in `cmd/agent-runner/tools.go`
2. If IPC-based, add watcher in `internal/ipc/bridge.go` and topic in `internal/eventbus/types.go`
3. If it needs a controller handler, add a router in `internal/controller/`
4. Rebuild `agent-runner` (and `ipc-bridge`/`controller` if changed)
5. Write an integration test in `test/integration/`
6. Document in `docs/guides/writing-tools.md`

### Adding a new channel
1. Create `channels/<name>/main.go`
2. Create `images/channel-<name>/Dockerfile`
3. Add to `CHANNELS` list in `Makefile`
4. The controller's `buildChannelDeployment` in `internal/controller/agent_controller.go` handles deployment

### Modifying a CRD
1. Edit type in `api/v1alpha1/<name>_types.go`
2. Run `make generate` to regenerate deepcopy and CRD YAML (and sync the chart copies)
3. Run `make install` to apply updated CRDs to cluster
4. Update the reconciler in `internal/controller/`

### Adding an Ensemble
1. Create a YAML file in `config/agent-configs/<name>.yaml` (the chart ships its own hand-maintained copy under `charts/sympozium/files/agent-configs/`; no target syncs them, so update both)
2. Define `agentConfigs` with system prompts, skills, schedules, and memory seeds
3. Apply: `kubectl apply -f config/agent-configs/<name>.yaml`
4. Activate via the TUI Ensembles tab or by patching `spec.authRefs` with kubectl

### Rebuilding after changes
```bash
# Compile check
go build ./...

# Rebuild affected images and load into Kind
make docker-build-<component> TAG=dev
make kind-load-<component> TAG=dev

# Restart controller if controller/ipc-bridge/agent-runner changed
kubectl rollout restart deployment sympozium-controller-manager -n sympozium-system
```
