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

Bucket `uploads` of project `shop` is the S3 bucket `shop--uploads`. The S3 name
(`<project>--<bucket>`) must fit in 63 characters, and can't end in a suffix S3 reserves
(a bucket named `x-s3`, `ol-s3` or `table-s3`, say). The double dash keeps every
project's buckets apart: `shop` + `a-b` and `shop-a` + `b` are different buckets.

## What your apps get

| Variable | Example |
|---|---|
| `S3_ENDPOINT`, `AWS_ENDPOINT_URL` | `http://127.0.0.1:7481` (on the box) |
| `S3_REGION`, `AWS_REGION` | `us-east-1` |
| `S3_ACCESS_KEY_ID`, `S3_SECRET_ACCESS_KEY` (and `AWS_*` twins) | the project's own key |
| `S3_BUCKET_<NAME>` | `S3_BUCKET_UPLOADS=shop--uploads` |
| `S3_BUCKET`, `AWS_BUCKET` | set when the project has exactly one bucket (Bun.s3's default) |
| `S3_PUBLIC_ENDPOINT` | `https://s3.<domain>`: for presigned URLs browsers use |
| `TIFFIN_FILES_URL` | `https://files.<domain>/shop`: public files |
| `TIFFIN_PUBLIC_BUCKETS` | `assets` |

The key can only reach the project's own buckets; it cannot create or delete
buckets (that is what `tiffin.config.ts` is for). `tiffin storage credentials <project>`
prints the same variables for tools and local development (it needs a key with
full access, because the key can delete every object).

```ts
import { upload, presign, publicUrl, signedUrl } from "@shiptiffin/sdk/storage";

await upload("uploads", `avatars/${user.id}.png`, file, { contentType: "image/png" });
const link = presign("uploads", `avatars/${user.id}.png`, { expiresIn: 600 });
publicUrl("assets", "logo.png");                    // https://files.<domain>/shop/assets/logo.png
publicUrl("assets", "hero.jpg", { width: 1200 });   // resized to WebP by the box (see Images)
signedUrl("uploads", `avatars/${user.id}.png`);     // a private file, for an hour
```

`@shiptiffin/sdk/storage` signs requests itself (it needs only `fetch` and `node:crypto`), so it
works on Bun and Node. `bucket(name)` returns a `Bun.S3Client` and is Bun only.

## Bucket rules

```ts
buckets: {
  uploads: {
    maxFileSize: 50 * 1024 * 1024,              // bytes
    allowedTypes: ["image/*", "application/pdf"],
    cors: ["https://example.com", "https://*.example.com"],
  },
},
```

The box checks every upload before it is stored: a file over `maxFileSize` is refused with
`EntityTooLarge` (HTTP 413), a `Content-Type` outside `allowedTypes` with `InvalidContentType`
(415). Multipart uploads are checked part by part and again at completion (an upload whose
parts add up to too much is refused and aborted). Server-side copies (`CopyObject`) are not
checked. Form (POST policy) uploads cannot be checked, so a bucket with rules refuses them: use
a presigned PUT.

`cors` lists the browser origins that may call the bucket's S3 API at `s3.<domain>`. Without
it, the project's own app hosts may (previews and custom domains included), and so may
`http://localhost` for local development. `"*"` allows any origin; the presigned URL is what
grants access, CORS only lets a page read the answer. `files.<domain>` answers every origin.

## Uploads from the browser

The bytes go from the browser straight to `s3.<domain>`; your app only hands out a ticket of
presigned URLs. The type, the exact size and a size cap are signed into the URLs, so a ticket
cannot be used for anything else. Files over 64 MiB go up in parts (8 MiB or more, at most
1,000), four at a time; a part that fails is retried, and calling `uploadFile` again with the
same ticket resumes, skipping parts already stored.

A route handler (Next.js `app/api/upload/route.ts`, or any `(Request) => Response` server):

```ts
import { uploadRoute } from "@shiptiffin/sdk/storage";

export const POST = uploadRoute({
  bucket: "uploads",
  maxSize: 50 << 20,
  allowedTypes: ["image/*"],
  authorize: async (file, req) => !!(await getSession(req)),  // false or a throw refuses
  key: (file) => `avatars/${crypto.randomUUID()}.png`,        // default: uploads/<id>/<file name>
});
```

In the page:

```ts
import { uploadFile } from "@shiptiffin/sdk/client";

const done = await uploadFile(file, "/api/upload", {
  onProgress: (p) => setPercent(p.percent),
  signal: controller.signal,      // pause; uploadFile(file, ticket) resumes
  onTicket: (t) => (ticket = t),  // keep the ticket to resume with
});
// done: { bucket, key, size, etag, url? }  url only for public buckets
```

Or a Server Action that returns a ticket:

```ts
"use server";
import { createUpload } from "@shiptiffin/sdk/storage";

export async function startUpload(name: string, size: number, type: string) {
  const user = await requireUser();
  return createUpload({ bucket: "uploads", key: `${user.id}/${name}`, contentType: type, size, maxSize: 2 << 30 });
}
// client: await uploadFile(file, await startUpload(file.name, file.size, file.type), { onProgress })
```

A refused upload throws `UploadError` with the box's `code` (`EntityTooLarge`,
`InvalidContentType`, `QuotaExceeded`). `abortUpload(ticket)` gives up a multipart upload and
frees its parts. `presign(bucket, key, { method: "PUT", contentType, maxSize })` and
`tiffin storage presign <project> <bucket> --key k --method PUT --content-type T --max-size N`
make a single upload URL with the same checks.

## Upload events

After each upload through the box (a PUT, a completed multipart upload, a copy, or
`tiffin storage objects put`) the box publishes an `object.created` event to the project's
queue topic `storage.object.created`. Declare the topic with a subscriber queue to receive
them like any other job, retried until your handler answers 2xx:

```ts
// tiffin.config.ts
queues: { uploads: { app: "web" } },                                 // POST /queues/uploads
topics: { "storage.object.created": { subscribers: ["uploads"] } },

// app/queues/uploads/route.ts
import { onUploadCompleted } from "@shiptiffin/sdk/storage";
export const POST = onUploadCompleted(async (e) => {
  // e: { event, project, bucket, key, size, contentType, etag, url?, at }
  await db.files.insert({ key: e.key, size: e.size });
});
```

A project without the topic gets no events.

## Images

Images in a bucket can be resized and converted on the way out:

```
https://files.<domain>/<project>/<bucket>/<key>?w=640&q=75&f=webp
```

| Parameter | Values |
|---|---|
| `w` | 16, 32, 48, 64, 96, 128, 256, 384, 640, 750, 828, 1080, 1200, 1920, 2048, 3840 (Next.js's sizes); never enlarges |
| `q` | 50, 75 (default), 90, 100 |
| `f` | `webp`, `avif` or `original` (the default) |

Other values answer 400. JPEG, PNG, WebP, AVIF and GIF (animations kept) are transformed; any
other file is served as stored. Each result is made once per version of the object (its
ETag) and kept in a disk cache (`/var/lib/tiffin/cache/images`, 2 GiB, least recently used
files go first); the `X-Tiffin-Cache` header says `HIT` or `MISS`. Transforms run with
libvips as an unprivileged, low-priority process with a 30 second limit and 2 GiB of
memory, on images up to 50 MiB; half the box's CPUs (at least 2) transform at once, one
project gets at most half of those, and projects waiting take turns. AVIF is encoded at
libvips effort 1 (of 0 to 9): about 20 times faster than the default on a detailed image,
for a few percent more bytes. Output is at most 3840 pixels wide (without `w` too, so
`f=webp` alone shrinks a wider image) and 40 megapixels; an image whose transform needs more
answers 422, and the same request answers 422 at once for the next 10 minutes rather than
running again.

Private buckets need a signed link: `signedUrl("uploads", key, { width: 256, expiresIn: 3600 })`.
The signature covers the file and the expiry, not `w`, `q` and `f`, so they can be added to it.

With next/image and its default loader, `/_next/image` requests for the project's own
bucket files are answered with these transforms, with no setup (see
[Next.js](apps.md#nextjs)). The loader below skips `/_next/image` altogether: the page
links `files.<domain>` directly, which browsers and CDNs cache by URL.

```ts
// image-loader.ts
export { default } from "@shiptiffin/sdk/next/image-loader";
// next.config.ts
images: { loader: "custom", loaderFile: "./image-loader.ts" },
// a page: src from publicUrl() or signedUrl()
<Image src={publicUrl("assets", "hero.jpg")} width={1200} height={600} alt="" />
```

The loader rounds widths up to the box's sizes and leaves images that are not on
`files.<domain>` alone.

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
seconds. An upload counts from the moment it is accepted, so uploads at once can't
overshoot together; replacing a file counts only what it adds. With a limit, an upload
must say its size (`Content-Length`). A project that reaches its limit becomes read-only (its database refuses
writes too, its apps' disk folders stop growing) until it is under it again (an app
that overrides the read-only default and keeps growing its database is locked out of
it, reads included, until then); raising or
clearing the limit lifts that within seconds. Disk folders count as files, and the sizes
apps give them (`disk: { data: "5GB" }`, see [Apps](apps.md#programs-folders-and-long-requests))
must fit in the limit: a limit below them is refused. The box owner sets limits on the project's Usage page or with the
CLI. Setting one is a change in History: undo puts the previous limit back.

```bash
tiffin storage quota set shop --max-bytes 53687091200   # 50 GiB for one project
tiffin storage quota set shop --max-bytes=-1            # no limit
tiffin storage quota set shop --max-bytes 0             # back to the box default
tiffin storage quota default --max-bytes 21474836480    # a default for everyone
tiffin storage quota get shop                           # the limit, and what counts toward it
```

## Deleting a bucket

Removing a bucket from `tiffin.config.ts` is an irreversible-tier change, but the
files are kept: the bucket's directory moves to `/var/lib/tiffin/trash/storage` for
7 days. Undoing the change (or adding the bucket back) restores it with its files.
`tiffin storage trash list` shows what is there; `tiffin storage trash purge <id>`
frees the space now. Deleting single objects (`tiffin storage objects delete`) is
immediate and final.

## Renaming, moving and deleting files

The dashboard's Files page (and the same operations from the CLI or an agent) renames
and moves files without copying them, and deletes them with Undo: deleted files are
kept for an hour, and the reply's undo id puts them back. A folder delete asks first,
with how many files and bytes would go.

```bash
tiffin storage objects move shop uploads --to archive/ --keys a.png,b.png   # into a folder
tiffin storage objects move shop uploads --prefix covers/ --to old-covers/  # rename a folder
tiffin storage objects remove shop uploads --keys a.png                     # kept for an hour
tiffin storage undo shop --id stu_...                                       # put it back
tiffin storage link shop uploads --key a.png --expires-in 86400 --w 640     # a link that works for a day
```

Moves and renames don't publish `object.created`; the file is the same file.

## Checks and backups

`tiffin storage audit <project>` reads every object, checks it against its recorded
MD5, and writes a SHA-256 manifest to `/var/lib/tiffin/storage/audit/<project>.json`.
Every backup set (`tiffin backups list`) includes the whole storage tree.

## Under the hood

[versitygw](https://github.com/versity/versitygw) (Apache-2.0, pinned release,
checksum-verified) runs as `tiffin-storage.service` on `127.0.0.1:7480` with its
POSIX backend on `/var/lib/tiffin/storage/data`. A small front server in Tiffin
(`127.0.0.1:7481`) enforces storage limits, read-only holds and bucket rules, answers CORS,
publishes upload events, serves (and transforms) files and passes S3 requests through
unchanged, so signatures and presigned URLs verify. The edge serves it as `s3.<domain>` and
`files.<domain>`. Image transforms run libvips' command-line tool (`libvips-tools`, installed
by `tiffin up`): Tiffin is a static binary without cgo, so it drives the tool rather than
linking the library.
