#!/usr/bin/env bash
# Second seeding stage (after seed-box.sh): deploys a demo worker to "shop" and
# drives queues, a topic, a cron, workflows (one waiting for a human) and a
# day of analytics through it.
#
#   TIFFIN_CONFIG_DIR=/tmp/tiffin-dev-dash e2e/seed-box-apps.sh <tiffin binary>
set -euo pipefail
BIN="${1:?usage: seed-box-apps.sh <tiffin binary>}"
: "${TIFFIN_CONFIG_DIR:?point TIFFIN_CONFIG_DIR at a dev box}"
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../../.." && pwd)"
CFG="$TIFFIN_CONFIG_DIR/boxes.json"
URL="$(jq -r '.boxes[.current].url' "$CFG")"
OWNER="$(jq -r '.boxes[.current].token' "$CFG")"
CA="$(jq -r '.boxes[.current].caFile' "$CFG")"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

api() {
  local m="$1" p="$2"
  shift 2
  curl -fsS --cacert "$CA" -X "$m" -H "Authorization: Bearer $OWNER" -H 'Content-Type: application/json' ${1:+-d "$1"} "$URL$p"
}
apply() {
  local hash
  hash="$("$BIN" plan "$1" --json | jq -r .hash)"
  "$BIN" apply "$1" --confirm "$hash" -m "$2" --json >/dev/null
}

mkdir -p "$WORK/shop"
cp -R "$ROOT/templates/queues-worker" "$WORK/shop/worker"
rm -rf "$WORK/shop/worker/node_modules" "$WORK/shop/worker/tiffin.config.ts"
cp "$HERE/fixtures/worker/index.ts" "$WORK/shop/worker/index.ts"
cat >"$WORK/shop/tiffin.config.ts" <<'TS'
import { defineConfig } from "tiffin-sdk";
export default defineConfig({
  project: "shop",
  env: { LOG_LEVEL: "info" },
  apps: { worker: { path: "worker", role: "worker" } },
  crons: { nightly: { schedule: "0 2 * * *", app: "worker", path: "/cron/nightly" } },
  services: {
    postgres: { extensions: ["pg_trgm"] },
    valkey: { maxMemoryMB: 64 },
    storage: { buckets: { uploads: {}, assets: { public: true } } },
    email: { from: "hello@shop.example" },
    analytics: {},
  },
});
TS
apply "$WORK/shop" "Add a background worker, a nightly report and analytics"
"$BIN" deploy "$WORK/shop" --app worker --json >/dev/null

for q in emails thumbnails webhooks backfill; do
  api PUT "/v1/projects/shop/queue/queues/$q" "$(jq -nc --arg q "$q" '{app:"worker", path:("/queues/" + $q)} + (if $q == "thumbnails" then {keyConcurrency:1, concurrency:2} elif $q == "webhooks" then {maxAttempts:4} else {} end)')" >/dev/null
done
api PUT /v1/projects/shop/queue/topics/order.placed/subscriptions/receipt '{"app":"worker","path":"/topics/order-email"}' >/dev/null
api PUT /v1/projects/shop/queue/topics/order.placed/subscriptions/stock '{"app":"worker","path":"/topics/order-stock"}' >/dev/null

send() { api POST /v1/projects/shop/queue/send "$1" >/dev/null; }
send '{"name":"backfill","payload":{}}'
for i in $(seq 1 40); do send "$(jq -nc --arg i "$i" '{name:"emails", payload:{to:("customer" + $i + "@example.com"), template:"receipt"}}')"; done
for i in $(seq 1 14); do send "$(jq -nc --arg i "$i" '{name:"thumbnails", key:"assets", payload:{key:("images/upload-" + $i + ".png")}}')"; done
for i in $(seq 1 10); do send "$(jq -nc --arg i "$i" '{name:"webhooks", payload:{url:"https://hooks.partner.example/orders", event:("order." + $i)}}')"; done
for i in 1 2; do send "$(jq -nc --arg i "$i" '{name:"webhooks", payload:{url:"https://old.partner.example/hook", event:("order.legacy-" + $i), broken:true}}')"; done
for i in $(seq 1040 1046); do send "$(jq -nc --argjson o "$i" '{name:"order.placed", payload:{order:$o, items:(1 + $o % 3)}}')"; done
# Something waiting in the future.
send '{"name":"emails","delaySeconds":7200,"payload":{"to":"ada@example.com","template":"abandoned-cart"}}'

start() { api POST /v1/projects/shop/workflows/runs "$1" >/dev/null; }
start '{"workflow":"fulfil-order","app":"worker","id":"order-1042","input":{"order":1042,"total":64000}}'
start '{"workflow":"fulfil-order","app":"worker","id":"order-1043","input":{"order":1043,"total":182500}}'
start '{"workflow":"onboard","app":"worker","id":"onboard-ada","input":{"email":"ada@example.com"}}'
start '{"workflow":"onboard","app":"worker","id":"onboard-grace","input":{"email":"grace@example.com"}}'
start '{"workflow":"onboard","app":"worker","id":"onboard-alan","input":{"email":"alan@example.com","failAt":"crm"}}'
sleep 8
api POST /v1/projects/shop/workflows/events '{"name":"verified-grace@example.com","payload":{"via":"link"}}' >/dev/null
# Decide one of the two approvals as a person (approvals are human-only).
CODE="$(api POST /v1/login-links | jq -r .code)"
HUMAN="$(curl -fsS --cacert "$CA" -D - -o /dev/null -H 'Content-Type: application/json' -d "{\"code\":\"$CODE\"}" "$URL/v1/session" | sed -n 's/^[Ss]et-[Cc]ookie: tiffin_session=\([^;]*\).*/\1/p')"
APR="$(api GET /v1/projects/shop/workflows/approvals | jq -r '[.[] | select(.title | test("1042"))][0].id // empty')"
if [ -n "$APR" ]; then
  curl -fsS --cacert "$CA" -X POST -H "Authorization: Bearer $HUMAN" -H 'Content-Type: application/json' \
    -d '{"decision":"approve","comment":"Address checked"}' "$URL/v1/projects/shop/workflows/approvals/$APR" >/dev/null
fi
curl -fsS --cacert "$CA" -X DELETE -H "Authorization: Bearer $HUMAN" "$URL/v1/session" >/dev/null || true
api POST /v1/projects/shop/queue/crons/nightly/trigger '{}' >/dev/null || true
# Protection: two manual bans, so decisions and alerts have something to show.
api POST /v1/protect/bans '{"ip":"203.0.113.77","duration":"24h","reason":"Hammering /login with password lists"}' >/dev/null || true
api POST /v1/protect/bans '{"ip":"198.51.100.0/24","duration":"4h","reason":"Scraper farm ignoring robots.txt"}' >/dev/null || true
echo "seeded apps: worker deployed, queues busy, workflows running"
