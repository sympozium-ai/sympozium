import type { AgentRuntime } from "./api";

export function persistentHarnessName(runtime: AgentRuntime): "Pi" | "Hermes" | undefined {
  if (runtime.spec.contractVersion !== "v1alpha2" || runtime.spec.session?.protocol !== "openai-chat") return undefined;
  const adapter = runtime.spec.image?.match(/\/(pi|hermes)@sha256:/)?.[1];
  return adapter === "pi" ? "Pi" : adapter === "hermes" ? "Hermes" : undefined;
}

export function persistentHarnesses(runtimes: AgentRuntime[]): AgentRuntime[] {
  return runtimes.filter((runtime) => persistentHarnessName(runtime) !== undefined);
}
