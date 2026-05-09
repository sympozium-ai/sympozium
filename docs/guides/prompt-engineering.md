# Prompt Engineering for Ensembles

Writing effective system prompts and stimulus prompts is the most important factor in getting Ensembles to behave the way you want. This guide covers patterns and pitfalls for each workflow type.

---

## General Principles

### Be explicit about workflow, not just role

A system prompt that says *"You are a code reviewer"* tells the model **what** it is but not **how** to work. Always include:

- **Concrete steps** the agent should follow (numbered workflow)
- **Which tools to use** and when
- **What to do when finished** (summarize, store in memory, stop)
- **What NOT to do** (guardrails)

!!! tip "The most common failure mode"
    Agents that don't know when to stop. Always include an explicit termination instruction: *"After collecting results, write a summary and stop."*

### Match prompt detail to model capability

Smaller or local models (Qwen, Llama, Mistral) need more prescriptive prompts than frontier models (Claude, GPT-4). With local models:

- Use `MANDATORY`, `MUST`, `DO NOT` for critical instructions
- Number your steps explicitly
- Repeat key constraints in both the system prompt and the stimulus prompt
- Avoid ambiguous phrasing like "you may want to" — say "you must"

### Stimulus prompt vs system prompt

| | System Prompt | Stimulus Prompt |
|---|---|---|
| **Purpose** | Defines the agent's role, capabilities, and rules | Gives the specific task to execute |
| **Persistence** | Same across all runs | Can change per trigger |
| **Scope** | General behavior and workflow patterns | Concrete objectives for this run |

The stimulus prompt is injected as the task for the first AgentRun. Write it as a clear instruction, not a description.

---

## Sub-Agent Spawning (`subagents` skill)

The `subagents` skill gives agents the `spawn_subagents` tool. Without the right prompting, agents will either ignore the tool entirely or use it incorrectly.

### Required prompt elements

Your system prompt **must** tell the agent:

1. That it has the `spawn_subagents` tool and should use it
2. When to use parallel vs sequential strategy
3. How to structure task descriptions for sub-agents
4. What to do after results come back

### Pattern: Fan-out analysis

```yaml
systemPrompt: |
  You are a lead analyst. Your ONLY job is to coordinate work by
  spawning sub-agents — you must NOT do the analysis yourself.

  MANDATORY WORKFLOW:
  1. Discover the items to analyze (list files, query resources, etc.)
  2. Call spawn_subagents with strategy "parallel" — one task per item.
     Give each task a clear ID and a self-contained description with
     all context the sub-agent needs.
  3. Wait for all sub-agents to return results.
  4. Synthesise a final report combining all findings.
  5. Store the report in memory and stop.

  RULES:
  - You MUST call spawn_subagents — this is non-negotiable
  - Do NOT analyze items yourself — spawn sub-agents to do it
  - Each sub-agent task must be self-contained with full context
  - After collecting results, write a summary and STOP
  - Do NOT spawn additional batches after the first
```

### Pattern: Creative fan-out

For creative tasks (writing, brainstorming), guide the decomposition:

```yaml
systemPrompt: |
  You are a creative director. Spawn sub-agents to produce creative
  work in parallel, then curate the results.

  WORKFLOW:
  1. Decide on the creative dimensions (e.g. themes, styles, perspectives)
  2. Call spawn_subagents ONCE with strategy "parallel" — one sub-agent
     per dimension. Each task should specify the theme, style, and any
     constraints.
  3. After all results return, write a brief summary highlighting the
     best work and common themes.
  4. Stop after summarising. Do NOT spawn more sub-agents.

  Each sub-agent task should be 2-3 sentences with clear creative
  direction. Include any style or tone requirements.
```

### Sub-agent task descriptions

Sub-agents don't share context with the parent. Every task must be self-contained:

```yaml
# Bad — sub-agent has no context
tasks:
  - id: "module-1"
    task: "Analyze this module"

# Good — sub-agent has everything it needs
tasks:
  - id: "internal-controller"
    task: |
      Analyze the Go package at internal/controller/. This package
      contains Kubernetes controller reconciliation loops for the
      Sympozium CRDs (Ensemble, Agent, AgentRun). Look for:
      - Error handling gaps in reconcile functions
      - Race conditions in status updates
      - Resource leak risks (unclosed clients, watchers)
      Report findings as a markdown list with file:line references.
```

### Common pitfalls

| Problem | Cause | Fix |
|---------|-------|-----|
| Agent analyzes everything itself | Prompt doesn't mandate tool use | Add `You MUST call spawn_subagents` and `Do NOT do the work yourself` |
| Agent spawns infinite batches | No termination instruction | Add `Do NOT spawn additional batches after the first` |
| Agent times out waiting | Sub-agents take too long or model loops | Set explicit `timeout` on sub-agent tasks and add `After collecting results, stop` |
| Sub-agents produce poor results | Task descriptions lack context | Make each task self-contained with file paths, scope, and output format |
| Malformed tool calls (local models) | Model sends JSON as string | Use a larger model or simplify task descriptions. Qwen models may need `tasks` as a flat list, not nested JSON |

---

## Delegation (`delegate_to_persona`)

Delegation is for **pre-defined team workflows** where agents hand off to named personas. Unlike sub-agents, delegation targets are real personas with their own system prompts and skills.

### Pattern: Lead-worker delegation

```yaml
# Lead persona
systemPrompt: |
  You are the project lead. Your job is to coordinate the team.

  WORKFLOW:
  1. Analyze the incoming request and decide which specialist to use
  2. Use delegate_to_persona to send the task to the right persona
  3. When the result comes back, review it and decide next steps
  4. Once all work is complete, compile a final summary and stop

  Available specialists:
  - "researcher" — investigates topics and gathers data
  - "writer" — produces polished reports from research notes
  - "reviewer" — checks reports for accuracy and quality

  When delegating, include ALL relevant context in the task parameter.
  The target persona cannot see your conversation history.
```

### Key difference from sub-agents

| | `spawn_subagents` | `delegate_to_persona` |
|---|---|---|
| **Target** | Ad-hoc agents (no pre-defined persona) | Named personas with their own system prompt |
| **Skills** | Inherits parent's skills | Has its own configured skills |
| **Best for** | Fan-out of similar tasks | Handoff to specialists with different capabilities |
| **Relationship** | No relationship edge required | Requires a `delegation` relationship edge |

### When to use which

- **Sub-agents**: Parallelize the same type of work across multiple items (analyze N modules, write N stories, check N services)
- **Delegation**: Hand off to a specialist with different skills (researcher → writer → reviewer)
- **Both together**: Lead spawns sub-agents for fan-out analysis, then delegates the synthesis to an architect for review

---

## Stimulus Prompts

The stimulus prompt fires when all agents in the ensemble reach the Serving phase. It should be a concrete instruction, not a role description.

### Pattern: Direct instruction

```yaml
# Bad — vague, repeats the system prompt
stimulus:
  name: start
  prompt: "You are the lead. Start working on the task."

# Good — specific, actionable
stimulus:
  name: kickoff
  prompt: |
    Begin a new code analysis cycle. Follow these steps:
    1. List the top-level directories in the repository
    2. Spawn one sub-agent per directory to analyze code quality
    3. After all results return, write a summary report
    4. Store the report in shared memory with tag "analysis"
```

### Reinforcing critical instructions

For local models, repeat key constraints in both the system prompt and stimulus:

```yaml
systemPrompt: |
  You MUST use spawn_subagents to fan out work.
  Do NOT analyze code yourself.
  After collecting results, summarise and STOP.

stimulus:
  name: analyze
  prompt: |
    Analyze the codebase. You MUST follow these steps exactly:
    Step 1: List the top-level modules.
    Step 2: Call spawn_subagents ONCE with strategy "parallel".
    Step 3: After all sub-agents return, write a summary.
    Step 4: Store the report in memory and stop.
    IMPORTANT: You MUST use spawn_subagents. Do NOT analyze yourself.
```

---

## Workflow-Specific Patterns

### Autonomous ensembles

Agents run independently on their own schedules. Prompts should be self-contained:

```yaml
systemPrompt: |
  You are a cluster health monitor. Every run, you:
  1. Check pod status across all namespaces
  2. Identify any crashlooping or pending pods
  3. Check node resource utilization
  4. Store findings in memory
  5. If critical issues found, write an alert summary

schedule:
  type: heartbeat
  interval: "30m"
  task: "Perform a routine cluster health check."
```

### Pipeline ensembles (sequential)

Each agent's output feeds the next. Prompts should specify input/output format:

```yaml
# Stage 1: Researcher
systemPrompt: |
  You are a researcher. Investigate the assigned topic and produce
  structured research notes in markdown format with:
  - Key findings (bulleted list)
  - Sources (URLs or references)
  - Open questions

  Your output will be passed directly to a Writer.

# Stage 2: Writer (receives researcher's output as task)
systemPrompt: |
  You are a technical writer. You receive research notes and transform
  them into a polished report with:
  - Executive summary (3-4 sentences)
  - Detailed findings
  - Recommendations

  Your input is structured research notes from the Researcher.
```

### Delegation ensembles

The lead coordinates work across specialists. See the [Lead-worker delegation](#pattern-lead-worker-delegation) pattern above.

---

## Local Model Tips

When using models like Qwen, Llama, or Mistral via LM Studio or Ollama:

1. **Be maximally explicit** — don't rely on the model inferring intent
2. **Use numbered steps** — local models follow numbered instructions more reliably
3. **Repeat constraints** — state critical rules in both system prompt and stimulus
4. **Keep sub-agent count low** — start with 2-3 sub-agents, not 5+
5. **Set shorter timeouts** — local models may loop; shorter timeouts fail faster
6. **Use simple task IDs** — `"task-1"` not `"analysis-of-internal-controller-package"`
7. **Add explicit stop instructions** — `"After step 4, you are DONE. Stop immediately."`

---

## Debugging Prompt Issues

### Agent ignores tools

Check that the skill is listed in `skills:` and the tool isn't blocked by `toolPolicy`. Then make the prompt more directive:

```yaml
# Before
systemPrompt: "You can use sub-agents to parallelize work."

# After
systemPrompt: "You MUST use the spawn_subagents tool. Do NOT skip this step."
```

### Agent runs forever

Add explicit termination and reduce `maxChildrenPerAgent`:

```yaml
systemPrompt: |
  ...
  After collecting all results, write your summary and STOP.
  Do NOT spawn additional sub-agents after your first batch.

subagents:
  maxDepth: 1
  maxChildrenPerAgent: 3
```

### Delegation target not found

Ensure a `delegation` relationship edge exists between the source and target personas in `relationships:`. The `delegate_to_persona` tool only works for personas connected by a delegation edge.

### Sub-agent results are empty

The sub-agent's task description was too vague. Make it self-contained with all context needed to produce output.

---

## Complete Examples

See the built-in ensembles for working patterns:

- **[code-analysis-team](https://github.com/sympozium-ai/sympozium/blob/main/config/agent-configs/code-analysis-team.yaml)** — Sub-agent fan-out + delegation to architect
- **[subagent-analysis-example](https://github.com/sympozium-ai/sympozium/blob/main/config/agent-configs/subagent-analysis-example.yaml)** — Pure sub-agent fan-out (no delegation)
- **[research-delegation-example](https://github.com/sympozium-ai/sympozium/blob/main/config/agent-configs/research-delegation-example.yaml)** — Multi-stage delegation pipeline
- **[developer-team](https://github.com/sympozium-ai/sympozium/blob/main/config/agent-configs/developer-team.yaml)** — Seven-agent team with supervision and delegation
