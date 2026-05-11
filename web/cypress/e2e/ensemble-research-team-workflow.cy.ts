/**
 * Research Team Ensemble — end-to-end test for the default research-delegation-example
 * pack that demonstrates all three relationship types (delegation, sequential,
 * supervision) on the workflow canvas.
 */

const PACK = "research-delegation-example";
const NS = "default";

function apiHeaders(): Record<string, string> {
	const token = Cypress.env("API_TOKEN");
	const h: Record<string, string> = { "Content-Type": "application/json" };
	if (token) h["Authorization"] = `Bearer ${token}`;
	return h;
}

describe("Research Team — default pack with relationships", () => {
	before(() => {
		// Apply the research-delegation-example pack to the default namespace.
		// Use sed to override the namespace from sympozium-system to default.
		cy.exec(
			`sed 's/namespace: sympozium-system/namespace: default/' ${Cypress.config().projectRoot}/../config/agent-configs/research-delegation-example.yaml | kubectl apply -f -`,
		);
		// Wait for API to serve it
		cy.request({
			url: `/api/v1/ensembles/${PACK}?namespace=${NS}`,
			headers: apiHeaders(),
			retryOnStatusCodeFailure: true,
		}).then((resp) => {
			expect(resp.status).to.eq(200);
		});
	});

	after(() => {
		cy.exec(`kubectl delete ensemble ${PACK} -n ${NS}`, {
			failOnNonZeroExit: false,
		});
		cy.exec(`kubectl delete ensemble ${PACK} -n sympozium-system`, {
			failOnNonZeroExit: false,
		});
	});

	it("has the correct agents and relationships via API", () => {
		cy.request({
			url: `/api/v1/ensembles/${PACK}?namespace=${NS}`,
			headers: apiHeaders(),
		}).then((resp) => {
			const spec = resp.body.spec;

			// 4 agents
			expect(spec.agentConfigs).to.have.length(4);
			const names = spec.agentConfigs.map((p: { name: string }) => p.name);
			expect(names).to.include.members([
				"lead",
				"researcher",
				"writer",
				"reviewer",
			]);

			// 6 relationships (includes stimulus → lead)
			expect(spec.relationships).to.have.length(6);

			// Verify relationship types
			const stimulus = spec.relationships.filter(
				(r: { type: string }) => r.type === "stimulus",
			);
			const delegation = spec.relationships.filter(
				(r: { type: string }) => r.type === "delegation",
			);
			const sequential = spec.relationships.filter(
				(r: { type: string }) => r.type === "sequential",
			);
			const supervision = spec.relationships.filter(
				(r: { type: string }) => r.type === "supervision",
			);
			expect(stimulus).to.have.length(1); // research-brief→lead
			expect(delegation).to.have.length(2); // lead→researcher, researcher→writer
			expect(sequential).to.have.length(1); // writer→reviewer
			expect(supervision).to.have.length(2); // lead→writer, lead→reviewer

			// Workflow type
			expect(spec.workflowType).to.eq("delegation");

			// Category
			expect(spec.category).to.eq("research");
		});
	});

	it("renders all 4 persona nodes on the workflow canvas", () => {
		cy.visit(`/ensembles/${PACK}?tab=workflow`);
		cy.contains("Research Lead", { timeout: 10000 }).should("be.visible");
		cy.contains("Researcher").should("be.visible");
		cy.contains("Writer").should("be.visible");
		cy.contains("Reviewer").should("be.visible");
	});

	it("shows the correct relationship count in the description", () => {
		cy.visit(`/ensembles/${PACK}?tab=workflow`);
		cy.contains("4 agents with 6 relationships", { timeout: 10000 }).should(
			"be.visible",
		);
	});

	it("canvas has ReactFlow controls and minimap", () => {
		cy.visit(`/ensembles/${PACK}?tab=workflow`);
		// ReactFlow controls exist in the DOM (may be off-viewport on small screens)
		cy.get(".react-flow__controls", { timeout: 10000 }).should("exist");
		cy.get(".react-flow__minimap").should("exist");
	});

	it("shows delegation workflow type in the header area", () => {
		cy.visit(`/ensembles/${PACK}`);
		// The header displays workflow type for non-autonomous packs
		cy.contains("delegation", { timeout: 10000 }).should("exist");
	});

	it("shows pack description and category", () => {
		cy.visit(`/ensembles/${PACK}`);
		cy.contains("research coordination team").should("be.visible");
		cy.contains("research").should("be.visible");
	});

	it("overview tab shows all 4 agents", () => {
		cy.visit(`/ensembles/${PACK}`);
		cy.contains("Agents", { timeout: 10000 }).should("be.visible");
		cy.contains("Research Lead").should("be.visible");
		cy.contains("Researcher").should("be.visible");
		cy.contains("Writer").should("be.visible");
		cy.contains("Reviewer").should("be.visible");
	});

	it("can update relationships via PATCH and see changes on canvas", () => {
		// Add a new relationship: reviewer → researcher (feedback loop)
		cy.request({
			url: `/api/v1/ensembles/${PACK}?namespace=${NS}`,
			headers: apiHeaders(),
		}).then((resp) => {
			const existingRels = resp.body.spec.relationships || [];
			const updatedRels = [
				...existingRels,
				{ source: "reviewer", target: "researcher", type: "delegation" },
			];

			cy.request({
				method: "PATCH",
				url: `/api/v1/ensembles/${PACK}?namespace=${NS}`,
				headers: apiHeaders(),
				body: { relationships: updatedRels },
			}).then((patchResp) => {
				expect(patchResp.status).to.eq(200);
				expect(patchResp.body.spec.relationships).to.have.length(7);
			});
		});

		// Verify the canvas shows the updated count
		cy.visit(`/ensembles/${PACK}?tab=workflow`);
		cy.contains("4 agents with 7 relationships", { timeout: 10000 }).should(
			"be.visible",
		);

		// Restore original relationships
		cy.request({
			method: "PATCH",
			url: `/api/v1/ensembles/${PACK}?namespace=${NS}`,
			headers: apiHeaders(),
			body: {
				relationships: [
					{
						source: "research-brief",
						target: "lead",
						type: "stimulus",
					},
					{
						source: "researcher",
						target: "writer",
						type: "delegation",
						condition: "when research is complete",
						timeout: "10m",
						resultFormat: "markdown",
					},
					{
						source: "writer",
						target: "reviewer",
						type: "sequential",
						condition: "when draft is ready",
						timeout: "5m",
					},
					{
						source: "lead",
						target: "researcher",
						type: "delegation",
						condition: "on new research request",
						timeout: "15m",
					},
					{ source: "lead", target: "writer", type: "supervision" },
					{ source: "lead", target: "reviewer", type: "supervision" },
				],
			},
		});
	});
});

export {};
