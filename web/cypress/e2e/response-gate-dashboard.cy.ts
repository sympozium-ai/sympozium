// Dashboard widget: verify the "Awaiting Approval" panel shows gated runs
// and that approve/reject buttons work from the dashboard.

const INSTANCE = `cy-gate-dash-${Date.now()}`;
let RUN_NAME = "";

function applyInstance() {
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
          - name: dash-gate
            image: busybox:1.36
            gate: true
            command: ["sh", "-c"]
            args:
              - sleep 300
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
          throw new Error(`waitForPostRunning timed out; last phase=${phase}`);
        }
        cy.wait(2000, { log: false });
        return poll();
      });
  };
  return poll();
}

describe("Dashboard -- Awaiting Approval widget", () => {
  before(() => {
    applyInstance();
    cy.wait(3000);
    cy.dispatchRun(INSTANCE, "Reply with: DASH_GATE_SENTINEL").then((name) => {
      RUN_NAME = name;
    });
    cy.then(() => waitForPostRunning(RUN_NAME));
  });

  after(() => {
    if (RUN_NAME) cy.deleteRun(RUN_NAME);
    cy.deleteAgent(INSTANCE);
  });

  it("shows the gated run in the dashboard widget and approves it", () => {
    // The approval alert should appear at the top of the dashboard.
    cy.visit("/");
    cy.get("[data-testid='gate-approval-alert']", { timeout: 30000 }).should(
      "be.visible",
    );
    cy.get("[data-testid='gate-approval-card']").should("be.visible");

    // Click approve on the dashboard.
    cy.get("[data-testid='gate-dash-approve-btn']").first().click();

    // Verify the run reaches Succeeded with approved verdict.
    // The approve click fires the API call; the controller resolves
    // the gate within a few reconcile cycles.
    cy.then(() => cy.waitForRunTerminal(RUN_NAME, 3 * 60 * 1000)).then(
      (phase) => {
        expect(phase).to.eq("Succeeded");
      },
    );

    cy.then(() =>
      cy
        .request({
          url: `/api/v1/runs/${RUN_NAME}?namespace=default`,
          headers: authHeaders(),
        })
        .then((resp) => {
          expect(resp.body.status.gateVerdict).to.eq("approved");
        }),
    );
  });
});

export {};
