#!/usr/bin/env bash
# Third seeding stage (after seed-box.sh and seed-box-apps.sh): a web app with
# a deploy history (a failed one included), a preview, a static docs site,
# traffic for logs and analytics, and the app's own users and organizations.
#
#   TIFFIN_CONFIG_DIR=/tmp/tiffin-dev-dash e2e/seed-box-runtime.sh <tiffin binary>
set -euo pipefail
BIN="${1:?usage: seed-box-runtime.sh <tiffin binary>}"
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
deploy() { "$BIN" deploy "$WORK/shop" --json "$@" >/dev/null || true; }

S="$WORK/shop"
mkdir -p "$S"
cp -R "$ROOT/templates/queues-worker" "$S/worker"
rm -rf "$S/worker/node_modules" "$S/worker/tiffin.config.ts"
cp "$HERE/fixtures/worker/index.ts" "$S/worker/index.ts"
cp -R "$ROOT/templates/hello-hono" "$S/web"
rm -rf "$S/web/node_modules" "$S/web/tiffin.config.ts"
cp "$HERE/fixtures/web/index.ts" "$S/web/index.ts"
cp -R "$ROOT/templates/static-site" "$S/docs"
rm -f "$S/docs/tiffin.config.ts"
cat >"$S/tiffin.config.ts" <<'TS'
import { defineConfig } from "tiffin-sdk";
export default defineConfig({
  project: "shop",
  env: { LOG_LEVEL: "info" },
  apps: {
    web: { framework: "hono", path: "web", healthcheck: "/healthz", instances: 2 },
    docs: { framework: "static", path: "docs" },
    worker: { path: "worker", role: "worker" },
  },
  crons: { nightly: { schedule: "*/10 * * * *", app: "worker", path: "/cron/nightly" } },
  services: {
    postgres: { extensions: ["pg_trgm"] },
    valkey: { maxMemoryMB: 64 },
    storage: { buckets: { uploads: {}, assets: { public: true } } },
    email: { from: "hello@shop.example" },
    analytics: {},
    auth: { methods: ["email", "magic-link", "passkey"], organizations: true },
  },
});
TS
apply "$S" "Add the storefront, its docs and sign-in"

deploy --app web
deploy --app docs
# A second release, then one that can't start (the running one stays live).
sed -i '' 's/Small things for slow lunches/Small things for slow lunches, now with tea/' "$S/web/index.ts"
deploy --app web
cp "$S/web/index.ts" "$WORK/good.ts"
sed -i '' 's/^const port = .*/throw new Error("DATABASE_URL is not set: did you forget services.postgres?");\nconst port = 3000;/' "$S/web/index.ts"
deploy --app web
cp "$WORK/good.ts" "$S/web/index.ts"
sed -i '' 's/Small things for slow lunches, now with tea/Try the new checkout/' "$S/web/index.ts"
deploy --app web --preview new-checkout

# Traffic: page views at the edge, API calls and a few errors for the logs.
WEB="$(api GET /v1/projects/shop/apps/web/runtime | jq -r '.production.url // empty')"
if [ -n "$WEB" ]; then
  for i in $(seq 1 40); do
    for p in / /shop /shop/bowls /shop/tiffins /about; do
      curl -s --cacert "$CA" -o /dev/null -H 'Sec-Fetch-Dest: document' -H 'User-Agent: Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15' "$WEB$p" || true
    done
    curl -s --cacert "$CA" -o /dev/null "$WEB/api/products" || true
    [ $((i % 7)) = 0 ] && curl -s --cacert "$CA" -o /dev/null -X POST -H 'Content-Type: application/json' -d '{}' "$WEB/api/checkout" || true
  done
fi

# The app's own users and teams. Sign-up is protected by a proof-of-work
# challenge, so demo users go straight into the project's auth tables.
api POST /v1/projects/shop/sql "$(jq -nc --rawfile s "$HERE/fixtures/auth-users.sql" '{sql:$s, write:true}')" >/dev/null
echo "seeded runtime: web, docs and worker deployed; users and organizations"
