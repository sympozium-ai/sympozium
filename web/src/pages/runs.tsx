import { useState, useEffect, useRef } from "react";
import { Link, useSearchParams } from "react-router-dom";
import {
  useRuns,
  useDeleteRun,
  useCreateRun,
  useAgents,
  useObservabilityMetrics,
  useGateVerdict,
  useCapabilities,
  useRuntimes,
  useCellnTools,
} from "@/hooks/use-api";
import { StatusBadge } from "@/components/status-badge";
import {
  Table,
  TableHeader,
  TableRow,
  TableHead,
  TableBody,
  TableCell,
} from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
  DialogDescription,
} from "@/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { CellnStarterTools } from "@/components/celln-starter-tools";
import {
  Plus,
  Trash2,
  ExternalLink,
  ShieldAlert,
  Check,
  X,
  AlertTriangle,
} from "lucide-react";
import {
  costTooltip,
  effectiveCost,
  formatAge,
  formatUsd,
  sumEffectiveCosts,
  taskText,
  truncate,
} from "@/lib/utils";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { useRunsSeen } from "@/hooks/use-runs-seen";
import type { AgentRun } from "@/lib/api";
import { CellnPermissionPreview } from "@/components/celln-permission-preview";

/** Returns true when a run is in PostRunning with a gate hook awaiting a verdict. */
function isAwaitingGate(run: AgentRun): boolean {
  if (run.status?.phase !== "PostRunning") return false;
  if (run.status?.gateVerdict) return false; // already resolved
  return !!run.spec.lifecycle?.postRun?.some((h) => h.gate);
}

export function RunsPage() {
  const { data, isLoading } = useRuns();
  const instances = useAgents();
  const observability = useObservabilityMetrics();
  const capabilities = useCapabilities();
  const runtimes = useRuntimes();
  const catalogue = useCellnTools();
  const [lentTools, setLentTools] = useState<{ name: string; revision: string }[]>([]);
  const [enduring, setEnduring] = useState(false);
  const [parentSystemPrompt, setParentSystemPrompt] = useState("");
  const [requireToolCall, setRequireToolCall] = useState(false);
  const [parentRequested, setParentRequested] = useState(false);
  const [parentLimits, setParentLimits] = useState({ leaseSeconds: 300, maxTurns: 4, maxModelRequests: 12, maxOutputTokens: 4096 });
  const deleteRun = useDeleteRun();
  const createRun = useCreateRun();
  const gateVerdict = useGateVerdict();
  const [open, setOpen] = useState(false);
  const [searchParams, setSearchParams] = useSearchParams();
  const [search, setSearch] = useState("");
  const { isUnseen, markAllSeen } = useRunsSeen();
  const markedRef = useRef(false);
  const [form, setForm] = useState({
    agentRef: "",
    task: "",
    model: "",
    timeout: "5m",
    backend: "job",
    runtimeRef: "",
  });
  const selectedAgent = (instances.data || []).find((agent) => agent.metadata.name === form.agentRef);
  const runtimeName = form.runtimeRef || selectedAgent?.spec.runtimeRef || "";
  const selectedRuntime = (runtimes.data || []).find((runtime) => runtime.metadata.name === runtimeName);
  const cellnHarness = form.backend === "celln" && !!runtimeName;
  const enduringRequest = cellnHarness && enduring;
  const parentBounds = { leaseSeconds: [1, 86400], maxTurns: [1, 1024], maxModelRequests: [0, 6144], maxOutputTokens: [0, 3145728] } as const;
  const invalidParent = enduringRequest && (new TextEncoder().encode(form.task).length > 2048 || form.task.includes("\0") ||
    (requireToolCall && (lentTools.length === 0 || parentLimits.maxModelRequests < 2 || parentLimits.maxOutputTokens < 1)) ||
    Object.entries(parentLimits).some(([key, value]) => {
      const [min, max] = parentBounds[key as keyof typeof parentLimits];
      return !Number.isInteger(value) || value < min || value > max;
    }));
  const compatibleHarness = selectedRuntime?.spec.celln?.contractVersion === "celln.json-tools/v1";
  const staleTools = lentTools.some((ref) => !(catalogue.data || []).some((tool) => tool.metadata.name === ref.name && tool.spec.revision === ref.revision));
  const incompatibleSkills = cellnHarness && !!selectedAgent?.spec.skills?.length;
  const blockedSelection = cellnHarness && (incompatibleSkills || !compatibleHarness || !form.model.trim() || catalogue.isLoading || catalogue.isError || staleTools);
  const jobIncompatible = form.backend === "job" && !!selectedRuntime?.spec.celln && !selectedRuntime.spec.image;

  useEffect(() => {
    if (searchParams.get("create") === "1") {
      const agentRef = searchParams.get("agent") || "";
      const agent = (instances.data || []).find((item) => item.metadata.name === agentRef);
      const execution = agent?.spec.execution;
      setForm((current) => ({
        ...current,
        agentRef,
        backend: execution?.backend || current.backend || "job",
        model: execution?.model || current.model,
      }));
      if (execution?.executionLifecycle === "enduring") setEnduring(true);
      if (execution?.cellnSelection?.toolRefs) setLentTools(execution.cellnSelection.toolRefs);
      setOpen(true);
      setSearchParams({}, { replace: true });
    }
  }, [searchParams, setSearchParams, instances.data]);

  // Mark all runs as seen after a short delay so "new" dots are visible briefly.
  useEffect(() => {
    if (markedRef.current || isLoading || !data) return;
    markedRef.current = true;
    const timer = setTimeout(markAllSeen, 2000);
    return () => clearTimeout(timer);
  }, [isLoading, data, markAllSeen]);

  const sorted = (data || []).sort((a, b) => {
    const ta = a.metadata.creationTimestamp ? new Date(a.metadata.creationTimestamp).getTime() : 0;
    const tb = b.metadata.creationTimestamp ? new Date(b.metadata.creationTimestamp).getTime() : 0;
    return tb - ta;
  });

  const filtered = sorted.filter(
    (r) =>
      r.metadata.name.toLowerCase().includes(search.toLowerCase()) ||
      r.spec.agentRef.toLowerCase().includes(search.toLowerCase()) ||
      taskText(r.spec.task).toLowerCase().includes(search.toLowerCase()),
  );

  const spend = sumEffectiveCosts(filtered);

  const oneShotCapability = capabilities.data?.celln.oneShot || capabilities.data?.celln;
  const cellnUnavailable = oneShotCapability && !oneShotCapability.available;
  const hasCellnRuns = sorted.some((r) => r.spec.backend === "celln" && r.spec.executionLifecycle !== "enduring");

  const handleCreate = () => {
    if (blockedSelection || jobIncompatible || invalidParent || parentRequested) return;
    const request = cellnHarness
      ? { ...form, runtimeRef: undefined, provider: "deepseek", cellnSelection: { runtimeRef: form.runtimeRef || undefined, toolRefs: lentTools } }
      : form;
    if (enduringRequest) setParentRequested(true);
    createRun.mutate({ ...request, ...(enduringRequest ? { timeout: `${parentLimits.leaseSeconds}s`, executionLifecycle: "enduring" as const, enduring: { ...parentLimits, ...(requireToolCall ? { requireToolCall: true } : {}) }, systemPrompt: parentSystemPrompt } : {}) }, {
      onSuccess: () => {
        setOpen(false);
        setForm({ agentRef: "", task: "", model: "", timeout: "5m", backend: "job", runtimeRef: "" });
        setLentTools([]);
        setEnduring(false);
        setParentSystemPrompt("");
        setRequireToolCall(false);
        setParentRequested(false);
      },
    });
  };

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold">Runs</h1>
          <p className="text-sm text-muted-foreground">
            AgentRuns — individual agent invocations
          </p>
        </div>
        <Dialog open={open} onOpenChange={setOpen}>
          <DialogTrigger asChild>
            <Button
              size="sm"
              className="bg-primary hover:bg-primary/90 text-primary-foreground border-0"
            >
              <Plus className="mr-2 h-4 w-4" /> New Run
            </Button>
          </DialogTrigger>
          <DialogContent className="max-h-[90vh] overflow-y-auto">
            <DialogHeader>
              <DialogTitle>Create Run</DialogTitle>
              <DialogDescription>
                Choose where the work runs, then select an Agent and a compatible harness.
              </DialogDescription>
            </DialogHeader>
            <div className="space-y-4 pt-2">
              <fieldset className="space-y-2 rounded-md border p-3" data-testid="execution-environment">
                <legend className="px-1 text-sm font-medium">Execution environment</legend>
                <div className="grid gap-2 sm:grid-cols-2">
                  {[["job", "Kubernetes", "Default · containers and OCI harnesses"], ["celln", "Celln", "Opt-in · hardware-isolated cells"]].map(([value, title, description]) => (
                    <label key={value} className={`cursor-pointer rounded-md border p-3 ${form.backend === value ? "border-primary bg-primary/5" : "border-border"}`}>
                      <span className="flex items-center gap-2 font-medium">
                        <input type="radio" name="execution-environment" value={value} checked={form.backend === value}
                          onChange={() => { setForm({ ...form, backend: value, model: value === "celln" && !form.model ? "deepseek-chat" : form.model }); setLentTools([]); }} />
                        {title}
                      </span>
                      <span className="mt-1 block text-xs text-muted-foreground">{description}</span>
                    </label>
                  ))}
                </div>
                <p className="text-xs text-muted-foreground">Harness selection does not change the execution environment.</p>
                {form.backend === "celln" && <p className="text-xs text-muted-foreground" role="status">
                  {capabilities.isLoading ? "Checking Celln availability…" : capabilities.isError ? "Cannot check Celln availability. Operator setup and admission are required." : capabilities.data?.celln.available ? "Celln host eligibility detected. Your harness, tools and permissions still need approval." : `Celln needs operator setup: ${capabilities.data?.celln.reason || "no eligible host reported"}`}
                </p>}
              </fieldset>
              <div className="space-y-2">
                <Label>Agent</Label>
                <Select
                  value={form.agentRef}
                  onValueChange={(v) => {
                    const agent = (instances.data || []).find((item) => item.metadata.name === v);
                    const execution = agent?.spec.execution;
                    const nextBackend = execution?.backend || "job";
                    setForm({
                      ...form,
                      agentRef: v,
                      runtimeRef: "",
                      backend: nextBackend,
                      model: execution?.model || (nextBackend === "celln" && !form.model ? "deepseek-chat" : form.model),
                    });
                    setEnduring(execution?.executionLifecycle === "enduring");
                    setLentTools(execution?.cellnSelection?.toolRefs || []);
                    if (execution?.enduring) {
                      setParentLimits({
                        leaseSeconds: execution.enduring.leaseSeconds,
                        maxTurns: execution.enduring.maxTurns,
                        maxModelRequests: execution.enduring.maxModelRequests,
                        maxOutputTokens: execution.enduring.maxOutputTokens,
                      });
                    }
                  }}
                >
                  <SelectTrigger>
                    <SelectValue placeholder="Select agent" />
                  </SelectTrigger>
                  <SelectContent>
                    {(instances.data || []).map((inst) => (
                      <SelectItem
                        key={inst.metadata.name}
                        value={inst.metadata.name}
                      >
                        {inst.metadata.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              <div className="space-y-2">
                <Label>Task</Label>
                <Textarea
                  value={form.task}
                  onChange={(e) => setForm({ ...form, task: e.target.value })}
                  placeholder="Describe the task for the agent…"
                  rows={4}
                />
              </div>
              <div className="space-y-2">
                <Label>Harness for this run</Label>
                <Select
                  value={form.runtimeRef || "inherit"}
                  onValueChange={(v) => { setForm({ ...form, runtimeRef: v === "inherit" ? "" : v }); setLentTools([]); }}
                >
                  <SelectTrigger>
                    <SelectValue placeholder="Use Agent default" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="inherit">Use Agent default{selectedAgent?.spec.runtimeRef ? ` — ${selectedAgent.spec.runtimeRef}` : " — built-in runner"}</SelectItem>
                    {(runtimes.data || []).map((runtime) => (
                      <SelectItem key={runtime.metadata.name} value={runtime.metadata.name}>
                        {runtime.metadata.name}{runtime.spec.supportOwner ? ` — ${runtime.spec.supportOwner}` : ""}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <p className="text-xs text-muted-foreground">
                  Inherits <span className="font-mono">{selectedAgent?.spec.runtimeRef || "the built-in runner"}</span> from the Agent. Choose another approved harness for this run if needed. Celln requires a compatible native harness for an enduring conversation.
                </p>
              </div>
              <div className="grid grid-cols-2 gap-4">
                <div className="space-y-2">
                  <Label>{cellnHarness ? "Model (required — DeepSeek)" : "Model (optional)"}</Label>
                  <Input
                    value={form.model}
                    onChange={(e) =>
                      setForm({ ...form, model: e.target.value })
                    }
                    placeholder={cellnHarness ? "deepseek-chat" : "gpt-4o"}
                  />
                </div>
                <div className="space-y-2">
                  <Label>{enduringRequest ? "Timeout (from parent lease)" : "Timeout"}</Label>
                  <Input
                    value={enduringRequest ? `${parentLimits.leaseSeconds}s` : form.timeout}
                    disabled={enduringRequest}
                    onChange={(e) =>
                      setForm({ ...form, timeout: e.target.value })
                    }
                    placeholder="5m"
                  />
                </div>
              </div>
              <div className="space-y-2">
                {jobIncompatible && <p role="alert" className="text-xs text-red-400">This Harness has no OCI image for the Job backend. Select Celln or choose an OCI-compatible Harness.</p>}
                {form.backend === "celln" && (
                  <>
                    <p className="text-xs text-muted-foreground mt-1">
                      Celln executes approved work in sealed microVMs. One-shot
                      work uses a disposable cell; enduring Harness work requires
                      a separately admitted persistent parent and per-turn children.
                    </p>
                    {!cellnHarness && <p className="text-xs text-amber-500/80 mt-1">
                      Uses whatever AI provider is configured on the KVM
                      host, not this run's Model field.
                    </p>}
                    {cellnHarness && <div className="space-y-3 rounded-md border p-3" data-testid="celln-harness-selection">
                      <p className="text-sm font-medium">Harness in Celln — {runtimeName}</p>
                      {incompatibleSkills && <p role="alert" className="text-xs text-red-400">This Agent has SkillPacks ({selectedAgent?.spec.skills?.map((skill) => skill.skillPackRef || skill.configMapRef).join(", ")}) that native Celln cannot use. Choose a dedicated native Agent with borrowed tools, or a compatible backend. Skills will not be silently removed.</p>}
                      <p className="text-xs text-muted-foreground">Skills are instructions; borrowed tools perform operations. Existing SkillPack sidecars and MCP connections are not automatically available in Celln.</p>
                      {!compatibleHarness && <p role="alert" className="text-xs text-red-400">This Harness does not declare the supported native JSON Celln contract. No backend fallback will be used.</p>}
                      <p className="text-xs text-muted-foreground">The model loop runs inside the cell. DeepSeek model access is independently approved by the host; Kubernetes model credentials are not used.</p>
                      <label className="flex items-center gap-2 text-sm"><input data-testid="celln-enduring-opt-in" type="checkbox" checked={enduring} onChange={(event) => setEnduring(event.target.checked)} />Enduring conversation (development — operator approval required)</label>
                      {enduringRequest && <div className="space-y-2" data-testid="celln-enduring-limits">
                        <p role="alert" className="text-xs text-amber-500">This creates a request, not a ready agent. A matching operator-prepared parent registration can admit it automatically. One-shot runtime metadata and node readiness do not establish enduring support. Fresh host-parent provisioning still requires operator preparation.</p>
                        <label className="block space-y-1 text-xs">Harness system prompt (optional)
                          <Textarea data-testid="celln-parent-system-prompt" value={parentSystemPrompt} onChange={(event) => setParentSystemPrompt(event.target.value)} placeholder="Instructions retained by the parent Harness" />
                        </label>
                        <p className="text-xs text-muted-foreground">These instructions must match the prepared parent registration exactly. An empty field requests no system prompt.</p>
                        <label className="flex items-center gap-2 text-xs"><input data-testid="celln-require-tool-call" type="checkbox" checked={requireToolCall} onChange={(event) => setRequireToolCall(event.target.checked)} />Require a fresh borrowed-tool call on every turn</label>
                        <p className="text-xs text-muted-foreground">Optional. Requires at least one selected tool and matching parent approval. A turn cannot report success without executing a lent tool; this does not require every selected tool.</p>
                        {(Object.keys(parentLimits) as (keyof typeof parentLimits)[]).map((key) => <label key={key} className="block text-xs">{key}
                          <Input data-testid={`celln-${key}`} type="number" min={parentBounds[key][0]} max={parentBounds[key][1]} step={1} value={Number.isNaN(parentLimits[key]) ? "" : parentLimits[key]} onChange={(event) => setParentLimits({ ...parentLimits, [key]: event.target.valueAsNumber })} />
                        </label>)}
                        <p className="text-xs">Max turns includes the initial message. Initial message: {new TextEncoder().encode(form.task).length}/2048 UTF-8 bytes. Context is retained while the parent lives, not restored after a crash.</p>
                        {invalidParent && <p role="alert" className="text-xs text-red-400">Correct the bounded integer limits or initial message before submitting.</p>}
                      </div>}
                      <Label>Borrowed catalogue tools (optional, maximum 16)</Label>
                      {enduringRequest && compatibleHarness && !catalogue.isLoading && !catalogue.isError && <CellnStarterTools agentRef={form.agentRef} runtimeRef={form.runtimeRef || undefined} catalogue={catalogue.data || []} onSelect={setLentTools} />}
                      <p className="text-xs text-muted-foreground" data-testid="celln-tools-explanation">Choose the tools this Harness may request. Selections are pinned to this run; installing a tool does not grant permission to use it. Agent, runtime and operator approvals must all allow it.</p>
                      <p className="text-xs text-muted-foreground">Only installed catalogue tools appear below. Shell, Python, general HTTP access and workspace read/write are not implicitly included. Adding tools to an existing enduring parent requires a new run.</p>
                      {catalogue.isLoading && <p className="text-xs">Loading catalogue…</p>}
                      {catalogue.isError && <p role="alert" className="text-xs text-red-400">Cannot load the catalogue. Submission is disabled.</p>}
                      {!catalogue.isLoading && !catalogue.isError && !(catalogue.data || []).length && <p className="text-xs">No reviewed tools in this namespace. An empty selection lends no tools.</p>}
                      {(catalogue.data || []).map((tool) => {
                        const checked = lentTools.some((ref) => ref.name === tool.metadata.name && ref.revision === tool.spec.revision);
                        const supported = tool.spec.invocationABI === "celln.json-stdio/v1" && tool.spec.lane === "tool";
                        return <label key={tool.metadata.uid || tool.metadata.name} className="block space-y-1 rounded border p-2 text-xs">
                          <span className="flex items-center gap-2"><input type="checkbox" checked={checked} disabled={!compatibleHarness || !supported || (!checked && lentTools.length >= 16)} onChange={() => setLentTools(checked ? lentTools.filter((ref) => ref.name !== tool.metadata.name) : [...lentTools, { name: tool.metadata.name, revision: tool.spec.revision }])} />
                            <span>{tool.metadata.name}@{tool.spec.revision}{!supported ? " — unsupported ABI/lane" : ""}</span></span>
                          <span className="block text-muted-foreground">{tool.spec.description}</span>
                          <span className="block text-muted-foreground">Support owner: {tool.spec.supportOwner}</span>
                          <span className="block break-all text-muted-foreground">Publisher: {tool.spec.publisherKey}</span>
                          <span className="block text-muted-foreground">Declared limits: {tool.spec.limits.timeoutMillis} ms · {tool.spec.limits.memoryBytes} bytes memory · workspace {tool.spec.limits.workspace} · effects {tool.spec.limits.effects}</span>
                        </label>;
                      })}
                      <p className="text-xs">Lending order: {lentTools.map((ref) => `${ref.name}@${ref.revision}`).join(" → ") || "none"}</p>
                      {compatibleHarness && !staleTools && <CellnPermissionPreview enduring={enduringRequest} agentRef={form.agentRef} selection={{ runtimeRef: form.runtimeRef || undefined, toolRefs: lentTools }} />}
                      {staleTools && <p role="alert" className="text-xs text-red-400">The catalogue changed. Clear and reselect the borrowed tools before submitting.</p>}
                      {!!lentTools.length && <Button type="button" variant="outline" size="sm" onClick={() => setLentTools([])}>Clear borrowed tools</Button>}
                      {!enduringRequest && <p className="text-xs text-amber-500">Selection readiness is not established. Catalogue metadata is not permission to run. Registered compositions can receive trusted issuance automatically; new combinations require operator preparation. Current approvals and effective permissions are checked before execution.</p>}
                    </div>}
                    {capabilities.data && !capabilities.data.celln.available ? (
                      <p className="flex items-start gap-1 text-xs text-red-400 mt-1">
                        <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0" />
                        <span>
                          Celln eligibility could not be confirmed
                          {capabilities.data.celln.reason
                            ? `: ${capabilities.data.celln.reason}`
                            : "."}{" "}
                          Dispatch still performs its own admission checks.
                        </span>
                      </p>
                    ) : capabilities.data?.celln.available ? (
                      <p className="text-xs text-amber-500/80 mt-1">
                        {capabilities.data.celln.reason ||
                          "Celln node preflight passed; runtime and tool readiness still require validation."}
                      </p>
                    ) : null}
                  </>
                )}
              </div>
              <Button
                className="w-full bg-primary hover:bg-primary/90 text-primary-foreground border-0"
                onClick={handleCreate}
                disabled={
                  !form.agentRef || !form.task || createRun.isPending || blockedSelection || jobIncompatible || invalidParent || parentRequested
                }
              >
                {createRun.isPending ? "Creating…" : enduringRequest ? "Request enduring run" : cellnHarness ? "Request catalogue run" : "Create Run"}
              </Button>
              {parentRequested && !createRun.isPending && <p role="alert" className="text-xs text-amber-500">Creation was not confirmed. Check the run list before making another request; this form will not resubmit it.</p>}
            </div>
          </DialogContent>
        </Dialog>
      </div>

      {cellnUnavailable && hasCellnRuns && (
        <div className="flex items-start gap-2 rounded-md border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-sm text-amber-400" data-testid="celln-capability-banner">
          <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" />
          <div>
            <p className="font-medium">Celln one-shot router needs attention</p>
            <p className="text-xs text-amber-400/80 mt-0.5">
              {oneShotCapability?.reason || "One-shot router preflight did not succeed."}
              {" "}Existing run statuses are unchanged. Native enduring runs use their own parent controller.
            </p>
          </div>
        </div>
      )}

      <Input
        placeholder="Search runs…"
        value={search}
        onChange={(e) => setSearch(e.target.value)}
        className="max-w-sm"
      />

      <div className="grid gap-4 md:grid-cols-5">
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-sm text-muted-foreground">
              Collector
            </CardTitle>
          </CardHeader>
          <CardContent>
            <p className="text-xl font-semibold">
              {observability.data?.collectorReachable
                ? "Connected"
                : observability.data?.collectorError?.includes("no such host")
                  ? "Not reachable"
                  : "Unavailable"}
            </p>
            {observability.data?.collectorError && (
              <p className="mt-1 text-xs text-muted-foreground">
                {observability.data.collectorError.includes("no such host")
                  ? "Collector DNS not resolvable — running outside cluster?"
                  : observability.data.collectorError}
              </p>
            )}
          </CardContent>
        </Card>
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-sm text-muted-foreground">
              Agent Runs
            </CardTitle>
          </CardHeader>
          <CardContent>
            <p className="text-xl font-semibold">
              {(observability.data?.agentRunsTotal || 0).toLocaleString()}
            </p>
          </CardContent>
        </Card>
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-sm text-muted-foreground">
              Token Usage
            </CardTitle>
          </CardHeader>
          <CardContent>
            <p className="text-sm font-mono">
              {(observability.data?.inputTokensTotal || 0).toLocaleString()} in
              / {(observability.data?.outputTokensTotal || 0).toLocaleString()}{" "}
              out
            </p>
          </CardContent>
        </Card>
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-sm text-muted-foreground">
              Tool Calls
            </CardTitle>
          </CardHeader>
          <CardContent>
            <p className="text-xl font-semibold">
              {(observability.data?.toolInvocations || 0).toLocaleString()}
            </p>
          </CardContent>
        </Card>
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-sm text-muted-foreground">
              Est. Spend
            </CardTitle>
          </CardHeader>
          <CardContent>
            <p
              className="text-xl font-semibold"
              title={
                spend.anySimulated
                  ? "Estimated spend for the runs listed below — includes simulated rates"
                  : "Estimated spend for the runs listed below"
              }
            >
              {spend.count > 0 ? formatUsd(spend.totalMicro) : "—"}
            </p>
          </CardContent>
        </Card>
      </div>

      {observability.data?.inputByModel?.length ? (
        <Card>
          <CardHeader>
            <CardTitle className="text-base">Model Token Breakdown</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="grid gap-2 md:grid-cols-2">
              {observability.data.inputByModel.slice(0, 6).map((row) => {
                const out =
                  observability.data?.outputByModel?.find(
                    (x) => x.label === row.label,
                  )?.value || 0;
                return (
                  <div key={row.label} className="rounded border p-3">
                    <p className="text-xs text-muted-foreground">{row.label}</p>
                    <p className="font-mono text-sm">
                      {Math.round(row.value).toLocaleString()} in /{" "}
                      {Math.round(out).toLocaleString()} out
                    </p>
                  </div>
                );
              })}
            </div>
          </CardContent>
        </Card>
      ) : null}

      {isLoading ? (
        <div className="space-y-2">
          {Array.from({ length: 5 }).map((_, i) => (
            <Skeleton key={i} className="h-12 w-full" />
          ))}
        </div>
      ) : filtered.length === 0 ? (
        <div className="py-12 text-center space-y-3">
          <p className="text-muted-foreground">
            {search ? "No runs match your search" : "No runs yet"}
          </p>
          {!search && (
            <p className="text-sm text-muted-foreground">
              Runs are created when you dispatch a task to an{" "}
              <Link
                to="/agents"
                className="text-blue-400 hover:text-blue-300"
              >
                Instance
              </Link>
              , or automatically via a{" "}
              <Link
                to="/schedules"
                className="text-blue-400 hover:text-blue-300"
              >
                Schedule
              </Link>
              .
            </p>
          )}
        </div>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>Instance</TableHead>
              <TableHead>Task</TableHead>
              <TableHead>Phase</TableHead>
              <TableHead>Tokens</TableHead>
              <TableHead>Est. Spend</TableHead>
              <TableHead>Age</TableHead>
              <TableHead className="w-20" />
            </TableRow>
          </TableHeader>
          <TableBody>
            {filtered.map((run) => (
              <TableRow key={run.metadata.name}>
                <TableCell className="font-mono text-xs">
                  <Link
                    to={`/runs/${run.metadata.name}`}
                    className="hover:text-primary flex items-center gap-1"
                  >
                    {isUnseen(run) && (
                      <span
                        className="h-2 w-2 rounded-full bg-blue-500 shrink-0"
                        title="New"
                      />
                    )}
                    {truncate(run.metadata.name, 32)}
                    <ExternalLink className="h-3 w-3 opacity-50" />
                  </Link>
                </TableCell>
                <TableCell className="text-sm">
                  <Link
                    to={`/agents/${run.spec.agentRef}`}
                    className="hover:text-primary"
                  >
                    {run.spec.agentRef}
                  </Link>
                </TableCell>
                <TableCell className="max-w-xs text-sm text-muted-foreground">
                  {truncate(taskText(run.spec.task), 60)}
                </TableCell>
                <TableCell>
                  <div className="flex items-center gap-1.5">
                    <StatusBadge phase={run.status?.phase} />
                    {isAwaitingGate(run) && (
                      <span
                        data-testid="gate-pending-badge"
                        className="inline-flex items-center gap-1 rounded-full border border-amber-500/40 bg-amber-500/10 px-2 py-0.5 text-[10px] font-medium text-amber-400"
                        title="Awaiting gate approval"
                      >
                        <ShieldAlert className="h-3 w-3" />
                        Approval
                      </span>
                    )}
                  </div>
                </TableCell>
                <TableCell className="text-xs text-muted-foreground">
                  {run.status?.tokenUsage
                    ? `${run.status.tokenUsage.totalTokens.toLocaleString()}`
                    : "—"}
                </TableCell>
                <TableCell className="text-xs text-muted-foreground">
                  {(() => {
                    const est = effectiveCost(run);
                    return est ? (
                      <span title={costTooltip(est)}>
                        {formatUsd(est.amountMicro)}
                      </span>
                    ) : null;
                  })()}
                </TableCell>
                <TableCell className="text-sm text-muted-foreground">
                  {formatAge(run.metadata.creationTimestamp)}
                </TableCell>
                <TableCell>
                  <div className="flex items-center gap-1">
                    {isAwaitingGate(run) && (
                      <>
                        <Button
                          variant="ghost"
                          size="icon"
                          data-testid="gate-approve-btn"
                          onClick={() =>
                            gateVerdict.mutate({
                              name: run.metadata.name,
                              data: {
                                action: "approve",
                                reason: "manual-approval",
                              },
                            })
                          }
                          disabled={gateVerdict.isPending}
                          title="Approve"
                        >
                          <Check className="h-4 w-4 text-green-400" />
                        </Button>
                        <Button
                          variant="ghost"
                          size="icon"
                          data-testid="gate-reject-btn"
                          onClick={() =>
                            gateVerdict.mutate({
                              name: run.metadata.name,
                              data: {
                                action: "reject",
                                response: "Rejected by operator",
                                reason: "manual-rejection",
                              },
                            })
                          }
                          disabled={gateVerdict.isPending}
                          title="Reject"
                        >
                          <X className="h-4 w-4 text-red-400" />
                        </Button>
                      </>
                    )}
                    <Button
                      variant="ghost"
                      size="icon"
                      onClick={() => deleteRun.mutate(run.metadata.name)}
                      disabled={deleteRun.isPending}
                      title="Delete"
                    >
                      <Trash2 className="h-4 w-4 text-destructive" />
                    </Button>
                  </div>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </div>
  );
}
