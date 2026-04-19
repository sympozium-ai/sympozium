#!/usr/bin/env bash
# Preflight check for `make ux-tests` / `make ux-tests-serve`:
# ensure whatever is on the given port (Vite dev proxy OR `sympozium serve`
# port-forwarded apiserver) can actually reach /api/v1/namespaces.
#
# Without this, Cypress fires off against a dead backend and every test
# fails with a 500 — usually a zombie vite whose port-forward died, or a
# stale `sympozium serve` whose port-forward was killed.

set -euo pipefail

PORT="${1:-5173}"
API_TOKEN="${2:-}"

probe="http://localhost:${PORT}/api/v1/namespaces"
status=$(curl -s -o /dev/null -w "%{http_code}" --max-time 5 \
  -H "Authorization: Bearer ${API_TOKEN}" \
  "$probe" 2>/dev/null || true)
status="${status:-000}"

if [ "$status" = "200" ]; then
  exit 0
fi

# Differentiate between "nothing listening" and "listening but broken".
if [ "$status" = "000" ]; then
  reason="nothing is listening on localhost:${PORT} — no dev server or \`sympozium serve\` is running."
  read -r -d '' fix <<'HINT' || true
Fix — pick one of:
  A) Vite dev server flow:
     make web-dev-serve    # then, in another shell:
     make ux-tests
  B) sympozium serve flow (embedded UI):
     sympozium serve       # then, in another shell:
     make ux-tests-serve
HINT
else
  reason="something is listening on :${PORT} but /api/v1/namespaces returned HTTP ${status}. Likely a stale port-forward or bad token."
  read -r -d '' fix <<'HINT' || true
Fix:
  1. Kill the stale listener:  lsof -t -i :${PORT} | xargs kill 2>/dev/null || true
  2. Start a fresh server:     make web-dev-serve  (or)  sympozium serve
  3. Re-run:                   make ux-tests  (or)  make ux-tests-serve
HINT
fi

cat >&2 <<EOF

ERROR: UX test backend preflight failed.

  Probed: GET ${probe}
  Got:    HTTP ${status} (expected 200)

${reason}

${fix}

EOF
exit 1
