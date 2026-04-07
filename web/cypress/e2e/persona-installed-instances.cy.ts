// PersonaPack detail — installed instances: verify that enabling a pack
// shows the stamped instances on the detail page with clickable links.

const PACK = `cy-ppinst-${Date.now()}`;
const PERSONA = "helper";
const STAMPED_INSTANCE = `${PACK}-${PERSONA}`;

describe("PersonaPack Detail — installed instances", () => {
  after(() => {
    cy.deletePersonaPack(PACK);
    cy.deleteInstance(STAMPED_INSTANCE);
  });

  it("shows the stamped instance on the persona pack detail page", () => {
    const manifest = `apiVersion: sympozium.ai/v1alpha1
kind: PersonaPack
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
  personas:
    - name: ${PERSONA}
      systemPrompt: You are a helper.
      model: qwen/qwen3.5-9b
`;
    cy.writeFile(`cypress/tmp/${PACK}.yaml`, manifest);
    cy.exec(`kubectl apply -f cypress/tmp/${PACK}.yaml`);

    // Wait for the instance to be stamped.
    cy.visit("/instances");
    cy.contains(STAMPED_INSTANCE, { timeout: 30000 }).should("be.visible");

    // Navigate to the persona pack detail page.
    cy.visit(`/personas/${PACK}`);

    // The "Installed Instances" section should show the stamped instance.
    cy.contains("Installed Instances", { timeout: 20000 }).should("be.visible");
    cy.contains(STAMPED_INSTANCE).should("be.visible");

    // Click the instance link — should navigate to instance detail.
    cy.contains("a", STAMPED_INSTANCE).click();
    cy.url().should("include", `/instances/${STAMPED_INSTANCE}`);
  });
});

export {};
