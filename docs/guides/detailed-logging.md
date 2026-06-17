# Detailed File Logging

By default, agent-runner truncates log output to keep `kubectl logs` readable.
Tool call arguments are capped at 200 characters, exec commands at 120, and
tool results at 8,000. This makes debugging difficult when payloads contain
long JSON or commands.

Detailed file logging is an opt-in mode that writes **untruncated** JSONL log
files alongside the normal truncated stdout output.

---

## How It Works

When enabled, agent-runner writes two JSONL files:

| File | Contents |
|------|----------|
| `agent.jsonl` | Untruncated agent-runner events: tool calls, exec requests, tool results, lifecycle events |
| `llm.jsonl` | Full LLM request and response payloads |

Every line in both files includes a shared `seq` counter and `run_id` field,
so you can merge and sort them for a complete chronological view.

Stdout (`kubectl logs`) continues to use truncated output — nothing changes
for normal operations.

---

## Enabling Detailed Logging

Set the `DETAILED_LOG_PATH` environment variable on your Agent or AgentRun to
a directory where the log files should be written:

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: Agent
metadata:
  name: my-agent
spec:
  agents:
    default:
      env:
        DETAILED_LOG_PATH: /var/log/agent
```

You must also mount a volume at that path:

```yaml
      volumes:
        - name: agent-logs
          emptyDir: {}
      volumeMounts:
        - name: agent-logs
          mountPath: /var/log/agent
```

Use `emptyDir` for ephemeral debugging (logs are lost when the pod dies) or a
PersistentVolumeClaim for durable storage.

---

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `DETAILED_LOG_PATH` | `""` (disabled) | Directory for JSONL log files. Empty = disabled. |
| `DETAILED_LOG_MAX_SIZE` | `50m` | Max size per file before rotation. Supports `m` (MB) and `g` (GB) suffixes. |

If `DETAILED_LOG_PATH` is set but the directory cannot be created, agent-runner
logs a warning and continues without file logging — it does not crash.

---

## Log Format

Each line is a self-contained JSON object.

**agent.jsonl:**

```json
{"ts":"2026-06-17T09:37:14.784Z","run_id":"enrichment-abc123","seq":1,"event":"tool_call","tool":"execute_command","args":"{\"command\":\"node /app/dist/cli.js fetch-catalog ...\"}"}
{"ts":"2026-06-17T09:37:14.785Z","run_id":"enrichment-abc123","seq":2,"event":"exec_request","request_id":"1781689051636467711","command":"node /app/dist/cli.js fetch-catalog ..."}
{"ts":"2026-06-17T09:37:15.100Z","run_id":"enrichment-abc123","seq":4,"event":"tool_result","tool":"execute_command","result_len":45231,"result":"...full untruncated output..."}
```

**llm.jsonl:**

```json
{"ts":"2026-06-17T09:37:14.780Z","run_id":"enrichment-abc123","seq":0,"event":"request","provider":"anthropic","model":"claude-sonnet-4-6","messages_count":12,"tools_count":8}
{"ts":"2026-06-17T09:37:17.980Z","run_id":"enrichment-abc123","seq":3,"event":"response","provider":"anthropic","model":"claude-sonnet-4-6","stop_reason":"tool_use","usage":{"input_tokens":1234,"output_tokens":567}}
```

---

## Merging Log Files

The `seq` counter is shared across both files. To get a unified chronological
view, merge and sort by `seq`:

```bash
jq -s 'sort_by(.seq)' agent.jsonl llm.jsonl
```

Or stream both files interleaved:

```bash
cat agent.jsonl llm.jsonl | jq -s 'sort_by(.seq) | .[]'
```

---

## File Rotation

When a log file exceeds `DETAILED_LOG_MAX_SIZE`, agent-runner rotates it:

- `agent.jsonl` is renamed to `agent.1.jsonl`
- A fresh `agent.jsonl` is created
- Same for `llm.jsonl` → `llm.1.jsonl`

Single rotation — at most one old file plus one current file per type (up to
4 files total). For long-running agents, increase `DETAILED_LOG_MAX_SIZE` or
copy files out periodically.

---

## Per-Run Override

Override logging for a single run using `AgentRun.spec.env`:

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: AgentRun
metadata:
  name: debug-run
spec:
  agentName: my-agent
  task: "investigate the login failure"
  env:
    DETAILED_LOG_PATH: /var/log/agent
    DETAILED_LOG_MAX_SIZE: "100m"
```

AgentRun-level env takes precedence over Agent-level env for the same key.