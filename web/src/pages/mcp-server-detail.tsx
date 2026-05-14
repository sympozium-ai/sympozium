import { useState } from "react";
import { useParams, Link } from "react-router-dom";
import { useMcpServer, usePatchMcpServer } from "@/hooks/use-api";
import { Card, CardHeader, CardTitle, CardContent } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";
import { Button } from "@/components/ui/button";
import { ArrowLeft } from "lucide-react";
import { formatAge } from "@/lib/utils";

function Row({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="flex justify-between text-sm">
      <span className="text-muted-foreground">{label}</span>
      <span className="font-mono text-right">{value ?? "—"}</span>
    </div>
  );
}

export function McpServerDetailPage() {
  const { name } = useParams<{ name: string }>();
  const { data: mcp, isLoading } = useMcpServer(name || "");
  const patchMutation = usePatchMcpServer();
  const [activeTab, setActiveTab] = useState("overview");

  if (isLoading) {
    return (
      <div className="space-y-4">
        <Skeleton className="h-8 w-64" />
        <Skeleton className="h-64 w-full" />
      </div>
    );
  }

  if (!mcp) {
    return <p className="text-muted-foreground">MCP server not found</p>;
  }

  return (
    <div className="space-y-6">
      <div className="flex items-center gap-3">
        <Link
          to="/mcp-servers"
          className="text-muted-foreground hover:text-foreground"
        >
          <ArrowLeft className="h-5 w-5" />
        </Link>
        <div>
          <h1 className="text-2xl font-bold font-mono">{mcp.metadata.name}</h1>
          <p className="flex items-center gap-2 text-sm text-muted-foreground">
            Created {formatAge(mcp.metadata.creationTimestamp)} ago
            {mcp.status?.ready ? (
              <Badge variant="default" className="text-xs">
                Ready
              </Badge>
            ) : (
              <Badge variant="secondary" className="text-xs">
                Not Ready
              </Badge>
            )}
          </p>
        </div>
      </div>

      {mcp.spec.suspended && (
        <div className="bg-yellow-900/20 border border-yellow-600/30 rounded-lg p-4 flex items-center justify-between">
          <div>
            <p className="text-sm font-medium text-yellow-400">
              This server is suspended
            </p>
            <p className="text-xs text-muted-foreground">
              Configure secrets and tokens, then enable the server.
            </p>
          </div>
          <Button
            size="sm"
            onClick={() =>
              patchMutation.mutate({
                name: mcp.metadata.name,
                suspended: false,
              })
            }
            disabled={patchMutation.isPending}
          >
            {patchMutation.isPending ? "Enabling..." : "Enable Server"}
          </Button>
        </div>
      )}

      <Tabs value={activeTab} onValueChange={setActiveTab}>
        <TabsList>
          <TabsTrigger value="overview">Overview</TabsTrigger>
          <TabsTrigger value="tools">Tools</TabsTrigger>
          <TabsTrigger value="configuration">Configuration</TabsTrigger>
        </TabsList>

        <TabsContent value="overview" className="space-y-4 pt-4">
          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
            {/* Status card */}
            <Card>
              <CardHeader>
                <CardTitle className="text-sm">Status</CardTitle>
              </CardHeader>
              <CardContent className="space-y-2">
                <Row
                  label="Suspended"
                  value={mcp.spec.suspended ? "Yes" : "No"}
                />
                <Row label="Ready" value={mcp.status?.ready ? "Yes" : "No"} />
                <Row
                  label="URL"
                  value={mcp.status?.url || mcp.spec.url || "—"}
                />
                <Row
                  label="Tools Discovered"
                  value={String(mcp.status?.toolCount ?? 0)}
                />
                <Row label="Transport" value={mcp.spec.transportType} />
              </CardContent>
            </Card>

            {/* Deployment card */}
            {mcp.spec.deployment && (
              <Card>
                <CardHeader>
                  <CardTitle className="text-sm">Deployment</CardTitle>
                </CardHeader>
                <CardContent className="space-y-2">
                  <Row label="Image" value={mcp.spec.deployment.image} />
                  <Row
                    label="Port"
                    value={String(mcp.spec.deployment.port ?? 8080)}
                  />
                  {mcp.spec.deployment.cmd && (
                    <Row label="Command" value={mcp.spec.deployment.cmd} />
                  )}
                  {mcp.spec.deployment.args &&
                    mcp.spec.deployment.args.length > 0 && (
                      <Row
                        label="Args"
                        value={mcp.spec.deployment.args.join(" ")}
                      />
                    )}
                  {mcp.spec.deployment.serviceAccountName && (
                    <Row
                      label="Service Account"
                      value={mcp.spec.deployment.serviceAccountName}
                    />
                  )}
                </CardContent>
              </Card>
            )}

            {/* Conditions card */}
            {mcp.status?.conditions && mcp.status.conditions.length > 0 && (
              <Card>
                <CardHeader>
                  <CardTitle className="text-sm">Conditions</CardTitle>
                </CardHeader>
                <CardContent className="space-y-2">
                  {mcp.status.conditions.map((c, i) => (
                    <div key={i} className="flex items-center gap-2 text-sm">
                      <Badge
                        variant={c.status === "True" ? "default" : "secondary"}
                        className="text-xs"
                      >
                        {c.type}
                      </Badge>
                      <span className="text-muted-foreground truncate">
                        {c.message}
                      </span>
                    </div>
                  ))}
                </CardContent>
              </Card>
            )}
          </div>
        </TabsContent>

        <TabsContent value="tools" className="pt-4">
          {mcp.status?.tools && mcp.status.tools.length > 0 ? (
            <Card>
              <CardHeader>
                <CardTitle className="text-sm">
                  Discovered Tools ({mcp.status.tools.length})
                  {mcp.spec.toolsPrefix && (
                    <span className="ml-2 font-normal text-muted-foreground">
                      prefix: <code>{mcp.spec.toolsPrefix}</code>
                    </span>
                  )}
                </CardTitle>
              </CardHeader>
              <CardContent>
                <div className="flex flex-wrap gap-2">
                  {mcp.status.tools.map((tool) => (
                    <Badge
                      key={tool}
                      variant="outline"
                      className="font-mono text-xs"
                    >
                      {tool}
                    </Badge>
                  ))}
                </div>
              </CardContent>
            </Card>
          ) : (
            <div className="py-12 text-center">
              <p className="text-muted-foreground">No tools discovered yet</p>
              <p className="text-xs text-muted-foreground/60 mt-1">
                Tools will appear here once the MCP server is ready and has been
                probed
              </p>
            </div>
          )}
        </TabsContent>

        <TabsContent value="configuration" className="space-y-4 pt-4">
          <Card>
            <CardHeader>
              <CardTitle className="text-sm">General</CardTitle>
            </CardHeader>
            <CardContent className="space-y-2">
              <Row label="Timeout" value={`${mcp.spec.timeout ?? 30}s`} />
              <Row label="Replicas" value={String(mcp.spec.replicas ?? 1)} />
              <Row label="Tools Prefix" value={mcp.spec.toolsPrefix} />
            </CardContent>
          </Card>

          {(mcp.spec.toolsAllow?.length || mcp.spec.toolsDeny?.length) && (
            <Card>
              <CardHeader>
                <CardTitle className="text-sm">Tool Filtering</CardTitle>
              </CardHeader>
              <CardContent className="space-y-3">
                {mcp.spec.toolsAllow && mcp.spec.toolsAllow.length > 0 && (
                  <div>
                    <p className="text-sm text-muted-foreground mb-1">
                      Allow list
                    </p>
                    <div className="flex flex-wrap gap-1">
                      {mcp.spec.toolsAllow.map((t) => (
                        <Badge
                          key={t}
                          variant="outline"
                          className="text-xs font-mono"
                        >
                          {t}
                        </Badge>
                      ))}
                    </div>
                  </div>
                )}
                {mcp.spec.toolsDeny && mcp.spec.toolsDeny.length > 0 && (
                  <div>
                    <p className="text-sm text-muted-foreground mb-1">
                      Deny list
                    </p>
                    <div className="flex flex-wrap gap-1">
                      {mcp.spec.toolsDeny.map((t) => (
                        <Badge
                          key={t}
                          variant="destructive"
                          className="text-xs font-mono"
                        >
                          {t}
                        </Badge>
                      ))}
                    </div>
                  </div>
                )}
              </CardContent>
            </Card>
          )}

          {mcp.spec.deployment?.secretRefs &&
            mcp.spec.deployment.secretRefs.length > 0 && (
              <Card>
                <CardHeader>
                  <CardTitle className="text-sm">Secret References</CardTitle>
                </CardHeader>
                <CardContent>
                  <div className="flex flex-wrap gap-2">
                    {mcp.spec.deployment.secretRefs.map((s) => (
                      <Badge
                        key={s.name}
                        variant="secondary"
                        className="text-xs font-mono"
                      >
                        {s.name}
                      </Badge>
                    ))}
                  </div>
                </CardContent>
              </Card>
            )}
        </TabsContent>
      </Tabs>
    </div>
  );
}
