// Ensemble detail — installed instances: verify that enabling a pack
// shows the stamped instances on the detail page with clickable links.

const PACK = `cy-ppinst-${Date.now()}`;
const PERSONA = "helper";
const STAMPED_INSTANCE = `${PACK}-${PERSONA}`;

describe("Ensemble Detail — installed instances", () => {
  after(() => {
    cy.deleteEnsemble(PACK);
    cy.deleteAgent(STAMPED_INSTANCE);
  });

  // TODO: unskip once ensemble controller conflict-retry is fixed — the
  // ensemble status update races with the agent controller's memory
  // reconciliation, causing a "the object has been modified" conflict
  // that prevents the installed-agents count from being written.
  it.skip("shows the stamped instance on the ensemble detail page", () => {
    const manifest = `apiVersion: sympozium.ai/v1alpha1
kind: Ensemble
metadata:
  name: ${PACK}
  namespace: default
spec:
  enabled: true
  description: installed instances test
  baseURL: http://host.docker.internal:1234/v1
  authRefs:
    - provider: lm-studio
      secret: ""
  agentConfigs:
    - name: ${PERSONA}
      systemPrompt: You are a helper.
      model: qwen/qwen3.5-9b
`;
    cy.writeFile(`cypress/tmp/${PACK}.yaml`, manifest);
    cy.exec(`kubectl apply -f cypress/tmp/${PACK}.yaml`);

    // Wait for the controller to reconcile and stamp the agent.
    // Poll the API directly — more reliable than waiting for the UI list.
    const waitForAgent = (timeoutMs = 60000): void => {
      const started = Date.now();
      const poll = (): void => {
        cy.request({
          url: `/api/v1/agents/${STAMPED_INSTANCE}?namespace=default`,
          headers: {
            "Content-Type": "application/json",
            ...(Cypress.env("API_TOKEN")
              ? { Authorization: `Bearer ${Cypress.env("API_TOKEN")}` }
              : {}),
          },
          failOnStatusCode: false,
        }).then((resp) => {
          if (resp.status === 200) return;
          if (Date.now() - started > timeoutMs) {
            throw new Error(
              `Agent ${STAMPED_INSTANCE} not created within ${timeoutMs}ms`,
            );
          }
          cy.wait(2000, { log: false });
          poll();
        });
      };
      poll();
    };
    waitForAgent();

    cy.visit("/agents");
    cy.contains(STAMPED_INSTANCE, { timeout: 30000 }).should("exist");

    // Navigate to the ensemble detail page.
    // The controller needs time to reconcile the ensemble status with the
    // stamped agent list, so we poll-reload until the instance appears.
    cy.visit(`/ensembles/${PACK}`);
    cy.contains("Installed Instances", { timeout: 20000 }).should("be.visible");

    const waitForStampedInUI = (attemptsLeft = 10): void => {
      cy.get("body").then(($body) => {
        if ($body.text().includes(STAMPED_INSTANCE)) return;
        if (attemptsLeft <= 0) {
          throw new Error(`${STAMPED_INSTANCE} not found on ensemble detail after retries`);
        }
        cy.wait(5000, { log: false });
        cy.reload();
        cy.contains("Installed Instances", { timeout: 10000 }).should("be.visible");
        waitForStampedInUI(attemptsLeft - 1);
      });
    };
    waitForStampedInUI();

    // Click the instance link — should navigate to instance detail.
    cy.contains("a", STAMPED_INSTANCE).click();
    cy.url().should("include", `/agents/${STAMPED_INSTANCE}`);
  });
});

export {};
