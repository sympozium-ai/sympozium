import { useEffect, useMemo, useState } from "react";
import { useModelList } from "@/hooks/use-model-list";
import { useProviderNodes } from "@/hooks/use-provider-nodes";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from "@/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { ScrollArea } from "@/components/ui/scroll-area";
import {
  Sparkles,
  Power,
  Server,
  ChevronRight,
  ChevronLeft,
  Check,
  Key,
  Bot,
  MessageSquare,
  Loader2,
  Search,
  Wrench,
  Clock,
  Cpu,
} from "lucide-react";
import { cn } from "@/lib/utils";

// ── Shared constants ─────────────────────────────────────────────────────────

export const PROVIDERS = [
  { value: "openai", label: "OpenAI", defaultModel: "gpt-4o", defaultBaseURL: "" },
  { value: "anthropic", label: "Anthropic", defaultModel: "claude-sonnet-4-20250514", defaultBaseURL: "" },
  { value: "azure-openai", label: "Azure OpenAI", defaultModel: "gpt-4o", defaultBaseURL: "" },
  { value: "ollama", label: "Ollama", defaultModel: "llama3", defaultBaseURL: "http://ollama.default.svc:11434/v1" },
  { value: "lm-studio", label: "LM Studio", defaultModel: "", defaultBaseURL: "http://localhost:1234/v1" },
  { value: "bedrock", label: "AWS Bedrock", defaultModel: "anthropic.claude-sonnet-4-20250514-v1:0", defaultBaseURL: "" },
  { value: "custom", label: "Custom", defaultModel: "", defaultBaseURL: "" },
];

const CHANNELS = [
  { value: "discord", label: "Discord" },
  { value: "slack", label: "Slack" },
  { value: "telegram", label: "Telegram" },
  { value: "whatsapp", label: "WhatsApp" },
];

// ── Types ────────────────────────────────────────────────────────────────────

const HEARTBEAT_INTERVALS = [
  { value: "30m", label: "Every 30 minutes" },
  { value: "1h", label: "Every hour" },
  { value: "6h", label: "Every 6 hours" },
  { value: "24h", label: "Once a day" },
];

function heartbeatOptions(mode: "instance" | "persona") {
  return [
    { value: "", label: mode === "persona" ? "Pack default" : "No heartbeat" },
    ...HEARTBEAT_INTERVALS,
  ];
}

export interface WizardResult {
  name: string;
  provider: string;
  apiKey: string;
  secretName: string;
  awsRegion: string;
  awsAccessKeyId: string;
  awsSecretAccessKey: string;
  awsSessionToken: string;
  model: string;
  baseURL: string;
  skills: string[];
  channels: string[];
  channelConfigs: Record<string, string>;
  heartbeatInterval: string;
  /** Web endpoint rate limit (requests per minute) when web-endpoint skill is selected */
  webEndpointRPM?: string;
  /** Custom hostname for web endpoint HTTPRoute */
  webEndpointHostname?: string;
  /** GitHub repository (owner/repo) when github-gitops skill is selected */
  githubRepo?: string;
  /** GitHub personal access token for the github-gitops skill */
  githubToken?: string;
  /** Team instructions propagated into each instance's memory */
  githubTeamInstructions?: string;
  /** Node selector for pinning agent pods to specific nodes */
  nodeSelector?: Record<string, string>;
}

interface OnboardingWizardProps {
  open: boolean;
  onClose: () => void;
  /** "instance" shows a Name step first; "persona" skips it */
  mode: "instance" | "persona";
  /** Display name shown in the dialog title */
  targetName?: string;
  /** Number of personas in the pack (persona mode only) */
  personaCount?: number;
  /** Available SkillPacks to choose from */
  availableSkills?: string[];
  /** Pre-fill form values */
  defaults?: Partial<WizardResult>;
  /** Called when the user clicks Activate / Create */
  onComplete: (result: WizardResult) => void;
  isPending: boolean;
}

// ── Steps ────────────────────────────────────────────────────────────────────

type WizardStep = "name" | "provider" | "apikey" | "model" | "skills" | "heartbeat" | "channels" | "confirm" | "channelAction";

function stepsForMode(mode: "instance" | "persona"): WizardStep[] {
  if (mode === "instance") {
    return ["name", "provider", "apikey", "model", "skills", "heartbeat", "channels", "confirm", "channelAction"];
  }
  return ["provider", "apikey", "model", "skills", "heartbeat", "channels", "confirm", "channelAction"];
}

// ── Step indicator ───────────────────────────────────────────────────────────

function StepIndicator({ steps, current }: { steps: WizardStep[]; current: WizardStep }) {
  const labels: Record<WizardStep, string> = {
    name: "Name",
    provider: "Provider",
    apikey: "Auth",
    model: "Model",
    skills: "Skills",
    heartbeat: "Heartbeat",
    channels: "Channels",
    confirm: "Confirm",
    channelAction: "Finalize",
  };
  const icons: Record<WizardStep, React.ReactNode> = {
    name: <Server className="h-3.5 w-3.5" />,
    provider: <Bot className="h-3.5 w-3.5" />,
    apikey: <Key className="h-3.5 w-3.5" />,
    model: <Sparkles className="h-3.5 w-3.5" />,
    skills: <Wrench className="h-3.5 w-3.5" />,
    heartbeat: <Clock className="h-3.5 w-3.5" />,
    channels: <MessageSquare className="h-3.5 w-3.5" />,
    confirm: <Check className="h-3.5 w-3.5" />,
    channelAction: <Key className="h-3.5 w-3.5" />,
  };
  const idx = steps.indexOf(current);

  return (
    <div className="flex flex-wrap items-center justify-center gap-1 mb-6">
      {steps.map((step, i) => (
        <div key={step} className="flex items-center gap-1">
          <div
            className={cn(
              "flex items-center gap-1 rounded-full px-2 py-1 text-[11px] font-medium transition-colors",
              i < idx
                ? "bg-blue-500/20 text-blue-400"
                : i === idx
                ? "bg-blue-500 text-white"
                : "bg-muted text-muted-foreground"
            )}
          >
            {i < idx ? <Check className="h-3 w-3" /> : icons[step]}
            <span>{labels[step]}</span>
          </div>
          {i < steps.length - 1 && (
            <ChevronRight className="h-3 w-3 text-muted-foreground" />
          )}
        </div>
      ))}
    </div>
  );
}

// ── Model selector with search ───────────────────────────────────────────────

function ModelSelector({
  provider,
  apiKey,
  baseURL,
  value,
  onChange,
  bedrockCredentials,
}: {
  provider: string;
  apiKey: string;
  baseURL?: string;
  value: string;
  onChange: (v: string) => void;
  bedrockCredentials?: import("@/hooks/use-model-list").BedrockCredentials;
}) {
  const { models, isLoading, isLive } = useModelList(provider, apiKey, baseURL, bedrockCredentials);
  const [search, setSearch] = useState("");

  const filtered = models.filter((m) =>
    m.toLowerCase().includes(search.toLowerCase())
  );

  return (
    <div className="space-y-2">
      <Label>Model</Label>

      {/* Search input */}
      <div className="relative">
        <Search className="absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
        <Input
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          placeholder="Search models…"
          className="h-8 pl-8 text-sm"
        />
      </div>

      {isLoading ? (
        <div className="flex items-center gap-2 py-4 text-xs text-muted-foreground justify-center">
          <Loader2 className="h-3.5 w-3.5 animate-spin" />
          Fetching models from {provider}…
        </div>
      ) : (
        <ScrollArea className="h-44 rounded-md border border-border/50">
          <div className="p-1 space-y-0.5">
            {filtered.length === 0 ? (
              <p className="py-3 text-center text-xs text-muted-foreground">
                No models match "{search}"
              </p>
            ) : (
              filtered.map((m) => (
                <button
                  key={m}
                  type="button"
                  onClick={() => onChange(m)}
                  className={cn(
                    "flex w-full items-center gap-2 rounded-md px-2.5 py-1.5 text-xs font-mono transition-colors text-left",
                    m === value
                      ? "bg-blue-500/15 text-blue-400 border border-blue-500/30"
                      : "text-foreground hover:bg-white/5 border border-transparent"
                  )}
                >
                  {m === value && <Check className="h-3 w-3 shrink-0" />}
                  <span className="truncate">{m}</span>
                </button>
              ))
            )}
          </div>
        </ScrollArea>
      )}

      {/* Custom input */}
      <div className="space-y-1">
        <Label className="text-xs text-muted-foreground">
          Or enter a custom model name
        </Label>
        <Input
          value={value}
          onChange={(e) => onChange(e.target.value)}
          placeholder="gpt-4o"
          className="h-8 text-sm font-mono"
        />
      </div>

      {isLive && (
        <p className="text-[10px] text-emerald-400/70">
          ✓ Live models fetched from {provider} API
        </p>
      )}
    </div>
  );
}

// ── Main wizard component ────────────────────────────────────────────────────

export function OnboardingWizard({
  open,
  onClose,
  mode,
  targetName,
  personaCount,
  availableSkills = [],
  defaults,
  onComplete,
  isPending,
}: OnboardingWizardProps) {
  const steps = stepsForMode(mode);
  const [step, setStep] = useState<WizardStep>(steps[0]);
  const [form, setForm] = useState<WizardResult>({
    name: defaults?.name || "",
    provider: defaults?.provider || "",
    apiKey: defaults?.apiKey || "",
    secretName: defaults?.secretName || "",
    model: defaults?.model || "",
    baseURL: defaults?.baseURL || "",
    skills: Array.from(new Set([...(defaults?.skills || []), "memory"])),
    channels: defaults?.channels || Object.keys(defaults?.channelConfigs || {}),
    channelConfigs: defaults?.channelConfigs || {},
    heartbeatInterval: defaults?.heartbeatInterval || "",
    webEndpointRPM: defaults?.webEndpointRPM || "60",
    webEndpointHostname: defaults?.webEndpointHostname || "",
    githubRepo: defaults?.githubRepo || "",
    githubToken: defaults?.githubToken || "",
    githubTeamInstructions: defaults?.githubTeamInstructions || "",
    nodeSelector: defaults?.nodeSelector,
    awsRegion: defaults?.awsRegion || "",
    awsAccessKeyId: defaults?.awsAccessKeyId || "",
    awsSecretAccessKey: defaults?.awsSecretAccessKey || "",
    awsSessionToken: defaults?.awsSessionToken || "",
  });
  const [inferenceMode, setInferenceMode] = useState<"workload" | "node">("workload");
  const [channelActionIdx, setChannelActionIdx] = useState(0);

  const isLocalProvider = form.provider === "ollama" || form.provider === "lm-studio" || form.provider === "custom";
  const { data: providerNodes, isLoading: nodesLoading } = useProviderNodes(
    isLocalProvider && inferenceMode === "node"
  );

  const stepIdx = steps.indexOf(step);

  const canNext = (() => {
    switch (step) {
      case "name":
        return !!form.name.trim();
      case "provider":
        return !!form.provider;
      case "apikey":
        if (form.provider === "ollama" || form.provider === "lm-studio") return true;
        if (form.provider === "bedrock") return !!form.secretName || !!form.awsRegion;
        return !!form.secretName || !!form.apiKey;
      case "model":
        return !!form.model;
      case "skills":
        return true;
      case "channelAction":
        return true;
      default:
        return true;
    }
  })();

  const actionChannels = useMemo(
    () => form.channels.filter((c) => c !== "whatsapp"),
    [form.channels]
  );
  const hasActionChannels = actionChannels.length > 0;

  function completeWithDefaults() {
    // Apply default baseURL for local providers if the user left it empty.
    const result = { ...form };
    if (!result.baseURL) {
      const prov = PROVIDERS.find((p) => p.value === result.provider);
      if (prov?.defaultBaseURL) {
        result.baseURL = prov.defaultBaseURL;
      }
    }
    onComplete(result);
  }

  function next() {
    if (step === "confirm") {
      if (hasActionChannels) {
        setChannelActionIdx(0);
        setStep("channelAction");
      } else {
        completeWithDefaults();
      }
      return;
    }
    if (step === "channelAction") {
      if (channelActionIdx < actionChannels.length - 1) {
        setChannelActionIdx(channelActionIdx + 1);
      } else {
        completeWithDefaults();
      }
      return;
    }
    if (stepIdx < steps.length - 1) setStep(steps[stepIdx + 1]);
  }
  function prev() {
    if (stepIdx > 0) setStep(steps[stepIdx - 1]);
  }

  function handleClose() {
    setStep(steps[0]);
    setChannelActionIdx(0);
    onClose();
  }

  // Reset form when defaults change (new wizard opened)
  function resetWith(d: Partial<WizardResult>) {
    setForm({
      name: d.name || "",
      provider: d.provider || "",
      apiKey: d.apiKey || "",
      secretName: d.secretName || "",
      model: d.model || "",
      baseURL: d.baseURL || "",
      skills: d.skills || [],
      channels: d.channels || Object.keys(d.channelConfigs || {}),
      channelConfigs: d.channelConfigs || {},
      heartbeatInterval: d.heartbeatInterval || "",
      webEndpointRPM: d.webEndpointRPM || "60",
      webEndpointHostname: d.webEndpointHostname || "",
      githubRepo: d.githubRepo || "",
      githubToken: d.githubToken || "",
      githubTeamInstructions: d.githubTeamInstructions || "",
      nodeSelector: d.nodeSelector,
      awsRegion: d.awsRegion || "",
      awsAccessKeyId: d.awsAccessKeyId || "",
      awsSecretAccessKey: d.awsSecretAccessKey || "",
      awsSessionToken: d.awsSessionToken || "",
    });
    setStep(steps[0]);
    setChannelActionIdx(0);
    setInferenceMode("workload");
  }

  const defaultsKey = JSON.stringify(defaults || {});
  useEffect(() => {
    if (open) {
      resetWith(defaults || {});
    }
  }, [open, defaultsKey]);

  const titleIcon =
    mode === "instance" ? (
      <Server className="h-5 w-5 text-blue-400" />
    ) : (
      <Sparkles className="h-5 w-5 text-blue-400" />
    );
  const titleText =
    mode === "instance"
      ? "Create Instance"
      : `Enable ${targetName || "Pack"}`;
  const completeLabel = mode === "instance" ? "Create" : "Activate";
  const completeIcon =
    mode === "instance" ? (
      <Server className="h-4 w-4" />
    ) : (
      <Power className="h-4 w-4" />
    );

  return (
    <Dialog open={open} onOpenChange={(v) => !v && handleClose()}>
      <DialogContent className="sm:max-w-md overflow-hidden">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            {titleIcon}
            {mode === "persona" ? (
              <>
                Enable{" "}
                <span className="font-mono text-blue-400">{targetName}</span>
              </>
            ) : (
              "Create Instance"
            )}
          </DialogTitle>
          <DialogDescription>
            {mode === "instance"
              ? "Configure a new SympoziumInstance with provider, model, and skills."
              : "Configure provider, model, skills, and channels to activate this persona pack."}
          </DialogDescription>
        </DialogHeader>

        <StepIndicator steps={steps.filter((s) => s !== "channelAction")} current={step === "channelAction" ? "confirm" : step} />

        {/* ── Name step (instance only) ─────────────────────────────── */}
        {step === "name" && (
          <div className="space-y-4">
            <div className="space-y-2">
              <Label>Instance Name</Label>
              <Input
                value={form.name}
                onChange={(e) => setForm({ ...form, name: e.target.value })}
                placeholder="my-agent"
                autoFocus
              />
            </div>
          </div>
        )}

        {/* ── Provider step ─────────────────────────────────────────── */}
        {step === "provider" && (
          <div className="space-y-4">
            <div className="space-y-2">
              <Label>AI Provider</Label>
              <Select
                value={form.provider}
                onValueChange={(v) => {
                  const prov = PROVIDERS.find((p) => p.value === v);
                  setForm({
                    ...form,
                    provider: v,
                    model: form.model || prov?.defaultModel || "",
                    baseURL: prov?.defaultBaseURL || "",
                  });
                }}
              >
                <SelectTrigger>
                  <SelectValue placeholder="Select a provider…" />
                </SelectTrigger>
                <SelectContent>
                  {PROVIDERS.map((p) => (
                    <SelectItem key={p.value} value={p.value}>
                      {p.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            {/* Inference mode toggle for local providers */}
            {isLocalProvider && (
              <div className="space-y-2">
                <Label>Inference Source</Label>
                <div className="flex gap-2">
                  <button
                    type="button"
                    onClick={() => {
                      setInferenceMode("workload");
                      setForm({ ...form, nodeSelector: undefined });
                    }}
                    className={cn(
                      "flex-1 flex items-center justify-center gap-1.5 rounded-md border px-3 py-2 text-xs transition-colors",
                      inferenceMode === "workload"
                        ? "border-blue-500/40 bg-blue-500/15 text-blue-300"
                        : "border-border/50 hover:bg-white/5"
                    )}
                  >
                    <Server className="h-3.5 w-3.5" /> In-cluster service
                  </button>
                  <button
                    type="button"
                    onClick={() => setInferenceMode("node")}
                    className={cn(
                      "flex-1 flex items-center justify-center gap-1.5 rounded-md border px-3 py-2 text-xs transition-colors",
                      inferenceMode === "node"
                        ? "border-blue-500/40 bg-blue-500/15 text-blue-300"
                        : "border-border/50 hover:bg-white/5"
                    )}
                  >
                    <Cpu className="h-3.5 w-3.5" /> Installed on node
                  </button>
                </div>
              </div>
            )}

            {/* In-cluster service: manual Base URL input */}
            {(form.provider === "azure-openai" || (isLocalProvider && inferenceMode === "workload")) && (
              <div className="space-y-2">
                <Label>Base URL</Label>
                <Input
                  value={form.baseURL}
                  onChange={(e) => setForm({ ...form, baseURL: e.target.value })}
                  placeholder={
                    form.provider === "ollama"
                      ? "http://ollama.default.svc:11434/v1"
                      : form.provider === "lm-studio"
                      ? "http://localhost:1234/v1"
                      : "https://your-endpoint.openai.azure.com/v1"
                  }
                />
              </div>
            )}

            {/* Node-based: discover and select a node */}
            {isLocalProvider && inferenceMode === "node" && (
              <div className="space-y-2">
                <Label>Select Node</Label>
                {nodesLoading ? (
                  <div className="flex items-center gap-2 py-4 text-xs text-muted-foreground justify-center">
                    <Loader2 className="h-3.5 w-3.5 animate-spin" />
                    Discovering nodes...
                  </div>
                ) : !providerNodes || providerNodes.filter((n) =>
                    form.provider === "custom" || n.providers.some((p) => p.name === form.provider)
                  ).length === 0 ? (
                  <div className="rounded-md border border-border/50 bg-muted/20 px-3 py-3 text-xs text-muted-foreground">
                    {providerNodes && providerNodes.length > 0
                      ? `No nodes with ${form.provider} detected. Found other providers on ${providerNodes.length} node${providerNodes.length === 1 ? "" : "s"}.`
                      : "No nodes with inference providers detected. Is the node-probe DaemonSet enabled?"}
                  </div>
                ) : (
                  <ScrollArea className="h-40 rounded-md border border-border/50">
                    <div className="p-1 space-y-0.5">
                      {providerNodes
                        .filter((node) =>
                          form.provider === "custom" || node.providers.some((p) => p.name === form.provider)
                        )
                        .map((node) => {
                        const isSelected = form.nodeSelector?.["kubernetes.io/hostname"] === node.nodeName;
                        const nodeProviders = node.providers
                          .filter((p) => !form.provider || form.provider === "custom" || p.name === form.provider)
                          .map((p) => p.name);
                        const nodeModels = node.providers
                          .filter((p) => !form.provider || form.provider === "custom" || p.name === form.provider)
                          .flatMap((p) => p.models);
                        const providerInfo = node.providers.find((p) => p.name === form.provider) || node.providers[0];

                        return (
                          <button
                            key={node.nodeName}
                            type="button"
                            onClick={() => {
                              if (providerInfo) {
                                // Use the node-probe reverse proxy when available,
                                // so the cluster can reach host-installed providers.
                                const base = providerInfo.proxyPort
                                  ? `http://${node.nodeIP}:${providerInfo.proxyPort}/proxy/${providerInfo.name}/v1`
                                  : `http://${node.nodeIP}:${providerInfo.port}/v1`;
                                setForm({
                                  ...form,
                                  baseURL: base,
                                  nodeSelector: { "kubernetes.io/hostname": node.nodeName },
                                });
                              }
                            }}
                            className={cn(
                              "flex w-full items-start gap-2 rounded-md px-2.5 py-2 text-left text-xs transition-colors",
                              isSelected
                                ? "bg-blue-500/15 text-blue-400 border border-blue-500/30"
                                : "text-foreground hover:bg-white/5 border border-transparent"
                            )}
                          >
                            <Cpu className="h-3.5 w-3.5 mt-0.5 shrink-0" />
                            <div className="min-w-0">
                              <div className="font-mono truncate">{node.nodeName}</div>
                              <div className="text-[10px] text-muted-foreground">
                                {node.nodeIP} &middot; {nodeProviders.join(", ")}
                                {nodeModels.length > 0 && ` &middot; ${nodeModels.length} model${nodeModels.length === 1 ? "" : "s"}`}
                              </div>
                            </div>
                            {isSelected && <Check className="h-3 w-3 shrink-0 mt-0.5 ml-auto" />}
                          </button>
                        );
                      })}
                    </div>
                  </ScrollArea>
                )}
              </div>
            )}
          </div>
        )}

        {/* ── Auth step ─────────────────────────────────────────────── */}
        {step === "apikey" && (
          <ScrollArea className="max-h-[60vh]">
          <div className="space-y-4">
            {form.provider !== "bedrock" &&
              form.provider !== "ollama" &&
              form.provider !== "lm-studio" && (
              <div className="space-y-2">
                <Label>API Key</Label>
                <Input
                  type="password"
                  value={form.apiKey}
                  onChange={(e) =>
                    setForm({ ...form, apiKey: e.target.value })
                  }
                  placeholder="sk-…"
                  autoComplete="off"
                />
                <p className="text-xs text-muted-foreground">
                  A Kubernetes Secret will be created automatically from this
                  key. Also used to fetch available models.
                </p>
              </div>
            )}
            {form.provider === "bedrock" && (
              <>
                <div className="space-y-2">
                  <Label>AWS Region</Label>
                  <Input
                    value={form.awsRegion}
                    onChange={(e) =>
                      setForm({ ...form, awsRegion: e.target.value })
                    }
                    placeholder="us-east-1"
                  />
                </div>
                <div className="space-y-2">
                  <Label>AWS Access Key ID</Label>
                  <Input
                    value={form.awsAccessKeyId}
                    onChange={(e) =>
                      setForm({ ...form, awsAccessKeyId: e.target.value })
                    }
                    placeholder="AKIA…"
                    autoComplete="off"
                  />
                </div>
                <div className="space-y-2">
                  <Label>AWS Secret Access Key</Label>
                  <Input
                    type="password"
                    value={form.awsSecretAccessKey}
                    onChange={(e) =>
                      setForm({ ...form, awsSecretAccessKey: e.target.value })
                    }
                    placeholder="wJalr…"
                    autoComplete="off"
                  />
                </div>
                <div className="space-y-2">
                  <Label>AWS Session Token <span className="text-muted-foreground font-normal">(optional)</span></Label>
                  <Input
                    type="password"
                    value={form.awsSessionToken}
                    onChange={(e) =>
                      setForm({ ...form, awsSessionToken: e.target.value })
                    }
                    placeholder="For temporary credentials"
                    autoComplete="off"
                  />
                </div>
                <p className="text-xs text-muted-foreground">
                  A Kubernetes Secret with your AWS credentials will be created
                  automatically. For EKS with IRSA, provide only the region and
                  use a pre-existing secret.
                </p>
              </>
            )}
            <div className="space-y-2">
              <Label>K8s Secret Name <span className="text-muted-foreground font-normal">(optional if credentials provided)</span></Label>
              <Input
                value={form.secretName}
                onChange={(e) =>
                  setForm({ ...form, secretName: e.target.value })
                }
                placeholder="my-provider-api-key"
              />
              <p className="text-xs text-muted-foreground">
                Use an existing Kubernetes Secret, or leave blank to
                auto-create one from the credentials above.
              </p>
            </div>
          </div>
          </ScrollArea>
        )}

        {/* ── Model step ────────────────────────────────────────────── */}
        {step === "model" && (
          <div className="space-y-2">
            <ModelSelector
              provider={form.provider}
              apiKey={form.apiKey}
              baseURL={form.baseURL}
              value={form.model}
              onChange={(v) => setForm({ ...form, model: v })}
              bedrockCredentials={
                form.provider === "bedrock" && form.awsAccessKeyId
                  ? {
                      region: form.awsRegion || "us-east-1",
                      accessKeyId: form.awsAccessKeyId,
                      secretAccessKey: form.awsSecretAccessKey,
                      sessionToken: form.awsSessionToken || undefined,
                    }
                  : undefined
              }
            />
            {mode === "persona" && personaCount !== undefined && (
              <p className="text-xs text-muted-foreground">
                Applied to all{" "}
                <span className="text-blue-400">{personaCount}</span>{" "}
                personas.
              </p>
            )}
          </div>
        )}

        {/* ── Skills step ───────────────────────────────────────────── */}
        {step === "skills" && (
          <ScrollArea className="max-h-[60vh]">
          <div className="space-y-3">
            <p className="text-sm text-muted-foreground">
              Select SkillPacks to attach.
            </p>
            {availableSkills.length === 0 ? (
              <p className="rounded-md border border-border/50 bg-muted/20 px-3 py-2 text-xs text-muted-foreground">
                No SkillPacks found in cluster.
              </p>
            ) : (
              <ScrollArea className="h-52 rounded-md border border-border/50">
                <div className="p-1 space-y-1">
                  {[...availableSkills].sort((a, b) => a.localeCompare(b)).map((skill) => {
                    const selected = form.skills.includes(skill);
                    const locked = skill === "memory";
                    return (
                      <button
                        key={skill}
                        type="button"
                        disabled={locked}
                        onClick={() => {
                          if (locked) return;
                          const next = selected
                            ? form.skills.filter((s) => s !== skill)
                            : [...form.skills, skill];
                          setForm({ ...form, skills: next });
                        }}
                        className={cn(
                          "flex w-full items-center justify-between rounded-md border px-2.5 py-2 text-left text-xs transition-colors",
                          locked
                            ? "border-blue-500/40 bg-blue-500/15 text-blue-300 opacity-70 cursor-not-allowed"
                            : selected
                              ? "border-blue-500/40 bg-blue-500/15 text-blue-300"
                              : "border-transparent hover:border-border/60 hover:bg-white/5"
                        )}
                      >
                        <span className="font-mono">{skill}</span>
                        <span className="text-[10px]">{locked ? "Required" : selected ? "Selected" : "Select"}</span>
                      </button>
                    );
                  })}
                </div>
              </ScrollArea>
            )}
            <p className="text-xs text-muted-foreground">
              {form.skills.length > 0
                ? `${form.skills.length} skill${form.skills.length === 1 ? "" : "s"} selected`
                : "No skills selected"}
            </p>

            {/* Web endpoint inline config */}
            {form.skills.includes("web-endpoint") && (
              <div className="rounded-md border border-blue-500/20 bg-blue-500/5 p-3 space-y-2">
                <p className="text-xs font-medium text-blue-400">Web Endpoint Config</p>
                <div className="space-y-1">
                  <Label className="text-xs">Rate Limit (req/min)</Label>
                  <Input
                    type="number"
                    value={form.webEndpointRPM || "60"}
                    onChange={(e) => setForm({ ...form, webEndpointRPM: e.target.value })}
                    className="h-7 text-xs"
                  />
                </div>
                <div className="space-y-1">
                  <Label className="text-xs">Custom Hostname <span className="text-muted-foreground">(optional)</span></Label>
                  <Input
                    value={form.webEndpointHostname || ""}
                    onChange={(e) => setForm({ ...form, webEndpointHostname: e.target.value })}
                    placeholder="auto from gateway"
                    className="h-7 text-xs"
                  />
                </div>
              </div>
            )}

            {/* GitHub GitOps inline config */}
            {form.skills.includes("github-gitops") && (
              <div className="rounded-md border border-blue-500/20 bg-blue-500/5 p-3 space-y-2">
                <p className="text-xs font-medium text-blue-400">GitHub GitOps Config</p>
                <div className="space-y-1">
                  <Label className="text-xs">Repository</Label>
                  <Input
                    value={form.githubRepo || ""}
                    onChange={(e) => setForm({ ...form, githubRepo: e.target.value })}
                    placeholder="owner/repo"
                    className="h-7 text-xs font-mono"
                  />
                  <p className="text-[10px] text-muted-foreground">
                    The GitHub repository this team will target for issues and PRs.
                  </p>
                </div>
                <div className="space-y-1">
                  <Label className="text-xs">Personal Access Token</Label>
                  <Input
                    type="password"
                    value={form.githubToken || ""}
                    onChange={(e) => setForm({ ...form, githubToken: e.target.value })}
                    placeholder="github_pat_..."
                    autoComplete="off"
                    className="h-7 text-xs font-mono"
                  />
                  <p className="text-[10px] text-muted-foreground">
                    A token with repo access. Stored as a cluster secret.
                  </p>
                </div>
                <div className="space-y-1">
                  <Label className="text-xs">Team Instructions <span className="text-muted-foreground">(optional)</span></Label>
                  <Textarea
                    value={form.githubTeamInstructions || ""}
                    onChange={(e) => setForm({ ...form, githubTeamInstructions: e.target.value })}
                    placeholder="Describe the project goals, coding standards, architecture decisions, or any context each instance should know…"
                    rows={4}
                    className="text-xs resize-y"
                  />
                  <p className="text-[10px] text-muted-foreground">
                    Shared instructions propagated into every instance's memory. Each instance will use these alongside its role.
                  </p>
                </div>
              </div>
            )}
          </div>
          </ScrollArea>
        )}

        {/* ── Heartbeat step ──────────────────────────────────────── */}
        {step === "heartbeat" && (
          <div className="space-y-4">
            <p className="text-sm text-muted-foreground">
              {mode === "persona"
                ? "How often should personas wake up? This overrides each persona's default schedule."
                : "How often should this instance wake up on a heartbeat schedule?"}
            </p>
            {heartbeatOptions(mode).map((opt) => (
              <button
                key={opt.value}
                type="button"
                onClick={() => setForm({ ...form, heartbeatInterval: opt.value })}
                className={cn(
                  "flex w-full items-center justify-between rounded-md border px-3 py-2 text-left text-sm transition-colors",
                  form.heartbeatInterval === opt.value
                    ? "border-blue-500/40 bg-blue-500/15 text-blue-300"
                    : "border-border/50 hover:bg-white/5"
                )}
              >
                <span>{opt.label}</span>
                <span className="text-xs">{form.heartbeatInterval === opt.value ? "Selected" : "Select"}</span>
              </button>
            ))}
          </div>
        )}

        {/* ── Channels step ─────────────────────────────────────────── */}
        {step === "channels" && (
          <div className="space-y-4">
            <p className="text-sm text-muted-foreground">
              Select channels to bind. Channel-specific setup happens after confirmation.
            </p>
            {CHANNELS.map((ch) => (
              <button
                key={ch.value}
                type="button"
                onClick={() => {
                  const selected = form.channels.includes(ch.value);
                  const nextChannels = selected
                    ? form.channels.filter((c) => c !== ch.value)
                    : [...form.channels, ch.value];
                  const nextConfigs = { ...form.channelConfigs };
                  if (selected) {
                    delete nextConfigs[ch.value];
                  }
                  setForm({ ...form, channels: nextChannels, channelConfigs: nextConfigs });
                }}
                className={cn(
                  "flex w-full items-center justify-between rounded-md border px-3 py-2 text-left text-sm transition-colors",
                  form.channels.includes(ch.value)
                    ? "border-blue-500/40 bg-blue-500/15 text-blue-300"
                    : "border-border/50 hover:bg-white/5"
                )}
              >
                <span>{ch.label}</span>
                <span className="text-xs">{form.channels.includes(ch.value) ? "Selected" : "Select"}</span>
              </button>
            ))}
            {form.channels.includes("whatsapp") && (
              <p className="text-xs text-muted-foreground">
                WhatsApp setup will open a QR pairing modal after creation/activation.
              </p>
            )}
          </div>
        )}

        {/* ── Confirm step ──────────────────────────────────────────── */}
        {step === "confirm" && (
          <div className="space-y-3">
            <div className="rounded-lg border border-blue-500/20 bg-blue-500/5 p-4 space-y-2 text-sm">
              {mode === "instance" && (
                <div className="flex justify-between">
                  <span className="text-muted-foreground">Name</span>
                  <span className="font-mono text-blue-400">{form.name}</span>
                </div>
              )}
              {mode === "persona" && targetName && (
                <div className="flex justify-between">
                  <span className="text-muted-foreground">Pack</span>
                  <span className="font-mono text-blue-400">{targetName}</span>
                </div>
              )}
              <div className="flex justify-between">
                <span className="text-muted-foreground">Provider</span>
                <span>{form.provider}</span>
              </div>
              <div className="flex justify-between">
                <span className="text-muted-foreground">Secret</span>
                <span className="font-mono">{form.secretName || "—"}</span>
              </div>
              <div className="flex justify-between">
                <span className="text-muted-foreground">Model</span>
                <span className="font-mono">{form.model}</span>
              </div>
              <div className="flex justify-between gap-4">
                <span className="text-muted-foreground">Skills</span>
                <span className="font-mono text-right">
                  {form.skills.length > 0 ? form.skills.join(", ") : "—"}
                </span>
              </div>
              {form.baseURL && (
                <div className="flex justify-between">
                  <span className="text-muted-foreground">Base URL</span>
                  <span className="font-mono text-xs truncate max-w-[200px]">
                    {form.baseURL}
                  </span>
                </div>
              )}
              {form.nodeSelector?.["kubernetes.io/hostname"] && (
                <div className="flex justify-between">
                  <span className="text-muted-foreground">Node</span>
                  <span className="font-mono text-xs">
                    {form.nodeSelector["kubernetes.io/hostname"]}
                  </span>
                </div>
              )}
              {mode === "persona" && personaCount !== undefined && (
                <div className="flex justify-between">
                  <span className="text-muted-foreground">Personas</span>
                  <span>{personaCount}</span>
                </div>
              )}
              {form.heartbeatInterval && (
                <div className="flex justify-between">
                  <span className="text-muted-foreground">Heartbeat</span>
                  <span>
                    {HEARTBEAT_INTERVALS.find((o) => o.value === form.heartbeatInterval)?.label || form.heartbeatInterval}
                  </span>
                </div>
              )}
              {form.channels.length > 0 && (
                <div className="flex justify-between">
                  <span className="text-muted-foreground">Channels</span>
                  <span>{form.channels.join(", ")}</span>
                </div>
              )}
              {form.skills.includes("web-endpoint") && (
                <div className="flex justify-between">
                  <span className="text-muted-foreground">Web Endpoint</span>
                  <span className="text-xs">
                    {form.webEndpointRPM || "60"} rpm
                    {form.webEndpointHostname ? `, ${form.webEndpointHostname}` : ""}
                  </span>
                </div>
              )}
              {form.skills.includes("github-gitops") && form.githubRepo && (
                <div className="flex justify-between">
                  <span className="text-muted-foreground">GitHub Repo</span>
                  <span className="font-mono text-xs">{form.githubRepo}</span>
                </div>
              )}
              {form.skills.includes("github-gitops") && form.githubToken && (
                <div className="flex justify-between">
                  <span className="text-muted-foreground">GitHub Token</span>
                  <span className="text-xs text-emerald-400">provided</span>
                </div>
              )}
              {form.skills.includes("github-gitops") && form.githubTeamInstructions && (
                <div className="flex justify-between">
                  <span className="text-muted-foreground">Team Instructions</span>
                  <span className="text-xs text-emerald-400">provided</span>
                </div>
              )}
            </div>
            <p className="text-xs text-muted-foreground">
              {mode === "instance"
                ? "A new SympoziumInstance will be created with this configuration."
                : "The controller will stamp out Instances, Schedules, and ConfigMaps for each persona."}
            </p>
          </div>
        )}

        {/* ── Channel action step (post-confirm) ───────────────────── */}
        {step === "channelAction" && (
          <div className="space-y-4">
            {actionChannels.length === 0 ? (
              <p className="text-sm text-muted-foreground">
                No additional channel setup required.
              </p>
            ) : (
              <>
                <p className="text-sm text-muted-foreground">
                  Channel-specific setup ({channelActionIdx + 1}/{actionChannels.length})
                </p>
                <div className="space-y-2">
                  <Label>
                    {actionChannels[channelActionIdx]} Secret Name
                  </Label>
                  <Input
                    value={form.channelConfigs[actionChannels[channelActionIdx]] || ""}
                    onChange={(e) => {
                      const ch = actionChannels[channelActionIdx];
                      const configs = { ...form.channelConfigs };
                      if (e.target.value.trim()) {
                        configs[ch] = e.target.value.trim();
                      } else {
                        delete configs[ch];
                      }
                      setForm({ ...form, channelConfigs: configs });
                    }}
                    placeholder={`${mode === "persona" ? targetName : form.name}-${actionChannels[channelActionIdx]}-secret`}
                    className="h-8 text-sm font-mono"
                    autoFocus
                  />
                  <p className="text-xs text-muted-foreground">
                    Use an existing secret that contains the channel token.
                  </p>
                </div>
              </>
            )}
          </div>
        )}

        {/* ── Navigation ────────────────────────────────────────────── */}
        <div className="flex items-center justify-between pt-2">
          <Button
            variant="ghost"
            size="sm"
            onClick={prev}
            disabled={stepIdx === 0}
            className="gap-1"
          >
            <ChevronLeft className="h-4 w-4" /> Back
          </Button>

          {step === "confirm" || step === "channelAction" ? (
            <Button
              size="sm"
              className="gap-1 bg-gradient-to-r from-blue-500 to-purple-600 hover:from-blue-600 hover:to-purple-700 text-white border-0"
              onClick={next}
              disabled={isPending}
            >
              {isPending ? (
                "Working…"
              ) : (
                <>
                  {step === "channelAction" && channelActionIdx < actionChannels.length - 1 ? (
                    <>
                      Next Channel <ChevronRight className="h-4 w-4" />
                    </>
                  ) : step === "confirm" && hasActionChannels ? (
                    <>
                      Finalize Channels <ChevronRight className="h-4 w-4" />
                    </>
                  ) : (
                    <>
                      {completeIcon} {completeLabel}
                    </>
                  )}
                </>
              )}
            </Button>
          ) : (
            <Button
              size="sm"
              onClick={next}
              disabled={!canNext}
              className="gap-1 bg-gradient-to-r from-blue-500 to-purple-600 hover:from-blue-600 hover:to-purple-700 text-white border-0"
            >
              Next <ChevronRight className="h-4 w-4" />
            </Button>
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}
