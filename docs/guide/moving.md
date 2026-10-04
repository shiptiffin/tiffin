# Moving a box

A box packs into one file and unpacks on another box: same projects, data, apps,
people and tokens.

```bash
tiffin box export shop.tiffin --key-out shop.key   # on the old box
tiffin box import shop.tiffin --key-file shop.key  # on a fresh box
```

## Export

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

## Import

`tiffin box import <file>` uploads the archive, verifies it on the box (every entry
against the archive's own digest), then restores it: databases, Valkey, files and app
images, then one restart of the box's service to swap in the state. It waits until every
project has converged and ends with the box's status. A small box imports in seconds
plus the upload.

- A box that already has projects is refused. `--replace --confirm <box name>` replaces
  everything on it, after taking a full backup of it.
- Archives from a newer Tiffin are refused: update the box first (`tiffin up`).
- The old box's tokens work on the new one, and so does the new box's own owner token.
- The new box keeps its own domain, Tiffin build, backups and backup schedule.
- The certificate authority becomes the old box's. The CLI updates its copy; run
  `tiffin trust` again for browsers.
- Deploys older than the live one cannot be rolled back to (their images did not travel).

## API

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
