// Create a schedule via the UI dialog, then verify it surfaces on /schedules
// with the correct cron.

const INSTANCE = `cy-scui-${Date.now()}`;
const SCHEDULE = `cy-scui-sched-${Date.now()}`;

describe("Schedule — create via UI", () => {
  before(() => {
    cy.createLMStudioInstance(INSTANCE);
  });

  after(() => {
    cy.deleteSchedule(SCHEDULE);
    cy.deleteInstance(INSTANCE);
  });

  it("creates a schedule and surfaces it on the list", () => {
    cy.visit("/schedules");

    cy.contains("button", /Create Schedule|New Schedule/, { timeout: 20000 }).click({ force: true });

    cy.get("[role='dialog']", { timeout: 10000 }).within(() => {
      // Name field — placeholder is "my-heartbeat".
      cy.get("input[placeholder='my-heartbeat']").clear().type(SCHEDULE);

      // Instance selector — Radix Select trigger has role="combobox".
      cy.get("[role='combobox']").first().click({ force: true });
    });
    // Select the instance from the Radix portal (rendered outside the dialog).
    cy.contains("[role='option']", INSTANCE, { timeout: 10000 }).click({ force: true });

    cy.get("[role='dialog']").within(() => {
      // Cron field — already defaults to */5 * * * *, but set it explicitly.
      cy.get("input[placeholder='*/5 * * * *']").clear().type("*/5 * * * *");

      // Task textarea.
      cy.get("textarea").type("scheduled cypress test");

      // Submit.
      cy.contains("button", "Create Schedule").click({ force: true });
    });

    // Wait for dialog to close.
    cy.get("[role='dialog']").should("not.exist", { timeout: 20000 });

    cy.visit("/schedules");
    cy.contains(SCHEDULE, { timeout: 20000 }).should("exist");
    cy.contains(/\*\/5/).should("exist");
  });
});

export {};
