#!/usr/bin/env bash
# Second seeding phase, against a running `tiffin serve --box`: agents ask
# for approvals (pending and rejected) and the owner stores a few secrets.
#
#   TIFFIN_HOME=... e2e/seed-live.sh <tiffin binary> http://127.0.0.1:7391
set -euo pipefail
BIN="${1:?usage: seed-live.sh <tiffin binary> <box url>}"
URL="${2:?usage: seed-live.sh <tiffin binary> <box url>}"
: "${TIFFIN_HOME:?}"
# shellcheck disable=SC1091
source "$TIFFIN_HOME/seed-agents.env"
OWNER="$(cat "$TIFFIN_HOME/owner-token")"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# ask <token> <session> <config> <intent>: plan, then apply; it needs approval.
ask() {
  local tok="$1" sess="$2" cfg="$3" intent="$4" dir hash
  dir="$WORK/$RANDOM"
  mkdir -p "$dir"
  printf '%s\n' "$cfg" > "$dir/tiffin.config.ts"
  hash="$(TIFFIN_URL="$URL" TIFFIN_TOKEN="$tok" TIFFIN_SESSION="$sess" "$BIN" plan "$dir" --json | jq -r .hash)"
  { TIFFIN_URL="$URL" TIFFIN_TOKEN="$tok" TIFFIN_SESSION="$sess" "$BIN" apply "$dir" --confirm "$hash" -m "$intent" --json || true; } | jq -r '.approval.id'
}

notes() {
  cat <<TS
import { defineConfig } from "tiffin-sdk";
export default defineConfig({
  project: "notes",
  apps: {
    site: { framework: "static", path: "site", routes: ["notes"] },
    search: { framework: "bun", path: "search", role: "worker", memoryMB: 256 }
  },
  services: { $1 storage: { buckets: { exports: { public: true }, thumbnails: { public: $2 } } } },
});
TS
}

# A dashboard session for the owner (rejecting is for humans).
code="$(curl -fsS -X POST -H "Authorization: Bearer $OWNER" "$URL/v1/login-links" | jq -r .code)"
HUMAN="$(curl -fsS -D - -o /dev/null -H 'Content-Type: application/json' -d "{\"code\":\"$code\"}" "$URL/v1/session" | sed -n 's/^[Ss]et-[Cc]ookie: tiffin_session=\([^;]*\).*/\1/p')"

# 1. Rejected: claude-code wanted to drop the notes database.
old="$(ask "$CLAUDE" claude-code-3 "$(notes "" false)" "Drop the notes Postgres; search reads from files now")"
curl -fsS -X POST -H "Authorization: Bearer $HUMAN" -H 'Content-Type: application/json' \
  -d '{"reason":"Not yet: the site still reads drafts from Postgres."}' "$URL/v1/approvals/$old/reject" >/dev/null

# 2. Pending, outbound: codex wants the thumbnails public.
ask "$CODEX" codex-7f3a "$(notes "postgres: {}," true)" "Make thumbnails public so link previews load" >/dev/null

# 3. Pending, irreversible: claude-code asks again, with a better reason.
ask "$CLAUDE" claude-code-4 "$(notes "" false)" "Drop the notes Postgres after moving drafts to the exports bucket" >/dev/null

# Secrets: names are listed, values never come back.
for kv in "hello STRIPE_SECRET_KEY sk_test_seed" "hello RESEND_API_KEY re_seed" "notes SESSION_SECRET seed-session-secret"; do
  set -- $kv
  curl -fsS -X PUT -H "Authorization: Bearer $OWNER" -H 'Content-Type: application/json' -d "{\"value\":\"$3\"}" "$URL/v1/projects/$1/secrets/$2" >/dev/null
done

# People: an admin, a member and a viewer (invites only; nobody signs in).
for who in 'Maya Okafor|maya@example.com|admin' 'Sam Reyes|sam@example.com|member' 'Jo|"|viewer'; do
  IFS='|' read -r name email role <<<"$who"
  [ "$email" = '"' ] && email=""
  curl -fsS -X POST -H "Authorization: Bearer $OWNER" -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg n "$name" --arg e "$email" --arg r "$role" '{name:$n, role:$r} + (if $e == "" then {} else {email:$e} end)')" "$URL/v1/people" >/dev/null
done

# End the helper session so it doesn't show up as a signed-in browser.
curl -fsS -X DELETE -H "Authorization: Bearer $HUMAN" "$URL/v1/session" >/dev/null || true
echo "seeded live: $(curl -fsS -H "Authorization: Bearer $OWNER" "$URL/v1/approvals" | jq length) approvals"
