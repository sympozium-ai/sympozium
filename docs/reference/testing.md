# Testing

This document is the quick entry point for running Sympozium tests locally and in CI.

## Local test commands

```bash
# Unit tests with the race detector (the bar for any change)
make test

# go vet, including build-tagged code that `go vet ./...` skips
make vet

# Controller tests against a real API server, no cluster (envtest)
make test-system

# End-to-end tool/channel integration tests (Kind cluster + a model)
make test-integration

# API-first integration regression suite
make integration-tests

# Console UX tests (Cypress) against a running UI
make ux-tests                      # Vite dev server (make web-dev-serve)
make ux-tests-serve                # `sympozium serve` on port 9090
(cd web && npm run test:stubbed)   # specs that stub every API call; no cluster, needs `npx vite`
```

Integration tests work against any OpenAI-compatible provider (a local
`llama-server`, `ollama` or LM Studio as well as OpenAI); see
[Writing Integration Tests](../guides/writing-integration-tests.md) and
[Writing UX Tests](../guides/writing-ux-tests.md).

## What CI runs

Every pull request runs, in `.github/workflows/build.yaml`:

| Job | What it checks |
|-----|----------------|
| **Verify** | `gofmt`, `go vet ./...`, `make vet-tags`, build, `go test -race -short ./...`, generated code up to date, `make helm-sync-check` |
| **System tests (envtest)** | `make test-system` |
| **Console stubbed Cypress specs** | `npm run test:stubbed` (on `web/**` changes) |

Images are built and pushed on `main`.

## Celln journeys

Celln tests need a Linux host with `/dev/kvm`; on Kind, copy the host kernel
into each node first (see
[Celln Fleet Installation](../guides/celln-fleet-installation.md#prerequisites)).

| Script / target | What it proves |
|-----------------|----------------|
| `test/integration/test-celln-fleet.sh` | The fleet on multi-node Kind: backends (including one added from the API), one-shot and enduring runs, the toolbox, tenancy, node loss and continuation |
| `test/integration/test-celln-oneliner.sh` | A bare `sympozium install` brings up the fleet |
| `make test-celln-authorisation-contract` | Namespace-authorisation fixtures (no cluster, no KVM) |
| `make test-celln-model-budget` | Model-budget accounting against a real PostgreSQL (`DATABASE_URL`) |
| `make test-celln-model-gateway-live` | The model gateway against a live API server and PostgreSQL |
| `make test-celln-tenancy-local` | Pinned Go/Rust/database/API/gateway checks and KVM prerequisites |

## API integration suite notes

`make integration-tests` runs the API-focused smoke and behavior checks under `test/integration/`, including:

- API smoke coverage for namespaces/skills/policies/ensembles/instances/schedules
- Ensemble provisioning and provider switch propagation
- Ensemble vs ad-hoc correctness checks
- Schedule dispatch behavior
- AgentRun pod container shape checks
- Observability API checks
- Web-endpoint skill enable/disable/status API checks
- Serving-mode AgentRun shape (Deployment + Service creation)
- Optional capability checks (`CLAUDE_TOKEN`, `GITHUB_TOKEN`)

Optional secrets can be passed locally:

```bash
CLAUDE_TOKEN=... GITHUB_TOKEN=... make integration-tests
```

## Scheduled GitHub Actions workflow

The repository includes a scheduled Kind-based workflow:

- Workflow file: `.github/workflows/integration-kind.yaml`
- Name: `Integration Tests (Kind)`
- Triggers:
  - Daily schedule (`0 6 * * *`, UTC)
  - Manual run via `workflow_dispatch`

### What the workflow does

1. Creates a Kind cluster and builds and loads the Sympozium images
2. Installs CRDs and the `sympozium` Helm chart (webhook and cert-manager off)
3. Runs `test/integration/test-api-smoke.sh`
4. Starts the deterministic model fixture (`test/integration/deploy-fake-model.sh`)
5. Runs `test/integration/test-persistent-harness-session.sh` for the Pi and
   Hermes session runtimes: restart, stop/resume, SSE, client-disconnect
   cancellation, idle timeout, actionable failure status and owned-resource cleanup
6. On failure, dumps cluster diagnostics and uploads the sanitized
   persistence evidence

### Deterministic model fixture

The lane tests Sympozium, not a model. A 1B local LLM made it fail most days
by misremembering an exact token after restart even when the stored history
was intact, and by finishing a reply before the cancellation check could
disconnect it (issue #471). The lane therefore uses
`test/integration/fake-model`, a small OpenAI-compatible server
(`/v1/chat/completions` with and without streaming, and `/v1/models`
advertising a 128K context window because Hermes refuses less than 64K):

- A recall question is answered with the token from an earlier
  "Remember this exact token" turn **in the request history the harness sends**.
  If the history does not carry it, the fixture replies
  `FIXTURE-NO-TOKEN-IN-HISTORY`, so lost conversation state still fails the
  unchanged exact-token assertions. Unit tests prove a request without the
  token in its history never returns it.
- A final prompt containing `SLOW:<seconds>` streams one byte per second for
  that long, so the one-second client disconnect always lands while model work
  is in flight.

To run the same scripts against a real model, leave `TEST_MODEL_FIXTURE` unset
and set `TEST_PROVIDER`, `TEST_BASE_URL`, `TEST_MODEL` and `TEST_API_KEY`
(any OpenAI-compatible provider, including a local `llama-server` or Ollama).
Real-model qualification of the harnesses lives in those explicit runs on an
installed cluster and in the KVM [Celln journeys](#celln-journeys), not in the
scheduled Kind lane.

### Repository secrets

The workflow passes these optional secrets to tests:

- `CLAUDE_TOKEN`
- `GITHUB_TOKEN`

If unset, the related capability checks are skipped by design.

## Running the workflow manually

In GitHub:

1. Open **Actions**
2. Select **Integration Tests (Kind)**
3. Click **Run workflow**
