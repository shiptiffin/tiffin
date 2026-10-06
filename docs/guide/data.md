# Postgres, KV and backups

## Postgres

```ts
services: { postgres: { extensions: ["vector", "pg_cron"] } }
```

Each project gets its own Postgres 18 database and role. Apps get `DATABASE_URL`
(and `PGHOST`/`PGPORT`/`PGUSER`/`PGPASSWORD`/`PGDATABASE`) through the box's connection
pooler, `DIRECT_DATABASE_URL` straight to Postgres, and `DATABASE_POOL_MAX`.

- **SQL:** `tiffin sql <project> "select ..."` runs one statement read-only (MCP `sql`;
  no confirmation). `tiffin sql write <project> "..."` (or `--write`; MCP `sql_write`)
  changes data and schema: it needs full access and takes a snapshot first.
- **Branches:** `tiffin branches create <project> --name pr-12` clones the database with
  copy-on-write in milliseconds, whatever its size. Every app preview gets one of its
  own (`pv-<preview>`, listed with `preview` set), made on its first deploy and deleted
  with it; `services: { postgres: { previews: "shared" } }` puts previews on the
  production database instead. See [Migrations and preview
  databases](apps.md#migrations-and-preview-databases).
- **Migrations:** an app's `release` command (`bunx drizzle-kit migrate`) runs once per
  deploy before the new version takes traffic, with `DATABASE_URL` set straight to
  Postgres (migration tools hold session locks); a failure keeps the old version serving.
- **Snapshots:** deleting the database (or writing through the console) keeps a
  snapshot for 7 days; `tiffin snapshots restore` brings it back.
- **Org isolation:** `auth.enable_org_rls('table')` adds row-level security keyed on the
  signed-in user's organization.
- **Safety limits:** a query is stopped after 5 minutes, or 30 seconds in a project with a limit (`statementTimeoutSeconds: 120`
  changes it; `SET LOCAL statement_timeout = '10min'` lets one long job run), a session
  left idle inside a transaction is closed after 60 seconds, and a project opens at most
  80 connections. A project with a limit gets its share of the connections and of the
  CPU for its queries (see [Sharing the box](concepts.md#sharing-the-box)).
- **Connection pools:** see the pooler below. `DATABASE_POOL_MAX` (20 per production
  instance, 5 per preview instance) is how many client connections one instance's pool
  should open to the pooler. Clients do not read it on their own: pass it as the pool's
  max, e.g. `new SQL({ max: Number(process.env.DATABASE_POOL_MAX) || 10 })` (Bun.SQL),
  `postgres(url, { max: ... })` (postgres.js), `new Pool({ max: ... })` (node-postgres, and
  `PrismaPg` with Prisma 7). A value you set (env or secret) is kept. A new value applies
  as instances start (a deploy or restart), and a plan warns when the apps' pools could
  open more client connections than the pooler lets a project hold (1,000).

### The connection pooler

PgBouncer runs in front of Postgres in transaction mode (127.0.0.1:6432, and its socket
in `/var/run/postgresql`). Apps hold as many client connections as they like (cheap: no
Postgres process each); a server connection is theirs only for the length of a
transaction. A project's server connections through the pooler stop at three quarters of
its connection limit (60 of 80), so a quarter stays free for direct connections; a
preview branch gets a fifth of that (12). Backends still run as the project's own role,
so its limits and its share of the CPU hold as before.

| Client | Through the pooler (`DATABASE_URL`) |
|---|---|
| Bun.SQL, postgres.js, node-postgres, Drizzle (either driver) | works as is, prepared statements included |
| Prisma 7 (`@prisma/adapter-pg`) | works as is |
| Prisma 6 (Rust engine) | works as is; `?pgbouncer=true` is not needed (it also works) |

Prepared statements work because the pooler re-prepares them on whichever server
connection runs them. Settings sent when connecting carry over for `search_path`,
`timezone`, `application_name`, `statement_timeout`, `lock_timeout` and
`idle_in_transaction_session_timeout`; the pooler refuses a connection that sends others
(set them with `SET LOCAL` inside a transaction instead). What does not survive transaction
pooling is state kept in the session between transactions: `LISTEN`, session advisory
locks, `SET` without `LOCAL`, temporary tables and `WITH HOLD` cursors. Use `DIRECT_DATABASE_URL` for those (Prisma's
`directUrl`, drizzle-kit, a LISTEN connection); release commands get it as `DATABASE_URL`
already. With node-postgres, give the pool an error listener
(`pool.on("error", ...)`): without one, a connection the server closes while idle ends
the process.

### Minor updates

Postgres, pgvector, pg_cron and PgBouncer come from the PostgreSQL project's apt
repository, which the box's automatic security updates do not cover, so the box updates
them itself:

```bash
tiffin maintenance show                        # versions, what waits, recent updates
tiffin maintenance postgres-update             # check now; says when it installs
tiffin maintenance postgres-update --now       # install now
```

An update downloads and installs the new packages while the old server runs, then pauses
the pooler (transactions in flight finish; new queries wait), restarts Postgres and
resumes. On a 2-CPU, 3 GB box the pause was 0.1–0.4 s and the slowest query under
constant load took under half a second; none failed.
Direct connections are closed by the restart and reconnect. If the new version does not
start, the old packages go back. With a maintenance window (`tiffin up --reboot-window
04:00`) updates install a quarter of an hour into it, once a day; without one the box
checks daily and `tiffin status` (`postgres-updates`) says what waits. Each update is in
the audit log (`tiffin audit list`, `postgres.update`), and one the box ran on its own
that failed sends an alert. `tiffin up` updates the packages the same way when this
Tiffin needs a newer version than the box runs.

Unattended upgrades never restart the box's services on their own (needrestart is told
to leave them); a maintenance run restarts those running on replaced libraries, in
order, Postgres with the pause. PgBouncer itself restarts only when asked
(`--restart-pooler`), because that closes every app's client connections; otherwise a new
version of it runs from the next reboot.

### From your computer

Postgres and KV listen only inside the box. `tiffin db tunnel <project>` forwards
`localhost:15432` to the project's database over SSH (the box's own SSH access, or the
Lima VM's for a local box) and prints a `postgresql://` URL for psql, TablePlus or a local
app; `--branch pr-12` reaches a branch instead, `--port` picks another local port.
`tiffin kv tunnel <project>` does the same for KV on `localhost:16379`. The URL carries the
project's password, so it needs a key with full access to the project, and every reveal
is recorded. It stays open until you press Ctrl-C.

## KV (Valkey)

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
  work), and the KV limit above.
- The endpoint is only reachable from apps on the box, not from the internet.

## Flexible JSON

For data you don't want to model as columns yet, add a `jsonb` column to a table. It
stores any JSON, can be indexed and queried by field, and joins and transactions keep
working. With Drizzle:

```ts
export const events = pgTable("events", {
  id: serial("id").primaryKey(),
  kind: text("kind").notNull(),
  data: jsonb("data").$type<Record<string, unknown>>().notNull(),
});
// where data->>'plan' = 'pro'
db.select().from(events).where(sql`${events.data}->>'plan' = 'pro'`);
```

Add `CREATE INDEX ON events USING gin (data jsonb_path_ops)` when you filter by fields
often.

## Backups

Daily full and hourly incremental backups of Postgres (pgBackRest), Valkey, files,
email, analytics and the box's own state, kept on the box, and copied off it when you
set a destination (below).

```bash
tiffin backup                 # now
tiffin backups list
tiffin restore <id>           # shows what it will overwrite; repeat with --confirm
tiffin restore latest         # the newest successful one
```

Restore takes a safety backup first. Targets are `postgres` and `valkey` by default;
add `--targets files` for buckets, mail and app disk folders, or `--targets all`.

Backups are restore points of this box. To copy one project (on this box under a new
name, to a file, or to another box), see [copying and moving](moving.md): Duplicate,
Export, Import and Move.

### Copies off the box

Backups on the box undo mistakes; they do not survive losing the server. Set an
S3-compatible bucket and every backup set is copied there, encrypted: Cloudflare R2,
AWS S3, Hetzner Object Storage or MinIO. The bucket must exist; the key needs to read,
write, list and delete objects in it.

```bash
tiffin backups offsite set --endpoint https://<account>.r2.cloudflarestorage.com \
  --region auto --bucket tiffin-backups --prefix shop-box \
  --access-key-id <id> --secret-access-key <secret>
tiffin backups offsite show          # on or off, the newest copy, what it sent
tiffin backups offsite test          # write, read and delete a test object; pgBackRest lists its repository
tiffin backups offsite copy          # copy the newest backup now (they also run after every backup)
tiffin backups offsite list          # the sets in the bucket
tiffin backups offsite off           # stop; the copies in the bucket stay
```

Other endpoints: `https://s3.<region>.amazonaws.com` (`--region <region>`),
`https://<location>.your-objectstorage.com` (`--region <location>`), or your MinIO's
HTTPS address (`--ca-cert "$(cat ca.pem)"` when a private CA signs it). Use one
`--prefix` per box. `set` tests the destination before it saves anything, and keeps the
secret sealed with the box key; `show` never returns it.

**The passphrase.** A new destination gets a generated passphrase, returned once by
`set` (the dashboard shows it once too). Everything in the bucket is encrypted with it,
so keep it off the server, in a password manager. Without it the copies cannot be read:
if the server is lost, a new box needs it to restore them. To use a passphrase of your
own, pass `--passphrase` (12 characters or more) the first time. The copies include the
box key that decrypts the projects' secrets, so the passphrase guards those too; the box
keeps it sealed with that key, and anyone with the bucket's keys but not the passphrase
sees only ciphertext with meaningless names.

What is copied, and how:

- **Postgres** goes to a second pgBackRest repository in the bucket (`<prefix>/pgbackrest`),
  encrypted with aes-256-cbc. After each backup, an incremental backup goes there (a full
  one each week). Postgres keeps archiving WAL to the local repository only; the box ships
  every archived segment on to the bucket every few minutes, and a copy counts as done
  only once the WAL its backup needs is there. A slow or unreachable bucket therefore
  never holds up Postgres or local backups: shipping catches up when it is back, from
  the WAL the local repository keeps. The bucket's Postgres part is a few minutes newer
  than the rest of its set (it is taken when the copy runs), so only sets from the last 6
  hours are copied, and never the safety backup of a restore.
- **Everything else** in the set (the Valkey snapshot, the platform state and box key, and
  registered files: buckets, mail, analytics, issues, apps' disk folders) goes to
  `<prefix>/tiffin/` as compressed, encrypted chunks of up to 4 MiB, named by a keyed
  hash of their content. A chunk the bucket already has is not sent again, and a file
  whose size and time have not changed is not even read, so a copy sends only what
  changed since the last one.
- Copies keep **30 days** by default (`--retention-days`); older sets, and chunks no
  remaining set uses, are deleted once a day. The newest copy is never deleted.

Copies run after the local backup, never inside it: a failing bucket does not stop local
backups. It shows instead: the `offsite-backups` status check fails, and the
`offsite-stale` alert fires, when the newest copy is more than 26 hours old. While copies
are off, the check and the dashboard say "Backups only on this server".

**Restoring from the bucket.** On the same box, `tiffin restore <id> --from offsite`
works like a local restore (Postgres from the bucket's repository, WAL from there too).
After losing the server, on a new one:

```bash
tiffin up                                         # a new box
tiffin backups offsite set ... --passphrase <the passphrase>
tiffin backups offsite list                       # the lost box's sets
tiffin restore latest --from offsite              # the preview: every target, no safety backup
tiffin restore latest --from offsite --confirm <hash> --timeout-seconds 1800
```

On a box with no projects every target is restored by default: Postgres, Valkey, the
files and the platform state (projects, settings, secrets, deploy records, tokens,
people, and the box key). The new box keeps its own owner token, domain and backup
settings, and its service restarts once to swap the state in. Until the restore, `set`
reports the destination as `foreign` (it holds another cluster's backups) and copies
are paused; afterwards the new box carries on copying into the same prefix. App images
are not in backups: deploy the apps again (`tiffin deploy`).

| Operation | What it does |
|---|---|
| `GET /v1/backups/offsite` | the destination (never its secret), `state` (off, active, foreign), `lastCopy`, `lastOk`, `message` |
| `PUT /v1/backups/offsite` | sets it (tested first) → the same, with `passphrase` once for a new destination |
| `POST /v1/backups/offsite/test` | → `ok` and each step with its time |
| `POST /v1/backups/offsite/copy` | copies a set (`backup`, default the newest), waits up to `timeoutSeconds` → copy |
| `GET /v1/backups/offsite/sets` | the sets in the bucket, newest first, with `restorable` |
| `DELETE /v1/backups/offsite` | stops copying |
| `POST /v1/backups/{id}/restore` | `from: "offsite"`; `id` may be `latest`; `targets` may be `platform` or `all` |
| `GET /v1/backups` | also has `offsite`; each set has `offsite` (its copy) |

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

With copies off the box, scheduled drills take turns between the local copy and the
off-box one (`tiffin backups drill --from offsite --wait` runs one by hand). An off-box
drill restores Postgres from the bucket's repository, WAL included, then downloads every
other part of the set into the scratch directory, decrypting each chunk and checking it
against its ID, opens the platform state, checks the box key, the Valkey snapshot and
every SQLite database, and deletes it all. A failed drill is retried from the same copy a
day later; the `restore-drill-failed` alert fires meanwhile.

| Operation | What it returns |
|---|---|
| `POST /v1/backups/drill?wait=true` | starts a drill of the newest good backup → drill (`&from=offsite`: its off-box copy) |
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
