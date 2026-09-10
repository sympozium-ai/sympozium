import type { AgentExecutionDefaults, CellnSelection } from "./api";

export interface WizardExecution {
  executionBackend?: "job" | "celln";
  executionLifecycle?: "one-shot" | "enduring";
  borrowedTools?: CellnSelection["toolRefs"];
  runtimeRef?: string;
  model: string;
}

// Shared by API creation and YAML preview: neither may silently lose tool refs.
export function executionFromWizard(form: WizardExecution): AgentExecutionDefaults {
  if (form.executionBackend !== "celln") return { backend: "job", executionLifecycle: "one-shot" };
  return {
    backend: "celln",
    executionLifecycle: form.executionLifecycle || "one-shot",
    provider: "deepseek",
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

export function agentCreationSteps(celln: boolean) {
  return ["name", "runtime", "plane", "skills", ...(celln ? ["tools", "model"] : ["provider", "apikey", "model", "heartbeat", "channels"]), "confirm", "channelAction"] as const;
}
