/**
 * Cluster identity in the console: the top-bar badge that names the cluster
 * the console is talking to, the Sympozium version in the sidebar, and the
 * warning shown when the cluster changes under an open browser session (a
 * port-forward that silently followed another kubeconfig context).
 * Intercepted browser contract tests — they need no cluster.
 */

const host = {
  clusterID: "0f3c9a52-1111-2222-3333-444455556666",
  name: "homelab",
  kubernetesVersion: "v1.33.1",
  nodes: [
    { name: "framework", roles: ["control-plane"], kubeletVersion: "v1.33.1" },
    { name: "gpu-2", roles: [], kubeletVersion: "v1.33.0" },
  ],
  nodeCount: 2,
  sympoziumVersion: "v0.10.80",
};

const kind = {
  clusterID: "b7d1e0aa-9999-8888-7777-666655554444",
  name: "kind",
  kubernetesVersion: "v1.32.2",
  nodes: [{ name: "kind-control-plane", roles: ["control-plane"], kubeletVersion: "v1.32.2" }],
  nodeCount: 1,
  sympoziumVersion: "v0.10.80",
};

describe("Cluster identity", () => {
  beforeEach(() => {
    cy.intercept("GET", "**/api/v1/**", { body: [] });
    cy.intercept("GET", "**/api/v1/namespaces*", { body: ["default"] });
  });

  it("names the cluster and its short ID in the top bar", () => {
    cy.intercept("GET", "**/api/v1/cluster/identity*", { body: host }).as("identity");
    cy.visit("/dashboard#token=test-token");
    cy.wait("@identity").its("request.url").should("not.contain", "namespace=");
    cy.get('[data-testid="cluster-badge"]').should("contain", "homelab").and("contain", "0f3c9a52");
    cy.get('[data-testid="cluster-badge"]').should("not.contain", "0f3c9a52-");
    cy.get('[data-testid="cluster-changed-banner"]').should("not.exist");
  });

  it("falls back to the first node name when the cluster has no name", () => {
    cy.intercept("GET", "**/api/v1/cluster/identity*", { body: { ...host, name: "" } });
    cy.visit("/dashboard#token=test-token");
    cy.get('[data-testid="cluster-badge"]').should("contain", "framework").and("contain", "0f3c9a52");
  });

  it("shows the full ID, versions and nodes in the popover", () => {
    cy.intercept("GET", "**/api/v1/cluster/identity*", { body: { ...host, nodeCount: 23 } });
    cy.visit("/dashboard#token=test-token");
    cy.get('[data-testid="cluster-badge"]').click();
    cy.get('[data-testid="cluster-popover"]').within(() => {
      cy.get('[data-testid="cluster-id-full"]').should("have.text", host.clusterID);
      cy.contains("v1.33.1").should("be.visible");
      cy.contains("v0.10.80").should("be.visible");
      cy.contains("Nodes (23)").should("be.visible");
      cy.contains("and 21 more").should("be.visible");
      cy.get('[data-testid="cluster-nodes"]').should("contain", "framework").and("contain", "control-plane").and("contain", "gpu-2");
    });
    cy.get("body").type("{esc}");
    cy.get('[data-testid="cluster-popover"]').should("not.exist");
  });

  it("keeps the Sympozium version visible in the sidebar", () => {
    cy.intercept("GET", "**/api/v1/cluster/identity*", { body: host });
    cy.visit("/dashboard#token=test-token");
    cy.get('[data-testid="sympozium-version"]').should("be.visible").and("have.text", "v0.10.80");
  });

  it("renders nothing, and breaks nothing, when identity is unavailable", () => {
    cy.intercept("GET", "**/api/v1/cluster/identity*", { statusCode: 403, body: { error: "forbidden" } });
    cy.visit("/dashboard#token=test-token");
    cy.contains("Namespace:").should("be.visible");
    cy.get('[data-testid="cluster-badge"]').should("not.exist");
    cy.get('[data-testid="sympozium-version"]').should("not.exist");
  });

  it("warns when a later fetch reaches a different cluster", () => {
    let current = host;
    cy.intercept("GET", "**/api/v1/cluster/identity*", (request) => request.reply({ body: current })).as("identity");
    cy.visit("/dashboard#token=test-token");
    cy.get('[data-testid="cluster-badge"]').should("contain", "homelab");
    cy.get('[data-testid="cluster-changed-banner"]').should("not.exist");

    // The port-forward now reaches another cluster. Identity polls once a
    // minute, so return to the tab — react-query refetches on visibility.
    cy.then(() => {
      current = kind;
    });
    cy.document().trigger("visibilitychange");
    cy.wait("@identity");
    cy.get('[data-testid="cluster-changed-banner"]')
      .should("be.visible")
      .and("contain", "This console is now connected to a different cluster")
      .and("contain", "0f3c9a52")
      .and("contain", "b7d1e0aa");
    cy.get('[data-testid="cluster-badge"]').should("contain", "kind").and("contain", "b7d1e0aa");

    // The warning survives a plain browser refresh: only Reload accepts the new cluster.
    cy.reload();
    cy.get('[data-testid="cluster-changed-banner"]').should("be.visible");
    cy.get('[data-testid="cluster-changed-banner"]').contains("button", "Reload").click();
    cy.get('[data-testid="cluster-badge"]').should("contain", "b7d1e0aa");
    cy.get('[data-testid="cluster-changed-banner"]').should("not.exist");
  });
});
