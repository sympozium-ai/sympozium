# CLI Commands

Every command accepts the global flags `--kubeconfig PATH` and
`-n/--namespace NS` (default `default`).

## Install & Uninstall

```bash
sympozium install                        # CRDs, control plane, built-in Ensembles, Celln and ergoz
sympozium install --image-tag v0.10.77   # a specific image tag
sympozium install --set key=value        # extra Helm values (repeatable)
sympozium install --no-celln             # skip Celln entirely
sympozium install --no-ergoz             # skip ergoz (accelerator power telemetry)
sympozium install --adopt-existing       # adopt leftover objects of an older install instead of refusing
sympozium doctor                         # read-only checklist of what is in the way, with remedies (--json)
sympozium uninstall                      # clean removal
```

`sympozium install` is idempotent; rerun it to add a Celln model backend.

## Update & Upgrade

```bash
sympozium update                 # update this CLI to the latest release (--check, --version vX.Y.Z, --force)
sympozium upgrade                # upgrade the cluster installation to this CLI's release
sympozium upgrade --dry-run      # show what would change (also --image-tag, --set, --no-ergoz)
```

Run `sympozium update` first, then `sympozium upgrade`, so the cluster moves
to the chart embedded in the new CLI. With mediated model access, `upgrade`
also moves the model gateway to the digest the new CLI pins (unless the
release runs a gateway from another repository).

### Celln

A bare `sympozium install` installs the **Celln fleet** when it finds a model
backend: `DEEPSEEK_API_KEY`, `OPENAI_API_KEY` or `ANTHROPIC_API_KEY` (each
present key becomes a backend), or explicit specs in
`SYMPOZIUM_CELLN_BACKEND` (semicolon-separated, same syntax as
`--celln-fleet-backend`). In a terminal with none set it asks for a provider
and key; without a terminal it installs only the one-shot router. See
[Celln Fleet Installation](../guides/celln-fleet-installation.md).

Every fleet install mediates model access by default: the installer
bootstraps the model gateway's trust, your key becomes the own key of the
`starter` Agent (Secret `starter-model-key`, ModelConnection and Agent
`starter` in the `-n` namespace) and no Celln node holds it. See
[Mediated model access](../guides/celln-mediated-model-access.md).

| Flag | Default | Description |
|------|---------|-------------|
| `--celln-fleet` | auto | Run the native Celln plane as a per-node fleet (implied by a found backend) |
| `--celln-fleet-backend` | — | A model backend, repeatable: `name=…,provider=…,model=…[,endpoint=URL][,protocol=openai-chat\|anthropic-messages][,credential-file=/path][,allow-insecure=true][,parameters-file=/abs.json][,max-output-tokens=N]` |
| `--celln-fleet-model-provider`, `--celln-fleet-model`, `--celln-fleet-model-endpoint`, `--celln-fleet-model-protocol`, `--celln-fleet-model-allow-insecure`, `--celln-fleet-model-credential-file` | `deepseek` | Define the single backend named `native` when `--celln-fleet-backend` is not used |
| `--celln-fleet-scope` | `starter` | Stable installation identity; node state lives at `/var/lib/sympozium-celln/<scope>` |
| `--celln-fleet-package-image`, `--celln-fleet-package-hash`, `--celln-fleet-publisher` | the release's starter package | Your own reviewed, digest-pinned starter package |
| `--celln-fleet-https-host` | any public host | Restrict the `https-fetch`/`https-post-json` tools to these hosts (repeatable). Unset, they may reach any public HTTPS host; private, loopback and link-local addresses are always refused |
| `--celln-fleet-authorise` | `all` | `all` (every non-system namespace) or `labeled` (`celln.sympozium.ai/scope=<scope>` only) |
| `--celln-fleet-max-lease-seconds`, `-max-turns`, `-max-model-requests`, `-max-output-tokens` | 86400 / 256 / 1536 / 786432 | Scope ceilings for every parent (tokens scale with the largest backend cap) |
| `--celln-fleet-model-parameters-file`, `--celln-fleet-model-max-output-tokens` | — | Model parameters and per-request output cap for the single backend (`parameters-file=`, `max-output-tokens=` in a backend spec) |
| `--celln-fleet-replace-package` | `false` | Move an installed scope to a new starter package (ends live parents) |
| `--celln-fleet-skip-preflight` | `false` | Skip the one-token chat probe of each backend |
| `--celln-fleet-output-dir` | `~/.sympozium/celln-fleet/<scope>` | Private directory for configuration and install records |
| `--celln-fleet-wait` | `15m` | How long to wait for the first labeled node |
| `--no-celln-mediation` | `false` | Opt out of mediated model access: publish the installer's key to every fleet node instead of the starter Agent |
| `--model-gateway-image` | the release's pin | Digest-pinned model gateway image; a source build pins none and installs without mediation unless this is given |
| `--celln-starter-namespace` | the `-n` namespace | Namespace of the starter Agent that owns the installer's key |
| `--celln-mediated-route`, `--celln-mediate-backends` | — | Further providers (or every HTTPS backend) Agents may bring their own key for |
| `--celln-native-approve-starter-tools` | `false` | Approve the starter tool grants |
| `--celln-host-installer` | `false` | Legacy bare-metal host dispatcher (with `--celln-backend URL`) |
| `--celln-native`, `--celln-native-*` | — | The single-node [native installation](../guides/celln-native-installation.md) |

## Onboarding

```bash
sympozium onboard                        # interactive text wizard: namespace, Agent, provider,
                                         # GitHub repo, instructions, channel, policy, heartbeat
```

## Launch Interfaces

```bash
sympozium                                # launch the interactive TUI (also: sympozium tui)
sympozium serve                          # port-forward the web dashboard and print its token
sympozium token                          # print the web dashboard authentication token
```

### `sympozium serve` Options

| Flag | Default | Description |
|------|---------|-------------|
| `--port` | `9090` | Local port to forward to |
| `--open` | `false` | Automatically open a browser |
| `--service-namespace` | `sympozium-system` | Namespace of the apiserver service |

### `sympozium token`

Prints only the dashboard bearer token, which is useful after a manual
port-forward. It requires permission to read the `sympozium-ui-token` Secret.

```bash
sympozium token
sympozium token --service-namespace sympozium-system
```

## Resource Management

```bash
sympozium agents list | get NAME | delete NAME
sympozium runs list | get NAME | logs NAME
sympozium policies list | get NAME
sympozium skills list
sympozium mcp-servers list | get NAME | delete NAME
sympozium mcp-servers create NAME --prefix gh --url https://mcp.example/sse   # external
sympozium mcp-servers create NAME --prefix pg --image REPO@sha256:… --transport stdio
sympozium features list --policy default-policy
sympozium features enable browser-automation --policy default-policy
sympozium features disable browser-automation --policy default-policy
sympozium version
```

### Workspaces

Per-session persistent workspaces (`WorkspaceSession` + PVC):

```bash
sympozium workspace list [--agent NAME] [--ensemble NAME]
sympozium workspace show NAME            # session, PVC and last AgentRun
sympozium workspace delete NAME [--force]  # cascades to the PVC
sympozium workspace exec NAME [--image alpine:3.20] [--ttl 1h]  # debug pod with the PVC at /workspace
```

### Celln operator tools

`sympozium celln-tool` holds operator-only commands for reviewing tools and
managing Celln grants. Most installations never need them; the fleet
installer does this for you.

| Command | Purpose |
|---------|---------|
| `celln-tool inspect NAME` | Print a tool submission's identity for review |
| `celln-tool approve NAME` | Verify a local bundle and publish a reviewed revision |
| `celln-tool plan AGENT` | Resolve live grants into a composition plan without executing |
| `celln-tool compose` / `issue` / `issue-remote` / `issue-run AGENT` | Catalogue issuance against a host issuer |
| `celln-tool withdraw-grant REPORT`, `recover-grants` | Withdraw issued host profiles |
| `celln-tool serve-issuer --config FILE` | Run the TLS host issuer |
| `celln-tool install-native` | Install a native starter catalogue and grant layers |

## Development

```bash
make test         # unit tests with the race detector
make test-system  # envtest system tests (no cluster needed)
make lint         # run linter
make manifests    # generate CRD manifests and sync the charts
make run-controller  # run controller locally (needs kubeconfig)
```
