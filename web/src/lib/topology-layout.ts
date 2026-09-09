import Dagre from "@dagrejs/dagre";
import type { Node, Edge } from "@xyflow/react";

/** Estimated node dimensions for dagre layout (width, height). */
export const NODE_SIZES: Record<string, [number, number]> = {
  gateway:       [220, 70],
  k8sNode:       [280, 110],
  cloudProvider: [180, 50],
  model:         [200, 70],
  ensemble:      [200, 50],
  stimulus:      [140, 40],
  persona:       [150, 50],
  agent:         [240, 56],
  agentRun:      [140, 40],
  harness:       [260, 56],
};

/** Run dagre layout on nodes and edges, positioning top-to-bottom. */
export function applyDagreLayout(nodes: Node[], edges: Edge[]): void {
  const g = new Dagre.graphlib.Graph({ compound: true })
    .setDefaultEdgeLabel(() => ({}))
    .setGraph({
      rankdir: "TB",
      nodesep: 60,
      ranksep: 100,
      edgesep: 30,
    });

  for (const node of [...nodes].sort((a, b) => a.id.localeCompare(b.id))) {
    const [w, h] = NODE_SIZES[node.type || ""] || [160, 50];
    if (node.parentId) continue; // skip children of compound nodes
    g.setNode(node.id, { width: w, height: h });
  }

  for (const edge of [...edges].sort((a, b) => a.id.localeCompare(b.id))) {
    // Only add edges between nodes that exist in the graph (skip child-only edges).
    if (g.hasNode(edge.source) && g.hasNode(edge.target)) {
      g.setEdge(edge.source, edge.target);
    }
  }

  Dagre.layout(g);

  // Graph depth is not resource hierarchy: disconnected agents must not float
  // beside providers, and a shared runtime is configuration, not a child run.
  // Use Dagre for horizontal ordering, then pack consistent semantic rows.
  const levels: Record<string, number> = {
    gateway: 0, k8sNode: 1, model: 2, cloudProvider: 2, harness: 2,
    ensemble: 3, stimulus: 4, agent: 5, persona: 5, agentRun: 6,
  };
  const topLevel = nodes.filter((node) => !node.parentId);
  const byId = new Map(topLevel.map((node) => [node.id, node]));
  function level(node: Node, visited = new Set<string>()): number {
    const base = levels[node.type || ""] ?? 5;
    if (node.type !== "agentRun" || visited.has(node.id)) return base;
    const next = new Set(visited).add(node.id);
    const parents = edges.filter((edge) => edge.target === node.id)
      .map((edge) => byId.get(edge.source))
      .filter((parent): parent is Node => parent?.type === "agentRun" && !next.has(parent.id));
    return Math.max(base, ...parents.map((parent) => level(parent, next) + 1));
  }
  const rows = new Map<number, Node[]>();
  for (const node of topLevel) {
    const rank = level(node);
    rows.set(rank, [...(rows.get(rank) || []), node]);
  }
  let y = 0;
  for (const [, row] of [...rows].sort(([a], [b]) => a - b)) {
    row.sort((a, b) => g.node(a.id).x - g.node(b.id).x || a.id.localeCompare(b.id));
    const size = (node: Node) => NODE_SIZES[node.type || ""] || [160, 50];
    const width = row.reduce((sum, node) => sum + size(node)[0], 0) + (row.length - 1) * 60;
    let x = -width / 2;
    for (const node of row) {
      node.position = { x, y };
      x += size(node)[0] + 60;
    }
    y += Math.max(...row.map((node) => size(node)[1])) + 100;
  }
}
