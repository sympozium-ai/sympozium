import { agentCreationSteps, executionFromWizard } from "../../src/lib/agent-execution";

// The Celln side of the wizard (a provider route, the Agent's own key, a
// declared model) is covered without a cluster by celln-own-key.cy.ts.
describe("Agent creation execution-plane flow", () => {

  it("uses the requested order and keeps native selections out of Kubernetes defaults", () => {
    expect(agentCreationSteps(true).slice(0, 5)).to.deep.equal(["name", "plane", "provider", "apikey", "model"]);
    expect(agentCreationSteps(true)).not.to.include("runtime");
    expect(agentCreationSteps(false)).to.include("runtime");
    expect(executionFromWizard({ executionBackend: "job", model: "test", borrowedTools: [{ name: "stale", revision: "v1" }] })).to.deep.equal({ backend: "job", executionLifecycle: "one-shot" });
  });

  it("keeps SkillPacks in the Kubernetes harness flow and omits borrowing", () => {
    // The default namespace carries the curated Pi/Hermes persistent harnesses.
    cy.visit("/agents?create=1&kind=agent");
    cy.get('input[placeholder="my-agent"]').type("cypress-kubernetes-flow");
    cy.wizardNext();
    cy.get('[data-testid="create-agent-execution-environment"]').contains("button", "Kubernetes").click();
    cy.wizardNext();
    cy.contains("Choose a persistent harness").should("be.visible");
    cy.get('[role="dialog"]').find('[role="combobox"]').click();
    cy.get('[role="option"]').contains("Pi — persistent chat").click();
    cy.wizardNext();
    cy.contains("Select SkillPacks to attach").should("be.visible");
    cy.get('[data-testid="create-agent-borrowed-tools"]').should("not.exist");
    cy.get('[data-testid="borrowed-tool-availability"]').should("contain", "cannot borrow native Celln tools");
  });
});
