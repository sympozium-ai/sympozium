# Roadmap: Evolving Ensemble into a Multi-Agent Coordination System

## 1. Overview

The objective is to transform `Ensemble` from a static collection of `Agent` templates into a dynamic, relationship-aware coordination layer. This enables agents within a pack to work as a cohesive team via subagent workflows.

### Current status (as of 2026-04-16)

| Phase | Status | Summary |
|---|---|---|
| **Phase 1: Schema** | **Done** | `PersonaRelationship`, `Relationships[]`, `WorkflowType` on EnsembleSpec |
| **Phase 2: Canvas (read-only)** | **Done** | ReactFlow canvas on persona detail Workflow tab |
| **Phase 2b: Canvas (editable)** | **Done** | Drag-to-connect with type picker, edge deletion, Save syncs to CRD |
| **Phase 2c: Global canvas** | **Done** | Persona Packs list page canvas showing all enabled packs with live run status |
| **Phase 3a: Persona-targeted spawning** | **Done** | `TargetPersona`/`PackName` on SpawnRequest, resolution + edge validation in Spawner |
| **Phase 3b: AwaitingDelegate phase** | **Done** | Controller handles AwaitingDelegate phase, skips timeout while parent waits |
| **Phase 3c: Delegate tool for agents** | **Done** | `delegate_to_persona` tool blocks until child completes, returns result to LLM |
| **Phase 3d: Controller await/resume** | **Done** | SpawnRouter subscribes to spawn requests, creates child AgentRuns, delivers results back via NATS/IPC |
| **Phase 4: Policy & safety** | **Not started** | Relationship-scoped delegation rules, cycle detection, timeouts |

### What exists today

| Capability | Location | Notes |
|---|---|---|
| **Subagent spawning** | `internal/orchestrator/spawner.go` | Creates child `AgentRun` CRs via IPC file protocol (`/ipc/spawn/request-*.json`) |
| **Persona-targeted spawning** | `spawner.go` `resolvePersonaTarget()` | Resolves `targetPersona` → instance name via Ensemble, validates relationship edges |
| **Parent-child tracking** | `AgentRun.Spec.Parent` (`ParentRunRef`) | Stores `RunName`, `SessionKey`, `SpawnDepth`; labels include `sympozium.ai/parent-run` |
| **Depth/concurrency guards** | `SubagentsSpec` on `Agent` | `MaxDepth` (default 2), `MaxConcurrent` (default 5), `MaxChildrenPerAgent` (default 3) |
| **Policy-level limits** | `SympoziumPolicy.Spec.Subagents` | `MaxDepth`, `MaxConcurrent` enforced by controller |
| **Response gate pattern** | `AgentRun` `PostRun` gate | Runs pause for external approval before completing — reusable pattern for await/resume |
| **Relationship graph in CRD** | `EnsembleSpec.Relationships[]` | Typed edges (delegation, sequential, supervision) with condition, timeout, resultFormat |
| **AwaitingDelegate phase** | `AgentRunPhase` enum + `DelegateStatus` | Controller transitions parent to AwaitingDelegate, SpawnRouter transitions back to Running on child completion |
| **SpawnRouter** | `internal/controller/spawn_router.go` | Subscribes to spawn events, creates child AgentRuns, tracks pending delegations, delivers results via NATS |
| **Blocking delegate tool** | `cmd/agent-runner/tools.go` | `delegate_to_persona` blocks up to 10 min, polls for result file, returns child output to LLM |
| **Visual canvas** | `web/src/components/ensemble-canvas.tsx` | Per-pack editable canvas + global read-only canvas with live run status highlighting |
| **Default research-delegation-example pack** | `config/agent-configs/research-delegation-example.yaml` | 4-persona pack demonstrating all 3 relationship types + shared memory |
| **OTel instrumentation** | `spawnerTracer` in spawner | Traces parent run, instance, spawn depth, target persona attributes |

### What's missing (to complete the delegation chain)

1. **Delegate tool for agents (Phase 3c).** Agents need a tool (e.g., `delegate_to_persona`) that writes a spawn request to `/ipc/spawn/request-*.json` with `targetPersona` and `packName`. Without this, agents have no way to trigger delegation — they can only do generic subagent spawns.
2. **Controller await/resume (Phase 3d).** When a parent run delegates, it should enter `AwaitingDelegate`. The controller needs to watch child `AgentRun` completion and write the result to the parent's IPC volume, then transition the parent back to `Running`.
3. **Policy enforcement (Phase 4).** Relationship-scoped delegation rules, runtime cycle detection, timeout enforcement.

---

## 2. Phase 1: Schema & API Evolution (The Foundation) — DONE

*Goal: Add the ability to define typed relationships between personas within a pack.*

### Delivered

- `PersonaRelationship` type with `source`, `target`, `type` (delegation/sequential/supervision), `condition`, `timeout`, `resultFormat`
- `Relationships[]` and `WorkflowType` (autonomous/pipeline/delegation) on `EnsembleSpec`
- `Workflow` print column on `kubectl get ensembles`
- CRD manifests regenerated and deployed
- PATCH API support for relationships and workflowType

### Key files
- `api/v1alpha1/ensemble_types.go`
- `internal/apiserver/server.go` (PatchEnsembleRequest)

---

## 3. Phase 2: Visual Representation (The Canvas) — DONE

*Goal: Let users see and edit the persona relationship graph.*

### Delivered

**Per-pack canvas** (persona detail page, Workflow tab):
- ReactFlow canvas with custom persona nodes showing name, model, skills, live run status
- Typed edges: delegation (animated blue), sequential (dashed amber), supervision (dotted gray)
- Interactive editing: drag-to-connect with type picker, edge deletion, Save syncs to CRD
- Relationship table below the canvas
- Status legend

**Global canvas** (Persona Packs list page):
- Table/canvas view toggle
- All enabled packs rendered as clusters with their persona nodes and relationship edges
- Live run status highlighting on nodes (pulsing rings, phase labels, task preview)
- Nodes draggable, read-only (no edge creation on global view)

**Live run status highlighting** (both canvases):
- Running: pulsing blue ring with glow + task preview
- Serving: pulsing violet ring with glow
- AwaitingDelegate: pulsing amber ring with glow
- Failed: red ring with glow
- Succeeded: subtle green ring

### Key files
- `web/src/components/persona-canvas.tsx` (shared: PersonaNode, GlobalPersonaCanvas, PersonaCanvas)
- `web/src/pages/persona-detail.tsx` (Workflow tab)
- `web/src/pages/personas.tsx` (view toggle)

---

## 4. Phase 3a: Persona-Targeted Spawning — DONE

*Goal: Let agents reference personas by name instead of raw instance names.*

### Delivered

- `TargetPersona` and `PackName` fields on `SpawnRequest` (orchestrator + IPC protocol)
- `resolvePersonaTarget()` in Spawner: looks up Ensemble, finds installed instance, validates relationship edge exists, inherits target persona's system prompt and skills
- OTel span attributes for target persona and pack name

### Key files
- `internal/orchestrator/spawner.go`
- `internal/ipc/protocol.go`

---

## 5. Phase 3b: AwaitingDelegate Phase — SCHEMA ONLY

*Goal: Enable a parent run to pause, wait for a delegate's result, and continue.*

### Delivered (schema)

- `AgentRunPhaseAwaitingDelegate` phase constant
- `DelegateStatus` type: `ChildRunName`, `TargetPersona`, `Phase`, `Result`, `Error`
- `Delegates []DelegateStatus` on `AgentRunStatus`
- Frontend types updated

### Not yet delivered (controller logic)

The controller does not yet:
1. Transition parent runs to `AwaitingDelegate` when a delegation spawn occurs
2. Watch child `AgentRun` completion
3. Write child result to parent's IPC volume (`/ipc/input/delegate-result.json`)
4. Transition parent back to `Running`

### Key files
- `api/v1alpha1/agentrun_types.go`
- `internal/controller/agentrun_controller.go` (TODO)

---

## 6. Phase 3c: Delegate Tool for Agents — NOT STARTED

*Goal: Give agents a tool to trigger persona-aware delegation.*

### Design

Agents need a tool (registered in the agent runner) that:
1. Accepts `targetPersona`, `task`, and optional `resultFormat`
2. Writes a spawn request to `/ipc/spawn/request-{uuid}.json` with:
   ```json
   {
     "task": "Write a report based on these findings: ...",
     "targetPersona": "writer",
     "packName": "research-delegation-example"
   }
   ```
3. The IPC bridge forwards this to the event bus
4. The spawner resolves the target and creates the child AgentRun

The `packName` can be auto-injected by the agent runner from the instance's labels (`sympozium.ai/persona-pack`).

### Key files
- Agent runner tool registration (new tool definition)
- `internal/ipc/protocol.go` (SpawnRequest already has the fields)

---

## 7. Phase 3d: Controller Await/Resume Loop — NOT STARTED

*Goal: Close the delegation round-trip so parent runs receive child results.*

### Design

```
┌─────────────┐     spawn request      ┌─────────────┐
│ Persona A   │ ──────────────────────► │ Persona B   │
│ (Researcher)│     targetPersona:      │ (Writer)    │
│             │     "writer"            │             │
│  Running    │                         │  Running    │
│      ↓      │                         │      ↓      │
│ Awaiting    │                         │  Succeeded  │
│ Delegate    │ ◄────── result ──────── │             │
│      ↓      │     (controller         └─────���───────┘
│  Running    │      delivers via
│  (resumes)  │      IPC /ipc/input/
│      ↓      │      delegate-result.json)
│  Succeeded  │
└��────────────┘
```

### Implementation steps

1. When spawner creates a child run with `TargetPersona`, update parent's `Status.Delegates` and transition to `AwaitingDelegate`
2. AgentRun controller watches for child runs with `sympozium.ai/parent-run` label transitioning to terminal phase
3. On child completion: write result to parent's IPC volume, update `DelegateStatus`, transition parent back to `Running`
4. Reuse the existing `ResponseGate` pattern — `AwaitingDelegate` is structurally identical (run pauses, waits for external signal)

### Key files
- `internal/controller/agentrun_controller.go`
- `internal/orchestrator/spawner.go` (update parent status)

---

## 8. Phase 4: Policy & Safety — NOT STARTED

*Goal: Ensure coordinated workflows respect constraints.*

### Existing guards (already built)

- `SubagentsSpec.MaxDepth` — prevents infinite delegation chains
- `SubagentsSpec.MaxConcurrent` — caps total concurrent runs per instance
- `SubagentsSpec.MaxChildrenPerAgent` — limits fan-out per parent
- `resolvePersonaTarget()` validates relationship edge exists before spawning

### Remaining work

- **Relationship-scoped policy**: `AllowedDelegations` rules in `SympoziumPolicy` (source/target persona allow/deny)
- **Runtime cycle detection**: reject delegation requests that would create a cycle (A→B→A) by checking parent lineage
- **Timeout enforcement**: controller starts timer when parent enters `AwaitingDelegate`; resumes with error if timeout expires

---

## 9. Default Pack: research-delegation-example

The `research-delegation-example` Ensemble is included in the default packs and demonstrates all three relationship types:

```
Lead ──delegation──► Researcher ──delegation──► Writer ──sequential──► Reviewer
  │                                                         ▲              ▲
  └──────────────────── supervision ────────────────────────┘──────────────┘
```

**Personas**: Lead, Researcher, Writer, Reviewer
**WorkflowType**: delegation
**Category**: research

Currently the pack serves as a visual demo of the relationship graph. To enable runtime delegation, Phase 3c (delegate tool) and Phase 3d (controller await/resume) must be completed.

---

## 10. Implementation Order (remaining work)

| Step | Phase | Depends on | Ships value |
|---|---|---|---|
| 1 | **Delegate tool** (3c) | — | Agents can trigger persona-aware delegation |
| 2 | **Controller await/resume** (3d) | Step 1 | Full delegation round-trip works end-to-end |
| 3 | **Pipeline orchestration** | Step 2 | Sequential edges auto-chain runs (A completes → B starts) |
| 4 | **Policy** (4) | Step 2 | Relationship-scoped rules, cycle detection, timeouts |

---

## 11. Success Metrics

- [x] A user can define a pack where "Researcher" delegates to "Writer" via a single CRD with `relationships` edges
- [x] The persona detail page shows a visual graph linking "Researcher" → "Writer" with edge type labels
- [x] The persona packs list page shows all enabled packs on a global canvas with live run status
- [x] Canvas nodes glow/pulse to indicate which personas are currently running
- [ ] An `AgentRun` for "Researcher" enters `AwaitingDelegate`, triggers an `AgentRun` for "Writer", and automatically resumes with Writer's output on completion
- [ ] Delegation respects `MaxDepth`, timeout constraints, and policy-defined `AllowedDelegations`

---

## 12. Key Files Reference

| Area | Files |
|---|---|
| Ensemble CRD | `api/v1alpha1/ensemble_types.go` |
| AgentRun CRD | `api/v1alpha1/agentrun_types.go` |
| Instance CRD (SubagentsSpec) | `api/v1alpha1/sympoziuminstance_types.go` |
| Policy CRD | `api/v1alpha1/sympoziumpolicy_types.go` |
| Ensemble controller | `internal/controller/ensemble_controller.go` |
| AgentRun controller | `internal/controller/agentrun_controller.go` |
| Spawner | `internal/orchestrator/spawner.go` |
| IPC protocol | `internal/ipc/protocol.go`, `internal/ipc/bridge.go` |
| API server | `internal/apiserver/server.go` |
| Canvas components | `web/src/components/persona-canvas.tsx` |
| Persona pages | `web/src/pages/personas.tsx`, `web/src/pages/persona-detail.tsx` |
| Frontend hooks | `web/src/hooks/use-api.ts` |
| Frontend types | `web/src/lib/api.ts` |
| Default research pack | `config/personas/research-delegation-example.yaml` |
| Cypress tests | `web/cypress/e2e/ensemble-workflow-canvas.cy.ts`, `web/cypress/e2e/ensemble-research-delegation-example-workflow.cy.ts` |
