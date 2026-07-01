# Channels

Channels connect Sympozium to external messaging platforms. Each channel runs as a dedicated Kubernetes Deployment. Messages flow through NATS JetStream and are routed to AgentRuns by the channel router.

## Supported Channels

| Channel | Protocol | Self-chat | Status |
|---------|----------|-----------|--------|
| **WhatsApp** | WhatsApp Web (multidevice) via `whatsmeow` | Owner can message themselves to interact with agents | **Stable** |
| **Telegram** | Bot API (`tgbotapi`) | Owner can message themselves to interact with agents | **Stable** |
| **Discord** | Gateway WebSocket (`discordgo`) | — | **Alpha** |
| **Slack** | Socket Mode (`slack-go`) | — | **Alpha** |

!!! info
    **Stable** — tested and actively used. **Alpha** — implemented but not yet production-tested.

Channels are optional. You can always interact through the TUI, web dashboard, or by creating AgentRun CRs directly with kubectl.

## Connecting Channels

Connect channels during onboarding or via the TUI edit modal:

| Channel | How to connect |
|---------|----------------|
| **Telegram** | Create a bot with [@BotFather](https://t.me/BotFather), get the token, pass it during onboarding or set it in the Agent channel config |
| **Slack** | Create a Slack app with Socket Mode enabled, add the bot/app token during onboarding |
| **Discord** | Create a Discord bot, grab the token, and connect it during onboarding |
| **WhatsApp** | Use the WhatsApp Business API — Sympozium displays a QR code in the TUI for pairing |

## Slack Setup (Socket Mode)

For reliable Slack connectivity, configure your Slack app with both tokens and required app settings:

- Provide both secrets in the channel secret:
    - `SLACK_BOT_TOKEN` (`xoxb-...`)
    - `SLACK_APP_TOKEN` (`xapp-...`)
- Enable **App Home → Messages Tab** and allow users to message the app
- Enable **Socket Mode**
- Add bot event subscriptions:
    - `message.im`
    - `message.channels`
    - `app_mention`
- Reinstall the app after changing scopes or event subscriptions

!!! warning
    If `SLACK_APP_TOKEN` is omitted, Sympozium falls back to Slack Events API mode, which requires a publicly reachable webhook URL.

## Slack Agent Routing (ISI-1497)

By default the channel router delivers inbound Slack messages to the first
Slack-bound persona in the Ensemble.  Two opt-in features give you finer
control:

### Designated Slack Receiver (`slackListener`)

Mark exactly one `agentConfig` with `slackListener: true` to make it the
explicit front door for all inbound Slack messages:

```yaml
agentConfigs:
  - name: triage
    displayName: "Support Triage"
    slackListener: true   # this persona receives all Slack messages
    channels:
      - slack
    systemPrompt: "..."
```

Rules:

- If **one** persona sets `slackListener: true`, all Slack messages go there.
- If **none** set it, today's behaviour is preserved (first Slack-bound persona).
- Setting it on **more than one** persona per Ensemble triggers a kubebuilder
  validation warning; only the first match is used at runtime.

### `@name` → Delegation

When the designated receiver gets a message that starts with `@<name>`, the
channel router turns the request into a **delegation** to the named persona
using the built-in delegation executor (ISI-1463 guardrails apply).

Name resolution is case-insensitive and checks both `Name` and `DisplayName`:

| Message | Resolves to |
|---|---|
| `@billing can I get a refund?` | persona with `name: billing` or `displayName: Billing` |
| `@Engineering Support debug this` | persona with `displayName: Engineering Support` |
| `@unknown help` | stays on receiver, friendly "I don't know that name" reply |

The named persona must be reachable from the receiver via a `delegation`
relationship in `spec.relationships`.  If no delegation edge exists the
message stays on the receiver.

### No-Listener Fallback

When no persona has `slackListener: true`, the router falls back to the
existing behaviour: the first Agent CR in the Ensemble whose `spec.channels`
includes `slack` is used.  This keeps older Ensembles working without changes.

### Per-Agent Identity

!!! tip "Per-Agent Identity"
    Replies from a delegated persona can carry a distinct Slack display name
    and icon once ISI-1497 C4 (outbound per-persona attribution) is deployed.
    See the `displayName` field on `AgentConfigSpec` and the `chat:write.customize`
    Slack scope for details.

### Sample Ensemble

A complete example showing the `slackListener` flag and delegation relationships
is available at [`examples/yaml/ensemble-slack-routing.yaml`](https://github.com/sympozium-ai/sympozium/blob/main/examples/yaml/ensemble-slack-routing.yaml).
