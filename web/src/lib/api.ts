// Sympozium API client — types match the Go CRD structs.

// ── Common K8s types ─────────────────────────────────────────────────────────

export interface ObjectMeta {
  name: string;
  namespace?: string;
  creationTimestamp?: string;
  labels?: Record<string, string>;
  annotations?: Record<string, string>;
  generateName?: string;
}

export interface Condition {
  type: string;
  status: string;
  reason: string;
  message: string;
  lastTransitionTime: string;
}

// ── SympoziumInstance ────────────────────────────────────────────────────────

export interface SecretRef {
  provider?: string;
  secret: string;
}

export interface MemorySpec {
  enabled: boolean;
  maxSizeKB?: number;
  systemPrompt?: string;
}

export interface ChannelSpec {
  type: string;
  configRef?: SecretRef;
}

export interface AgentSandboxInstanceSpec {
  enabled: boolean;
  runtimeClass?: string;
  warmPool?: {
    size?: number;
    runtimeClass?: string;
  };
}

export interface EnvVar {
  name: string;
  value: string;
}

export interface LifecycleHookContainer {
  name: string;
  image: string;
  command?: string[];
  args?: string[];
  env?: EnvVar[];
  timeout?: string;
  gate?: boolean;
}

export interface RBACRule {
  apiGroups: string[];
  resources: string[];
  verbs: string[];
}

export interface LifecycleHooks {
  preRun?: LifecycleHookContainer[];
  postRun?: LifecycleHookContainer[];
  rbac?: RBACRule[];
  gateDefault?: string;
}

export interface GateVerdictRequest {
  action: "approve" | "reject" | "rewrite";
  response?: string;
  reason?: string;
}

export interface AgentConfig {
  model: string;
  baseURL?: string;
  thinking?: string;
  nodeSelector?: Record<string, string>;
  agentSandbox?: AgentSandboxInstanceSpec;
  lifecycle?: LifecycleHooks;
}

export interface AgentsSpec {
  default: AgentConfig;
}

export interface SkillRef {
  skillPackRef: string;
  configMapRef?: string;
  params?: Record<string, string>;
}

export interface ChannelStatus {
  type: string;
  status: string;
  lastHealthCheck?: string;
  message?: string;
}

export interface SympoziumInstanceSpec {
  channels?: ChannelSpec[];
  agents: AgentsSpec;
  skills?: SkillRef[];
  policyRef?: string;
  authRefs?: SecretRef[];
  memory?: MemorySpec;
}

export interface SympoziumInstanceStatus {
  phase?: string;
  channels?: ChannelStatus[];
  activeAgentPods?: number;
  totalAgentRuns?: number;
  conditions?: Condition[];
}

export interface SympoziumInstance {
  metadata: ObjectMeta;
  spec: SympoziumInstanceSpec;
  status?: SympoziumInstanceStatus;
}

// ── AgentRun ─────────────────────────────────────────────────────────────────

export interface ModelSpec {
  provider?: string;
  model?: string;
  baseURL?: string;
  thinking?: string;
  authSecretRef?: string;
  nodeSelector?: Record<string, string>;
}

export interface ToolPolicySpec {
  allow?: string[];
  deny?: string[];
}

export interface TokenUsage {
  inputTokens: number;
  outputTokens: number;
  totalTokens: number;
  toolCalls: number;
  durationMs: number;
}

export interface AgentRunSpec {
  instanceRef: string;
  agentId: string;
  sessionKey: string;
  task: string;
  systemPrompt?: string;
  model?: ModelSpec;
  toolPolicy?: ToolPolicySpec;
  timeout?: string;
  cleanup?: string;
  mode?: string;
  lifecycle?: LifecycleHooks;
}

export interface AgentRunStatus {
  phase?: string;
  podName?: string;
  jobName?: string;
  deploymentName?: string;
  serviceName?: string;
  startedAt?: string;
  completedAt?: string;
  result?: string;
  error?: string;
  exitCode?: number;
  tokenUsage?: TokenUsage;
  postRunJobName?: string;
  gateVerdict?: string;
  conditions?: Condition[];
}

export interface AgentRun {
  metadata: ObjectMeta;
  spec: AgentRunSpec;
  status?: AgentRunStatus;
}

// ── SympoziumPolicy ──────────────────────────────────────────────────────────

export interface ToolGatingRule {
  tool: string;
  action: string;
}

export interface ToolGatingSpec {
  defaultAction?: string;
  rules?: ToolGatingRule[];
}

export interface SandboxPolicySpec {
  required?: boolean;
  defaultImage?: string;
  maxCPU?: string;
  maxMemory?: string;
  allowHostMounts?: boolean;
}

export interface EgressRule {
  host: string;
  port: number;
}

export interface NetworkPolicySpec {
  denyAll?: boolean;
  allowDNS?: boolean;
  allowEventBus?: boolean;
  allowedEgress?: EgressRule[];
}

export interface SympoziumPolicySpec {
  sandboxPolicy?: SandboxPolicySpec;
  toolGating?: ToolGatingSpec;
  featureGates?: Record<string, boolean>;
  networkPolicy?: NetworkPolicySpec;
}

export interface SympoziumPolicyStatus {
  boundInstances?: number;
  conditions?: Condition[];
}

export interface SympoziumPolicy {
  metadata: ObjectMeta;
  spec: SympoziumPolicySpec;
  status?: SympoziumPolicyStatus;
}

// ── SkillPack ────────────────────────────────────────────────────────────────

export interface Skill {
  name: string;
  description?: string;
  content?: string;
  requires?: { bins?: string[]; tools?: string[] };
}

export interface RBACRule {
  apiGroups: string[];
  resources: string[];
  verbs: string[];
}

export interface HostPathMount {
  hostPath: string;
  mountPath: string;
  readOnly?: boolean;
}

export interface HostAccessSpec {
  enabled?: boolean;
  hostNetwork?: boolean;
  hostPID?: boolean;
  privileged?: boolean;
  runAsRoot?: boolean;
  mounts?: HostPathMount[];
}

export interface SkillSidecar {
  image: string;
  command?: string[];
  mountWorkspace?: boolean;
  rbac?: RBACRule[];
  clusterRBAC?: RBACRule[];
  secretRef?: string;
  secretMountPath?: string;
  hostAccess?: HostAccessSpec;
}

// ── Gateway Config ──────────────────────────────────────────────────────────

export interface GatewayConfigResponse {
  enabled: boolean;
  gatewayClassName?: string;
  name?: string;
  baseDomain?: string;
  tlsEnabled: boolean;
  certManagerClusterIssuer?: string;
  tlsSecretName?: string;
  phase?: string;
  ready: boolean;
  address?: string;
  listenerCount?: number;
  message?: string;
}

export interface PatchGatewayConfigRequest {
  enabled?: boolean;
  gatewayClassName?: string;
  name?: string;
  baseDomain?: string;
  tlsEnabled?: boolean;
  certManagerClusterIssuer?: string;
  tlsSecretName?: string;
}

export interface GithubAuthStatusResponse {
  status: string;
}

export interface MetricBreakdown {
  label: string;
  value: number;
}

export interface ObservabilityMetricsResponse {
  collectorReachable: boolean;
  collectorError?: string;
  collectedAt: string;
  namespace: string;
  agentRunsTotal: number;
  inputTokensTotal: number;
  outputTokensTotal: number;
  toolInvocations: number;
  runStatus?: Record<string, number>;
  inputByModel?: MetricBreakdown[];
  outputByModel?: MetricBreakdown[];
  toolsByName?: MetricBreakdown[];
  rawMetricNames?: string[];
}

export interface GatewayBucket {
  ts: number;
  requests: number;
  errors: number;
  avgDurationSec: number;
}

export interface GatewayMetricsResponse {
  totalRequests: number;
  successCount: number;
  errorCount: number;
  avgDurationSec: number;
  uptimeSec: number;
  servingInstances: number;
  buckets: GatewayBucket[];
}

export interface SkillPackSpec {
  skills: Skill[];
  category?: string;
  source?: string;
  version?: string;
  sidecar?: SkillSidecar;
}

export interface SkillPackStatus {
  phase?: string;
  configMapName?: string;
  skillCount?: number;
  conditions?: Condition[];
}

export interface SkillPack {
  metadata: ObjectMeta;
  spec: SkillPackSpec;
  status?: SkillPackStatus;
}

// ── SympoziumSchedule ────────────────────────────────────────────────────────

export interface SympoziumScheduleSpec {
  instanceRef: string;
  schedule: string;
  task: string;
  type?: string;
  suspend?: boolean;
  concurrencyPolicy?: string;
  includeMemory?: boolean;
}

export interface SympoziumScheduleStatus {
  phase?: string;
  lastRunTime?: string;
  nextRunTime?: string;
  lastRunName?: string;
  totalRuns?: number;
  conditions?: Condition[];
}

export interface SympoziumSchedule {
  metadata: ObjectMeta;
  spec: SympoziumScheduleSpec;
  status?: SympoziumScheduleStatus;
}

// ── PersonaPack ──────────────────────────────────────────────────────────────

export interface PersonaToolPolicy {
  allow?: string[];
  deny?: string[];
}

export interface PersonaSchedule {
  type: string;
  interval?: string;
  cron?: string;
  task: string;
}

export interface PersonaMemory {
  enabled: boolean;
  seeds?: string[];
}

export interface PersonaWebEndpoint {
  enabled: boolean;
  hostname?: string;
}

export interface PersonaSpec {
  name: string;
  displayName?: string;
  systemPrompt: string;
  model?: string;
  skills?: string[];
  toolPolicy?: PersonaToolPolicy;
  schedule?: PersonaSchedule;
  memory?: PersonaMemory;
  channels?: string[];
  webEndpoint?: PersonaWebEndpoint;
  lifecycle?: LifecycleHooks;
}

export interface InstalledPersona {
  name: string;
  instanceName: string;
  scheduleName?: string;
}

export interface PersonaPackSpec {
  enabled?: boolean;
  description?: string;
  category?: string;
  version?: string;
  personas: PersonaSpec[];
  authRefs?: SecretRef[];
  excludePersonas?: string[];
  channelConfigs?: Record<string, string>;
  policyRef?: string;
  skillParams?: Record<string, Record<string, string>>;
  taskOverride?: string;
}

export interface PersonaPackStatus {
  phase?: string;
  personaCount?: number;
  installedCount?: number;
  installedPersonas?: InstalledPersona[];
  conditions?: Condition[];
}

export interface PersonaPack {
  metadata: ObjectMeta;
  spec: PersonaPackSpec;
  status?: PersonaPackStatus;
}

export interface InstallDefaultPersonaPacksResponse {
  sourceNamespace: string;
  targetNamespace: string;
  copied: string[];
  alreadyPresent: string[];
}

// ── MCPServer ────────────────────────────────────────────────────────────────

export interface MCPSecretRef {
  name: string;
}

export interface MCPServerDeployment {
  image: string;
  cmd?: string;
  args?: string[];
  port?: number;
  env?: Record<string, string>;
  secretRefs?: MCPSecretRef[];
  serviceAccountName?: string;
}

export interface MCPServerSpec {
  transportType: string;
  url?: string;
  deployment?: MCPServerDeployment;
  toolsPrefix: string;
  timeout?: number;
  replicas?: number;
  toolsAllow?: string[];
  toolsDeny?: string[];
}

export interface MCPServerStatus {
  ready: boolean;
  url?: string;
  toolCount?: number;
  tools?: string[];
  conditions?: Condition[];
}

export interface MCPServer {
  metadata: ObjectMeta;
  spec: MCPServerSpec;
  status?: MCPServerStatus;
}

export interface InstallDefaultMCPServersResponse {
  sourceNamespace: string;
  targetNamespace: string;
  copied: string[];
  alreadyPresent: string[];
}

export interface MCPServerAuthStatusResponse {
  status: string;
  secretName: string;
}

// ── Pod info (returned by /api/v1/pods) ──────────────────────────────────────

export interface PodInfo {
  name: string;
  namespace: string;
  phase: string;
  nodeName?: string;
  podIP?: string;
  startTime?: string;
  restartCount: number;
  labels?: Record<string, string>;
  instanceRef?: string;
}

// ── Cluster Info ─────────────────────────────────────────────────────────

export interface ClusterInfoResponse {
  nodes: number;
  namespaces: number;
  pods: number;
  version?: string;
}

// ── Provider Discovery ───────────────────────────────────────────────────────

export interface NodeProvider {
  name: string;
  port: number;
  proxyPort?: number;
  models: string[];
  lastProbe?: string;
}

export interface ProviderNode {
  nodeName: string;
  nodeIP: string;
  providers: NodeProvider[];
  labels?: Record<string, string>;
}

export interface ProviderModelsResponse {
  models: string[];
  source: string;
}

// ── Capabilities ─────────────────────────────────────────────────────────────

export interface CapabilityStatus {
  available: boolean;
  reason?: string;
}

export interface CapabilitiesResponse {
  agentSandbox: CapabilityStatus;
}

// ── API client ───────────────────────────────────────────────────────────────

/** Typed error so callers can inspect the HTTP status code. */
export class ApiError extends Error {
  status: number;
  constructor(message: string, status: number) {
    super(message);
    this.name = "ApiError";
    this.status = status;
  }
}

const TOKEN_KEY = "sympozium_token";
const NS_KEY = "sympozium_namespace";
export const AUTH_UNAUTHORIZED_EVENT = "sympozium-auth-unauthorized";

function handleUnauthorized() {
  clearToken();
  window.dispatchEvent(new Event(AUTH_UNAUTHORIZED_EVENT));
}

export function getToken(): string | null {
  return localStorage.getItem(TOKEN_KEY);
}

export function setToken(token: string) {
  localStorage.setItem(TOKEN_KEY, token);
}

export function clearToken() {
  localStorage.removeItem(TOKEN_KEY);
}

export function getNamespace(): string {
  return localStorage.getItem(NS_KEY) || "default";
}

export function setNamespace(ns: string) {
  localStorage.setItem(NS_KEY, ns);
}

async function apiFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const token = getToken();
  const headers = new Headers(init?.headers);
  if (!headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }
  if (token) {
    // Ensure the token only contains valid HTTP header characters (Latin1).
    // Strip any non-Latin1 codepoints that can cause Firefox ByteString errors.
    const safeToken = token.replace(/[^\x00-\xFF]/g, "");
    headers.set("Authorization", `Bearer ${safeToken}`);
  }

  const ns = getNamespace();
  const separator = path.includes("?") ? "&" : "?";
  const url = `${path}${separator}namespace=${ns}`;

  // Retry network errors (port-forward drops, transient failures) up to 2
  // times with a short delay.  Non-network errors (4xx, 5xx) are NOT retried
  // here — React Query handles those via its own retry config.
  const maxAttempts = 3;
  let lastError: unknown;
  for (let attempt = 0; attempt < maxAttempts; attempt++) {
    try {
      const res = await fetch(url, { ...init, headers });
      if (res.status === 401) {
        handleUnauthorized();
        throw new ApiError("Unauthorized", 401);
      }
      if (res.status === 204) return undefined as T;
      if (!res.ok) {
        const text = await res.text();
        throw new Error(text || `HTTP ${res.status}`);
      }
      // Guard against SPA HTML fallback for unrecognised routes which
      // would throw a SyntaxError from .json().  Only reject responses
      // that are explicitly HTML — missing Content-Type is fine (Go's
      // WriteHeader before writeJSON drops the header but body is JSON).
      const ct = res.headers.get("Content-Type") || "";
      if (ct.includes("text/html")) {
        throw new Error(
          "Unexpected HTML response — the API endpoint may not exist on this server version",
        );
      }
      return res.json();
    } catch (err) {
      lastError = err;
      // Only retry on network-level failures (TypeError from fetch).
      // Don't retry application-level errors (ApiError, HTTP errors).
      const isNetworkError =
        err instanceof TypeError ||
        (err instanceof Error &&
          !("status" in err) &&
          /network|failed to fetch|load failed|aborted/i.test(err.message));
      if (!isNetworkError || attempt >= maxAttempts - 1) {
        throw err;
      }
      // Wait before retrying (1s, then 2s).
      await new Promise((r) => setTimeout(r, 1000 * (attempt + 1)));
    }
  }
  throw lastError;
}

// ── Instances ────────────────────────────────────────────────────────────────

export const api = {
  instances: {
    list: () => apiFetch<SympoziumInstance[]>("/api/v1/instances"),
    get: (name: string) =>
      apiFetch<SympoziumInstance>(`/api/v1/instances/${name}`),
    delete: (name: string) =>
      apiFetch<void>(`/api/v1/instances/${name}`, { method: "DELETE" }),
    patch: (
      name: string,
      data: {
        webEndpoint?: {
          enabled?: boolean;
          hostname?: string;
          rateLimit?: { requestsPerMinute?: number };
        };
        lifecycle?: LifecycleHooks | null;
        requireApproval?: boolean;
      },
    ) =>
      apiFetch<SympoziumInstance>(`/api/v1/instances/${name}`, {
        method: "PATCH",
        body: JSON.stringify(data),
      }),
    create: (data: {
      name: string;
      provider: string;
      model: string;
      baseURL?: string;
      secretName?: string;
      apiKey?: string;
      awsRegion?: string;
      awsAccessKeyId?: string;
      awsSecretAccessKey?: string;
      awsSessionToken?: string;
      policyRef?: string;
      skills?: SkillRef[];
      channels?: ChannelSpec[];
      heartbeatInterval?: string;
      nodeSelector?: Record<string, string>;
      agentSandbox?: { enabled: boolean; runtimeClass?: string };
      runTimeout?: string;
      requireApproval?: boolean;
    }) =>
      apiFetch<SympoziumInstance>("/api/v1/instances", {
        method: "POST",
        body: JSON.stringify(data),
      }),
  },

  runs: {
    list: () => apiFetch<AgentRun[]>("/api/v1/runs"),
    get: (name: string) => apiFetch<AgentRun>(`/api/v1/runs/${name}`),
    create: (data: {
      instanceRef: string;
      task: string;
      model?: string;
      timeout?: string;
    }) =>
      apiFetch<AgentRun>("/api/v1/runs", {
        method: "POST",
        body: JSON.stringify(data),
      }),
    delete: (name: string) =>
      apiFetch<void>(`/api/v1/runs/${name}`, { method: "DELETE" }),
    gateVerdict: (name: string, data: GateVerdictRequest) =>
      apiFetch<AgentRun>(`/api/v1/runs/${name}/gate-verdict`, {
        method: "POST",
        body: JSON.stringify(data),
      }),
  },

  policies: {
    list: () => apiFetch<SympoziumPolicy[]>("/api/v1/policies"),
    get: (name: string) =>
      apiFetch<SympoziumPolicy>(`/api/v1/policies/${name}`),
    delete: (name: string) =>
      apiFetch<void>(`/api/v1/policies/${name}`, { method: "DELETE" }),
  },

  skills: {
    list: () => apiFetch<SkillPack[]>("/api/v1/skills"),
    get: (name: string) => apiFetch<SkillPack>(`/api/v1/skills/${name}`),
  },

  schedules: {
    list: () => apiFetch<SympoziumSchedule[]>("/api/v1/schedules"),
    get: (name: string) =>
      apiFetch<SympoziumSchedule>(`/api/v1/schedules/${name}`),
    create: (data: {
      instanceRef: string;
      schedule: string;
      task: string;
      type?: string;
      name?: string;
    }) =>
      apiFetch<SympoziumSchedule>("/api/v1/schedules", {
        method: "POST",
        body: JSON.stringify(data),
      }),
    delete: (name: string) =>
      apiFetch<void>(`/api/v1/schedules/${name}`, { method: "DELETE" }),
  },

  personaPacks: {
    list: () => apiFetch<PersonaPack[]>("/api/v1/personapacks"),
    get: (name: string) =>
      apiFetch<PersonaPack>(`/api/v1/personapacks/${name}`),
    delete: (name: string) =>
      apiFetch<void>(`/api/v1/personapacks/${name}`, { method: "DELETE" }),
    patch: (
      name: string,
      data: {
        enabled?: boolean;
        provider?: string;
        secretName?: string;
        apiKey?: string;
        model?: string;
        baseURL?: string;
        channelConfigs?: Record<string, string>;
        policyRef?: string;
        heartbeatInterval?: string;
        skillParams?: Record<string, Record<string, string>>;
        githubToken?: string;
        personas?: Array<{
          name: string;
          systemPrompt?: string;
          skills?: string[];
        }>;
        agentSandbox?: { enabled: boolean; runtimeClass?: string };
      },
    ) =>
      apiFetch<PersonaPack>(`/api/v1/personapacks/${name}`, {
        method: "PATCH",
        body: JSON.stringify(data),
      }),
    installDefaults: () =>
      apiFetch<InstallDefaultPersonaPacksResponse>(
        "/api/v1/personapacks/install-defaults",
        {
          method: "POST",
        },
      ),
  },

  mcpServers: {
    list: () => apiFetch<MCPServer[]>("/api/v1/mcpservers"),
    get: (name: string) => apiFetch<MCPServer>(`/api/v1/mcpservers/${name}`),
    create: (data: {
      name: string;
      transportType: string;
      toolsPrefix: string;
      url?: string;
      image?: string;
      timeout?: number;
      toolsAllow?: string[];
      toolsDeny?: string[];
      env?: Record<string, string>;
      secretRefs?: string[];
      args?: string[];
    }) =>
      apiFetch<MCPServer>("/api/v1/mcpservers", {
        method: "POST",
        body: JSON.stringify(data),
      }),
    delete: (name: string) =>
      apiFetch<void>(`/api/v1/mcpservers/${name}`, { method: "DELETE" }),
    patch: (
      name: string,
      data: {
        transportType?: string;
        url?: string;
        toolsPrefix?: string;
        timeout?: number;
        toolsAllow?: string[];
        toolsDeny?: string[];
      },
    ) =>
      apiFetch<MCPServer>(`/api/v1/mcpservers/${name}`, {
        method: "PATCH",
        body: JSON.stringify(data),
      }),
    installDefaults: () =>
      apiFetch<InstallDefaultMCPServersResponse>(
        "/api/v1/mcpservers/install-defaults",
        { method: "POST" },
      ),
    authStatus: (name: string) =>
      apiFetch<MCPServerAuthStatusResponse>(
        `/api/v1/mcpservers/${name}/auth/status`,
      ),
    authToken: (name: string, token: string) =>
      apiFetch<MCPServerAuthStatusResponse>(
        `/api/v1/mcpservers/${name}/auth/token`,
        { method: "POST", body: JSON.stringify({ token }) },
      ),
  },

  pods: {
    list: () => apiFetch<PodInfo[]>("/api/v1/pods"),
    logs: (name: string) =>
      apiFetch<{ logs: string }>(`/api/v1/pods/${name}/logs`),
  },

  namespaces: {
    list: () => apiFetch<string[]>("/api/v1/namespaces"),
  },

  cluster: {
    info: () => apiFetch<ClusterInfoResponse>("/api/v1/cluster"),
  },

  capabilities: {
    get: () => apiFetch<CapabilitiesResponse>("/api/v1/capabilities"),
  },

  agentSandbox: {
    install: (version?: string) =>
      apiFetch<{ installed: string[]; version: string }>(
        "/api/v1/agent-sandbox/install",
        {
          method: "POST",
          body: JSON.stringify(version ? { version } : {}),
        },
      ),
    uninstall: () =>
      apiFetch<{ deleted: string[] }>("/api/v1/agent-sandbox/install", {
        method: "DELETE",
      }),
  },

  observability: {
    metrics: () =>
      apiFetch<ObservabilityMetricsResponse>("/api/v1/observability/metrics"),
  },

  gateway: {
    get: () => apiFetch<GatewayConfigResponse>("/api/v1/gateway"),
    create: (data: PatchGatewayConfigRequest) =>
      apiFetch<GatewayConfigResponse>("/api/v1/gateway", {
        method: "POST",
        body: JSON.stringify(data),
      }),
    patch: (data: PatchGatewayConfigRequest) =>
      apiFetch<GatewayConfigResponse>("/api/v1/gateway", {
        method: "PATCH",
        body: JSON.stringify(data),
      }),
    delete: () => apiFetch<void>("/api/v1/gateway", { method: "DELETE" }),
    metrics: (range?: string) =>
      apiFetch<GatewayMetricsResponse>(
        `/api/v1/gateway/metrics${range ? `?range=${range}` : ""}`,
      ),
  },

  providers: {
    nodes: () => apiFetch<ProviderNode[]>("/api/v1/providers/nodes"),
    models: (baseURL: string, apiKey?: string) =>
      apiFetch<ProviderModelsResponse>(
        `/api/v1/providers/models?baseURL=${encodeURIComponent(baseURL)}`,
        apiKey ? { headers: { "X-Provider-Api-Key": apiKey } } : undefined,
      ),
    bedrockModels: (data: {
      region: string;
      accessKeyId: string;
      secretAccessKey: string;
      sessionToken?: string;
    }) =>
      apiFetch<ProviderModelsResponse>("/api/v1/providers/bedrock/models", {
        method: "POST",
        body: JSON.stringify(data),
      }),
  },

  githubAuth: {
    setToken: (token: string) =>
      apiFetch<GithubAuthStatusResponse>(
        "/api/v1/skills/github-gitops/auth/token",
        {
          method: "POST",
          body: JSON.stringify({ token }),
        },
      ),
    status: () =>
      apiFetch<GithubAuthStatusResponse>(
        "/api/v1/skills/github-gitops/auth/status",
      ),
  },
};
