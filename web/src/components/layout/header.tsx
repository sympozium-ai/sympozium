import { useAuth } from "@/components/auth-provider";
import { getNamespace, setNamespace } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { LogOut, Wifi, WifiOff } from "lucide-react";
import { useWebSocket } from "@/hooks/use-websocket";
import { useState } from "react";

export function Header() {
  const { logout } = useAuth();
  const { connected } = useWebSocket();
  const [ns, setNs] = useState(getNamespace());

  const handleNsChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    setNs(e.target.value);
  };

  const handleNsBlur = () => {
    if (ns.trim()) {
      setNamespace(ns.trim());
      window.location.reload();
    }
  };

  const handleNsKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === "Enter") handleNsBlur();
  };

  return (
    <header className="flex h-14 items-center justify-between border-b border-border/50 bg-card px-6">
      <div className="flex items-center gap-4">
        <div className="flex items-center gap-2 text-sm text-muted-foreground">
          <span>Namespace:</span>
          <Input
            value={ns}
            onChange={handleNsChange}
            onBlur={handleNsBlur}
            onKeyDown={handleNsKeyDown}
            className="h-7 w-36 text-xs"
          />
        </div>
      </div>
      <div className="flex items-center gap-3">
        {/* Connection status indicator */}
        <div
          className={`flex items-center gap-2 rounded-full px-3 py-1 text-xs font-medium border ${
            connected
              ? "bg-emerald-500/10 text-emerald-400 border-emerald-500/20"
              : "bg-red-500/10 text-red-400 border-red-500/20"
          }`}
        >
          {connected ? (
            <>
              <span className="relative flex h-2 w-2">
                <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-emerald-400 opacity-75" />
                <span className="relative inline-flex h-2 w-2 rounded-full bg-emerald-400" />
              </span>
              <Wifi className="h-3.5 w-3.5" />
              <span>Stream Connected</span>
            </>
          ) : (
            <>
              <span className="relative flex h-2 w-2">
                <span className="relative inline-flex h-2 w-2 rounded-full bg-red-400" />
              </span>
              <WifiOff className="h-3.5 w-3.5" />
              <span>Offline</span>
            </>
          )}
        </div>
        <Button variant="ghost" size="icon" onClick={logout} title="Logout">
          <LogOut className="h-4 w-4" />
        </Button>
      </div>
    </header>
  );
}
