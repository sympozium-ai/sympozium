// Response gate: gate hook approves the agent output. Verify the run
// succeeds, the gate verdict is "approved", and the UI shows the banner.

const INSTANCE = `cy-gate-approve-${Date.now()}`;
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
        rbac:
          - apiGroups: ["sympozium.ai"]
            resources: ["agentruns"]
            verbs: ["get", "patch"]
        postRun:
          - name: approve-gate
            image: bitnami/kubectl:latest
            gate: true
            command: ["sh", "-c"]
            args:
              - |
                kubectl patch agentrun $AGENT_RUN_ID -n $AGENT_NAMESPACE --type=merge \\
                  -p '{"metadata":{"annotations":{"sympozium.ai/gate-verdict":"{\\"action\\":\\"approve\\",\\"reason\\":\\"cypress-test\\"}"}}}'
`;
  cy.writeFile(`cypress/tmp/${INSTANCE}.yaml`, manifest);
  cy.exec(`kubectl apply -f cypress/tmp/${INSTANCE}.yaml`);
}

describe("Response gate -- approve", () => {
  before(() => {
    applyInstance();
    // Wait for instance to reconcile.
    cy.wait(3000);
    cy.dispatchRun(INSTANCE, "Reply with exactly: GATE_APPROVE_SENTINEL").then(
      (name) => {
        RUN_NAME = name;
      },
    );
    cy.then(() => cy.waitForRunTerminal(RUN_NAME, 5 * 60 * 1000));
  });

  after(() => {
    if (RUN_NAME) cy.deleteRun(RUN_NAME);
    cy.deleteAgent(INSTANCE);
  });

  it("succeeds with gate verdict approved and shows banner in UI", () => {
    // Verify via API that the run succeeded with gate verdict.
    cy.request({
      url: `/api/v1/runs/${RUN_NAME}?namespace=default`,
      headers: {
        "Content-Type": "application/json",
        ...(Cypress.env("API_TOKEN")
          ? { Authorization: `Bearer ${Cypress.env("API_TOKEN")}` }
          : {}),
      },
    }).then((resp) => {
      expect(resp.body.status.phase).to.eq("Succeeded");
      expect(resp.body.status.gateVerdict).to.eq("approved");
      // Original result should be passed through.
      expect(resp.body.status.result).to.include("GATE_APPROVE_SENTINEL");
    });

    // Verify the gate verdict banner appears on the run detail page.
    cy.visit(`/runs/${RUN_NAME}`);
    cy.contains("Succeeded", { timeout: 20000 }).should("be.visible");
    cy.get("[data-testid='gate-verdict-banner']", { timeout: 10000 })
      .should("be.visible")
      .and("contain.text", "approved");
  });
});

export {};
