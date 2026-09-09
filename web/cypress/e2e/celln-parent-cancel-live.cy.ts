// Real browser/API/controller/TLS/native Celln/model path. No intercepts.
describe("Live child cancellation", () => {
  it("cancels a turn and continues with the same parent's original context", () => {
    const namespace = Cypress.env("PROOF_NAMESPACE");
    const run = Cypress.env("PROOF_RUN");
    const starter = String(Cypress.env("STARTER_TOOLS")) === "true";
    const violet = (_: number, element: HTMLElement) => !!element.textContent?.startsWith("Agent: ") && element.textContent.toLowerCase().includes("violet");
    expect(namespace).to.match(/^celln-parent-manager-/);
    cy.visit(`/runs/${encodeURIComponent(run)}`, {
      onBeforeLoad(win) { win.localStorage.setItem("sympozium_namespace", namespace); },
    });
    if (starter) {
      cy.get("p").filter(violet).should("have.length", 1);
      cy.get('[data-testid="celln-turn-message"]').type("Call https-fetch for https://example.com/. Reply with only the page title from the tool result.");
      cy.get('[data-testid="celln-turn-send"]').should("be.enabled").click();
      cy.contains("p", /Agent:.*Example Domain/i, { timeout: 45000 }).should("be.visible");
      cy.get('[data-testid="celln-turn-message"]').should("be.enabled").type("Call workspace-read for notes.txt. Reply only with the content returned by the tool.");
      cy.get('[data-testid="celln-turn-send"]').should("be.enabled").click();
      cy.get("p", { timeout: 45000 }).filter(violet, { timeout: 45000 }).should("have.length", 2);
      cy.reload();
    } else { cy.contains("Agent: VIOLET").should("be.visible"); }
    cy.get('[data-testid="celln-turn-message"]').type(starter ? "For this cancellable turn, call workspace-read for notes.txt and report its content." : "My value is orange. Call uppercase on my value and return the tool result.");
    cy.get('[data-testid="celln-turn-send"]').should("be.enabled").click();
    cy.contains(starter ? "You: For this cancellable turn" : "You: My value is orange", { timeout: 15000 }).should("be.visible");
    // Refresh real run/turn observations promptly; no substitute API mutation.
    cy.reload();
    cy.on("window:confirm", (message) => { expect(message).to.contain("persistent parent is not stopped"); return true; });
    cy.get('[data-testid="celln-turn-cancel"]', { timeout: 15000 }).should("be.enabled").click();
    cy.contains("Turn failed: Turn cancelled after child teardown.", { timeout: 30000 }).should("be.visible");
    cy.get('[data-testid="celln-turn-message"]', { timeout: 15000 }).should("be.enabled").type(starter ? "Call workspace-read for notes.txt again after cancellation. Reply only with the content returned by this tool call." : "Find my value in the first user message. Call uppercase on that original value now and return only the tool result.");
    cy.get('[data-testid="celln-turn-send"]').should("be.enabled").click();
    cy.get("p", { timeout: 45000 }).filter(violet, { timeout: 45000 }).should("have.length", starter ? 3 : 2);
    cy.reload();
    cy.contains("Turn failed: Turn cancelled after child teardown.").should("be.visible");
    cy.get('[data-testid="celln-turn-message"]', { timeout: 15000 }).should("be.enabled");
  });
});
