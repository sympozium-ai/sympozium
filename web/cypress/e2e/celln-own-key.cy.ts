/**
 * A Celln Agent owns its model backend: its own ModelConnection — any provider
 * preset, local server or custom endpoint — with the Agent's own key (pasted,
 * or an existing Secret) and a model name. Celln holds no model; the
 * namespace's policy only guards the endpoint origin (allowed/denied host
 * globs, and an operator's exact approval for private addresses). Intercepted
 * browser contract tests — they need no cluster and are not live execution
 * evidence. The Secret's fixed key name is written by the API server
 * (internal/apiserver/server_modelconnections.go, covered by its Go tests);
 * here it is what the console tells the user and what it asks the Secret
 * listing for.
 */

const KEY = "sk-ant-cypress-never-shown-again";

const profile = {
  name: "celln-native-starter", revision: "v1", policy: "celln-fleet-starter", model: "", provider: "", endpoint: "", systemPrompt: "Keep replies brief.",
  backend: "native", wrapper: "celln-native", agent: "", tools: [{ name: "celln-starter-grep", revision: "v1" }], mediationOnly: true,
  ceilings: { leaseSeconds: 86400, maxTurns: 256, maxModelRequests: 1536, maxOutputTokens: 786432 },
  sessionDefaults: { leaseSeconds: 14400, maxTurns: 64, maxModelRequests: 384, maxOutputTokens: 196608 },
};

type Mediation = { enabled: boolean; allowedOrigins: string[]; deniedOrigins: string[]; policies: string[]; privateOrigins?: string[]; routes: never[]; pending: never[] };

/** The namespace's guardrail as the API answers it; by default every public origin is admitted. */
function mediation(overrides: Partial<Mediation> = {}): Mediation {
  return { enabled: true, allowedOrigins: [], deniedOrigins: [], policies: ["celln-fleet-starter"], routes: [], pending: [], ...overrides };
}

function stub(guardrail: Mediation = mediation()) {
  cy.intercept("GET", "**/api/v1/**", { body: [] });
  cy.intercept("GET", "**/api/v1/celln-platform/profiles*", { body: [profile] });
  cy.intercept("GET", "**/api/v1/celln-platform/mediation*", { body: guardrail }).as("mediation");
  cy.intercept("GET", "**/api/v1/capabilities*", { body: { celln: { available: true, state: "ready", reason: "fleet ready" } } });
  // The old fleet-backend API must never be consulted again.
  cy.intercept("**/api/v1/celln-platform/backends*", () => { throw new Error("the console asked for fleet backends"); });
}

const dialog = () => cy.get('[role="dialog"]').first();
const next = () => dialog().contains("button", "Next").click();

function openProviderStep(name = "mine") {
  cy.visit("/agents?create=1&kind=agent#token=test-token");
  dialog().find('input[placeholder="my-agent"]').type(name);
  next();
  cy.get('[data-testid="create-agent-execution-environment"]').contains("button", "Celln").click();
  next();
}

const chooseProvider = (value: string) => cy.get(`[data-testid="celln-provider-${value}"]`).click();
const typeModel = (model: string) => cy.get('[data-testid="celln-model-name"]').clear().type(model);

describe("Create Agent → Celln: the Agent's own model backend", () => {
  it("offers every provider preset, a local server and a custom endpoint, and never asks the fleet for a route", () => {
    stub();
    openProviderStep();
    cy.get('[data-testid="wizard-steps"] [data-step]').then(($steps) => {
      expect([...$steps].map((el) => el.getAttribute("data-step"))).to.deep.equal(["name", "plane", "provider", "apikey", "model", "confirm"]);
    });
    cy.get('[data-testid="celln-provider-grid"] button').should("have.length", 13);
    for (const label of ["OpenAI", "Anthropic", "OpenRouter", "DeepSeek", "Mistral", "Groq", "Google", "xAI", "Ollama", "llama-server", "LM Studio", "vLLM", "Custom"]) {
      cy.get('[data-testid="celln-provider-grid"]').contains("button", label).should("exist");
    }
    cy.get('[data-testid="celln-route-select"]').should("not.exist");
    dialog().should("not.contain", "declared").and("not.contain", "--celln-mediated-route").and("not.contain", "apply-routes").and("not.contain", "starter Agent").and("not.contain", "holds no key");
    dialog().contains("button", "Next").should("be.disabled");
    chooseProvider("deepseek");
    cy.get('[data-testid="celln-route-summary"]').should("contain", "https://api.deepseek.com").and("contain", "any model").and("contain", "celln-fleet-starter").and("contain", "its own key");
    cy.get('[data-testid="celln-endpoint"]').should("not.exist");
    dialog().contains("button", "Next").should("not.be.disabled");
  });

  it("says what the operator must run when mediated model access is disabled, and offers nothing in its place", () => {
    stub(mediation({ enabled: false }));
    openProviderStep();
    cy.get('[data-testid="celln-mediation-disabled"]').should("contain", "Mediated model access is not enabled").and("contain", "celln.mediation.enabled").and("contain", "sympozium install").and("not.contain", "--celln-mediated-route");
    cy.get('[data-testid="celln-mediation-disabled"] a').should("have.attr", "href").and("contain", "celln-mediated-model-access.md");
    cy.get('[data-testid="celln-provider-grid"]').should("not.exist");
    dialog().should("not.contain", "fleet backend").and("not.contain", "Add to the fleet");
    dialog().contains("button", "Next").should("be.disabled");
  });

  it("refuses an origin the policy denies, quoting its allowed and denied globs, and admits one it allows", () => {
    stub(mediation({ allowedOrigins: ["api.anthropic.com", "*.example.com"], deniedOrigins: ["*.openai.com"] }));
    openProviderStep();
    chooseProvider("openai");
    cy.get('[data-testid="celln-origin-denied"]')
      .should("contain", "celln-fleet-starter")
      .and("contain", "https://api.openai.com")
      .and("contain", "denied pattern *.openai.com")
      .and("contain", "api.anthropic.com, *.example.com")
      .and("contain", "allowedOrigins");
    cy.get('[data-testid="celln-route-summary"]').should("not.exist");
    dialog().contains("button", "Next").should("be.disabled");
    // Not denied, but outside a declared allow-list.
    chooseProvider("deepseek");
    cy.get('[data-testid="celln-origin-denied"]').should("contain", "https://api.deepseek.com").and("not.contain", "denied pattern");
    dialog().contains("button", "Next").should("be.disabled");
    // A "*." glob admits the bare domain and its subdomains.
    chooseProvider("custom");
    cy.get('[data-testid="celln-endpoint"]').type("https://llm.example.com/v1/chat/completions");
    cy.get('[data-testid="celln-origin-denied"]').should("not.exist");
    cy.get('[data-testid="celln-route-summary"]').should("contain", "https://llm.example.com");
    chooseProvider("anthropic");
    cy.get('[data-testid="celln-origin-denied"]').should("not.exist");
    cy.get('[data-testid="celln-route-summary"]').should("contain", "https://api.anthropic.com");
    dialog().contains("button", "Next").should("not.be.disabled");
  });

  it("holds a private, LAN or loopback origin until an operator approves exactly it in modelGateway.privateOrigins", () => {
    stub(mediation({ privateOrigins: ["http://llama-server.default.svc:8080"] }));
    openProviderStep();
    chooseProvider("ollama");
    cy.get('[data-testid="celln-endpoint"]').should("have.value", "");
    dialog().contains("button", "Next").should("be.disabled");
    cy.get('[data-testid="celln-endpoint"]').type("not a url");
    cy.get('[data-testid="celln-endpoint-error"]').should("exist");
    cy.get('[data-testid="celln-endpoint"]').clear().type("http://ollama.default.svc:11434");
    cy.get('[data-testid="celln-origin-needs-approval"]')
      .should("contain", "http://ollama.default.svc:11434")
      .and("contain", "modelGateway.privateOrigins")
      .and("contain", "operator")
      .and("contain", "http://llama-server.default.svc:8080");
    dialog().contains("button", "Next").should("be.disabled");
    for (const origin of ["http://192.168.1.20:1234", "http://127.0.0.1:8080", "http://framework:8080", "http://10.0.0.5"]) {
      cy.get('[data-testid="celln-endpoint"]').clear().type(origin);
      cy.get('[data-testid="celln-origin-needs-approval"]').should("contain", origin);
    }
    // The approved origin, exactly: the key may travel there.
    cy.get('[data-testid="celln-endpoint"]').clear().type("http://llama-server.default.svc:8080/v1/chat/completions");
    cy.get('[data-testid="celln-origin-needs-approval"]').should("not.exist");
    cy.get('[data-testid="celln-route-summary"]').should("contain", "http://llama-server.default.svc:8080");
    dialog().contains("button", "Next").should("not.be.disabled");
  });

  it("creates a keyless local model connection without reading or writing a Secret", () => {
    stub(mediation({ privateOrigins: ["http://framework:8080"] }));
    cy.intercept("GET", "**/api/v1/celln-platform/key-secrets*", () => { throw new Error("keyless endpoint listed Secrets"); });
    cy.intercept("POST", "**/api/v1/model-connections*", (request) => {
      expect(request.body).not.to.have.property("apiKey");
      expect(request.body.spec).to.deep.equal({ provider: "llama-server", protocol: "openai-chat", endpoint: "http://framework:8080/v1/chat/completions", models: ["qwen3"], allowInsecure: true });
      request.reply({ statusCode: 201, body: { metadata: { name: "mine-connection" }, spec: request.body.spec } });
    }).as("connection");
    cy.intercept("POST", "**/api/v1/celln-platform/wrappers*", { body: { runtime: "celln-native", created: [] } });
    cy.intercept("POST", "**/api/v1/agents*", (request) => {
      expect(request.body).not.to.have.any.keys("apiKey", "secretName");
      expect(request.body.execution.modelConnectionRef).to.equal("mine-connection");
      request.reply({ statusCode: 201, body: { metadata: { name: "mine" }, spec: {} } });
    }).as("agent");
    openProviderStep();
    chooseProvider("llama-server");
    cy.get('[data-testid="celln-endpoint"]').type("http://framework:8080");
    cy.get('[data-testid="celln-keyed"]').should("not.be.checked");
    cy.get('[data-testid="celln-route-summary"]').should("contain", "No key is required");
    next();
    cy.get('[data-testid="celln-keyless-route"]').should("contain", "No API key is required");
    cy.get('[data-testid="celln-api-key"]').should("not.exist");
    next();
    cy.get('[data-testid="celln-origin"]').should("have.text", "http://framework:8080");
    cy.get('[data-testid="celln-endpoint-path"]').should("have.value", "/v1/chat/completions");
    typeModel("qwen3");
    next();
    cy.get('[data-testid="celln-own-key-confirmation"]').should("contain", "keyless endpoint").and("contain", "allowInsecure");
    dialog().contains("button", "YAML").click();
    cy.get('[role="dialog"]').last().should("not.contain", "secretRef:").and("not.contain", "authRefs:").and("contain", "allowInsecure: true");
    cy.get("body").type("{esc}");
    dialog().contains("button", /Create\s*$/).click();
    cy.wait(["@connection", "@agent"]);
    cy.get('[role="dialog"]').should("not.exist");
  });

  it("creates the Secret, the connection, the runtime wrapper and the Agent from a pasted key, and never shows the key again", () => {
    stub();
    const calls: string[] = [];
    cy.intercept("POST", "**/api/v1/model-connections*", (request) => {
      calls.push("connection");
      expect(request.url).to.contain("namespace=default");
      expect(request.body).to.deep.equal({
        name: "mine-connection",
        spec: { provider: "anthropic", protocol: "anthropic-messages", endpoint: "https://api.anthropic.com/v1/messages", models: ["claude-sonnet-5"] },
        apiKey: KEY,
      });
      // As the API answers: the Secret's name, never its value.
      request.reply({ statusCode: 201, body: { metadata: { name: "mine-connection", namespace: "default" }, spec: { ...request.body.spec, secretRef: "mine-connection-anthropic-key" } } });
    }).as("connection");
    cy.intercept("POST", "**/api/v1/celln-platform/wrappers*", (request) => {
      calls.push("runtime");
      // The namespace's offered profile, runtime only: no shared Agent or connection.
      expect(request.body).to.deep.equal({ profile: "celln-native-starter", runtimeOnly: true });
      request.reply({ body: { backend: "native", runtime: "celln-native", agent: "", connection: "", created: ["celln-native"] } });
    }).as("runtime");
    cy.intercept("POST", "**/api/v1/agents*", (request) => {
      calls.push("agent");
      expect(JSON.stringify(request.body)).not.to.contain(KEY);
      expect(request.body).to.deep.include({ name: "mine", provider: "anthropic", model: "claude-sonnet-5", runtimeRef: "celln-native", skills: [], channels: [] });
      expect(request.body).not.to.have.any.keys("apiKey", "secretName", "baseURL");
      expect(request.body.execution).to.deep.equal({
        backend: "celln", executionLifecycle: "enduring", modelConnectionRef: "mine-connection", model: "claude-sonnet-5",
        cellnSelection: { runtimeRef: "celln-native", toolRefs: [] },
        enduring: profile.sessionDefaults,
      });
      request.reply({ statusCode: 201, body: { metadata: { name: "mine", namespace: "default" }, spec: {} } });
    }).as("agent");

    openProviderStep();
    chooseProvider("anthropic");
    cy.get('[data-testid="celln-route-summary"]').should("contain", "anthropic-messages").and("contain", "https://api.anthropic.com").and("contain", "pinned to this origin");
    next();

    // Auth: the key name is fixed by the protocol and the Secret lives here.
    cy.get('[data-testid="celln-key-mode-create"]').should("have.attr", "aria-pressed", "true");
    dialog().contains("button", "Next").should("be.disabled");
    cy.get('[data-testid="celln-key-step"]').should("contain", "ANTHROPIC_API_KEY").and("contain", "mine-connection-anthropic-key").and("contain", "default");
    cy.get('[data-testid="celln-api-key"]').should("have.attr", "type", "password").type(KEY, { log: false });
    next();

    // Model: any model name, prefilled with a well-known one; the origin is fixed.
    cy.get('[data-testid="celln-models"]').should("not.exist");
    cy.get('[data-testid="celln-model-name"]').should("have.value", "claude-sonnet-5-5");
    typeModel("claude-sonnet-5");
    cy.get('[data-testid="celln-origin"]').should("have.text", "https://api.anthropic.com");
    cy.get('[data-testid="celln-origin-select"]').should("not.exist");
    cy.get('[data-testid="celln-endpoint-path"]').should("have.value", "/v1/messages");
    // Hosted Anthropic takes no chat-template switch.
    cy.get('[data-testid="celln-model-parameters"] summary').click();
    cy.get('[data-testid="celln-disable-thinking"]').should("not.exist");
    next();

    cy.get('[data-testid="celln-own-key-confirmation"]')
      .should("contain", "anthropic (anthropic-messages)")
      .and("contain", "mine-connection-anthropic-key").and("contain", "ANTHROPIC_API_KEY")
      .and("contain", "https://api.anthropic.com/v1/messages")
      .and("contain", "celln-native").and("contain", "celln-native-starter")
      .and("contain", "One conversation")
      .and("not.contain", "declared in policy");
    cy.get('[data-testid="celln-confirm-origin"]').should("have.text", "https://api.anthropic.com");
    // The YAML preview names the Secret and never carries the key.
    dialog().contains("button", "YAML").click();
    cy.get('[role="dialog"]').last().should("contain", "secretRef: mine-connection-anthropic-key").and("contain", "authRefs").and("contain", "modelConnectionRef: mine-connection").and("not.contain", KEY).and("not.contain", "credentialProfile");
    cy.get("body").type("{esc}");
    dialog().contains("button", /Create\s*$/).click();

    cy.wait(["@connection", "@runtime", "@agent"]).then(() => expect(calls).to.deep.equal(["connection", "runtime", "agent"]));
    cy.get('[role="dialog"]').should("not.exist");
    // After submission the key is nowhere: not in the page, not in storage.
    cy.document().then((doc) => expect(doc.documentElement.outerHTML).not.to.contain(KEY));
    cy.window().then((win) => {
      for (const store of [win.localStorage, win.sessionStorage]) {
        for (let i = 0; i < store.length; i++) expect(store.getItem(store.key(i)!) || "").not.to.contain(KEY);
      }
    });
  });

  it("validates a typed model name on a hosted preset, with the origin still fixed", () => {
    stub();
    cy.intercept("POST", "**/api/v1/model-connections*", (request) => {
      expect(request.body.spec).to.deep.equal({ provider: "openai", protocol: "openai-chat", endpoint: "https://api.openai.com/v1/chat/completions", models: ["gpt-new-2026"] });
      request.reply({ statusCode: 201, body: { metadata: { name: "mine-connection", namespace: "default" }, spec: { ...request.body.spec, secretRef: "mine-connection-openai-key" } } });
    }).as("connection");
    cy.intercept("POST", "**/api/v1/celln-platform/wrappers*", { body: { runtime: "celln-native", created: [] } });
    cy.intercept("POST", "**/api/v1/agents*", (request) => {
      expect(request.body.execution.model).to.equal("gpt-new-2026");
      request.reply({ statusCode: 201, body: { metadata: { name: "mine" }, spec: {} } });
    }).as("agent");
    openProviderStep();
    chooseProvider("openai");
    next();
    cy.get('[data-testid="celln-key-step"]').should("contain", "OPENAI_API_KEY");
    cy.get('[data-testid="celln-api-key"]').type(KEY, { log: false });
    next();
    cy.get('[data-testid="celln-model-name"]').should("have.value", "gpt-4o");
    dialog().contains("button", "Next").should("not.be.disabled");
    for (const bad of ["gpt 4o", "*"]) {
      typeModel(bad);
      dialog().contains("button", "Next").should("be.disabled");
    }
    cy.get('[data-testid="celln-model-name"]').clear();
    dialog().contains("button", "Next").should("be.disabled");
    typeModel("gpt-new-2026");
    cy.get('[data-testid="celln-model-name-error"]').should("not.exist");
    cy.get('[data-testid="celln-origin"]').should("have.text", "https://api.openai.com");
    next();
    dialog().contains("button", /Create\s*$/).click();
    cy.wait(["@connection", "@agent"]);
  });

  it("links an existing Secret that already holds the protocol's key, with a kubectl prompt and a refresh", () => {
    stub();
    let listed = 0;
    cy.intercept("GET", "**/api/v1/celln-platform/key-secrets*", (request) => {
      listed++;
      expect(request.url).to.contain("key=ANTHROPIC_API_KEY").and.contain("namespace=default");
      // Names and the key name only, as the API answers.
      request.reply({ body: listed === 1 ? [] : [{ name: "team-anthropic", key: "ANTHROPIC_API_KEY" }, { name: "other-connection-anthropic-key", key: "ANTHROPIC_API_KEY", managed: true }] });
    }).as("secrets");
    cy.intercept("POST", "**/api/v1/model-connections*", (request) => {
      expect(request.body).to.deep.equal({
        name: "mine-connection",
        spec: { provider: "anthropic", protocol: "anthropic-messages", endpoint: "https://api.anthropic.com/v1/messages", models: ["claude-opus-5"], secretRef: "team-anthropic" },
      });
      request.reply({ statusCode: 201, body: { metadata: { name: "mine-connection" }, spec: request.body.spec } });
    }).as("connection");
    cy.intercept("POST", "**/api/v1/celln-platform/wrappers*", { body: { backend: "native", runtime: "celln-native", agent: "", connection: "", created: [] } }).as("runtime");
    cy.intercept("POST", "**/api/v1/agents*", { statusCode: 201, body: { metadata: { name: "mine" }, spec: {} } }).as("agent");

    openProviderStep();
    chooseProvider("anthropic");
    next();
    cy.get('[data-testid="celln-key-mode-existing"]').click();
    cy.wait("@secrets");
    cy.get('[data-testid="celln-key-none"]').should("contain", "ANTHROPIC_API_KEY");
    cy.get('[data-testid="celln-key-step"]').should("contain", "exactly ANTHROPIC_API_KEY");
    cy.get('[data-testid="celln-key-command"]').should("have.text", "kubectl -n default create secret generic <name> --from-literal=ANTHROPIC_API_KEY=<your key>");
    dialog().contains("button", "Next").should("be.disabled");
    // Created out of band; refresh finds it.
    cy.get('[data-testid="celln-key-refresh"]').click();
    cy.wait("@secrets");
    cy.get('[data-testid="celln-key-secrets"] button').should("have.length", 2);
    cy.get('[data-testid="celln-key-secrets"] [data-secret="other-connection-anthropic-key"]').should("contain", "created by the console");
    cy.get('[data-testid="celln-key-secrets"] [data-secret="team-anthropic"]').click();
    next();
    typeModel("claude-opus-5");
    next();
    cy.get('[data-testid="celln-own-key-confirmation"]').should("contain", "existing Secret team-anthropic").and("contain", "pinned to https://api.anthropic.com");
    dialog().contains("button", /Create\s*$/).click();
    cy.wait(["@connection", "@runtime", "@agent"]);
    cy.get('[role="dialog"]').should("not.exist");
  });

  it("takes a custom endpoint with its protocol and key, keeps its path, and writes the advanced parameters to this Agent's connection", () => {
    stub();
    cy.intercept("POST", "**/api/v1/model-connections*", (request) => {
      expect(request.body.spec).to.deep.equal({
        provider: "custom", protocol: "openai-chat", endpoint: "https://llm-eu.example.com/openai/v1/chat/completions", models: ["qwen3"],
        parameters: { chat_template_kwargs: { enable_thinking: false } }, maxOutputTokens: 2048,
      });
      expect(request.body.apiKey).to.equal(KEY);
      request.reply({ statusCode: 201, body: { metadata: { name: "mine-connection" }, spec: { ...request.body.spec, secretRef: "mine-connection-custom-key" } } });
    }).as("connection");
    cy.intercept("POST", "**/api/v1/celln-platform/wrappers*", { body: { backend: "native", runtime: "celln-native", agent: "", connection: "", created: [] } });
    cy.intercept("POST", "**/api/v1/agents*", (request) => {
      // A turn reserves 6 requests of 2048 tokens, so the ceilings pay for 64 turns of 12288.
      expect(request.body.execution.enduring).to.deep.equal({ leaseSeconds: 14400, maxTurns: 64, maxModelRequests: 384, maxOutputTokens: 786432 });
      request.reply({ statusCode: 201, body: { metadata: { name: "mine" }, spec: {} } });
    }).as("agent");

    openProviderStep();
    chooseProvider("custom");
    cy.get('[data-testid="celln-protocol"]').should("contain", "OpenAI-compatible");
    cy.get('[data-testid="celln-endpoint"]').type("https://llm-eu.example.com/openai/v1/chat/completions");
    cy.get('[data-testid="celln-keyed"]').check();
    cy.get('[data-testid="celln-route-summary"]').should("contain", "https://llm-eu.example.com").and("contain", "its own key");
    next();
    // An OpenAI-protocol endpoint fixes OPENAI_API_KEY, whatever the provider is called.
    cy.get('[data-testid="celln-key-step"]').should("contain", "OPENAI_API_KEY").and("not.contain", "ANTHROPIC_API_KEY").and("contain", "mine-connection-custom-key");
    cy.get('[data-testid="celln-api-key"]').type(KEY, { log: false });
    next();
    // The typed path is kept; the origin is the one admitted on the Provider step.
    cy.get('[data-testid="celln-origin"]').should("have.text", "https://llm-eu.example.com");
    cy.get('[data-testid="celln-endpoint-path"]').should("have.value", "/openai/v1/chat/completions");
    typeModel("qwen3");

    cy.get('[data-testid="celln-model-parameters"] summary').click();
    cy.get('[data-testid="celln-model-parameters"]').should("contain", "this Agent").and("contain", "model gateway").and("not.contain", "fleet backend").and("not.contain", "cannot be changed");
    cy.get('[data-testid="celln-disable-thinking"]').check();
    cy.get('[data-testid="celln-model-parameters-json"]').should("contain.value", '"enable_thinking": false');
    // Invalid values stop here, before anything is posted.
    cy.get('[data-testid="celln-max-output-tokens"]').type("9000");
    cy.get('[data-testid="celln-max-output-tokens-error"]').should("be.visible");
    dialog().contains("button", "Next").should("be.disabled");
    cy.get('[data-testid="celln-max-output-tokens"]').clear().type("2048");
    cy.get('[data-testid="celln-turn-reservation"]').should("have.text", "one turn reserves 12288 tokens (6 requests)");
    next();
    cy.get('[data-testid="celln-own-key-confirmation"]').should("contain", "up to 2048 output tokens per request").and("contain", "with model parameters");
    dialog().contains("button", /Create\s*$/).click();
    cy.wait(["@connection", "@agent"]);
  });

  it("offers the Anthropic protocol on a custom endpoint and fixes ANTHROPIC_API_KEY for it", () => {
    stub();
    cy.intercept("POST", "**/api/v1/model-connections*", (request) => {
      expect(request.body.spec).to.deep.equal({ provider: "custom", protocol: "anthropic-messages", endpoint: "https://claude-proxy.example.com/v1/messages", models: ["claude-opus-5"] });
      request.reply({ statusCode: 201, body: { metadata: { name: "mine-connection" }, spec: { ...request.body.spec, secretRef: "mine-connection-custom-key" } } });
    }).as("connection");
    cy.intercept("POST", "**/api/v1/celln-platform/wrappers*", { body: { runtime: "celln-native", created: [] } });
    cy.intercept("POST", "**/api/v1/agents*", { statusCode: 201, body: { metadata: { name: "mine" }, spec: {} } }).as("agent");
    openProviderStep();
    chooseProvider("custom");
    cy.get('[data-testid="celln-endpoint"]').type("https://claude-proxy.example.com");
    cy.get('[data-testid="celln-protocol"]').click();
    cy.get('[role="option"]').contains("Anthropic-compatible").click();
    cy.get('[data-testid="celln-keyed"]').check();
    cy.get('[data-testid="celln-route-summary"]').should("contain", "anthropic-messages");
    next();
    cy.get('[data-testid="celln-key-step"]').should("contain", "ANTHROPIC_API_KEY").and("not.contain", "OPENAI_API_KEY");
    cy.get('[data-testid="celln-api-key"]').type(KEY, { log: false });
    next();
    cy.get('[data-testid="celln-endpoint-path"]').should("have.value", "/v1/messages");
    typeModel("claude-opus-5");
    next();
    dialog().contains("button", /Create\s*$/).click();
    cy.wait(["@connection", "@agent"]);
  });

  it("says exactly what was created when a later step fails, and a retry does not send the key again", () => {
    stub();
    const bodies: Record<string, unknown>[] = [];
    cy.intercept("POST", "**/api/v1/model-connections*", (request) => {
      bodies.push(request.body);
      request.reply({ statusCode: 201, body: { metadata: { name: "mine-connection" }, spec: { ...request.body.spec, secretRef: "mine-connection-anthropic-key" } } });
    }).as("connection");
    let wrapperAttempts = 0;
    cy.intercept("POST", "**/api/v1/celln-platform/wrappers*", (request) => {
      wrapperAttempts++;
      if (wrapperAttempts === 1) request.reply({ statusCode: 403, body: 'no execution policy admits profile "celln-native-starter" for namespace "default"' });
      else request.reply({ body: { backend: "native", runtime: "celln-native", agent: "", connection: "", created: ["celln-native"] } });
    }).as("runtime");
    cy.intercept("POST", "**/api/v1/agents*", { statusCode: 500, body: 'agents.sympozium.ai "mine" already exists' }).as("agent");

    openProviderStep();
    chooseProvider("anthropic");
    next();
    cy.get('[data-testid="celln-api-key"]').type(KEY, { log: false });
    next();
    typeModel("claude-opus-5");
    next();
    dialog().contains("button", /Create\s*$/).click();
    cy.wait("@runtime");

    const state = (step: string) => cy.get(`[data-testid="celln-own-key-progress"] [data-step="${step}"]`);
    cy.get('[data-testid="celln-own-key-progress"]').should("contain", "no execution policy admits").and("contain", "safe to repeat");
    state("secret").should("have.attr", "data-state", "done").and("contain", "Secret mine-connection-anthropic-key");
    state("connection").should("have.attr", "data-state", "done").and("contain", "ModelConnection mine-connection");
    state("runtime").should("have.attr", "data-state", "failed").and("contain", "AgentRuntime celln-native");
    state("agent").should("have.attr", "data-state", "not-started").and("contain", "not created");
    cy.get('[data-testid="celln-own-key-progress"]').should("not.contain", KEY);
    cy.get("@agent.all").should("have.length", 0);

    // Second attempt: the wrapper now succeeds and the Agent step fails.
    dialog().contains("button", /Create\s*$/).click();
    cy.wait("@agent");
    state("runtime").should("have.attr", "data-state", "done");
    state("agent").should("have.attr", "data-state", "failed");
    cy.get('[data-testid="celln-own-key-progress"]').should("contain", "already exists");
    cy.get('[role="dialog"]').should("exist");
    cy.then(() => {
      expect(bodies).to.have.length(2);
      expect(bodies[0]).to.have.property("apiKey", KEY);
      // The key was written the first time; the retry links the Secret by name.
      expect(bodies[1]).not.to.have.property("apiKey");
      expect(bodies[1].spec).to.have.property("secretRef", "mine-connection-anthropic-key");
    });
    cy.document().then((doc) => expect(doc.documentElement.outerHTML).not.to.contain(KEY));
  });

  it("reports a connection the API refused before writing anything", () => {
    stub();
    cy.intercept("POST", "**/api/v1/model-connections*", { statusCode: 400, body: "endpoint must be an HTTP(S) API URL without credentials, query or fragment" }).as("connection");
    cy.intercept("POST", "**/api/v1/celln-platform/wrappers*", () => { throw new Error("the runtime step ran after a refused connection"); });
    openProviderStep();
    chooseProvider("anthropic");
    next();
    cy.get('[data-testid="celln-api-key"]').type(KEY, { log: false });
    next();
    typeModel("claude-opus-5");
    next();
    dialog().contains("button", /Create\s*$/).click();
    cy.wait("@connection");
    cy.get('[data-testid="celln-own-key-progress"] [data-step="secret"]').should("have.attr", "data-state", "not-started");
    cy.get('[data-testid="celln-own-key-progress"] [data-step="connection"]').should("have.attr", "data-state", "failed");
    cy.get('[data-testid="celln-own-key-progress"] [data-step="agent"]').should("have.attr", "data-state", "not-started");
  });

  it("shows the API's own words when a Secret is already pinned to another origin (409)", () => {
    stub();
    const refusal = 'Secret "team-anthropic" is pinned to origin https://api.openai.com and cannot be moved to https://api.anthropic.com; a key used elsewhere needs a new key entered by a person';
    cy.intercept("GET", "**/api/v1/celln-platform/key-secrets*", { body: [{ name: "team-anthropic", key: "ANTHROPIC_API_KEY" }] });
    cy.intercept("POST", "**/api/v1/model-connections*", { statusCode: 409, body: refusal }).as("connection");
    cy.intercept("POST", "**/api/v1/celln-platform/wrappers*", () => { throw new Error("the runtime step ran after a refused connection"); });
    openProviderStep();
    chooseProvider("anthropic");
    next();
    cy.get('[data-testid="celln-key-mode-existing"]').click();
    cy.get('[data-testid="celln-key-secrets"] [data-secret="team-anthropic"]').click();
    next();
    typeModel("claude-opus-5");
    next();
    dialog().contains("button", /Create\s*$/).click();
    cy.wait("@connection");
    cy.get('[data-testid="celln-own-key-progress"]').should("contain", refusal);
    cy.get('[data-testid="celln-own-key-progress"] [data-step="connection"]').should("have.attr", "data-state", "failed");
  });
});

describe("Agent → Harness: the Agent's own model backend", () => {
  const agent = {
    metadata: { name: "mine", namespace: "default" },
    spec: {
      runtimeRef: "celln-native", agents: { default: { model: "claude-sonnet-5" } }, authRefs: [{ provider: "anthropic", secret: "mine-connection-anthropic-key" }],
      execution: { backend: "celln", executionLifecycle: "enduring", modelConnectionRef: "mine-connection", model: "claude-sonnet-5", cellnSelection: { runtimeRef: "celln-native", toolRefs: [] }, enduring: profile.sessionDefaults },
    },
    status: { phase: "Running" },
  };
  const connection = { metadata: { name: "mine-connection", namespace: "default" }, spec: { provider: "anthropic", protocol: "anthropic-messages", endpoint: "https://api.anthropic.com/v1/messages", secretRef: "mine-connection-anthropic-key", models: ["claude-sonnet-5"], maxOutputTokens: 1024 } };
  const runtime = { metadata: { name: "celln-native", namespace: "default" }, spec: { cellnProfileRef: { name: profile.name, revision: "v1" }, supportOwner: "celln-platform" } };

  function open(guardrail: Mediation = mediation()) {
    stub(guardrail);
    cy.intercept("GET", "**/api/v1/agents/mine*", { body: agent });
    cy.intercept("GET", "**/api/v1/agents?*", { body: [agent] });
    cy.intercept("GET", "**/api/v1/runtimes*", { body: [runtime] });
    cy.intercept("GET", "**/api/v1/model-connections*", { body: [connection] });
    cy.visit("/agents/mine?tab=harness#token=test-token");
  }

  it("shows the provider, model, origin, Secret name and limits, and no fleet-backend controls", () => {
    open();
    cy.get('[data-testid="agent-model-backend"]').should("contain", "anthropic").and("contain", "claude-sonnet-5").and("contain", "https://api.anthropic.com").and("contain", "ANTHROPIC_API_KEY").and("contain", "pinned to this origin").and("contain", "1024 per request");
    cy.get('[data-testid="agent-model-backend-secret"]').should("have.text", "mine-connection-anthropic-key");
    cy.get('[data-testid="agent-model-backend-origin-denied"]').should("not.exist");
    cy.get('[data-testid="agent-model-backend-origin-needs-approval"]').should("not.exist");
    cy.get('[data-testid="agent-backend-picker"]').should("not.exist");
    cy.get('[data-testid="celln-add-backend"]').should("not.exist");
    cy.get('[data-testid="agent-execution-defaults"]').should("not.contain", "Add a fleet backend").and("not.contain", "Add to the fleet").and("not.contain", "declared");
  });

  it("warns when the policy no longer admits the connection's origin", () => {
    open(mediation({ deniedOrigins: ["*.anthropic.com"] }));
    cy.get('[data-testid="agent-model-backend-origin-denied"]').should("contain", "AUTH_ROUTE_MISMATCH").and("contain", "https://api.anthropic.com").and("contain", "*.anthropic.com").and("contain", "celln-fleet-starter");
  });

  it("edits the parameters and output tokens of this Agent's connection and says a new conversation is needed", () => {
    open();
    cy.intercept("POST", "**/api/v1/model-connections*", (request) => {
      expect(request.body).to.deep.equal({
        name: "mine-connection",
        spec: { provider: "anthropic", protocol: "anthropic-messages", endpoint: "https://api.anthropic.com/v1/messages", models: ["claude-sonnet-5"], secretRef: "mine-connection-anthropic-key", parameters: { temperature: 0.2 }, maxOutputTokens: 4096 },
      });
      request.reply({ body: { ...connection, spec: request.body.spec } });
    }).as("save");
    cy.intercept("PATCH", "**/api/v1/agents/mine*", (request) => {
      // 6 × 4096 per turn: the ceilings pay for 32 turns.
      expect(request.body.execution).to.deep.include({ modelConnectionRef: "mine-connection", model: "claude-sonnet-5" });
      expect(request.body.execution.enduring).to.deep.equal({ leaseSeconds: 14400, maxTurns: 32, maxModelRequests: 192, maxOutputTokens: 786432 });
      request.reply({ body: agent });
    }).as("patch");
    cy.get('[data-testid="agent-model-backend-edit"]').click();
    cy.get('[data-testid="agent-model-backend-route-changed"]').should("contain", "MODEL_ROUTE_CHANGED").and("contain", "start a new conversation").and("contain", "pinned to the origin");
    cy.get('[data-testid="celln-model-parameters"] summary').click();
    cy.get('[data-testid="celln-max-output-tokens"]').should("have.value", "1024").clear().type("4096");
    cy.get('[data-testid="celln-model-parameters-json"]').type('{"temperature": 0.2}', { parseSpecialCharSequences: false });
    cy.get('[data-testid="agent-model-backend-save"]').click();
    cy.wait(["@save", "@patch"]);
    cy.get('[data-testid="agent-model-backend"]').should("contain", "Start a new conversation");
  });

  it("replaces the key with a pasted one or an existing Secret, never echoing it, and shows a 409 as the API words it", () => {
    open();
    cy.intercept("GET", "**/api/v1/celln-platform/key-secrets*", { body: [{ name: "team-anthropic", key: "ANTHROPIC_API_KEY" }] });
    const bodies: Record<string, unknown>[] = [];
    const refusal = 'Secret "team-anthropic" is pinned to origin https://api.openai.com; a key moved to another origin needs a new key entered by a person';
    cy.intercept("POST", "**/api/v1/model-connections*", (request) => {
      bodies.push(request.body);
      if (request.body.spec.secretRef === "team-anthropic") request.reply({ statusCode: 409, body: refusal });
      else request.reply({ body: { ...connection, spec: { ...request.body.spec, secretRef: request.body.spec.secretRef || "mine-connection-anthropic-key" } } });
    }).as("save");
    cy.intercept("PATCH", "**/api/v1/agents/mine*", { body: agent }).as("patch");

    cy.get('[data-testid="agent-model-backend-edit"]').click();
    cy.get('[data-testid="agent-model-backend-replace-key"]').check();
    cy.get('[data-testid="agent-model-backend-save"]').should("be.disabled");
    cy.get('[data-testid="celln-api-key"]').type(KEY, { log: false });
    cy.get('[data-testid="agent-model-backend-save"]').click();
    cy.wait(["@save", "@patch"]);
    cy.document().then((doc) => expect(doc.documentElement.outerHTML).not.to.contain(KEY));

    cy.get('[data-testid="agent-model-backend-edit"]').click();
    cy.get('[data-testid="agent-model-backend-replace-key"]').check();
    cy.get('[data-testid="celln-key-mode-existing"]').click();
    cy.get('[data-testid="celln-key-secrets"] [data-secret="team-anthropic"]').click();
    cy.get('[data-testid="agent-model-backend-save"]').click();
    cy.wait("@save").then(() => {
      // A pasted key goes with no secretRef (the API names the Secret); a linked one with no key.
      expect(bodies[0]).to.have.property("apiKey", KEY);
      expect(bodies[0].spec).not.to.have.property("secretRef");
      expect(bodies[0].spec).to.have.property("maxOutputTokens", 1024);
      expect(bodies[1]).not.to.have.property("apiKey");
      expect(bodies[1].spec).to.have.property("secretRef", "team-anthropic");
    });
    cy.get('[data-testid="agent-model-backend"] [role="alert"]').should("contain", refusal);
    cy.get("@patch.all").should("have.length", 1);
  });
});

// 0.12 creates every fleet runtime as a profile reference (internal/cellnplatform/platform.go),
// so the run dialog must admit a wrapper runtime without an inline celln.json-tools/v1 contract.
describe("New Run from a profile-backed Celln Agent", () => {
  it("admits the fleet wrapper runtime and submits the Agent's own connection and inherited limits", () => {
    const enduring = { leaseSeconds: 600, maxTurns: 3, maxModelRequests: 18, maxOutputTokens: 9216, requireToolCall: true };
    const tool = { metadata: { name: "celln-starter-grep", namespace: "default", uid: "grep" }, spec: { revision: "v1", description: "grep fixture", publisherKey: "public-publisher", supportOwner: "celln-platform", invocationABI: "celln.json-stdio/v1", lane: "tool", limits: { timeoutMillis: 1000, memoryBytes: 1024, argumentBytes: 128, outputBytes: 128, workspace: "none", effects: "none" } } };
    const toolRef = { name: tool.metadata.name, revision: tool.spec.revision };
    cy.intercept("GET", "**/api/v1/**", { body: [] });
    // The Agent arrives after the page: the deep link must wait for it instead of opening an empty dialog.
    cy.intercept("GET", "**/api/v1/agents*", { delay: 500, body: [{ metadata: { name: "own-key" }, spec: {
      runtimeRef: "celln-native-starter-wrapper", agents: { default: { model: "deepseek-chat" } },
      execution: { backend: "celln", executionLifecycle: "enduring", model: "deepseek-chat", modelConnectionRef: "own-key-connection", enduring, cellnSelection: { runtimeRef: "celln-native-starter-wrapper", toolRefs: [toolRef] } },
    } }] });
    cy.intercept("GET", "**/api/v1/runtimes*", { body: [{ metadata: { name: "celln-native-starter-wrapper" }, spec: { cellnProfileRef: { name: profile.name, revision: profile.revision }, supportOwner: "celln-platform" } }] });
    cy.intercept("GET", "**/api/v1/celln-tools*", { body: [tool] });
    cy.intercept("GET", "**/api/v1/capabilities*", { body: { celln: { available: true, state: "ready", reason: "fleet ready" } } });
    cy.intercept("POST", "**/api/v1/runs*", (request) => {
      expect(request.body).to.include({ agentRef: "own-key", backend: "celln", modelConnectionRef: "own-key-connection", model: "deepseek-chat", executionLifecycle: "enduring", timeout: "600s" });
      expect(request.body.enduring).to.deep.eq(enduring);
      expect(request.body.cellnSelection).to.deep.eq({ toolRefs: [toolRef] });
      expect(request.body).not.to.have.any.keys("apiKey", "secretName", "provider");
      request.reply({ statusCode: 201, body: { metadata: { name: "own-key-run" } } });
    }).as("createRun");
    cy.visit("/runs?create=1&agent=own-key");
    dialog().should("contain", "own-key");
    // The Agent's defaults carry requireToolCall alongside the four limits; only the limits become bounded inputs.
    cy.get('[data-testid="celln-leaseSeconds"]').should("have.value", "600");
    cy.get('[data-testid="celln-maxOutputTokens"]').should("have.value", "9216");
    cy.get('[data-testid="celln-require-tool-call"]').should("be.checked");
    dialog().should("contain", "model gateway").and("not.contain", "fleet ready").and("not.contain", "does not declare");
    cy.get('textarea[placeholder="Describe the task for the agent…"]').type("Reply READY only.");
    // A wrapper runtime has no OCI image: the Job backend is refused, the cell is not.
    cy.get('[data-testid="execution-environment"]').contains("button", "Kubernetes").click();
    dialog().should("contain", "no OCI image");
    dialog().contains("button", "Create Run").should("be.disabled");
    cy.get('[data-testid="execution-environment"]').contains("button", "Celln cell").click();
    dialog().contains("label", `${toolRef.name}@${toolRef.revision}`).find('input[type="checkbox"]').check();
    dialog().contains("button", "Request enduring run").should("be.enabled").click();
    cy.wait("@createRun");
  });
});
