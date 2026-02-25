# Contributing to Sympozium

Thanks for your interest in contributing! This document covers how we work, what we expect from PRs, and how to get started.

---

## Where to Start

1. **Issues** — Check [GitHub Issues](https://github.com/AlexsJones/sympozium/issues) for open bugs, feature requests, and `good first issue` labels.
2. **Roadmap** — The project roadmap lives in [GitHub Projects](https://github.com/AlexsJones/sympozium/projects). Pick items from the current milestone.
3. **AGENTS.md** — If you're an AI coding agent (Copilot, Cursor, etc.), read [`AGENTS.md`](AGENTS.md) for repo layout, build instructions, and common task recipes.
4. **Documentation** — Architecture and guides live in [`docs/`](docs/):
   - [`sympozium-design.md`](docs/sympozium-design.md) — Full architecture and CRD schemas
   - [`writing-tools.md`](docs/writing-tools.md) — How to add agent tools
   - [`writing-skills.md`](docs/writing-skills.md) — How to create SkillPack CRDs
   - [`writing-integration-tests.md`](docs/writing-integration-tests.md) — Integration test patterns

---

## Development Setup

See [`AGENTS.md`](AGENTS.md) for the full setup guide. The short version:

```bash
# Prerequisites: Go 1.25+, Docker, Kind, kubectl
kind create cluster --name kind
make install                          # Install CRDs
make docker-build TAG=v0.0.32        # Build all images
# Load images into Kind (see AGENTS.md for the full loop)
kubectl apply -k config/             # Deploy control plane
```

---

## GitHub Checks

Every push and PR runs the following checks via GitHub Actions (`.github/workflows/build.yaml`):

| Check | What it does |
|-------|-------------|
| **go vet** | Static analysis for common Go mistakes |
| **go test -race -short** | Unit tests with the race detector enabled |
| **Docker build** | All 10 component images build successfully |

PRs must pass all checks before merging. On merge to `main`, images are automatically built and pushed to `ghcr.io/alexsjones/sympozium/`.

Run checks locally before pushing:

```bash
make vet        # go vet ./...
make test       # go test -race ./...
make build      # compile all binaries
go build ./...  # quick compile check
```

---

## Multi-Architecture Builds

Sympozium supports `linux/amd64` and `linux/arm64` (darwin for the CLI).

- **Docker images** are built with Docker Buildx + `docker/build-push-action@v6` with GitHub Actions cache (`type=gha`).
- **CLI releases** are cross-compiled for `linux/amd64`, `linux/arm64`, `darwin/amd64`, and `darwin/arm64` via the release workflow (`.github/workflows/release.yaml`).
- All Go binaries are built with `CGO_ENABLED=0` for static linking.

When adding a new image, ensure its Dockerfile works on both `amd64` and `arm64`. Use multi-stage builds from the existing Dockerfiles as a template.

---

## Conventional Commits

We use [Conventional Commits](https://www.conventionalcommits.org/) for all commit messages. This keeps the history readable and enables automated changelog generation.

### Format

```
<type>(<scope>): <description>

[optional body]

[optional footer(s)]
```

### Types

| Type | When to use |
|------|-------------|
| `feat` | A new feature (`feat: add schedule_task tool`) |
| `fix` | A bug fix (`fix: deduplicate fsnotify events in IPC bridge`) |
| `docs` | Documentation only (`docs: add Telegram setup instructions`) |
| `chore` | Maintenance, deps, CI (`chore: bump controller-gen to v0.17.2`) |
| `refactor` | Code change that neither fixes a bug nor adds a feature |
| `test` | Adding or updating tests (`test: add write-file integration test`) |
| `ci` | CI/CD changes (`ci: add multi-arch Docker build`) |

### Scopes (optional)

Use the component name: `agent-runner`, `controller`, `ipc-bridge`, `webhook`, `tui`, `slack`, `telegram`, `crd`, etc.

### Examples

```
feat(agent-runner): add fetch_url tool
fix(ipc-bridge): deduplicate fsnotify Create+Write events
docs: add CONTRIBUTING.md
test(integration): add k8s-ops-nodes test
ci: add arm64 to Docker build matrix
chore(deps): bump gorilla/websocket to v1.5.1
```

---

## Semantic Versioning

Sympozium follows [Semantic Versioning](https://semver.org/) (`vMAJOR.MINOR.PATCH`):

- **PATCH** (`v0.0.31` → `v0.0.32`) — Bug fixes, docs, minor improvements
- **MINOR** (`v0.1.0`) — New features, new CRD fields (backward-compatible)
- **MAJOR** (`v1.0.0`) — Breaking API/CRD changes

### Releasing

1. Ensure all changes are committed and pushed to `main`.
2. Create and push a tag:
   ```bash
   git tag v0.0.33
   git push origin v0.0.33
   ```
3. The release workflow automatically:
   - Builds CLI binaries for all platforms
   - Packages install manifests
   - Builds and pushes all Docker images tagged with the version
   - Creates a GitHub Release with assets

While in `v0.x.x`, the API is not yet stable and breaking changes may occur in minor versions.

---

## Pull Request Guidelines

1. **One concern per PR** — Don't mix a feature, a bug fix, and a refactor in one PR.
2. **Conventional commit title** — The PR title becomes the merge commit message.
3. **Tests required** — Add or update unit tests. For new tools or major features, add an integration test in `test/integration/`.
4. **CRD changes** — If you modify types in `api/v1alpha1/`, run `make generate` and commit the generated files.
5. **Docs** — Update relevant docs in `docs/` if behavior changes.
6. **Compile check** — Run `go build ./...` before pushing.

---

## Code Standards

- **Go** — Follow standard Go conventions. Run `make fmt` and `make vet`.
- **Error handling** — Return errors, don't panic. Use `fmt.Errorf("context: %w", err)` for wrapping.
- **Logging** — Use the structured logger (`log.Info`, `log.Error`) with key-value pairs, not `fmt.Printf`.
- **Naming** — CRD types use PascalCase (`SympoziumInstance`). Tool names use snake_case (`execute_command`). NATS topics use dot-separated (`agent.run.completed`).
- **IPC protocol** — New IPC-based tools must follow the JSON file drop pattern: write to `/ipc/<dir>/`, bridge watches with fsnotify, publishes to NATS.

---

## Project Structure Conventions

| Pattern | Convention |
|---------|-----------|
| CRD types | `api/v1alpha1/<name>_types.go` |
| Reconcilers | `internal/controller/<name>_controller.go` |
| Routers (NATS → K8s) | `internal/controller/<name>_router.go` |
| Agent tools | All in `cmd/agent-runner/tools.go` |
| Channel pods | `channels/<name>/main.go` |
| Dockerfiles | `images/<name>/Dockerfile` |
| Integration tests | `test/integration/test-<name>.sh` |
| Sample CRs | `config/samples/<name>_sample.yaml` |

---

## Need Help?

- Open an issue for questions or discussion
- Check existing docs in `docs/` before asking
- Look at recent PRs for examples of good contributions
