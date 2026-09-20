// Model parameters of an Agent's ModelConnection (spec.parameters): a bounded
// JSON object the model gateway merges into every provider request; the guest
// never sees it. These are the rules of api/v1alpha1 ValidateModelParameters
// (Go), which the API applies again; keep the two in step.

export type ModelParameters = Record<string, unknown>;

const KEY = /^[a-z][a-z0-9_]{0,63}$/;
const MAX_KEYS = 16;
const MAX_DEPTH = 3;
const MAX_STRING_BYTES = 256;
const MAX_ARRAY_ITEMS = 8;
const MAX_BYTES = 2048;

/** Request fields the gateway owns; a parameter may not set them. */
export const RESERVED_MODEL_PARAMETERS = ["model", "messages", "system", "stream", "stream_options", "max_tokens", "max_completion_tokens", "n", "tools", "tool_choice", "functions", "function_call", "parallel_tool_calls", "user"];

/** What a llama-server (or another chat-template server) takes to answer without a reasoning phase. */
export const THINKING_OFF: ModelParameters = { chat_template_kwargs: { enable_thinking: false } };

const bytes = (text: string) => new TextEncoder().encode(text).length;
const isObject = (value: unknown): value is ModelParameters => typeof value === "object" && value !== null && !Array.isArray(value);

function scalarError(value: unknown, at: string): string | null {
  if (typeof value === "boolean") return null;
  if (typeof value === "number") return Number.isFinite(value) ? null : `${at} must be a finite number`;
  if (typeof value === "string") return bytes(value) > MAX_STRING_BYTES || value.includes("\u0000") ? `${at} must be a string of at most ${MAX_STRING_BYTES} bytes without NUL` : null;
  if (value === null || value === undefined) return `${at} is null; use a boolean, number, string, object or array`;
  return `${at} has an unsupported type`;
}

function objectError(object: ModelParameters, path: string, depth: number): string | null {
  if (depth > MAX_DEPTH) return `${path} nests deeper than ${MAX_DEPTH} levels`;
  for (const key of Object.keys(object).sort()) {
    const at = path ? `${path}.${key}` : key;
    if (!KEY.test(key)) return `key "${at}" must match ${KEY.source}`;
    const value = object[key];
    let error: string | null;
    if (isObject(value)) error = objectError(value, at, depth + 1);
    else if (Array.isArray(value)) {
      error = value.length > MAX_ARRAY_ITEMS ? `${at} holds ${value.length} items, at most ${MAX_ARRAY_ITEMS}` : null;
      for (let i = 0; !error && i < value.length; i++) {
        error = isObject(value[i]) || Array.isArray(value[i]) ? `${at}[${i}] must be a boolean, number or string` : scalarError(value[i], `${at}[${i}]`);
      }
    } else error = scalarError(value, at);
    if (error) return error;
  }
  return null;
}

/** The first rule the object breaks, or null when the API accepts it. */
export function validateModelParameters(parameters: unknown): string | null {
  if (!isObject(parameters)) return "model parameters: a JSON object is required";
  const keys = Object.keys(parameters);
  if (keys.length > MAX_KEYS) return `model parameters: at most ${MAX_KEYS} top-level keys, got ${keys.length}`;
  const reserved = keys.sort().find((key) => RESERVED_MODEL_PARAMETERS.includes(key));
  if (reserved) return `model parameters: "${reserved}" is reserved (the gateway sets it on every request)`;
  const error = objectError(parameters, "", 1);
  if (error) return `model parameters: ${error}`;
  const size = bytes(JSON.stringify(parameters));
  return size > MAX_BYTES ? `model parameters: serialized size ${size} bytes exceeds ${MAX_BYTES}` : null;
}

/** Parses the form's JSON text. Blank text and {} mean no parameters. */
export function parseModelParameters(text: string): { parameters?: ModelParameters; error?: string } {
  if (!text.trim()) return {};
  let value: unknown;
  try {
    value = JSON.parse(text);
  } catch (e) {
    return { error: `model parameters: not valid JSON: ${e instanceof Error ? e.message : String(e)}` };
  }
  const error = validateModelParameters(value);
  if (error) return { error };
  return Object.keys(value as ModelParameters).length ? { parameters: value as ModelParameters } : {};
}

/** Whether the object switches the chat template's reasoning phase off. */
export function thinkingDisabled(parameters: ModelParameters | undefined): boolean {
  const kwargs = parameters?.chat_template_kwargs;
  return isObject(kwargs) && kwargs.enable_thinking === false;
}

/** The same object with the reasoning switch set or removed; everything else is kept. */
export function withThinkingDisabled(parameters: ModelParameters | undefined, disabled: boolean): ModelParameters {
  const next: ModelParameters = { ...(parameters || {}) };
  const kwargs: ModelParameters = isObject(next.chat_template_kwargs) ? { ...next.chat_template_kwargs } : {};
  if (disabled) kwargs.enable_thinking = false;
  else delete kwargs.enable_thinking;
  if (Object.keys(kwargs).length) next.chat_template_kwargs = kwargs;
  else delete next.chat_template_kwargs;
  return next;
}

/** Compact JSON for a list line; empty when there are none. */
export function compactModelParameters(parameters: ModelParameters | undefined): string {
  return parameters && Object.keys(parameters).length ? JSON.stringify(parameters) : "";
}

// Output tokens one model request through a connection may produce
// (spec.maxOutputTokens; api/v1alpha1 ValidateRequestOutputTokens in Go, which
// the API applies again). A turn reserves TURN_MODEL_REQUESTS requests of it.
export const DEFAULT_MAX_OUTPUT_TOKENS = 512;
export const MIN_MAX_OUTPUT_TOKENS = 256;
export const MAX_MAX_OUTPUT_TOKENS = 4096;
const TURN_MODEL_REQUESTS = 6;

/**
 * Reads the "Max output tokens per request" field. Blank keeps the default;
 * the default itself is sent as nothing, so the connection is saved exactly
 * as one that never named it.
 */
export function parseMaxOutputTokens(text: string): { maxOutputTokens?: number; error?: string } {
  const trimmed = text.trim();
  if (!trimmed) return {};
  const value = Number(trimmed);
  if (!/^\d+$/.test(trimmed) || !Number.isInteger(value) || value < MIN_MAX_OUTPUT_TOKENS || value > MAX_MAX_OUTPUT_TOKENS) {
    return { error: `max output tokens per request must be a whole number from ${MIN_MAX_OUTPUT_TOKENS} to ${MAX_MAX_OUTPUT_TOKENS} (leave it blank for the default ${DEFAULT_MAX_OUTPUT_TOKENS})` };
  }
  return value === DEFAULT_MAX_OUTPUT_TOKENS ? {} : { maxOutputTokens: value };
}

/** What one turn reserves from a conversation's lifetime output tokens. */
export function turnOutputTokens(maxOutputTokens?: number): number {
  return TURN_MODEL_REQUESTS * (maxOutputTokens || DEFAULT_MAX_OUTPUT_TOKENS);
}

/** "one turn reserves N tokens (6 requests)". */
export function describeTurnReservation(maxOutputTokens?: number): string {
  return `one turn reserves ${turnOutputTokens(maxOutputTokens)} tokens (${TURN_MODEL_REQUESTS} requests)`;
}
