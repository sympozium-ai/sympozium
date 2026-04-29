// Regression guard: the run detail page MUST display status.result when it
// is populated on the CR. Directly guards against the session-discovered bug
// where LM Studio runs emitted output tokens but the UX showed "No result
// available" (because the response field was silently dropped).

const INSTANCE = `cy-runvis-${Date.now()}`;
let RUN_NAME = "";

describe("Run Detail — response visibility", () => {
  before(() => {
    cy.createLMStudioAgent(INSTANCE);
  });

  after(() => {
    if (RUN_NAME) cy.deleteRun(RUN_NAME);
    cy.deleteAgent(INSTANCE);
  });

  it("shows status.result text on the Result tab when populated", () => {
    cy.dispatchRun(INSTANCE, "Say hello").then(
      (name) => {
        RUN_NAME = name;
        cy.waitForRunTerminal(name).then((phase) => {
          expect(phase).to.eq("Succeeded");
        });
      },
    );

    // Fetch the actual result from the API so we know what to look for.
    cy.then(() => {
      cy.request({
        url: `/api/v1/runs/${RUN_NAME}?namespace=default`,
        headers: {
          "Content-Type": "application/json",
          ...(Cypress.env("API_TOKEN")
            ? { Authorization: `Bearer ${Cypress.env("API_TOKEN")}` }
            : {}),
        },
      }).then((resp) => {
        const result = (resp.body?.status?.result || "") as string;
        expect(result.length, "API should return a non-empty result").to.be.greaterThan(0);

        // Navigate to the run detail page.
        cy.visit(`/runs/${RUN_NAME}`);

        // Result tab selected by default (or click it).
        cy.contains("button", "Result", { timeout: 20000 }).click({ force: true });

        // The first few words of the result should be visible in the UI.
        const snippet = result.split(/\s+/).slice(0, 3).join(" ");
        cy.contains(snippet, { timeout: 20000 }).should("be.visible");

        // The "No result available" fallback must NOT be showing.
        cy.contains("No result available").should("not.exist");
      });
    });
  });
});

export {};
