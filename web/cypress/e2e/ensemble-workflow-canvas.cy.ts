/**
 * Ensemble Workflow Canvas — tests for the relationship graph visualization
 * and interactive editing on the persona detail page.
 */

const PACK_NAME = `cypress-workflow-${Date.now()}`;
const NS = "default";

function apiHeaders() {
	const token = Cypress.env("API_TOKEN");
	return token
		? { Authorization: `Bearer ${token}`, "Content-Type": "application/json" }
		: { "Content-Type": "application/json" };
}

describe("Ensemble Workflow Canvas", () => {
	before(() => {
		// Create pack with relationships via API PATCH (two-step: create then patch)
		const manifest = `
apiVersion: sympozium.ai/v1alpha1
kind: Ensemble
metadata:
  name: ${PACK_NAME}
  namespace: ${NS}
spec:
  enabled: false
  description: Cypress workflow canvas test pack
  category: test
  version: "1.0"
  workflowType: delegation
  agentConfigs:
    - name: researcher
      displayName: Researcher
      systemPrompt: "You are a researcher."
      skills: [memory]
    - name: writer
      displayName: Writer
      systemPrompt: "You are a technical writer."
      skills: [memory]
    - name: reviewer
      displayName: Reviewer
      systemPrompt: "You are a code reviewer."
      skills: [memory]
  relationships:
    - source: researcher
      target: writer
      type: delegation
    - source: writer
      target: reviewer
      type: sequential
`;
		cy.writeFile(`cypress/tmp/${PACK_NAME}.yaml`, manifest);
		cy.exec(`kubectl apply -f cypress/tmp/${PACK_NAME}.yaml`);
		// Verify it was created with relationships
		cy.request({
			url: `/api/v1/ensembles/${PACK_NAME}?namespace=${NS}`,
			headers: apiHeaders(),
		}).then((resp) => {
			expect(resp.status).to.eq(200);
			const rels = resp.body.spec.relationships || [];
			expect(rels.length).to.be.greaterThan(0);
		});
	});

	after(() => {
		cy.deleteEnsemble(PACK_NAME);
		cy.exec(`rm -f cypress/tmp/${PACK_NAME}.yaml`, {
			failOnNonZeroExit: false,
		});
	});

	it("shows the Workflow tab on persona detail page", () => {
		cy.visit(`/ensembles/${PACK_NAME}`);
		cy.contains("Workflow").should("be.visible");
		cy.contains("Overview").should("be.visible");
	});

	it("renders persona nodes on the canvas", () => {
		cy.visit(`/ensembles/${PACK_NAME}?tab=workflow`);
		cy.contains("Researcher", { timeout: 10000 }).should("be.visible");
		cy.contains("Writer").should("be.visible");
		cy.contains("Reviewer").should("be.visible");
	});

	it("shows the relationships table below the canvas", () => {
		cy.visit(`/ensembles/${PACK_NAME}?tab=workflow`);
		// The relationship table renders source/target persona names as badges
		// Wait for the canvas content to load first
		cy.contains("Persona Workflow", { timeout: 10000 }).should("be.visible");
		// Scroll down to find the relationships card
		cy.contains("3 agents with 2 relationships").should("be.visible");
	});

	it("switches between Overview and Workflow tabs via URL", () => {
		cy.visit(`/ensembles/${PACK_NAME}`);
		// Default is overview — should see agents section
		cy.contains("Agents", { timeout: 10000 }).should("be.visible");

		// Switch to workflow via click
		cy.contains("Workflow").click();
		cy.url().should("include", "tab=workflow");
		cy.contains("Persona Workflow").should("be.visible");

		// Switch back
		cy.contains("Overview").click();
		cy.url().should("include", "tab=overview");
		cy.contains("Agents", { timeout: 10000 }).should("be.visible");
	});

	it("shows Save button and drag hint on the workflow canvas", () => {
		cy.visit(`/ensembles/${PACK_NAME}?tab=workflow`);
		cy.contains("button", "Save", { timeout: 10000 }).should("be.visible");
		cy.contains("Drag from one persona").should("be.visible");
	});

	it("canvas has zoom controls and minimap", () => {
		cy.visit(`/ensembles/${PACK_NAME}?tab=workflow`);
		// ReactFlow controls panel
		cy.get(".react-flow__controls", { timeout: 10000 }).should("be.visible");
		// ReactFlow minimap
		cy.get(".react-flow__minimap").should("be.visible");
	});
});

describe("Ensemble Workflow — no relationships", () => {
	const EMPTY_PACK = `cypress-empty-wf-${Date.now()}`;

	before(() => {
		const manifest = `
apiVersion: sympozium.ai/v1alpha1
kind: Ensemble
metadata:
  name: ${EMPTY_PACK}
  namespace: ${NS}
spec:
  enabled: false
  description: Pack with no relationships
  agentConfigs:
    - name: solo
      displayName: Solo Agent
      systemPrompt: "You work alone."
`;
		cy.writeFile(`cypress/tmp/${EMPTY_PACK}.yaml`, manifest);
		cy.exec(`kubectl apply -f cypress/tmp/${EMPTY_PACK}.yaml`);
	});

	after(() => {
		cy.deleteEnsemble(EMPTY_PACK);
		cy.exec(`rm -f cypress/tmp/${EMPTY_PACK}.yaml`, {
			failOnNonZeroExit: false,
		});
	});

	it("shows guidance text when no relationships exist", () => {
		cy.visit(`/ensembles/${EMPTY_PACK}?tab=workflow`);
		cy.contains("Define relationships between agents", {
			timeout: 10000,
		}).should("be.visible");
	});

	it("renders the single agent node", () => {
		cy.visit(`/ensembles/${EMPTY_PACK}?tab=workflow`);
		cy.contains("Solo Agent", { timeout: 10000 }).should("be.visible");
	});
});

describe("Ensemble Workflow — PATCH relationships API", () => {
	const API_PACK = `cypress-api-wf-${Date.now()}`;

	before(() => {
		const manifest = `
apiVersion: sympozium.ai/v1alpha1
kind: Ensemble
metadata:
  name: ${API_PACK}
  namespace: ${NS}
spec:
  enabled: false
  agentConfigs:
    - name: alpha
      systemPrompt: "Agent alpha."
    - name: beta
      systemPrompt: "Agent beta."
`;
		cy.writeFile(`cypress/tmp/${API_PACK}.yaml`, manifest);
		cy.exec(`kubectl apply -f cypress/tmp/${API_PACK}.yaml`);
	});

	after(() => {
		cy.deleteEnsemble(API_PACK);
		cy.exec(`rm -f cypress/tmp/${API_PACK}.yaml`, {
			failOnNonZeroExit: false,
		});
	});

	it("saves relationships and workflowType via PATCH", () => {
		cy.request({
			method: "PATCH",
			url: `/api/v1/ensembles/${API_PACK}?namespace=${NS}`,
			headers: apiHeaders(),
			body: {
				relationships: [
					{ source: "alpha", target: "beta", type: "delegation" },
				],
				workflowType: "delegation",
			},
		}).then((resp) => {
			expect(resp.status).to.eq(200);
			expect(resp.body.spec.relationships).to.have.length(1);
			expect(resp.body.spec.relationships[0].source).to.eq("alpha");
			expect(resp.body.spec.relationships[0].target).to.eq("beta");
			expect(resp.body.spec.relationships[0].type).to.eq("delegation");
			expect(resp.body.spec.workflowType).to.eq("delegation");
		});
	});

	it("persisted relationships appear in the canvas UI", () => {
		// First ensure the relationship was saved (from previous test)
		cy.request({
			url: `/api/v1/ensembles/${API_PACK}?namespace=${NS}`,
			headers: apiHeaders(),
		}).then((resp) => {
			expect(resp.body.spec.relationships).to.have.length(1);
		});

		cy.visit(`/ensembles/${API_PACK}?tab=workflow`);
		cy.contains("alpha", { timeout: 10000 }).should("be.visible");
		cy.contains("beta").should("be.visible");
		cy.contains("Persona Workflow").should("be.visible");
	});

	it("can clear all relationships via PATCH", () => {
		cy.request({
			method: "PATCH",
			url: `/api/v1/ensembles/${API_PACK}?namespace=${NS}`,
			headers: apiHeaders(),
			body: {
				relationships: [],
			},
		}).then((resp) => {
			expect(resp.status).to.eq(200);
			// Empty array or null — both mean no relationships
			const rels = resp.body.spec.relationships || [];
			expect(rels).to.have.length(0);
		});
	});
});

export {};
