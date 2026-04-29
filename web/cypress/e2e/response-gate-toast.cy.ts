// Response gate toast: verify that when a run transitions to PostRunning with
// a gate hook, a warning toast fires telling the user approval is required.

const INSTANCE = `cy-gate-toast-${Date.now()}`;
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
          - name: toast-gate
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

describe("Response gate -- toast notification", () => {
  before(() => {
    applyInstance();
    cy.wait(3000);
  });

  after(() => {
    if (RUN_NAME) cy.deleteRun(RUN_NAME);
    cy.deleteAgent(INSTANCE);
  });

  it("shows a warning toast when a run enters PostRunning with a gate hook", () => {
    // Visit the runs page first so the notification hook is mounted and
    // initializes its phase snapshot (no toast for existing runs).
    cy.visit("/runs");
    cy.wait(3000); // let the hook initialize

    // Dispatch the run while on the page so the hook sees the transition.
    cy.dispatchRun(INSTANCE, "Reply with exactly: TOAST_SENTINEL").then(
      (name) => {
        RUN_NAME = name;
      },
    );

    // The "Approval required" toast should appear when the run transitions
    // to PostRunning. Give it time for the agent to run and the 5s polling
    // to pick up the phase change.
    cy.contains("Approval required", { timeout: 5 * 60 * 1000 }).should(
      "be.visible",
    );
  });
});

export {};
