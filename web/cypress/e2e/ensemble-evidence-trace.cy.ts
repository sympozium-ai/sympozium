/**
 * Ensemble Evidence Trace — tests for evidence policy configuration,
 * the Memory tab UI, and membrane evidence policy display.
 *
 * Tests cover:
 * - API: PATCH evidence policy on/off, update minKind, persistence via GET
 * - UI: Memory tab visibility, empty state, summary cards, filter dropdowns
 * - UI: Evidence policy display in membrane section on workflow tab
 * - UI: Memory tab disabled state when shared memory is not enabled
 */

const NS = "default";

function apiHeaders(): Record<string, string> {
	const token = Cypress.env("API_TOKEN");
	const h: Record<string, string> = { "Content-Type": "application/json" };
	if (token) h["Authorization"] = `Bearer ${token}`;
	return h;
}

// ════════════════════════════════════════════════════════════════════════════
// Suite 1: Evidence Policy API
// ════════════════════════════════════════════════════════════════════════════

describe("Evidence Trace — API", () => {
	const PACK = `cypress-evidence-api-${Date.now()}`;

	before(() => {
		const manifest = `
apiVersion: sympozium.ai/v1alpha1
kind: Ensemble
metadata:
  name: ${PACK}
  namespace: ${NS}
spec:
  enabled: false
  description: Evidence trace API test
  category: test
  agentConfigs:
    - name: researcher
      systemPrompt: "You research."
      skills: [memory]
    - name: analyst
      systemPrompt: "You analyze."
      skills: [memory]
  sharedMemory:
    enabled: true
    storageSize: "512Mi"
    accessRules:
      - agentConfig: researcher
        access: read-write
      - agentConfig: analyst
        access: read-write
`;
		cy.writeFile(`cypress/tmp/${PACK}.yaml`, manifest);
		cy.exec(`kubectl apply -f cypress/tmp/${PACK}.yaml`);
	});

	after(() => {
		cy.deleteEnsemble(PACK);
		cy.exec(`rm -f cypress/tmp/${PACK}.yaml`, { failOnNonZeroExit: false });
	});

	it("can set evidence policy via membrane PATCH", () => {
		cy.request({
			method: "PATCH",
			url: `/api/v1/ensembles/${PACK}?namespace=${NS}`,
			headers: apiHeaders(),
			body: {
				sharedMemory: {
					enabled: true,
					storageSize: "512Mi",
					accessRules: [
						{ agentConfig: "researcher", access: "read-write" },
						{ agentConfig: "analyst", access: "read-write" },
					],
					membrane: {
						defaultVisibility: "public",
						evidencePolicy: {
							minKind: "external_source",
						},
					},
				},
			},
		}).then((resp) => {
			expect(resp.status).to.eq(200);
			expect(resp.body.spec.sharedMemory.membrane.evidencePolicy).to.deep.include({
				minKind: "external_source",
			});
		});
	});

	it("evidence policy persists on GET", () => {
		cy.request({
			url: `/api/v1/ensembles/${PACK}?namespace=${NS}`,
			headers: apiHeaders(),
		}).then((resp) => {
			expect(resp.status).to.eq(200);
			expect(resp.body.spec.sharedMemory.membrane.evidencePolicy.minKind).to.eq("external_source");
		});
	});

	it("can update evidence policy minKind", () => {
		cy.request({
			method: "PATCH",
			url: `/api/v1/ensembles/${PACK}?namespace=${NS}`,
			headers: apiHeaders(),
			body: {
				sharedMemory: {
					enabled: true,
					storageSize: "512Mi",
					accessRules: [
						{ agentConfig: "researcher", access: "read-write" },
						{ agentConfig: "analyst", access: "read-write" },
					],
					membrane: {
						defaultVisibility: "public",
						evidencePolicy: {
							minKind: "tool_result",
						},
					},
				},
			},
		}).then((resp) => {
			expect(resp.status).to.eq(200);
			expect(resp.body.spec.sharedMemory.membrane.evidencePolicy.minKind).to.eq("tool_result");
		});
	});

	it("can remove evidence policy by omitting it", () => {
		cy.request({
			method: "PATCH",
			url: `/api/v1/ensembles/${PACK}?namespace=${NS}`,
			headers: apiHeaders(),
			body: {
				sharedMemory: {
					enabled: true,
					storageSize: "512Mi",
					membrane: {
						defaultVisibility: "public",
					},
				},
			},
		}).then((resp) => {
			expect(resp.status).to.eq(200);
			// evidencePolicy should be nil/undefined
			const ep = resp.body.spec.sharedMemory.membrane?.evidencePolicy;
			expect(ep == null || ep.minKind === "").to.eq(true);
		});
	});
});

// ════════════════════════════════════════════════════════════════════════════
// Suite 2: Memory Tab UI
// ════════════════════════════════════════════════════════════════════════════

describe("Evidence Trace — Memory Tab UI", () => {
	const PACK = `cypress-evidence-ui-${Date.now()}`;

	before(() => {
		const manifest = `
apiVersion: sympozium.ai/v1alpha1
kind: Ensemble
metadata:
  name: ${PACK}
  namespace: ${NS}
spec:
  enabled: false
  description: Evidence trace UI test
  category: test
  agentConfigs:
    - name: agent-a
      displayName: Agent A
      systemPrompt: "You are agent A."
      skills: [memory]
    - name: agent-b
      displayName: Agent B
      systemPrompt: "You are agent B."
      skills: [memory]
  sharedMemory:
    enabled: true
    storageSize: "512Mi"
    membrane:
      defaultVisibility: public
      evidencePolicy:
        minKind: external_source
`;
		cy.writeFile(`cypress/tmp/${PACK}.yaml`, manifest);
		cy.exec(`kubectl apply -f cypress/tmp/${PACK}.yaml`);
	});

	after(() => {
		cy.deleteEnsemble(PACK);
		cy.exec(`rm -f cypress/tmp/${PACK}.yaml`, { failOnNonZeroExit: false });
	});

	it("shows Memory tab in ensemble detail", () => {
		cy.visit(`/ensembles/${PACK}`);
		cy.contains("Memory", { timeout: 10000 }).should("be.visible");
	});

	it("Memory tab shows empty state", () => {
		cy.visit(`/ensembles/${PACK}?tab=memory`);
		cy.contains("Shared Memory Entries", { timeout: 10000 }).should("be.visible");
		cy.contains("No shared memory entries yet").should("be.visible");
	});

	it("shows summary cards with zero counts", () => {
		cy.visit(`/ensembles/${PACK}?tab=memory`);
		cy.contains("Total Entries", { timeout: 10000 }).should("be.visible");
		cy.contains("Tool-Backed").should("be.visible");
		cy.contains("Contributors").should("be.visible");
		cy.contains("Evidence Policy").should("be.visible");
	});

	it("shows evidence kind filter dropdown", () => {
		cy.visit(`/ensembles/${PACK}?tab=memory`);
		cy.get("select", { timeout: 10000 }).should("have.length.at.least", 1);
		cy.contains("All evidence kinds").should("exist");
	});

	it("shows evidence policy in the membrane section on workflow tab", () => {
		cy.visit(`/ensembles/${PACK}?tab=workflow`);
		cy.contains("Synthetic Membrane", { timeout: 10000 }).scrollIntoView().should("be.visible");
	});
});

// ════════════════════════════════════════════════════════════════════════════
// Suite 3: Memory Tab disabled when no shared memory
// ════════════════════════════════════════════════════════════════════════════

describe("Evidence Trace — Memory Tab disabled", () => {
	const PACK = `cypress-evidence-nomem-${Date.now()}`;

	before(() => {
		const manifest = `
apiVersion: sympozium.ai/v1alpha1
kind: Ensemble
metadata:
  name: ${PACK}
  namespace: ${NS}
spec:
  enabled: false
  description: No shared memory
  category: test
  agentConfigs:
    - name: solo
      systemPrompt: "You work alone."
`;
		cy.writeFile(`cypress/tmp/${PACK}.yaml`, manifest);
		cy.exec(`kubectl apply -f cypress/tmp/${PACK}.yaml`);
	});

	after(() => {
		cy.deleteEnsemble(PACK);
		cy.exec(`rm -f cypress/tmp/${PACK}.yaml`, { failOnNonZeroExit: false });
	});

	it("Memory tab trigger is disabled when shared memory is off", () => {
		cy.visit(`/ensembles/${PACK}`);
		// The Memory tab trigger should exist but be disabled
		cy.contains("Memory", { timeout: 10000 }).should("be.visible");
		cy.contains("button", "Memory").should("be.disabled");
	});
});

export {};
