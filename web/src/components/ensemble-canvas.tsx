import { useMemo, useCallback, useState, useEffect, useRef } from "react";
import {
  ReactFlow,
  Background,
  Controls,
  MiniMap,
  type Node,
  type Edge,
  type NodeProps,
  type Connection,
  Handle,
  Position,
  useNodesState,
  useEdgesState,
  MarkerType,
  addEdge,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  useRuns,
  useEnsembles,
  useModels,
  usePatchEnsembleRelationships,
} from "@/hooks/use-api";
import { useWebSocket } from "@/hooks/use-websocket";
import { useQueryClient } from "@tanstack/react-query";
import { Save, Plus, Trash2, Database, Cpu } from "lucide-react";
import type {
  Ensemble,
  Model,
  AgentConfigSpec,
  AgentConfigRelationship,
  AgentRun,
  SecretRef,
} from "@/lib/api";
import { PROVIDERS } from "@/components/onboarding-wizard";
import {
  AddProviderModal,
  type AddProviderResult,
} from "@/components/add-provider-modal";

// ── Real-time run status updates via WebSocket ─────────────────────────────

/** Invalidates the runs query when a run lifecycle event arrives over the
 *  WebSocket, giving the canvas near-instant status updates. */
function useRunEventInvalidation() {
  const { events } = useWebSocket();
  const qc = useQueryClient();
  const lastSeenRef = useRef(0);

  useEffect(() => {
    if (events.length <= lastSeenRef.current) return;
    const newEvents = events.slice(lastSeenRef.current);
    lastSeenRef.current = events.length;

    const hasRunEvent = newEvents.some(
      (e) =>
        e.topic === "agent.run.completed" ||
        e.topic === "agent.run.failed" ||
        e.topic === "agent.run.started" ||
        e.topic === "agent.run.requested",
    );
    if (hasRunEvent) {
      qc.invalidateQueries({ queryKey: ["runs"] });
    }
  }, [events, qc]);
}

// ── Shared node data ────────────────────────────────────────────────────────

export interface AgentConfigNodeData {
  persona: AgentConfigSpec;
  packName?: string;
  agentName?: string;
  runPhase?: string;
  runTask?: string;
  hasSharedMemory?: boolean;
  label: string;
  [key: string]: unknown;
}

// ── Phase styling ───────────────────────────────────────────────────────────

const phaseBorder: Record<string, string> = {
  Running: "ring-2 ring-blue-500/70 shadow-[0_0_12px_rgba(59,130,246,0.3)]",
  Succeeded: "ring-1 ring-green-500/40",
  Failed: "ring-2 ring-red-500/60 shadow-[0_0_12px_rgba(239,68,68,0.3)]",
  Pending: "ring-1 ring-yellow-500/40",
  Serving: "ring-2 ring-violet-500/60 shadow-[0_0_12px_rgba(139,92,246,0.3)]",
  AwaitingDelegate:
    "ring-2 ring-amber-500/60 shadow-[0_0_12px_rgba(245,158,11,0.3)]",
};

const phaseDot: Record<string, string> = {
  Running: "bg-blue-500 animate-pulse",
  Succeeded: "bg-green-500",
  Failed: "bg-red-500",
  Pending: "bg-yellow-500 animate-pulse",
  Serving: "bg-violet-500 animate-pulse",
  AwaitingDelegate: "bg-amber-500 animate-pulse",
};

const phaseLabel: Record<string, string> = {
  Running: "Running",
  Succeeded: "Done",
  Failed: "Failed",
  Pending: "Pending",
  Serving: "Serving",
  AwaitingDelegate: "Awaiting",
};

// ── Custom persona node ─────────────────────────────────────────────────────

function AgentConfigNode({ data }: NodeProps<Node<AgentConfigNodeData>>) {
  const {
    persona,
    packName,
    agentName,
    runPhase,
    runTask,
    hasSharedMemory,
  } = data;

  const borderClass = runPhase ? phaseBorder[runPhase] || "" : "";
  const dotClass = runPhase ? phaseDot[runPhase] || "bg-muted-foreground" : "";

  return (
    <div
      className={`rounded-lg border border-border/60 bg-card shadow-md px-4 py-3 min-w-[180px] max-w-[220px] transition-shadow duration-300 ${borderClass}`}
    >
      <Handle
        type="target"
        position={Position.Top}
        className="!bg-muted-foreground !w-2 !h-2"
      />

      {/* Pack name label (for global canvas) */}
      {packName && (
        <p className="text-[9px] text-muted-foreground/50 font-mono mb-1 -mt-0.5 truncate">
          {packName}
        </p>
      )}

      <div className="flex items-center justify-between gap-2 mb-1">
        <span className="font-semibold text-sm truncate">
          {persona.displayName || persona.name}
        </span>
        {runPhase && (
          <div className="flex items-center gap-1 shrink-0" title={runPhase}>
            <span className={`h-2 w-2 rounded-full ${dotClass}`} />
            <span className="text-[9px] text-muted-foreground">
              {phaseLabel[runPhase] || runPhase}
            </span>
          </div>
        )}
      </div>

      <p className="text-[10px] text-muted-foreground font-mono mb-1.5 truncate">
        {persona.name}
      </p>

      {persona.model && (
        <Badge
          variant="outline"
          className="text-[10px] font-mono mb-1.5 block w-fit"
        >
          {persona.model}
        </Badge>
      )}

      {hasSharedMemory && (
        <Badge
          variant="outline"
          className="text-[9px] px-1 py-0 mb-1 gap-0.5 w-fit"
          title="Shared workflow memory"
        >
          <Database className="h-2.5 w-2.5" />
          shared memory
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

      {/* Running task preview */}
      {runTask && runPhase === "Running" && (
        <p
          className="text-[9px] text-blue-400/80 mt-1.5 truncate italic"
          title={runTask}
        >
          {runTask}
        </p>
      )}

      {agentName && !runTask && (
        <p className="text-[9px] text-muted-foreground/60 mt-1.5 truncate">
          {agentName}
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

// ── Model node (local inference) ────────────────────────────────────────────

export interface ModelNodeData {
  model: Model;
  label: string;
  [key: string]: unknown;
}

const modelPhaseBorder: Record<string, string> = {
  Ready: "ring-2 ring-emerald-500/60 shadow-[0_0_12px_rgba(16,185,129,0.25)]",
  Loading: "ring-2 ring-blue-500/50 shadow-[0_0_10px_rgba(59,130,246,0.2)]",
  Downloading: "ring-2 ring-amber-500/50",
  Placing: "ring-2 ring-blue-500/50",
  Failed: "ring-2 ring-red-500/60",
};

function ModelNode({ data }: NodeProps<Node<ModelNodeData>>) {
  const { model } = data;
  const phase = model.status?.phase || "Pending";
  const border = modelPhaseBorder[phase] || "";

  return (
    <div
      className={`rounded-lg border border-violet-500/40 bg-card shadow-md px-4 py-3 min-w-[180px] max-w-[220px] transition-shadow duration-300 ${border}`}
    >
      <div className="flex items-center gap-1.5 mb-1">
        <Cpu className="h-3.5 w-3.5 text-violet-400" />
        <span className="font-semibold text-sm text-violet-300">
          Local Model
        </span>
      </div>

      <p className="text-[10px] text-muted-foreground font-mono mb-1.5 truncate">
        {model.metadata.name}
      </p>

      <div className="flex flex-wrap gap-1 mb-1">
        <Badge
          variant="outline"
          className={`text-[9px] px-1 py-0 ${
            phase === "Ready"
              ? "border-emerald-500/50 text-emerald-400"
              : phase === "Failed"
                ? "border-red-500/50 text-red-400"
                : "border-blue-500/50 text-blue-400"
          }`}
        >
          {phase}
        </Badge>
        {(model.spec.resources?.gpu ?? 0) > 0 && (
          <Badge variant="outline" className="text-[9px] px-1 py-0">
            GPU: {model.spec.resources?.gpu}
          </Badge>
        )}
      </div>

      {model.status?.endpoint && (
        <p
          className="text-[9px] text-muted-foreground/60 truncate"
          title={model.status.endpoint}
        >
          {model.status.endpoint}
        </p>
      )}

      {model.status?.placedNode && (
        <p className="text-[9px] text-violet-400/60 truncate mt-0.5">
          node: {model.status.placedNode}
        </p>
      )}

      <Handle
        type="source"
        position={Position.Bottom}
        className="!bg-violet-400 !w-2 !h-2"
      />
    </div>
  );
}

// ── Provider node (remote LLM inference) ───────────────────────────────────

export interface ProviderNodeData {
  provider: string;
  label: string;
  baseURL?: string;
  isModelRef?: boolean;
  model?: Model;
  [key: string]: unknown;
}

const providerColors: Record<string, { border: string; text: string; edge: string }> = {
  openai:         { border: "border-emerald-500/40", text: "text-emerald-400", edge: "#10b981" },
  anthropic:      { border: "border-orange-500/40",  text: "text-orange-400",  edge: "#f97316" },
  "azure-openai": { border: "border-blue-500/40",    text: "text-blue-400",    edge: "#3b82f6" },
  ollama:         { border: "border-cyan-500/40",     text: "text-cyan-400",    edge: "#06b6d4" },
  "lm-studio":    { border: "border-teal-500/40",     text: "text-teal-400",    edge: "#14b8a6" },
  "llama-server": { border: "border-amber-500/40",   text: "text-amber-400",   edge: "#f59e0b" },
  bedrock:        { border: "border-yellow-500/40",   text: "text-yellow-400",  edge: "#eab308" },
  custom:         { border: "border-gray-500/40",     text: "text-gray-400",    edge: "#6b7280" },
};

const defaultProviderColor = { border: "border-blue-500/40", text: "text-blue-400", edge: "#3b82f6" };

function ProviderNode({ data }: NodeProps<Node<ProviderNodeData>>) {
  // For local models, render inline with model node styling.
  if (data.isModelRef && data.model) {
    const model = data.model;
    const phase = model.status?.phase || "Pending";
    const border = modelPhaseBorder[phase] || "";
    return (
      <div className={`rounded-lg border border-violet-500/40 bg-card shadow-md px-4 py-3 min-w-[180px] max-w-[220px] transition-shadow duration-300 ${border}`}>
        <div className="flex items-center gap-1.5 mb-1">
          <Cpu className="h-3.5 w-3.5 text-violet-400" />
          <span className="font-semibold text-sm text-violet-300">Local Model</span>
        </div>
        <p className="text-[10px] text-muted-foreground font-mono mb-1.5 truncate">{model.metadata.name}</p>
        <Badge variant="outline" className={`text-[9px] px-1 py-0 ${phase === "Ready" ? "border-emerald-500/50 text-emerald-400" : phase === "Failed" ? "border-red-500/50 text-red-400" : "border-blue-500/50 text-blue-400"}`}>{phase}</Badge>
        {model.status?.endpoint && <p className="text-[9px] text-muted-foreground/60 truncate mt-1" title={model.status.endpoint}>{model.status.endpoint}</p>}
        <Handle type="source" position={Position.Bottom} className="!bg-violet-400 !w-2 !h-2" />
      </div>
    );
  }

  const providerDef = PROVIDERS.find((p) => p.value === data.provider);
  const colors = providerColors[data.provider] || defaultProviderColor;
  const Icon = providerDef?.icon || Cpu;

  return (
    <div
      className={`rounded-lg border ${colors.border} bg-card shadow-md px-4 py-3 min-w-[180px] max-w-[220px] transition-shadow duration-300`}
    >
      <div className="flex items-center gap-1.5 mb-1">
        <Icon className={`h-3.5 w-3.5 ${colors.text}`} />
        <span className={`font-semibold text-sm ${colors.text}`}>
          {providerDef?.label || data.provider}
        </span>
      </div>

      {data.baseURL && (
        <p
          className="text-[9px] text-muted-foreground/60 font-mono truncate"
          title={data.baseURL}
        >
          {data.baseURL}
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

const nodeTypes = { persona: AgentConfigNode, model: ModelNode, provider: ProviderNode };

// ── Shared edge styling ─────────────────────────────────────────────────────

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

function styledEdge(
  id: string,
  source: string,
  target: string,
  relType: string,
): Edge {
  const style = edgeStyles[relType] || edgeStyles.delegation;
  return {
    id,
    source,
    target,
    label: edgeLabels[relType] || relType,
    style,
    data: { relType },
    markerEnd:
      relType !== "supervision"
        ? { type: MarkerType.ArrowClosed, color: style.stroke }
        : undefined,
    labelStyle: { fontSize: 10, fill: "#9ca3af" },
    animated: relType === "delegation",
  };
}

// ── Shared helpers ──────────────────────────────────────────────────────────

/** Build a run-phase map from runs: persona name → { phase, task } */
function buildRunPhaseMap(
  runs: AgentRun[] | undefined,
  installedAgentConfigs: Array<{ name: string; agentName: string }> | undefined,
): Map<string, { phase: string; task?: string }> {
  const map = new Map<string, { phase: string; task?: string }>();
  if (!runs || !installedAgentConfigs) return map;
  for (const ip of installedAgentConfigs) {
    const instanceRuns = runs
      .filter((r) => r.spec.agentRef === ip.agentName)
      .sort(
        (a, b) =>
          new Date(b.metadata.creationTimestamp || 0).getTime() -
          new Date(a.metadata.creationTimestamp || 0).getTime(),
      );
    if (instanceRuns.length > 0 && instanceRuns[0].status?.phase) {
      map.set(ip.name, {
        phase: instanceRuns[0].status.phase,
        task: instanceRuns[0].spec.task,
      });
    }
  }
  return map;
}

function layoutNodes(
  personas: AgentConfigSpec[],
  relationships: AgentConfigRelationship[],
  offsetX = 0,
  offsetY = 0,
  prefix = "",
): Node<AgentConfigNodeData>[] {
  const outDegree = new Map<string, number>();
  const inDegree = new Map<string, number>();
  for (const r of relationships) {
    outDegree.set(r.source, (outDegree.get(r.source) || 0) + 1);
    inDegree.set(r.target, (inDegree.get(r.target) || 0) + 1);
  }

  const sorted = [...personas].sort((a, b) => {
    const aScore = (outDegree.get(a.name) || 0) - (inDegree.get(a.name) || 0);
    const bScore = (outDegree.get(b.name) || 0) - (inDegree.get(b.name) || 0);
    if (bScore !== aScore) return bScore - aScore;
    return a.name.localeCompare(b.name);
  });

  const cols = Math.max(2, Math.ceil(Math.sqrt(sorted.length)));
  const xGap = 260;
  const yGap = 200;

  return sorted.map((persona, i) => ({
    id: prefix ? `${prefix}/${persona.name}` : persona.name,
    type: "persona",
    position: {
      x: offsetX + (i % cols) * xGap,
      y: offsetY + Math.floor(i / cols) * yGap,
    },
    data: { persona, label: persona.displayName || persona.name },
  }));
}

function buildEdges(relationships: AgentConfigRelationship[], prefix = ""): Edge[] {
  return relationships.map((rel, i) =>
    styledEdge(
      `e-${prefix}-${i}-${rel.source}-${rel.target}`,
      prefix ? `${prefix}/${rel.source}` : rel.source,
      prefix ? `${prefix}/${rel.target}` : rel.target,
      rel.type,
    ),
  );
}

// ── Provider node derivation ─────────────────────────────────────────────

interface DerivedProvider {
  id: string;
  provider: string;
  label: string;
  baseURL?: string;
  isModelRef?: boolean;
  model?: Model;
}

/** Derive unique provider/model nodes from ensemble data. */
function deriveProviders(
  pack: Ensemble,
  modelMap: Map<string, Model>,
): DerivedProvider[] {
  const seen = new Set<string>();
  const result: DerivedProvider[] = [];

  // 1. Ensemble-level modelRef → local model provider
  if (pack.spec.modelRef) {
    const model = modelMap.get(pack.spec.modelRef);
    const key = `model:${pack.spec.modelRef}`;
    if (!seen.has(key)) {
      seen.add(key);
      result.push({
        id: key,
        provider: "local-model",
        label: pack.spec.modelRef,
        isModelRef: true,
        model,
      });
    }
  }

  // 2. Ensemble-level authRefs → cloud providers
  for (const ref of pack.spec.authRefs || []) {
    if (ref.provider && !seen.has(ref.provider)) {
      seen.add(ref.provider);
      const prov = PROVIDERS.find((p) => p.value === ref.provider);
      result.push({
        id: ref.provider,
        provider: ref.provider,
        label: prov?.label || ref.provider,
        baseURL: pack.spec.baseURL,
      });
    }
  }

  // 3. Per-persona provider overrides
  for (const persona of pack.spec.agentConfigs || []) {
    if (persona.provider && !seen.has(persona.provider)) {
      seen.add(persona.provider);
      const prov = PROVIDERS.find((p) => p.value === persona.provider);
      result.push({
        id: persona.provider,
        provider: persona.provider,
        label: prov?.label || persona.provider,
        baseURL: persona.baseURL,
      });
    }
  }

  // 4. If nothing derived but personas have models, infer from ensemble context
  // (e.g. ensemble was activated via onboarding which sets authRefs).
  // If still empty and there are personas, show nothing — provider is implicit.

  return result;
}

/** Determine which provider a persona connects to. */
function personaProviderId(persona: AgentConfigSpec, pack: Ensemble): string | null {
  // Per-persona provider override
  if (persona.provider) return persona.provider;
  // Ensemble-level modelRef
  if (pack.spec.modelRef) return `model:${pack.spec.modelRef}`;
  // Ensemble-level authRefs (first one is the default)
  const defaultRef = (pack.spec.authRefs || [])[0];
  if (defaultRef?.provider) return defaultRef.provider;
  return null;
}

/** Build provider nodes + edges for a pack. */
function buildProviderNodesAndEdges(
  pack: Ensemble,
  modelMap: Map<string, Model>,
  personas: AgentConfigSpec[],
  offsetX: number,
  prefix: string,
): { nodes: Node<ProviderNodeData | ModelNodeData>[]; edges: Edge[] } {
  const providers = deriveProviders(pack, modelMap);
  if (providers.length === 0) return { nodes: [], edges: [] };

  const cols = Math.max(2, Math.ceil(Math.sqrt(personas.length)));
  const totalWidth = (cols - 1) * 260;
  const providerGap = 240;
  const providerStartX =
    offsetX + totalWidth / 2 - ((providers.length - 1) * providerGap) / 2 - 90;

  const nodes: Node<ProviderNodeData | ModelNodeData>[] = providers.map(
    (prov, i) => ({
      id: prefix ? `${prefix}/__prov__${prov.id}` : `__prov__${prov.id}`,
      type: prov.isModelRef && prov.model ? "model" : "provider",
      position: { x: providerStartX + i * providerGap, y: 0 },
      data: prov.isModelRef && prov.model
        ? ({ model: prov.model, label: prov.label } as ModelNodeData)
        : ({
            provider: prov.provider,
            label: prov.label,
            baseURL: prov.baseURL,
            isModelRef: prov.isModelRef,
            model: prov.model,
          } as ProviderNodeData),
    }),
  );

  const edges: Edge[] = [];
  for (const persona of personas) {
    const provId = personaProviderId(persona, pack);
    if (!provId) continue;
    // Find the matching provider node
    const provNode = nodes.find((n) =>
      n.id.endsWith(`__prov__${provId}`),
    );
    if (!provNode) continue;

    const targetId = prefix ? `${prefix}/${persona.name}` : persona.name;
    const colors = providerColors[provId] || (provId.startsWith("model:") ? { edge: "#8b5cf6" } : defaultProviderColor);

    edges.push({
      id: `prov-${prefix}-${provId}-${persona.name}`,
      source: provNode.id,
      target: targetId,
      type: "default",
      animated: provId.startsWith("model:")
        ? modelMap.get(provId.replace("model:", ""))?.status?.phase === "Ready"
        : true,
      style: { stroke: colors.edge, strokeWidth: 1.5, strokeDasharray: "4 3" },
      markerEnd: {
        type: MarkerType.ArrowClosed,
        color: colors.edge,
        width: 14,
        height: 14,
      },
    });
  }

  return { nodes, edges };
}

function edgesToRelationships(edges: Edge[]): AgentConfigRelationship[] {
  return edges.map((e) => ({
    source: e.source.includes("/") ? e.source.split("/")[1] : e.source,
    target: e.target.includes("/") ? e.target.split("/")[1] : e.target,
    type: (e.data?.relType as AgentConfigRelationship["type"]) || "delegation",
  }));
}

// ── Shared ReactFlow wrapper ────────────────────────────────────────────────

const rfDefaults = {
  fitView: true,
  fitViewOptions: { padding: 0.3 },
  minZoom: 0.2,
  maxZoom: 1.5,
  proOptions: { hideAttribution: true },
  colorMode: "dark" as const,
};

function CanvasShell({ children }: { children: React.ReactNode }) {
  return (
    <>
      <Background gap={20} size={1} color="#ffffff08" />
      <Controls
        showInteractive={false}
        className="!bg-card !border-border/40 !shadow-md [&>button]:!bg-card [&>button]:!border-border/40 [&>button]:!text-muted-foreground [&>button:hover]:!bg-white/5"
      />
      <MiniMap
        nodeColor="#3b82f6"
        maskColor="rgba(0,0,0,0.7)"
        className="!bg-card !border-border/40"
      />
      {children}
    </>
  );
}

// ── Edge type picker ────────────────────────────────────────────────────────

function EdgeTypePicker({
  onSelect,
  onCancel,
}: {
  onSelect: (type: string) => void;
  onCancel: () => void;
}) {
  return (
    <div className="absolute top-2 left-1/2 -translate-x-1/2 z-50 flex gap-1 rounded-lg border border-border/60 bg-card shadow-lg p-2">
      <span className="text-xs text-muted-foreground self-center mr-1">
        Type:
      </span>
      {EDGE_TYPES.map((t) => (
        <Button
          key={t}
          variant="ghost"
          size="sm"
          className="text-xs capitalize h-7 px-2"
          onClick={() => onSelect(t)}
          type="button"
        >
          <span
            className="w-2 h-2 rounded-full mr-1.5"
            style={{ backgroundColor: edgeStyles[t].stroke }}
          />
          {t}
        </Button>
      ))}
      <Button
        variant="ghost"
        size="sm"
        className="text-xs h-7 px-2 text-muted-foreground"
        onClick={onCancel}
        type="button"
      >
        Cancel
      </Button>
    </div>
  );
}

// ── Status legend ───────────────────────────────────────────────────────────

function StatusLegend() {
  const items = [
    { phase: "Running", dot: "bg-blue-500 animate-pulse" },
    { phase: "Serving", dot: "bg-violet-500 animate-pulse" },
    {
      phase: "AwaitingDelegate",
      dot: "bg-amber-500 animate-pulse",
      label: "Awaiting",
    },
    { phase: "Succeeded", dot: "bg-green-500" },
    { phase: "Failed", dot: "bg-red-500" },
  ];
  return (
    <div className="flex items-center gap-3 text-[10px] text-muted-foreground">
      {items.map((it) => (
        <span key={it.phase} className="flex items-center gap-1">
          <span className={`h-1.5 w-1.5 rounded-full ${it.dot}`} />
          {it.label || it.phase}
        </span>
      ))}
    </div>
  );
}

// ══════════════════════════════════════════════════════════════════════════════
// Per-pack canvas (used on persona detail Workflow tab)
// ══════════════════════════════════════════════════════════════════════════════

interface EnsembleCanvasProps {
  pack: Ensemble;
}

export function EnsembleCanvas({ pack }: EnsembleCanvasProps) {
  useRunEventInvalidation();
  const { data: runs } = useRuns();
  const { data: models } = useModels();
  const patchMutation = usePatchEnsembleRelationships();
  const relationships = pack.spec.relationships || [];
  const personas = pack.spec.agentConfigs || [];

  const [pendingConnection, setPendingConnection] = useState<Connection | null>(
    null,
  );
  const [dirty, setDirty] = useState(false);
  const [selectedEdge, setSelectedEdge] = useState<string | null>(null);
  const [showAddProvider, setShowAddProvider] = useState(false);

  const modelMap = useMemo(() => {
    const m = new Map<string, Model>();
    for (const model of models || []) m.set(model.metadata.name, model);
    return m;
  }, [models]);

  const runPhaseMap = useMemo(
    () => buildRunPhaseMap(runs, pack.status?.installedAgentConfigs),
    [runs, pack.status?.installedAgentConfigs],
  );

  const initialNodes = useMemo(() => {
    // Derive provider/model nodes from ensemble data.
    const provResult = buildProviderNodesAndEdges(pack, modelMap, personas, 0, "");
    const hasProviders = provResult.nodes.length > 0;
    const yOffset = hasProviders ? 140 : 0;

    const nodes: Node<AgentConfigNodeData | ModelNodeData | ProviderNodeData>[] =
      layoutNodes(personas, relationships, 0, yOffset);
    const sharedMemEnabled = pack.spec.sharedMemory?.enabled ?? false;
    for (const node of nodes) {
      node.data.hasSharedMemory = sharedMemEnabled;
      const ip = pack.status?.installedAgentConfigs?.find(
        (p) => p.name === node.id,
      );
      if (ip) node.data.agentName = ip.agentName;
      const status = runPhaseMap.get(node.id);
      if (status) {
        node.data.runPhase = status.phase;
        node.data.runTask = status.task;
      }
    }

    // Add provider/model nodes above personas.
    nodes.push(...provResult.nodes);

    return nodes;
  }, [
    personas,
    relationships,
    pack,
    pack.spec.sharedMemory?.enabled,
    pack.status?.installedAgentConfigs,
    runPhaseMap,
    modelMap,
  ]);

  const initialEdges = useMemo(() => {
    const edges = buildEdges(relationships);
    const provResult = buildProviderNodesAndEdges(pack, modelMap, personas, 0, "");
    edges.push(...provResult.edges);
    return edges;
  }, [relationships, pack, modelMap, personas]);

  const [nodes, setNodes, onNodesChange] = useNodesState(initialNodes);
  const [edges, setEdges, onEdgesChange] = useEdgesState(initialEdges);

  // Sync run status into nodes when polling data changes — preserves
  // user-dragged positions while updating phase/task indicators.
  useEffect(() => {
    setNodes((prev) =>
      prev.map((node) => {
        const personaName = node.id;
        const status = runPhaseMap.get(personaName);
        const newPhase = status?.phase;
        const newTask = status?.task;
        if (node.data.runPhase === newPhase && node.data.runTask === newTask) {
          return node; // no change — keep reference stable
        }
        return {
          ...node,
          data: { ...node.data, runPhase: newPhase, runTask: newTask },
        };
      }),
    );
  }, [runPhaseMap, setNodes]);

  const onConnect = useCallback(
    (connection: Connection) => {
      if (!connection.source || !connection.target) return;
      if (connection.source === connection.target) return;
      if (
        edges.some(
          (e) =>
            e.source === connection.source && e.target === connection.target,
        )
      )
        return;
      setPendingConnection(connection);
    },
    [edges],
  );

  const handleEdgeTypeSelect = useCallback(
    (relType: string) => {
      if (!pendingConnection?.source || !pendingConnection?.target) return;
      const id = `e-new-${pendingConnection.source}-${pendingConnection.target}-${Date.now()}`;
      setEdges((eds) =>
        addEdge(
          styledEdge(
            id,
            pendingConnection.source!,
            pendingConnection.target!,
            relType,
          ),
          eds,
        ),
      );
      setPendingConnection(null);
      setDirty(true);
    },
    [pendingConnection, setEdges],
  );

  const handleDeleteSelected = useCallback(() => {
    if (!selectedEdge) return;
    setEdges((eds) => eds.filter((e) => e.id !== selectedEdge));
    setSelectedEdge(null);
    setDirty(true);
  }, [selectedEdge, setEdges]);

  const handleSave = useCallback(() => {
    patchMutation.mutate(
      { name: pack.metadata.name, relationships: edgesToRelationships(edges) },
      { onSuccess: () => setDirty(false) },
    );
  }, [edges, pack.metadata.name, patchMutation]);

  const onEdgeClick = useCallback((_: React.MouseEvent, edge: Edge) => {
    setSelectedEdge((prev) => (prev === edge.id ? null : edge.id));
  }, []);

  const onKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      if ((e.key === "Delete" || e.key === "Backspace") && selectedEdge)
        handleDeleteSelected();
    },
    [selectedEdge, handleDeleteSelected],
  );

  if (personas.length === 0) {
    return (
      <div className="flex items-center justify-center h-[400px] text-sm text-muted-foreground">
        No personas defined in this pack.
      </div>
    );
  }

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-4">
          <p className="text-xs text-muted-foreground">
            <Plus className="h-3 w-3 inline mr-1" />
            Drag from one persona to another to create a relationship.
            {selectedEdge && " Press Delete to remove selected edge."}
          </p>
          <StatusLegend />
        </div>
        <div className="flex items-center gap-2">
          <Button
            variant="outline"
            size="sm"
            onClick={() => setShowAddProvider(true)}
            type="button"
          >
            <Cpu className="h-3.5 w-3.5 mr-1" />
            Add Provider
          </Button>
          {selectedEdge && (
            <Button
              variant="destructive"
              size="sm"
              onClick={handleDeleteSelected}
              type="button"
            >
              <Trash2 className="h-3.5 w-3.5 mr-1" />
              Delete Edge
            </Button>
          )}
          <Button
            size="sm"
            onClick={handleSave}
            disabled={!dirty || patchMutation.isPending}
            type="button"
          >
            <Save className="h-3.5 w-3.5 mr-1" />
            {patchMutation.isPending ? "Saving..." : "Save"}
          </Button>
        </div>
      </div>

      <div
        className="h-[500px] w-full rounded-lg border border-border/40 bg-background relative"
        onKeyDown={onKeyDown}
        tabIndex={0}
      >
        {pendingConnection && (
          <EdgeTypePicker
            onSelect={handleEdgeTypeSelect}
            onCancel={() => setPendingConnection(null)}
          />
        )}
        <ReactFlow
          nodes={nodes}
          edges={edges.map((e) => ({ ...e, selected: e.id === selectedEdge }))}
          onNodesChange={onNodesChange}
          onEdgesChange={onEdgesChange}
          onConnect={onConnect}
          onEdgeClick={onEdgeClick}
          nodeTypes={nodeTypes}
          {...rfDefaults}
        >
          <CanvasShell>{null}</CanvasShell>
        </ReactFlow>
      </div>

      <AddProviderModal
        open={showAddProvider}
        onClose={() => setShowAddProvider(false)}
        onAdd={(result) => {
          const provId = result.modelRef
            ? `model:${result.modelRef}`
            : result.provider;
          const nodeId = `__prov__${provId}`;
          setNodes((prev) => [
            ...prev,
            {
              id: nodeId,
              type: "provider" as const,
              position: { x: 100, y: -160 },
              data: {
                provider: result.provider,
                label: result.label,
                baseURL: result.baseURL,
                isModelRef: !!result.modelRef,
              },
            },
          ]);
          setDirty(true);
        }}
      />
    </div>
  );
}

// ══════════════════════════════════════════════════════════════════════════════
// Global canvas (used on the ensembles list page)
// Shows all enabled packs together with live run status.
// ══════════════════════════════════════════════════════════════════════════════

export function GlobalEnsembleCanvas() {
  useRunEventInvalidation();
  const { data: packs } = useEnsembles();
  const { data: runs } = useRuns();
  const { data: models } = useModels();

  const enabledPacks = useMemo(
    () => (packs || []).filter((p) => p.spec.enabled),
    [packs],
  );

  // Build a lookup map from model name → Model object.
  const modelMap = useMemo(() => {
    const m = new Map<string, Model>();
    for (const model of models || []) m.set(model.metadata.name, model);
    return m;
  }, [models]);

  // Build layout (positions + edges) only when packs change — NOT on run updates.
  const { layoutedNodes, allEdges } = useMemo(() => {
    const nodes: Node<AgentConfigNodeData | ModelNodeData | ProviderNodeData>[] = [];
    const edges: Edge[] = [];

    const packGapX = 50;
    let currentX = 0;

    for (const pack of enabledPacks) {
      const personas = pack.spec.agentConfigs || [];
      const relationships = pack.spec.relationships || [];
      const prefix = pack.metadata.name;

      // Derive provider/model nodes for this pack.
      const provResult = buildProviderNodesAndEdges(pack, modelMap, personas, currentX, prefix);
      const hasProviders = provResult.nodes.length > 0;
      const yOffset = hasProviders ? 140 : 0;

      const packNodes = layoutNodes(
        personas,
        relationships,
        currentX,
        yOffset,
        prefix,
      );

      const sharedMemoryEnabled = pack.spec.sharedMemory?.enabled ?? false;
      for (const node of packNodes) {
        node.data.packName = pack.metadata.name;
        node.data.hasSharedMemory = sharedMemoryEnabled;
        const personaName = node.id.split("/")[1] || node.id;
        const ip = pack.status?.installedAgentConfigs?.find(
          (p) => p.name === personaName,
        );
        if (ip) node.data.agentName = ip.agentName;
      }

      nodes.push(...provResult.nodes);
      nodes.push(...packNodes);
      edges.push(...provResult.edges);
      edges.push(...buildEdges(relationships, prefix));

      const cols = Math.max(2, Math.ceil(Math.sqrt(personas.length)));
      currentX += cols * 260 + packGapX;
    }

    return { layoutedNodes: nodes, allEdges: edges };
  }, [enabledPacks, modelMap]);

  // Merge run status into nodes without recalculating positions.
  const allNodes = useMemo(() => {
    const runPhaseMaps = new Map<
      string,
      Map<string, { phase?: string; task?: string }>
    >();
    for (const pack of enabledPacks) {
      runPhaseMaps.set(
        pack.metadata.name,
        buildRunPhaseMap(runs, pack.status?.installedAgentConfigs),
      );
    }
    return layoutedNodes.map((node) => {
      // Model nodes don't have run status — pass through unchanged.
      if (node.type === "model") return node;
      const packName = (node.data as AgentConfigNodeData).packName || "";
      const personaName = node.id.split("/")[1] || node.id;
      const status = runPhaseMaps.get(packName)?.get(personaName);
      if (!status) return node;
      return {
        ...node,
        data: { ...node.data, runPhase: status.phase, runTask: status.task },
      };
    });
  }, [layoutedNodes, runs, enabledPacks]);

  if (enabledPacks.length === 0) {
    return (
      <div className="flex items-center justify-center h-[500px] text-sm text-muted-foreground">
        No enabled ensembles. Enable an ensemble to see it on the canvas.
      </div>
    );
  }

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-4">
          <p className="text-xs text-muted-foreground">
            {enabledPacks.length} active pack
            {enabledPacks.length !== 1 ? "s" : ""} &middot; {allNodes.length}{" "}
            personas
          </p>
          <StatusLegend />
        </div>
      </div>
      <div className="h-[600px] w-full rounded-lg border border-border/40 bg-background">
        <ReactFlow
          nodes={allNodes}
          edges={allEdges}
          nodeTypes={nodeTypes}
          nodesDraggable
          nodesConnectable={false}
          {...rfDefaults}
        >
          <CanvasShell>{null}</CanvasShell>
        </ReactFlow>
      </div>
    </div>
  );
}

// ══════════════════════════════════════════════════════════════════════════════
// Dashboard widget canvas (compact, with pack selector dropdown)
// ══════════════════════════════════════════════════════════════════════════════

export function DashboardEnsembleCanvas() {
  useRunEventInvalidation();
  const { data: packs } = useEnsembles();
  const { data: runs } = useRuns();
  const [selectedPack, setSelectedPack] = useState<string>("__all__");

  const enabledPacks = useMemo(
    () => (packs || []).filter((p) => p.spec.enabled),
    [packs],
  );

  // Filter to selected pack or show all
  const visiblePacks = useMemo(
    () =>
      selectedPack === "__all__"
        ? enabledPacks
        : enabledPacks.filter((p) => p.metadata.name === selectedPack),
    [enabledPacks, selectedPack],
  );

  // Build layout only when packs change — NOT on run updates.
  const { layoutedNodes: dashLayoutNodes, allEdges } = useMemo(() => {
    const nodes: Node<AgentConfigNodeData>[] = [];
    const edges: Edge[] = [];
    let currentX = 0;

    for (const pack of visiblePacks) {
      const personas = pack.spec.agentConfigs || [];
      const relationships = pack.spec.relationships || [];
      const prefix = pack.metadata.name;

      const packNodes = layoutNodes(
        personas,
        relationships,
        currentX,
        0,
        prefix,
      );
      const sharedMemEnabled = pack.spec.sharedMemory?.enabled ?? false;
      for (const node of packNodes) {
        node.data.packName =
          visiblePacks.length > 1 ? pack.metadata.name : undefined;
        node.data.hasSharedMemory = sharedMemEnabled;
        const personaName = node.id.split("/")[1] || node.id;
        const ip = pack.status?.installedAgentConfigs?.find(
          (p) => p.name === personaName,
        );
        if (ip) node.data.agentName = ip.agentName;
      }

      nodes.push(...packNodes);
      edges.push(...buildEdges(relationships, prefix));
      const cols = Math.max(2, Math.ceil(Math.sqrt(personas.length)));
      currentX += cols * 260 + 50;
    }

    return { layoutedNodes: nodes, allEdges: edges };
  }, [visiblePacks]);

  // Merge run status without recalculating positions.
  const allNodes = useMemo(() => {
    const runPhaseMaps = new Map<
      string,
      Map<string, { phase?: string; task?: string }>
    >();
    for (const pack of visiblePacks) {
      runPhaseMaps.set(
        pack.metadata.name,
        buildRunPhaseMap(runs, pack.status?.installedAgentConfigs),
      );
    }
    return dashLayoutNodes.map((node) => {
      const packName = node.data.packName || "";
      const personaName = node.id.split("/")[1] || node.id;
      const status = runPhaseMaps.get(packName)?.get(personaName);
      if (!status) return node;
      return {
        ...node,
        data: { ...node.data, runPhase: status.phase, runTask: status.task },
      };
    });
  }, [dashLayoutNodes, runs, visiblePacks]);

  if (enabledPacks.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center h-full text-sm text-muted-foreground gap-2">
        <p>No enabled ensembles</p>
        <p className="text-xs">Enable an ensemble to see the team canvas</p>
      </div>
    );
  }

  return (
    <div className="flex flex-col h-full">
      {/* Header: pack selector + legend */}
      <div className="flex items-center justify-between px-1 pb-2 shrink-0">
        <select
          value={selectedPack}
          onChange={(e) => setSelectedPack(e.target.value)}
          className="text-xs bg-transparent border border-border/40 rounded px-2 py-1 text-foreground focus:outline-none focus:ring-1 focus:ring-ring"
        >
          <option value="__all__">All Packs ({enabledPacks.length})</option>
          {enabledPacks.map((p) => (
            <option key={p.metadata.name} value={p.metadata.name}>
              {p.metadata.name} ({p.spec.agentConfigs?.length ?? 0})
            </option>
          ))}
        </select>
        <StatusLegend />
      </div>
      {/* Canvas */}
      <div className="flex-1 min-h-0 rounded-lg border border-border/40 bg-background">
        <ReactFlow
          nodes={allNodes}
          edges={allEdges}
          nodeTypes={nodeTypes}
          nodesDraggable
          nodesConnectable={false}
          {...rfDefaults}
        >
          <Background gap={20} size={1} color="#ffffff08" />
          <Controls
            showInteractive={false}
            className="!bg-card !border-border/40 !shadow-md [&>button]:!bg-card [&>button]:!border-border/40 [&>button]:!text-muted-foreground [&>button:hover]:!bg-white/5"
          />
        </ReactFlow>
      </div>
    </div>
  );
}
