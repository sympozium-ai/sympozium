// Test: Cluster Fitness page — verifies the fitness UI renders correctly,
// handles empty state gracefully, and responds to user interaction.
//
// When the llmfit DaemonSet is deployed and publishing, the page shows
// per-node hardware cards, a model catalog, and a search query tab.
// When fitness data is unavailable, the page shows a helpful empty state.

function authHeaders(): Record<string, string> {
  const token = Cypress.env("API_TOKEN");
  const h: Record<string, string> = { "Content-Type": "application/json" };
  if (token) h["Authorization"] = `Bearer ${token}`;
  return h;
}

describe("Cluster Fitness Page", () => {
  it("renders the page with correct heading", () => {
    cy.visit("/cluster-fitness");
    cy.contains("Cluster Fitness", { timeout: 15000 }).should("be.visible");
    cy.contains("Real-time hardware fitness data").should("be.visible");
  });

  it("shows empty state or node data", () => {
    cy.visit("/cluster-fitness");

    // Wait for the page to load — either we get node cards or the empty state.
    cy.get("body", { timeout: 15000 }).then(($body) => {
      if ($body.text().includes("No fitness data available")) {
        // Empty state: DaemonSet not deployed or no data yet.
        cy.contains("No fitness data available").should("be.visible");
        cy.contains("Deploy the llmfit DaemonSet").should("be.visible");
      } else {
        // Data present: tabs should be visible.
        cy.contains("button", "Nodes").should("be.visible");
        cy.contains("button", "Model Catalog").should("be.visible");
        cy.contains("button", "Query").should("be.visible");
      }
    });
  });

  it("fitness nodes API returns valid response", () => {
    cy.request({
      url: "/api/v1/fitness/nodes",
      headers: authHeaders(),
      failOnStatusCode: false,
    }).then((resp) => {
      expect(resp.status).to.eq(200);
      expect(resp.body).to.have.property("nodes");
      expect(resp.body).to.have.property("total");
      expect(resp.body.nodes).to.be.an("array");
      expect(resp.body.total).to.be.a("number");
    });
  });

  it("catalog API returns valid response", () => {
    cy.request({
      url: "/api/v1/catalog",
      headers: authHeaders(),
      failOnStatusCode: false,
    }).then((resp) => {
      expect(resp.status).to.eq(200);
      expect(resp.body).to.have.property("models");
      expect(resp.body).to.have.property("total");
      expect(resp.body.models).to.be.an("array");
    });
  });

  it("switches between tabs", () => {
    // Only test tab switching if we have data. Otherwise skip gracefully.
    cy.request({
      url: "/api/v1/fitness/nodes",
      headers: authHeaders(),
    }).then((resp) => {
      if (resp.body.total === 0) {
        cy.log("No fitness data — skipping tab interaction test");
        return;
      }

      cy.visit("/cluster-fitness");

      // Nodes tab is default.
      cy.contains("button", "Nodes", { timeout: 15000 }).should("be.visible");

      // Switch to Model Catalog tab.
      cy.contains("button", "Model Catalog").click();
      // Catalog should show a table header or empty message.
      cy.get("body").then(($body) => {
        if ($body.find("table").length > 0) {
          cy.contains("th", "Model").should("be.visible");
          cy.contains("th", "Best Score").should("be.visible");
        }
      });

      // Switch to Query tab.
      cy.contains("button", "Query").click();
      cy.get("input[placeholder*='Search models']").should("be.visible");
    });
  });

  it("query tab searches for models", () => {
    cy.request({
      url: "/api/v1/fitness/nodes",
      headers: authHeaders(),
    }).then((resp) => {
      if (resp.body.total === 0) {
        cy.log("No fitness data — skipping query test");
        return;
      }

      cy.visit("/cluster-fitness");
      cy.contains("button", "Query", { timeout: 15000 }).click();

      // Type a search query.
      cy.get("input[placeholder*='Search models']").type("Qwen");

      // Should show results table or "No matching models" message.
      cy.get("body", { timeout: 10000 }).then(($body) => {
        const hasTable = $body.find("table").length > 0;
        const hasNoResults = $body
          .text()
          .includes("No matching models found");
        expect(hasTable || hasNoResults).to.be.true;
      });
    });
  });

  it("shows node hardware details when data is present", () => {
    cy.request({
      url: "/api/v1/fitness/nodes",
      headers: authHeaders(),
    }).then((resp) => {
      if (resp.body.total === 0) {
        cy.log("No fitness data — skipping node card test");
        return;
      }

      const firstNode = resp.body.nodes[0];
      cy.visit("/cluster-fitness");

      // Node name should appear in a card.
      cy.contains(firstNode.nodeName, { timeout: 15000 }).should(
        "be.visible",
      );

      // Should show RAM info.
      cy.contains("GB RAM").should("be.visible");

      // Should show model fit count.
      cy.contains("models fit").should("be.visible");
    });
  });

  it("is accessible from the sidebar navigation", () => {
    cy.visit("/dashboard");

    // Find and click the Cluster Fitness nav item.
    cy.contains("Cluster Fitness", { timeout: 15000 }).click();
    cy.url().should("include", "/cluster-fitness");
    cy.contains("Real-time hardware fitness data").should("be.visible");
  });
});

export {};
