import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Loader2, ShieldCheck } from "lucide-react";

interface GithubAuthDialogProps {
  open: boolean;
  onClose: () => void;
}

export function GithubAuthDialog({ open, onClose }: GithubAuthDialogProps) {
  const [status, setStatus] = useState<"idle" | "complete" | "saving" | "error">("idle");
  const [token, setToken] = useState("");
  const [errorMsg, setErrorMsg] = useState("");

  // Check existing status on open
  useEffect(() => {
    if (!open) return;
    setToken("");
    setErrorMsg("");
    let cancelled = false;
    const check = async () => {
      try {
        const res = await api.githubAuth.status();
        if (!cancelled && res.status === "complete") setStatus("complete");
        else if (!cancelled) setStatus("idle");
      } catch {
        if (!cancelled) setStatus("idle");
      }
    };
    check();
    return () => { cancelled = true; };
  }, [open]);

  const handleSave = async () => {
    setErrorMsg("");
    if (!token.trim()) {
      setErrorMsg("Please paste a GitHub token.");
      return;
    }
    setStatus("saving");
    try {
      const res = await api.githubAuth.setToken(token.trim());
      if (res.status === "complete") {
        setStatus("complete");
      } else {
        setStatus("error");
        setErrorMsg("Token saved, but auth status is not complete yet.");
      }
    } catch (err) {
      setStatus("error");
      setErrorMsg(err instanceof Error ? err.message : String(err));
    }
  };

  return (
    <Dialog open={open} onOpenChange={(v) => !v && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <svg
              className="h-5 w-5"
              viewBox="0 0 24 24"
              fill="currentColor"
              aria-hidden="true"
            >
              <path d="M12 .297c-6.63 0-12 5.373-12 12 0 5.303 3.438 9.8 8.205 11.385.6.113.82-.258.82-.577 0-.285-.01-1.04-.015-2.04-3.338.724-4.042-1.61-4.042-1.61C4.422 18.07 3.633 17.7 3.633 17.7c-1.087-.744.084-.729.084-.729 1.205.084 1.838 1.236 1.838 1.236 1.07 1.835 2.809 1.305 3.495.998.108-.776.417-1.305.76-1.605-2.665-.3-5.466-1.332-5.466-5.93 0-1.31.465-2.38 1.235-3.22-.135-.303-.54-1.523.105-3.176 0 0 1.005-.322 3.3 1.23.96-.267 1.98-.399 3-.405 1.02.006 2.04.138 3 .405 2.28-1.552 3.285-1.23 3.285-1.23.645 1.653.24 2.873.12 3.176.765.84 1.23 1.91 1.23 3.22 0 4.61-2.805 5.625-5.475 5.92.42.36.81 1.096.81 2.22 0 1.606-.015 2.896-.015 3.286 0 .315.21.69.825.57C20.565 22.092 24 17.592 24 12.297c0-6.627-5.373-12-12-12" />
            </svg>
            Connect GitHub
          </DialogTitle>
          <DialogDescription>
            Paste a GitHub personal access token with <code>repo</code> scope to
            allow Sympozium to create issues and pull requests on your behalf.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4 py-2">
          {status === "complete" ? (
            <div className="flex flex-col items-center gap-3 py-6">
              <ShieldCheck className="h-10 w-10 text-green-500" />
              <p className="text-sm font-medium">
                GitHub authenticated successfully
              </p>
              <p className="text-xs text-muted-foreground">
                Token saved to cluster. You can close this dialog.
              </p>
              <Button variant="outline" onClick={onClose}>
                Done
              </Button>
            </div>
          ) : (
            <div className="space-y-3">
              <Input
                type="password"
                value={token}
                onChange={(e) => setToken(e.target.value)}
                placeholder="github_pat_... or ghp_..."
                onKeyDown={(e) => e.key === "Enter" && handleSave()}
              />
              {errorMsg && (
                <p className="text-xs text-destructive">{errorMsg}</p>
              )}
              <Button
                className="w-full"
                onClick={handleSave}
                disabled={status === "saving"}
              >
                {status === "saving" ? (
                  <>
                    <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                    Saving token…
                  </>
                ) : (
                  "Save token"
                )}
              </Button>
              <p className="text-xs text-muted-foreground">
                The token is stored as a Kubernetes Secret (<code>github-gitops-token</code>)
                and mounted into agent pods via the github-gitops skill sidecar.
              </p>
            </div>
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}
