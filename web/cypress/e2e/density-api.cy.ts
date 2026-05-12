// Test: Density API endpoints — verifies all fitness-related API endpoints
// return correct response shapes and handle edge cases.
//
// These tests validate the API contract regardless of whether the llmfit
// DaemonSet is actively publishing data.

function authHeaders(): Record<string, string> {
  const token = Cypress.env("API_TOKEN");
  const h: Record<string, string> = { "Content-Type": "application/json" };
  if (token) h["Authorization"] = `Bearer ${token}`;
  return h;
}

describe("Density API Endpoints", () => {
  // ── GET /api/v1/density/nodes ─────────────────────────────────────────

  describe("GET /api/v1/density/nodes", () => {
    it("returns 200 with nodes array and total", () => {
      cy.request({
        url: "/api/v1/density/nodes",
        headers: authHeaders(),
      }).then((resp) => {
        expect(resp.status).to.eq(200);
        expect(resp.body).to.have.property("nodes").that.is.an("array");
        expect(resp.body).to.have.property("total").that.is.a("number");
        expect(resp.body.total).to.eq(resp.body.nodes.length);
      });
    });

    it("node objects have expected shape when data present", () => {
      cy.request({
        url: "/api/v1/density/nodes",
        headers: authHeaders(),
      }).then((resp) => {
        if (resp.body.total === 0) {
          cy.log("No nodes — skipping shape validation");
          return;
        }

        const node = resp.body.nodes[0];
        expect(node).to.have.property("nodeName").that.is.a("string");
        expect(node).to.have.property("lastSeen").that.is.a("string");
        expect(node).to.have.property("stale").that.is.a("boolean");
        expect(node).to.have.property("system").that.is.an("object");
        expect(node).to.have.property("modelFitCount").that.is.a("number");

        // System specs shape.
        const sys = node.system;
        expect(sys).to.have.property("total_ram_gb").that.is.a("number");
        expect(sys).to.have.property("cpu_cores").that.is.a("number");
        expect(sys).to.have.property("has_gpu").that.is.a("boolean");
      });
    });
  });

  // ── GET /api/v1/density/nodes/{name} ──────────────────────────────────

  describe("GET /api/v1/density/nodes/{name}", () => {
    it("returns 404 for non-existent node", () => {
      cy.request({
        url: "/api/v1/density/nodes/nonexistent-node-xyz",
        headers: authHeaders(),
        failOnStatusCode: false,
      }).then((resp) => {
        expect(resp.status).to.eq(404);
      });
    });

    it("returns node detail when node exists", () => {
      cy.request({
        url: "/api/v1/density/nodes",
        headers: authHeaders(),
      }).then((resp) => {
        if (resp.body.total === 0) {
          cy.log("No nodes — skipping detail test");
          return;
        }

        const nodeName = resp.body.nodes[0].nodeName;
        cy.request({
          url: `/api/v1/density/nodes/${nodeName}`,
          headers: authHeaders(),
        }).then((detailResp) => {
          expect(detailResp.status).to.eq(200);
          expect(detailResp.body).to.have.property("NodeName", nodeName);
          expect(detailResp.body).to.have.property("System");
          expect(detailResp.body).to.have.property("ModelFits").that.is.an("array");
        });
      });
    });
  });

  // ── GET /api/v1/density/runtimes ──────────────────────────────────────

  describe("GET /api/v1/density/runtimes", () => {
    it("returns 200 with nodes array", () => {
      cy.request({
        url: "/api/v1/density/runtimes",
        headers: authHeaders(),
      }).then((resp) => {
        expect(resp.status).to.eq(200);
        expect(resp.body).to.have.property("nodes").that.is.an("array");
      });
    });
  });

  // ── GET /api/v1/density/installed-models ───────────────────────────────

  describe("GET /api/v1/density/installed-models", () => {
    it("returns 200 with nodes array", () => {
      cy.request({
        url: "/api/v1/density/installed-models",
        headers: authHeaders(),
      }).then((resp) => {
        expect(resp.status).to.eq(200);
        expect(resp.body).to.have.property("nodes").that.is.an("array");
      });
    });
  });

  // ── GET /api/v1/density/query ─────────────────────────────────────────

  describe("GET /api/v1/density/query", () => {
    it("returns 400 when model parameter is missing", () => {
      cy.request({
        url: "/api/v1/density/query",
        headers: authHeaders(),
        failOnStatusCode: false,
      }).then((resp) => {
        expect(resp.status).to.eq(400);
      });
    });

    it("returns 200 with query and rankedNodes", () => {
      cy.request({
        url: "/api/v1/density/query?model=Qwen",
        headers: authHeaders(),
      }).then((resp) => {
        expect(resp.status).to.eq(200);
        expect(resp.body).to.have.property("query", "Qwen");
        expect(resp.body).to.have.property("rankedNodes").that.is.an("array");
      });
    });

    it("returns empty results for non-matching model", () => {
      cy.request({
        url: "/api/v1/density/query?model=nonexistent-model-xyz-12345",
        headers: authHeaders(),
      }).then((resp) => {
        expect(resp.status).to.eq(200);
        const nodes = resp.body.rankedNodes || [];
        expect(nodes).to.have.length(0);
      });
    });

    it("ranked nodes have correct shape when data present", () => {
      cy.request({
        url: "/api/v1/density/query?model=Qwen",
        headers: authHeaders(),
      }).then((resp) => {
        if (resp.body.rankedNodes.length === 0) {
          cy.log("No matches — skipping shape validation");
          return;
        }

        const result = resp.body.rankedNodes[0];
        expect(result).to.have.property("nodeName").that.is.a("string");
        expect(result).to.have.property("score").that.is.a("number");
        expect(result).to.have.property("fitLevel").that.is.a("string");
        expect(result).to.have.property("model").that.is.an("object");
        expect(result.model).to.have.property("name");
        expect(result.model).to.have.property("estimated_tps");
      });
    });

    it("results are sorted by score descending", () => {
      cy.request({
        url: "/api/v1/density/query?model=Qwen",
        headers: authHeaders(),
      }).then((resp) => {
        const nodes = resp.body.rankedNodes;
        for (let i = 1; i < nodes.length; i++) {
          expect(nodes[i - 1].score).to.be.at.least(nodes[i].score);
        }
      });
    });
  });

  // ── GET /api/v1/catalog ───────────────────────────────────────────────

  describe("GET /api/v1/catalog", () => {
    it("returns 200 with models array and total", () => {
      cy.request({
        url: "/api/v1/catalog",
        headers: authHeaders(),
      }).then((resp) => {
        expect(resp.status).to.eq(200);
        expect(resp.body).to.have.property("models").that.is.an("array");
        expect(resp.body).to.have.property("total").that.is.a("number");
      });
    });

    it("catalog entries have correct shape when data present", () => {
      cy.request({
        url: "/api/v1/catalog",
        headers: authHeaders(),
      }).then((resp) => {
        if (resp.body.total === 0) {
          cy.log("Empty catalog — skipping shape validation");
          return;
        }

        const entry = resp.body.models[0];
        expect(entry).to.have.property("modelName").that.is.a("string");
        expect(entry).to.have.property("bestScore").that.is.a("number");
        expect(entry).to.have.property("bestNode").that.is.a("string");
        expect(entry).to.have.property("fitLevel").that.is.a("string");
        expect(entry).to.have.property("nodes").that.is.an("array");
      });
    });

    it("catalog is sorted alphabetically by model name", () => {
      cy.request({
        url: "/api/v1/catalog",
        headers: authHeaders(),
      }).then((resp) => {
        const models = resp.body.models;
        for (let i = 1; i < models.length; i++) {
          expect(models[i - 1].modelName <= models[i].modelName).to.be.true;
        }
      });
    });
  });

  // ── POST /api/v1/density/simulate ─────────────────────────────────────

  describe("POST /api/v1/density/simulate", () => {
    it("returns 400 when model field is missing", () => {
      cy.request({
        method: "POST",
        url: "/api/v1/density/simulate",
        headers: authHeaders(),
        body: {},
        failOnStatusCode: false,
      }).then((resp) => {
        expect(resp.status).to.eq(400);
      });
    });

    it("returns 200 with simulation results", () => {
      cy.request({
        method: "POST",
        url: "/api/v1/density/simulate",
        headers: authHeaders(),
        body: { model: "Qwen2.5-7B" },
      }).then((resp) => {
        expect(resp.status).to.eq(200);
        expect(resp.body).to.have.property("model", "Qwen2.5-7B");
        const nodes = resp.body.rankedNodes || [];
        expect(nodes).to.be.an("array");
        expect(resp.body).to.have.property("canFitAnywhere").that.is.a("boolean");
      });
    });

    it("simulation results have memory fields when data present", () => {
      cy.request({
        method: "POST",
        url: "/api/v1/density/simulate",
        headers: authHeaders(),
        body: { model: "Qwen" },
      }).then((resp) => {
        if (resp.body.rankedNodes.length === 0) {
          cy.log("No simulation results — skipping shape validation");
          return;
        }

        const node = resp.body.rankedNodes[0];
        expect(node).to.have.property("nodeName");
        expect(node).to.have.property("currentScore").that.is.a("number");
        expect(node).to.have.property("fitLevel").that.is.a("string");
        expect(node).to.have.property("availableMemoryGb").that.is.a("number");
        expect(node).to.have.property("requiredMemoryGb").that.is.a("number");
        expect(node).to.have.property("remainingMemoryGb").that.is.a("number");
      });
    });

    it("accepts optional memoryGb override", () => {
      cy.request({
        method: "POST",
        url: "/api/v1/density/simulate",
        headers: authHeaders(),
        body: { model: "Qwen", memoryGb: 48 },
      }).then((resp) => {
        expect(resp.status).to.eq(200);
        // If results exist, the requiredMemoryGb should reflect the override.
        if (resp.body.rankedNodes.length > 0) {
          expect(resp.body.rankedNodes[0].requiredMemoryGb).to.eq(48);
        }
      });
    });
  });

  // ── GET /api/v1/density/cost ──────────────────────────────────────────

  describe("GET /api/v1/density/cost", () => {
    it("returns 200 with models and namespaces arrays", () => {
      cy.request({
        url: "/api/v1/density/cost",
        headers: authHeaders(),
      }).then((resp) => {
        expect(resp.status).to.eq(200);
        expect(resp.body).to.have.property("models").that.is.an("array");
        expect(resp.body).to.have.property("namespaces").that.is.an("array");
      });
    });

    it("model cost entries have expected fields", () => {
      cy.request({
        url: "/api/v1/density/cost",
        headers: authHeaders(),
      }).then((resp) => {
        if (resp.body.models.length === 0) {
          cy.log("No models deployed — skipping shape validation");
          return;
        }

        const model = resp.body.models[0];
        expect(model).to.have.property("name").that.is.a("string");
        expect(model).to.have.property("namespace").that.is.a("string");
        expect(model).to.have.property("phase").that.is.a("string");
        expect(model).to.have.property("gpu").that.is.a("number");
      });
    });

    it("namespace aggregation is correct", () => {
      cy.request({
        url: "/api/v1/density/cost",
        headers: authHeaders(),
      }).then((resp) => {
        if (resp.body.namespaces.length === 0) {
          cy.log("No namespaces — skipping aggregation test");
          return;
        }

        const ns = resp.body.namespaces[0];
        expect(ns).to.have.property("namespace").that.is.a("string");
        expect(ns).to.have.property("modelCount").that.is.a("number");
        expect(ns).to.have.property("totalGpu").that.is.a("number");
        expect(ns).to.have.property("totalMemoryGb").that.is.a("number");
      });
    });
  });
});

export {};
