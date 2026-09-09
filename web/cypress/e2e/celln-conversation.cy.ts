// Intercepted browser contract tests: not live Celln/model execution evidence.
describe("Persistent Celln conversation", () => {
  it("explains pending admission without enabling execution or showing stale observations", () => {
    let generation = 1;
    cy.intercept("GET", "/api/v1/**", { body: [] });
    cy.intercept("GET", "/api/v1/runs/conversation*", (request) => request.reply({ body: {
      metadata: { name: "conversation", namespace: "default", uid: "pending-uid", generation },
      spec: { agentRef: "agent", backend: "celln", task: "Remember violet", executionLifecycle: "enduring", enduring: { maxTurns: 4 } },
      status: { phase: "Pending", conditions: [{ type: "CellnParentReady", status: "False", reason: "AdmissionPending", observedGeneration: 1, message: "Waiting for a matching operator-prepared parent registration and current grants." }] },
    } }));
    cy.intercept("GET", "/api/v1/runs/conversation/turns*", { body: { runUID: "pending-uid", items: [], continue: "" } });
    cy.visit("/runs/conversation");
    cy.contains("Waiting for parent admission — sending disabled").should("be.visible");
    cy.get('[data-testid="celln-parent-admission"]').should("contain", "current grants");
    cy.get('[data-testid="celln-turn-send"]').should("be.disabled");
    cy.then(() => { generation = 2; });
    cy.reload();
    cy.get('[data-testid="celln-conversation"]').should("be.visible");
    cy.get('[data-testid="celln-parent-admission"]').should("not.exist");
  });
  function open(ready = true, generation = 1, options: { reason?: string; acceptedTurns?: number; initialSucceeded?: boolean; active?: boolean } = {}) {
    cy.intercept("GET", "/api/v1/**", { body: [] });
    cy.intercept("GET", "/api/v1/runs/conversation*", { body: {
      metadata: { name: "conversation", namespace: "default", uid: "parent-uid", generation },
      spec: { agentRef: "agent", backend: "celln", task: "Remember violet", executionLifecycle: "enduring", enduring: { maxTurns: 4 } },
      status: { phase: ready ? "Running" : "Failed", conditions: [{ type: "CellnParentReady", status: ready ? "True" : "False", observedGeneration: 1, reason: options.reason }], cellnParent: {
        acceptedTurns: options.acceptedTurns ?? 0, createAttempted: true,
        activeTurn: options.active ? { name: "original-turn", uid: "original-turn-uid" } : undefined,
        initialTurn: { id: "initial", message: "Remember violet", attempted: true, result: { succeeded: options.initialSucceeded ?? true, answer: "Remembered violet" } },
      } },
    } });
    cy.intercept("GET", "/api/v1/runs/conversation/turns*", { body: { runUID: "parent-uid", items: [], continue: "" } }).as("history");
    cy.visit("/runs/conversation");
    cy.wait("@history");
  }

  for (const code of [202, 502]) {
    it(`keeps exact cancellation pending after HTTP ${code} and reload without resending`, () => {
      open(true, 1, { active: true });
      const turn = {
        metadata: { name: "original-turn", uid: "original-turn-uid" },
        spec: { runName: "conversation", runUID: "parent-uid", message: "Working" },
        status: { execution: { attempted: true } },
      };
      cy.intercept("GET", "/api/v1/runs/conversation/turns*", { body: { runUID: "parent-uid", items: [turn], continue: "" } }).as("cancelHistory");
      let requests = 0;
      cy.intercept("POST", "/api/v1/runs/conversation/turns/original-turn/cancel*", (request) => {
        requests++;
        expect(request.body).to.deep.eq({ runUID: "parent-uid", turnUID: "original-turn-uid" });
        request.reply({ statusCode: code, body: code === 202 ? turn : "Unconfirmed" });
      }).as("cancelTurn");
      cy.on("window:confirm", () => true);
      cy.get('[data-testid="celln-turn-cancel"]').should("be.enabled").click();
      cy.wait("@cancelTurn");
      cy.get('[data-testid="celln-turn-cancel-pending"]').should("contain", "child teardown is not confirmed");
      cy.get('[data-testid="celln-turn-cancel"]').should("be.disabled");
      cy.get('[data-testid="celln-turn-send"]').should("be.disabled");
      cy.reload();
      cy.wait("@cancelHistory");
      cy.get('[data-testid="celln-turn-cancel-pending"]').should("be.visible");
      cy.get('[data-testid="celln-turn-cancel"]').should("be.disabled").then(() => expect(requests).to.eq(1));
      cy.intercept("GET", "/api/v1/runs/conversation/turns*", { body: { runUID: "parent-uid", continue: "", items: [{
        ...turn, status: { execution: { attempted: true, result: { succeeded: false, answer: "Turn cancelled after child teardown." } } },
      }] } });
      cy.contains("Turn failed: Turn cancelled after child teardown.").should("be.visible");
      cy.get('[data-testid="celln-turn-cancel-pending"]').should("not.exist");
      // Parent-slot evidence is still active; the answer alone cannot enable work.
      cy.get('[data-testid="celln-turn-send"]').should("be.disabled");
    });
  }

  it("sends data only and displays the committed answer", () => {
    open();
    cy.intercept("POST", "/api/v1/runs/conversation/turns*", (request) => {
      expect(Object.keys(request.body).sort()).to.deep.eq(["message", "requestId", "runUID"]);
      expect(request.body.runUID).to.eq("parent-uid");
      expect(request.body.message).to.eq("What colour?");
      request.reply({ statusCode: 202, body: {} });
    }).as("submit");
    cy.get('[data-testid="celln-turn-message"]').type("What colour?");
    cy.get('[data-testid="celln-turn-send"]').click();
    cy.wait("@submit");
    cy.window().then((win) => {
      const pending = JSON.parse(win.sessionStorage.getItem("celln-turn:default:parent-uid")!);
      cy.intercept("GET", "/api/v1/runs/conversation/turns*", { body: { runUID: "parent-uid", continue: "", items: [{
        metadata: { name: pending.name, uid: "turn-uid" }, spec: { message: pending.message },
        status: { execution: { attempted: true, result: { succeeded: true, answer: "VIOLET" } } },
      }] } });
    });
    cy.contains("Agent: VIOLET").should("be.visible");
    cy.window().should((win) => expect(win.sessionStorage.getItem("celln-turn:default:parent-uid")).to.eq(null));
  });

  it("does not enable sending from readiness for an older run spec", () => {
    open(true, 2);
    cy.contains("Agent: Remembered violet").should("be.visible");
    cy.contains("Parent initialized").should("not.exist");
    cy.get('[data-testid="celln-turn-message"]').should("be.disabled");
    cy.get('[data-testid="celln-turn-send"]').should("be.disabled");
  });

  it("does not retry an unconfirmed POST, including after reload", () => {
    open();
    let submissions = 0;
    cy.intercept("POST", "/api/v1/runs/conversation/turns*", (request) => {
      submissions++;
      request.reply({ statusCode: 502, body: "Unconfirmed" });
    });
    // Reject at the fetch boundary: forceNetworkError can cause transport-level
    // retries inside Chromium/Cypress, which do not count application calls.
    cy.window().then((win) => {
      const original = win.fetch.bind(win);
      cy.stub(win, "fetch").callsFake((input, init) => {
        if (String(input).includes("/runs/conversation/turns") && init?.method === "POST") {
          submissions++;
          return Promise.reject(new TypeError("Failed to fetch"));
        }
        return original(input, init);
      });
    });
    cy.get('[data-testid="celln-turn-message"]').type("What colour?");
    cy.get('[data-testid="celln-turn-send"]').click();
    cy.contains("Submission could not be confirmed").should("be.visible");
    cy.get('[data-testid="celln-turn-send"]').should("be.disabled");
    cy.reload();
    cy.contains("It will not be resubmitted automatically").should("be.visible");
    cy.wait("@history");
    cy.get('[data-testid="celln-turn-send"]').should("be.disabled").then(() => expect(submissions).to.eq(1));
  });

  it("keeps historical answers visible but refuses sending after context loss", () => {
    open(false);
    cy.contains("Agent: Remembered violet").should("be.visible");
    cy.contains("Parent unavailable or starting").should("be.visible");
    cy.get('[data-testid="celln-turn-message"]').should("be.disabled");
    cy.get('[data-testid="celln-turn-send"]').should("be.disabled");
  });

  it("does not confuse a reused run name with the original conversation", () => {
    open();
    cy.intercept("GET", "/api/v1/runs/conversation/turns*", { body: { runUID: "replacement-uid", items: [], continue: "" } });
    cy.reload();
    cy.contains("Turn history unavailable").should("be.visible");
    cy.get('[data-testid="celln-turn-message"]').type("What colour?");
    cy.get('[data-testid="celln-turn-send"]').should("be.disabled");
  });

  for (const [reason, explanation] of [
    ["ContextLost", "Live harness context was lost"],
    ["Stopped", "The parent has stopped"],
    ["TeardownUncertain", "Parent teardown is unconfirmed"],
    ["ReconciliationRequired", "original parent's outcome is unconfirmed"],
  ]) {
    it(`explains ${reason} without treating history as live context`, () => {
      open(false, 1, { reason });
      cy.get('[data-testid="celln-parent-lifecycle-detail"]').should("contain", explanation);
      cy.contains("Agent: Remembered violet").should("be.visible");
      cy.get('[data-testid="celln-turn-send"]').should("be.disabled");
    });
  }

  it("does not display terminal details from an older generation", () => {
    open(false, 2, { reason: "ContextLost" });
    cy.get('[data-testid="celln-parent-lifecycle-detail"]').should("not.exist");
    cy.get('[data-testid="celln-turn-send"]').should("be.disabled");
  });

  it("explains the requested turn ceiling without claiming host capacity", () => {
    open(true, 1, { acceptedTurns: 3 });
    cy.get('[data-testid="celln-parent-turn-limit"]').should("contain", "4 total turns").and("contain", "host may enforce stricter limits");
    cy.get('[data-testid="celln-parent-lifecycle-detail"]').should("contain", "requested turn ceiling is exhausted");
    cy.get('[data-testid="celln-turn-message"]').should("be.disabled");
    cy.reload();
    cy.get('[data-testid="celln-turn-send"]').should("be.disabled");
  });

  it("labels an initial failure and explains why sending is disabled", () => {
    open(true, 1, { initialSucceeded: false });
    cy.contains("Initial turn failed: Remembered violet").should("be.visible");
    cy.get('[data-testid="celln-parent-lifecycle-detail"]').should("contain", "initial turn failed");
    cy.get('[data-testid="celln-turn-send"]').should("be.disabled");
  });

  it("explains the active turn slot", () => {
    open(true, 1, { active: true });
    cy.get('[data-testid="celln-parent-lifecycle-detail"]').should("contain", "One turn is already active");
    cy.get('[data-testid="celln-turn-message"]').should("be.disabled");
  });

  it("distinguishes a failed follow-up from an agent answer", () => {
    open();
    cy.intercept("GET", "/api/v1/runs/conversation/turns*", { body: { runUID: "parent-uid", continue: "", items: [{
      metadata: { name: "failed-turn", uid: "failed-turn-uid" }, spec: { message: "More context" },
      status: { execution: { attempted: true, result: { succeeded: false, answer: "context capacity exceeded" } } },
    }] } });
    cy.reload();
    cy.contains("Turn failed: context capacity exceeded").should("be.visible");
    cy.contains("Agent: context capacity exceeded").should("not.exist");
  });

  it("shows current turn reconciliation without exposing stale conditions", () => {
    open();
    let generation = 1;
    cy.intercept("GET", "/api/v1/runs/conversation/turns*", (request) => request.reply({ body: { runUID: "parent-uid", continue: "", items: [{
      metadata: { name: "uncertain-turn", uid: "uncertain-uid", generation }, spec: { message: "Original message" },
      status: { conditions: [{ type: "CellnTurnComplete", status: "False", reason: "ReconciliationRequired", observedGeneration: 1 }] },
    }] } }));
    cy.reload();
    cy.get('[data-testid="celln-turn-reconciliation"]').should("contain", "original request is retained; do not resubmit");
    cy.then(() => { generation = 2; });
    cy.reload();
    cy.get('[data-testid="celln-turn-reconciliation"]').should("not.exist");
  });

  it("pins confirmed deletion to the displayed run UID without claiming teardown", () => {
    open();
    cy.on("window:confirm", (message) => {
      expect(message).to.contain("not a pause");
      return true;
    });
    cy.intercept("DELETE", "/api/v1/runs/conversation*", (request) => {
      const url = new URL(request.url);
      expect(url.searchParams.get("uid")).to.eq("parent-uid");
      expect(url.searchParams.get("namespace")).to.eq("default");
      request.reply({ statusCode: 204 });
    }).as("deleteOriginal");
    cy.get('[data-testid="celln-delete-run"]').click();
    cy.wait("@deleteOriginal");
    cy.get('[data-testid="celln-delete-pending"]').should("contain", "not confirmation");
    cy.get('[data-testid="celln-turn-send"]').should("be.disabled");
  });

  it("does not replay uncertain deletion or re-enable turns after refresh", () => {
    open();
    cy.on("window:confirm", () => true);
    let deletions = 0;
    cy.intercept("DELETE", "/api/v1/runs/conversation*", (request) => {
      deletions++;
      request.reply({ statusCode: 503, body: "unavailable" });
    });
    cy.get('[data-testid="celln-delete-run"]').click();
    cy.contains("Deletion could not be confirmed").should("be.visible");
    cy.reload();
    cy.get('[data-testid="celln-delete-pending"]').should("be.visible");
    cy.get('[data-testid="celln-delete-run"]').should("be.disabled");
    cy.get('[data-testid="celln-turn-send"]').should("be.disabled").then(() => expect(deletions).to.eq(1));
  });

  it("does not request deletion when confirmation is declined", () => {
    open();
    cy.on("window:confirm", () => false);
    cy.intercept("DELETE", "/api/v1/runs/conversation*", () => { throw new Error("cancelled deletion reached API"); });
    cy.get('[data-testid="celln-delete-run"]').click();
    cy.get('[data-testid="celln-delete-pending"]').should("not.exist");
    cy.get('[data-testid="celln-turn-message"]').should("be.enabled");
  });

  it("clears a cached run after confirmed API absence", () => {
    open();
    cy.intercept("GET", /\/api\/v1\/runs\/conversation\?/, { statusCode: 404, body: "run not found" });
    cy.contains("Run not found", { timeout: 20000 }).should("be.visible");
    cy.get('[data-testid="celln-conversation"]').should("not.exist");
  });

  it("does not mistake an unavailable refresh for deletion or live readiness", () => {
    open();
    cy.intercept("GET", /\/api\/v1\/runs\/conversation\?/, { statusCode: 503, body: "run storage unavailable" });
    cy.contains("Run status could not be refreshed", { timeout: 20000 }).should("be.visible");
    cy.contains("Agent: Remembered violet").should("be.visible");
    cy.contains("Run not found").should("not.exist");
    cy.get('[data-testid="celln-turn-send"]').should("be.disabled");
  });
});
