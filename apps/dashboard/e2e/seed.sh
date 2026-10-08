#!/usr/bin/env bash
# Seeds a throwaway box with a realistic change history for the dashboard:
# two projects, a human owner and two agents, every risk tier and one undo.
#
#   TIFFIN_HOME=$(mktemp -d)/box apps/dashboard/e2e/seed.sh /tmp/tiffin-dash
#
# Prints nothing on success except the agent token names it created.
set -euo pipefail

BIN="${1:?usage: seed.sh <tiffin binary>}"
: "${TIFFIN_HOME:?set TIFFIN_HOME to a throwaway directory}"
unset TIFFIN_URL TIFFIN_TOKEN TIFFIN_SESSION
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# apply <dir> <intent>: plan, then apply with the plan's hash.
apply() {
  local dir="$1" intent="$2" hash
  hash="$("$BIN" plan "$dir" --json | jq -r .hash)"
  "$BIN" apply "$dir" --confirm "$hash" -m "$intent" --json >/dev/null
}

last_change() { "$BIN" changes list --json | jq -r '.items[0].id'; }

mkdir -p "$WORK/hello" "$WORK/notes"
H="$WORK/hello/tiffin.config.ts"
cat > "$H" <<'TS'
import { defineConfig } from "@shiptiffin/sdk";
export default defineConfig({
  project: "hello",
  env: {
    LOG_LEVEL: "info",
  },
  apps: {
    web: { framework: "next", path: "apps/web", routes: ["hello"], memoryMB: 1024 },
    api: {
      framework: "hono",
      path: "apps/api",
      routes: ["hello/api"],
      healthcheck: "/healthz",
      instances: 2,
      env: { CORS_ORIGIN: "https://hello" },
    },
    worker: { framework: "bun", path: "apps/worker", role: "worker", memoryMB: 256 },
  },
  services: {
    postgres: { extensions: ["vector"] },
    valkey: { maxMemoryMB: 128 },
    analytics: { retentionDays: 400 },
    storage: {
      buckets: {
        uploads: { public: true },
      },
    },
  },
});
TS

# 1. The owner sets up the example project.
apply "$WORK/hello" "Set up the hello project from the example"

# 2. A second, smaller project with a private bucket.
cat > "$WORK/notes/tiffin.config.ts" <<'TS'
import { defineConfig } from "@shiptiffin/sdk";
export default defineConfig({
  project: "notes",
  apps: { site: { framework: "static", path: "site", routes: ["notes"] } },
  services: { postgres: {}, storage: { buckets: { exports: {} } } },
});
TS
apply "$WORK/notes" "Start a notes site with Postgres and an exports bucket"

# Agents: API keys with their own names (claude-code reaches every project, codex only notes).
CLAUDE="$("$BIN" tokens create --name claude-code --projects all --access full --json | jq -r .secret)"
CODEX="$("$BIN" tokens create --name codex --projects notes --access full --json | jq -r .secret)"
# Kept for seed-live.sh, which plays the agents against a running box.
printf 'CLAUDE=%s\nCODEX=%s\n' "$CLAUDE" "$CODEX" > "$TIFFIN_HOME/seed-agents.env"

agent_apply() {
  local tok="$1" sess="$2" dir="$3" intent="$4" hash
  hash="$(TIFFIN_TOKEN="$tok" TIFFIN_SESSION="$sess" "$BIN" plan "$dir" --json | jq -r .hash)"
  TIFFIN_TOKEN="$tok" TIFFIN_SESSION="$sess" "$BIN" apply "$dir" --confirm "$hash" -m "$intent" --json >/dev/null
}

# 3. claude-code scales the API and adds an env var (reversible).
sed -i '' 's/instances: 2/instances: 3/' "$H"
agent_apply "$CLAUDE" claude-code-1 "$WORK/hello" "Scale the API to three instances before the launch post"
sed -i '' 's/LOG_LEVEL: "info",/LOG_LEVEL: "debug",\n    FEATURE_SEARCH: "on",/' "$H"
agent_apply "$CLAUDE" claude-code-1 "$WORK/hello" "Turn on search and debug logging to chase the slow query"

# 4. The owner undoes the debug logging change.
UNDO_ID="$(last_change)"
uh="$({ "$BIN" undo "$UNDO_ID" --json || true; } | jq -r .plan.hash)"
"$BIN" undo "$UNDO_ID" --confirm "$uh" -m "Debug logs were too noisy; put it back" --json >/dev/null
sed -i '' -e 's/LOG_LEVEL: "debug",/LOG_LEVEL: "info",/' -e '/FEATURE_SEARCH/d' "$H"

# 5. codex adds a worker app to notes.
sed -i '' 's/site: { framework: "static", path: "site", routes: \["notes"\] }/site: { framework: "static", path: "site", routes: ["notes"] },\n    search: { framework: "bun", path: "search", role: "worker", memoryMB: 256 }/' "$WORK/notes/tiffin.config.ts"
agent_apply "$CODEX" codex-7f3a "$WORK/notes" "Add a search indexer worker for the notes site"

# 6. claude-code adds a bucket for previews (reversible; undoing it would delete the bucket).
sed -i '' 's/exports: {}/exports: {}, thumbnails: {}/' "$WORK/notes/tiffin.config.ts"
agent_apply "$CLAUDE" claude-code-2 "$WORK/notes" "Add a thumbnails bucket for PDF previews"

# 7. claude-code makes the exports bucket public (outbound).
sed -i '' 's/exports: {}/exports: { public: true }/' "$WORK/notes/tiffin.config.ts"
agent_apply "$CLAUDE" claude-code-2 "$WORK/notes" "Share the exports bucket so readers can download PDFs"

# 8. The owner drops the uploads bucket (irreversible).
sed -i '' '/buckets: {/,/},$/{/uploads: { public: true },/d;}' "$H"
perl -0pi -e 's/storage: \{\s*buckets: \{\s*\},\s*\},/storage: {},/s' "$H"
apply "$WORK/hello" "Drop the uploads bucket; files moved to the notes project"

# Optional: spread the history over ten days so the timeline groups by day
# (screenshots and demos). Rewrites only the timestamps, in place.
if [ "${SEED_SPREAD:-}" = 1 ]; then
  db="$TIFFIN_HOME/state.db"
  i=0
  for mins in 13080 8940 3070 3062 2940 1680 186 180 25; do
    i=$((i + 1))
    ts="strftime('%Y-%m-%dT%H:%M:%fZ','now','-$mins minutes')"
    sqlite3 "$db" "UPDATE changes SET at = $ts, body = json_set(body, '\$.at', $ts) WHERE seq = $i;"
  done
fi

echo "seeded: $("$BIN" changes list --json --limit 200 | jq '.items | length') changes"
