import { useState } from "react";
import { Check, Copy, Loader2, Server } from "lucide-react";
import { FLEET_PRIVILEGE_NOTICE, fleetInstallCommand } from "@/lib/celln-fleet-setup";
import { Button } from "@/components/ui/button";

const GUIDE = "https://github.com/sympozium-ai/sympozium/blob/main/docs/guides/celln-fleet-installation.md";

/**
 * Shown when no Celln fleet serves the namespace. The console cannot install
 * the fleet itself (that is a privileged Helm upgrade of the control plane),
 * so it hands the operator the command and, while `watching`, the caller polls
 * the fleet's runtime profiles and unlocks the Celln plane once they appear.
 * Nothing about a model is asked here: Celln is isolation, and every Agent
 * brings its own connection and key.
 */
export function CellnFleetSetup({ watching, onWatch }: { watching: boolean; onWatch: (watching: boolean) => void }) {
  const [copied, setCopied] = useState(false);
  const command = fleetInstallCommand();
  const { nodeLabel, daemonSets, mounts } = FLEET_PRIVILEGE_NOTICE;

  if (!watching) {
    return (
      <div role="status" className="space-y-2 rounded-lg border border-amber-500/30 bg-amber-500/5 p-3 text-xs text-muted-foreground" data-testid="celln-fleet-missing">
        <p>
          The <strong className="text-foreground">Celln parent</strong> plane runs the native harness inside a hardware-isolated cell on the Celln fleet, which this cluster does not offer to this namespace yet. Pi and Hermes run on the Kubernetes plane.
        </p>
        <Button type="button" size="sm" variant="outline" className="h-7 gap-1 text-xs" onClick={() => onWatch(true)} data-testid="celln-fleet-setup-open">
          <Server className="h-3 w-3" /> Set up the Celln fleet
        </Button>
      </div>
    );
  }

  return (
    <div className="space-y-3 rounded-lg border border-blue-500/30 bg-blue-500/5 p-3 text-xs text-muted-foreground" data-testid="celln-fleet-setup">
      <p className="text-sm font-medium text-foreground">Set up the Celln fleet</p>
      <p>
        The fleet is opt-in: a default install runs Agents on the Kubernetes plane only. Adding it upgrades the control plane, so it runs from a terminal with cluster-admin access, not from the console. Run the command; this step continues by itself when the fleet reaches this namespace.
      </p>
      <div className="flex items-start gap-2">
        <pre className="min-w-0 flex-1 overflow-x-auto whitespace-pre-wrap break-all rounded bg-muted/50 p-2 font-mono text-[11px] text-foreground" data-testid="celln-fleet-command">{command}</pre>
        <Button type="button" size="sm" variant="outline" className="h-7 shrink-0 gap-1 text-xs" onClick={() => { navigator.clipboard?.writeText(command).then(() => { setCopied(true); setTimeout(() => setCopied(false), 2000); }).catch(() => undefined); }}>
          {copied ? <Check className="h-3 w-3" /> : <Copy className="h-3 w-3" />} {copied ? "Copied" : "Copy"}
        </Button>
      </div>
      <p data-testid="celln-fleet-privilege">
        On every node labeled <code>{nodeLabel}</code> this deploys two privileged DaemonSets ({daemonSets.map((name, index) => <span key={name}>{index ? ", " : ""}<code>{name}</code></span>)}) that mount {mounts.map((mount, index) => <span key={mount}>{index ? ", " : ""}<code>{mount}</code></span>)}, because booting micro-VMs needs them. Nodes hold no model key: every Agent's model requests leave through the model gateway with that Agent's own key, which you give it when you create it. Nodes with <code>/dev/kvm</code> and a boot kernel are labeled by the node probe; label others by hand. See the <a className="underline" href={GUIDE} target="_blank" rel="noreferrer">fleet installation guide</a>.
      </p>
      <div className="flex items-center justify-between gap-2">
        <span className="flex items-center gap-1.5 text-foreground" role="status" data-testid="celln-fleet-waiting">
          <Loader2 className="h-3 w-3 animate-spin" /> Waiting for the fleet to reach this namespace…
        </span>
        <Button type="button" size="sm" variant="ghost" className="h-7 text-xs" onClick={() => onWatch(false)}>Cancel</Button>
      </div>
    </div>
  );
}
