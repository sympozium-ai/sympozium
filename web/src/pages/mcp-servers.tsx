import { useState } from "react";
import { Link } from "react-router-dom";
import {
  useMcpServers,
  useCreateMcpServer,
  useDeleteMcpServer,
  useInstallDefaultMcpServers,
  useMcpServerAuthToken,
} from "@/hooks/use-api";
import {
  Table,
  TableHeader,
  TableRow,
  TableHead,
  TableBody,
  TableCell,
} from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
  DialogDescription,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Eye, Trash2, Plus, Download, KeyRound } from "lucide-react";
import { formatAge } from "@/lib/utils";

export function McpServersPage() {
  const { data, isLoading } = useMcpServers();
  const createMutation = useCreateMcpServer();
  const deleteMutation = useDeleteMcpServer();
  const installDefaults = useInstallDefaultMcpServers();
  const authTokenMutation = useMcpServerAuthToken();
  const [search, setSearch] = useState("");
  const [dialogOpen, setDialogOpen] = useState(false);
  const [tokenDialogOpen, setTokenDialogOpen] = useState(false);
  const [tokenTarget, setTokenTarget] = useState<string | null>(null);
  const [tokenValue, setTokenValue] = useState("");

  const [form, setForm] = useState({
    name: "",
    transportType: "http",
    toolsPrefix: "",
    url: "",
    image: "",
    timeout: "30",
  });

  const filtered = (data || [])
    .filter((s) => s.metadata.name.toLowerCase().includes(search.toLowerCase()))
    .sort((a, b) => a.metadata.name.localeCompare(b.metadata.name));

  const handleCreate = () => {
    createMutation.mutate(
      {
        name: form.name,
        transportType: form.transportType,
        toolsPrefix: form.toolsPrefix,
        url: form.url || undefined,
        image: form.image || undefined,
        timeout: parseInt(form.timeout) || 30,
      },
      {
        onSuccess: () => {
          setDialogOpen(false);
          setForm({
            name: "",
            transportType: "http",
            toolsPrefix: "",
            url: "",
            image: "",
            timeout: "30",
          });
        },
      },
    );
  };

  const handleSaveToken = () => {
    if (!tokenTarget || !tokenValue) return;
    authTokenMutation.mutate(
      { name: tokenTarget, token: tokenValue },
      {
        onSuccess: () => {
          setTokenDialogOpen(false);
          setTokenTarget(null);
          setTokenValue("");
        },
      },
    );
  };

  const openTokenDialog = (name: string) => {
    setTokenTarget(name);
    setTokenValue("");
    setTokenDialogOpen(true);
  };

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold">MCP Servers</h1>
          <p className="text-sm text-muted-foreground">
            Model Context Protocol servers — external tool providers for agent
            instances
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button
            variant="outline"
            className="gap-2"
            onClick={() => installDefaults.mutate()}
            disabled={installDefaults.isPending}
          >
            <Download className="h-4 w-4" />
            Install Defaults
          </Button>
          <Dialog open={dialogOpen} onOpenChange={setDialogOpen}>
            <DialogTrigger asChild>
              <Button size="sm">
                <Plus className="mr-2 h-4 w-4" />
                Create Server
              </Button>
            </DialogTrigger>
            <DialogContent>
              <DialogHeader>
                <DialogTitle>Create MCP Server</DialogTitle>
              </DialogHeader>
              <div className="space-y-4 pt-2">
                <div className="space-y-2">
                  <Label htmlFor="name">Name</Label>
                  <Input
                    id="name"
                    value={form.name}
                    onChange={(e) => setForm({ ...form, name: e.target.value })}
                    placeholder="my-mcp-server"
                  />
                </div>
                <div className="space-y-2">
                  <Label htmlFor="transport">Transport Type</Label>
                  <Select
                    value={form.transportType}
                    onValueChange={(v) =>
                      setForm({ ...form, transportType: v })
                    }
                  >
                    <SelectTrigger>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="http">HTTP</SelectItem>
                      <SelectItem value="stdio">stdio</SelectItem>
                    </SelectContent>
                  </Select>
                </div>
                <div className="space-y-2">
                  <Label htmlFor="prefix">Tools Prefix</Label>
                  <Input
                    id="prefix"
                    value={form.toolsPrefix}
                    onChange={(e) =>
                      setForm({ ...form, toolsPrefix: e.target.value })
                    }
                    placeholder="mytools"
                  />
                </div>
                <div className="space-y-2">
                  <Label htmlFor="url">URL (external server)</Label>
                  <Input
                    id="url"
                    value={form.url}
                    onChange={(e) => setForm({ ...form, url: e.target.value })}
                    placeholder="http://mcp-server:8080"
                  />
                </div>
                <div className="space-y-2">
                  <Label htmlFor="image">Image (managed deployment)</Label>
                  <Input
                    id="image"
                    value={form.image}
                    onChange={(e) =>
                      setForm({ ...form, image: e.target.value })
                    }
                    placeholder="ghcr.io/org/mcp-server:latest"
                  />
                </div>
                <div className="space-y-2">
                  <Label htmlFor="timeout">Timeout (seconds)</Label>
                  <Input
                    id="timeout"
                    type="number"
                    value={form.timeout}
                    onChange={(e) =>
                      setForm({ ...form, timeout: e.target.value })
                    }
                  />
                </div>
                <Button
                  onClick={handleCreate}
                  disabled={
                    !form.name || !form.toolsPrefix || createMutation.isPending
                  }
                  className="w-full"
                >
                  {createMutation.isPending ? "Creating..." : "Create"}
                </Button>
              </div>
            </DialogContent>
          </Dialog>
        </div>
      </div>

      <Input
        placeholder="Search MCP servers..."
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
        <div className="py-8 text-center">
          <p className="text-muted-foreground">No MCP servers found</p>
          {!search && (
            <p className="text-sm text-muted-foreground mt-1">
              Click{" "}
              <button
                onClick={() => installDefaults.mutate()}
                className="text-blue-400 hover:text-blue-300"
              >
                Install Defaults
              </button>{" "}
              to get started with pre-configured MCP servers.
            </p>
          )}
        </div>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>Transport</TableHead>
              <TableHead>Ready</TableHead>
              <TableHead>URL</TableHead>
              <TableHead>Tools</TableHead>
              <TableHead>Prefix</TableHead>
              <TableHead>Age</TableHead>
              <TableHead className="w-32" />
            </TableRow>
          </TableHeader>
          <TableBody>
            {filtered.map((mcp) => (
              <TableRow key={mcp.metadata.name}>
                <TableCell className="font-mono text-sm">
                  {mcp.metadata.name}
                </TableCell>
                <TableCell>
                  <Badge variant="outline" className="text-xs">
                    {mcp.spec.transportType}
                  </Badge>
                </TableCell>
                <TableCell>
                  {mcp.status?.ready ? (
                    <Badge variant="default" className="text-xs">
                      Ready
                    </Badge>
                  ) : (
                    <Badge variant="secondary" className="text-xs">
                      Not Ready
                    </Badge>
                  )}
                </TableCell>
                <TableCell className="text-sm text-muted-foreground font-mono max-w-48 truncate">
                  {mcp.status?.url || mcp.spec.url || "—"}
                </TableCell>
                <TableCell className="text-sm">
                  {mcp.status?.toolCount ?? 0}
                </TableCell>
                <TableCell className="text-sm font-mono">
                  {mcp.spec.toolsPrefix}
                </TableCell>
                <TableCell className="text-sm text-muted-foreground">
                  {formatAge(mcp.metadata.creationTimestamp)}
                </TableCell>
                <TableCell>
                  <div className="flex items-center gap-1">
                    <Button
                      variant="ghost"
                      size="icon"
                      title="Configure token"
                      onClick={() => openTokenDialog(mcp.metadata.name)}
                    >
                      <KeyRound className="h-4 w-4" />
                    </Button>
                    <Button variant="ghost" size="icon" asChild>
                      <Link to={`/mcp-servers/${mcp.metadata.name}`}>
                        <Eye className="h-4 w-4" />
                      </Link>
                    </Button>
                    <Button
                      variant="ghost"
                      size="icon"
                      onClick={() => deleteMutation.mutate(mcp.metadata.name)}
                      disabled={deleteMutation.isPending}
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

      {/* Token configuration dialog */}
      <Dialog open={tokenDialogOpen} onOpenChange={setTokenDialogOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Configure Token</DialogTitle>
            <DialogDescription>
              Set the authentication token for the{" "}
              <span className="font-mono font-semibold">{tokenTarget}</span> MCP
              server. This creates a Kubernetes Secret that is injected into the
              server pod.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-4 pt-2">
            <div className="space-y-2">
              <Label htmlFor="token">Personal Access Token</Label>
              <Input
                id="token"
                type="password"
                value={tokenValue}
                onChange={(e) => setTokenValue(e.target.value)}
                placeholder="ghp_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
              />
              <p className="text-xs text-muted-foreground">
                For the GitHub MCP server, use a GitHub Personal Access Token
                with appropriate scopes (repo, read:org, etc.).
              </p>
            </div>
            <Button
              onClick={handleSaveToken}
              disabled={!tokenValue || authTokenMutation.isPending}
              className="w-full"
            >
              {authTokenMutation.isPending ? "Saving..." : "Save Token"}
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    </div>
  );
}
