// Login flow: visiting / without a valid token should route to /login
// (or show a login prompt). With a valid token injected via localStorage,
// the app should load directly and persist across a reload.

describe("Login flow", () => {
  it("redirects to /login when no token is set", () => {
    // Our onBeforeLoad runs AFTER the support-override's token injection,
    // so the final state of localStorage on page boot has NO token.
    cy.visit("/", {
      failOnStatusCode: false,
      onBeforeLoad(win) {
        win.localStorage.removeItem("sympozium_token");
      },
    });
    cy.url({ timeout: 20000 }).should("include", "/login");
  });

  it("persists authenticated session across reload with a valid token", () => {
    cy.visit("/"); // token auto-injected via support override
    cy.contains(/dashboard|agents|instances|runs/i, { timeout: 20000 }).should(
      "exist",
    );
    cy.reload();
    cy.contains(/dashboard|agents|instances|runs/i, { timeout: 20000 }).should(
      "exist",
    );
  });
});

export {};
