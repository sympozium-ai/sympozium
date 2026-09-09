import { applyDagreLayout, NODE_SIZES } from "../../src/lib/topology-layout";
import type { Node, Edge } from "@xyflow/react";

function fixture() {
  const nodes: Node[] = [
    ["provider", "cloudProvider"], ["harness", "harness"],
    ["agent-a", "agent"], ["agent-b", "agent"], ["disconnected", "agent"],
    ["run", "agentRun"], ["child", "agentRun"],
  ].map(([id, type]) => ({ id, type, data: {}, position: { x: 0, y: 0 } }));
  const edges: Edge[] = [
    ["provider", "agent-a"], ["harness", "agent-a"],
    ["harness", "agent-b"], ["agent-a", "run"], ["run", "child"],
  ].map(([source, target]) => ({ id: `${source}-${target}`, source, target }));
  return { nodes, edges };
}

describe("Topology semantic ordering", () => {
  it("renders the live topology with the new configuration label", () => {
    cy.visit("/topology");
    cy.get(".react-flow__node-agent").should("have.length.greaterThan", 0);
    cy.contains("default harness").should("exist");
    cy.contains("executes with").should("not.exist");
    cy.get(".react-flow__node-agent").should(($nodes) => {
      const positions = [...$nodes].map((node) => node.style.transform.match(/translate\([^,]+,\s*([^)]+)\)/)?.[1]);
      expect(new Set(positions).size).to.equal(1);
    });
  });

  it("keeps shared configuration above agents, including disconnected agents", () => {
    const { nodes, edges } = fixture();
    applyDagreLayout(nodes, edges);
    const y = (id: string) => nodes.find((node) => node.id === id)!.position.y;
    expect(y("provider")).to.equal(y("harness"));
    expect(y("agent-a")).to.equal(y("agent-b"));
    expect(y("agent-a")).to.equal(y("disconnected"));
    expect(y("harness")).to.be.lessThan(y("agent-a"));
    expect(y("agent-a")).to.be.lessThan(y("run"));
    expect(y("run")).to.be.lessThan(y("child"));
    const agents = nodes.filter((node) => node.type === "agent").sort((a, b) => a.position.x - b.position.x);
    for (let i = 1; i < agents.length; i++) {
      expect(agents[i].position.x).to.be.greaterThan(agents[i - 1].position.x + NODE_SIZES.agent[0]);
    }
  });

  it("is stable when API list order changes", () => {
    const first = fixture();
    const second = fixture();
    applyDagreLayout(first.nodes, first.edges);
    applyDagreLayout(second.nodes.reverse(), second.edges.reverse());
    for (const node of first.nodes) {
      expect(second.nodes.find((other) => other.id === node.id)!.position).to.deep.equal(node.position);
    }
  });
});
