// Real API and Kubernetes resources supplied by the owned hardware proof.
// No intercepts and no API substitute for the form's create action.
describe("Live enduring Celln creation", () => {
  it("selects a harness, lends a tool and requests a persistent parent", () => {
    const namespace = Cypress.env("PROOF_NAMESPACE");
    const persona = Cypress.env("PARENT_SYSTEM_PROMPT");
    const starter = String(Cypress.env("STARTER_TOOLS")) === "true";
    expect(namespace).to.match(/^celln-parent-manager-/);
    expect(persona).to.be.a("string").and.not.be.empty;
    cy.visit("/runs?create=1", {
      onBeforeLoad(win) { win.localStorage.setItem("sympozium_namespace", namespace); },
    });
    cy.get('[role="dialog"]').find('[role="combobox"]').eq(0).click();
    cy.get('[role="option"]').contains(/^agent$/).click();
    cy.get('textarea[placeholder="Describe the task for the agent…"]').type(starter ? "Call workspace-write with name notes.txt, revision 0, content violet. On success reply only violet." : "My value is violet. Call uppercase with my value and answer only the tool result text.");
    cy.get('[role="dialog"]').find('[role="combobox"]').eq(1).click();
    cy.get('[role="option"]').contains(/^worker$/).click();
    cy.get('[role="dialog"]').find('[role="combobox"]').eq(2).click();
    cy.get('[role="option"]').contains("Celln —").click();
    cy.get('[data-testid="celln-enduring-opt-in"]').check();
    cy.get('[data-testid="celln-parent-system-prompt"]').type(persona, { parseSpecialCharSequences: false, delay: 0 });
    if (starter) {
      cy.contains("button", "Use approved starter tools (3/3)", { timeout: 15000 }).scrollIntoView().should("be.enabled").click();
      cy.get('[data-testid="celln-permission-preview"]').should("contain", "Run files: read").and("contain", "example.com");
    } else {
      cy.contains("label", "uppercase").find('input[type="checkbox"]').check();
    }
    cy.get('[data-testid="celln-require-tool-call"]').check();
    for (const [key, value] of Object.entries({ leaseSeconds: 180, maxTurns: 2, maxModelRequests: 6, maxOutputTokens: 3072 })) {
      cy.get(`[data-testid="celln-${key}"]`).clear().type(String(value));
    }
    cy.get('input[placeholder="5m"]').clear().type("180s");
    cy.contains("button", "Request enduring run").should("be.enabled").click();
    cy.get('[role="dialog"]').should("not.exist");
  });
});
