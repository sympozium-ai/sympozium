import { useState, useEffect } from "react";
import { useParams, Link, useSearchParams, useNavigate } from "react-router-dom";
import {
  useEnsemble,
  useActivateEnsemble,
  useDeleteEnsemble,
  useSkills,
} from "@/hooks/use-api";
import {
  YamlButton,
  ensembleYamlFromResource,
} from "@/components/yaml-panel";
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
  Settings,
  Trash2,
} from "lucide-react";
import { Breadcrumbs } from "@/components/breadcrumbs";
import { formatAge } from "@/lib/utils";
import { EnsembleCanvas } from "@/components/ensemble-canvas";
import {
  OnboardingWizard,
  type WizardResult,
} from "@/components/onboarding-wizard";

interface PersonaEditState {
  systemPrompt: string;
  skills: string[];
}

export function EnsembleDetailPage() {
  const { name } = useParams<{ name: string }>();
  const navigate = useNavigate();
  const { data: pack, isLoading } = useEnsemble(name || "");
  const { data: skillPacks } = useSkills();
  const patchMutation = useActivateEnsemble();
  const deleteMutation = useDeleteEnsemble();

  const [wizardOpen, setWizardOpen] = useState(false);

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
        agentConfigs: [
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

  function handleProviderChange(result: WizardResult) {
    if (!name) return;
    let skillParams: Record<string, Record<string, string>> | undefined;
    if (result.skills.includes("github-gitops") && result.githubRepo) {
      skillParams = { "github-gitops": { repo: result.githubRepo } };
    }
    patchMutation.mutate(
      {
        name,
        provider: result.provider,
        secretName: result.secretName || undefined,
        apiKey: result.apiKey || undefined,
        awsRegion: result.awsRegion || undefined,
        awsAccessKeyId: result.awsAccessKeyId || undefined,
        awsSecretAccessKey: result.awsSecretAccessKey || undefined,
        awsSessionToken: result.awsSessionToken || undefined,
        model: result.model,
        baseURL: result.baseURL || undefined,
        modelRef: result.modelRef || undefined,
        channels: result.channels.length > 0 ? result.channels : undefined,
        channelConfigs:
          Object.keys(result.channelConfigs).length > 0
            ? result.channelConfigs
            : undefined,
        heartbeatInterval: result.heartbeatInterval || undefined,
        skillParams,
        githubToken: result.githubToken || undefined,
        agentSandbox: result.agentSandboxEnabled
          ? {
              enabled: true,
              runtimeClass: result.agentSandboxRuntimeClass || "gvisor",
            }
          : undefined,
      },
      { onSuccess: () => setWizardOpen(false) },
    );
  }

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
          {pack.spec.enabled && (
            <Button
              variant="outline"
              size="sm"
              className="gap-1.5 text-xs"
              onClick={() => setWizardOpen(true)}
            >
              <Settings className="h-3.5 w-3.5" />
              Change Provider
            </Button>
          )}
          <YamlButton
            yaml={ensembleYamlFromResource(pack)}
            title={`Ensemble — ${pack.metadata.name}`}
          />
          <Button
            variant="ghost"
            size="sm"
            className="gap-1.5 text-xs text-destructive hover:text-destructive"
            disabled={deleteMutation.isPending}
            onClick={() => {
              if (
                window.confirm(
                  `Delete ensemble "${pack.metadata.name}"? This will remove all associated agents, schedules, and shared memory.`,
                )
              ) {
                deleteMutation.mutate(pack.metadata.name, {
                  onSuccess: () => navigate("/ensembles"),
                });
              }
            }}
            title="Delete ensemble"
          >
            <Trash2 className="h-3.5 w-3.5" />
            Delete
          </Button>
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
              <Badge
                variant="secondary"
                className="ml-1.5 text-[10px] px-1 py-0"
              >
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
                  ? `${pack.spec.agentConfigs?.length ?? 0} agents with ${pack.spec.relationships!.length} relationships`
                  : "Define relationships between agents to enable coordination. Drag to rearrange."}
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
                  ? "Shared memory pool active — all agents can access team knowledge."
                  : "Enable shared memory to let agents share knowledge across runs."}
              </CardDescription>
            </CardHeader>
            <CardContent>
              {pack.spec.sharedMemory?.enabled ? (
                <div className="space-y-3">
                  <div className="flex items-center gap-2 text-sm">
                    <Badge variant="default" className="text-xs">
                      Enabled
                    </Badge>
                    {pack.status?.sharedMemoryReady && (
                      <Badge variant="secondary" className="text-xs">
                        Ready
                      </Badge>
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
                            key={rule.agentConfig}
                            className="flex items-center gap-2 text-sm"
                          >
                            <Badge
                              variant="outline"
                              className="font-mono text-xs"
                            >
                              {rule.agentConfig}
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

                  {/* Membrane config */}
                  {pack.spec.sharedMemory.membrane && (
                    <div className="space-y-2 border-t pt-3 mt-3">
                      <p className="text-xs font-medium text-muted-foreground">
                        Synthetic Membrane
                      </p>
                      <div className="flex flex-wrap items-center gap-2 text-sm">
                        <Badge variant="outline" className="text-xs">
                          Visibility: {pack.spec.sharedMemory.membrane.defaultVisibility || "public"}
                        </Badge>
                        {pack.spec.sharedMemory.membrane.timeDecay?.ttl && (
                          <Badge variant="outline" className="text-xs">
                            TTL: {pack.spec.sharedMemory.membrane.timeDecay.ttl}
                          </Badge>
                        )}
                      </div>

                      {/* Token budget */}
                      {pack.spec.sharedMemory.membrane.tokenBudget && (
                        <div className="space-y-0.5">
                          <p className="text-xs text-muted-foreground">Token Budget</p>
                          <div className="flex items-center gap-2 text-sm">
                            <span className="font-mono text-xs">
                              {(pack.status?.tokenBudgetUsed ?? 0).toLocaleString()} / {(pack.spec.sharedMemory.membrane.tokenBudget.maxTokens ?? 0).toLocaleString()}
                            </span>
                            <Badge
                              variant={pack.spec.sharedMemory.membrane.tokenBudget.action === "warn" ? "secondary" : "destructive"}
                              className="text-xs"
                            >
                              {pack.spec.sharedMemory.membrane.tokenBudget.action || "halt"}
                            </Badge>
                          </div>
                        </div>
                      )}

                      {/* Circuit breaker */}
                      {pack.spec.sharedMemory.membrane.circuitBreaker && (
                        <div className="space-y-0.5">
                          <p className="text-xs text-muted-foreground">Circuit Breaker</p>
                          <div className="flex items-center gap-2 text-sm">
                            <Badge
                              variant={pack.status?.circuitBreakerOpen ? "destructive" : "secondary"}
                              className="text-xs"
                            >
                              {pack.status?.circuitBreakerOpen ? "OPEN" : "Closed"}
                            </Badge>
                            <span className="text-xs text-muted-foreground">
                              {pack.status?.consecutiveDelegateFailures ?? 0} / {pack.spec.sharedMemory.membrane.circuitBreaker.consecutiveFailures ?? 3} failures
                            </span>
                          </div>
                        </div>
                      )}

                      {/* Trust groups */}
                      {pack.spec.sharedMemory.membrane.trustGroups &&
                        pack.spec.sharedMemory.membrane.trustGroups.length > 0 && (
                        <div className="space-y-0.5">
                          <p className="text-xs text-muted-foreground">Trust Groups</p>
                          {pack.spec.sharedMemory.membrane.trustGroups.map((g) => (
                            <div key={g.name} className="flex items-center gap-1.5 text-sm">
                              <span className="text-xs font-medium">{g.name}:</span>
                              {g.agentConfigs.map((ac) => (
                                <Badge key={ac} variant="outline" className="font-mono text-xs">
                                  {ac}
                                </Badge>
                              ))}
                            </div>
                          ))}
                        </div>
                      )}

                      {/* Permeability rules */}
                      {pack.spec.sharedMemory.membrane.permeability &&
                        pack.spec.sharedMemory.membrane.permeability.length > 0 && (
                        <div className="space-y-0.5">
                          <p className="text-xs text-muted-foreground">Permeability</p>
                          {pack.spec.sharedMemory.membrane.permeability.map((rule) => (
                            <div key={rule.agentConfig} className="flex items-center gap-1.5 text-sm">
                              <Badge variant="outline" className="font-mono text-xs">
                                {rule.agentConfig}
                              </Badge>
                              <Badge
                                variant={
                                  rule.defaultVisibility === "public"
                                    ? "default"
                                    : rule.defaultVisibility === "trusted"
                                      ? "secondary"
                                      : "outline"
                                }
                                className="text-xs"
                              >
                                {rule.defaultVisibility || "public"}
                              </Badge>
                            </div>
                          ))}
                        </div>
                      )}
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
                  <p className="text-sm text-muted-foreground">Agents</p>
                  <p className="text-2xl font-bold">
                    {pack.status?.agentConfigCount ??
                      pack.spec.agentConfigs?.length ??
                      0}
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
          {pack.status?.installedAgents &&
            pack.status.installedAgents.length > 0 && (
              <Card>
                <CardHeader>
                  <CardTitle className="text-base">
                    Installed Instances
                  </CardTitle>
                </CardHeader>
                <CardContent>
                  <div className="space-y-2">
                    {pack.status.installedAgents.map((ip) => (
                      <Link
                        key={ip.agentName}
                        to={`/agents/${ip.agentName}`}
                        className="flex items-center justify-between rounded-lg border p-3 hover:bg-white/5 transition-colors"
                      >
                        <div className="flex items-center gap-3">
                          <span className="font-mono text-sm">
                            {ip.agentName}
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

          {/* Agents */}
          <div className="space-y-4">
            <h2 className="text-lg font-semibold">
              Agents ({pack.spec.agentConfigs?.length ?? 0})
            </h2>
            {pack.spec.agentConfigs?.map((persona, i) => {
              const installed = pack.status?.installedAgents?.some(
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
                          <Badge
                            variant="outline"
                            className="text-xs font-mono"
                          >
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
                            <Badge
                              variant="outline"
                              className="text-xs font-mono"
                            >
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
        </TabsContent>
      </Tabs>

      <OnboardingWizard
        open={wizardOpen}
        onClose={() => setWizardOpen(false)}
        mode="persona"
        targetName={pack.metadata.name}
        agentConfigCount={pack.spec.agentConfigs?.length ?? 0}
        availableSkills={(skillPacks || []).map((s) => s.metadata.name)}
        defaults={{
          provider: pack.spec.authRefs?.[0]?.provider || "",
          secretName: pack.spec.authRefs?.[0]?.secret || "",
          model: pack.spec.agentConfigs?.[0]?.model || "",
          skills: Array.from(
            new Set((pack.spec.agentConfigs || []).flatMap((p) => p.skills || [])),
          ),
          channelConfigs: pack.spec.channelConfigs || {},
          channels:
            pack.spec.agentConfigs?.[0]?.channels ||
            Object.keys(pack.spec.channelConfigs || {}),
        }}
        onComplete={handleProviderChange}
        isPending={patchMutation.isPending}
      />
    </div>
  );
}
