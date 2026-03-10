import { useMemo, useState } from "react";
import {
  Card,
  CardHeader,
  CardTitle,
  CardContent,
} from "@/components/ui/card";
import { StatusBadge } from "@/components/status-badge";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableHeader,
  TableRow,
  TableHead,
  TableBody,
  TableCell,
} from "@/components/ui/table";
import { useInstances, useRuns, usePolicies, useSkills, useSchedules, usePersonaPacks } from "@/hooks/use-api";
import { useWebSocket } from "@/hooks/use-websocket";
import { formatAge, truncate } from "@/lib/utils";
import { Link } from "react-router-dom";
import {
  Server,
  Play,
  Shield,
  Wrench,
  Clock,
  Users,
  Activity,
} from "lucide-react";
import { Button } from "@/components/ui/button";

type ActivityBucket = {
  ts: number;
  label: string;
  runs: number;
  failed: number;
  durationTotalSec: number;
  durationSamples: number;
  durationValuesSec: number[];
  avgDurationSec: number;
  p95DurationSec: number;
  agentsInstalled: number;
  serving: number;
};

type DurationMode = "avg" | "p95";

function percentile(values: number[], p: number): number {
  if (values.length === 0) return 0;
  const sorted = [...values].sort((a, b) => a - b);
  const idx = Math.max(0, Math.min(sorted.length - 1, Math.ceil((p / 100) * sorted.length) - 1));
  return sorted[idx];
}

type RangeKey = "1h" | "24h" | "7d";

function buildActivityBuckets(
  runs: NonNullable<ReturnType<typeof useRuns>["data"]>,
  instances: NonNullable<ReturnType<typeof useInstances>["data"]>,
  range: RangeKey
): ActivityBucket[] {
  const now = new Date();
  const buckets: ActivityBucket[] = [];

  if (range === "1h") {
    const start = new Date(now.getTime() - 59 * 60 * 1000);
    start.setSeconds(0, 0);
    for (let i = 0; i < 60; i++) {
      const d = new Date(start.getTime() + i * 60 * 1000);
      buckets.push({
        ts: d.getTime(),
        label: d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }),
        runs: 0,
        failed: 0,
        durationTotalSec: 0,
        durationSamples: 0,
        durationValuesSec: [],
        avgDurationSec: 0,
        p95DurationSec: 0,
        agentsInstalled: 0,
        serving: 0,
      });
    }
    for (const run of runs || []) {
      const created = new Date(run.metadata.creationTimestamp || "").getTime();
      if (!Number.isFinite(created) || created < buckets[0].ts) continue;
      const idx = Math.floor((created - buckets[0].ts) / (60 * 1000));
      if (idx < 0 || idx >= buckets.length) continue;
      buckets[idx].runs++;
      const phase = (run.status?.phase || "").toLowerCase();
      if (phase === "failed" || phase === "error") buckets[idx].failed++;
      const durationSec = (run.status?.tokenUsage?.durationMs || 0) / 1000;
      if (durationSec > 0) {
        buckets[idx].durationTotalSec += durationSec;
        buckets[idx].durationSamples++;
        buckets[idx].durationValuesSec.push(durationSec);
      }
    }
    for (const b of buckets) {
      b.avgDurationSec = b.durationSamples > 0 ? b.durationTotalSec / b.durationSamples : 0;
      b.p95DurationSec = percentile(b.durationValuesSec, 95);
    }
    const createdAt = (instances || [])
      .map((i) => new Date(i.metadata.creationTimestamp || "").getTime())
      .filter((n) => Number.isFinite(n))
      .sort((a, b) => a - b);
    let ptr = 0;
    for (let i = 0; i < buckets.length; i++) {
      const bucketEnd = buckets[i].ts + 60 * 1000 - 1;
      while (ptr < createdAt.length && createdAt[ptr] <= bucketEnd) ptr++;
      buckets[i].agentsInstalled = ptr;
    }
    countServingPerBucket(runs, buckets, 60 * 1000);
    return buckets;
  }

  if (range === "24h") {
    const start = new Date(now.getTime() - 23 * 60 * 60 * 1000);
    start.setMinutes(0, 0, 0);
    for (let i = 0; i < 24; i++) {
      const d = new Date(start.getTime() + i * 60 * 60 * 1000);
      buckets.push({
        ts: d.getTime(),
        label: d.toLocaleTimeString([], { hour: "numeric" }),
        runs: 0,
        failed: 0,
        durationTotalSec: 0,
        durationSamples: 0,
        durationValuesSec: [],
        avgDurationSec: 0,
        p95DurationSec: 0,
        agentsInstalled: 0,
        serving: 0,
      });
    }
    for (const run of runs || []) {
      const created = new Date(run.metadata.creationTimestamp || "").getTime();
      if (!Number.isFinite(created) || created < buckets[0].ts) continue;
      const idx = Math.floor((created - buckets[0].ts) / (60 * 60 * 1000));
      if (idx < 0 || idx >= buckets.length) continue;
      buckets[idx].runs++;
      const phase = (run.status?.phase || "").toLowerCase();
      if (phase === "failed" || phase === "error") buckets[idx].failed++;
      const durationSec = (run.status?.tokenUsage?.durationMs || 0) / 1000;
      if (durationSec > 0) {
        buckets[idx].durationTotalSec += durationSec;
        buckets[idx].durationSamples++;
        buckets[idx].durationValuesSec.push(durationSec);
      }
    }
    for (const b of buckets) {
      b.avgDurationSec = b.durationSamples > 0 ? b.durationTotalSec / b.durationSamples : 0;
      b.p95DurationSec = percentile(b.durationValuesSec, 95);
    }
    const createdAt = (instances || [])
      .map((i) => new Date(i.metadata.creationTimestamp || "").getTime())
      .filter((n) => Number.isFinite(n))
      .sort((a, b) => a - b);
    let ptr = 0;
    for (let i = 0; i < buckets.length; i++) {
      const bucketEnd = buckets[i].ts + 60 * 60 * 1000 - 1;
      while (ptr < createdAt.length && createdAt[ptr] <= bucketEnd) ptr++;
      buckets[i].agentsInstalled = ptr;
    }
    countServingPerBucket(runs, buckets, 60 * 60 * 1000);
    return buckets;
  }

  const days = 7;
  const start = new Date(now);
  start.setHours(0, 0, 0, 0);
  start.setDate(start.getDate() - (days - 1));
  for (let i = 0; i < days; i++) {
    const d = new Date(start);
    d.setDate(start.getDate() + i);
    buckets.push({
      ts: d.getTime(),
      label: d.toLocaleDateString([], { month: "short", day: "numeric" }),
      runs: 0,
      failed: 0,
      durationTotalSec: 0,
      durationSamples: 0,
      durationValuesSec: [],
      avgDurationSec: 0,
      p95DurationSec: 0,
      agentsInstalled: 0,
      serving: 0,
    });
  }
  for (const run of runs || []) {
    const created = new Date(run.metadata.creationTimestamp || "").getTime();
    if (!Number.isFinite(created) || created < buckets[0].ts) continue;
    const idx = Math.floor((created - buckets[0].ts) / (24 * 60 * 60 * 1000));
    if (idx < 0 || idx >= buckets.length) continue;
    buckets[idx].runs++;
    const phase = (run.status?.phase || "").toLowerCase();
    if (phase === "failed" || phase === "error") buckets[idx].failed++;
    const durationSec = (run.status?.tokenUsage?.durationMs || 0) / 1000;
    if (durationSec > 0) {
      buckets[idx].durationTotalSec += durationSec;
      buckets[idx].durationSamples++;
      buckets[idx].durationValuesSec.push(durationSec);
    }
  }
  for (const b of buckets) {
    b.avgDurationSec = b.durationSamples > 0 ? b.durationTotalSec / b.durationSamples : 0;
    b.p95DurationSec = percentile(b.durationValuesSec, 95);
  }
  const createdAt = (instances || [])
    .map((i) => new Date(i.metadata.creationTimestamp || "").getTime())
    .filter((n) => Number.isFinite(n))
    .sort((a, b) => a - b);
  let ptr = 0;
  for (let i = 0; i < buckets.length; i++) {
    const bucketEnd = buckets[i].ts + 24 * 60 * 60 * 1000 - 1;
    while (ptr < createdAt.length && createdAt[ptr] <= bucketEnd) ptr++;
    buckets[i].agentsInstalled = ptr;
  }
  countServingPerBucket(runs, buckets, 24 * 60 * 60 * 1000);
  return buckets;
}

/** Count serving-phase runs active during each bucket. A serving run is active
 *  from its creation time onward (it's long-lived), so it counts in every
 *  bucket whose end is >= its creation timestamp. */
function countServingPerBucket(
  runs: NonNullable<ReturnType<typeof useRuns>["data"]>,
  buckets: ActivityBucket[],
  bucketMs: number,
) {
  const servingRuns = (runs || []).filter(
    (r) => (r.status?.phase || "").toLowerCase() === "serving",
  );
  for (const run of servingRuns) {
    const created = new Date(run.metadata.creationTimestamp || "").getTime();
    if (!Number.isFinite(created)) continue;
    for (let i = 0; i < buckets.length; i++) {
      const bucketEnd = buckets[i].ts + bucketMs - 1;
      if (created <= bucketEnd) {
        buckets[i].serving++;
      }
    }
  }
}

function linePath(points: Array<{ x: number; y: number }>) {
  if (!points.length) return "";
  return points.map((p, i) => `${i === 0 ? "M" : "L"}${p.x},${p.y}`).join(" ");
}

export function DashboardPage() {
  const instances = useInstances();
  const runs = useRuns();
  const policies = usePolicies();
  const skills = useSkills();
  const schedules = useSchedules();
  const personaPacks = usePersonaPacks();
  const { events, connected } = useWebSocket();
  const [range, setRange] = useState<RangeKey>("24h");
  const [durationMode, setDurationMode] = useState<DurationMode>("avg");
  const [hoverIdx, setHoverIdx] = useState<number | null>(null);

  const recentRuns = (runs.data || [])
    .sort(
      (a, b) =>
        new Date(b.metadata.creationTimestamp || "").getTime() -
        new Date(a.metadata.creationTimestamp || "").getTime()
    )
    .slice(0, 8);

  const activeRuns = (runs.data || []).filter(
    (r) => r.status?.phase === "Running" || r.status?.phase === "Pending" || r.status?.phase === "Serving"
  );
  const activity = useMemo(
    () => buildActivityBuckets(runs.data || [], instances.data || [], range),
    [runs.data, instances.data, range]
  );
  const totalInRange = useMemo(
    () => activity.reduce((acc, b) => acc + b.runs, 0),
    [activity]
  );
  const failedInRange = useMemo(
    () => activity.reduce((acc, b) => acc + b.failed, 0),
    [activity]
  );
  const failureRate = totalInRange > 0 ? (failedInRange / totalInRange) * 100 : 0;
  const avgDurationSecInRange = useMemo(() => {
    const dur = activity.reduce((acc, b) => acc + b.durationTotalSec, 0);
    const samples = activity.reduce((acc, b) => acc + b.durationSamples, 0);
    return samples > 0 ? dur / samples : 0;
  }, [activity]);
  const p95DurationSecInRange = useMemo(() => {
    const all = activity.flatMap((b) => b.durationValuesSec);
    return percentile(all, 95);
  }, [activity]);

  const stats = [
    {
      label: "Instances",
      value: instances.data?.length ?? "—",
      icon: Server,
      to: "/instances",
      color: "text-indigo-400",
    },
    {
      label: "Active Runs",
      value: activeRuns.length,
      icon: Play,
      to: "/runs",
      color: "text-emerald-400",
    },
    {
      label: "Policies",
      value: policies.data?.length ?? "—",
      icon: Shield,
      to: "/policies",
      color: "text-cyan-400",
    },
    {
      label: "Skills",
      value: skills.data?.length ?? "—",
      icon: Wrench,
      to: "/skills",
      color: "text-orange-400",
    },
    {
      label: "Schedules",
      value: schedules.data?.length ?? "—",
      icon: Clock,
      to: "/schedules",
      color: "text-violet-400",
    },
    {
      label: "Persona Packs",
      value: personaPacks.data?.length ?? "—",
      icon: Users,
      to: "/personas",
      color: "text-purple-400",
    },
  ];

  const chartW = 760;
  const chartH = 250;
  const padX = 32;
  const padY = 20;
  const innerW = chartW - padX * 2;
  const innerH = chartH - padY * 2;
  const durationFor = (b: ActivityBucket) =>
    durationMode === "p95" ? b.p95DurationSec : b.avgDurationSec;
  const maxDurationY = Math.max(1, ...activity.map((b) => durationFor(b)));
  const maxAgentsY = Math.max(1, ...activity.map((b) => b.agentsInstalled));
  const barW = Math.max(3, Math.min(14, (activity.length > 0 ? innerW / activity.length : 8) * 0.7));
  const xFor = (idx: number) =>
    padX + (activity.length <= 1 ? innerW / 2 : (idx / (activity.length - 1)) * innerW);
  const yForDuration = (value: number) => padY + innerH - (value / maxDurationY) * innerH;
  const yForAgents = (value: number) => padY + innerH - (value / maxAgentsY) * innerH;
  const maxServingY = Math.max(1, ...activity.map((b) => b.serving));
  const yForServing = (value: number) => padY + innerH - (value / maxServingY) * innerH;
  const durationPoints = activity.map((b, i) => ({ x: xFor(i), y: yForDuration(durationFor(b)) }));
  const servingPoints = activity.map((b, i) => ({ x: xFor(i), y: yForServing(b.serving) }));
  const totalServing = activity.length > 0 ? activity[activity.length - 1].serving : 0;
  const activePoint = hoverIdx !== null ? activity[hoverIdx] : null;
  const activeX = hoverIdx !== null ? xFor(hoverIdx) : null;
  const activeYDuration = hoverIdx !== null ? yForDuration(durationFor(activity[hoverIdx])) : null;

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between gap-6">
        <div className="shrink-0">
          <h1 className="text-2xl font-bold text-white">Dashboard</h1>
          <p className="text-sm text-muted-foreground">
            Overview of your Sympozium cluster
          </p>
        </div>
        <div className="flex flex-1 items-center gap-6 rounded-lg border border-border/40 bg-card/50 px-5 py-3 text-sm text-muted-foreground">
          <div className="flex items-center gap-2 font-medium text-white shrink-0">
            <Activity className="h-4 w-4 text-emerald-400" />
            Cluster Status
          </div>
          <div className="h-8 w-px bg-border/60 shrink-0" />
          <div className="flex flex-1 items-center justify-around gap-4">
            <div className="text-center">
              <div className="text-xs text-muted-foreground">Instances</div>
              <div className="text-lg font-semibold text-foreground">{instances.data?.length ?? "—"}</div>
            </div>
            <div className="text-center">
              <div className="text-xs text-muted-foreground">Active Runs</div>
              <div className="text-lg font-semibold text-foreground">{activeRuns.length}</div>
            </div>
            <div className="text-center">
              <div className="text-xs text-muted-foreground">Skills</div>
              <div className="text-lg font-semibold text-foreground">{skills.data?.length ?? "—"}</div>
            </div>
            <div className="text-center">
              <div className="text-xs text-muted-foreground">Policies</div>
              <div className="text-lg font-semibold text-foreground">{policies.data?.length ?? "—"}</div>
            </div>
            <div className="text-center">
              <div className="text-xs text-muted-foreground">Failure Rate</div>
              <div className={`text-lg font-semibold ${failureRate > 10 ? "text-red-400" : "text-foreground"}`}>
                {totalInRange > 0 ? `${failureRate.toFixed(1)}%` : "—"}
              </div>
            </div>
            <div className="text-center">
              <div className="text-xs text-muted-foreground">Avg Duration</div>
              <div className="text-lg font-semibold text-foreground">
                {avgDurationSecInRange > 0 ? `${avgDurationSecInRange.toFixed(1)}s` : "—"}
              </div>
            </div>
          </div>
        </div>
      </div>

      <Card className="overflow-hidden">
        <CardContent className="p-0">
          <div className="grid grid-cols-2 divide-x divide-y divide-border/60 sm:grid-cols-3 lg:grid-cols-6 lg:divide-y-0">
            {stats.map((stat) => (
              <Link
                key={stat.label}
                to={stat.to}
                className="group flex min-h-24 items-center gap-3 px-4 py-3 transition-colors hover:bg-white/[0.03]"
              >
                <div className="rounded-md bg-background/60 p-2 border border-border/50">
                  <stat.icon className={`h-4 w-4 ${stat.color}`} />
                </div>
                <div className="min-w-0">
                  <div className="text-[11px] uppercase tracking-wide text-muted-foreground">
                    {stat.label}
                  </div>
                  {instances.isLoading ? (
                    <Skeleton className="mt-1 h-6 w-10" />
                  ) : (
                    <div className="text-xl font-bold leading-tight">{stat.value}</div>
                  )}
                </div>
              </Link>
            ))}
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
          <div>
            <CardTitle className="text-base">Agent Activity</CardTitle>
            <p className="text-xs text-muted-foreground mt-1">
              Runs over time with failed runs highlighted
            </p>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <Button
              size="sm"
              variant={durationMode === "avg" ? "default" : "outline"}
              className="h-7 text-xs"
              onClick={() => setDurationMode("avg")}
            >
              Avg
            </Button>
            <Button
              size="sm"
              variant={durationMode === "p95" ? "default" : "outline"}
              className="h-7 text-xs"
              onClick={() => setDurationMode("p95")}
            >
              P95
            </Button>
            <Button
              size="sm"
              variant={range === "1h" ? "default" : "outline"}
              className="h-7 text-xs"
              onClick={() => setRange("1h")}
            >
              1h
            </Button>
            <Button
              size="sm"
              variant={range === "24h" ? "default" : "outline"}
              className="h-7 text-xs"
              onClick={() => setRange("24h")}
            >
              24hr
            </Button>
            <Button
              size="sm"
              variant={range === "7d" ? "default" : "outline"}
              className="h-7 text-xs"
              onClick={() => setRange("7d")}
            >
              7 days
            </Button>
          </div>
        </CardHeader>
        <CardContent>
          <div className="mb-3 flex flex-wrap items-center gap-4 text-xs">
            <span className="text-muted-foreground">
              Total runs: <span className="text-foreground font-semibold">{totalInRange}</span>
            </span>
            <span className="text-cyan-400">
              {durationMode === "p95" ? "P95 duration" : "Avg duration"}:{" "}
              <span className="font-semibold">
                {(durationMode === "p95" ? p95DurationSecInRange : avgDurationSecInRange).toFixed(1)}s
              </span>
            </span>
            <span className="text-red-400">
              Failed runs: <span className="font-semibold">{failedInRange}</span>
            </span>
            <span className="text-muted-foreground">
              Failure rate: <span className="text-foreground font-semibold">{failureRate.toFixed(1)}%</span>
            </span>
            {totalServing > 0 && (
              <span className="text-yellow-400">
                Serving: <span className="font-semibold">{totalServing}</span>
              </span>
            )}
          </div>
          {runs.isLoading ? (
            <Skeleton className="h-[250px] w-full" />
          ) : (
            <div className="relative h-[250px] w-full rounded-lg border border-border/50 bg-background/40 p-2">
              <svg viewBox={`0 0 ${chartW} ${chartH}`} className="h-full w-full">
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

                {activeX !== null && (
                  <line
                    x1={activeX}
                    x2={activeX}
                    y1={padY}
                    y2={chartH - padY}
                    stroke="currentColor"
                    className="text-indigo-300/60"
                    strokeDasharray="4 3"
                    strokeWidth={1}
                  />
                )}

                {activity.map((b, i) => (
                  <g key={b.ts}>
                    <rect
                      x={xFor(i) - barW / 2}
                      y={yForAgents(b.agentsInstalled)}
                      width={barW}
                      height={padY + innerH - yForAgents(b.agentsInstalled)}
                      className="pointer-events-none fill-cyan-400/15"
                    />
                  </g>
                ))}

                <path
                  d={linePath(durationPoints)}
                  fill="none"
                  stroke="currentColor"
                  className="text-indigo-400"
                  strokeWidth={2.5}
                />

                {/* Serving agents line */}
                {totalServing > 0 && (
                  <path
                    d={linePath(servingPoints)}
                    fill="none"
                    stroke="currentColor"
                    className="text-yellow-400"
                    strokeWidth={2}
                    strokeDasharray="6 3"
                  />
                )}
                {totalServing > 0 && activity.map((b, i) => (
                  <circle
                    key={`srv-${b.ts}`}
                    cx={xFor(i)}
                    cy={yForServing(b.serving)}
                    r={hoverIdx === i ? 4 : 2.5}
                    className="fill-yellow-400"
                  />
                ))}

                {activity.map((b, i) => (
                  <g key={`pt-${b.ts}`}>
                    <circle
                      cx={xFor(i)}
                      cy={yForDuration(durationFor(b))}
                      r={hoverIdx === i ? 5 : 3.5}
                      className="cursor-pointer fill-indigo-400"
                      onMouseEnter={() => setHoverIdx(i)}
                    />
                    {b.failed > 0 && (
                      <circle
                        cx={xFor(i)}
                        cy={yForDuration(durationFor(b))}
                        r={hoverIdx === i ? 7 : 6}
                        className="cursor-pointer fill-transparent stroke-red-400"
                        strokeWidth={2}
                        onMouseEnter={() => setHoverIdx(i)}
                      />
                    )}
                  </g>
                ))}

                {activity.map((b, i) => (
                  <text
                    key={`x-${b.ts}`}
                    x={xFor(i)}
                    y={chartH - 5}
                    textAnchor="middle"
                    className={`fill-current text-[10px] ${
                      i % Math.ceil(activity.length / 8) === 0 || i === activity.length - 1
                        ? "text-muted-foreground"
                        : "text-transparent"
                    }`}
                  >
                    {b.label}
                  </text>
                ))}

                {activeX !== null && activeYDuration !== null && (
                  <circle cx={activeX} cy={activeYDuration} r={6} className="fill-indigo-300/30" />
                )}
              </svg>

              {activePoint && (
                <div className="pointer-events-none absolute right-3 top-3 rounded-md border border-white/10 bg-black/70 px-3 py-2 text-xs backdrop-blur">
                  <div className="font-semibold text-foreground">{activePoint.label}</div>
                  <div className="text-indigo-300">
                    {durationMode === "p95" ? "P95 duration" : "Avg duration"}:{" "}
                    {(durationMode === "p95" ? activePoint.p95DurationSec : activePoint.avgDurationSec).toFixed(1)}s
                  </div>
                  <div className="text-muted-foreground">Runs: {activePoint.runs}</div>
                  <div className="text-red-300">Failed: {activePoint.failed}</div>
                  <div className="text-cyan-300">Agents installed: {activePoint.agentsInstalled}</div>
                  {activePoint.serving > 0 && (
                    <div className="text-yellow-300">Serving: {activePoint.serving}</div>
                  )}
                </div>
              )}
              <div className="mt-1 flex items-center gap-4 px-2 text-xs">
                <span className="inline-flex items-center gap-2 text-muted-foreground">
                  <span className="h-2 w-2 rounded-full bg-indigo-400" />
                  {durationMode === "p95" ? "P95 run duration" : "Avg run duration"}
                </span>
                <span className="inline-flex items-center gap-2 text-muted-foreground">
                  <span className="h-2 w-2 rounded-sm bg-cyan-400/40" />
                  Agents installed (bars)
                </span>
                <span className="inline-flex items-center gap-2 text-muted-foreground">
                  <span className="h-2 w-2 rounded-full bg-yellow-400" />
                  Serving agents
                </span>
                <span className="inline-flex items-center gap-2 text-muted-foreground">
                  <span className="h-2 w-2 rounded-full bg-red-400" />
                  Failed buckets
                </span>
              </div>
            </div>
          )}
        </CardContent>
      </Card>

      <div className="grid gap-6 lg:grid-cols-2">
        {/* Recent runs */}
        <Card>
          <CardHeader className="flex flex-row items-center justify-between">
            <CardTitle className="text-base">Recent Runs</CardTitle>
            <Link
              to="/runs"
              className="text-xs text-muted-foreground hover:text-foreground"
            >
              View all →
            </Link>
          </CardHeader>
          <CardContent>
            {runs.isLoading ? (
              <div className="space-y-2">
                {Array.from({ length: 4 }).map((_, i) => (
                  <Skeleton key={i} className="h-8 w-full" />
                ))}
              </div>
            ) : recentRuns.length === 0 ? (
              <p className="text-sm text-muted-foreground">No runs yet</p>
            ) : (
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Name</TableHead>
                    <TableHead>Instance</TableHead>
                    <TableHead>Phase</TableHead>
                    <TableHead>Age</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {recentRuns.map((run) => (
                    <TableRow key={run.metadata.name}>
                      <TableCell className="font-mono text-xs">
                        <Link
                          to={`/runs/${run.metadata.name}`}
                          className="hover:text-primary"
                        >
                          {truncate(run.metadata.name, 30)}
                        </Link>
                      </TableCell>
                      <TableCell className="text-xs text-muted-foreground">
                        {run.spec.instanceRef}
                      </TableCell>
                      <TableCell>
                        <StatusBadge phase={run.status?.phase} />
                      </TableCell>
                      <TableCell className="text-xs text-muted-foreground">
                        {formatAge(run.metadata.creationTimestamp)}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            )}
          </CardContent>
        </Card>

        {/* Live event stream */}
        <Card>
          <CardHeader className="flex flex-row items-center justify-between">
            <CardTitle className="flex items-center gap-2 text-base">
              <Activity className="h-4 w-4" />
              Event Stream
              {connected ? (
                <span className="relative flex h-2 w-2">
                  <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-emerald-400 opacity-75" />
                  <span className="relative inline-flex h-2 w-2 rounded-full bg-emerald-400" />
                </span>
              ) : (
                <span className="h-2 w-2 rounded-full bg-red-400" />
              )}
            </CardTitle>
            <span className="text-xs text-muted-foreground">
              {events.length} events
            </span>
          </CardHeader>
          <CardContent>
            <div className="h-64 space-y-1 overflow-auto rounded-lg bg-background/50 border border-border/50 p-3 font-mono text-xs">
              {events.length === 0 ? (
                <p className="text-muted-foreground">
                  {connected
                    ? "Waiting for events…"
                    : "Connecting to stream…"}
                </p>
              ) : (
                events
                  .slice()
                  .reverse()
                  .map((evt, i) => (
                    <div key={i} className="text-muted-foreground">
                      <span className="text-emerald-400/80">
                        {new Date(evt.timestamp).toLocaleTimeString()}
                      </span>{" "}
                      <span className="text-indigo-400">{evt.topic}</span>{" "}
                      {typeof evt.data === "string"
                        ? truncate(evt.data, 80)
                        : truncate(JSON.stringify(evt.data), 80)}
                    </div>
                  ))
              )}
            </div>
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
