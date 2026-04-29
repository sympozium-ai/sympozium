// Sidebar hierarchy: verify the grouped sidebar renders section headers
// and all navigation links work.

describe("Sidebar — grouped navigation", () => {
  it("shows section labels and all nav links", () => {
    cy.visit("/dashboard");

    // Section headers should be visible.
    cy.get("aside").contains("Agents", { timeout: 20000 }).should("exist");
    cy.get("aside").contains("Infrastructure").should("exist");

    // All nav links should be present.
    cy.get("aside").contains("Dashboard").should("exist");
    cy.get("aside").contains("Gateway").should("exist");
    cy.get("aside").contains("Ensembles").should("exist");
    cy.get("aside").contains("Agents").should("exist");
    cy.get("aside").contains("Runs").should("exist");
    cy.get("aside").contains("Schedules").should("exist");
    cy.get("aside").contains("Policies").should("exist");
    cy.get("aside").contains("Skills").should("exist");
    cy.get("aside").contains("MCP Servers").should("exist");
  });

  it("navigates to the correct pages via sidebar links", () => {
    cy.visit("/dashboard");

    // Click Ensembles in sidebar.
    cy.get("aside").contains("a", "Ensembles").click();
    cy.url().should("include", "/ensembles");

    // Click Instances in sidebar.
    cy.get("aside").contains("a", "Agents").click();
    cy.url().should("include", "/agents");

    // Click Runs in sidebar.
    cy.get("aside").contains("a", "Runs").click();
    cy.url().should("include", "/runs");

    // Click Policies in sidebar (Infrastructure section).
    cy.get("aside").contains("a", "Policies").click();
    cy.url().should("include", "/policies");
  });
});

export {};
