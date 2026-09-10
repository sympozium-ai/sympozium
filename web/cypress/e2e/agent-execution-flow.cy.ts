import { agentCreationSteps, executionFromWizard } from "../../src/lib/agent-execution";

// Requires the native starter catalogue in celln-agents. No fabricated API
// responses: the wizard creates a real Agent, reads it back and deletes it.
const namespace = "celln-agents";
const name = `cypress-native-flow-${Date.now()}`;
function request(method: string, path: string, body?: object) {
  return cy.request({ method, url: `/api/v1/${path}?namespace=${namespace}`, body,
    headers: { Authorization: `Bearer ${Cypress.env("API_TOKEN")}` }, failOnStatusCode: false });
}
function visit(path: string) {
  cy.visit(path, { onBeforeLoad(win) { win.localStorage.setItem("sympozium_namespace", namespace); } });
}

describe("Agent creation execution-plane flow", () => {
  after(() => { request("DELETE", `agents/${name}`); });

  it("uses the requested order and keeps native selections out of Kubernetes defaults", () => {
    expect(agentCreationSteps(true).slice(0, 5)).to.deep.equal(["name", "runtime", "plane", "skills", "tools"]);
    expect(agentCreationSteps(false)).not.to.include("tools");
    expect(executionFromWizard({ executionBackend: "job", model: "test", borrowedTools: [{ name: "stale", revision: "v1" }] })).to.deep.equal({ backend: "job", executionLifecycle: "one-shot" });
  });

  it("does not bypass the plane step for a preselected harness", () => {
    visit("/agents?create=1&runtime=celln-native");
    cy.get('input[placeholder="my-agent"]').type(name);
    cy.wizardNext();
    cy.contains("label", "Harness / Run").should("be.visible");
    cy.wizardNext();
    cy.get('[data-testid="create-agent-execution-environment"]').should("be.visible");
    cy.contains("This harness has no Kubernetes image").should("be.visible");
    cy.get('[role="dialog"]').contains("button", "Next").should("be.disabled");
  });

  it("creates a native Agent with pinned borrowed tools and matching YAML", () => {
    visit("/agents?create=1&runtime=celln-native");
    cy.get('input[placeholder="my-agent"]').type(name);
    cy.wizardNext();
    cy.wizardNext();
    cy.get('[data-testid="create-agent-execution-environment"]').contains("button", "Celln").click();
    cy.contains("label", "enduring").find("input").check();
    cy.wizardNext();
    cy.get('[data-testid="native-skill-compatibility"]').should("be.visible");
    cy.get('[role="dialog"]').contains("button", "Next").should("be.disabled");
    cy.contains("button", "Continue without SkillPacks").click();
    cy.wizardNext();
    cy.get('[data-testid="create-agent-borrowed-tools"]').within(() => {
      cy.contains("label", "workspace-read@").find('input[type="checkbox"]').check();
      cy.contains("label", "workspace-write@").find('input[type="checkbox"]').check();
    });
    cy.wizardNext();
    cy.get("#native-model").should("have.value", "deepseek-chat");
    cy.wizardNext();
    cy.get('[data-testid="execution-confirmation"]').should("contain", "workspace-read@v1").and("contain", "workspace-write@v1");
    cy.contains("button", "YAML").click();
    cy.get('[role="dialog"]').last().should("contain", "backend: celln").and("contain", "toolRefs:").and("contain", "workspace-write").and("not.contain", "skillPackRef: memory");
    cy.get("body").type("{esc}");
    cy.get('[role="dialog"]').contains("button", /Create\s*$/).click();
    cy.get('[role="dialog"]').should("not.exist");
    request("GET", `agents/${name}`).then(({ status, body }) => {
      expect(status).to.equal(200);
      expect(body.spec.execution.backend).to.equal("celln");
      expect(body.spec.execution.executionLifecycle).to.equal("enduring");
      expect(body.spec.execution.cellnSelection.toolRefs).to.deep.equal([{ name: "workspace-read", revision: "v1" }, { name: "workspace-write", revision: "v1" }]);
      expect(body.spec.skills || []).to.have.length(0);
      expect(body.spec.authRefs || []).to.have.length(0);
      expect(body.spec.memory.enabled).to.equal(false);
    });
  });

  it("keeps SkillPacks in the Kubernetes flow and omits borrowing", () => {
    visit("/agents?create=1");
    cy.get('input[placeholder="my-agent"]').type("cypress-kubernetes-flow");
    cy.wizardNext();
    cy.contains("label", "Harness / Run").should("be.visible");
    cy.wizardNext();
    cy.get('[data-testid="create-agent-execution-environment"]').contains("Kubernetes");
    cy.wizardNext();
    cy.contains("Select SkillPacks to attach").should("be.visible");
    cy.get('[data-testid="native-skill-compatibility"]').should("not.exist");
    cy.wizardNext();
    cy.get('[data-testid="create-agent-borrowed-tools"]').should("not.exist");
    cy.contains("label", "AI Provider").should("exist");
  });
});
