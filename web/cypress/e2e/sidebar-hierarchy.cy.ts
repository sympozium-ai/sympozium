// Sidebar hierarchy: verify the grouped sidebar renders section headers
// and all navigation links work.

describe("Sidebar — grouped navigation", () => {
  it("shows section labels and all nav links", () => {
    cy.visit("/dashboard");

    // Section headers should be visible.
    cy.get("aside").contains("Agents", { timeout: 20000 }).should("exist");
    cy.get("aside").contains("Configuration").should("exist");

    // All nav links should be present.
    cy.get("aside").contains("Dashboard").should("exist");
    cy.get("aside").contains("Gateway").should("exist");
    cy.get("aside").contains("Persona Packs").should("exist");
    cy.get("aside").contains("Instances").should("exist");
    cy.get("aside").contains("Runs").should("exist");
    cy.get("aside").contains("Schedules").should("exist");
    cy.get("aside").contains("Policies").should("exist");
    cy.get("aside").contains("Skills").should("exist");
    cy.get("aside").contains("MCP Servers").should("exist");
  });

  it("navigates to the correct pages via sidebar links", () => {
    cy.visit("/dashboard");

    // Click Persona Packs in sidebar.
    cy.get("aside").contains("a", "Persona Packs").click();
    cy.url().should("include", "/personas");

    // Click Instances in sidebar.
    cy.get("aside").contains("a", "Instances").click();
    cy.url().should("include", "/instances");

    // Click Runs in sidebar.
    cy.get("aside").contains("a", "Runs").click();
    cy.url().should("include", "/runs");

    // Click Policies in sidebar (Configuration section).
    cy.get("aside").contains("a", "Policies").click();
    cy.url().should("include", "/policies");
  });
});

export {};
