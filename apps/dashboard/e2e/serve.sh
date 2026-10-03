#!/usr/bin/env bash
# Starts a seeded throwaway box for the Playwright smoke test, serving the
# production dashboard build embedded in the binary (so the real CSP applies).
#
#   bash e2e/serve.sh 7392        # used by playwright.config.ts
#
# E2E_BIN=/path/to/tiffin skips the dashboard and Go builds.
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
"$BIN" serve --addr "127.0.0.1:$PORT" &
pid=$!
wait "$pid"
