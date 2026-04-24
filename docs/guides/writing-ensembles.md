# Writing Ensembles

A Ensemble bundles multiple agent personas into a single CRD. When activated,
the Ensemble controller stamps out all the Kubernetes resources — SympoziumInstances,
SympoziumSchedules, and memory seeds — automatically.

This guide walks through creating a custom Ensemble from scratch.

---

## Prerequisites

- Sympozium installed (`sympozium install`)
- Familiarity with [Ensembles concepts](../concepts/ensembles.md)
- An API key for your chosen provider, **or** a [cluster-local Model](./local-models.md)

---

## Anatomy of a Ensemble

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: Ensemble
metadata:
  name: my-team
spec:
  description: "My custom agent team"
  category: custom
  version: "1.0.0"
  personas:
    - name: my-agent
      # ... persona spec
```

### Top-level fields

| Field | Required | Description |
|-------|----------|-------------|
| `description` | No | Human-readable summary shown in the TUI |
| `category` | No | Grouping in the TUI (e.g. `platform`, `security`, `devops`, `custom`) |
| `version` | No | Semantic version of the pack |
| `policyRef` | No | Default SympoziumPolicy for all generated instances |
| `baseURL` | No | Override provider API endpoint (for Ollama, LM Studio, etc.) |
| `modelRef` | No | Reference a [cluster-local Model](./local-models.md) — all personas use this model, no API key needed |
| `sharedMemory` | No | Shared memory pool for cross-persona knowledge sharing (see Step 5) |
| `personas` | Yes | List of agent personas (see below) |

### Persona fields

| Field | Required | Description |
|-------|----------|-------------|
| `name` | Yes | Identifier — used as suffix in generated resource names |
| `displayName` | No | Human-readable name shown in the TUI |
| `systemPrompt` | Yes | The system prompt defining the agent's role and behaviour |
| `model` | No | Override the model for this persona (otherwise uses the onboarding-time default) |
| `skills` | No | List of SkillPack names to mount |
| `toolPolicy` | No | Restrict which tools the agent can use |
| `schedule` | No | Recurring task configuration |
| `memory` | No | Initial memory seeds |
| `channels` | No | Channel types to bind (e.g. `["telegram", "slack"]`) |
| `webEndpoint` | No | Expose this persona as an HTTP API |

---

## Step 1: Define personas

Start by defining the agents in your team. Each persona gets its own
SympoziumInstance when the pack is activated.

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: Ensemble
metadata:
  name: data-team
spec:
  description: "Data engineering agents for pipeline monitoring"
  category: data
  version: "1.0.0"
  personas:
    - name: pipeline-monitor
      displayName: "Pipeline Monitor"
      systemPrompt: |
        You are a data pipeline monitoring agent. Your job is to:
        - Check the status of data pipeline CronJobs
        - Verify that output datasets are fresh (last-modified < 2h)
        - Alert on failed jobs or stale data
        - Provide summaries of pipeline health

        Always check the actual state of resources before reporting.
        When you find issues, include the relevant logs and events.
      skills:
        - k8s-ops

    - name: schema-auditor
      displayName: "Schema Auditor"
      systemPrompt: |
        You are a schema auditing agent. Your job is to:
        - Review ConfigMaps containing schema definitions
        - Check for backwards-incompatible changes
        - Verify schema versions are consistent across services
      skills:
        - k8s-ops
```

### Tips for system prompts

- **Be specific** about the agent's role and responsibilities
- **List concrete actions** the agent should take, not vague goals
- **Specify output format** — tables, summaries, or structured reports
- **Include guardrails** — what the agent should _not_ do
- **Reference tool capabilities** — if the agent has `k8s-ops`, tell it to use kubectl

---

## Step 2: Add schedules

Schedules trigger agent runs automatically. There are three schedule types:

| Type | Use case | Example |
|------|----------|---------|
| `heartbeat` | Regular polling at a fixed interval | Check cluster health every 30m |
| `scheduled` | Run at specific times (cron expression) | Daily report at 9am |
| `sweep` | Periodic sweeps of resources | Audit RBAC every 6h |

```yaml
    - name: pipeline-monitor
      displayName: "Pipeline Monitor"
      systemPrompt: |
        ...
      skills:
        - k8s-ops
      schedule:
        type: heartbeat
        interval: "30m"
        task: "Check the status of all CronJobs in the data namespace. Report any failures or jobs that haven't run on schedule."

    - name: schema-auditor
      displayName: "Schema Auditor"
      systemPrompt: |
        ...
      skills:
        - k8s-ops
      schedule:
        type: scheduled
        cron: "0 9 * * 1-5"    # weekdays at 9am
        task: "Audit all schema ConfigMaps in the data namespace. Report any backwards-incompatible changes since last run."
```

You can use either `interval` (human-readable, converted to cron by the controller)
or `cron` (raw cron expression). `cron` takes precedence if both are set.

---

## Step 3: Add tool policies

Tool policies restrict which tools an agent can use. This is important for
least-privilege access.

```yaml
    - name: pipeline-monitor
      displayName: "Pipeline Monitor"
      systemPrompt: |
        ...
      skills:
        - k8s-ops
      toolPolicy:
        allow:
          - read_file
          - list_directory
          - execute_command
          - grep_search
          - fetch_url
        deny:
          - write_file
      schedule:
        type: heartbeat
        interval: "30m"
        task: "Check pipeline health."
```

If `toolPolicy` is omitted, the agent inherits the policy from `policyRef`
(pack-level or instance-level).

---

## Step 4: Seed memory

Memory seeds are initial entries injected into the agent's persistent memory
when the pack is first activated. Use these to give agents baseline context.

```yaml
    - name: pipeline-monitor
      displayName: "Pipeline Monitor"
      systemPrompt: |
        ...
      memory:
        enabled: true
        seeds:
          - "Critical pipelines: etl-orders, etl-users, etl-events — these run hourly and must not be stale for more than 2 hours"
          - "The data namespace is 'data-prod' in production and 'data-staging' in staging"
          - "Alert channel: #data-alerts in Slack"
```

Memory seeds are written to a ConfigMap that is mounted into the agent pod.
The agent can update its own memory across runs.

---

## Step 5: Enable shared memory (optional)

If your personas need to share knowledge — for example, a researcher's findings
feeding into a writer's reports — enable **shared workflow memory**:

```yaml
spec:
  sharedMemory:
    enabled: true
    storageSize: "1Gi"
    accessRules:
      - persona: pipeline-monitor
        access: read-write
      - persona: schema-auditor
        access: read-only
```

This provisions a pack-level SQLite database that all personas can query. Each
persona retains its own private memory alongside the shared pool.

### Access rules

- `read-write`: persona can search, list, and store entries (default if no rules specified)
- `read-only`: persona can search and list, but cannot store

Entries stored via `workflow_memory_store` are automatically tagged with the
source persona's name, so other agents can filter by contributor.

### When to use shared memory

- **Research teams**: researcher stores findings, writer reads them
- **Incident response**: first responder logs context, escalation agent reads it
- **Pipeline monitoring**: multiple monitors share a common knowledge base
- **Any team where one persona's output informs another's work**

---

## Step 6: Bind channels (optional)

If you want personas to respond to messages from Slack, Telegram, Discord, or
WhatsApp, list the channel types:

```yaml
    - name: pipeline-monitor
      displayName: "Pipeline Monitor"
      systemPrompt: |
        ...
      channels:
        - slack
        - telegram
```

Channel credentials are configured at activation time (via the TUI wizard or
by setting `channelConfigs` on the pack spec).

---

## Step 7: Expose as HTTP API (optional)

To expose a persona as an OpenAI-compatible HTTP endpoint:

```yaml
    - name: pipeline-monitor
      displayName: "Pipeline Monitor"
      systemPrompt: |
        ...
      webEndpoint:
        enabled: true
```

This deploys the `web-endpoint` skill with a web-proxy sidecar. See the
[Web Endpoint Skill](../skills/web-endpoint.md) for full details.

---

## Complete example

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: Ensemble
metadata:
  name: data-team
spec:
  description: "Data engineering agents for pipeline monitoring and schema auditing"
  category: data
  version: "1.0.0"
  policyRef: default-policy
  personas:
    - name: pipeline-monitor
      displayName: "Pipeline Monitor"
      systemPrompt: |
        You are a data pipeline monitoring agent. Your job is to:
        - Check the status of data pipeline CronJobs in the data namespace
        - Verify that output datasets are fresh (last-modified < 2h)
        - Alert on failed jobs or stale data
        - Provide clear summaries of pipeline health as a table

        Always check the actual state of resources before reporting.
        When you find issues, include the relevant logs and events.
      skills:
        - k8s-ops
      toolPolicy:
        allow:
          - read_file
          - list_directory
          - execute_command
          - grep_search
          - fetch_url
      schedule:
        type: heartbeat
        interval: "30m"
        task: "Check the status of all CronJobs in the data namespace. Report any failures or stale data."
      memory:
        enabled: true
        seeds:
          - "Critical pipelines: etl-orders, etl-users, etl-events — hourly, max 2h staleness"
          - "Data namespace: data-prod (production), data-staging (staging)"
      channels:
        - slack

    - name: schema-auditor
      displayName: "Schema Auditor"
      systemPrompt: |
        You are a schema auditing agent. Your job is to:
        - Review ConfigMaps containing schema definitions in the data namespace
        - Check for backwards-incompatible changes
        - Verify schema versions are consistent across services
        - Report findings as a structured summary
      skills:
        - k8s-ops
      toolPolicy:
        allow:
          - read_file
          - list_directory
          - execute_command
          - grep_search
      schedule:
        type: scheduled
        cron: "0 9 * * 1-5"
        task: "Audit all schema ConfigMaps in the data namespace. Report any backwards-incompatible changes."
      memory:
        enabled: true
        seeds:
          - "Schema ConfigMaps follow naming convention: schema-<service>-<version>"
          - "Backwards-incompatible changes: removing fields, changing types, renaming fields"
```

---

## Applying and activating

### Apply the pack

```bash
kubectl apply -f data-team.yaml
```

The pack appears in the TUI's Personas tab but is not yet active (no
instances are created).

### Activate via the TUI

1. Launch `sympozium` and go to the **Personas** tab
2. Select your pack and press **Enter**
3. Choose your AI provider and paste an API key
4. Optionally bind channels
5. Confirm — the controller creates all instances

### Activate via kubectl

```bash
# Create the provider secret
kubectl create secret generic data-team-key \
  --from-literal=OPENAI_API_KEY=sk-...

# Patch the pack to activate
kubectl patch ensemble data-team --type=merge -p '{
  "spec": {
    "enabled": true,
    "authRefs": [{"provider": "openai", "secret": "data-team-key"}]
  }
}'
```

### Verify

```bash
kubectl get sympoziuminstance -l sympozium.ai/ensemble=data-team
kubectl get sympoziumschedule -l sympozium.ai/ensemble=data-team
```

---

## What the controller creates

When a Ensemble is activated, the controller stamps out resources for each
persona:

```
Ensemble "data-team" (2 personas)
  │
  ├── Secret: data-team-key (created by user)
  │
  ├── SympoziumInstance: data-team-pipeline-monitor
  │   ├── SympoziumSchedule: data-team-pipeline-monitor-schedule
  │   └── ConfigMap: data-team-pipeline-monitor-memory
  │
  └── SympoziumInstance: data-team-schema-auditor
      ├── SympoziumSchedule: data-team-schema-auditor-schedule
      └── ConfigMap: data-team-schema-auditor-memory
```

All generated resources have `ownerReferences` pointing back to the
Ensemble — delete the pack and everything gets garbage-collected.

---

## Overriding per-persona settings

### Task override

Set a team-level objective that prepends every persona's schedule task:

```yaml
spec:
  taskOverride: "Focus on the payments service migration this week."
```

Each persona's schedule task becomes:
`"Focus on the payments service migration this week. <original task>"`

### Skill parameters

Pass per-skill configuration to all generated instances:

```yaml
spec:
  skillParams:
    github-gitops:
      REPO: "myorg/myrepo"
      BRANCH: "main"
```

These become `SKILL_REPO` and `SKILL_BRANCH` environment variables in the
skill sidecar.

### Excluding personas

Disable individual personas without deleting the pack:

```bash
kubectl patch ensemble data-team --type=merge -p '{
  "spec": {
    "excludePersonas": ["schema-auditor"]
  }
}'
```

The controller deletes the Instance and Schedule for that persona while
keeping the rest active.

---

## Troubleshooting

| Issue | Check |
|-------|-------|
| Pack not appearing in TUI | `kubectl get ensemble` — is the CRD applied? |
| Instances not created | Is `spec.enabled: true`? Are `authRefs` set? |
| Schedule not firing | `kubectl get sympoziumschedule` — check the cron expression and last run time |
| Memory not seeded | `kubectl get configmap -l sympozium.ai/ensemble=<name>` |
| Wrong model | Set `model` on individual personas, or check the onboarding-time default |
