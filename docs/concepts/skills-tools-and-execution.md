# Skills, tools and execution

The sidebar's **Concepts** glossary uses these same terms. They describe
separate choices, not alternative names for one capability.

| Term | Meaning |
|---|---|
| Agent | Identity and configuration; not proof that compute is running. |
| Skill | Instructions, domain knowledge or a workflow the agent follows. |
| SkillPack | Packaged skills, optionally with sidecar tools, dependencies and RBAC. |
| Tool | An operation the agent can invoke. Instructions alone do not grant access. |
| Harness | The model/tool-use loop. `AgentRuntime` describes an approved runtime; `AgentHarness` is not a separate resource here. |
| Backend | Where execution occurs: the Kubernetes pod path or Celln. |
| Lifecycle | One-shot or enduring execution; independent of harness choice. |
| AgentRun | The execution record, including lifecycle, backend, status and results. |
| AgentRunTurn | A follow-up message and result within an enduring native run. |

A Kubernetes diagnostic skill explains **how to investigate**. Its tools perform
**the actual cluster reads**. RBAC or native grants determine **which reads are
allowed**. A skill cannot bypass those permissions.

## Compatibility today

Existing SkillPacks such as `k8s-ops`, GitHub GitOps and SRE observability remain
available on their supported paths. They are not automatically converted to
borrowed Celln tools. The native starter rejects run-level SkillPacks and does
not provide arbitrary MCP, shell, Python or Kubernetes administration. A skill
may itself assume pod credentials or a sidecar; copying its text is not parity.

The Celln fleet lends explicitly selected, reviewed revisions of two kinds of
tool (see [the toolbox](../guides/celln-fleet-installation.md#the-toolbox)):

- **Brokered tools**, eight of them:
  `workspace-read`, `workspace-list`, `workspace-search` (exact substring),
  `workspace-write`, `workspace-append`, `workspace-delete` over bounded,
  revision-checked files belonging to the live run; `https-fetch` (GET) and
  `https-post-json` (a JSON object, no credential) to the scope's approved
  hosts (`--celln-fleet-https-host`; by default any public HTTPS host, never a private, loopback or link-local address).
- **Borrowed commands** taken from digest-pinned images (busybox's grep, sed,
  awk, sort, …, and jq), called with validated arguments and no shell. Each
  `ClusterCellnTool` names its source image in `spec.sourceImage`.

These are not arbitrary host filesystem access or unrestricted shell commands.
The policy, runtime and agent grants must all permit access. A permission
preview explains grants; it neither issues authority nor proves execution
readiness. Tool selection is fixed for a live native parent.

## Persistence is backend-specific

Native Celln uses `AgentRun.spec.executionLifecycle: enduring`: one leased parent
cell and a disposable child (sub-cell) per turn. A child is a separate cell,
not an AI sub-agent or nested VM. Live context is bounded by the starter package (`spec.limits.taskBytes`, about 16 KiB on a current package).
Run-owned files and live context belong to the parent; controller/API restarts
do not destroy it.

When a parent is lost with its node (or its owner is replaced), the run
reports `ContextLost` and the controller creates a **continuation**: a new run
seeded with the conversation's committed exchanges, placed on any node with
capacity (`spec.conversation`, `status.cellnParent.continuedBy`; **Restart
elsewhere** in the UI). This carries the conversation forward, not the VM:
workspace files, tool output and failed turns are not restored. See
[Conversations survive their node](celln-backend.md#conversations-survive-their-node).

Existing OCI `HarnessSession` chat is different. Supported adapters may retain
state on a PVC and implement stop/resume; that does not transfer to Celln.
One-shot Celln work remains disposable and can invoke a deterministic tool with
no model or harness at all.

## Choosing through UI or YAML

Choose a runtime compatible with the backend. Native YAML selects the runtime
and tool revisions in `cellnSelection`, and lifecycle/ceilings in
`executionLifecycle` and `enduring`. The operator template must match the
persona, model, tools and limits. On the fleet, each namespace gets these
wrappers on first use; see
[Celln Fleet Installation](../guides/celln-fleet-installation.md).

The run form blocks native selection for an Agent with SkillPacks. The API and
admission webhook also reject SkillPacks and Agent MCP connections explicitly;
use a backend's wrapper Agent (`celln-agent`, `celln-agent-<backend>`).
Incompatible selections must not silently drop skills or switch backends; this
is not a capability-parity claim.
