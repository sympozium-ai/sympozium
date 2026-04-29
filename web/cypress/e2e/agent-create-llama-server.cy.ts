// Test: create an ad-hoc instance using llama-server.

const INSTANCE = `cypress-llama-server-${Date.now()}`;

describe("Create Agent — llama-server", () => {
  after(() => {
    cy.deleteAgent(INSTANCE);
  });

  it("walks through the wizard and creates the instance", () => {
    cy.visit("/agents");

    cy.contains("button", "Create Agent", { timeout: 20000 }).click();

    // ── Step 1: Name ──────────────────────────────────────────
    cy.get("[role='dialog']")
      .find("input[placeholder='my-agent']")
      .clear()
      .type(INSTANCE);
    cy.wizardNext();

    // ── Step 2: Provider — select llama-server ────────────────
    cy.get("[role='dialog']")
      .find("button[role='combobox']")
      .click({ force: true });
    cy.get("[data-radix-popper-content-wrapper]")
      .contains("llama-server")
      .click({ force: true });
    cy.wizardNext();

    // ── Step 3: Auth ──────────────────────────────────────────
    // llama-server needs no API key — go straight through.
    cy.wizardNext();

    // ── Step 4: Model ─────────────────────────────────────────
    cy.get("[role='dialog']")
      .find("input[placeholder='gpt-4o']")
      .clear()
      .type("default");
    cy.wizardNext();

    // ── Step 5: Skills ────────────────────────────────────────
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
    cy.get("[role='dialog']").contains("llama-server");
    cy.get("[role='dialog']").contains("default");
    cy.get("[role='dialog']")
      .contains("button", "Create")
      .click({ force: true });

    // Wait for the dialog to close (instance was created).
    cy.get("[role='dialog']").should("not.exist", { timeout: 20000 });

    // ── Verify instance appears in the list ───────────────────
    cy.contains(INSTANCE, { timeout: 20000 }).should("be.visible");
  });
});

export {};
