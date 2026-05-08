import { useMemo, useCallback, useState, useEffect, useRef } from "react";
import {
  ReactFlow,
  type Node,
  type Edge,
  type Connection,
  useNodesState,
  useEdgesState,
  addEdge,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { Button } from "@/components/ui/button";
import {
  useRuns,
  useEnsembles,
  useModels,
  usePatchEnsembleRelationships,
  useTriggerStimulus,
} from "@/hooks/use-api";
import { useWebSocket } from "@/hooks/use-websocket";
import { useQueryClient } from "@tanstack/react-query";
import { Save, Plus, Trash2, Cpu, Zap } from "lucide-react";
import type {
  Ensemble,
  Model,
  AgentConfigSpec,
  AgentRun,
  StimulusSpec,
} from "@/lib/api";
import {
  AddProviderModal,
} from "@/components/add-provider-modal";

// ── Import all shared primitives from the single source of truth ──────────
import {
  type AgentConfigNodeData,
  type ModelNodeData,
  type ProviderNodeData,
  type StimulusNodeData,
  nodeTypes,
  edgeStyles,
  EDGE_TYPES,
  styledEdge,
  layoutNodes,
  buildEdges,
  edgesToRelationships,
  buildProviderNodesAndEdges,
  buildRunPhaseMap,
  rfDefaults,
  CanvasShell,
  EdgeTypePicker,
  StatusLegend,
  StimulusDialogProvider,
} from "@/components/canvas-primitives";

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

// Re-export types that external consumers may depend on.
export type { AgentConfigNodeData, StimulusNodeData };

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
  const triggerStimulusMutation = useTriggerStimulus();
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
    () => buildRunPhaseMap(runs, pack.status?.installedPersonas),
    [runs, pack.status?.installedPersonas],
  );

  const initialNodes = useMemo(() => {
    // Derive provider/model nodes from ensemble data.
    const provResult = buildProviderNodesAndEdges(pack, modelMap, personas, 0, "");
    const hasProviders = provResult.nodes.length > 0;
    const yOffset = hasProviders ? 140 : 0;

    const nodes: Node<AgentConfigNodeData | ModelNodeData | ProviderNodeData | StimulusNodeData>[] =
      layoutNodes(personas, relationships, 0, yOffset);
    const sharedMemEnabled = pack.spec.sharedMemory?.enabled ?? false;
    const membrane = pack.spec.sharedMemory?.membrane;
    for (const node of nodes) {
      node.data.hasSharedMemory = sharedMemEnabled;
      if (membrane) {
        const rule = membrane.permeability?.find((r) => r.agentConfig === node.id);
        node.data.membraneVisibility = rule?.defaultVisibility || membrane.defaultVisibility || "public";
      }
      const ip = pack.status?.installedPersonas?.find(
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

    // Add stimulus node if configured.
    if (pack.spec.stimulus) {
      const stimulusYOffset = hasProviders ? -60 : -80;
      nodes.push({
        id: `__stimulus__${pack.spec.stimulus.name}`,
        type: "stimulus",
        position: { x: 0, y: stimulusYOffset },
        data: {
          stimulus: pack.spec.stimulus,
          ensembleName: pack.metadata.name,
          delivered: pack.status?.stimulusDelivered,
          generation: pack.status?.stimulusGeneration,
          label: pack.spec.stimulus.name,
        },
      });
    }

    return nodes;
  }, [
    personas,
    relationships,
    pack,
    pack.spec.sharedMemory?.enabled,
    pack.status?.installedPersonas,
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
      // Provider→Agent connections: auto-wire without relationship picker
      if (connection.source.startsWith("__prov__")) {
        setEdges((eds) =>
          addEdge(
            {
              ...connection,
              id: `prov-${connection.source}-${connection.target}-${Date.now()}`,
              style: { stroke: "#8b5cf6", strokeWidth: 1.5, strokeDasharray: "4 3" },
              animated: true,
            },
            eds,
          ),
        );
        setDirty(true);
        return;
      }
      if (
        edges.some(
          (e) =>
            e.source === connection.source && e.target === connection.target,
        )
      )
        return;
      setPendingConnection(connection);
    },
    [edges, setEdges],
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
    <StimulusDialogProvider>
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
          {pack.spec.stimulus && (
            <Button
              size="sm"
              variant="outline"
              onClick={() =>
                triggerStimulusMutation.mutate(pack.metadata.name)
              }
              disabled={triggerStimulusMutation.isPending}
              type="button"
              data-testid="stimulus-retrigger-btn"
            >
              <Zap className="h-3.5 w-3.5 mr-1" />
              {triggerStimulusMutation.isPending
                ? "Triggering..."
                : "Re-trigger Stimulus"}
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
    </StimulusDialogProvider>
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

  const modelMap = useMemo(() => {
    const m = new Map<string, Model>();
    for (const model of models || []) m.set(model.metadata.name, model);
    return m;
  }, [models]);

  // Build layout only when packs change — NOT on run updates.
  const { layoutedNodes, allEdges } = useMemo(() => {
    const nodes: Node<AgentConfigNodeData | ModelNodeData | ProviderNodeData | StimulusNodeData>[] = [];
    const edges: Edge[] = [];

    const packGapX = 50;
    let currentX = 0;

    for (const pack of enabledPacks) {
      const personas = pack.spec.agentConfigs || [];
      const relationships = pack.spec.relationships || [];
      const prefix = pack.metadata.name;

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
      const packMembrane = pack.spec.sharedMemory?.membrane;
      for (const node of packNodes) {
        node.data.packName = pack.metadata.name;
        node.data.hasSharedMemory = sharedMemoryEnabled;
        if (packMembrane) {
          const personaId = node.id.split("/")[1] || node.id;
          const rule = packMembrane.permeability?.find((r) => r.agentConfig === personaId);
          node.data.membraneVisibility = rule?.defaultVisibility || packMembrane.defaultVisibility || "public";
        }
        const personaName = node.id.split("/")[1] || node.id;
        const ip = pack.status?.installedPersonas?.find(
          (p) => p.name === personaName,
        );
        if (ip) node.data.agentName = ip.agentName;
      }

      nodes.push(...provResult.nodes);
      nodes.push(...packNodes);
      edges.push(...provResult.edges);
      edges.push(...buildEdges(relationships, prefix));

      // Add stimulus node if configured.
      if (pack.spec.stimulus) {
        const stimId = `__stimulus__${pack.spec.stimulus.name}`;
        const prefixedStimId = prefix ? `${prefix}/${stimId}` : stimId;
        nodes.push({
          id: prefixedStimId,
          type: "stimulus",
          position: { x: currentX, y: hasProviders ? -60 : -80 },
          data: {
            stimulus: pack.spec.stimulus,
            ensembleName: pack.metadata.name,
            delivered: pack.status?.stimulusDelivered,
            generation: pack.status?.stimulusGeneration,
            label: pack.spec.stimulus.name,
          },
        });
      }

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
        buildRunPhaseMap(runs, pack.status?.installedPersonas),
      );
    }

    // Count active sub-agent runs per persona (agentRef).
    const subagentCounts = new Map<string, number>();
    const activePhases = new Set(["Running", "Pending", "AwaitingDelegate"]);
    for (const run of runs || []) {
      if (
        run.spec?.parent &&
        run.status?.phase &&
        activePhases.has(run.status.phase)
      ) {
        const ref = run.spec.agentRef;
        subagentCounts.set(ref, (subagentCounts.get(ref) || 0) + 1);
      }
    }

    return layoutedNodes.map((node) => {
      if (node.type !== "persona") return node;
      const packName = (node.data as AgentConfigNodeData).packName || "";
      const personaName = node.id.split("/")[1] || node.id;
      const status = runPhaseMaps.get(packName)?.get(personaName);
      const agentRef = `${packName}-${personaName}`;
      const activeSubagents = subagentCounts.get(agentRef) || 0;
      if (!status && !activeSubagents) return node;
      return {
        ...node,
        data: {
          ...node.data,
          ...(status ? { runPhase: status.phase, runTask: status.task } : {}),
          ...(activeSubagents > 0 ? { activeSubagents } : {}),
        },
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
    <StimulusDialogProvider>
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-4">
          <p className="text-xs text-muted-foreground">
            {enabledPacks.length} active pack
            {enabledPacks.length !== 1 ? "s" : ""} &middot; {allNodes.length}{" "}
            agents
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
    </StimulusDialogProvider>
  );
}

// ══════════════════════════════════════════════════════════════════════════════
// Dashboard widget canvas (compact, with pack selector dropdown)
// ══════════════════════════════════════════════════════════════════════════════

export function DashboardEnsembleCanvas() {
  useRunEventInvalidation();
  const { data: packs } = useEnsembles();
  const { data: runs } = useRuns();
  const { data: models } = useModels();
  const [selectedPack, setSelectedPack] = useState<string>("__all__");

  const enabledPacks = useMemo(
    () => (packs || []).filter((p) => p.spec.enabled),
    [packs],
  );

  const visiblePacks = useMemo(
    () =>
      selectedPack === "__all__"
        ? enabledPacks
        : enabledPacks.filter((p) => p.metadata.name === selectedPack),
    [enabledPacks, selectedPack],
  );

  const modelMap = useMemo(() => {
    const m = new Map<string, Model>();
    for (const model of models || []) m.set(model.metadata.name, model);
    return m;
  }, [models]);

  const { layoutedNodes: dashLayoutNodes, allEdges } = useMemo(() => {
    const nodes: Node<AgentConfigNodeData | ModelNodeData | ProviderNodeData | StimulusNodeData>[] = [];
    const edges: Edge[] = [];
    let currentX = 0;

    for (const pack of visiblePacks) {
      const personas = pack.spec.agentConfigs || [];
      const relationships = pack.spec.relationships || [];
      const prefix = pack.metadata.name;

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
      const sharedMemEnabled = pack.spec.sharedMemory?.enabled ?? false;
      const dashMembrane = pack.spec.sharedMemory?.membrane;
      for (const node of packNodes) {
        node.data.packName =
          visiblePacks.length > 1 ? pack.metadata.name : undefined;
        node.data.hasSharedMemory = sharedMemEnabled;
        if (dashMembrane) {
          const personaId = node.id.split("/")[1] || node.id;
          const rule = dashMembrane.permeability?.find((r) => r.agentConfig === personaId);
          node.data.membraneVisibility = rule?.defaultVisibility || dashMembrane.defaultVisibility || "public";
        }
        const personaName = node.id.split("/")[1] || node.id;
        const ip = pack.status?.installedPersonas?.find(
          (p) => p.name === personaName,
        );
        if (ip) node.data.agentName = ip.agentName;
      }

      nodes.push(...provResult.nodes);
      nodes.push(...packNodes);
      edges.push(...provResult.edges);
      edges.push(...buildEdges(relationships, prefix));

      // Add stimulus node if configured.
      if (pack.spec.stimulus) {
        const stimId = `__stimulus__${pack.spec.stimulus.name}`;
        const prefixedStimId = prefix ? `${prefix}/${stimId}` : stimId;
        nodes.push({
          id: prefixedStimId,
          type: "stimulus",
          position: { x: currentX, y: hasProviders ? -60 : -80 },
          data: {
            stimulus: pack.spec.stimulus,
            ensembleName: pack.metadata.name,
            delivered: pack.status?.stimulusDelivered,
            generation: pack.status?.stimulusGeneration,
            label: pack.spec.stimulus.name,
          },
        });
      }

      const cols = Math.max(2, Math.ceil(Math.sqrt(personas.length)));
      currentX += cols * 260 + 50;
    }

    return { layoutedNodes: nodes, allEdges: edges };
  }, [visiblePacks, modelMap]);

  const allNodes = useMemo(() => {
    const runPhaseMaps = new Map<
      string,
      Map<string, { phase?: string; task?: string }>
    >();
    for (const pack of visiblePacks) {
      runPhaseMaps.set(
        pack.metadata.name,
        buildRunPhaseMap(runs, pack.status?.installedPersonas),
      );
    }

    // Count active sub-agent runs per persona (agentRef).
    const subagentCounts = new Map<string, number>();
    const activePhases = new Set(["Running", "Pending", "AwaitingDelegate"]);
    for (const run of runs || []) {
      if (
        run.spec?.parent &&
        run.status?.phase &&
        activePhases.has(run.status.phase)
      ) {
        const ref = run.spec.agentRef;
        subagentCounts.set(ref, (subagentCounts.get(ref) || 0) + 1);
      }
    }

    return dashLayoutNodes.map((node) => {
      if (node.type !== "persona") return node;
      const packName = (node.data as AgentConfigNodeData).packName || visiblePacks[0]?.metadata.name || "";
      const personaName = node.id.split("/")[1] || node.id;
      const status = runPhaseMaps.get(packName)?.get(personaName);
      const agentRef = `${packName}-${personaName}`;
      const activeSubagents = subagentCounts.get(agentRef) || 0;
      if (!status && !activeSubagents) return node;
      return {
        ...node,
        data: {
          ...node.data,
          ...(status ? { runPhase: status.phase, runTask: status.task } : {}),
          ...(activeSubagents > 0 ? { activeSubagents } : {}),
        },
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
    <StimulusDialogProvider>
    <div className="flex flex-col h-full">
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
      <div className="flex-1 min-h-0 rounded-lg border border-border/40 bg-background">
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
    </StimulusDialogProvider>
  );
}
