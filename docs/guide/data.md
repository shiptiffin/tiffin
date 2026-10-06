# Postgres, Valkey and backups

## Postgres

```ts
services: { postgres: { extensions: ["vector", "pg_cron"] } }
```

Each project gets its own Postgres 18 database and role. Apps get `DATABASE_URL`.

- **SQL:** `tiffin sql <project> "select ..."` runs one statement read-only (MCP `sql`;
  no confirmation). `tiffin sql write <project> "..."` (or `--write`; MCP `sql_write`)
  changes data and schema: it needs full access and takes a snapshot first.
- **Branches:** `tiffin branches create <project> --name pr-12` clones the database with
  copy-on-write in milliseconds, whatever its size. Previews don't use one: they share
  the production database.
- **Snapshots:** deleting the database (or writing through the console) keeps a
  snapshot for 7 days; `tiffin snapshots restore` brings it back.
- **Org isolation:** `auth.enable_org_rls('table')` adds row-level security keyed on the
  signed-in user's organization.
- **Safety limits:** a query is stopped after 5 minutes, or 30 seconds in a project with a limit (`statementTimeoutSeconds: 120`
  changes it; `SET LOCAL statement_timeout = '10min'` lets one long job run), a session
  left idle inside a transaction is closed after 60 seconds, and a project opens at most
  80 connections. A project with a limit gets its share of the connections and of the
  CPU for its queries (see [Sharing the box](concepts.md#sharing-the-box)).

## Valkey

```ts
services: { valkey: { maxMemoryMB: 128 } }
```

Each project gets a Valkey user limited to its own key prefix. Apps get `REDIS_URL` and
`VALKEY_PREFIX`. `Bun.redis` works as is; `tiffin-sdk/kv` adds prefixed helpers, an
atomic rate limiter and `cached()`. `maxMemoryMB` (64 by default) is held while the
project has a limit: over it, its keys with an expiry are cleared first, then new writes
are refused until it is under it (reads and deletes keep working).

### Apps written for Upstash or Vercel KV

Apps that use `@upstash/redis`, `@upstash/ratelimit` or `@vercel/kv` run unchanged: the
box serves an Upstash-compatible REST endpoint inside the box and gives apps
`UPSTASH_REDIS_REST_URL`, `UPSTASH_REDIS_REST_TOKEN`, `KV_REST_API_URL`, `KV_REST_API_TOKEN`
and `KV_REST_API_READ_ONLY_TOKEN`. `Redis.fromEnv()` picks them up. Your own env or
secrets with these names win, so delete the old Upstash values from them when you move an app.

- It speaks what those clients use: one command as a JSON array, path-style commands
  (`/set/key/value`), `/pipeline`, `/multi-exec`, base64 replies, and Lua scripts (`EVAL`,
  `EVALSHA`, `SCRIPT LOAD`). Subscriptions over REST are not available; use `REDIS_URL`.
- Keys are clean: the endpoint adds the project's prefix to every key (and to `PUBLISH`
  channels), so `user:1` over REST is `VALKEY_PREFIX + "user:1"` for `Bun.redis`. A Lua
  script gets prefixed `KEYS`; one that builds key names itself is refused.
- Every command runs as the project's own Valkey user, with the same limits as
  `REDIS_URL`: no `KEYS` or `SCAN` (so `@upstash/ratelimit`'s `resetUsedTokens` does not
  work), and the cache limit above.
- The endpoint is only reachable from apps on the box, not from the internet.

## Documents

`tiffin-sdk/db` gives typed JSONB collections on your Postgres database, for data you
don't want to model as tables yet.

## Backups

Daily full and hourly incremental backups of Postgres (pgBackRest), Valkey, files,
email, analytics and the box's own state, kept on the box.

```bash
tiffin backup                 # now
tiffin backups list
tiffin restore <id>           # shows what it will overwrite; repeat with --confirm
```

Restore takes a safety backup first. Off-site copies are not supported yet.

Backups are restore points of this box. To copy one project (on this box under a new
name, to a file, or to another box), see [copying and moving](moving.md): Duplicate,
Export, Import and Move.

### Restore drills

A backup you have never restored is a hope, not a backup. A restore drill proves one
works without touching anything live: it restores the backup's Postgres cluster into a
scratch directory on the data disk, starts a private temporary Postgres on it (unix
socket only, WAL archiving off), counts every table of every database, checks that each
database and table the box had when the backup was taken is there, then stops the
temporary server and deletes the scratch copy.

```bash
tiffin backups drill --wait          # drill the newest backup (waits up to 50 s)
tiffin backups drills start <bk_id>  # drill an older one
tiffin backups drills                # history, newest first
tiffin backups drills get <dr_id>    # one drill: phase while running, counts when done
tiffin backups drills cancel <dr_id>
tiffin backups schedule --drill-every-days 7 --drill-enabled=false
```

A drill runs weekly by default (the first a day after the box starts). The
`restore-drill` status check reads "restore drill passed 2 days ago (restored in 14 s)"
and fails when the last drill failed or none passed in 14 days. A drill is refused when
the data disk has less free space than the backup's size plus 20%. If the box restarts
mid-drill, the scratch copy is deleted when it comes back.

| Operation | What it returns |
|---|---|
| `POST /v1/backups/drill?wait=true` | starts a drill of the newest good backup → drill |
| `POST /v1/backups/{id}/drill?wait=true` | starts a drill of backup `bk_...` → drill |
| `GET /v1/backups/drills` | drills, newest first (last 30 kept) |
| `GET /v1/backups/drills/{id}` | one drill |
| `POST /v1/backups/drills/{id}/cancel` | stops a running drill → drill (failed, cancelled) |
| `GET /v1/backups` | also has `lastDrill` (newest drill or null) and the schedule's `drillEnabled`, `drillEveryDays` |
| `PUT /v1/backups/schedule` | accepts `drillEnabled`, `drillEveryDays` (1-90) |

Starting returns at once with `status: "running"` (409 when a drill is running or the
disk is too full). Poll `GET /v1/backups/drills/{id}` every second or two; `phase`
says what it is doing and `percent`/`restoredBytes` grow while it restores. A drill:

```json
{
  "id": "dr_01K...", "backup": "bk_01K...", "backupLabel": "20261003-101500F",
  "backupTakenAt": "2026-10-03T10:15:00Z", "backupAgeSeconds": 7260,
  "trigger": "manual", "status": "passed", "phase": "",
  "startedAt": "2026-10-03T12:16:00Z", "finishedAt": "2026-10-03T12:16:19Z",
  "seconds": { "restore": 14.2, "start": 2.5, "verify": 0.4, "total": 17.6 },
  "backupBytes": 52428800, "restoredBytes": 52101120, "percent": 100,
  "comparedWith": "backup",
  "databases": [
    { "name": "p_shop", "ok": true, "tables": 3, "rows": 6311, "liveTables": 3, "liveRows": 6311,
      "missing": [], "counts": [ { "table": "public.orders", "rows": 5000, "exact": true, "liveRows": 5000 } ] }
  ],
  "message": "Restored backup bk_01K... (taken 2 hours before the drill, 49.7 MB) in 14 s; ...",
  "hint": "", "scratch": "/var/lib/tiffin/drill/dr_01K..."
}
```

`status` is `running`, `passed` or `failed`. A failed drill's `message` says why in plain
words (for example `p_shop (1 missing table: public.orders)` or the pgBackRest error) and
`hint` says what to do. Per database, `missing` lists tables the backup should hold but
the restored copy lacks and `problems` lists tables that could not be read; `rows` are
exact counts unless `exact` is false (counting took over a minute). `liveRows` are exact
for small live tables and the planner's estimate for big ones, so they may differ from
the restored counts by whatever changed since the backup. `comparedWith: "live"` means
the backup predates table lists in backups and was checked against the live cluster.
