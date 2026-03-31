import { useState } from "react";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { FileCode, Copy, Check } from "lucide-react";
import { cn } from "@/lib/utils";
import { toYaml, type YamlValue } from "@/lib/yaml";
import type { WizardResult } from "@/components/onboarding-wizard";
import type { SympoziumInstance, PersonaPack } from "@/lib/api";

// ── YAML builders ─────────────────────────────────────────────────────────────

/** Build a SympoziumInstance YAML manifest from wizard form state. */
export function instanceYamlFromWizard(result: WizardResult): string {
  const skills = result.skills
    .filter((s) => s !== "memory")
    .map((s) => {
      const ref: Record<string, YamlValue> = { skillPackRef: s };
      if (s === "web-endpoint") {
        const params: Record<string, string> = {};
        if (result.webEndpointRPM && result.webEndpointRPM !== "60")
          params.rate_limit_rpm = result.webEndpointRPM;
        if (result.webEndpointHostname)
          params.hostname = result.webEndpointHostname;
        if (Object.keys(params).length > 0) ref.params = params;
      }
      return ref;
    });

  // Always include the memory skill
  skills.unshift({ skillPackRef: "memory" });

  const channels = result.channels.map((type) => {
    const ch: Record<string, YamlValue> = { type };
    if (result.channelConfigs[type]) {
      ch.configRef = { secret: result.channelConfigs[type] };
    }
    return ch;
  });

  const agentConfig: Record<string, YamlValue> = { model: result.model };
  if (result.baseURL) agentConfig.baseURL = result.baseURL;
  if (result.nodeSelector && Object.keys(result.nodeSelector).length > 0)
    agentConfig.nodeSelector = result.nodeSelector;
  if (result.agentSandboxEnabled)
    agentConfig.agentSandbox = {
      enabled: true,
      runtimeClass: result.agentSandboxRuntimeClass || "gvisor",
    };

  const authRefs: Record<string, string>[] = [];
  if (result.secretName) {
    authRefs.push({ provider: result.provider, secret: result.secretName });
  }

  const obj: Record<string, YamlValue> = {
    apiVersion: "sympozium.ai/v1alpha1",
    kind: "SympoziumInstance",
    metadata: { name: result.name || "<instance-name>" },
    spec: {
      agents: { default: agentConfig },
      skills,
      ...(channels.length > 0 ? { channels } : {}),
      ...(authRefs.length > 0 ? { authRefs } : {}),
      memory: { enabled: true },
    },
  };

  return toYaml(obj);
}

/** Build a PersonaPack activation YAML (the full PersonaPack CR is already in the cluster;
 *  this shows a kubectl-patch equivalent as a full resource). */
export function personaPackYamlFromWizard(
  packName: string,
  result: WizardResult,
  personaCount?: number,
): string {
  const authRefs: Record<string, string>[] = [];
  if (result.secretName) {
    authRefs.push({ provider: result.provider, secret: result.secretName });
  }

  const channelConfigs: Record<string, string> = {};
  for (const [ch, secret] of Object.entries(result.channelConfigs)) {
    if (secret) channelConfigs[ch] = secret;
  }

  const skillParams: Record<string, Record<string, string>> = {};
  if (result.skills.includes("github-gitops") && result.githubRepo) {
    skillParams["github-gitops"] = { repo: result.githubRepo };
  }

  const spec: Record<string, YamlValue> = {
    enabled: true,
    ...(authRefs.length > 0 ? { authRefs } : {}),
    ...(result.baseURL ? { baseURL: result.baseURL } : {}),
    ...(Object.keys(channelConfigs).length > 0 ? { channelConfigs } : {}),
    ...(Object.keys(skillParams).length > 0 ? { skillParams } : {}),
    ...(result.heartbeatInterval ? { heartbeatInterval: result.heartbeatInterval } : {}),
    ...(result.agentSandboxEnabled
      ? {
          agentSandbox: {
            enabled: true,
            runtimeClass: result.agentSandboxRuntimeClass || "gvisor",
          },
        }
      : {}),
    personas: [`# ${personaCount ?? "?"} personas defined in pack (omitted for brevity)`],
  };

  const obj: Record<string, YamlValue> = {
    apiVersion: "sympozium.ai/v1alpha1",
    kind: "PersonaPack",
    metadata: { name: packName },
    spec,
  };

  return toYaml(obj);
}

/** Build a SympoziumInstance YAML from an existing API resource. */
export function instanceYamlFromResource(inst: SympoziumInstance): string {
  const spec: Record<string, YamlValue> = {};

  if (inst.spec.agents) spec.agents = inst.spec.agents;
  if (inst.spec.skills && inst.spec.skills.length > 0) spec.skills = inst.spec.skills;
  if (inst.spec.channels && inst.spec.channels.length > 0) spec.channels = inst.spec.channels;
  if (inst.spec.authRefs && inst.spec.authRefs.length > 0) spec.authRefs = inst.spec.authRefs;
  if (inst.spec.memory) spec.memory = inst.spec.memory;
  if (inst.spec.policyRef) spec.policyRef = inst.spec.policyRef;

  const obj: Record<string, YamlValue> = {
    apiVersion: "sympozium.ai/v1alpha1",
    kind: "SympoziumInstance",
    metadata: {
      name: inst.metadata.name,
      ...(inst.metadata.labels && Object.keys(inst.metadata.labels).length > 0
        ? { labels: inst.metadata.labels }
        : {}),
    },
    spec,
  };

  return toYaml(obj);
}

/** Build a PersonaPack YAML from an existing API resource. */
export function personaPackYamlFromResource(pack: PersonaPack): string {
  const spec: Record<string, YamlValue> = {};

  if (pack.spec.enabled !== undefined) spec.enabled = pack.spec.enabled;
  if (pack.spec.description) spec.description = pack.spec.description;
  if (pack.spec.category) spec.category = pack.spec.category;
  if (pack.spec.version) spec.version = pack.spec.version;
  if (pack.spec.authRefs && pack.spec.authRefs.length > 0) spec.authRefs = pack.spec.authRefs;
  if (pack.spec.channelConfigs && Object.keys(pack.spec.channelConfigs).length > 0)
    spec.channelConfigs = pack.spec.channelConfigs;
  if (pack.spec.skillParams && Object.keys(pack.spec.skillParams).length > 0)
    spec.skillParams = pack.spec.skillParams;
  if (pack.spec.policyRef) spec.policyRef = pack.spec.policyRef;
  if (pack.spec.taskOverride) spec.taskOverride = pack.spec.taskOverride;

  spec.personas = pack.spec.personas.map((p) => {
    const persona: Record<string, YamlValue> = {
      name: p.name,
      systemPrompt: p.systemPrompt,
    };
    if (p.displayName) persona.displayName = p.displayName;
    if (p.model) persona.model = p.model;
    if (p.skills && p.skills.length > 0) persona.skills = p.skills;
    if (p.toolPolicy) persona.toolPolicy = p.toolPolicy;
    if (p.schedule) persona.schedule = p.schedule as unknown as YamlValue;
    if (p.memory) persona.memory = p.memory as unknown as YamlValue;
    if (p.channels && p.channels.length > 0) persona.channels = p.channels;
    if (p.webEndpoint) persona.webEndpoint = p.webEndpoint as unknown as YamlValue;
    if (p.lifecycle) persona.lifecycle = p.lifecycle as unknown as YamlValue;
    return persona;
  });

  const obj: Record<string, YamlValue> = {
    apiVersion: "sympozium.ai/v1alpha1",
    kind: "PersonaPack",
    metadata: {
      name: pack.metadata.name,
      ...(pack.metadata.labels && Object.keys(pack.metadata.labels).length > 0
        ? { labels: pack.metadata.labels }
        : {}),
    },
    spec,
  };

  return toYaml(obj);
}

// ── YAML modal dialog ─────────────────────────────────────────────────────────

interface YamlModalProps {
  open: boolean;
  onClose: () => void;
  yaml: string;
  title?: string;
}

export function YamlModal({ open, onClose, yaml, title }: YamlModalProps) {
  const [copied, setCopied] = useState(false);

  function handleCopy() {
    navigator.clipboard.writeText(yaml).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    });
  }

  return (
    <Dialog open={open} onOpenChange={(v) => !v && onClose()}>
      <DialogContent className="max-w-2xl max-h-[85vh] flex flex-col">
        <DialogHeader>
          <div className="flex items-center justify-between pr-6">
            <DialogTitle className="flex items-center gap-2 text-base">
              <FileCode className="h-4 w-4 text-blue-400" />
              {title || "Resource YAML"}
            </DialogTitle>
            <Button
              variant="outline"
              size="sm"
              className="h-7 gap-1.5 text-xs"
              onClick={handleCopy}
            >
              {copied ? (
                <>
                  <Check className="h-3.5 w-3.5 text-emerald-400" />
                  Copied!
                </>
              ) : (
                <>
                  <Copy className="h-3.5 w-3.5" />
                  Copy
                </>
              )}
            </Button>
          </div>
        </DialogHeader>
        <div className="flex-1 min-h-0 overflow-auto rounded-lg border border-border/50 bg-black/40">
          <pre className="p-4 text-sm font-mono text-blue-200/90 leading-relaxed whitespace-pre">
            {yaml}
          </pre>
        </div>
        <p className="text-[11px] text-muted-foreground">
          Apply with <code className="px-1 py-0.5 rounded bg-muted text-[11px]">kubectl apply -f</code>
        </p>
      </DialogContent>
    </Dialog>
  );
}

// ── Button that opens the YAML modal ──────────────────────────────────────────

interface YamlButtonProps {
  yaml: string;
  title?: string;
  className?: string;
}

/** A button that opens a YAML modal when clicked. Self-contained state. */
export function YamlButton({ yaml, title, className }: YamlButtonProps) {
  const [open, setOpen] = useState(false);

  return (
    <>
      <Button
        variant="ghost"
        size="sm"
        className={cn(
          "h-7 gap-1.5 text-xs text-muted-foreground hover:text-foreground",
          className,
        )}
        onClick={() => setOpen(true)}
      >
        <FileCode className="h-3.5 w-3.5" />
        Show YAML
      </Button>
      <YamlModal
        open={open}
        onClose={() => setOpen(false)}
        yaml={yaml}
        title={title}
      />
    </>
  );
}
