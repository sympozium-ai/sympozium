import { useState } from "react";
import { Link } from "react-router-dom";
import {
  useModels,
  useDeleteModel,
  useCreateModel,
  useClusterNodes,
  useNamespaces,
  useDensityQuery,
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
import { Skeleton } from "@/components/ui/skeleton";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Badge } from "@/components/ui/badge";
import { Plus, Trash2, ExternalLink, Cpu } from "lucide-react";
import { formatAge, cn } from "@/lib/utils";

type ServerType = "llama-cpp" | "vllm" | "tgi" | "custom";

const SERVER_TYPES: { value: ServerType; label: string }[] = [
  { value: "llama-cpp", label: "llama.cpp" },
  { value: "vllm", label: "vLLM" },
  { value: "tgi", label: "TGI" },
  { value: "custom", label: "Custom" },
];

const LLAMACPP_PRESETS = [
  {
    label: "Qwen3 8B (Q4)",
    name: "qwen3-8b-q4",
    url: "https://huggingface.co/Qwen/Qwen3-8B-GGUF/resolve/main/Qwen3-8B-Q4_K_M.gguf",
    storageSize: "8Gi",
    memory: "12Gi",
    cpu: "8",
    gpu: 0,
    contextSize: 8192,
    description: "5 GB · 8K context · strong reasoning",
  },
  {
    label: "Qwen3.5 9B (Q4)",
    name: "qwen3-5-9b-q4",
    url: "https://huggingface.co/unsloth/Qwen3.5-9B-GGUF/resolve/main/Qwen3.5-9B-Q4_K_M.gguf",
    storageSize: "8Gi",
    memory: "12Gi",
    cpu: "8",
    gpu: 0,
    contextSize: 8192,
    description: "5.7 GB · 8K context · latest Qwen",
  },
  {
    label: "Phi-3 Mini 4K (Q4)",
    name: "phi-3-mini-q4",
    url: "https://huggingface.co/microsoft/Phi-3-mini-4k-instruct-gguf/resolve/main/Phi-3-mini-4k-instruct-q4.gguf",
    storageSize: "4Gi",
    memory: "6Gi",
    cpu: "6",
    gpu: 0,
    contextSize: 4096,
    description: "2.2 GB · 4K context · fast & lightweight",
  },
  {
    label: "Qwen3 0.6B (Q8)",
    name: "qwen3-0-6b-q8",
    url: "https://huggingface.co/Qwen/Qwen3-0.6B-GGUF/resolve/main/Qwen3-0.6B-Q8_0.gguf",
    storageSize: "2Gi",
    memory: "4Gi",
    cpu: "4",
    gpu: 0,
    contextSize: 4096,
    description: "0.6 GB · 4K context · tiny, for testing",
  },
];

const VLLM_PRESETS = [
  {
    label: "Llama 3.1 8B Instruct",
    name: "llama-3-1-8b-vllm",
    modelID: "meta-llama/Llama-3.1-8B-Instruct",
    storageSize: "30Gi",
    memory: "24Gi",
    cpu: "4",
    gpu: 1,
    contextSize: 8192,
    description: "16 GB VRAM · 8K context · general purpose",
  },
  {
    label: "Qwen 2.5 7B Instruct",
    name: "qwen-2-5-7b-vllm",
    modelID: "Qwen/Qwen2.5-7B-Instruct",
    storageSize: "30Gi",
    memory: "20Gi",
    cpu: "4",
    gpu: 1,
    contextSize: 8192,
    description: "14 GB VRAM · 8K context · strong reasoning",
  },
  {
    label: "Mistral 7B Instruct v0.3",
    name: "mistral-7b-vllm",
    modelID: "mistralai/Mistral-7B-Instruct-v0.3",
    storageSize: "30Gi",
    memory: "20Gi",
    cpu: "4",
    gpu: 1,
    contextSize: 8192,
    description: "14 GB VRAM · 8K context · fast inference",
  },
];

const TGI_PRESETS = [
  {
    label: "Llama 3.1 8B Instruct",
    name: "llama-3-1-8b-tgi",
    modelID: "meta-llama/Llama-3.1-8B-Instruct",
    storageSize: "30Gi",
    memory: "24Gi",
    cpu: "4",
    gpu: 1,
    contextSize: 8192,
    description: "16 GB VRAM · 8K context · general purpose",
  },
  {
    label: "Mistral 7B Instruct v0.3",
    name: "mistral-7b-tgi",
    modelID: "mistralai/Mistral-7B-Instruct-v0.3",
    storageSize: "30Gi",
    memory: "20Gi",
    cpu: "4",
    gpu: 1,
    contextSize: 8192,
    description: "14 GB VRAM · 8K context · fast inference",
  },
];

export function ModelsPage() {
  const { data, isLoading } = useModels();
  const deleteModel = useDeleteModel();
  const createModel = useCreateModel();
  const { data: clusterNodes } = useClusterNodes();
  const { data: namespaces } = useNamespaces();
  const [search, setSearch] = useState("");
  const [showCreate, setShowCreate] = useState(false);
  const [serverType, setServerType] = useState<ServerType>("llama-cpp");
  const [form, setForm] = useState({
    name: "",
    url: "",
    modelID: "",
    filename: "model.gguf",
    storageSize: "10Gi",
    gpu: 0,
    memory: "12Gi",
    cpu: "8",
    contextSize: 4096,
    args: "",
    node: "",
    placement: "auto" as "auto" | "manual",
    namespace: "sympozium-system",
    huggingFaceTokenSecret: "",
  });

  const readyNodes = (clusterNodes || []).filter((n) => n.ready);

  // Derive a model query for fitness lookup from the deploy form.
  const densityModelQuery =
    serverType === "llama-cpp"
      ? form.name || ""
      : form.modelID || form.name || "";
  const { data: densityData } = useDensityQuery(
    form.placement === "auto" ? densityModelQuery : "",
  );

  const filtered = (data || [])
    .filter((m) =>
      m.metadata.name.toLowerCase().includes(search.toLowerCase()),
    )
    .sort((a, b) => {
      const aTime = a.metadata.creationTimestamp || "";
      const bTime = b.metadata.creationTimestamp || "";
      return bTime.localeCompare(aTime);
    });

  const needsURL = serverType === "llama-cpp";
  const needsModelID = serverType === "vllm" || serverType === "tgi";
  const canDeploy =
    form.name &&
    (needsURL ? form.url : true) &&
    (needsModelID ? form.modelID : true) &&
    (serverType === "custom" ? true : true);

  function resetForm() {
    setForm({
      name: "",
      url: "",
      modelID: "",
      filename: "model.gguf",
      storageSize: "10Gi",
      gpu: 0,
      memory: "12Gi",
      cpu: "8",
      contextSize: 4096,
      args: "",
      node: "",
      placement: "auto",
      namespace: "sympozium-system",
      huggingFaceTokenSecret: "",
    });
  }

  function handleCreate() {
    const args = form.args
      .split(/\s+/)
      .filter((a) => a.length > 0);
    createModel.mutate(
      {
        name: form.name,
        serverType,
        url: needsURL ? form.url : undefined,
        modelID: needsModelID ? form.modelID : undefined,
        filename: needsURL ? form.filename : undefined,
        storageSize: form.storageSize,
        gpu: form.gpu,
        memory: form.memory,
        cpu: form.cpu,
        contextSize: form.contextSize,
        args: args.length > 0 ? args : undefined,
        placement: form.placement,
        namespace: form.namespace,
        huggingFaceTokenSecret: form.huggingFaceTokenSecret || undefined,
        nodeSelector:
          form.placement === "manual" && form.node
            ? { "kubernetes.io/hostname": form.node }
            : undefined,
      },
      {
        onSuccess: () => {
          setShowCreate(false);
          resetForm();
        },
      },
    );
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold">Models</h1>
          <p className="text-sm text-muted-foreground">
            Deploy and manage local LLM inference models
          </p>
        </div>
        <Button
          size="sm"
          className="bg-gradient-to-r from-blue-500 to-purple-600 hover:from-blue-600 hover:to-purple-700 text-white border-0"
          onClick={() => setShowCreate(true)}
        >
          <Plus className="mr-2 h-4 w-4" /> Deploy Model
        </Button>
      </div>

      <Input
        placeholder="Search models..."
        value={search}
        onChange={(e) => setSearch(e.target.value)}
        className="max-w-sm"
      />

      {isLoading ? (
        <div className="space-y-2">
          {Array.from({ length: 3 }).map((_, i) => (
            <Skeleton key={i} className="h-12 w-full" />
          ))}
        </div>
      ) : filtered.length === 0 ? (
        <div className="py-12 text-center space-y-3">
          <Cpu className="mx-auto h-12 w-12 text-muted-foreground/30" />
          <p className="text-muted-foreground">
            {search ? "No models match your search" : "No models deployed yet"}
          </p>
          {!search && (
            <p className="text-xs text-muted-foreground/60">
              Deploy a model to run local inference in your cluster
            </p>
          )}
        </div>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>Namespace</TableHead>
              <TableHead>Status</TableHead>
              <TableHead>GPU</TableHead>
              <TableHead>Endpoint</TableHead>
              <TableHead>Age</TableHead>
              <TableHead className="w-12" />
            </TableRow>
          </TableHeader>
          <TableBody>
            {filtered.map((model) => (
              <TableRow key={model.metadata.name}>
                <TableCell className="font-mono text-sm">
                  <Link
                    to={`/models/${model.metadata.name}?namespace=${model.metadata.namespace}`}
                    className="hover:text-primary flex items-center gap-1"
                  >
                    {model.metadata.name}
                    <ExternalLink className="h-3 w-3 opacity-50" />
                  </Link>
                </TableCell>
                <TableCell className="text-xs text-muted-foreground">
                  {model.metadata.namespace}
                </TableCell>
                <TableCell>
                  <StatusBadge phase={model.status?.phase} />
                </TableCell>
                <TableCell className="text-sm">
                  {model.spec.resources?.gpu ?? 1}
                </TableCell>
                <TableCell className="text-xs font-mono text-muted-foreground max-w-[300px] truncate">
                  {model.status?.endpoint || "--"}
                </TableCell>
                <TableCell className="text-sm text-muted-foreground">
                  {formatAge(model.metadata.creationTimestamp)}
                </TableCell>
                <TableCell>
                  <Button
                    variant="ghost"
                    size="icon"
                    onClick={() =>
                      deleteModel.mutate({
                        name: model.metadata.name,
                        namespace: model.metadata.namespace,
                      })
                    }
                    disabled={deleteModel.isPending}
                    title="Delete model"
                  >
                    <Trash2 className="h-4 w-4 text-destructive" />
                  </Button>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}

      {/* Deploy Model Dialog */}
      <Dialog open={showCreate} onOpenChange={setShowCreate}>
        <DialogContent className="max-w-lg max-h-[85vh] overflow-y-auto">
          <DialogHeader>
            <DialogTitle>Deploy Model</DialogTitle>
          </DialogHeader>
          <div className="space-y-4">
            {/* Server type tabs */}
            <div className="flex gap-1 rounded-lg bg-muted/50 p-1">
              {SERVER_TYPES.map((st) => (
                <button
                  key={st.value}
                  type="button"
                  onClick={() => {
                    setServerType(st.value);
                    resetForm();
                  }}
                  className={cn(
                    "flex-1 rounded-md px-3 py-1.5 text-xs font-medium transition-colors",
                    serverType === st.value
                      ? "bg-background text-foreground shadow-sm"
                      : "text-muted-foreground hover:text-foreground",
                  )}
                >
                  {st.label}
                </button>
              ))}
            </div>

            {/* Preset selector (not shown for custom) */}
            {serverType !== "custom" && (
              <div className="space-y-2">
                <Label>Quick Start</Label>
                <div className="grid grid-cols-2 gap-2">
                  {(serverType === "llama-cpp"
                    ? LLAMACPP_PRESETS
                    : serverType === "vllm"
                      ? VLLM_PRESETS
                      : TGI_PRESETS
                  ).map((preset) => (
                    <button
                      key={preset.name}
                      type="button"
                      onClick={() =>
                        setForm({
                          ...form,
                          name: preset.name,
                          url: "url" in preset ? (preset as { url: string }).url : "",
                          modelID: "modelID" in preset ? (preset as { modelID: string }).modelID : "",
                          storageSize: preset.storageSize,
                          memory: preset.memory,
                          cpu: preset.cpu,
                          gpu: preset.gpu,
                          contextSize: preset.contextSize,
                        })
                      }
                      className={cn(
                        "rounded-md border px-3 py-2 text-left text-xs transition-colors",
                        form.name === preset.name
                          ? "border-blue-500/40 bg-blue-500/15 text-blue-300"
                          : "border-border/50 hover:bg-white/5",
                      )}
                    >
                      <div className="font-medium">{preset.label}</div>
                      <div className="text-muted-foreground">{preset.description}</div>
                    </button>
                  ))}
                </div>
              </div>
            )}

            <div className="grid grid-cols-2 gap-4">
              <div className="space-y-2">
                <Label>Name</Label>
                <Input
                  placeholder={serverType === "vllm" ? "llama-3-1-8b-vllm" : "llama-3.1-8b-q4"}
                  value={form.name}
                  onChange={(e) => setForm({ ...form, name: e.target.value })}
                />
              </div>
              <div className="space-y-2">
                <Label>Namespace</Label>
                <Select
                  value={form.namespace}
                  onValueChange={(v) => setForm({ ...form, namespace: v })}
                >
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {(namespaces || []).map((ns) => (
                      <SelectItem key={ns} value={ns}>
                        {ns}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
            </div>

            {/* Source: URL for llama-cpp, ModelID for vllm/tgi */}
            {needsURL && (
              <>
                <div className="space-y-2">
                  <Label>GGUF Download URL</Label>
                  <Input
                    placeholder="https://huggingface.co/..."
                    value={form.url}
                    onChange={(e) => setForm({ ...form, url: e.target.value })}
                  />
                </div>
                <div className="grid grid-cols-2 gap-4">
                  <div className="space-y-2">
                    <Label>Filename</Label>
                    <Input
                      value={form.filename}
                      onChange={(e) =>
                        setForm({ ...form, filename: e.target.value })
                      }
                    />
                  </div>
                  <div className="space-y-2">
                    <Label>Storage Size</Label>
                    <Input
                      value={form.storageSize}
                      onChange={(e) =>
                        setForm({ ...form, storageSize: e.target.value })
                      }
                    />
                  </div>
                </div>
              </>
            )}
            {needsModelID && (
              <>
                <div className="space-y-2">
                  <Label>HuggingFace Model ID</Label>
                  <Input
                    placeholder="meta-llama/Llama-3.1-8B-Instruct"
                    value={form.modelID}
                    onChange={(e) => setForm({ ...form, modelID: e.target.value })}
                  />
                  <p className="text-xs text-muted-foreground">
                    The model will be pulled from HuggingFace at container startup
                  </p>
                </div>
                <div className="grid grid-cols-2 gap-4">
                  <div className="space-y-2">
                    <Label>HF Token Secret</Label>
                    <Input
                      placeholder="hf-token (optional)"
                      value={form.huggingFaceTokenSecret}
                      onChange={(e) =>
                        setForm({ ...form, huggingFaceTokenSecret: e.target.value })
                      }
                    />
                    <p className="text-xs text-muted-foreground">
                      K8s Secret name for gated models (Llama, Mistral)
                    </p>
                  </div>
                  <div className="space-y-2">
                    <Label>Storage Size (HF cache)</Label>
                    <Input
                      value={form.storageSize}
                      onChange={(e) =>
                        setForm({ ...form, storageSize: e.target.value })
                      }
                    />
                  </div>
                </div>
              </>
            )}
            {serverType === "custom" && (
              <div className="space-y-2">
                <Label>Storage Size</Label>
                <Input
                  value={form.storageSize}
                  onChange={(e) =>
                    setForm({ ...form, storageSize: e.target.value })
                  }
                />
              </div>
            )}

            <div className="grid grid-cols-2 gap-4">
              <div className="space-y-2">
                <Label>GPU</Label>
                <Input
                  type="number"
                  min={0}
                  value={form.gpu}
                  onChange={(e) =>
                    setForm({ ...form, gpu: parseInt(e.target.value) || 0 })
                  }
                />
              </div>
              <div className="space-y-2">
                <Label>Context Size</Label>
                <Input
                  type="number"
                  min={512}
                  step={512}
                  value={form.contextSize}
                  onChange={(e) =>
                    setForm({
                      ...form,
                      contextSize: parseInt(e.target.value) || 4096,
                    })
                  }
                />
              </div>
            </div>
            <div className="grid grid-cols-2 gap-4">
              <div className="space-y-2">
                <Label>Memory</Label>
                <Input
                  value={form.memory}
                  onChange={(e) =>
                    setForm({ ...form, memory: e.target.value })
                  }
                />
              </div>
              <div className="space-y-2">
                <Label>CPU</Label>
                <Input
                  value={form.cpu}
                  onChange={(e) => setForm({ ...form, cpu: e.target.value })}
                />
              </div>
            </div>
            <div className="space-y-2">
              <Label>Extra Args</Label>
              <Input
                placeholder={
                  serverType === "vllm"
                    ? "--dtype auto --gpu-memory-utilization 0.9"
                    : serverType === "tgi"
                      ? "--quantize awq --max-batch-prefill-tokens 4096"
                      : "--n-gpu-layers 99"
                }
                value={form.args}
                onChange={(e) => setForm({ ...form, args: e.target.value })}
              />
              <p className="text-xs text-muted-foreground">
                Additional {serverType === "vllm" ? "vLLM" : serverType === "tgi" ? "TGI" : "inference server"} arguments (space-separated)
              </p>
            </div>
            <div className="space-y-2">
              <Label>Node Placement</Label>
              <Select
                value={form.placement}
                onValueChange={(v) =>
                  setForm({
                    ...form,
                    placement: v as "auto" | "manual",
                    node: "",
                  })
                }
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="auto">Auto (recommended)</SelectItem>
                  <SelectItem value="manual">Manual</SelectItem>
                </SelectContent>
              </Select>
              <p className="text-xs text-muted-foreground">
                {form.placement === "auto"
                  ? "llmfit will select the best fit for this model using cached fitness data"
                  : "Pin the inference server to a specific node"}
              </p>
              {form.placement === "auto" &&
                densityData?.rankedNodes &&
                densityData.rankedNodes.length > 0 && (
                  <div className="mt-2 space-y-1">
                    <p className="text-xs font-medium text-muted-foreground">
                      Node density preview:
                    </p>
                    {densityData.rankedNodes.slice(0, 3).map((r, i) => (
                      <div
                        key={`${r.nodeName}-${i}`}
                        className="flex items-center gap-2 text-xs"
                      >
                        <span className="font-mono">{r.nodeName}</span>
                        <Badge
                          variant="outline"
                          className={
                            r.fitLevel === "perfect"
                              ? "bg-green-500/15 text-green-700 border-green-500/30"
                              : r.fitLevel === "good"
                                ? "bg-yellow-500/15 text-yellow-700 border-yellow-500/30"
                                : "bg-orange-500/15 text-orange-700 border-orange-500/30"
                          }
                        >
                          {r.fitLevel}
                        </Badge>
                        <span className="text-muted-foreground">
                          score {Math.round(r.score)}
                        </span>
                        {i === 0 && (
                          <Badge variant="secondary" className="text-xs">
                            recommended
                          </Badge>
                        )}
                      </div>
                    ))}
                  </div>
                )}
            </div>
            {form.placement === "manual" && readyNodes.length > 1 && (
              <div className="space-y-2">
                <Label>Target Node</Label>
                <Select
                  value={form.node}
                  onValueChange={(v) =>
                    setForm({ ...form, node: v === "__any__" ? "" : v })
                  }
                >
                  <SelectTrigger>
                    <SelectValue placeholder="Any node (default)" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="__any__">Any node</SelectItem>
                    {readyNodes.map((n) => (
                      <SelectItem key={n.name} value={n.name}>
                        {n.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
            )}
          </div>
          <div className="flex justify-end gap-2 pt-2">
            <Button variant="outline" onClick={() => setShowCreate(false)}>
              Cancel
            </Button>
            <Button
              onClick={handleCreate}
              disabled={!canDeploy || createModel.isPending}
            >
              {createModel.isPending ? "Deploying..." : "Deploy"}
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    </div>
  );
}
