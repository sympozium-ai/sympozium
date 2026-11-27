// A Celln Agent's own ModelConnection is its model route: any provider, on any
// OpenAI-compatible (openai-chat) or Anthropic-compatible (anthropic-messages)
// endpoint, with the Agent's own key. These presets fill the connection in;
// the operator's guardrail over it is the policy's origin globs
// (CellnMediation.allowedOrigins/deniedOrigins, api AdmitsOrigin) and the
// exact private origins approved in modelGateway.privateOrigins.
import type { ComponentType } from "react";
import { Bot, Cpu, Settings, Sparkles, Zap } from "lucide-react";
import type { CellnMediatedRoute, CellnMediation } from "@/lib/api";

export type CellnProtocol = "openai-chat" | "anthropic-messages";

export interface CellnProviderPreset {
  /** The provider name written to the connection. */
  value: string;
  label: string;
  /** hosted: fixed public origin, key required. local: the user types the endpoint, key optional. custom: endpoint, protocol and optional key. */
  kind: "hosted" | "local" | "custom";
  protocol: CellnProtocol;
  /** The fixed origin of a hosted provider. */
  origin?: string;
  /** The provider's request path on that origin. */
  path: string;
  /** Prefilled model name, when one is well known. */
  suggestedModel?: string;
  /** Example endpoint shown while the user types one. */
  placeholder?: string;
  /** Fallback icon when onboarding-wizard's PROVIDERS has none for this value. */
  icon: ComponentType<{ className?: string }>;
}

const CHAT = "/v1/chat/completions";

export const CELLN_PROVIDERS: CellnProviderPreset[] = [
  { value: "openai", label: "OpenAI", kind: "hosted", protocol: "openai-chat", origin: "https://api.openai.com", path: CHAT, suggestedModel: "gpt-4o", icon: Bot },
  { value: "anthropic", label: "Anthropic", kind: "hosted", protocol: "anthropic-messages", origin: "https://api.anthropic.com", path: "/v1/messages", suggestedModel: "claude-sonnet-5-5", icon: Bot },
  { value: "openrouter", label: "OpenRouter", kind: "hosted", protocol: "openai-chat", origin: "https://openrouter.ai", path: "/api/v1/chat/completions", icon: Sparkles },
  { value: "deepseek", label: "DeepSeek", kind: "hosted", protocol: "openai-chat", origin: "https://api.deepseek.com", path: "/chat/completions", suggestedModel: "deepseek-chat", icon: Bot },
  { value: "mistral", label: "Mistral", kind: "hosted", protocol: "openai-chat", origin: "https://api.mistral.ai", path: CHAT, suggestedModel: "mistral-large-latest", icon: Bot },
  { value: "groq", label: "Groq", kind: "hosted", protocol: "openai-chat", origin: "https://api.groq.com", path: "/openai/v1/chat/completions", icon: Zap },
  { value: "google", label: "Google", kind: "hosted", protocol: "openai-chat", origin: "https://generativelanguage.googleapis.com", path: "/v1beta/openai/chat/completions", suggestedModel: "gemini-2.5-flash", icon: Sparkles },
  { value: "xai", label: "xAI", kind: "hosted", protocol: "openai-chat", origin: "https://api.x.ai", path: CHAT, suggestedModel: "grok-4", icon: Bot },
  { value: "ollama", label: "Ollama", kind: "local", protocol: "openai-chat", path: CHAT, placeholder: "http://ollama.default.svc:11434", icon: Cpu },
  { value: "llama-server", label: "llama-server", kind: "local", protocol: "openai-chat", path: CHAT, placeholder: "http://llama-server.default.svc:8080", icon: Cpu },
  { value: "lm-studio", label: "LM Studio", kind: "local", protocol: "openai-chat", path: CHAT, placeholder: "http://192.168.1.20:1234", icon: Cpu },
  { value: "vllm", label: "vLLM", kind: "local", protocol: "openai-chat", path: CHAT, placeholder: "http://vllm.default.svc:8000", icon: Cpu },
  { value: "custom", label: "Custom", kind: "custom", protocol: "openai-chat", path: CHAT, placeholder: "https://llm.example.com/v1/chat/completions", icon: Settings },
];

export function cellnPreset(value: string | undefined): CellnProviderPreset | undefined {
  return CELLN_PROVIDERS.find((preset) => preset.value === value);
}

/** The fixed Secret key name of a protocol (api DefaultSecretKey). */
export function secretKeyFor(protocol: CellnProtocol): string {
  return protocol === "anthropic-messages" ? "ANTHROPIC_API_KEY" : "OPENAI_API_KEY";
}

/** What the user chose on the provider step; the route is derived from it. */
export interface CellnProviderSelection {
  preset: string;
  /** The endpoint (origin, or full request URL) typed for a local or custom preset; a hosted preset's origin otherwise. */
  endpoint: string;
  protocol: CellnProtocol;
  /** Whether the endpoint takes an API key; fixed to true for a hosted preset. */
  keyed: boolean;
}

export function selectionForPreset(preset: CellnProviderPreset): CellnProviderSelection {
  return { preset: preset.value, endpoint: preset.origin || "", protocol: preset.protocol, keyed: preset.kind === "hosted" };
}

/** The origin of an http(s) URL, or "" when it is not one. */
export function endpointOrigin(endpoint: string): string {
  try {
    const url = new URL(endpoint.trim());
    return url.protocol === "http:" || url.protocol === "https:" ? url.origin : "";
  } catch {
    return "";
  }
}

/** The request path of an endpoint: the one typed, else the preset's, or the protocol's standard on a custom endpoint. */
export function presetPath(preset: CellnProviderPreset, endpoint: string, protocol: CellnProtocol = preset.protocol): string {
  try {
    const path = new URL(endpoint.trim()).pathname;
    if (path && path !== "/") return path;
  } catch { /* the preset's path */ }
  if (preset.kind === "custom") return protocol === "anthropic-messages" ? "/v1/messages" : CHAT;
  return preset.path;
}

/**
 * The route an Agent's own connection takes, in the shape the key and model
 * steps and ownKeyConnectionSpec work with: any model at exactly this origin,
 * admitted (or not) by the namespace's first policy. Undefined when the
 * endpoint is not an http(s) URL.
 */
export function routeFromPreset(preset: CellnProviderPreset, endpoint: string, mediation: CellnMediation | undefined, options: { protocol?: CellnProtocol; keyed?: boolean } = {}): CellnMediatedRoute | undefined {
  const origin = endpointOrigin(preset.kind === "hosted" ? preset.origin || endpoint : endpoint);
  if (!origin) return undefined;
  const protocol = preset.kind === "custom" ? options.protocol || preset.protocol : preset.protocol;
  const keyed = preset.kind === "hosted" ? true : options.keyed ?? false;
  return {
    provider: preset.value,
    protocol,
    models: ["*"],
    anyModel: true,
    endpointOrigins: [origin],
    policy: mediation?.policies[0],
    secretKey: keyed ? secretKeyFor(protocol) : "",
    auth: keyed ? "secret" : "none",
    ...(origin.startsWith("http:") ? { allowInsecure: true } : {}),
  };
}

export function routeFromSelection(selection: CellnProviderSelection | undefined, mediation: CellnMediation | undefined): CellnMediatedRoute | undefined {
  const preset = cellnPreset(selection?.preset);
  if (!preset || !selection) return undefined;
  return routeFromPreset(preset, selection.endpoint, mediation, { protocol: selection.protocol, keyed: selection.keyed });
}

// ── Origin guardrail (mirrors api/v1alpha1 OriginMatches and AdmitsOrigin) ──

const ORIGIN_PATTERN = /^(\*\.)?[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*(:[0-9]{1,5})?$/;

/** An exact lowercase host with an optional port, or a leading "*." wildcard. */
export function validOriginPattern(pattern: string): boolean {
  return pattern.length <= 255 && ORIGIN_PATTERN.test(pattern);
}

/** host:port → [hostname, port]; a bracketed IPv6 literal keeps its brackets off. */
function splitHostPort(host: string): [string, string] {
  if (host.startsWith("[")) {
    const end = host.indexOf("]");
    if (end === -1) return [host, ""];
    return [host.slice(1, end), host.slice(end + 2)];
  }
  const colon = host.lastIndexOf(":");
  return colon === -1 ? [host, ""] : [host.slice(0, colon), host.slice(colon + 1)];
}

/** The lowercased host (with its port) of an origin, "" when unreadable (api OriginHost). */
export function originHost(origin: string): string {
  try { return new URL(origin).host.toLowerCase(); } catch { return ""; }
}

/**
 * Whether an origin's host matches a policy pattern: the exact host (with its
 * port when the pattern names one), or any subdomain of a "*." pattern, which
 * also matches the bare domain and never "notexample.com".
 */
export function originMatches(origin: string, pattern: string): boolean {
  const host = originHost(origin);
  if (!host || !validOriginPattern(pattern)) return false;
  const [hostname, port] = splitHostPort(host);
  const [patternHost, patternPort] = splitHostPort(pattern);
  if (patternPort && patternPort !== port) return false;
  if (patternHost.startsWith("*.")) {
    const suffix = patternHost.slice(2);
    return hostname === suffix || hostname.endsWith(`.${suffix}`);
  }
  return hostname === patternHost;
}

function privateIPv4(hostname: string): boolean {
  const match = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(hostname);
  if (!match) return false;
  const [a, b] = [Number(match[1]), Number(match[2])];
  return a === 10 || a === 127 || a === 0 || (a === 172 && b >= 16 && b <= 31) || (a === 192 && b === 168) || (a === 169 && b === 254) || (a === 100 && b >= 64 && b <= 127);
}

function privateIPv6(hostname: string): boolean {
  const h = hostname.toLowerCase();
  return h === "::1" || h === "::" || /^f[cd]/.test(h) || /^fe[89ab]/.test(h);
}

/**
 * Whether an origin is private: loopback, RFC1918, link-local or CGNAT
 * addresses, cluster and mDNS suffixes (.svc, .cluster.local, .local,
 * .internal), or a hostname without a dot. The model gateway reaches such an
 * origin only once an operator approves it exactly (modelGateway.privateOrigins).
 */
export function isPrivateOrigin(origin: string): boolean {
  const host = originHost(origin);
  if (!host) return false;
  const [hostname] = splitHostPort(host);
  if (hostname.includes(":")) return privateIPv6(hostname);
  if (privateIPv4(hostname)) return true;
  if (hostname === "localhost" || !hostname.includes(".")) return true;
  return [".svc", ".cluster.local", ".local", ".internal", ".localhost"].some((suffix) => hostname.endsWith(suffix)) || hostname.includes(".svc.");
}

export type OriginStatus = "allowed" | "denied" | "needs-approval";

/** An origin as the chart value lists it: lowercase scheme://host[:port], no path. */
export function canonicalOrigin(origin: string): string {
  return endpointOrigin(origin).toLowerCase();
}

/**
 * Whether the model gateway would carry an Agent's key to this origin: a denied
 * glob wins; a private origin needs the operator's exact approval; otherwise
 * an empty allow-list admits every public origin and a declared one must match.
 */
export function originStatus(origin: string, mediation: Pick<CellnMediation, "allowedOrigins" | "deniedOrigins" | "privateOrigins">): OriginStatus {
  if ((mediation.deniedOrigins || []).some((pattern) => originMatches(origin, pattern))) return "denied";
  if (isPrivateOrigin(origin) && !(mediation.privateOrigins || []).some((approved) => canonicalOrigin(approved) === canonicalOrigin(origin))) return "needs-approval";
  const allowed = mediation.allowedOrigins || [];
  return allowed.length === 0 || allowed.some((pattern) => originMatches(origin, pattern)) ? "allowed" : "denied";
}

/** The denied glob that refused an origin, if one did. */
export function denyingPattern(origin: string, mediation: Pick<CellnMediation, "deniedOrigins">): string | undefined {
  return (mediation.deniedOrigins || []).find((pattern) => originMatches(origin, pattern));
}
