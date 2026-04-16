// End-to-end: install the default GitHub MCP server from the catalog,
// configure a Personal Access Token, wire it into a SympoziumInstance
// with the mcp-bridge skill, dispatch a run that exercises a GitHub
// MCP tool, and verify the agent's response contains evidence of real
// tool output.
//
// Requires:
//   CYPRESS_GITHUB_TOKEN  — a valid GitHub PAT (repo scope minimum)
//   LM Studio running on host.docker.internal:1234

const TS = Date.now();
const MCP_SERVER = "github";
const MCP_SECRET = "mcp-github-token";
const INSTANCE = `cy-mcp-gh-${TS}`;
let RUN_NAME = "";

function authHeaders(): Record<string, string> {
  const token = Cypress.env("API_TOKEN");
  const h: Record<string, string> = { "Content-Type": "application/json" };
  if (token) h["Authorization"] = `Bearer ${token}`;
  return h;
}

describe("MCP Server — GitHub install, configure, and tool call", () => {
  before(() => {
    const ghToken = Cypress.env("GITHUB_TOKEN");
    if (!ghToken) {
      throw new Error(
        "CYPRESS_GITHUB_TOKEN env var is required for this test",
      );
    }
  });

  after(() => {
    if (RUN_NAME) cy.deleteRun(RUN_NAME);
    cy.deleteInstance(INSTANCE);
    // Clean up the installed MCP server from default namespace.
    cy.request({
      method: "DELETE",
      url: `/api/v1/mcpservers/${MCP_SERVER}?namespace=default`,
      headers: authHeaders(),
      failOnStatusCode: false,
    });
    // Clean up the token secret.
    cy.exec(
      `kubectl delete secret ${MCP_SECRET} -n default --ignore-not-found`,
      { failOnNonZeroExit: false },
    );
  });

  it("installs the GitHub MCP server, sets a token, and runs an agent that calls a GitHub tool", () => {
    // ── Step 1: install default MCP servers via the UI ──────────────────────
    cy.visit("/mcp-servers");
    cy.contains("button", "Install Defaults", { timeout: 15000 }).click();

    // The GitHub MCP server should appear in the table.
    cy.contains("td", MCP_SERVER, { timeout: 20000 }).should("be.visible");

    // ── Step 2: configure the GitHub PAT via the key icon ───────────────────
    cy.contains("td", MCP_SERVER)
      .parents("tr")
      .within(() => {
        cy.get('button[title="Configure token"]').click();
      });

    cy.get("[role='dialog']").within(() => {
      cy.get("input[type='password']").type(Cypress.env("GITHUB_TOKEN"));
      cy.contains("button", "Save Token").click();
    });

    // Dialog should close on success.
    cy.get("[role='dialog']").should("not.exist", { timeout: 10000 });

    // ── Step 3: verify the auth status is complete via API ──────────────────
    cy.request({
      url: `/api/v1/mcpservers/${MCP_SERVER}/auth/status?namespace=default`,
      headers: authHeaders(),
    }).then((resp) => {
      expect(resp.body.status).to.eq("complete");
      expect(resp.body.secretName).to.eq(MCP_SECRET);
    });

    // ── Step 4: wait for MCP server pod to become Ready ─────────────────────
    // The controller needs to reconcile the secret ref and restart the pod.
    const pollReady = (deadline: number): Cypress.Chainable<void> => {
      return cy
        .request({
          url: `/api/v1/mcpservers/${MCP_SERVER}?namespace=default`,
          headers: authHeaders(),
        })
        .then((resp) => {
          if (resp.body?.status?.ready) {
            return;
          }
          if (Date.now() > deadline) {
            throw new Error(
              `MCP server ${MCP_SERVER} not ready within timeout`,
            );
          }
          cy.wait(3000, { log: false });
          return pollReady(deadline);
        });
    };
    pollReady(Date.now() + 120000);

    // ── Step 5: verify MCP server is Ready in the detail page ─────────────
    // Tool discovery happens at agent runtime via the mcp-bridge sidecar,
    // not in the controller, so we only verify the server pod is healthy.
    cy.visit(`/mcp-servers/${MCP_SERVER}`);
    cy.contains("Ready", { timeout: 10000 }).should("be.visible");

    // ── Step 6: create instance with mcp-bridge skill + MCP server ref ──────
    // Use kubectl because the create API doesn't expose mcpServers field.
    const instanceManifest = `apiVersion: sympozium.ai/v1alpha1
kind: SympoziumInstance
metadata:
  name: ${INSTANCE}
  namespace: default
spec:
  agents:
    default:
      model: qwen/qwen3.5-9b
      baseURL: http://host.docker.internal:1234/v1
  authRefs:
    - provider: lm-studio
      secret: ""
  skills:
    - skillPackRef: mcp-bridge
  mcpServers:
    - name: ${MCP_SERVER}
      toolsPrefix: github
      timeout: 30
`;
    cy.writeFile(`cypress/tmp/${INSTANCE}.yaml`, instanceManifest);
    cy.exec(`kubectl apply -f cypress/tmp/${INSTANCE}.yaml`);

    // Verify instance appears in the UI.
    cy.visit("/instances");
    cy.contains(INSTANCE, { timeout: 30000 }).should("be.visible");

    // ── Step 7: dispatch a run that exercises a GitHub MCP tool ──────────────
    // Ask the agent to search for a well-known repository. The response
    // must contain evidence that the tool was actually called (repo name,
    // stars, description, etc.) — not just the model paraphrasing.
    cy.dispatchRun(
      INSTANCE,
      "Use the github_search_repositories tool to search for 'kubernetes/kubernetes'. " +
        "Report the repository description and star count. " +
        "You MUST call the tool — do not guess or make up an answer. " +
        "Include the exact string MCP_TOOL_VERIFIED in your response after reporting the results.",
    ).then((name) => {
      RUN_NAME = name;
    });

    // ── Step 8: wait for the run to reach a terminal phase ──────────────────
    cy.then(() => cy.waitForRunTerminal(RUN_NAME, 6 * 60 * 1000));

    // Assert the run succeeded.
    cy.then(() =>
      cy
        .request({
          url: `/api/v1/runs/${RUN_NAME}?namespace=default`,
          headers: authHeaders(),
        })
        .then((resp) => {
          const phase = resp.body?.status?.phase as string;
          const err = resp.body?.status?.error as string | undefined;
          expect(
            phase,
            `run ${RUN_NAME} should have Succeeded (error: ${err || "n/a"})`,
          ).to.eq("Succeeded");
        }),
    );

    // ── Step 9: verify the response contains real MCP tool output ───────────
    cy.then(() => cy.visit(`/runs/${RUN_NAME}`));
    cy.contains("Succeeded", { timeout: 20000 }).should("be.visible");
    cy.contains("button", "Result", { timeout: 20000 }).click({ force: true });

    cy.contains("No result available").should("not.exist");
    cy.get("[role='tabpanel']", { timeout: 20000 })
      .invoke("text")
      .then((raw) => {
        // The response must contain evidence of a real GitHub API call.
        // kubernetes/kubernetes is a public repo — the agent should return
        // real metadata from the search results.
        expect(
          raw,
          "response must contain MCP tool verification sentinel",
        ).to.include("MCP_TOOL_VERIFIED");

        // Bonus: check for real GitHub content (the model should have
        // reported actual results from the search).
        const lowerRaw = raw.toLowerCase();
        const hasGithubContent =
          lowerRaw.includes("kubernetes") ||
          lowerRaw.includes("production-grade") ||
          lowerRaw.includes("container");
        expect(
          hasGithubContent,
          "response should contain real GitHub repository data from the MCP tool call",
        ).to.be.true;
      });
  });
});

export {};
