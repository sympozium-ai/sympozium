/**
 * AgentConfigPanel — slide-in side panel for editing a persona's
 * configuration within the Ensemble Builder canvas.
 *
 * Uses the provider context from the builder to show a provider-aware
 * model selector (same as the onboarding wizard).
 */

import { useState, useEffect } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { Badge } from "@/components/ui/badge";
import { Separator } from "@/components/ui/separator";
import { ScrollArea } from "@/components/ui/scroll-area";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { X, Trash2, Save, Search, Check, Loader2 } from "lucide-react";
import type { AgentConfigSpec } from "@/lib/api";
import type { ProviderContext } from "@/components/ensemble-builder";
import { useSkills } from "@/hooks/use-api";
import { useModelList } from "@/hooks/use-model-list";

interface AgentConfigPanelProps {
  persona: AgentConfigSpec;
  providerCtx: ProviderContext;
  onSave: (updated: AgentConfigSpec) => void;
  onDelete: () => void;
  onClose: () => void;
}

export function AgentConfigPanel({
  persona,
  providerCtx,
  onSave,
  onDelete,
  onClose,
}: AgentConfigPanelProps) {
  const { data: skills } = useSkills();
  const [draft, setDraft] = useState<AgentConfigSpec>({ ...persona });
  const [modelSearch, setModelSearch] = useState("");

  // Provider-aware model list
  const {
    models,
    isLoading: modelsLoading,
    isLive,
  } = useModelList(
    providerCtx.provider,
    providerCtx.apiKey,
    providerCtx.baseURL || undefined,
  );

  const filteredModels = models.filter((m) =>
    m.toLowerCase().includes(modelSearch.toLowerCase()),
  );

  // Reset draft when the selected persona changes.
  useEffect(() => {
    setDraft({ ...persona });
    setModelSearch("");
  }, [persona.name, persona.provider, persona.model]);

  const availableSkills = (skills || [])
    .map((s) => (typeof s === "string" ? s : s.metadata?.name || ""))
    .filter(Boolean)
    .sort();

  function toggleSkill(skill: string) {
    const current = draft.skills || [];
    if (current.includes(skill)) {
      setDraft({ ...draft, skills: current.filter((s) => s !== skill) });
    } else {
      setDraft({ ...draft, skills: [...current, skill] });
    }
  }

  return (
    <div className="w-80 border-l border-border bg-card flex flex-col h-full">
      {/* Header */}
      <div className="flex items-center justify-between px-4 py-3 border-b border-border">
        <h3 className="font-semibold text-sm truncate">
          {draft.displayName || draft.name || "New Persona"}
        </h3>
        <Button
          variant="ghost"
          size="icon"
          onClick={onClose}
          className="h-7 w-7"
        >
          <X className="h-4 w-4" />
        </Button>
      </div>

      {/* Form */}
      <div className="flex-1 overflow-y-auto px-4 py-3 space-y-4">
        <div className="space-y-1.5">
          <Label htmlFor="persona-name" className="text-xs">
            Name (identifier)
          </Label>
          <Input
            id="persona-name"
            value={draft.name}
            onChange={(e) =>
              setDraft({
                ...draft,
                name: e.target.value.toLowerCase().replace(/[^a-z0-9-]/g, "-"),
              })
            }
            placeholder="e.g. researcher"
            className="h-8 text-sm font-mono"
          />
        </div>

        <div className="space-y-1.5">
          <Label htmlFor="persona-display" className="text-xs">
            Display Name
          </Label>
          <Input
            id="persona-display"
            value={draft.displayName || ""}
            onChange={(e) =>
              setDraft({ ...draft, displayName: e.target.value })
            }
            placeholder="e.g. Lead Researcher"
            className="h-8 text-sm"
          />
        </div>

        {/* Model selector — provider-aware */}
        <div className="space-y-1.5">
          <Label className="text-xs">Model</Label>
          {providerCtx.modelRef || draft.provider === "local-model" ? (
            <div className="rounded-md border border-violet-500/30 bg-violet-500/10 px-3 py-2 text-xs">
              <p className="font-mono text-violet-400">
                {draft.model || providerCtx.modelRef || "local model"}
              </p>
              <p className="text-[10px] text-muted-foreground mt-0.5">
                Using cluster-local model
              </p>
            </div>
          ) : (
            <>
              <div className="relative">
                <Search className="absolute left-2.5 top-1/2 h-3 w-3 -translate-y-1/2 text-muted-foreground" />
                <Input
                  value={modelSearch}
                  onChange={(e) => setModelSearch(e.target.value)}
                  placeholder="Search models..."
                  className="h-8 pl-7 text-sm"
                />
              </div>
              {modelsLoading ? (
                <div className="flex items-center gap-2 py-2 text-xs text-muted-foreground justify-center">
                  <Loader2 className="h-3 w-3 animate-spin" />
                  Fetching models...
                </div>
              ) : (
                <ScrollArea className="h-28 rounded-md border border-border/50">
                  <div className="p-1 space-y-0.5">
                    {filteredModels.length === 0 ? (
                      <p className="py-2 text-center text-[10px] text-muted-foreground">
                        {modelSearch
                          ? `No models match "${modelSearch}"`
                          : "No models available"}
                      </p>
                    ) : (
                      filteredModels.map((m) => (
                        <button
                          key={m}
                          type="button"
                          onClick={() => setDraft({ ...draft, model: m })}
                          className={`flex w-full items-center gap-1.5 rounded px-2 py-1 text-[11px] font-mono text-left transition-colors
                            ${
                              m === draft.model
                                ? "bg-blue-500/15 text-blue-400 border border-blue-500/30"
                                : "text-foreground hover:bg-white/5 border border-transparent"
                            }`}
                        >
                          {m === draft.model && (
                            <Check className="h-2.5 w-2.5 shrink-0" />
                          )}
                          <span className="truncate">{m}</span>
                        </button>
                      ))
                    )}
                  </div>
                </ScrollArea>
              )}
              {/* Custom model input */}
              <Input
                value={draft.model || ""}
                onChange={(e) => setDraft({ ...draft, model: e.target.value })}
                placeholder="Or type a custom model name"
                className="h-7 text-[11px] font-mono"
              />
              {isLive && (
                <p className="text-[9px] text-green-500/70">
                  Live models from {providerCtx.provider}
                </p>
              )}
            </>
          )}
        </div>

        <div className="space-y-1.5">
          <Label htmlFor="persona-prompt" className="text-xs">
            System Prompt
          </Label>
          <Textarea
            id="persona-prompt"
            value={draft.systemPrompt}
            onChange={(e) =>
              setDraft({ ...draft, systemPrompt: e.target.value })
            }
            placeholder="Describe this agent's role, responsibilities, and behaviour..."
            className="min-h-[120px] text-sm"
          />
        </div>

        <Separator />

        {/* Skills */}
        <div className="space-y-1.5">
          <Label className="text-xs">Skills</Label>
          <div className="flex flex-wrap gap-1">
            {(availableSkills.length > 0
              ? availableSkills
              : ["memory", "k8s-ops", "shell"]
            ).map((skill) => (
              <Badge
                key={skill}
                variant={
                  (draft.skills || []).includes(skill) ? "default" : "outline"
                }
                className="text-[10px] px-1.5 py-0.5 cursor-pointer select-none"
                onClick={() => toggleSkill(skill)}
              >
                {skill}
              </Badge>
            ))}
          </div>
          <p className="text-[10px] text-muted-foreground">
            Click to toggle skills on/off
          </p>
        </div>

        {/* Subagents config (shown when subagents skill is enabled) */}
        {(draft.skills || []).includes("subagents") && (
          <div className="rounded-md border border-teal-500/20 bg-teal-500/5 p-2.5 space-y-2">
            <p className="text-[10px] font-medium text-teal-400">
              Sub-Agent Limits
            </p>
            <div className="grid grid-cols-3 gap-2">
              <div className="space-y-0.5">
                <Label className="text-[9px]">Max Depth</Label>
                <Input
                  type="number"
                  min={1}
                  max={5}
                  value={draft.subagents?.maxDepth ?? 2}
                  onChange={(e) =>
                    setDraft({
                      ...draft,
                      subagents: {
                        ...draft.subagents,
                        maxDepth: parseInt(e.target.value) || 2,
                      },
                    })
                  }
                  className="h-6 text-[10px] px-1.5"
                />
              </div>
              <div className="space-y-0.5">
                <Label className="text-[9px]">Concurrent</Label>
                <Input
                  type="number"
                  min={1}
                  max={20}
                  value={draft.subagents?.maxConcurrent ?? 5}
                  onChange={(e) =>
                    setDraft({
                      ...draft,
                      subagents: {
                        ...draft.subagents,
                        maxConcurrent: parseInt(e.target.value) || 5,
                      },
                    })
                  }
                  className="h-6 text-[10px] px-1.5"
                />
              </div>
              <div className="space-y-0.5">
                <Label className="text-[9px]">Per Agent</Label>
                <Input
                  type="number"
                  min={1}
                  max={10}
                  value={draft.subagents?.maxChildrenPerAgent ?? 3}
                  onChange={(e) =>
                    setDraft({
                      ...draft,
                      subagents: {
                        ...draft.subagents,
                        maxChildrenPerAgent: parseInt(e.target.value) || 3,
                      },
                    })
                  }
                  className="h-6 text-[10px] px-1.5"
                />
              </div>
            </div>
            <p className="text-[9px] text-muted-foreground">
              Depth: nesting levels. Concurrent: total active runs. Per Agent:
              children per spawn call.
            </p>
          </div>
        )}

        <Separator />

        {/* Schedule */}
        <div className="space-y-1.5">
          <Label className="text-xs">Schedule (optional)</Label>
          <Select
            value={draft.schedule?.type || ""}
            onValueChange={(v) =>
              setDraft({
                ...draft,
                schedule: v
                  ? {
                      type: v,
                      interval: draft.schedule?.interval || "30m",
                      task: draft.schedule?.task || "",
                    }
                  : undefined,
              })
            }
          >
            <SelectTrigger className="h-8 text-sm">
              <SelectValue placeholder="No schedule" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="heartbeat">Heartbeat</SelectItem>
              <SelectItem value="scheduled">Scheduled (cron)</SelectItem>
              <SelectItem value="sweep">Sweep</SelectItem>
            </SelectContent>
          </Select>

          {draft.schedule && (
            <>
              <Input
                value={draft.schedule.interval || draft.schedule.cron || ""}
                onChange={(e) =>
                  setDraft({
                    ...draft,
                    schedule: { ...draft.schedule!, interval: e.target.value },
                  })
                }
                placeholder="e.g. 30m, 1h, 6h"
                className="h-8 text-sm font-mono"
              />
              <Textarea
                value={draft.schedule.task}
                onChange={(e) =>
                  setDraft({
                    ...draft,
                    schedule: { ...draft.schedule!, task: e.target.value },
                  })
                }
                placeholder="Task description for each scheduled run..."
                className="min-h-[60px] text-sm"
              />
            </>
          )}
        </div>
      </div>

      {/* Footer */}
      <div className="px-4 py-3 border-t border-border flex gap-2">
        <Button
          size="sm"
          onClick={() => onSave(draft)}
          disabled={!draft.name || !draft.systemPrompt}
          className="flex-1"
        >
          <Save className="h-3.5 w-3.5 mr-1" />
          Save
        </Button>
        <Button size="sm" variant="destructive" onClick={onDelete}>
          <Trash2 className="h-3.5 w-3.5" />
        </Button>
      </div>
    </div>
  );
}
