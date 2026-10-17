# Lifecycle Hooks

Run containers before and after your agent — fetch context from external systems, upload artifacts, notify Slack, clean up resources. Lifecycle hooks let you wire arbitrary setup and teardown logic into the agent execution flow without modifying the agent itself.

## How It Works

```
AgentRun created
  → Pending phase
    → Controller creates workspace PVC (if postRun defined)
    → Controller creates lifecycle RBAC (if rbac defined)
    → PreRun init containers execute sequentially
  → Running phase
    → Agent container runs
  → PostRunning phase (if postRun defined)
    → Controller creates follow-up Job
    → PostRun init containers execute sequentially
  → Succeeded / Failed
    → Workspace PVC cleaned up
    → Lifecycle RBAC garbage-collected (owner reference)
```

A [response gate](#response-gate) with `lifecycle.retry` replaces `PostRunning`
with `AwaitingGate` and can loop back to `Running` — see
[in-place retry](#in-place-retry).

### PreRun Hooks

PreRun hooks execute as **init containers** before the agent starts. They have access to:

| Path | Description |
|------|-------------|
| `/workspace` | Shared working directory — write files here for the agent to read |
| `/ipc` | IPC bus (tool calls, task input) |
| `/tmp` | Scratch space |

**Use cases:** Fetch incident context from PagerDuty, clone a git repo, download test data, warm caches.

#### Skipping a run

A preRun hook can **skip the run entirely** when there is no work to do — for
example, a hook that polls a queue or inbox and finds it empty. This avoids the
LLM call and its token cost.

To skip, the hook writes the marker file `/ipc/control/skip` and exits `0`. Any
text written to the file is recorded as the human-readable skip reason. The
agent container then short-circuits before any LLM call, and the AgentRun lands
in the terminal **`Skipped`** phase (distinct from `Succeeded`/`Failed`).

```yaml
spec:
  lifecycle:
    preRun:
      - name: check-queue
        image: curlimages/curl:latest
        command: ["sh", "-c",
          "test -s /workspace/queue.json || { mkdir -p /ipc/control && echo 'queue empty' > /ipc/control/skip; }"]
```

Notes:

- **Create the directory.** Nothing creates `/ipc/control` before preRun hooks
  run, so the hook must `mkdir -p` it before it writes the marker.
- **In [harness mode](../modes/harness.md), the adapter does the skip.** The
  harness replaces `agent-runner`. Sympozium mounts `/ipc/control` read-only
  into the harness container, and the adapter must check the marker and return
  a `skipped` result. An adapter that ignores the marker runs the harness
  anyway. See [Skipped runs](../modes/harness-adapters.md#skipped-runs).

- **Exit `0`, don't fail.** A non-zero exit fails the whole Pod (standard init
  container behavior) and marks the run `Failed` — that is *not* a skip.
- When a run is skipped, **postRun hooks are bypassed** (including gate hooks)
  and memory is not persisted — there was no agent output to process.
- Channel-triggered runs that are skipped send **no reply** (the skip is silent).
- Skipped runs are counted separately from successes/failures in gateway metrics
  (`skippedCount`).

### PostRun Hooks

PostRun hooks execute in a **follow-up Job** after the agent completes. They receive everything preRun hooks get, plus:

| Env Var | Description |
|---------|-------------|
| `AGENT_EXIT_CODE` | `"0"` on success, non-zero on failure |
| `AGENT_RESULT` | The agent's final response text (truncated to 32Ki) |

The workspace is shared between the agent and postRun hooks via a PersistentVolumeClaim. PostRun failures are **best-effort** — they're recorded as a `PostRunFailed` Condition but don't change the agent's final phase.

#### PostRun timeouts

PostRun hooks run sequentially in one Job, and that Job's budget is the **sum of the hooks' `timeout` fields** — 5 minutes each when unset, and never less than 10 minutes in total.

```yaml
postRun:
  - name: upload-artifacts
    image: amazon/aws-cli:latest
    timeout: 30m          # a slow upload gets the room it needs
```

Two things to know:

- The budget is measured from when the **postRun Job** starts, not from when the agent run started. A long-running agent still gets its full postRun budget.
- Kubernetes has no per-init-container timeout, so `timeout` bounds the Job as a whole rather than each container. Use it to give a slow hook room, not to police a fast one — a hook that overruns simply eats into what is left for the hooks after it.

**Use cases:** Upload artifacts to S3, post a summary to Slack, clean up temporary resources, trigger downstream pipelines.

### Environment Variables

All lifecycle hook containers receive these env vars:

| Env Var | Description |
|---------|-------------|
| `AGENT_RUN_ID` | Unique identifier for this agent run |
| `INSTANCE_NAME` | The Agent this run belongs to |
| `AGENT_NAMESPACE` | Kubernetes namespace |
| Custom env vars | Any `spec.env` entries from the AgentRun |

## RBAC for Hooks

Every AgentRun gets a unique ServiceAccount with automatic token mounting disabled. Lifecycle hooks receive **no Kubernetes token or permissions** by default. If your hooks need to interact with the Kubernetes API (for example, to create or delete ConfigMaps), declare the required RBAC rules:

```yaml
spec:
  lifecycle:
    rbac:
      - apiGroups: [""]
        resources: ["configmaps"]
        verbs: ["get", "list", "create", "delete"]
    preRun:
      - name: create-context
        image: soldevelo/kubectl:1.36
        command: ["kubectl", "create", "configmap", "run-context",
                  "--from-literal=started=$(date)"]
    postRun:
      - name: cleanup-context
        image: soldevelo/kubectl:1.36
        command: ["kubectl", "delete", "configmap", "run-context"]
```

The controller creates a namespace-scoped Role and RoleBinding for the run, bound only to that AgentRun's ServiceAccount. It projects a pod-bound, 10-minute token into the declared hook containers, not into the agent, harness, IPC bridge, or unrelated sidecars. The ServiceAccount and namespace-scoped RBAC are garbage-collected when the AgentRun is deleted; cluster-scoped RBAC is removed when the run completes.

## Examples

### Fetch PagerDuty incidents before the agent runs

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: Agent
metadata:
  name: oncall-agent
spec:
  agents:
    default:
      model: gpt-4o
      lifecycle:
        preRun:
          - name: fetch-incidents
            image: curlimages/curl:latest
            command: ["sh", "-c",
              "curl -s -H 'Authorization: Token token=$PD_TOKEN' \
               https://api.pagerduty.com/incidents?statuses[]=triggered \
               > /workspace/context/incidents.json"]
            env:
              - name: PD_TOKEN
                value: "your-pagerduty-token"
```

The agent's system prompt can then instruct it to read `/workspace/context/incidents.json` for current incident context.

### Upload artifacts to S3 after completion

```yaml
spec:
  lifecycle:
    postRun:
      - name: upload-report
        image: amazon/aws-cli:latest
        command: ["sh", "-c",
          "aws s3 cp /workspace/report.md s3://my-bucket/reports/$AGENT_RUN_ID.md"]
        env:
          - name: AWS_ACCESS_KEY_ID
            value: "AKIA..."
          - name: AWS_SECRET_ACCESS_KEY
            value: "..."
```

### Create and clean up a ConfigMap

```yaml
spec:
  lifecycle:
    rbac:
      - apiGroups: [""]
        resources: ["configmaps"]
        verbs: ["create", "delete", "get"]
    preRun:
      - name: create-config
        image: soldevelo/kubectl:1.36
        command: ["sh", "-c",
          "kubectl create configmap agent-scratch --from-literal=run=$AGENT_RUN_ID"]
    postRun:
      - name: delete-config
        image: soldevelo/kubectl:1.36
        command: ["kubectl", "delete", "configmap", "agent-scratch"]
```

### Ensemble with lifecycle hooks

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: Ensemble
metadata:
  name: oncall-team
spec:
  agentConfigs:
    - name: triage-agent
      systemPrompt: "You are an SRE triage agent..."
      lifecycle:
        preRun:
          - name: fetch-alerts
            image: curlimages/curl:latest
            command: ["sh", "-c", "curl -s $ALERTMANAGER_URL/api/v2/alerts > /workspace/context/alerts.json"]
```

## Response Gate

A **response gate** is a PostRun hook with `gate: true`. It holds the agent's output from reaching users until the gate explicitly approves, rejects, or rewrites it. This is useful for compliance checks, content filtering, PII scanning, or human-in-the-loop approval workflows.

### How It Works

Without a gate, the agent's response is published to channels (Slack, Telegram, web UI) the instant the agent finishes. With a gate:

1. The IPC bridge suppresses the completion event
2. The PostRun Job runs the gate hook container
3. The gate hook inspects the agent's output (via `AGENT_RESULT` env var)
4. The gate hook posts its verdict to the Sympozium API using the per-run token it was given
5. The controller reads the verdict, applies it, and publishes the (possibly rewritten) response

If no verdict is written (hook fails, times out, or the hook is designed for manual approval), the `gateDefault` field controls behavior: `"block"` (default) replaces the output with an error, `"allow"` passes it through.

### Declaring a Gated Instance

Add `gate: true` to one PostRun hook in your instance's lifecycle config. At most one PostRun hook may be a gate. The gate hook needs no Kubernetes RBAC — the controller gives it a token scoped to this one run:

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: Agent
metadata:
  name: gated-agent
spec:
  agents:
    default:
      model: gpt-4o
      lifecycle:
        gateDefault: block   # or "allow"
        postRun:
          - name: content-filter
            image: busybox:1.36   # wget is a busybox applet; curl is not
            gate: true
            command: ["sh", "-c"]
            args:
              - |
                # Inspect $AGENT_RESULT, then post the verdict:
                wget -q -O- \
                  --header "Authorization: Bearer $GATE_VERDICT_TOKEN" \
                  --header "Content-Type: application/json" \
                  --post-data '{"action":"approve"}' \
                  "$SYMPOZIUM_API_URL/api/v1/runs/$AGENT_RUN_ID/gate-verdict?namespace=$AGENT_NAMESPACE"
```

### Delivering the Verdict

The controller injects two environment variables into the gate container, and
into no other container:

| Variable | Meaning |
|----------|---------|
| `GATE_VERDICT_TOKEN` | A bearer minted for this run alone. Accepted only for this AgentRun, and only while it is `PostRunning`. |
| `SYMPOZIUM_API_URL` | The in-cluster API base URL to post to. |

That is the whole contract. The hook needs no `kubectl`, no ServiceAccount
token and no entry under `lifecycle.rbac` — a gated Agent that declares no
`rbac:` block gets no Kubernetes credentials in its pod at all.

Any HTTP client will do. The examples below use `wget`, which is a busybox
applet and so needs no extra layer; an image built on Alpine or Debian is more
likely to carry `curl`:

```sh
curl -fsS -X POST \
  -H "Authorization: Bearer $GATE_VERDICT_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"action":"approve"}' \
  "$SYMPOZIUM_API_URL/api/v1/runs/$AGENT_RUN_ID/gate-verdict?namespace=$AGENT_NAMESPACE"
```

Prefer a form that fails loudly — `curl -f`, or checking `wget`'s exit code. A
POST that quietly fails leaves no verdict, and the run falls back to
`gateDefault` with nothing to explain why.

A verdict is capped at 32KiB, matching the truncation already applied to
`AGENT_RESULT`; a larger body is refused with `413` rather than silently cut.

`lifecycle.rbac` still exists for hooks that need the Kubernetes API for other
reasons. It is simply no longer required to report a verdict.

> **Network note.** The gate container must be able to reach the API server
> Service. Nothing restricts postRun pod egress today, but if you add a
> NetworkPolicy covering them, allow the apiserver — otherwise every gate falls
> back to `gateDefault`.

Treat `AGENT_RESULT` as untrusted: it is whatever the agent produced, and a
gate is precisely the thing that should not take it on faith.

### Verdict Format

A verdict is a JSON object, the same on either channel — the hook's POST above, or the annotation `sympozium.ai/gate-verdict` set directly by a human or the API:

| Action | Effect | Fields |
|--------|--------|--------|
| `approve` | Passes the original response through unchanged | `{"action":"approve"}` |
| `reject` | Replaces the response with a custom message | `{"action":"reject","response":"Blocked by policy"}` |
| `rewrite` | Replaces the response with a sanitized version | `{"action":"rewrite","response":"cleaned output"}` |
| `retry` | Hands the rejection back to the agent for another attempt — see [Retrying a rejected response](#retrying-a-rejected-response) | `{"action":"retry","reason":"npm run check failed","response":"<gate output>"}` |

All actions accept an optional `reason` field for audit logging.

### Retrying a Rejected Response

`reject` and `rewrite` are terminal: the run ends and the user sees the gate's
message. `retry` instead feeds the gate's output back to the agent as a **new
attempt**, so the agent can fix what the gate objected to.

This is not the Job's `backoffLimit` (which is disabled). That replays the pod
with an identical task and no knowledge of why the last attempt was rejected.

There are two ways an attempt is retried, selected by `retry.inPlace`:

| | [In-place retry](#in-place-retry) | [Successor run](#successor-runs) |
|---|---|---|
| `retry.inPlace` | `true` | `false` (default) |
| Conversation | kept — the agent sees its own tool results | rebuilt from a prose card |
| Workspace | kept | fresh and empty |
| CR | one run, `status.attempts[]` | one run per attempt, linked by `status.retryOf` |

In-place retry needs a pod to park in. The Job and
[Agent Sandbox](agent-sandbox.md) backends both have one and behave
identically; [Celln](celln-backend.md) dispatches to a remote executor and has
no pod, so it always uses successor runs whatever `inPlace` says.

In-place retry also needs `agent-runner`, which holds the park loop. A
[harness](../modes/harness.md) task replaces `agent-runner`, so it uses
successor runs too, and its container never receives the gate key or the
`/gate` mount.

Enable it with `lifecycle.retry`:

```yaml
spec:
  lifecycle:
    gateDefault: block
    retry:
      maxAttempts: 3          # total attempts including the first
      backoff: 30s            # successor runs only: delay before the next one starts
      maxChainTokens: 200000  # cumulative across the chain; 0 = unlimited
      on: [gate]              # which outcomes may trigger a retry
      inPlace: true           # keep the pod and continue the conversation (default false)
    postRun:
      - name: build-check
        image: my-org/build-check:latest
        gate: true
        command: ["sh", "-c"]
        args:
          - |
            if OUT=$(npm run check 2>&1); then
              VERDICT='{"action":"approve"}'
            else
              VERDICT=$(jq -nc --arg out "$OUT" \
                '{action:"retry", reason:"npm run check failed", response:$out}')
            fi
            wget -q -O- \
              --header "Authorization: Bearer $GATE_VERDICT_TOKEN" \
              --header "Content-Type: application/json" \
              --post-data "$VERDICT" \
              "$SYMPOZIUM_API_URL/api/v1/runs/$AGENT_RUN_ID/gate-verdict?namespace=$AGENT_NAMESPACE"
```

The verdict goes over the API, so `response` carries the build log as-is —
no annotation escaping, and no `jq -Rs` to wrap it in.

#### In-place retry

The agent pod stays alive across the gate cycle. Instead of exiting after its
answer, the agent-runner **parks**: it publishes the attempt, holds its open
conversation with the model, and waits.

```
Running        agent works, then parks and publishes the attempt
AwaitingGate   the gate Job judges it, reading the same /workspace
retry          verdict is delivered to the pod, status.attempt++, back to Running
approve
reject         pod is released, run resolves as usual
rewrite
```

The verdict arrives as the next **user turn** in the conversation that produced
the rejected answer, so the model keeps:

- **its own reasoning and tool results** — it corrects work it can still see,
  rather than re-deriving it from a summary
- **`/workspace`** — every file it wrote, and anything a preRun hook put there
- **warm state** — sidecars, MCP connections, and the provider's prefix cache

The turn it receives is short, because the conversation already holds the task
and the previous answer:

```
## Attempt 2 of 3

Your previous response was rejected by the response gate. Your earlier work is
still here — the files you wrote and the tool results above are unchanged, so
correct that work rather than starting over.

### Why It Was Rejected
npm run check failed

### Gate Output
<the gate's stdout/stderr>
```

`backoff` does not apply here. The parked pod takes the next attempt as soon
as the verdict arrives.

Parking changes how time is budgeted. `spec.timeout` becomes a **per-attempt**
budget rather than a whole-run one — waiting on a gate is not the agent working
— and the Job's deadline is widened to cover every attempt. `maxAttempts` and
`maxChainTokens` bound the total.

A runner does not park forever. If no verdict arrives within the gate's
budget plus five minutes, it ends the run with the attempt it has. A `retry`
verdict that arrives after that has no pod to go to, so the run resolves on
the last attempt as a `reject`. A gate Job that never appears is handled the
same way as one that fails.

Because both attempts share one `/workspace` PVC (ReadWriteOnce), the gate Job
is scheduled onto the node already running the parked pod. On an Agent with
`workspace.perSessionPVC`, that PVC is the session's, so the gate reads the
same files the agent wrote.

The parked attempt reaches the controller as a marker in the agent's log. The
verdict goes back through a ConfigMap, `<run>-gate`, which the controller
creates with the run and the agent container mounts read-only at `/gate`. No
other container mounts it. After each write the controller touches an
annotation on the pod, so kubelet refreshes the mount at once (under a second)
rather than on its periodic sync (about a minute). No event bus is involved.

The model cannot speak for the gate. The parked attempt and the verdict that
answers it are both signed with a per-run key. The controller keeps that key
in the run's gate Secret and hands it only to the agent-runner process, as an
env var on the agent container. The model never sees it: its tools cannot
read the runner's environment, and the gate hook and skill sidecars do not
receive the key at all. So although the model can write to the log, it cannot
announce an attempt early or report its own token count. It cannot write to
`/gate` at all, and a verdict someone else puts in the ConfigMap is ignored
unless it is signed.

#### Successor runs

With `inPlace: false` (the default) — or on a backend with no pod to park — the spec is cloned
onto a new AgentRun named `<run>-retry-<n>`, whose task is a card carrying the
feedback:

```
## Retry 2 of 3

### Original Task
<the original task, with earlier retry cards stripped>

### Your Previous Attempt
<the rejected output>

### Why It Was Rejected
npm run check failed

### Gate Output
<the gate's stdout/stderr>
```

The successor gets a fresh workspace, so a retry that depends on files the
previous attempt wrote needs in-place retry.

In both modes gate output is clipped to 4000 characters, with the clip announced
in the card. Set `SYMPOZIUM_RETRY_GATE_OUTPUT_MAX_CHARS` on the controller to
change it — a test-suite dump needs more room than a lint summary.

#### Inspecting a chain

An in-place chain is one AgentRun. Each attempt is an entry in
`status.attempts[]` with its own result, verdict, tokens and cost:

```bash
kubectl get agentrun my-run -o jsonpath='{.status.attempts[*].gateVerdict}{"\n"}'
# retried retried approved
```

`status.attempt` is the current attempt, and `status.tokenUsage` /
`status.costEstimate` cover the whole chain.

A successor chain is several AgentRuns linked by `status.retryOf`, plus labels
for querying:

```bash
kubectl get agentruns -l sympozium.ai/retry-of=my-run
kubectl get agentrun my-run-retry-2 -o jsonpath='{.status.retryOf}{"\n"}'
```

A superseded successor ends in the `Failed` phase with
`status.gateVerdict: retried` and a `Retried` condition naming its replacement.
It publishes nothing — no channel reply, no failure event — because the chain
has not finished.

Either way, when the attempts or the token budget run out the last attempt
resolves as `retries-exhausted` and `gateDefault` decides whether its output is
blocked or passed through.

#### What a successor inherits

A successor is the same run again, so it starts from the predecessor's spec and
keeps the context that says where the work came from and who is waiting on it:
the reply address of the channel that triggered it, the trace, `spec.parent`,
and the labels other controllers find live work by — `sympozium.ai/schedule`,
`sympozium.ai/stimulus`, `sympozium.ai/request-hash`, `sympozium.ai/ensemble`.
A schedule with `concurrencyPolicy: Forbid` therefore waits for the whole chain,
not the first attempt.

A caller blocked on the run it created — an OpenAI-compatible request on
`/v1/chat/completions`, or an MCP tool call — is answered by whichever attempt
passes the gate. The retired attempts publish nothing, and the proxy follows
`status.retryOf` to recognise the successor's completion as the answer to the
request it is holding.

The task keeps its form. A string task becomes the retry card. A
[harness](../modes/harness.md) task stays a harness task on the same runtime,
with the card as its `prompt`. A task with no prompt, such as `sidecar-driven`,
is re-run unchanged.

The gate verdict annotation is deliberately *not* carried: a successor
inheriting it would resolve against its predecessor's verdict before running.

#### Retrying a delegated run

An agent's lifecycle hooks are inherited by the children it delegates to, so a
delegate child can be gated and retried like any other run. The parent stays
blocked in its `delegate_to_persona` call throughout: when a child retries, the
parent's `status.delegates[]` entry is repointed at the successor before the
retired attempt is marked `Failed`, and the successor's result answers the
original delegation. The parent never sees the intermediate attempts.

An Ensemble `relationships[].timeout` bounds the delegation, not one attempt of
it — a chain that outlives the edge timeout is expired at whichever attempt is
running.

#### Why this is safe

**The retry decision is not agent-controlled.** The gate hook is an
operator-declared image in `lifecycle.postRun`, and the agent has no permission
to patch the `sympozium.ai/gate-verdict` annotation for its own run. An agent
cannot grant itself another attempt. That property is the reason retry is safe
here and would not be if the agent could self-retry.

Two further bounds on a gate that always says `retry`:

- `maxAttempts` is capped at admission by the bound `SympoziumPolicy`, so an
  operator sets the ceiling regardless of what a run requests:

  ```yaml
  apiVersion: sympozium.ai/v1alpha1
  kind: SympoziumPolicy
  metadata:
    name: default-governance
  spec:
    lifecyclePolicy:
      maxRetryAttempts: 3
  ```

- `maxChainTokens` caps the cumulative tokens of every attempt in the chain.
  An in-place chain sums `status.attempts[].tokenUsage`; a successor chain walks
  `status.retryOf`. Both are checked **before** the next attempt starts.

### Manual (Human-in-the-Loop) Approval

If you want a human to approve or reject each response:

1. Set the gate hook to sleep indefinitely, and declare a matching `timeout` — otherwise the reviewer only gets the 10-minute default before `gateDefault` decides for them
2. Set `gateDefault: block` so unapproved responses are blocked
3. Use the web UI or API to approve or reject

```yaml
postRun:
  - name: manual-approval-gate
    image: busybox:1.36
    gate: true
    timeout: 24h        # how long a human has to respond
    command: ["sh", "-c", "sleep 86400"]
```

The `requireApproval` toggle in the API and UI configures exactly this, with a 24-hour window.

This hook decides nothing itself — it only holds the run open — so it needs
neither a verdict token nor RBAC. The human's approval arrives through the same
endpoint, authenticated with the ordinary API token.

A verdict is recorded by last writer, and the endpoint accepts one only while
the run is `PostRunning`. In practice an approving human ends the gate: the
controller kills the gate Job as soon as it sees a verdict, and once the run
leaves `PostRunning` any later post is refused with `409`. A gate hook that
posts in the short window before that reconcile does still overwrite the
human's verdict, so pair manual approval with a hook that waits rather than
one that decides on a timer.

In the web UI, gated runs show an amber "Approval" badge on the runs list and an approval bar on the run detail page with Approve and Reject buttons. A warning toast fires when a run requires approval.

For a run in `AwaitingGate` the approval bar names the attempt being judged,
and sends it as `attempt` with the verdict. If the run has moved on to a later
attempt, the API refuses the verdict with `409`, so a stale tab that still
shows attempt 1's buttons cannot approve attempt 2. A caller that leaves
`attempt` out (a gate hook, which only ever judges the current attempt) gets
the current one.

Via the API:

```bash
# Approve
curl -X POST http://localhost:9090/api/v1/runs/my-run/gate-verdict?namespace=default \
  -H 'Content-Type: application/json' \
  -d '{"action":"approve","reason":"reviewed by operator"}'

# Reject
curl -X POST http://localhost:9090/api/v1/runs/my-run/gate-verdict?namespace=default \
  -H 'Content-Type: application/json' \
  -d '{"action":"reject","response":"Content not approved","reason":"PII detected"}'
```

### Example: PII Scanner Gate

```yaml
spec:
  lifecycle:
    gateDefault: block
    postRun:
      - name: pii-scanner
        image: my-org/pii-scanner:latest
        gate: true
        command: ["sh", "-c"]
        args:
          - |
            if echo "$AGENT_RESULT" | pii-detect --strict; then
              VERDICT='{"action":"reject","response":"Response blocked: PII detected","reason":"pii-scanner"}'
            else
              VERDICT='{"action":"approve","reason":"pii-scanner-clean"}'
            fi
            wget -q -O- \
              --header "Authorization: Bearer $GATE_VERDICT_TOKEN" \
              --header "Content-Type: application/json" \
              --post-data "$VERDICT" \
              "$SYMPOZIUM_API_URL/api/v1/runs/$AGENT_RUN_ID/gate-verdict?namespace=$AGENT_NAMESPACE"
```

A scanner that sees the agent's raw output is exactly the container you least
want holding Kubernetes credentials. With the per-run token it holds none.

### Gate Status in the UI

| State | Web UI Indicator |
|-------|-----------------|
| Awaiting approval | Amber "Approval" badge on runs list, amber approval bar on detail page |
| Parked mid-chain | Amber `AwaitingGate` phase, plus an attempt timeline on the detail page |
| Approved | Green "approved" banner on detail page |
| Rejected | Red "rejected" banner on detail page |
| Rewritten | Blue "rewritten" banner on detail page |
| Retried | Amber "retried" banner on detail page — superseded, not failed |
| Retries exhausted | Red "retries-exhausted" banner on detail page |
| Timeout/error | Red "timeout" or "error" banner on detail page |

## Agent Sandbox Compatibility

Lifecycle hooks work with both standard Job mode and [Agent Sandbox](agent-sandbox.md) mode:

- **PreRun hooks** are injected as init containers into the Sandbox CR — they execute inside the gVisor/Kata sandbox.
- **PostRun hooks** always run as a separate follow-up Job (outside the sandbox), since the sandbox is torn down after the agent completes.
- The workspace PVC is shared between both.
- **Gate retry** works the same as on the Job backend, including
  [in-place retry](#in-place-retry) — the sandbox pod is rendered from the
  same template and parks the same way.

## Phases

With lifecycle hooks, the AgentRun phase transitions become:

`Pending` → `Running` → `PostRunning` → `Succeeded` (or `Failed`)

The `PostRunning` phase is only entered when `postRun` hooks are defined. Without them, the flow is the standard `Pending` → `Running` → `Succeeded`/`Failed`.

A gate with `lifecycle.retry` and `inPlace` uses `AwaitingGate` instead, and
loops while the gate keeps saying `retry`:

`Pending` → `Running` → `AwaitingGate` → (`Running` → `AwaitingGate`)* → `Succeeded` (or `Failed`)

`AwaitingGate` means the pod is alive and parked — the same shape as
`AwaitingDelegate`, which blocks a live pod on an external result.

When a preRun hook [skips the run](#skipping-a-run), the flow short-circuits to the terminal `Skipped` phase: `Pending` → `Running` → `Skipped` (postRun hooks are bypassed).
