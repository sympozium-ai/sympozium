# Roadmap: Evolving PersonaPack into a Multi-Agent Coordination System

## 1. Overview

The objective is to transform `PersonaPack` from a static collection of `SympoziumInstance` templates into a dynamic, relationship-aware coordination layer. This enables agents within a pack to work as a cohesive team via subagent workflows.

### What exists today

The following infrastructure is already built and should be leveraged, not rebuilt:

| Capability | Location | Notes |
|---|---|---|
| **Subagent spawning** | `internal/orchestrator/spawner.go` | Creates child `AgentRun` CRs via IPC file protocol (`/ipc/spawn/request-*.json`) |
| **Parent-child tracking** | `AgentRun.Spec.Parent` (`ParentRunRef`) | Stores `RunName`, `SessionKey`, `SpawnDepth`; labels include `sympozium.ai/parent-run` |
| **Depth/concurrency guards** | `SubagentsSpec` on `SympoziumInstance` | `MaxDepth` (default 2), `MaxConcurrent` (default 5), `MaxChildrenPerAgent` (default 3) |
| **Policy-level limits** | `SympoziumPolicy.Spec.Subagents` | `MaxDepth`, `MaxConcurrent` enforced by controller |
| **Response gate pattern** | `AgentRun` `PostRun` gate | Runs pause for external approval before completing — reusable pattern for await/resume |
| **OTel instrumentation** | `spawnerTracer` in spawner | Traces parent run, instance, spawn depth attributes |

### What's missing

1. **No persona-aware delegation.** Spawn requests target `instanceName` + `agentID`, not a persona name from the same pack. An agent can't say "ask the Writer persona."
2. **No relationship graph in the CRD.** `PersonaPackSpec.Personas` is a flat list with no edges.
3. **No result delivery to parent.** Spawner is fire-and-forget. No mechanism for a parent run to pause/await a child's result.
4. **No visual representation.** The dashboard has metric charts but no node/edge canvas.

---

## 2. Phase 1: Schema & API Evolution (The Foundation)

*Goal: Add the ability to define typed relationships between personas within a pack.*

### 2.1 Extend `PersonaPackSpec` with edges

Add a `Relationships` field to `PersonaPackSpec` (`api/v1alpha1/personapack_types.go`):

```go
type PersonaPackSpec struct {
    // ... existing fields ...

    // Relationships defines directed edges between personas in the pack.
    // +optional
    Relationships []PersonaRelationship `json:"relationships,omitempty"`

    // WorkflowType describes the overall orchestration pattern for this pack.
    // Informs the controller which runtime strategy to apply.
    // +kubebuilder:validation:Enum=autonomous;pipeline;delegation
    // +kubebuilder:default="autonomous"
    // +optional
    WorkflowType string `json:"workflowType,omitempty"`
}
```

### 2.2 Define the relationship model

```go
// PersonaRelationship defines a directed edge between two personas.
type PersonaRelationship struct {
    // Source is the persona name that initiates the interaction.
    Source string `json:"source"`

    // Target is the persona name that receives the interaction.
    Target string `json:"target"`

    // Type categorises the relationship.
    // +kubebuilder:validation:Enum=delegation;supervision;sequential
    Type string `json:"type"`

    // Condition is an optional description of when this edge activates
    // (e.g. "when source run succeeds", "on explicit request").
    // +optional
    Condition string `json:"condition,omitempty"`

    // Timeout is the maximum duration to wait for the target to complete.
    // Applies to delegation and sequential types. Format: "5m", "1h".
    // +optional
    Timeout string `json:"timeout,omitempty"`

    // ResultFormat constrains the expected output (e.g. "json", "markdown").
    // +optional
    ResultFormat string `json:"resultFormat,omitempty"`
}
```

**Relationship types explained:**

| Type | Runtime semantics | Example |
|---|---|---|
| `delegation` | Source requests target, **awaits result**, then continues. Parent run enters `AwaitingDelegate` phase. | Researcher asks Writer to draft a report |
| `sequential` | Source must **complete successfully** before target starts. Controller chains runs. | Planner finishes → Executor begins |
| `supervision` | Source **monitors** target's runs (read-only). No runtime orchestration, only observability edges in the canvas. | Tech Lead watches Backend Dev |

**`WorkflowType` at the pack level:**

| Value | Meaning |
|---|---|
| `autonomous` | Default. Personas run independently on their own schedules. Relationships are informational/supervisory only. |
| `pipeline` | Personas execute in sequence defined by `sequential` edges. Controller chains runs. |
| `delegation` | Personas can actively delegate to each other at runtime via `delegation` edges. |

### 2.3 Validation

- [ ] Add a webhook or CEL rule ensuring `source` and `target` reference valid persona names in `spec.personas[].name`
- [ ] Reject cycles in `sequential` relationships (pipeline must be a DAG)
- [ ] Reject self-referencing edges (`source == target`)

### 2.4 Tooling

- [ ] Run `make generate && make manifests` to regenerate deepcopy and CRD YAML
- [ ] Update `charts/sympozium/crds/` via `make helm-sync`
- [ ] Extend `GET /api/v1/personapacks/{name}` response — no new endpoint needed; the frontend derives the graph from `spec.personas[]` (nodes) + `spec.relationships[]` (edges)

---

## 3. Phase 2: Visual Representation (The Canvas)

*Goal: Let users see and edit the persona relationship graph.*

### 3.1 Add ReactFlow dependency

```bash
cd web && npm install @xyflow/react
```

ReactFlow is React-native, supports typed nodes/edges, handles layout, zoom/pan, and interactive editing out of the box. ~45kB gzipped.

### 3.2 Canvas component (`web/src/components/persona-canvas.tsx`)

- **Nodes**: One per `PersonaSpec`. Display persona name, model, skills badges, phase (from `status.installedPersonas`). Use custom node components styled with the existing Tailwind/Radix design system.
- **Edges**: One per `PersonaRelationship`. Styled by type:
  - `delegation` → solid arrow, blue
  - `sequential` → dashed arrow, amber
  - `supervision` → dotted line, muted
- **Layout**: Auto-layout via dagre or elkjs for initial positioning; user can drag to rearrange.
- **Real-time status**: Overlay run phase badges on nodes using existing `useRuns()` hook (filter by `sympozium.ai/instance` label matching the persona's installed instance).

### 3.3 Integration points

| Location | Change |
|---|---|
| **Persona detail page** | Add a "Workflow" tab showing the canvas for this pack |
| **Dashboard** | Add an optional "Team Canvas" panel (new grid widget) showing the active pack's graph with live run status |

### 3.4 Interactive editing (Phase 2b — after read-only ships)

- [ ] Drag-to-connect: create new `PersonaRelationship` edges on the canvas
- [ ] Edge context menu: edit type, timeout, condition
- [ ] Sync changes back via `PATCH /api/v1/personapacks/{name}` (already exists)
- [ ] Optimistic update with React Query invalidation on success

---

## 4. Phase 3a: Persona-Targeted Spawning (The Bridge)

*Goal: Let agents reference personas by name instead of raw instance names.*

This is a prerequisite for Phase 3b and can ship independently.

### 4.1 Extend `SpawnRequest`

Add an optional `TargetPersona` field to `SpawnRequest` (`internal/orchestrator/spawner.go`):

```go
type SpawnRequest struct {
    // ... existing fields ...

    // TargetPersona is the persona name within the same PersonaPack.
    // If set, the spawner resolves it to the correct instance name.
    // Mutually exclusive with InstanceName when used for pack-aware delegation.
    TargetPersona string `json:"targetPersona,omitempty"`

    // PackName is the PersonaPack that owns both parent and target personas.
    PackName string `json:"packName,omitempty"`
}
```

### 4.2 Resolution logic in `Spawner.Spawn()`

```
if req.TargetPersona != "" && req.PackName != "" {
    1. GET PersonaPack by name
    2. Find InstalledPersona where Name == req.TargetPersona
    3. Set req.InstanceName = installedPersona.InstanceName
    4. Validate the relationship edge exists (source=parent's persona, target=req.TargetPersona)
}
```

### 4.3 IPC protocol extension

Extend `/ipc/spawn/request-*.json` to accept `targetPersona` and `packName` fields. The IPC bridge passes them through to the spawner.

---

## 5. Phase 3b: Await/Resume Loop (The Intelligence)

*Goal: Enable a parent run to pause, wait for a delegate's result, and continue.*

### 5.1 New `AgentRun` phase: `AwaitingDelegate`

Add to the existing phase enum (`api/v1alpha1/agentrun_types.go`):

```go
const (
    // ... existing phases ...
    AgentRunPhaseAwaitingDelegate = "AwaitingDelegate"
)
```

### 5.2 Delegation flow

```
┌─────────────┐     spawn request      ┌─────────────┐
│ Persona A   │ ──────────────────────► │ Persona B   │
│ (Researcher)│     targetPersona:      │ (Writer)    │
│             │     "writer"            │             │
│  Running    │                         │  Running    │
│      ↓      │                         │      ↓      │
│ Awaiting    │                         │  Succeeded  │
│ Delegate    │ ◄────── result ──────── │             │
│      ↓      │     (controller         └─────────────┘
│  Running    │      delivers via
│  (resumes)  │      IPC /ipc/input/
│      ↓      │      delegate-result.json)
│  Succeeded  │
└─────────────┘
```

### 5.3 Controller changes (`internal/controller/agentrun_controller.go`)

- [ ] When a child `AgentRun` with `Parent` ref transitions to `Succeeded` or `Failed`:
  1. Look up the parent `AgentRun`
  2. If parent phase is `AwaitingDelegate`:
     - Write child's result to parent's IPC volume (`/ipc/input/delegate-result.json`)
     - Transition parent back to `Running`
  3. If child failed and relationship has a timeout: respect it and resume parent with an error payload

### 5.4 Reuse the ResponseGate pattern

The existing `PostRun` response gate already implements "run pauses, waits for external signal, resumes." The delegation await is structurally identical:
- Gate type: `delegate` (new enum value alongside `PostRun`)
- Verdict source: controller (automatic on child completion) instead of human (manual API call)

### 5.5 State tracking

```go
// DelegateStatus tracks an in-flight delegation within an AgentRun.
type DelegateStatus struct {
    // ChildRunName is the spawned AgentRun.
    ChildRunName string `json:"childRunName"`

    // TargetPersona is the persona that was delegated to.
    TargetPersona string `json:"targetPersona"`

    // Phase is the child run's current phase.
    Phase string `json:"phase,omitempty"`

    // Result is populated when the child completes.
    // +optional
    Result string `json:"result,omitempty"`
}
```

Add `Delegates []DelegateStatus` to `AgentRunStatus` for observability.

---

## 6. Phase 4: Policy & Safety

*Goal: Ensure coordinated workflows respect constraints.*

### 6.1 Relationship-scoped policy

Extend `SympoziumPolicy` to govern which delegation edges are permitted:

```go
type SubagentPolicySpec struct {
    // ... existing MaxDepth, MaxConcurrent ...

    // AllowedDelegations restricts which persona-to-persona delegations are permitted.
    // If empty, all edges defined in the PersonaPack are allowed.
    // +optional
    AllowedDelegations []DelegationRule `json:"allowedDelegations,omitempty"`
}

type DelegationRule struct {
    Source string `json:"source"`           // persona name or "*"
    Target string `json:"target"`           // persona name or "*"
    Allow  bool   `json:"allow"`            // true = permit, false = deny
}
```

### 6.2 Existing guards (already built)

These require no changes — they apply automatically to delegation-spawned runs:

- `SubagentsSpec.MaxDepth` — prevents infinite delegation chains
- `SubagentsSpec.MaxConcurrent` — caps total concurrent runs per instance
- `SubagentsSpec.MaxChildrenPerAgent` — limits fan-out per parent

### 6.3 Cycle detection at runtime

The controller must reject delegation requests that would create a cycle (A delegates to B, B delegates back to A). Check the `SpawnDepth` chain and parent lineage before creating the child run.

### 6.4 Timeout enforcement

For `delegation` edges with a `Timeout`:
- [ ] Controller starts a timer when parent enters `AwaitingDelegate`
- [ ] If timeout expires before child completes: resume parent with a timeout error, cancel child run

---

## 7. Implementation Order

| Step | Phase | Depends on | Ships value |
|---|---|---|---|
| 1 | **Schema** — Add `Relationships[]` and `WorkflowType` to `PersonaPackSpec` | — | Pack authors can declare team structure |
| 2 | **Canvas (read-only)** — ReactFlow visualization on persona detail page | Step 1 | Users see the team graph |
| 3 | **Canvas (editable)** — Drag-to-connect, sync to CRD | Step 2 | Users build workflows visually |
| 4 | **Persona-targeted spawning** — Extend `SpawnRequest` with `targetPersona` resolution | Step 1 | Agents can reference teammates by name |
| 5 | **Await/resume loop** — `AwaitingDelegate` phase, result delivery, controller watches | Step 4 | Full delegation round-trip works |
| 6 | **Live canvas** — Overlay run phases on canvas nodes via WebSocket | Steps 2 + 5 | Real-time team activity visualization |
| 7 | **Policy** — Relationship-scoped delegation rules, cycle detection, timeouts | Step 5 | Production safety |

Steps 2-3 (canvas) and Step 4 (persona spawning) can proceed in parallel after Step 1.

---

## 8. Success Metrics

- A user can define a pack where "Researcher" delegates to "Writer" via a single CRD with `relationships` edges.
- The persona detail page shows a visual graph linking "Researcher" → "Writer" with edge type labels.
- An `AgentRun` for "Researcher" enters `AwaitingDelegate`, triggers an `AgentRun` for "Writer", and automatically resumes with Writer's output on completion.
- The canvas shows real-time run status (Running, AwaitingDelegate, Succeeded) overlaid on persona nodes.
- Delegation respects `MaxDepth`, timeout constraints, and policy-defined `AllowedDelegations`.

---

## 9. Key Files

| Area | Files |
|---|---|
| PersonaPack CRD | `api/v1alpha1/personapack_types.go` |
| AgentRun CRD | `api/v1alpha1/agentrun_types.go` |
| Instance CRD (SubagentsSpec) | `api/v1alpha1/sympoziuminstance_types.go` |
| Policy CRD | `api/v1alpha1/sympoziumpolicy_types.go` |
| PersonaPack controller | `internal/controller/personapack_controller.go` |
| AgentRun controller | `internal/controller/agentrun_controller.go` |
| Spawner | `internal/orchestrator/spawner.go` |
| IPC protocol | `internal/ipc/protocol.go`, `internal/ipc/bridge.go` |
| API server | `internal/apiserver/server.go` |
| Frontend hooks | `web/src/hooks/use-api.ts` |
| Persona pages | `web/src/pages/personas.tsx`, `web/src/pages/persona-detail.tsx` |
| Dashboard | `web/src/pages/dashboard.tsx` |
