// Launched by the real Celln/KVM + API/controller manager proof. No intercepts.
describe("Live persistent Celln conversation", () => {
  it("sends a follow-up from the UI and renders retained-context tool output", () => {
    const namespace = Cypress.env("PROOF_NAMESPACE");
    const run = Cypress.env("PROOF_RUN");
    const starter = String(Cypress.env("STARTER_TOOLS")) === "true";
    const answer = (_: number, element: HTMLElement) => starter ? element.textContent?.startsWith("Agent: ") && element.textContent.toLowerCase().includes("violet") : element.textContent === "Agent: VIOLET";
    expect(namespace).to.match(/^celln-parent-manager-/);
    expect(run).to.be.a("string").and.not.be.empty;
    cy.visit(`/runs/${encodeURIComponent(run)}`, {
      onBeforeLoad(win) { win.localStorage.setItem("sympozium_namespace", namespace); },
    });
    if (Cypress.env("EXPECT_STREAM")) cy.contains("Stream Connected").should("be.visible");
    cy.get('[data-testid="celln-conversation"]').within(() => {
      cy.contains("Parent initialized").should("be.visible");
      cy.get("p").filter(answer).should("have.length", 1);
      cy.get('[data-testid="celln-turn-message"]').type(starter ? "Call workspace-read for notes.txt now. Reply only with the content returned by the tool." : "Find the value stated in the first user message of this conversation. You must invoke the uppercase tool on that original value now, even if you already know its uppercase form. Do not answer from memory or reuse a previous tool result. Answer only the text returned by this new tool call.");
      cy.get('[data-testid="celln-turn-send"]').click();
      cy.contains("p", starter ? "You: Call workspace-read" : "You: Find the value", {timeout:45000}).should("be.visible");
      cy.get("p", {timeout:45000}).filter(answer, {timeout:45000}).should("have.length",2);
    });
    cy.reload();
    if (Cypress.env("EXPECT_STREAM")) cy.contains("Stream Connected").should("be.visible");
    cy.get('[data-testid="celln-conversation"]').within(() => {
      cy.get("p").filter(answer).should("have.length",2);
      // Two-turn ceiling is exhausted, not automatically replenished by reload.
      cy.get('[data-testid="celln-turn-send"]').should("be.disabled");
    });
  });
});
