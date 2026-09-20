import { useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import type { CellnCell, CellnCellsSource, CellnNodeCells, CellnNodeParent, CellnRunRef } from "@/lib/api";
import { useCellnFleetCells } from "@/hooks/use-api";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Cpu } from "lucide-react";
import { cn } from "@/lib/utils";

const ALL = "__all__";

const statusTone: Record<string, string> = {
  running: "border-blue-500/50 text-blue-400",
  dissolved: "border-border text-muted-foreground",
  failed: "border-destructive/60 text-destructive",
  refused: "border-amber-500/60 text-amber-500",
  died: "border-destructive/60 text-destructive",
};

function ago(ms: number, now: number) {
  const s = Math.max(0, Math.round((now - ms) / 1000));
  if (s < 60) return `${s}s ago`;
  if (s < 3600) return `${Math.round(s / 60)}m ago`;
  if (s < 86400) return `${Math.round(s / 3600)}h ago`;
  return `${Math.round(s / 86400)}d ago`;
}

// Older journals record a turn by its 64-hex hash; keep the column readable.
function shortTurn(turn?: string) {
  if (!turn) return "—";
  return /^[0-9a-f]{64}$/.test(turn) ? `${turn.slice(0, 12)}…` : turn;
}

function duration(cell: CellnCell, now: number) {
  const ms = cell.duration_ms ?? (cell.finished_ms ? cell.finished_ms - cell.started_ms : now - cell.started_ms);
  return ms < 1000 ? `${ms} ms` : `${(ms / 1000).toFixed(1)} s`;
}

export { shortTurn };

const sourceLabel: Record<CellnCellsSource, string> = { gateway: "gateway", "node-report": "node reports" };

// A parent the gateway observed live holds a context unless its owner says
// otherwise; without the gateway, the run's phase is the only evidence.
const endedParentStatus = new Set(["Stopped", "ContextLost", "TeardownUncertain"]);
function parentIsLive(p: CellnNodeParent) {
  if (p.status && p.statusLive) return !endedParentStatus.has(p.status);
  return !!p.run?.live;
}

export function RunLink({ run }: { run: CellnRunRef }) {
  return (
    <Link to={`/runs/${run.name}`} className="font-mono text-blue-400 hover:underline">
      {run.namespace}/{run.name}
    </Link>
  );
}

/**
 * `celln ps` for every fleet node: the Celln gateway lists each node's cells
 * and parents (or, on older Celln releases, each node's configure pod reports
 * them), and the API server joins them to their runs. Worker
 * cells run one turn each; the persistent parent holding a conversation's
 * context is listed separately, as `celln ps` does not show it.
 */
/** The current time, advancing every interval, so relative times keep moving. */
function useNow(intervalMs: number) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now()), intervalMs);
    return () => window.clearInterval(id);
  }, [intervalMs]);
  return now;
}

export function CellnNodeCellsCard() {
  const { data, isLoading, isError, dataUpdatedAt } = useCellnFleetCells();
  const [node, setNode] = useState(ALL);
  const [all, setAll] = useState(false);
  const now = useNow(1000);
  const nodes = useMemo(() => (data || []).filter((n) => node === ALL || n.node === node), [data, node]);

  // No fleet on this cluster: the section has nothing to say. A failed poll
  // after a good one keeps the last report on screen and says so.
  if (!data && (isError || !isLoading)) return null;
  if (data && data.length === 0) return null;
  const polling = !isError && now - dataUpdatedAt < 10000;
  // One source for the whole fleet is said once, in the header; a mix (a
  // backend the gateway could not list, filled from its node report) per node.
  const sources = Array.from(new Set((data || []).map((n) => n.source).filter((s): s is CellnCellsSource => !!s)));
  const mixed = sources.length > 1;

  return (
    <Card data-testid="celln-node-cells">
      <CardHeader className="pb-3">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div>
            <CardTitle className="flex items-center gap-2 text-base">
              <Cpu className="h-4 w-4" /> Celln cells
              <span className={cn("flex items-center gap-1 text-xs font-normal", polling ? "text-emerald-400" : "text-amber-500")} data-testid="celln-cells-live">
                <span className={cn("h-1.5 w-1.5 rounded-full", polling ? "animate-pulse bg-emerald-400" : "bg-amber-500")} />
                {polling ? "live" : "not updating"} · updated {ago(dataUpdatedAt, now)}
              </span>
              {sources.length > 0 && (
                <span className="text-xs font-normal text-muted-foreground" data-testid="celln-cells-source">
                  source: {mixed ? "mixed" : sourceLabel[sources[0]]}
                </span>
              )}
            </CardTitle>
            <p className="mt-1 text-xs text-muted-foreground">
              <code>celln ps{all ? " -a" : ""}</code> on each fleet node, refreshed every 2 seconds. Each turn runs in its own worker cell; the parent holding a conversation is listed below it.
            </p>
          </div>
          <div className="flex items-center gap-3">
            <Select value={node} onValueChange={setNode}>
              <SelectTrigger className="h-8 w-48 text-xs"><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem value={ALL} className="text-xs">All nodes</SelectItem>
                {(data || []).map((n) => <SelectItem key={n.node} value={n.node} className="text-xs">{n.node}</SelectItem>)}
              </SelectContent>
            </Select>
            <label className="flex items-center gap-1.5 text-xs text-muted-foreground">
              <input type="checkbox" checked={all} onChange={(e) => setAll(e.target.checked)} />
              Show finished (-a)
            </label>
          </div>
        </div>
      </CardHeader>
      <CardContent className="space-y-5">
        {isLoading && <p className="text-xs text-muted-foreground">Reading fleet nodes…</p>}
        {nodes.map((n) => <NodeCells key={n.node} report={n} all={all} now={now} showSource={mixed} />)}
      </CardContent>
    </Card>
  );
}

function NodeCells({ report, all, now, showSource }: { report: CellnNodeCells; all: boolean; now: number; showSource: boolean }) {
  const cells = all ? report.cells : report.cells.filter((c) => c.status === "running");
  const liveParents = report.parents.filter(parentIsLive);
  const parents = all ? report.parents : liveParents;
  return (
    <div className="space-y-2" data-testid={`celln-node-${report.node}`}>
      <div className="flex flex-wrap items-center gap-2 text-sm">
        <span className="font-mono font-medium">{report.node}</span>
        <Badge variant="secondary">{report.cells.filter((c) => c.status === "running").length} running</Badge>
        <Badge variant="secondary">{liveParents.length} live parent{liveParents.length === 1 ? "" : "s"}</Badge>
        {report.error ? (
          <Badge variant="destructive">{report.error}</Badge>
        ) : (
          <span className={cn("text-xs", report.stale ? "text-amber-500" : "text-muted-foreground")}>
            reported {ago(report.reportedMs, now)}{report.stale ? " — stale; check celln-node-configure on this node" : ""}
          </span>
        )}
        {showSource && report.source && (
          <span className="text-xs text-muted-foreground" data-testid={`celln-node-source-${report.node}`}>source: {sourceLabel[report.source]}</span>
        )}
      </div>
      <div className="overflow-x-auto rounded border">
        <table className="w-full text-xs">
          <thead className="bg-muted/40 text-left text-muted-foreground">
            <tr>
              <th className="px-2 py-1.5 font-medium">Cell</th>
              <th className="px-2 py-1.5 font-medium">Status</th>
              <th className="px-2 py-1.5 font-medium">Run</th>
              <th className="px-2 py-1.5 font-medium">Turn</th>
              <th className="px-2 py-1.5 font-medium">Tools</th>
              <th className="px-2 py-1.5 font-medium">Started</th>
              <th className="px-2 py-1.5 font-medium">Duration</th>
            </tr>
          </thead>
          <tbody>
            {cells.length === 0 ? (
              <tr><td colSpan={7} className="px-2 py-3 text-center text-muted-foreground">{all ? "No cells recorded on this node." : "No running cells. Tick “Show finished” for recent ones."}</td></tr>
            ) : cells.map((c) => (
              <tr key={c.id} className="border-t align-top">
                <td className="px-2 py-1.5 font-mono" title={c.description}>{c.id}</td>
                <td className="px-2 py-1.5">
                  <span className={cn("rounded border px-1.5 py-0.5", statusTone[c.status] || statusTone.dissolved)}>{c.status}</span>
                  {c.error && <p className="mt-1 max-w-xs break-words text-destructive">{c.error}</p>}
                </td>
                <td className="px-2 py-1.5">{c.run ? <RunLink run={c.run} /> : <span className="text-muted-foreground">—</span>}</td>
                <td className="px-2 py-1.5 font-mono" title={c.turn}>{shortTurn(c.turn)}</td>
                <td className="px-2 py-1.5 font-mono">{c.tools.join(", ")}</td>
                <td className="px-2 py-1.5 whitespace-nowrap">{ago(c.started_ms, now)}</td>
                <td className="px-2 py-1.5 whitespace-nowrap">{duration(c, now)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {parents.length > 0 && (
        <div className="rounded border p-2 text-xs">
          <p className="mb-1 font-medium">Persistent parents</p>
          <ul className="space-y-1">
            {parents.map((p) => {
              const last = p.turns[p.turns.length - 1];
              return (
                <li key={p.incarnation} className="flex flex-wrap items-center gap-x-3 gap-y-0.5">
                  <span className="font-mono" title={p.incarnation}>{p.incarnation.slice(7, 19)}</span>
                  {p.run ? <RunLink run={p.run} /> : <span className="text-muted-foreground">run not found</span>}
                  {p.status ? (
                    // The owner's own observation beats the run's phase.
                    <span className={p.statusLive ? "text-foreground" : "text-muted-foreground"} title={`Parent status from the Celln gateway${p.statusLive ? "" : " (not a live observation)"}; run phase ${p.run?.phase || "unknown"}`} data-testid="celln-parent-status">
                      {p.status}{p.statusLive ? "" : " (last known)"}
                    </span>
                  ) : (
                    <span className="text-muted-foreground">{p.run?.phase || "unknown"}{p.run && !p.run.live ? " (ended)" : ""}</span>
                  )}
                  <span className="text-muted-foreground">{p.turns.length} turn{p.turns.length === 1 ? "" : "s"}{last ? `, last ${shortTurn(last.turnId)}: ${last.stage}${last.succeeded === false ? " (failed)" : ""}` : ""}</span>
                  <span className="text-muted-foreground">{ago(p.updatedMs, now)}</span>
                </li>
              );
            })}
          </ul>
        </div>
      )}
    </div>
  );
}
