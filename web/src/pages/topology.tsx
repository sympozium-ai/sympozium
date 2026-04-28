/**
 * TopologyPage — bird's-eye ReactFlow canvas showing the full cluster topology:
 * K8s nodes + providers, deployed models, ensembles + agents, and gateway routes.
 */

import { useMemo, useCallback, useRef, useEffect, useState } from "react";
import {
  ReactFlow,
  Background,
  Controls,
  MiniMap,
  type Node,
  type Edge,
  type NodeProps,
  Handle,
  Position,
  MarkerType,
  useNodesState,
  useEdgesState,
  useReactFlow,
  ReactFlowProvider,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { Badge } from "@/components/ui/badge";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from "@/components/ui/dialog";
import {
  useAgents,
  useRuns,
  useEnsembles,
  useModels,
  useGatewayConfig,
} from "@/hooks/use-api";
import { useProviderNodes } from "@/hooks/use-provider-nodes";
import {
  Server,
  Cpu,
  Globe,
  Users,
  Activity,
  Radio,
  User,
  Lock,
  Unlock,
  RotateCcw,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import type {
  Ensemble,
  Model,
  ProviderNode,
  NodeProvider,
  GatewayConfigResponse,
} from "@/lib/api";
import { Link } from "react-router-dom";

// ── Custom node components ────────────────────────────────────────────────────

function K8sNodeNode({ data }: NodeProps<Node<K8sNodeData>>) {
  return (
    <div className="rounded-lg border border-emerald-500/30 bg-emerald-500/5 px-4 py-3 min-w-[240px] shadow-md cursor-pointer hover:border-emerald-500/50 hover:bg-emerald-500/10 transition-colors">
      <Handle type="source" position={Position.Bottom} className="!bg-emerald-500 !w-2 !h-2" />
      <div className="flex items-center gap-2 mb-2">
        <Server className="h-4 w-4 text-emerald-400" />
        <span className="font-semibold text-sm text-emerald-300">{data.name}</span>
      </div>
      <p className="text-[10px] text-muted-foreground font-mono mb-1">{data.ip}</p>
      {data.providers.length > 0 && (
        <div className="flex flex-wrap gap-1 mt-1">
          {data.providers.map((p) => (
            <Badge
              key={p.name}
              variant="outline"
              className="text-[9px] border-emerald-500/30 text-emerald-400"
            >
              {p.name}
              {p.models.length > 0 && ` (${p.models.length})`}
            </Badge>
          ))}
        </div>
      )}
    </div>
  );
}

function ModelNode({ data }: NodeProps<Node<ModelNodeData>>) {
  const phaseColor =
    data.phase === "Ready"
      ? "text-green-400 border-green-500/30 bg-green-500/5"
      : data.phase === "Failed"
        ? "text-red-400 border-red-500/30 bg-red-500/5"
        : "text-yellow-400 border-yellow-500/30 bg-yellow-500/5";

  return (
    <div className={`rounded-lg border px-4 py-3 min-w-[180px] shadow-md ${phaseColor}`}>
      <Handle type="target" position={Position.Top} className="!bg-violet-500 !w-2 !h-2" />
      <Handle type="source" position={Position.Bottom} className="!bg-violet-500 !w-2 !h-2" />
      <div className="flex items-center gap-2 mb-1">
        <Cpu className="h-4 w-4 text-violet-400" />
        <Link to={`/models/${data.name}?namespace=${data.namespace}`} className="font-semibold text-sm hover:underline">
          {data.name}
        </Link>
      </div>
      <div className="flex items-center gap-2 text-[10px] text-muted-foreground">
        <Badge variant="outline" className="text-[9px] border-violet-500/30 text-violet-400">Pod</Badge>
        <span>{data.serverType || "llama-cpp"}</span>
        {data.gpu > 0 && <span>GPU:{data.gpu}</span>}
        <Badge variant="outline" className="text-[9px]">{data.phase}</Badge>
      </div>
    </div>
  );
}

function EnsembleNode({ data }: NodeProps<Node<EnsembleNodeData>>) {
  const active = data.enabled;
  return (
    <Link to={`/ensembles/${data.name}`} className="block">
      <div className={`rounded-lg border px-3 py-2 shadow-md transition-colors cursor-pointer ${
        active
          ? "border-blue-500/30 bg-blue-500/5 hover:border-blue-500/50 hover:bg-blue-500/10"
          : "border-border/20 bg-muted/5 opacity-50 hover:opacity-70"
      }`}>
        <Handle type="target" position={Position.Top} className={active ? "!bg-blue-500 !w-2 !h-2" : "!bg-muted-foreground/40 !w-2 !h-2"} />
        <Handle type="source" position={Position.Bottom} className={active ? "!bg-blue-500 !w-2 !h-2" : "!bg-muted-foreground/40 !w-2 !h-2"} />
        <div className="flex items-center gap-2">
          <Users className={`h-3.5 w-3.5 shrink-0 ${active ? "text-blue-400" : "text-muted-foreground/40"}`} />
          <span className={`font-medium text-xs truncate max-w-[140px] ${active ? "text-blue-300" : "text-muted-foreground/60"}`}>
            {data.name}
          </span>
          <span className="text-[9px] text-muted-foreground/40 shrink-0">
            {data.personas.length}
          </span>
          {active && (
            <span className="h-1.5 w-1.5 rounded-full bg-green-500 shrink-0" />
          )}
          {data.runningCount > 0 && (
            <span className="text-[9px] text-cyan-400 shrink-0">{data.runningCount} running</span>
          )}
        </div>
      </div>
    </Link>
  );
}

/** Wrapper node that renders as a dashed group box around active ensemble + personas. */
function EnsembleGroupNode({ data }: NodeProps<Node<EnsembleGroupNodeData>>) {
  return (
    <div
      className="rounded-xl border border-dashed border-blue-500/20 bg-blue-500/[0.02]"
      style={{ width: data.width, height: data.height }}
    >
      <Handle type="target" position={Position.Top} className="!bg-blue-500 !w-2 !h-2" />
      <Handle type="source" position={Position.Bottom} className="!bg-blue-500 !w-2 !h-2" />
    </div>
  );
}

function PersonaNode({ data }: NodeProps<Node<PersonaNodeData>>) {
  const dotClass = data.runPhase === "Running" || data.runPhase === "Serving"
    ? "bg-blue-500 animate-pulse"
    : data.runPhase === "Succeeded"
      ? "bg-green-500"
      : data.runPhase === "Failed"
        ? "bg-red-500"
        : "bg-muted-foreground/40";

  return (
    <div className="rounded-md border border-border/50 bg-card px-3 py-1.5 shadow-sm min-w-[120px]">
      <Handle type="target" position={Position.Top} className="!bg-blue-400 !w-1.5 !h-1.5" />
      <Handle type="source" position={Position.Bottom} className="!bg-blue-400 !w-1.5 !h-1.5" />
      <div className="flex items-center gap-1.5">
        <User className="h-3 w-3 text-blue-400 shrink-0" />
        <span className="text-[11px] font-medium truncate">{data.displayName || data.name}</span>
        {data.runPhase && (
          <span className={`h-1.5 w-1.5 rounded-full shrink-0 ${dotClass}`} />
        )}
      </div>
    </div>
  );
}

function CloudProviderNode({ data }: NodeProps<Node<CloudProviderNodeData>>) {
  return (
    <div className="rounded-lg border border-orange-500/30 bg-orange-500/5 px-3 py-2 shadow-md">
      <Handle type="target" position={Position.Top} className="!bg-orange-500 !w-2 !h-2" />
      <Handle type="source" position={Position.Bottom} className="!bg-orange-500 !w-2 !h-2" />
      <div className="flex items-center gap-2">
        <Globe className="h-3.5 w-3.5 text-orange-400" />
        <span className="font-medium text-xs text-orange-300">{data.label}</span>
        <Badge variant="outline" className="text-[9px] border-orange-500/30 text-orange-400">API</Badge>
      </div>
    </div>
  );
}

function GatewayNode({ data }: NodeProps<Node<GatewayNodeData>>) {
  const configured = data.ready || (data.phase && data.phase !== "Not Configured");
  return (
    <div className={`rounded-lg border px-4 py-3 min-w-[200px] shadow-md ${
      configured
        ? "border-amber-500/30 bg-amber-500/5"
        : "border-border/20 bg-muted/5 opacity-50"
    }`}>
      <Handle type="source" position={Position.Bottom} className={configured ? "!bg-amber-500 !w-2 !h-2" : "!bg-muted-foreground/40 !w-2 !h-2"} />
      <div className="flex items-center gap-2 mb-1">
        <Globe className={`h-4 w-4 ${configured ? "text-amber-400" : "text-muted-foreground/40"}`} />
        <span className={`font-semibold text-sm ${configured ? "text-amber-300" : "text-muted-foreground/60"}`}>Gateway</span>
        <Badge
          variant="outline"
          className={`text-[9px] ${data.ready ? "border-green-500/30 text-green-400" : "border-muted text-muted-foreground"}`}
        >
          {data.ready ? "Ready" : data.phase || "Not Configured"}
        </Badge>
      </div>
      {data.address && (
        <p className="text-[10px] text-muted-foreground font-mono">{data.address}</p>
      )}
      {data.routes.length > 0 && (
        <div className="mt-1.5 flex flex-wrap gap-1">
          {data.routes.map((r) => (
            <Badge
              key={r}
              variant="outline"
              className="text-[9px] border-amber-500/20 text-amber-400"
            >
              <Radio className="h-2.5 w-2.5 mr-0.5" />
              {r}
            </Badge>
          ))}
        </div>
      )}
    </div>
  );
}

// ── Node data types ───────────────────────────────────────────────────────────

interface K8sNodeData {
  name: string;
  ip: string;
  providers: { name: string; models: string[] }[];
  [key: string]: unknown;
}

interface ModelNodeData {
  name: string;
  namespace: string;
  phase: string;
  serverType: string;
  gpu: number;
  placedNode?: string;
  [key: string]: unknown;
}

interface EnsembleNodeData {
  name: string;
  description: string;
  enabled: boolean;
  personas: string[];
  runningCount: number;
  [key: string]: unknown;
}

interface EnsembleGroupNodeData {
  width: number;
  height: number;
  [key: string]: unknown;
}

interface PersonaNodeData {
  name: string;
  displayName: string;
  runPhase?: string;
  [key: string]: unknown;
}

interface CloudProviderNodeData {
  provider: string;
  label: string;
  [key: string]: unknown;
}

interface GatewayNodeData {
  ready: boolean;
  phase: string;
  address: string;
  routes: string[];
  [key: string]: unknown;
}

const nodeTypes = {
  k8sNode: K8sNodeNode,
  model: ModelNode,
  ensemble: EnsembleNode,
  ensembleGroup: EnsembleGroupNode,
  persona: PersonaNode,
  cloudProvider: CloudProviderNode,
  gateway: GatewayNode,
};

// ── Layout ────────────────────────────────────────────────────────────────────

const COL_GAP = 340;
const ROW_GAP = 180;

/** Center a layer of N items horizontally around x=0 with COL_GAP spacing. */
function centerX(count: number, index: number): number {
  const totalWidth = (count - 1) * COL_GAP;
  return -totalWidth / 2 + index * COL_GAP;
}

/** Lay out items in a grid (maxCols wide), centered horizontally, returning {x, y} offset from layerY. */
function gridPosition(
  index: number,
  total: number,
  maxCols: number,
  rowHeight = 60,
): { dx: number; dy: number } {
  const cols = Math.min(total, maxCols);
  const row = Math.floor(index / cols);
  const itemsInRow = Math.min(cols, total - row * cols);
  const col = index % cols;
  return {
    dx: centerX(itemsInRow, col),
    dy: row * rowHeight,
  };
}

/** How many rows a grid of `total` items with `maxCols` columns occupies. */
function gridRows(total: number, maxCols: number): number {
  return Math.ceil(total / Math.min(total, maxCols));
}

/** Build a stable fingerprint from entity IDs so we know when layout needs recomputing. */
function entityFingerprint(
  providerNodes: ProviderNode[],
  models: Model[],
  ensembles: Ensemble[],
  hasGateway: boolean,
): string {
  const parts = [
    providerNodes.map((n) => n.nodeName).sort().join(","),
    models.map((m) => m.metadata.name).sort().join(","),
    ensembles.map((e) => e.metadata.name).sort().join(","),
    hasGateway ? "gw" : "",
  ];
  return parts.join("|");
}

interface RunPhaseMap {
  [agentName: string]: string; // latest run phase per stamped agent name
}

function buildTopology(
  providerNodes: ProviderNode[],
  models: Model[],
  ensembles: Ensemble[],
  gateway: GatewayConfigResponse | undefined,
  runningByEnsemble: Record<string, number>,
  webEndpointAgents: string[],
  runPhases: RunPhaseMap,
): { nodes: Node[]; edges: Edge[] } {
  const nodes: Node[] = [];
  const edges: Edge[] = [];

  // Collect unique providers from ensembles (from authRefs or inferred from baseURL).
  const PROVIDER_LABELS: Record<string, string> = {
    openai: "OpenAI", anthropic: "Anthropic", "azure-openai": "Azure OpenAI",
    bedrock: "AWS Bedrock", "lm-studio": "LM Studio", ollama: "Ollama",
    "llama-server": "llama-server", unsloth: "Unsloth", custom: "Custom",
    vllm: "vLLM", tgi: "TGI",
  };

  // Infer provider from baseURL patterns.
  function inferProvider(baseURL: string): string | null {
    if (!baseURL) return null;
    if (baseURL.includes("/proxy/lm-studio/") || baseURL.includes(":1234")) return "lm-studio";
    if (baseURL.includes("/proxy/ollama/") || baseURL.includes(":11434")) return "ollama";
    if (baseURL.includes("/proxy/vllm/") || baseURL.includes(":8000")) return "vllm";
    if (baseURL.includes("/proxy/llama-cpp/") || baseURL.includes(":8080/v1")) return "llama-server";
    if (baseURL.includes("openai.com")) return "openai";
    if (baseURL.includes("anthropic.com")) return "anthropic";
    return null;
  }

  // Track which provider each ensemble uses, and whether providers are local (on a node).
  const ensProviders = new Map<string, string>(); // provider key → label
  const ensProviderMap = new Map<string, string>(); // ensemble name → provider key
  const LOCAL_PROVIDERS = new Set(["lm-studio", "ollama", "llama-server", "vllm", "unsloth"]);

  for (const ens of ensembles) {
    // From authRefs.
    for (const ref of ens.spec.authRefs || []) {
      if (ref.provider) {
        ensProviders.set(ref.provider, PROVIDER_LABELS[ref.provider] || ref.provider);
        ensProviderMap.set(ens.metadata.name, ref.provider);
      }
    }
    // From baseURL if no authRef matched.
    if (!ensProviderMap.has(ens.metadata.name) && ens.spec.baseURL) {
      // Skip model endpoints (they're handled separately as Model nodes).
      const isModelEndpoint = models.some(
        (m) => m.status?.endpoint && ens.spec.baseURL?.includes(m.status.endpoint.replace("/v1", "")),
      );
      if (!isModelEndpoint) {
        const inferred = inferProvider(ens.spec.baseURL);
        if (inferred) {
          ensProviders.set(inferred, PROVIDER_LABELS[inferred] || inferred);
          ensProviderMap.set(ens.metadata.name, inferred);
        }
      }
    }
  }

  let layerY = 0;

  // ── Layer 0: Gateway (external ingress — top of topology) ───────────────
  if (gateway) {
    nodes.push({
      id: "gateway",
      type: "gateway",
      position: { x: centerX(1, 0), y: layerY },
      data: {
        ready: gateway.ready,
        phase: gateway.phase || "",
        address: gateway.address || "",
        routes: webEndpointAgents,
      },
    });
    layerY += 100;
  }

  // ── Layer 1: K8s Nodes ──────────────────────────────────────────────────
  if (providerNodes.length > 0) {
    providerNodes.forEach((pn, i) => {
      nodes.push({
        id: `node-${pn.nodeName}`,
        type: "k8sNode",
        position: { x: centerX(providerNodes.length, i), y: layerY },
        data: {
          name: pn.nodeName,
          ip: pn.nodeIP,
          providers: pn.providers.map((p) => ({
            name: p.name,
            models: p.models,
          })),
        },
      });
    });
    layerY += ROW_GAP;
  }

  // ── Layer 2: Providers ─────────────────────────────────────────────────
  // Split into local (discovered on node → positioned below it) and cloud (standalone).
  const localProvEntries = Array.from(ensProviders.entries()).filter(([p]) => LOCAL_PROVIDERS.has(p));
  const cloudProvEntries = Array.from(ensProviders.entries()).filter(([p]) => !LOCAL_PROVIDERS.has(p));

  // Local providers — below the K8s node, connected to it.
  if (localProvEntries.length > 0) {
    localProvEntries.forEach(([prov, label], i) => {
      nodes.push({
        id: `cp-${prov}`,
        type: "cloudProvider",
        position: { x: centerX(localProvEntries.length, i), y: layerY },
        data: { provider: prov, label },
      });

      // Edge: K8s node → local provider.
      for (const pn of providerNodes) {
        if (pn.providers.some((dp) => dp.name === prov || dp.name === prov.replace("-", ""))) {
          edges.push({
            id: `e-node-${pn.nodeName}-cp-${prov}`,
            source: `node-${pn.nodeName}`,
            target: `cp-${prov}`,
            style: { stroke: "#f97316", strokeWidth: 1.5 },
            markerEnd: { type: MarkerType.ArrowClosed, color: "#f97316" },
          });
        }
      }
    });
    layerY += 90;
  }

  // Cloud/external providers — standalone, positioned to the right.
  if (cloudProvEntries.length > 0) {
    // Place cloud providers at the same Y as the K8s node but offset right.
    const cloudBaseX = (providerNodes.length > 0 ? providerNodes.length : 1) * COL_GAP / 2 + 200;
    const cloudY = providerNodes.length > 0
      ? layerY - 90 - ROW_GAP + 30 // align with the node layer
      : layerY;
    cloudProvEntries.forEach(([prov, label], i) => {
      nodes.push({
        id: `cp-${prov}`,
        type: "cloudProvider",
        position: { x: cloudBaseX + i * 200, y: cloudY },
        data: { provider: prov, label },
      });
    });
  }

  // ── Layer 3: Models ─────────────────────────────────────────────────────
  if (models.length > 0) {
    models.forEach((m, i) => {
      const modelId = `model-${m.metadata.name}`;
      nodes.push({
        id: modelId,
        type: "model",
        position: { x: centerX(models.length, i), y: layerY },
        data: {
          name: m.metadata.name,
          namespace: m.metadata.namespace,
          phase: m.status?.phase || "Pending",
          serverType: m.spec.inference?.serverType || "llama-cpp",
          gpu: m.spec.resources?.gpu ?? 0,
          placedNode: m.status?.placedNode,
        },
      });

      // Edge: K8s node → model (runs on)
      if (m.status?.placedNode) {
        const nodeId = `node-${m.status.placedNode}`;
        if (providerNodes.some((pn) => pn.nodeName === m.status?.placedNode)) {
          edges.push({
            id: `e-${modelId}-${nodeId}`,
            source: nodeId,
            target: modelId,
            style: { stroke: "#8b5cf6", strokeWidth: 1.5 },
            markerEnd: { type: MarkerType.ArrowClosed, color: "#8b5cf6" },
            animated: m.status?.phase === "Loading",
            label: "runs on",
            labelStyle: { fontSize: 9, fill: "#9ca3af" },
            labelBgStyle: { fill: "#09090b", fillOpacity: 0.8 },
            labelBgPadding: [4, 2] as [number, number],
          });
        }
      } else if (providerNodes.length === 1) {
        // Single-node cluster: connect model to the only node
        edges.push({
          id: `e-${modelId}-node-${providerNodes[0].nodeName}`,
          source: `node-${providerNodes[0].nodeName}`,
          target: modelId,
          style: { stroke: "#8b5cf680", strokeWidth: 1 },
          markerEnd: { type: MarkerType.ArrowClosed, color: "#8b5cf680" },
        });
      }
    });
    layerY += ROW_GAP;
  }

  // ── Layer 3: Ensembles ───────────────────────────────────────────────
  // Split into active (expanded with personas) and inactive (compact grid).
  const PERSONA_COL_W = 210;
  const activeEnsembles = ensembles.filter((e) => e.spec.enabled);
  const inactiveEnsembles = ensembles.filter((e) => !e.spec.enabled);

  // Helper: add model/provider edges for an ensemble.
  function addEnsembleEdges(ensId: string, ens: Ensemble, active = true) {
    const edgeColor = active ? "#6366f1" : "#4b5563";
    const provEdgeColor = active ? "#f97316" : "#4b5563";
    if (ens.spec.modelRef) {
      const modelId = `model-${ens.spec.modelRef}`;
      if (models.some((m) => m.metadata.name === ens.spec.modelRef)) {
        edges.push({
          id: `e-${ensId}-${modelId}`,
          source: modelId,
          target: ensId,
          style: { stroke: edgeColor, strokeWidth: active ? 1.5 : 1, strokeDasharray: "4 3" },
          markerEnd: { type: MarkerType.ArrowClosed, color: edgeColor },
          animated: active,
          label: "inference",
          labelStyle: { fontSize: 9, fill: "#9ca3af" },
          labelBgStyle: { fill: "#09090b", fillOpacity: 0.8 },
          labelBgPadding: [4, 2] as [number, number],
        });
      }
    } else if (ens.spec.baseURL) {
      const matchedModel = models.find(
        (m) => m.status?.endpoint && ens.spec.baseURL?.includes(m.status.endpoint.replace("/v1", "")),
      );
      if (matchedModel) {
        edges.push({
          id: `e-${ensId}-model-${matchedModel.metadata.name}`,
          source: `model-${matchedModel.metadata.name}`,
          target: ensId,
          style: { stroke: edgeColor, strokeWidth: active ? 1.5 : 1, strokeDasharray: "4 3" },
          markerEnd: { type: MarkerType.ArrowClosed, color: edgeColor },
          label: "inference",
          labelStyle: { fontSize: 9, fill: "#9ca3af" },
          labelBgStyle: { fill: "#09090b", fillOpacity: 0.8 },
          labelBgPadding: [4, 2] as [number, number],
        });
      }
    }
    // Edge: provider → ensemble.
    const ensProv = ensProviderMap.get(ens.metadata.name);
    if (ensProv) {
      edges.push({
        id: `e-cp-${ensProv}-${ensId}`,
        source: `cp-${ensProv}`,
        target: ensId,
        style: { stroke: provEdgeColor, strokeWidth: active ? 1.5 : 1, strokeDasharray: "4 3" },
        markerEnd: { type: MarkerType.ArrowClosed, color: provEdgeColor },
        label: "inference",
        labelStyle: { fontSize: 9, fill: "#9ca3af" },
        labelBgStyle: { fill: "#09090b", fillOpacity: 0.8 },
        labelBgPadding: [4, 2] as [number, number],
      });
    }
  }

  // 3a. Active ensembles — each gets a group box with personas expanded inside.
  // Pre-compute widths so we can lay them out without overlapping.
  const ACTIVE_GAP = 60; // px between group boxes
  const PERSONA_MAX_COLS = 2;
  const PERSONA_ROW_H = 70;
  const activeWidths = activeEnsembles.map((ens) => {
    const n = (ens.spec.agentConfigs || []).length;
    const cols = Math.min(n, PERSONA_MAX_COLS);
    // 80px padding each side for persona node width overhang
    return Math.max(cols * PERSONA_COL_W + 160, 280);
  });
  const totalActiveW = activeWidths.reduce((s, w) => s + w, 0) + Math.max(0, activeWidths.length - 1) * ACTIVE_GAP;
  let activeX = -totalActiveW / 2; // start from left edge of centered row

  activeEnsembles.forEach((ens, i) => {
    const ensId = `ens-${ens.metadata.name}`;
    const configs = ens.spec.agentConfigs || [];
    const personas = configs.map((p) => p.displayName || p.name);

    const groupW = activeWidths[i];
    const personaRows = configs.length > 0 ? Math.ceil(configs.length / PERSONA_MAX_COLS) : 0;
    const groupH = personaRows > 0 ? 60 + personaRows * PERSONA_ROW_H + 10 : 50;
    const groupLeft = activeX;
    activeX += groupW + ACTIVE_GAP;

    // Group box — parent node; ensemble chip + personas are children.
    const groupId = `${ensId}-group`;
    nodes.push({
      id: groupId,
      type: "ensembleGroup",
      position: { x: groupLeft, y: layerY },
      data: { width: groupW, height: groupH },
      zIndex: -1,
    } as Node);

    // Ensemble chip (child of group — position relative to group).
    nodes.push({
      id: ensId,
      type: "ensemble",
      position: { x: groupW / 2, y: 15 },
      parentId: groupId,
      data: {
        name: ens.metadata.name,
        description: ens.spec.description || "",
        enabled: true,
        personas,
        runningCount: runningByEnsemble[ens.metadata.name] || 0,
      },
    });
    addEnsembleEdges(ensId, ens);

    // Persona sub-nodes below the chip, inside the group box.
    if (configs.length > 0) {
      const personaBaseY = 60;
      configs.forEach((cfg, pi) => {
        const pid = `${ensId}-p-${cfg.name}`;
        const stampedName = `${ens.metadata.name}-${cfg.name}`;
        const pRow = Math.floor(pi / PERSONA_MAX_COLS);
        const pCol = pi % PERSONA_MAX_COLS;
        const itemsInRow = Math.min(PERSONA_MAX_COLS, configs.length - pRow * PERSONA_MAX_COLS);
        const rowW = (itemsInRow - 1) * PERSONA_COL_W;
        const pxRel = (groupW - rowW) / 2 + pCol * PERSONA_COL_W;
        const pyRel = personaBaseY + pRow * PERSONA_ROW_H;
        nodes.push({
          id: pid,
          type: "persona",
          position: { x: pxRel, y: pyRel },
          parentId: groupId,
          data: {
            name: cfg.name,
            displayName: cfg.displayName || cfg.name,
            runPhase: runPhases[stampedName],
          },
        });
        edges.push({
          id: `e-${ensId}-${pid}`,
          source: ensId,
          target: pid,
          style: { stroke: "#3b82f640", strokeWidth: 1 },
        });
      });

      // Relationship edges between personas.
      for (const rel of ens.spec.relationships || []) {
        const srcId = `${ensId}-p-${rel.source}`;
        const tgtId = `${ensId}-p-${rel.target}`;
        const relColor =
          rel.type === "delegation" ? "#60a5fa"
            : rel.type === "sequential" ? "#fbbf24"
              : "#9ca3af";
        edges.push({
          id: `e-rel-${ensId}-${rel.source}-${rel.target}`,
          source: srcId,
          target: tgtId,
          style: rel.type === "delegation"
            ? { stroke: relColor, strokeWidth: 1.5 }
            : { stroke: relColor, strokeWidth: 1, strokeDasharray: "4 3" },
          markerEnd: { type: MarkerType.ArrowClosed, color: relColor },
          label: rel.type,
          labelStyle: { fontSize: 8, fill: "#9ca3af" },
          labelBgStyle: { fill: "#09090b", fillOpacity: 0.8 },
          labelBgPadding: [4, 2] as [number, number],
          animated: rel.type === "delegation",
        });
      }
    }
  });
  if (activeEnsembles.length > 0) {
    const maxGroupH = activeEnsembles.reduce((max, ens, i) => {
      const rows = Math.ceil((ens.spec.agentConfigs || []).length / PERSONA_MAX_COLS);
      const h = rows > 0 ? 60 + rows * PERSONA_ROW_H + 10 : 50;
      return Math.max(max, h);
    }, 0);
    layerY += maxGroupH + 50;
  }

  // 3b. Inactive ensembles — faded, spread wider to the sides.
  const INACTIVE_MAX_COLS = 3;
  const INACTIVE_COL_GAP = 300;
  if (inactiveEnsembles.length > 0) {
    inactiveEnsembles.forEach((ens, i) => {
      const ensId = `ens-${ens.metadata.name}`;
      const personas = (ens.spec.agentConfigs || []).map((p) => p.displayName || p.name);
      const cols = Math.min(inactiveEnsembles.length, INACTIVE_MAX_COLS);
      const row = Math.floor(i / cols);
      const col = i % cols;
      const itemsInRow = Math.min(cols, inactiveEnsembles.length - row * cols);
      const totalW = (itemsInRow - 1) * INACTIVE_COL_GAP;
      const dx = -totalW / 2 + col * INACTIVE_COL_GAP;
      const dy = row * 60;

      nodes.push({
        id: ensId,
        type: "ensemble",
        position: { x: dx, y: layerY + dy },
        data: {
          name: ens.metadata.name,
          description: ens.spec.description || "",
          enabled: false,
          personas,
          runningCount: 0,
        },
      });
      addEnsembleEdges(ensId, ens, false);
    });
    layerY += Math.ceil(inactiveEnsembles.length / INACTIVE_MAX_COLS) * 60 + 30;
  }

  // ── Gateway edges (gateway is at top, ensembles/nodes are below) ─────────
  if (gateway) {
    // Gateway → ensembles with web endpoints (traffic flows down).
    webEndpointAgents.forEach((agentName) => {
      const ownerEns = ensembles.find((ens) =>
        (ens.spec.agentConfigs || []).some(
          (p) => `${ens.metadata.name}-${p.name}` === agentName,
        ),
      );
      if (ownerEns) {
        edges.push({
          id: `e-gw-${agentName}`,
          source: "gateway",
          target: `ens-${ownerEns.metadata.name}`,
          style: { stroke: "#f59e0b", strokeWidth: 1.5, strokeDasharray: "6 3" },
          markerEnd: { type: MarkerType.ArrowClosed, color: "#f59e0b" },
          label: "web endpoint",
          labelStyle: { fontSize: 9, fill: "#9ca3af" },
          labelBgStyle: { fill: "#09090b", fillOpacity: 0.8 },
          labelBgPadding: [4, 2] as [number, number],
        });
      }
    });

    // Gateway → first K8s node (ingress enters cluster).
    if (providerNodes.length > 0) {
      edges.push({
        id: "e-gw-node",
        source: "gateway",
        target: `node-${providerNodes[0].nodeName}`,
        style: { stroke: "#f59e0b40", strokeWidth: 1 },
      });
    }
  }

  return { nodes, edges };
}

// ── Inner component (needs ReactFlowProvider above it) ────────────────────────

const TOPO_POSITIONS_KEY = "sympozium_topology_positions";
const TOPO_LOCKED_KEY = "sympozium_topology_locked";

function savePositions(nodes: Node[]) {
  const map: Record<string, { x: number; y: number }> = {};
  for (const n of nodes) {
    map[n.id] = { x: n.position.x, y: n.position.y };
  }
  localStorage.setItem(TOPO_POSITIONS_KEY, JSON.stringify(map));
}

function loadPositions(): Record<string, { x: number; y: number }> | null {
  try {
    const raw = localStorage.getItem(TOPO_POSITIONS_KEY);
    return raw ? JSON.parse(raw) : null;
  } catch {
    return null;
  }
}

function TopologyCanvas() {
  const { data: ensembles } = useEnsembles();
  const { data: models } = useModels();
  const { data: agents } = useAgents();
  const { data: runs } = useRuns();
  const { data: providerNodes } = useProviderNodes(true);
  const { data: gateway } = useGatewayConfig();
  const { fitView } = useReactFlow();

  const [rfNodes, setNodes, onNodesChange] = useNodesState<Node>([] as Node[]);
  const [rfEdges, setEdges, onEdgesChange] = useEdgesState<Edge>([] as Edge[]);
  const [locked, setLocked] = useState(() => localStorage.getItem(TOPO_LOCKED_KEY) === "true");
  const [selectedK8sNode, setSelectedK8sNode] = useState<ProviderNode | null>(null);

  // Track when we've done the initial fitView so we don't re-fit on every refetch.
  const hasFitRef = useRef(false);
  // Track the entity fingerprint so we only recompute layout when entities change.
  const prevFingerprintRef = useRef("");

  const runningByEnsemble = useMemo(() => {
    const counts: Record<string, number> = {};
    for (const run of runs || []) {
      if (run.status?.phase === "Running" || run.status?.phase === "Serving") {
        const agentRef = run.spec?.agentRef;
        if (agentRef) {
          for (const ens of ensembles || []) {
            if (
              (ens.spec.agentConfigs || []).some(
                (p) => `${ens.metadata.name}-${p.name}` === agentRef,
              )
            ) {
              counts[ens.metadata.name] = (counts[ens.metadata.name] || 0) + 1;
              break;
            }
          }
        }
      }
    }
    return counts;
  }, [runs, ensembles]);

  const webEndpointAgents = useMemo(() => {
    return (agents || [])
      .filter((a) =>
        (a.spec?.skills || []).some(
          (s) =>
            s.skillPackRef === "web-endpoint" ||
            s.skillPackRef === "skillpack-web-endpoint",
        ),
      )
      .map((a) => a.metadata.name);
  }, [agents]);

  // Latest run phase per stamped agent name (for persona status dots).
  const runPhases = useMemo<RunPhaseMap>(() => {
    const map: RunPhaseMap = {};
    for (const run of runs || []) {
      const ref = run.spec?.agentRef;
      if (ref && run.status?.phase) {
        const existing = map[ref];
        // Prefer active phases over terminal ones.
        if (
          !existing ||
          run.status.phase === "Running" ||
          run.status.phase === "Serving"
        ) {
          map[ref] = run.status.phase;
        }
      }
    }
    return map;
  }, [runs]);

  // Recompute layout only when the set of entities changes (add/remove),
  // not on every status update or data refetch.
  useEffect(() => {
    const fp = entityFingerprint(
      providerNodes || [],
      models || [],
      ensembles || [],
      !!gateway,
    );

    const entitiesChanged = fp !== prevFingerprintRef.current;
    prevFingerprintRef.current = fp;

    if (entitiesChanged) {
      const { nodes, edges } = buildTopology(
        providerNodes || [],
        models || [],
        ensembles || [],
        gateway,
        runningByEnsemble,
        webEndpointAgents,
        runPhases,
      );

      // Apply saved positions if available.
      const saved = loadPositions();
      if (saved) {
        for (const n of nodes) {
          if (saved[n.id]) {
            n.position = saved[n.id];
          }
        }
      }

      setNodes(nodes);
      setEdges(edges);

      // Fit view on first load only.
      if (!hasFitRef.current) {
        setTimeout(() => fitView({ padding: 0.2, duration: 300 }), 100);
      }
      hasFitRef.current = true;
    } else {
      // Entities are the same — just update node data in-place (status, run counts)
      // without changing positions.
      setNodes((prev) => {
        const { nodes: freshNodes } = buildTopology(
          providerNodes || [],
          models || [],
          ensembles || [],
          gateway,
          runningByEnsemble,
          webEndpointAgents,
          runPhases,
        );
        const freshMap = new Map(freshNodes.map((n) => [n.id, n]));
        return prev.map((n) => {
          const fresh = freshMap.get(n.id);
          if (fresh) {
            return { ...n, data: fresh.data };
          }
          return n;
        });
      });
      setEdges(() => {
        const { edges: freshEdges } = buildTopology(
          providerNodes || [],
          models || [],
          ensembles || [],
          gateway,
          runningByEnsemble,
          webEndpointAgents,
          runPhases,
        );
        return freshEdges;
      });
    }
  }, [providerNodes, models, ensembles, gateway, runningByEnsemble, webEndpointAgents, runPhases, setNodes, setEdges, fitView]);

  // Save positions to localStorage after any node drag ends.
  const handleNodesChange = useCallback(
    (changes: Parameters<typeof onNodesChange>[0]) => {
      onNodesChange(changes);
      // Save after position changes (drag end).
      const hasDragStop = changes.some((c) => c.type === "position" && c.dragging === false);
      if (hasDragStop) {
        // Use a microtask so state has settled.
        requestAnimationFrame(() => {
          setNodes((current) => {
            savePositions(current);
            return current;
          });
        });
      }
    },
    [onNodesChange, setNodes],
  );

  function handleReset() {
    localStorage.removeItem(TOPO_POSITIONS_KEY);
    prevFingerprintRef.current = ""; // force layout recompute
    hasFitRef.current = false;
  }

  function toggleLock() {
    setLocked((prev) => {
      const next = !prev;
      localStorage.setItem(TOPO_LOCKED_KEY, String(next));
      return next;
    });
  }

  const handleNodeClick = useCallback(
    (_event: React.MouseEvent, node: Node) => {
      if (node.type === "k8sNode") {
        const pn = (providerNodes || []).find(
          (p) => p.nodeName === (node.data as K8sNodeData).name,
        );
        if (pn) setSelectedK8sNode(pn);
      }
    },
    [providerNodes],
  );

  // Models placed on the selected K8s node.
  const modelsOnSelectedNode = useMemo(() => {
    if (!selectedK8sNode) return [];
    return (models || []).filter(
      (m) => m.status?.placedNode === selectedK8sNode.nodeName,
    );
  }, [selectedK8sNode, models]);

  return (
    <div className="h-[calc(100vh-4rem)]">
      <div className="flex items-center justify-between px-4 py-2 border-b border-border">
        <div>
          <h1 className="text-lg font-bold">Topology</h1>
          <p className="text-xs text-muted-foreground">
            Cluster-wide view of nodes, models, ensembles, and gateway
          </p>
        </div>
        <div className="flex items-center gap-3">
          <div className="flex items-center gap-3 text-[10px] text-muted-foreground">
            <span className="flex items-center gap-1">
              <span className="h-2 w-2 rounded-full bg-amber-500" /> Gateway
            </span>
            <span className="flex items-center gap-1">
              <span className="h-2 w-2 rounded-full bg-emerald-500" /> K8s Nodes
            </span>
            <span className="flex items-center gap-1">
              <span className="h-2 w-2 rounded-full bg-orange-500" /> Providers
            </span>
            <span className="flex items-center gap-1">
              <span className="h-2 w-2 rounded-full bg-violet-500" /> Models (Pod)
            </span>
            <span className="flex items-center gap-1">
              <span className="h-2 w-2 rounded-full bg-blue-500" /> Ensembles
            </span>
            <span className="flex items-center gap-1">
              <span className="h-2 w-2 rounded-full bg-blue-400" /> Agents
            </span>
          </div>
          <div className="flex items-center gap-1 ml-2">
            <Button
              variant={locked ? "default" : "outline"}
              size="sm"
              className="h-7 text-[10px] gap-1"
              onClick={toggleLock}
            >
              {locked ? <Lock className="h-3 w-3" /> : <Unlock className="h-3 w-3" />}
              {locked ? "Locked" : "Unlocked"}
            </Button>
            <Button
              variant="outline"
              size="sm"
              className="h-7 text-[10px] gap-1"
              onClick={handleReset}
            >
              <RotateCcw className="h-3 w-3" /> Reset
            </Button>
          </div>
        </div>
      </div>
      <div className="h-[calc(100%-3rem)]">
        <ReactFlow
          nodes={rfNodes}
          edges={rfEdges}
          onNodesChange={handleNodesChange}
          onEdgesChange={onEdgesChange}
          onNodeClick={handleNodeClick}
          nodeTypes={nodeTypes}
          proOptions={{ hideAttribution: true }}
          minZoom={0.2}
          maxZoom={2}
          nodesDraggable={!locked}
          nodesConnectable={false}
          className="topology-canvas"
        >
          <Background color="#333" />
          <Controls showInteractive={false} />
          <MiniMap
            style={{ background: "hsl(var(--card))" }}
            maskColor="rgba(0,0,0,0.6)"
            nodeColor={(node) => {
              switch (node.type) {
                case "cloudProvider":
                  return "#f97316";
                case "k8sNode":
                  return "#10b981";
                case "model":
                  return "#8b5cf6";
                case "ensemble":
                  return "#3b82f6";
                case "persona":
                  return "#60a5fa";
                case "gateway":
                  return "#f59e0b";
                default:
                  return "#6b7280";
              }
            }}
          />
        </ReactFlow>
      </div>

      {/* K8s Node detail panel */}
      <Dialog
        open={selectedK8sNode !== null}
        onOpenChange={(open) => { if (!open) setSelectedK8sNode(null); }}
      >
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2 text-emerald-300">
              <Server className="h-4 w-4 text-emerald-400" />
              {selectedK8sNode?.nodeName}
            </DialogTitle>
            <DialogDescription className="font-mono text-xs">
              {selectedK8sNode?.nodeIP}
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-4 text-sm">
            {/* Providers */}
            {selectedK8sNode && selectedK8sNode.providers.length > 0 && (
              <div>
                <h4 className="text-xs font-semibold text-muted-foreground uppercase tracking-wider mb-2">
                  Inference Providers
                </h4>
                <div className="space-y-2">
                  {selectedK8sNode.providers.map((p) => (
                    <div
                      key={p.name}
                      className="rounded-md border border-emerald-500/20 bg-emerald-500/5 p-2"
                    >
                      <div className="flex items-center justify-between mb-1">
                        <span className="font-medium text-emerald-300">{p.name}</span>
                        <span className="text-[10px] text-muted-foreground font-mono">
                          port {p.port}
                          {p.proxyPort ? ` / proxy ${p.proxyPort}` : ""}
                        </span>
                      </div>
                      {p.models.length > 0 && (
                        <div className="flex flex-wrap gap-1">
                          {p.models.map((m) => (
                            <Badge
                              key={m}
                              variant="outline"
                              className="text-[9px] border-emerald-500/30 text-emerald-400"
                            >
                              {m}
                            </Badge>
                          ))}
                        </div>
                      )}
                      {p.lastProbe && (
                        <p className="text-[10px] text-muted-foreground mt-1">
                          Last probe: {new Date(p.lastProbe).toLocaleString()}
                        </p>
                      )}
                    </div>
                  ))}
                </div>
              </div>
            )}

            {/* Models placed on this node */}
            {modelsOnSelectedNode.length > 0 && (
              <div>
                <h4 className="text-xs font-semibold text-muted-foreground uppercase tracking-wider mb-2">
                  Models on this Node
                </h4>
                <div className="space-y-1.5">
                  {modelsOnSelectedNode.map((m) => (
                    <div
                      key={m.metadata.name}
                      className="flex items-center justify-between rounded-md border border-violet-500/20 bg-violet-500/5 px-2 py-1.5"
                    >
                      <Link
                        to={`/models/${m.metadata.name}?namespace=${m.metadata.namespace}`}
                        className="font-medium text-xs text-violet-300 hover:underline"
                      >
                        {m.metadata.name}
                      </Link>
                      <Badge
                        variant="outline"
                        className={`text-[9px] ${
                          m.status?.phase === "Ready"
                            ? "border-green-500/30 text-green-400"
                            : m.status?.phase === "Failed"
                              ? "border-red-500/30 text-red-400"
                              : "border-yellow-500/30 text-yellow-400"
                        }`}
                      >
                        {m.status?.phase || "Pending"}
                      </Badge>
                    </div>
                  ))}
                </div>
              </div>
            )}

            {/* Labels */}
            {selectedK8sNode?.labels && Object.keys(selectedK8sNode.labels).length > 0 && (
              <div>
                <h4 className="text-xs font-semibold text-muted-foreground uppercase tracking-wider mb-2">
                  Labels
                </h4>
                <div className="flex flex-wrap gap-1 max-h-32 overflow-y-auto">
                  {Object.entries(selectedK8sNode.labels).map(([k, v]) => (
                    <Badge
                      key={k}
                      variant="outline"
                      className="text-[9px] font-mono"
                    >
                      {k}={v}
                    </Badge>
                  ))}
                </div>
              </div>
            )}
          </div>
        </DialogContent>
      </Dialog>
    </div>
  );
}

// ── Exported page (wraps in ReactFlowProvider) ────────────────────────────────

export function TopologyPage() {
  return (
    <ReactFlowProvider>
      <TopologyCanvas />
    </ReactFlowProvider>
  );
}
