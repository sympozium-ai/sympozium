import { useState } from "react";
import type { Agent, AgentRun } from "@/lib/api";
import { useContinueRun, useCreateRun } from "@/hooks/use-api";
import { CellnConversation } from "@/components/celln-conversation";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";

import { LEGACY_ENDURING_DEFAULTS } from "@/lib/agent-execution";
import { useCellnPlatformProfiles } from "@/hooks/use-api";

/**
 * Interactive chat for a native Celln Agent. Each conversation is an enduring
 * AgentRun (its own host-native parent with its own context); the first message
 * is the initial turn and follow-up turns are durable AgentRunTurn records. An
 * Agent may hold any number of conversations at once; the platform, not this
 * view, decides capacity.
 */
export function CellnAgentConversation({
  agent,
  parents = [],
}: {
  agent: Agent;
  /** This Agent's enduring runs, newest first. */
  parents?: AgentRun[];
}) {
  const createRun = useCreateRun();
  const continueRun = useContinueRun();
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");
  const [selected, setSelected] = useState<string>("");
  const [composing, setComposing] = useState(false);
  // "Answer once" sends the message as a one-shot: a single-turn parent that
  // answers and releases its cells, on the same backend as a conversation.
  const [once, setOnce] = useState(false);

  const execution = agent.spec.execution;
  const runtimeRef =
    agent.spec.runtimeRef || execution?.cellnSelection?.runtimeRef || "";
  const toolRefs = execution?.cellnSelection?.toolRefs || [];
  const clusterToolRefs = execution?.cellnSelection?.clusterToolRefs || [];
  const model = execution?.model || agent.spec.agents?.default?.model || "";
  const modelConnectionRef = execution?.modelConnectionRef;
  const provider = modelConnectionRef ? undefined : execution?.provider;
  // An Agent created before session defaults existed carries no budget; take
  // the platform profile's suggestion when there is one, else the legacy one.
  const profiles = useCellnPlatformProfiles();
  const platformProfile = profiles.data?.find((profile) => profile.wrapper === runtimeRef);
  const platformDefaults = platformProfile?.sessionDefaults;
  const limits = execution?.enduring || platformDefaults || LEGACY_ENDURING_DEFAULTS;

  // A conversation whose parent was lost carries on in the run that continues
  // it; follow that link rather than leaving the reader on a dead parent.
  const chosen = parents.find((run) => run.metadata.name === selected) || parents[0];
  const continuedBy = chosen?.status?.cellnParent?.continuedBy;
  const current = (continuedBy && parents.find((run) => run.metadata.name === continuedBy)) || chosen;
  const showComposer = composing || parents.length === 0;
  const canRestart = Boolean(current && current.spec.executionLifecycle === "enduring" && !current.metadata.deletionTimestamp && !continueRun.isPending);
  function restart() {
    if (!current?.metadata.uid) return;
    continueRun.mutate(
      { name: current.metadata.name, namespace: current.metadata.namespace || "default", uid: current.metadata.uid },
      { onSuccess: (run) => { if (run?.metadata?.name) setSelected(run.metadata.name); setComposing(false); } },
    );
  }

  function start() {
    const text = message.trim();
    if (!text || createRun.isPending) return;
    setError("");
    createRun.mutate(
      {
        agentRef: agent.metadata.name,
        task: text,
        backend: "celln",
        // A fleet profile binds its persona; the run must carry it verbatim.
        ...(platformProfile?.systemPrompt ? { systemPrompt: platformProfile.systemPrompt } : {}),
        model: model || undefined,
        modelConnectionRef,
        provider,
        cellnSelection: { runtimeRef: runtimeRef || undefined, toolRefs, ...(clusterToolRefs.length ? { clusterToolRefs } : {}) },
        ...(once
          ? { executionLifecycle: "one-shot" as const }
          : { timeout: `${limits.leaseSeconds}s`, executionLifecycle: "enduring" as const, enduring: limits }),
      },
      {
        onSuccess: (run) => {
          setMessage("");
          setComposing(false);
          if (run?.metadata?.name) setSelected(run.metadata.name);
        },
        onError: (err) =>
          setError(
            err instanceof Error
              ? err.message
              : "Could not start the conversation",
          ),
      },
    );
  }

  const conversationList = parents.length > 0 && (
    <div className="flex flex-wrap items-center gap-2" data-testid="celln-agent-conversations">
      {parents.map((run) => {
        const active = !showComposer && run.metadata.name === current?.metadata.name;
        return (
          <Button
            key={run.metadata.uid || run.metadata.name}
            size="sm"
            variant={active ? "default" : "outline"}
            onClick={() => { setSelected(run.metadata.name); setComposing(false); }}
            title={run.spec.task ? String(run.spec.task).slice(0, 200) : undefined}
          >
            {run.metadata.name}
            <span className="ml-2 text-xs opacity-70">{run.status?.phase || "Pending"}</span>
            {run.spec.conversation?.continuesFrom && <span className="ml-2 text-xs opacity-70" title={`Continues ${run.spec.conversation.continuesFrom} with ${run.spec.conversation.seed?.length || 0} remembered exchange(s)`}>↺ continued</span>}
          </Button>
        );
      })}
      <Button size="sm" variant={showComposer ? "default" : "secondary"} data-testid="celln-agent-new-conversation" onClick={() => setComposing(true)}>
        New conversation
      </Button>
      {!showComposer && canRestart && (
        <Button size="sm" variant="outline" data-testid="celln-agent-restart" onClick={restart} title="Move this conversation to a new parent on any node with capacity, seeded with what was said so far. The current run is deleted.">
          Restart elsewhere
        </Button>
      )}
    </div>
  );

  if (!showComposer && current) {
    return (
      <div className="space-y-3">
        {conversationList}
        <CellnConversation run={current} onContinued={(next) => { if (next?.metadata?.name) setSelected(next.metadata.name); setComposing(false); }} />
      </div>
    );
  }

  return (
    <div className="space-y-3">
    {conversationList}
    <Card data-testid="celln-agent-conversation">
      <CardHeader>
        <CardTitle className="text-base">
          Enduring Celln conversation
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        <p className="text-sm text-muted-foreground">
          Start an enduring native parent. The first message runs as the initial
          turn; each follow-up turn runs in a disposable child cell. Every
          conversation is its own parent with its own context, so you can keep
          several open for this Agent at once.
        </p>
        <label className="block space-y-2">
          <span className="text-sm font-medium">First message</span>
          <textarea
            data-testid="celln-agent-first-message"
            className="min-h-24 w-full rounded border bg-background p-2 text-sm"
            value={message}
            onChange={(event) => setMessage(event.target.value)}
            disabled={createRun.isPending}
            placeholder="Ask the parent a question to begin…"
          />
        </label>
        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            data-testid="celln-agent-once"
            checked={once}
            onChange={(event) => setOnce(event.target.checked)}
            disabled={createRun.isPending}
          />
          Answer once (one-shot: no follow-up turns, cells released after the answer)
        </label>
        <Button
          data-testid="celln-agent-start"
          onClick={start}
          disabled={createRun.isPending || message.trim().length === 0}
        >
          {createRun.isPending ? "Starting…" : once ? "Ask once" : "Start conversation"}
        </Button>
        {error && (
          <p role="alert" className="text-sm text-destructive">
            {error}
          </p>
        )}
        {parents.length > 0 && (
          <Button size="sm" variant="ghost" onClick={() => setComposing(false)}>
            Back to conversations
          </Button>
        )}
      </CardContent>
    </Card>
    </div>
  );
}
