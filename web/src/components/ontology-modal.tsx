/** Shared concepts guide for both navigation layouts. */
import {
  Dialog, DialogContent, DialogDescription, DialogHeader,
  DialogTitle, DialogTrigger,
} from "@/components/ui/dialog";
import { HelpCircle } from "lucide-react";

const CHOICES = [
  ["Who?", "Agent", "The identity and defaults: instructions, model settings, skills and approved access. Creating an Agent does not start a process. You can create one directly; an Ensemble is optional."],
  ["How?", "Harness", "The program that manages the model conversation and tool use. Choose an approved harness, or use the built-in runner where supported. A one-shot tool job may need neither a harness nor a model."],
  ["Where?", "Execution environment", "Kubernetes is the default and runs work in containers. Celln is opt-in and runs work in hardware-isolated cells. Choosing a harness does not switch the environment. Harness and tool support must match the environment."],
  ["How long?", "Run lifecycle", "A one-shot completes a task and finishes. An enduring native run stays available for more messages within its limits. Both are AgentRuns—not different kinds of Agent."],
  ["With what access?", "Skills, tools and policy", "Skills explain how to do a job. Tools perform actions. Policy and permission grants limit which actions are allowed. Selecting a skill or tool requests access; it does not grant permission."],
];

const GLOSSARY = [
  ["AgentRun", "The record of an execution: its task, settings, lifecycle, status and results. One Agent can have multiple runs."],
  ["AgentRuntime / AgentHarness", "AgentRuntime is the operator-managed resource describing an approved harness and its supported contracts. AgentHarness is the product/design term, not another resource you need to create. Harness selection does not choose a lifecycle or grant tools."],
  ["Model", "The language model used for reasoning. Agents can use configured external providers or cluster-hosted models. A Model resource describes managed model serving; it is not a prerequisite for every Agent."],
  ["Ensemble / Agent Config / Workflow", "An Ensemble bundles a team. Its Agent Configs are templates used to create Agents. A Workflow describes how the team coordinates, such as delegation or a sequential pipeline. None is required for a standalone Agent."],
  ["Skill / SkillPack", "A skill supplies instructions or domain knowledge. A SkillPack packages those instructions and may include tooling and access requirements. Kubernetes administration and observability packs still depend on their supported tools; they are not automatically available in native Celln."],
  ["Tool / CellnTool / Borrowed tool", "A tool performs an operation. CellnTool describes a reviewed native tool revision and its limits. Borrowing means selecting that revision for a run, subject to grants and host readiness. Tools cannot be added to an already-live native parent."],
  ["Policy / Permission grant / Preview", "Policy sets constraints. A grant bounds allowed access, such as file sizes or HTTPS destinations. A preview explains effective access; it neither issues permission nor proves host readiness. A native cell cannot expand its own authority."],
  ["MCP server", "An integration exposing tools and context through Model Context Protocol. It requires a compatible execution path and approved access. An MCP connection is not automatically a borrowed Celln tool."],
  ["Turn / AgentRunTurn", "A message and its work within a continuing run. In native Celln, the first message is recorded on AgentRun; later messages use AgentRunTurn. Cancelling a turn stops its work once teardown is confirmed; it is different from ending the parent run."],
  ["Parent cell / Sub-cell", "The native parent keeps live context between turns. Each turn uses a disposable child cell, also called a sub-cell. A child is a separate cell—not an AI sub-agent or a VM nested inside the parent."],
  ["Persistent context / Workspace / Lease", "The native parent retains bounded conversation context and logical run-owned files between turns. These are not arbitrary host files or durable Agent memory. A lease limits the parent’s lifetime. Losing the parent loses this live state; saved chat history is not a checkpoint."],
  ["Celln / Cell / Mote", "Celln is the execution plane. A mote is the substrate at rest: a stripped kernel and pilot supervisor. A cell is a live, sealed, tool-loaned mote. Spawn forks a warm mote instead of booting a guest in the hot path."],
  ["Assay / Warden / Pilot", "Infrastructure, not user-selectable skills: assay is the host daemon and distribution layer; warden is the per-cell virtual-machine monitor (one warden, one microVM, one cell); pilot is the in-cell supervisor."],
];

function ConceptsGuide() {
  return (
    <div className="space-y-6 text-sm">
      <section aria-labelledby="concepts-start">
        <h3 id="concepts-start" className="font-semibold">Start with an Agent. Give it work in a Run.</h3>
        <p className="mt-2 text-muted-foreground leading-relaxed">
          The Agent holds the configuration. The Run is the execution.
          A native enduring conversation is one run with multiple turns.
        </p>
        <dl className="mt-4 divide-y divide-border rounded-md border px-4">
          {CHOICES.map(([question, name, detail]) => (
            <div key={name} className="py-3">
              <dt className="font-medium"><span className="text-muted-foreground">{question}</span> {name}</dt>
              <dd className="mt-1 text-muted-foreground leading-relaxed">{detail}</dd>
            </div>
          ))}
        </dl>
      </section>
      <section aria-labelledby="concepts-example" className="rounded-md bg-muted/30 p-4 space-y-2">
        <h3 id="concepts-example" className="font-semibold">Example: keep talking, contain each turn’s work</h3>
        <p className="text-muted-foreground leading-relaxed">
          Choose an approved native Harness, Celln and an enduring lifecycle.
          Select the permitted workspace read/write and HTTPS fetch tools you need.
          A parent cell keeps the live conversation context. Each turn does its
          work in a disposable child cell, returns a result and is cleaned up.
        </p>
        <p className="text-muted-foreground leading-relaxed">
          For a one-off Celln task, choose one-shot instead. Its cell finishes and
          is cleaned up; no persistent parent is needed.
        </p>
      </section>
      <section aria-labelledby="concepts-limits" className="space-y-2">
        <h3 id="concepts-limits" className="font-semibold">What native Celln supports today</h3>
        <ul className="list-disc space-y-2 pl-5 text-muted-foreground leading-relaxed">
          <li>Opt-in Linux amd64/KVM execution with a single owner, one active turn, a small context budget (about 2 KiB), and finite lifetime and usage limits.</li>
          <li>Approved workspace read/write and allowlisted HTTPS fetch. Model credentials stay on the host. Default tool suggestions are not permission grants.</li>
          <li>Live context and workspace files last only as long as the parent. Parent loss is context loss—not automatic recovery. Native checkpoints and pause/resume are not available.</li>
          <li>Existing Kubernetes agents and OCI harnesses remain available. Native Celln does not run arbitrary OCI/Pi/Hermes harnesses, shell, Python, SkillPack sidecars or arbitrary MCP integrations.</li>
        </ul>
      </section>
      <details className="rounded-md border p-4">
        <summary className="cursor-pointer font-semibold focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary">Technical glossary and YAML names</summary>
        <p className="mt-3 text-muted-foreground leading-relaxed">
          AgentRun defaults to Kubernetes when <code>spec.backend</code> is omitted.
          Select Celln with <code>backend: celln</code>. Native continuing runs use
          {" "}<code>executionLifecycle: enduring</code> with an <code>enduring</code> limits block.
          Native selections use <code>cellnSelection.runtimeRef</code> and <code>cellnSelection.toolRefs</code>;
          the ordinary harness path uses <code>runtimeRef</code>. These fields are not a complete installation manifest.
        </p>
        <dl className="mt-4 space-y-4">
          {GLOSSARY.map(([name, detail]) => (
            <div key={name}>
              <dt className="font-medium">{name}</dt>
              <dd className="mt-1 text-muted-foreground leading-relaxed">{detail}</dd>
            </div>
          ))}
        </dl>
      </details>
    </div>
  );
}

function ConceptsDialog({ expanded = false }: { expanded?: boolean }) {
  return (
    <Dialog>
      <DialogTrigger asChild>
        <button aria-label="Concepts" title="Concepts"
          className={expanded
            ? "flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-xs font-medium text-muted-foreground hover:bg-white/5 hover:text-foreground transition-colors"
            : "flex items-center justify-center rounded-md p-1.5 text-muted-foreground hover:bg-white/5 hover:text-foreground transition-colors"}>
          <HelpCircle className="h-4 w-4" aria-hidden="true" />
          {expanded && "Concepts"}
        </button>
      </DialogTrigger>
      <DialogContent className="flex max-h-[85dvh] w-[calc(100%-2rem)] max-w-2xl flex-col overflow-hidden">
        <DialogHeader className="shrink-0 pr-5">
          <DialogTitle>How Sympozium fits together</DialogTitle>
          <DialogDescription>A short guide to agents, runs and the choices that connect them.</DialogDescription>
        </DialogHeader>
        <div className="min-h-0 overflow-y-auto pr-2"><ConceptsGuide /></div>
      </DialogContent>
    </Dialog>
  );
}

export function OntologyModal() { return <ConceptsDialog />; }
export function OntologyModalExpanded() { return <ConceptsDialog expanded />; }
