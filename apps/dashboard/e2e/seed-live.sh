#!/usr/bin/env bash
# Second seeding phase, against a running `tiffin serve --box`: the owner
# stores a few secrets (names are listed, values never come back).
#
#   TIFFIN_HOME=... e2e/seed-live.sh <tiffin binary> http://127.0.0.1:7391
set -euo pipefail
BIN="${1:?usage: seed-live.sh <tiffin binary> <box url>}"
URL="${2:?usage: seed-live.sh <tiffin binary> <box url>}"
: "${TIFFIN_HOME:?}"
OWNER="$(cat "$TIFFIN_HOME/owner-token")"

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

echo "seeded live: $(curl -fsS -H "Authorization: Bearer $OWNER" "$URL/v1/projects/hello/secrets" | jq length) secrets in hello"
