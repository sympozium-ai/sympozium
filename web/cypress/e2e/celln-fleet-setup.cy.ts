/**
 * Create Agent with no Celln fleet: the console hands the operator the opt-in
 * install command (Celln is not part of the default install, #693, and holds
 * no model, so nothing about a provider or key is asked) and, while it waits, unlocks
 * the Celln plane as soon as the fleet's profiles reach the namespace.
 * Intercepted browser contract tests; no cluster.
 */

const profile = {
  name: "celln-native-starter", revision: "v1", policy: "celln-fleet-starter", model: "", provider: "", endpoint: "", systemPrompt: "",
  backend: "native", wrapper: "celln-native", agent: "", tools: [], mediationOnly: true,
  ceilings: { leaseSeconds: 86400, maxTurns: 256, maxModelRequests: 1536, maxOutputTokens: 786432 },
  sessionDefaults: { leaseSeconds: 14400, maxTurns: 64, maxModelRequests: 384, maxOutputTokens: 196608 },
};

const dialog = () => cy.get('[role="dialog"]').first();
const next = () => dialog().contains("button", "Next").click();

describe("Create Agent → Celln without a fleet", () => {
  it("hands over the opt-in install command, says what it deploys, and continues on Celln once the fleet appears", () => {
    let installed = false;
    cy.intercept("GET", "**/api/v1/**", { body: [] });
    cy.intercept("GET", "**/api/v1/celln-platform/profiles*", (request) => request.reply({ body: installed ? [profile] : [] })).as("profiles");
    cy.intercept("GET", "**/api/v1/celln-platform/mediation*", { body: { enabled: true, allowedOrigins: [], deniedOrigins: [], policies: ["celln-fleet-starter"], routes: [], pending: [] } });
    cy.intercept("GET", "**/api/v1/capabilities*", { body: { celln: { available: false, state: "absent", reason: "no fleet" } } });

    cy.visit("/agents?create=1&kind=agent#token=test-token");
    dialog().find('input[placeholder="my-agent"]').type("mine");
    next();
    cy.get('[data-testid="celln-fleet-missing"]').should("contain", "Celln parent");
    cy.get('[data-testid="celln-fleet-setup-open"]').click();

    // No model, key or backend to choose: the fleet is one opt-in flag.
    cy.get('[data-testid="celln-fleet-command"]').should("have.text", "sympozium install --celln-fleet");
    cy.get('[data-testid="celln-fleet-setup"]').should("not.contain", "--celln-fleet-package").and("not.contain", "credential-env").and("not.contain", "read -rs").and("not.contain", "Model provider");
    cy.get('[data-testid="celln-fleet-provider"]').should("not.exist");
    cy.get('[data-testid="celln-fleet-model"]').should("not.exist");
    // The privilege the fleet needs is disclosed as the CLI discloses it.
    cy.get('[data-testid="celln-fleet-privilege"]')
      .should("contain", "celln.dev/kvm=true")
      .and("contain", "two privileged DaemonSets")
      .and("contain", "celln-node-configure")
      .and("contain", "/dev/kvm")
      .and("contain", "Nodes hold no model key")
      .and("not.contain", "--no-celln");
    cy.get('[data-testid="celln-fleet-waiting"]').should("be.visible");

    // The operator's install lands: the polling step selects Celln by itself.
    cy.then(() => { installed = true; });
    cy.get('[data-testid="celln-fleet-setup"]', { timeout: 15000 }).should("not.exist");
    cy.get('[data-testid="create-agent-execution-environment"]').should("contain", "Enduring Celln parent");
    next();
    // The Agent's own connection is its route: the provider presets are offered right away.
    cy.get('[data-testid="celln-provider-grid"]').should("exist");
    cy.get('[data-testid="celln-provider-anthropic"]').should("exist");
  });
});
