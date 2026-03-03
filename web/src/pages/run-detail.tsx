import { useParams, Link } from "react-router-dom";
import ReactMarkdown from "react-markdown";
import { useRun } from "@/hooks/use-api";
import { StatusBadge } from "@/components/status-badge";
import {
  Card,
  CardHeader,
  CardTitle,
  CardContent,
} from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Separator } from "@/components/ui/separator";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";
import { ArrowLeft, Clock, Cpu, Zap } from "lucide-react";
import { formatAge } from "@/lib/utils";

export function RunDetailPage() {
  const { name } = useParams<{ name: string }>();
  const { data: run, isLoading } = useRun(name || "");

  if (isLoading) {
    return (
      <div className="space-y-4">
        <Skeleton className="h-8 w-64" />
        <Skeleton className="h-64 w-full" />
      </div>
    );
  }

  if (!run) {
    return <p className="text-muted-foreground">Run not found</p>;
  }

  const usage = run.status?.tokenUsage;
  const duration = usage?.durationMs
    ? `${(usage.durationMs / 1000).toFixed(1)}s`
    : "—";

  return (
    <div className="space-y-6">
      <div className="flex items-center gap-3">
        <Link to="/runs" className="text-muted-foreground hover:text-foreground">
          <ArrowLeft className="h-5 w-5" />
        </Link>
        <div>
          <h1 className="text-xl font-bold font-mono">{run.metadata.name}</h1>
          <div className="flex items-center gap-2 text-sm text-muted-foreground">
            <Link to={`/instances/${run.spec.instanceRef}`} className="hover:text-primary">
              {run.spec.instanceRef}
            </Link>
            <span>·</span>
            <StatusBadge phase={run.status?.phase} />
            <span>·</span>
            {formatAge(run.metadata.creationTimestamp)} ago
          </div>
        </div>
      </div>

      {/* Stats row */}
      {usage && (
        <div className="grid gap-4 sm:grid-cols-4">
          <Card>
            <CardContent className="flex items-center gap-3 p-4">
              <Zap className="h-5 w-5 text-amber-400" />
              <div>
                <p className="text-sm text-muted-foreground">Total Tokens</p>
                <p className="text-lg font-bold">
                  {usage.totalTokens.toLocaleString()}
                </p>
              </div>
            </CardContent>
          </Card>
          <Card>
            <CardContent className="flex items-center gap-3 p-4">
              <Cpu className="h-5 w-5 text-blue-400" />
              <div>
                <p className="text-sm text-muted-foreground">Tool Calls</p>
                <p className="text-lg font-bold">{usage.toolCalls}</p>
              </div>
            </CardContent>
          </Card>
          <Card>
            <CardContent className="flex items-center gap-3 p-4">
              <Clock className="h-5 w-5 text-purple-400" />
              <div>
                <p className="text-sm text-muted-foreground">Duration</p>
                <p className="text-lg font-bold">{duration}</p>
              </div>
            </CardContent>
          </Card>
          <Card>
            <CardContent className="flex items-center gap-3 p-4">
              <div>
                <p className="text-sm text-muted-foreground">In / Out</p>
                <p className="text-sm font-mono">
                  {usage.inputTokens.toLocaleString()} /{" "}
                  {usage.outputTokens.toLocaleString()}
                </p>
              </div>
            </CardContent>
          </Card>
        </div>
      )}

      <Tabs defaultValue="task">
        <TabsList>
          <TabsTrigger value="task">Task</TabsTrigger>
          <TabsTrigger value="result">Result</TabsTrigger>
          <TabsTrigger value="spec">Spec</TabsTrigger>
        </TabsList>

        <TabsContent value="task">
          <Card>
            <CardContent className="pt-6">
              <pre className="whitespace-pre-wrap text-sm">{run.spec.task}</pre>
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="result">
          <Card>
            <CardContent className="pt-6">
              {run.status?.result ? (
                <div className="prose prose-sm prose-invert max-w-none">
                  <ReactMarkdown>{run.status.result}</ReactMarkdown>
                </div>
              ) : run.status?.error ? (
                <div className="space-y-2">
                  <Badge variant="destructive">Error</Badge>
                  <pre className="whitespace-pre-wrap text-sm text-destructive">
                    {run.status.error}
                  </pre>
                </div>
              ) : (
                <p className="text-sm text-muted-foreground">
                  {run.status?.phase === "Running"
                    ? "Run is still in progress…"
                    : "No result available"}
                </p>
              )}
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="spec">
          <Card>
            <CardContent className="pt-6">
              <pre className="text-xs font-mono whitespace-pre-wrap rounded bg-muted/50 p-4 overflow-auto max-h-96">
                {JSON.stringify(run.spec, null, 2)}
              </pre>
            </CardContent>
          </Card>
        </TabsContent>
      </Tabs>

      {/* Pod info */}
      {run.status?.podName && (
        <>
          <Separator />
          <div className="text-sm text-muted-foreground">
            Pod: <span className="font-mono">{run.status.podName}</span>
            {run.status.exitCode !== undefined && (
              <>
                {" "}
                · Exit code:{" "}
                <span className="font-mono">{run.status.exitCode}</span>
              </>
            )}
          </div>
        </>
      )}
    </div>
  );
}
