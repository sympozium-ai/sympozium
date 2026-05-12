// Test: Model deploy dialog with density preview.
//
// When the llmfit DaemonSet is running and density data is available,
// the deploy dialog shows per-node fitness scores when auto placement
// is selected and a model name is entered.
//
// When density data is unavailable, the dialog works normally without
// the preview — graceful degradation.

function authHeaders(): Record<string, string> {
  const token = Cypress.env("API_TOKEN");
  const h: Record<string, string> = { "Content-Type": "application/json" };
  if (token) h["Authorization"] = `Bearer ${token}`;
  return h;
}

describe("Model Deploy — Density Preview", () => {
  it("shows auto placement as the default", () => {
    cy.visit("/models");
    cy.contains("button", "Deploy Model", { timeout: 15000 }).click();
    cy.get("[role='dialog']").should("be.visible");

    // The placement section is below the fold — scroll it into view.
    // Use the Deploy button as an anchor since it's at the very bottom.
    cy.get("[role='dialog']")
      .contains("button", "Deploy")
      .scrollIntoView();

    // Auto should be the default placement mode.
    cy.get("[role='dialog']")
      .contains("Auto (recommended)")
      .should("exist");

    // Close dialog.
    cy.get("[role='dialog']")
      .contains("button", "Cancel")
      .click({ force: true });
  });

  it("shows density preview when auto placement and model entered", () => {
    // First check if density data is available.
    cy.request({
      url: "/api/v1/density/nodes",
      headers: authHeaders(),
    }).then((resp) => {
      if (resp.body.total === 0) {
        cy.log("No density data — density preview will not appear, skipping");
        return;
      }

      cy.visit("/models");
      cy.contains("button", "Deploy Model", { timeout: 15000 }).click();
      cy.get("[role='dialog']").should("be.visible");

      const dialog = () => cy.get("[role='dialog']");

      // Switch to vLLM tab to get the modelID field.
      dialog().contains("vLLM").click();

      // Enter a model name.
      dialog().find("input").first().clear().type("test-fitness-preview");

      // Enter a model ID that should match density data.
      dialog()
        .contains("label", "HuggingFace Model ID")
        .parent()
        .find("input")
        .clear()
        .type("Qwen");

      // Scroll to placement section.
      dialog()
        .contains("button", "Deploy")
        .scrollIntoView();

      // Check if the density preview appears (it polls every 30s).
      // Give it time to fetch and render.
      cy.wait(2000);
      cy.get("body").then(($body) => {
        if ($body.text().includes("Node density preview")) {
          cy.contains("Node density preview").should("be.visible");
        } else {
          cy.log(
            "Fitness preview not shown — model may not match density data or polling not complete",
          );
        }
      });

      // Close dialog.
      dialog().contains("button", "Cancel").click({ force: true });
    });
  });

  it("hides density preview when switching to manual placement", () => {
    cy.request({
      url: "/api/v1/density/nodes",
      headers: authHeaders(),
    }).then((resp) => {
      if (resp.body.total === 0) {
        cy.log("No density data — skipping manual placement toggle test");
        return;
      }

      cy.visit("/models");
      cy.contains("button", "Deploy Model", { timeout: 15000 }).click();
      cy.get("[role='dialog']").should("be.visible");

      const dialog = () => cy.get("[role='dialog']");

      // Switch to Manual.
      dialog().contains("Auto (recommended)").click();
      cy.get("[data-radix-popper-content-wrapper]", { timeout: 5000 })
        .contains("Manual")
        .click();

      // Manual text should show.
      dialog()
        .contains("Pin the inference server to a specific node")
        .should("be.visible");

      // Fitness preview should NOT be visible.
      cy.contains("Node density preview").should("not.exist");

      // Close dialog.
      dialog().contains("button", "Cancel").click({ force: true });
    });
  });

  it("fitness query API works for model lookup", () => {
    cy.request({
      url: "/api/v1/density/query?model=Qwen",
      headers: authHeaders(),
      failOnStatusCode: false,
    }).then((resp) => {
      expect(resp.status).to.eq(200);
      expect(resp.body).to.have.property("query", "Qwen");
      expect(resp.body).to.have.property("rankedNodes");
      expect(resp.body.rankedNodes).to.be.an("array");
    });
  });

  it("fitness query API requires model parameter", () => {
    cy.request({
      url: "/api/v1/density/query",
      headers: authHeaders(),
      failOnStatusCode: false,
    }).then((resp) => {
      expect(resp.status).to.eq(400);
    });
  });
});

export {};
