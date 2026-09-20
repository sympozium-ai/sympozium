import { useMemo, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { AlertTriangle, ChevronDown, ChevronRight, Info, XCircle } from "lucide-react";
import type { AgentRun, AgentRunTurn } from "@/lib/api";
import { diagnoseRun, type Diagnosis, type DiagnosisStep } from "@/lib/run-diagnosis";
import { useCellnMediation, useCellnPlatformProfiles, useContinueRun, useParentTurns } from "@/hooks/use-api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { cn } from "@/lib/utils";

const severityStyle = {
  error: { card: "border-destructive/50", icon: XCircle, tone: "text-destructive", label: "Failed" },
  warning: { card: "border-amber-500/50", icon: AlertTriangle, tone: "text-amber-500", label: "Needs attention" },
  info: { card: "border-blue-500/40", icon: Info, tone: "text-blue-400", label: "For information" },
} as const;

/**
 * The run's diagnosis. Fleet profiles are fetched only for an admission
 * refusal, where they let the explanation name the tool or ceiling at fault,
 * and the declared provider routes only for a route refusal.
 */
export function useRunDiagnosis(run: AgentRun, turns?: AgentRunTurn[]): Diagnosis | null {
  const base = useMemo(() => diagnoseRun(run, turns), [run, turns]);
  const profiles = useCellnPlatformProfiles(base?.kind === "admission-refused");
  // The declared routes let a route refusal say whether anything matches.
  const mediation = useCellnMediation(base?.code === "AUTH_ROUTE_MISMATCH");
  return useMemo(() => (profiles.data || mediation.data ? diagnoseRun(run, turns, { profiles: profiles.data, mediation: mediation.data }) : base), [base, profiles.data, mediation.data, run, turns]);
}

/** Self-contained panel for pages that do not already hold the turn history. */
export function RunDiagnosis({ run, onContinued }: { run: AgentRun; onContinued?: (next: AgentRun) => void }) {
  const history = useParentTurns(run, run.spec.executionLifecycle === "enduring");
  const turns = useMemo(() => history.data?.pages.flatMap((page) => page.items), [history.data]);
  const diagnosis = useRunDiagnosis(run, turns);
  return diagnosis ? <RunDiagnosisPanel run={run} diagnosis={diagnosis} onContinued={onContinued} /> : null;
}

export function RunDiagnosisPanel({ run, diagnosis, onContinued }: { run: AgentRun; diagnosis: Diagnosis; onContinued?: (next: AgentRun) => void }) {
  const style = severityStyle[diagnosis.severity];
  const Icon = style.icon;
  const [showEvidence, setShowEvidence] = useState(false);
  return (
    <Card data-testid="run-diagnosis" data-kind={diagnosis.kind} className={style.card}>
      <CardHeader className="pb-3">
        <div className="flex flex-wrap items-center gap-2">
          <Icon className={cn("h-4 w-4 shrink-0", style.tone)} />
          <CardTitle className="text-base" data-testid="run-diagnosis-title">{diagnosis.title}</CardTitle>
          <Badge variant="outline" className={style.tone}>{style.label}</Badge>
          {diagnosis.code && <Badge variant="secondary" className="font-mono normal-case" data-testid="run-diagnosis-code">{diagnosis.code}</Badge>}
        </div>
      </CardHeader>
      <CardContent className="space-y-4 text-sm">
        <div>
          <p className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">Why did this fail</p>
          <p data-testid="run-diagnosis-cause">{diagnosis.cause}</p>
        </div>
        {diagnosis.nextSteps.length > 0 && (
          <div>
            <p className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">What to do</p>
            <ol className="mt-1 list-decimal space-y-2 pl-5" data-testid="run-diagnosis-steps">
              {diagnosis.nextSteps.map((step) => <Step key={step.label} run={run} step={step} onContinued={onContinued} />)}
            </ol>
          </div>
        )}
        {diagnosis.evidence.length > 0 && (
          <div className="rounded border bg-muted/30 px-3 py-2">
            <button type="button" data-testid="run-diagnosis-evidence" aria-expanded={showEvidence} onClick={() => setShowEvidence((open) => !open)}
              className="flex w-full items-center gap-1 text-left text-xs font-semibold uppercase tracking-wider text-muted-foreground">
              {showEvidence ? <ChevronDown className="h-3 w-3" /> : <ChevronRight className="h-3 w-3" />}Evidence
            </button>
            {showEvidence && (
              <div className="mt-2 space-y-2" data-testid="run-diagnosis-evidence-text">
                {diagnosis.evidence.map((text) => <pre key={text} className="whitespace-pre-wrap break-words font-mono text-xs text-muted-foreground">{text}</pre>)}
              </div>
            )}
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function Step({ run, step, onContinued }: { run: AgentRun; step: DiagnosisStep; onContinued?: (next: AgentRun) => void }) {
  const navigate = useNavigate();
  const continueRun = useContinueRun();
  const action = step.action;
  // Same rule as the API: only a live enduring conversation that has not already been continued.
  const canContinue = run.spec.executionLifecycle === "enduring" && Boolean(run.metadata.uid) &&
    !run.metadata.deletionTimestamp && !run.status?.cellnParent?.continuedBy;
  if (action?.kind === "continue" && !canContinue) return null;
  function restart() {
    if (!run.metadata.uid || continueRun.isPending) return;
    continueRun.mutate(
      { name: run.metadata.name, namespace: run.metadata.namespace || "default", uid: run.metadata.uid },
      { onSuccess: (next) => { if (onContinued) onContinued(next); else if (next?.metadata?.name) navigate(`/runs/${encodeURIComponent(next.metadata.name)}`); } },
    );
  }
  return (
    <li data-testid="run-diagnosis-step">
      <span className="font-medium">{step.label}{/[.?!]$/.test(step.label) ? "" : "."}</span>{" "}
      <span className="text-muted-foreground">{step.detail}</span>
      {action?.kind === "link" && (
        <Button asChild variant="link" size="sm" className="h-auto px-1 py-0" data-testid="run-diagnosis-link">
          <Link to={action.to}>{action.label}</Link>
        </Button>
      )}
      {action?.kind === "continue" && (
        <Button variant="outline" size="sm" className="ml-2" data-testid="run-diagnosis-continue" disabled={continueRun.isPending} onClick={restart}>
          {continueRun.isPending ? "Restarting…" : action.label}
        </Button>
      )}
    </li>
  );
}
