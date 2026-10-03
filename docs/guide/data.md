# Postgres, Valkey and backups

## Postgres

```ts
services: { postgres: { extensions: ["vector", "pg_cron"] } }
```

Each project gets its own Postgres 18 database and role. Apps get `DATABASE_URL`.

- **SQL:** `tiffin sql <project> "select ..."` is read-only by default. Writes need
  `write: true`, an owner-level token, and take a snapshot first.
- **Branches:** `tiffin branches create <project> --name pr-12` clones the database with
  copy-on-write in milliseconds, whatever its size. Previews can use their own branch.
- **Snapshots:** deleting the database (or writing through the console) keeps a
  snapshot for 7 days; `tiffin snapshots restore` brings it back.
- **Org isolation:** `auth.enable_org_rls('table')` adds row-level security keyed on the
  signed-in user's organization.

## Valkey

```ts
services: { valkey: { maxMemoryMB: 128 } }
```

Each project gets a Valkey user limited to its own key prefix. Apps get `REDIS_URL` and
`VALKEY_PREFIX`. `Bun.redis` works as is; `tiffin-sdk/kv` adds prefixed helpers, an
atomic rate limiter and `cached()`.

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

Restore takes a safety backup first. Off-site copies (R2 or any S3) come next.
