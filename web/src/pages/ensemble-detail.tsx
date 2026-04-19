import { useState, useEffect } from "react";
import { useParams, Link, useSearchParams } from "react-router-dom";
import {
  useEnsemble,
  useActivateEnsemble,
  useSkills,
} from "@/hooks/use-api";
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
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";
import {
  Clock,
  Wrench,
  MessageSquare,
  Brain,
  Shield,
  Pencil,
  X,
  Check,
  Workflow,
  Database,
} from "lucide-react";
import { Breadcrumbs } from "@/components/breadcrumbs";
import { formatAge } from "@/lib/utils";
import {
  YamlButton,
  ensembleYamlFromResource,
} from "@/components/yaml-panel";
import { EnsembleCanvas } from "@/components/ensemble-canvas";

interface PersonaEditState {
  systemPrompt: string;
  skills: string[];
}

export function EnsembleDetailPage() {
  const { name } = useParams<{ name: string }>();
  const { data: pack, isLoading } = useEnsemble(name || "");
  const { data: skillPacks } = useSkills();
  const patchMutation = useActivateEnsemble();

  // Track which persona is being edited (by name), and its draft state
  const [editingPersona, setEditingPersona] = useState<string | null>(null);
  const [editState, setEditState] = useState<PersonaEditState>({
    systemPrompt: "",
    skills: [],
  });

  // Reset edit state when pack data changes
  useEffect(() => {
    setEditingPersona(null);
  }, [pack?.metadata.name]);

  const startEditing = (persona: {
    name: string;
    systemPrompt: string;
    skills?: string[];
  }) => {
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
      { onSuccess: () => setEditingPersona(null) },
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

  const [searchParams, setSearchParams] = useSearchParams();
  const activeTab = searchParams.get("tab") || "overview";
  const setTab = (tab: string) => setSearchParams({ tab }, { replace: true });

  if (isLoading) {
    return (
      <div className="space-y-4">
        <Skeleton className="h-8 w-64" />
        <Skeleton className="h-64 w-full" />
      </div>
    );
  }

  if (!pack) {
    return <p className="text-muted-foreground">Ensemble not found</p>;
  }

  const hasRelationships =
    pack.spec.relationships && pack.spec.relationships.length > 0;

  return (
    <div className="space-y-6">
      <div className="space-y-1">
        <Breadcrumbs
          items={[
            { label: "Ensembles", to: "/ensembles" },
            { label: pack.metadata.name },
          ]}
        />
        <h1 className="text-2xl font-bold font-mono">{pack.metadata.name}</h1>
        <div className="flex items-center gap-2 text-sm text-muted-foreground">
          {pack.spec.description && <span>{pack.spec.description}</span>}
          <StatusBadge phase={pack.status?.phase} />
          {pack.spec.category && (
            <Badge variant="outline" className="capitalize">
              {pack.spec.category}
            </Badge>
          )}
          {pack.spec.version && (
            <Badge variant="secondary">v{pack.spec.version}</Badge>
          )}
          {pack.spec.workflowType &&
            pack.spec.workflowType !== "autonomous" && (
              <Badge variant="outline" className="capitalize">
                <Workflow className="h-3 w-3 mr-1" />
                {pack.spec.workflowType}
              </Badge>
            )}
        </div>
      </div>

      <Tabs value={activeTab} onValueChange={setTab}>
        <TabsList>
          <TabsTrigger value="overview">Overview</TabsTrigger>
          <TabsTrigger value="workflow">
            Workflow
            {hasRelationships && (
              <Badge variant="secondary" className="ml-1.5 text-[10px] px-1 py-0">
                {pack.spec.relationships!.length}
              </Badge>
            )}
          </TabsTrigger>
        </TabsList>

        <TabsContent value="workflow" className="mt-4 space-y-4">
          <Card>
            <CardHeader>
              <CardTitle className="text-base">Persona Workflow</CardTitle>
              <CardDescription>
                {hasRelationships
                  ? `${pack.spec.personas?.length ?? 0} personas with ${pack.spec.relationships!.length} relationships`
                  : "Define relationships between personas to enable coordination. Drag to rearrange."}
              </CardDescription>
            </CardHeader>
            <CardContent>
              <EnsembleCanvas pack={pack} />
            </CardContent>
          </Card>

          {/* Relationship table */}
          {hasRelationships && (
            <Card>
              <CardHeader>
                <CardTitle className="text-base">Relationships</CardTitle>
              </CardHeader>
              <CardContent>
                <div className="space-y-2">
                  {pack.spec.relationships!.map((rel, i) => (
                    <div
                      key={i}
                      className="flex items-center gap-3 rounded-lg border p-3 text-sm"
                    >
                      <Badge variant="outline" className="font-mono text-xs">
                        {rel.source}
                      </Badge>
                      <Badge
                        variant={
                          rel.type === "delegation"
                            ? "default"
                            : rel.type === "sequential"
                              ? "secondary"
                              : "outline"
                        }
                        className="text-xs"
                      >
                        {rel.type}
                      </Badge>
                      <Badge variant="outline" className="font-mono text-xs">
                        {rel.target}
                      </Badge>
                      {rel.timeout && (
                        <span className="text-xs text-muted-foreground ml-auto">
                          timeout: {rel.timeout}
                        </span>
                      )}
                      {rel.condition && (
                        <span className="text-xs text-muted-foreground">
                          {rel.condition}
                        </span>
                      )}
                    </div>
                  ))}
                </div>
              </CardContent>
            </Card>
          )}

          {/* Shared Memory */}
          <Card>
            <CardHeader>
              <CardTitle className="text-base flex items-center gap-2">
                <Database className="h-4 w-4" />
                Shared Workflow Memory
              </CardTitle>
              <CardDescription>
                {pack.spec.sharedMemory?.enabled
                  ? "Shared memory pool active — all personas can access team knowledge."
                  : "Enable shared memory to let personas share knowledge across runs."}
              </CardDescription>
            </CardHeader>
            <CardContent>
              {pack.spec.sharedMemory?.enabled ? (
                <div className="space-y-3">
                  <div className="flex items-center gap-2 text-sm">
                    <Badge variant="default" className="text-xs">Enabled</Badge>
                    {pack.status?.sharedMemoryReady && (
                      <Badge variant="secondary" className="text-xs">Ready</Badge>
                    )}
                    {pack.spec.sharedMemory.storageSize && (
                      <span className="text-muted-foreground">
                        Storage: {pack.spec.sharedMemory.storageSize}
                      </span>
                    )}
                  </div>
                  {pack.spec.sharedMemory.accessRules &&
                    pack.spec.sharedMemory.accessRules.length > 0 && (
                      <div className="space-y-1">
                        <p className="text-xs font-medium text-muted-foreground">
                          Access Rules
                        </p>
                        {pack.spec.sharedMemory.accessRules.map((rule) => (
                          <div
                            key={rule.persona}
                            className="flex items-center gap-2 text-sm"
                          >
                            <Badge variant="outline" className="font-mono text-xs">
                              {rule.persona}
                            </Badge>
                            <Badge
                              variant={
                                rule.access === "read-write"
                                  ? "default"
                                  : "secondary"
                              }
                              className="text-xs"
                            >
                              {rule.access}
                            </Badge>
                          </div>
                        ))}
                      </div>
                    )}
                </div>
              ) : (
                <p className="text-sm text-muted-foreground">
                  Shared memory is not configured for this pack.
                </p>
              )}
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="overview" className="mt-4 space-y-6">

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
      {pack.status?.installedPersonas &&
        pack.status.installedPersonas.length > 0 && (
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
                      <span className="font-mono text-sm">
                        {ip.instanceName}
                      </span>
                      <Badge variant="outline" className="text-xs">
                        {ip.name}
                      </Badge>
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
            (ip) => ip.name === persona.name,
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
                        setEditState((prev) => ({
                          ...prev,
                          systemPrompt: e.target.value,
                        }))
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
                            {active ? "- " : "+ "}
                            {sk}
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
                          <Badge
                            key={sk}
                            variant="secondary"
                            className="text-xs"
                          >
                            {sk}
                          </Badge>
                        ))
                      ) : (
                        <span className="text-xs text-muted-foreground">
                          (no skills)
                        </span>
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
                          <Badge
                            key={t}
                            variant="secondary"
                            className="text-xs font-mono"
                          >
                            ✓ {t}
                          </Badge>
                        ))}
                        {persona.toolPolicy.deny?.map((t) => (
                          <Badge
                            key={t}
                            variant="destructive"
                            className="text-xs font-mono"
                          >
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
              yaml={ensembleYamlFromResource(pack)}
              title={`Ensemble — ${pack.metadata.name}`}
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
                      variant={cond.status === "True" ? "default" : "secondary"}
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

        </TabsContent>
      </Tabs>
    </div>
  );
}
