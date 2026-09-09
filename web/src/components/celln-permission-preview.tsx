import { useQuery } from "@tanstack/react-query";
import { api, getNamespace, type CellnSelection } from "@/lib/api";

export function CellnPermissionPreview({ agentRef, selection, enduring = false }: { agentRef: string; selection: CellnSelection; enduring?: boolean }) {
  const preview = useQuery({
    queryKey: ["celln-permission-preview", getNamespace(), agentRef, selection, enduring],
    queryFn: () => api.cellnTools.preview(agentRef, selection, enduring ? "enduring" : undefined),
    retry: false,
    staleTime: 0,
    gcTime: 0,
    refetchInterval: 10000,
  });
  return <div className="space-y-1 rounded border p-2 text-xs" data-testid="celln-permission-preview">
    <p>Effective tool permissions — current approval intersection</p>
    {preview.isPending || preview.isFetching ? <p>Checking current approvals…</p> : preview.isError ? <p role="status">Permissions unavailable: {preview.error.message}</p> : preview.data ? <>
      {preview.data.tools.length === 0 && <p>No tools lent.</p>}
      {preview.data.tools.map(({ tool, limits }) => <div key={tool.name}>
        <p>{tool.name}@{tool.revision}: {limits.timeoutMillis} ms · {limits.memoryBytes} bytes memory · input {limits.argumentBytes} bytes · output {limits.outputBytes} bytes · host mounts {limits.workspace} · effects {limits.effects}</p>
        {limits.artifacts && <p>Run files: {limits.artifacts.operation} · {limits.artifacts.maxOperations} operations/turn · {limits.artifacts.maxFiles} files · {limits.artifacts.maxFileBytes} bytes/file · {limits.artifacts.maxTotalBytes} bytes total</p>}
        {limits.https && <p>HTTPS GET: {limits.https.allowHosts.join(", ")} · {limits.https.maxRequests} requests/turn · {limits.https.maxResponseBytes} response bytes · {limits.https.timeoutMillis} ms · no model credentials</p>}
      </div>)}
      <p>Shared cell memory ceiling: {preview.data.runtimeLimits.memoryBytes} bytes</p>
    </> : null}
    <p className="text-muted-foreground">Observation only, refreshed every 10 seconds. Does not check model authority, host readiness or capacity and does not authorize execution. Approvals are checked again before execution.</p>
  </div>;
}
