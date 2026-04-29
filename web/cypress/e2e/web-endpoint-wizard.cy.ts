// Test: create an instance via the wizard with the web-endpoint skill enabled.
// Verifies the inline config (RPM, hostname) appears and persists to confirm step.

const INSTANCE = `cy-webep-wiz-${Date.now()}`;

describe("Create Agent — web-endpoint skill", () => {
  after(() => {
    cy.deleteAgent(INSTANCE);
  });

  it("walks the wizard, enables web-endpoint, and creates the instance", () => {
    cy.visit("/agents");
    cy.contains("button", "Create Agent", { timeout: 20000 }).click();

    // ── Step 1: Name ──────────────────────────────────────────
    cy.get("[role='dialog']")
      .find("input[placeholder='my-agent']")
      .clear()
      .type(INSTANCE);
    cy.wizardNext();

    // ── Step 2: Provider — LM Studio ──────────────────────────
    cy.get("[role='dialog']")
      .find("button[role='combobox']")
      .click({ force: true });
    cy.get("[data-radix-popper-content-wrapper]")
      .contains("LM Studio")
      .click({ force: true });
    cy.wizardNext();

    // ── Step 3: Auth ──────────────────────────────────────────
    cy.wizardNext();

    // ── Step 4: Model ─────────────────────────────────────────
    cy.get("[role='dialog']")
      .find("input[placeholder='gpt-4o']")
      .clear()
      .type("qwen/qwen3.5-9b");
    cy.wizardNext();

    // ── Step 5: Skills — toggle web-endpoint ──────────────────
    cy.get("[role='dialog']").contains("button", "web-endpoint").click();

    // Inline config for web-endpoint should appear.
    cy.get("[role='dialog']").contains("Web Endpoint Config").should("exist");
    cy.get("[role='dialog']")
      .find("input[type='number']")
      .should("have.value", "60");
    cy.get("[role='dialog']")
      .find("input[placeholder='auto from gateway']")
      .should("exist");

    // Set a custom RPM.
    cy.get("[role='dialog']")
      .find("input[type='number']")
      .focus()
      .type("{selectall}120");

    cy.wizardNext();

    // ── Step 6: Heartbeat ─────────────────────────────────────
    cy.get("[role='dialog']")
      .contains("button", "No heartbeat")
      .click({ force: true });
    cy.wizardNext();

    // ── Step 7: Channels ──────────────────────────────────────
    cy.wizardNext();

    // ── Step 8: Confirm ───────────────────────────────────────
    cy.get("[role='dialog']").contains(INSTANCE);
    cy.get("[role='dialog']").contains("lm-studio");
    cy.get("[role='dialog']").contains("Web Endpoint").scrollIntoView();
    cy.get("[role='dialog']").contains("120 rpm");

    cy.get("[role='dialog']")
      .contains("button", "Create")
      .scrollIntoView()
      .click({ force: true });

    // Dialog closes on success.
    cy.get("[role='dialog']").should("not.exist", { timeout: 20000 });

    // Instance appears in the list.
    cy.contains(INSTANCE, { timeout: 20000 }).should("be.visible");
  });
});

export {};
