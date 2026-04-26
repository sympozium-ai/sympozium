/**
 * AddProviderModal — lets users add a new provider node to the canvas.
 * Reuses the PROVIDERS list from the onboarding wizard and supports
 * both cloud providers and local models.
 */

import { useState } from "react";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Cpu, Check } from "lucide-react";
import { cn } from "@/lib/utils";
import { PROVIDERS } from "@/components/onboarding-wizard";
import { useModels } from "@/hooks/use-api";

export interface AddProviderResult {
  provider: string;
  label: string;
  baseURL: string;
  apiKey: string;
  modelRef?: string;
}

interface AddProviderModalProps {
  open: boolean;
  onClose: () => void;
  onAdd: (result: AddProviderResult) => void;
}

export function AddProviderModal({
  open,
  onClose,
  onAdd,
}: AddProviderModalProps) {
  const [provider, setProvider] = useState("");
  const [apiKey, setApiKey] = useState("");
  const [baseURL, setBaseURL] = useState("");
  const [selectedModelRef, setSelectedModelRef] = useState("");
  const { data: models } = useModels();
  const readyModels = (models || []).filter(
    (m) => m.status?.phase === "Ready",
  );

  const isLocalModel = provider === "local-model";
  const selectedProvider = PROVIDERS.find((p) => p.value === provider);
  const isLocal =
    provider === "ollama" ||
    provider === "lm-studio" ||
    provider === "llama-server" ||
    provider === "unsloth";
  const needsKey = !isLocal && !isLocalModel && provider !== "custom" && provider !== "";

  const canAdd =
    provider !== "" &&
    (isLocalModel
      ? selectedModelRef !== ""
      : (!needsKey || apiKey !== "") && (!isLocal || baseURL !== ""));

  function handleAdd() {
    const prov = PROVIDERS.find((p) => p.value === provider);
    onAdd({
      provider: isLocalModel ? "local-model" : provider,
      label: isLocalModel ? selectedModelRef : (prov?.label || provider),
      baseURL,
      apiKey,
      modelRef: isLocalModel ? selectedModelRef : undefined,
    });
    // Reset
    setProvider("");
    setApiKey("");
    setBaseURL("");
    setSelectedModelRef("");
    onClose();
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>Add Provider</DialogTitle>
        </DialogHeader>

        <div className="space-y-4">
          {/* Provider grid */}
          <div className="grid grid-cols-3 gap-2">
            <button
              onClick={() => {
                setProvider("local-model");
                setBaseURL("");
                setApiKey("");
              }}
              className={cn(
                "flex flex-col items-center gap-1.5 rounded-lg border p-3 text-xs transition-colors",
                provider === "local-model"
                  ? "border-violet-500/60 bg-violet-500/10 text-violet-400"
                  : "border-border/50 hover:border-border hover:bg-white/5",
              )}
            >
              <Cpu className="h-5 w-5" />
              Local Model
            </button>
            {PROVIDERS.map((p) => {
              const Icon = p.icon;
              return (
                <button
                  key={p.value}
                  onClick={() => {
                    setProvider(p.value);
                    setBaseURL(p.defaultBaseURL || "");
                    setSelectedModelRef("");
                  }}
                  className={cn(
                    "flex flex-col items-center gap-1.5 rounded-lg border p-3 text-xs transition-colors",
                    provider === p.value
                      ? "border-blue-500/60 bg-blue-500/10 text-blue-400"
                      : "border-border/50 hover:border-border hover:bg-white/5",
                  )}
                >
                  <Icon className="h-5 w-5" />
                  {p.label}
                </button>
              );
            })}
          </div>

          {/* Local model selector */}
          {isLocalModel && (
            <div className="space-y-1.5">
              <Label className="text-xs">Select Model</Label>
              {readyModels.length === 0 ? (
                <p className="text-xs text-muted-foreground rounded-md border border-border/50 bg-muted/20 px-3 py-3">
                  No models are ready. Deploy one on the Models page first.
                </p>
              ) : (
                <ScrollArea className="h-32 rounded-md border border-border/50">
                  <div className="p-1 space-y-0.5">
                    {readyModels.map((model) => (
                      <button
                        key={`${model.metadata.namespace}/${model.metadata.name}`}
                        type="button"
                        onClick={() =>
                          setSelectedModelRef(model.metadata.name)
                        }
                        className={cn(
                          "flex w-full items-start gap-2 rounded-md px-2.5 py-2 text-left text-xs transition-colors",
                          selectedModelRef === model.metadata.name
                            ? "bg-violet-500/15 text-violet-400 border border-violet-500/30"
                            : "text-foreground hover:bg-white/5 border border-transparent",
                        )}
                      >
                        <Cpu className="h-3.5 w-3.5 mt-0.5 shrink-0" />
                        <div className="min-w-0">
                          <div className="font-mono truncate">
                            {model.metadata.name}
                          </div>
                          <div className="text-[10px] text-muted-foreground">
                            {model.metadata.namespace}
                          </div>
                        </div>
                        {selectedModelRef === model.metadata.name && (
                          <Check className="h-3 w-3 shrink-0 mt-0.5 ml-auto" />
                        )}
                      </button>
                    ))}
                  </div>
                </ScrollArea>
              )}
            </div>
          )}

          {/* API key */}
          {needsKey && (
            <div className="space-y-1.5">
              <Label className="text-xs">API Key</Label>
              <Input
                type="password"
                value={apiKey}
                onChange={(e) => setApiKey(e.target.value)}
                placeholder={`${selectedProvider?.label || "Provider"} API key`}
                className="h-8 text-sm font-mono"
              />
            </div>
          )}

          {/* Base URL for local providers */}
          {(isLocal || provider === "custom") && (
            <div className="space-y-1.5">
              <Label className="text-xs">Base URL</Label>
              <Input
                value={baseURL}
                onChange={(e) => setBaseURL(e.target.value)}
                placeholder="http://localhost:8080/v1"
                className="h-8 text-sm font-mono"
              />
            </div>
          )}

          <div className="flex justify-end gap-2">
            <Button variant="outline" size="sm" onClick={onClose}>
              Cancel
            </Button>
            <Button size="sm" disabled={!canAdd} onClick={handleAdd}>
              Add to Canvas
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}
