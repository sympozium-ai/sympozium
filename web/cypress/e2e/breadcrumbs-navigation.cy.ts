// Breadcrumbs: verify breadcrumb links render on detail pages and navigate
// correctly. Covers instance detail, run detail, and persona pack detail.

const INSTANCE = `cy-breadcrumbs-${Date.now()}`;
let RUN_NAME = "";

describe("Breadcrumbs — detail page navigation", () => {
  before(() => {
    cy.createLMStudioAgent(INSTANCE);
    cy.dispatchRun(INSTANCE, "Breadcrumb nav test").then((name) => {
      RUN_NAME = name;
    });
  });

  after(() => {
    if (RUN_NAME) cy.deleteRun(RUN_NAME);
    cy.deleteAgent(INSTANCE);
  });

  it("shows breadcrumbs on the instance detail page", () => {
    cy.visit(`/agents/${INSTANCE}`);

    // Breadcrumb trail should contain Ensembles, Instances, and the name.
    cy.contains("nav", "Ensembles", { timeout: 20000 }).should("exist");
    cy.contains("nav", "Agents").should("exist");
    cy.contains("nav", INSTANCE).should("exist");

    // Clicking "Agents" breadcrumb should navigate to the list.
    cy.get("nav").contains("a", "Agents").click();
    cy.url().should("include", "/agents");
  });

  it("shows breadcrumbs on the run detail page with instance link", () => {
    cy.visit(`/runs/${RUN_NAME}`);

    // Breadcrumb should include the instance ref as a clickable link.
    cy.contains("nav", INSTANCE, { timeout: 20000 }).should("exist");
    cy.contains("nav", RUN_NAME).should("exist");

    // Clicking the instance breadcrumb should navigate to instance detail.
    cy.get("nav").contains("a", INSTANCE).click();
    cy.url().should("include", `/agents/${INSTANCE}`);
  });
});

export {};
