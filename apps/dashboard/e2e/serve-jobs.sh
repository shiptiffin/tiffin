#!/usr/bin/env bash
# Starts a throwaway box whose Jobs module really runs, for the Jobs specs
# (jobs.spec.ts): a Postgres for the queue (the build the Go tests download
# into ~/.embedded-postgres-go), `tiffin serve --box` (one at a time per machine: its
# SMTP and OTLP ports are fixed), and e2e/jobs-worker.ts
# as the outside world (schedules and queues call it) and as the app
# `worker` (workflows run in it). Projects:
#   hooks    no apps: a schedule and two queues that call web addresses
#   reports  one app, `worker`: the monthly-report workflow
#
#   E2E_PORT=7393 E2E_SERVE=e2e/serve-jobs.sh bunx playwright test jobs
#
# E2E_BIN=/path/to/tiffin skips the dashboard and Go builds.
set -euo pipefail
PORT="${1:-7393}"
WORKER=$((PORT + 2))
PGPORT=$((PORT + 3000))
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../../.." && pwd)"
DIR="${TMPDIR:-/tmp}"
DIR="${DIR%/}/tiffin-dash-e2e-$PORT"
rm -rf "$DIR"
mkdir -p "$DIR/pg"
cleanup() {
  [ -n "${worker:-}" ] && kill $worker 2>/dev/null || true
  [ -n "${pid:-}" ] && kill "$pid" 2>/dev/null || true
  "$DIR/pg/bin/pg_ctl" -D "$DIR/pg/data" -m immediate stop >/dev/null 2>&1 || true
  rm -rf "$DIR"
}
trap cleanup EXIT
trap 'exit 0' TERM INT

TXZ="$(ls "$HOME"/.embedded-postgres-go/embedded-postgres-binaries-*.txz 2>/dev/null | head -1 || true)"
if [ -z "$TXZ" ]; then
  echo "no Postgres build in ~/.embedded-postgres-go: run go test ./internal/mod/queue once" >&2
  exit 1
fi
tar -xJf "$TXZ" -C "$DIR/pg"
"$DIR/pg/bin/initdb" -D "$DIR/pg/data" -U tiffin --auth=trust >/dev/null
"$DIR/pg/bin/pg_ctl" -D "$DIR/pg/data" -o "-p $PGPORT -c listen_addresses=127.0.0.1 -c unix_socket_directories= -c fsync=off" -l "$DIR/pg/log" -w start >/dev/null

BIN="${E2E_BIN:-}"
if [ -z "$BIN" ]; then
  (cd "$HERE/.." && bun run build >/dev/null)
  BIN="$DIR/tiffin"
  (cd "$ROOT" && go build -o "$BIN" ./cmd/tiffin)
fi

export TIFFIN_HOME="$DIR/box"
export TIFFIN_QUEUE_DATABASE_URL="postgres://tiffin@127.0.0.1:$PGPORT/postgres"
# The worker plays the outside world on loopback, which jobs may not call on a real box.
export TIFFIN_QUEUE_ALLOW_NETS="127.0.0.1/32"
unset TIFFIN_URL TIFFIN_TOKEN TIFFIN_SESSION

apply() { # apply <dir> <intent>
  local hash
  hash="$("$BIN" plan "$1" --json | jq -r .hash)"
  "$BIN" apply "$1" --confirm "$hash" -m "$2" --json >/dev/null
}
mkdir -p "$DIR/seed/hooks" "$DIR/seed/reports"
cat >"$DIR/seed/hooks/tiffin.config.ts" <<TS
export default {
  project: "hooks",
  crons: {
    digest: { schedule: "0 9 * * 1-5", url: "http://127.0.0.1:$WORKER/hooks/digest", timezone: "Europe/London" },
    tick: { schedule: "* * * * *", url: "http://127.0.0.1:$WORKER/hooks/tick" },
  },
  queues: {
    orders: { url: "http://127.0.0.1:$WORKER/hooks/orders", concurrency: 4 },
    reports: { url: "http://127.0.0.1:$WORKER/hooks/report" },
    warehouse: { url: "http://127.0.0.1:$WORKER/hooks/broken", maxAttempts: 2 },
  },
  topics: { "order.created": { subscribers: ["orders"] } },
};
TS
cat >"$DIR/seed/reports/tiffin.config.ts" <<'TS'
export default {
  project: "reports",
  apps: { worker: { framework: "bun", path: ".", role: "worker" } },
};
TS
apply "$DIR/seed/hooks" "Schedules and queues that call web addresses"
apply "$DIR/seed/reports" "A worker app for the monthly report"

"$BIN" serve --box --public-url "${E2E_PUBLIC_URL:-http://localhost:$PORT}" --addr "127.0.0.1:$PORT" >"$DIR/serve.log" 2>&1 &
pid=$!
for _ in $(seq 1 100); do curl -fsS "http://127.0.0.1:$PORT/v1/health" >/dev/null 2>&1 && break; sleep 0.2; done
curl -fsS "http://127.0.0.1:$PORT/v1/health" >/dev/null || { tail -20 "$DIR/serve.log" >&2; exit 1; }
OWNER="$(cat "$TIFFIN_HOME/owner-token")"
api() { curl -fsS -X "$1" -H "Authorization: Bearer $OWNER" -H 'Content-Type: application/json' ${3:+-d "$3"} "http://127.0.0.1:$PORT$2"; }
for _ in $(seq 1 100); do api GET /v1/projects/hooks/queue/stats >/dev/null 2>&1 && break; sleep 0.3; done
api GET /v1/projects/hooks/queue/stats >/dev/null || { grep -i queue "$DIR/serve.log" | tail -5 >&2; exit 1; }

# The worker app "runs" on the second worker's port: what the runtime would publish for a deploy.
APP=$((WORKER + 1))
sqlite3 "$TIFFIN_HOME/state.db" "INSERT OR REPLACE INTO kv (ns, key, value) VALUES ('runtime/state', 'reports/worker', CAST('{\"live\":\"dep_e2e\",\"instances\":[{\"port\":$APP,\"deploy\":\"dep_e2e\"}]}' AS BLOB));"
keys() { # keys <project>: its app key and signing secret, as the box hands them to apps
  api GET "/v1/projects/$1/queue/signing-secret" >/dev/null
  sqlite3 "$TIFFIN_HOME/state.db" "SELECT CAST(value AS TEXT) FROM kv WHERE ns = 'queue' AND key = 'keys/$1'"
}
start_worker() { # start_worker <port> <project>
  local k
  k="$(keys "$2")"
  PORT="$1" TIFFIN_QUEUE_URL="http://127.0.0.1:7075" TIFFIN_QUEUE_KEY="$(jq -r .appKey <<<"$k")" TIFFIN_QUEUE_SIGNING_SECRET="$(jq -r .signing <<<"$k")" \
    bun "$HERE/jobs-worker.ts" >>"$DIR/worker.log" 2>&1 &
}
start_worker "$WORKER" hooks
worker=$!
start_worker "$APP" reports
worker="$worker $!"
for p in "$WORKER" "$APP"; do
  for _ in $(seq 1 50); do curl -fsS "http://127.0.0.1:$p/_calls" >/dev/null 2>&1 && break; sleep 0.2; done
done

# A few runs to look at: orders that worked, a warehouse job that gave up.
api POST /v1/projects/hooks/queue/send '{"name":"orders","payload":{"order":1043,"total":"24.50"}}' >/dev/null
api POST /v1/projects/hooks/queue/send '{"name":"order.created","payload":{"order":1044}}' >/dev/null
api POST /v1/projects/hooks/queue/send '{"name":"warehouse","payload":{"sku":"moss-guide"}}' >/dev/null
api POST /v1/projects/hooks/queue/crons/digest/trigger >/dev/null
touch "$DIR/ready"
wait "$pid"
