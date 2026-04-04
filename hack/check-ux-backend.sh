#!/usr/bin/env bash
# Preflight check for `make ux-tests`:
# ensure the Vite dev server's /api proxy can actually reach the apiserver.
#
# Without this, Cypress fires off against a dead backend and every test fails
# with a 500 — which usually means a stale vite is still listening after its
# port-forward died.

set -euo pipefail

VITE_PORT="${1:-5173}"
API_TOKEN="${2:-}"

probe="http://localhost:${VITE_PORT}/api/v1/namespaces"
status=$(curl -s -o /dev/null -w "%{http_code}" --max-time 5 \
  -H "Authorization: Bearer ${API_TOKEN}" \
  "$probe" 2>/dev/null || true)
status="${status:-000}"

if [ "$status" = "200" ]; then
  exit 0
fi

# Differentiate between "nothing listening" and "listening but broken proxy".
if [ "$status" = "000" ]; then
  reason="nothing is listening on localhost:${VITE_PORT} — the dev server is not running."
else
  reason="the Vite dev server on :${VITE_PORT} is up but its /api proxy is broken (likely a zombie vite whose port-forward to apiserver died)."
fi

cat >&2 <<EOF

ERROR: UX test backend preflight failed.

  Probed: GET ${probe}
  Got:    HTTP ${status} (expected 200)

${reason}

Fix:
  1. Kill any stale vite:  lsof -t -i :5173 -i :5174 | xargs kill 2>/dev/null || true
  2. Start a fresh server: make web-dev-serve
  3. Re-run:               make ux-tests

EOF
exit 1
