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

## Slack Agent Routing

By default the channel router delivers inbound Slack messages to the first
Slack-bound persona in the Ensemble. Two opt-in features give you finer
control: a designated receiver that acts as the front door, and `@name`
addressing that redirects a message to a specific persona.

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
- If **none** set it, today's behavior is preserved (first Slack-bound persona).
- Setting it on **more than one** persona per Ensemble triggers a kubebuilder
  validation warning; only the first match is used at runtime.

### Addressing a persona with `@name`

When an inbound message begins with `@<name>` (or `name:`), the channel router
**redirects the message to the addressed persona** instead of the designated
receiver. This is a direct reassignment: the router looks up the named
persona's Agent instance in the same Ensemble and hands the message to it,
stripping the mention from the text. There is no separate delegation step and
no in-flight or depth guardrail on this path — the addressed persona simply
receives the message as if it had been sent to it directly.

Both prefixes are accepted, so `@billing can I get a refund?` and
`billing: can I get a refund?` route the same way.

Name resolution matches the token (case-insensitive, exact) against each
persona's `name` **or** `displayName` in `spec.agentConfigs`. No
`spec.relationships` edge is required or consulted — any persona in the
Ensemble can be addressed by name.

| Message | Resolves to |
|---|---|
| `@billing can I get a refund?` | persona with `name: billing` (or a single-word `displayName: Billing`) |
| `@docs where are the guides?` | persona with `name: docs` |
| `@unknown help` | no match — see below |

**Unknown names are dropped.** If the token matches no persona, the router
replies with a note listing the available personas (for example, *"Sorry, I
don't know who \"unknown\" is. Available personas: Support Triage, Billing
Support, …"*) and stops — the message is not processed further.

**Missing Agent instance falls through.** If the name matches a persona but its
Agent instance can't be fetched (for example, the Ensemble hasn't finished
reconciling that persona yet), the message falls through to the designated
receiver rather than being dropped.

!!! note
    A `@name` / `name:` token is only recognised as a mention when the name
    itself contains no whitespace. To address a persona whose `displayName`
    has spaces, use its single-word `name` instead.

### No-listener fallback

When no persona has `slackListener: true`, the router falls back to the
existing behavior: the first Agent CR in the Ensemble whose `spec.channels`
includes `slack` is used. This keeps older Ensembles working without changes.

### Per-agent identity

!!! tip
    Replies from an addressed persona can carry a distinct Slack display name
    once per-persona outbound attribution is deployed. See the `displayName`
    field on `AgentConfigSpec` and the `chat:write.customize` Slack scope for
    details.

### Sample Ensemble

A complete example showing the `slackListener` flag and `@name` addressing is
available at
[`examples/yaml/ensemble-slack-routing.yaml`](../../examples/yaml/ensemble-slack-routing.yaml).
