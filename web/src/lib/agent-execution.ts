import type { AgentExecutionDefaults, CellnSelection } from "./api";

export interface WizardExecution {
  executionBackend?: "job" | "celln";
  executionLifecycle?: "one-shot" | "enduring";
  borrowedTools?: CellnSelection["toolRefs"];
  runtimeRef?: string;
  model: string;
  provider?: string;
  modelConnectionRef?: string;
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
      toolRefs: (form.borrowedTools || []).map((tool) => ({ ...tool })),
    },
    ...(form.executionLifecycle === "enduring" ? {
      enduring: { leaseSeconds: 600, maxTurns: 8, maxModelRequests: 24, maxOutputTokens: 8192 },
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
  return ["name", "plane", "runtime", ...(celln ? ["tools", "provider", "apikey", "model"] : ["skills", "provider", "apikey", "model", "heartbeat", "channels"]), "confirm", "channelAction"] as const;
}
