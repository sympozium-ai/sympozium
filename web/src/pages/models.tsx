import { useState } from "react";
import { Link } from "react-router-dom";
import {
  useModels,
  useDeleteModel,
  useCreateModel,
  useClusterNodes,
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
import { Plus, Trash2, ExternalLink, Cpu } from "lucide-react";
import { formatAge, cn } from "@/lib/utils";

const MODEL_PRESETS = [
  {
    label: "Qwen3 8B (Q4)",
    name: "qwen3-8b-q4",
    url: "https://huggingface.co/Qwen/Qwen3-8B-GGUF/resolve/main/Qwen3-8B-Q4_K_M.gguf",
    storageSize: "8Gi",
    memory: "12Gi",
    cpu: "8",
    contextSize: 8192,
    description: "5 GB \u00b7 8K context \u00b7 strong reasoning",
  },
  {
    label: "Qwen3.5 9B (Q4)",
    name: "qwen3-5-9b-q4",
    url: "https://huggingface.co/unsloth/Qwen3.5-9B-GGUF/resolve/main/Qwen3.5-9B-Q4_K_M.gguf",
    storageSize: "8Gi",
    memory: "12Gi",
    cpu: "8",
    contextSize: 8192,
    description: "5.7 GB \u00b7 8K context \u00b7 latest Qwen",
  },
  {
    label: "Phi-3 Mini 4K (Q4)",
    name: "phi-3-mini-q4",
    url: "https://huggingface.co/microsoft/Phi-3-mini-4k-instruct-gguf/resolve/main/Phi-3-mini-4k-instruct-q4.gguf",
    storageSize: "4Gi",
    memory: "6Gi",
    cpu: "6",
    contextSize: 4096,
    description: "2.2 GB \u00b7 4K context \u00b7 fast & lightweight",
  },
  {
    label: "Qwen3 0.6B (Q8)",
    name: "qwen3-0-6b-q8",
    url: "https://huggingface.co/Qwen/Qwen3-0.6B-GGUF/resolve/main/Qwen3-0.6B-Q8_0.gguf",
    storageSize: "2Gi",
    memory: "4Gi",
    cpu: "4",
    contextSize: 4096,
    description: "0.6 GB \u00b7 4K context \u00b7 tiny, for testing",
  },
];

export function ModelsPage() {
  const { data, isLoading } = useModels();
  const deleteModel = useDeleteModel();
  const createModel = useCreateModel();
  const { data: clusterNodes } = useClusterNodes();
  const [search, setSearch] = useState("");
  const [showCreate, setShowCreate] = useState(false);
  const [form, setForm] = useState({
    name: "",
    url: "",
    filename: "model.gguf",
    storageSize: "10Gi",
    gpu: 0,
    memory: "12Gi",
    cpu: "8",
    contextSize: 4096,
    args: "",
    node: "",
  });

  const readyNodes = (clusterNodes || []).filter((n) => n.ready);

  const filtered = (data || [])
    .filter((m) =>
      m.metadata.name.toLowerCase().includes(search.toLowerCase()),
    )
    .sort((a, b) => {
      const aTime = a.metadata.creationTimestamp || "";
      const bTime = b.metadata.creationTimestamp || "";
      return bTime.localeCompare(aTime);
    });

  function handleCreate() {
    const args = form.args
      .split(/\s+/)
      .filter((a) => a.length > 0);
    createModel.mutate(
      {
        name: form.name,
        url: form.url,
        filename: form.filename,
        storageSize: form.storageSize,
        gpu: form.gpu,
        memory: form.memory,
        cpu: form.cpu,
        contextSize: form.contextSize,
        args: args.length > 0 ? args : undefined,
        nodeSelector: form.node
          ? { "kubernetes.io/hostname": form.node }
          : undefined,
      },
      {
        onSuccess: () => {
          setShowCreate(false);
          setForm({
            name: "",
            url: "",
            filename: "model.gguf",
            storageSize: "10Gi",
            gpu: 0,
            memory: "12Gi",
            cpu: "8",
            contextSize: 4096,
            args: "",
            node: "",
          });
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
              Deploy a GGUF model to run local inference in your cluster
            </p>
          )}
        </div>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
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
                    to={`/models/${model.metadata.name}`}
                    className="hover:text-primary flex items-center gap-1"
                  >
                    {model.metadata.name}
                    <ExternalLink className="h-3 w-3 opacity-50" />
                  </Link>
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
                    onClick={() => deleteModel.mutate(model.metadata.name)}
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
        <DialogContent className="max-w-lg">
          <DialogHeader>
            <DialogTitle>Deploy Model</DialogTitle>
          </DialogHeader>
          <div className="space-y-4">
            {/* Preset selector */}
            <div className="space-y-2">
              <Label>Quick Start</Label>
              <div className="grid grid-cols-2 gap-2">
                {MODEL_PRESETS.map((preset) => (
                  <button
                    key={preset.name}
                    type="button"
                    onClick={() =>
                      setForm({
                        ...form,
                        name: preset.name,
                        url: preset.url,
                        storageSize: preset.storageSize,
                        memory: preset.memory,
                        cpu: preset.cpu,
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
            <div className="space-y-2">
              <Label>Name</Label>
              <Input
                placeholder="llama-3.1-8b-q4"
                value={form.name}
                onChange={(e) => setForm({ ...form, name: e.target.value })}
              />
            </div>
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
                placeholder="--ctx-size 8192 --n-gpu-layers 99"
                value={form.args}
                onChange={(e) => setForm({ ...form, args: e.target.value })}
              />
              <p className="text-xs text-muted-foreground">
                Additional llama-server arguments (space-separated)
              </p>
            </div>
            {readyNodes.length > 1 && (
              <div className="space-y-2">
                <Label>Node Placement</Label>
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
                <p className="text-xs text-muted-foreground">
                  Pin the inference server to a specific node
                </p>
              </div>
            )}
          </div>
          <div className="flex justify-end gap-2 pt-2">
            <Button variant="outline" onClick={() => setShowCreate(false)}>
              Cancel
            </Button>
            <Button
              onClick={handleCreate}
              disabled={
                !form.name || !form.url || createModel.isPending
              }
            >
              {createModel.isPending ? "Deploying..." : "Deploy"}
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    </div>
  );
}
