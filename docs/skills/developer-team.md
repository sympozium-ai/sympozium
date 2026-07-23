# Developer Team Ensemble

The `developer-team` Ensemble provides a 2-pizza software development team (7 agents) that collaborates on a single GitHub repository. Each agent has a distinct role, schedule, and set of responsibilities — coordinating through GitHub issues, pull requests, and code reviews.

## Agents

| Agent | Role | Schedule | Skills |
|-------|------|----------|--------|
| **Tech Lead** | Triages issues, reviews architecture, coordinates team, merges PRs | Every 2h | github-gitops, code-review, software-dev |
| **Backend Dev** | Implements server-side features, APIs, business logic | Sweep 1h | github-gitops, code-review, software-dev |
| **Frontend Dev** | Implements UI components, styling, client-side logic | Sweep 1h | github-gitops, code-review, software-dev |
| **QA Engineer** | Writes tests, finds bugs, validates coverage | Sweep 45m | github-gitops, code-review, software-dev |
| **Code Reviewer** | Reviews all PRs for correctness, security, performance | Sweep 30m | github-gitops, code-review, software-dev |
| **DevOps Engineer** | Maintains CI/CD, Dockerfiles, deployment configs | Sweep 2h | github-gitops, code-review, software-dev |
| **Docs Writer** | Updates documentation, READMEs, changelogs | Weekdays 6pm | github-gitops, code-review, software-dev |

## Prerequisites

- **GitHub token**: A personal access token or GitHub App with `repo` scope, stored as a Kubernetes secret named `github-gitops-token`
- **AI provider**: An API key for OpenAI, Anthropic, Azure, or Ollama

## Quick Start

### Via the TUI (recommended)

```bash
sympozium
```

1. Navigate to the **Personas** tab (press `1`)
2. Select `developer-team` and press **Enter**
3. Choose your AI provider and paste an API key
4. Complete the GitHub authentication wizard
5. Confirm — the controller creates all 7 instances and schedules

### Via kubectl

```bash
# 1. Apply the Ensemble
kubectl apply -f config/personas/developer-team.yaml

# 2. Create the AI provider secret
kubectl create secret generic dev-team-openai-key \
  --from-literal=OPENAI_API_KEY=sk-... \
  -n sympozium-system

# 3. Create the GitHub token secret
kubectl create secret generic github-gitops-token \
  --from-literal=GH_TOKEN=ghp_... \
  -n sympozium-system

# 4. Activate the pack
kubectl patch ensemble developer-team -n sympozium-system --type=merge -p '{
  "spec": {
    "enabled": true,
    "authRefs": [{"provider": "openai", "secret": "dev-team-openai-key"}]
  }
}'
```

## How the Team Coordinates

Agents coordinate entirely through GitHub:

1. **Tech Lead** triages issues by adding role labels (`backend`, `frontend`, `qa`, `devops`, `docs`)
2. **Developers** pick up issues matching their role label
3. Each agent creates a branch following the convention `sympozium/<role>/<issue>-<description>`
4. **Code Reviewer** reviews all PRs labelled `sympozium` and leaves structured feedback
5. **QA Engineer** validates PRs for test coverage and opens follow-up PRs for missing tests
6. **Tech Lead** merges approved PRs and posts status updates on tracking issues

### Branch naming convention

```
sympozium/backend/<issue>-<desc>    # Backend work
sympozium/frontend/<issue>-<desc>   # Frontend work
sympozium/devops/<issue>-<desc>     # CI/CD and infra
sympozium/docs/<issue>-<desc>       # Documentation
sympozium/qa/<issue>-<desc>         # Test additions
sympozium/fix/<issue>-<desc>        # Bug fixes
```

### Deduplication

Before creating issues or PRs, agents check for existing work:

```bash
gh pr list --repo "$REPO" --label "sympozium" --state open
gh issue list --repo "$REPO" --label "sympozium" --state open
```

If another agent has already opened a PR for the same issue, they review it instead of duplicating work.

## Skills

The developer-team pack uses three skills:

| Skill | Purpose |
|-------|---------|
| `github-gitops` | GitHub CLI access (`gh`), git operations, PR/issue management |
| `code-review` | Structured code review methodology, security patterns, Go-specific review |
| `software-dev` | Development workflow (clone, branch, code, test, PR), team coordination patterns |

### software-dev skill

The `software-dev` SkillPack (`config/skills/software-dev.yaml`) is a new skill created for this pack. It teaches agents how to:

- Clone and branch from the target repository
- Explore and understand an unfamiliar codebase
- Write clean commits with conventional commit messages
- Open well-structured PRs that reference issues
- Coordinate with other team members via GitHub
- Review code and leave constructive feedback

It uses the same `github-gitops` sidecar image (which includes `gh`, `git`, `bash`, and `jq`).

## Customisation

### Targeting a specific repository

Set the target repository via skill params when creating instances manually:

```yaml
spec:
  skills:
    - skillPackRef: github-gitops
      params:
        targetRepository: "myorg/myrepo"
        defaultBranch: "main"
        prLabels: "sympozium,automated"
        prReviewers: "platform-team"
```

When using the TUI onboarding wizard, you'll be prompted for the target repository.

### Disabling individual agents

You can disable specific agents without removing the entire pack:

```bash
kubectl patch ensemble developer-team --type=merge -p '{
  "spec": {
    "excludePersonas": ["docs-writer", "devops-engineer"]
  }
}'
```

Or toggle them in the TUI by pressing Enter on the pack and using Space to toggle agents.

### Adjusting schedules

Edit the Ensemble YAML to change intervals:

```yaml
agentConfigs:
  - name: code-reviewer
    schedule:
      type: sweep
      interval: "15m"  # Review PRs more frequently
```

## Integration Test

An end-to-end integration test simulates the full team workflow on a real GitHub repository:

```bash
# Requires GITHUB_TOKEN with repo scope
GITHUB_TOKEN=ghp_... bash test/integration/developer_team_test.sh
```

The test:
1. **Tech Lead** creates a tracking issue
2. **Backend Dev** creates a Dockerfile and opens a PR
3. **DevOps Engineer** creates docker-compose + CI workflow and opens a PR
4. **Docs Writer** updates the README and opens a PR
5. **Code Reviewer** reviews all 3 PRs
6. **QA Engineer** validates the Dockerfile with a structured checklist
7. **Tech Lead** posts a final coordination status update

All actions happen on the real repository using the `gh` CLI — the same tooling the agents use in production.

## Helm

The developer-team pack is included in the Helm chart and installed by default:

```yaml
# values.yaml
defaultPersonas:
  enabled: true   # Installs Ensemble CRDs (disabled until activated)
```

The pack is installed in a disabled state. Activate it via the TUI or kubectl after setting up auth.
