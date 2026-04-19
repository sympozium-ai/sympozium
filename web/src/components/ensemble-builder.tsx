/**
 * EnsembleBuilder — canvas-first visual builder for creating new Ensembles.
 *
 * Flow:
 * 1. Provider setup (provider + API key / base URL) — gates the canvas
 * 2. Canvas: add persona nodes, drag edges, configure via side panels
 * 3. Save → POST /api/v1/ensembles
 *
 * The provider context flows down to PersonaConfigPanel so the model
 * selector can show provider-specific model lists.
 */

import { useCallback, useState, useMemo } from "react";
import {
  ReactFlow,
  Background,
  Controls,
  MiniMap,
  type Node,
  type Edge,
  type Connection,
  Handle,
  Position,
  type NodeProps,
  useNodesState,
  useEdgesState,
  MarkerType,
  addEdge,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Plus, Settings, Save, ArrowRight, Database } from "lucide-react";
import type {
  PersonaSpec,
  PersonaRelationship,
  SharedMemorySpec,
} from "@/lib/api";
import { PersonaConfigPanel } from "@/components/persona-config-panel";
import {
  EnsembleSettingsPanel,
  type EnsembleSettings,
} from "@/components/ensemble-settings-panel";
import { PROVIDERS } from "@/components/onboarding-wizard";
import { useCreateEnsemble } from "@/hooks/use-api";
import { useNavigate } from "react-router-dom";

// ── Provider context shared with PersonaConfigPanel ────────────────────────

export interface ProviderContext {
  provider: string;
  apiKey: string;
  baseURL: string;
}

// ── Node data ──────────────────────────────────────────────────────────────

interface BuilderNodeData {
  persona: PersonaSpec;
  isConfigured: boolean;
  label: string;
  [key: string]: unknown;
}

// ── Custom node ────────────────────────────────────────────────────────────

function BuilderNode({ data }: NodeProps<Node<BuilderNodeData>>) {
  const { persona, isConfigured } = data;

  return (
    <div
      className={`rounded-lg border bg-card shadow-md px-4 py-3 min-w-[180px] max-w-[220px] cursor-pointer transition-all
        ${isConfigured ? "border-border/60" : "border-dashed border-muted-foreground/40"}`}
    >
      <Handle
        type="target"
        position={Position.Top}
        className="!bg-muted-foreground !w-2 !h-2"
      />

      <div className="flex items-center justify-between gap-2 mb-1">
        <span className="font-semibold text-sm truncate">
          {persona.displayName || persona.name || "Unnamed"}
        </span>
      </div>

      <p className="text-[10px] text-muted-foreground font-mono mb-1.5 truncate">
        {persona.name || "click to configure"}
      </p>

      {persona.model && (
        <Badge
          variant="outline"
          className="text-[10px] font-mono mb-1.5 block w-fit"
        >
          {persona.model}
        </Badge>
      )}

      <div className="flex flex-wrap gap-0.5">
        {persona.skills?.slice(0, 3).map((sk) => (
          <Badge key={sk} variant="secondary" className="text-[9px] px-1 py-0">
            {sk}
          </Badge>
        ))}
        {(persona.skills?.length ?? 0) > 3 && (
          <Badge variant="secondary" className="text-[9px] px-1 py-0">
            +{(persona.skills?.length ?? 0) - 3}
          </Badge>
        )}
      </div>

      {!isConfigured && (
        <p className="text-[9px] text-amber-500/80 mt-1.5 italic">
          Click to configure
        </p>
      )}

      <Handle
        type="source"
        position={Position.Bottom}
        className="!bg-muted-foreground !w-2 !h-2"
      />
    </div>
  );
}

const nodeTypes = { builder: BuilderNode };

// ── Edge styling ───────────────────────────────────────────────────────────

const EDGE_TYPES = ["delegation", "sequential", "supervision"] as const;

const edgeStyles: Record<string, { stroke: string; strokeDasharray?: string }> =
  {
    delegation: { stroke: "#3b82f6" },
    sequential: { stroke: "#f59e0b", strokeDasharray: "6 3" },
    supervision: { stroke: "#6b7280", strokeDasharray: "2 4" },
  };

const edgeLabels: Record<string, string> = {
  delegation: "delegates to",
  sequential: "then",
  supervision: "supervises",
};

// ── Provider setup step ────────────────────────────────────────────────────

function ProviderSetup({
  onComplete,
}: {
  onComplete: (ctx: ProviderContext) => void;
}) {
  const [provider, setProvider] = useState("");
  const [apiKey, setApiKey] = useState("");
  const [baseURL, setBaseURL] = useState("");

  const selectedProvider = PROVIDERS.find((p) => p.value === provider);
  const isLocal =
    provider === "ollama" ||
    provider === "lm-studio" ||
    provider === "llama-server" ||
    provider === "unsloth";
  const needsKey = !isLocal && provider !== "custom" && provider !== "";
  const canContinue =
    provider !== "" &&
    (!needsKey || apiKey !== "") &&
    (!isLocal || baseURL !== "");

  return (
    <div className="flex items-center justify-center h-[calc(100vh-12rem)]">
      <div className="w-full max-w-md space-y-6">
        <div className="text-center space-y-2">
          <h2 className="text-xl font-bold">Choose AI Provider</h2>
          <p className="text-sm text-muted-foreground">
            Select the provider your ensemble will use. This determines which
            models are available for your personas.
          </p>
        </div>

        {/* Provider grid */}
        <div className="grid grid-cols-3 gap-2">
          {PROVIDERS.map((p) => {
            const Icon = p.icon;
            return (
              <button
                key={p.value}
                onClick={() => {
                  setProvider(p.value);
                  setBaseURL(p.defaultBaseURL || "");
                }}
                className={`flex flex-col items-center gap-1.5 rounded-lg border p-3 text-xs transition-colors
                  ${
                    provider === p.value
                      ? "border-blue-500/60 bg-blue-500/10 text-blue-400"
                      : "border-border/50 hover:border-border hover:bg-white/5"
                  }`}
              >
                <Icon className="h-5 w-5" />
                {p.label}
              </button>
            );
          })}
        </div>

        {/* API key (for cloud providers) */}
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

        {/* Base URL (for local providers) */}
        {(isLocal || provider === "custom") && (
          <div className="space-y-1.5">
            <Label className="text-xs">Base URL</Label>
            <Input
              value={baseURL}
              onChange={(e) => setBaseURL(e.target.value)}
              placeholder="http://localhost:1234/v1"
              className="h-8 text-sm font-mono"
            />
          </div>
        )}

        <Button
          className="w-full"
          disabled={!canContinue}
          onClick={() => onComplete({ provider, apiKey, baseURL })}
        >
          Continue to Builder
          <ArrowRight className="h-4 w-4 ml-2" />
        </Button>

        {/* Skip for now option */}
        <button
          onClick={() =>
            onComplete({ provider: "openai", apiKey: "", baseURL: "" })
          }
          className="w-full text-center text-xs text-muted-foreground hover:text-foreground transition-colors"
        >
          Skip — I'll configure the provider when I activate the ensemble
        </button>
      </div>
    </div>
  );
}

// ── Main builder ───────────────────────────────────────────────────────────

interface EnsembleBuilderProps {
  initialPersonas?: PersonaSpec[];
  initialRelationships?: PersonaRelationship[];
  initialSettings?: Partial<EnsembleSettings>;
}

export function EnsembleBuilder({
  initialPersonas,
  initialRelationships,
  initialSettings,
}: EnsembleBuilderProps) {
  const navigate = useNavigate();
  const createMutation = useCreateEnsemble();

  // ── Provider gate ──────────────────────────────────────────────────────
  const [providerCtx, setProviderCtx] = useState<ProviderContext | null>(null);

  // ── State ──────────────────────────────────────────────────────────────

  const [personas, setPersonas] = useState<PersonaSpec[]>(
    initialPersonas || [],
  );
  const [relationships, setRelationships] = useState<PersonaRelationship[]>(
    initialRelationships || [],
  );
  const [settings, setSettings] = useState<EnsembleSettings>({
    name: initialSettings?.name || "",
    description: initialSettings?.description || "",
    category: initialSettings?.category || "",
    workflowType: initialSettings?.workflowType || "autonomous",
    sharedMemory: initialSettings?.sharedMemory || {
      enabled: true,
      storageSize: "1Gi",
    },
  });

  const [selectedPersona, setSelectedPersona] = useState<string | null>(null);
  const [showSettings, setShowSettings] = useState(false);
  const [pendingConnection, setPendingConnection] = useState<Connection | null>(
    null,
  );

  // ── Show provider setup if not configured ──────────────────────────────

  if (!providerCtx) {
    return <ProviderSetup onComplete={setProviderCtx} />;
  }

  // ── From here, provider is configured — render the canvas ──────────────

  return (
    <BuilderCanvas
      providerCtx={providerCtx}
      personas={personas}
      setPersonas={setPersonas}
      relationships={relationships}
      setRelationships={setRelationships}
      settings={settings}
      setSettings={setSettings}
      selectedPersona={selectedPersona}
      setSelectedPersona={setSelectedPersona}
      showSettings={showSettings}
      setShowSettings={setShowSettings}
      pendingConnection={pendingConnection}
      setPendingConnection={setPendingConnection}
      createMutation={createMutation}
      navigate={navigate}
    />
  );
}

// ── Canvas (extracted to avoid hooks-after-early-return) ────────────────

function BuilderCanvas({
  providerCtx,
  personas,
  setPersonas,
  relationships,
  setRelationships,
  settings,
  setSettings,
  selectedPersona,
  setSelectedPersona,
  showSettings,
  setShowSettings,
  pendingConnection,
  setPendingConnection,
  createMutation,
  navigate,
}: {
  providerCtx: ProviderContext;
  personas: PersonaSpec[];
  setPersonas: React.Dispatch<React.SetStateAction<PersonaSpec[]>>;
  relationships: PersonaRelationship[];
  setRelationships: React.Dispatch<React.SetStateAction<PersonaRelationship[]>>;
  settings: EnsembleSettings;
  setSettings: React.Dispatch<React.SetStateAction<EnsembleSettings>>;
  selectedPersona: string | null;
  setSelectedPersona: React.Dispatch<React.SetStateAction<string | null>>;
  showSettings: boolean;
  setShowSettings: React.Dispatch<React.SetStateAction<boolean>>;
  pendingConnection: Connection | null;
  setPendingConnection: React.Dispatch<React.SetStateAction<Connection | null>>;
  createMutation: ReturnType<typeof useCreateEnsemble>;
  navigate: ReturnType<typeof useNavigate>;
}) {
  const initialNodes = useMemo(() => {
    const cols = Math.max(2, Math.ceil(Math.sqrt(personas.length || 1)));
    return personas.map((p, i) => ({
      id: p.name || `persona-${i}`,
      type: "builder" as const,
      position: { x: (i % cols) * 260, y: Math.floor(i / cols) * 200 },
      data: {
        persona: p,
        isConfigured: !!(p.name && p.systemPrompt),
        label: p.displayName || p.name || "Unnamed",
      },
    }));
  }, [personas]);

  const initialEdges = useMemo(
    () =>
      relationships.map((rel, i) => {
        const style = edgeStyles[rel.type] || edgeStyles.delegation;
        return {
          id: `e-${i}-${rel.source}-${rel.target}`,
          source: rel.source,
          target: rel.target,
          label: edgeLabels[rel.type] || rel.type,
          style,
          data: { relType: rel.type },
          markerEnd:
            rel.type !== "supervision"
              ? { type: MarkerType.ArrowClosed, color: style.stroke }
              : undefined,
          labelStyle: { fontSize: 10, fill: "#9ca3af" },
          animated: rel.type === "delegation",
        } as Edge;
      }),
    [relationships],
  );

  const [nodes, setNodes, onNodesChange] = useNodesState(initialNodes);
  const [edges, setEdges, onEdgesChange] = useEdgesState(initialEdges);

  const onNodeClick = useCallback(
    (_: React.MouseEvent, node: Node) => {
      setSelectedPersona(node.id);
      setShowSettings(false);
    },
    [setSelectedPersona, setShowSettings],
  );

  const onConnect = useCallback(
    (connection: Connection) => setPendingConnection(connection),
    [setPendingConnection],
  );

  function confirmEdgeType(type: (typeof EDGE_TYPES)[number]) {
    if (!pendingConnection) return;
    const style = edgeStyles[type];
    const newEdge: Edge = {
      id: `e-${edges.length}-${pendingConnection.source}-${pendingConnection.target}`,
      source: pendingConnection.source!,
      target: pendingConnection.target!,
      label: edgeLabels[type],
      style,
      data: { relType: type },
      markerEnd:
        type !== "supervision"
          ? { type: MarkerType.ArrowClosed, color: style.stroke }
          : undefined,
      labelStyle: { fontSize: 10, fill: "#9ca3af" },
      animated: type === "delegation",
    };
    setEdges((eds) => addEdge(newEdge, eds));
    setRelationships((prev) => [
      ...prev,
      {
        source: pendingConnection.source!,
        target: pendingConnection.target!,
        type,
      },
    ]);
    setPendingConnection(null);
  }

  function addPersona() {
    const idx = personas.length + 1;
    const name = `persona-${idx}`;
    const defaultModel =
      PROVIDERS.find((p) => p.value === providerCtx.provider)?.defaultModel ||
      "";
    const newPersona: PersonaSpec = {
      name,
      displayName: "",
      systemPrompt: "",
      model: defaultModel,
      skills: ["memory"],
    };
    setPersonas((prev) => [...prev, newPersona]);
    const cols = Math.max(2, Math.ceil(Math.sqrt(personas.length + 1)));
    const i = personas.length;
    setNodes((prev) => [
      ...prev,
      {
        id: name,
        type: "builder" as const,
        position: { x: (i % cols) * 260, y: Math.floor(i / cols) * 200 },
        data: { persona: newPersona, isConfigured: false, label: name },
      },
    ]);
    setSelectedPersona(name);
    setShowSettings(false);
  }

  function updatePersona(updated: PersonaSpec) {
    const oldName = selectedPersona;
    setPersonas((prev) => prev.map((p) => (p.name === oldName ? updated : p)));
    setNodes((prev) =>
      prev.map((n) =>
        n.id === oldName
          ? {
              ...n,
              id: updated.name,
              data: {
                persona: updated,
                isConfigured: !!(updated.name && updated.systemPrompt),
                label: updated.displayName || updated.name,
              },
            }
          : n,
      ),
    );
    if (oldName !== updated.name) {
      setRelationships((prev) =>
        prev.map((r) => ({
          ...r,
          source: r.source === oldName ? updated.name : r.source,
          target: r.target === oldName ? updated.name : r.target,
        })),
      );
      setEdges((prev) =>
        prev.map((e) => ({
          ...e,
          source: e.source === oldName ? updated.name : e.source,
          target: e.target === oldName ? updated.name : e.target,
        })),
      );
    }
    setSelectedPersona(updated.name);
  }

  function deletePersona() {
    if (!selectedPersona) return;
    setPersonas((prev) => prev.filter((p) => p.name !== selectedPersona));
    setRelationships((prev) =>
      prev.filter(
        (r) => r.source !== selectedPersona && r.target !== selectedPersona,
      ),
    );
    setNodes((prev) => prev.filter((n) => n.id !== selectedPersona));
    setEdges((prev) =>
      prev.filter(
        (e) => e.source !== selectedPersona && e.target !== selectedPersona,
      ),
    );
    setSelectedPersona(null);
  }

  const selectedPersonaData = personas.find((p) => p.name === selectedPersona);

  const canSave =
    settings.name &&
    personas.length > 0 &&
    personas.every((p) => p.name && p.systemPrompt);

  function handleSave() {
    createMutation.mutate(
      {
        name: settings.name,
        description: settings.description,
        category: settings.category,
        workflowType: settings.workflowType,
        personas,
        relationships: relationships.length > 0 ? relationships : undefined,
        sharedMemory: settings.sharedMemory?.enabled
          ? settings.sharedMemory
          : undefined,
      },
      { onSuccess: () => navigate(`/ensembles/${settings.name}`) },
    );
  }

  const providerLabel =
    PROVIDERS.find((p) => p.value === providerCtx.provider)?.label ||
    providerCtx.provider;

  return (
    <div className="flex h-[calc(100vh-8rem)]">
      <div className="flex-1 flex flex-col">
        {/* Toolbar */}
        <div className="flex items-center gap-2 px-4 py-2 border-b border-border bg-card">
          <Input
            value={settings.name}
            onChange={(e) =>
              setSettings((s) => ({
                ...s,
                name: e.target.value.toLowerCase().replace(/[^a-z0-9-]/g, "-"),
              }))
            }
            placeholder="ensemble-name (required)"
            className="h-8 w-56 text-sm font-mono"
          />
          <Button size="sm" variant="outline" onClick={addPersona}>
            <Plus className="h-3.5 w-3.5 mr-1" />
            Add Persona
          </Button>
          <Button
            size="sm"
            variant={showSettings ? "default" : "outline"}
            onClick={() => {
              setShowSettings(!showSettings);
              setSelectedPersona(null);
            }}
          >
            <Settings className="h-3.5 w-3.5 mr-1" />
            Settings
          </Button>
          <button
            onClick={() =>
              setSettings((s) => ({
                ...s,
                sharedMemory: s.sharedMemory?.enabled
                  ? null
                  : { enabled: true, storageSize: "1Gi" },
              }))
            }
            className={`flex items-center gap-1 rounded-md border px-2 py-1 text-[10px] transition-colors
              ${
                settings.sharedMemory?.enabled
                  ? "border-blue-500/40 bg-blue-500/10 text-blue-400"
                  : "border-border/50 text-muted-foreground hover:border-border"
              }`}
            title="Toggle shared workflow memory"
          >
            <Database className="h-3 w-3" />
            Shared Memory {settings.sharedMemory?.enabled ? "ON" : "OFF"}
          </button>
          <div className="flex-1" />
          <Badge variant="outline" className="text-[10px] font-mono">
            {providerLabel}
          </Badge>
          <Button
            size="sm"
            onClick={handleSave}
            disabled={!canSave || createMutation.isPending}
          >
            <Save className="h-3.5 w-3.5 mr-1" />
            {createMutation.isPending ? "Saving..." : "Save Ensemble"}
          </Button>
        </div>

        {/* Canvas */}
        <div className="flex-1">
          <ReactFlow
            nodes={nodes}
            edges={edges}
            onNodesChange={onNodesChange}
            onEdgesChange={onEdgesChange}
            onConnect={onConnect}
            onNodeClick={onNodeClick}
            nodeTypes={nodeTypes}
            fitView
            proOptions={{ hideAttribution: true }}
          >
            <Background />
            <Controls />
            <MiniMap />
          </ReactFlow>
        </div>

        {/* Hints */}
        <div className="px-4 py-1.5 border-t border-border bg-muted/30 text-[10px] text-muted-foreground flex items-center gap-4">
          <span>Click a node to configure</span>
          <span>Drag between nodes to create relationships</span>
          {!canSave && (
            <span className="text-amber-500">
              {!settings.name
                ? "Enter an ensemble name to save"
                : personas.length === 0
                  ? "Add at least one persona"
                  : "All personas need a name and system prompt"}
            </span>
          )}
        </div>
      </div>

      {/* Edge type picker modal */}
      {pendingConnection && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50">
          <div className="bg-card border border-border rounded-lg p-4 space-y-3 shadow-xl">
            <h4 className="text-sm font-semibold">Relationship Type</h4>
            <p className="text-xs text-muted-foreground">
              {pendingConnection.source} &rarr; {pendingConnection.target}
            </p>
            <div className="flex gap-2">
              {EDGE_TYPES.map((type) => (
                <Button
                  key={type}
                  size="sm"
                  variant="outline"
                  onClick={() => confirmEdgeType(type)}
                  className="capitalize"
                >
                  {type}
                </Button>
              ))}
            </div>
            <Button
              size="sm"
              variant="ghost"
              onClick={() => setPendingConnection(null)}
              className="w-full"
            >
              Cancel
            </Button>
          </div>
        </div>
      )}

      {/* Side panels */}
      {selectedPersonaData && (
        <PersonaConfigPanel
          persona={selectedPersonaData}
          providerCtx={providerCtx}
          onSave={updatePersona}
          onDelete={deletePersona}
          onClose={() => setSelectedPersona(null)}
        />
      )}
      {showSettings && (
        <EnsembleSettingsPanel
          settings={settings}
          onChange={setSettings}
          onClose={() => setShowSettings(false)}
        />
      )}
    </div>
  );
}
