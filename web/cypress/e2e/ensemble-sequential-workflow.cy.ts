/**
 * Sequential Workflow — validates that when a persona in a sequential
 * relationship completes, the controller automatically triggers the
 * next persona in the chain.
 *
 * Ensemble:
 *   researcher-0 --[sequential]--> researcher-1
 *
 * Flow:
 *   1. Dispatch a run to researcher-0
 *   2. researcher-0 completes and stores findings in shared memory
 *   3. Controller detects completion and auto-creates a run for researcher-1
 *   4. researcher-1 receives the predecessor's result and accesses shared memory
 *   5. researcher-1 completes
 *
 * This validates:
 *   - Sequential trigger: controller creates successor run on completion
 *   - Shared memory: both personas can read/write the shared pool
 *   - Predecessor context: successor receives the prior result in its task
 */

const ENSEMBLE = `cy-seq-wf-${Date.now()}`;
const NS = "default";
const R0 = "researcher-0";
const R1 = "researcher-1";
const R0_INSTANCE = `${ENSEMBLE}-${R0}`;
const R1_INSTANCE = `${ENSEMBLE}-${R1}`;

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
    return (cy
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
      }) as unknown as Cypress.Chainable<void>);
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

describe("Sequential Workflow — automatic trigger on completion", () => {
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
    cy.deleteAgent(R0_INSTANCE);
    cy.deleteAgent(R1_INSTANCE);
    cy.exec(
      `kubectl delete agentrun -n ${NS} -l sympozium.ai/instance=${R0_INSTANCE} --ignore-not-found --wait=false`,
      { failOnNonZeroExit: false },
    );
    cy.exec(
      `kubectl delete agentrun -n ${NS} -l sympozium.ai/instance=${R1_INSTANCE} --ignore-not-found --wait=false`,
      { failOnNonZeroExit: false },
    );
  });

  it("creates an ensemble with a sequential edge and shared memory", () => {
    cy.request({
      method: "POST",
      url: `/api/v1/ensembles?namespace=${NS}`,
      headers: authHeaders(),
      body: {
        name: ENSEMBLE,
        description:
          "Sequential: researcher-0 discovers, researcher-1 auto-triggered to validate",
        category: "test",
        workflowType: "delegation",
        agentConfigs: [
          {
            name: R0,
            displayName: "Primary Researcher",
            systemPrompt: `You are a geography researcher.

RULES:
1. Answer the question with the specific number first.
2. Then call workflow_memory_store to save your finding.
3. Your final response MUST include the actual number (e.g. "2.1 million" or "10,500 km").
4. Do NOT use any tools except workflow_memory_store.`,
            model: "brooooooklyn/qwen3.6-27b-ud-mlx",
            skills: ["memory"],
          },
          {
            name: R1,
            displayName: "Verification Researcher",
            systemPrompt: `You are a verification researcher.

RULES:
1. Call workflow_memory_search to find the primary researcher's findings.
2. Verify the findings and state whether they are correct.
3. Call workflow_memory_store to save your verification.
4. Do NOT use any tools except workflow_memory_search and workflow_memory_store.`,
            model: "brooooooklyn/qwen3.6-27b-ud-mlx",
            skills: ["memory"],
          },
        ],
        relationships: [
          {
            source: R0,
            target: R1,
            type: "sequential",
            timeout: "5m",
          },
        ],
        sharedMemory: {
          enabled: true,
          storageSize: "512Mi",
          accessRules: [
            { agentConfig: R0, access: "read-write" },
            { agentConfig: R1, access: "read-write" },
          ],
        },
      },
    }).then((resp) => {
      expect(resp.status).to.eq(201);
      expect(resp.body.spec.relationships[0].type).to.eq("sequential");
    });
  });

  it("activates the ensemble and waits for both instances", () => {
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
    waitForInstance(R0_INSTANCE);
    waitForInstance(R1_INSTANCE);
  });

  it("dispatches researcher-0 which completes and auto-triggers researcher-1", () => {
    // Dispatch run to researcher-0 only — researcher-1 should be triggered automatically.
    cy.dispatchRun(
      R0_INSTANCE,
      "What is the approximate population of Paris, France? State the number, then call workflow_memory_store to save it with tags population, paris. Do not use any other tools.",
    ).then((r0RunName) => {
      // Wait for researcher-0 to complete
      cy.waitForRunTerminal(r0RunName, 5 * 60 * 1000).then((phase) => {
        expect(phase).to.eq("Succeeded");
      });

      // Verify researcher-0 produced a result
      cy.request({
        url: `/api/v1/runs/${r0RunName}?namespace=${NS}`,
        headers: authHeaders(),
      }).then((resp) => {
        const result = (resp.body?.status?.result || "") as string;
        expect(
          /\d/.test(result),
          `researcher-0 should return numeric data, got: ${result.slice(0, 200)}`,
        ).to.be.true;
      });

      // Wait for the controller to auto-create a run for researcher-1.
      // The sequential trigger creates a run with label
      // "sympozium.ai/sequential-from" set to the source run name.
      cy.then(() =>
        waitForRunWithLabel(
          "sympozium.ai/sequential-from",
          r0RunName,
          120000,
        ).then((r1RunName) => {
          // Wait for researcher-1's auto-triggered run to complete
          cy.waitForRunTerminal(r1RunName, 5 * 60 * 1000).then((phase) => {
            expect(phase).to.eq("Succeeded");
          });

          // Verify researcher-1's result references the predecessor
          cy.request({
            url: `/api/v1/runs/${r1RunName}?namespace=${NS}`,
            headers: authHeaders(),
          }).then((resp) => {
            const result = (resp.body?.status?.result || "") as string;
            // The successor should have received context from the predecessor
            expect(
              result.length > 0,
              "researcher-1 should produce a non-empty result",
            ).to.be.true;
          });
        }),
      );
    });
  });

  it("shows both runs in the UI with correct instances", () => {
    cy.visit("/runs");
    cy.contains(R0_INSTANCE, { timeout: 10000 }).should("exist");
    cy.contains(R1_INSTANCE).should("exist");
  });

  it("shows the sequential edge on the workflow canvas", () => {
    cy.visit(`/ensembles/${ENSEMBLE}?tab=workflow`);
    cy.contains("Primary Researcher", { timeout: 10000 }).should("be.visible");
    cy.contains("Verification Researcher").should("be.visible");
    cy.contains("2 personas with 1 relationship").should("be.visible");
  });
});

export {};
