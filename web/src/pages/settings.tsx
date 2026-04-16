import { useState } from "react";
import {
  useCapabilities,
  useInstallAgentSandbox,
  useUninstallAgentSandbox,
} from "@/hooks/use-api";
import {
  Card,
  CardHeader,
  CardTitle,
  CardContent,
  CardDescription,
} from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { Badge } from "@/components/ui/badge";
import {
  AlertTriangle,
  CheckCircle2,
  Download,
  Trash2,
  ExternalLink,
  ShieldCheck,
} from "lucide-react";

const DEFAULT_VERSION = "v0.3.10";

export function SettingsPage() {
  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold">Settings</h1>
        <p className="text-sm text-muted-foreground">
          Cluster-wide configuration and optional components
        </p>
      </div>

      <AgentSandboxSection />
    </div>
  );
}

function AgentSandboxSection() {
  const { data: capabilities, isLoading } = useCapabilities();
  const installMutation = useInstallAgentSandbox();
  const uninstallMutation = useUninstallAgentSandbox();
  const [version, setVersion] = useState(DEFAULT_VERSION);
  const [showConfirmUninstall, setShowConfirmUninstall] = useState(false);

  const crdInstalled = capabilities?.agentSandbox?.available ?? false;
  const busy = installMutation.isPending || uninstallMutation.isPending;

  const handleInstall = () => {
    installMutation.mutate(version || undefined);
  };

  const handleUninstall = () => {
    uninstallMutation.mutate(undefined, {
      onSuccess: () => setShowConfirmUninstall(false),
    });
  };

  if (isLoading) {
    return (
      <Card>
        <CardHeader>
          <Skeleton className="h-5 w-48" />
        </CardHeader>
        <CardContent className="space-y-3">
          <Skeleton className="h-4 w-full" />
          <Skeleton className="h-4 w-3/4" />
          <Skeleton className="h-10 w-32" />
        </CardContent>
      </Card>
    );
  }

  return (
    <Card>
      <CardHeader>
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-2">
            <ShieldCheck className="h-5 w-5 text-muted-foreground" />
            <CardTitle className="text-base">Agent Sandbox CRDs</CardTitle>
          </div>
          <Badge variant={crdInstalled ? "default" : "secondary"}>
            {crdInstalled ? "Installed" : "Not Installed"}
          </Badge>
        </div>
        <CardDescription>
          Install the{" "}
          <a
            href="https://github.com/kubernetes-sigs/agent-sandbox"
            target="_blank"
            rel="noopener noreferrer"
            className="underline underline-offset-4 hover:text-foreground inline-flex items-center gap-1"
          >
            kubernetes-sigs/agent-sandbox
            <ExternalLink className="h-3 w-3" />
          </a>{" "}
          CRDs to enable kernel-level isolation (gVisor/Kata) for agent runs.
          Provides Sandbox, SandboxTemplate, SandboxClaim, and SandboxWarmPool
          resources.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        {crdInstalled ? (
          <>
            <div className="flex items-start gap-2 rounded-lg border border-green-500/30 bg-green-500/5 p-3">
              <CheckCircle2 className="h-4 w-4 mt-0.5 text-green-500 shrink-0" />
              <div className="text-sm">
                <p className="font-medium text-green-500">
                  CRDs are installed
                </p>
                <p className="text-muted-foreground">
                  Agent Sandbox resources (agents.x-k8s.io/v1alpha1) are
                  available in the cluster. You can enable sandbox isolation per
                  instance or persona pack.
                </p>
              </div>
            </div>

            {!showConfirmUninstall ? (
              <Button
                variant="destructive"
                size="sm"
                onClick={() => setShowConfirmUninstall(true)}
                disabled={busy}
              >
                <Trash2 className="h-4 w-4 mr-2" />
                Uninstall CRDs
              </Button>
            ) : (
              <div className="flex items-start gap-2 rounded-lg border border-red-500/30 bg-red-500/5 p-3">
                <AlertTriangle className="h-4 w-4 mt-0.5 text-red-500 shrink-0" />
                <div className="space-y-2">
                  <p className="text-sm font-medium text-red-500">
                    This will remove all Agent Sandbox CRDs and any existing
                    Sandbox resources in the cluster.
                  </p>
                  <div className="flex items-center gap-2">
                    <Button
                      variant="destructive"
                      size="sm"
                      onClick={handleUninstall}
                      disabled={busy}
                    >
                      {uninstallMutation.isPending
                        ? "Removing..."
                        : "Confirm Uninstall"}
                    </Button>
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => setShowConfirmUninstall(false)}
                      disabled={busy}
                    >
                      Cancel
                    </Button>
                  </div>
                </div>
              </div>
            )}
          </>
        ) : (
          <>
            <div className="flex items-start gap-2 rounded-lg border border-yellow-500/30 bg-yellow-500/5 p-3">
              <AlertTriangle className="h-4 w-4 mt-0.5 text-yellow-600 shrink-0" />
              <div className="text-sm">
                <p className="font-medium text-yellow-600">Not installed</p>
                <p className="text-muted-foreground">
                  {capabilities?.agentSandbox?.reason ||
                    "Agent Sandbox CRDs are not installed in the cluster."}
                </p>
              </div>
            </div>

            <div className="space-y-3">
              <div className="space-y-1.5">
                <Label htmlFor="sandbox-version" className="text-xs">
                  Release version
                </Label>
                <div className="flex items-center gap-2">
                  <Input
                    id="sandbox-version"
                    value={version}
                    onChange={(e) => setVersion(e.target.value)}
                    placeholder={DEFAULT_VERSION}
                    className="w-40 font-mono text-sm"
                    disabled={busy}
                  />
                  <Button onClick={handleInstall} disabled={busy} size="sm">
                    <Download className="h-4 w-4 mr-2" />
                    {installMutation.isPending
                      ? "Installing..."
                      : "Install CRDs"}
                  </Button>
                </div>
              </div>
              <p className="text-xs text-muted-foreground">
                Fetches CRD manifests from the{" "}
                <a
                  href={`https://github.com/kubernetes-sigs/agent-sandbox/releases/tag/${version}`}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="underline underline-offset-4 hover:text-foreground"
                >
                  {version} release
                </a>{" "}
                and applies them to the cluster.
              </p>
            </div>
          </>
        )}
      </CardContent>
    </Card>
  );
}
