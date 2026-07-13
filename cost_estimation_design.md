# Design: AgentRun-level dollar cost estimation

Status: approved design, v1 scope. Repo: `github.com/sympozium-ai/sympozium`.

---

## 1. Overview & goals

Add an estimated dollar cost to completed AgentRuns, computed from the already-captured `status.tokenUsage` times a per-model price table.

Goals (mapped to maintainer requirements):

- **R1** — `AgentRun.status.costEstimate` is written by the controller at run completion, alongside `tokenUsage`.
- **R2** — Local/self-hosted providers (`ollama`, `lm-studio`, `llama-server`, `unsloth`, `vllm`, `llamacpp`, `local`) and all `modelRef`-backed runs are **exempt**: no persisted estimate, nothing shown by default.
- **R3** — Users can define **simulated prices** in the web UI Settings page. Simulated prices may target exempt local providers (chargeback). Simulated estimates are a **read-time overlay** computed by the apiserver, returned as a separate `simulatedCostEstimate` JSON field, and always visually labeled.
- **R4** — A default price table ships as a Helm-rendered ConfigMap, editable via `values.yaml` or `kubectl` without a binary upgrade. Unknown model/provider with no match → estimate **absent**, never `$0`.
- **R5** — No floats in CRD types: integer **micro-USD** (`int64`) plus an explicit `currency` field. Model lookup is longest-prefix match within a provider to survive dated model suffixes.
- **R6** — All repo conventions honored: `make generate && make manifests` for CRD/chart sync, controller-side derivation (no mutating webhook), agent output treated as adversarial, no new secret plumbing, no pod-spec changes.

Core architectural invariant: **persisted status carries only "real" estimates from the RBAC-protected default table; simulated numbers never touch CR status, metrics, or enforcement.** This single decision satisfies R2+R3 labeling, makes simulation retroactive and reversible, and caps the blast radius of the apiserver's opt-in auth.

## 2. Non-goals (v1)

- **No enforcement.** Cost never gates admission; `checkTokenBudget` stays token-denominated. No dollar budgets (halt/warn on $X) until usage metering moves off the agent-writable log-marker channel.
- **No spec-level pricing override** on AgentRun/Agent (unanimous panel position; see §13).
- **No persisted ensemble cost aggregate.** Ensemble totals are computed read-side by the apiserver.
- **No UI editing of the real (default) price table.** Real-price corrections go through Helm values or `kubectl edit` on the ConfigMap.
- **No multi-currency.** `currency` is present and validated `== "USD"`; the field is forward-compat only.
- **No cache/reasoning token pricing tiers.** The table prices input/output tokens only, matching what `TokenUsage` captures today (extending it touches the runner result-marker contract, deliberately frozen).
- **No recomputation/backfill** of historical estimates when the table changes (frozen-at-completion; see unresolved question if backfill becomes a product ask).
- **No fix for the retry undercount** (`strings.LastIndex` keeps only the last attempt's marker) or the failed-run usage gap — both documented on the field and in the UI tooltip.

## 3. Provider categories & exemption mechanism

New package **`internal/llmprovider`**:

```go
package llmprovider

// LocalProviders returns the canonical list of self-hosted provider ids.
func LocalProviders() []string {
    return []string{"ollama", "lm-studio", "llama-server", "unsloth", "vllm", "llamacpp", "local"}
}

func IsLocal(provider string) bool { /* switch over the list */ }
```

- `cmd/agent-runner/main.go` `isLocalProvider` (~line 69) becomes a one-line delegate; `canary.go:195` and the retry/timeout call sites are unchanged in behavior. One compiled source of truth for runner + controller + apiserver.
- The apiserver includes `localProviders` in `GET /api/v1/pricing` so the React UI renders exemption state from the server (no hardcoded copy).
- **No CRD-visible category field** — it would be a second writable source of truth requiring controller-side defaulting and webhook policing.

**Exemption predicate** (lives in `internal/pricing`, used by the controller):

```go
// Exempt: no persisted cost estimate is ever produced.
func Exempt(m sympoziumv1alpha1.ModelSpec) bool {
    return m.ModelRef != "" || llmprovider.IsLocal(m.Provider)
}
```

Critical detail (verified in code): `reconcilePending` sets `agentRun.Spec.Model.Provider = "openai"` for `modelRef` runs **in-memory only** (agentrun_controller.go ~342) — the spec in etcd still has `provider: ""` + `modelRef` set at completion. The exemption predicate therefore keys on **`spec.model.modelRef != ""` as read from etcd**, never on any resolved provider string. Model-CR-backed runs are cluster-local inference and are exempt (a future remote-gateway Model type revisits this).

**Opt-in for exempt providers is via simulation only.** A simulated price entry naming a local provider (or the reserved provider key `modelref` for Model-CR runs, matched against the Model name) makes the apiserver emit `simulatedCostEstimate` for those runs. No spec-level opt-in.

**Unknown providers are treated as remote** (priceable if the table matches, absent otherwise) — OpenAI-compatible remote gateways use arbitrary provider strings and must be priceable.

## 4. Pricing data model & storage

### 4.1 Default table (real prices)

A plain ConfigMap **`sympozium-model-pricing`** in the release namespace, data key `pricing.yaml`, rendered by new template `charts/sympozium/templates/model-pricing.yaml` from `charts/sympozium/files/pricing/defaults.yaml` (`.Files.Get`) merged with `values.pricing.extraEntries`. Values keys: `pricing.enabled` (default `true`), `pricing.extraEntries` (same schema, appended, wins ties — documented as the place for persistent local corrections so Helm upgrades don't clobber them).

Schema (`version: 1`):

```yaml
version: 1
currency: USD
entries:
  - provider: openai            # exact, case-insensitive
    match: gpt-4o               # literal prefix of spec.model.model
    inputPerMTokMicro: 2500000  # $2.50 / 1M input tokens, integer micro-USD
    outputPerMTokMicro: 10000000
  - provider: anthropic
    match: claude-sonnet-4-5
    ...
  - provider: bedrock
    match: anthropic.claude-3-5-sonnet
    ...
```

The controller learns its location from env `SYMPOZIUM_PRICING_CONFIGMAP` / `SYMPOZIUM_PRICING_NAMESPACE` on the controller Deployment (same pattern as `SYMPOZIUM_IMAGE_TAG`).

Why not a CRD: a static lookup table has no reconcile loop; a CRD drags in deepcopy, `make manifests`, the two-chart CRD sync, and webhook validation for zero benefit. The chart already ships default resources this way. Why not a second apiserver-owned "overrides" ConfigMap (dev's original proposal): it required new RBAC (a resourceName-scoped Role, plus an unscopable `create`), a merge layer, and — fatally — let a possibly-unauthenticated apiserver write prices that **displace** the real persisted estimate (qa-security blocking objection, upheld; see §13).

### 4.2 Simulated prices

Persist in the existing **`SympoziumConfig`** CR (singleton the apiserver already writes for canary config — verified: apiserver ClusterRole has full `sympoziumconfigs` CRUD at rbac.yaml:201; canary handlers auto-create the CR). New spec block in `api/v1alpha1/sympoziumconfig_types.go`:

```go
// PricingSpec configures simulated model pricing (display-only overlay).
type PricingSpec struct {
    // SimulatedEnabled toggles the read-time simulated-cost overlay.
    // +optional
    SimulatedEnabled bool `json:"simulatedEnabled,omitempty"`
    // SimulatedPrices are user-defined rates applied at read time only.
    // +optional
    // +kubebuilder:validation:MaxItems=500
    SimulatedPrices []SimulatedPrice `json:"simulatedPrices,omitempty"`
    // UpdatedAt records the last modification of this block.
    // +optional
    UpdatedAt *metav1.Time `json:"updatedAt,omitempty"`
    // UpdatedBy records the caller identity when apiserver auth is enabled.
    // +optional
    UpdatedBy string `json:"updatedBy,omitempty"`
}

type SimulatedPrice struct {
    // +kubebuilder:validation:Pattern=`^[a-zA-Z0-9][a-zA-Z0-9._:/-]{0,127}$`
    Provider string `json:"provider"` // may be a local provider, or "modelref"
    // Match is a literal prefix of the model name.
    // +kubebuilder:validation:Pattern=`^[a-zA-Z0-9][a-zA-Z0-9._:/-]{0,127}$`
    Match string `json:"match"`
    // +kubebuilder:validation:Minimum=1
    // +kubebuilder:validation:Maximum=1000000000
    InputPerMTokMicro int64 `json:"inputPerMTokMicro"`
    // +kubebuilder:validation:Minimum=1
    // +kubebuilder:validation:Maximum=1000000000
    OutputPerMTokMicro int64 `json:"outputPerMTokMicro"`
}
```

### 4.3 Precedence — deliberately NOT a merge chain

There are exactly **two sources** and they never mix:

1. **Persisted `status.costEstimate`** — computed once by the controller from the **default ConfigMap table only**. `source: "defaultTable"`.
2. **Read-time `simulatedCostEstimate`** — computed by the apiserver when serving run JSON, from `SympoziumConfig.spec.pricing` only. `source: "simulated"`. Never written to any CR status, never displaces the real estimate; when both exist the UI shows both.

This structural split makes mislabeling impossible (the source *is* the storage), makes simulation retroactive (applies to already-completed runs), makes toggling it off leave zero residue in etcd, and lets exempt local providers get simulated chargeback numbers without violating their status-level exemption.

`source` is an extensible enum so a future `userTable` / `specOverride` slots in without schema change.

### 4.4 Lookup rules (`internal/pricing`)

- Case-insensitive exact match on `provider`; **no cross-provider fallback**.
- Within the provider: exact model match first, else the entry whose `match` is the **longest literal prefix** of the model name (handles `gpt-4o` vs `gpt-4o-mini`, dated suffixes, `anthropic.claude-3-5-sonnet-20241022-v2:0`). Prefix only — no globs.
- `values.pricing.extraEntries` win ties against shipped defaults at equal prefix length.
- No match → `nil` (absent). Exempt → `nil`. Never `$0`.

### 4.5 Cost arithmetic — overflow-proof by construction

```go
// costMicro = rate (micro-USD per 1M tokens) applied to tokens, round half-up.
func costMicro(tokens, ratePerMTokMicro int64) int64 {
    q, r := tokens/1_000_000, tokens%1_000_000
    return ratePerMTokMicro*q + (ratePerMTokMicro*r+500_000)/1_000_000
}
```

With validation caps (tokens ≤ 1e10 per field after clamping, rate ≤ 1e9), the largest intermediate is `rate*q ≤ 1e9 * 1e4 = 1e13` and `rate*r < 1e9 * 1e6 = 1e15` — no int64 overflow possible. A naive `tokens*rate` would overflow at the caps (1e19 > MaxInt64), which is why the split formula is **mandatory** and unit-tested at exactly `tokens=1e10, rate=1e9`, asserting the correct value (1e13), not a clamp.

Package layout: `internal/pricing/{table.go,lookup.go,estimate.go}` + tests. `Table` parsing tolerates unknown fields; `version != 1` → table treated as absent + Warning event (fail open).

## 5. CRD changes

### 5.1 `api/v1alpha1/agentrun_types.go` — next to `TokenUsage` (~line 392)

```go
// CostEstimate is an estimated dollar cost derived from tokenUsage and a
// price table. It is an ESTIMATE, not billing data: on retried runs only the
// final attempt's tokens are counted, and failed runs report no usage, so
// values are a floor relative to provider invoices. Absent when the provider
// is local/self-hosted, the run uses modelRef, or no price-table entry
// matches — never zero.
type CostEstimate struct {
    // AmountMicro is the total estimated cost in integer micro-USD (1e-6 USD).
    AmountMicro int64 `json:"amountMicro"`
    // +optional
    InputAmountMicro int64 `json:"inputAmountMicro,omitempty"`
    // +optional
    OutputAmountMicro int64 `json:"outputAmountMicro,omitempty"`
    // Currency is always "USD" in v1.
    Currency string `json:"currency"`
    // Source identifies the price table used. Persisted estimates are always
    // "defaultTable"; "simulated" appears only in apiserver JSON, never in status.
    // +kubebuilder:validation:Enum=defaultTable;simulated
    Source string `json:"source"`
    // PriceKey is the matched table entry ("provider/matchPrefix") for audit.
    // +optional
    PriceKey string `json:"priceKey,omitempty"`
    // EstimatedAt records when the estimate was frozen.
    // +optional
    EstimatedAt *metav1.Time `json:"estimatedAt,omitempty"`
}
```

`AgentRunStatus` gains (next to `TokenUsage`, ~line 323):

```go
// +optional
CostEstimate *CostEstimate `json:"costEstimate,omitempty"`
```

### 5.2 `sympoziumconfig_types.go`

`SympoziumConfigSpec` gains `Pricing *PricingSpec` (+optional) as in §4.2.

### 5.3 No other CRD changes

No Ensemble status field, no spec fields on AgentRun/Agent/Schedule. After editing: `make generate && make manifests` (regenerates deepcopy, `config/crd/bases/`, and syncs `charts/sympozium/crds/` + `charts/sympozium-crds/templates/`; CI `helm-sync-check` gates drift).

## 6. Controller changes

### 6.1 PR0 prerequisite — clamp adversarial token counts (live bug fix)

In `parseAgentResultFromLogs` (~3624), before constructing `TokenUsage`:

- Reject the marker's usage if any of `InputTokens`, `OutputTokens`, `ToolCalls`, `DurationMs` is **negative** (verified live bug: the current guard is `InputTokens > 0 || OutputTokens > 0`, so `{inputTokens: -5e9, outputTokens: 1}` passes, yields negative `TotalTokens`, and `updateTokenBudget` adds it to `Ensemble.status.tokenBudgetUsed` — an adversarial agent can decrement the ensemble ledger and defeat a `halt` budget **today**).
- Cap each token field at `1e10`; clamp with a Warning event.
- Ignore any `cost`/`price`/`currency` field an agent puts in the marker JSON — cost is computed exclusively server-side.

This ships as its own PR (with an envtest regression proving a negative marker cannot reduce `tokenBudgetUsed`) because it fixes existing token-budget enforcement, not just pricing. Consider backport.

### 6.2 Cost computation in `succeedRun` (~3490)

After usage is parsed and inside the existing `updateStatusWithRetry` closure (one status write, no new phases):

```
if usage != nil && !pricing.Exempt(ar.Spec.Model) {
    table, err := r.Pricing.Load(ctx)        // see 6.3; fail open
    if err != nil { emit Warning event; skip }
    if est := pricing.Estimate(table, ar.Spec.Model.Provider, ar.Spec.Model.Model, usage); est != nil {
        ar.Status.CostEstimate = est          // Source=defaultTable, frozen
    }
}
```

Rules:

- **Fail open**: missing/malformed ConfigMap → run still succeeds, estimate absent, Warning event. Estimation must never block or fail a run.
- **Frozen at completion**: never recomputed when the table changes (audit integrity: table edits can't retroactively rewrite spend history). `PriceKey` + `EstimatedAt` make estimates auditable against a drifting table.
- Idempotent by construction: written in the same guarded status update as `tokenUsage`.

### 6.3 ConfigMap read path — do NOT use the cached client

Verified: `internal/controller` never Gets ConfigMaps through the manager client today (only Creates), and `cmd/controller/main.go` has no `cache.Options.ByObject` scoping — the first cached `Get` would spin up a **cluster-wide ConfigMap informer** (list+watch of every ConfigMap in the cluster). Instead, `internal/pricing` reads via `mgr.GetAPIReader()` (uncached) behind a ~30s in-process TTL cache, with a code comment explaining why plain `r.Get` is forbidden here. (The ByObject field-selector alternative is acceptable if an implementer prefers it; APIReader+TTL is chosen as the smaller change.)

### 6.4 No ensemble status aggregation

Dev's proposal to accumulate `Ensemble.status.costEstimateMicro` inside `updateTokenBudget` is rejected on verified grounds: `updateTokenBudget` early-returns when `pack.Spec.SharedMemory.Membrane.TokenBudget == nil` (~3055), so the rollup would silently never accumulate for ensembles without a token budget — wrong by construction. Ensemble totals are read-time (§8). When dollar budgets ship (post trusted metering), a dedicated accumulator with its own `costCountedAnnotation` gated on a `CostBudget` spec is the designed seam.

### 6.5 Metrics

A controller-side `promauto` counter (e.g. `sympozium_run_cost_estimate_microusd_total`, labels: provider, priceKey) emitting **real estimates only**. Simulated values never enter metrics: a `pricing_source` label would not survive aggregation/recording rules/screenshots, and an apiserver-side read-time metric would be read-rate-dependent garbage.

## 7. Runner changes

**Effectively none.** The result-marker contract is frozen (SYMPOZIUM_IMAGE_TAG skew between control plane and run pods is a real deployment mode, so lockstep upgrades are unacceptable). The only change: `isLocalProvider` in `cmd/agent-runner/main.go` becomes a delegate to `internal/llmprovider` (the runner already imports internal packages, e.g. `internal/ipc`). The runner never sees or emits dollar figures.

## 8. apiserver API

New file `internal/apiserver/pricing_handlers.go`, routes registered beside the gateway routes (~server.go:200). Deliberately **not** under `/api/v1/density/cost` — that namespace is GPU placement cost (`density_cost.go`); this feature says "pricing" / "estimated spend" everywhere to avoid the collision.

| Route | Method | Behavior |
|---|---|---|
| `/api/v1/pricing` | GET | `{ currency, defaultTable: []PriceEntry (source:"defaultTable"), simulated: { enabled, prices, updatedAt, updatedBy }, localProviders: []string }` — merged view for the UI. Read follows existing route auth policy. |
| `/api/v1/pricing/simulated` | PUT | Whole-document replace of `SympoziumConfig.spec.pricing` (canary-style write; auto-creates the CR like the canary handler). Stamps `updatedAt` (+`updatedBy` when auth on). |
| `/api/v1/pricing/simulated` | DELETE | Clears `spec.pricing.simulatedPrices` and disables the overlay. |

**Authz carve-out (required):** the write handlers check whether the server was started with a non-empty bearer token; if auth is disabled they return **403** with body `"pricing edits require apiserver authentication (apiserver.authToken)"`. Rationale: apiserver auth is opt-in (buildMux warns the API is otherwise open; flagged systemic in the July 2026 review); prices are cluster-wide financial-looking state. This is enforceable and degrades gracefully — estimation from the Helm defaults still works read-only. When auth IS enabled, routes sit behind the existing bearer middleware like everything else.

**Server-side validation on PUT** (mirrors kubebuilder markers, returns 400 with field-level messages): rates `> 0` and `≤ 1_000_000_000` micro-USD/MTok (catches the per-token-vs-per-million 1e6x typo); provider/match match `^[a-zA-Z0-9][a-zA-Z0-9._:/-]{0,127}$` (no control chars/whitespace — these strings render in the UI); ≤ 500 entries; currency omitted or `USD`.

**Run JSON:** the apiserver serializes AgentRun CRs, so `status.costEstimate` flows through automatically. Additionally, when the simulated overlay is enabled, run responses (get + list) gain a computed **`simulatedCostEstimate`** field (same shape, `source:"simulated"`) — a pure function applied at serialization from `spec.model` (+`modelRef` → provider key `modelref`, model = Model name) and `status.tokenUsage`. Ensemble detail responses gain read-time totals: sum of children's `costEstimate.amountMicro` and, separately, `simulatedCostEstimate` — **never summed together**, grouped by source and (future-proofing) currency. Per §14.6, the same read-time summation also backs accumulated-spend totals: cluster-wide (for the dashboard) and per-agent (runs filtered by agent label, for agent detail). Known limitation, documented: runs removed by `cleanup: delete` vanish from all these sums.

TypeScript client (`web/src/lib/api.ts`): `CostEstimate` type on `AgentRunStatus` (~line 187) + `simulatedCostEstimate`, plus `getPricing` / `putSimulatedPrices` / `deleteSimulatedPrices` and hooks in `web/src/hooks/use-api`.

## 9. Web UI

### 9.1 Settings — `web/src/pages/settings.tsx`

New `PricingSection` card (pattern-match the existing `SystemCanarySection`):

- **Default table**: read-only rendering of the shipped/merged real prices with a note that edits go through Helm values (`pricing.extraEntries`) or `kubectl` — "Prices apply cluster-wide."
- **Simulated prices**: enable toggle + editable table — columns: provider (free text with a `datalist` of known + local providers + `modelref`), model prefix, input $/MTok, output $/MTok. UI accepts dollars and converts on save with `Math.round(parseFloat(v) * 1e6)` — integers on the wire, never dollar floats. Prominent explanation ("Simulated prices produce clearly-labeled hypothetical estimates; they may target local providers for chargeback"), a clear-all button, and `updatedAt`/`updatedBy` displayed next to the table.
- When the server rejects writes with the auth-disabled 403, the form is disabled and shows the same message.

### 9.2 Run detail — `web/src/pages/run-detail.tsx`

Extend the stats grid (line 88, `sm:grid-cols-4` → 5) with an "Est. spend" card:

- Rendered only when `costEstimate` or `simulatedCostEstimate` exists (absent → nothing; satisfies R2/R4 in the UI for free).
- Format `amountMicro` as `$X.XXXX` (4dp under $1, 2dp above).
- Simulated values get a tilde prefix and a distinct amber **"SIMULATED"** badge; tooltip shows `priceKey` and the caveat "estimate covers the final attempt only".
- When both real and simulated exist, both are shown, visually distinct.

### 9.3 Elsewhere

- **Accumulated spend (maintainer requirement, §14.6)**: `dashboard.tsx` gains an "Est. spend so far" stat tile (cluster-wide read-time total from the apiserver) and `agent-detail.tsx` a per-agent running total — real and simulated always separate, with the `cleanup: delete` undercount noted in the tooltip.
- `runs.tsx`: optional compact cost column (nice-to-have, cuttable).
- `ensemble-detail.tsx`: read-time totals from the ensemble API — real and simulated shown separately, never merged.
- `spec.model.model` strings are untrusted display data: rely on React's default escaping, no `dangerouslySetInnerHTML`.

## 10. Security & threat model

**Trust chain.** Token usage originates from a `__SYMPOZIUM_RESULT__` marker in the agent pod's own stdout; tool subprocesses share that stdout, and `strings.LastIndex` means a backgrounded process printing a forged marker after the runner exits wins. Every cost figure is therefore an estimate an adversarial agent can distort. The design's job is to ensure spoofing buys nothing beyond a wrong number on a dashboard:

1. **Cost never feeds enforcement** (unanimous). `checkTokenBudget` (~2998) is untouched; no admission decision reads `costEstimate`. Dollar-denominated budgets are vetoed until provider-side/attested usage capture exists.
2. **Bounds clamp at the parse boundary** (§6.1) — also fixes the live negative-token budget bypass. Agent-supplied cost fields in the marker are ignored.
3. **Pricing lookup inputs** are `spec.model.provider/model` — operator-authored spec fields, not agent-written. Caveat: agents can spawn child AgentRuns; since there is no spec-level price override, a child's spec cannot carry a price.
4. **Real prices** are guarded by ordinary K8s RBAC on the `sympozium-model-pricing` ConfigMap. The UI cannot write them in v1, so the weakly-authed apiserver can at worst alter clearly-labeled simulated numbers.
5. **Simulated prices** can never displace or masquerade as real estimates: they live in `SympoziumConfig.spec`, are applied read-time into a separate JSON field, are excluded from metrics, and carry `updatedAt`/`updatedBy` for audit. Write endpoints 403 when the apiserver runs tokenless.
6. **RBAC delta: zero.** The apiserver already has full `sympoziumconfigs` CRUD (verified rbac.yaml:201); the controller reads the pricing ConfigMap under its existing configmaps grant via APIReader. No new Roles, no widened ClusterRoles, no new grants to run pods — an agent cannot touch price tables.
7. **Frozen-at-completion** real estimates are tamper-evident relative to later table edits (recomputation-on-read would let any table editor rewrite spend history).
8. **Multi-tenancy stance**: explicitly single-tenant — one cluster-global table, edit rights = possession of the bearer token, simulated prices visible to all viewers (server-side, not localStorage, so every viewer sees the same numbers — required for chargeback consistency). Documented in the Settings panel and chart README.

**Residual risks (accepted, documented):** estimates are systematically low on retried/failed runs; simulated numbers in screenshots can still be socially misread despite badges; the systemic opt-in-auth issue remains for all other mutating routes (out of scope, tracked from the July review).

## 11. Test plan

**Unit (`go test -race`, `internal/pricing`, `internal/llmprovider`, controller):**
- Lookup: exact match, longest-prefix (incl. `gpt-4o` vs `gpt-4o-mini`, bedrock IDs, tie via extraEntries), unknown model → nil, unknown provider → remote-treated, exempt (local provider; `modelRef` set with empty provider) → nil. Case-insensitivity.
- Arithmetic: split-formula correctness, round-half-up, and the boundary case `tokens=1e10, rate=1e9` asserting the exact value (no clamp, no overflow).
- Table parsing: malformed YAML, wrong version, unknown fields tolerated.
- Adversarial-marker suite for `parseAgentResultFromLogs`: negative/huge/mixed-sign tokens, multiple markers (forged second marker), marker text echoed inside agent response content, malformed JSON, agent-supplied cost field ignored.

**envtest (`make test-system`, extend agentrun tests):**
- Remote provider + populated defaults CM → `costEstimate` written with correct micro-USD amount, `source=defaultTable`, `priceKey`; written exactly once across repeated reconciles.
- `llama-server` provider → absent. `modelRef` run + populated defaults table → absent (regression for the in-memory-provider trap).
- Unpriced model → absent, never 0. Deleted/garbled pricing CM → run succeeds, cost absent, Warning event.
- PR0 regression: negative-token marker cannot reduce `Ensemble.status.tokenBudgetUsed`.

**apiserver httptest (pattern: `server_providers_test.go`):**
- GET works per existing auth policy; PUT/DELETE 401 wrong token, **403 when auth disabled**, 400 per validation rule (negative/oversized rate, bad key pattern, >500 entries, non-USD, oversized body); update-conflict retry; `simulatedCostEstimate` computed for local-provider and `modelref` runs when overlay enabled, absent when disabled; real estimate never displaced.

**Cypress (`web/cypress/e2e`):**
- `pricing-settings.cy.ts`: CRUD round-trip; exact dollar→micro conversion (`2.50` → `2500000`); client+server rejection of bad input; auth-disabled banner/disabled form.
- `run-cost-display.cy.ts`: real cost shown; SIMULATED badge on a local-provider run under simulation; no cost rendered for exempt/unknown/legacy runs.

**CI/chart:** `helm lint`, `make manifests` + `make helm-sync-check`, `go vet`, `gofmt`.

## 12. Rollout & PR breakdown

All intermediate states shippable; version skew safe by construction (absence is the default everywhere: old controllers never set the field, new controller with missing CM produces no estimate, old UIs ignore unknown JSON keys). Rollback: downgrade leaves stale-but-harmless optional status fields and an orphaned ConfigMap.

- **PR0 (0.5–1d)** — token clamp in `parseAgentResultFromLogs` + adversarial-marker tests + envtest budget regression. Fixes a live vulnerability; independent of pricing; consider backport.
- **PR1 (1.5d)** — `internal/llmprovider` (move `isLocalProvider`, refactor runner call sites) + `internal/pricing` (schema, lookup, split-formula estimate, `Exempt`) with table-driven unit tests.
- **PR2 (2d)** — `api/v1alpha1`: `CostEstimate` + `AgentRunStatus.costEstimate` + `SympoziumConfigSpec.pricing` → `make generate && make manifests` (single CRD-ceremony PR); controller wiring in `succeedRun` incl. APIReader+TTL read path and fail-open; envtest coverage.
- **PR3 (1.5d)** — chart: `files/pricing/defaults.yaml` generated from LiteLLM's price dataset via a new `hack/update-pricing` generator (committed output, release-checklist refresh — §14.1), `templates/model-pricing.yaml`, `values.pricing` block, controller env vars; `helm lint`. No RBAC changes.
- **PR4 (1.5d)** — apiserver `pricing_handlers.go` + routes + validation + auth carve-out + read-time overlay/ensemble totals + httptest suite.
- **PR5 (3d)** — web: api.ts types/functions, hooks, `PricingSection`, run-detail card, ensemble totals, accumulated-spend tiles on dashboard + agent detail (§14.6), Cypress specs.

Merge order: PR0 → PR1 → PR2 → PR3 strictly; PR4 and PR5 parallel after PR3. Total ~9–10 engineer-days.

## 13. Explicitly rejected alternatives

1. **Persisting simulated estimates into AgentRun.status via an apiserver-owned overrides ConfigMap that BEATS the defaults** (dev). Rejected — qa-security's **blocking** objection upheld: with opt-in apiserver auth, this creates an unauthenticated network write path that displaces the real persisted estimate for all subsequent runs and bakes stale simulated dollars into etcd with no scrub path; any `kubectl`/script consumer missing the `source` check reads attacker-chosen numbers as spend. It also breaks R3 in practice (no retroactive application; qa-security's own freeze-at-completion argument only applies to *real* estimates — a labeled simulated number tracking the current simulated table is the expected semantic). Dev conceded in cross-review; systems' read-time overlay adopted.
2. **Tri-source enum with a UI-writable `userTable` of real prices** (qa-security). Rejected per systems' objection: on an opt-in-auth apiserver it reintroduces mint-real-prices risk that endpoint 403s only partially mitigate, whereas the two-source model makes mislabeling structurally impossible (storage determines label). Real-price edits go through values/kubectl; UI editing of real prices deferred to v2 behind real authn. qa-security's provenance-propagation requirements retained on the two-source enum.
3. **Second `sympozium-pricing-overrides` ConfigMap + new resourceName-scoped Role** (dev/qa-security variants). Rejected: `SympoziumConfig.spec.pricing` needs zero RBAC changes and reuses the tested canary write path; the CM route needed a new Role + RoleBinding + unscopable `create`, and dev's original placement of the grant on the apiserver **ClusterRole** would have authorized writing any same-named ConfigMap in every namespace (qa-security objection, verified against rbac.yaml).
4. **`Ensemble.status.costEstimateMicro` accumulated in `updateTokenBudget`** (dev). Rejected on verified code grounds: the early-return when `TokenBudget == nil` (~3055) means the rollup silently never accumulates for budget-less ensembles — wrong by construction — and it couples display-only cost to enforcement machinery. Read-time apiserver sums in v1; a properly-gated accumulator is the seam for future cost budgets.
5. **A Pricing CRD** (considered by all, proposed by none): deepcopy/manifests/two-chart-sync/webhook ceremony for drifting reference data with no reconcile loop.
6. **`resource.Quantity` for money** (allowed by R5, rejected unanimously): suffixed-string serialization leaks into the TS client, DecimalSI surprises, semantically for compute resources. Integer micro-USD chosen.
7. **Spec-level pricing override on AgentRun/Agent** (unanimous rejection): agents can spawn child AgentRuns, so a spec-carried price is a self-declared rate an adversarial agent could zero; no mutating webhook exists for defaulting; the simulated table covers the chargeback use case. `source` enum leaves the seam.
8. **Re-resolving `modelRef` at completion to categorize by resolved provider** (dev). Rejected on verified code grounds: resolution sets `provider="openai"` in-memory only, so following it literally routes cluster-local Model runs through the OpenAI defaults table (false dollars on self-hosted inference, violating R2). Exemption pins on `modelRef != ""` in etcd.
9. **Reading the pricing ConfigMap via the controller's cached client** (systems, mechanism detail). Rejected per dev's verified objection: no ConfigMap informer exists today and no ByObject scoping is configured, so the first cached Get creates a cluster-wide ConfigMap list+watch. APIReader + 30s TTL cache instead.
10. **Simulated costs in OTel metrics with a `pricing_source` label** (qa-security). Rejected per dev: labels don't survive aggregation/recording rules, and an apiserver-side read-time metric is read-rate-dependent garbage. Metrics carry real estimates only, emitted controller-side.
11. **Runner-computed cost / extending the result marker**: hands a dollar channel to an adversarial pod and forces lockstep image upgrades across the SYMPOZIUM_IMAGE_TAG skew boundary.
12. **Reusing `/api/v1/density/cost`**: that namespace is GPU placement cost; collision avoided by the `/api/v1/pricing` namespace and "estimated spend" copy.

## 14. Maintainer decisions (2026-07-13)

The panel's open questions were resolved by the maintainer as follows:

1. **Defaults-table seed: generate from a public dataset.** LiteLLM maintains the de-facto community price list (`model_prices_and_context_window.json` in github.com/BerriAI/litellm) with per-token input/output costs for OpenAI/Anthropic/Bedrock and most gateways. PR3 gains a small generator (`hack/update-pricing`) that converts it into `charts/sympozium/files/pricing/defaults.yaml` (micro-USD per MTok). The output is **committed** — generated at release time, never fetched at runtime (supply chain, determinism, air-gapped clusters). Refreshing becomes a release-checklist step: run the generator, review the diff, commit.
2. **Bedrock: region-insensitive pricing accepted for v1**, keyed on model ID. A note ships in the chart README and the docs pricing page stating that estimates ignore region multipliers.
3. **Dollar budgets: not now.** v1 stays estimation/display-only as designed; the trusted-metering prerequisite remains attached to whenever budgets are picked up.
4. **No backfill.** Frozen-at-completion is the permanent semantic; no recompute command.
5. **Input/output split: confirmed in.** `inputAmountMicro`/`outputAmountMicro` stay on `CostEstimate` (§5.1), the table keeps separate input/output rates, and the UI cost card tooltip shows the split. Cache/reasoning-token tiers stay out of v1 (runner contract stays frozen).
6. **Accumulated spend in the UX (new requirement).** The UI must show estimated spend *so far*, not just per-run numbers: a cluster-wide running total on the dashboard, a per-agent total on agent detail, plus the ensemble totals already designed. Implemented read-time in the apiserver — sums of `costEstimate.amountMicro` over stored AgentRuns, real and simulated always separate. Caveat inherited from §8: runs removed by `cleanup: delete` leave the sum; if that undercount proves unacceptable, the follow-up is a persisted per-agent accumulator using the same annotation-guard pattern as `updateTokenBudget` (the seam named in §13.4).

Still open, deferred and non-blocking: per-user apiserver authn/authz (blocks UI editing of real prices, per-tenant visibility, and price-change audit trails).

## 15. Post-v1 maintainer UX revisions (2026-07-13, after live testing)

1. **Single effective spend value.** The dual real+simulated presentation (amber SIMULATED badge, `~` prefix, two values side by side) was judged unhelpful in practice. Every view now renders ONE number per run: `costEstimate` when present, else `simulatedCostEstimate`. Source distinction lives in tooltips only. Rationale: on all-local clusters, simulated rates are the *only* source of numbers, so the loud labeling dominated every screen. The **storage/API invariant is unchanged** — simulated values are still read-time-only, never persisted, never in metrics; only the presentation merged. Totals sum effective values and note "includes simulated rates" in their tooltip when applicable.
2. **Totals shipped earlier than planned**: runs page gets a per-row spend column + total across listed runs; the conversation view (agent-detail) gets a per-conversation (session) total. Computed client-side from the runs list response.
3. **Known trap found in testing**: UI-created runs get `provider: custom` (apiserver `createRun` infers provider by baseURL substring and misses e.g. `http://172.18.0.1:8080/v1`), while YAML-created runs say `llama-server` — so simulated entries may need both provider keys. Follow-up: `createRun` should prefer the Agent's declared provider over URL sniffing.
4. **UX nit found in testing**: saving simulated price rows without flipping the enable toggle silently does nothing; consider auto-enabling on first save or making the toggle state more prominent.

