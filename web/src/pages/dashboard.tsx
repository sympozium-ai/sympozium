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

export function DashboardPage() {
  const instances = useInstances();
  const runs = useRuns();
  const policies = usePolicies();
  const skills = useSkills();
  const schedules = useSchedules();
  const personaPacks = usePersonaPacks();
  const { events, connected } = useWebSocket();

  const recentRuns = (runs.data || [])
    .sort(
      (a, b) =>
        new Date(b.metadata.creationTimestamp || "").getTime() -
        new Date(a.metadata.creationTimestamp || "").getTime()
    )
    .slice(0, 8);

  const activeRuns = (runs.data || []).filter(
    (r) => r.status?.phase === "Running" || r.status?.phase === "Pending"
  );

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

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold text-white">Dashboard</h1>
        <p className="text-sm text-muted-foreground">
          Overview of your Sympozium cluster
        </p>
      </div>

      {/* Stats grid */}
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-6">
        {stats.map((stat) => (
          <Link key={stat.label} to={stat.to}>
            <Card className="transition-all hover:bg-white/[0.03] hover:border-white/10">
              <CardHeader className="flex flex-row items-center justify-between pb-2">
                <CardTitle className="text-sm font-medium text-muted-foreground">
                  {stat.label}
                </CardTitle>
                <stat.icon className={`h-4 w-4 ${stat.color}`} />
              </CardHeader>
              <CardContent>
                {instances.isLoading ? (
                  <Skeleton className="h-8 w-12" />
                ) : (
                  <div className="text-2xl font-bold">{stat.value}</div>
                )}
              </CardContent>
            </Card>
          </Link>
        ))}
      </div>

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
