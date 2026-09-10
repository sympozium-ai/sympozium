// Exercise the installed catalogue without creating agents or model calls.
describe("Persistent harness creation", () => {
  for (const harness of ["Pi", "Hermes"]) {
    it(`offers ${harness} and progresses directly to provider setup`, () => {
      cy.visit("/agents?create=1");
      cy.get('[role="dialog"]').within(() => {
        cy.get('input[placeholder="my-agent"]').type("persistent-choice-check");
        cy.contains("button", "Next").click();
        cy.contains("Choose a persistent harness").should("be.visible");
        cy.contains("button", "Next").should("be.disabled");
        cy.get('[role="combobox"]').click();
      });
      cy.get('[role="option"]').should("have.length", 2);
      cy.get('[role="option"]').each(($option) => {
        expect($option.text()).to.match(/^(Pi|Hermes) — persistent chat$/);
      });
      cy.contains('[role="option"]', `${harness} — persistent chat`).click();
      cy.get('[role="dialog"]').within(() => {
        cy.contains(`Harness selected: ${harness}.`).should("be.visible");
        cy.contains("persistent Kubernetes session").should("be.visible");
        cy.contains("Default Celln lifecycle").should("not.exist");
        cy.contains("button", "Next").click();
        cy.contains("AI Provider").should("be.visible");
      });
    });
  }
  it("retains a preselected persistent harness while the catalogue loads", () => {
    cy.visit("/agents?create=1&runtime=pi-session-v0-84-4&policy=harness-examples");
    cy.get('[role="dialog"]').within(() => {
      cy.get('input[placeholder="my-agent"]').type("preselected-choice-check");
      cy.contains("button", "Next").click();
      cy.contains("AI Provider").should("be.visible");
      cy.contains("Choose a persistent harness").should("not.exist");
    });
  });
});
