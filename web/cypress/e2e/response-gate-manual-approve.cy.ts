// Manual gate approval: gate hook exits without writing a verdict (simulating
// human-in-the-loop). Verify the run stays in PostRunning with an "Approval"
// badge, the detail page shows the approval bar, clicking Approve resolves
// the gate, and a toast fires when the run enters PostRunning.

const INSTANCE = `cy-gate-manual-${Date.now()}`;
let RUN_NAME = "";

function applyInstance() {
  // The gate hook exits 0 immediately without patching a verdict.
  // With gateDefault=allow, the controller will eventually pass the result
  // through -- but we set gateDefault=block so the run stays held until
  // manual intervention via the UI.
  const manifest = `apiVersion: sympozium.ai/v1alpha1
kind: Agent
metadata:
  name: ${INSTANCE}
  namespace: default
spec:
  authRefs:
    - provider: lm-studio
      secret: ""
  agents:
    default:
      model: qwen/qwen3.5-9b
      baseURL: http://host.docker.internal:1234/v1
      lifecycle:
        gateDefault: block
        rbac:
          - apiGroups: ["sympozium.ai"]
            resources: ["agentruns"]
            verbs: ["get", "patch"]
        postRun:
          - name: manual-gate
            image: busybox:1.36
            gate: true
            command: ["sh", "-c"]
            args:
              - echo "gate hook running, exiting without verdict"; sleep 300
            timeout: 10m
`;
  cy.writeFile(`cypress/tmp/${INSTANCE}.yaml`, manifest);
  cy.exec(`kubectl apply -f cypress/tmp/${INSTANCE}.yaml`);
}

function authHeaders(): Record<string, string> {
  const token = Cypress.env("API_TOKEN");
  const h: Record<string, string> = { "Content-Type": "application/json" };
  if (token) h["Authorization"] = `Bearer ${token}`;
  return h;
}

/** Poll until the run reaches PostRunning phase. */
function waitForPostRunning(runName: string, timeoutMs = 5 * 60 * 1000) {
  const started = Date.now();
  const poll = (): Cypress.Chainable<void> => {
    return cy
      .request({
        url: `/api/v1/runs/${runName}?namespace=default`,
        headers: authHeaders(),
        failOnStatusCode: false,
      })
      .then((resp) => {
        const phase = resp.body?.status?.phase as string | undefined;
        if (phase === "PostRunning") return;
        if (Date.now() - started > timeoutMs) {
          throw new Error(
            `waitForPostRunning(${runName}) timed out; last phase=${phase ?? "none"}`,
          );
        }
        cy.wait(2000, { log: false });
        return poll();
      });
  };
  return poll();
}

describe("Response gate -- manual approval via UI", () => {
  before(() => {
    applyInstance();
    cy.wait(3000);
    cy.dispatchRun(INSTANCE, "Reply with exactly: MANUAL_GATE_SENTINEL").then(
      (name) => {
        RUN_NAME = name;
      },
    );
    // Wait for the run to reach PostRunning (gate hook is sleeping).
    cy.then(() => waitForPostRunning(RUN_NAME));
  });

  after(() => {
    if (RUN_NAME) cy.deleteRun(RUN_NAME);
    cy.deleteAgent(INSTANCE);
  });

  it("shows gate pending badge on runs list and approval bar on detail page", () => {
    // Runs list: gate pending badge should be visible.
    cy.visit("/runs");
    cy.contains("td", INSTANCE, { timeout: 20000 })
      .parents("tr")
      .within(() => {
        cy.get("[data-testid='gate-pending-badge']").should("be.visible");
        cy.get("[data-testid='gate-approve-btn']").should("be.visible");
        cy.get("[data-testid='gate-reject-btn']").should("be.visible");
      });

    // Detail page: approval bar should be visible.
    cy.visit(`/runs/${RUN_NAME}`);
    cy.get("[data-testid='gate-approval-bar']", { timeout: 20000 }).should(
      "be.visible",
    );
    cy.get("[data-testid='gate-approve-detail-btn']").should("be.visible");
    cy.get("[data-testid='gate-reject-detail-btn']").should("be.visible");
  });

  it("clicking Approve resolves the gate and the run succeeds", () => {
    cy.visit(`/runs/${RUN_NAME}`);
    cy.get("[data-testid='gate-approval-bar']", { timeout: 20000 }).should(
      "be.visible",
    );

    // Click Approve.
    cy.get("[data-testid='gate-approve-detail-btn']").click();

    // Success toast should appear.
    cy.contains("Run approved", { timeout: 10000 }).should("be.visible");

    // Wait for the run to reach terminal phase.
    cy.then(() => cy.waitForRunTerminal(RUN_NAME, 3 * 60 * 1000));

    // Verify the run succeeded with gate verdict.
    cy.request({
      url: `/api/v1/runs/${RUN_NAME}?namespace=default`,
      headers: authHeaders(),
    }).then((resp) => {
      expect(resp.body.status.phase).to.eq("Succeeded");
      expect(resp.body.status.gateVerdict).to.eq("approved");
    });

    // The approval bar should be gone, replaced by the verdict banner.
    cy.visit(`/runs/${RUN_NAME}`);
    cy.get("[data-testid='gate-approval-bar']").should("not.exist");
    cy.get("[data-testid='gate-verdict-banner']", { timeout: 10000 })
      .should("be.visible")
      .and("contain.text", "approved");
  });
});

export {};
