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
  usePatchEnsembleRelationships,
} from "@/hooks/use-api";
import { useWebSocket } from "@/hooks/use-websocket";
import { useQueryClient } from "@tanstack/react-query";
import { Save, Plus, Trash2, Database } from "lucide-react";
import type {
  Ensemble,
  PersonaSpec,
  PersonaRelationship,
  AgentRun,
} from "@/lib/api";

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

export interface PersonaNodeData {
  persona: PersonaSpec;
  packName?: string;
  instanceName?: string;
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

function PersonaNode({ data }: NodeProps<Node<PersonaNodeData>>) {
  const {
    persona,
    packName,
    instanceName,
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

      {instanceName && !runTask && (
        <p className="text-[9px] text-muted-foreground/60 mt-1.5 truncate">
          {instanceName}
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

const nodeTypes = { persona: PersonaNode };

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
  installedPersonas: Array<{ name: string; instanceName: string }> | undefined,
): Map<string, { phase: string; task?: string }> {
  const map = new Map<string, { phase: string; task?: string }>();
  if (!runs || !installedPersonas) return map;
  for (const ip of installedPersonas) {
    const instanceRuns = runs
      .filter((r) => r.spec.instanceRef === ip.instanceName)
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
  personas: PersonaSpec[],
  relationships: PersonaRelationship[],
  offsetX = 0,
  offsetY = 0,
  prefix = "",
): Node<PersonaNodeData>[] {
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

function buildEdges(relationships: PersonaRelationship[], prefix = ""): Edge[] {
  return relationships.map((rel, i) =>
    styledEdge(
      `e-${prefix}-${i}-${rel.source}-${rel.target}`,
      prefix ? `${prefix}/${rel.source}` : rel.source,
      prefix ? `${prefix}/${rel.target}` : rel.target,
      rel.type,
    ),
  );
}

function edgesToRelationships(edges: Edge[]): PersonaRelationship[] {
  return edges.map((e) => ({
    source: e.source.includes("/") ? e.source.split("/")[1] : e.source,
    target: e.target.includes("/") ? e.target.split("/")[1] : e.target,
    type: (e.data?.relType as PersonaRelationship["type"]) || "delegation",
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
  const patchMutation = usePatchEnsembleRelationships();
  const relationships = pack.spec.relationships || [];
  const personas = pack.spec.personas || [];

  const [pendingConnection, setPendingConnection] = useState<Connection | null>(
    null,
  );
  const [dirty, setDirty] = useState(false);
  const [selectedEdge, setSelectedEdge] = useState<string | null>(null);

  const runPhaseMap = useMemo(
    () => buildRunPhaseMap(runs, pack.status?.installedPersonas),
    [runs, pack.status?.installedPersonas],
  );

  const initialNodes = useMemo(() => {
    const nodes = layoutNodes(personas, relationships);
    const sharedMemEnabled = pack.spec.sharedMemory?.enabled ?? false;
    for (const node of nodes) {
      node.data.hasSharedMemory = sharedMemEnabled;
      const ip = pack.status?.installedPersonas?.find(
        (p) => p.name === node.id,
      );
      if (ip) node.data.instanceName = ip.instanceName;
      const status = runPhaseMap.get(node.id);
      if (status) {
        node.data.runPhase = status.phase;
        node.data.runTask = status.task;
      }
    }
    return nodes;
  }, [
    personas,
    relationships,
    pack.spec.sharedMemory?.enabled,
    pack.status?.installedPersonas,
    runPhaseMap,
  ]);

  const initialEdges = useMemo(
    () => buildEdges(relationships),
    [relationships],
  );

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

  const enabledPacks = useMemo(
    () => (packs || []).filter((p) => p.spec.enabled),
    [packs],
  );

  // Build combined nodes and edges from all enabled packs
  const { allNodes, allEdges } = useMemo(() => {
    const nodes: Node<PersonaNodeData>[] = [];
    const edges: Edge[] = [];

    // Layout each pack as a cluster, offset horizontally
    const packGapX = 50;
    let currentX = 0;

    for (const pack of enabledPacks) {
      const personas = pack.spec.personas || [];
      const relationships = pack.spec.relationships || [];
      const prefix = pack.metadata.name;

      const runPhaseMap = buildRunPhaseMap(
        runs,
        pack.status?.installedPersonas,
      );

      const packNodes = layoutNodes(
        personas,
        relationships,
        currentX,
        0,
        prefix,
      );

      // Annotate nodes with pack name, status, and shared memory
      const sharedMemoryEnabled = pack.spec.sharedMemory?.enabled ?? false;
      for (const node of packNodes) {
        node.data.packName = pack.metadata.name;
        node.data.hasSharedMemory = sharedMemoryEnabled;
        const personaName = node.id.split("/")[1] || node.id;
        const ip = pack.status?.installedPersonas?.find(
          (p) => p.name === personaName,
        );
        if (ip) node.data.instanceName = ip.instanceName;
        const status = runPhaseMap.get(personaName);
        if (status) {
          node.data.runPhase = status.phase;
          node.data.runTask = status.task;
        }
      }

      nodes.push(...packNodes);
      edges.push(...buildEdges(relationships, prefix));

      // Calculate width of this pack's cluster for offset
      const cols = Math.max(2, Math.ceil(Math.sqrt(personas.length)));
      currentX += cols * 260 + packGapX;
    }

    return { allNodes: nodes, allEdges: edges };
  }, [enabledPacks, runs]);

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

  const { allNodes, allEdges } = useMemo(() => {
    const nodes: Node<PersonaNodeData>[] = [];
    const edges: Edge[] = [];
    let currentX = 0;

    for (const pack of visiblePacks) {
      const personas = pack.spec.personas || [];
      const relationships = pack.spec.relationships || [];
      const prefix = pack.metadata.name;
      const runPhaseMap = buildRunPhaseMap(
        runs,
        pack.status?.installedPersonas,
      );

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
        const ip = pack.status?.installedPersonas?.find(
          (p) => p.name === personaName,
        );
        if (ip) node.data.instanceName = ip.instanceName;
        const status = runPhaseMap.get(personaName);
        if (status) {
          node.data.runPhase = status.phase;
          node.data.runTask = status.task;
        }
      }

      nodes.push(...packNodes);
      edges.push(...buildEdges(relationships, prefix));
      const cols = Math.max(2, Math.ceil(Math.sqrt(personas.length)));
      currentX += cols * 260 + 50;
    }

    return { allNodes: nodes, allEdges: edges };
  }, [visiblePacks, runs]);

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
              {p.metadata.name} ({p.spec.personas?.length ?? 0})
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
