import type { AgentExecutionDefaults, CellnSelection, EnduringLimits } from "./api";

export interface WizardExecution {
  executionBackend?: "job" | "celln";
  executionLifecycle?: "one-shot" | "enduring";
  borrowedTools?: CellnSelection["toolRefs"];
  /** Shared platform catalogue revisions for a runtime wrapper; excludes borrowedTools. */
  clusterTools?: CellnSelection["clusterToolRefs"];
  runtimeRef?: string;
  model: string;
  provider?: string;
  modelConnectionRef?: string;
  /** Budget a platform profile suggests for one conversation (within its policy ceilings). */
  enduringDefaults?: EnduringLimits;
}

/**
 * What one turn reserves from a parent's lifetime totals with the current
 * Celln starter package (api/v1alpha1 TurnModelRequests, TurnOutputTokens).
 * Totals are sized as turns × allowance; smaller totals end the conversation
 * before its turn count is reached. A fleet backend that raises its max output
 * tokens per request (512 by default, up to 4096) reserves 6 × that per turn;
 * its platform profile's sessionDefaults already account for it.
 */
export const TURN_MODEL_REQUESTS = 6;
export const TURN_OUTPUT_TOKENS = 3072;

const LEGACY_TURNS = 8;

/** Budget for a legacy namespaced native runtime, whose registration bounds it. */
export const LEGACY_ENDURING_DEFAULTS: EnduringLimits = {
  leaseSeconds: 600,
  maxTurns: LEGACY_TURNS,
  maxModelRequests: LEGACY_TURNS * TURN_MODEL_REQUESTS,
  maxOutputTokens: LEGACY_TURNS * TURN_OUTPUT_TOKENS,
};

/** One sentence naming a budget, for review screens. */
export function describeEnduringLimits(limits: EnduringLimits): string {
  return `${limits.leaseSeconds}-second lease, ${limits.maxTurns} turns, ${limits.maxModelRequests} model requests, ${limits.maxOutputTokens} output tokens`;
}

// Shared by API creation and YAML preview: neither may silently lose tool refs.
export function executionFromWizard(form: WizardExecution): AgentExecutionDefaults {
  if (form.executionBackend !== "celln") return { backend: "job", executionLifecycle: "one-shot", ...(form.modelConnectionRef ? { modelConnectionRef: form.modelConnectionRef } : {}) };
  return {
    backend: "celln",
    executionLifecycle: form.executionLifecycle || "one-shot",
    provider: form.modelConnectionRef ? undefined : form.provider || "deepseek",
    modelConnectionRef: form.modelConnectionRef,
    model: form.model,
    cellnSelection: {
      runtimeRef: form.runtimeRef || undefined,
      toolRefs: form.clusterTools ? [] : (form.borrowedTools || []).map((tool) => ({ ...tool })),
      ...(form.clusterTools?.length ? { clusterToolRefs: form.clusterTools.map((tool) => ({ ...tool })) } : {}),
    },
    ...(form.executionLifecycle === "enduring" ? {
      enduring: { ...(form.enduringDefaults || LEGACY_ENDURING_DEFAULTS) },
    } : {}),
  };
}

const CONNECTION_SUFFIX = "-connection";

/** Names the ModelConnection the wizard creates for an Agent's model route. */
export function modelConnectionName(agentName: string): string {
  const max = 253 - CONNECTION_SUFFIX.length;
  const base = agentName.length > max ? agentName.slice(0, max) : agentName;
  return `${base.replace(/-+$/, "")}${CONNECTION_SUFFIX}`;
}

/**
 * Default API base URL for a provider, used when the operator leaves Base URL
 * empty. completionEndpointFromBase turns it into the request URL.
 */
export function defaultProviderBaseURL(provider: string): string {
  switch (provider) {
    case "openai":
      return "https://api.openai.com";
    case "anthropic":
      return "https://api.anthropic.com";
    case "ollama":
      return "http://ollama.default.svc:11434";
    case "lm-studio":
      return "http://localhost:1234";
    case "llama-server":
    case "unsloth":
      return "http://localhost:8080";
    default:
      return "";
  }
}

/**
 * The Base URL field is an API root. Sympozium assumes the standard
 * OpenAI-compatible completion path (Anthropic uses /v1/messages) and builds
 * the exact URL the host transport posts to. A full request URL is accepted
 * unchanged, so a pasted endpoint keeps working.
 */
export function completionEndpointFromBase(
  baseURL: string,
  provider: string,
): string {
  const base = baseURL.trim().replace(/\/+$/, "");
  if (base === "") return base;
  if (/\/(chat\/completions|completions|messages)$/.test(base)) return base;
  const path = provider === "anthropic" ? "/messages" : "/chat/completions";
  const rooted = /\/v\d+$/.test(base) ? base : `${base}/v1`;
  return `${rooted}${path}`;
}

/** Full request URL for a provider's model connection. Azure carries its own
 *  deployment path, so its base is used verbatim. */
export function modelConnectionEndpoint(
  baseURL: string,
  provider: string,
): string {
  const base = baseURL || defaultProviderBaseURL(provider);
  if (provider === "azure-openai") return base;
  return completionEndpointFromBase(base, provider);
}

export function agentCreationSteps(celln: boolean) {
  // A Celln Agent owns its model backend (provider route, key, model); its
  // runtime is the fleet's and the mediated path is chat only.
  return ["name", "plane", ...(celln ? ["provider", "apikey", "model"] : ["runtime", "skills", "provider", "apikey", "model", "heartbeat", "channels"]), "confirm", "channelAction"] as const;
}
