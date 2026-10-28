# CLI Commands

Every command accepts the global flags `--kubeconfig PATH` and
`-n/--namespace NS` (default `default`).

## Install & Uninstall

```bash
sympozium install                        # CRDs, control plane, built-in Ensembles, the Celln fleet, the model gateway and ergoz
sympozium install --image-tag v0.12.0    # a specific image tag
sympozium install --set key=value        # extra Helm values (repeatable)
sympozium install --no-celln             # skip Celln entirely (no fleet, router or gateway)
sympozium install --no-ergoz             # skip ergoz (accelerator power telemetry)
sympozium install --adopt-existing       # adopt leftover objects of an older install instead of refusing
sympozium doctor                         # read-only checklist of what is in the way, with remedies (--json)
sympozium uninstall                      # clean removal
```

`sympozium install` is idempotent and asks nothing about models. Rerun it to
upgrade, after labeling more KVM nodes, or to migrate a 0.11 keyed fleet
(see [Upgrading from 0.11](../guides/celln-fleet-installation.md#upgrading-from-011)).

## Update & Upgrade

```bash
sympozium update                 # update this CLI to the latest release (--check, --version vX.Y.Z, --force)
sympozium upgrade                # upgrade the cluster installation to this CLI's release
sympozium upgrade --dry-run      # show what would change (also --image-tag, --set, --no-ergoz)
```

Run `sympozium update` first, then `sympozium upgrade`, so the cluster moves
to the chart embedded in the new CLI. `upgrade` reuses the deployed release's
values and refreshes the image tags, the Celln node and router images and the
model gateway digest to the ones the new CLI pins.

### Celln

A bare `sympozium install` deploys the **Celln fleet** whenever the build
pins a starter package (every release does): two privileged DaemonSets on
every node labeled `celln.dev/kvm=true`, the router and the model gateway.
It prints the privilege it needs before it proceeds. No provider or key is
asked for: each Agent brings its own `ModelConnection` and key, which the
gateway adds to its requests and no node ever holds. See
[Celln Fleet Installation](../guides/celln-fleet-installation.md) and
[Agent-bound model access](../guides/celln-mediated-model-access.md).

| Flag | Default | Description |
|------|---------|-------------|
| `--no-celln` | `false` | Install without Celln |
| `--celln-fleet` | on when the build pins a package | Deploy the fleet explicitly; with the three package flags for your own package (a source build pins none) |
| `--celln-fleet-package-image`, `--celln-fleet-package-hash`, `--celln-fleet-publisher` | the release's starter package | A reviewed, digest-pinned starter package (`repository@sha256:…`, `blake3:…`, publisher key from `celln starter-inspect`) |
| `--celln-fleet-scope` | `starter` | Stable installation identity; node state lives at `/var/lib/sympozium-celln/<scope>` |
| `--celln-fleet-principal` | `sympozium:celln` | Parent principal every fleet owner authenticates |
| `--celln-fleet-authorise` | `all` | `all` (every namespace except `kube-*`, `cert-manager`, the control-plane namespaces and namespaces labeled `celln.sympozium.ai/excluded`) or `labeled` (`celln.sympozium.ai/scope=<scope>` only) |
| `--celln-fleet-https-host` | any public host | Restrict the `https-fetch`/`https-post-json` tools to these hosts (repeatable); private addresses are always refused |
| `--celln-fleet-max-lease-seconds`, `-max-turns`, `-max-model-requests`, `-max-output-tokens` | 86400 / 256 / 1536 / 6291456 | Scope ceilings for every parent; output tokens are sized as turns × 6 × 4096 when unset |
| `--model-gateway-image` | the release's pin | Digest-pinned model gateway image; a source build pins none and needs this flag |
| `--celln-fleet-replace-package` | `false` | Approve moving an installed scope to another package or scope (ends live parents) |
| `--celln-fleet-output-dir` | `~/.sympozium/celln-fleet/<scope>` | Private directory for configuration and install records |
| `--celln-fleet-wait` | `15m` | How long to wait for the first labeled node |
| `--celln-router-image` | `ghcr.io/sympozium-ai/celln:v0.5.35` | Router image, `repo:tag` or `repo@sha256:…` |
| `--celln-installer-image` | `ghcr.io/sympozium-ai/sympozium/celln-installer:<release>` | The fleet node image (the `celln` binary and runtime assets); a source build uses the embedded chart's `appVersion` |
| `--celln-router-replicas` | `1` | Router replicas for the generated ownership PVC |

`--enable-hermetic-workloads` is deprecated and does nothing: Celln is on by
default; use `--no-celln` to skip it.

### `sympozium doctor`

Reads the cluster and prints `PASS`, `WARN` or `FAIL` per check, each problem
with the command that fixes it; nothing is changed, and it exits 1 when any
check fails.

| Flag | Default | Description |
|------|---------|-------------|
| `--json` | `false` | Print the report as JSON |
| `--celln-fleet-scope` | `starter` | Fleet scope the install would use, for the package comparison |
| `--celln-fleet-package-hash` | the build's starter package | Package the install would use |

Checks: **Cluster**, **Helm release**, **Ownership**, **Terminating**, **KVM
nodes**, **Fleet pods**, **Fleet package**, **Fleet turn budget** and
**Mediated model access** (trust objects, gateway readiness and the policy's
`allowedOrigins`/`deniedOrigins` guardrail).

### `sympozium celln-mediation bootstrap`

Mints the model gateway's trust (issuer key, JWKS, transport tokens, private
CA and certificates) once and publishes it as Secrets and ConfigMaps;
`sympozium install` does this itself, so the command is for Helm-only
installs. It prints the credential-free chart values to pass to `helm
upgrade`; an existing installation is verified and never replaced.

| Flag | Default | Description |
|------|---------|-------------|
| `--cluster-id` | required | Cluster identity bound into every signed decision |
| `--system-namespace` | `sympozium-system` | Control-plane namespace of the release |
| `--release-fullname` | `sympozium` | Chart full name; the gateway Service is `<fullname>-model-gateway` |
| `--gateway-host`, `--receiver-host` | the chart's Services | Certificate names |
| `--validity` | ten years | Lifetime of the CA and both certificates; rotation is the renewal |
| `--values-out` | stdout | Write the chart values to this file |

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

## Development

```bash
make test         # unit tests with the race detector
make test-system  # envtest system tests (no cluster needed)
make lint         # run linter
make manifests    # generate CRD manifests and sync the charts
make run-controller  # run controller locally (needs kubeconfig)
```
