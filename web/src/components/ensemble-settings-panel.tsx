/**
 * EnsembleSettingsPanel — slide-in panel for ensemble-level settings
 * (name, description, category, workflow type, shared memory).
 */

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { X } from "lucide-react";
import type {
  SharedMemorySpec,
  AgentConfigRelationship,
  PermeabilityRule,
} from "@/lib/api";

export interface EnsembleSettings {
  name: string;
  description: string;
  category: string;
  workflowType: string;
  sharedMemory: SharedMemorySpec | null;
}

/**
 * Auto-derive permeability rules from relationships:
 * - Delegation sources → trusted (produce findings for peers)
 * - Supervision targets → public (supervisors need full visibility)
 * - Terminal nodes (only targets) → private
 * - Everyone else → ensemble default
 */
function derivePermeability(
  personaNames: string[],
  relationships: AgentConfigRelationship[],
  defaultVis: "public" | "trusted" | "private" = "public",
): PermeabilityRule[] {
  const delegationSources = new Set<string>();
  const supervisionTargets = new Set<string>();
  const sources = new Set<string>();

  for (const rel of relationships) {
    sources.add(rel.source);
    if (rel.type === "delegation") delegationSources.add(rel.source);
    if (rel.type === "supervision") supervisionTargets.add(rel.target);
  }

  return personaNames.map((name) => {
    let vis = defaultVis;
    if (delegationSources.has(name)) vis = "trusted";
    else if (supervisionTargets.has(name)) vis = "public";
    else if (!sources.has(name) && relationships.length > 0) vis = "private";
    return { agentConfig: name, defaultVisibility: vis };
  });
}

interface EnsembleSettingsPanelProps {
  settings: EnsembleSettings;
  onChange: (settings: EnsembleSettings) => void;
  onClose: () => void;
  /** Names of personas in the ensemble (for auto-deriving permeability). */
  personaNames?: string[];
  /** Relationships between personas (for auto-deriving permeability). */
  relationships?: AgentConfigRelationship[];
}

export function EnsembleSettingsPanel({
  settings,
  onChange,
  onClose,
  personaNames = [],
  relationships = [],
}: EnsembleSettingsPanelProps) {
  function update(partial: Partial<EnsembleSettings>) {
    onChange({ ...settings, ...partial });
  }

  return (
    <div className="w-80 border-l border-border bg-card flex flex-col h-full">
      {/* Header */}
      <div className="flex items-center justify-between px-4 py-3 border-b border-border">
        <h3 className="font-semibold text-sm">Ensemble Settings</h3>
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
          <Label htmlFor="ens-name" className="text-xs">
            Name (required)
          </Label>
          <Input
            id="ens-name"
            value={settings.name}
            onChange={(e) =>
              update({
                name: e.target.value.toLowerCase().replace(/[^a-z0-9-]/g, "-"),
              })
            }
            placeholder="e.g. my-research-delegation-example"
            className="h-8 text-sm font-mono"
          />
          <p className="text-[10px] text-muted-foreground">
            RFC 1123: lowercase, alphanumeric, and hyphens
          </p>
        </div>

        <div className="space-y-1.5">
          <Label htmlFor="ens-desc" className="text-xs">
            Description
          </Label>
          <Textarea
            id="ens-desc"
            value={settings.description}
            onChange={(e) => update({ description: e.target.value })}
            placeholder="What does this ensemble do?"
            className="min-h-[60px] text-sm"
          />
        </div>

        <div className="space-y-1.5">
          <Label htmlFor="ens-cat" className="text-xs">
            Category
          </Label>
          <Input
            id="ens-cat"
            value={settings.category}
            onChange={(e) => update({ category: e.target.value })}
            placeholder="e.g. research, platform, devops"
            className="h-8 text-sm"
          />
        </div>

        <div className="space-y-1.5">
          <Label className="text-xs">Workflow Type</Label>
          <Select
            value={settings.workflowType || "autonomous"}
            onValueChange={(v) => update({ workflowType: v })}
          >
            <SelectTrigger className="h-8 text-sm">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="autonomous">Autonomous</SelectItem>
              <SelectItem value="pipeline">Pipeline</SelectItem>
              <SelectItem value="delegation">Delegation</SelectItem>
            </SelectContent>
          </Select>
          <p className="text-[10px] text-muted-foreground">
            {settings.workflowType === "delegation"
              ? "Personas can delegate tasks to each other at runtime"
              : settings.workflowType === "pipeline"
                ? "Personas execute in sequence defined by edges"
                : "Personas run independently on their own schedules"}
          </p>
        </div>

        {/* Shared Memory */}
        <div className="space-y-2">
          <div className="flex items-center gap-2">
            <input
              type="checkbox"
              id="shared-mem"
              checked={settings.sharedMemory?.enabled ?? false}
              onChange={(e) =>
                update({
                  sharedMemory: e.target.checked
                    ? { enabled: true, storageSize: "1Gi" }
                    : null,
                })
              }
              className="rounded"
            />
            <Label htmlFor="shared-mem" className="text-xs cursor-pointer">
              Enable Shared Memory
            </Label>
          </div>
          {settings.sharedMemory?.enabled && (
            <div className="space-y-3 pl-5">
              <div className="space-y-1.5">
                <Label htmlFor="sm-size" className="text-xs">
                  Storage Size
                </Label>
                <Input
                  id="sm-size"
                  value={settings.sharedMemory.storageSize || "1Gi"}
                  onChange={(e) =>
                    update({
                      sharedMemory: {
                        ...settings.sharedMemory!,
                        storageSize: e.target.value,
                      },
                    })
                  }
                  placeholder="e.g. 1Gi"
                  className="h-8 text-sm font-mono"
                />
                <p className="text-[10px] text-muted-foreground">
                  Access rules are configured per-persona after saving.
                </p>
              </div>

              {/* Membrane settings */}
              <div className="space-y-2 border-t pt-2">
                <div className="flex items-center gap-2">
                  <input
                    type="checkbox"
                    id="membrane-enable"
                    checked={!!settings.sharedMemory.membrane}
                    onChange={(e) => {
                      if (e.target.checked) {
                        const defaultVis = "public" as const;
                        const derived =
                          personaNames.length > 0 && relationships.length > 0
                            ? derivePermeability(personaNames, relationships, defaultVis)
                            : undefined;
                        update({
                          sharedMemory: {
                            ...settings.sharedMemory!,
                            membrane: {
                              defaultVisibility: defaultVis,
                              permeability: derived,
                            },
                          },
                        });
                      } else {
                        update({
                          sharedMemory: {
                            ...settings.sharedMemory!,
                            membrane: undefined,
                          },
                        });
                      }
                    }}
                    className="rounded"
                  />
                  <Label htmlFor="membrane-enable" className="text-xs cursor-pointer">
                    Enable Membrane
                  </Label>
                </div>

                {settings.sharedMemory.membrane && (
                  <div className="space-y-2 pl-5">
                    <div className="space-y-1">
                      <Label className="text-xs">Default Visibility</Label>
                      <Select
                        value={settings.sharedMemory.membrane.defaultVisibility || "public"}
                        onValueChange={(v) =>
                          update({
                            sharedMemory: {
                              ...settings.sharedMemory!,
                              membrane: {
                                ...settings.sharedMemory!.membrane!,
                                defaultVisibility: v as "public" | "trusted" | "private",
                              },
                            },
                          })
                        }
                      >
                        <SelectTrigger className="h-8 text-sm">
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          <SelectItem value="public">Public</SelectItem>
                          <SelectItem value="trusted">Trusted</SelectItem>
                          <SelectItem value="private">Private</SelectItem>
                        </SelectContent>
                      </Select>
                      <p className="text-[10px] text-muted-foreground">
                        Default visibility for new entries
                      </p>
                    </div>

                    <div className="space-y-1">
                      <Label htmlFor="mb-budget" className="text-xs">
                        Token Budget
                      </Label>
                      <Input
                        id="mb-budget"
                        type="number"
                        value={settings.sharedMemory.membrane.tokenBudget?.maxTokens ?? ""}
                        onChange={(e) =>
                          update({
                            sharedMemory: {
                              ...settings.sharedMemory!,
                              membrane: {
                                ...settings.sharedMemory!.membrane!,
                                tokenBudget: e.target.value
                                  ? {
                                      ...settings.sharedMemory!.membrane!.tokenBudget,
                                      maxTokens: parseInt(e.target.value, 10),
                                      action: settings.sharedMemory!.membrane!.tokenBudget?.action || "halt",
                                    }
                                  : undefined,
                              },
                            },
                          })
                        }
                        placeholder="e.g. 100000 (0 = unlimited)"
                        className="h-8 text-sm font-mono"
                      />
                    </div>

                    <div className="space-y-1">
                      <Label htmlFor="mb-cb" className="text-xs">
                        Circuit Breaker (consecutive failures)
                      </Label>
                      <Input
                        id="mb-cb"
                        type="number"
                        value={settings.sharedMemory.membrane.circuitBreaker?.consecutiveFailures ?? ""}
                        onChange={(e) =>
                          update({
                            sharedMemory: {
                              ...settings.sharedMemory!,
                              membrane: {
                                ...settings.sharedMemory!.membrane!,
                                circuitBreaker: e.target.value
                                  ? { consecutiveFailures: parseInt(e.target.value, 10) }
                                  : undefined,
                              },
                            },
                          })
                        }
                        placeholder="e.g. 3"
                        className="h-8 text-sm font-mono"
                      />
                    </div>

                    <div className="space-y-1">
                      <Label htmlFor="mb-ttl" className="text-xs">
                        Time Decay TTL
                      </Label>
                      <Input
                        id="mb-ttl"
                        value={settings.sharedMemory.membrane.timeDecay?.ttl ?? ""}
                        onChange={(e) =>
                          update({
                            sharedMemory: {
                              ...settings.sharedMemory!,
                              membrane: {
                                ...settings.sharedMemory!.membrane!,
                                timeDecay: e.target.value
                                  ? {
                                      ttl: e.target.value,
                                      decayFunction: settings.sharedMemory!.membrane!.timeDecay?.decayFunction || "linear",
                                    }
                                  : undefined,
                              },
                            },
                          })
                        }
                        placeholder="e.g. 168h (7 days)"
                        className="h-8 text-sm font-mono"
                      />
                    </div>
                  </div>
                )}
              </div>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
