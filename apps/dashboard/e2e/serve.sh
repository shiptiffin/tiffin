#!/usr/bin/env bash
# Starts a seeded throwaway box for the Playwright smoke test, serving the
# production dashboard build embedded in the binary (so the real CSP applies).
#
#   bash e2e/serve.sh 7392        # used by playwright.config.ts
#
# E2E_BIN=/path/to/tiffin skips the dashboard and Go builds.
# E2E_PUBLIC_URL=http://localhost:5391 when the browser uses the Vite dev server.
set -euo pipefail
PORT="${1:-7392}"
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../../.." && pwd)"
DIR="${TMPDIR:-/tmp}"
DIR="${DIR%/}/tiffin-dash-e2e-$PORT"
rm -rf "$DIR"
mkdir -p "$DIR"
cleanup() {
  [ -n "${pid:-}" ] && kill "$pid" 2>/dev/null || true
  rm -rf "$DIR"
}
trap cleanup EXIT
trap 'exit 0' TERM INT

BIN="${E2E_BIN:-}"
if [ -z "$BIN" ]; then
  (cd "$HERE/.." && bun run build >/dev/null)
  BIN="$DIR/tiffin"
  (cd "$ROOT" && go build -o "$BIN" ./cmd/tiffin)
fi

export TIFFIN_HOME="$DIR/box"
SEED_SPREAD=1 bash "$HERE/seed.sh" "$BIN" >&2
bun "$HERE/seed-analytics.ts" "$DIR" >&2
# --box turns on passkeys and secrets. The public URL must be the
# origin the browser uses: passkeys are bound to it.
"$BIN" serve --box --public-url "${E2E_PUBLIC_URL:-http://localhost:$PORT}" --addr "127.0.0.1:$PORT" &
pid=$!
for _ in $(seq 1 50); do curl -fsS "http://127.0.0.1:$PORT/v1/health" >/dev/null 2>&1 && break; sleep 0.2; done
bash "$HERE/seed-live.sh" "$BIN" "http://127.0.0.1:$PORT" >&2
touch "$DIR/ready"
wait "$pid"
