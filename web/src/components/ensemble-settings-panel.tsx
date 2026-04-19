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
import type { SharedMemorySpec } from "@/lib/api";

export interface EnsembleSettings {
  name: string;
  description: string;
  category: string;
  workflowType: string;
  sharedMemory: SharedMemorySpec | null;
}

interface EnsembleSettingsPanelProps {
  settings: EnsembleSettings;
  onChange: (settings: EnsembleSettings) => void;
  onClose: () => void;
}

export function EnsembleSettingsPanel({
  settings,
  onChange,
  onClose,
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
            placeholder="e.g. my-research-team"
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
            <div className="space-y-1.5 pl-5">
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
          )}
        </div>
      </div>
    </div>
  );
}
