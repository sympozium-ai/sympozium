// Test: create an ad-hoc instance using LM Studio with qwen/qwen3.5-9b,
// verify it on the detail page, then dispatch a run and confirm it exists.

const INSTANCE = `cypress-adhoc-${Date.now()}`;

describe("Ad-hoc Instance — Create and Run", () => {
  function authHeaders(): Record<string, string> {
    const token = Cypress.env("API_TOKEN");
    const h: Record<string, string> = { "Content-Type": "application/json" };
    if (token) h["Authorization"] = `Bearer ${token}`;
    return h;
  }

  after(() => {
    cy.deleteInstance(INSTANCE);
  });

  it("creates an instance via the wizard", () => {
    cy.visit("/instances");

    cy.contains("button", "Create Instance", { timeout: 20000 }).click();

    // ── Step 1: Name ──────────────────────────────────────────
    cy.get("[role='dialog']").find("input[placeholder='my-agent']").clear().type(INSTANCE);
    cy.wizardNext();

    // ── Step 2: Provider — select LM Studio ───────────────────
    cy.get("[role='dialog']").find("button[role='combobox']").click({ force: true });
    cy.get("[data-radix-popper-content-wrapper]")
      .contains("LM Studio")
      .click({ force: true });
    cy.wizardNext();

    // ── Step 3: Auth — LM Studio needs no key ─────────────────
    cy.wizardNext();

    // ── Step 4: Model ─────────────────────────────────────────
    cy.get("[role='dialog']").find("input[placeholder='gpt-4o']").clear().type("qwen/qwen3.5-9b");
    cy.wizardNext();

    // ── Step 5: Skills — accept defaults ──────────────────────
    cy.wizardNext();

    // ── Step 6: Heartbeat — none ──────────────────────────────
    cy.get("[role='dialog']").contains("button", "No heartbeat").click({ force: true });
    cy.wizardNext();

    // ── Step 7: Channels — skip ───────────────────────────────
    cy.wizardNext();

    // ── Step 8: Confirm ───────────────────────────────────────
    cy.get("[role='dialog']").contains(INSTANCE);
    cy.get("[role='dialog']").contains("lm-studio");
    cy.get("[role='dialog']").contains("qwen/qwen3.5-9b");
    cy.get("[role='dialog']").contains("button", "Create").click({ force: true });

    // Wait for dialog to close.
    cy.get("[role='dialog']").should("not.exist", { timeout: 20000 });

    // ── Verify instance in the list ───────────────────────────
    cy.contains(INSTANCE, { timeout: 20000 }).should("be.visible");
  });

  it("shows correct config on the detail page", () => {
    cy.visit(`/instances/${INSTANCE}`);

    cy.contains(INSTANCE, { timeout: 20000 }).should("be.visible");
    cy.contains("qwen/qwen3.5-9b").should("be.visible");
    cy.contains("http://localhost:1234/v1").should("be.visible");
  });

  it("dispatches an ad-hoc run via API", () => {
    cy.request({
      method: "POST",
      url: "/api/v1/runs?namespace=default",
      headers: authHeaders(),
      body: {
        instanceRef: INSTANCE,
        task: "Say hello from Cypress test",
      },
    }).then((resp) => {
      expect(resp.status).to.eq(201);
      expect(resp.body?.metadata?.name).to.be.a("string").and.not.be.empty;

      // Verify the run appears on the Runs page by matching its instance name.
      cy.visit("/runs");
      cy.contains("td", INSTANCE, { timeout: 20000 }).should("be.visible");
      cy.contains("Say hello from Cypress test").should("be.visible");
    });
  });
});
