// Test: create an ad-hoc LM Studio instance with qwen/qwen3.5-9b, dispatch
// multiple agent runs against it, then delete the instance and assert it is
// gone.

const INSTANCE = `cypress-multirun-${Date.now()}`;
const RUN_COUNT = 3;
const TASKS = Array.from(
  { length: RUN_COUNT },
  (_, i) => `Cypress multi-run task #${i + 1}`,
);

describe("Ad-hoc Instance — Multiple Runs and Delete", () => {
  function authHeaders(): Record<string, string> {
    const token = Cypress.env("API_TOKEN");
    const h: Record<string, string> = { "Content-Type": "application/json" };
    if (token) h["Authorization"] = `Bearer ${token}`;
    return h;
  }

  // Safety net: if the delete test fails or is skipped, still clean up.
  after(() => {
    cy.deleteAgent(INSTANCE);
  });

  it("creates the LM Studio instance via the wizard", () => {
    cy.visit("/agents");

    cy.contains("button", "Create Agent", { timeout: 20000 }).click();

    // ── Step 1: Name ──────────────────────────────────────────
    cy.get("[role='dialog']")
      .find("input[placeholder='my-agent']")
      .clear()
      .type(INSTANCE);
    cy.wizardNext();

    // ── Step 2: Provider — select LM Studio ───────────────────
    cy.get("[role='dialog']")
      .find("button[role='combobox']")
      .click({ force: true });
    cy.get("[data-radix-popper-content-wrapper]")
      .contains("LM Studio")
      .click({ force: true });
    cy.wizardNext();

    // ── Step 3: Auth — LM Studio needs no key ─────────────────
    cy.wizardNext();

    // ── Step 4: Model ─────────────────────────────────────────
    cy.get("[role='dialog']")
      .find("input[placeholder='gpt-4o']")
      .clear()
      .type("qwen/qwen3.5-9b");
    cy.wizardNext();

    // ── Step 5: Skills — accept defaults ──────────────────────
    cy.wizardNext();

    // ── Step 6: Heartbeat — none ──────────────────────────────
    cy.get("[role='dialog']")
      .contains("button", "No heartbeat")
      .click({ force: true });
    cy.wizardNext();

    // ── Step 7: Channels — skip ───────────────────────────────
    cy.wizardNext();

    // ── Step 8: Confirm ───────────────────────────────────────
    cy.get("[role='dialog']").contains(INSTANCE);
    cy.get("[role='dialog']").contains("lm-studio");
    cy.get("[role='dialog']").contains("qwen/qwen3.5-9b");
    cy.get("[role='dialog']")
      .contains("button", "Create")
      .click({ force: true });

    cy.get("[role='dialog']").should("not.exist", { timeout: 20000 });
    cy.contains(INSTANCE, { timeout: 20000 }).should("be.visible");
  });

  it(`dispatches ${RUN_COUNT} ad-hoc runs against the instance`, () => {
    TASKS.forEach((task) => {
      cy.request({
        method: "POST",
        url: "/api/v1/runs?namespace=default",
        headers: authHeaders(),
        body: {
          agentRef: INSTANCE,
          task,
        },
      }).then((resp) => {
        expect(resp.status).to.eq(201);
        expect(resp.body?.metadata?.name).to.be.a("string").and.not.be.empty;
      });
    });

    // All runs should appear on the Runs page tied to this instance.
    cy.visit("/runs");
    cy.contains("td", INSTANCE, { timeout: 20000 }).should("exist");
    TASKS.forEach((task) => {
      cy.contains(task, { timeout: 20000 }).should("exist");
    });
  });

  it("deletes the instance and confirms it is gone", () => {
    const token = Cypress.env("API_TOKEN");

    cy.request({
      method: "DELETE",
      url: `/api/v1/agents/${INSTANCE}?namespace=default`,
      headers: token ? { Authorization: `Bearer ${token}` } : {},
    }).then((resp) => {
      expect(resp.status).to.be.oneOf([200, 202, 204]);
    });

    // API: subsequent GET should eventually 404 (finalizers may delay removal).
    cy.waitForDeleted(`/api/v1/agents/${INSTANCE}?namespace=default`);

    // UI: instance no longer shown in the list.
    cy.visit("/agents");
    cy.contains(INSTANCE, { timeout: 20000 }).should("not.exist");
  });
});

export {};
