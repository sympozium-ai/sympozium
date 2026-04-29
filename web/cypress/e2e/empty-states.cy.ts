// Empty states: verify contextual empty-state messages render with helpful
// links when no resources exist. Uses search filtering to guarantee the
// empty state is shown without needing a truly empty cluster.

describe("Empty States — contextual guidance", () => {
  it("shows contextual empty state on instances page", () => {
    cy.visit("/agents");

    // Search for something guaranteed not to exist.
    cy.get("input[placeholder*='Search']", { timeout: 20000 })
      .clear()
      .type("zzz-nonexistent-xyz");

    cy.contains("No agents match your search", { timeout: 10000 }).should(
      "be.visible",
    );
  });

  it("shows contextual empty state on runs page", () => {
    cy.visit("/runs");

    cy.get("input[placeholder*='Search']", { timeout: 20000 })
      .clear()
      .type("zzz-nonexistent-xyz");

    cy.contains("No runs match your search", { timeout: 10000 }).should(
      "be.visible",
    );
  });

  it("shows contextual empty state on persona packs page", () => {
    cy.visit("/ensembles");

    cy.get("input[placeholder*='Search']", { timeout: 20000 })
      .clear()
      .type("zzz-nonexistent-xyz");

    cy.contains("No ensembles match your search", {
      timeout: 10000,
    }).should("be.visible");
  });
});

export {};
