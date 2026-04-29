// Response gate: gate hook rejects the agent output. Verify the run
// succeeds but with a replacement response, gate verdict is "rejected",
// and the UI shows the red rejection banner.

const INSTANCE = `cy-gate-reject-${Date.now()}`;
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
          - name: reject-gate
            image: bitnami/kubectl:latest
            gate: true
            command: ["sh", "-c"]
            args:
              - |
                kubectl patch agentrun $AGENT_RUN_ID -n $AGENT_NAMESPACE --type=merge \\
                  -p '{"metadata":{"annotations":{"sympozium.ai/gate-verdict":"{\\"action\\":\\"reject\\",\\"response\\":\\"BLOCKED_BY_POLICY\\",\\"reason\\":\\"cypress-reject-test\\"}"}}}'
`;
  cy.writeFile(`cypress/tmp/${INSTANCE}.yaml`, manifest);
  cy.exec(`kubectl apply -f cypress/tmp/${INSTANCE}.yaml`);
}

describe("Response gate -- reject", () => {
  before(() => {
    applyInstance();
    cy.wait(3000);
    cy.dispatchRun(
      INSTANCE,
      "Reply with exactly: SECRET_DATA_THAT_SHOULD_BE_BLOCKED",
    ).then((name) => {
      RUN_NAME = name;
    });
    cy.then(() => cy.waitForRunTerminal(RUN_NAME, 5 * 60 * 1000));
  });

  after(() => {
    if (RUN_NAME) cy.deleteRun(RUN_NAME);
    cy.deleteAgent(INSTANCE);
  });

  it("replaces the result with the rejection message and shows banner", () => {
    // Verify via API that the run has the rejection result.
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
      expect(resp.body.status.gateVerdict).to.eq("rejected");
      // The original agent output should have been replaced.
      expect(resp.body.status.result).to.eq("BLOCKED_BY_POLICY");
      expect(resp.body.status.result).to.not.include(
        "SECRET_DATA_THAT_SHOULD_BE_BLOCKED",
      );
    });

    // Verify the rejection banner appears on the run detail page.
    cy.visit(`/runs/${RUN_NAME}`);
    cy.contains("Succeeded", { timeout: 20000 }).should("be.visible");
    cy.get("[data-testid='gate-verdict-banner']", { timeout: 10000 })
      .should("be.visible")
      .and("contain.text", "rejected");

    // The result tab should show the replacement message, not the original.
    cy.contains("button", "Result").click({ force: true });
    cy.get("[role='tabpanel']").should("contain.text", "BLOCKED_BY_POLICY");
  });
});

export {};
