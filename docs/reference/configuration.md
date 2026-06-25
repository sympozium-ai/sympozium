# Configuration

## Environment Variables

| Variable | Component | Description |
|----------|-----------|-------------|
| `EVENT_BUS_URL` | All | NATS server URL |
| `DATABASE_URL` | API Server | PostgreSQL connection string |
| `INSTANCE_NAME` | Channels | Owning Agent name |
| `MEMORY_ENABLED` | Agent Runner | Whether persistent memory is active |
| `MAX_TOOL_ITERATIONS` | Agent Runner | Maximum LLM round-trips before the agent stops (default: 50). Each round may contain multiple parallel tool calls. Can also be set per-run via `spec.env` in AgentRun CR. |
| `DETAILED_LOG_PATH` | Agent Runner | Directory for untruncated JSONL log files. Empty = disabled. See [Detailed Logging](../guides/detailed-logging.md). |
| `DETAILED_LOG_MAX_SIZE` | Agent Runner | Max size per log file before rotation (default: `50m`). Supports `m` (MB) and `g` (GB) suffixes. |
| `TELEGRAM_BOT_TOKEN` | Telegram | Bot API token |
| `SLACK_BOT_TOKEN` | Slack | Bot OAuth token |
| `SLACK_APP_TOKEN` | Slack | App-level token for Socket Mode |
| `DISCORD_BOT_TOKEN` | Discord | Bot token |
| `WHATSAPP_ACCESS_TOKEN` | WhatsApp | Cloud API access token |

## LLM Providers

Sympozium supports any GenAI provider with an OpenAI-compatible API:

| Provider | Base URL | API Key Variable |
|----------|----------|-----------------|
| OpenAI | (default) | `OPENAI_API_KEY` |
| Anthropic | (default) | `ANTHROPIC_API_KEY` |
| Azure OpenAI | your endpoint | `AZURE_OPENAI_API_KEY` |
| Ollama | `http://ollama:11434/v1` | none |
| LM Studio | `http://localhost:1234/v1` | none |
| llama-server | `http://localhost:8080/v1` | none |
| Unsloth | `http://localhost:8080/v1` | none |
| Any OpenAI-compatible | custom URL | custom |

See the [Ollama guide](../guides/ollama.md), [LM Studio guide](../guides/lm-studio.md), [llama-server guide](../guides/llama-server.md), or [Unsloth guide](../guides/unsloth.md) for detailed local LLM setup.
