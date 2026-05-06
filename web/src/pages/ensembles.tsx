import { useState } from "react";
import { Link } from "react-router-dom";
import {
  useEnsembles,
  useActivateEnsemble,
  useDeleteEnsemble,
  useInstallDefaultEnsembles,
  useSkills,
} from "@/hooks/use-api";
import { StatusBadge } from "@/components/status-badge";
import {
  OnboardingWizard,
  type WizardResult,
} from "@/components/onboarding-wizard";
import { WhatsAppQRModal } from "@/components/whatsapp-qr-modal";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from "@/components/ui/dialog";
import {
  Table,
  TableHeader,
  TableRow,
  TableHead,
  TableBody,
  TableCell,
} from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { Input } from "@/components/ui/input";
import {
  ExternalLink,
  Sparkles,
  PowerOff,
  Download,
  LayoutGrid,
  Workflow,
  Plus,
  Trash2,
} from "lucide-react";
import { formatAge } from "@/lib/utils";
import type { Ensemble } from "@/lib/api";
import { GlobalEnsembleCanvas } from "@/components/ensemble-canvas";

export function EnsemblesPage() {
  const { data, isLoading } = useEnsembles();
  const { data: skillPacks } = useSkills();
  const activatePack = useActivateEnsemble();
  const installDefaults = useInstallDefaultEnsembles();
  const [search, setSearch] = useState("");
  const [view, setView] = useState<"table" | "canvas">("table");

  // Wizard state
  const [wizardOpen, setWizardOpen] = useState(false);
  const [wizardPack, setWizardPack] = useState<Ensemble | null>(null);
  const [whatsAppPack, setWhatsAppPack] = useState<string | null>(null);

  // Disable / delete confirmation state
  const [disablePack, setDisablePack] = useState<Ensemble | null>(null);
  const [deletePack, setDeletePack] = useState<Ensemble | null>(null);
  const deleteEnsemble = useDeleteEnsemble();

  const filtered = (data || [])
    .filter((p) => p.metadata.name.toLowerCase().includes(search.toLowerCase()))
    .sort((a, b) => a.metadata.name.localeCompare(b.metadata.name));

  function openWizard(pack: Ensemble) {
    setWizardPack(pack);
    setWizardOpen(true);
  }

  function closeWizard() {
    setWizardOpen(false);
    setWizardPack(null);
  }

  function handleComplete(result: WizardResult) {
    if (!wizardPack) return;

    // Build skillParams from inline skill configs
    let skillParams: Record<string, Record<string, string>> | undefined;
    if (result.skills.includes("github-gitops") && result.githubRepo) {
      skillParams = {
        ...skillParams,
        "github-gitops": { repo: result.githubRepo },
      };
    }

    activatePack.mutate(
      {
        name: wizardPack.metadata.name,
        enabled: true,
        provider: result.provider,
        secretName: result.secretName || undefined,
        apiKey: result.apiKey || undefined,
        awsRegion: result.awsRegion || undefined,
        awsAccessKeyId: result.awsAccessKeyId || undefined,
        awsSecretAccessKey: result.awsSecretAccessKey || undefined,
        awsSessionToken: result.awsSessionToken || undefined,
        model: result.model,
        baseURL: result.baseURL || undefined,
        channels: result.channels.length > 0 ? result.channels : undefined,
        channelConfigs:
          Object.keys(result.channelConfigs).length > 0
            ? result.channelConfigs
            : undefined,
        heartbeatInterval: result.heartbeatInterval || undefined,
        skillParams,
        githubToken: result.githubToken || undefined,
        agentSandbox: result.agentSandboxEnabled
          ? {
              enabled: true,
              runtimeClass: result.agentSandboxRuntimeClass || "gvisor",
            }
          : undefined,
      },
      {
        onSuccess: () => {
          closeWizard();
          if (result.channels.includes("whatsapp")) {
            setWhatsAppPack(wizardPack.metadata.name);
          }
        },
      },
    );
  }

  function confirmDisable(pack: Ensemble) {
    setDisablePack(pack);
  }

  function handleDisable() {
    if (!disablePack) return;
    activatePack.mutate(
      {
        name: disablePack.metadata.name,
        enabled: false,
      },
      { onSuccess: () => setDisablePack(null) },
    );
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold">Ensembles</h1>
          <p className="text-sm text-muted-foreground">
            Coordinated agent teams with workflows, shared memory, and
            delegation
          </p>
        </div>
        <div className="flex items-center gap-2">
          <div className="flex rounded-md border border-border/50 overflow-hidden">
            <button
              onClick={() => setView("table")}
              className={`px-2.5 py-1.5 text-xs font-medium transition-colors ${view === "table" ? "bg-white/10 text-foreground" : "text-muted-foreground hover:text-foreground hover:bg-white/5"}`}
              title="Table view"
            >
              <LayoutGrid className="h-3.5 w-3.5" />
            </button>
            <button
              onClick={() => setView("canvas")}
              className={`px-2.5 py-1.5 text-xs font-medium transition-colors border-l border-border/50 ${view === "canvas" ? "bg-white/10 text-foreground" : "text-muted-foreground hover:text-foreground hover:bg-white/5"}`}
              title="Canvas view"
            >
              <Workflow className="h-3.5 w-3.5" />
            </button>
          </div>
          <Link to="/ensembles/new">
            <Button variant="default" className="gap-2">
              <Plus className="h-4 w-4" />
              New Ensemble
            </Button>
          </Link>
          <Button
            variant="outline"
            className="gap-2"
            onClick={() => installDefaults.mutate()}
            disabled={installDefaults.isPending}
          >
            <Download className="h-4 w-4" />
            Install Defaults
          </Button>
        </div>
      </div>

      <Input
        placeholder="Search ensembles…"
        value={search}
        onChange={(e) => setSearch(e.target.value)}
        className="max-w-sm"
      />

      {view === "canvas" && !isLoading && <GlobalEnsembleCanvas />}

      {view === "table" && isLoading ? (
        <div className="space-y-2">
          {Array.from({ length: 3 }).map((_, i) => (
            <Skeleton key={i} className="h-12 w-full" />
          ))}
        </div>
      ) : view === "table" && filtered.length === 0 ? (
        <div className="py-12 text-center space-y-3">
          <p className="text-muted-foreground">
            {search ? "No ensembles match your search" : "No ensembles yet"}
          </p>
          {!search && (
            <p className="text-sm text-muted-foreground">
              Click{" "}
              <button
                onClick={() => installDefaults.mutate()}
                className="text-blue-400 hover:text-blue-300"
              >
                Install Defaults
              </button>{" "}
              to get started with pre-built ensembles.
            </p>
          )}
        </div>
      ) : view === "table" ? (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>Category</TableHead>
              <TableHead>Version</TableHead>
              <TableHead>Personas</TableHead>
              <TableHead>Installed</TableHead>
              <TableHead>Phase</TableHead>
              <TableHead>Enabled</TableHead>
              <TableHead>Age</TableHead>
              <TableHead className="w-36" />
            </TableRow>
          </TableHeader>
          <TableBody>
            {filtered.map((pack) => (
              <TableRow key={pack.metadata.name}>
                <TableCell className="font-mono text-sm">
                  <Link
                    to={`/ensembles/${pack.metadata.name}`}
                    className="hover:text-primary flex items-center gap-1"
                  >
                    {pack.metadata.name}
                    <ExternalLink className="h-3 w-3 opacity-50" />
                  </Link>
                </TableCell>
                <TableCell>
                  {pack.spec.category ? (
                    <Badge variant="outline" className="text-xs capitalize">
                      {pack.spec.category}
                    </Badge>
                  ) : (
                    "—"
                  )}
                </TableCell>
                <TableCell className="text-sm text-muted-foreground">
                  {pack.spec.version || "—"}
                </TableCell>
                <TableCell className="text-sm">
                  {pack.status?.agentConfigCount ?? pack.spec.agentConfigs?.length ?? 0}
                </TableCell>
                <TableCell className="text-sm">
                  {pack.status?.installedCount ?? 0}
                </TableCell>
                <TableCell>
                  <StatusBadge phase={pack.status?.phase} />
                </TableCell>
                <TableCell>
                  {pack.spec.enabled ? (
                    <Badge variant="default" className="text-xs">
                      Yes
                    </Badge>
                  ) : (
                    <Badge variant="secondary" className="text-xs">
                      No
                    </Badge>
                  )}
                </TableCell>
                <TableCell className="text-sm text-muted-foreground">
                  {formatAge(pack.metadata.creationTimestamp)}
                </TableCell>
                <TableCell>
                  <div className="flex items-center gap-1">
                    {!pack.spec.enabled ? (
                      <Button
                        size="sm"
                        variant="ghost"
                        className="h-7 gap-1 text-xs text-blue-400 hover:text-blue-300 hover:bg-blue-500/10"
                        onClick={() => openWizard(pack)}
                      >
                        <Sparkles className="h-3 w-3" />
                        Enable
                      </Button>
                    ) : (
                      <Button
                        size="sm"
                        variant="ghost"
                        className="h-7 gap-1 text-xs text-amber-400 hover:text-amber-300 hover:bg-amber-500/10"
                        onClick={() => confirmDisable(pack)}
                        disabled={activatePack.isPending}
                      >
                        <PowerOff className="h-3 w-3" />
                        Disable
                      </Button>
                    )}
                    <Button
                      size="sm"
                      variant="ghost"
                      className="h-7 gap-1 text-xs text-red-400 hover:text-red-300 hover:bg-red-500/10"
                      onClick={() => setDeletePack(pack)}
                    >
                      <Trash2 className="h-3 w-3" />
                    </Button>
                  </div>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      ) : null}

      {/* Shared onboarding wizard */}
      <OnboardingWizard
        open={wizardOpen}
        onClose={closeWizard}
        mode="persona"
        targetName={wizardPack?.metadata.name}
        agentConfigCount={wizardPack?.spec.agentConfigs?.length ?? 0}
        availableSkills={(skillPacks || []).map((s) => s.metadata.name)}
        defaults={{
          provider: wizardPack?.spec.authRefs?.[0]?.provider || "",
          secretName: wizardPack?.spec.authRefs?.[0]?.secret || "",
          model: wizardPack?.spec.agentConfigs?.[0]?.model || "",
          skills: Array.from(
            new Set(
              (wizardPack?.spec.agentConfigs || []).flatMap((p) => p.skills || []),
            ),
          ),
          channelConfigs: wizardPack?.spec.channelConfigs || {},
          channels:
            wizardPack?.spec.agentConfigs?.[0]?.channels ||
            Object.keys(wizardPack?.spec.channelConfigs || {}),
        }}
        onComplete={handleComplete}
        isPending={activatePack.isPending}
      />

      <WhatsAppQRModal
        open={!!whatsAppPack}
        onClose={() => setWhatsAppPack(null)}
        ensembleName={whatsAppPack || undefined}
      />

      {/* Disable confirmation dialog */}
      <Dialog
        open={!!disablePack}
        onOpenChange={(open) => !open && setDisablePack(null)}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Disable Ensemble</DialogTitle>
            <DialogDescription>
              This will disable <strong>{disablePack?.metadata.name}</strong>{" "}
              and remove all associated Instances, Schedules, and resources. The
              ensemble will remain available and can be re-enabled at any time.
            </DialogDescription>
          </DialogHeader>
          <div className="flex justify-end gap-2 pt-2">
            <Button variant="outline" onClick={() => setDisablePack(null)}>
              Cancel
            </Button>
            <Button
              variant="destructive"
              onClick={handleDisable}
              disabled={activatePack.isPending}
            >
              <PowerOff className="mr-1 h-3.5 w-3.5" />
              Disable
            </Button>
          </div>
        </DialogContent>
      </Dialog>

      {/* Delete confirmation dialog */}
      <Dialog
        open={!!deletePack}
        onOpenChange={(open) => !open && setDeletePack(null)}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete Ensemble</DialogTitle>
            <DialogDescription>
              This will permanently delete{" "}
              <strong>{deletePack?.metadata.name}</strong> and all associated
              agents, schedules, and shared memory. This action cannot be undone.
            </DialogDescription>
          </DialogHeader>
          <div className="flex justify-end gap-2 pt-2">
            <Button variant="outline" onClick={() => setDeletePack(null)}>
              Cancel
            </Button>
            <Button
              variant="destructive"
              onClick={() => {
                if (!deletePack) return;
                deleteEnsemble.mutate(deletePack.metadata.name, {
                  onSuccess: () => setDeletePack(null),
                });
              }}
              disabled={deleteEnsemble.isPending}
            >
              <Trash2 className="mr-1 h-3.5 w-3.5" />
              {deleteEnsemble.isPending ? "Deleting..." : "Delete"}
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    </div>
  );
}
