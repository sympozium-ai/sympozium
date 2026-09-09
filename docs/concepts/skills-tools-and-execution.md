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

The native starter offers explicitly selected, reviewed revisions of:

- `workspace-read`: bounded logical files belonging to the live run.
- `workspace-write`: bounded, revision-checked updates to those files.
- `https-fetch`: bounded host-brokered HTTPS to approved destinations (the first
  starter profile permits `example.com`).

These are not arbitrary host filesystem access or unrestricted shell commands.
The operator, runtime and agent grants must all permit access. A permission
preview explains grants; it neither issues authority nor proves execution
readiness. Tool selection is fixed for a live native parent.

## Persistence is backend-specific

Native Celln uses `AgentRun.spec.executionLifecycle: enduring`: one leased parent
cell and a disposable child (sub-cell) per turn. A child is a separate cell,
not an AI sub-agent or nested VM. Context is currently approximately 2 KiB.
Run-owned files and live context disappear with the parent; checkpoint/resume
after parent or host loss is not supported. Controller/API restarts do not
themselves destroy the host parent. Saved history is not a restorable VM.

Existing OCI `HarnessSession` chat is different. Supported adapters may retain
state on a PVC and implement stop/resume; that does not transfer to Celln.
One-shot Celln work remains disposable and can invoke a deterministic tool with
no model or harness at all.

## Choosing through UI or YAML

Choose a runtime compatible with the backend. Native YAML selects the runtime
and tool revisions in `cellnSelection`, and lifecycle/ceilings in
`executionLifecycle` and `enduring`. The operator template must match the
persona, model, tools and limits. See
[native installation](../guides/celln-native-installation.md).

The run form blocks native selection for an Agent with SkillPacks. The API and
admission webhook also reject SkillPacks and Agent MCP connections explicitly;
use the dedicated installed native starter Agent. Incompatible selections must
not silently drop skills or switch backends. Full installed
acceptance is still being qualified; this is not a capability-parity claim.
