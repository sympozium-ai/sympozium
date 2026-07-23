# Sidecar-Driven Mode

> **Status:** Stable. The first registered [`TaskModeHandler`][taskmodehandler] in Sympozium. Use this mode for any orchestrator that wants to drive a multi-step workflow end-to-end while keeping the LLM in the loop as a sub-call.

The `AgentRun` CR exposes a polymorphic `spec.task` field. It accepts either:

- **A string** — the legacy Path A. The string is the conversational prompt passed to the LLM via the `TASK` env var.
- **An object** — `{mode, tool, parameters}`. The controller looks up `mode` in the [`taskmodes` registry][taskmodes] and dispatches to a registered handler.

Sidecar-driven mode is the first object-form mode and the only one shipped by default. It flips the pod's process topology so the sidecar owns the workflow and the LLM is consulted only at decision points.

## What it does

Sidecar-driven mode inverts the usual agent loop:

- **The sidecar is the primary process.** It runs as the pod's orchestrator and drives the workflow end-to-end (iterate, decide, persist, complete).
- **The agent-runner is a sub-call.** It runs in `AGENT_MODE=prompt-server` and just answers action decisions over `/ipc/prompts/`. There is no main agent loop.

This solves the failure mode where smaller LLMs spiral into multi-step orchestrations with exponential token scaling. The sidecar owns the control flow; the LLM is consulted only at decision points.

!!! note "When to use this mode"
    Use sidecar-driven mode when your orchestrator already knows the steps it needs to take (iterate, decide, persist) and only needs the LLM to fill in the per-step decision. For free-form LLM-driven tasks, stick with the string form of `spec.task`.

## Example

A photo tagger that walks a directory of images, asks the LLM to refine candidate tags for each one, and writes a JSON manifest.

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: AgentRun
metadata:
  name: photo-tagger-batch-1
  namespace: default
spec:
  agentRef: photo-tagger-agent
  agentId: tagger
  sessionKey: "photo-tagger-batch-1"
  cleanup: delete
  mode: task
  model:
    provider: openai
    model: gpt-5-mini
    authSecretRef: openai-api-key
  skills:
    - skillPackRef: photo-tagger
  task:
    mode: sidecar-driven
    tool: tagger_run
    parameters:
      batchSize: "10"
      inputDir: "/data/photos"
      outputFile: "/data/tags.json"
  timeout: 1h
```

The controller looks up `sidecar-driven` in the taskmodes registry, validates the task, and the [`SidecarDrivenHandler`][sidecar-driven-handler]:

1. Sets `AGENT_MODE=prompt-server` on the agent-runner container so the main loop is skipped.
2. Finds the sidecar that declares `tagger_run` in its `spec.sidecar.tools[]`.
3. Overrides that sidecar's command to `["python", "/app/tagger.py", "run"]` (the tool's `Exec` + `Subcommand`).
4. Sets `SYMPOZIUM_RUN_CONFIG_JSON='{"batchSize":"10","inputDir":"/data/photos","outputFile":"/data/tags.json"}'` on that sidecar — the JSON-marshalled `parameters`.

## Lifecycle

```
kubectl apply -f <manifest>
        │
        ▼
sympozium controller reconciles the AgentRun
        │
        ├─ taskmodes.Get("sidecar-driven") → SidecarDrivenHandler
        ├─ handler.Validate(task) → ok
        ├─ handler.AdjustSidecars(task, resolvedSidecars)
        │    → []SidecarAdjustment{
        │         SkillPackName: "photo-tagger",
        │         OverrideCommand: ["python","/app/tagger.py","run"],
        │         AddEnv: [{SYMPOZIUM_RUN_CONFIG_JSON, "<json>"}],
        │       }
        ├─ handler.ConfigureAgentContainer(task, &agentEnv)
        │    → append {AGENT_MODE=prompt-server}
        │
        └─ buildContainers renders the pod
             - agent-runner container: AGENT_MODE=prompt-server appended
             - photo-tagger container: command overridden, SYMPOZIUM_RUN_CONFIG_JSON set
             - ipc-bridge container: as usual
        │
        ▼
pod starts. Three containers come up in parallel.

agent-runner (prompt-server):
  1. Logs "supported AGENT_MODE values: prompt-server".
  2. Sees AGENT_MODE=prompt-server → enters runPromptServer.
  3. Runs runPromptServiceLoop in a goroutine: watches /ipc/prompts/ for
     request-{id}.json files, calls LLM, writes result-{id}.json.
  4. Polls /ipc/done every 500ms. Exits when /ipc/done appears.
  5. Copies /ipc/run-result.json to /ipc/output/result.json and emits
     __SYMPOZIUM_RESULT__<json>__SYMPOZIUM_END__ for the controller's
     log scraper.

photo-tagger (tagger_run):
  1. Reads SYMPOZIUM_RUN_CONFIG_JSON from env.
  2. Boots the orchestrator: walk the input directory, enumerate images.
  3. For each image:
     a. Runs cheap local pre-processing (thumbnail, EXIF, dominant colour).
     b. Writes /ipc/prompts/request-{id}.json with the candidate tags.
     c. Polls /ipc/prompts/result-{id}.json for the LLM's refined tags.
     d. Calls tagger_persist (writes tags for this image to the manifest).
     e. Writes /ipc/context/clear-{id}.json to flatten the LLM's
        conversation history between images.
  4. Calls tagger_finalize (closes the manifest, writes outputFile).
  5. Writes /ipc/run-result.json with the final summary.
  6. **finally**: writes /ipc/done (the agent-runner is waiting on this).

ipc-bridge:
  - Republishes every /ipc/prompts/ request and result on per-run
    NATS topics (agent.prompt.request.<runID> /
    agent.prompt.result.<runID>) for audit / replay.
```

## Tool resolution

`task.tool` is matched against `spec.sidecar.tools[].name` on the resolved SkillPack sidecars. First match wins. The matching tool's `Exec + Subcommand` becomes the sidecar container's command.

```yaml
spec:
  sidecar:
    image: registry.example.com/photo-tagger:TAG
    tools:
      - name: tagger_run           # ← task.tool matches here
        exec: ["python", "/app/tagger.py"]
        subcommand: run
        inputMode: args
      - name: tagger_persist       # ← per-image persistence helper
        exec: ["python", "/app/tagger.py"]
        subcommand: persist
        inputMode: stdin
```

If no sidecar declares the named tool, the handler returns an error naming the available tools:

```
sidecar-driven: no sidecar declares tool "foo" (declared tools:
[photo-tagger.tagger_run photo-tagger.tagger_persist
photo-tagger.tagger_finalize])
```

The error surfaces on `AgentRun.status.error` and the AgentRun transitions to `phase: Failed`. No pod is created.

## Parameter passing

`task.parameters` is a flat `map[string]string`. The handler marshals it to JSON and sets it as `SYMPOZIUM_RUN_CONFIG_JSON` on the resolved sidecar. The sidecar's CLI parses the env into a typed input manifest.

```python
# tagger.py (run subcommand)
import os, json
config = json.loads(os.environ["SYMPOZIUM_RUN_CONFIG_JSON"])
# config -> {"batchSize": "10", "inputDir": "/data/photos", "outputFile": "/data/tags.json"}
```

!!! warning "Empty parameters serialise to `{}`, not `null`"
    Empty / nil parameters must serialise to `{}` (not `null`) so the sidecar's `JSON.parse` returns an object the input schema can validate. The `SidecarDrivenHandler` enforces this — see [`internal/controller/taskmodes/sidecar_driven.go`][sidecar-driven-handler].

## UseContext

Sidecar-driven runs benefit from `useContext=true` (the default) so the LLM can navigate the multi-step decision sequence within a record without losing conversation history.

Set `useContext: false` on the AgentRun spec to answer every prompt in isolation — useful for replaying a single decision or debugging a specific step.

!!! info "Where `useContext` lives"
    `useContext` is settable only on the `AgentRun` CR. The sidecar cannot override it at runtime.

## Why the sidecar owns `/ipc/done`

Earlier sidecar-initiated designs had the agent-runner write `/ipc/done` itself as soon as it observed `/ipc/run-result.json`. If the sidecar's orchestrator was mid-prompt↔LLM call when this happened, the new `/ipc/done` caused `promptLLM` to return "observed before result arrived", which misclassified successful records as failed and routed them to an unreliable fallback branch.

The fix: the sidecar's primary tool writes `/ipc/done` in a `finally` block, after the orchestrator, `save_*`, and `complete_run` calls have all returned. By the time `/ipc/done` appears, there are no in-flight prompts.

The agent-runner now observes `/ipc/done` (not `/ipc/run-result.json`) as its completion signal.

## Adding a sidecar-driven variant

To onboard a new orchestrator sidecar that follows the same pattern:

1. Add the `<tool-name>` subcommand to your sidecar's CLI. It must:
    - Read `SYMPOZIUM_RUN_CONFIG_JSON` from env.
    - Drive its own workflow, calling the LLM via `/ipc/prompts/`.
    - Write `/ipc/run-result.json` with the final summary.
    - Write `/ipc/done` in a `finally` block.
2. Declare it in the SkillPack's `spec.sidecar.tools[]`:

    ```yaml
    spec:
      sidecar:
        image: registry.example.com/my-orchestrator:TAG
        tools:
          - name: my_orchestrator_run
            exec: ["python", "/app/orchestrator.py"]
            subcommand: run
            inputMode: args
    ```

3. From your trigger (k8s job, ensemble schedule, etc.) submit an AgentRun with `task: {mode: sidecar-driven, tool: my_orchestrator_run, parameters: {...}}`.

No controller changes are needed. The handler resolves the tool name to the matching sidecar and overrides its command.

If your variant needs additional config beyond `parameters`, extend `TaskSpec` with new fields. See the [extension guide](extension-guide.md) for the CRD update path.

## See also

- [Extension guide](extension-guide.md) — how to add a new task mode.
- [Writing Orchestrator Sidecars](../sidecars/writing-orchestrator-sidecars.md) — how to build the orchestrator's CLI (LLM-call helper, context clearing, atomic writes, `/ipc/done` semantics).
- [`internal/controller/taskmodes/`][taskmodes] — handler package and registry.
- [Custom Resources](../concepts/custom-resources.md) — the `AgentRun` CR shape.
- [Skills & Sidecars](../concepts/skills.md) — declaring tools on a SkillPack.

[taskmodehandler]: https://github.com/sympozium-ai/sympozium/blob/main/internal/controller/taskmodes/taskmodes.go
[taskmodes]: https://github.com/sympozium-ai/sympozium/tree/main/internal/controller/taskmodes
[sidecar-driven-handler]: https://github.com/sympozium-ai/sympozium/blob/main/internal/controller/taskmodes/sidecar_driven.go