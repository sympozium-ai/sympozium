// Test: instance name validation in the creation wizard.
// Verifies RFC 1123 subdomain rules are enforced before submission.

describe("Instance name validation", () => {
  beforeEach(() => {
    cy.visit("/agents");
    cy.contains("button", "Create Agent", { timeout: 20000 }).click();
  });

  it("auto-sanitizes uppercase and spaces to lowercase hyphens", () => {
    cy.get("[role='dialog']")
      .find("input[placeholder='my-agent']")
      .type("Custom Instance");

    // Should auto-convert to "custom-instance".
    cy.get("[role='dialog']")
      .find("input[placeholder='my-agent']")
      .should("have.value", "custom-instance");

    // No error shown — the sanitized value is valid.
    cy.get("[role='dialog']").find(".text-red-500").should("not.exist");
  });

  it("auto-sanitizes underscores to hyphens", () => {
    cy.get("[role='dialog']")
      .find("input[placeholder='my-agent']")
      .type("custom_deployment");

    cy.get("[role='dialog']")
      .find("input[placeholder='my-agent']")
      .should("have.value", "custom-deployment");
  });

  it("shows error for names starting with a hyphen", () => {
    cy.get("[role='dialog']")
      .find("input[placeholder='my-agent']")
      .type("-invalid");

    cy.get("[role='dialog']").contains("RFC 1123").should("be.visible");

    // Next button should be disabled.
    cy.contains("button", "Next").should("be.disabled");
  });

  it("shows error for names ending with a hyphen", () => {
    cy.get("[role='dialog']")
      .find("input[placeholder='my-agent']")
      .type("invalid-");

    cy.get("[role='dialog']").contains("RFC 1123").should("be.visible");
    cy.contains("button", "Next").should("be.disabled");
  });

  it("allows valid RFC 1123 names and enables Next", () => {
    cy.get("[role='dialog']")
      .find("input[placeholder='my-agent']")
      .type("my-valid-agent.v1");

    cy.get("[role='dialog']").find(".text-red-500").should("not.exist");
    cy.contains("button", "Next").should("not.be.disabled");
  });
});

export {};
