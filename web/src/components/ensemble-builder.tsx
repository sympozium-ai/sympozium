/**
 * EnsembleBuilder — canvas-first visual builder for creating new Ensembles.
 *
 * Flow:
 * 1. Provider setup (provider + API key / base URL) — gates the canvas
 * 2. Canvas: add persona nodes, drag edges, configure via side panels
 * 3. Save → POST /api/v1/ensembles
 *
 * The provider context flows down to AgentConfigPanel so the model
 * selector can show provider-specific model lists.
 */

import { useCallback, useEffect, useRef, useState, useMemo } from "react";
import {
  ReactFlow,
  Background,
  Controls,
  MiniMap,
  type Node,
  type Edge,
  type Connection,
  Handle,
  Position,
  type NodeProps,
  useNodesState,
  useEdgesState,
  MarkerType,
  addEdge,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Plus,
  Settings,
  Save,
  ArrowRight,
  Database,
  Server,
  Cpu,
  Loader2,
  Check,
} from "lucide-react";
import type {
  AgentConfigSpec,
  AgentConfigRelationship,
  SharedMemorySpec,
} from "@/lib/api";
import { AgentConfigPanel } from "@/components/agent-config-panel";
import {
  EnsembleSettingsPanel,
  type EnsembleSettings,
} from "@/components/ensemble-settings-panel";
import { PROVIDERS } from "@/components/onboarding-wizard";
import { useCreateEnsemble, useModels } from "@/hooks/use-api";
import {
  AddProviderModal,
  type AddProviderResult,
} from "@/components/add-provider-modal";
import { useProviderNodes } from "@/hooks/use-provider-nodes";
import { ScrollArea } from "@/components/ui/scroll-area";
import { cn } from "@/lib/utils";
import { useNavigate } from "react-router-dom";

// ── Random agent name generator ───────────────────────────────────────────

const ADJECTIVES = [
  "swift", "brave", "calm", "keen", "bold", "warm", "cool", "fair",
  "wise", "neat", "deft", "glad", "mild", "pure", "safe", "true",
  "fast", "kind", "firm", "rare", "bright", "sharp", "steady", "quick",
];
const NOUNS = [
  "mango", "cedar", "flint", "coral", "birch", "ember", "frost", "maple",
  "quartz", "river", "solar", "tidal", "basil", "onyx", "pebble", "sage",
  "raven", "crane", "otter", "falcon", "pike", "finch", "spark", "bloom",
];

function randomAgentName(): string {
  const adj = ADJECTIVES[Math.floor(Math.random() * ADJECTIVES.length)];
  const noun = NOUNS[Math.floor(Math.random() * NOUNS.length)];
  return `${adj}-${noun}`;
}

// ── Provider context shared with AgentConfigPanel ────────────────────────

export interface ProviderContext {
  provider: string;
  apiKey: string;
  baseURL: string;
  modelRef?: string;
}

// ── Node data ──────────────────────────────────────────────────────────────

interface BuilderNodeData {
  persona: AgentConfigSpec;
  isConfigured: boolean;
  label: string;
  [key: string]: unknown;
}

// ── Custom node ────────────────────────────────────────────────────────────

function BuilderNode({ data }: NodeProps<Node<BuilderNodeData>>) {
  const { persona, isConfigured } = data;

  return (
    <div
      className={`rounded-lg border bg-card shadow-md px-4 py-3 min-w-[180px] max-w-[220px] cursor-pointer transition-all
        ${isConfigured ? "border-border/60" : "border-dashed border-muted-foreground/40"}`}
    >
      <Handle
        type="target"
        position={Position.Top}
        className="!bg-muted-foreground !w-2 !h-2"
      />

      <div className="flex items-center justify-between gap-2 mb-1">
        <span className="font-semibold text-sm truncate">
          {persona.displayName || persona.name || "Unnamed"}
        </span>
      </div>

      <p className="text-[10px] text-muted-foreground font-mono mb-1.5 truncate">
        {persona.name || "click to configure"}
      </p>

      {persona.model && (
        <Badge
          variant="outline"
          className="text-[10px] font-mono mb-1.5 block w-fit"
        >
          {persona.model}
        </Badge>
      )}

      <div className="flex flex-wrap gap-0.5">
        {persona.skills?.slice(0, 3).map((sk) => (
          <Badge key={sk} variant="secondary" className="text-[9px] px-1 py-0">
            {sk}
          </Badge>
        ))}
        {(persona.skills?.length ?? 0) > 3 && (
          <Badge variant="secondary" className="text-[9px] px-1 py-0">
            +{(persona.skills?.length ?? 0) - 3}
          </Badge>
        )}
      </div>

      {!isConfigured && (
        <p className="text-[9px] text-amber-500/80 mt-1.5 italic">
          Click to configure
        </p>
      )}

      <Handle
        type="source"
        position={Position.Bottom}
        className="!bg-muted-foreground !w-2 !h-2"
      />
    </div>
  );
}

const nodeTypes = { builder: BuilderNode };

// ── Edge styling ───────────────────────────────────────────────────────────

const EDGE_TYPES = ["delegation", "sequential", "supervision"] as const;

const edgeStyles: Record<string, { stroke: string; strokeDasharray?: string }> =
  {
    delegation: { stroke: "#3b82f6" },
    sequential: { stroke: "#f59e0b", strokeDasharray: "6 3" },
    supervision: { stroke: "#6b7280", strokeDasharray: "2 4" },
  };

const edgeLabels: Record<string, string> = {
  delegation: "delegates to",
  sequential: "then",
  supervision: "supervises",
};

// ── Provider setup step ────────────────────────────────────────────────────

function ProviderSetup({
  onComplete,
}: {
  onComplete: (ctx: ProviderContext) => void;
}) {
  const [provider, setProvider] = useState("");
  const [apiKey, setApiKey] = useState("");
  const [baseURL, setBaseURL] = useState("");
  const [selectedModelRef, setSelectedModelRef] = useState("");
  const [inferenceMode, setInferenceMode] = useState<"workload" | "node">(
    "workload",
  );
  const { data: models } = useModels();
  const readyModels = (models || []).filter(
    (m) => m.status?.phase === "Ready",
  );

  const selectedProvider = PROVIDERS.find((p) => p.value === provider);
  const isLocal =
    provider === "ollama" ||
    provider === "lm-studio" ||
    provider === "llama-server" ||
    provider === "unsloth";
  const needsKey = !isLocal && provider !== "custom" && provider !== "";

  const nodeProviderMatches = (probeName: string) => {
    if (provider === "custom") return true;
    if (provider === "unsloth")
      return (
        probeName === "unsloth" ||
        probeName === "llama-cpp" ||
        probeName === "vllm"
      );
    if (provider === "llama-server") return probeName === "llama-cpp";
    return probeName === provider;
  };

  const { data: providerNodes, isLoading: nodesLoading } = useProviderNodes(
    isLocal || provider === "custom",
  );

  // Auto-switch to "node" mode when matching providers are discovered.
  const userOverrodeInferenceMode = useRef(false);
  useEffect(() => {
    if (
      !isLocal ||
      nodesLoading ||
      !providerNodes ||
      userOverrodeInferenceMode.current
    )
      return;
    const hasMatch = providerNodes.some((n) =>
      n.providers.some((p) => nodeProviderMatches(p.name)),
    );
    if (hasMatch) setInferenceMode("node");
  }, [providerNodes, nodesLoading, provider]);
  useEffect(() => {
    userOverrodeInferenceMode.current = false;
  }, [provider]);

  const isLocalModel = provider === "local-model";
  const canContinue =
    provider !== "" &&
    (isLocalModel
      ? selectedModelRef !== ""
      : (!needsKey || apiKey !== "") && (!isLocal || baseURL !== ""));

  return (
    <div className="flex items-center justify-center h-[calc(100vh-12rem)]">
      <div className="w-full max-w-md space-y-6">
        <div className="text-center space-y-2">
          <h2 className="text-xl font-bold">Choose AI Provider</h2>
          <p className="text-sm text-muted-foreground">
            Select the provider your ensemble will use. This determines which
            models are available for your agents.
          </p>
        </div>

        {/* Provider grid */}
        <div className="grid grid-cols-3 gap-2">
          {/* Local Model option */}
          <button
            onClick={() => {
              setProvider("local-model");
              setBaseURL("");
              setApiKey("");
            }}
            className={`flex flex-col items-center gap-1.5 rounded-lg border p-3 text-xs transition-colors
              ${
                provider === "local-model"
                  ? "border-violet-500/60 bg-violet-500/10 text-violet-400"
                  : "border-border/50 hover:border-border hover:bg-white/5"
              }`}
          >
            <Cpu className="h-5 w-5" />
            Local Model
            {readyModels.length > 0 && (
              <span className="text-[9px] text-muted-foreground">
                {readyModels.length} ready
              </span>
            )}
          </button>
          {PROVIDERS.map((p) => {
            const Icon = p.icon;
            return (
              <button
                key={p.value}
                onClick={() => {
                  setProvider(p.value);
                  setBaseURL(p.defaultBaseURL || "");
                  setSelectedModelRef("");
                }}
                className={`flex flex-col items-center gap-1.5 rounded-lg border p-3 text-xs transition-colors
                  ${
                    provider === p.value
                      ? "border-blue-500/60 bg-blue-500/10 text-blue-400"
                      : "border-border/50 hover:border-border hover:bg-white/5"
                  }`}
              >
                <Icon className="h-5 w-5" />
                {p.label}
              </button>
            );
          })}
        </div>

        {/* Local Model selector */}
        {isLocalModel && (
          <div className="space-y-1.5">
            <Label className="text-xs">Select Model</Label>
            {readyModels.length === 0 ? (
              <div className="rounded-md border border-border/50 bg-muted/20 px-3 py-3 text-xs text-muted-foreground">
                No models are ready. Deploy a model first on the{" "}
                <a href="/models" className="text-blue-400 hover:underline">
                  Models page
                </a>
                .
              </div>
            ) : (
              <ScrollArea className="h-40 rounded-md border border-border/50">
                <div className="p-1 space-y-0.5">
                  {readyModels.map((model) => {
                    const isSelected =
                      selectedModelRef === model.metadata.name;
                    return (
                      <button
                        key={`${model.metadata.namespace}/${model.metadata.name}`}
                        type="button"
                        onClick={() =>
                          setSelectedModelRef(model.metadata.name)
                        }
                        className={cn(
                          "flex w-full items-start gap-2 rounded-md px-2.5 py-2 text-left text-xs transition-colors",
                          isSelected
                            ? "bg-violet-500/15 text-violet-400 border border-violet-500/30"
                            : "text-foreground hover:bg-white/5 border border-transparent",
                        )}
                      >
                        <Cpu className="h-3.5 w-3.5 mt-0.5 shrink-0" />
                        <div className="min-w-0">
                          <div className="font-mono truncate">
                            {model.metadata.name}
                          </div>
                          <div className="text-[10px] text-muted-foreground">
                            {model.metadata.namespace}
                            {model.spec.resources?.gpu
                              ? ` · GPU: ${model.spec.resources.gpu}`
                              : " · CPU"}
                            {model.status?.placedNode &&
                              ` · ${model.status.placedNode}`}
                          </div>
                        </div>
                        {isSelected && (
                          <Check className="h-3 w-3 shrink-0 mt-0.5 ml-auto" />
                        )}
                      </button>
                    );
                  })}
                </div>
              </ScrollArea>
            )}
          </div>
        )}

        {/* API key (for cloud providers) */}
        {needsKey && (
          <div className="space-y-1.5">
            <Label className="text-xs">API Key</Label>
            <Input
              type="password"
              value={apiKey}
              onChange={(e) => setApiKey(e.target.value)}
              placeholder={`${selectedProvider?.label || "Provider"} API key`}
              className="h-8 text-sm font-mono"
            />
          </div>
        )}

        {/* Inference source toggle for local providers */}
        {(isLocal || provider === "custom") && (
          <div className="space-y-2">
            <Label className="text-xs">Inference Source</Label>
            <div className="flex gap-2">
              <button
                type="button"
                onClick={() => {
                  userOverrodeInferenceMode.current = true;
                  setInferenceMode("workload");
                }}
                className={cn(
                  "flex-1 flex items-center justify-center gap-1.5 rounded-md border px-3 py-2 text-xs transition-colors",
                  inferenceMode === "workload"
                    ? "border-blue-500/40 bg-blue-500/15 text-blue-300"
                    : "border-border/50 hover:bg-white/5",
                )}
              >
                <Server className="h-3.5 w-3.5" /> In-cluster service
              </button>
              <button
                type="button"
                onClick={() => {
                  userOverrodeInferenceMode.current = true;
                  setInferenceMode("node");
                }}
                className={cn(
                  "flex-1 flex items-center justify-center gap-1.5 rounded-md border px-3 py-2 text-xs transition-colors",
                  inferenceMode === "node"
                    ? "border-blue-500/40 bg-blue-500/15 text-blue-300"
                    : "border-border/50 hover:bg-white/5",
                )}
              >
                <Cpu className="h-3.5 w-3.5" /> Installed on node
              </button>
            </div>
          </div>
        )}

        {/* In-cluster service: manual Base URL input */}
        {(isLocal || provider === "custom") && inferenceMode === "workload" && (
          <div className="space-y-1.5">
            <Label className="text-xs">Base URL</Label>
            <Input
              value={baseURL}
              onChange={(e) => setBaseURL(e.target.value)}
              placeholder={
                provider === "ollama"
                  ? "http://ollama.default.svc:11434/v1"
                  : provider === "lm-studio"
                    ? "http://localhost:1234/v1"
                    : "http://localhost:8080/v1"
              }
              className="h-8 text-sm font-mono"
            />
          </div>
        )}

        {/* Node-based: discover and select a node */}
        {(isLocal || provider === "custom") && inferenceMode === "node" && (
          <div className="space-y-1.5">
            <Label className="text-xs">Select Node</Label>
            {nodesLoading ? (
              <div className="flex items-center gap-2 py-4 text-xs text-muted-foreground justify-center">
                <Loader2 className="h-3.5 w-3.5 animate-spin" />
                Discovering nodes...
              </div>
            ) : !providerNodes ||
              providerNodes.filter((n) =>
                n.providers.some((p) => nodeProviderMatches(p.name)),
              ).length === 0 ? (
              <div className="rounded-md border border-border/50 bg-muted/20 px-3 py-3 text-xs text-muted-foreground">
                {providerNodes && providerNodes.length > 0
                  ? `No nodes with ${provider} detected. Found other providers on ${providerNodes.length} node${providerNodes.length === 1 ? "" : "s"}.`
                  : "No nodes with inference providers detected. Is the node-probe DaemonSet enabled?"}
              </div>
            ) : (
              <ScrollArea className="h-40 rounded-md border border-border/50">
                <div className="p-1 space-y-0.5">
                  {providerNodes
                    .filter((node) =>
                      node.providers.some((p) => nodeProviderMatches(p.name)),
                    )
                    .map((node) => {
                      const providerInfo =
                        node.providers.find((p) =>
                          nodeProviderMatches(p.name),
                        ) || node.providers[0];
                      const nodeBase = providerInfo
                        ? providerInfo.proxyPort
                          ? `http://${node.nodeIP}:${providerInfo.proxyPort}/proxy/${providerInfo.name}/v1`
                          : `http://${node.nodeIP}:${providerInfo.port}/v1`
                        : "";
                      const isSelected = baseURL === nodeBase;
                      const nodeProviders = node.providers
                        .filter((p) => nodeProviderMatches(p.name))
                        .map((p) => p.name);
                      const nodeModels = node.providers
                        .filter((p) => nodeProviderMatches(p.name))
                        .flatMap((p) => p.models);

                      return (
                        <button
                          key={node.nodeName}
                          type="button"
                          onClick={() => setBaseURL(nodeBase)}
                          className={cn(
                            "flex w-full items-start gap-2 rounded-md px-2.5 py-2 text-left text-xs transition-colors",
                            isSelected
                              ? "bg-blue-500/15 text-blue-400 border border-blue-500/30"
                              : "text-foreground hover:bg-white/5 border border-transparent",
                          )}
                        >
                          <Cpu className="h-3.5 w-3.5 mt-0.5 shrink-0" />
                          <div className="min-w-0">
                            <div className="font-mono truncate">
                              {node.nodeName}
                            </div>
                            <div className="text-[10px] text-muted-foreground">
                              {node.nodeIP} &middot; {nodeProviders.join(", ")}
                              {nodeModels.length > 0 &&
                                ` · ${nodeModels.length} model${nodeModels.length === 1 ? "" : "s"}`}
                            </div>
                          </div>
                          {isSelected && (
                            <Check className="h-3 w-3 shrink-0 mt-0.5 ml-auto" />
                          )}
                        </button>
                      );
                    })}
                </div>
              </ScrollArea>
            )}
          </div>
        )}

        <Button
          className="w-full"
          disabled={!canContinue}
          onClick={() =>
            onComplete({
              provider: isLocalModel ? "openai" : provider,
              apiKey,
              baseURL,
              modelRef: isLocalModel ? selectedModelRef : undefined,
            })
          }
        >
          Continue to Builder
          <ArrowRight className="h-4 w-4 ml-2" />
        </Button>

        {/* Skip for now option */}
        <button
          onClick={() =>
            onComplete({ provider: "openai", apiKey: "", baseURL: "" })
          }
          className="w-full text-center text-xs text-muted-foreground hover:text-foreground transition-colors"
        >
          Skip — I'll configure the provider when I activate the ensemble
        </button>
      </div>
    </div>
  );
}

// ── Main builder ───────────────────────────────────────────────────────────

interface EnsembleBuilderProps {
  initialPersonas?: AgentConfigSpec[];
  initialRelationships?: AgentConfigRelationship[];
  initialSettings?: Partial<EnsembleSettings>;
}

export function EnsembleBuilder({
  initialPersonas,
  initialRelationships,
  initialSettings,
}: EnsembleBuilderProps) {
  const navigate = useNavigate();
  const createMutation = useCreateEnsemble();

  // ── Provider gate ──────────────────────────────────────────────────────
  const [providerCtx, setProviderCtx] = useState<ProviderContext | null>(null);

  // ── State ──────────────────────────────────────────────────────────────

  const [personas, setPersonas] = useState<AgentConfigSpec[]>(
    initialPersonas || [],
  );
  const [relationships, setRelationships] = useState<AgentConfigRelationship[]>(
    initialRelationships || [],
  );
  const [settings, setSettings] = useState<EnsembleSettings>({
    name: initialSettings?.name || "",
    description: initialSettings?.description || "",
    category: initialSettings?.category || "",
    workflowType: initialSettings?.workflowType || "autonomous",
    sharedMemory: initialSettings?.sharedMemory || {
      enabled: true,
      storageSize: "1Gi",
    },
  });

  const [selectedPersona, setSelectedPersona] = useState<string | null>(null);
  const [showSettings, setShowSettings] = useState(false);
  const [pendingConnection, setPendingConnection] = useState<Connection | null>(
    null,
  );

  // ── Show provider setup if not configured ──────────────────────────────

  if (!providerCtx) {
    return <ProviderSetup onComplete={setProviderCtx} />;
  }

  // ── From here, provider is configured — render the canvas ──────────────

  return (
    <BuilderCanvas
      providerCtx={providerCtx}
      personas={personas}
      setPersonas={setPersonas}
      relationships={relationships}
      setRelationships={setRelationships}
      settings={settings}
      setSettings={setSettings}
      selectedPersona={selectedPersona}
      setSelectedPersona={setSelectedPersona}
      showSettings={showSettings}
      setShowSettings={setShowSettings}
      pendingConnection={pendingConnection}
      setPendingConnection={setPendingConnection}
      createMutation={createMutation}
      navigate={navigate}
    />
  );
}

// ── Canvas (extracted to avoid hooks-after-early-return) ────────────────

function BuilderCanvas({
  providerCtx,
  personas,
  setPersonas,
  relationships,
  setRelationships,
  settings,
  setSettings,
  selectedPersona,
  setSelectedPersona,
  showSettings,
  setShowSettings,
  pendingConnection,
  setPendingConnection,
  createMutation,
  navigate,
}: {
  providerCtx: ProviderContext;
  personas: AgentConfigSpec[];
  setPersonas: React.Dispatch<React.SetStateAction<AgentConfigSpec[]>>;
  relationships: AgentConfigRelationship[];
  setRelationships: React.Dispatch<React.SetStateAction<AgentConfigRelationship[]>>;
  settings: EnsembleSettings;
  setSettings: React.Dispatch<React.SetStateAction<EnsembleSettings>>;
  selectedPersona: string | null;
  setSelectedPersona: React.Dispatch<React.SetStateAction<string | null>>;
  showSettings: boolean;
  setShowSettings: React.Dispatch<React.SetStateAction<boolean>>;
  pendingConnection: Connection | null;
  setPendingConnection: React.Dispatch<React.SetStateAction<Connection | null>>;
  createMutation: ReturnType<typeof useCreateEnsemble>;
  navigate: ReturnType<typeof useNavigate>;
}) {
  const initialNodes = useMemo(() => {
    const cols = Math.max(2, Math.ceil(Math.sqrt(personas.length || 1)));
    return personas.map((p, i) => ({
      id: p.name || `persona-${i}`,
      type: "builder" as const,
      position: { x: (i % cols) * 260, y: Math.floor(i / cols) * 200 },
      data: {
        persona: p,
        isConfigured: !!(p.name && p.systemPrompt),
        label: p.displayName || p.name || "Unnamed",
      },
    }));
  }, [personas]);

  const initialEdges = useMemo(
    () =>
      relationships.map((rel, i) => {
        const style = edgeStyles[rel.type] || edgeStyles.delegation;
        return {
          id: `e-${i}-${rel.source}-${rel.target}`,
          source: rel.source,
          target: rel.target,
          label: edgeLabels[rel.type] || rel.type,
          style,
          data: { relType: rel.type },
          markerEnd:
            rel.type !== "supervision"
              ? { type: MarkerType.ArrowClosed, color: style.stroke }
              : undefined,
          labelStyle: { fontSize: 10, fill: "#9ca3af" },
          animated: rel.type === "delegation",
        } as Edge;
      }),
    [relationships],
  );

  const [nodes, setNodes, onNodesChange] = useNodesState(initialNodes);
  const [edges, setEdges, onEdgesChange] = useEdgesState(initialEdges);
  const [showAddProvider, setShowAddProvider] = useState(false);

  function handleAddProvider(result: AddProviderResult) {
    const provId = result.modelRef
      ? `model:${result.modelRef}`
      : result.provider;
    const nodeId = `__prov__${provId}`;

    // Add provider node to canvas
    setNodes((prev) => [
      ...prev,
      {
        id: nodeId,
        type: "builder" as const,
        position: { x: 0, y: -160 },
        data: {
          persona: {
            name: provId,
            displayName: result.label,
            systemPrompt: "",
            model: result.modelRef || "",
            provider: result.modelRef ? undefined : result.provider,
            baseURL: result.baseURL || undefined,
          } as AgentConfigSpec,
          isConfigured: true,
          label: result.label,
        },
      },
    ]);
  }

  const onNodeClick = useCallback(
    (_: React.MouseEvent, node: Node) => {
      // Don't open config panel for provider nodes
      if (node.id.startsWith("__prov__")) return;
      setSelectedPersona(node.id);
      setShowSettings(false);
    },
    [setSelectedPersona, setShowSettings],
  );

  const onConnect = useCallback(
    (connection: Connection) => {
      if (!connection.source || !connection.target) return;
      // Provider→Agent connections: auto-wire without relationship picker
      if (connection.source.startsWith("__prov__")) {
        const provId = connection.source.replace("__prov__", "");
        const targetPersona = personas.find((p) => p.name === connection.target);
        if (targetPersona) {
          // Update the agent config's provider
          const isModelRef = provId.startsWith("model:");
          const updated = { ...targetPersona };
          if (isModelRef) {
            updated.provider = undefined;
            updated.baseURL = undefined;
          } else {
            updated.provider = provId;
          }
          setPersonas((prev) =>
            prev.map((p) => (p.name === connection.target ? updated : p)),
          );
          // Add a visual edge
          setEdges((eds) =>
            addEdge(
              {
                ...connection,
                id: `prov-${provId}-${connection.target}`,
                style: { stroke: "#8b5cf6", strokeWidth: 1.5, strokeDasharray: "4 3" },
                animated: true,
              },
              eds,
            ),
          );
        }
        return;
      }
      setPendingConnection(connection);
    },
    [setPendingConnection, personas, setPersonas, setEdges],
  );

  function confirmEdgeType(type: (typeof EDGE_TYPES)[number]) {
    if (!pendingConnection) return;
    const style = edgeStyles[type];
    const newEdge: Edge = {
      id: `e-${edges.length}-${pendingConnection.source}-${pendingConnection.target}`,
      source: pendingConnection.source!,
      target: pendingConnection.target!,
      label: edgeLabels[type],
      style,
      data: { relType: type },
      markerEnd:
        type !== "supervision"
          ? { type: MarkerType.ArrowClosed, color: style.stroke }
          : undefined,
      labelStyle: { fontSize: 10, fill: "#9ca3af" },
      animated: type === "delegation",
    };
    setEdges((eds) => addEdge(newEdge, eds));
    setRelationships((prev) => [
      ...prev,
      {
        source: pendingConnection.source!,
        target: pendingConnection.target!,
        type,
      },
    ]);
    setPendingConnection(null);
  }

  function addPersona() {
    const name = randomAgentName();
    // When using a local model via modelRef, don't set a per-persona model —
    // the controller resolves the endpoint from the ensemble-level modelRef.
    const defaultModel = providerCtx.modelRef
      ? ""
      : PROVIDERS.find((p) => p.value === providerCtx.provider)?.defaultModel ||
        "";
    const newPersona: AgentConfigSpec = {
      name,
      displayName: "",
      systemPrompt: "",
      model: defaultModel || undefined,
      skills: ["memory"],
    };
    setPersonas((prev) => [...prev, newPersona]);
    const cols = Math.max(2, Math.ceil(Math.sqrt(personas.length + 1)));
    const i = personas.length;
    setNodes((prev) => [
      ...prev,
      {
        id: name,
        type: "builder" as const,
        position: { x: (i % cols) * 260, y: Math.floor(i / cols) * 200 },
        data: { persona: newPersona, isConfigured: false, label: name },
      },
    ]);
    setSelectedPersona(name);
    setShowSettings(false);
  }

  function updatePersona(updated: AgentConfigSpec) {
    const oldName = selectedPersona;
    setPersonas((prev) => prev.map((p) => (p.name === oldName ? updated : p)));
    setNodes((prev) =>
      prev.map((n) =>
        n.id === oldName
          ? {
              ...n,
              id: updated.name,
              data: {
                persona: updated,
                isConfigured: !!(updated.name && updated.systemPrompt),
                label: updated.displayName || updated.name,
              },
            }
          : n,
      ),
    );
    if (oldName !== updated.name) {
      setRelationships((prev) =>
        prev.map((r) => ({
          ...r,
          source: r.source === oldName ? updated.name : r.source,
          target: r.target === oldName ? updated.name : r.target,
        })),
      );
      setEdges((prev) =>
        prev.map((e) => ({
          ...e,
          source: e.source === oldName ? updated.name : e.source,
          target: e.target === oldName ? updated.name : e.target,
        })),
      );
    }
    setSelectedPersona(updated.name);
  }

  function deletePersona() {
    if (!selectedPersona) return;
    setPersonas((prev) => prev.filter((p) => p.name !== selectedPersona));
    setRelationships((prev) =>
      prev.filter(
        (r) => r.source !== selectedPersona && r.target !== selectedPersona,
      ),
    );
    setNodes((prev) => prev.filter((n) => n.id !== selectedPersona));
    setEdges((prev) =>
      prev.filter(
        (e) => e.source !== selectedPersona && e.target !== selectedPersona,
      ),
    );
    setSelectedPersona(null);
  }

  const selectedPersonaData = personas.find((p) => p.name === selectedPersona);

  const canSave =
    settings.name &&
    personas.length > 0 &&
    personas.every((p) => p.name && p.systemPrompt);

  function handleSave() {
    createMutation.mutate(
      {
        name: settings.name,
        description: settings.description,
        category: settings.category,
        workflowType: settings.workflowType,
        agentConfigs: personas,
        relationships: relationships.length > 0 ? relationships : undefined,
        sharedMemory: settings.sharedMemory?.enabled
          ? settings.sharedMemory
          : undefined,
        modelRef: providerCtx.modelRef || undefined,
      },
      { onSuccess: () => navigate(`/ensembles/${settings.name}`) },
    );
  }

  const providerLabel =
    PROVIDERS.find((p) => p.value === providerCtx.provider)?.label ||
    providerCtx.provider;

  return (
    <div className="flex h-[calc(100vh-8rem)]">
      <div className="flex-1 flex flex-col">
        {/* Toolbar */}
        <div className="flex items-center gap-2 px-4 py-2 border-b border-border bg-card">
          <Input
            value={settings.name}
            onChange={(e) =>
              setSettings((s) => ({
                ...s,
                name: e.target.value.toLowerCase().replace(/[^a-z0-9-]/g, "-"),
              }))
            }
            placeholder="ensemble-name (required)"
            className="h-8 w-56 text-sm font-mono"
          />
          <Button size="sm" variant="outline" onClick={addPersona}>
            <Plus className="h-3.5 w-3.5 mr-1" />
            Add Agent
          </Button>
          <Button
            size="sm"
            variant="outline"
            onClick={() => setShowAddProvider(true)}
          >
            <Cpu className="h-3.5 w-3.5 mr-1" />
            Add Provider
          </Button>
          <Button
            size="sm"
            variant={showSettings ? "default" : "outline"}
            onClick={() => {
              setShowSettings(!showSettings);
              setSelectedPersona(null);
            }}
          >
            <Settings className="h-3.5 w-3.5 mr-1" />
            Settings
          </Button>
          <button
            onClick={() =>
              setSettings((s) => ({
                ...s,
                sharedMemory: s.sharedMemory?.enabled
                  ? null
                  : { enabled: true, storageSize: "1Gi" },
              }))
            }
            className={`flex items-center gap-1 rounded-md border px-2 py-1 text-[10px] transition-colors
              ${
                settings.sharedMemory?.enabled
                  ? "border-blue-500/40 bg-blue-500/10 text-blue-400"
                  : "border-border/50 text-muted-foreground hover:border-border"
              }`}
            title="Toggle shared workflow memory"
          >
            <Database className="h-3 w-3" />
            Shared Memory {settings.sharedMemory?.enabled ? "ON" : "OFF"}
          </button>
          <div className="flex-1" />
          <Badge variant="outline" className="text-[10px] font-mono">
            {providerCtx.modelRef ? (
              <span className="flex items-center gap-1">
                <Cpu className="h-2.5 w-2.5" />
                {providerCtx.modelRef}
              </span>
            ) : (
              providerLabel
            )}
          </Badge>
          <Button
            size="sm"
            onClick={handleSave}
            disabled={!canSave || createMutation.isPending}
          >
            <Save className="h-3.5 w-3.5 mr-1" />
            {createMutation.isPending ? "Saving..." : "Save Ensemble"}
          </Button>
        </div>

        {/* Canvas */}
        <div className="flex-1">
          <ReactFlow
            nodes={nodes}
            edges={edges}
            onNodesChange={onNodesChange}
            onEdgesChange={onEdgesChange}
            onConnect={onConnect}
            onNodeClick={onNodeClick}
            nodeTypes={nodeTypes}
            fitView
            proOptions={{ hideAttribution: true }}
          >
            <Background />
            <Controls />
            <MiniMap />
          </ReactFlow>
        </div>

        {/* Hints */}
        <div className="px-4 py-1.5 border-t border-border bg-muted/30 text-[10px] text-muted-foreground flex items-center gap-4">
          <span>Click a node to configure</span>
          <span>Drag between nodes to create relationships</span>
          {!canSave && (
            <span className="text-amber-500">
              {!settings.name
                ? "Enter an ensemble name to save"
                : personas.length === 0
                  ? "Add at least one persona"
                  : "All personas need a name and system prompt"}
            </span>
          )}
        </div>
      </div>

      {/* Edge type picker modal */}
      {pendingConnection && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50">
          <div className="bg-card border border-border rounded-lg p-4 space-y-3 shadow-xl">
            <h4 className="text-sm font-semibold">Relationship Type</h4>
            <p className="text-xs text-muted-foreground">
              {pendingConnection.source} &rarr; {pendingConnection.target}
            </p>
            <div className="flex gap-2">
              {EDGE_TYPES.map((type) => (
                <Button
                  key={type}
                  size="sm"
                  variant="outline"
                  onClick={() => confirmEdgeType(type)}
                  className="capitalize"
                >
                  {type}
                </Button>
              ))}
            </div>
            <Button
              size="sm"
              variant="ghost"
              onClick={() => setPendingConnection(null)}
              className="w-full"
            >
              Cancel
            </Button>
          </div>
        </div>
      )}

      {/* Side panels */}
      {selectedPersonaData && (
        <AgentConfigPanel
          persona={selectedPersonaData}
          providerCtx={providerCtx}
          onSave={updatePersona}
          onDelete={deletePersona}
          onClose={() => setSelectedPersona(null)}
        />
      )}
      {showSettings && (
        <EnsembleSettingsPanel
          settings={settings}
          onChange={setSettings}
          onClose={() => setShowSettings(false)}
        />
      )}

      <AddProviderModal
        open={showAddProvider}
        onClose={() => setShowAddProvider(false)}
        onAdd={handleAddProvider}
      />
    </div>
  );
}
