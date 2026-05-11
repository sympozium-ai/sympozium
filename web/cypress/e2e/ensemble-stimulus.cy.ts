/**
 * Ensemble Stimulus — tests for the stimulus configuration, API trigger,
 * and workflow canvas rendering.
 */

const PACK_NAME = `cypress-stimulus-${Date.now()}`;
const NS = "default";

function apiHeaders() {
	const token = Cypress.env("API_TOKEN");
	return token
		? { Authorization: `Bearer ${token}`, "Content-Type": "application/json" }
		: { "Content-Type": "application/json" };
}

describe("Ensemble Stimulus", () => {
	before(() => {
		const manifest = `
apiVersion: sympozium.ai/v1alpha1
kind: Ensemble
metadata:
  name: ${PACK_NAME}
  namespace: ${NS}
spec:
  enabled: false
  description: Cypress stimulus test pack
  category: test
  version: "1.0"
  workflowType: pipeline
  stimulus:
    name: kickoff
    prompt: "Begin the research workflow. Summarize recent developments."
  agentConfigs:
    - name: lead
      displayName: Lead Researcher
      systemPrompt: "You are a lead researcher."
      skills: [memory]
    - name: analyst
      displayName: Analyst
      systemPrompt: "You are an analyst."
      skills: [memory]
  relationships:
    - source: kickoff
      target: lead
      type: stimulus
    - source: lead
      target: analyst
      type: sequential
`;
		cy.writeFile(`cypress/tmp/${PACK_NAME}.yaml`, manifest);
		cy.exec(`kubectl apply -f cypress/tmp/${PACK_NAME}.yaml`);
		cy.request({
			url: `/api/v1/ensembles/${PACK_NAME}?namespace=${NS}`,
			headers: apiHeaders(),
		}).then((resp) => {
			expect(resp.status).to.eq(200);
			expect(resp.body.spec.stimulus).to.not.be.undefined;
			expect(resp.body.spec.stimulus.name).to.eq("kickoff");
		});
	});

	after(() => {
		cy.deleteEnsemble(PACK_NAME);
		cy.exec(`rm -f cypress/tmp/${PACK_NAME}.yaml`, {
			failOnNonZeroExit: false,
		});
	});

	it("validates stimulus configuration via API", () => {
		cy.request({
			url: `/api/v1/ensembles/${PACK_NAME}?namespace=${NS}`,
			headers: apiHeaders(),
		}).then((resp) => {
			const spec = resp.body.spec;
			expect(spec.stimulus.name).to.eq("kickoff");
			expect(spec.stimulus.prompt).to.include("Begin the research workflow");

			// Verify stimulus relationship exists
			const stimulusRel = spec.relationships.find(
				(r: { type: string }) => r.type === "stimulus",
			);
			expect(stimulusRel).to.not.be.undefined;
			expect(stimulusRel.source).to.eq("kickoff");
			expect(stimulusRel.target).to.eq("lead");
		});
	});

	it("can manually trigger stimulus via API", () => {
		cy.request({
			method: "POST",
			url: `/api/v1/ensembles/${PACK_NAME}/stimulus/trigger?namespace=${NS}`,
			headers: apiHeaders(),
			failOnStatusCode: false,
		}).then((resp) => {
			// Pack is not enabled so target agent isn't stamped out — expect 404.
			// A 400 would mean stimulus config is missing (bug), 404 is correct.
			expect(resp.status).to.eq(404);
		});
	});

	it("rejects trigger on ensemble without stimulus", () => {
		// Create a pack without stimulus
		const noStimName = `${PACK_NAME}-no-stim`;
		const manifest = `
apiVersion: sympozium.ai/v1alpha1
kind: Ensemble
metadata:
  name: ${noStimName}
  namespace: ${NS}
spec:
  enabled: false
  description: No stimulus test
  category: test
  version: "1.0"
  agentConfigs:
    - name: worker
      systemPrompt: "You are a worker."
`;
		cy.writeFile(`cypress/tmp/${noStimName}.yaml`, manifest);
		cy.exec(`kubectl apply -f cypress/tmp/${noStimName}.yaml`);

		cy.request({
			method: "POST",
			url: `/api/v1/ensembles/${noStimName}/stimulus/trigger?namespace=${NS}`,
			headers: apiHeaders(),
			failOnStatusCode: false,
		}).then((resp) => {
			expect(resp.status).to.eq(400);
		});

		// Cleanup
		cy.exec(`kubectl delete ensemble ${noStimName} -n ${NS}`, {
			failOnNonZeroExit: false,
		});
		cy.exec(`rm -f cypress/tmp/${noStimName}.yaml`, {
			failOnNonZeroExit: false,
		});
	});

	it("shows the Workflow tab on ensemble detail page", () => {
		cy.visit(`/ensembles/${PACK_NAME}`);
		cy.contains("Workflow").should("be.visible");
	});

	it("shows stimulus relationship in the workflow tab", () => {
		cy.visit(`/ensembles/${PACK_NAME}?tab=workflow`);
		// The workflow tab should show the relationship summary
		cy.contains("Persona Workflow", { timeout: 10000 }).should("be.visible");
		cy.contains("2 agents with 2 relationships").should("be.visible");
	});

	it("shows re-trigger stimulus button on workflow tab", () => {
		cy.visit(`/ensembles/${PACK_NAME}?tab=workflow`);
		cy.contains("Persona Workflow", { timeout: 10000 }).should("be.visible");
		cy.get('[data-testid="stimulus-retrigger-btn"]', {
			timeout: 10000,
		}).should("exist");
	});
});
