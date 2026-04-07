// Breadcrumbs: verify breadcrumb links render on detail pages and navigate
// correctly. Covers instance detail, run detail, and persona pack detail.

const INSTANCE = `cy-breadcrumbs-${Date.now()}`;
let RUN_NAME = "";

describe("Breadcrumbs — detail page navigation", () => {
  before(() => {
    cy.createLMStudioInstance(INSTANCE);
    cy.dispatchRun(INSTANCE, "Breadcrumb nav test").then((name) => {
      RUN_NAME = name;
    });
  });

  after(() => {
    if (RUN_NAME) cy.deleteRun(RUN_NAME);
    cy.deleteInstance(INSTANCE);
  });

  it("shows breadcrumbs on the instance detail page", () => {
    cy.visit(`/instances/${INSTANCE}`);

    // Breadcrumb trail should contain Persona Packs, Instances, and the name.
    cy.contains("nav", "Persona Packs", { timeout: 20000 }).should("exist");
    cy.contains("nav", "Instances").should("exist");
    cy.contains("nav", INSTANCE).should("exist");

    // Clicking "Instances" breadcrumb should navigate to the list.
    cy.get("nav").contains("a", "Instances").click();
    cy.url().should("include", "/instances");
  });

  it("shows breadcrumbs on the run detail page with instance link", () => {
    cy.visit(`/runs/${RUN_NAME}`);

    // Breadcrumb should include the instance ref as a clickable link.
    cy.contains("nav", INSTANCE, { timeout: 20000 }).should("exist");
    cy.contains("nav", RUN_NAME).should("exist");

    // Clicking the instance breadcrumb should navigate to instance detail.
    cy.get("nav").contains("a", INSTANCE).click();
    cy.url().should("include", `/instances/${INSTANCE}`);
  });
});

export {};
