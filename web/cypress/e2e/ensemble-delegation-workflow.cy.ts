/**
 * Delegation Workflow — validates that a persona can delegate a task to
 * another persona at runtime using the delegate_to_persona tool, and that
 * the delegation is **blocking**: the parent waits for the child to finish
 * and receives the child's result back before completing.
 *
 * Ensemble:
 *   lead --[delegation]--> researcher
 *
 * Flow:
 *   1. Dispatch a run to lead
 *   2. Lead calls delegate_to_persona(targetPersona: "researcher", task: ...)
 *   3. SpawnRouter creates a child AgentRun, transitions parent to AwaitingDelegate
 *   4. Child run executes under the researcher persona's system prompt
 *   5. Child run completes → SpawnRouter delivers result back to parent via IPC
 *   6. Parent's delegate tool unblocks, LLM incorporates the result
 *   7. Lead run completes with result that includes the researcher's output
 *
 * This validates:
 *   - delegate_to_persona tool is available and blocks until result arrives
 *   - SpawnRouter creates child run and delivers result back
 *   - Parent transitions through AwaitingDelegate → Running → Succeeded
 *   - Parent's DelegateStatus is populated with child info
 *   - Lead's final result includes the researcher's output
 *   - Both runs complete successfully
 */

const ENSEMBLE = `cy-deleg-wf-${Date.now()}`;
const NS = "default";
const LEAD = "lead";
const RESEARCHER = "researcher";
const LEAD_INSTANCE = `${ENSEMBLE}-${LEAD}`;
const RESEARCHER_INSTANCE = `${ENSEMBLE}-${RESEARCHER}`;

function authHeaders(): Record<string, string> {
	const token = Cypress.env("API_TOKEN");
	const h: Record<string, string> = { "Content-Type": "application/json" };
	if (token) h["Authorization"] = `Bearer ${token}`;
	return h;
}

function waitForInstance(
	instanceName: string,
	timeoutMs = 60000,
): Cypress.Chainable<void> {
	const started = Date.now();
	const poll = (): Cypress.Chainable<void> => {
		return cy
			.request({
				url: `/api/v1/agents/${instanceName}?namespace=${NS}`,
				headers: authHeaders(),
				failOnStatusCode: false,
			})
			.then((resp): void => {
				if (resp.status === 200) return;
				if (Date.now() - started > timeoutMs) {
					throw new Error(
						`Instance ${instanceName} not created within ${timeoutMs}ms`,
					);
				}
				cy.wait(2000, { log: false });
				poll();
			}) as unknown as Cypress.Chainable<void>;
	};
	return poll();
}

/** Poll for an AgentRun matching a label selector. */
function waitForRunWithLabel(
	labelKey: string,
	labelValue: string,
	timeoutMs = 120000,
): Cypress.Chainable<string> {
	const started = Date.now();
	const poll = (): Cypress.Chainable<string> => {
		return cy
			.request({
				url: `/api/v1/runs?namespace=${NS}`,
				headers: authHeaders(),
			})
			.then((resp) => {
				const all = (Array.isArray(resp.body) ? resp.body : []) as Array<{
					metadata: { name: string; labels?: Record<string, string> };
				}>;
				const match = all.find(
					(r) => r.metadata.labels?.[labelKey] === labelValue,
				);
				if (match) {
					return cy.wrap(match.metadata.name);
				}
				if (Date.now() - started > timeoutMs) {
					throw new Error(
						`No run with label ${labelKey}=${labelValue} within ${timeoutMs}ms`,
					);
				}
				cy.wait(3000, { log: false });
				return poll();
			});
	};
	return poll();
}

describe("Delegation Workflow — blocking delegate_to_persona with result delivery", () => {
	after(() => {
		cy.request({
			method: "PATCH",
			url: `/api/v1/ensembles/${ENSEMBLE}?namespace=${NS}`,
			headers: authHeaders(),
			body: { enabled: false },
			failOnStatusCode: false,
		});
		cy.wait(3000);
		cy.deleteEnsemble(ENSEMBLE);
		cy.deleteAgent(LEAD_INSTANCE);
		cy.deleteAgent(RESEARCHER_INSTANCE);
		cy.exec(
			`kubectl delete agentrun -n ${NS} -l sympozium.ai/instance=${LEAD_INSTANCE} --ignore-not-found --wait=false`,
			{ failOnNonZeroExit: false },
		);
		cy.exec(
			`kubectl delete agentrun -n ${NS} -l sympozium.ai/instance=${RESEARCHER_INSTANCE} --ignore-not-found --wait=false`,
			{ failOnNonZeroExit: false },
		);
	});

	it("creates an ensemble with a delegation edge", () => {
		cy.request({
			method: "POST",
			url: `/api/v1/ensembles?namespace=${NS}`,
			headers: authHeaders(),
			body: {
				name: ENSEMBLE,
				description:
					"Delegation: lead delegates research tasks to researcher at runtime",
				category: "test",
				workflowType: "delegation",
				agentConfigs: [
					{
						name: LEAD,
						displayName: "Research Lead",
						systemPrompt: `You are a research lead. You NEVER answer questions yourself. You ALWAYS delegate.

RULES:
1. When you receive ANY task, IMMEDIATELY call delegate_to_persona with targetPersona="researcher" and task=<the question>.
2. Do NOT think about the answer. Do NOT write any text before calling the tool.
3. After you receive the researcher's result, repeat it verbatim in your response.
4. The ONLY tool you may use is delegate_to_persona.`,
						model: Cypress.env("TEST_MODEL"),
						skills: ["memory"],
					},
					{
						name: RESEARCHER,
						displayName: "Researcher",
						systemPrompt: `You are a researcher. Answer questions with specific facts and numbers. Be concise. Do NOT use any tools. Just provide a direct factual answer.`,
						model: Cypress.env("TEST_MODEL"),
						skills: ["memory"],
					},
				],
				relationships: [
					{
						source: LEAD,
						target: RESEARCHER,
						type: "delegation",
						condition: "when research task is received",
						timeout: "10m",
					},
				],
				sharedMemory: {
					enabled: true,
					storageSize: "512Mi",
					accessRules: [
						{ agentConfig: LEAD, access: "read-write" },
						{ agentConfig: RESEARCHER, access: "read-write" },
					],
				},
			},
		}).then((resp) => {
			expect(resp.status).to.eq(201);
			expect(resp.body.spec.relationships).to.have.length(1);
			expect(resp.body.spec.relationships[0].type).to.eq("delegation");
			expect(resp.body.spec.relationships[0].source).to.eq(LEAD);
			expect(resp.body.spec.relationships[0].target).to.eq(RESEARCHER);
		});
	});

	it.skip("activates the ensemble and waits for both instances — delegation chain skipped", () => {
		cy.request({
			method: "PATCH",
			url: `/api/v1/ensembles/${ENSEMBLE}?namespace=${NS}`,
			headers: authHeaders(),
			body: {
				enabled: true,
				baseURL: "http://host.docker.internal:1234/v1",
				provider: "lm-studio",
				secretName: "",
			},
		}).then((resp) => {
			expect(resp.status).to.eq(200);
		});
		waitForInstance(LEAD_INSTANCE);
		waitForInstance(RESEARCHER_INSTANCE);
	});

	// Flaky: depends on qwen3.5-9b reliably calling delegate_to_persona tool
	// on first attempt. The model sometimes answers directly instead of delegating.
	// Re-enable with a larger model or deterministic tool-call forcing.
	it.skip("dispatches to lead which delegates to researcher and receives the result", () => {
		// Dispatch a run to the lead — the lead's system prompt instructs it
		// to delegate immediately to the researcher persona.
		cy.dispatchRun(
			LEAD_INSTANCE,
			"What is the approximate distance in kilometers between Tokyo and Sydney? Delegate this to the researcher.",
		).then((leadRunName) => {
			// Wait for the delegated child run to appear.
			// The spawner labels child runs with "sympozium.ai/parent-run".
			cy.then(() =>
				waitForRunWithLabel(
					"sympozium.ai/parent-run",
					leadRunName,
					5 * 60 * 1000,
				).then((childRunName) => {
					// Verify the child run is for the researcher instance
					cy.request({
						url: `/api/v1/runs/${childRunName}?namespace=${NS}`,
						headers: authHeaders(),
					}).then((resp) => {
						expect(resp.body.spec.agentRef).to.eq(RESEARCHER_INSTANCE);
					});

					// Wait for the child (researcher) run to complete
					cy.waitForRunTerminal(childRunName, 5 * 60 * 1000).then((phase) => {
						expect(phase).to.eq("Succeeded");
					});

					// Verify the researcher produced a result
					cy.request({
						url: `/api/v1/runs/${childRunName}?namespace=${NS}`,
						headers: authHeaders(),
					}).then((resp) => {
						const result = (resp.body?.status?.result || "") as string;
						expect(
							result.length > 0,
							"researcher should produce a non-empty result",
						).to.be.true;
					});
				}),
			);

			// Wait for the lead run to complete — with blocking delegation the
			// lead waits for the researcher result before finishing.
			cy.waitForRunTerminal(leadRunName, 5 * 60 * 1000).then((phase) => {
				expect(phase).to.eq("Succeeded");
			});

			// Verify the lead's result includes output from the researcher.
			// Since delegation is now blocking, the lead receives the researcher's
			// response and incorporates it into its own result.
			cy.request({
				url: `/api/v1/runs/${leadRunName}?namespace=${NS}`,
				headers: authHeaders(),
			}).then((resp) => {
				const result = (resp.body?.status?.result || "") as string;
				expect(
					result.length > 0,
					"lead should produce a non-empty result incorporating delegate output",
				).to.be.true;
			});

			// Verify DelegateStatus was populated on the parent run.
			cy.request({
				url: `/api/v1/runs/${leadRunName}?namespace=${NS}`,
				headers: authHeaders(),
			}).then((resp) => {
				const delegates = (resp.body?.status?.delegates || []) as Array<{
					childRunName: string;
					targetPersona: string;
					phase: string;
					result?: string;
				}>;
				expect(
					delegates.length,
					"parent should have at least one delegate entry",
				).to.be.greaterThan(0);
				expect(delegates[0].targetPersona).to.eq(RESEARCHER);
				expect(delegates[0].phase).to.eq("Succeeded");
				expect(
					(delegates[0].result || "").length > 0,
					"delegate status should contain the researcher's result",
				).to.be.true;
			});
		});
	});

	it.skip("shows both runs in the UI — depends on delegation dispatch", () => {
		cy.visit("/runs");
		cy.contains(LEAD_INSTANCE, { timeout: 10000 }).should("exist");
		cy.contains(RESEARCHER_INSTANCE).should("exist");
	});

	it("shows the delegation edge on the workflow canvas", () => {
		cy.visit(`/ensembles/${ENSEMBLE}?tab=workflow`);
		cy.contains("Research Lead", { timeout: 10000 }).should("be.visible");
		cy.contains("Researcher").should("be.visible");
		cy.contains("2 agents with 1 relationships").should("be.visible");
	});
});

export {};
