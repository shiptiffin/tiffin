# Copying and moving

Four words, one job each:

| Word | What it does |
|---|---|
| **Backups** | Restore points of the whole box, kept on the box ([data](https://shiptiffin.com/docs/data.md#backups)) |
| **Duplicate** | A full copy of one project on the same box, under a new name |
| **Export / Import** | One project to a `.tiffin` file, and that file to a new project on any box |
| **Move** | One project from this box to another of your boxes |

A whole box packs the same way: see [moving a box](#moving-a-box) below.

## One project

```bash
tiffin projects duplicate shop shop-copy             # a copy on this box
tiffin projects export shop -o shop.tiffin           # one file
tiffin projects import shop.tiffin --name shop-2     # a new project from it
tiffin projects move shop --to prod                  # to another box (a name from `tiffin up`)
```

The dashboard has the same under a project's **Settings › Copy & move** (Duplicate,
Export, and the move command to copy), and **New project › Import a .tiffin file**.

### Duplicate

A full copy on the same box: the database, buckets and files, KV keys, secrets,
settings and apps (started again from the same images or files). The copy gets its own
addresses: a box name that starts with the project's moves with it (`shop` →
`shop-copy`, `shop-api` → `shop-copy-api`); an app served at its own app name (the old
default) or only at custom domains gets the copy's default (`shop-copy`,
`shop-copy-<app>`); any other name gets the new name added (`www` → `www-shop-copy`).
Custom domains and GitHub deploys stay with the original;
the job's notes say which. The copy starts its own History, with one first entry,
"Duplicated from shop". To undo a duplicate, destroy the copy.

### Export

`tiffin projects export <project> [-o file]` writes one `.tiffin` file (a
zstd-compressed tar) that streams to your computer as it is made. Inside, ordinary
files you can use without Tiffin (`tar --zstd -xf shop.tiffin`):

| File | What it is |
|---|---|
| `README.md` | What is inside and how to run it |
| `project.json` | The project, machine-readable: config, apps, buckets, secret names, extensions |
| `tiffin.config.ts` | The project's config |
| `docker-compose.yml` | Postgres, Valkey, MinIO (for the buckets) and the apps, with the same env vars as on the box (`DATABASE_URL`, `REDIS_URL`, `S3_*`...) |
| `database-setup.sql`, `database.sql` | Extensions and Tiffin's helper functions, then `pg_dump` as plain SQL with no owners or grants: loads into any Postgres as any user |
| `cache.jsonl` | The cache keys (without the project's prefix), each with its TTL and `DUMP` payload |
| `files/<bucket>/...` | Every object, one file each |
| `apps/<app>/image.tar` | A container app's live image: `docker load -i` |
| `apps/<app>/site/` | A static site's live files |
| `source.git/` | The push-to-deploy git repository, when the project has one |
| `secrets.json` or `.env` | Secrets: sealed to this box's key, or with `--include-secrets` in plain text |
| `history/changes.jsonl` | The project's History, with `--with-history` |

Secrets stay sealed to the box key unless you pass `--include-secrets`: then anyone
with the file can read them. The database is one consistent snapshot; files and cache
keys are read live. An export needs full access to the project, since it hands out all
of its data.

### Import

`tiffin projects import <file>` makes a **new** project from an export, beside the box's
other projects; it never replaces one. A name already in use is refused: pass
`--name`. Under another name the apps get that project's own addresses, as with a
duplicate. The database loads as the new project's own Postgres role, so an archive can
do nothing the project could not do itself. Then the apps start from the archive's images
or files, and the project's History comes along if it was exported.

If an app does not start, the import fails (the CLI exits non-zero) though the project
is there with its data: the result names the app, why its deploy failed and what to do.
Deploy that app again once fixed, or destroy the project and import again.

Secrets sealed to another box need that box's key: `--secrets-key-file <file>` (its
`/var/lib/tiffin/platform/secrets.key`). Or `--without-secrets` imports the rest and
lists the secrets to set again (`tiffin secrets set`). Archives from the same box, and
`.env` archives, need nothing.

A name destroyed less than 7 days ago is refused too: re-creating it would bring back
the destroyed project's data (that is how its undo works).

### Move

`tiffin projects move <project> --to <box>` moves a project to another box this CLI knows
(see `~/.tiffin/boxes.json`). The export streams from this box straight into an import
on the other (secrets travel inside it, never stored on your computer), with its History.
Then it:

- prints each app's old and new address;
- re-points custom domains: with `--update-dns` the new box sets their DNS records where
  its DNS provider holds the zone; otherwise it prints the records to set;
- **stops** the project here: its apps go down, its data stays (`tiffin projects start
  <project>` brings it back). Destroy it here once you are happy with the move.

If the import fails, an app of it not starting included, nothing here changes. Agents get the same as the `project_move` tool
of `tiffin mcp`, when the CLI knows more than one box.

### Stop and start

`tiffin projects stop <project>` stops every app of a project and keeps them stopped:
deploys, restarts and rollbacks are refused until `tiffin projects start <project>`. The
data and settings stay. Queued jobs and workflow turns wait without spending attempts, and
crons do not fire; on start the jobs are delivered and each cron continues from its next
tick (ticks that fell inside the stop are skipped). Both are changes in History, so undo
works too.

### API

| Call | What |
|---|---|
| `POST /v1/projects/{project}/duplicate` `{name}` | start a duplicate → job |
| `POST /v1/projects/{project}/exports` `{includeSecrets, withHistory}` | start an export → `download` path |
| `GET /v1/projects/{project}/exports/{id}[/download]` | progress and result; the archive (once) |
| `POST /v1/project-imports` (raw body) | upload and verify → job with `source` (what it holds, whether the name is taken) |
| `POST /v1/project-imports/{id}/apply` `{name, secretsKey, withoutSecrets}` | import it → job |
| `DELETE /v1/project-imports/{id}` | discard an upload |
| `GET /v1/project-jobs/{id}` | a duplicate or import: phase, percent, then the new project, its apps' addresses, secrets left out, notes |
| `POST /v1/projects/{project}/stop`, `/start` | stop or start its apps (a change) |

## Moving a box

A box packs into one file and unpacks on another box: same projects, data, apps,
people and tokens.

```bash
tiffin box export shop.tiffin --key-out shop.key   # on the old box
tiffin box import shop.tiffin --key-file shop.key  # on a fresh box
```

### Export

`tiffin box export [file]` writes one archive (`.tiffin`, a zstd-compressed tar) and
ends with its size and SHA-256. It streams to your computer as it is made, so the box
needs no spare disk (`--store` writes it on the box first instead).

What goes in:

| Part | How |
|---|---|
| Platform state: projects, change history, settings, deploy records, API keys, people, passkeys | a consistent SQLite copy |
| Secrets | inside the state, **still encrypted** to the box key |
| Every Postgres database (with auth users and queue jobs, which live there) | `pg_dump`, plain SQL, plus roles and pg_cron jobs |
| Valkey | an RDB snapshot |
| Buckets and objects, S3 accounts | the storage tree, metadata included |
| Email inbox and outbox, analytics, error issues and alert rules | file copies (SQLite stores snapshotted) |
| Apps | the live deploys' container images and static sites, and the git repositories |
| HTTPS | the box's certificate authority and certificates |

Left out unless you ask: logs, metrics, build logs and database snapshots
(`--with-history`). Never included: caches, build caches, the box's backups.

Postgres travels as SQL rather than a physical copy, so it restores into any later
Postgres and does not drag the old cluster's identity along.

**Consistency.** App containers and the object store pause while the snapshot is taken
(usually well under a second; the export reports `writesPausedMs`). Every database
dump reads the same instant; the rest streams afterwards while the box keeps serving.

**The key.** Secrets in the archive stay encrypted to the box key
(`/var/lib/tiffin/platform/secrets.key` on the box), which is not in the archive unless
you pass `--include-key`. Import needs it, so keep it before you delete the old box:
`--key-out shop.key` saves it from a local box. With `--include-key`, anyone holding
the file can read every secret.

### Import

`tiffin box import <file>` uploads the archive, verifies it on the box (every entry
against the archive's own digest), then restores it: databases, Valkey, files and app
images, then one restart of the box's service to swap in the state. It waits until every
project has converged and ends with the box's status. A small box imports in seconds
plus the upload.

- A box that already has projects is refused. `--replace --confirm <box name>` replaces
  everything on it, after taking a full backup of it.
- Archives from a newer Tiffin are refused: update the box first (`tiffin up --name <box>`, or Settings › Updates).
- The old box's tokens work on the new one, and so does the new box's own owner token.
- The new box keeps its own domain, Tiffin build, backups and backup schedule.
- The certificate authority becomes the old box's. The CLI updates its copy; run
  `tiffin trust` again for browsers.
- Deploys older than the live one cannot be rolled back to (their images did not travel).

### API

For the dashboard and agents (owner or admin; importing is owner only):

| Call | What |
|---|---|
| `POST /v1/box/exports` `{includeKey, withHistory, store}` | start an export → `Export` |
| `GET /v1/box/exports`, `GET /v1/box/exports/{id}` | list, progress (`phase`, `percent`, `contentBytes`), result (`sizeBytes`, `sha256`, `parts`, `key`) |
| `GET /v1/box/exports/{id}/download` | the archive; for a stream export this runs it |
| `DELETE /v1/box/exports/{id}` | cancel, or delete a stored archive |
| `POST /v1/box/imports` (raw body) | upload and verify → `Import` |
| `GET /v1/box/imports`, `GET /v1/box/imports/{id}` | list, progress (`status`, `phase`, `percent`), result (`healthy`, `failing`) |
| `POST /v1/box/imports/{id}/apply` `{replace, secretsKey, confirm}` | restore it: without `confirm` you get 428 with the preview and the confirm value |
| `DELETE /v1/box/imports/{id}` | discard an upload |
