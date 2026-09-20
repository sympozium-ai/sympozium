/**
 * A Celln Agent owns its model backend: a provider route the operator declared
 * for the namespace, the Agent's own key (pasted, or an existing Secret) and
 * one of the route's models. Intercepted browser contract tests — they need no
 * cluster and are not live execution evidence. The Secret's fixed key name is
 * written by the API server (internal/apiserver/server_modelconnections.go,
 * covered by its Go tests); here it is what the console tells the user and
 * what it asks the Secret listing for.
 */

const KEY = "sk-ant-cypress-never-shown-again";

const anthropic = { provider: "anthropic", protocol: "anthropic-messages", models: ["claude-opus-5", "claude-sonnet-5"], endpointOrigins: ["https://api.anthropic.com"], policy: "celln-fleet-starter", secretKey: "ANTHROPIC_API_KEY" };
const gateway = { provider: "llm-gateway", protocol: "openai-chat", models: ["qwen3"], endpointOrigins: ["https://llm.example.com", "https://llm-eu.example.com"], policy: "celln-fleet-starter", secretKey: "OPENAI_API_KEY" };

const profile = {
  name: "celln-native-starter", revision: "v1", policy: "celln-fleet-starter", model: "deepseek-chat", provider: "deepseek",
  endpoint: "https://api.deepseek.com/chat/completions", credentialProfile: "starter", systemPrompt: "Keep replies brief.",
  backend: "native", wrapper: "celln-native", agent: "celln-agent", tools: [{ name: "celln-starter-grep", revision: "v1" }],
  ceilings: { leaseSeconds: 86400, maxTurns: 256, maxModelRequests: 1536, maxOutputTokens: 786432 },
  sessionDefaults: { leaseSeconds: 14400, maxTurns: 64, maxModelRequests: 384, maxOutputTokens: 196608 },
};

type Mediation = { enabled: boolean; mediateBackends: boolean; routes: unknown[]; pending: unknown[] };

function stub(mediation: Mediation) {
  cy.intercept("GET", "**/api/v1/**", { body: [] });
  cy.intercept("GET", "**/api/v1/celln-platform/profiles*", { body: [profile] });
  cy.intercept("GET", "**/api/v1/celln-platform/mediation*", { body: mediation }).as("mediation");
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

function chooseProvider(label: string) {
  cy.get('[data-testid="celln-route-select"]').click();
  cy.get('[role="option"]').contains(label).click();
}

describe("Create Agent → Celln: the Agent's own model backend", () => {
  it("creates a keyless local model connection without reading or writing a Secret", () => {
    const local = { ...gateway, provider: "llama-server", endpointOrigins: ["http://framework:8080"], auth: "none", allowInsecure: true, secretKey: "" };
    stub({ enabled: true, mediateBackends: false, routes: [local], pending: [] });
    cy.intercept("GET", "**/api/v1/celln-platform/key-secrets*", () => { throw new Error("keyless route listed Secrets"); });
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
    cy.get('[data-testid="celln-route-select"]').click();
    cy.get('[role="option"]').first().click();
    next();
    cy.get('[data-testid="celln-keyless-route"]').should("contain", "No API key is required");
    cy.get('[data-testid="celln-api-key"]').should("not.exist");
    next();
    next();
    cy.get('[data-testid="celln-own-key-confirmation"]').should("contain", "keyless route");
    dialog().contains("button", "YAML").click();
    cy.get('[role="dialog"]').last().should("not.contain", "secretRef:").and("not.contain", "authRefs:").and("contain", "allowInsecure: true");
    cy.get("body").type("{esc}");
    dialog().contains("button", /Create\s*$/).click();
    cy.wait(["@connection", "@agent"]);
    cy.get('[role="dialog"]').should("not.exist");
  });

  it("says what the operator must run when mediated model access is disabled", () => {
    stub({ enabled: false, mediateBackends: false, routes: [], pending: [] });
    openProviderStep();
    cy.get('[data-testid="wizard-steps"] [data-step]').then(($steps) => {
      expect([...$steps].map((el) => el.getAttribute("data-step"))).to.deep.equal(["name", "plane", "provider", "apikey", "model", "confirm"]);
    });
    cy.get('[data-testid="celln-mediation-disabled"]').should("contain", "Mediated model access is not enabled").and("contain", "celln.mediation.enabled");
    cy.get('[data-testid="celln-route-command"]').should("contain", "sympozium install").and("contain", "--set celln.mediation.enabled=true").and("contain", "--celln-mediated-route provider=").and("contain", "protocol=").and("contain", "origin=https://").and("contain", "models=");
    cy.get('[data-testid="celln-mediation-disabled"] a').should("have.attr", "href").and("contain", "celln-mediated-model-access.md");
    // Nothing else is offered in its place.
    cy.get('[data-testid="celln-route-select"]').should("not.exist");
    dialog().should("not.contain", "fleet backend").and("not.contain", "Add to the fleet");
    dialog().contains("button", "Next").should("be.disabled");
  });

  it("says no provider is declared", () => {
    stub({ enabled: true, mediateBackends: false, routes: [], pending: [] });
    openProviderStep();
    cy.get('[data-testid="celln-no-routes"]').should("contain", "No provider is declared for this namespace").and("contain", "AUTH_ROUTE_MISMATCH");
    cy.get('[data-testid="celln-route-command"]').should("contain", "--celln-mediated-route provider=").and("not.contain", "celln.mediation.enabled=true");
    dialog().contains("button", "Next").should("be.disabled");
  });

  it("says declared providers are not published yet", () => {
    stub({ enabled: true, mediateBackends: false, routes: [], pending: [{ ...anthropic, policy: undefined }] });
    openProviderStep();
    cy.get('[data-testid="celln-no-routes"]').should("contain", "not published yet").and("contain", "anthropic");
    cy.get('[data-testid="celln-route-command"]').should("have.text", "sympozium celln-mediation apply-routes");
  });

  it("creates the Secret, the connection, the runtime wrapper and the Agent from a pasted key, and never shows the key again", () => {
    stub({ enabled: true, mediateBackends: false, routes: [anthropic, gateway], pending: [] });
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
    // Only the declared providers, in the Kubernetes plane's visual language.
    cy.get('[data-testid="celln-route-select"]').click();
    cy.get('[role="option"]').should("have.length", 2);
    cy.get('[role="option"]').contains("OpenAI").should("not.exist");
    cy.get('[role="option"]').contains("Anthropic").click();
    cy.get('[data-testid="celln-route-summary"]').should("contain", "celln-fleet-starter").and("contain", "its own key");
    next();

    // Auth: the key name is fixed by the protocol and the Secret lives here.
    cy.get('[data-testid="celln-key-mode-create"]').should("have.attr", "aria-pressed", "true");
    dialog().contains("button", "Next").should("be.disabled");
    cy.get('[data-testid="celln-key-step"]').should("contain", "ANTHROPIC_API_KEY").and("contain", "mine-connection-anthropic-key").and("contain", "default");
    cy.get('[data-testid="celln-api-key"]').should("have.attr", "type", "password").type(KEY, { log: false });
    next();

    // Model: exactly the route's models; nothing is free text.
    cy.get('[data-testid="celln-models"] button').should("have.length", 2);
    cy.get('[data-testid="celln-model-step"]').find('input[placeholder="gpt-4o"]').should("not.exist");
    dialog().contains("button", "Next").should("be.disabled");
    cy.get('[data-testid="celln-models"] [data-model="claude-sonnet-5"]').click();
    cy.get('[data-testid="celln-origin"]').should("have.text", "https://api.anthropic.com");
    cy.get('[data-testid="celln-endpoint-path"]').should("have.value", "/v1/messages");
    // Hosted Anthropic takes no chat-template switch.
    cy.get('[data-testid="celln-model-parameters"] summary').click();
    cy.get('[data-testid="celln-disable-thinking"]').should("not.exist");
    next();

    cy.get('[data-testid="celln-own-key-confirmation"]').should("contain", "mine-connection-anthropic-key").and("contain", "ANTHROPIC_API_KEY").and("contain", "https://api.anthropic.com/v1/messages").and("contain", "celln-native");
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

  it("links an existing Secret that already holds the protocol's key, with a kubectl prompt and a refresh", () => {
    stub({ enabled: true, mediateBackends: false, routes: [anthropic], pending: [] });
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
    chooseProvider("Anthropic");
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
    cy.get('[data-testid="celln-models"] [data-model="claude-opus-5"]').click();
    next();
    cy.get('[data-testid="celln-own-key-confirmation"]').should("contain", "existing Secret team-anthropic");
    dialog().contains("button", /Create\s*$/).click();
    cy.wait(["@connection", "@runtime", "@agent"]);
    cy.get('[role="dialog"]').should("not.exist");
  });

  it("offers a route's several origins, fixes the key name by protocol, and writes the advanced parameters to this Agent's connection", () => {
    stub({ enabled: true, mediateBackends: false, routes: [anthropic, gateway], pending: [] });
    cy.intercept("POST", "**/api/v1/model-connections*", (request) => {
      expect(request.body.spec).to.deep.equal({
        provider: "llm-gateway", protocol: "openai-chat", endpoint: "https://llm-eu.example.com/openai/v1/chat/completions", models: ["qwen3"],
        parameters: { chat_template_kwargs: { enable_thinking: false } }, maxOutputTokens: 2048,
      });
      request.reply({ statusCode: 201, body: { metadata: { name: "mine-connection" }, spec: { ...request.body.spec, secretRef: "mine-connection-llm-gateway-key" } } });
    }).as("connection");
    cy.intercept("POST", "**/api/v1/celln-platform/wrappers*", { body: { backend: "native", runtime: "celln-native", agent: "", connection: "", created: [] } });
    cy.intercept("POST", "**/api/v1/agents*", (request) => {
      // A turn reserves 6 requests of 2048 tokens, so the ceilings pay for 64 turns of 12288.
      expect(request.body.execution.enduring).to.deep.equal({ leaseSeconds: 14400, maxTurns: 64, maxModelRequests: 384, maxOutputTokens: 786432 });
      request.reply({ statusCode: 201, body: { metadata: { name: "mine" }, spec: {} } });
    }).as("agent");

    openProviderStep();
    chooseProvider("llm-gateway");
    next();
    // An OpenAI-protocol route fixes OPENAI_API_KEY, whatever the provider is called.
    cy.get('[data-testid="celln-key-step"]').should("contain", "OPENAI_API_KEY").and("not.contain", "ANTHROPIC_API_KEY");
    cy.get('[data-testid="celln-api-key"]').type(KEY, { log: false });
    next();
    // The route's only model is already chosen; the origin is the operator's list.
    cy.get('[data-testid="celln-models"] [data-model="qwen3"]').should("have.attr", "aria-pressed", "true");
    cy.get('[data-testid="celln-origin-select"]').click();
    cy.get('[role="option"]').should("have.length", 2).contains("https://llm-eu.example.com").click();
    cy.get('[data-testid="celln-endpoint-path"]').should("have.value", "/v1/chat/completions").clear().type("/openai/v1/chat/completions");

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

  it("says exactly what was created when a later step fails, and a retry does not send the key again", () => {
    stub({ enabled: true, mediateBackends: false, routes: [anthropic], pending: [] });
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
    chooseProvider("Anthropic");
    next();
    cy.get('[data-testid="celln-api-key"]').type(KEY, { log: false });
    next();
    cy.get('[data-testid="celln-models"] [data-model="claude-opus-5"]').click();
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
    stub({ enabled: true, mediateBackends: false, routes: [anthropic], pending: [] });
    cy.intercept("POST", "**/api/v1/model-connections*", { statusCode: 400, body: "endpoint must be an HTTP(S) API URL without credentials, query or fragment" }).as("connection");
    cy.intercept("POST", "**/api/v1/celln-platform/wrappers*", () => { throw new Error("the runtime step ran after a refused connection"); });
    openProviderStep();
    chooseProvider("Anthropic");
    next();
    cy.get('[data-testid="celln-api-key"]').type(KEY, { log: false });
    next();
    cy.get('[data-testid="celln-models"] [data-model="claude-opus-5"]').click();
    next();
    dialog().contains("button", /Create\s*$/).click();
    cy.wait("@connection");
    cy.get('[data-testid="celln-own-key-progress"] [data-step="secret"]').should("have.attr", "data-state", "not-started");
    cy.get('[data-testid="celln-own-key-progress"] [data-step="connection"]').should("have.attr", "data-state", "failed");
    cy.get('[data-testid="celln-own-key-progress"] [data-step="agent"]').should("have.attr", "data-state", "not-started");
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

  function open(mediation: Mediation) {
    stub(mediation);
    cy.intercept("GET", "**/api/v1/agents/mine*", { body: agent });
    cy.intercept("GET", "**/api/v1/agents?*", { body: [agent] });
    cy.intercept("GET", "**/api/v1/runtimes*", { body: [runtime] });
    cy.intercept("GET", "**/api/v1/model-connections*", { body: [connection] });
    cy.visit("/agents/mine?tab=harness#token=test-token");
  }

  it("shows the provider, model, origin, Secret name and limits, and no fleet-backend controls", () => {
    open({ enabled: true, mediateBackends: false, routes: [anthropic], pending: [] });
    cy.get('[data-testid="agent-model-backend"]').should("contain", "anthropic").and("contain", "claude-sonnet-5").and("contain", "https://api.anthropic.com").and("contain", "ANTHROPIC_API_KEY").and("contain", "1024 per request");
    cy.get('[data-testid="agent-model-backend-secret"]').should("have.text", "mine-connection-anthropic-key");
    cy.get('[data-testid="agent-model-backend-no-route"]').should("not.exist");
    cy.get('[data-testid="agent-backend-picker"]').should("not.exist");
    cy.get('[data-testid="celln-add-backend"]').should("not.exist");
    cy.get('[data-testid="agent-execution-defaults"]').should("not.contain", "Add a fleet backend").and("not.contain", "Add to the fleet");
  });

  it("warns when no declared route matches the connection any more", () => {
    open({ enabled: true, mediateBackends: false, routes: [{ ...anthropic, models: ["claude-opus-5"] }], pending: [] });
    cy.get('[data-testid="agent-model-backend-no-route"]').should("contain", "AUTH_ROUTE_MISMATCH").and("contain", "claude-sonnet-5");
  });

  it("edits the parameters and output tokens of this Agent's connection and says a new conversation is needed", () => {
    open({ enabled: true, mediateBackends: false, routes: [anthropic], pending: [] });
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
    cy.get('[data-testid="agent-model-backend-route-changed"]').should("contain", "MODEL_ROUTE_CHANGED").and("contain", "start a new conversation");
    cy.get('[data-testid="celln-model-parameters"] summary').click();
    cy.get('[data-testid="celln-max-output-tokens"]').should("have.value", "1024").clear().type("4096");
    cy.get('[data-testid="celln-model-parameters-json"]').type('{"temperature": 0.2}', { parseSpecialCharSequences: false });
    cy.get('[data-testid="agent-model-backend-save"]').click();
    cy.wait(["@save", "@patch"]);
    cy.get('[data-testid="agent-model-backend"]').should("contain", "Start a new conversation");
  });

  it("replaces the key with a pasted one or an existing Secret, never echoing it", () => {
    open({ enabled: true, mediateBackends: false, routes: [anthropic], pending: [] });
    cy.intercept("GET", "**/api/v1/celln-platform/key-secrets*", { body: [{ name: "team-anthropic", key: "ANTHROPIC_API_KEY" }] });
    const bodies: Record<string, unknown>[] = [];
    cy.intercept("POST", "**/api/v1/model-connections*", (request) => { bodies.push(request.body); request.reply({ body: { ...connection, spec: { ...request.body.spec, secretRef: request.body.spec.secretRef || "mine-connection-anthropic-key" } } }); }).as("save");
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
    cy.wait(["@save", "@patch"]).then(() => {
      // A pasted key goes with no secretRef (the API names the Secret); a linked one with no key.
      expect(bodies[0]).to.have.property("apiKey", KEY);
      expect(bodies[0].spec).not.to.have.property("secretRef");
      expect(bodies[0].spec).to.have.property("maxOutputTokens", 1024);
      expect(bodies[1]).not.to.have.property("apiKey");
      expect(bodies[1].spec).to.have.property("secretRef", "team-anthropic");
    });
  });
});
