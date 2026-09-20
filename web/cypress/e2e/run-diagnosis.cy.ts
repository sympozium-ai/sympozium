// Intercepted browser contract tests plus a table over the pure diagnosis
// function (this repo has no unit-test runner): not live Celln evidence.
// Fixtures are statuses observed on a real cluster.
import { AUTH_REASONS, HARNESS_ERRORS, diagnoseRun, harnessError, parseAdmissionRefusal } from "../../src/lib/run-diagnosis";
import type { AgentRun, AgentRunTurn, CellnMediation, CellnPlatformProfile } from "../../src/lib/api";

const REFUSAL_TAIL = "Ask the operator to authorise this namespace, runtime profile, tools and model route; do not create a replacement run.";
const LOST_MESSAGE = "owner=ContextLost reachedReady=true admittedAge=33s incarnation=blake3:1f0c launchProfile=blake3:77ab; parent context unavailable; no automatic reconstruction";
const BUDGET_ANSWER = 'Turn failed; no result committed: child refused: guest exited with code 1: CELLN_HARNESS_EVENT {"type":"tool_call","name":"web-fetch"} CELLN_HARNESS_ERROR tool call budget exhausted';
const LENGTH_ANSWER = 'Turn failed; no result committed: child refused: guest exited with code 1: CELLN_HARNESS_EVENT {"type":"final"} CELLN_HARNESS_ERROR final answer is empty or exceeds limit';
const TOO_LONG_ANSWER = 'Turn failed; no result committed: child refused: guest exited with code 1: CELLN_HARNESS_EVENT {"type":"final"} CELLN_HARNESS_ERROR final answer exceeds limit: 9120 bytes, at most 8192';
const BUDGET_SPENT_ANSWER = 'Turn failed; no result committed: child refused: guest exited with code 1: CELLN_HARNESS_EVENT {"type":"final"} CELLN_HARNESS_ERROR final answer is empty: the model used its whole output budget (512 tokens) without answering';

function run(status: Record<string, unknown>, spec: Record<string, unknown> = {}): AgentRun {
  return {
    metadata: { name: "hermes-abc12", namespace: "default", uid: "run-uid", generation: 1 },
    spec: { agentRef: "hermes", agentId: "hermes", sessionKey: "s", backend: "celln", task: "Remember violet", executionLifecycle: "enduring", enduring: { leaseSeconds: 3600, maxTurns: 8, maxModelRequests: 32, maxOutputTokens: 4096 }, ...spec },
    status,
  } as unknown as AgentRun;
}

function condition(type: string, reason: string, message: string, status = "False") {
  return { type, status, reason, message, observedGeneration: 1, lastTransitionTime: "2026-09-18T10:00:00Z" };
}

const initialOK = { id: "initial", message: "Remember violet", child: "c", attempted: true, result: { succeeded: true, answer: "Remembered violet" } };

const parentLost = run({
  phase: "Failed",
  error: `Celln parent context lost or stopped; reconcile recorded turns: ${LOST_MESSAGE}; continued as hermes-tzvz6`,
  conditions: [condition("CellnParentReady", "ContextLost", LOST_MESSAGE)],
  cellnParent: { binding: { incarnation: "blake3:1f0c", runUID: "run-uid" }, createAttempted: true, acceptedTurns: 1, initialTurn: initialOK,
    ownerOutcome: { status: "ContextLost", reachedReady: true, observedAt: "2026-09-18T10:00:33Z" }, continuedBy: "hermes-tzvz6" },
});

const admissionRefused = run({
  phase: "Pending",
  conditions: [condition("CellnParentReady", "AdmissionPending", `Platform policy refused admission (AUTH_TOOL_UNKNOWN). ${REFUSAL_TAIL}`)],
}, { cellnSelection: { runtimeRef: "celln-fleet-native", toolRefs: [], clusterToolRefs: [{ name: "web-fetch", revision: "r1" }, { name: "shell-exec", revision: "r9" }] } });

const turnFailed = run({
  phase: "Running",
  conditions: [condition("CellnParentReady", "Ready", "Native parent initialized; turn completion is tracked separately", "True")],
  cellnParent: { binding: { incarnation: "blake3:1f0c", runUID: "run-uid" }, createAttempted: true, acceptedTurns: 0,
    initialTurn: { ...initialOK, result: { succeeded: false, answer: BUDGET_ANSWER } } },
});

const continuationWithheld = run({
  phase: "Failed",
  error: `Celln parent lost again on its continuation; not continuing automatically — start a new conversation: ${LOST_MESSAGE}`,
  conditions: [
    condition("CellnParentReady", "ContextLost", LOST_MESSAGE),
    condition("CellnContinuation", "LostBeforeFollowUp", "Celln parent lost again on its continuation; not continuing automatically — start a new conversation"),
  ],
  cellnParent: { binding: { incarnation: "blake3:1f0c", runUID: "run-uid" }, createAttempted: true, acceptedTurns: 0, initialTurn: initialOK,
    ownerOutcome: { status: "ContextLost", reachedReady: true } },
}, { conversation: { continuation: "automatic", continuesFrom: "hermes-first", depth: 1 } });

const profiles = [{
  name: "native", revision: "p1", policy: "fleet", model: "qwen3", provider: "llama-server", endpoint: "", credentialProfile: "", systemPrompt: "You are Hermes.",
  backend: "native", wrapper: "celln-fleet-native", agent: "hermes", tools: [{ name: "web-fetch", revision: "r1" }],
  ceilings: { leaseSeconds: 7200, maxTurns: 16, maxModelRequests: 64, maxOutputTokens: 8192 },
  sessionDefaults: { leaseSeconds: 3600, maxTurns: 8, maxModelRequests: 32, maxOutputTokens: 4096 },
}] as CellnPlatformProfile[];

describe("diagnoseRun", () => {
  it("has an entry for every reason code the platform resolver can return", () => {
    cy.readFile("../internal/cellnauthority/platform_resolver.go").then((source: string) => {
      const codes = [...new Set([...source.matchAll(/=\s*"(AUTH_[A-Z_]+)"/g)].map((match) => match[1]))];
      expect(codes.length, "reason constants found").to.be.greaterThan(5);
      for (const code of [...codes, "AUTH_PROTOCOL_UNSUPPORTED"]) expect(AUTH_REASONS, code).to.have.property(code);
    });
  });

  it("explains each admission refusal without deferring to someone else", () => {
    for (const code of [...Object.keys(AUTH_REASONS), "AUTH_CRED_SIG_INVALID", "AUTH_ADMISSION_WINDOW_EXPIRED", "AUTH_SOMETHING_NEW"]) {
      const diagnosis = diagnoseRun(run({ phase: "Pending", conditions: [condition("CellnParentReady", "AdmissionPending", `Platform policy refused admission (${code}). ${REFUSAL_TAIL}`)] }));
      expect(diagnosis, code).to.include({ kind: "admission-refused", code, severity: "error" });
      expect(diagnosis!.cause, code).to.have.length.greaterThan(20);
      expect(diagnosis!.nextSteps, code).to.have.length.greaterThan(0);
      expect(JSON.stringify([diagnosis!.title, diagnosis!.cause, diagnosis!.nextSteps]), code).not.to.match(/ask (the|your|an) operator/i);
      expect(diagnosis!.evidence[0], code).to.contain(code);
    }
  });

  const table: [string, AgentRun, AgentRunTurn[], Partial<ReturnType<typeof diagnoseRun>> | null, RegExp?][] = [
    ["healthy running run", run({ phase: "Running", conditions: [condition("CellnParentReady", "Ready", "ok", "True")], cellnParent: { acceptedTurns: 0, initialTurn: initialOK } }), [], null],
    ["succeeded one-shot", run({ phase: "Succeeded" }, { executionLifecycle: "one-shot" }), [], null],
    ["parent lost and continued", parentLost, [], { kind: "parent-lost", severity: "info", code: "ContextLost" }, /continues as hermes-tzvz6/],
    ["parent stopped", run({ phase: "Failed", conditions: [condition("CellnParentReady", "Stopped", "owner=Stopped")], cellnParent: { acceptedTurns: 0 } }), [], { kind: "parent-lost", code: "Stopped", severity: "error" }, /^The parent has stopped/],
    ["teardown uncertain", run({ phase: "Failed", conditions: [condition("CellnParentReady", "TeardownUncertain", "owner=TeardownUncertain")], cellnParent: { acceptedTurns: 0 } }), [], { kind: "parent-lost", code: "TeardownUncertain", severity: "warning" }, /^Parent teardown is unconfirmed/],
    ["lost while warming", run({ phase: "Failed", cellnParent: { acceptedTurns: 0, ownerOutcome: { status: "ContextLost", reachedReady: false } } }), [], { kind: "parent-lost", code: "ContextLost" }, /warming up/],
    ["owner refused create", run({ phase: "Failed", conditions: [condition("CellnParentReady", "CreateRefused", "refused")], cellnParent: { acceptedTurns: 0 } }), [], { kind: "create-refused" }],
    ["continuation withheld", continuationWithheld, [], { kind: "continuation-withheld", code: "LostBeforeFollowUp" }, /avoid a loop/],
    ["stale generation is ignored", { ...parentLost, metadata: { ...parentLost.metadata, generation: 2 }, status: { ...parentLost.status, cellnParent: undefined, error: undefined } } as AgentRun, [], { kind: "run-failed" }],
    ["waiting admission", run({ phase: "Pending", conditions: [condition("CellnParentReady", "AdmissionPending", "Waiting for a matching operator-prepared parent registration and current grants.")] }), [], { kind: "admission-pending", severity: "info" }, /current grants/],
    ["initial turn tool budget", turnFailed, [], { kind: "turn-failed", code: "tool call budget exhausted" }, /per-turn tool call limit/],
    ["follow-up answer bound", { ...turnFailed, status: { ...turnFailed.status, cellnParent: { ...turnFailed.status!.cellnParent!, initialTurn: initialOK } } } as AgentRun,
      [{ metadata: { name: "turn-1" }, spec: { runName: "r", runUID: "run-uid", message: "List everything" }, status: { execution: { ...initialOK, result: { succeeded: false, answer: LENGTH_ANSWER } } } }],
      { kind: "turn-failed", code: "final answer is empty or exceeds limit" }, /answer size bound.*reasoning model/],
    ["follow-up answer spent on reasoning", { ...turnFailed, status: { ...turnFailed.status, cellnParent: { ...turnFailed.status!.cellnParent!, initialTurn: initialOK } } } as AgentRun,
      [{ metadata: { name: "turn-1" }, spec: { runName: "r", runUID: "run-uid", message: "List everything" }, status: { execution: { ...initialOK, result: { succeeded: false, answer: BUDGET_SPENT_ANSWER } } } }],
      { kind: "turn-failed", title: "Turn failed: the model spent its output budget before answering" }, /512 output tokens.*reasoning model/],
    ["older failed turn followed by an answer", { ...turnFailed, status: { ...turnFailed.status, cellnParent: { ...turnFailed.status!.cellnParent!, initialTurn: initialOK } } } as AgentRun, [
      { metadata: { name: "turn-1", creationTimestamp: "2026-09-18T10:00:00Z" }, spec: { runName: "r", runUID: "run-uid", message: "a" }, status: { execution: { ...initialOK, result: { succeeded: false, answer: LENGTH_ANSWER } } } },
      { metadata: { name: "turn-2", creationTimestamp: "2026-09-18T10:01:00Z" }, spec: { runName: "r", runUID: "run-uid", message: "b" }, status: { execution: initialOK } },
    ], null],
    ["unrecognised harness error", { ...turnFailed, status: { ...turnFailed.status, cellnParent: { ...turnFailed.status!.cellnParent!, initialTurn: { ...initialOK, result: { succeeded: false, answer: "CELLN_HARNESS_ERROR model route returned 503" } } } } } as AgentRun, [], { kind: "turn-failed" }, /model route returned 503/],
    ["generic failure shows status.error", run({ phase: "Failed", error: "pod OOMKilled", conditions: [condition("PodReady", "OOM", "container exceeded memory")] }, { executionLifecycle: "one-shot" }), [], { kind: "run-failed" }, /pod OOMKilled/],
  ];
  for (const [name, subject, turns, expected, cause] of table) {
    it(`diagnoses: ${name}`, () => {
      const diagnosis = diagnoseRun(subject, turns);
      if (expected === null) { expect(diagnosis).to.equal(null); return; }
      expect(diagnosis).to.include(expected);
      if (cause) expect(diagnosis!.cause).to.match(cause);
      expect(diagnosis!.evidence.every((text) => text.length <= 600)).to.equal(true);
    });
  }

  it("names disabling thinking on this Agent's model connection as the remedy for an empty answer", () => {
    for (const answer of [LENGTH_ANSWER, BUDGET_SPENT_ANSWER]) {
      const failed = { ...turnFailed, status: { ...turnFailed.status, cellnParent: { ...turnFailed.status!.cellnParent!, initialTurn: { ...initialOK, result: { succeeded: false, answer } } } } } as AgentRun;
      const steps = diagnoseRun(failed)!.nextSteps;
      const step = steps.find((candidate) => candidate.label === "Disable thinking on this Agent's model connection")!;
      // The message that names the spent budget leads with the remedy.
      expect(steps.indexOf(step)).to.equal(answer === BUDGET_SPENT_ANSWER ? 0 : 1);
      expect(step.detail).to.contain("Disable thinking (reasoning models)").and.contain('{"chat_template_kwargs":{"enable_thinking":false}}').and.contain("JSON parameters");
      // The Agent owns its backend: edit it, then start a new conversation.
      expect(step.detail).to.contain("Agent → Harness → Model backend → Edit").and.contain("MODEL_ROUTE_CHANGED").and.contain("start a new conversation");
      expect(step.detail).not.to.match(/fleet backend|new name|cannot change/i);
      expect(step.action).to.deep.include({ kind: "link", to: "/agents/hermes?tab=harness" });
    }
  });

  it("names both remedies for an empty answer: thinking disabled, or more output tokens per request", () => {
    for (const answer of [LENGTH_ANSWER, BUDGET_SPENT_ANSWER]) {
      const failed = { ...turnFailed, status: { ...turnFailed.status, cellnParent: { ...turnFailed.status!.cellnParent!, initialTurn: { ...initialOK, result: { succeeded: false, answer } } } } } as AgentRun;
      const steps = diagnoseRun(failed)!.nextSteps;
      const thinking = steps.findIndex((candidate) => candidate.label === "Disable thinking on this Agent's model connection");
      const raise = steps[thinking + 1];
      expect(raise.label).to.equal("Or raise this Agent's max output tokens per request");
      expect(raise.detail).to.contain("Max output tokens per request").and.contain("2048–4096").and.contain("4–8×").and.contain("4 minutes at 2048").and.contain("start a new conversation");
      expect(raise.detail).not.to.match(/fleet backend|new name/i);
      expect(raise.action).to.deep.include({ kind: "link", to: "/agents/hermes?tab=harness" });
    }
  });

  it("diagnoses a turn stopped at its time limit", () => {
    const failed = { ...turnFailed, status: { ...turnFailed.status, cellnParent: { ...turnFailed.status!.cellnParent!, initialTurn: { ...initialOK, result: { succeeded: false, answer: "Turn failed; no result committed: child timed out" } } } } } as AgentRun;
    const diagnosis = diagnoseRun(failed)!;
    expect(diagnosis).to.include({ kind: "turn-failed", title: "Turn failed: the turn ran out of time" });
    expect(diagnosis.cause).to.contain("60 seconds").and.contain("longer when the Agent's model connection allows more");
    expect(diagnosis.nextSteps.map((step) => step.label)).to.include.members(["Ask for less in one message", "Disable thinking on this Agent's model connection", "If the connection already allows more output tokens"]);
  });

  it("diagnoses an over-long answer: ask for less, or lower the Agent's output tokens", () => {
    const failed = { ...turnFailed, status: { ...turnFailed.status, cellnParent: { ...turnFailed.status!.cellnParent!, initialTurn: { ...initialOK, result: { succeeded: false, answer: TOO_LONG_ANSWER } } } } } as AgentRun;
    const diagnosis = diagnoseRun(failed)!;
    expect(diagnosis).to.include({ kind: "turn-failed", title: "Turn failed: the answer was too long" });
    expect(diagnosis.code).to.contain("final answer exceeds limit");
    expect(diagnosis.cause).to.contain("answer size bound").and.not.contain("thinking");
    // The parent is Ready, so the conversation stays open after the failed first turn.
    expect(diagnosis.nextSteps.map((step) => step.label)).to.deep.equal(["Ask for a shorter answer", "Or lower this Agent's max output tokens per request", "Send another message"]);
    expect(diagnosis.nextSteps[1].detail).to.contain("Max output tokens per request").and.contain("lower it");
    expect(diagnosis.nextSteps[1].action).to.deep.include({ kind: "link", to: "/agents/hermes?tab=harness" });
  });

  // An Agent with its own key runs gateway-mediated; its refusals arrive on the
  // CellnScopedExecution condition (internal/controller/celln_scoped.go), where
  // the controller shares the reason code but never the resolver's sentence.
  const SCOPED_REFUSAL = "AUTH_ROUTE_MISMATCH: scoped authority refused before receiver enrollment; no native execution started";
  const ownKeyModel = { connectionRef: "mine-connection", provider: "anthropic", protocol: "anthropic-messages", model: "claude-opus-5", baseURL: "https://api.anthropic.com/v1/messages" };
  const mediation = (models: string[]): CellnMediation => ({ enabled: true, mediateBackends: false, pending: [],
    routes: [{ provider: "anthropic", protocol: "anthropic-messages", models, endpointOrigins: ["https://api.anthropic.com"], policy: "celln-fleet-starter", secretKey: "ANTHROPIC_API_KEY" }] });
  const scopedRefused = run({ phase: "Failed", error: SCOPED_REFUSAL, conditions: [condition("CellnScopedExecution", "AdmissionRefused", SCOPED_REFUSAL)] }, { model: ownKeyModel });

  it("AUTH_ROUTE_MISMATCH names the provider, model and origin asked and that no declared route matches", () => {
    const diagnosis = diagnoseRun(scopedRefused, [], { mediation: mediation(["claude-sonnet-5"]) })!;
    expect(diagnosis).to.include({ kind: "admission-refused", code: "AUTH_ROUTE_MISMATCH" });
    expect(diagnosis.cause).to.contain("anthropic (anthropic-messages)").and.contain("claude-opus-5").and.contain("https://api.anthropic.com").and.contain("no route an operator declared").and.contain("claude-sonnet-5");
    // The controller's boilerplate is evidence, not an explanation.
    expect(diagnosis.cause).not.to.contain("receiver enrollment");
    expect(diagnosis.evidence.join(" ")).to.contain("receiver enrollment");
    expect(diagnosis.nextSteps[0].label).to.equal("Declare the route, or pick a declared one");
    expect(diagnosis.nextSteps[0].detail).to.contain("--celln-mediated-route provider=anthropic,protocol=anthropic-messages,origin=https://api.anthropic.com,models=claude-opus-5").and.contain("apply-routes");
  });

  it("AUTH_ROUTE_MISMATCH with a matching declared route points at the connection and the Agent's authRefs", () => {
    const diagnosis = diagnoseRun(scopedRefused, [], { mediation: mediation(["claude-opus-5"]) })!;
    expect(diagnosis.cause).to.contain("an operator declared").and.contain("mine-connection").and.contain("authRefs");
    expect(diagnosis.nextSteps.map((step) => step.label)).to.deep.equal(["Run on this Agent's own connection", "Delete this run, then start a new one"]);
    // Without the declared routes nothing is claimed about them.
    expect(diagnoseRun(scopedRefused)!.cause).to.contain("claude-opus-5").and.contain("must all match one declared route").and.contain("Secret");
  });

  it("the authRefs refusal says the run named a connection whose Secret the Agent was not given", () => {
    // platform_resolver.go resolveDecisionRoute; shared verbatim on the fleet parent path.
    const message = `Platform policy refused admission (AUTH_ROUTE_MISMATCH): Agent "hermes" does not grant the model connection's Secret; add it to the Agent's authRefs or select the connection in the Agent's execution defaults. ${REFUSAL_TAIL}`;
    const diagnosis = diagnoseRun(run({ phase: "Pending", conditions: [condition("CellnParentReady", "AdmissionPending", message)] }, { model: ownKeyModel }), [], { mediation: mediation(["claude-opus-5"]) })!;
    expect(diagnosis.code).to.equal("AUTH_ROUTE_MISMATCH");
    expect(diagnosis.cause).to.contain("mine-connection").and.contain("whose Secret this Agent was not given");
    expect(diagnosis.nextSteps[0].label).to.equal("Run on this Agent's own connection");
    expect(diagnosis.nextSteps[0].detail).to.contain("authRefs");
  });

  it("the resolver still words the authRefs refusal and the controller still reports the scoped reasons this way", () => {
    cy.readFile("../internal/cellnauthority/platform_resolver.go").then((source: string) => expect(source).to.contain("does not grant the model connection's Secret"));
    cy.readFile("../internal/controller/celln_scoped.go").then((source: string) => {
      expect(source).to.contain('"ScopedDispatchDisabled"').and.contain('"AdmissionRefused"').and.contain('"CellnScopedExecution"');
      expect(source).to.contain("scoped authority refused before receiver enrollment; no native execution started");
    });
  });

  it("ScopedDispatchDisabled: mediation is not enabled, the run waits", () => {
    const held = run({ phase: "Pending", conditions: [condition("CellnScopedExecution", "ScopedDispatchDisabled", "Operator scoped receiver configuration is absent; no legacy, model, OCI, or native execution was submitted")] }, { model: ownKeyModel });
    const diagnosis = diagnoseRun(held)!;
    expect(diagnosis).to.include({ kind: "mediation-disabled", code: "ScopedDispatchDisabled", severity: "warning" });
    expect(diagnosis.cause).to.contain("own provider key").and.contain("celln.mediation.enabled").and.contain("never sent to a fleet backend");
    expect(diagnosis.nextSteps.map((step) => step.label)).to.deep.equal(["Enable mediated model access", "Leave this run in place"]);
  });

  it("has an entry for the gateway refusals a per-Agent connection can hit", () => {
    cy.readFile("../internal/modelgateway/types.go").then((source: string) => {
      for (const code of ["MODEL_ROUTE_CHANGED", "MODEL_AUTH_FORBIDDEN", "MODEL_CREDENTIAL_SOURCE_CHANGED"]) {
        expect(source, code).to.contain(`"${code}"`);
        expect(HARNESS_ERRORS.some((entry) => entry.match.test(`model request refused: 403 {"reason":"${code}"}`)), code).to.equal(true);
      }
    });
    const gatewayTurn = (code: string) => ({ ...turnFailed, status: { ...turnFailed.status, cellnParent: { ...turnFailed.status!.cellnParent!, initialTurn: { ...initialOK, result: { succeeded: false, answer: `Turn failed; no result committed: CELLN_HARNESS_ERROR model request refused: 403 {"reason":"${code}"}` } } } } }) as AgentRun;
    const changed = diagnoseRun(gatewayTurn("MODEL_ROUTE_CHANGED"))!;
    expect(changed.title).to.contain("model connection changed mid-conversation");
    expect(changed.nextSteps[0].label).to.equal("Start a new conversation");
    // Resending into the pinned conversation cannot work.
    expect(changed.nextSteps.some((step) => step.label === "Send another message")).to.equal(false);
    const forbidden = diagnoseRun(gatewayTurn("MODEL_AUTH_FORBIDDEN"))!;
    expect(forbidden.cause).to.contain("more output tokens than this Agent's model connection allows").and.contain("pin");
    expect(forbidden.nextSteps[0].detail).to.contain("Max output tokens per request").and.contain("Parameters (JSON)");
    expect(diagnoseRun(gatewayTurn("MODEL_CREDENTIAL_SOURCE_CHANGED"))!.nextSteps[0].detail).to.contain("Replace the key");
    // A one-shot run has only its error.
    const oneShot = diagnoseRun(run({ phase: "Failed", error: "model gateway: MODEL_ROUTE_CHANGED" }, { executionLifecycle: "one-shot" }))!;
    expect(oneShot).to.include({ kind: "run-failed" });
    expect(oneShot.title).to.contain("Run failed: this Agent's model connection changed");
  });

  it("names the refused tool from the condition detail, else from the profile", () => {
    const detailed = run({ phase: "Pending", conditions: [condition("CellnParentReady", "AdmissionPending", `Platform policy refused admission (AUTH_TOOL_UNKNOWN): policy "fleet" does not permit tool "shell-exec" at the selected revision. ${REFUSAL_TAIL}`)] });
    expect(diagnoseRun(detailed)!.cause).to.contain("shell-exec").and.not.contain("fleet\"");
    expect(diagnoseRun(admissionRefused)!.cause).not.to.contain("shell-exec");
    const named = diagnoseRun(admissionRefused, [], { profiles })!;
    expect(named.cause).to.contain("shell-exec@r9").and.not.contain("web-fetch");
    expect(named.nextSteps[0].label).to.contain("Remove shell-exec@r9");
    expect(named.nextSteps[0].detail).to.contain("web-fetch@r1");
  });

  it("uses the controller's persona detail for AUTH_POLICY_CONTRACTED", () => {
    const message = `Platform policy refused admission (AUTH_POLICY_CONTRACTED): run persona differs from the runtime profile's bound persona; send the profile's systemPrompt verbatim. ${REFUSAL_TAIL}`;
    expect(parseAdmissionRefusal(message)).to.deep.equal({ code: "AUTH_POLICY_CONTRACTED", detail: "run persona differs from the runtime profile's bound persona; send the profile's systemPrompt verbatim" });
    const diagnosis = diagnoseRun(run({ phase: "Pending", conditions: [condition("CellnParentReady", "AdmissionPending", message)] }))!;
    expect(diagnosis.cause).to.contain("system prompt differs from the persona");
    expect(diagnosis.nextSteps[0].action).to.deep.equal({ kind: "link", label: "Open the Agent's Chat tab", to: "/agents/hermes?tab=chat" });
  });

  it("a failed first turn offers Send another message only while the parent is Ready", () => {
    const steps = diagnoseRun(turnFailed)!.nextSteps;
    // The specific remedy still leads; the conversation is not a dead end.
    expect(steps[0].label).to.equal("Ask for fewer actions per message");
    expect(steps[0].detail).to.contain("send the next message below").and.not.contain("not accepting messages");
    expect(steps[0].action).to.equal(undefined);
    const send = steps.find((step) => step.label === "Send another message")!;
    expect(send.detail).to.contain("The first turn failed; the conversation is still open — send another message").and.contain("turn limit");
    // Parent not reported Ready, run not Running, or deleting: a new conversation, as before.
    const closed: AgentRun[] = [
      { ...turnFailed, status: { ...turnFailed.status, conditions: [] } } as AgentRun,
      { ...turnFailed, status: { ...turnFailed.status, conditions: [condition("CellnParentReady", "Initializing", "Parent startup pending")] } } as AgentRun,
      { ...turnFailed, status: { ...turnFailed.status, phase: "Failed" } } as AgentRun,
      { ...turnFailed, metadata: { ...turnFailed.metadata, deletionTimestamp: "2026-09-19T10:00:00Z" } } as AgentRun,
    ];
    for (const subject of closed) {
      const closedSteps = diagnoseRun(subject)!.nextSteps;
      expect(closedSteps.some((step) => step.label === "Send another message")).to.equal(false);
      expect(closedSteps[0].label).to.equal("Ask for fewer actions per message");
      expect(closedSteps[0].detail).to.contain("this parent is not accepting messages");
      expect(closedSteps[0].action).to.include({ kind: "link" });
    }
    // A lost parent keeps its own diagnosis, whatever the first turn's result.
    const lost = { ...turnFailed, status: { ...turnFailed.status, phase: "Failed", conditions: [condition("CellnParentReady", "ContextLost", "parent context unavailable")] } } as AgentRun;
    expect(diagnoseRun(lost)!.kind).to.equal("parent-lost");
    // A later failed turn never gets the first-turn step, and a context overflow is not retried by resending.
    const later = { ...turnFailed, status: { ...turnFailed.status, cellnParent: { ...turnFailed.status!.cellnParent!, initialTurn: initialOK } } } as AgentRun;
    const laterSteps = diagnoseRun(later, [{ metadata: { name: "turn-1" }, spec: { runName: "r", runUID: "run-uid", message: "List everything" }, status: { execution: { ...initialOK, result: { succeeded: false, answer: LENGTH_ANSWER } } } }] as AgentRunTurn[])!.nextSteps;
    expect(laterSteps.some((step) => step.label === "Send another message")).to.equal(false);
    const overflow = { ...turnFailed, status: { ...turnFailed.status, cellnParent: { ...turnFailed.status!.cellnParent!, initialTurn: { ...initialOK, result: { succeeded: false, answer: "CELLN_HARNESS_ERROR context capacity exceeded" } } } } } as AgentRun;
    expect(diagnoseRun(overflow)!.nextSteps.some((step) => step.label === "Send another message")).to.equal(false);
  });

  it("strips harness event JSON from the error line and truncates evidence", () => {
    expect(harnessError(BUDGET_ANSWER)).to.equal("tool call budget exhausted");
    const long = run({ phase: "Failed", error: "x".repeat(5000) }, { executionLifecycle: "one-shot" });
    expect(diagnoseRun(long)!.evidence[0].length).to.be.at.most(600);
  });
});

describe("Why did this fail panel", () => {
  function open(subject: AgentRun, turns: unknown[] = []) {
    cy.intercept("GET", "**/api/v1/**", { body: [] });
    cy.intercept("GET", "**/api/v1/celln-platform/profiles*", { body: profiles }).as("profiles");
    cy.intercept("GET", `**/api/v1/runs/${subject.metadata.name}*`, { body: subject });
    cy.intercept("GET", `**/api/v1/runs/${subject.metadata.name}/turns*`, { body: { runUID: subject.metadata.uid, items: turns, continue: "" } });
    cy.visit(`/runs/${subject.metadata.name}#token=test-token`);
    cy.get('[data-testid="run-diagnosis"]').should("have.length", 1).and("be.visible");
  }

  function evidenceCollapsedThenShows(text: string) {
    cy.get('[data-testid="run-diagnosis-evidence"]').should("have.attr", "aria-expanded", "false");
    cy.get('[data-testid="run-diagnosis-evidence-text"]').should("not.exist");
    cy.get('[data-testid="run-diagnosis-evidence"]').click();
    cy.get('[data-testid="run-diagnosis-evidence-text"]').should("be.visible").and("contain", text);
  }

  it("parent lost: says the conversation moved and links to the continuation", () => {
    open(parentLost);
    cy.get('[data-testid="run-diagnosis"]').should("have.attr", "data-kind", "parent-lost");
    cy.get('[data-testid="run-diagnosis-cause"]').should("contain", "Live harness context was lost").and("contain", "continues as hermes-tzvz6");
    // The conversation view carries the same sentence, from the same function.
    cy.get('[data-testid="celln-parent-lifecycle-detail"]').should("contain", "continues as hermes-tzvz6");
    cy.get('[data-testid="run-diagnosis-continue"]').should("not.exist");
    evidenceCollapsedThenShows("owner=ContextLost reachedReady=true admittedAge=33s");
    cy.get('[data-testid="run-diagnosis-evidence-text"]').should("contain", "ownerOutcome: status=ContextLost");
    cy.intercept("GET", "**/api/v1/runs/hermes-tzvz6*", { body: run({ phase: "Running" }) });
    cy.get('[data-testid="run-diagnosis-link"]').should("contain", "Open hermes-tzvz6").click();
    cy.location("pathname").should("eq", "/runs/hermes-tzvz6");
  });

  it("admission refused: names the unknown tool and points at the Agent's Harness tab", () => {
    open(admissionRefused);
    cy.wait("@profiles");
    cy.get('[data-testid="run-diagnosis-code"]').should("contain", "AUTH_TOOL_UNKNOWN");
    cy.get('[data-testid="run-diagnosis-cause"]').should("contain", "does not lend").and("contain", "shell-exec@r9");
    cy.get('[data-testid="run-diagnosis-step"]').first().should("contain", "Remove shell-exec@r9 from the Agent's tools")
      .find('[data-testid="run-diagnosis-link"] a, a[data-testid="run-diagnosis-link"]').should("have.attr", "href", "/agents/hermes?tab=harness");
    cy.get('[data-testid="run-diagnosis"]').should("not.contain", "Ask the operator");
    cy.get('[data-testid="celln-parent-admission"]').should("contain", "shell-exec@r9");
    evidenceCollapsedThenShows("Platform policy refused admission (AUTH_TOOL_UNKNOWN)");
  });

  it("turn failed with the parent alive: explains the per-turn tool call limit", () => {
    open(turnFailed);
    cy.get('[data-testid="run-diagnosis"]').should("have.attr", "data-kind", "turn-failed");
    cy.get('[data-testid="run-diagnosis-cause"]').should("contain", "per-turn tool call limit");
    cy.get('[data-testid="run-diagnosis-step"]').first().should("contain", "Ask for fewer actions per message");
    cy.get('[data-testid="run-diagnosis-steps"]').should("contain", "Send another message").and("contain", "the conversation is still open").and("not.contain", "not accepting messages");
    cy.get('[data-testid="celln-turn-message"]').should("be.enabled");
    evidenceCollapsedThenShows("CELLN_HARNESS_ERROR tool call budget exhausted");
  });

  it("follow-up turn failed: suggests a shorter answer", () => {
    const alive = { ...turnFailed, status: { ...turnFailed.status, cellnParent: { ...turnFailed.status!.cellnParent!, initialTurn: initialOK } } } as AgentRun;
    open(alive, [{ metadata: { name: "turn-1", uid: "turn-1-uid" }, spec: { runName: alive.metadata.name, runUID: "run-uid", message: "List everything" }, status: { execution: { ...initialOK, result: { succeeded: false, answer: LENGTH_ANSWER } } } }]);
    cy.get('[data-testid="run-diagnosis-cause"]').should("contain", "answer size bound");
    cy.get('[data-testid="run-diagnosis-step"]').first().should("contain", "Ask for a shorter answer").and("contain", "send the next message below");
    cy.get('[data-testid="run-diagnosis-step"]').eq(1).should("contain", "Disable thinking on this Agent's model connection").and("contain", "Disable thinking (reasoning models)");
  });

  it("continuation withheld: explains the loop guard and restarts elsewhere on request", () => {
    open(continuationWithheld);
    cy.get('[data-testid="run-diagnosis"]').should("have.attr", "data-kind", "continuation-withheld");
    cy.get('[data-testid="run-diagnosis-cause"]').should("contain", "already an automatic continuation").and("contain", "avoid a loop");
    cy.get('[data-testid="run-diagnosis-steps"]').should("contain", "Start a new conversation");
    evidenceCollapsedThenShows("CellnContinuation=False (LostBeforeFollowUp)");
    cy.intercept("POST", "**/api/v1/runs/hermes-abc12/continue*", (request) => {
      expect(request.url).to.contain("uid=run-uid");
      request.reply({ statusCode: 201, body: { ...run({ phase: "Pending" }), metadata: { name: "hermes-next", namespace: "default", uid: "next-uid", generation: 1 } } });
    }).as("continue");
    cy.intercept("GET", "**/api/v1/runs/hermes-next*", { body: { ...run({ phase: "Pending" }), metadata: { name: "hermes-next", namespace: "default", uid: "next-uid", generation: 1 } } });
    cy.get('[data-testid="run-diagnosis-continue"]').should("contain", "Restart elsewhere").click();
    cy.wait("@continue");
    cy.location("pathname").should("eq", "/runs/hermes-next");
  });

  it("stays out of the way of a healthy run", () => {
    cy.intercept("GET", "**/api/v1/**", { body: [] });
    const healthy = run({ phase: "Running", conditions: [condition("CellnParentReady", "Ready", "ok", "True")], cellnParent: { acceptedTurns: 0, createAttempted: true, initialTurn: initialOK } });
    cy.intercept("GET", "**/api/v1/runs/hermes-abc12*", { body: healthy });
    cy.intercept("GET", "**/api/v1/runs/hermes-abc12/turns*", { body: { runUID: "run-uid", items: [], continue: "" } });
    cy.visit("/runs/hermes-abc12#token=test-token");
    cy.get('[data-testid="celln-conversation"]').should("be.visible");
    cy.get('[data-testid="run-diagnosis"]').should("not.exist");
  });
});
