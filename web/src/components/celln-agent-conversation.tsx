import { useState } from "react";
import type { Agent, AgentRun } from "@/lib/api";
import { useCreateRun } from "@/hooks/use-api";
import { CellnConversation } from "@/components/celln-conversation";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";

const DEFAULT_ENDURING = {
  leaseSeconds: 600,
  maxTurns: 8,
  maxModelRequests: 24,
  maxOutputTokens: 8192,
};

/**
 * Interactive chat for a native Celln Agent. The conversation is an enduring
 * AgentRun (the host-native parent); the first message is the initial turn and
 * follow-up turns are durable AgentRunTurn records. This mirrors Run detail,
 * but starts or resumes the parent directly from the Agent's Harness tab.
 */
export function CellnAgentConversation({
  agent,
  parent,
}: {
  agent: Agent;
  parent?: AgentRun;
}) {
  const createRun = useCreateRun();
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");

  const execution = agent.spec.execution;
  const runtimeRef =
    agent.spec.runtimeRef || execution?.cellnSelection?.runtimeRef || "";
  const toolRefs = execution?.cellnSelection?.toolRefs || [];
  const model = execution?.model || agent.spec.agents?.default?.model || "";
  const modelConnectionRef = execution?.modelConnectionRef;
  const provider = modelConnectionRef ? undefined : execution?.provider;
  const limits = execution?.enduring || DEFAULT_ENDURING;

  // The newest live parent is the conversation; a terminal one is surfaced by
  // CellnConversation so the operator can reconcile or delete it.
  if (parent) {
    return <CellnConversation run={parent} />;
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
        model: model || undefined,
        modelConnectionRef,
        provider,
        cellnSelection: { runtimeRef: runtimeRef || undefined, toolRefs },
        timeout: `${limits.leaseSeconds}s`,
        executionLifecycle: "enduring",
        enduring: limits,
      },
      {
        onSuccess: () => setMessage(""),
        onError: (err) =>
          setError(
            err instanceof Error
              ? err.message
              : "Could not start the conversation",
          ),
      },
    );
  }

  return (
    <Card data-testid="celln-agent-conversation">
      <CardHeader>
        <CardTitle className="text-base">
          Persistent Celln conversation
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        <p className="text-sm text-muted-foreground">
          Start an enduring native parent. The first message runs as the initial
          turn; each follow-up turn runs in a disposable child cell. This creates
          a request — the host operator's parent registration must admit it.
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
        <Button
          data-testid="celln-agent-start"
          onClick={start}
          disabled={createRun.isPending || message.trim().length === 0}
        >
          {createRun.isPending ? "Starting…" : "Start conversation"}
        </Button>
        {error && (
          <p role="alert" className="text-sm text-destructive">
            {error}
          </p>
        )}
      </CardContent>
    </Card>
  );
}
