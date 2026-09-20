import { useEffect, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, Server } from "lucide-react";
import { useCluster } from "@/hooks/use-api";
import { Button } from "@/components/ui/button";

/** First eight characters of the kube-system UID — enough to tell two
 *  clusters apart at a glance. */
export function shortClusterID(id: string | undefined): string {
  return (id ?? "").slice(0, 8);
}

/**
 * ClusterBadge names the cluster the console is talking to. `sympozium serve`
 * and `make web-dev-serve` follow whatever kubeconfig context is active, so
 * without this nothing on screen distinguishes a Kind cluster from the host's.
 */
export function ClusterBadge() {
  const { data } = useCluster();
  const [open, setOpen] = useState(false);
  const root = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (root.current && !root.current.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);

  const nodes = data?.nodes ?? [];
  const label = data?.name || nodes[0]?.name || "";
  if (!data?.clusterID && !label) return null;
  const nodeCount = data?.nodeCount ?? nodes.length;

  return (
    <div className="relative" ref={root}>
      <button
        type="button"
        data-testid="cluster-badge"
        aria-expanded={open}
        aria-haspopup="dialog"
        title={`Cluster ${label || "unknown"} · ${data?.clusterID || "no ID"}`}
        onClick={() => setOpen((v) => !v)}
        className="flex h-7 items-center gap-1.5 rounded-full border border-border/50 bg-muted/50 px-3 text-xs font-medium text-muted-foreground hover:text-foreground"
      >
        <Server className="h-3.5 w-3.5" />
        <span className="max-w-40 truncate text-foreground">{label || "cluster"}</span>
        {data?.clusterID && (
          <span className="font-mono text-[10px]">{shortClusterID(data.clusterID)}</span>
        )}
      </button>
      {open && (
        <div
          role="dialog"
          aria-label="Cluster details"
          data-testid="cluster-popover"
          className="absolute left-0 top-9 z-50 w-80 rounded-md border border-border bg-popover p-3 text-xs text-popover-foreground shadow-md"
        >
          <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1.5">
            <dt className="text-muted-foreground">Cluster</dt>
            <dd>{data?.name || <span className="text-muted-foreground">unnamed</span>}</dd>
            <dt className="text-muted-foreground">Cluster ID</dt>
            <dd className="break-all font-mono" data-testid="cluster-id-full">
              {data?.clusterID || "unavailable"}
            </dd>
            <dt className="text-muted-foreground">Kubernetes</dt>
            <dd>{data?.kubernetesVersion || "unknown"}</dd>
            <dt className="text-muted-foreground">Sympozium</dt>
            <dd>{data?.sympoziumVersion || "unknown"}</dd>
          </dl>
          <p className="mb-1 mt-3 text-muted-foreground">
            Nodes ({nodeCount})
          </p>
          <ul className="max-h-48 space-y-1 overflow-auto" data-testid="cluster-nodes">
            {nodes.map((node) => (
              <li key={node.name} className="flex items-baseline justify-between gap-2">
                <span className="truncate font-mono">{node.name}</span>
                <span className="shrink-0 text-muted-foreground">
                  {[(node.roles ?? []).join(", "), node.kubeletVersion].filter(Boolean).join(" · ")}
                </span>
              </li>
            ))}
          </ul>
          {nodeCount > nodes.length && (
            <p className="mt-1 text-muted-foreground">
              and {nodeCount - nodes.length} more
            </p>
          )}
        </div>
      )}
    </div>
  );
}

const CLUSTER_ID_KEY = "sympozium_cluster_id";

function readSeenClusterID(): string {
  try {
    return sessionStorage.getItem(CLUSTER_ID_KEY) ?? "";
  } catch {
    return "";
  }
}

function writeSeenClusterID(id: string) {
  try {
    sessionStorage.setItem(CLUSTER_ID_KEY, id);
  } catch {
    // Storage unavailable: the in-memory ref still guards this page load.
  }
}

/**
 * ClusterChangeBanner warns when the cluster behind this browser session
 * changes — a port-forward that silently re-pointed at another kubeconfig
 * context. The first ID seen is kept in sessionStorage so the warning also
 * survives a manual page refresh; only the Reload button accepts the new one.
 */
export function ClusterChangeBanner() {
  const { data } = useCluster();
  const queryClient = useQueryClient();
  const seen = useRef(readSeenClusterID());
  const [change, setChange] = useState<{ from: string; to: string } | null>(null);

  const current = data?.clusterID ?? "";
  useEffect(() => {
    if (!current) return;
    if (!seen.current) {
      seen.current = current;
      writeSeenClusterID(current);
      return;
    }
    setChange(seen.current === current ? null : { from: seen.current, to: current });
  }, [current]);

  if (!change) return null;

  const reload = () => {
    writeSeenClusterID(change.to);
    queryClient.clear();
    window.location.reload();
  };

  return (
    <div
      role="alert"
      data-testid="cluster-changed-banner"
      className="flex items-center justify-between gap-3 border-b border-yellow-500/30 bg-yellow-500/10 px-6 py-2 text-sm text-yellow-700 dark:text-yellow-300"
    >
      <span className="flex items-center gap-2">
        <AlertTriangle className="h-4 w-4 shrink-0" />
        <span>
          This console is now connected to a different cluster (
          <span className="font-mono">{shortClusterID(change.from)}</span> →{" "}
          <span className="font-mono">{shortClusterID(change.to)}</span>). What is on screen may
          belong to the previous cluster.
        </span>
      </span>
      <Button size="sm" variant="outline" onClick={reload}>
        Reload
      </Button>
    </div>
  );
}
