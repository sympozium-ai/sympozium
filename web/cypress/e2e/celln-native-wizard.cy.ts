const NS = "default";

function visitInNamespace(path: string) {
  cy.visit(path, {
    onBeforeLoad(win) {
      win.localStorage.setItem("sympozium_namespace", NS);
    },
  });
}

describe("Native Celln harness wizard", () => {
  it("does not list the native Celln runtime as a Kubernetes harness", () => {
    visitInNamespace("/harnesses");
    cy.contains("Harnesses").should("be.visible");
    cy.contains("pi-session-v0-84-4").should("be.visible");
    cy.contains("celln-native").should("not.exist");
  });
});
