/**
 * OntologyModal — explains the core Sympozium concepts and how they relate.
 */

import { useState } from "react";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { ScrollArea } from "@/components/ui/scroll-area";
import { HelpCircle, ArrowRight } from "lucide-react";

interface Concept {
  name: string;
  icon: string;
  short: string;
  detail: string;
  relates?: string;
}

const CONCEPTS: Concept[] = [
  {
    name: "Model",
    icon: "M",
    short: "A local LLM running inside the cluster",
    detail:
      "A Model CRD declares a GGUF model to be downloaded, served via llama-server, and exposed as an OpenAI-compatible endpoint. Models are auto-placed on the best node via llmfit. No external API keys required.",
    relates: "Referenced by Ensembles and AgentRuns via modelRef",
  },
  {
    name: "Ensemble",
    icon: "E",
    short: "A team of AI agents bundled together",
    detail:
      "An Ensemble is a Helm-like bundle that defines a group of Agent Configs, their relationships (delegation, sequential, supervision), shared memory, and the AI provider they use. Activating an Ensemble stamps out Agents, Schedules, and memory for each Agent Config.",
    relates: "Contains Agent Configs, creates Agents when activated",
  },
  {
    name: "Agent Config",
    icon: "C",
    short: "A role definition within an Ensemble",
    detail:
      "An Agent Config defines an agent's identity: its name, system prompt, model, skills, and schedule. Each Agent Config in an Ensemble becomes an Agent when the Ensemble is activated. Agent Configs can have different models and provider overrides.",
    relates: "Lives inside an Ensemble, becomes an Agent",
  },
  {
    name: "Agent",
    icon: "A",
    short: "An agent identity and its configuration",
    detail:
      "An Agent holds model and provider settings, credential references, skills, an optional runtime reference, memory settings and channel bindings. It is configuration, not proof that a process is running. Agents can be created directly or from Ensembles.",
    relates: "Created from an Agent Config, runs AgentRuns",
  },
  {
    name: "AgentRun",
    icon: "R",
    short: "An execution with its own lifecycle",
    detail:
      "An AgentRun records requested work, execution settings, status and results. It can be one-shot or enduring. The backend determines where it executes: the usual Kubernetes path uses pods; Celln uses sealed cells. A one-shot can invoke a deterministic tool without an LLM or harness.",
    relates: "Uses an Agent; lifecycle and backend are separate choices",
  },
  {
    name: "Harness / AgentRuntime",
    icon: "H",
    short: "How the agent reasons and uses tools",
    detail:
      "A harness implements the model and tool-use loop. AgentRuntime is the Kubernetes resource describing an operator-configured runtime and its supported contracts. It may describe an OCI adapter, a native Celln profile, or both. Selecting a harness does not choose a lifecycle or grant tool permissions. AgentHarness is a design term, not a separate resource in this path.",
    relates: "Selected through runtimeRef or cellnSelection.runtimeRef; compatibility is backend-specific",
  },
  {
    name: "Execution environment / Backend",
    icon: "B",
    short: "Where the work runs",
    detail:
      "The Kubernetes execution path runs containers in pods. The Celln backend runs work in hardware-isolated cells while Kubernetes still orchestrates the AgentRun. Harness and tool support varies by backend: choosing Celln does not automatically make existing SkillPack sidecars or MCP integrations available.",
    relates: "AgentRun.spec.backend selects Celln with celln; omitted uses the default path",
  },
  {
    name: "Lifecycle: one-shot / Enduring",
    icon: "L",
    short: "How long an execution lives",
    detail:
      "A one-shot performs its work and finishes; on Celln its cell is disposable. An enduring native Celln run keeps a parent cell alive for multiple turns, with a fresh disposable child for each turn. YAML uses executionLifecycle: enduring and an enduring block of limits. A harness is optional for one-shots.",
    relates: "Duration is separate from harness and execution environment",
  },
  {
    name: "Persistent context / Lease",
    icon: "P",
    short: "Live continuity, not crash recovery",
    detail:
      "The native parent retains bounded conversation context and run-owned files between turns. A lease limits its lifetime; turn, model-request and output-token budgets also apply. Current native context is approximately 2 KiB. Parent loss destroys this live state: persistence does not mean checkpointing, pause/resume or recovery after a host restart. Saved conversation history is not a restorable parent.",
    relates: "Controller/API restarts are different from losing the host parent",
  },
  {
    name: "Turn / AgentRunTurn",
    icon: "T",
    short: "One message within an enduring run",
    detail:
      "A turn submits work to the same live parent. The first message is tracked on AgentRun; later messages have AgentRunTurn resources with their own identities and results. Cancelling a turn requests that its child stop; cancellation is not confirmed until teardown is reported, and is distinct from stopping the parent.",
    relates: "Multiple turns belong to one enduring AgentRun",
  },
  {
    name: "Workflow",
    icon: "W",
    short: "How Agent Configs coordinate within an Ensemble",
    detail:
      "Workflows define relationships between Agent Configs: delegation (one agent asks another for help), sequential pipelines (output flows to next), and supervision (one agent oversees another). Visualised on the interactive canvas.",
    relates: "Defined by Ensemble relationships, visible on the canvas",
  },
  {
    name: "Skill",
    icon: "S",
    short: "Instructions for doing a job",
    detail:
      "A skill gives an agent instructions, domain knowledge or a workflow, such as investigating Kubernetes health. It may require tools, but instructions are not executable tools or permission to use them. A diagnostic skill still needs access to the relevant cluster operations.",
    relates: "Skills guide behaviour; tools perform operations",
  },
  {
    name: "SkillPack",
    icon: "SP",
    short: "Packaged skills and optional tooling",
    detail:
      "A SkillPack bundles skill instructions and may also declare a tool sidecar, dependencies and Kubernetes RBAC. Existing examples include k8s-ops, GitHub GitOps and SRE observability. Not every skill needs a sidecar. These packages are not automatically portable to native Celln, which currently rejects run-level SkillPacks.",
    relates: "Selected on Agents through skills; tool access depends on the execution path",
  },
  {
    name: "Tool / Borrowed tool",
    icon: "TL",
    short: "An operation the agent can perform",
    detail:
      "Tools read files, write files, fetch URLs or perform other operations. A borrowed Celln tool is an explicitly selected, reviewed executable revision with bounded inputs, outputs and permissions. Selection requests access; it does not grant authority by itself. Tools cannot currently be added to an already-live native parent.",
    relates: "Harnesses invoke tools; skills explain when and how to use them",
  },
  {
    name: "Tool catalogue / CellnTool",
    icon: "CT",
    short: "Reviewed tool identities and revisions",
    detail:
      "CellnTool records a tool revision, signed artifact identities, JSON schemas and declared limits. Runs select these using cellnSelection.toolRefs. A catalogue entry is not proof that the host has admitted the tool or is ready to execute it. The native starter currently offers workspace-read, workspace-write and https-fetch, not arbitrary shell, Python or Kubernetes administration.",
    relates: "Catalogue metadata, permission approval and execution readiness are separate",
  },
  {
    name: "Permission grant / Preview",
    icon: "G",
    short: "What selected tools are allowed to do",
    detail:
      "Native tool access must fit the operator, runtime and agent grants together. Limits can bound operations, file sizes and HTTPS destinations. The permission preview explains effective access without issuing execution authority or proving host readiness. Selecting a skill or tool never overrides these limits.",
    relates: "Authority can only shrink during execution; it cannot expand itself",
  },
  {
    name: "Run-owned workspace",
    icon: "F",
    short: "Files belonging to one live native parent",
    detail:
      "workspace-read and workspace-write operate on bounded logical files owned by the native run, not arbitrary host paths, a mounted repository or a Kubernetes persistent volume. Writes use revisions to detect conflicting updates. Files survive child turns but are lost when the parent is destroyed. https-fetch is separately restricted to operator-approved destinations.",
    relates: "Live run storage is distinct from durable Agent memory or chat history",
  },
  {
    name: "MCP server",
    icon: "MCP",
    short: "An integration exposing tools over MCP",
    detail:
      "Model Context Protocol servers expose tools and other context to compatible clients. An MCP integration is neither a skill nor automatic permission to access a service. Existing MCP connections are not automatically borrowed Celln tools; the native starter does not yet support arbitrary MCP integrations.",
    relates: "Availability depends on harness, backend and approved access",
  },
  {
    name: "Celln / Cell / Mote",
    icon: "C",
    short: "Execution plane, live unit and substrate",
    detail:
      "Celln is the execution plane. A mote is the substrate at rest: a stripped kernel and pilot supervisor. A cell is a live, sealed, tool-loaned mote; every cell is a sealed mote. Spawn uses a copy-on-write fork of a warm mote, rather than booting a guest in the hot path. Cell and mote are not interchangeable names.",
    relates: "Kubernetes orchestrates runs; Celln executes their isolated work",
  },
  {
    name: "Parent cell / Sub-cell",
    icon: "PC",
    short: "Retained context and disposable turn work",
    detail:
      "An enduring native run has a persistent parent cell for live context and disposable child cells, also called sub-cells, for per-turn work. A sub-cell is a separate cell, not an AI sub-agent or a nested VM inside the parent. A completed child is torn down; the parent can accept another turn within its remaining limits.",
    relates: "One-shot cells remain disposable and need no persistent parent",
  },
  {
    name: "Assay / Warden / Pilot",
    icon: "A/W/P",
    short: "Celln host, isolation and guest components",
    detail:
      "Assay is the host daemon and distribution layer. Warden is the per-cell virtual-machine monitor: one warden, one microVM, one cell. Pilot is the in-cell supervisor. The native host broker mediates bounded model and tool requests; model credentials remain host-side rather than entering the guest.",
    relates: "These are infrastructure components, not extra user-selectable skills",
  },
  {
    name: "Policy",
    icon: "G",
    short: "Governance rules for agent behaviour",
    detail:
      "A SympoziumPolicy enforces sandbox requirements, resource limits, sub-agent depth, tool gating, network isolation, and model access restrictions. Policies are bound to Agents and validated by an admission webhook.",
    relates: "Bound to Agents via policyRef",
  },
];

export function OntologyModal() {
  const [open, setOpen] = useState(false);

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <button
          title="Concepts"
          className="flex items-center justify-center rounded-md p-1.5 text-muted-foreground hover:bg-white/5 hover:text-foreground transition-colors"
        >
          <HelpCircle className="h-4 w-4" />
        </button>
      </DialogTrigger>
      <DialogContent className="max-w-lg max-h-[80vh]">
        <DialogHeader>
          <DialogTitle className="text-lg">Sympozium Concepts</DialogTitle>
          <p className="text-sm text-muted-foreground">
            Skills guide behaviour; tools perform operations. Harness, execution
            environment and lifecycle are separate choices.
          </p>
        </DialogHeader>
        <ScrollArea className="max-h-[60vh] pr-2">
          <div className="space-y-4">
            {/* Relationship diagram */}
            <div className="rounded-md border border-border/50 bg-muted/20 px-4 py-3 text-xs font-mono text-muted-foreground">
              <div className="flex items-center justify-center gap-1 flex-wrap">
                <span className="text-violet-400">Model</span>
                <ArrowRight className="h-3 w-3" />
                <span className="text-blue-400">Ensemble</span>
                <ArrowRight className="h-3 w-3" />
                <span className="text-cyan-400">Agent Config</span>
                <ArrowRight className="h-3 w-3" />
                <span className="text-emerald-400">Agent</span>
                <ArrowRight className="h-3 w-3" />
                <span className="text-amber-400">AgentRun</span>
              </div>
              <p className="text-center mt-1 text-[10px]">
                Skills guide Agents. Policies govern access. Each AgentRun
                selects an execution environment and lifecycle.
              </p>
            </div>

            {/* Concept cards */}
            {CONCEPTS.map((c) => (
              <div
                key={c.name}
                className="rounded-md border border-border/50 px-4 py-3 space-y-1"
              >
                <div className="flex items-center gap-2">
                  <span className="flex items-center justify-center h-6 w-6 rounded bg-primary/10 text-primary text-xs font-bold">
                    {c.icon}
                  </span>
                  <h3 className="font-semibold text-sm">{c.name}</h3>
                  <span className="text-xs text-muted-foreground ml-auto">
                    {c.short}
                  </span>
                </div>
                <p className="text-xs text-muted-foreground leading-relaxed">
                  {c.detail}
                </p>
                {c.relates && (
                  <p className="text-[10px] text-primary/70 italic">
                    {c.relates}
                  </p>
                )}
              </div>
            ))}
          </div>
        </ScrollArea>
      </DialogContent>
    </Dialog>
  );
}

/** Expanded variant for the sidebar (shows label text). */
export function OntologyModalExpanded() {
  const [open, setOpen] = useState(false);

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <button className="flex items-center gap-2 rounded-md px-2 py-1.5 text-xs font-medium text-muted-foreground hover:bg-white/5 hover:text-foreground transition-colors w-full">
          <HelpCircle className="h-3.5 w-3.5" />
          Concepts
        </button>
      </DialogTrigger>
      <DialogContent className="max-w-lg max-h-[80vh]">
        <DialogHeader>
          <DialogTitle className="text-lg">Sympozium Concepts</DialogTitle>
          <p className="text-sm text-muted-foreground">
            Skills guide behaviour; tools perform operations. Harness, execution
            environment and lifecycle are separate choices.
          </p>
        </DialogHeader>
        <ScrollArea className="max-h-[60vh] pr-2">
          <div className="space-y-4">
            <div className="rounded-md border border-border/50 bg-muted/20 px-4 py-3 text-xs font-mono text-muted-foreground">
              <div className="flex items-center justify-center gap-1 flex-wrap">
                <span className="text-violet-400">Model</span>
                <ArrowRight className="h-3 w-3" />
                <span className="text-blue-400">Ensemble</span>
                <ArrowRight className="h-3 w-3" />
                <span className="text-cyan-400">Agent Config</span>
                <ArrowRight className="h-3 w-3" />
                <span className="text-emerald-400">Agent</span>
                <ArrowRight className="h-3 w-3" />
                <span className="text-amber-400">AgentRun</span>
              </div>
              <p className="text-center mt-1 text-[10px]">
                Skills guide Agents. Policies govern access. Each AgentRun
                selects an execution environment and lifecycle.
              </p>
            </div>

            {CONCEPTS.map((c) => (
              <div
                key={c.name}
                className="rounded-md border border-border/50 px-4 py-3 space-y-1"
              >
                <div className="flex items-center gap-2">
                  <span className="flex items-center justify-center h-6 w-6 rounded bg-primary/10 text-primary text-xs font-bold">
                    {c.icon}
                  </span>
                  <h3 className="font-semibold text-sm">{c.name}</h3>
                  <span className="text-xs text-muted-foreground ml-auto">
                    {c.short}
                  </span>
                </div>
                <p className="text-xs text-muted-foreground leading-relaxed">
                  {c.detail}
                </p>
                {c.relates && (
                  <p className="text-[10px] text-primary/70 italic">
                    {c.relates}
                  </p>
                )}
              </div>
            ))}
          </div>
        </ScrollArea>
      </DialogContent>
    </Dialog>
  );
}
