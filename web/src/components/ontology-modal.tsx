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
      "An Ensemble is a Helm-like bundle that defines a group of Personas, their relationships (delegation, sequential, supervision), shared memory, and the AI provider they use. Activating an Ensemble stamps out Instances, Schedules, and memory for each Persona.",
    relates: "Contains Personas, creates Instances when activated",
  },
  {
    name: "Persona",
    icon: "P",
    short: "A role definition within an Ensemble",
    detail:
      "A Persona defines an agent's identity: its name, system prompt, model, skills, and schedule. Each Persona in an Ensemble becomes a SympoziumInstance when the Ensemble is activated. Personas can have different models and provider overrides.",
    relates: "Lives inside an Ensemble, becomes an Instance",
  },
  {
    name: "Instance",
    icon: "I",
    short: "A configured, ready-to-run agent",
    detail:
      "A SympoziumInstance is a running agent configuration created from a Persona. It holds the agent's model, provider, API key reference, skills, memory settings, and channel bindings. Instances are the launchpad for AgentRuns.",
    relates: "Created from a Persona, runs AgentRuns",
  },
  {
    name: "AgentRun",
    icon: "R",
    short: "A single execution of an agent task",
    detail:
      "An AgentRun is a Job-like resource that executes a task using an Instance's configuration. Each run gets its own ephemeral Pod with least-privilege RBAC, skill sidecars, and network policies. Runs can delegate to other agents or be triggered on a schedule.",
    relates: "Launched from an Instance, runs in a Pod",
  },
  {
    name: "Workflow",
    icon: "W",
    short: "How Personas coordinate within an Ensemble",
    detail:
      "Workflows define relationships between Personas: delegation (one agent asks another for help), sequential pipelines (output flows to next), and supervision (one agent oversees another). Visualised on the interactive canvas.",
    relates: "Defined by Ensemble relationships, visible on the canvas",
  },
  {
    name: "SkillPack",
    icon: "S",
    short: "A reusable set of tools for agents",
    detail:
      "A SkillPack bundles tools that agents can use: Kubernetes operations, GitHub GitOps, SRE observability, memory, llmfit, and more. Each skill runs in its own sidecar container with ephemeral RBAC, auto-provisioned at runtime.",
    relates: "Mounted into Instances as sidecar containers",
  },
  {
    name: "Policy",
    icon: "G",
    short: "Governance rules for agent behaviour",
    detail:
      "A SympoziumPolicy enforces sandbox requirements, resource limits, sub-agent depth, tool gating, network isolation, and model access restrictions. Policies are bound to Instances and validated by an admission webhook.",
    relates: "Bound to Instances via policyRef",
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
            How the core resources relate to each other.
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
                <span className="text-cyan-400">Persona</span>
                <ArrowRight className="h-3 w-3" />
                <span className="text-emerald-400">Instance</span>
                <ArrowRight className="h-3 w-3" />
                <span className="text-amber-400">AgentRun</span>
              </div>
              <p className="text-center mt-1 text-[10px]">
                SkillPacks mount into Instances. Policies govern Instances.
                Workflows connect Personas.
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
            How the core resources relate to each other.
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
                <span className="text-cyan-400">Persona</span>
                <ArrowRight className="h-3 w-3" />
                <span className="text-emerald-400">Instance</span>
                <ArrowRight className="h-3 w-3" />
                <span className="text-amber-400">AgentRun</span>
              </div>
              <p className="text-center mt-1 text-[10px]">
                SkillPacks mount into Instances. Policies govern Instances.
                Workflows connect Personas.
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
