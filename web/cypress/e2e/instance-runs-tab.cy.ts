// Instance detail — Runs tab: verify that runs for an instance are visible
// on the instance detail Runs tab and link to run detail.

const INSTANCE = `cy-runstab-${Date.now()}`;
let RUN_NAME = "";

describe("Instance Detail — Runs tab", () => {
  before(() => {
    cy.createLMStudioInstance(INSTANCE);
    cy.dispatchRun(INSTANCE, "Runs tab visibility test").then((name) => {
      RUN_NAME = name;
    });
  });

  after(() => {
    if (RUN_NAME) cy.deleteRun(RUN_NAME);
    cy.deleteInstance(INSTANCE);
  });

  it("shows the run on the instance Runs tab", () => {
    cy.visit(`/instances/${INSTANCE}`);

    // Click the Runs tab.
    cy.contains("button", "Runs", { timeout: 20000 }).click();

    // The dispatched run should appear in the list.
    cy.contains(RUN_NAME, { timeout: 20000 }).should("be.visible");
    cy.contains("Runs tab visibility test").should("exist");
  });

  it("links from the Runs tab to run detail", () => {
    cy.visit(`/instances/${INSTANCE}`);
    cy.contains("button", "Runs", { timeout: 20000 }).click();

    // Click the run link.
    cy.contains("a", RUN_NAME, { timeout: 20000 }).click();
    cy.url().should("include", `/runs/${RUN_NAME}`);
  });
});

export {};
