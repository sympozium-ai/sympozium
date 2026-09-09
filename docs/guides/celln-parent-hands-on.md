# Local persistent-parent hands-on session

This development supervisor runs the real native Celln owner, TLS edge,
namespace-scoped parent controller, API/embedded web UI and NATS as separate
local processes. It first runs the two-parent/four-turn browser/model proof,
then deletes those exhausted runs and creates a fresh persistent parent with
12 total turns, including its initial turn. It retains live context and spawns
a disposable child per turn. It does not implement arbitrary Pi/Hermes adapters.
The proof includes the UI's UID-pinned destructive deletion of its first run,
with a mismatched-UID refusal check before deleting the intended run.

## Start

Prerequisites: the existing `kind-celln-deployed` cluster with current CRDs,
KVM/kernel/guest build prerequisites from Celln's parent proof, built guest
artifacts, frontend dependencies including Cypress, and a local NATS binary.
The script builds the host programs, static native guest tools and frontend;
it does not install a cluster, CRDs, the musl guest toolchain or dependencies.
It never sources or prints the zshrc.

From this worktree:

```sh
CELLN_WORKTREE=/home/axjns/Code/celln-worktrees/merged-epic-validation \
CELLN_INTEROP_NATS_BINARY=/home/axjns/Code/sympozium-m0-celln/target/nats-proof/bin/nats-server \
CELLN_PARENT_MODEL_KEY_FROM_ZSHRC=/home/axjns/.zshrc \
CELLN_INTEROP_HOLD_SECONDS=43200 \
bash test/integration/test-celln-parent-hands-on.sh
```

The maximum hold is 12 hours. A shorter value is useful for automatic cleanup
tests. After the initial proof, the console reports a private `hands-on.json`
file under the unique Celln proof directory. It contains the URL, namespace,
run name, API token-file path, matching YAML path, stop-file path and deadline.
No token value or model key is included in that handoff file.

## Use

Open the URL, enter the API token from the private file, select the namespace
from the handoff, then open its `hands-on-*` run. The first turn records the
value `violet` in `notes.txt` through `workspace-write`. Try “Read notes.txt using
workspace-read”, then “Fetch https://example.com/ using https-fetch”. The starter
tools are installed as revision-pinned catalogue entries with independent
operator/runtime/agent grants. The UI's starter preset and permission preview
show what is currently approved. Keep prompts short: the native parent currently
has a bounded approximately 2 KiB conversation context, not unlimited memory.

Workspace names are logical run-owned artifacts, not paths into the host project.
The starter grants allow 8 files, 4096 bytes/file and 16384 bytes total; writes
must supply the current workspace revision (initial write returned revision 1).
HTTPS is limited to `example.com`, four requests per turn and 4096 response
bytes; it receives no model credential. Cancellation does not roll back writes
already completed. Files and live context are lost when the parent is destroyed.

The matching `hands-on-run.yaml` shows the exact supported Harness + Celln +
enduring + borrowed-tool configuration. It includes the operator-approved
persona and limits. For a fresh run after deleting the current one, use
`kubectl --context kind-celln-deployed create -f /absolute/hands-on-run.yaml`.
Do not use `apply`: it has `generateName`. The UI can select these same fields,
but arbitrary personas/models/tool combinations require new operator templates
and grants; the existing template will refuse mismatches.

Only one live parent fits this fixture's admitted capacity. Delete the previous
run and wait for confirmed removal before creating another. Browser reload does
not reset turn budgets. Token renewal does not extend a parent's lease, restore
lost guest context, or renew execution authority.

## Stop and limits

Create the exact `hands-on.stop` file named in the handoff (`touch /absolute/path`).
The supervisor closes its API, deletes runs in its own isolated namespace,
waits for their finalizers to confirm teardown, then stops its owned processes.
An uncertain cleanup retains the namespace and reports failure. It does not
touch older environments. Do not kill the supervisor to stop a session normally.
The temporary host model credential is removed on normal supervisor completion.

Private host audit files intentionally retain conversation and tool output in
the proof directory. They have no automatic expiry; treat them as sensitive and
apply your retention policy. Namespace resources are deleted at session end;
this is not a durable production installation. The API currently uses the
operator Kubernetes identity behind a private loopback bearer endpoint; do not
expose it publicly or describe it as a multi-tenant API RBAC proof. Controller
credentials are independently scoped and renewed by the operator supervisor.
