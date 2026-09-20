// "Why did this fail": turns the conditions, owner outcome and turn results a
// run already carries into one plain explanation and what to do about it. Pure
// and browser-only — it reads nothing the API has not already returned, and it
// is the single source for failure wording in the console.
import type { AgentRun, AgentRunTurn, CellnMediation, CellnPlatformProfile, Condition } from "@/lib/api";

export type DiagnosisSeverity = "error" | "warning" | "info";

export type DiagnosisKind =
  | "admission-refused"
  | "admission-pending"
  | "mediation-disabled"
  | "continuation-withheld"
  | "parent-lost"
  | "create-refused"
  | "reconciliation-required"
  | "turn-failed"
  | "run-failed";

export type DiagnosisAction =
  | { kind: "link"; label: string; to: string }
  /** Restart the conversation on a new parent (api.runs.continue); the component decides whether it applies. */
  | { kind: "continue"; label: string };

export interface DiagnosisStep {
  label: string;
  detail: string;
  action?: DiagnosisAction;
}

export interface Diagnosis {
  kind: DiagnosisKind;
  severity: DiagnosisSeverity;
  title: string;
  /** One plain sentence. */
  cause: string;
  /** Stable machine code when there is one (AUTH_*, owner status, harness error). */
  code?: string;
  /** Raw condition / outcome text, truncated. */
  evidence: string[];
  nextSteps: DiagnosisStep[];
}

export interface DiagnosisContext {
  /** Fleet runtime profiles (api.cellnPlatform.profiles) — lets a tool refusal name the tool. */
  profiles?: CellnPlatformProfile[];
  /** Provider routes declared for the run's namespace (api.cellnPlatform.mediation) — lets a route refusal say whether any route matches. */
  mediation?: CellnMediation;
}

const EVIDENCE_LIMIT = 600;
const LOST_STATUSES = new Set(["ContextLost", "Stopped", "TeardownUncertain"]);

function truncate(text: string, limit = EVIDENCE_LIMIT): string {
  const clean = text.trim();
  return clean.length > limit ? `${clean.slice(0, limit - 1)}…` : clean;
}

/** A condition counts only for the generation it observed, as everywhere else in the console. */
function currentConditions(run: AgentRun): Condition[] {
  const generation = run.metadata.generation;
  return (run.status?.conditions || []).filter((condition) =>
    generation === undefined || condition.observedGeneration === undefined || condition.observedGeneration === generation);
}

function conditionText(condition: Condition): string {
  return `${condition.type}=${condition.status}${condition.reason ? ` (${condition.reason})` : ""}${condition.message ? `: ${condition.message}` : ""}`;
}

function agentTab(run: AgentRun, tab: "harness" | "chat"): string {
  return `/agents/${encodeURIComponent(run.spec.agentRef)}?tab=${tab}`;
}

function harnessStep(run: AgentRun, label: string, detail: string): DiagnosisStep {
  return { label, detail, action: { kind: "link", label: "Open the Agent's Harness tab", to: agentTab(run, "harness") } };
}

function newConversationStep(run: AgentRun, detail: string): DiagnosisStep {
  return { label: "Start a new conversation", detail, action: { kind: "link", label: "Open the Agent's Chat tab", to: agentTab(run, "chat") } };
}

// A Celln Agent owns its model backend, so the remedies below edit THIS
// Agent's model connection (Agent → Harness → Model backend → Edit). A
// conversation already running stays pinned to the connection as it was.
const CONNECTION_EDIT = "Agent → Harness → Model backend → Edit";
const NEW_CONVERSATION = "The change applies to new conversations: one already running is pinned to the old settings and the gateway refuses its next request (MODEL_ROUTE_CHANGED), so start a new conversation afterwards.";

/** The remedy for a reasoning model that returns nothing: send the provider's "no thinking" parameter. */
function thinkingOffStep(run: AgentRun): DiagnosisStep {
  return harnessStep(run, "Disable thinking on this Agent's model connection",
    `${CONNECTION_EDIT}, then tick “Disable thinking (reasoning models)” under “Advanced: model parameters” (it sends {"chat_template_kwargs":{"enable_thinking":false}} with every request; other providers take their own switch in the JSON parameters). ${NEW_CONVERSATION}`);
}

/** The other remedy for a reasoning model that returns nothing: more output tokens per request. */
function raiseOutputTokensStep(run: AgentRun): DiagnosisStep {
  return harnessStep(run, "Or raise this Agent's max output tokens per request",
    `To keep the model thinking, ${CONNECTION_EDIT} and raise “Max output tokens per request” (2048–4096 for a reasoning model; the default is 512). A turn reserves 6 requests of it, so it costs 4–8× the tokens per turn and buys fewer turns under the policy's ceilings; the turn's time limit grows with it (60 seconds at 512, 4 minutes at 2048, 5 minutes at most). ${NEW_CONVERSATION}`);
}

/** The remedy for an answer longer than the answer size bound when the connection's output cap was raised. */
function lowerOutputTokensStep(run: AgentRun): DiagnosisStep {
  return harnessStep(run, "Or lower this Agent's max output tokens per request",
    `A raised “Max output tokens per request” lets the model write more than an answer may hold. ${CONNECTION_EDIT} and lower it (the default 512 always fits). ${NEW_CONVERSATION}`);
}

const replaceRunStep: DiagnosisStep = {
  label: "Delete this run, then start a new one",
  detail: "A run keeps the selection it was created with, so the fix only reaches a new run. Delete this pending one first so two runs never compete for the same admission.",
};

const waitStep: DiagnosisStep = {
  label: "Leave this run in place",
  detail: "Admission is retried automatically; once the policy side is fixed this run starts on its own. Do not create a replacement while it is still pending.",
};

// ── Admission refusals ───────────────────────────────────────────────────────

interface AuthEntry {
  title: string;
  cause: (facts: AuthFacts) => string;
  steps: (run: AgentRun, facts: AuthFacts) => DiagnosisStep[];
}

interface AuthFacts {
  code: string;
  /** The resolver's own sentence, when the controller could share it. */
  detail: string;
  /** Tools the run borrows that the fleet profile does not lend. */
  unknownTools: string[];
  profile?: CellnPlatformProfile;
  /** What the run asked for, and what the operator declared, for AUTH_ROUTE_MISMATCH. */
  route?: RouteFacts;
}

interface RouteFacts {
  provider: string;
  protocol: string;
  model: string;
  origin: string;
  connection: string;
  /** Known only once the namespace's declared routes were read. */
  declared?: boolean;
  mediationEnabled?: boolean;
  offered: string[];
}

/**
 * One entry per reason code the platform resolver can return
 * (internal/cellnauthority/platform_resolver.go) plus AUTH_PROTOCOL_UNSUPPORTED.
 * The person reading this usually IS the operator, so every entry says what to
 * change, not whom to ask.
 */
export const AUTH_REASONS: Record<string, AuthEntry> = {
  AUTH_TOOL_UNKNOWN: {
    title: "Admission refused: a tool is not lent by the fleet policy",
    cause: ({ unknownTools }) => unknownTools.length
      ? `The run borrows ${unknownTools.length === 1 ? "a tool" : "tools"} the fleet policy does not lend at that revision: ${unknownTools.join(", ")}.`
      : "The run borrows a tool the fleet policy does not lend (unknown tool, or a revision the policy does not list).",
    steps: (run, { unknownTools, profile }) => [
      harnessStep(run, unknownTools.length ? `Remove ${unknownTools.join(", ")} from the Agent's tools` : "Remove the unknown tool from the Agent's tools",
        profile?.tools.length
          ? `Agent → Harness. The profile "${profile.name}" lends only: ${profile.tools.map((tool) => `${tool.name}@${tool.revision}`).join(", ")}.`
          : "Agent → Harness. Keep only tools the fleet profile lists, at the exact revision it lists."),
      { label: "Or lend the tool", detail: "If the run should have it, add the tool and revision to the CellnExecutionPolicy that selects this namespace (kubectl edit cellnexecutionpolicy)." },
      replaceRunStep,
    ],
  },
  AUTH_TOOL_ORDER_MISMATCH: {
    title: "Admission refused: the tool selection is not exact",
    cause: () => "The run's tool list repeats a name, omits an exact revision, or changed between being resolved and being admitted.",
    steps: (run) => [
      harnessStep(run, "Re-save the Agent's tools", "Agent → Harness. Pick each tool once, at the revision the fleet profile lists."),
      replaceRunStep,
    ],
  },
  AUTH_POLICY_CONTRACTED: {
    title: "Admission refused: the run no longer matches what policy grants",
    cause: ({ detail }) => /persona/i.test(detail)
      ? "The run's system prompt differs from the persona bound to its fleet runtime profile."
      : detail
      ? `The run asks for something the fleet policy or runtime profile does not grant: ${detail}.`
      : "The run asks for something the fleet policy or runtime profile does not grant, or one of them changed while the run was being admitted.",
    steps: (run, { detail, profile }) => /persona/i.test(detail) ? [
      newConversationStep(run, "The Chat tab sends the profile's system prompt verbatim, which is what the policy requires."),
      { label: "Creating runs through the API?", detail: `Send spec.systemPrompt exactly as the profile${profile ? ` "${profile.name}"` : ""} returns it from /api/v1/celln-platform/profiles — no edits, no extra whitespace.` },
      replaceRunStep,
    ] : [
      harnessStep(run, "Check what the Agent asks for", "Agent → Harness. The runtime, lifecycle, tools and conversation budget must be ones the profile and policy grant; an Agent created before the fleet moved to a new package keeps the old profile revision, and creating it again (Create Agent → Celln) binds the current one."),
      { label: "Check the policy still permits this", detail: "The CellnExecutionPolicy selecting this namespace must allow the runtime profile revision and the enduring lifecycle (kubectl get cellnexecutionpolicy -o yaml)." },
      replaceRunStep,
    ],
  },
  AUTH_POLICY_WITHDRAWN: {
    title: "Admission refused: no fleet policy covers this run",
    cause: ({ detail }) => detail
      ? `Nothing currently authorises this run: ${detail}.`
      : "Nothing currently authorises this run: no execution policy selects its namespace, or the Agent, runtime or runtime profile it depends on is missing.",
    steps: (run) => [
      { label: "Make a policy select this namespace", detail: `Label namespace "${run.metadata.namespace || "default"}" so a CellnExecutionPolicy's namespaceSelector matches it, or widen the selector (kubectl get cellnexecutionpolicy -o yaml).` },
      harnessStep(run, "Check the Agent's runtime still exists", "Agent → Harness. If the AgentRuntime wrapper or its fleet profile was removed, Create Agent → Celln creates the wrapper in this namespace again."),
      waitStep,
    ],
  },
  AUTH_ROUTE_MISMATCH: {
    title: "Admission refused: the model route is not permitted",
    cause: ({ detail, route }) => {
      const asked = route ? `${route.provider} (${route.protocol}), model ${route.model || "unset"}, on ${route.origin || "an unreadable endpoint"}` : "";
      if (/does not grant the model connection's Secret/i.test(detail)) return `The run names model connection ${route?.connection || "of another owner"}, whose Secret this Agent was not given: ${detail}.`;
      if (route?.declared === false) return `The run asks for ${asked}, and no route an operator declared for this namespace matches it exactly${route.mediationEnabled === false ? " (mediated model access is disabled on this cluster)" : ""}. ${route.offered.length ? `Declared: ${route.offered.join("; ")}.` : "No provider is declared for this namespace."}`;
      if (detail) return `The run's model route is not one the policy permits: ${detail}.`;
      return route?.declared
        ? `The run asks for ${asked}, which an operator declared, so the route itself is allowed. What is left: the connection ${route.connection} is disabled, invalid or no longer lists that model, it changed after the run was created, or the run names a connection whose Secret the Agent was not given (authRefs).`
        : `The run's model connection and model${asked ? ` (${asked})` : ""} are not the exact route the policy permits: provider, protocol, model and endpoint origin must all match one declared route, the connection must be enabled and list the model, and the Agent must be given the connection's Secret.`;
    },
    steps: (run, { route, detail }) => [
      ...(route?.declared === false ? [
        { label: "Declare the route, or pick a declared one", detail: `An operator declares it with: sympozium install --celln-fleet … --celln-mediated-route provider=${route.provider},protocol=${route.protocol},origin=${route.origin || "https://…"},models=${route.model || "…"} (or celln.mediation.routes in the values, then sympozium celln-mediation apply-routes). Matching is exact on all four; sympozium doctor lists what is declared. Otherwise create the Agent on a provider and model that are declared (Create Agent → Celln offers only those).` },
      ] : []),
      ...(route?.declared !== false || /does not grant/i.test(detail) ? [
        harnessStep(run, "Run on this Agent's own connection", "Agent → Harness → Model backend shows the connection and Secret this Agent owns. A run must name that connection (the Chat tab does); a run that names another Agent's Secret-backed connection is refused unless this Agent's authRefs list that Secret."),
      ] : []),
      replaceRunStep,
    ],
  },
  AUTH_LIMIT_OUT_OF_RANGE: {
    title: "Admission refused: a limit is outside what policy allows",
    cause: ({ detail }) => detail
      ? `The run asks for a budget or size the policy does not allow: ${detail}.`
      : "The run's task size or requested budget (lease, turns, model requests, output tokens) is outside the policy's range.",
    steps: (run, { profile }) => [
      { label: "Ask for less, or a shorter task", detail: profile
        ? `The profile "${profile.name}" allows at most ${profile.ceilings.maxTurns} turns, ${profile.ceilings.maxModelRequests} model requests, ${profile.ceilings.maxOutputTokens} output tokens and a ${profile.ceilings.leaseSeconds}s lease — and the budget must still afford one full turn.`
        : "Keep spec.enduring within the profile's ceilings while still affording one full turn, and keep the first message short." },
      newConversationStep(run, "The Chat tab asks for the profile's default budget, which is always in range."),
      replaceRunStep,
    ],
  },
  AUTH_LIFECYCLE_INVALID: {
    title: "Admission refused: the run asks for something fleet runs cannot do",
    cause: ({ detail }) => detail
      ? `Fleet execution does not support what this run or its Agent requests: ${detail}.`
      : "The run or its Agent requests pods, delegation, lifecycle hooks, extra context or a non-text task, which fleet execution does not support.",
    steps: (run) => [
      harnessStep(run, "Remove the unsupported settings", "Agent → Harness / Lifecycle. Drop lifecycle hooks, delegation and pod-level settings, and send a plain text task — or run this Agent on a non-fleet backend."),
      replaceRunStep,
    ],
  },
  AUTH_NAMESPACE_UID_MISMATCH: {
    title: "Admission refused: the namespace changed identity",
    cause: () => "The namespace was deleted and recreated (or changed) while this run was being admitted, so the grant no longer names it.",
    steps: () => [replaceRunStep],
  },
  AUTH_PARENT_TURN_MISMATCH: {
    title: "Admission refused: the parent or turn identity does not match",
    cause: () => "The turn or parent this admission was issued for is not the one now asking — typically a run or turn that was recreated under the same name.",
    steps: (run) => [newConversationStep(run, "The old identity cannot be re-admitted; a new conversation gets its own."), replaceRunStep],
  },
  AUTH_PROTOCOL_UNSUPPORTED: {
    title: "Admission refused: shared tools need fleet-mediated admission",
    cause: () => "The run selects shared catalogue tools but reached the legacy admission path, which cannot honour them and refuses rather than dropping them.",
    steps: (run) => [
      { label: "Enable fleet admission for this install", detail: "Shared (cluster) tools are only admitted through the platform resolver; check that the controller runs with Celln platform admission configured." },
      harnessStep(run, "Or remove the shared tools", "Agent → Harness. Without clusterToolRefs the run can use the legacy path."),
      replaceRunStep,
    ],
  },
};

function defaultAuthEntry(code: string): AuthEntry {
  const credential = code.startsWith("AUTH_CRED_") || ["AUTH_ISS_MISMATCH", "AUTH_AUD_MISMATCH", "AUTH_SUBJECT_MISMATCH", "AUTH_OPERATION_MISMATCH", "AUTH_DECISION_DIGEST_MISMATCH", "AUTH_VERSION_UNSUPPORTED"].includes(code);
  const expired = /EXPIRED|TIME_|WINDOW/.test(code);
  return {
    title: `Admission refused (${code})`,
    cause: ({ detail }) => credential
      ? "The admission credential the controller presented was not accepted by the node (wrong key, issuer or audience)."
      : expired
      ? "The admission was issued but not used inside its time window, usually because clocks differ or the node was slow to respond."
      : detail ? `Platform policy refused this run: ${detail}.` : `Platform policy refused this run with ${code}.`,
    steps: () => credential ? [
      { label: "Check the fleet signing key", detail: "The controller's issuer key and the dispatcher's trusted key must be the same generation; re-run the fleet install step that distributes it." },
      waitStep,
    ] : expired ? [
      { label: "Check node clocks and load", detail: "Make sure controller and node clocks agree (NTP) and the dispatcher is responsive." },
      waitStep,
    ] : [
      { label: "Compare the run with its fleet profile", detail: "Namespace, runtime profile, tools and model route must all be ones the CellnExecutionPolicy permits." },
      waitStep,
    ],
  };
}

const REFUSAL = /refused admission \((AUTH_[A-Z0-9_]+)\)(?::\s*(.*?))?\.\s+Ask the operator/s;

export function parseAdmissionRefusal(message: string | undefined): { code: string; detail: string } | null {
  if (!message) return null;
  const match = REFUSAL.exec(message);
  if (match) return { code: match[1], detail: (match[2] || "").trim() };
  const bare = /\b(AUTH_[A-Z0-9_]+)\b(?::\s*([^\n]*))?/.exec(message);
  return bare ? { code: bare[1], detail: (bare[2] || "").trim().replace(/\.$/, "") } : null;
}

function profileFor(run: AgentRun, profiles: CellnPlatformProfile[] | undefined): CellnPlatformProfile | undefined {
  const runtime = run.spec.cellnSelection?.runtimeRef;
  return profiles?.find((profile) => (runtime && profile.wrapper === runtime) || profile.agent === run.spec.agentRef);
}

/** Names the refused tool: from the resolver's sentence if shared, else by comparing the selection with what the profile lends. */
export function unknownTools(run: AgentRun, detail: string, profile: CellnPlatformProfile | undefined): string[] {
  const named = [...detail.matchAll(/tool "([^"]+)"/g)].map((match) => match[1]);
  if (named.length) return [...new Set(named)];
  if (!profile) return [];
  const lent = new Set(profile.tools.map((tool) => `${tool.name}@${tool.revision}`));
  const selection = run.spec.cellnSelection;
  return [
    ...(selection?.clusterToolRefs || []).filter((tool) => !lent.has(`${tool.name}@${tool.revision}`)).map((tool) => `${tool.name}@${tool.revision}`),
    // Legacy namespaced tools are never lent by a shared profile.
    ...(selection?.clusterToolRefs?.length ? [] : (selection?.toolRefs || []).map((tool) => `${tool.name}@${tool.revision}`)),
  ];
}

function diagnoseAdmission(run: AgentRun, condition: Condition, context: DiagnosisContext): Diagnosis {
  const refusal = parseAdmissionRefusal(condition.message);
  const evidence = [truncate(conditionText(condition))];
  if (!refusal) {
    return {
      kind: "admission-pending", severity: "info", title: "Waiting for admission",
      cause: "No operator-prepared parent registration with current grants matches this run yet, so nothing has started.",
      evidence,
      nextSteps: [
        { label: "Check that this Agent runs on the Celln fleet", detail: "A run is admitted either by fleet policy or by a parent registration prepared for it on a node. If neither exists, it waits here.", action: { kind: "link", label: "Open the Agent's Harness tab", to: agentTab(run, "harness") } },
        waitStep,
      ],
    };
  }
  return refusedDiagnosis(run, refusal, evidence, context);
}

function refusedDiagnosis(run: AgentRun, refusal: { code: string; detail: string }, evidence: string[], context: DiagnosisContext): Diagnosis {
  const profile = profileFor(run, context.profiles);
  const facts: AuthFacts = { ...refusal, profile, unknownTools: refusal.code === "AUTH_TOOL_UNKNOWN" ? unknownTools(run, refusal.detail, profile) : [], route: refusal.code === "AUTH_ROUTE_MISMATCH" ? routeFacts(run, context.mediation) : undefined };
  const entry = AUTH_REASONS[refusal.code] || defaultAuthEntry(refusal.code);
  return { kind: "admission-refused", severity: "error", code: refusal.code, title: entry.title, cause: entry.cause(facts), evidence, nextSteps: entry.steps(run, facts) };
}

/** The route a run asked for (the controller freezes it into spec.model) against the namespace's declared routes. */
function routeFacts(run: AgentRun, mediation: CellnMediation | undefined): RouteFacts | undefined {
  const model = run.spec.model;
  if (!model?.connectionRef) return undefined;
  let origin = "";
  try { origin = model.baseURL ? new URL(model.baseURL).origin : ""; } catch { /* reported as unreadable */ }
  const facts: RouteFacts = { provider: model.provider || "", protocol: model.protocol || "", model: model.model || "", origin, connection: model.connectionRef, offered: [] };
  // Without the frozen route there is nothing to compare; say only what is known.
  if (!mediation || !facts.provider || !facts.protocol || !origin) return facts;
  return {
    ...facts,
    mediationEnabled: mediation.enabled,
    declared: mediation.routes.some((route) => route.provider === facts.provider && route.protocol === facts.protocol && route.models.includes(facts.model) && route.endpointOrigins.includes(origin)),
    offered: mediation.routes.map((route) => `${route.provider} (${route.protocol}) ${route.models.join(", ")} on ${route.endpointOrigins.join(", ")}`),
  };
}

// ── Gateway-mediated (scoped) runs ───────────────────────────────────────────

/**
 * A run on an Agent's own key is executed by the scoped receiver and reports
 * through the CellnScopedExecution condition (internal/controller/celln_scoped.go).
 * A policy refusal there carries the reason code only: the controller never
 * shares the resolver's sentence on this path.
 */
const SCOPED_BOILERPLATE = /scoped authority refused before receiver enrollment; no native execution started/i;

function diagnoseScoped(run: AgentRun, scoped: Condition, context: DiagnosisContext): Diagnosis | null {
  const evidence = [truncate(conditionText(scoped)), run.status?.error ? truncate(`status.error: ${run.status.error}`) : ""].filter(Boolean);
  if (scoped.reason === "ScopedDispatchDisabled") {
    return {
      kind: "mediation-disabled", severity: "warning", code: "ScopedDispatchDisabled", title: "Waiting: mediated model access is not enabled",
      cause: "This Agent brings its own provider key, which only the model gateway may use. The controller has no scoped receiver configured (celln.mediation.enabled is off), so the run is held and nothing was started; it is never sent to a fleet backend instead.",
      evidence,
      nextSteps: [
        { label: "Enable mediated model access", detail: "An operator bootstraps the trust (sympozium celln-mediation bootstrap), enables celln.mediation.enabled with the model gateway, and declares the providers Agents may bring a key for (--celln-mediated-route). See docs/guides/celln-mediated-model-access.md; sympozium doctor reports “Mediated model access”." },
        waitStep,
      ],
    };
  }
  if (scoped.reason !== "AdmissionRefused") return null;
  const refusal = parseAdmissionRefusal(scoped.message);
  if (!refusal) return null;
  return refusedDiagnosis(run, { code: refusal.code, detail: SCOPED_BOILERPLATE.test(refusal.detail) ? "" : refusal.detail }, evidence, context);
}

// ── Lost parents ─────────────────────────────────────────────────────────────

function lostEvidence(run: AgentRun, conditions: (Condition | undefined)[]): string[] {
  const outcome = run.status?.cellnParent?.ownerOutcome;
  return [
    ...conditions.filter((condition): condition is Condition => Boolean(condition)).map(conditionText),
    outcome ? `ownerOutcome: status=${outcome.status} reachedReady=${outcome.reachedReady}${outcome.observedAt ? ` observedAt=${outcome.observedAt}` : ""}` : "",
    run.status?.error ? `status.error: ${run.status.error}` : "",
  ].filter(Boolean).map((text) => truncate(text));
}

const restartStep: DiagnosisStep = {
  label: "Restart the conversation elsewhere",
  detail: "Starts a new parent on any node with capacity, seeded with the recorded exchanges. This run is removed once the new one exists.",
  action: { kind: "continue", label: "Restart elsewhere" },
};

function diagnoseLost(run: AgentRun, status: string, ready: Condition | undefined, withheld: Condition | undefined): Diagnosis {
  const parent = run.status?.cellnParent;
  const continuedBy = parent?.continuedBy;
  const neverReady = parent?.ownerOutcome && !parent.ownerOutcome.reachedReady;
  const evidence = lostEvidence(run, [withheld, ready]);
  if (withheld) {
    return {
      kind: "continuation-withheld", severity: "error", code: withheld.reason, title: "Lost again — not continued automatically",
      cause: "This run was already an automatic continuation and lost its parent before taking a single follow-up, so the controller stopped continuing it to avoid a loop.",
      evidence,
      nextSteps: [
        { label: "Check the node before retrying", detail: "Two losses in a row usually mean the node cannot keep a parent alive (memory pressure, a draining node, or a harness that exits on start). Look at the dispatcher on that node first." },
        newConversationStep(run, "Recorded answers stay on this run; a new conversation starts clean on any node with capacity."),
        restartStep,
      ],
    };
  }
  if (status === "TeardownUncertain") {
    return {
      kind: "parent-lost", severity: "warning", code: status, title: "Parent teardown is unconfirmed",
      cause: "Parent teardown is unconfirmed: the node that owned this parent has not confirmed it stopped, so it may still hold memory and a model slot.",
      evidence,
      nextSteps: [
        { label: "Confirm the old parent is gone", detail: "Check the dispatcher on the owning node (or wait for the node to report back). This run is not continued automatically, because the old parent may still be live." },
        newConversationStep(run, "Once the node confirms, or if you accept the old parent may linger until its lease ends, start a new conversation. Recorded answers stay here."),
      ],
    };
  }
  const base = { kind: "parent-lost" as const, code: status, evidence };
  if (continuedBy) {
    return {
      ...base, severity: "info", title: status === "Stopped" ? "Parent stopped — conversation continued" : "Context lost — conversation continued",
      cause: status === "Stopped"
        ? `The parent has stopped; the conversation continues as ${continuedBy} on another node, seeded with what was said here.`
        : `Live harness context was lost; the conversation continues as ${continuedBy} on another node, seeded with what was said here.`,
      nextSteps: [{
        label: "Carry on in the continuation", detail: "Nothing to fix: send your next message there. It remembers the recorded exchanges, not the harness's unsaved working state.",
        action: { kind: "link", label: `Open ${continuedBy}`, to: `/runs/${encodeURIComponent(continuedBy)}` },
      }],
    };
  }
  return {
    ...base, severity: "error", title: status === "Stopped" ? "The parent has stopped" : "Live harness context was lost",
    cause: status === "Stopped"
      ? "The parent has stopped. Recorded answers remain available; this conversation cannot accept more turns."
      : neverReady
      ? "Live harness context was lost while the parent was still warming up, before it was ever ready. Recorded answers remain available, but this parent cannot resume."
      : "Live harness context was lost. Recorded answers remain available, but this parent cannot resume.",
    nextSteps: [
      restartStep,
      ...(status === "Stopped" ? [{ label: "If this was the lease", detail: "A parent stops when its lease or turn budget runs out, or when its node drains. A restarted conversation gets a fresh lease under policy." }] : []),
      ...(neverReady ? [{ label: "If it keeps dying on start", detail: "A parent that never reaches Ready points at the node or harness image rather than the conversation — check the dispatcher log on the owning node." }] : []),
    ],
  };
}

// ── Failed turns ─────────────────────────────────────────────────────────────

/**
 * closed: the parent takes no further message, so a retry needs a new
 * conversation. A failed turn alone never closes a parent — the owner keeps
 * its context after a failed result, first turn or later — so this is true
 * only for a failed first turn whose parent is not reported Ready.
 * resendPointless: another message would fail the same way.
 */
interface HarnessEntry { match: RegExp; title: string; cause: string; resendPointless?: boolean; steps: (run: AgentRun, closed: boolean) => DiagnosisStep[] }

function retryStep(run: AgentRun, closed: boolean, label: string, detail: string): DiagnosisStep {
  return closed
    ? { ...newConversationStep(run, `${detail} The first turn failed and this parent is not accepting messages.`), label }
    : { label, detail: `${detail} The parent is still alive — send the next message below.` };
}

const sendAnotherStep: DiagnosisStep = {
  label: "Send another message",
  detail: "The first turn failed; the conversation is still open — send another message below. The failed turn still counts toward this conversation's turn limit.",
};

export const HARNESS_ERRORS: HarnessEntry[] = [
  // The model gateway's refusals (internal/modelgateway/types.go). The gateway
  // answers the worker's model request with the bare reason code.
  {
    match: /MODEL_ROUTE_CHANGED/,
    title: "Turn failed: this Agent's model connection changed mid-conversation",
    cause: "The conversation is pinned to the Agent's model connection as it was when the run was admitted. The connection has since been edited (parameters, max output tokens, the Secret it names, its endpoint or models), disabled or deleted, so the model gateway refuses every further request of this conversation instead of silently moving it to the new settings.",
    resendPointless: true,
    steps: (run) => [
      newConversationStep(run, "A new conversation is admitted against the connection as it is now. Nothing of this one is lost: recorded answers stay on this run."),
      harnessStep(run, "Check the connection is what you intend", "Agent → Harness → Model backend shows the provider, model, Secret name, parameters and max output tokens new conversations will use."),
    ],
  },
  {
    match: /MODEL_CREDENTIAL_SOURCE_CHANGED/,
    title: "Turn failed: this Agent's key Secret was replaced or emptied",
    cause: "The conversation is pinned to the exact Secret object that held the Agent's key. That Secret was deleted and created again, emptied, or the connection now names another one, so the model gateway refuses this conversation's requests. (Updating the key inside the same Secret does not end a conversation.)",
    resendPointless: true,
    steps: (run) => [
      harnessStep(run, "Make sure the Agent has a usable key", "Agent → Harness → Model backend → Edit → Replace the key: paste a new key, or link a Secret that holds it under the protocol's fixed key name."),
      newConversationStep(run, "A new conversation pins the Secret as it is now."),
    ],
  },
  {
    match: /MODEL_AUTH_FORBIDDEN/,
    title: "Turn failed: the model gateway refused the request",
    cause: "The request asked for more output tokens than this Agent's model connection allows per request (or than the turn was funded for), or it tried to set a request field that the connection's parameters pin. The gateway refuses rather than trimming the request. It answers the same for a run it has no registration for.",
    steps: (run, closed) => [
      harnessStep(run, "Check the connection's limits", `${CONNECTION_EDIT}: “Max output tokens per request” is the most one request may ask for (512 unless raised), and a key under “Parameters (JSON)” cannot also be set by the agent's request. ${NEW_CONVERSATION}`),
      retryStep(run, closed, "Then send the message again", "Nothing was committed, so the turn is not replayed automatically."),
    ],
  },
  {
    match: /tool call budget exhausted/i,
    title: "Turn failed: too many tool calls for one turn",
    cause: "The agent used up the per-turn tool call limit before it produced an answer, so the turn was discarded.",
    steps: (run, closed) => [
      retryStep(run, closed, "Ask for fewer actions per message", "Every turn has a tool call limit, set by the fleet's starter package. Split the request (“fetch X”, then “now summarise it”) so each message stays inside the turn's tool call limit."),
      { label: "Nothing was committed", detail: "A failed turn records no answer; work the tools already did is not replayed." },
    ],
  },
  {
    // Newer Celln releases say which of the two it was.
    match: /final answer is empty: the model used its whole output budget/i,
    title: "Turn failed: the model spent its output budget before answering",
    cause: "The model used the whole output budget of a request without writing an answer, so no result was committed. A request may produce 512 output tokens unless this Agent's model connection raises it; a reasoning model (Qwen, DeepSeek-R1 and the like) can spend them all thinking and return nothing.",
    steps: (run, closed) => [
      thinkingOffStep(run),
      raiseOutputTokensStep(run),
      retryStep(run, closed, "Then send the message again", "Nothing was committed, so the turn is not replayed automatically. Rephrasing alone rarely helps while the model still thinks first."),
    ],
  },
  {
    // A Celln newer than v0.5.23 names the over-long answer on its own.
    match: /final answer exceeds/i,
    title: "Turn failed: the answer was too long",
    cause: "The agent's final answer exceeded the turn's answer size bound, so no result was committed. A model connection that allows many output tokens per request lets the model write more than an answer may hold.",
    steps: (run, closed) => [
      retryStep(run, closed, "Ask for a shorter answer", "Ask for a summary, a fixed number of bullet points, or one part at a time."),
      lowerOutputTokensStep(run),
    ],
  },
  {
    match: /final answer is empty/i,
    title: "Turn failed: the answer was empty or too long",
    cause: "The agent's final answer was empty or exceeded the turn's answer size bound, so no result was committed. An empty answer usually means a reasoning model spent the request's whole output budget (512 tokens unless the Agent's model connection raises it) thinking.",
    steps: (run, closed) => [
      retryStep(run, closed, "Ask for a shorter answer", "Ask for a summary, a fixed number of bullet points, or one part at a time."),
      thinkingOffStep(run),
      raiseOutputTokensStep(run),
      { label: "If answers are routinely cut", detail: "The answer size bound is fixed by the fleet's Celln starter package, not by this conversation's budget. A fleet still on an older package has a smaller bound until its operator moves it to a current one." },
    ],
  },
  {
    match: /child timed out/i,
    title: "Turn failed: the turn ran out of time",
    cause: "The worker cell was stopped at the turn's time limit before the model finished, so no result was committed. The limit covers every model request and tool call of the turn: 60 seconds with the default 512 output tokens per request, longer when the Agent's model connection allows more. A slow local model, or a reasoning model thinking at length, runs past it.",
    steps: (run, closed) => [
      retryStep(run, closed, "Ask for less in one message", "A shorter answer, or one tool action per message, finishes sooner."),
      thinkingOffStep(run),
      { label: "If the connection already allows more output tokens", detail: "The turn limit only grows with the cap on a fleet whose Celln is newer than v0.5.24. On an older fleet every turn has 60 seconds whatever the cap; an operator moves the fleet to a current release." },
    ],
  },
  {
    match: /context capacity exceeded|context (window|length)/i,
    title: "Turn failed: the conversation no longer fits the model's context",
    cause: "The retained conversation plus this message exceeded the harness's context capacity.",
    resendPointless: true,
    steps: (run) => [restartStep, newConversationStep(run, "Or start clean if the earlier exchanges are no longer needed.")],
  },
  {
    match: /cancel/i,
    title: "Turn cancelled",
    cause: "The turn was cancelled before it committed a result; the parent itself was not stopped.",
    steps: (run, closed) => [retryStep(run, closed, "Send the message again if you still need it", "A cancelled turn records no answer and is never replayed.")],
  },
];

/** The harness's own last error line, without the event JSON that precedes it. */
export function harnessError(answer: string): string {
  const marker = answer.lastIndexOf("CELLN_HARNESS_ERROR");
  return (marker >= 0 ? answer.slice(marker + "CELLN_HARNESS_ERROR".length) : answer).trim();
}

function failedTurn(run: AgentRun, turns: AgentRunTurn[]): { answer: string; initial: boolean } | null {
  const ordered = [...turns].sort((a, b) => (a.metadata.creationTimestamp || "").localeCompare(b.metadata.creationTimestamp || "") || a.metadata.name.localeCompare(b.metadata.name));
  const last = ordered[ordered.length - 1]?.status?.execution?.result;
  // Only the latest exchange matters: an older failure followed by an answer needs no explanation.
  if (last) return last.succeeded ? null : { answer: last.answer, initial: false };
  if (ordered.length) return null;
  const initial = run.status?.cellnParent?.initialTurn?.result;
  return initial && !initial.succeeded ? { answer: initial.answer, initial: true } : null;
}

function diagnoseTurn(run: AgentRun, failure: { answer: string; initial: boolean }, accepting: boolean): Diagnosis {
  const reason = harnessError(failure.answer);
  const entry = HARNESS_ERRORS.find((candidate) => candidate.match.test(reason));
  const evidence = [truncate(`${failure.initial ? "initialTurn" : "turn"}.result.answer: ${failure.answer}`)];
  const closed = failure.initial && !accepting;
  const open = failure.initial && accepting;
  if (entry) {
    return { kind: "turn-failed", severity: "warning", code: reason.slice(0, 80), title: entry.title, cause: entry.cause, evidence,
      nextSteps: [...entry.steps(run, closed), ...(open && !entry.resendPointless ? [sendAnotherStep] : [])] };
  }
  return {
    kind: "turn-failed", severity: "warning", title: failure.initial ? "The first turn failed" : "The last turn failed",
    cause: reason ? `The turn ended without a committed answer: ${truncate(reason, 200)}` : "The turn ended without a committed answer.",
    evidence,
    nextSteps: [retryStep(run, closed, "Rephrase and try again", "Nothing was committed, so the turn is not replayed automatically."), ...(open ? [sendAnotherStep] : [])],
  };
}

// ── Entry point ──────────────────────────────────────────────────────────────

export function diagnoseRun(run: AgentRun, turns: AgentRunTurn[] = [], context: DiagnosisContext = {}): Diagnosis | null {
  const conditions = currentConditions(run);
  const parent = run.status?.cellnParent;
  const ready = conditions.find((condition) => condition.type === "CellnParentReady");
  const notReady = ready?.status === "False" ? ready : undefined;

  if (!parent && notReady?.reason === "AdmissionPending") return diagnoseAdmission(run, notReady, context);

  const scoped = conditions.find((condition) => condition.type === "CellnScopedExecution" && condition.status === "False");
  const scopedDiagnosis = scoped ? diagnoseScoped(run, scoped, context) : null;
  if (scopedDiagnosis) return scopedDiagnosis;

  const withheld = conditions.find((condition) => condition.type === "CellnContinuation" && condition.status === "False" && condition.reason === "LostBeforeFollowUp");
  const ownerStatus = notReady?.reason && LOST_STATUSES.has(notReady.reason) ? notReady.reason
    : parent?.ownerOutcome && LOST_STATUSES.has(parent.ownerOutcome.status) ? parent.ownerOutcome.status : undefined;
  if (ownerStatus || withheld) return diagnoseLost(run, ownerStatus || "ContextLost", notReady, withheld);

  if (notReady?.reason === "ReconciliationRequired") {
    return {
      kind: "reconciliation-required", severity: "warning", code: "ReconciliationRequired", title: "The parent's outcome is unconfirmed",
      cause: "The original parent's outcome is unconfirmed: its node did not answer, so sending is paused and no work will be replayed automatically.",
      evidence: lostEvidence(run, [notReady]),
      nextSteps: [
        { label: "Give the node a moment", detail: "The controller keeps asking the owning node every few seconds; when it answers, this conversation resumes or is reported lost." },
        { label: "If it stays like this", detail: "Check that the dispatcher on the owning node is running and reachable from the controller. Do not start a replacement while the original may still be live." },
      ],
    };
  }

  if (notReady?.reason === "CreateRefused" ||parent?.ownerOutcome?.status === "CreateRefused") {
    return {
      kind: "create-refused", severity: "error", code: "CreateRefused", title: "The node refused to start this parent",
      cause: "The owner node refused this parent — it had no capacity left, or did not accept the admission — so nothing was started and this run is never retried.",
      evidence: lostEvidence(run, [notReady]),
      nextSteps: [newConversationStep(run, "A new run is placed on any node with capacity. If every node refuses, free a parent slot (end an idle conversation) or add a node.")],
    };
  }

  const failure = failedTurn(run, turns);
  // The same evidence the API requires before it takes a turn: a running run
  // whose owner reports the parent ready, with no terminal outcome recorded.
  const accepting = run.status?.phase === "Running" && ready?.status === "True" && !parent?.ownerOutcome && !run.metadata.deletionTimestamp;
  if (failure) return diagnoseTurn(run, failure, accepting);

  const phase = run.status?.phase;
  if (phase !== "Failed" && phase !== "Refused") return null;
  const firstFalse = conditions.find((condition) => condition.status === "False");
  const error = run.status?.error?.trim();
  // A one-shot run has no turn record; a gateway refusal can only be in its error.
  const gateway = error ? HARNESS_ERRORS.find((entry) => /MODEL_/.test(entry.match.source) && entry.match.test(error)) : undefined;
  if (gateway && error) {
    return { kind: "run-failed", severity: "error", code: gateway.match.source, title: gateway.title.replace(/^Turn failed/, "Run failed"), cause: gateway.cause, evidence: [truncate(`status.error: ${error}`)], nextSteps: gateway.steps(run, true) };
  }
  return {
    kind: "run-failed", severity: "error", title: phase === "Refused" ? "Run refused" : "Run failed",
    cause: error ? truncate(error, 240) : firstFalse?.message ? truncate(firstFalse.message, 240) : "The run ended in failure without recording a reason.",
    evidence: [error ? `status.error: ${error}` : "", firstFalse ? conditionText(firstFalse) : "", run.status?.exitCode ? `exitCode: ${run.status.exitCode}` : ""].filter(Boolean).map((text) => truncate(text)),
    nextSteps: [
      { label: "Read the recorded error", detail: "The Result tab and the evidence below hold everything the controller recorded for this run." },
      { label: "Run it again once fixed", detail: "A failed run is never retried automatically.", action: { kind: "link", label: `Open ${run.spec.agentRef}`, to: `/agents/${encodeURIComponent(run.spec.agentRef)}` } },
    ],
  };
}
