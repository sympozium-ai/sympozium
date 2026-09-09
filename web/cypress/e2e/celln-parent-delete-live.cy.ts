// Real API/Kubernetes/controller/Celln deletion. No intercepted responses.
describe("Live persistent Celln deletion", () => {
  it("refuses a stale UID and deletes only the original run through the UI", () => {
    const namespace = Cypress.env("PROOF_NAMESPACE");
    const run = Cypress.env("PROOF_RUN");
    const uid = Cypress.env("PROOF_UID");
    expect(namespace).to.match(/^celln-parent-manager-/);
    expect(uid).to.be.a("string").and.not.be.empty;
    cy.visit(`/runs/${encodeURIComponent(run)}`, {
      onBeforeLoad(win) { win.localStorage.setItem("sympozium_namespace", namespace); },
    });
    cy.get('[data-testid="celln-conversation"]').should("be.visible");
    cy.window().then(async (win) => {
      const path = `/api/v1/runs/${encodeURIComponent(run)}?namespace=${encodeURIComponent(namespace)}`;
      const headers = { Authorization: `Bearer ${win.localStorage.getItem("sympozium_token")}` };
      const refused = await win.fetch(`${path}&uid=not-the-original-uid`, { method: "DELETE", headers });
      expect(refused.status).to.eq(409);
      const stillPresent = await win.fetch(path, { headers });
      expect(stillPresent.status).to.eq(200);
      expect((await stillPresent.json()).metadata.uid).to.eq(uid);
    });
    cy.on("window:confirm", (message) => { expect(message).to.contain("not a pause"); return true; });
    cy.get('[data-testid="celln-delete-run"]').click();
    cy.window().should((win) => expect(win.sessionStorage.getItem(`celln-delete:${namespace}:${uid}`)).to.eq("requested"));
    // Kubernetes finalizer disappearance is checked independently by the driver;
    // the outer Rust proof checks the original owner's joined teardown/capacity.
    cy.contains("Run not found", { timeout: 20000 }).should("be.visible");
  });
});
