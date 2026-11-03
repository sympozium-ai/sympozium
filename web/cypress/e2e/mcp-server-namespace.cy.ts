/**
 * MCP servers live in their own namespace (Install Defaults puts them in
 * sympozium-system) and the list shows them across namespaces, so the row
 * actions must address the server's namespace, not the namespace selector's.
 * Intercepted browser contract tests — they need no cluster.
 */

const server = {
  metadata: {
    name: "github",
    namespace: "sympozium-system",
    creationTimestamp: "2026-10-30T10:00:00Z",
  },
  spec: { transportType: "stdio", toolsPrefix: "github", suspended: false },
  status: { ready: true, url: "http://github.sympozium-system.svc:8080" },
};

describe("MCP servers outside the selected namespace", () => {
  beforeEach(() => {
    cy.intercept("GET", "**/api/v1/**", { body: [] });
    cy.intercept("GET", "**/api/v1/namespaces*", { body: ["default", "sympozium-system"] });
    cy.intercept("GET", "**/api/v1/mcpservers?*", { body: [server] }).as("list");
    cy.intercept("GET", "**/api/v1/mcpservers", { body: [server] });
  });

  it("deletes the server in its own namespace", () => {
    cy.intercept("DELETE", "**/api/v1/mcpservers/github*", { statusCode: 204 }).as("delete");
    cy.visit("/mcp-servers#token=test-token");
    cy.contains("tr", "github").find("button").last().click();
    cy.wait("@delete").its("request.url").should("match", /\/mcpservers\/github\?namespace=sympozium-system$/);
  });

  it("suspends the server in its own namespace", () => {
    cy.intercept("PATCH", "**/api/v1/mcpservers/github*", {
      body: { ...server, spec: { ...server.spec, suspended: true } },
    }).as("patch");
    cy.visit("/mcp-servers#token=test-token");
    cy.contains("tr", "github").find('button[title="Suspend server"]').click();
    cy.wait("@patch").then(({ request }) => {
      expect(request.url).to.match(/\/mcpservers\/github\?namespace=sympozium-system$/);
      expect(request.body).to.deep.equal({ suspended: true });
    });
  });

  it("saves a token in the server's namespace", () => {
    cy.intercept("POST", "**/api/v1/mcpservers/github/auth/token*", {
      body: { status: "configured" },
    }).as("token");
    cy.visit("/mcp-servers#token=test-token");
    cy.contains("tr", "github").find('button[title="Configure token"]').click();
    cy.get("#token").type("ghp_test");
    cy.contains("button", "Save Token").click();
    cy.wait("@token").its("request.url").should("match", /\/auth\/token\?namespace=sympozium-system$/);
  });

  it("opens the detail page in the server's namespace", () => {
    cy.intercept("GET", "**/api/v1/mcpservers/github?*", { body: server }).as("get");
    cy.visit("/mcp-servers#token=test-token");
    cy.contains("tr", "github").find('a[href*="/mcp-servers/github"]').click();
    cy.wait("@get").its("request.url").should("match", /\/mcpservers\/github\?namespace=sympozium-system$/);
    cy.get("h1").should("have.text", "github");
  });
});

export {};
