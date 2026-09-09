import { useQueries } from "@tanstack/react-query";
import { api, getNamespace, type CellnSelection, type CellnTool } from "@/lib/api";
import { Button } from "@/components/ui/button";

const names = ["workspace-read", "workspace-write", "https-fetch"] as const;

// A familiar name is not approval. Each installed revision is checked against
// the live operator/runtime/agent intersection before the preset can select it.
export function CellnStarterTools({ agentRef, runtimeRef, catalogue, onSelect }: {
  agentRef: string;
  runtimeRef?: string;
  catalogue: CellnTool[];
  onSelect: (tools: CellnSelection["toolRefs"]) => void;
}) {
  const tools = names.map((name) => catalogue.find((tool) => tool.metadata.name === name));
  const compatible = tools.map((tool, index) => !!tool && tool.spec.invocationABI === "celln.json-stdio/v1" && tool.spec.lane === "tool" && (
    index === 2 ? !!tool.spec.limits.https : tool.spec.limits.artifacts?.operation === (index === 0 ? "read" : "write")
  ));
  const approvals = useQueries({ queries: tools.map((tool, index) => ({
    queryKey: ["celln-starter-approval", getNamespace(), agentRef, runtimeRef, names[index], tool?.metadata.uid, tool?.spec.revision],
    queryFn: () => api.cellnTools.preview(agentRef, { runtimeRef, toolRefs: [{ name: tool!.metadata.name, revision: tool!.spec.revision }] }, "enduring"),
    enabled: !!agentRef && compatible[index],
    retry: false,
    staleTime: 0,
    gcTime: 0,
    refetchInterval: 10000,
  })) });
  const approved = tools.flatMap((tool, index) => {
    const query = approvals[index];
    return tool && compatible[index] && query.isSuccess && !query.isFetching && query.data.tools.some((entry) => entry.tool.name === tool.metadata.name && entry.tool.revision === tool.spec.revision)
      ? [{ name: tool.metadata.name, revision: tool.spec.revision }] : [];
  });
  const checking = approvals.some((query, index) => compatible[index] && query.isFetching);
  return <div className="space-y-2 rounded border p-3 text-xs" data-testid="celln-starter-tools">
    <p>Starter tools — run files and bounded HTTPS</p>
    {names.map((name, index) => <p key={name}>{name}: {!tools[index] ? "not installed" : !compatible[index] ? "installed revision is incompatible" : approvals[index].isFetching ? "checking approvals…" : approvals[index].isError ? "unavailable: current approvals could not be established" : approved.some((tool) => tool.name === name) ? `approved revision ${tools[index]!.spec.revision}` : "approval not established"}</p>)}
    <Button type="button" variant="outline" size="sm" disabled={checking || approved.length === 0} onClick={() => onSelect(approved)}>Use approved starter tools ({approved.length}/3)</Button>
    <p>This replaces the selection with the approved revisions listed above. No Python, shell, host mounts or unrestricted network access. Readiness and authority are checked again before execution.</p>
  </div>;
}
