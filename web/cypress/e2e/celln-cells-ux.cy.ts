/**
 * Celln cells in the console: `celln ps` per fleet node on the Harnesses page,
 * parents and running cells on the topology graph, and enduring Celln agents
 * in the feed's Persistent tab. Intercepted browser contract tests — they
 * need no cluster and are not live execution evidence.
 */

const now = Date.now();
const incarnation = "blake3:" + "a".repeat(64);
const child = "blake3:" + "c".repeat(64);
const run = { namespace: "default", name: "hermes-abc12", agent: "hermes", phase: "Running", live: true };

const cells = [
  {
    node: "framework",
    reportedMs: now - 3000,
    stale: false,
    source: "gateway",
    cells: [
      { id: "2e1e3481f3b7", description: child.slice(0, 29) + "…", status: "running", backend: "kvm", started_ms: now - 12000, finished_ms: null, duration_ms: null, error: null, tools: ["/worker"], run, parent: incarnation, turn: "initial" },
      { id: "07359a714094", description: "blake3:80a5803438c9b4dba1f6bc8…", status: "failed", backend: "kvm", started_ms: now - 600000, finished_ms: now - 570000, duration_ms: 30000, error: "guest exited with code 1", tools: ["/worker"] },
    ],
    parents: [
      { incarnation, updatedMs: now - 12000, turns: [{ turnId: "initial", stage: "reserved", child }], run, status: "TurnActive", statusLive: true },
      { incarnation: "blake3:" + "b".repeat(64), updatedMs: now - 900000, turns: [{ turnId: "initial", stage: "child-destroyed", succeeded: false }], run: { ...run, name: "hermes-old01", phase: "Failed", live: false } },
    ],
  },
  // A backend the gateway could not list, filled from its node's own report.
  { node: "gpu-2", reportedMs: now - 600000, stale: true, source: "node-report", cells: [], parents: [] },
];

const hermes = {
  metadata: { name: "hermes", namespace: "default" },
  spec: {
    runtimeRef: "celln-llama-server",
    agents: { default: { model: "Qwen3.8-27B-UD-Q4_K_XL.gguf" } },
    execution: {
      backend: "celln", executionLifecycle: "enduring", modelConnectionRef: "celln-llama-server", model: "Qwen3.8-27B-UD-Q4_K_XL.gguf",
      cellnSelection: { runtimeRef: "celln-llama-server", toolRefs: [], clusterToolRefs: [{ name: "celln-starter-grep", revision: "v1" }] },
      enduring: { leaseSeconds: 14400, maxTurns: 64, maxModelRequests: 384, maxOutputTokens: 196608 },
    },
  },
  status: { phase: "Running" },
};

const wrapper = {
  metadata: { name: "celln-llama-server", namespace: "default", labels: { "sympozium.ai/managed-by": "celln-platform" } },
  spec: { cellnProfileRef: { name: "celln-native-starter-llama-server", revision: "v1" }, supportOwner: "celln-platform" },
};

// The platform's own wrapper Agent names only its fleet runtime.
const wrapperAgent = { metadata: { name: "celln-agent", namespace: "default" }, spec: { runtimeRef: "celln-native" }, status: { phase: "Running" } };
const nativeWrapper = {
  metadata: { name: "celln-native", namespace: "default", labels: { "sympozium.ai/managed-by": "celln-platform" } },
  spec: { cellnProfileRef: { name: "celln-native-starter", revision: "v1" }, supportOwner: "celln-platform" },
};

const conversation = {
  metadata: { name: "hermes-abc12", namespace: "default", uid: "run-uid", creationTimestamp: new Date(now - 12000).toISOString() },
  spec: { agentRef: "hermes", backend: "celln", executionLifecycle: "enduring", task: "Hello", enduring: { maxTurns: 64 } },
  status: { phase: "Running", conditions: [{ type: "CellnParentReady", status: "True" }], cellnParent: { acceptedTurns: 0, createAttempted: true, binding: { incarnation }, initialTurn: { id: "initial", attempted: true } } },
};

function stubConsole() {
  cy.intercept("GET", "**/api/v1/**", { body: [] });
  cy.intercept("GET", "**/api/v1/celln-platform/cells*", { body: cells }).as("cells");
  cy.intercept("GET", "**/api/v1/agents*", { body: [hermes, wrapperAgent] });
  cy.intercept("GET", "**/api/v1/runtimes*", { body: [wrapper, nativeWrapper] });
  cy.intercept("GET", "**/api/v1/runs*", { body: [conversation] });
  cy.intercept("GET", "**/api/v1/gateway*", { body: {} });
  cy.intercept("GET", "**/api/v1/density/**", { body: { nodes: [] } });
}

describe("Celln cells in the console", () => {
  beforeEach(stubConsole);

  it("shows celln ps per node on the Harnesses page, with -a and a node selector", () => {
    cy.visit("/harnesses#token=test-token");
    cy.wait("@cells");
    cy.get('[data-testid="celln-node-cells"]').within(() => {
      cy.contains("celln ps").should("be.visible");
      // Two sources in one fleet: said per node, and the parent's own status shown.
      cy.get('[data-testid="celln-cells-source"]').should("contain", "source: mixed");
      cy.get('[data-testid="celln-node-source-framework"]').should("contain", "source: gateway");
      cy.get('[data-testid="celln-node-source-gpu-2"]').should("contain", "source: node reports");
      cy.get('[data-testid="celln-parent-status"]').should("contain", "TurnActive");
      cy.get('[data-testid="celln-node-framework"]').within(() => {
        cy.contains("1 running");
        cy.contains("1 live parent");
        cy.contains("tr", "2e1e3481f3b7").should("contain", "default/hermes-abc12").and("contain", "initial");
        // Finished cells and ended parents only with -a.
        cy.contains("07359a714094").should("not.exist");
        cy.contains("hermes-old01").should("not.exist");
      });
      cy.get('[data-testid="celln-node-gpu-2"]').should("contain", "stale");
      cy.contains("label", "Show finished (-a)").click();
      cy.contains("celln ps -a");
      cy.get('[data-testid="celln-node-framework"]').within(() => {
        cy.contains("tr", "07359a714094").should("contain", "failed").and("contain", "guest exited with code 1");
        cy.contains("hermes-old01").should("be.visible");
      });
      cy.get("button[role=combobox]").click();
    });
    cy.get("[role=option]").contains("gpu-2").click();
    cy.get('[data-testid="celln-node-framework"]').should("not.exist");
    cy.get('[data-testid="celln-node-gpu-2"]').should("be.visible");
  });

  it("updates the cells table without a reload", () => {
    let polls = 0;
    cy.intercept("GET", "**/api/v1/celln-platform/cells*", (request) => {
      polls++;
      const report = structuredClone(cells[0]);
      report.reportedMs = Date.now() - 1000;
      // From the third poll on, the node's turn has finished.
      if (polls >= 3) {
        report.cells[0] = { ...report.cells[0], status: "dissolved", finished_ms: Date.now(), duration_ms: 4200 };
      }
      request.reply({ body: [report] });
    }).as("poll");
    cy.visit("/harnesses#token=test-token");
    cy.get('[data-testid="celln-cells-live"]').should("contain", "live");
    cy.get('[data-testid="celln-node-framework"]').should("contain", "1 running").and("contain", "2e1e3481f3b7");
    cy.wait(["@poll", "@poll", "@poll"]);
    cy.get('[data-testid="celln-node-framework"]', { timeout: 10000 }).should("contain", "0 running").and("not.contain", "2e1e3481f3b7");
    cy.contains("label", "Show finished (-a)").click();
    cy.contains("tr", "2e1e3481f3b7").should("contain", "dissolved").and("contain", "4.2 s");
  });

  it("does not list the fleet's wrapper runtimes as Kubernetes harnesses", () => {
    cy.visit("/harnesses#token=test-token");
    cy.wait("@cells");
    cy.contains("No approved harnesses are registered").should("be.visible");
  });

  it("draws the live parent and its running cell on the topology", () => {
    cy.visit("/topology#token=test-token", {
      onBeforeLoad(win) {
        win.localStorage.removeItem("sympozium_topology_positions");
      },
    });
    cy.wait("@cells");
    cy.get(".react-flow__node-k8sNode").should("contain", "framework");
    cy.get(".react-flow__node-cellnCell").should("have.length", 2);
    cy.get(".react-flow__node-cellnCell").contains("Celln parent").parents(".react-flow__node").should("contain", "hermes-abc12");
    cy.get(".react-flow__node-cellnCell").contains("Celln cell").parents(".react-flow__node").should("contain", "2e1e3481f3b7").and("contain", "turn initial");
  });

  it("lists enduring Celln agents as persistent chats in the feed", () => {
    cy.intercept("GET", "**/api/v1/runs/hermes-abc12/turns*", { body: { runUID: "run-uid", items: [], continue: "" } });
    cy.visit("/dashboard#token=test-token");
    cy.get('[title="Open feed"]').click();
    cy.contains("button", "Persistent").click();
    cy.get('[data-testid="feed-celln-agent-hermes"]').should("contain", "1 live").and("contain", "celln-llama-server").within(() => {
      cy.contains("button", "Open").click();
    });
    cy.get('[data-testid="feed-celln-conversation"]').should("contain", "hermes");
    cy.contains("button", "Back").click();
    // The platform wrapper Agent is a persistent Celln chat too.
    cy.get('[data-testid="feed-celln-agent-celln-agent"]').should("contain", "New").and("contain", "celln-native");
    cy.get('[data-testid="feed-celln-agent-hermes"]').should("be.visible");
    // A Celln agent is not offered for Kubernetes quick tasks.
    cy.contains("button", "Runs").click();
    cy.contains("No persistent agents").should("not.exist");
  });
});

// Two fleet backends: the Provider step picks the backend, so the wizard must
// not also ask which wrapper runtime to use, and a fleet runtime must not be
// asked for a Kubernetes harness policy. Intercepted; no cluster needed.
