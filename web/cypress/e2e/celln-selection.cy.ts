// Browser UI contract tests with intercepted APIs, not model/cluster E2E.
const native = { metadata: { name: "native", namespace: "default" }, spec: { celln: { contractVersion: "celln.json-tools/v1", revision: "v1", lifecycle: "disposable-one-shot", publisherKey: "public" } } };
const oci = { metadata: { name: "oci", namespace: "default" }, spec: { image: "example/oci:v1" } };
const tools = ["uppercase", "length"].map((name) => ({ metadata: { name, namespace: "default" }, spec: { revision: "v1", description: `${name} fixture`, publisherKey: "public-publisher", invocationABI: "celln.json-stdio/v1", lane: "tool", limits: { timeoutMillis: 1000, memoryBytes: 1024, argumentBytes: 128, outputBytes: 128, workspace: "none", effects: "none" } } }));

function openForm(runtime = "native", unavailable = false, catalogue: unknown[] = tools) {
  // Keep this intercepted contract suite independent of a live API's auth.
  // Permission preview itself is covered by the unmocked live journey.
  cy.intercept("POST", "/api/v1/celln-selection/preview*", { statusCode: 503, body: "Preview is outside this selection fixture" });
  cy.intercept("GET", "/api/v1/**", { body: [] });
  cy.intercept("GET", "/api/v1/runs*", { body: [] });
  cy.intercept("GET", "/api/v1/agents*", { body: [{ metadata: { name: "agent" }, spec: { runtimeRef: runtime, agents: { default: { model: "deepseek-chat" } } } }] });
  cy.intercept("GET", "/api/v1/runtimes*", { body: [native, oci] });
  cy.intercept("GET", "/api/v1/celln-tools*", unavailable ? { statusCode: 503, body: "unavailable" } : { body: catalogue });
  cy.intercept("GET", "/api/v1/capabilities*", { body: { celln: { available: true, reason: "Node preflight only" } } });
  cy.visit("/runs?create=1");
  cy.get('[role="dialog"]').find('[role="combobox"]').eq(0).click();
  cy.get('[role="option"]').contains("agent").click();
  cy.get('textarea').type("Uppercase celln, then measure its length");
  cy.get('[role="dialog"]').find('[role="combobox"]').eq(2).click();
  cy.get('[role="option"]').contains("Celln —").click();
}

describe("Harness in Celln run selection", () => {
  it("selects only explicitly approved starter revisions and shows bounded permissions", () => {
    const catalogue = ["workspace-read", "workspace-write", "https-fetch"].map((name, index) => ({
      ...tools[0], metadata: { name, namespace: "default", uid: name },
      spec: { ...tools[0].spec, limits: { ...tools[0].spec.limits,
        ...(index === 2 ? { https: { allowHosts: ["example.com"], maxRequests: 2, maxResponseBytes: 4096, timeoutMillis: 1000 } }
          : { artifacts: { operation: index === 0 ? "read" : "write", maxOperations: 4, maxFiles: 8, maxFileBytes: 4096, maxTotalBytes: 16384 } }),
      } },
    }));
    openForm("native", false, catalogue);
    cy.intercept("POST", "/api/v1/celln-selection/preview*", (request) => {
      expect(request.body.executionLifecycle).to.eq("enduring");
      const refs = request.body.cellnSelection.toolRefs as { name: string; revision: string }[];
      if (refs.some((ref) => ref.name === "workspace-write")) {
        request.reply({ statusCode: 422, body: "Current agent grant refuses writes" });
      } else {
        request.reply({ body: { tools: refs.map((ref) => ({ tool: ref, limits: catalogue.find((tool) => tool.metadata.name === ref.name)!.spec.limits })), runtimeLimits: { memoryBytes: 1024 }, executionAuthorized: false, readiness: "not-established" } });
      }
    });
    cy.get('[data-testid="celln-enduring-opt-in"]').check();
    cy.get('[data-testid="celln-starter-tools"]').should("contain", "workspace-write: unavailable");
    cy.contains("button", "Use approved starter tools (2/3)").scrollIntoView().click();
    cy.get('[data-testid="celln-permission-preview"]').should("contain", "Run files: read").and("contain", "example.com").and("contain", "no model credentials");
    cy.intercept("POST", "/api/v1/runs*", (request) => {
      expect(request.body.cellnSelection.toolRefs).to.deep.eq([{ name: "workspace-read", revision: "v1" }, { name: "https-fetch", revision: "v1" }]);
      request.reply({ statusCode: 201, body: { metadata: { name: "starter" } } });
    }).as("starterCreate");
    cy.contains("button", "Request enduring run").click();
    cy.wait("@starterCreate");
  });

  it("does not offer absent starter tools as approved defaults", () => {
    openForm();
    cy.get('[data-testid="celln-enduring-opt-in"]').check();
    cy.get('[data-testid="celln-starter-tools"]').should("contain", "workspace-read: not installed");
    cy.contains("button", "Use approved starter tools (0/3)").should("be.disabled");
  });
  it("requires selected tools before submitting explicit per-turn tool execution intent", () => {
    openForm();
    cy.get('[data-testid="celln-enduring-opt-in"]').check();
    cy.get('[data-testid="celln-require-tool-call"]').check();
    cy.contains("button", "Request enduring run").should("be.disabled");
    cy.contains("label", "uppercase").find('input[type="checkbox"]').check();
    cy.intercept("POST", "/api/v1/runs*", (request) => {
      expect(request.body.enduring.requireToolCall).to.eq(true);
      expect(request.body.cellnSelection.toolRefs).to.deep.eq([{ name: "uppercase", revision: "v1" }]);
      request.reply({ statusCode: 201, body: { metadata: { name: "required-parent" } } });
    }).as("requiredParent");
    cy.contains("button", "Request enduring run").click();
    cy.wait("@requiredParent");
  });
  it("requests an enduring parent explicitly without claiming one-shot approval", () => {
    openForm();
    cy.get('[data-testid="celln-enduring-opt-in"]').check();
    cy.contains("A matching operator-prepared parent registration can admit it automatically").should("be.visible");
    cy.get('[data-testid="celln-parent-system-prompt"]').type("Retain conversation context.");
    cy.contains("Registered compositions can receive trusted issuance automatically").should("not.exist");
    cy.get('[data-testid="celln-maxTurns"]').clear().type("6");
    cy.intercept("POST", "/api/v1/runs*", (request) => {
      expect(request.body.executionLifecycle).to.eq("enduring");
      expect(request.body.systemPrompt).to.eq("Retain conversation context.");
      expect(request.body.enduring).to.deep.eq({ leaseSeconds: 300, maxTurns: 6, maxModelRequests: 12, maxOutputTokens: 4096 });
      expect(request.body.cellnSelection.toolRefs).to.deep.eq([]);
      expect(request.body).not.to.have.property("runtimeRef");
      request.reply({ statusCode: 201, body: { metadata: { name: "parent-request" } } });
    }).as("parentRequest");
    cy.contains("button", "Request enduring run").click();
    cy.wait("@parentRequest");
  });

  it("rejects invalid parent bounds and removes parent fields after opt-out", () => {
    openForm();
    cy.get('[data-testid="celln-enduring-opt-in"]').check();
    cy.get('[data-testid="celln-maxTurns"]').clear().type("1025");
    cy.contains("button", "Request enduring run").should("be.disabled");
    cy.get('[data-testid="celln-enduring-opt-in"]').uncheck();
    cy.intercept("POST", "/api/v1/runs*", (request) => {
      expect(request.body).not.to.have.property("executionLifecycle");
      expect(request.body).not.to.have.property("enduring");
      expect(request.body).not.to.have.property("systemPrompt");
      request.reply({ statusCode: 201, body: { metadata: { name: "one-shot" } } });
    }).as("oneShot");
    cy.contains("button", "Request catalogue run").click();
    cy.wait("@oneShot");
  });

  it("locks an unconfirmed enduring creation against another form submission", () => {
    openForm();
    cy.get('[data-testid="celln-enduring-opt-in"]').check();
    let submissions = 0;
    cy.intercept("POST", "/api/v1/runs*", (request) => {
      submissions++;
      request.reply({ statusCode: 502, body: "Unconfirmed" });
    });
    cy.contains("button", "Request enduring run").click();
    cy.contains("Creation was not confirmed").scrollIntoView().should("be.visible");
    cy.contains("button", "Request enduring run").should("be.disabled").then(() => expect(submissions).to.eq(1));
  });

  it("sends explicit ordered lending without an OCI runtime task override", () => {
    openForm();
    cy.contains("Selection readiness is not established").scrollIntoView().should("be.visible");
    cy.contains("Uses whatever AI provider").should("not.exist");
    cy.get('[data-testid="celln-harness-selection"]').contains("label", "uppercase@v1").find("input").check();
    cy.get('[data-testid="celln-harness-selection"]').contains("label", "length@v1").find("input").check();
    cy.intercept("POST", "/api/v1/runs*", (request) => {
      expect(request.body.backend).to.eq("celln");
      expect(request.body.provider).to.eq("deepseek");
      expect(request.body.model).to.eq("deepseek-chat");
      expect(request.body).not.to.have.property("runtimeRef");
      expect(request.body.cellnSelection).to.deep.eq({ toolRefs: [{ name: "uppercase", revision: "v1" }, { name: "length", revision: "v1" }] });
      request.reply({ statusCode: 201, body: { metadata: { name: "pending" } } });
    }).as("create");
    cy.contains("button", "Request catalogue run").click();
    cy.wait("@create");
  });

  it("keeps an empty list explicit and does not lend the whole catalogue", () => {
    openForm();
    cy.intercept("POST", "/api/v1/runs*", (request) => {
      expect(request.body.cellnSelection.toolRefs).to.deep.eq([]);
      request.reply({ statusCode: 201, body: { metadata: { name: "empty" } } });
    }).as("empty");
    cy.contains("button", "Request catalogue run").click();
    cy.wait("@empty");
  });

  it("blocks an OCI-only runtime instead of silently changing execution", () => {
    openForm("oci");
    cy.get('[role="dialog"]').contains('[role="alert"]', "does not declare the supported native JSON Celln contract").scrollIntoView().should("be.visible");
    cy.contains("button", "Request catalogue run").should("be.disabled");
  });

  it("blocks when catalogue loading fails", () => {
    openForm("native", true);
    cy.contains("Cannot load the catalogue", { timeout: 15000 }).scrollIntoView().should("be.visible");
    cy.contains("button", "Request catalogue run").should("be.disabled");
  });

  it("sends a one-run override inside catalogue selection, never as an OCI task", () => {
    openForm("oci");
    cy.get('[role="dialog"]').find('[role="combobox"]').eq(1).click();
    cy.get('[role="option"]').contains("native").click();
    cy.intercept("POST", "/api/v1/runs*", (request) => {
      expect(request.body).not.to.have.property("runtimeRef");
      expect(request.body.cellnSelection).to.deep.eq({ runtimeRef: "native", toolRefs: [] });
      request.reply({ statusCode: 201, body: { metadata: { name: "override" } } });
    }).as("override");
    cy.contains("button", "Request catalogue run").click();
    cy.wait("@override");
  });
});

describe("Celln Harness result presentation", () => {
  function openResult(result: string) {
    cy.intercept("GET", "/api/v1/**", { body: [] });
    cy.intercept("GET", "/api/v1/runs/example*", { body: {
      metadata: { name: "example", namespace: "default" },
      spec: { agentRef: "agent", backend: "celln", task: "Example task", cellnSelection: { runtimeRef: "native", toolRefs: [] } },
      status: { phase: "Succeeded", result },
    } });
    cy.visit("/runs/example");
    cy.contains('[role="tab"]', "Result").click();
  }

  it("shows the answer with an inspectable original trace", () => {
    openResult('CELLN_HARNESS_EVENT {"type":"tool","name":"uppercase"}\nCELLN_HARNESS_EVENT {"type":"completed","answer":"Hello from Celln"}');
    cy.get('[data-testid="celln-answer"]').should("have.text", "Hello from Celln");
    cy.contains("Harness-reported tool calls: uppercase").should("be.visible");
    cy.contains("summary", "Raw Harness output").click();
    cy.get("details pre").should("be.visible").and("contain.text", '"type":"completed"');
  });

  it("preserves malformed output instead of inventing an answer", () => {
    openResult('CELLN_HARNESS_EVENT null\nCELLN_HARNESS_EVENT {broken');
    cy.get('[data-testid="celln-answer"]').should("not.exist");
    cy.contains("pre", "{broken").should("be.visible");
  });

  it("does not execute HTML in a reported answer", () => {
    openResult('CELLN_HARNESS_EVENT {"type":"completed","answer":"<img src=x onerror=alert(1)>"}');
    cy.get('[data-testid="celln-answer"] img').should("not.exist");
    cy.get('[data-testid="celln-answer"]').should("contain.text", "<img");
  });
});
