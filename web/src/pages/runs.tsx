import { useState } from "react";
import { Link } from "react-router-dom";
import { useRuns, useDeleteRun, useCreateRun, useInstances } from "@/hooks/use-api";
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
import { Plus, Trash2, ExternalLink } from "lucide-react";
import { formatAge, truncate } from "@/lib/utils";

export function RunsPage() {
  const { data, isLoading } = useRuns();
  const instances = useInstances();
  const deleteRun = useDeleteRun();
  const createRun = useCreateRun();
  const [open, setOpen] = useState(false);
  const [search, setSearch] = useState("");
  const [form, setForm] = useState({
    instanceRef: "",
    task: "",
    model: "",
    timeout: "5m",
  });

  const sorted = (data || []).sort(
    (a, b) =>
      new Date(b.metadata.creationTimestamp || "").getTime() -
      new Date(a.metadata.creationTimestamp || "").getTime()
  );

  const filtered = sorted.filter(
    (r) =>
      r.metadata.name.toLowerCase().includes(search.toLowerCase()) ||
      r.spec.instanceRef.toLowerCase().includes(search.toLowerCase()) ||
      r.spec.task.toLowerCase().includes(search.toLowerCase())
  );

  const handleCreate = () => {
    createRun.mutate(form, {
      onSuccess: () => {
        setOpen(false);
        setForm({ instanceRef: "", task: "", model: "", timeout: "5m" });
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
            <Button size="sm" className="bg-gradient-to-r from-indigo-500 to-purple-600 hover:from-indigo-600 hover:to-purple-700 text-white border-0">
              <Plus className="mr-2 h-4 w-4" /> New Run
            </Button>
          </DialogTrigger>
          <DialogContent>
            <DialogHeader>
              <DialogTitle>Create Run</DialogTitle>
              <DialogDescription>
                Task an agent instance to perform work.
              </DialogDescription>
            </DialogHeader>
            <div className="space-y-4 pt-2">
              <div className="space-y-2">
                <Label>Instance</Label>
                <Select
                  value={form.instanceRef}
                  onValueChange={(v) =>
                    setForm({ ...form, instanceRef: v })
                  }
                >
                  <SelectTrigger>
                    <SelectValue placeholder="Select instance" />
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
              <div className="grid grid-cols-2 gap-4">
                <div className="space-y-2">
                  <Label>Model (optional)</Label>
                  <Input
                    value={form.model}
                    onChange={(e) =>
                      setForm({ ...form, model: e.target.value })
                    }
                    placeholder="gpt-4o"
                  />
                </div>
                <div className="space-y-2">
                  <Label>Timeout</Label>
                  <Input
                    value={form.timeout}
                    onChange={(e) =>
                      setForm({ ...form, timeout: e.target.value })
                    }
                    placeholder="5m"
                  />
                </div>
              </div>
              <Button
                className="w-full bg-gradient-to-r from-indigo-500 to-purple-600 hover:from-indigo-600 hover:to-purple-700 text-white border-0"
                onClick={handleCreate}
                disabled={
                  !form.instanceRef || !form.task || createRun.isPending
                }
              >
                {createRun.isPending ? "Creating…" : "Create Run"}
              </Button>
            </div>
          </DialogContent>
        </Dialog>
      </div>

      <Input
        placeholder="Search runs…"
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
          No runs found
        </p>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>Instance</TableHead>
              <TableHead>Task</TableHead>
              <TableHead>Phase</TableHead>
              <TableHead>Tokens</TableHead>
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
                    {truncate(run.metadata.name, 32)}
                    <ExternalLink className="h-3 w-3 opacity-50" />
                  </Link>
                </TableCell>
                <TableCell className="text-sm">
                  <Link to={`/instances/${run.spec.instanceRef}`} className="hover:text-primary">
                    {run.spec.instanceRef}
                  </Link>
                </TableCell>
                <TableCell className="max-w-xs text-sm text-muted-foreground">
                  {truncate(run.spec.task, 60)}
                </TableCell>
                <TableCell>
                  <StatusBadge phase={run.status?.phase} />
                </TableCell>
                <TableCell className="text-xs text-muted-foreground">
                  {run.status?.tokenUsage
                    ? `${run.status.tokenUsage.totalTokens.toLocaleString()}`
                    : "—"}
                </TableCell>
                <TableCell className="text-sm text-muted-foreground">
                  {formatAge(run.metadata.creationTimestamp)}
                </TableCell>
                <TableCell>
                  <Button
                    variant="ghost"
                    size="icon"
                    onClick={() => deleteRun.mutate(run.metadata.name)}
                    disabled={deleteRun.isPending}
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
