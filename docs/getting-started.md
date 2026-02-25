# Getting Started with KubeClaw

This guide walks you through installing KubeClaw, deploying it to a Kubernetes cluster,
and using both the interactive TUI and the CLI to create and manage AI agents.

---

## 1. Install the CLI

Run the installer — it will place the `kubeclaw` binary in `~/.local/bin` (or
`/usr/local/bin` if it is writable):

```sh
curl -fsSL https://deploy.k8sclaw.ai/install.sh | sh
```

To install system-wide (requires sudo, must be run directly, not piped):

```sh
sh <(curl -fsSL https://deploy.k8sclaw.ai/install.sh)
```

To install only to `~/.local/bin` without sudo:

```sh
curl -fsSL https://deploy.k8sclaw.ai/install.sh | sh -s -- --local
```

Verify the install:

```sh
kubeclaw version
```

---

## 2. Deploy KubeClaw to Your Cluster

Make sure `kubectl` is configured and pointing at your target cluster, then run:

```sh
kubeclaw install
```

What this does:
1. Downloads the release manifests from GitHub
2. Applies CRDs (`ClawInstance`, `AgentRun`, `ClawPolicy`, `SkillPack`, `ClawSchedule`)
3. Creates the `kubeclaw-system` namespace
4. Deploys the NATS event bus
5. Installs cert-manager (if not already present) and creates the webhook certificate
6. Deploys the controller, API server, and admission webhook
7. Applies RBAC and network policies
8. Installs default SkillPacks

To uninstall:

```sh
kubeclaw uninstall
```

---

## 3. Onboard Your First Agent

The onboard wizard is the fastest way to get up and running — it creates a
`ClawInstance` with your chosen AI provider and, optionally, a messaging channel.

```sh
kubeclaw onboard
```

Or launch it from inside the TUI by pressing **O** or typing `/onboard`.

### Wizard steps

| Step | What you provide |
|------|-----------------|
| 1 — Cluster check | Automatic — verifies CRDs are installed |
| 2 — Instance name | A name for your agent (e.g. `my-agent`) |
| 3 — AI provider | OpenAI, Anthropic, Azure OpenAI, Ollama, or custom |
| 4 — Channel | Telegram, Slack, Discord, WhatsApp, or skip |
| 5 — Default policy | Whether to apply the default `ClawPolicy` |

At the end you see a summary and confirm before anything is applied.

**After onboarding you will see:**

```
  ✅ Onboarding complete!

  Next steps:
  ─────────────────────────────────────────────────
  • Check your instance:   kubeclaw instances get my-agent
  • Send a message to your Telegram bot — it's live!
  • Run an agent:          kubectl apply -f config/samples/agentrun_sample.yaml
  • View runs:             kubeclaw runs list
  • Feature gates:         kubeclaw features list --policy default-policy
```

---

## 4. The Interactive TUI

Launch the TUI by running `kubeclaw` (or `kubeclaw tui`):

```sh
kubeclaw
```

The screen is divided into:
- **Tab bar** at the top (views 1–7)
- **Table pane** showing the active view's resources
- **Feed pane** on the right showing recent runs for the selected instance
- **Log bar** at the bottom for status messages and command output
- **Command input** at the very bottom (activated with `/`)

### Views

Press the number key or **Tab** / **Shift+Tab** to switch views:

| Key | View | What you see |
|-----|------|-------------|
| `1` | Instances | All `ClawInstance` resources — name, phase, channels, active pods |
| `2` | Runs | All `AgentRun` resources — instance, phase, pod name, age |
| `3` | Policies | All `ClawPolicy` resources — name, bound instances, age |
| `4` | Skills | All `SkillPack` resources — name, skill count, ConfigMap |
| `5` | Channels | Channels across all instances — type, secret, status |
| `6` | Pods | Agent pods — instance, phase, node, IP, restarts |
| `7` | Schedules | All `ClawSchedule` resources — instance, cron, type, status |

### Navigation keys

| Key | Action |
|-----|--------|
| `↑` / `k` | Move selection up |
| `↓` / `j` | Move selection down |
| `Tab` | Next view |
| `Shift+Tab` | Previous view |
| `1`–`7` | Jump to view |
| `Esc` | Go back / return to Instances view |
| `?` | Open help modal |
| `r` | Refresh data from cluster |

### Row action keys (on any selected item)

| Key | Action |
|-----|--------|
| `Enter` | Show detail for the selected resource |
| `l` | View logs for the selected pod or resource |
| `d` | Describe the selected resource (like `kubectl describe`) |
| `x` | Delete the selected resource (asks for confirmation: press `y`) |
| `e` | Edit the selected instance (memory, heartbeat schedule, skills) |
| `R` | Create a new `AgentRun` on the selected instance |
| `O` | Launch the onboard wizard |

### Feed pane

The feed pane on the right shows the conversation history for the selected
instance. Press **f** to expand it to fullscreen.

Inside fullscreen feed:

| Key | Action |
|-----|--------|
| `i` / `/` / `Enter` | Enter chat input — type a task and press Enter to create a run |
| `Esc` | Exit chat input |
| `↑` / `k` | Scroll up |
| `↓` / `j` | Scroll down |
| `g` | Jump to top |
| `G` | Jump to bottom (latest) |
| `f` or `q` | Exit fullscreen feed |

### Edit modal (`e`)

Select an instance and press **e** to open the edit modal. It has three tabs:

| Tab | What you can change |
|-----|---------------------|
| **Memory** | Enable/disable memory, max size in KB, system prompt |
| **Heartbeat** | Cron schedule, task text, schedule type, concurrency policy, suspend |
| **Skills** | Toggle SkillPacks on/off with `Space` or `Enter` |

Navigate tabs with **Tab** / **Shift+Tab**, fields with `j`/`k`, save with **Ctrl+S**, cancel with **Esc**.

---

## 5. Slash Commands

Press `/` in the TUI to enter command mode. Type the beginning of a command to
see auto-complete suggestions — navigate them with `↑`/`↓` and accept with
**Tab** or **Enter**.

### Instance & run commands

| Command | Description |
|---------|-------------|
| `/instances` | Switch to Instances view |
| `/runs` | Switch to Runs view |
| `/run <instance> <task>` | Create a new `AgentRun` on the given instance |
| `/abort <run-name>` | Abort a running `AgentRun` |
| `/result <run-name>` | Show the result / LLM response for a completed run |
| `/status [run-name]` | Show cluster or run status |

**Example — run a task:**
```
/run my-agent "List all pods in the default namespace"
```

### Channel commands

| Command | Description |
|---------|-------------|
| `/channels [instance]` | Switch to Channels view (optionally filtered) |
| `/channel <inst> <type> <secret>` | Add a channel to an instance |
| `/rmchannel <inst> <type>` | Remove a channel |

Supported channel types: `telegram`, `slack`, `discord`, `whatsapp`

**Example — add a Telegram channel:**
```
/channel my-agent telegram my-telegram-secret
```

### Provider & model

| Command | Description |
|---------|-------------|
| `/provider <inst> <provider> <model>` | Change the AI provider and model |
| `/baseurl <inst> <url>` | Set a custom base URL (Ollama, Azure, etc.) |

**Example:**
```
/provider my-agent anthropic claude-sonnet-4-20250514
```

### Other resource views

| Command | Description |
|---------|-------------|
| `/policies` | Switch to Policies view |
| `/skills` | Switch to Skills view |
| `/pods [instance]` | Switch to Pods view |
| `/schedules` | Switch to Schedules view |
| `/memory <instance>` | View memory contents for an instance |

### Policy & feature gates

| Command | Description |
|---------|-------------|
| `/features <policy>` | Show feature gates on a policy |
| `/delete <type> <name>` | Delete a resource (`instance`, `run`, `policy`, `schedule`, `channel`) |

**Example:**
```
/features default-policy
/delete run my-agent-run-abc123
```

### Schedule commands

| Command | Description |
|---------|-------------|
| `/schedule <inst> <cron> <task>` | Create a recurring `ClawSchedule` |

**Example — run a task every hour:**
```
/schedule my-agent "0 * * * *" "Summarise open PRs"
```

### Utility

| Command | Description |
|---------|-------------|
| `/ns <namespace>` | Switch to a different namespace |
| `/onboard` | Launch the interactive setup wizard |
| `/help` or `?` | Show the help modal |
| `/quit` | Exit the TUI |

---

## 6. CLI Reference

For scripting and CI use the CLI subcommands directly:

```sh
# Instances
kubeclaw instances list
kubeclaw instances get <name>
kubeclaw instances delete <name>

# Runs
kubeclaw runs list
kubeclaw runs get <name>
kubeclaw runs logs <name>          # prints the kubectl logs command

# Policies
kubeclaw policies list
kubeclaw policies get <name>

# Skills
kubeclaw skills list

# Feature gates
kubeclaw features list   --policy <policy>
kubeclaw features enable <feature> --policy <policy>
kubeclaw features disable <feature> --policy <policy>

# Namespace (-n / --namespace)
kubeclaw instances list -n production
```

All commands accept `--kubeconfig` and `--namespace` / `-n` flags.

---

## 7. What to Expect: A Full Walkthrough

### Step 1 — Install and deploy

```sh
curl -fsSL https://deploy.k8sclaw.ai/install.sh | sh
kubeclaw install
```

You will see:

```
  Installing KubeClaw v0.0.32...
  Downloading manifests...
  Extracting...
  Applying CRDs...
  Creating namespace...
  Deploying NATS event bus...
  Checking cert-manager...
  Installing cert-manager...
  Waiting for cert-manager to be ready...
  Creating webhook certificate...
  Applying RBAC...
  Deploying control plane...
  Deploying webhook...
  Applying network policies...
  Installing default SkillPacks...

  KubeClaw installed successfully!
  Run: kubeclaw
```

### Step 2 — Onboard

```sh
kubeclaw onboard
```

Walk through the 5-step wizard. After confirming, you will see secrets and the
`ClawInstance` being created live:

```
  Creating secret my-agent-openai-key...
  Applying default ClawPolicy...
  Creating ClawInstance my-agent...

  ✅ Onboarding complete!
```

### Step 3 — Open the TUI

```sh
kubeclaw
```

You arrive on the **Instances** view (tab `1`). Your new instance appears with
phase `Ready`.

### Step 4 — Create your first run

Select the instance with `↑`/`↓` and press **R**, or type:

```
/run my-agent "What Kubernetes version is this cluster running?"
```

Switch to the **Runs** view (`2`) to watch the phase change:
`Pending` → `Running` → `Succeeded`

Press **Enter** on the completed run to see the full result, or press **l** to
tail the agent pod logs.

### Step 5 — Explore

- Press `5` to see your channel in the **Channels** view
- Press `6` to see the completed agent pod in the **Pods** view
- Press `e` on your instance to tune memory or add a heartbeat schedule
- Press `f` to expand the feed and chat with your agent directly
