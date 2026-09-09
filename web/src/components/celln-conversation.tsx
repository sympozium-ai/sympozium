import { useEffect, useState } from "react";
import { useInfiniteQuery } from "@tanstack/react-query";
import { api, type AgentRun, type AgentRunTurn } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";

type Pending = { name: string; requestId: string; message: string };

export function CellnConversation({ run, observationUnavailable = false }: { run: AgentRun; observationUnavailable?: boolean }) {
  const uid = run.metadata.uid || "";
  const namespace = run.metadata.namespace || "default";
  const storageKey = `celln-turn:${namespace}:${uid}`;
  const deleteKey = `celln-delete:${namespace}:${uid}`;
  const cancelKey = `celln-cancel:${namespace}:${uid}`;
  const [cancelledRequests, setCancelledRequests] = useState<string[]>(() => {
    try {
      const stored: unknown = JSON.parse(sessionStorage.getItem(cancelKey) || "[]");
      return Array.isArray(stored) ? stored.filter((id): id is string => typeof id === "string") : [];
    } catch { return []; }
  });
  const [deleteRequested, setDeleteRequested] = useState(() => {
    try { return sessionStorage.getItem(deleteKey) === "requested"; } catch { return false; }
  });
  const [draft, setDraft] = useState("");
  const [error, setError] = useState("");
  const [sending, setSending] = useState(false);
  const [pending, setPending] = useState<Pending | null>(() => {
    try { return JSON.parse(sessionStorage.getItem(storageKey) || "null"); } catch { return null; }
  });
  const history = useInfiniteQuery({
    queryKey: ["parent-turns", namespace, run.metadata.name, uid],
    initialPageParam: "",
    queryFn: async ({ pageParam }) => {
      const page = await api.runs.turns(run.metadata.name, namespace, pageParam);
      if (page.runUID !== uid) throw new Error("Run identity changed. Reload the run before continuing.");
      return page;
    },
    getNextPageParam: (page) => page.continue || undefined,
    enabled: Boolean(uid),
    refetchInterval: 2000,
  });
  const turns = (history.data?.pages.flatMap((page) => page.items) || []).sort((a, b) =>
    (a.metadata.creationTimestamp || "").localeCompare(b.metadata.creationTimestamp || "") || a.metadata.name.localeCompare(b.metadata.name));
  const pendingTurn = turns.find((turn) => turn.metadata.name === pending?.name);
  const completedPending = pendingTurn?.status?.execution?.result;
  useEffect(() => {
    if (completedPending) {
      sessionStorage.removeItem(storageKey);
      setPending(null);
      setError("");
    }
  }, [completedPending, storageKey]);
  const parent = run.status?.cellnParent;
  const parentCondition = run.status?.conditions?.find((condition) => condition.type === "CellnParentReady" && run.metadata.generation !== undefined && condition.observedGeneration === run.metadata.generation);
  const admissionPending = !parent && parentCondition?.status === "False" && parentCondition.reason === "AdmissionPending";
  const deleting = deleteRequested || Boolean(run.metadata.deletionTimestamp);
  const ready = !observationUnavailable && !deleting && run.status?.phase === "Running" && parentCondition?.status === "True";
  const requestedTurns = run.spec.enduring?.maxTurns || 1;
  const ceilingReached = Boolean(parent && parent.acceptedTurns >= requestedTurns - 1);
  const initialFailed = parent?.initialTurn?.result?.succeeded === false;
  const unavailableReason = parentCondition?.status === "False" ? parentCondition.reason : undefined;
  const lifecycleDetail = unavailableReason === "ContextLost"
    ? "Live harness context was lost. Recorded answers remain available, but this parent cannot resume. It will not be silently recreated."
    : unavailableReason === "Stopped"
    ? "The parent has stopped. Recorded answers remain available; this conversation cannot accept more turns."
    : unavailableReason === "TeardownUncertain"
    ? "Parent teardown is unconfirmed. Ask the operator to reconcile the original owner; do not create replacement work or assume its resources are free."
    : unavailableReason === "ReconciliationRequired"
    ? "The original parent's outcome is unconfirmed. Sending is paused while its owner is reconciled; no work will be replayed automatically."
    : ready && initialFailed
    ? "The initial turn failed. Sending is disabled; inspect the recorded failure before creating any new work."
    : ready && parent?.activeTurn
    ? "One turn is already active or awaiting reconciliation. It must have a committed result before another turn can begin."
    : ready && ceilingReached
    ? "The requested turn ceiling is exhausted, including the initial turn. Refreshing this page does not restore the budget."
    : "";
  const canCompose = ready && !initialFailed && !parent?.activeTurn && !ceilingReached && !pending && !sending;
  const bytes = new TextEncoder().encode(draft).length;
  const canSend = ready && parent?.initialTurn?.result?.succeeded && !parent.activeTurn && !pending && !sending && !history.isError && Boolean(history.data) &&
    !ceilingReached && draft.trim().length > 0 && bytes <= 2048 && !draft.includes("\0");

  async function send() {
    if (!canSend) return;
    setSending(true);
    setError("");
    try {
      const requestId = crypto.randomUUID();
      const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(JSON.stringify([uid, requestId])));
      const name = `turn-${Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, "0")).join("")}`;
      const saved = { name, requestId, message: draft };
      // Persist identity before POST. Network failure never clears this record
      // or triggers another submission; subsequent polling reconciles it.
      sessionStorage.setItem(storageKey, JSON.stringify(saved));
      setPending(saved);
      await api.runs.submitTurn(run.metadata.name, namespace, { runUID: uid, requestId, message: draft });
      setDraft("");
      await history.refetch();
    } catch {
      setError("Submission could not be confirmed. Checking the original request; do not resend it.");
    } finally { setSending(false); }
  }

  function cancellationPending(turn: AgentRunTurn) {
    return Boolean(turn.spec.cancelRequested || (turn.metadata.uid && cancelledRequests.includes(turn.metadata.uid)));
  }

  function canCancel(turn: AgentRunTurn) {
    return ready && !history.isError && Boolean(turn.metadata.uid) &&
      parent?.activeTurn?.uid === turn.metadata.uid && parent?.activeTurn?.name === turn.metadata.name &&
      Boolean(turn.status?.execution?.attempted) && !turn.status?.execution?.result && !cancellationPending(turn);
  }

  async function cancelTurn(turn: AgentRunTurn) {
    if (!canCancel(turn) || !turn.metadata.uid || !window.confirm("Cancel this turn's sub-cell? The persistent parent is not stopped. Wait for a committed result before sending another turn.")) return;
    try {
      const requests = [...cancelledRequests, turn.metadata.uid];
      // Keep the exact UID before POST so a refresh or lost response cannot
      // silently resend or target a new turn occupying the same parent slot.
      sessionStorage.setItem(cancelKey, JSON.stringify(requests));
      setCancelledRequests(requests);
      await api.runs.cancelTurn(run.metadata.name, namespace, turn.metadata.name, { runUID: uid, turnUID: turn.metadata.uid });
      await history.refetch();
    } catch {
      setError("Cancellation could not be confirmed. Checking the original turn; the request will not be resent automatically. Child teardown is not confirmed.");
    }
  }

  async function deleteRun() {
    if (!uid || deleting || !window.confirm("Delete this enduring run and its Kubernetes turn history? This requests parent/child teardown, not a pause. Live context cannot be resumed afterward.")) return;
    try {
      // Persist the intent before DELETE; uncertainty must not re-enable work
      // after refresh. The UID precondition protects against reused names.
      sessionStorage.setItem(deleteKey, "requested");
      setDeleteRequested(true);
      await api.runs.deleteEnduring(run.metadata.name, namespace, uid);
    } catch {
      setError("Deletion could not be confirmed. Reconcile this run's original UID; do not assume its parent has stopped.");
    }
  }

  const initial = parent?.initialTurn;
  return <Card data-testid="celln-conversation">
    <CardHeader><CardTitle>Persistent Celln conversation</CardTitle></CardHeader>
    <CardContent className="space-y-4">
      <p className="text-sm text-muted-foreground">The parent retains live context. Each turn runs in a disposable child cell. A recorded answer does not mean the parent is still available.</p>
      <p role="status">{ready ? "Parent initialized" : admissionPending ? "Waiting for parent admission — sending disabled" : "Parent unavailable or starting — sending disabled"}</p>
      {observationUnavailable && <p role="alert">Run status could not be refreshed. Recorded history is shown, but sending is disabled until the current run can be checked.</p>}
      {deleting && <p role="status" data-testid="celln-delete-pending">Deletion requested. Sending is disabled while the controller reconciles teardown. Acceptance of deletion is not confirmation that the parent has stopped.</p>}
      {lifecycleDetail && <p role="status" data-testid="celln-parent-lifecycle-detail">{lifecycleDetail}</p>}
      <p className="text-sm text-muted-foreground" data-testid="celln-parent-turn-limit">Requested ceiling: {requestedTurns} total turns, including the initial turn. The host may enforce stricter limits; this is not a guarantee of remaining capacity.</p>
      {admissionPending && <p className="text-sm" data-testid="celln-parent-admission">{parentCondition.message}</p>}
      {initial && <div className="space-y-2 rounded border p-3"><p className="whitespace-pre-wrap">You: {initial.message}</p><p className="whitespace-pre-wrap">{initial.result ? `${initial.result.succeeded ? "Agent" : "Initial turn failed"}: ${initial.result.answer}` : initial.attempted ? "Initial turn awaiting reconciliation" : "Initial turn queued"}</p></div>}
      {turns.map((turn) => <div key={turn.metadata.uid || turn.metadata.name} className="space-y-2 rounded border p-3">
        <p className="whitespace-pre-wrap">You: {turn.spec.message}</p>
        <p className="whitespace-pre-wrap">{turn.status?.execution?.result ? `${turn.status.execution.result.succeeded ? "Agent" : "Turn failed"}: ${turn.status.execution.result.answer}` : turn.status?.execution?.attempted ? "Submitted — awaiting committed result" : "Queued"}</p>
        {!turn.status?.execution?.result && cancellationPending(turn) && <p role="status" data-testid="celln-turn-cancel-pending">Cancellation requested for this turn only. Waiting for the original parent's committed result; child teardown is not confirmed.</p>}
        {!turn.status?.execution?.result && turn.status?.execution?.attempted && <Button variant="outline" data-testid="celln-turn-cancel" disabled={!canCancel(turn)} onClick={() => cancelTurn(turn)}>Cancel turn</Button>}
        {!turn.status?.execution?.result && turn.status?.conditions?.some((condition) => condition.type === "CellnTurnComplete" && condition.status === "False" && condition.reason === "ReconciliationRequired" && turn.metadata.generation !== undefined && condition.observedGeneration === turn.metadata.generation) && <p role="status" data-testid="celln-turn-reconciliation">Turn admission or outcome is unconfirmed. The original request is retained; do not resubmit it. Ask the operator to reconcile this turn.</p>}
      </div>)}
      {history.isError && <p role="alert">Turn history unavailable. Sending is disabled until history can be checked.</p>}
      {history.hasNextPage && <Button variant="outline" disabled={history.isFetchingNextPage} onClick={() => history.fetchNextPage()}>Load more turns</Button>}
      {pending && <p role="status">{pendingTurn ? "Waiting for the saved turn to complete." : `Checking unconfirmed request ${pending.requestId}. It will not be resubmitted automatically.`}</p>}
      {error && <p role="alert">{error}</p>}
      <label className="block space-y-2">Next message
        <textarea data-testid="celln-turn-message" className="min-h-24 w-full rounded border bg-background p-2" value={draft} onChange={(event) => setDraft(event.target.value)} disabled={!canCompose} />
      </label>
      <p className="text-sm text-muted-foreground">{bytes}/2048 UTF-8 bytes</p>
      <p className="text-sm text-muted-foreground">Retained conversation context is also bounded by the selected harness. A message below this input limit may still exceed its remaining context capacity.</p>
      <Button data-testid="celln-turn-send" disabled={!canSend} onClick={send}>Send turn</Button>
      <div className="border-t pt-4">
        <p className="text-sm text-muted-foreground">Deletion stops this parent and removes the Kubernetes run/turn history after cleanup. It is not pause/resume; privately retained host audit may remain.</p>
        <Button variant="destructive" data-testid="celln-delete-run" disabled={!uid || deleting || sending} onClick={deleteRun}>Delete run and stop parent</Button>
      </div>
    </CardContent>
  </Card>;
}
