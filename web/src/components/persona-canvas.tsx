import { useMemo, useCallback } from "react";
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
  useNodesState,
  useEdgesState,
  MarkerType,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { Badge } from "@/components/ui/badge";
import { useRuns } from "@/hooks/use-api";
import type {
  PersonaPack,
  PersonaSpec,
  PersonaRelationship,
} from "@/lib/api";

// ── Node data ───────────────────────────────────────────────────────────────

interface PersonaNodeData {
  persona: PersonaSpec;
  instanceName?: string;
  runPhase?: string;
  label: string;
  [key: string]: unknown;
}

// ── Custom node ─────────────────────────────────────────────────────────────

function PersonaNode({ data }: NodeProps<Node<PersonaNodeData>>) {
  const { persona, instanceName, runPhase } = data;

  const phaseColor: Record<string, string> = {
    Running: "bg-blue-500",
    Succeeded: "bg-green-500",
    Failed: "bg-red-500",
    Pending: "bg-yellow-500",
    Serving: "bg-violet-500",
    AwaitingDelegate: "bg-amber-500",
  };

  return (
    <div className="rounded-lg border border-border/60 bg-card shadow-md px-4 py-3 min-w-[180px]">
      <Handle type="target" position={Position.Top} className="!bg-muted-foreground !w-2 !h-2" />

      <div className="flex items-center justify-between gap-2 mb-1.5">
        <span className="font-semibold text-sm truncate">
          {persona.displayName || persona.name}
        </span>
        {runPhase && (
          <span
            className={`h-2 w-2 rounded-full shrink-0 ${phaseColor[runPhase] || "bg-muted-foreground"}`}
            title={runPhase}
          />
        )}
      </div>

      <p className="text-[10px] text-muted-foreground font-mono mb-2 truncate">
        {persona.name}
      </p>

      {persona.model && (
        <Badge variant="outline" className="text-[10px] font-mono mb-1.5 block w-fit">
          {persona.model}
        </Badge>
      )}

      <div className="flex flex-wrap gap-0.5">
        {persona.skills?.slice(0, 4).map((sk) => (
          <Badge key={sk} variant="secondary" className="text-[9px] px-1 py-0">
            {sk}
          </Badge>
        ))}
        {(persona.skills?.length ?? 0) > 4 && (
          <Badge variant="secondary" className="text-[9px] px-1 py-0">
            +{(persona.skills?.length ?? 0) - 4}
          </Badge>
        )}
      </div>

      {instanceName && (
        <p className="text-[9px] text-muted-foreground/60 mt-1.5 truncate">
          {instanceName}
        </p>
      )}

      <Handle type="source" position={Position.Bottom} className="!bg-muted-foreground !w-2 !h-2" />
    </div>
  );
}

const nodeTypes = { persona: PersonaNode };

// ── Edge styling ────────────────────────────────────────────────────────────

const edgeStyles: Record<string, { stroke: string; strokeDasharray?: string }> = {
  delegation: { stroke: "#3b82f6" },
  sequential: { stroke: "#f59e0b", strokeDasharray: "6 3" },
  supervision: { stroke: "#6b7280", strokeDasharray: "2 4" },
};

const edgeLabels: Record<string, string> = {
  delegation: "delegates to",
  sequential: "then",
  supervision: "supervises",
};

// ── Layout helper ───────────────────────────────────────────────────────────

function layoutNodes(personas: PersonaSpec[], relationships: PersonaRelationship[]): Node<PersonaNodeData>[] {
  // Simple grid layout with relationship-aware ordering.
  // Personas with only outgoing edges go to the top; only incoming to the bottom.
  const outDegree = new Map<string, number>();
  const inDegree = new Map<string, number>();
  for (const r of relationships) {
    outDegree.set(r.source, (outDegree.get(r.source) || 0) + 1);
    inDegree.set(r.target, (inDegree.get(r.target) || 0) + 1);
  }

  // Sort: higher out-degree first (sources at top), then by name
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
    id: persona.name,
    type: "persona",
    position: {
      x: (i % cols) * xGap,
      y: Math.floor(i / cols) * yGap,
    },
    data: { persona, label: persona.displayName || persona.name },
  }));
}

function buildEdges(relationships: PersonaRelationship[]): Edge[] {
  return relationships.map((rel, i) => {
    const style = edgeStyles[rel.type] || edgeStyles.delegation;
    return {
      id: `e-${i}-${rel.source}-${rel.target}`,
      source: rel.source,
      target: rel.target,
      label: edgeLabels[rel.type] || rel.type,
      style,
      markerEnd: rel.type !== "supervision"
        ? { type: MarkerType.ArrowClosed, color: style.stroke }
        : undefined,
      labelStyle: { fontSize: 10, fill: "#9ca3af" },
      animated: rel.type === "delegation",
    };
  });
}

// ── Main component ──────────────────────────────────────────────────────────

interface PersonaCanvasProps {
  pack: PersonaPack;
}

export function PersonaCanvas({ pack }: PersonaCanvasProps) {
  const { data: runs } = useRuns();
  const relationships = pack.spec.relationships || [];
  const personas = pack.spec.personas || [];

  // Map persona name → latest run phase
  const runPhaseMap = useMemo(() => {
    const map = new Map<string, string>();
    if (!runs || !pack.status?.installedPersonas) return map;
    for (const ip of pack.status.installedPersonas) {
      const instanceRuns = runs
        .filter((r) => r.spec.instanceRef === ip.instanceName)
        .sort(
          (a, b) =>
            new Date(b.metadata.creationTimestamp || 0).getTime() -
            new Date(a.metadata.creationTimestamp || 0).getTime(),
        );
      if (instanceRuns.length > 0 && instanceRuns[0].status?.phase) {
        map.set(ip.name, instanceRuns[0].status.phase);
      }
    }
    return map;
  }, [runs, pack.status?.installedPersonas]);

  // Build nodes with run status overlay
  const initialNodes = useMemo(() => {
    const nodes = layoutNodes(personas, relationships);
    for (const node of nodes) {
      const ip = pack.status?.installedPersonas?.find(
        (p) => p.name === node.id,
      );
      if (ip) {
        node.data.instanceName = ip.instanceName;
      }
      node.data.runPhase = runPhaseMap.get(node.id);
    }
    return nodes;
  }, [personas, relationships, pack.status?.installedPersonas, runPhaseMap]);

  const initialEdges = useMemo(
    () => buildEdges(relationships),
    [relationships],
  );

  const [nodes, , onNodesChange] = useNodesState(initialNodes);
  const [edges, , onEdgesChange] = useEdgesState(initialEdges);

  const onInit = useCallback(() => {
    // could fitView here
  }, []);

  if (personas.length === 0) {
    return (
      <div className="flex items-center justify-center h-[400px] text-sm text-muted-foreground">
        No personas defined in this pack.
      </div>
    );
  }

  return (
    <div className="h-[500px] w-full rounded-lg border border-border/40 bg-background">
      <ReactFlow
        nodes={nodes}
        edges={edges}
        onNodesChange={onNodesChange}
        onEdgesChange={onEdgesChange}
        onInit={onInit}
        nodeTypes={nodeTypes}
        fitView
        fitViewOptions={{ padding: 0.3 }}
        minZoom={0.3}
        maxZoom={1.5}
        proOptions={{ hideAttribution: true }}
        colorMode="dark"
      >
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
      </ReactFlow>
    </div>
  );
}
