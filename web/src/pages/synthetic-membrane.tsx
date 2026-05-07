/**
 * SyntheticMembranePage — explains the synthetic membrane architecture:
 * how Sympozium implements shared-medium coordination for multi-agent systems.
 */

import { ScrollArea } from "@/components/ui/scroll-area";
import { Badge } from "@/components/ui/badge";
import { ExternalLink } from "lucide-react";

// ── SVG Diagrams ─────────────────────────────────────────────────────────────

/** Six-layer architecture diagram. */
function ArchitectureSvg() {
  const layers = [
    { id: "L-1", label: "Governance", sub: "Circuit breakers \u00b7 Human override \u00b7 Dissent surface \u00b7 Accountability", color: "#ef4444", bg: "#ef444418" },
    { id: "L0", label: "Discovery / Registry", sub: "Behavioural index \u00b7 Execution traces \u00b7 Identity \u00b7 Reputation", color: "#f59e0b", bg: "#f59e0b18" },
    { id: "L1", label: "Permeability", sub: "Default-deny gates \u00b7 SVAF field-level filters \u00b7 Cost-benefit analysis", color: "#10b981", bg: "#10b98118" },
    { id: "L2", label: "Shared Medium", sub: "CRDT document store \u00b7 Immutable event log \u00b7 CAT7 CMBs \u00b7 Lineage", color: "#3b82f6", bg: "#3b82f618" },
    { id: "L3", label: "Coordination", sub: "Quorum sensing \u00b7 Task claim/release \u00b7 Consensus \u00b7 Span of control", color: "#8b5cf6", bg: "#8b5cf618" },
    { id: "Im", label: "Immune / Observability", sub: "Anomaly detection \u00b7 Cytokine gossip \u00b7 OTel traces \u00b7 Memory cells", color: "#ec4899", bg: "#ec489918" },
  ];
  const lh = 62;
  const pad = 24;
  const w = 680;
  const totalH = layers.length * lh + pad * 2 + 100;

  return (
    <svg viewBox={`0 0 ${w} ${totalH}`} className="w-full max-w-2xl mx-auto" role="img" aria-label="Synthetic membrane six-layer architecture">
      <defs>
        <filter id="glow">
          <feGaussianBlur stdDeviation="2" result="blur" />
          <feMerge><feMergeNode in="blur" /><feMergeNode in="SourceGraphic" /></feMerge>
        </filter>
      </defs>

      {/* Layers */}
      {layers.map((l, i) => {
        const y = pad + i * lh;
        const isImmune = l.id === "Im";
        return (
          <g key={l.id}>
            <rect
              x={40} y={y} width={w - 80} height={lh - 6} rx={8}
              fill={l.bg} stroke={l.color} strokeWidth={1.5}
              strokeDasharray={isImmune ? "6 3" : undefined}
            />
            <text x={60} y={y + 22} fill={l.color} fontSize={11} fontWeight={700} fontFamily="monospace">
              {l.id}
            </text>
            <text x={110} y={y + 22} fill={l.color} fontSize={13} fontWeight={600}>
              {l.label}
            </text>
            <text x={110} y={y + 40} fill="#9ca3af" fontSize={10}>
              {l.sub}
            </text>
          </g>
        );
      })}

      {/* Agent boxes below */}
      {[0, 1, 2].map((i) => {
        const ax = 160 + i * 140;
        const ay = pad + layers.length * lh + 30;
        return (
          <g key={i}>
            <rect x={ax} y={ay} width={100} height={40} rx={6} fill="#1e293b" stroke="#475569" strokeWidth={1} />
            <text x={ax + 50} y={ay + 24} fill="#94a3b8" fontSize={12} fontWeight={500} textAnchor="middle">
              Agent {String.fromCharCode(65 + i)}
            </text>
            {/* Arrow up */}
            <line x1={ax + 50} y1={ay} x2={ax + 50} y2={ay - 20} stroke="#475569" strokeWidth={1} strokeDasharray="4 2" />
            <polygon points={`${ax + 50},${ay - 24} ${ax + 46},${ay - 18} ${ax + 54},${ay - 18}`} fill="#475569" />
          </g>
        );
      })}

      {/* Protocol label */}
      <text x={w / 2} y={pad + layers.length * lh + 18} fill="#64748b" fontSize={10} textAnchor="middle" fontStyle="italic">
        Agents speak MCP / A2A / native
      </text>
    </svg>
  );
}

/** Permeability gate diagram showing default-deny flow. */
function PermeabilitySvg() {
  const w = 600;
  const h = 220;

  return (
    <svg viewBox={`0 0 ${w} ${h}`} className="w-full max-w-xl mx-auto" role="img" aria-label="Permeability gate: default-deny with cost-benefit analysis">
      {/* Agent A */}
      <rect x={20} y={70} width={110} height={50} rx={8} fill="#1e293b" stroke="#3b82f6" strokeWidth={1.5} />
      <text x={75} y={100} fill="#60a5fa" fontSize={13} fontWeight={600} textAnchor="middle">Agent A</text>

      {/* CMB packet */}
      <rect x={160} y={60} width={80} height={30} rx={4} fill="#f59e0b20" stroke="#f59e0b" strokeWidth={1} />
      <text x={200} y={80} fill="#fbbf24" fontSize={10} fontWeight={600} textAnchor="middle">CMB</text>

      {/* Arrow A -> Gate */}
      <line x1={130} y1={95} x2={270} y2={95} stroke="#475569" strokeWidth={1.5} markerEnd="url(#arrowGate)" />

      {/* Gate */}
      <rect x={270} y={55} width={90} height={80} rx={10} fill="#10b98118" stroke="#10b981" strokeWidth={2} />
      <text x={315} y={82} fill="#10b981" fontSize={11} fontWeight={700} textAnchor="middle">GATE</text>
      <text x={315} y={98} fill="#6ee7b7" fontSize={9} textAnchor="middle">default-deny</text>
      <text x={315} y={112} fill="#6ee7b7" fontSize={9} textAnchor="middle">cost-benefit</text>

      {/* Arrow Gate -> B (pass) */}
      <line x1={360} y1={80} x2={460} y2={80} stroke="#10b981" strokeWidth={1.5} markerEnd="url(#arrowPass)" />
      <text x={410} y={72} fill="#6ee7b7" fontSize={9} textAnchor="middle">PASS</text>

      {/* Arrow Gate -> Deny */}
      <line x1={315} y1={135} x2={315} y2={180} stroke="#ef4444" strokeWidth={1.5} strokeDasharray="4 2" />
      <text x={315} y={200} fill="#f87171" fontSize={10} textAnchor="middle">DENY</text>

      {/* Agent B */}
      <rect x={460} y={55} width={110} height={50} rx={8} fill="#1e293b" stroke="#8b5cf6" strokeWidth={1.5} />
      <text x={515} y={85} fill="#a78bfa" fontSize={13} fontWeight={600} textAnchor="middle">Agent B</text>

      {/* SVAF filter detail */}
      <rect x={160} y={140} width={80} height={24} rx={4} fill="#10b98110" stroke="#10b98160" strokeWidth={1} />
      <text x={200} y={156} fill="#6ee7b7" fontSize={9} fontWeight={500} textAnchor="middle">SVAF Filter</text>

      <line x1={240} y1={152} x2={270} y2={110} stroke="#10b98140" strokeWidth={1} strokeDasharray="3 2" />

      <defs>
        <marker id="arrowGate" viewBox="0 0 10 10" refX={8} refY={5} markerWidth={6} markerHeight={6} orient="auto-start-reverse">
          <path d="M 0 0 L 10 5 L 0 10 z" fill="#475569" />
        </marker>
        <marker id="arrowPass" viewBox="0 0 10 10" refX={8} refY={5} markerWidth={6} markerHeight={6} orient="auto-start-reverse">
          <path d="M 0 0 L 10 5 L 0 10 z" fill="#10b981" />
        </marker>
      </defs>
    </svg>
  );
}

/** Shared medium: CRDT + event log with CMB lineage. */
function SharedMediumSvg() {
  const w = 640;
  const h = 260;

  return (
    <svg viewBox={`0 0 ${w} ${h}`} className="w-full max-w-xl mx-auto" role="img" aria-label="Shared medium with CRDT document store and event log">
      {/* Event log stream */}
      <rect x={30} y={20} width={w - 60} height={50} rx={8} fill="#3b82f610" stroke="#3b82f6" strokeWidth={1.5} />
      <text x={50} y={50} fill="#60a5fa" fontSize={12} fontWeight={600}>Immutable Event Log</text>

      {/* Event entries */}
      {[0, 1, 2, 3, 4].map((i) => {
        const ex = 280 + i * 65;
        return (
          <g key={i}>
            <rect x={ex} y={30} width={55} height={30} rx={4} fill="#3b82f618" stroke="#3b82f640" strokeWidth={1} />
            <text x={ex + 28} y={50} fill="#93c5fd" fontSize={9} textAnchor="middle">e{i + 1}</text>
          </g>
        );
      })}

      {/* Arrow down to CRDT store */}
      <line x1={w / 2} y1={70} x2={w / 2} y2={100} stroke="#475569" strokeWidth={1.5} markerEnd="url(#arrowDown)" />

      {/* CRDT store */}
      <rect x={80} y={100} width={w - 160} height={70} rx={10} fill="#8b5cf610" stroke="#8b5cf6" strokeWidth={1.5} />
      <text x={100} y={125} fill="#a78bfa" fontSize={12} fontWeight={600}>CRDT Document Store</text>
      <text x={100} y={145} fill="#9ca3af" fontSize={10}>Guaranteed convergence \u00b7 Conflict-free \u00b7 Replayable</text>

      {/* CMB boxes */}
      {["CMB-a1", "CMB-b1", "CMB-a2"].map((label, i) => {
        const cx = 140 + i * 150;
        const cy = 195;
        return (
          <g key={label}>
            <rect x={cx} y={cy} width={100} height={40} rx={6} fill="#f59e0b10" stroke="#f59e0b80" strokeWidth={1} />
            <text x={cx + 50} y={cy + 16} fill="#fbbf24" fontSize={10} fontWeight={600} textAnchor="middle">{label}</text>
            <text x={cx + 50} y={cy + 30} fill="#9ca3af" fontSize={8} textAnchor="middle">CAT7 schema</text>
            {/* Lineage arrow between CMBs */}
            {i > 0 && (
              <line x1={cx} y1={cy + 20} x2={cx - 50} y2={cy + 20} stroke="#f59e0b40" strokeWidth={1} strokeDasharray="3 2" markerEnd="url(#arrowLineage)" />
            )}
          </g>
        );
      })}

      <defs>
        <marker id="arrowDown" viewBox="0 0 10 10" refX={5} refY={8} markerWidth={6} markerHeight={6} orient="auto">
          <path d="M 0 0 L 10 0 L 5 10 z" fill="#475569" />
        </marker>
        <marker id="arrowLineage" viewBox="0 0 10 10" refX={2} refY={5} markerWidth={5} markerHeight={5} orient="auto-start-reverse">
          <path d="M 10 0 L 0 5 L 10 10 z" fill="#f59e0b80" />
        </marker>
      </defs>
    </svg>
  );
}

/** Coordination: span of control and modular reorganisation. */
function CoordinationSvg() {
  const w = 560;
  const h = 260;

  return (
    <svg viewBox={`0 0 ${w} ${h}`} className="w-full max-w-lg mx-auto" role="img" aria-label="Coordination layer showing span of control with auto-expansion">
      {/* Coordinator */}
      <rect x={220} y={15} width={120} height={40} rx={8} fill="#8b5cf618" stroke="#8b5cf6" strokeWidth={1.5} />
      <text x={280} y={40} fill="#a78bfa" fontSize={12} fontWeight={600} textAnchor="middle">Coordinator</text>

      {/* 5 agents in span */}
      {[0, 1, 2, 3, 4].map((i) => {
        const ax = 40 + i * 100;
        return (
          <g key={i}>
            <line x1={280} y1={55} x2={ax + 50} y2={85} stroke="#8b5cf640" strokeWidth={1} />
            <rect x={ax} y={85} width={90} height={34} rx={6} fill="#1e293b" stroke={i < 5 ? "#8b5cf640" : "#ef444440"} strokeWidth={1} />
            <text x={ax + 45} y={107} fill="#94a3b8" fontSize={10} textAnchor="middle">Agent {i + 1}</text>
          </g>
        );
      })}

      {/* Span of control label */}
      <text x={280} y={140} fill="#6ee7b7" fontSize={10} textAnchor="middle" fontWeight={600}>Span of control: 3\u20135 (max 7)</text>

      {/* Threshold exceeded */}
      <line x1={280} y1={150} x2={280} y2={170} stroke="#ef4444" strokeWidth={1.5} strokeDasharray="4 2" />
      <text x={280} y={185} fill="#f87171" fontSize={10} textAnchor="middle" fontWeight={600}>Threshold exceeded</text>

      {/* Auto-expansion arrow */}
      <line x1={280} y1={190} x2={280} y2={205} stroke="#10b981" strokeWidth={1.5} markerEnd="url(#arrowExpand)" />

      {/* Split into sub-coordinators */}
      {[0, 1].map((i) => {
        const sx = 120 + i * 200;
        return (
          <g key={i}>
            <rect x={sx} y={210} width={130} height={34} rx={8} fill="#10b98118" stroke="#10b981" strokeWidth={1.5} />
            <text x={sx + 65} y={232} fill="#6ee7b7" fontSize={10} fontWeight={600} textAnchor="middle">Sub-coord {i + 1}</text>
          </g>
        );
      })}

      <defs>
        <marker id="arrowExpand" viewBox="0 0 10 10" refX={5} refY={8} markerWidth={6} markerHeight={6} orient="auto">
          <path d="M 0 0 L 10 0 L 5 10 z" fill="#10b981" />
        </marker>
      </defs>
    </svg>
  );
}

/** Immune defence: anomaly detection and quarantine flow. */
function ImmuneSvg() {
  const w = 600;
  const h = 200;

  return (
    <svg viewBox={`0 0 ${w} ${h}`} className="w-full max-w-xl mx-auto" role="img" aria-label="Immune layer: anomaly detection, quarantine, and cytokine gossip">
      {/* Normal agent */}
      <rect x={30} y={60} width={100} height={44} rx={8} fill="#1e293b" stroke="#3b82f6" strokeWidth={1.5} />
      <text x={80} y={87} fill="#60a5fa" fontSize={11} fontWeight={600} textAnchor="middle">Agent X</text>

      {/* Writes CMB */}
      <line x1={130} y1={82} x2={185} y2={82} stroke="#f59e0b" strokeWidth={1.5} markerEnd="url(#arrowImm)" />

      {/* CMB (potentially poisoned) */}
      <rect x={185} y={65} width={80} height={34} rx={6} fill="#ef444418" stroke="#ef4444" strokeWidth={1.5} />
      <text x={225} y={87} fill="#f87171" fontSize={10} fontWeight={600} textAnchor="middle">CMB?</text>

      {/* Anomaly detector */}
      <rect x={295} y={30} width={120} height={50} rx={10} fill="#ec489918" stroke="#ec4899" strokeWidth={1.5} />
      <text x={355} y={52} fill="#f472b6" fontSize={11} fontWeight={700} textAnchor="middle">Anomaly</text>
      <text x={355} y={66} fill="#f472b6" fontSize={11} fontWeight={700} textAnchor="middle">Detector</text>

      <line x1={265} y1={75} x2={295} y2={60} stroke="#ec489960" strokeWidth={1.5} />

      {/* Quarantine */}
      <rect x={295} y={110} width={120} height={40} rx={8} fill="#ef444418" stroke="#ef4444" strokeWidth={1.5} strokeDasharray="4 2" />
      <text x={355} y={135} fill="#f87171" fontSize={11} fontWeight={600} textAnchor="middle">Quarantine</text>

      <line x1={355} y1={80} x2={355} y2={110} stroke="#ef4444" strokeWidth={1.5} markerEnd="url(#arrowQuar)" />

      {/* Cytokine gossip */}
      <line x1={415} y1={55} x2={470} y2={55} stroke="#ec489960" strokeWidth={1} strokeDasharray="3 2" />

      <rect x={470} y={35} width={110} height={40} rx={8} fill="#ec489910" stroke="#ec489960" strokeWidth={1} />
      <text x={525} y={55} fill="#f9a8d4" fontSize={10} fontWeight={500} textAnchor="middle">Gossip alert to</text>
      <text x={525} y={68} fill="#f9a8d4" fontSize={10} fontWeight={500} textAnchor="middle">all agents</text>

      {/* Reputation update */}
      <line x1={355} y1={150} x2={225} y2={170} stroke="#ef444460" strokeWidth={1} strokeDasharray="3 2" />
      <text x={280} y={185} fill="#9ca3af" fontSize={9} textAnchor="middle" fontStyle="italic">reputation \u2193</text>

      <defs>
        <marker id="arrowImm" viewBox="0 0 10 10" refX={8} refY={5} markerWidth={6} markerHeight={6} orient="auto-start-reverse">
          <path d="M 0 0 L 10 5 L 0 10 z" fill="#f59e0b" />
        </marker>
        <marker id="arrowQuar" viewBox="0 0 10 10" refX={5} refY={8} markerWidth={6} markerHeight={6} orient="auto">
          <path d="M 0 0 L 10 0 L 5 10 z" fill="#ef4444" />
        </marker>
      </defs>
    </svg>
  );
}

/** Sympozium CRD mapping diagram. */
function SympoziumMappingSvg() {
  const w = 640;
  const h = 290;
  const layers = [
    { label: "Governance (L-1)", k8s: "SympoziumPolicy CRD", color: "#ef4444" },
    { label: "Discovery (L0)", k8s: "Agent CRD + behavioural index", color: "#f59e0b" },
    { label: "Permeability (L1)", k8s: "Membrane MCP Server", color: "#10b981" },
    { label: "Shared Medium (L2)", k8s: "SharedMemory CRD + CRDT store", color: "#3b82f6" },
    { label: "Coordination (L3)", k8s: "Ensemble CRD + relationships", color: "#8b5cf6" },
    { label: "Immune (cross-cut)", k8s: "OTel + anomaly controllers", color: "#ec4899" },
  ];

  return (
    <svg viewBox={`0 0 ${w} ${h}`} className="w-full max-w-2xl mx-auto" role="img" aria-label="Mapping membrane layers to Sympozium Kubernetes CRDs">
      {/* Headers */}
      <text x={150} y={20} fill="#e2e8f0" fontSize={12} fontWeight={700} textAnchor="middle">Membrane Layer</text>
      <text x={470} y={20} fill="#e2e8f0" fontSize={12} fontWeight={700} textAnchor="middle">Sympozium Implementation</text>
      <line x1={30} y1={28} x2={w - 30} y2={28} stroke="#334155" strokeWidth={1} />

      {layers.map((l, i) => {
        const y = 50 + i * 42;
        return (
          <g key={i}>
            {/* Layer */}
            <rect x={30} y={y} width={240} height={32} rx={6} fill={`${l.color}10`} stroke={`${l.color}60`} strokeWidth={1} />
            <text x={150} y={y + 20} fill={l.color} fontSize={11} fontWeight={600} textAnchor="middle">{l.label}</text>

            {/* Arrow */}
            <line x1={270} y1={y + 16} x2={320} y2={y + 16} stroke="#475569" strokeWidth={1.5} markerEnd="url(#arrowMap)" />

            {/* K8s impl */}
            <rect x={320} y={y} width={280} height={32} rx={6} fill="#1e293b" stroke="#47556980" strokeWidth={1} />
            <text x={460} y={y + 20} fill="#94a3b8" fontSize={11} textAnchor="middle">{l.k8s}</text>
          </g>
        );
      })}

      <defs>
        <marker id="arrowMap" viewBox="0 0 10 10" refX={8} refY={5} markerWidth={6} markerHeight={6} orient="auto-start-reverse">
          <path d="M 0 0 L 10 5 L 0 10 z" fill="#475569" />
        </marker>
      </defs>
    </svg>
  );
}

/** Biological analogy diagram. */
function BiologySvg() {
  const w = 620;
  const h = 200;
  const analogies = [
    { bio: "Cell Membrane", mem: "Permeability (L1)", color: "#10b981", desc: "Selective gates" },
    { bio: "Quorum Sensing", mem: "Coordination (L3)", color: "#8b5cf6", desc: "Collective thresholds" },
    { bio: "Immune System", mem: "Immune Layer", color: "#ec4899", desc: "Adaptive defence" },
    { bio: "Mycelium", mem: "Shared Medium (L2)", color: "#3b82f6", desc: "Ambient state" },
  ];

  return (
    <svg viewBox={`0 0 ${w} ${h}`} className="w-full max-w-xl mx-auto" role="img" aria-label="Biological analogies for membrane layers">
      {analogies.map((a, i) => {
        const x = 20 + i * 150;
        return (
          <g key={i}>
            {/* Biology */}
            <rect x={x} y={15} width={130} height={44} rx={8} fill="#10b98108" stroke="#10b98130" strokeWidth={1} />
            <text x={x + 65} y={35} fill="#6ee7b7" fontSize={10} fontWeight={600} textAnchor="middle">{a.bio}</text>
            <text x={x + 65} y={50} fill="#6ee7b780" fontSize={8} textAnchor="middle">{a.desc}</text>

            {/* Arrow */}
            <line x1={x + 65} y1={59} x2={x + 65} y2={95} stroke="#47556980" strokeWidth={1} strokeDasharray="3 2" />
            <text x={x + 65} y={82} fill="#64748b" fontSize={8} textAnchor="middle" fontStyle="italic">inspires</text>

            {/* Membrane */}
            <rect x={x} y={95} width={130} height={44} rx={8} fill={`${a.color}10`} stroke={`${a.color}50`} strokeWidth={1} />
            <text x={x + 65} y={122} fill={a.color} fontSize={10} fontWeight={600} textAnchor="middle">{a.mem}</text>
          </g>
        );
      })}

      {/* Paper citation */}
      <text x={w / 2} y={170} fill="#64748b" fontSize={9} textAnchor="middle" fontStyle="italic">
        Drawing from cell biology, bacterial quorum sensing, vertebrate immunity, and mycelial networks
      </text>
    </svg>
  );
}

// ── Page ──────────────────────────────────────────────────────────────────────

export function SyntheticMembranePage() {
  return (
    <ScrollArea className="h-[calc(100vh-3.5rem)]">
      <div className="max-w-4xl mx-auto px-6 py-10 space-y-16">
        {/* Header */}
        <header className="space-y-4">
          <div className="flex items-center gap-3">
            <Badge variant="outline" className="text-xs border-blue-500/30 text-blue-400">
              Research
            </Badge>
            <Badge variant="outline" className="text-xs border-amber-500/30 text-amber-400">
              Architecture
            </Badge>
          </div>
          <h1 className="text-3xl font-bold tracking-tight">
            The Synthetic Membrane
          </h1>
          <p className="text-lg text-muted-foreground leading-relaxed max-w-2xl">
            A shared, semi-permeable coordination layer for multi-agent AI systems.
            Multi-agent AI doesn't lack agents &mdash; it lacks a <em>medium</em>.
          </p>
          <a
            href="https://zenodo.org/records/20070699"
            target="_blank"
            rel="noopener noreferrer"
            className="inline-flex items-center gap-1.5 text-sm text-blue-400 hover:text-blue-300 transition-colors"
          >
            Read the full research paper
            <ExternalLink className="h-3.5 w-3.5" />
          </a>
        </header>

        {/* Thesis */}
        <section className="space-y-4">
          <h2 className="text-xl font-semibold">The Thesis</h2>
          <blockquote className="border-l-2 border-blue-500/40 pl-4 text-muted-foreground leading-relaxed italic">
            Structured, gated, persistent communication is a prerequisite &mdash; not an
            accelerant &mdash; for collective intelligence in multi-agent systems.
          </blockquote>
          <p className="text-sm text-muted-foreground leading-relaxed">
            Current multi-agent systems use narrow channels: MCP for tools, A2A for point-to-point
            delegation, or orchestration graphs. None provides what biological systems offer: a shared,
            permeable boundary through which neighbours sense one another, exchange digested signals,
            and coordinate without a central conductor.
          </p>
          <div className="grid grid-cols-1 sm:grid-cols-3 gap-4 pt-2">
            {[
              { title: "Structured", desc: "Typed primitives (CMBs, capability declarations, intent signals) so semantics survive transport. No more shuffling tokens." },
              { title: "Gated", desc: "Default-deny permeability. Every traversal justified by cost-benefit analysis. Uncontrolled communication degrades outcomes." },
              { title: "Persistent", desc: "The medium outlives any single agent session. Event-sourced, append-only substrate with full provenance." },
            ].map((p) => (
              <div key={p.title} className="rounded-lg border border-border/50 p-4 space-y-2">
                <h3 className="text-sm font-semibold text-foreground">{p.title}</h3>
                <p className="text-xs text-muted-foreground leading-relaxed">{p.desc}</p>
              </div>
            ))}
          </div>
        </section>

        {/* Evidence */}
        <section className="space-y-4">
          <h2 className="text-xl font-semibold">The Coordination Gap</h2>
          <p className="text-sm text-muted-foreground leading-relaxed">
            Four converging empirical findings motivate the membrane:
          </p>
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
            {[
              { stat: "1,600+", label: "failure traces analysed", detail: "MAST study: inter-agent misalignment is the primary failure cluster across seven frameworks." },
              { stat: "1,000\u00d7", label: "token overhead", detail: "Agentic tasks consume 1,000\u00d7 more tokens than non-agentic equivalents. Input tokens (context shipping) dominate cost." },
              { stat: "2M", label: "agents tested", detail: "Superminds Test: two million agents without structured substrate produce noise, not intelligence. Threads rarely extend beyond one reply." },
              { stat: "1.7B", label: "workflows analysed", detail: "CrewAI postmortem: \"the gap isn't intelligence, it's architecture.\" Execution collapses to sequential task chaining." },
            ].map((e) => (
              <div key={e.label} className="rounded-lg border border-border/50 p-4 space-y-1">
                <div className="flex items-baseline gap-2">
                  <span className="text-2xl font-bold text-blue-400">{e.stat}</span>
                  <span className="text-xs text-muted-foreground">{e.label}</span>
                </div>
                <p className="text-xs text-muted-foreground leading-relaxed">{e.detail}</p>
              </div>
            ))}
          </div>
        </section>

        {/* Architecture */}
        <section className="space-y-6">
          <h2 className="text-xl font-semibold">Six-Layer Architecture</h2>
          <p className="text-sm text-muted-foreground leading-relaxed">
            The membrane is a six-layer stack. Layers are conceptual &mdash; implementations may collapse
            some. The immune/observability layer cross-cuts all others.
          </p>
          <div className="rounded-xl border border-border/50 bg-card/50 p-6 overflow-x-auto">
            <ArchitectureSvg />
          </div>
        </section>

        {/* Layer deep-dives */}
        <section className="space-y-8">
          <h2 className="text-xl font-semibold">Layer Deep-Dives</h2>

          {/* Governance */}
          <div className="space-y-3">
            <div className="flex items-center gap-2">
              <span className="flex items-center justify-center h-6 w-6 rounded bg-red-500/10 text-red-400 text-xs font-bold font-mono">-1</span>
              <h3 className="text-base font-semibold">Governance</h3>
            </div>
            <p className="text-sm text-muted-foreground leading-relaxed">
              The outermost layer. Circuit breakers halt coordination when failure cascades exceed thresholds.
              Human override surfaces are tied to accountability logs. A <strong>dissent surface</strong> presents
              agent disagreements to human reviewers rather than hiding behind consensus headlines.
              Authority mapping follows NIMS' Unified Command &mdash; multiple authorities each get a seat at the
              command table.
            </p>
            <p className="text-xs text-muted-foreground/70 italic">
              In Sympozium: implemented via SympoziumPolicy CRDs with admission webhooks.
            </p>
          </div>

          {/* Discovery */}
          <div className="space-y-3">
            <div className="flex items-center gap-2">
              <span className="flex items-center justify-center h-6 w-6 rounded bg-amber-500/10 text-amber-400 text-xs font-bold font-mono">L0</span>
              <h3 className="text-base font-semibold">Discovery &amp; Registry</h3>
            </div>
            <p className="text-sm text-muted-foreground leading-relaxed">
              Before agents can communicate, they must find each other. Description-based discovery fails &mdash;
              semantic similarity to a self-reported capability statement doesn't predict actual performance.
              The registry indexes agents by <strong>demonstrated behaviour</strong>: execution traces,
              cost profiles, success rates per task class, and cryptographic identity.
            </p>
            <p className="text-xs text-muted-foreground/70 italic">
              In Sympozium: Agent CRDs with behavioural indexing and ICS-typed capability registration.
            </p>
          </div>

          {/* Permeability */}
          <div className="space-y-3">
            <div className="flex items-center gap-2">
              <span className="flex items-center justify-center h-6 w-6 rounded bg-emerald-500/10 text-emerald-400 text-xs font-bold font-mono">L1</span>
              <h3 className="text-base font-semibold">Permeability</h3>
            </div>
            <p className="text-sm text-muted-foreground leading-relaxed">
              The membrane proper. Permeability is <strong>field-level</strong>: an agent may accept the
              "evidence" field of a peer's CMB while rejecting the "conclusion" field.
              Permeability is <strong>default-deny</strong>: an agent works locally until a cost-benefit
              analysis justifies a traversal. The membrane provides the gate as a first-class service, not
              as agent-internal logic each developer must reinvent.
            </p>
            <div className="rounded-xl border border-border/50 bg-card/50 p-6 overflow-x-auto">
              <PermeabilitySvg />
            </div>
          </div>

          {/* Shared Medium */}
          <div className="space-y-3">
            <div className="flex items-center gap-2">
              <span className="flex items-center justify-center h-6 w-6 rounded bg-blue-500/10 text-blue-400 text-xs font-bold font-mono">L2</span>
              <h3 className="text-base font-semibold">Shared Medium</h3>
            </div>
            <p className="text-sm text-muted-foreground leading-relaxed">
              The cytoplasm. An immutable event log layered with CRDT documents. Cognitive Memory Blocks
              (CMBs) using MMP's CAT7 schema are written as events with content-hash IDs and lineage
              pointers. CRDTs handle convergence under concurrent writes. New agents joining mid-session
              can replay the log to catch up.
            </p>
            <div className="rounded-xl border border-border/50 bg-card/50 p-6 overflow-x-auto">
              <SharedMediumSvg />
            </div>
            <div className="rounded-md border border-border/50 bg-muted/10 px-4 py-3">
              <p className="text-xs text-muted-foreground font-mono leading-relaxed">
                <strong className="text-foreground">CAT7 Schema:</strong>{" "}
                source &middot; timestamp &middot; evidence &middot; conclusion &middot; confidence &middot; lineage &middot; remix
              </p>
            </div>
          </div>

          {/* Coordination */}
          <div className="space-y-3">
            <div className="flex items-center gap-2">
              <span className="flex items-center justify-center h-6 w-6 rounded bg-violet-500/10 text-violet-400 text-xs font-bold font-mono">L3</span>
              <h3 className="text-base font-semibold">Coordination</h3>
            </div>
            <p className="text-sm text-muted-foreground leading-relaxed">
              Swarm primitives: task broadcast and claim, quorum-sensing thresholds, dynamic group
              formation. Span of control is a first-class constraint &mdash; when an agent's fan-out
              exceeds 5, the coordination layer automatically triggers structural reorganisation,
              spawning sub-coordinators and re-sharding work. This is ICS's modular organisation
              principle, automated.
            </p>
            <div className="rounded-xl border border-border/50 bg-card/50 p-6 overflow-x-auto">
              <CoordinationSvg />
            </div>
          </div>

          {/* Immune */}
          <div className="space-y-3">
            <div className="flex items-center gap-2">
              <span className="flex items-center justify-center h-6 w-6 rounded bg-pink-500/10 text-pink-400 text-xs font-bold font-mono">Im</span>
              <h3 className="text-base font-semibold">Immune &amp; Observability</h3>
            </div>
            <p className="text-sm text-muted-foreground leading-relaxed">
              Cross-cutting adaptive defence modelled on the vertebrate immune system.
              Behavioural anomaly detection at L0/L1, cytokine-style gossip propagation across L3,
              memory cells in the registry, and proportional response via gated permeability.
              Poisoned entries (Spore Attacks) are quarantined before spreading through lineage chains.
            </p>
            <div className="rounded-xl border border-border/50 bg-card/50 p-6 overflow-x-auto">
              <ImmuneSvg />
            </div>
          </div>
        </section>

        {/* Biological inspiration */}
        <section className="space-y-4">
          <h2 className="text-xl font-semibold">Biological Inspiration</h2>
          <p className="text-sm text-muted-foreground leading-relaxed">
            The membrane draws directly from four biological systems that solve coordination
            without central control.
          </p>
          <div className="rounded-xl border border-border/50 bg-card/50 p-6 overflow-x-auto">
            <BiologySvg />
          </div>
        </section>

        {/* Design Principles */}
        <section className="space-y-4">
          <h2 className="text-xl font-semibold">Design Principles</h2>
          <div className="space-y-3">
            {[
              { n: 1, title: "Default-Deny Permeability", desc: "Every traversal justified by cost-benefit analysis. The token-economics finding makes this economically correct, not just operationally sound." },
              { n: 2, title: "Token-Efficient Wire Formats", desc: "Every byte in a CMB is multiplied across every reading agent. Agents store compressed interpretations (remix), not raw signals." },
              { n: 3, title: "Structured Primitives Over Free-Form", desc: "Typed schemas for every operational object. ICS solved interoperability at the protocol layer (common terminology) before standardising transport." },
              { n: 4, title: "Persistence and Provenance", desc: "Event-sourced, append-only log with content-hash IDs and lineage pointers. Every signal traceable to source. New agents replay to catch up." },
              { n: 5, title: "Span of Control", desc: "No agent manages more than five subordinates. Exceeding the threshold triggers automatic structural reorganisation \u2014 sub-coordinators spawned, work re-sharded." },
            ].map((p) => (
              <div key={p.n} className="flex gap-3 rounded-lg border border-border/50 p-4">
                <span className="flex items-center justify-center h-7 w-7 rounded-full bg-blue-500/10 text-blue-400 text-xs font-bold shrink-0">
                  {p.n}
                </span>
                <div className="space-y-1">
                  <h3 className="text-sm font-semibold">{p.title}</h3>
                  <p className="text-xs text-muted-foreground leading-relaxed">{p.desc}</p>
                </div>
              </div>
            ))}
          </div>
        </section>

        {/* Sympozium implementation */}
        <section className="space-y-4">
          <h2 className="text-xl font-semibold">Implementation in Sympozium</h2>
          <p className="text-sm text-muted-foreground leading-relaxed">
            Sympozium implements the membrane as Kubernetes-native Custom Resource Definitions (CRDs).
            Each membrane layer maps to existing or planned CRDs and controllers.
          </p>
          <div className="rounded-xl border border-border/50 bg-card/50 p-6 overflow-x-auto">
            <SympoziumMappingSvg />
          </div>
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-4 pt-2">
            {[
              { title: "Incident CRD", desc: "First-class operational object with status, Incident Action Plan, Common Operating Picture, hypothesis list, role assignments, and timeline." },
              { title: "Hypothesis CRD", desc: "Lifecycle states: proposed \u2192 testing \u2192 confirmed/rejected/superseded. Agents subscribe to transitions like pod events." },
              { title: "SharedMemory CRD", desc: "CRDT-backed shared medium with CMBs written as events. Content-hash lineage and vector + structured indexes." },
              { title: "Ensemble CRD", desc: "Team coordination with relationships (delegation, sequential, supervision), span-of-control enforcement, and stimulus triggers." },
            ].map((c) => (
              <div key={c.title} className="rounded-lg border border-border/50 p-4 space-y-1">
                <h3 className="text-sm font-semibold font-mono text-blue-400">{c.title}</h3>
                <p className="text-xs text-muted-foreground leading-relaxed">{c.desc}</p>
              </div>
            ))}
          </div>
        </section>

        {/* Comparison table */}
        <section className="space-y-4">
          <h2 className="text-xl font-semibold">Comparison with Alternatives</h2>
          <div className="rounded-lg border border-border/50 overflow-hidden">
            <table className="w-full text-xs">
              <thead>
                <tr className="border-b border-border/50 bg-muted/20">
                  <th className="px-3 py-2 text-left font-semibold text-muted-foreground">Approach</th>
                  <th className="px-3 py-2 text-center font-semibold text-muted-foreground">Shared Medium</th>
                  <th className="px-3 py-2 text-center font-semibold text-muted-foreground">Gated</th>
                  <th className="px-3 py-2 text-center font-semibold text-muted-foreground">Governance</th>
                  <th className="px-3 py-2 text-center font-semibold text-muted-foreground">Immune</th>
                  <th className="px-3 py-2 text-center font-semibold text-muted-foreground">Persistent</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-border/30">
                {[
                  { name: "A2A / Message passing", vals: [false, false, false, false, false] },
                  { name: "LangGraph", vals: ["partial", false, false, false, "session"] },
                  { name: "Blackboard (Salemi)", vals: [true, false, false, false, true] },
                  { name: "MMP", vals: ["partial", true, false, false, true] },
                  { name: "Synthetic Membrane", vals: [true, true, true, true, true] },
                ].map((row) => (
                  <tr key={row.name} className={row.name === "Synthetic Membrane" ? "bg-blue-500/5" : ""}>
                    <td className={`px-3 py-2 font-medium ${row.name === "Synthetic Membrane" ? "text-blue-400" : "text-foreground"}`}>
                      {row.name}
                    </td>
                    {row.vals.map((v, i) => (
                      <td key={i} className="px-3 py-2 text-center">
                        {v === true ? (
                          <span className="text-green-400">Yes</span>
                        ) : v === false ? (
                          <span className="text-muted-foreground/40">No</span>
                        ) : (
                          <span className="text-amber-400">{v}</span>
                        )}
                      </td>
                    ))}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </section>

        {/* Acceptance Criteria */}
        <section className="space-y-4">
          <h2 className="text-xl font-semibold">Acceptance Criteria</h2>
          <p className="text-sm text-muted-foreground leading-relaxed">
            The prototype succeeds if, against a fixed agent population:
          </p>
          <div className="space-y-2">
            {[
              "Membrane-connected swarm outperforms individual frontier models on joint reasoning tasks",
              "Swarm synthesises distributed information not held by any single agent",
              "Multi-turn coordination sustains beyond single-reply threads",
              "Total token cost no more than 2\u00d7 single-agent baseline at equal quality",
              "Failure attribution achieves >70% agent-level accuracy on injected-fault scenarios",
            ].map((c, i) => (
              <div key={i} className="flex items-start gap-3 rounded-md border border-border/50 px-4 py-2.5">
                <span className="flex items-center justify-center h-5 w-5 rounded-full bg-emerald-500/10 text-emerald-400 text-[10px] font-bold shrink-0 mt-0.5">
                  {i + 1}
                </span>
                <p className="text-xs text-muted-foreground leading-relaxed">{c}</p>
              </div>
            ))}
          </div>
          <p className="text-xs text-muted-foreground/70 italic">
            These are concrete; the prototype either meets them or the thesis is wrong about something specific.
            Not persuasion, but falsifiability.
          </p>
        </section>

        {/* Footer */}
        <footer className="border-t border-border/50 pt-6 pb-10 space-y-2">
          <p className="text-xs text-muted-foreground">
            Based on{" "}
            <a
              href="https://zenodo.org/records/20070699"
              target="_blank"
              rel="noopener noreferrer"
              className="text-blue-400 hover:text-blue-300 underline underline-offset-2"
            >
              "The Synthetic Membrane: A Coordination Layer for Multi-Agent AI Systems"
            </a>{" "}
            by Alex Jones, May 2026.
          </p>
          <p className="text-[10px] text-muted-foreground/50">
            Sympozium is the Incident Command System for AI agents, implemented on Kubernetes.
          </p>
        </footer>
      </div>
    </ScrollArea>
  );
}
