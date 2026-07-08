/**
 * Auto-login via URL fragment: `sympozium serve` prints a URL like
 * http://localhost:9090/#token=<token>. Visiting it must store the token,
 * scrub it from the address bar, and land on the dashboard instead of the
 * login page. Self-contained — needs no API server or model.
 */

describe("URL fragment auto-login", () => {
  it("stores the token, scrubs the fragment, and skips the login page", () => {
    cy.visit("/#token=test-token-123", {
      onBeforeLoad(win) {
        win.localStorage.removeItem("sympozium_token");
      },
    });

    // Token is persisted for API calls.
    cy.window()
      .its("localStorage")
      .invoke("getItem", "sympozium_token")
      .should("eq", "test-token-123");

    // The fragment is scrubbed from the address bar.
    cy.location("hash").should("eq", "");

    // Authenticated: the app shell renders instead of the login form.
    cy.contains("Enter your API token").should("not.exist");
    cy.get("nav").should("exist");
  });

  it("still shows the login page without a token", () => {
    cy.visit("/", {
      onBeforeLoad(win) {
        win.localStorage.removeItem("sympozium_token");
      },
    });
    cy.contains("Enter your API token").should("exist");
  });
});
