#!/usr/bin/env bash
# Seeds a real dev box (tiffin up, with services) with data for every module
# page of the dashboard: two projects with Postgres tables, Valkey keys,
# buckets with files, captured mail, a branch, a backup, errors and logs.
#
#   TIFFIN_CONFIG_DIR=/tmp/tiffin-dev-dash e2e/seed-box.sh <tiffin binary>
#
# Talks to the box in $TIFFIN_CONFIG_DIR/boxes.json as its owner. Throwaway
# boxes only: it applies configs and writes data.
set -euo pipefail
BIN="${1:?usage: seed-box.sh <tiffin binary>}"
: "${TIFFIN_CONFIG_DIR:?point TIFFIN_CONFIG_DIR at a dev box}"
HERE="$(cd "$(dirname "$0")" && pwd)"
CFG="$TIFFIN_CONFIG_DIR/boxes.json"
URL="$(jq -r '.boxes[.current].url' "$CFG")"
OWNER="$(jq -r '.boxes[.current].token' "$CFG")"
CA="$(jq -r '.boxes[.current].caFile' "$CFG")"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

api() { # api METHOD PATH [JSON]
  local m="$1" p="$2"
  shift 2
  curl -fsS --cacert "$CA" -X "$m" -H "Authorization: Bearer ${TOKEN:-$OWNER}" -H 'Content-Type: application/json' \
    ${TIFFIN_SESSION:+-H "X-Tiffin-Session: $TIFFIN_SESSION"} ${1:+-d "$1"} "$URL$p"
}
apply() { # apply <dir> <intent>
  local hash
  hash="$("$BIN" plan "$1" --json | jq -r .hash)"
  "$BIN" apply "$1" --confirm "$hash" -m "$2" --json >/dev/null
}
sql() { # sql <project> <statement> (a write; snapshots first)
  api POST "/v1/projects/$1/sql" "$(jq -nc --arg s "$2" '{sql:$s, write:true}')" >/dev/null
}
wait_ready() { # until every resource of a project is ready
  for _ in $(seq 1 90); do
    if api GET "/v1/projects/$1" | jq -e '[.status[]?.state] | all(. == "ready")' >/dev/null; then return 0; fi
    sleep 2
  done
  echo "project $1 is not ready" >&2
  return 1
}

# ---- projects -------------------------------------------------------------
mkdir -p "$WORK/shop" "$WORK/notes"
cat >"$WORK/shop/tiffin.config.ts" <<'TS'
import { defineConfig } from "tiffin-sdk";
export default defineConfig({
  project: "shop",
  env: { LOG_LEVEL: "info" },
  services: {
    postgres: { extensions: ["pg_trgm"] },
    valkey: { maxMemoryMB: 64 },
    storage: { buckets: { uploads: {}, assets: { public: true }, imports: {} } },
    email: { from: "hello@shop.example" },
  },
});
TS
apply "$WORK/shop" "Set up the shop: Postgres, Valkey, buckets and email"
cat >"$WORK/notes/tiffin.config.ts" <<'TS'
import { defineConfig } from "tiffin-sdk";
export default defineConfig({
  project: "notes",
  services: { postgres: {}, storage: { buckets: { exports: {} } }, email: {} },
});
TS
apply "$WORK/notes" "Start the notes project"
wait_ready shop
wait_ready notes

# ---- Postgres ---------------------------------------------------------------
sql shop "CREATE TABLE customers (id bigserial PRIMARY KEY, email text NOT NULL UNIQUE, name text NOT NULL, city text, created_at timestamptz NOT NULL DEFAULT now())"
sql shop "CREATE TABLE products (id bigserial PRIMARY KEY, sku text NOT NULL UNIQUE, name text NOT NULL, price_cents int NOT NULL, stock int NOT NULL DEFAULT 0, tags text[] NOT NULL DEFAULT '{}')"
sql shop "CREATE TABLE orders (id bigserial PRIMARY KEY, customer_id bigint NOT NULL REFERENCES customers(id), status text NOT NULL DEFAULT 'paid', total_cents int NOT NULL, items jsonb NOT NULL, placed_at timestamptz NOT NULL DEFAULT now())"
# A believable small shop: a few cities and regulars carry most orders, and each
# order's total comes from the product it bought (plus shipping under $75).
sql shop "INSERT INTO customers (email, name, city, created_at) SELECT lower(f) || '.' || lower(l) || i || '@example.com', f || ' ' || l, c, now() - ((i * 6 + (i * 7) % 5) || ' hours')::interval FROM (SELECT i, (ARRAY['Maya','Theo','Priya','Jonas','Amara','Luca','Hana','Sam','Ines','Kofi','Mei','Oscar','Zara','Ravi','Elena','Tomás','Noor','Felix','Aiko','Ben'])[1 + i % 20] AS f, (ARRAY['Okafor','Lindqvist','Sato','Moreau','Patel','Silva','Novak','Haddad','Kim','Fischer','Mensah','Rossi','Costa','Byrne','Tanaka','Ali','Weber','Nakamura','Hughes','Ortiz','Berg','Dubois','Eze'])[1 + (i * 7) % 23] AS l, (ARRAY['Austin','Leeds','Lisbon','Osaka','Pune','Oslo','Lagos','Quito'])[1 + floor(power(random(), 1.8) * 8)::int] AS c FROM generate_series(1, 240) i) s"
sql shop "INSERT INTO products (sku, name, price_cents, stock, tags) VALUES ('BWL-01','Speckled ceramic bowl',3200,48,'{kitchen,ceramic}'),('MUG-02','Stoneware mug',1800,120,'{kitchen}'),('TIF-03','Three-tier tiffin, brass',5400,15,'{lunch,brass}'),('TEA-04','Hojicha, 100 g',1400,80,'{tea}'),('NAP-05','Linen napkins, set of 4',2600,32,'{linen}'),('KNF-06','Paring knife',3900,22,'{kitchen,steel}'),('CUP-07','Tea cup, celadon',1600,64,'{tea,ceramic}'),('BRD-08','Olive wood board',4800,9,'{kitchen,wood}')"
sql shop "INSERT INTO orders (customer_id, status, total_cents, items, placed_at) SELECT o.c, o.st, p.price_cents * o.q + CASE WHEN p.price_cents * o.q < 7500 THEN 495 ELSE 0 END, jsonb_build_array(jsonb_build_object('sku', p.sku, 'qty', o.q)), o.t FROM (SELECT i, 1 + floor(power(random(), 1.7) * 240)::int AS c, (ARRAY['shipped','shipped','shipped','shipped','paid','paid','paid','refunded','shipped','shipped','shipped','shipped','shipped','shipped','shipped','paid','paid','shipped','shipped','refunded'])[1 + i % 20] AS st, 1 + floor(power(random(), 3) * 3)::int AS q, 1 + floor(power(random(), 1.4) * 8)::int AS pi, now() - power(i / 1180.0, 1.2) * interval '45 days' - random() * interval '20 minutes' AS t FROM generate_series(1, 1180) i) o JOIN products p ON p.id = o.pi"
sql shop "CREATE INDEX orders_customer ON orders (customer_id)"
sql shop "CREATE VIEW daily_revenue AS SELECT date_trunc('day', placed_at) AS day, count(*) AS orders, sum(total_cents) AS revenue_cents FROM orders WHERE status <> 'refunded' GROUP BY 1 ORDER BY 1 DESC"
sql shop "ANALYZE"
sql notes "CREATE TABLE notes (id bigserial PRIMARY KEY, title text NOT NULL, body text NOT NULL DEFAULT '', pinned boolean NOT NULL DEFAULT false, updated_at timestamptz NOT NULL DEFAULT now())"
sql notes "INSERT INTO notes (title, body, pinned) VALUES ('Groceries','Rice, lentils, ginger, limes', true), ('Ideas','A tiffin that keeps soup warm', false), ('Reading','Designing Data-Intensive Applications', false)"
api POST /v1/projects/shop/branches '{"name":"checkout-v2"}' >/dev/null

# ---- Valkey (keys under the project's prefix, set from inside the box) ------
KV="$(api GET /v1/projects/shop/kv/connection)"
REDIS="$(jq -r .redisUrl <<<"$KV")"
PREFIX="$(jq -r .prefix <<<"$KV")"
{
  # Sessions last two weeks from sign-in, so they run out at uneven times over the next days.
  for i in $(seq 1 24); do echo "SET ${PREFIX}session:s$i '{\"user\":$i,\"cart\":[\"BWL-01\"]}' EX $((86400 + (i * 7919) % 1123200))"; done
  for i in 1 2 3 4 5 6; do echo "HSET ${PREFIX}cart:$i sku BWL-01 qty $i updated $(date +%s)"; done
  echo "SET ${PREFIX}feature:new-checkout on"
  echo "SET ${PREFIX}rate:203.0.113.7 41 EX 3600"
  echo "SET ${PREFIX}cache:product:BWL-01 '{\"sku\":\"BWL-01\",\"price_cents\":3200,\"stock\":48}' EX 21600"
  echo "SET ${PREFIX}cache:product:TIF-03 '{\"sku\":\"TIF-03\",\"price_cents\":5400,\"stock\":15}' EX 14400"
  echo "SET ${PREFIX}reset:grace@example.com 6f1c2a EX 1800"
  echo "LPUSH ${PREFIX}recent:orders 1042 1041 1040 1039 1038"
  echo "ZADD ${PREFIX}leaderboard:bowls 48 BWL-01 31 CUP-07 12 TIF-03"
  echo "SADD ${PREFIX}tags:kitchen BWL-01 MUG-02 KNF-06 BRD-08"
} | limactl shell "${TIFFIN_LIMA_INSTANCE:-tiffin}" -- valkey-cli -u "$REDIS" >/dev/null

# ---- Storage ----------------------------------------------------------------
put() { # put <project> <bucket> <key> <file>
  curl -fsS --cacert "$CA" -H "Authorization: Bearer $OWNER" -F "key=$3" -F "file=@$4" "$URL/v1/projects/$1/storage/buckets/$2/objects" >/dev/null
}
F="$HERE/fixtures"
put shop assets "images/sunset.png" "$F/sunset.png"
put shop assets "images/bowl.png" "$F/bowl.png"
put shop assets "images/pattern.png" "$F/pattern.png"
put shop assets "robots.txt" <(printf 'User-agent: *\nAllow: /\n')
put shop uploads "receipts/2026-10/receipt-1042.pdf" "$F/receipt-1042.pdf"
put shop uploads "avatars/ada.png" "$F/bowl.png"
put shop uploads "avatars/grace.png" "$F/pattern.png"
put shop uploads "exports/customers.csv" <(printf 'id,email,name,city\n1,customer1@example.com,Grace Hopper 1,Osaka\n2,customer2@example.com,Alan Turing 2,Lagos\n3,customer3@example.com,Katherine Johnson 3,Austin\n')
put shop uploads "notes/README.md" <(printf '# Uploads\n\nCustomer uploads land here. Receipts are kept for 7 years.\n')
put shop uploads "config/shipping.json" <(printf '{\n  "zones": ["EU", "US", "JP"],\n  "freeOver": 7500,\n  "carrier": "postal"\n}\n')
put notes exports "2026/notes-export.md" <(printf '# Notes export\n\n- Groceries\n- Ideas\n- Reading\n')
# A bucket that gets deleted, so the trash has something in it.
put shop imports "legacy/products-2019.csv" <(printf 'sku,name\nOLD-1,Old bowl\n')
sed -i '' 's/, imports: {}//' "$WORK/shop/tiffin.config.ts"
apply "$WORK/shop" "Drop the imports bucket; the 2019 catalogue is in Postgres now"

# ---- Email (captured in the dev inbox) --------------------------------------
send() { api POST /v1/projects/shop/email/send "$1" >/dev/null; }
send "$(jq -nc '{to:["ada@example.com"], subject:"Your sign-in link", text:"Hi Ada,\n\nClick to sign in: https://shop.tiffin.localhost/auth/verify?token=7f3a91c2\n\nThis link works for 15 minutes.", html:"<div style=\"font-family:Georgia,serif;max-width:520px;margin:0 auto;padding:32px;color:#2b2520\"><h1 style=\"font-weight:500\">Sign in to Shop</h1><p>Hi Ada, click the button to sign in. This link works for 15 minutes.</p><p><a href=\"https://shop.tiffin.localhost/auth/verify?token=7f3a91c2\" style=\"display:inline-block;background:#2b2520;color:#fbf7ef;padding:12px 20px;border-radius:6px;text-decoration:none\">Sign in</a></p><p style=\"color:#8a8076;font-size:13px\">If you didn&#39;t ask for this, ignore it.</p></div>"}')"
send "$(jq -nc --arg pdf "$(base64 <"$F/receipt-1042.pdf")" '{to:["grace@example.com"], subject:"Receipt for order #1042", text:"Thanks for your order!\n\nSpeckled ceramic bowl x2  $64.00\n\nView it: https://shop.tiffin.localhost/orders/1042", html:"<div style=\"font-family:-apple-system,Helvetica,sans-serif;max-width:560px;margin:0 auto;padding:28px;color:#222\"><h2 style=\"margin:0 0 8px\">Thanks, Grace!</h2><p style=\"color:#666\">Order #1042 is paid and on its way soon.</p><table style=\"width:100%;border-collapse:collapse;margin:20px 0\"><tr><td style=\"padding:8px 0;border-bottom:1px solid #eee\">Speckled ceramic bowl &times; 2</td><td style=\"text-align:right;border-bottom:1px solid #eee\">$64.00</td></tr><tr><td style=\"padding:8px 0\"><b>Total</b></td><td style=\"text-align:right\"><b>$64.00</b></td></tr></table><a href=\"https://shop.tiffin.localhost/orders/1042\">View your order</a><img src=\"https://tracker.example/pixel.gif\" width=1 height=1></div>", attachments:[{filename:"receipt-1042.pdf", contentType:"application/pdf", base64:$pdf}]}')"
send "$(jq -nc '{to:["team@shop.example"], subject:"Low stock: Olive wood board", text:"Only 9 left of BRD-08 (Olive wood board). Reorder?"}')"
send "$(jq -nc '{to:["alan@example.com"], subject:"Welcome to Shop", text:"Welcome aboard, Alan. Your account is ready.", html:"<div style=\"font-family:Georgia,serif;max-width:520px;margin:0 auto;padding:32px\"><h1 style=\"font-weight:500\">Welcome, Alan</h1><p>Your account is ready. Here are three things to try first:</p><ol><li>Save a favourite</li><li>Set your city for shipping</li><li>Say hi: just reply to this email</li></ol></div>"}')"
api POST /v1/projects/shop/email/suppressions '{"address":"bounced@example.com","reason":"bounce","detail":"550 5.1.1 mailbox does not exist"}' >/dev/null
api POST /v1/projects/shop/email/suppressions '{"address":"no-more-mail@example.com","reason":"unsubscribe"}' >/dev/null
api POST /v1/projects/notes/email/send "$(jq -nc '{to:["me@example.com"], subject:"Daily notes digest", text:"3 notes, 1 pinned."}')" >/dev/null

# ---- Observe: an error from an app, and edge traffic for the logs -----------
ING="$(api GET '/v1/observe/ingest?project=shop&app=web')"
DSN="$(jq -r .publicSentryDsn <<<"$ING")"
KEY="$(sed -E 's#^https?://([^@]+)@.*#\1#' <<<"$DSN")"
BASE="$(sed -E 's#^(https?://)[^@]+@([^/]+)/(.*)$#\1\2#' <<<"$DSN")"
PID="$(sed -E 's#.*/([0-9]+)$#\1#' <<<"$DSN")"
event() {
  curl -fsS --cacert "$CA" -H 'Content-Type: application/json' -H "X-Sentry-Auth: Sentry sentry_version=7, sentry_key=$KEY" \
    -d "$1" "$BASE/api/$PID/store/" >/dev/null
}
for i in 1 2 3 4 5 6 7; do
  event "$(jq -nc --arg id "$(uuidgen | tr -d - | tr 'A-Z' 'a-z')" '{event_id:$id, level:"error", platform:"javascript", release:"web@1.4.2", environment:"production", transaction:"POST /api/checkout", server_name:"box", exception:{values:[{type:"TypeError", value:"Cannot read properties of undefined (reading '"'"'price_cents'"'"')", stacktrace:{frames:[{filename:"node_modules/hono/dist/hono-base.js", function:"dispatch", lineno:204, colno:12, in_app:false},{filename:"src/routes/checkout.ts", function:"handleCheckout", lineno:57, colno:19, in_app:true, context_line:"  const total = items.reduce((t, i) => t + i.product.price_cents * i.qty, 0);"},{filename:"src/lib/cart.ts", function:"lineTotal", lineno:23, colno:31, in_app:true, context_line:"  return item.product.price_cents * item.qty;"}]}}]}, tags:{route:"/api/checkout"}}')"
done
for i in 1 2; do
  event "$(jq -nc --arg id "$(uuidgen | tr -d - | tr 'A-Z' 'a-z')" '{event_id:$id, level:"warning", platform:"javascript", release:"web@1.4.2", message:"Slow query: SELECT * FROM orders WHERE customer_id = $1 took 2.3 s", transaction:"GET /account/orders"}')"
done
FILES="$(api GET /v1/projects/shop/storage | jq -r '.buckets[] | select(.public) | .publicUrl')"
for k in images/sunset.png images/bowl.png robots.txt images/missing.png images/sunset.png; do curl -s --cacert "$CA" -o /dev/null "$FILES/$k" || true; done
api POST /v1/observe/alerts/test '{}' >/dev/null || true

# ---- Backups ----------------------------------------------------------------
api POST /v1/backups '{"kind":"full"}' >/dev/null || true

echo "seeded box: shop and notes with data, mail, files, an error issue and a backup"
