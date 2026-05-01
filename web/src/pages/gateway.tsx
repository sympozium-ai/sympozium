import { useState, useEffect, useMemo } from "react";
import {
  useGatewayConfig,
  usePatchGatewayConfig,
  useCreateGatewayConfig,
  useAgents,
  useGatewayMetrics,
} from "@/hooks/use-api";
import {
  Card,
  CardHeader,
  CardTitle,
  CardContent,
  CardDescription,
} from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { toast } from "sonner";

type RangeKey = "1h" | "24h" | "7d";

function formatUptime(seconds: number): string {
  if (seconds <= 0) return "—";
  const d = Math.floor(seconds / 86400);
  const h = Math.floor((seconds % 86400) / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  return `${m}m`;
}

function linePath(points: Array<{ x: number; y: number }>) {
  if (!points.length) return "";
  return points.map((p, i) => `${i === 0 ? "M" : "L"}${p.x},${p.y}`).join(" ");
}

export function GatewayPage() {
  const { data, isLoading } = useGatewayConfig();
  const patchMutation = usePatchGatewayConfig();
  const createMutation = useCreateGatewayConfig();

  const [form, setForm] = useState({
    enabled: false,
    gatewayClassName: "sympozium",
    name: "sympozium-gateway",
    baseDomain: "",
    tlsEnabled: false,
    certManagerClusterIssuer: "",
    tlsSecretName: "sympozium-wildcard-cert",
  });
  const [dirty, setDirty] = useState(false);

  // Sync form state when data loads
  useEffect(() => {
    if (data) {
      setForm({
        enabled: data.enabled,
        gatewayClassName: data.gatewayClassName || "sympozium",
        name: data.name || "sympozium-gateway",
        baseDomain: data.baseDomain || "",
        tlsEnabled: data.tlsEnabled,
        certManagerClusterIssuer: data.certManagerClusterIssuer || "",
        tlsSecretName: data.tlsSecretName || "sympozium-wildcard-cert",
      });
      setDirty(false);
    }
  }, [data]);

  const update = (patch: Partial<typeof form>) => {
    setForm((prev) => ({ ...prev, ...patch }));
    setDirty(true);
  };

  const handleSave = async () => {
    const isNew = !data?.phase;
    if (data?.enabled && !form.enabled) {
      toast.error(
        "This WebUI is served through the Gateway. Disabling it from here would make the UI unreachable. Use GitOps or kubectl for an intentional edge teardown.",
      );
      setForm((prev) => ({ ...prev, enabled: true }));
      setDirty(false);
      return;
    }
    try {
      if (isNew) {
        await createMutation.mutateAsync(form);
      } else {
        await patchMutation.mutateAsync(form);
      }
      toast.success("Gateway configuration saved");
      setDirty(false);
    } catch {
      // Error toast handled by mutation hook
    }
  };

  const phase = data?.phase || "Not Configured";

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold">Gateway</h1>
        <p className="text-sm text-muted-foreground">
          Manage the shared Envoy Gateway for instance web endpoints
        </p>
      </div>

      {/* Status */}
      <Card>
        <CardHeader>
          <CardTitle className="text-sm">Status</CardTitle>
        </CardHeader>
        <CardContent className="space-y-3 text-sm">
          {isLoading ? (
            <div className="space-y-2">
              <Skeleton className="h-5 w-full" />
              <Skeleton className="h-5 w-3/4" />
            </div>
          ) : (
            <>
              <div className="flex items-center justify-between">
                <span className="text-muted-foreground">Phase</span>
                <PhaseBadge phase={phase} />
              </div>
              {data?.message && (
                <p className="text-xs text-destructive">{data.message}</p>
              )}
              {data?.address && (
                <div className="flex items-center justify-between">
                  <span className="text-muted-foreground">Address</span>
                  <span className="font-mono">{data.address}</span>
                </div>
              )}
              {data?.listenerCount != null && data.listenerCount > 0 && (
                <div className="flex items-center justify-between">
                  <span className="text-muted-foreground">Listeners</span>
                  <span>{data.listenerCount}</span>
                </div>
              )}
            </>
          )}
        </CardContent>
      </Card>

      {/* Observability */}
      <GatewayMetricsCard />

      {/* Configuration */}
      <Card>
        <CardHeader>
          <CardTitle className="text-sm">Configuration</CardTitle>
          <CardDescription>
            Enable the gateway to expose instance web endpoints via Envoy
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          {isLoading ? (
            <div className="space-y-3">
              <Skeleton className="h-9 w-full" />
              <Skeleton className="h-9 w-full" />
              <Skeleton className="h-9 w-full" />
            </div>
          ) : (
            <>
              <div className="space-y-2">
                <div className="flex items-center justify-between">
                  <Label>Enabled</Label>
                  <Button
                    variant={form.enabled ? "default" : "secondary"}
                    size="sm"
                    onClick={() => {
                      if (form.enabled) {
                        toast.warning(
                          "Gateway teardown is intentionally blocked in the WebUI because it can disconnect this page. Use GitOps/kubectl for an explicit edge teardown.",
                        );
                        return;
                      }
                      update({ enabled: true });
                    }}
                  >
                    {form.enabled ? "On (GitOps-managed)" : "Turn on"}
                  </Button>
                </div>
                <p className="text-xs text-muted-foreground">
                  The public WebUI and agent web endpoints are served through this Gateway. Turning it off from the WebUI can remove the route you are currently using, so disable/teardown is an explicit GitOps operation.
                </p>
              </div>

              <div className="space-y-2">
                <Label htmlFor="gw-baseDomain">Base Domain</Label>
                <Input
                  id="gw-baseDomain"
                  placeholder="sympozium.example.com"
                  value={form.baseDomain}
                  onChange={(e) => update({ baseDomain: e.target.value })}
                />
                <p className="text-xs text-muted-foreground">
                  Instances will be available at &lt;name&gt;.
                  {form.baseDomain || "<baseDomain>"}
                </p>
              </div>

              <div className="grid grid-cols-2 gap-4">
                <div className="space-y-2">
                  <Label htmlFor="gw-className">GatewayClass Name</Label>
                  <Input
                    id="gw-className"
                    value={form.gatewayClassName}
                    onChange={(e) =>
                      update({ gatewayClassName: e.target.value })
                    }
                  />
                </div>
                <div className="space-y-2">
                  <Label htmlFor="gw-name">Gateway Name</Label>
                  <Input
                    id="gw-name"
                    value={form.name}
                    onChange={(e) => update({ name: e.target.value })}
                  />
                </div>
              </div>
            </>
          )}
        </CardContent>
      </Card>

      {/* TLS */}
      <Card>
        <CardHeader>
          <CardTitle className="text-sm">TLS</CardTitle>
          <CardDescription>
            Configure HTTPS with cert-manager for automatic certificate
            provisioning
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          {isLoading ? (
            <div className="space-y-3">
              <Skeleton className="h-9 w-full" />
              <Skeleton className="h-9 w-full" />
            </div>
          ) : (
            <>
              <div className="flex items-center justify-between">
                <Label>Enable TLS</Label>
                <Button
                  variant={form.tlsEnabled ? "default" : "secondary"}
                  size="sm"
                  onClick={() => update({ tlsEnabled: !form.tlsEnabled })}
                >
                  {form.tlsEnabled ? "On" : "Off"}
                </Button>
              </div>

              {form.tlsEnabled && (
                <>
                  <div className="space-y-2">
                    <Label htmlFor="gw-issuer">
                      cert-manager ClusterIssuer
                    </Label>
                    <Input
                      id="gw-issuer"
                      placeholder="letsencrypt-prod"
                      value={form.certManagerClusterIssuer}
                      onChange={(e) =>
                        update({ certManagerClusterIssuer: e.target.value })
                      }
                    />
                  </div>
                  <div className="space-y-2">
                    <Label htmlFor="gw-secretName">TLS Secret Name</Label>
                    <Input
                      id="gw-secretName"
                      value={form.tlsSecretName}
                      onChange={(e) =>
                        update({ tlsSecretName: e.target.value })
                      }
                    />
                  </div>
                </>
              )}
            </>
          )}
        </CardContent>
      </Card>

      {/* Routes */}
      <RoutesCard baseDomain={form.baseDomain} />

      {/* Save */}
      {!isLoading && (
        <div className="flex justify-end">
          <Button
            onClick={handleSave}
            disabled={
              !dirty || patchMutation.isPending || createMutation.isPending
            }
          >
            {patchMutation.isPending || createMutation.isPending
              ? "Saving..."
              : "Save"}
          </Button>
        </div>
      )}
    </div>
  );
}

function GatewayMetricsCard() {
  const [range, setRange] = useState<RangeKey>("24h");
  const [hoverIdx, setHoverIdx] = useState<number | null>(null);
  const { data: metrics, isLoading } = useGatewayMetrics(range);

  const buckets = metrics?.buckets ?? [];
  const totalRequests = metrics?.totalRequests ?? 0;
  const successCount = metrics?.successCount ?? 0;
  const errorCount = metrics?.errorCount ?? 0;
  const avgDurationSec = metrics?.avgDurationSec ?? 0;
  const uptimeSec = metrics?.uptimeSec ?? 0;
  const servingInstances = metrics?.servingInstances ?? 0;
  const successRate =
    totalRequests > 0 ? (successCount / totalRequests) * 100 : 0;

  const chartW = 760;
  const chartH = 220;
  const padX = 32;
  const padY = 20;
  const innerW = chartW - padX * 2;
  const innerH = chartH - padY * 2;
  const maxRequests = Math.max(1, ...buckets.map((b) => b.requests));
  const maxDuration = Math.max(0.1, ...buckets.map((b) => b.avgDurationSec));
  const barW = Math.max(
    3,
    Math.min(14, (buckets.length > 0 ? innerW / buckets.length : 8) * 0.7),
  );
  const xFor = (idx: number) =>
    padX +
    (buckets.length <= 1 ? innerW / 2 : (idx / (buckets.length - 1)) * innerW);
  const yForRequests = (value: number) =>
    padY + innerH - (value / maxRequests) * innerH;
  const yForDuration = (value: number) =>
    padY + innerH - (value / maxDuration) * innerH;
  const durationPoints = buckets.map((b, i) => ({
    x: xFor(i),
    y: yForDuration(b.avgDurationSec),
  }));
  const activePoint = hoverIdx !== null ? buckets[hoverIdx] : null;
  const activeX = hoverIdx !== null ? xFor(hoverIdx) : null;

  const stats = [
    {
      label: "Total Requests",
      value: totalRequests.toLocaleString(),
      color: "text-blue-400",
    },
    {
      label: "Success Rate",
      value: totalRequests > 0 ? `${successRate.toFixed(1)}%` : "—",
      color: "text-emerald-400",
    },
    {
      label: "Errors",
      value: errorCount.toLocaleString(),
      color: errorCount > 0 ? "text-red-400" : "text-muted-foreground",
    },
    { label: "Uptime", value: formatUptime(uptimeSec), color: "text-cyan-400" },
  ];

  return (
    <Card>
      <CardHeader className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <CardTitle className="text-sm">Web Endpoint Activity</CardTitle>
          <CardDescription>
            Request volume, errors, and latency for web-proxy endpoints
          </CardDescription>
        </div>
        <div className="flex items-center gap-2">
          {(["1h", "24h", "7d"] as RangeKey[]).map((r) => (
            <Button
              key={r}
              size="sm"
              variant={range === r ? "default" : "outline"}
              className="h-7 text-xs"
              onClick={() => setRange(r)}
            >
              {r === "1h" ? "1h" : r === "24h" ? "24hr" : "7 days"}
            </Button>
          ))}
        </div>
      </CardHeader>
      <CardContent className="space-y-4">
        {/* Stats row */}
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
          {stats.map((s) => (
            <div
              key={s.label}
              className="rounded-lg border border-border/50 bg-background/40 px-3 py-2"
            >
              <div className="text-[11px] uppercase tracking-wide text-muted-foreground">
                {s.label}
              </div>
              {isLoading ? (
                <Skeleton className="mt-1 h-6 w-12" />
              ) : (
                <div className={`text-xl font-bold leading-tight ${s.color}`}>
                  {s.value}
                </div>
              )}
            </div>
          ))}
        </div>

        {/* Serving instances badge */}
        {!isLoading && servingInstances > 0 && (
          <div className="flex items-center gap-2 text-xs text-muted-foreground">
            <Badge variant="secondary" className="text-xs">
              {servingInstances} serving
            </Badge>
            <span>instance{servingInstances !== 1 ? "s" : ""} active</span>
          </div>
        )}

        {/* Chart */}
        {isLoading ? (
          <Skeleton className="h-[220px] w-full" />
        ) : buckets.length === 0 ? (
          <div className="flex h-[220px] items-center justify-center rounded-lg border border-border/50 bg-background/40">
            <p className="text-sm text-muted-foreground">
              No web endpoint data yet
            </p>
          </div>
        ) : (
          <div className="relative h-[220px] w-full rounded-lg border border-border/50 bg-background/40 p-2">
            <svg
              viewBox={`0 0 ${chartW} ${chartH}`}
              className="h-full w-full"
              onMouseLeave={() => setHoverIdx(null)}
            >
              {/* Grid lines */}
              {[0, 0.25, 0.5, 0.75, 1].map((t) => {
                const y = padY + innerH - innerH * t;
                return (
                  <line
                    key={t}
                    x1={padX}
                    x2={chartW - padX}
                    y1={y}
                    y2={y}
                    stroke="currentColor"
                    className="text-border/60"
                    strokeWidth={1}
                  />
                );
              })}

              {/* Hover crosshair */}
              {activeX !== null && (
                <line
                  x1={activeX}
                  x2={activeX}
                  y1={padY}
                  y2={chartH - padY}
                  stroke="currentColor"
                  className="text-blue-300/60"
                  strokeDasharray="4 3"
                  strokeWidth={1}
                />
              )}

              {/* Request bars */}
              {buckets.map((b, i) => {
                const successH =
                  ((b.requests - b.errors) / maxRequests) * innerH;
                const errorH = (b.errors / maxRequests) * innerH;
                return (
                  <g key={b.ts}>
                    {/* Success portion */}
                    <rect
                      x={xFor(i) - barW / 2}
                      y={padY + innerH - successH - errorH}
                      width={barW}
                      height={successH}
                      className="fill-emerald-400/70"
                      rx={1}
                    />
                    {/* Error portion stacked on top */}
                    {b.errors > 0 && (
                      <rect
                        x={xFor(i) - barW / 2}
                        y={padY + innerH - errorH}
                        width={barW}
                        height={errorH}
                        className="fill-red-400/80"
                        rx={1}
                      />
                    )}
                    {/* Hover zone */}
                    <rect
                      x={xFor(i) - barW}
                      y={padY}
                      width={barW * 2}
                      height={innerH}
                      fill="transparent"
                      className="cursor-pointer"
                      onMouseEnter={() => setHoverIdx(i)}
                    />
                  </g>
                );
              })}

              {/* Duration line */}
              <path
                d={linePath(durationPoints)}
                fill="none"
                stroke="currentColor"
                className="text-blue-400"
                strokeWidth={2}
              />

              {/* Duration dots */}
              {buckets.map((b, i) => (
                <circle
                  key={`dot-${b.ts}`}
                  cx={xFor(i)}
                  cy={yForDuration(b.avgDurationSec)}
                  r={hoverIdx === i ? 4.5 : 2.5}
                  className="fill-blue-400"
                />
              ))}

              {/* Tooltip */}
              {activePoint && activeX !== null && hoverIdx !== null && (
                <g>
                  <rect
                    x={activeX + (hoverIdx > buckets.length / 2 ? -140 : 10)}
                    y={padY}
                    width={130}
                    height={62}
                    rx={6}
                    className="fill-background/95 stroke-border"
                    strokeWidth={1}
                  />
                  <text
                    x={activeX + (hoverIdx > buckets.length / 2 ? -75 : 75)}
                    y={padY + 16}
                    textAnchor="middle"
                    className="fill-foreground text-[10px]"
                  >
                    {new Date(activePoint.ts).toLocaleString(undefined, {
                      month: "short",
                      day: "numeric",
                      hour: "2-digit",
                      minute: "2-digit",
                    })}
                  </text>
                  <text
                    x={activeX + (hoverIdx > buckets.length / 2 ? -130 : 20)}
                    y={padY + 32}
                    className="fill-emerald-400 text-[10px]"
                  >
                    {activePoint.requests - activePoint.errors} ok
                  </text>
                  <text
                    x={activeX + (hoverIdx > buckets.length / 2 ? -70 : 80)}
                    y={padY + 32}
                    className="fill-red-400 text-[10px]"
                  >
                    {activePoint.errors} err
                  </text>
                  <text
                    x={activeX + (hoverIdx > buckets.length / 2 ? -130 : 20)}
                    y={padY + 48}
                    className="fill-blue-400 text-[10px]"
                  >
                    avg {activePoint.avgDurationSec.toFixed(1)}s
                  </text>
                </g>
              )}

              {/* Y-axis labels */}
              <text
                x={padX - 4}
                y={padY + 4}
                textAnchor="end"
                className="fill-muted-foreground text-[9px]"
              >
                {maxRequests}
              </text>
              <text
                x={padX - 4}
                y={padY + innerH + 4}
                textAnchor="end"
                className="fill-muted-foreground text-[9px]"
              >
                0
              </text>

              {/* Legend */}
              <rect
                x={chartW - padX - 120}
                y={padY - 2}
                width={8}
                height={8}
                rx={1}
                className="fill-emerald-400/70"
              />
              <text
                x={chartW - padX - 108}
                y={padY + 6}
                className="fill-muted-foreground text-[9px]"
              >
                requests
              </text>
              <rect
                x={chartW - padX - 60}
                y={padY - 2}
                width={8}
                height={8}
                rx={1}
                className="fill-blue-400"
              />
              <text
                x={chartW - padX - 48}
                y={padY + 6}
                className="fill-muted-foreground text-[9px]"
              >
                latency
              </text>
            </svg>
          </div>
        )}

        {/* Summary line */}
        {!isLoading && totalRequests > 0 && (
          <div className="flex flex-wrap items-center gap-4 text-xs">
            <span className="text-muted-foreground">
              Total:{" "}
              <span className="text-foreground font-semibold">
                {totalRequests}
              </span>
            </span>
            <span className="text-emerald-400">
              Success: <span className="font-semibold">{successCount}</span>
            </span>
            <span className="text-red-400">
              Errors: <span className="font-semibold">{errorCount}</span>
            </span>
            <span className="text-blue-400">
              Avg latency:{" "}
              <span className="font-semibold">
                {avgDurationSec.toFixed(1)}s
              </span>
            </span>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function RoutesCard({ baseDomain }: { baseDomain: string }) {
  const { data: instances, isLoading } = useAgents();

  const routes = useMemo(() => {
    if (!instances) return [];
    return instances.filter((i) =>
      i.spec.skills?.some(
        (s) =>
          s.skillPackRef === "web-endpoint" ||
          s.skillPackRef === "skillpack-web-endpoint",
      ),
    );
  }, [instances]);

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-sm">Routes</CardTitle>
        <CardDescription>
          HTTPRoutes created for instances with web endpoints enabled
        </CardDescription>
      </CardHeader>
      <CardContent>
        {isLoading ? (
          <div className="space-y-2">
            <Skeleton className="h-5 w-full" />
            <Skeleton className="h-5 w-3/4" />
          </div>
        ) : routes.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            No routes — enable web endpoints on instances to create routes
          </p>
        ) : (
          <div className="space-y-1 text-sm">
            <div className="grid grid-cols-4 gap-2 font-medium text-muted-foreground text-xs pb-1 border-b">
              <span>Instance</span>
              <span>Hostname</span>
              <span>Status</span>
              <span>URL</span>
            </div>
            {routes.map((inst) => {
              const webSkill = inst.spec.skills?.find(
                (s) =>
                  s.skillPackRef === "web-endpoint" ||
                  s.skillPackRef === "skillpack-web-endpoint",
              );
              const hostname =
                webSkill?.params?.hostname ||
                (baseDomain ? `${inst.metadata.name}.${baseDomain}` : "-");
              return (
                <div
                  key={inst.metadata.name}
                  className="grid grid-cols-4 gap-2 py-1"
                >
                  <span className="font-medium truncate">
                    {inst.metadata.name}
                  </span>
                  <span className="font-mono text-xs truncate">{hostname}</span>
                  <Badge variant="secondary" className="w-fit">
                    Skill
                  </Badge>
                  <span className="font-mono text-xs truncate">-</span>
                </div>
              );
            })}
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function PhaseBadge({ phase }: { phase: string }) {
  const variant =
    phase === "Ready"
      ? "default"
      : phase === "Error"
        ? "destructive"
        : "secondary";
  return <Badge variant={variant}>{phase}</Badge>;
}
