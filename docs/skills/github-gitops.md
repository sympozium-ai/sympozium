# GitHub GitOps Skill

**Status:** Design / RFC
**Date:** 2026-03-02
**Category:** Integration

---

## 1. Overview

The `github-gitops` SkillPack lets Sympozium agents open GitHub pull requests
when they detect problems in a Kubernetes cluster. Rather than just reporting
issues through a channel message, the agent can propose fixes directly in the
repository that owns the cluster configuration — the GitOps loop.

**Example scenario:**

> A scheduled AgentRun observes that a Deployment's `replicas` has drifted away
> from what is declared in the GitOps repo. The agent opens a PR that corrects
> the value, adds a comment explaining the drift, and tags the relevant team for
> review.

---

## 2. Architecture Overview

```
┌────────────────────────────────────────────────────────────────┐
│  Agent Pod (AgentRun)                                          │
│                                                                │
│   ┌───────────────┐   exec_command   ┌──────────────────────┐ │
│   │  agent-runner │ ──────────────── │  github-gitops       │ │
│   │  (LLM loop)   │                  │  sidecar             │ │
│   └───────────────┘                  │                      │ │
│                                      │  gh CLI + git        │ │
│   /skills/github-*.md ──────────────►│  reads /skills/      │ │
│   /workspace/                        │  reads /secrets/     │ │
│   /secrets/github-token (read-only)  └──────────────────────┘ │
└────────────────────────────────────────────────────────────────┘
                                  │
                                  │ gh pr create / git push
                                  ▼
                        GitHub Repository
```

**New components required:**

| Component | What it does |
|-----------|-------------|
| `SkillPack: github-gitops` | Skill instructions + sidecar definition |
| `Dockerfile: images/skill-github-gitops/` | `gh` CLI + `git` + `tool-executor.sh` |
| `config/skills/github-gitops.yaml` | The CRD manifest |
| `GithubSkillConfig` struct (API types) | Per-instance repository binding |
| `SkillRef.Params` field (API types) | Pass per-instance config to sidecar |
| `POST /api/v1/skills/{name}/auth` | Trigger GitHub device-flow auth |
| `GET /api/v1/skills/{name}/auth/status` | Poll auth completion |
| TUI wizard step | Repository prompt + browser auth |
| Fingerprint labels on GitHub issues/PRs | Prevent duplicate submissions |

---

## 3. Authentication Flow

The `gh` CLI supports GitHub's
[OAuth device flow](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/authorizing-oauth-apps#device-flow),
which does not require a redirect URI and works perfectly in headless/server
environments. The flow mirrors what Sympozium already does for WhatsApp (QR
code in pod logs, CLI polls and renders it for the user).

### 3.1 Sequence Diagram

```
Operator CLI (TUI)         API Server / Controller         GitHub
       │                           │                           │
       │  POST /api/v1/skills/     │                           │
       │  github-gitops/auth       │                           │
       │ ─────────────────────────►│                           │
       │                           │  gh auth login --web      │
       │                           │ ─────────────────────────►│
       │                           │◄── user_code + verify_uri │
       │◄── { code, url } ─────── │                           │
       │                           │                           │
       │  Displays in TUI:         │                           │
       │  "Visit: github.com/      │                           │
       │   login/device            │                           │
       │   Code: ABCD-1234"        │                           │
       │  Opens browser (optional) │                           │
       │                           │                           │
       │  (user authorises in      │                           │
       │   their browser)          │                           │
       │                           │◄──── access_token ───────│
       │                           │  Store in K8s Secret      │
       │                           │  (github-gitops-token)    │
       │◄── 200 OK ────────────── │                           │
       │                           │                           │
       │  TUI: "✅ GitHub linked!" │                           │
```

### 3.2 Token Storage

The access token is stored as a Kubernetes Secret in `sympozium-system`:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: github-gitops-token
  namespace: sympozium-system
  labels:
    sympozium.ai/skill: github-gitops
type: Opaque
data:
  GH_TOKEN: <base64-encoded token>
```

The SkillPack sidecar mounts this secret as a read-only volume projected into
the pod at `/secrets/github-token`:

```yaml
sidecar:
  secretRef: github-gitops-token
  secretMountPath: /secrets/github-token
```

A new `secretRef` field is added to `SkillSidecar` (see §7.1).

The `gh` CLI reads `$GH_TOKEN` automatically, so the `tool-executor.sh` exports
the env var from the file at startup:

```bash
export GH_TOKEN=$(cat /secrets/github-token/GH_TOKEN)
```

### 3.3 Token Rotation

The Secret can be overwritten at any time by re-running the auth flow. Pods
already running will pick up the new token on next exec via the file mount (no
pod restart required since the file is always read fresh).

---

## 4. Per-Instance Repository Configuration

Different `SympoziumInstance` objects should be able to target **different**
GitHub repositories (e.g. one agent watches `infra/prod`, another watches
`infra/staging`). This is handled through a new `params` field on `SkillRef`.

### 4.1 SkillRef Params (API change)

```go
// SkillRef now carries optional per-instance parameters.
type SkillRef struct {
    SkillPackRef string            `json:"skillPackRef,omitempty"`
    Params       map[string]string `json:"params,omitempty"` // NEW
}
```

### 4.2 On a SympoziumInstance

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: SympoziumInstance
metadata:
  name: platform-agent
spec:
  skills:
    - skillPackRef: github-gitops
      params:
        targetRepository: "myorg/infra"
        defaultBranch: "main"
        prLabels: "sympozium,automated"
        prReviewers: "platform-team"
```

### 4.3 How Params reach the Sidecar

The SkillPack controller injects `params` as environment variables when
building the sidecar container spec for the AgentRun pod. Keys are
upper-cased and prefixed with `SKILL_`:

| Param key | Env var injected |
|-----------|-----------------|
| `targetRepository` | `SKILL_TARGETREPOSITORY` |
| `defaultBranch` | `SKILL_DEFAULTBRANCH` |
| `prLabels` | `SKILL_PRLABELS` |
| `prReviewers` | `SKILL_PRREVIEWERS` |

The skill Markdown instructs the agent to include these values when calling
`gh pr create`.

---

## 5. TUI / UX Flow

When the user selects the `github-gitops` skill in the TUI (Instances → Edit
→ Skills tab), Sympozium detects that this skill requires interactive
authentication and repository configuration, and launches a wizard step.

### 5.1 TUI Wizard (Bubble Tea)

```
  ╔══════════════════════════════════════════════════════════╗
  ║  GitHub GitOps Skill — Setup                             ║
  ╚══════════════════════════════════════════════════════════╝

  This skill lets your agent open pull requests on GitHub.
  A GitHub OAuth device code will be displayed for you to
  authorise in your browser.

  ─────────────────────────────────────────────────────────
  Target repository (owner/repo): [myorg/infra          ]
  Default branch                : [main                 ]
  PR labels (comma-separated)   : [sympozium,automated  ]
  PR reviewers (comma-separated): [                     ]
  ─────────────────────────────────────────────────────────

  Press Enter to continue and authorise GitHub access.
```

After pressing Enter:

```
  ⏳ Requesting GitHub device code...

  ┌────────────────────────────────────────────────────────┐
  │  1. Open this URL in your browser:                     │
  │                                                        │
  │     https://github.com/login/device                    │
  │                                                        │
  │  2. Enter this code:                                   │
  │                                                        │
  │     ABCD-1234                                          │
  │                                                        │
  │  Code expires in 15 minutes.                           │
  └────────────────────────────────────────────────────────┘

  Waiting for authorisation ....
```

On success:

```
  ✅ GitHub linked!

     Token stored in Secret: sympozium-system/github-gitops-token
     Skill enabled on instance: platform-agent
     Target repository: myorg/infra

  Press any key to return to the instance view.
```

### 5.2 CLI onboard `--skills` flag

The existing `sympozium onboard` wizard gains a skill-enabling step:

```
📋 Step 5/6 — Skills (optional)

  Available skills:
    1) k8s-ops            Kubernetes operations
    2) sre-observability  SRE observability
    3) github-gitops      GitOps pull request automation  ← NEW
    0) Skip

  Choice [0-3]: 3

  GitHub GitOps Skill — target repository (owner/repo): myorg/infra
  Default branch [main]:

  ⏳ Requesting GitHub device code...
  
  Visit: https://github.com/login/device
  Code:  ABCD-1234
  
  Waiting... ✅ GitHub linked!
```

A new `runGithubSkillSetup(reader, instanceName)` function in
`cmd/sympozium/main.go` handles this step (mirrors `streamWhatsAppQR`).

---

## 6. API Server Endpoints

Two new endpoints are added to the API server to support the TUI and any
future web UI:

### `POST /api/v1/skills/github-gitops/auth`

Initiates the GitHub device flow. The API server (or a short-lived pod) runs:

```bash
gh auth login --hostname github.com --git-protocol https --web
```

and captures the device code output. Returns:

```json
{
  "userCode": "ABCD-1234",
  "verificationURI": "https://github.com/login/device",
  "expiresIn": 899,
  "interval": 5
}
```

### `GET /api/v1/skills/github-gitops/auth/status`

Polls GitHub's token endpoint (the device flow polling loop) and returns:

```json
{ "status": "pending" }       // still waiting
{ "status": "complete" }      // token saved to Secret
{ "status": "expired" }       // device code expired
```

Once `"complete"`, the token has been written to the
`github-gitops-token` Secret.

---

## 7. CRD & API Type Changes

### 7.1 `SkillSidecar` — add `SecretRef` and `SecretMountPath`

```go
// SkillSidecar additions
type SkillSidecar struct {
    // ... existing fields ...

    // SecretRef is the name of a Kubernetes Secret whose keys are
    // mounted as files at SecretMountPath inside the sidecar.
    // +optional
    SecretRef string `json:"secretRef,omitempty"`

    // SecretMountPath is the path where the Secret is mounted.
    // Defaults to /secrets/<SecretRef>.
    // +optional
    SecretMountPath string `json:"secretMountPath,omitempty"`
}
```

### 7.2 `SkillRef` — add `Params`

```go
type SkillRef struct {
    SkillPackRef string            `json:"skillPackRef,omitempty"`
    // Params are per-instance key/value pairs injected
    // as SKILL_<KEY> env vars into the skill sidecar.
    // +optional
    Params map[string]string `json:"params,omitempty"`
}
```

---

## 8. Finding Classification: Issue vs PR

Not every cluster finding warrants a pull request. The agent must decide
whether to raise a **GitHub Issue** (observation, ambiguous finding, security
concern) or a **Pull Request** (actionable diff with a concrete, deterministic
fix) based on the nature and severity of the finding.

### 8.1 Decision Matrix

| Finding type | Can auto-fix? | Severity | Action |
|-------------|--------------|----------|--------|
| Replica count drift | Yes | Medium | **PR** |
| Image tag drift (pinned hash) | Yes | Medium | **PR** |
| Image tag drift (semver — newer available) | No | Low | **Issue** |
| ConfigMap mutated in-cluster | Yes (restore from repo) | High | **PR** |
| Resource deleted from cluster, still in repo | Yes (re-apply) | High | **PR** |
| Unknown resource in cluster, not in repo | No | Medium | **Issue** |
| RBAC permission escalation detected | No | Critical | **Issue** + channel alert |
| PodSecurity / PSA violation | No | Critical | **Issue** + channel alert |
| Orphaned resource (no owner ref) | No | Low | **Issue** |
| Generic observation / informational | No | Low | **Issue** |

### 8.2 Decision Rules

The agent follows these rules in strict priority order:

1. **Severity first.** If the finding is `critical` (security, data integrity,
   compliance):
   - Always file an **Issue**.
   - Also call `send_channel_message` with an alert immediately.
   - Do **not** attempt a PR — require explicit human sign-off before any
     automated change.

2. **Fixability check.** Ask: can I produce a deterministic YAML diff that
   restores the declared state without side effects or human judgment?
   - If yes → attempt a **PR**.
   - If no → file an **Issue** with the suggested fix in the body for review.

3. **Confidence threshold.** If confidence in the correct fix is below ~80%,
   always prefer an **Issue** — include the candidate patch as a code block in
   the issue body so a human can apply it with one click.

4. **Low severity.** For `low` severity findings, always prefer an **Issue**
   over a PR to keep PR queues clean. Bundle multiple low-severity observations
   affecting the same namespace into a single issue.

---

## 9. Deduplication and Prior Submission Detection

The sidecar must never create a duplicate issue or PR for a finding that has
already been reported. This is especially critical for scheduled runs — without
deduplication, every 30-minute sweep would generate a new artifact for the same
drift.

### 9.1 Fingerprint Labels

Every issue and PR opened by Sympozium carries a uniquely identifying
**fingerprint label**:

```
sympozium-fp:<fingerprint>
```

The fingerprint is a short hash of the 5-tuple that identifies a specific
finding:

```
<api-group>/<Kind>/<namespace>/<name>/<finding-type>
```

Example:

```
apps/Deployment/production/api-server/replica-drift
→ sha256sum | first 12 chars
→ label: sympozium-fp:3a7f2c91b445
```

This label is set at creation time and never changes. It allows the agent to
query GitHub for an exact match before creating anything new.

A second label, `sympozium-action: pr|issue`, records which type was filed so
the agent can make consistent follow-up decisions.

### 9.2 Detection Algorithm

Before creating any issue or PR, the agent runs this check sequence:

```bash
# Compute fingerprint for this specific finding
FP=$(sympozium_fingerprint "apps" "Deployment" "production" "api-server" "replica-drift")

# Check for open items with this fingerprint
EXISTING_ISSUE=$(gh issue list \
  --repo "$SKILL_TARGETREPOSITORY" \
  --state open \
  --label "sympozium-fp:${FP}" \
  --json number,url,updatedAt \
  --jq '.[0]')

EXISTING_PR=$(gh pr list \
  --repo "$SKILL_TARGETREPOSITORY" \
  --state open \
  --label "sympozium-fp:${FP}" \
  --json number,url,updatedAt \
  --jq '.[0]')

# Check for recently closed items
CLOSED_ITEM=$(gh issue list \
  --repo "$SKILL_TARGETREPOSITORY" \
  --state closed \
  --label "sympozium-fp:${FP}" \
  --json number,url,closedAt \
  --jq '.[0]')
```

**Decision table after detection:**

| State | Action |
|-------|--------|
| Open issue/PR exists, updated < 24h ago | **Skip** — already actively tracked |
| Open issue/PR exists, stale (≥ 24h since last update) | **Comment** on the existing item with fresh evidence |
| Closed within the last 7 days | **Skip** — allow time for the fix to propagate to the cluster |
| Closed ≥ 7 days ago | **Create new** issue/PR — regression detected |
| No existing item anywhere | **Create new** issue/PR per §8 decision rules |

### 9.3 Commenting on Stale Open Items

When an open item is stale (≥ 24h without an update), the agent posts a
follow-up comment instead of a duplicate:

```bash
gh issue comment "$EXISTING_NUMBER" \
  --repo "$SKILL_TARGETREPOSITORY" \
  --body "$(cat <<'EOF'
## Sympozium — Observation Update

**Timestamp:** $(date -u +%Y-%m-%dT%H:%M:%SZ)
**AgentRun:** $AGENTRUN_NAME

This finding is **still present** in the cluster.

**Latest evidence:**

\`\`\`
<kubectl output snippet>
\`\`\`

No new action taken — tracking on this thread.
EOF
  )"
```

### 9.4 Fingerprint Helper in the Sidecar

`tool-executor.sh` exports a `sympozium_fingerprint` function available to all
commands executed through the sidecar:

```bash
# Appended to tool-executor.sh startup block
# Usage: sympozium_fingerprint <group> <kind> <namespace> <name> <finding-type>
sympozium_fingerprint() {
    local input="${1}/${2}/${3}/${4}/${5}"
    echo -n "$input" | sha256sum | cut -c1-12
}
export -f sympozium_fingerprint
```

The agent calls it via `execute_command`:

```bash
FP=$(sympozium_fingerprint "apps" "Deployment" "production" "api-server" "replica-drift")
# → FP=3a7f2c91b445
```

---

## 10. SkillPack Manifest

```yaml
---
apiVersion: sympozium.ai/v1alpha1
kind: SkillPack
metadata:
  name: github-gitops
  namespace: sympozium-system
  labels:
    sympozium.ai/builtin: "true"
    sympozium.ai/category: gitops
    sympozium.ai/requires-auth: "true"        # hint for TUI wizard
spec:
  category: gitops
  version: "0.1.0"
  source: builtin
  skills:
    - name: open-pull-request
      description: |
        Detect cluster config drift and open a GitHub pull request with a fix.
      content: |
        # GitHub GitOps — Open Pull Request

        You have access to the `gh` CLI and `git` inside the sidecar.
        Your task is to open a pull request against the GitOps repository
        when you detect a discrepancy between the cluster state and the
        declared configuration.

        ## Configuration

        The following environment variables are pre-configured for you:

        | Variable | Description |
        |----------|-------------|
        | `SKILL_TARGETREPOSITORY` | Owner/repo to open PRs against |
        | `SKILL_DEFAULTBRANCH` | Base branch for PRs |
        | `SKILL_PRLABELS` | Comma-separated labels to apply |
        | `SKILL_PRREVIEWERS` | Comma-separated reviewer handles |
        | `GH_TOKEN` | GitHub auth token (set automatically) |

        ## Step 1: Classify the finding

        Apply the decision rules in §8 of the skills design to determine
        whether to open a **PR** or an **Issue**:

        - **PR** — you have a deterministic YAML fix, severity is medium/high,
          and confidence is ≥ 80%.
        - **Issue** — finding is ambiguous, critical severity, low confidence,
          or no concrete diff can be produced. Use the `report-issue` skill
          in that case.

        For `critical` severity findings, call `send_channel_message` with
        an immediate alert **before** touching GitHub.

        ## Step 2: Deduplication check

        Compute a fingerprint for this specific finding and check GitHub
        before taking any action:

        ```bash
        FP=$(sympozium_fingerprint "<api-group>" "<Kind>" "<namespace>" "<name>" "<finding-type>")

        # Check open PRs
        OPEN_PR=$(gh pr list --repo "$SKILL_TARGETREPOSITORY" \
          --state open --label "sympozium-fp:${FP}" \
          --json number,url,updatedAt --jq '.[0]')

        # Check open issues
        OPEN_ISSUE=$(gh issue list --repo "$SKILL_TARGETREPOSITORY" \
          --state open --label "sympozium-fp:${FP}" \
          --json number,url,updatedAt --jq '.[0]')

        # Check recently closed (within 7 days)
        CLOSED=$(gh issue list --repo "$SKILL_TARGETREPOSITORY" \
          --state closed --label "sympozium-fp:${FP}" \
          --json number,closedAt --jq '.[0]')
        ```

        **Act on the results:**
        - If an open PR/issue exists and was updated < 24h ago → **stop**, report
          "already tracked" via `send_channel_message`.
        - If an open PR/issue exists and is stale (≥ 24h) → **comment** on it
          with fresh evidence (see §9.3) and stop.
        - If a closed item exists closed < 7 days ago → **stop**, allow
          propagation time.
        - If a closed item exists closed ≥ 7 days ago, or no item exists →
          **proceed** to open a new PR below.

        ## Step 3: Open the PR

        ### 3a. Clone the repository (shallow)
        ```bash
        gh repo clone "$SKILL_TARGETREPOSITORY" /workspace/repo -- --depth=1
        cd /workspace/repo
        git checkout "$SKILL_DEFAULTBRANCH"
        ```

        ### 3b. Create a fix branch
        ```bash
        BRANCH="sympozium/fix-$(date +%Y%m%d-%H%M%S)"
        git checkout -b "$BRANCH"
        ```

        ### 3c. Apply the fix
        Edit or create the relevant YAML file(s) in `/workspace/repo`.
        Use `read_file` and `write_file` tools if you need to inspect
        or modify files.

        ### 3d. Commit
        ```bash
        git config user.email "sympozium-agent@cluster.local"
        git config user.name "Sympozium Agent"
        git add -A
        git commit -m "fix(<resource>): correct drift detected by Sympozium

        <description of what changed and why>

        Detected by: AgentRun $AGENTRUN_NAME
        Cluster: $CLUSTER_NAME"
        ```

        ### 3e. Push and create PR with fingerprint label
        ```bash
        git push origin "$BRANCH"

        gh pr create \
          --repo "$SKILL_TARGETREPOSITORY" \
          --base "$SKILL_DEFAULTBRANCH" \
          --head "$BRANCH" \
          --title "fix(<resource>): <short description>" \
          --body "$(cat <<'EOF'
        ## Summary
        <what was detected and what this PR fixes>

        ## Cluster Evidence
        <kubectl output / logs that triggered this PR>

        ## Change
        <description of the YAML change made>

        ---
        *Opened automatically by [Sympozium](https://github.com/sympozium-ai/sympozium)
        AgentRun: $AGENTRUN_NAME*
        EOF
        )" \
          --label "$SKILL_PRLABELS,sympozium-fp:${FP},sympozium-action:pr" \
          --reviewer "$SKILL_PRREVIEWERS"
        ```

        ## Step 4: Report back
        Send the PR URL via `send_channel_message` so the operator is informed.

      requires:
        bins:
          - gh
          - git
        tools:
          - bash
          - read_file
          - write_file
          - send_channel_message

    - name: report-issue
      description: |
        File a GitHub Issue for findings that cannot be auto-fixed, are ambiguous,
        or are critical severity. Includes full deduplication logic.
      content: |
        # GitHub GitOps — Report Issue

        Use this skill when the `open-pull-request` skill determines that an
        **Issue** is the right action (see §8 decision rules): the finding is
        ambiguous, critical, low confidence, or no concrete diff exists.

        ## Step 1: Deduplication check

        ```bash
        FP=$(sympozium_fingerprint "<api-group>" "<Kind>" "<namespace>" "<name>" "<finding-type>")

        OPEN_ISSUE=$(gh issue list --repo "$SKILL_TARGETREPOSITORY" \
          --state open --label "sympozium-fp:${FP}" \
          --json number,url,updatedAt --jq '.[0]')

        CLOSED=$(gh issue list --repo "$SKILL_TARGETREPOSITORY" \
          --state closed --label "sympozium-fp:${FP}" \
          --json number,closedAt --jq '.[0]')
        ```

        Apply the same deduplication logic as in `open-pull-request` §Step 2.
        If an open issue exists and is stale, comment on it instead of creating
        a new one. If closed < 7 days, skip.

        ## Step 2: Create the Issue

        ```bash
        gh issue create \
          --repo "$SKILL_TARGETREPOSITORY" \
          --title "[sympozium] <finding type>: <resource>"\
          --body "$(cat <<'EOF'
        ## Finding
        <description of what was observed in the cluster>

        ## Severity
        <critical / high / medium / low>

        ## Cluster Evidence
        <kubectl output / events / logs>

        ## Suggested Action
        <what a human should do to resolve this — or candidate patch if
         confidence is below the auto-PR threshold>

        ---
        *Opened automatically by [Sympozium](https://github.com/sympozium-ai/sympozium)
        AgentRun: $AGENTRUN_NAME*
        EOF
        )" \
          --label "$SKILL_PRLABELS,sympozium-fp:${FP},sympozium-action:issue"
        ```

        ## Step 3: Critical severity — alert immediately

        If severity is `critical`, also call `send_channel_message` with:

        ```
        🚨 CRITICAL finding: <description>
        GitHub Issue: <url>
        Cluster: $CLUSTER_NAME  Namespace: <ns>
        ```

      requires:
        bins:
          - gh
        tools:
          - bash
          - send_channel_message

    - name: pr-review-checker
      description: Check the status of open Sympozium-created PRs and Issues.
      content: |
        # GitHub GitOps — PR Status Checker

        Use this skill when asked to review or summarise the state of
        outstanding pull requests opened by Sympozium.

        ```bash
        gh pr list \
          --repo "$SKILL_TARGETREPOSITORY" \
          --state open \
          --label sympozium \
          --json number,title,url,createdAt,reviews
        ```

        Present the results as a table with columns:
        | # | Type | Title | URL | Age | Review Status |

        Also list open **issues** filed by Sympozium:
        ```bash
        gh issue list \
          --repo "$SKILL_TARGETREPOSITORY" \
          --state open \
          --label "sympozium-action:issue" \
          --json number,title,url,createdAt,labels
        ```

        Highlight items with a ⚠️  warning if:
        - A PR has been open for > 48 hours without a review.
        - An issue has been open for > 72 hours without a comment.
        - A `critical` severity issue has been open for > 1 hour without a comment.
      requires:
        bins:
          - gh
        tools:
          - bash

  sidecar:
    image: ghcr.io/sympozium-ai/sympozium/skill-github-gitops:latest
    command: ["/usr/local/bin/tool-executor.sh"]
    mountWorkspace: true
    secretRef: github-gitops-token
    secretMountPath: /secrets/github-token
    resources:
      cpu: "200m"
      memory: "256Mi"
    rbac: []          # no K8s API access needed for this skill
```

---

## 11. Sidecar Image

### `images/skill-github-gitops/Dockerfile`

```dockerfile
# Skill sidecar: github-gitops
# Provides gh CLI + git for GitHub pull request automation.
# Runs as non-root (UID 1000) with a read-only rootfs.
FROM alpine:3.20

ARG GH_VERSION=2.65.0
ARG TARGETARCH=amd64

RUN apk add --no-cache \
      bash \
      curl \
      jq \
      git \
      openssh-client \
    && adduser -D -u 1000 agent \
    && curl -fsSL \
       "https://github.com/cli/cli/releases/download/v${GH_VERSION}/gh_${GH_VERSION}_linux_${TARGETARCH}.tar.gz" \
       | tar -xz -C /usr/local/bin --strip-components=2 \
         "gh_${GH_VERSION}_linux_${TARGETARCH}/bin/gh" \
    && chmod +x /usr/local/bin/gh

COPY images/skill-github-gitops/tool-executor.sh /usr/local/bin/tool-executor.sh
RUN chmod +x /usr/local/bin/tool-executor.sh

USER 1000
WORKDIR /workspace

CMD ["/usr/local/bin/tool-executor.sh"]
```

### `images/skill-github-gitops/tool-executor.sh`

The script is identical to `images/skill-k8s-ops/tool-executor.sh` with one
addition at startup — it sources the token from the mounted secret before
processing any request:

```bash
#!/bin/bash
# tool-executor.sh — github-gitops variant
# Loads GH_TOKEN from the mounted Secret before processing exec requests.

# Load GitHub token from mounted Secret (written by the auth flow).
if [[ -f /secrets/github-token/GH_TOKEN ]]; then
    export GH_TOKEN
    GH_TOKEN=$(cat /secrets/github-token/GH_TOKEN)
    echo "[tool-executor] GH_TOKEN loaded from secret mount"
else
    echo "[tool-executor] WARNING: /secrets/github-token/GH_TOKEN not found — gh commands will fail"
fi

# ... rest is identical to skill-k8s-ops/tool-executor.sh ...
```

---

## 12. Auth Flow Implementation

### 10.1 `internal/apiserver/server_github_auth.go` (new file)

The auth flow is orchestrated by the API server. It runs the GitHub device
flow by calling the GitHub API directly (no `gh` binary on the API server):

```
POST /api/v1/skills/github-gitops/auth
  → calls https://github.com/login/device/code  (GitHub OAuth app client_id)
  → returns { userCode, verificationURI, expiresIn, interval }
  → starts background goroutine polling https://github.com/login/oauth/access_token
  → on success: writes token to K8s Secret github-gitops-token in sympozium-system

GET /api/v1/skills/github-gitops/auth/status
  → returns { status: "pending" | "complete" | "expired" }
```

A **GitHub OAuth App** (or GitHub App) must be registered and its `client_id`
supplied to the API server via an environment variable:

```
GITHUB_OAUTH_CLIENT_ID=<your-app-client-id>
```

This can be a Sympozium-owned OAuth App, or operators can register their own
for private GitHub Enterprise instances.

### 10.2 TUI polling in `cmd/sympozium/main.go`

```go
// runGithubSkillSetup handles the GitHub device-flow auth wizard step.
// Called from the onboard wizard and the TUI skill configuration screen.
func runGithubSkillSetup(reader *bufio.Reader, apiBase, instanceName string) error {
    // 1. Prompt for repository and PR settings
    repo       := prompt(reader, "  Target repository (owner/repo)", "")
    branch     := prompt(reader, "  Default branch", "main")
    labels     := prompt(reader, "  PR labels (comma-separated)", "sympozium,automated")
    reviewers  := prompt(reader, "  PR reviewers (comma-separated)", "")

    if repo == "" {
        return fmt.Errorf("target repository is required")
    }

    // 2. Request device code from API server
    resp, err := http.Post(apiBase+"/api/v1/skills/github-gitops/auth", "application/json", nil)
    // ... handle response, extract userCode and verificationURI ...

    fmt.Printf("\n  ┌─────────────────────────────────────────────┐\n")
    fmt.Printf("  │  1. Open: %-35s│\n", verificationURI)
    fmt.Printf("  │  2. Code: %-35s│\n", userCode)
    fmt.Printf("  └─────────────────────────────────────────────┘\n\n")

    // Optionally open the browser
    _ = exec.Command("xdg-open", verificationURI).Start()

    // 3. Poll for completion
    fmt.Println("  Waiting for authorisation", strings.Repeat(".", 3))
    deadline := time.Now().Add(15 * time.Minute)
    for time.Now().Before(deadline) {
        resp2, _ := http.Get(apiBase + "/api/v1/skills/github-gitops/auth/status")
        // ... parse status ...
        if status == "complete" {
            break
        }
        time.Sleep(5 * time.Second)
        fmt.Print(".")
    }

    // 4. Patch instance SkillRef with params
    // ... PATCH SympoziumInstance to add github-gitops SkillRef with params ...

    fmt.Println("\n\n  ✅ GitHub linked!")
    fmt.Printf("     Repository: %s\n", repo)
    return nil
}
```

---

## 13. Makefile Changes

```makefile
# Add to IMAGES list
IMAGES := controller apiserver ipc-bridge webhook agent-runner \
          channel-telegram channel-slack channel-discord channel-whatsapp \
          skill-k8s-ops skill-sre-observability skill-github-gitops  # ← NEW

# Specific target
docker-build-skill-github-gitops:
	docker build -f images/skill-github-gitops/Dockerfile \
	  -t ghcr.io/sympozium-ai/sympozium/skill-github-gitops:$(TAG) .
```

---

## 14. Security Considerations

| Concern | Mitigation |
|---------|------------|
| Token exposure | Stored as a Kubernetes Secret, not in CRD spec; mounted read-only |
| Token scope | The OAuth App requests minimal scopes: `repo` (or `public_repo` for public repos only) |
| Untrusted PRs | Agent constructs PR bodies — policy should gate `execute_command` calls to prevent prompt injection via repo contents |
| Network egress | The sidecar needs HTTPS to `api.github.com` and `github.com`; `NetworkPolicy` should allowlist these |
| RBAC | The sidecar needs **no** Kubernetes API access for this skill; `rbac: []` is intentional |
| Secret rotation | Re-run `POST /api/v1/skills/github-gitops/auth` at any time; all subsequent pods pick up the new token |
| Multi-tenancy | One token per cluster — if multi-tenant token isolation is needed, use separate `secretRef` per instance (requires expanding `SkillRef.Params` to support `secretRef` override) |
| Fingerprint label injection | Labels are set by the agent running inside the sidecar — label names are not validated by GitHub and could theoretically collide; use the `sympozium-fp:` prefix consistently to avoid clashes |

---

## 15. End-to-End Example

```yaml
# 1. Create the instance with github-gitops skill bound to a repo
apiVersion: sympozium.ai/v1alpha1
kind: SympoziumInstance
metadata:
  name: gitops-watcher
  namespace: default
spec:
  provider: openai
  model: gpt-4o
  apiKeyRef:
    secretName: openai-secret
    key: OPENAI_API_KEY
  skills:
    - skillPackRef: k8s-ops
    - skillPackRef: github-gitops
      params:
        targetRepository: "myorg/infra-config"
        defaultBranch: "main"
        prLabels: "sympozium,drift-correction"
        prReviewers: "platform-team"
---
# 2. Create a scheduled run to check for drift every 30 minutes
apiVersion: sympozium.ai/v1alpha1
kind: SympoziumSchedule
metadata:
  name: gitops-drift-check
  namespace: default
spec:
  instanceRef: gitops-watcher
  schedule: "*/30 * * * *"
  type: scheduled
  task: |
    Check all Deployments in the 'production' namespace.
    Compare their current replica counts, resource limits, and
    image tags against what is declared in the GitOps repository
    at $SKILL_TARGETREPOSITORY under the path 'clusters/prod/'.
    
    If any drift is detected, open a pull request with the
    corrected values using the github-gitops skill.
    
    If no drift is found, report: "✅ No drift detected."
```

---

## 16. Implementation Checklist

- [ ] Add `Params` field to `SkillRef` in `api/v1alpha1/sympoziuminstance_types.go`
- [ ] Add `SecretRef` / `SecretMountPath` fields to `SkillSidecar` in `api/v1alpha1/skillpack_types.go`
- [ ] Re-run `make generate` to regenerate deepcopy + CRD YAML
- [ ] Update SkillPack controller (`internal/controller/skillpack_controller.go`) to:
  - Mount `SecretRef` as a volume in the sidecar container spec
  - Inject `Params` as `SKILL_` prefixed env vars into the sidecar
- [ ] Create `config/skills/github-gitops.yaml`
- [ ] Create `images/skill-github-gitops/Dockerfile`
- [ ] Create `images/skill-github-gitops/tool-executor.sh`
- [ ] Create `internal/apiserver/server_github_auth.go` with device-flow endpoints
- [ ] Add `runGithubSkillSetup` to `cmd/sympozium/main.go`
- [ ] Hook `github-gitops` into TUI skill toggle screen with wizard detection via `sympozium.ai/requires-auth: "true"` label
- [ ] Add `skill-github-gitops` to `IMAGES` in `Makefile`
- [ ] Register a GitHub OAuth App and document the `GITHUB_OAUTH_CLIENT_ID` env var
- [ ] Add `sympozium_fingerprint` helper function to `images/skill-github-gitops/tool-executor.sh`
- [ ] Add `report-issue` skill entry to `config/skills/github-gitops.yaml`
- [ ] Write integration test: `test/integration/test-github-gitops-pr.sh`
- [ ] Write integration test: `test/integration/test-github-gitops-dedup.sh` — run a second sweep and verify no duplicate issue/PR is created
- [ ] Update `docs/writing-skills.md` — add note on `SecretRef`, `Params`, and fingerprint label convention
