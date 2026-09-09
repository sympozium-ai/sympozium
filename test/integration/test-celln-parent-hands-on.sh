#!/usr/bin/env bash
# Operator-owned local development supervisor. Never source the credential file.
set -euo pipefail
sympozium_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
: "${CELLN_WORKTREE:?Set CELLN_WORKTREE to the absolute native Celln worktree}"
: "${CELLN_INTEROP_NATS_BINARY:?Set CELLN_INTEROP_NATS_BINARY to an absolute nats-server binary}"
: "${CELLN_PARENT_MODEL_KEY_FROM_ZSHRC:?Set the absolute zshrc path for the existing safe literal-key reader}"
export CELLN_INTEROP_HOLD_SECONDS=${CELLN_INTEROP_HOLD_SECONDS:-43200}
if [[ ! $CELLN_INTEROP_HOLD_SECONDS =~ ^[0-9]+$ ]] || (( CELLN_INTEROP_HOLD_SECONDS < 1 || CELLN_INTEROP_HOLD_SECONDS > 43200 )); then
  echo 'CELLN_INTEROP_HOLD_SECONDS must be 1..43200' >&2
  exit 1
fi
for required_path in "$CELLN_WORKTREE" "$CELLN_INTEROP_NATS_BINARY" "$CELLN_PARENT_MODEL_KEY_FROM_ZSHRC"; do
  [[ $required_path = /* && -e $required_path ]] || { echo 'Existing absolute paths required' >&2; exit 1; }
done
[[ -r /dev/kvm && -w /dev/kvm ]] || { echo 'Accessible KVM required' >&2; exit 1; }
kubectl --context kind-celln-deployed get --raw=/readyz >/dev/null
cd "$sympozium_dir/web"
npm run build
cd "$sympozium_dir"
go build -o target/celln-parent-api ./cmd/apiserver
go build -o target/celln-parent-controller ./cmd/controller
go build -o target/celln-parent-proxy ./cmd/celln-parent-proxy
go test -c -o target/celln-parent-manager.test ./internal/controller
export CELLN_INTEROP_API_BINARY="$sympozium_dir/target/celln-parent-api"
export CELLN_INTEROP_CONTROLLER_BINARY="$sympozium_dir/target/celln-parent-controller"
export CELLN_INTEROP_PROXY_BINARY="$sympozium_dir/target/celln-parent-proxy"
export CELLN_PARENT_INTEROP_BINARY="$sympozium_dir/target/celln-parent-manager.test"
export CELLN_INTEROP_DISPATCHER_BINARY="$CELLN_WORKTREE/target/validation/debug/celln"
export CELLN_INTEROP_PROVISION_BINARY="$CELLN_INTEROP_DISPATCHER_BINARY"
export CELLN_INTEROP_PARENT_RBAC="$sympozium_dir/config/samples/celln-parent-controller-rbac.yaml"
export CELLN_INTEROP_WEB_DIR="$sympozium_dir/web"
export CELLN_INTEROP_OWNER_TLS=true CELLN_INTEROP_SCOPED_CONTROLLER=true CELLN_INTEROP_REUSE_TEMPLATE=true
export CELLN_INTEROP_BROWSER_DELETE=true
export CELLN_INTEROP_STARTER=true
# This command hands control to the user after qualification. The separate
# acceptance invocation sets BROWSER_CANCEL to run and clean up immediately.
unset CELLN_INTEROP_BROWSER_CANCEL
export CELLN_INTEROP_KUBE_CONTEXT=kind-celln-deployed
export CELLN_INTEROP_NATS_BINARY CELLN_PARENT_MODEL_KEY_FROM_ZSHRC
export CARGO_TARGET_DIR=target/validation
cd "$CELLN_WORKTREE"
CARGO_TARGET_DIR=target cargo build -p celln-pilot --release --target x86_64-unknown-linux-musl \
  --bin celln-pilot --bin pilot-fetch --bin celln-harness-parent --bin celln-harness-turn \
  --bin celln-workspace-read --bin celln-workspace-write --bin celln-https-fetch
cargo build -p celln-cli
echo 'Wait for the hands-on.json handoff path. Create its hands-on.stop file for joined cleanup; do not kill the supervisor.'
exec cargo test -p celln-cli native_parent_starter_cross_turn_live -- --ignored --nocapture --test-threads=1
