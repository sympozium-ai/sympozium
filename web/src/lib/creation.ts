/**
 * Shared creation model for the Run/Harness × Kubernetes/Celln matrix.
 *
 * The Run dialog and the Agent/Harness wizard used to carry their own copies of
 * the execution-plane rules and the provider allow-list, which drifted. This
 * module is the single source of truth for both.
 */

/** Execution planes are identified by the AgentRun `spec.backend` value. */
export type ExecutionPlane = "job" | "celln";

/** What the user is creating. */
export type CreationKind = "run" | "harness";

export const PLANE_VALUES: readonly ExecutionPlane[] = ["job", "celln"] as const;

export interface PlaneMeta {
  value: ExecutionPlane;
  /** Short label for the plane, independent of the artifact. */
  label: string;
  /** One-line description when creating a one-off Run. */
  run: string;
  /** One-line description when creating a persistent Harness/Agent. */
  harness: string;
}

export const PLANES: readonly PlaneMeta[] = [
  {
    value: "job",
    label: "Kubernetes",
    run: "Default · containerized run",
    harness: "Persistent Pi or Hermes",
  },
  {
    value: "celln",
    label: "Celln",
    run: "One-shot · hardware-isolated KVM cell",
    harness: "Enduring · persistent native parent",
  },
] as const;

/** Plane option title, differentiated by what is being created. */
export function planeTitle(
  plane: ExecutionPlane,
  kind: CreationKind,
): string {
  if (plane === "celln") return kind === "run" ? "Celln cell" : "Celln parent";
  return "Kubernetes";
}

export function planeDescription(
  plane: ExecutionPlane,
  kind: CreationKind,
): string {
  const meta = PLANES.find((p) => p.value === plane);
  if (!meta) return "";
  return kind === "run" ? meta.run : meta.harness;
}

export interface ProviderReachContext {
  plane: ExecutionPlane;
  /** Persistent Kubernetes (a Pi/Hermes session) speaks OpenAI-compatible chat. */
  persistentHarness?: boolean;
  /** Explicit HTTP/self-signed private-endpoint opt-in (Celln only). */
  allowInsecure?: boolean;
}

/**
 * Single source of truth for which model providers a plane can reach.
 *
 * Mirrors the backend, which narrows provider support per execution plane:
 * a provider shown here but unsupported on the selected plane is a setup bug.
 */
export function providerReachableOnPlane(
  provider: string,
  ctx: ProviderReachContext,
): boolean {
  if (ctx.plane === "celln") {
    // The native Celln host transport reaches public HTTPS endpoints by
    // default. The insecure opt-in also allows HTTP/self-signed locals;
    // Bedrock has no compatible protocol either way.
    if (provider === "bedrock") return false;
    if (ctx.allowInsecure) return true;
    return (
      provider === "openai" ||
      provider === "anthropic" ||
      provider === "azure-openai" ||
      provider === "custom"
    );
  }
  if (ctx.persistentHarness) {
    // Persistent Kubernetes harnesses speak OpenAI-compatible chat only.
    return provider !== "anthropic" && provider !== "bedrock";
  }
  return true;
}

/** Filter a provider list down to what the selected plane can reach. */
export function providersForPlane<T extends { value: string }>(
  providers: readonly T[],
  ctx: ProviderReachContext,
): T[] {
  return providers.filter((p) => providerReachableOnPlane(p.value, ctx));
}
