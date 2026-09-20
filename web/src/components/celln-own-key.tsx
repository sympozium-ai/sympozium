import { useState, type ComponentType } from "react";
import { Bot, Check, Copy, Loader2, RefreshCw } from "lucide-react";
import { getNamespace, type CellnMediatedRoute, type CellnMediation } from "@/lib/api";
import { useCellnKeySecrets } from "@/hooks/use-api";
import { GUIDE_URL, offersThinkingSwitch, routeId, type KeyChoice, type OwnKeyStep } from "@/lib/celln-own-key";
import { CellnModelParametersField } from "@/components/celln-model-parameters";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { cn } from "@/lib/utils";

export interface ProviderChoice {
  value: string;
  label: string;
  icon: ComponentType<{ className?: string }>;
}

const ROUTE_FLAG = "--celln-mediated-route provider=anthropic,protocol=anthropic-messages,origin=https://api.anthropic.com,models=claude-sonnet-5";

/** Why no provider can be chosen, and what the operator runs to change that. */
function NoRoutes({ mediation }: { mediation: CellnMediation }) {
  const pending = mediation.pending.length > 0;
  return (
    <div role="status" className="space-y-2 rounded-lg border border-amber-500/30 bg-amber-500/5 p-3 text-xs text-muted-foreground" data-testid={mediation.enabled ? "celln-no-routes" : "celln-mediation-disabled"}>
      <p className="text-sm font-medium text-foreground">
        {!mediation.enabled ? "Mediated model access is not enabled on this cluster" : pending ? "The declared providers are not published yet" : "No provider is declared for this namespace"}
      </p>
      {!mediation.enabled ? (
        <p>A Celln Agent brings its own provider key, which the model gateway adds to its requests. That needs mediated model access, which is off (<code>celln.mediation.enabled=false</code>). An operator enables it and declares each provider an Agent may bring a key for:</p>
      ) : pending ? (
        <p>The operator declared {mediation.pending.map((route) => route.provider).join(", ")}, but no execution policy carries {mediation.pending.length === 1 ? "it" : "them"} yet, so a run would be refused <code>AUTH_ROUTE_MISMATCH</code>. An operator publishes {mediation.pending.length === 1 ? "it" : "them"} with:</p>
      ) : (
        <p>Mediation is enabled, but enabling it admits nothing by itself: an operator declares each provider, model and endpoint origin an Agent in <code>{getNamespace()}</code> may bring a key for. Until then every run would be refused <code>AUTH_ROUTE_MISMATCH</code>.</p>
      )}
      <pre className="overflow-x-auto whitespace-pre-wrap break-all rounded bg-muted/50 p-2 font-mono text-[11px] text-foreground" data-testid="celln-route-command">
        {mediation.enabled && pending
          ? "sympozium celln-mediation apply-routes"
          : `sympozium install --celln-fleet … ${mediation.enabled ? "" : "--set celln.mediation.enabled=true --set celln.mediation.clusterId=<id> … "}\\\n  ${ROUTE_FLAG}`}
      </pre>
      <p>
        Repeat the flag per provider; <code>models</code> and <code>origin</code> take several values joined with <code>+</code>. See <a className="underline" href={GUIDE_URL} target="_blank" rel="noreferrer">Mediated model access for native Celln Agents</a> (sections 5 and 6). <code>sympozium doctor</code> reports what is declared and published.
      </p>
    </div>
  );
}

/**
 * The Provider step of a Celln Agent: only the providers the operator declared
 * for this namespace. There is no other way to run one, so without a route the
 * step says what the operator must do instead of offering something else.
 */
export function CellnRouteStep({ mediation, isLoading, error, providers, selected, onSelect }: {
  mediation?: CellnMediation;
  isLoading: boolean;
  error?: string;
  providers: ProviderChoice[];
  selected?: CellnMediatedRoute;
  onSelect: (route: CellnMediatedRoute) => void;
}) {
  if (isLoading) return <p className="flex items-center gap-2 text-xs text-muted-foreground"><Loader2 className="h-3.5 w-3.5 animate-spin" /> Reading the providers declared for this namespace…</p>;
  if (error || !mediation) return <p role="alert" className="break-words text-xs text-red-400" data-testid="celln-mediation-error">Could not read the declared providers: {error || "no answer"}</p>;
  if (!mediation.enabled || mediation.routes.length === 0) return <NoRoutes mediation={mediation} />;
  const meta = (route: CellnMediatedRoute) => providers.find((provider) => provider.value === route.provider);
  // Two routes may share a provider name (another protocol or origin); say which.
  const ambiguous = (route: CellnMediatedRoute) => mediation.routes.filter((candidate) => candidate.provider === route.provider).length > 1;
  return (
    <div className="space-y-4" data-testid="celln-route-step">
      <div className="space-y-2">
        <Label>AI Provider</Label>
        <Select value={selected ? routeId(selected) : ""} onValueChange={(value) => { const route = mediation.routes.find((candidate) => routeId(candidate) === value); if (route) onSelect(route); }}>
          <SelectTrigger data-testid="celln-route-select"><SelectValue placeholder="Select a provider…" /></SelectTrigger>
          <SelectContent>
            {mediation.routes.map((route) => {
              const Icon = meta(route)?.icon || Bot;
              return (
                <SelectItem key={routeId(route)} value={routeId(route)}>
                  <span className="flex items-center gap-2">
                    <Icon className="h-4 w-4 shrink-0" />
                    {meta(route)?.label || route.provider}
                    {ambiguous(route) && <span className="text-xs text-muted-foreground">{route.protocol} · {route.endpointOrigins.join(", ")}</span>}
                  </span>
                </SelectItem>
              );
            })}
          </SelectContent>
        </Select>
      </div>
      {selected && (
        <p className="break-words text-xs text-muted-foreground" data-testid="celln-route-summary">
          Declared by the operator in policy <code>{selected.policy}</code>: {selected.protocol} on {selected.endpointOrigins.join(", ")}, models {selected.models.join(", ")}. {selected.auth === "none" ? "No key is required. This Agent gets its own model connection." : "This Agent gets its own key and its own model connection; nothing is shared with another Agent."}
        </p>
      )}
      <p className="text-xs text-muted-foreground">Only providers an operator declared for this namespace are listed. Another provider, model or endpoint needs a declared route first (<a className="underline" href={GUIDE_URL} target="_blank" rel="noreferrer">guide</a>).</p>
    </div>
  );
}

/**
 * The Auth step: paste a key (the API writes it into a Secret in this
 * namespace under the protocol's fixed key name) or link a Secret that already
 * holds it. The pasted key lives in this component's parent state only until
 * it is submitted; it is never stored, logged or shown back.
 */
export function CellnKeyStep({ route, value, onChange, managedSecretName }: {
  route: CellnMediatedRoute;
  value: KeyChoice;
  onChange: (choice: KeyChoice) => void;
  /** The Secret a pasted key is written to. */
  managedSecretName: string;
}) {
  const namespace = getNamespace();
  const secrets = useCellnKeySecrets(route.secretKey, route.auth !== "none" && value.mode === "existing");
  const [copied, setCopied] = useState(false);
  if (route.auth === "none") return <p data-testid="celln-keyless-route" className="text-sm text-muted-foreground">No API key is required for this model endpoint. The gateway sends requests without credentials to the route approved by your operator.</p>;
  const command = `kubectl -n ${namespace} create secret generic <name> --from-literal=${route.secretKey}=<your key>`;
  const tab = (mode: KeyChoice["mode"], label: string) => (
    <button
      type="button"
      data-testid={`celln-key-mode-${mode}`}
      aria-pressed={value.mode === mode}
      onClick={() => onChange(mode === "create" ? { mode, apiKey: "" } : { mode, secretName: "" })}
      className={cn("flex-1 rounded-md border px-3 py-2 text-xs transition-colors", value.mode === mode ? "border-blue-500/40 bg-blue-500/15 text-blue-300" : "border-border/50 hover:bg-white/5")}
    >
      {label}
    </button>
  );
  return (
    <div className="space-y-4" data-testid="celln-key-step">
      <div className="flex gap-2">{tab("create", "Create a new key")}{tab("existing", "Use an existing Secret")}</div>
      {value.mode === "create" ? (
        <div className="space-y-2">
          <Label htmlFor="celln-api-key">{route.provider} API key</Label>
          <Input id="celln-api-key" data-testid="celln-api-key" type="password" autoComplete="off" spellCheck={false} value={value.apiKey} onChange={(e) => onChange({ mode: "create", apiKey: e.target.value })} placeholder="Paste the key" />
          <p className="break-words text-xs text-muted-foreground">
            Written once to Secret <code>{managedSecretName}</code> in namespace <code>{namespace}</code> under the key <code>{route.secretKey}</code>, which the {route.protocol} protocol fixes. Only the model gateway reads it; the console never shows it again.
          </p>
        </div>
      ) : (
        <div className="space-y-2">
          <div className="flex items-center justify-between gap-2">
            <Label>Secret in {namespace}</Label>
            <Button type="button" size="sm" variant="ghost" className="h-7 gap-1 text-xs" data-testid="celln-key-refresh" onClick={() => secrets.refetch()} disabled={secrets.isFetching}>
              <RefreshCw className={cn("h-3 w-3", secrets.isFetching && "animate-spin")} /> Refresh
            </Button>
          </div>
          {secrets.isError ? (
            <p role="alert" className="break-words text-xs text-red-400">Could not list Secrets: {secrets.error instanceof Error ? secrets.error.message : "request failed"}</p>
          ) : secrets.isLoading ? (
            <p className="flex items-center gap-2 text-xs text-muted-foreground"><Loader2 className="h-3.5 w-3.5 animate-spin" /> Looking for Secrets holding {route.secretKey}…</p>
          ) : (secrets.data || []).length === 0 ? (
            <p role="status" className="text-xs text-amber-500" data-testid="celln-key-none">No Secret in <code>{namespace}</code> holds <code>{route.secretKey}</code> yet. Create one with the command below, then refresh.</p>
          ) : (
            <ScrollArea className="max-h-36 rounded-md border border-border/50">
              <div className="space-y-0.5 p-1" data-testid="celln-key-secrets">
                {(secrets.data || []).map((secret) => (
                  <button
                    key={secret.name}
                    type="button"
                    data-secret={secret.name}
                    onClick={() => onChange({ mode: "existing", secretName: secret.name })}
                    className={cn("flex w-full items-center gap-2 rounded-md border px-2.5 py-1.5 text-left font-mono text-xs transition-colors", secret.name === value.secretName ? "border-blue-500/30 bg-blue-500/15 text-blue-400" : "border-transparent text-foreground hover:bg-white/5")}
                  >
                    {secret.name === value.secretName && <Check className="h-3 w-3 shrink-0" />}
                    <span className="truncate">{secret.name}</span>
                    {secret.managed && <span className="ml-auto shrink-0 font-sans text-[10px] text-muted-foreground">created by the console</span>}
                  </button>
                ))}
              </div>
            </ScrollArea>
          )}
          <p className="break-words text-xs text-muted-foreground">
            The Secret must be in namespace <code>{namespace}</code> and hold the key under exactly <code>{route.secretKey}</code> (the {route.protocol} protocol fixes the name, whatever the provider is called). Only Secrets that already do are listed; their values are never read here. To create one yourself:
          </p>
          <div className="flex items-start gap-2">
            <pre className="min-w-0 flex-1 overflow-x-auto whitespace-pre-wrap break-all rounded bg-muted/50 p-2 font-mono text-[11px]" data-testid="celln-key-command">{command}</pre>
            <Button type="button" size="sm" variant="outline" className="h-7 shrink-0 gap-1 text-xs" onClick={() => { navigator.clipboard?.writeText(command).then(() => { setCopied(true); setTimeout(() => setCopied(false), 2000); }).catch(() => undefined); }}>
              {copied ? <Check className="h-3 w-3" /> : <Copy className="h-3 w-3" />} {copied ? "Copied" : "Copy"}
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}

/**
 * The Model step: exactly the models and endpoint origins of the chosen route
 * (admission matches them exactly, so nothing is free text), and this Agent's
 * own model parameters and output-token limit.
 */
export function CellnModelStep({ route, model, onModel, origin, onOrigin, path, onPath, parameters, onParameters, maxOutputTokens, onMaxOutputTokens }: {
  route: CellnMediatedRoute;
  model: string;
  onModel: (model: string) => void;
  origin: string;
  onOrigin: (origin: string) => void;
  path: string;
  onPath: (path: string) => void;
  parameters: string;
  onParameters: (text: string) => void;
  maxOutputTokens: string;
  onMaxOutputTokens: (text: string) => void;
}) {
  return (
    <div className="max-h-[60vh] space-y-4 overflow-y-auto pr-1" data-testid="celln-model-step">
      <div className="space-y-2">
        <Label>Model</Label>
        <div className="space-y-0.5 rounded-md border border-border/50 p-1" data-testid="celln-models">
          {route.models.map((candidate) => (
            <button
              key={candidate}
              type="button"
              data-model={candidate}
              aria-pressed={candidate === model}
              onClick={() => onModel(candidate)}
              className={cn("flex w-full items-center gap-2 rounded-md border px-2.5 py-1.5 text-left font-mono text-xs transition-colors", candidate === model ? "border-blue-500/30 bg-blue-500/15 text-blue-400" : "border-transparent text-foreground hover:bg-white/5")}
            >
              {candidate === model && <Check className="h-3 w-3 shrink-0" />}
              <span className="truncate">{candidate}</span>
            </button>
          ))}
        </div>
        <p className="text-xs text-muted-foreground">The operator declared these models for {route.provider}. A run on any other model is refused, so there is no free-text model here.</p>
      </div>
      <div className="space-y-2">
        <Label>Endpoint</Label>
        <div className="flex flex-wrap items-center gap-2">
          {route.endpointOrigins.length > 1 ? (
            <Select value={origin} onValueChange={onOrigin}>
              <SelectTrigger className="w-auto min-w-[14rem] font-mono text-xs" data-testid="celln-origin-select"><SelectValue placeholder="Choose an origin" /></SelectTrigger>
              <SelectContent>{route.endpointOrigins.map((candidate) => <SelectItem key={candidate} value={candidate}>{candidate}</SelectItem>)}</SelectContent>
            </Select>
          ) : (
            <code className="rounded bg-muted/50 px-2 py-1.5 text-xs" data-testid="celln-origin">{origin}</code>
          )}
          <Input aria-label="Request path" data-testid="celln-endpoint-path" className="h-8 w-56 font-mono text-xs" spellCheck={false} value={path} onChange={(e) => onPath(e.target.value)} />
        </div>
        <p className="text-xs text-muted-foreground">The origin is the operator's; only the request path is yours, and it defaults to the {route.protocol} standard.</p>
      </div>
      <CellnModelParametersField value={parameters} onChange={onParameters} showThinking={offersThinkingSwitch(route)} maxOutputTokens={maxOutputTokens} onMaxOutputTokensChange={onMaxOutputTokens} />
    </div>
  );
}

/** What exists and what does not, after a save that did not finish. */
export function OwnKeyProgress({ steps, error }: { steps: OwnKeyStep[]; error: string }) {
  const label: Record<OwnKeyStep["state"], string> = { done: "done", failed: "failed", "not-started": "not created" };
  return (
    <div role="alert" className="space-y-2 rounded-lg border border-red-500/30 bg-red-500/5 p-3 text-xs" data-testid="celln-own-key-progress">
      <p className="break-words font-medium text-red-400">{error}</p>
      <ul className="space-y-1">
        {steps.map((step) => (
          <li key={step.step} data-step={step.step} data-state={step.state} className="flex flex-wrap items-baseline gap-x-2">
            <span className={cn("w-20 shrink-0 font-medium", step.state === "done" ? "text-emerald-400" : step.state === "failed" ? "text-red-400" : "text-muted-foreground")}>{label[step.state]}</span>
            <code className="break-all">{step.object}</code>
            {step.note && <span className="text-muted-foreground">({step.note})</span>}
          </li>
        ))}
      </ul>
      <p className="text-muted-foreground">Every step is safe to repeat: fix the cause and press Create again, and what is already done is kept. Nothing above is removed automatically.</p>
    </div>
  );
}
