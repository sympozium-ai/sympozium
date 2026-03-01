import { useParams, Link } from "react-router-dom";
import { useInstance } from "@/hooks/use-api";
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
import { ArrowLeft } from "lucide-react";
import { formatAge } from "@/lib/utils";

export function InstanceDetailPage() {
  const { name } = useParams<{ name: string }>();
  const { data: inst, isLoading } = useInstance(name || "");

  if (isLoading) {
    return (
      <div className="space-y-4">
        <Skeleton className="h-8 w-64" />
        <Skeleton className="h-64 w-full" />
      </div>
    );
  }

  if (!inst) {
    return <p className="text-muted-foreground">Instance not found</p>;
  }

  return (
    <div className="space-y-6">
      <div className="flex items-center gap-3">
        <Link to="/instances" className="text-muted-foreground hover:text-foreground">
          <ArrowLeft className="h-5 w-5" />
        </Link>
        <div>
          <h1 className="text-2xl font-bold font-mono">{inst.metadata.name}</h1>
          <p className="flex items-center gap-2 text-sm text-muted-foreground">
            Created {formatAge(inst.metadata.creationTimestamp)} ago
            <StatusBadge phase={inst.status?.phase} />
          </p>
        </div>
      </div>

      <Tabs defaultValue="overview">
        <TabsList>
          <TabsTrigger value="overview">Overview</TabsTrigger>
          <TabsTrigger value="channels">Channels</TabsTrigger>
          <TabsTrigger value="skills">Skills</TabsTrigger>
          <TabsTrigger value="memory">Memory</TabsTrigger>
        </TabsList>

        <TabsContent value="overview">
          <div className="grid gap-4 md:grid-cols-2">
            <Card>
              <CardHeader>
                <CardTitle className="text-base">Agent Configuration</CardTitle>
              </CardHeader>
              <CardContent className="space-y-3">
                <Row label="Model" value={inst.spec.agents?.default?.model} />
                <Row label="Base URL" value={inst.spec.agents?.default?.baseURL} />
                <Row label="Thinking" value={inst.spec.agents?.default?.thinking} />
                <Row label="Policy" value={inst.spec.policyRef} />
              </CardContent>
            </Card>

            <Card>
              <CardHeader>
                <CardTitle className="text-base">Status</CardTitle>
              </CardHeader>
              <CardContent className="space-y-3">
                <Row label="Phase" value={inst.status?.phase} />
                <Row label="Active Pods" value={String(inst.status?.activeAgentPods ?? 0)} />
                <Row label="Total Runs" value={String(inst.status?.totalAgentRuns ?? 0)} />
                <Row
                  label="Tokens (in/out/total)"
                  value={
                    inst.status?.tokenUsage
                      ? `${inst.status.tokenUsage.inputTokens.toLocaleString()} / ${inst.status.tokenUsage.outputTokens.toLocaleString()} / ${inst.status.tokenUsage.totalTokens.toLocaleString()}`
                      : "—"
                  }
                />
              </CardContent>
            </Card>

            {inst.spec.authRefs && inst.spec.authRefs.length > 0 && (
              <Card>
                <CardHeader>
                  <CardTitle className="text-base">Auth References</CardTitle>
                </CardHeader>
                <CardContent>
                  <div className="space-y-2">
                    {inst.spec.authRefs.map((ref, i) => (
                      <div
                        key={i}
                        className="flex items-center gap-2 text-sm"
                      >
                        <Badge variant="secondary">{ref.provider}</Badge>
                        <span className="font-mono text-muted-foreground">
                          {ref.secret}
                        </span>
                      </div>
                    ))}
                  </div>
                </CardContent>
              </Card>
            )}
          </div>
        </TabsContent>

        <TabsContent value="channels">
          <Card>
            <CardContent className="pt-6">
              {inst.spec.channels && inst.spec.channels.length > 0 ? (
                <div className="space-y-3">
                  {inst.spec.channels.map((ch, i) => {
                    const chStatus = inst.status?.channels?.find(
                      (s) => s.type === ch.type
                    );
                    return (
                      <div key={i} className="flex items-center justify-between rounded-lg border p-3">
                        <div className="flex items-center gap-3">
                          <Badge variant="outline" className="capitalize">{ch.type}</Badge>
                          {ch.configRef && (
                            <span className="text-xs text-muted-foreground font-mono">
                              secret: {ch.configRef.secret}
                            </span>
                          )}
                        </div>
                        <div className="flex items-center gap-2">
                          <StatusBadge phase={chStatus?.status} />
                          {chStatus?.message && (
                            <span className="text-xs text-muted-foreground">
                              {chStatus.message}
                            </span>
                          )}
                        </div>
                      </div>
                    );
                  })}
                </div>
              ) : (
                <p className="text-sm text-muted-foreground">
                  No channels configured
                </p>
              )}
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="skills">
          <Card>
            <CardContent className="pt-6">
              {inst.spec.skills && inst.spec.skills.length > 0 ? (
                <div className="flex flex-wrap gap-2">
                  {inst.spec.skills.map((sk, i) => (
                    <Badge key={i} variant="secondary">
                      {sk.skillPackRef}
                    </Badge>
                  ))}
                </div>
              ) : (
                <p className="text-sm text-muted-foreground">
                  No skills attached
                </p>
              )}
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="memory">
          <Card>
            <CardContent className="pt-6">
              {inst.spec.memory ? (
                <div className="space-y-3">
                  <Row label="Enabled" value={inst.spec.memory.enabled ? "Yes" : "No"} />
                  <Row label="Max Size" value={inst.spec.memory.maxSizeKB ? `${inst.spec.memory.maxSizeKB} KB` : "Default"} />
                  <Separator />
                  {inst.spec.memory.systemPrompt && (
                    <div>
                      <p className="text-sm font-medium mb-2">System Prompt</p>
                      <pre className="rounded bg-muted/50 p-3 text-xs whitespace-pre-wrap">
                        {inst.spec.memory.systemPrompt}
                      </pre>
                    </div>
                  )}
                </div>
              ) : (
                <p className="text-sm text-muted-foreground">
                  Memory not configured
                </p>
              )}
            </CardContent>
          </Card>
        </TabsContent>
      </Tabs>
    </div>
  );
}

function Row({ label, value }: { label: string; value?: string | null }) {
  return (
    <div className="flex items-center justify-between text-sm">
      <span className="text-muted-foreground">{label}</span>
      <span className="font-mono">{value || "—"}</span>
    </div>
  );
}
