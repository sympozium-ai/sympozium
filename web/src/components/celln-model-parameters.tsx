import { useState } from "react";
import { DEFAULT_MAX_OUTPUT_TOKENS, MAX_MAX_OUTPUT_TOKENS, MIN_MAX_OUTPUT_TOKENS, compactModelParameters, describeTurnReservation, parseMaxOutputTokens, parseModelParameters, thinkingDisabled, withThinkingDisabled, type ModelParameters } from "@/lib/model-parameters";

/**
 * The "Advanced" part of an Agent's model connection: the model parameters
 * (ModelConnection spec.parameters) and the output tokens one request may
 * produce (spec.maxOutputTokens). The JSON text is the one value of the
 * parameters: the "Disable thinking" checkbox reads and rewrites the same
 * object, so the two never disagree.
 */
export function CellnModelParametersField({ value, onChange, showThinking, maxOutputTokens, onMaxOutputTokensChange }: { value: string; onChange: (text: string) => void; showThinking: boolean; maxOutputTokens: string; onMaxOutputTokensChange: (text: string) => void }) {
  const [open, setOpen] = useState(false);
  const parsed = parseModelParameters(value);
  const tokens = parseMaxOutputTokens(maxOutputTokens);
  const object = currentObject(value);
  return (
    <details className="rounded-md border p-2 text-xs sm:col-span-2" data-testid="celln-model-parameters" open={open} onToggle={(e) => setOpen(e.currentTarget.open)}>
      <summary className="cursor-pointer select-none text-sm">Advanced: model parameters and output tokens</summary>
      <div className="mt-2 space-y-2">
        <p className="text-muted-foreground">
          A JSON object the model gateway merges into every model request of this Agent. It belongs to this Agent's model connection alone; the agent cannot see it, and a request that tries to set one of its keys is refused.
        </p>
        {showThinking && (
          <label className="flex items-start gap-2">
            <input
              type="checkbox"
              className="mt-0.5"
              data-testid="celln-disable-thinking"
              disabled={object === undefined}
              checked={thinkingDisabled(object)}
              onChange={(e) => {
                const next = withThinkingDisabled(object, e.target.checked);
                onChange(Object.keys(next).length ? JSON.stringify(next, null, 2) : "");
              }}
            />
            <span>
              <span className="text-foreground">Disable thinking (reasoning models)</span>
              <span className="block text-muted-foreground">A request may produce {DEFAULT_MAX_OUTPUT_TOKENS} output tokens by default; a reasoning model can spend them all thinking and return nothing.</span>
            </span>
          </label>
        )}
        <label className="block space-y-1">
          <span>Parameters (JSON)</span>
          <textarea
            className="min-h-20 w-full rounded-md border bg-transparent p-2 font-mono text-xs"
            data-testid="celln-model-parameters-json"
            spellCheck={false}
            value={value}
            onChange={(e) => onChange(e.target.value)}
            placeholder={'{"chat_template_kwargs": {"enable_thinking": false}}'}
          />
        </label>
        {parsed.error && <p role="alert" className="break-words text-red-500" data-testid="celln-model-parameters-error">{parsed.error}</p>}
        <label className="block space-y-1">
          <span>Max output tokens per request</span>
          <input
            type="number"
            inputMode="numeric"
            min={MIN_MAX_OUTPUT_TOKENS}
            max={MAX_MAX_OUTPUT_TOKENS}
            step={1}
            className="block w-40 rounded-md border bg-transparent p-2 font-mono text-xs"
            data-testid="celln-max-output-tokens"
            value={maxOutputTokens}
            onChange={(e) => onMaxOutputTokensChange(e.target.value)}
            placeholder={String(DEFAULT_MAX_OUTPUT_TOKENS)}
          />
        </label>
        <p className="text-muted-foreground" data-testid="celln-max-output-tokens-guidance">
          <span className="block">{DEFAULT_MAX_OUTPUT_TOKENS} suits chat with thinking disabled.</span>
          <span className="block">A reasoning model left thinking needs 2048–4096, which costs 4–8× the tokens per turn. The turn limit grows with it: 60 seconds at 512, 4 minutes at 2048, 5 minutes at most.</span>
        </p>
        <p className="text-muted-foreground">
          {MIN_MAX_OUTPUT_TOKENS}–{MAX_MAX_OUTPUT_TOKENS}, blank for {DEFAULT_MAX_OUTPUT_TOKENS}. The limit is this Agent's own and the model gateway enforces it: a request that asks for more is refused. You can change it later on the Agent's Harness tab.
        </p>
        {tokens.error
          ? <p role="alert" className="break-words text-red-500" data-testid="celln-max-output-tokens-error">{tokens.error}</p>
          : <p className="text-foreground" data-testid="celln-turn-reservation">{describeTurnReservation(tokens.maxOutputTokens)}</p>}
      </div>
    </details>
  );
}

/** The object the text holds, valid or not; undefined when it is not a JSON object. */
function currentObject(text: string): ModelParameters | undefined {
  if (!text.trim()) return {};
  try {
    const value: unknown = JSON.parse(text);
    return typeof value === "object" && value !== null && !Array.isArray(value) ? (value as ModelParameters) : undefined;
  } catch {
    return undefined;
  }
}

/** A connection's output cap per request, shown only when it is not the default. */
export function CellnMaxOutputTokensSummary({ maxOutputTokens, testId }: { maxOutputTokens?: number; testId?: string }) {
  if (!maxOutputTokens || maxOutputTokens === DEFAULT_MAX_OUTPUT_TOKENS) return null;
  return (
    <span className="text-muted-foreground" title={describeTurnReservation(maxOutputTokens)} data-testid={testId}>
      up to {maxOutputTokens} output tokens per request
    </span>
  );
}

/** A connection's parameters on one line, truncated, with the whole object as the tooltip. */
export function CellnModelParametersSummary({ parameters, testId }: { parameters?: ModelParameters; testId?: string }) {
  const text = compactModelParameters(parameters);
  if (!text) return null;
  return (
    <span className="inline-block max-w-[24rem] truncate align-bottom font-mono text-muted-foreground" title={text} data-testid={testId}>
      sends {text}
    </span>
  );
}
