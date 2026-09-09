// Read-only UX checks: opening help and configuring a form submits no runs.
describe("Concepts and execution choice", () => {
  it("explains the defaults and reveals technical terms on demand", () => {
    cy.visit("/runs");
    cy.get('button[aria-label="Concepts"]').click();
    cy.get('[role="dialog"]').within(() => {
      cy.contains("Start with an Agent. Give it work in a Run.").should("be.visible");
      cy.contains("Kubernetes is the default").should("exist");
      cy.get("details").should("not.have.attr", "open");
      cy.get("summary").scrollIntoView().click();
      cy.get("details").should("have.attr", "open");
      cy.contains("AgentRuntime / AgentHarness").should("exist");
      cy.contains("Parent loss is context loss").should("exist");
    });
    cy.get("body").type("{esc}");
    cy.get('[role="dialog"]').should("not.exist");
    cy.get('button[title="Collapse sidebar"]').click();
    cy.get('button[aria-label="Concepts"]').click();
    cy.contains("How Sympozium fits together").should("be.visible");
  });

  it("makes Celln an explicit choice at the start of New Run", () => {
    cy.visit("/runs?create=1");
    cy.get('[role="dialog"]').within(() => {
      cy.get('[data-testid="execution-environment"]').should("be.visible");
      cy.get('input[name="execution-environment"][value="job"]').should("be.checked");
      cy.get('input[name="execution-environment"][value="celln"]').check();
      cy.get('input[name="execution-environment"][value="celln"]').should("be.checked");
      cy.get('[data-testid="execution-environment"] [role="status"]').should("be.visible");
      cy.contains("Harness for this run").should("be.visible");
      cy.get('input[name="execution-environment"][value="job"]').check();
      cy.get('[data-testid="execution-environment"] [role="status"]').should("not.exist");
    });
  });

  it("keeps the concepts dialog within a narrow viewport", () => {
    cy.viewport(390, 740);
    cy.visit("/runs");
    cy.get('button[aria-label="Concepts"]').click();
    cy.get('[role="dialog"]').then(($dialog) => {
      const bounds = $dialog[0].getBoundingClientRect();
      expect(bounds.left).to.be.at.least(0);
      expect(bounds.right).to.be.at.most(390);
      expect(bounds.height).to.be.at.most(740);
    });
  });
});
