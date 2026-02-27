import { useState } from "react";
import { Link } from "react-router-dom";
import { useInstances, useDeleteInstance, useCreateInstance } from "@/hooks/use-api";
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
import { Plus, Trash2, ExternalLink } from "lucide-react";
import { formatAge } from "@/lib/utils";

export function InstancesPage() {
  const { data, isLoading } = useInstances();
  const deleteInstance = useDeleteInstance();
  const createInstance = useCreateInstance();
  const [open, setOpen] = useState(false);
  const [search, setSearch] = useState("");
  const [form, setForm] = useState({
    name: "",
    provider: "openai",
    model: "gpt-4o",
    baseURL: "",
    secretName: "",
  });

  const filtered = (data || []).filter((inst) =>
    inst.metadata.name.toLowerCase().includes(search.toLowerCase())
  );

  const handleCreate = () => {
    createInstance.mutate(form, {
      onSuccess: () => {
        setOpen(false);
        setForm({ name: "", provider: "openai", model: "gpt-4o", baseURL: "", secretName: "" });
      },
    });
  };

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold">Instances</h1>
          <p className="text-sm text-muted-foreground">
            Manage SympoziumInstances — each represents an agent identity
          </p>
        </div>
        <Dialog open={open} onOpenChange={setOpen}>
          <DialogTrigger asChild>
            <Button size="sm" className="bg-gradient-to-r from-indigo-500 to-purple-600 hover:from-indigo-600 hover:to-purple-700 text-white border-0">
              <Plus className="mr-2 h-4 w-4" /> Create Instance
            </Button>
          </DialogTrigger>
          <DialogContent>
            <DialogHeader>
              <DialogTitle>Create Instance</DialogTitle>
              <DialogDescription>
                Create a new SympoziumInstance with provider configuration.
              </DialogDescription>
            </DialogHeader>
            <div className="space-y-4 pt-2">
              <div className="space-y-2">
                <Label>Name</Label>
                <Input
                  value={form.name}
                  onChange={(e) => setForm({ ...form, name: e.target.value })}
                  placeholder="my-agent"
                />
              </div>
              <div className="space-y-2">
                <Label>Provider</Label>
                <Select
                  value={form.provider}
                  onValueChange={(v) => setForm({ ...form, provider: v })}
                >
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="openai">OpenAI</SelectItem>
                    <SelectItem value="anthropic">Anthropic</SelectItem>
                    <SelectItem value="azure-openai">Azure OpenAI</SelectItem>
                    <SelectItem value="ollama">Ollama</SelectItem>
                    <SelectItem value="custom">Custom</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              <div className="space-y-2">
                <Label>Model</Label>
                <Input
                  value={form.model}
                  onChange={(e) => setForm({ ...form, model: e.target.value })}
                  placeholder="gpt-4o"
                />
              </div>
              <div className="space-y-2">
                <Label>Base URL (optional)</Label>
                <Input
                  value={form.baseURL}
                  onChange={(e) => setForm({ ...form, baseURL: e.target.value })}
                  placeholder="https://api.openai.com/v1"
                />
              </div>
              <div className="space-y-2">
                <Label>Secret Name (optional)</Label>
                <Input
                  value={form.secretName}
                  onChange={(e) => setForm({ ...form, secretName: e.target.value })}
                  placeholder="my-api-key"
                />
              </div>
              <Button
                className="w-full bg-gradient-to-r from-indigo-500 to-purple-600 hover:from-indigo-600 hover:to-purple-700 text-white border-0"
                onClick={handleCreate}
                disabled={!form.name || createInstance.isPending}
              >
                {createInstance.isPending ? "Creating…" : "Create"}
              </Button>
            </div>
          </DialogContent>
        </Dialog>
      </div>

      <Input
        placeholder="Search instances…"
        value={search}
        onChange={(e) => setSearch(e.target.value)}
        className="max-w-sm"
      />

      {isLoading ? (
        <div className="space-y-2">
          {Array.from({ length: 5 }).map((_, i) => (
            <Skeleton key={i} className="h-12 w-full" />
          ))}
        </div>
      ) : filtered.length === 0 ? (
        <p className="py-8 text-center text-muted-foreground">
          No instances found
        </p>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>Provider</TableHead>
              <TableHead>Model</TableHead>
              <TableHead>Skills</TableHead>
              <TableHead>Channels</TableHead>
              <TableHead>Phase</TableHead>
              <TableHead>Runs</TableHead>
              <TableHead>Age</TableHead>
              <TableHead className="w-20" />
            </TableRow>
          </TableHeader>
          <TableBody>
            {filtered.map((inst) => (
              <TableRow key={inst.metadata.name}>
                <TableCell className="font-mono text-sm">
                  <Link
                    to={`/instances/${inst.metadata.name}`}
                    className="hover:text-primary flex items-center gap-1"
                  >
                    {inst.metadata.name}
                    <ExternalLink className="h-3 w-3 opacity-50" />
                  </Link>
                </TableCell>
                <TableCell className="text-sm">
                  {inst.spec.authRefs?.[0]?.provider || "—"}
                </TableCell>
                <TableCell className="text-sm">
                  {inst.spec.agents?.default?.model || "—"}
                </TableCell>
                <TableCell className="text-sm text-muted-foreground">
                  {inst.spec.skills?.length || 0}
                </TableCell>
                <TableCell className="text-sm text-muted-foreground">
                  {inst.spec.channels?.length || 0}
                </TableCell>
                <TableCell>
                  <StatusBadge phase={inst.status?.phase} />
                </TableCell>
                <TableCell className="text-sm">
                  {inst.status?.totalAgentRuns ?? 0}
                </TableCell>
                <TableCell className="text-sm text-muted-foreground">
                  {formatAge(inst.metadata.creationTimestamp)}
                </TableCell>
                <TableCell>
                  <Button
                    variant="ghost"
                    size="icon"
                    onClick={() => deleteInstance.mutate(inst.metadata.name)}
                    disabled={deleteInstance.isPending}
                    title="Delete"
                  >
                    <Trash2 className="h-4 w-4 text-destructive" />
                  </Button>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </div>
  );
}
