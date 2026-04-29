// Test: enable a Ensemble through the onboarding wizard.
// Installs default packs if needed, then enables one with LM Studio.

describe("Ensemble — Enable via Wizard", () => {
  let packName: string;

  function authHeaders(): Record<string, string> {
    const token = Cypress.env("API_TOKEN");
    const h: Record<string, string> = { "Content-Type": "application/json" };
    if (token) h["Authorization"] = `Bearer ${token}`;
    return h;
  }

  before(() => {
    // Install default packs so we have something to enable.
    cy.request({
      method: "POST",
      url: "/api/v1/ensembles/install-defaults?namespace=default",
      headers: authHeaders(),
      failOnStatusCode: false,
    });
    // Disable all packs so the test has at least one to enable.
    cy.request({
      url: "/api/v1/ensembles?namespace=default",
      headers: authHeaders(),
    }).then((resp) => {
      const packs = resp.body?.items || resp.body || [];
      for (const p of packs) {
        if (p.spec?.enabled) {
          cy.request({
            method: "PATCH",
            url: `/api/v1/ensembles/${p.metadata.name}?namespace=default`,
            body: { enabled: false },
            headers: authHeaders(),
            failOnStatusCode: false,
          });
        }
      }
    });
  });

  after(() => {
    if (packName) {
      cy.request({
        method: "PATCH",
        url: `/api/v1/ensembles/${packName}?namespace=default`,
        body: { enabled: false },
        headers: authHeaders(),
        failOnStatusCode: false,
      });
    }
  });

  it("selects a ensemble, configures LM Studio, and activates it", () => {
    cy.visit("/ensembles");

    // Wait for the ensembles to render and find an Enable button.
    cy.contains("button", "Enable", { timeout: 20000 }).should("be.visible");

    // Capture the pack name from the row and click Enable.
    cy.contains("button", "Enable")
      .first()
      .then(($btn) => {
        const row = $btn.closest("tr");
        if (row.length) {
          packName = row.find("td").first().text().trim();
        }
      })
      .click({ force: true });

    // ── Wizard opens in persona mode ──────────────────────────
    // Step: Provider — select LM Studio.
    cy.get("[role='dialog']")
      .find("button[role='combobox']")
      .click({ force: true });
    cy.get("[data-radix-popper-content-wrapper]")
      .contains("LM Studio")
      .click({ force: true });
    cy.wizardNext();

    // Step: Auth — LM Studio needs no key.
    cy.wizardNext();

    // Step: Model — type the model name.
    cy.get("[role='dialog']")
      .find("input[placeholder='gpt-4o']")
      .clear()
      .type("qwen/qwen3.5-9b");
    cy.wizardNext();

    // Step: Skills — accept defaults.
    cy.wizardNext();

    // Step: Heartbeat — accept ensemble default.
    cy.get("[role='dialog']")
      .contains("button", "Ensemble default")
      .click({ force: true });
    cy.wizardNext();

    // Step: Channels — skip.
    cy.wizardNext();

    // Step: Confirm — verify summary and activate (or finalize channels first).
    cy.get("[role='dialog']").contains("lm-studio");
    cy.get("[role='dialog']").contains("qwen/qwen3.5-9b");
    cy.get("[role='dialog']").then(($dialog) => {
      // If the ensemble has channels, the button says "Finalize Channels"
      // and we need to go through channel action steps before the dialog closes.
      if ($dialog.text().includes("Finalize Channels")) {
        cy.wrap($dialog)
          .contains("button", "Finalize Channels")
          .click({ force: true });
        // Complete any channel action steps
        const finishChannels = (): void => {
          cy.get("[role='dialog']", { timeout: 5000 }).then(($d) => {
            if ($d.length && $d.find("button:contains('Next Channel')").length) {
              cy.contains("button", "Next Channel").click({ force: true });
              finishChannels();
            } else if ($d.length && $d.find("button:contains('Activate')").length) {
              cy.contains("button", "Activate").click({ force: true });
            }
          });
        };
        finishChannels();
      } else {
        cy.wrap($dialog)
          .contains("button", "Activate")
          .click({ force: true });
      }
    });

    // Wait for dialog to close.
    cy.get("[role='dialog']").should("not.exist", { timeout: 20000 });

    // ── Verify instances were created by the pack ─────────────
    cy.visit("/agents");
    // Ensemble activation creates stamped instances — at least one should exist.
    cy.get("table", { timeout: 20000 }).should("exist");
  });
});
