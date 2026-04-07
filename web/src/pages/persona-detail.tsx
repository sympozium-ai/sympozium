import { useState, useEffect } from "react";
import { useParams, Link } from "react-router-dom";
import { usePersonaPack, useActivatePersonaPack, useSkills } from "@/hooks/use-api";
import { StatusBadge } from "@/components/status-badge";
import {
  Card,
  CardHeader,
  CardTitle,
  CardDescription,
  CardContent,
} from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Separator } from "@/components/ui/separator";
import { Skeleton } from "@/components/ui/skeleton";
import { Textarea } from "@/components/ui/textarea";
import { Clock, Wrench, MessageSquare, Brain, Shield, Pencil, X, Check } from "lucide-react";
import { Breadcrumbs } from "@/components/breadcrumbs";
import { formatAge } from "@/lib/utils";
import { YamlButton, personaPackYamlFromResource } from "@/components/yaml-panel";

interface PersonaEditState {
  systemPrompt: string;
  skills: string[];
}

export function PersonaDetailPage() {
  const { name } = useParams<{ name: string }>();
  const { data: pack, isLoading } = usePersonaPack(name || "");
  const { data: skillPacks } = useSkills();
  const patchMutation = useActivatePersonaPack();

  // Track which persona is being edited (by name), and its draft state
  const [editingPersona, setEditingPersona] = useState<string | null>(null);
  const [editState, setEditState] = useState<PersonaEditState>({ systemPrompt: "", skills: [] });

  // Reset edit state when pack data changes
  useEffect(() => {
    setEditingPersona(null);
  }, [pack?.metadata.name]);

  const startEditing = (persona: { name: string; systemPrompt: string; skills?: string[] }) => {
    setEditingPersona(persona.name);
    setEditState({
      systemPrompt: persona.systemPrompt,
      skills: persona.skills ? [...persona.skills] : [],
    });
  };

  const cancelEditing = () => {
    setEditingPersona(null);
  };

  const saveEditing = (personaName: string) => {
    if (!name) return;
    patchMutation.mutate(
      {
        name,
        personas: [
          {
            name: personaName,
            systemPrompt: editState.systemPrompt,
            skills: editState.skills,
          },
        ],
      },
      { onSuccess: () => setEditingPersona(null) }
    );
  };

  const toggleSkill = (skillName: string) => {
    setEditState((prev) => ({
      ...prev,
      skills: prev.skills.includes(skillName)
        ? prev.skills.filter((s) => s !== skillName)
        : [...prev.skills, skillName],
    }));
  };

  // Collect all available skill names from SkillPacks
  const availableSkills = skillPacks?.flatMap((sp) => sp.metadata.name) ?? [];

  if (isLoading) {
    return (
      <div className="space-y-4">
        <Skeleton className="h-8 w-64" />
        <Skeleton className="h-64 w-full" />
      </div>
    );
  }

  if (!pack) {
    return <p className="text-muted-foreground">Persona pack not found</p>;
  }

  return (
    <div className="space-y-6">
      <div className="space-y-1">
        <Breadcrumbs items={[
          { label: "Persona Packs", to: "/personas" },
          { label: pack.metadata.name },
        ]} />
        <h1 className="text-2xl font-bold font-mono">{pack.metadata.name}</h1>
        <div className="flex items-center gap-2 text-sm text-muted-foreground">
          {pack.spec.description && (
            <span>{pack.spec.description}</span>
          )}
          <StatusBadge phase={pack.status?.phase} />
          {pack.spec.category && (
            <Badge variant="outline" className="capitalize">
              {pack.spec.category}
            </Badge>
          )}
          {pack.spec.version && (
            <Badge variant="secondary">v{pack.spec.version}</Badge>
          )}
        </div>
      </div>

      {/* Summary stats */}
      <div className="grid gap-4 sm:grid-cols-4">
        <Card>
          <CardContent className="flex items-center gap-3 p-4">
            <div>
              <p className="text-sm text-muted-foreground">Personas</p>
              <p className="text-2xl font-bold">
                {pack.status?.personaCount ?? pack.spec.personas?.length ?? 0}
              </p>
            </div>
          </CardContent>
        </Card>
        <Card>
          <CardContent className="flex items-center gap-3 p-4">
            <div>
              <p className="text-sm text-muted-foreground">Installed</p>
              <p className="text-2xl font-bold">
                {pack.status?.installedCount ?? 0}
              </p>
            </div>
          </CardContent>
        </Card>
        <Card>
          <CardContent className="flex items-center gap-3 p-4">
            <div>
              <p className="text-sm text-muted-foreground">Enabled</p>
              <p className="text-2xl font-bold">
                {pack.spec.enabled ? "Yes" : "No"}
              </p>
            </div>
          </CardContent>
        </Card>
        <Card>
          <CardContent className="flex items-center gap-3 p-4">
            <div>
              <p className="text-sm text-muted-foreground">Age</p>
              <p className="text-lg font-bold">
                {formatAge(pack.metadata.creationTimestamp)}
              </p>
            </div>
          </CardContent>
        </Card>
      </div>

      {/* Installed Instances */}
      {pack.status?.installedPersonas && pack.status.installedPersonas.length > 0 && (
        <Card>
          <CardHeader>
            <CardTitle className="text-base">Installed Instances</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="space-y-2">
              {pack.status.installedPersonas.map((ip) => (
                <Link
                  key={ip.instanceName}
                  to={`/instances/${ip.instanceName}`}
                  className="flex items-center justify-between rounded-lg border p-3 hover:bg-white/5 transition-colors"
                >
                  <div className="flex items-center gap-3">
                    <span className="font-mono text-sm">{ip.instanceName}</span>
                    <Badge variant="outline" className="text-xs">{ip.name}</Badge>
                  </div>
                  <div className="flex items-center gap-2">
                    {ip.scheduleName && (
                      <Badge variant="secondary" className="text-xs">
                        <Clock className="h-3 w-3 mr-1" />
                        {ip.scheduleName}
                      </Badge>
                    )}
                  </div>
                </Link>
              ))}
            </div>
          </CardContent>
        </Card>
      )}

      {/* Auth refs */}
      {pack.spec.authRefs && pack.spec.authRefs.length > 0 && (
        <Card>
          <CardHeader>
            <CardTitle className="text-base">Auth References</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="flex flex-wrap gap-2">
              {pack.spec.authRefs.map((ref, i) => (
                <Badge key={i} variant="secondary">
                  {ref.provider}: {ref.secret}
                </Badge>
              ))}
            </div>
          </CardContent>
        </Card>
      )}

      {/* Personas */}
      <div className="space-y-4">
        <h2 className="text-lg font-semibold">
          Personas ({pack.spec.personas?.length ?? 0})
        </h2>
        {pack.spec.personas?.map((persona, i) => {
          const installed = pack.status?.installedPersonas?.some(
            (ip) => ip.name === persona.name
          );
          const isEditing = editingPersona === persona.name;
          return (
            <Card key={i}>
              <CardHeader>
                <div className="flex items-center justify-between">
                  <CardTitle className="text-base">
                    {persona.displayName || persona.name}
                  </CardTitle>
                  <div className="flex gap-2">
                    {isEditing ? (
                      <>
                        <Button
                          variant="ghost"
                          size="sm"
                          onClick={cancelEditing}
                          disabled={patchMutation.isPending}
                        >
                          <X className="h-4 w-4 mr-1" /> Cancel
                        </Button>
                        <Button
                          variant="default"
                          size="sm"
                          onClick={() => saveEditing(persona.name)}
                          disabled={patchMutation.isPending}
                        >
                          <Check className="h-4 w-4 mr-1" />
                          {patchMutation.isPending ? "Saving..." : "Save"}
                        </Button>
                      </>
                    ) : (
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => startEditing(persona)}
                      >
                        <Pencil className="h-4 w-4 mr-1" /> Edit
                      </Button>
                    )}
                    {installed && (
                      <Badge variant="default" className="text-xs">
                        Installed
                      </Badge>
                    )}
                    {persona.model && (
                      <Badge variant="outline" className="text-xs font-mono">
                        {persona.model}
                      </Badge>
                    )}
                  </div>
                </div>
                <CardDescription className="font-mono text-xs">
                  {persona.name}
                </CardDescription>
              </CardHeader>
              <CardContent className="space-y-4">
                {/* System prompt */}
                <div>
                  <p className="text-xs font-medium text-muted-foreground mb-1 flex items-center gap-1">
                    <Brain className="h-3 w-3" /> System Prompt
                  </p>
                  {isEditing ? (
                    <Textarea
                      value={editState.systemPrompt}
                      onChange={(e) =>
                        setEditState((prev) => ({ ...prev, systemPrompt: e.target.value }))
                      }
                      className="font-mono text-xs min-h-[120px]"
                    />
                  ) : (
                    <pre className="rounded bg-muted/50 p-3 text-xs whitespace-pre-wrap max-h-32 overflow-auto">
                      {persona.systemPrompt || "(no system prompt)"}
                    </pre>
                  )}
                </div>

                {/* Skills */}
                <div>
                  <p className="text-xs font-medium text-muted-foreground mb-1 flex items-center gap-1">
                    <Wrench className="h-3 w-3" /> Skills
                  </p>
                  {isEditing ? (
                    <div className="flex flex-wrap gap-1">
                      {availableSkills.map((sk) => {
                        const active = editState.skills.includes(sk);
                        return (
                          <Badge
                            key={sk}
                            variant={active ? "default" : "outline"}
                            className="text-xs cursor-pointer select-none"
                            onClick={() => toggleSkill(sk)}
                          >
                            {active ? "- " : "+ "}{sk}
                          </Badge>
                        );
                      })}
                      {editState.skills
                        .filter((sk) => !availableSkills.includes(sk))
                        .map((sk) => (
                          <Badge
                            key={sk}
                            variant="default"
                            className="text-xs cursor-pointer select-none"
                            onClick={() => toggleSkill(sk)}
                          >
                            - {sk}
                          </Badge>
                        ))}
                    </div>
                  ) : (
                    <div className="flex flex-wrap gap-1">
                      {persona.skills && persona.skills.length > 0 ? (
                        persona.skills.map((sk) => (
                          <Badge key={sk} variant="secondary" className="text-xs">
                            {sk}
                          </Badge>
                        ))
                      ) : (
                        <span className="text-xs text-muted-foreground">(no skills)</span>
                      )}
                    </div>
                  )}
                </div>

                {/* Grid for other metadata (read-only) */}
                <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
                  {/* Tool policy */}
                  {persona.toolPolicy && (
                    <div>
                      <p className="text-xs font-medium text-muted-foreground mb-1 flex items-center gap-1">
                        <Shield className="h-3 w-3" /> Tool Policy
                      </p>
                      <div className="flex flex-wrap gap-1">
                        {persona.toolPolicy.allow?.map((t) => (
                          <Badge key={t} variant="secondary" className="text-xs font-mono">
                            ✓ {t}
                          </Badge>
                        ))}
                        {persona.toolPolicy.deny?.map((t) => (
                          <Badge key={t} variant="destructive" className="text-xs font-mono">
                            ✗ {t}
                          </Badge>
                        ))}
                      </div>
                    </div>
                  )}

                  {/* Channels */}
                  {persona.channels && persona.channels.length > 0 && (
                    <div>
                      <p className="text-xs font-medium text-muted-foreground mb-1 flex items-center gap-1">
                        <MessageSquare className="h-3 w-3" /> Channels
                      </p>
                      <div className="flex flex-wrap gap-1">
                        {persona.channels.map((ch, ci) => (
                          <Badge
                            key={ci}
                            variant="outline"
                            className="text-xs capitalize"
                          >
                            {ch}
                          </Badge>
                        ))}
                      </div>
                    </div>
                  )}

                  {/* Schedule */}
                  {persona.schedule && (
                    <div>
                      <p className="text-xs font-medium text-muted-foreground mb-1 flex items-center gap-1">
                        <Clock className="h-3 w-3" /> Schedule
                      </p>
                      <div className="space-y-1">
                        <Badge variant="outline" className="text-xs font-mono">
                          {persona.schedule.cron}
                        </Badge>
                        <p className="text-xs text-muted-foreground capitalize">
                          {persona.schedule.type}
                        </p>
                      </div>
                    </div>
                  )}
                </div>

                {/* Memory */}
                {persona.memory && (
                  <div>
                    <p className="text-xs font-medium text-muted-foreground mb-1 flex items-center gap-1">
                      <Brain className="h-3 w-3" /> Memory Seeds
                    </p>
                    <pre className="rounded bg-muted/50 p-2 text-xs whitespace-pre-wrap max-h-24 overflow-auto">
                      {persona.memory.seeds?.join("\n") || "(empty)"}
                    </pre>
                  </div>
                )}
              </CardContent>
            </Card>
          );
        })}
      </div>

      {/* YAML */}
      <Card>
        <CardHeader>
          <div className="flex items-center justify-between">
            <CardTitle className="text-base">Resource YAML</CardTitle>
            <YamlButton
              yaml={personaPackYamlFromResource(pack)}
              title={`PersonaPack — ${pack.metadata.name}`}
            />
          </div>
        </CardHeader>
      </Card>

      {/* Conditions */}
      {pack.status?.conditions && pack.status.conditions.length > 0 && (
        <>
          <Separator />
          <div>
            <h2 className="text-lg font-semibold mb-3">Conditions</h2>
            <div className="space-y-2">
              {pack.status.conditions.map((cond, i) => (
                <div
                  key={i}
                  className="flex items-center justify-between rounded-lg border p-3 text-sm"
                >
                  <div className="flex items-center gap-2">
                    <Badge
                      variant={
                        cond.status === "True" ? "default" : "secondary"
                      }
                      className="text-xs"
                    >
                      {cond.type}
                    </Badge>
                    <span className="text-muted-foreground">
                      {cond.message}
                    </span>
                  </div>
                  <span className="text-xs text-muted-foreground">
                    {cond.reason}
                  </span>
                </div>
              ))}
            </div>
          </div>
        </>
      )}
    </div>
  );
}
