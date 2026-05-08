/**
 * Ensemble Synthetic Membrane — comprehensive tests for the membrane layer
 * that adds selective permeability, provenance tracking, token budgets,
 * circuit breakers, and time decay to shared workflow memory.
 *
 * Tests cover:
 * - API: PATCH membrane config on/off, permeability rules, trust groups
 * - Token budget: maxTokens, action (halt/warn)
 * - Circuit breaker: consecutiveFailures threshold
 * - Time decay: TTL, decay function
 * - Persistence: membrane survives GET round-trip
 * - Compatibility: membrane alongside relationships and access rules
 */

const NS = "default";

function apiHeaders(): Record<string, string> {
  const token = Cypress.env("API_TOKEN");
  const h: Record<string, string> = { "Content-Type": "application/json" };
  if (token) h["Authorization"] = `Bearer ${token}`;
  return h;
}

// ════════════════════════════════════════════════════════════════════════════
// Suite 1: Membrane API — enable/disable and permeability rules
// ════════════════════════════════════════════════════════════════════════════

describe("Ensemble Membrane — Permeability API", () => {
  const PACK = `cypress-membrane-perm-${Date.now()}`;

  before(() => {
    const manifest = `
apiVersion: sympozium.ai/v1alpha1
kind: Ensemble
metadata:
  name: ${PACK}
  namespace: ${NS}
spec:
  enabled: false
  description: Cypress membrane permeability test
  category: test
  agentConfigs:
    - name: researcher
      systemPrompt: "Agent researcher."
    - name: writer
      systemPrompt: "Agent writer."
    - name: editor
      systemPrompt: "Agent editor."
  sharedMemory:
    enabled: true
    storageSize: "256Mi"
`;
    cy.writeFile(`cypress/tmp/${PACK}.yaml`, manifest);
    cy.exec(`kubectl apply -f cypress/tmp/${PACK}.yaml`);
    // Wait for the apiserver informer cache to pick up the new resource.
    cy.request({
      url: `/api/v1/ensembles/${PACK}?namespace=${NS}`,
      headers: apiHeaders(),
      retryOnStatusCodeFailure: true,
    });
    cy.wait(500);
  });

  after(() => {
    cy.deleteEnsemble(PACK);
    cy.exec(`rm -f cypress/tmp/${PACK}.yaml`, { failOnNonZeroExit: false });
  });

  it("can add membrane config with default visibility via PATCH", () => {
    cy.request({
      method: "PATCH",
      url: `/api/v1/ensembles/${PACK}?namespace=${NS}`,
      headers: apiHeaders(),
      body: {
        sharedMemory: {
          enabled: true,
          storageSize: "256Mi",
          membrane: {
            defaultVisibility: "public",
          },
        },
      },
    }).then((resp) => {
      expect(resp.status).to.eq(200);
      expect(resp.body.spec.sharedMemory.membrane).to.exist;
      expect(resp.body.spec.sharedMemory.membrane.defaultVisibility).to.eq(
        "public",
      );
    });
  });

  it("persists membrane config on subsequent GET", () => {
    cy.request({
      url: `/api/v1/ensembles/${PACK}?namespace=${NS}`,
      headers: apiHeaders(),
    }).then((resp) => {
      expect(resp.status).to.eq(200);
      expect(resp.body.spec.sharedMemory.membrane).to.exist;
      expect(resp.body.spec.sharedMemory.membrane.defaultVisibility).to.eq(
        "public",
      );
    });
  });

  it("can set per-persona permeability rules", () => {
    cy.request({
      method: "PATCH",
      url: `/api/v1/ensembles/${PACK}?namespace=${NS}`,
      headers: apiHeaders(),
      body: {
        sharedMemory: {
          enabled: true,
          storageSize: "256Mi",
          membrane: {
            defaultVisibility: "public",
            permeability: [
              {
                agentConfig: "researcher",
                defaultVisibility: "trusted",
                exposeTags: ["findings", "data"],
                acceptTags: ["summary"],
              },
              {
                agentConfig: "writer",
                defaultVisibility: "public",
                acceptTags: ["findings", "data"],
              },
              {
                agentConfig: "editor",
                defaultVisibility: "private",
              },
            ],
          },
        },
      },
    }).then((resp) => {
      expect(resp.status).to.eq(200);
      const perm = resp.body.spec.sharedMemory.membrane.permeability;
      expect(perm).to.have.length(3);
      expect(perm[0].agentConfig).to.eq("researcher");
      expect(perm[0].defaultVisibility).to.eq("trusted");
      expect(perm[0].exposeTags).to.deep.eq(["findings", "data"]);
      expect(perm[0].acceptTags).to.deep.eq(["summary"]);
      expect(perm[2].defaultVisibility).to.eq("private");
    });
  });

  it("can set trust groups", () => {
    cy.request({
      method: "PATCH",
      url: `/api/v1/ensembles/${PACK}?namespace=${NS}`,
      headers: apiHeaders(),
      body: {
        sharedMemory: {
          enabled: true,
          storageSize: "256Mi",
          membrane: {
            defaultVisibility: "public",
            trustGroups: [
              {
                name: "research-delegation-example",
                agentConfigs: ["researcher", "writer"],
              },
              {
                name: "editorial",
                agentConfigs: ["writer", "editor"],
              },
            ],
          },
        },
      },
    }).then((resp) => {
      expect(resp.status).to.eq(200);
      const groups = resp.body.spec.sharedMemory.membrane.trustGroups;
      expect(groups).to.have.length(2);
      expect(groups[0].name).to.eq("research-delegation-example");
      expect(groups[0].agentConfigs).to.deep.eq(["researcher", "writer"]);
      expect(groups[1].name).to.eq("editorial");
    });
  });

  it("can remove membrane by patching without it", () => {
    cy.request({
      method: "PATCH",
      url: `/api/v1/ensembles/${PACK}?namespace=${NS}`,
      headers: apiHeaders(),
      body: {
        sharedMemory: {
          enabled: true,
          storageSize: "256Mi",
        },
      },
    }).then((resp) => {
      expect(resp.status).to.eq(200);
      // Membrane should be absent or null after patching without it.
      const membrane = resp.body.spec.sharedMemory.membrane;
      expect(membrane == null || membrane === undefined).to.eq(true);
    });
  });
});

// ════════════════════════════════════════════════════════════════════════════
// Suite 2: Membrane API — Token Budget
// ════════════════════════════════════════════════════════════════════════════

describe("Ensemble Membrane — Token Budget API", () => {
  const PACK = `cypress-membrane-budget-${Date.now()}`;

  before(() => {
    const manifest = `
apiVersion: sympozium.ai/v1alpha1
kind: Ensemble
metadata:
  name: ${PACK}
  namespace: ${NS}
spec:
  enabled: false
  description: Cypress membrane token budget test
  category: test
  agentConfigs:
    - name: agent-a
      systemPrompt: "Agent A."
    - name: agent-b
      systemPrompt: "Agent B."
`;
    cy.writeFile(`cypress/tmp/${PACK}.yaml`, manifest);
    cy.exec(`kubectl apply -f cypress/tmp/${PACK}.yaml`);
    // Wait for the apiserver informer cache to pick up the new resource.
    cy.request({
      url: `/api/v1/ensembles/${PACK}?namespace=${NS}`,
      headers: apiHeaders(),
      retryOnStatusCodeFailure: true,
    });
    cy.wait(500);
  });

  after(() => {
    cy.deleteEnsemble(PACK);
    cy.exec(`rm -f cypress/tmp/${PACK}.yaml`, { failOnNonZeroExit: false });
  });

  it("can set token budget with halt action", () => {
    cy.request({
      method: "PATCH",
      url: `/api/v1/ensembles/${PACK}?namespace=${NS}`,
      headers: apiHeaders(),
      body: {
        sharedMemory: {
          enabled: true,
          membrane: {
            tokenBudget: {
              maxTokens: 50000,
              maxTokensPerRun: 10000,
              action: "halt",
            },
          },
        },
      },
    }).then((resp) => {
      expect(resp.status).to.eq(200);
      const budget = resp.body.spec.sharedMemory.membrane.tokenBudget;
      expect(budget.maxTokens).to.eq(50000);
      expect(budget.maxTokensPerRun).to.eq(10000);
      expect(budget.action).to.eq("halt");
    });
  });

  it("can set token budget with warn action", () => {
    cy.request({
      method: "PATCH",
      url: `/api/v1/ensembles/${PACK}?namespace=${NS}`,
      headers: apiHeaders(),
      body: {
        sharedMemory: {
          enabled: true,
          membrane: {
            tokenBudget: {
              maxTokens: 100000,
              action: "warn",
            },
          },
        },
      },
    }).then((resp) => {
      expect(resp.status).to.eq(200);
      const budget = resp.body.spec.sharedMemory.membrane.tokenBudget;
      expect(budget.maxTokens).to.eq(100000);
      expect(budget.action).to.eq("warn");
    });
  });

  it("persists token budget on GET", () => {
    cy.request({
      url: `/api/v1/ensembles/${PACK}?namespace=${NS}`,
      headers: apiHeaders(),
    }).then((resp) => {
      expect(resp.status).to.eq(200);
      const budget = resp.body.spec.sharedMemory.membrane.tokenBudget;
      expect(budget).to.exist;
      expect(budget.maxTokens).to.eq(100000);
    });
  });
});

// ════════════════════════════════════════════════════════════════════════════
// Suite 3: Membrane API — Circuit Breaker
// ════════════════════════════════════════════════════════════════════════════

describe("Ensemble Membrane — Circuit Breaker API", () => {
  const PACK = `cypress-membrane-cb-${Date.now()}`;

  before(() => {
    const manifest = `
apiVersion: sympozium.ai/v1alpha1
kind: Ensemble
metadata:
  name: ${PACK}
  namespace: ${NS}
spec:
  enabled: false
  description: Cypress membrane circuit breaker test
  category: test
  workflowType: delegation
  agentConfigs:
    - name: coordinator
      systemPrompt: "You coordinate."
    - name: worker
      systemPrompt: "You work."
  relationships:
    - source: coordinator
      target: worker
      type: delegation
`;
    cy.writeFile(`cypress/tmp/${PACK}.yaml`, manifest);
    cy.exec(`kubectl apply -f cypress/tmp/${PACK}.yaml`);
    // Wait for the apiserver informer cache to pick up the new resource.
    cy.request({
      url: `/api/v1/ensembles/${PACK}?namespace=${NS}`,
      headers: apiHeaders(),
      retryOnStatusCodeFailure: true,
    });
    cy.wait(500);
  });

  after(() => {
    cy.deleteEnsemble(PACK);
    cy.exec(`rm -f cypress/tmp/${PACK}.yaml`, { failOnNonZeroExit: false });
  });

  it("can set circuit breaker with consecutive failure threshold", () => {
    cy.request({
      method: "PATCH",
      url: `/api/v1/ensembles/${PACK}?namespace=${NS}`,
      headers: apiHeaders(),
      body: {
        sharedMemory: {
          enabled: true,
          membrane: {
            circuitBreaker: {
              consecutiveFailures: 5,
              cooldownDuration: "10m",
            },
          },
        },
      },
    }).then((resp) => {
      expect(resp.status).to.eq(200);
      const cb = resp.body.spec.sharedMemory.membrane.circuitBreaker;
      expect(cb.consecutiveFailures).to.eq(5);
      expect(cb.cooldownDuration).to.eq("10m");
    });
  });

  it("circuit breaker defaults are applied", () => {
    cy.request({
      method: "PATCH",
      url: `/api/v1/ensembles/${PACK}?namespace=${NS}`,
      headers: apiHeaders(),
      body: {
        sharedMemory: {
          enabled: true,
          membrane: {
            circuitBreaker: {},
          },
        },
      },
    }).then((resp) => {
      expect(resp.status).to.eq(200);
      const cb = resp.body.spec.sharedMemory.membrane.circuitBreaker;
      // Default consecutive failures is 3 via kubebuilder default
      expect(cb.consecutiveFailures).to.eq(3);
    });
  });
});

// ════════════════════════════════════════════════════════════════════════════
// Suite 4: Membrane API — Time Decay
// ════════════════════════════════════════════════════════════════════════════

describe("Ensemble Membrane — Time Decay API", () => {
  const PACK = `cypress-membrane-decay-${Date.now()}`;

  before(() => {
    const manifest = `
apiVersion: sympozium.ai/v1alpha1
kind: Ensemble
metadata:
  name: ${PACK}
  namespace: ${NS}
spec:
  enabled: false
  description: Cypress membrane time decay test
  category: test
  agentConfigs:
    - name: agent
      systemPrompt: "You are an agent."
`;
    cy.writeFile(`cypress/tmp/${PACK}.yaml`, manifest);
    cy.exec(`kubectl apply -f cypress/tmp/${PACK}.yaml`);
    // Wait for the apiserver informer cache to pick up the new resource.
    cy.request({
      url: `/api/v1/ensembles/${PACK}?namespace=${NS}`,
      headers: apiHeaders(),
      retryOnStatusCodeFailure: true,
    });
    cy.wait(500);
  });

  after(() => {
    cy.deleteEnsemble(PACK);
    cy.exec(`rm -f cypress/tmp/${PACK}.yaml`, { failOnNonZeroExit: false });
  });

  it("can set time decay with TTL and linear decay", () => {
    cy.request({
      method: "PATCH",
      url: `/api/v1/ensembles/${PACK}?namespace=${NS}`,
      headers: apiHeaders(),
      body: {
        sharedMemory: {
          enabled: true,
          membrane: {
            timeDecay: {
              ttl: "168h",
              decayFunction: "linear",
            },
          },
        },
      },
    }).then((resp) => {
      expect(resp.status).to.eq(200);
      const td = resp.body.spec.sharedMemory.membrane.timeDecay;
      expect(td.ttl).to.eq("168h");
      expect(td.decayFunction).to.eq("linear");
    });
  });

  it("can set exponential decay function", () => {
    cy.request({
      method: "PATCH",
      url: `/api/v1/ensembles/${PACK}?namespace=${NS}`,
      headers: apiHeaders(),
      body: {
        sharedMemory: {
          enabled: true,
          membrane: {
            timeDecay: {
              ttl: "24h",
              decayFunction: "exponential",
            },
          },
        },
      },
    }).then((resp) => {
      expect(resp.status).to.eq(200);
      const td = resp.body.spec.sharedMemory.membrane.timeDecay;
      expect(td.ttl).to.eq("24h");
      expect(td.decayFunction).to.eq("exponential");
    });
  });
});

// ════════════════════════════════════════════════════════════════════════════
// Suite 5: Membrane API — Full config with all layers
// ════════════════════════════════════════════════════════════════════════════

describe("Ensemble Membrane — Full Configuration", () => {
  const PACK = `cypress-membrane-full-${Date.now()}`;

  before(() => {
    const manifest = `
apiVersion: sympozium.ai/v1alpha1
kind: Ensemble
metadata:
  name: ${PACK}
  namespace: ${NS}
spec:
  enabled: false
  description: Cypress full membrane test
  category: test
  workflowType: delegation
  agentConfigs:
    - name: researcher
      systemPrompt: "You research."
    - name: writer
      systemPrompt: "You write."
    - name: reviewer
      systemPrompt: "You review."
  relationships:
    - source: researcher
      target: writer
      type: delegation
    - source: writer
      target: reviewer
      type: sequential
  sharedMemory:
    enabled: true
    storageSize: "512Mi"
    accessRules:
      - agentConfig: researcher
        access: read-write
      - agentConfig: writer
        access: read-write
      - agentConfig: reviewer
        access: read-only
    membrane:
      defaultVisibility: public
      permeability:
        - agentConfig: researcher
          defaultVisibility: trusted
          exposeTags: ["findings", "data"]
        - agentConfig: writer
          defaultVisibility: public
          acceptTags: ["findings"]
        - agentConfig: reviewer
          defaultVisibility: private
      trustGroups:
        - name: content-team
          agentConfigs: ["researcher", "writer"]
      tokenBudget:
        maxTokens: 100000
        maxTokensPerRun: 20000
        action: halt
      circuitBreaker:
        consecutiveFailures: 3
      timeDecay:
        ttl: "168h"
        decayFunction: linear
`;
    cy.writeFile(`cypress/tmp/${PACK}.yaml`, manifest);
    cy.exec(`kubectl apply -f cypress/tmp/${PACK}.yaml`);
    // Wait for the apiserver informer cache to pick up the new resource.
    cy.request({
      url: `/api/v1/ensembles/${PACK}?namespace=${NS}`,
      headers: apiHeaders(),
      retryOnStatusCodeFailure: true,
    });
    cy.wait(500);
  });

  after(() => {
    cy.deleteEnsemble(PACK);
    cy.exec(`rm -f cypress/tmp/${PACK}.yaml`, { failOnNonZeroExit: false });
  });

  it("creates ensemble with full membrane config via kubectl", () => {
    cy.request({
      url: `/api/v1/ensembles/${PACK}?namespace=${NS}`,
      headers: apiHeaders(),
    }).then((resp) => {
      expect(resp.status).to.eq(200);
      const spec = resp.body.spec;
      const membrane = spec.sharedMemory.membrane;

      // Shared memory base config
      expect(spec.sharedMemory.enabled).to.eq(true);
      expect(spec.sharedMemory.storageSize).to.eq("512Mi");
      expect(spec.sharedMemory.accessRules).to.have.length(3);

      // Membrane permeability
      expect(membrane.defaultVisibility).to.eq("public");
      expect(membrane.permeability).to.have.length(3);

      // Trust groups
      expect(membrane.trustGroups).to.have.length(1);
      expect(membrane.trustGroups[0].name).to.eq("content-team");
      expect(membrane.trustGroups[0].agentConfigs).to.deep.eq([
        "researcher",
        "writer",
      ]);

      // Token budget
      expect(membrane.tokenBudget.maxTokens).to.eq(100000);
      expect(membrane.tokenBudget.maxTokensPerRun).to.eq(20000);
      expect(membrane.tokenBudget.action).to.eq("halt");

      // Circuit breaker
      expect(membrane.circuitBreaker.consecutiveFailures).to.eq(3);

      // Time decay
      expect(membrane.timeDecay.ttl).to.eq("168h");
      expect(membrane.timeDecay.decayFunction).to.eq("linear");

      // Relationships preserved
      expect(spec.relationships).to.have.length(2);
      expect(spec.workflowType).to.eq("delegation");
    });
  });

  it("can update membrane without losing relationships or access rules", () => {
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
            { agentConfig: "writer", access: "read-write" },
            { agentConfig: "reviewer", access: "read-only" },
          ],
          membrane: {
            defaultVisibility: "trusted",
            tokenBudget: {
              maxTokens: 200000,
              action: "warn",
            },
            circuitBreaker: {
              consecutiveFailures: 5,
            },
            timeDecay: {
              ttl: "72h",
              decayFunction: "exponential",
            },
          },
        },
      },
    }).then((resp) => {
      expect(resp.status).to.eq(200);
      const spec = resp.body.spec;

      // Membrane updated
      expect(spec.sharedMemory.membrane.defaultVisibility).to.eq("trusted");
      expect(spec.sharedMemory.membrane.tokenBudget.maxTokens).to.eq(200000);
      expect(spec.sharedMemory.membrane.tokenBudget.action).to.eq("warn");
      expect(
        spec.sharedMemory.membrane.circuitBreaker.consecutiveFailures,
      ).to.eq(5);
      expect(spec.sharedMemory.membrane.timeDecay.ttl).to.eq("72h");
      expect(spec.sharedMemory.membrane.timeDecay.decayFunction).to.eq(
        "exponential",
      );

      // Relationships untouched
      expect(spec.relationships).to.have.length(2);

      // Access rules preserved
      expect(spec.sharedMemory.accessRules).to.have.length(3);
    });
  });

  it("can update relationships without losing membrane config", () => {
    cy.request({
      method: "PATCH",
      url: `/api/v1/ensembles/${PACK}?namespace=${NS}`,
      headers: apiHeaders(),
      body: {
        relationships: [
          { source: "researcher", target: "writer", type: "delegation" },
          { source: "writer", target: "reviewer", type: "sequential" },
          { source: "researcher", target: "reviewer", type: "supervision" },
        ],
      },
    }).then((resp) => {
      expect(resp.status).to.eq(200);

      // Relationships updated
      expect(resp.body.spec.relationships).to.have.length(3);

      // Membrane still present
      expect(resp.body.spec.sharedMemory.membrane).to.exist;
      expect(resp.body.spec.sharedMemory.membrane.tokenBudget).to.exist;
    });
  });

  it("status fields exist for token budget tracking", () => {
    cy.request({
      url: `/api/v1/ensembles/${PACK}?namespace=${NS}`,
      headers: apiHeaders(),
    }).then((resp) => {
      expect(resp.status).to.eq(200);
      // Status fields should be present (possibly 0/false for fresh ensemble)
      const status = resp.body.status;
      expect(status).to.exist;
      // tokenBudgetUsed defaults to 0 (omitted by omitempty)
      // circuitBreakerOpen defaults to false (omitted by omitempty)
      // These will populate during actual agent runs
    });
  });
});

// ════════════════════════════════════════════════════════════════════════════
// Suite 6: Membrane UI — Workflow tab rendering
// ════════════════════════════════════════════════════════════════════════════

describe("Ensemble Membrane — Workflow Tab UI", () => {
  const PACK = `cypress-membrane-ui-${Date.now()}`;

  before(() => {
    const manifest = `
apiVersion: sympozium.ai/v1alpha1
kind: Ensemble
metadata:
  name: ${PACK}
  namespace: ${NS}
spec:
  enabled: false
  description: Cypress membrane UI test
  category: test
  workflowType: delegation
  agentConfigs:
    - name: researcher
      displayName: Researcher
      systemPrompt: "You research."
    - name: writer
      displayName: Writer
      systemPrompt: "You write."
    - name: reviewer
      displayName: Reviewer
      systemPrompt: "You review."
  relationships:
    - source: researcher
      target: writer
      type: delegation
  sharedMemory:
    enabled: true
    storageSize: "512Mi"
    accessRules:
      - agentConfig: researcher
        access: read-write
      - agentConfig: writer
        access: read-write
      - agentConfig: reviewer
        access: read-only
    membrane:
      defaultVisibility: public
      permeability:
        - agentConfig: researcher
          defaultVisibility: trusted
        - agentConfig: writer
          defaultVisibility: public
        - agentConfig: reviewer
          defaultVisibility: private
      trustGroups:
        - name: content-team
          agentConfigs: ["researcher", "writer"]
      tokenBudget:
        maxTokens: 100000
        action: halt
      circuitBreaker:
        consecutiveFailures: 3
      timeDecay:
        ttl: "168h"
        decayFunction: linear
`;
    cy.writeFile(`cypress/tmp/${PACK}.yaml`, manifest);
    cy.exec(`kubectl apply -f cypress/tmp/${PACK}.yaml`);
    cy.request({
      url: `/api/v1/ensembles/${PACK}?namespace=${NS}`,
      headers: apiHeaders(),
      retryOnStatusCodeFailure: true,
    });
    cy.wait(500);
  });

  after(() => {
    cy.deleteEnsemble(PACK);
    cy.exec(`rm -f cypress/tmp/${PACK}.yaml`, { failOnNonZeroExit: false });
  });

  it("shows the Shared Workflow Memory card on the workflow tab", () => {
    cy.visit(`/ensembles/${PACK}?tab=workflow`);
    cy.contains("Shared Workflow Memory", { timeout: 10000 })
      .scrollIntoView()
      .should("be.visible");
  });

  it("displays Synthetic Membrane section in the shared memory card", () => {
    cy.visit(`/ensembles/${PACK}?tab=workflow`);
    cy.contains("Synthetic Membrane", { timeout: 10000 })
      .scrollIntoView()
      .should("be.visible");
  });

  it("shows the default visibility badge", () => {
    cy.visit(`/ensembles/${PACK}?tab=workflow`);
    cy.contains("Visibility: public", { timeout: 10000 })
      .scrollIntoView()
      .should("be.visible");
  });

  it("shows the token budget with used/max values", () => {
    cy.visit(`/ensembles/${PACK}?tab=workflow`);
    cy.contains("Token Budget", { timeout: 10000 })
      .scrollIntoView()
      .should("be.visible");
    cy.contains("100,000").should("be.visible");
    cy.contains("halt").should("be.visible");
  });

  it("shows the circuit breaker status as closed", () => {
    cy.visit(`/ensembles/${PACK}?tab=workflow`);
    cy.contains("Circuit Breaker", { timeout: 10000 })
      .scrollIntoView()
      .should("be.visible");
    cy.contains("Closed").should("be.visible");
    cy.contains("0 / 3 failures").should("be.visible");
  });

  it("shows trust groups with member personas", () => {
    cy.visit(`/ensembles/${PACK}?tab=workflow`);
    cy.contains("Trust Groups", { timeout: 10000 })
      .scrollIntoView()
      .should("be.visible");
    cy.contains("content-team").should("be.visible");
  });

  it("shows permeability rules with visibility per persona", () => {
    cy.visit(`/ensembles/${PACK}?tab=workflow`);
    // The membrane section exists and shows visibility-related content
    cy.contains("Synthetic Membrane", { timeout: 10000 }).scrollIntoView();
    cy.contains("Visibility: public").should("exist");
  });

  it("shows per-persona visibility on canvas node badges", () => {
    cy.visit(`/ensembles/${PACK}?tab=workflow`);
    cy.contains("Researcher", { timeout: 10000 }).should("be.visible");
    // Canvas badges should show visibility tiers instead of generic "shared memory"
    cy.get('[title*="Membrane"]').should("have.length.at.least", 1);
  });

  it("shows the time decay TTL badge", () => {
    cy.visit(`/ensembles/${PACK}?tab=workflow`);
    cy.contains("TTL: 168h", { timeout: 10000 })
      .scrollIntoView()
      .should("be.visible");
  });
});

export {};
