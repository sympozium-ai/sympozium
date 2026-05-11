/**
 * Demo Walkthrough — ~40 second recorded tour of Sympozium.
 *
 * Run with:  npm run demo:record
 *
 * Flow:
 *   1. Dashboard — activity charts, token usage, event stream
 *   2. Ensembles catalog — browse pre-built agent teams
 *   3. New Ensemble via canvas builder — pick provider, add agents,
 *      configure personas, draw relationships, save
 *   4. Ensemble detail — overview with personas
 *   5. Workflow canvas — relationship graph
 *   6. Topology — full cluster visualization (nodes, providers, models, ensembles)
 *   7. Runs page — execution history
 *   8. Models page — local model deployment
 *   9. Skills catalog — available skill packs
 *  10. Policies — tool gating
 */

const CANVAS_ENSEMBLE = "demo-devops-team";
const NS = "default";

function apiHeaders(): Record<string, string> {
	const token = Cypress.env("API_TOKEN");
	const h: Record<string, string> = { "Content-Type": "application/json" };
	if (token) h["Authorization"] = `Bearer ${token}`;
	return h;
}

/** Pause long enough for viewers to read / appreciate the page. */
function pause(ms = 1200) {
	cy.wait(ms, { log: false });
}

/** Visit a page — alias kept for readability. */
function visitAndWait(path: string) {
	cy.visit(path);
}

describe("Sympozium — Demo Walkthrough", () => {
	before(() => {
		// Seed: install default ensembles so the catalog has content.
		cy.request({
			method: "POST",
			url: `/api/v1/ensembles/install-defaults?namespace=${NS}`,
			headers: apiHeaders(),
			failOnStatusCode: false,
		});
		// Clean up any leftover demo ensemble from a previous run.
		cy.request({
			method: "DELETE",
			url: `/api/v1/ensembles/${CANVAS_ENSEMBLE}?namespace=${NS}`,
			headers: apiHeaders(),
			failOnStatusCode: false,
		});
	});

	after(() => {
		cy.deleteEnsemble(CANVAS_ENSEMBLE);
	});

	it("tours the full Sympozium UI", () => {
		// ─── 1. Dashboard ────────────────────────────────────────────
		visitAndWait("/dashboard");
		cy.contains("Dashboard", { timeout: 15000 }).should("be.visible");
		pause(1200);

		// ─── 2. Ensembles catalog ────────────────────────────────────
		visitAndWait("/ensembles");
		cy.contains("Ensembles", { timeout: 10000 }).should("be.visible");
		pause(1500);

		// ─── 3. Create new ensemble via canvas builder ───────────────

		// Click "New Ensemble" button.
		cy.contains("New Ensemble", { timeout: 5000 }).click();
		cy.url().should("include", "/ensembles/new");
		cy.contains("Choose AI Provider", { timeout: 10000 }).should("be.visible");
		pause(1000);

		// 3a. Provider setup — select LM Studio.
		cy.contains("button", "LM Studio").click({ force: true });
		pause(600);

		// "Continue to Builder" should now be enabled (LM Studio has a default base URL).
		cy.contains("button", "Continue to Builder", { timeout: 5000 })
			.should("not.be.disabled")
			.click();
		pause(800);

		// 3b. Canvas is now visible — name the ensemble.
		cy.get("input[placeholder='ensemble-name (required)']", { timeout: 5000 })
			.should("be.visible")
			.clear()
			.type(CANVAS_ENSEMBLE, { delay: 30 });
		pause(500);

		// Helper: configure a persona in the side panel (opened by Add Agent).
		function configurePersona(name: string, display: string, prompt: string) {
			cy.get("#persona-name", { timeout: 5000 }).should("exist");
			cy.get("#persona-name")
				.clear({ force: true })
				.type(name, { delay: 30, force: true });
			cy.get("#persona-display")
				.clear({ force: true })
				.type(display, { delay: 30, force: true });
			cy.get("#persona-prompt")
				.clear({ force: true })
				.type(prompt, { delay: 10, force: true });
			pause(400);
			// Click Save in the side panel — it's the only standalone "Save" button
			// besides "Save Ensemble" in the toolbar.
			cy.get("#persona-name")
				.parents("div")
				.last()
				.find("button")
				.not(":contains('Save Ensemble')")
				.contains("Save")
				.click({ force: true });
			pause(300);
		}

		// 3c. Add first agent and configure it.
		cy.contains("button", "Add Agent").click({ force: true });
		configurePersona(
			"analyst",
			"Analyst",
			"You are a metrics analyst. Monitor cluster health and triage alerts.",
		);

		// 3d. Add second agent.
		cy.contains("button", "Add Agent").click({ force: true });
		configurePersona(
			"auditor",
			"Auditor",
			"You are a security auditor. Review changes and scan for vulnerabilities.",
		);

		// 3e. Add third agent.
		cy.contains("button", "Add Agent").click({ force: true });
		configurePersona(
			"remediator",
			"Remediator",
			"You are a remediation agent. Apply fixes approved by the auditor.",
		);

		// Close the side panel by clicking the canvas pane (deselects persona).
		// The panel only closes when selectedPersona becomes null, which happens
		// on node click toggle or pane click — let's just click a node that's
		// already selected to deselect it, or click outside.
		// Simplest: click the pane area to the left of any nodes.
		cy.get(".react-flow__pane").click(100, 100, { force: true });
		pause(300);
		// If panel is still open, force-close by clicking the toolbar Settings
		// button (which sets selectedPersona to null).
		cy.get("body").then(($body) => {
			if ($body.find("#persona-name").length > 0) {
				cy.contains("button", "Settings").click({ force: true });
				pause(200);
				cy.contains("button", "Settings").click({ force: true });
				pause(200);
			}
		});
		pause(400);

		// 3f. Draw relationships between personas on the canvas.
		//     ReactFlow's drag-to-connect doesn't work with synthetic events,
		//     so we trigger the connection modal programmatically, then interact
		//     with the real modal UI (visible in the recording).
		function drawRelationship(source: string, target: string, type: string) {
			// eslint-disable-next-line @typescript-eslint/no-explicit-any
			cy.window().then((win: any) => {
				win.__testSetPendingConnection({ source, target });
			});
			// The "Relationship Type" modal is real UI — viewer sees it.
			cy.contains("Relationship Type", { timeout: 5000 }).should("be.visible");
			pause(800);
			cy.contains("button", type).click({ force: true });
			pause(500);
		}

		// analyst → auditor: delegation
		drawRelationship("analyst", "auditor", "delegation");

		// auditor → remediator: sequential
		drawRelationship("auditor", "remediator", "sequential");

		pause(800);

		// 3g. Toggle shared memory on (if not already on).
		cy.get("body").then(($body) => {
			if ($body.text().includes("Shared Memory OFF")) {
				cy.contains("Shared Memory OFF").click({ force: true });
			}
		});
		pause(500);

		// 3h. Save the ensemble.
		cy.contains("button", "Save Ensemble").should("not.be.disabled");
		cy.contains("button", "Save Ensemble").click({ force: true });

		// Should navigate to the new ensemble's detail page.
		cy.url({ timeout: 15000 }).should(
			"include",
			`/ensembles/${CANVAS_ENSEMBLE}`,
		);
		pause(800);

		// ─── 4. Ensemble detail — overview ───────────────────────────
		cy.contains("Analyst", { timeout: 10000 }).should("exist");
		cy.contains("Auditor").should("exist");
		cy.contains("Remediator").should("exist");
		pause(1500);

		// ─── 5. Workflow canvas ──────────────────────────────────────
		cy.contains("Workflow").click();
		cy.url().should("include", "tab=workflow");
		cy.contains("Persona Workflow", { timeout: 10000 }).should("be.visible");
		// Wait for all 3 nodes and the relationship edges to render.
		cy.get(".react-flow__node", { timeout: 10000 }).should(
			"have.length.gte",
			3,
		);
		cy.get(".react-flow__edge", { timeout: 10000 }).should(
			"have.length.gte",
			2,
		);
		pause(2500);
		// Scroll down to show the relationships summary.
		cy.contains("3 agents with").scrollIntoView({ duration: 400 });
		pause(1500);

		// ─── 6. Topology — full cluster visualization ─────────────────
		visitAndWait("/topology");
		cy.contains("Topology", { timeout: 10000 }).should("be.visible");
		// Wait for the canvas to render nodes.
		cy.get(".react-flow__node", { timeout: 10000 }).should(
			"have.length.gte",
			1,
		);
		pause(3000);

		// ─── 7. Runs page ────────────────────────────────────────────
		visitAndWait("/runs");
		cy.contains("Runs", { timeout: 10000 }).should("be.visible");
		pause(1200);

		// ─── 8. Models page ──────────────────────────────────────────
		visitAndWait("/models");
		cy.contains("Models", { timeout: 10000 }).should("be.visible");
		pause(1200);

		// ─── 9. Skills catalog ───────────────────────────────────────
		visitAndWait("/skills");
		cy.contains("Skills", { timeout: 10000 }).should("be.visible");
		pause(1200);

		// ─── 10. Policies ────────────────────────────────────────────
		visitAndWait("/policies");
		cy.contains("Policies", { timeout: 10000 }).should("be.visible");
		pause(1200);

		// ─── End ─────────────────────────────────────────────────────
		visitAndWait("/dashboard");
		cy.contains("Dashboard", { timeout: 10000 }).should("be.visible");
		pause(1500);
	});
});

export {};
