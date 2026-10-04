# File storage

Every project can have S3-compatible buckets on the box's data disk. Apps use them
with `Bun.s3` or any AWS SDK, with no setup.

```ts
// tiffin.config.ts
services: {
  storage: {
    buckets: {
      uploads: {},               // private: signed requests only
      assets: { public: true },  // anyone can read files by URL
    },
  },
},
```

Bucket `uploads` of project `shop` is the S3 bucket `shop-uploads`. The S3 name
(`<project>-<bucket>`) must fit in 63 characters.

## What your apps get

| Variable | Example |
|---|---|
| `S3_ENDPOINT`, `AWS_ENDPOINT_URL` | `http://127.0.0.1:7481` (on the box) |
| `S3_REGION`, `AWS_REGION` | `us-east-1` |
| `S3_ACCESS_KEY_ID`, `S3_SECRET_ACCESS_KEY` (and `AWS_*` twins) | the project's own key |
| `S3_BUCKET_<NAME>` | `S3_BUCKET_UPLOADS=shop-uploads` |
| `S3_BUCKET`, `AWS_BUCKET` | set when the project has exactly one bucket (Bun.s3's default) |
| `S3_PUBLIC_ENDPOINT` | `https://s3.<domain>`: for presigned URLs browsers use |
| `TIFFIN_FILES_URL` | `https://files.<domain>/shop`: public files |
| `TIFFIN_PUBLIC_BUCKETS` | `assets` |

The key can only reach the project's own buckets; it cannot create or delete
buckets (that is what `tiffin.config.ts` is for). `tiffin storage credentials <project>`
prints the same variables for tools and local development (it needs
`apply:irreversible`, because the key can delete every object).

```ts
import { upload, presign, publicUrl } from "tiffin-sdk/storage";

await upload("uploads", `avatars/${user.id}.png`, file, { contentType: "image/png" });
const link = presign("uploads", `avatars/${user.id}.png`, { expiresIn: 600 });
const putUrl = presign("uploads", "incoming/big.mov", { method: "PUT" }); // browser uploads
publicUrl("assets", "logo.png"); // https://files.<domain>/shop/assets/logo.png
```

## Public files

Objects in public buckets are served at `https://files.<domain>/<project>/<bucket>/<key>`.
Keys that contain a content hash (`app.3f9a2c1d.js`, or `contentKey()` from the SDK)
are cached for a year as immutable; other keys for five minutes. Files are served
with a sandboxing Content-Security-Policy, so an uploaded HTML file cannot run
script on that domain. Private buckets answer 403 there: use a presigned URL.

## Storage limits

A project's storage limit counts its databases (branches included) and its files
together. There is none by default: the box's disk guard already keeps one project
from filling the disk (see [Concepts](concepts.md)). Uploads that would go over a
limit are refused with `QuotaExceeded` (S3) or a `precondition` problem (API). Files
are measured every minute, plus what was uploaded since, and databases every 30
seconds. A project that reaches its limit becomes read-only (its database refuses
writes too) until it is under it again; raising or clearing the limit lifts that
within seconds. The box owner sets limits, on the project's Usage page or:

```bash
tiffin storage quota set shop --max-bytes 53687091200   # 50 GiB for one project
tiffin storage quota set shop --max-bytes=-1            # no limit
tiffin storage quota set shop --max-bytes 0             # back to the box default
tiffin storage quota default --max-bytes 21474836480    # a default for everyone
```

## Deleting a bucket

Removing a bucket from `tiffin.config.ts` is an irreversible-tier change, but the
files are kept: the bucket's directory moves to `/var/lib/tiffin/trash/storage` for
7 days. Undoing the change (or adding the bucket back) restores it with its files.
`tiffin storage trash list` shows what is there; `tiffin storage trash purge <id>`
frees the space now. Deleting single objects (`tiffin storage objects delete`) is
immediate and final.

## Checks and backups

`tiffin storage audit <project>` reads every object, checks it against its recorded
MD5, and writes a SHA-256 manifest to `/var/lib/tiffin/storage/audit/<project>.json`.
Every backup set (`tiffin backups list`) includes the whole storage tree.

## Under the hood

[versitygw](https://github.com/versity/versitygw) (Apache-2.0, pinned release,
checksum-verified) runs as `tiffin-storage.service` on `127.0.0.1:7480` with its
POSIX backend on `/var/lib/tiffin/storage/data`. A small front server in Tiffin
(`127.0.0.1:7481`) enforces storage limits and read-only holds, serves public files and passes S3 requests
through unchanged, so signatures and presigned URLs verify. The edge serves it as
`s3.<domain>` and `files.<domain>`.
