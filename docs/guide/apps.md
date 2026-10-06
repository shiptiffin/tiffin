# Apps and deploys

An app is one deployable unit in `tiffin.config.ts`:

```ts
apps: {
  web:    { framework: "next", path: "apps/web" },
  api:    { framework: "hono", path: "apps/api", routes: ["web/api"], instances: 2 },
  worker: { framework: "bun",  path: "apps/worker", role: "worker" },
  site:   { framework: "static", path: "site" },
}
```

Bun is the only runtime. `next`, `hono` and any Bun server listening on `$PORT` run as
containers; `static` sites are served straight from the edge.

The edge compresses text responses (zstd or gzip) for every app; a response the app
compressed itself is passed through. A static site's pages and files are revalidated on
every visit, except fingerprinted build assets (`/assets/index-B1x9Qa2c.js`,
`/_next/static/…`), which browsers keep for a year. `.br`, `.zst` or `.gz` files next to
the originals are sent instead of compressing on the fly.

## Deploy

```bash
tiffin deploy                 # every app in tiffin.config.ts
tiffin deploy --app api       # one app
tiffin deploy --preview pr-12 # a preview at pr-12--<project>.tiffin.localhost
git push tiffin main          # after `tiffin git-remote --add`
```

The box builds with Railpack and BuildKit, starts the new instances, waits for the
health check, switches traffic with no dropped requests, then drains the old ones. A
failed build or health check leaves the old version serving.

- **Rollback:** `tiffin rollback <app> [deploy]`, to one of the last 3 production deploys
  before the live one (older builds are cleaned up; their records stay listed).
- **Logs:** `tiffin logs <app> -f`.
- **Previews** sleep when idle and wake on the first request. Previews use this
  project's live data: the same database, cache, files and secrets. Email goes to the
  dev inbox. A preview keeps only its latest build (no rollback), and one nobody
  requested or deployed to for 7 days is deleted, as if its pull request had closed;
  the next push or deploy builds it again.
- **Start command:** an app runs its build's start command (package.json `start`); set
  `command` to run something else. One folder can then hold a web app and a worker:
  `web: { path: "app" }, worker: { path: "app", role: "worker", command: "bun run worker.ts" }`.
  It applies from the next deploy; static apps have none.
- **Prebuilt images:** `tiffin deploy --prebuilt image.tar` (one image, from `docker save`
  or `nerdctl save`). The tarball's own names are replaced by the deploy's as it loads:
  the image is kept under the deploy's name only, so it cannot replace another project's
  or the box's images.
- **Client assets:** for Next.js, Nuxt, TanStack Start, SolidStart, React Router, Remix,
  SvelteKit (adapter-node) and Astro (`@astrojs/node`) apps, the box copies the build's
  browser files (JS, CSS, images) out of the image and serves them itself: hashed files
  with a year-long immutable cache, others with revalidation. Hashed files of the
  previous releases stay served for a day, so a page loaded before a deploy keeps
  finding its chunks. The box serves these files without running the app's middleware.
  For another framework, name the directory: `assets: { dir: "dist/client", path: "/" }`
  (path defaults to `/`; files there are kept for old pages too, revalidated).
- **Shutdown:** a replaced release finishes the requests it has (for up to 15 minutes,
  so a long render survives a deploy), gets SIGTERM once they are done, then 30 seconds
  before it is killed, for work it does after responding.

## Programs, folders and long requests

```ts
apps: {
  web: {
    packages: ["ffmpeg", "chromium"],        // Debian packages in the app's image
    disk: ["data", ".renders", "uploads/tmp"], // folders kept across deploys
  },
}
```

- **Packages:** `packages` installs Debian (apt) packages in the image the app runs, so
  it can call `ffmpeg`, `ffprobe` or a headless `chromium`. Names are Debian's (`ffmpeg`,
  `chromium`, `imagemagick`, `poppler-utils`). They apply from the next deploy (the build
  log says what it installs). A prebuilt image (`--prebuilt`) brings its own; static apps
  have none.
- **Disk folders:** each path in `disk` is a folder, relative to the app's working
  directory (`/app`), that keeps what the app writes across deploys, restarts and
  rollbacks. All production instances share it (one disk, so SQLite works across them).
  The first time the app starts with a folder, the folder gets what the image has at
  that path (a SQLite file the repository ships, say); after that it is the app's and a
  deploy never touches it. Each preview gets its own folders,
  made the same way from the preview's build and deleted with the preview, so a preview
  never writes to production's. The folders count toward the project's storage limit
  (as files, in `tiffin projects usage`), travel in `projects export`, `duplicate` and
  `move`, and are in box backups. Over the limit, the database and buckets turn read-only
  but the folders themselves are not blocked: give apps that write a lot a storage limit
  and watch usage. Deleting the app, or the project, moves its folders to
  `/var/lib/tiffin/runtime/disks-trash` for 7 days. Taking a path out of `disk` unmounts
  it but keeps its data (counted) until the app is deleted. Files the app writes to
  `public/` at run time are not served by Next.js (it only serves what the build had):
  put user files in a bucket (`services.storage`) instead.
- **Long requests:** the box puts no time limit on a request. A response may take 15
  minutes or more, streamed or all at once, and nothing cuts a stream for pausing. Only a
  client that closes the connection ends it (browsers and proxies in front of the box may
  have limits of their own). A preview with a request under way does not fall asleep.
- **Uploads:** no size limit on request bodies; they stream to the app as they arrive.
  With the WAF on (see [Protection](protection.md)), it inspects the first 12.5 MB of a body and passes
  the rest through, and its rules refuse some content types (a raw
  `application/octet-stream` body, for one): send files as `multipart/form-data`.
  For very large files, an upload straight to a bucket (a presigned URL) spares the app.

## Without a checkout: templates and git URLs

The dashboard creates apps without any files on your machine; so can the CLI and agents.

```bash
tiffin templates list                                  # starters shipped inside tiffin
tiffin deploys template shop api --template hono-postgres
tiffin deploys git shop web --git-url https://github.com/owner/repo --ref main --path apps/web
```

Four starters ship in the binary: `static-site`, `hono-postgres` (a notes API that
creates its table on boot), `guestbook` (page + API + Postgres + Valkey + analytics in
one Hono app) and `next-postgres` (App Router, reads and writes Postgres). Each lists
the manifest fragment it needs: add that to the project (see
[Concepts](concepts.md#changes)), apply, then deploy the template.

To change a starter app, `tiffin pull <dir> --project <project>` writes the config and
the starter's source into `<dir>` (it never overwrites a file that is there); edit it,
then `tiffin deploy` from that folder ships it to the same address.

A git URL deploy shallow-clones one commit of a **public https** repository on the box
(no credentials, public hosts only, no submodules, 512 MB and 3 minutes at most) and
builds it like `tiffin deploy`; the clone shows in the build log. For private code, push
to the box instead, or connect GitHub.

## Deploy from GitHub

Connect the box to GitHub once, and it deploys like Vercel: every push to an app's
production branch goes live, every pull request gets a preview at its own address (with
one comment on the pull request, kept up to date), and closing the pull request removes
the preview.

**1. Connect.** Dashboard › Settings › Git › **Connect GitHub**. The box makes its own
GitHub App (the *manifest flow*: no shared secrets, nothing to copy): GitHub asks you to
confirm `tiffin-<your box>` for your account (tick *In an organization* for an org), then
you choose which repositories it may see. The app's private key and webhook secret stay
on the box, encrypted with the box's own key. The box needs a public HTTPS address first,
because GitHub delivers pushes to `https://<dashboard>/v1/github/webhook`.

The app asks for: code (read), metadata (read), pull requests (write, for the preview
comment), commit statuses (write) and deployments (write). Events: push and pull request.

**2. Import a repository.** New project › **Import from GitHub**: search your repositories
(private ones included), pick one, then its production branch and folder (monorepos list
each app they find, with the framework the box would use), add environment variables
(they're saved as encrypted secrets) and create. That writes the app with a `git` block
and deploys the branch's latest commit:

```ts
apps: {
  web: { framework: "next", git: { repo: "acme/shop", branch: "main", path: "apps/web" } },
}
```

| `git` field | Default | |
|---|---|---|
| `repo` | (required) | `owner/name` on GitHub |
| `branch` | `"main"` | the production branch: every push to it deploys |
| `path` | the top | the app's folder in the repository |
| `previews` | `"same-repo"` | `"same-repo"`: a preview per pull request from a branch of this repository; `"forks"`: forks too; `"off"` |

`tiffin pull` writes the block back, so the config round-trips.

**3. Push.** That's it. What happens on GitHub:

- the commit gets a status `tiffin/<project>/<app>`: *pending* while it builds, then
  *success* (linking the live address) or *failure* (linking the build log);
- a GitHub deployment per deploy (`tiffin/<project>/<app>`, previews as transient
  environments `…/pr-12`);
- a pull request's preview lives at `pr-12--<app address>.<domain>` (`pr-12--shop` for the app at
  `shop`); one comment says
  "Preview of `web`: https://pr-12--shop.example.com · built in 34 s · logs".

Rapid pushes to one branch coalesce: while one builds, only the newest waiting push is
built next (the ones in between show as *skipped*). A commit message with `[skip deploy]`
or `[skip ci]` deploys nothing. On the app's page: the connected repository and branch,
each version's commit (message, author, SHA), **Redeploy** and **Disconnect repo**.

**Security.** Deliveries must carry a valid `X-Hub-Signature-256` (HMAC-SHA256 with the
app's webhook secret, compared in constant time); a delivery id is accepted once, and
events older than an hour are refused. Each clone uses a fresh token that can only read
that one repository, handed to git through its environment (never a file or the command
line) and revoked as soon as the clone is done. **Pull requests from forks are not built**
unless the app says `previews: "forks"`: a fork's code would run on your box with the
project's env and secrets.

**From the terminal or an agent** (every step is an API operation, so also an MCP tool):

```bash
tiffin github status                       # connected? installed where? recent deliveries
tiffin github repos --q shop               # repositories the app can see
tiffin github repo acme shop               # branches, latest commit, folders + framework
# add apps.web.git to the manifest, then plan and apply it (projects manifest → plan → apply)
tiffin deploys github shop web             # deploy the branch now (Redeploy); --ref for another
```

Connecting needs a person in a browser (`tiffin github connect` returns the form GitHub
expects); agents should ask the owner to click Connect GitHub. `tiffin github disconnect`
forgets the app (delete it on GitHub too: Settings › Developer settings › GitHub Apps).

**A shared app instead** (a hosted box, or an app you already have): `tiffin github use-app
--app-id 123 --private-key "$(cat app.pem)" --webhook-secret … [--client-id … --client-secret
… --public]`, or the operator writes `/var/lib/tiffin/platform/github.json`:

```json
{ "app": { "id": 123, "privateKeyFile": "/etc/tiffin/github-app.pem", "webhookSecretFile": "/etc/tiffin/github-webhook",
           "clientId": "Iv1.…", "clientSecretFile": "/etc/tiffin/github-client", "public": true } }
```

The app's webhook must point at `<box>/v1/github/webhook`. A **public** app is installed by
other accounts too, so the box only acts for installations made from it: enable *Request
user authorization (OAuth) during installation* on the app with `<box>/v1/github/setup` as
a callback URL; after an install the box checks, with GitHub sign-in, that the person can
reach that installation. The same file takes `"apiUrl"` and `"webUrl"` for GitHub
Enterprise Server.

**Try it for real (once the box has its public domain):**

1. Dashboard › Settings › Git › Connect GitHub → confirm on GitHub → choose a repository
   (a small Next.js or static one) → you land back on Settings › Git with "Connected".
2. New project › Import from GitHub → pick it → Create. The build log streams; the commit
   on GitHub shows a pending, then green, `tiffin/…` check.
3. Push a commit to the branch: a new version appears on the app's page within seconds of
   the push, with its message and author.
4. Open a pull request: a preview comment appears, then updates with the address; open it.
   Push to the pull request: the same comment updates. Close it: the preview is removed
   and the comment says so.
5. Settings › Git › Recently lists each delivery; GitHub's app settings › Advanced lists
   them too (all should be 2xx).

## Sharing the box

Every app copy of a project, previews included, runs inside the project's share of the
box. Apps don't need a memory setting: by default their copies share the project's
memory, and the project grows into whatever the box has free (see
[Sharing the box](concepts.md#sharing-the-box)). To give a project a fixed share, set
`resources` at the top of `tiffin.config.ts`; to also stop one copy from crowding out
its siblings, give the app its own cap:

```ts
resources: { memoryMB: 1024 },               // the whole project
apps: { web: { instances: 2, memoryMB: 384 } } // each web copy, within that
```

`tiffin projects usage shop` shows how much the project uses, how much more it could
take, and whether it ran out lately. When an app is stopped for memory, it restarts on
its own and the usage says `pressure: "oom"`: raise the budget or find the leak.

## What your app gets

`PORT`, `NODE_ENV`, `TIFFIN_URL` (its public URL), plus each service's variables:
`DATABASE_URL`, `REDIS_URL`, `S3_*`, `SMTP_URL`, `TIFFIN_AUTH_URL`, `SENTRY_DSN`,
`OTEL_*`, `TIFFIN_QUEUE_*` and your secrets (`tiffin secrets set`). Changing env or
secrets restarts the app with the new values. Setting, copying or deleting a secret is a
change in History (`-m` gives the reason) that `tiffin undo <id>` reverts, putting back the
old value: the change log keeps values only encrypted to the box key. Destroying a project
deletes its secrets too (its plan lists them).

## Next.js

Next runs as a long-lived Bun server (`bun --bun next start`), so route handlers and
server actions run in the process; long work goes to queues. No next.config is needed:
with Next.js 16.2 or later, the box adds its adapter to every build
(`NEXT_ADAPTER_PATH`), which sets what next.config leaves unset:

- `deploymentId`: the deploy's ID. A browser still on an older release reloads the page
  instead of mixing builds.
- With Valkey in the project (`services: { valkey: {} }`): `cacheHandler` and
  `cacheHandlers` (`default`, `remote`) from `tiffin-sdk/next`, and `cacheMaxMemorySize: 0`,
  so every instance and preview shares one cache and `revalidateTag` reaches all of
  them. It takes effect on the next deploy after adding Valkey.
- `compress: false`: the edge compresses.
- `images.maximumDiskCacheSize`: 512 MB. Optimized images live in a directory per app
  environment, shared by its instances and kept across deploys (deleted with the
  preview or app).

The app also gets `NEXT_SERVER_ACTIONS_ENCRYPTION_KEY`, made once per app and used at
build and run time, so Server Actions in a page from the previous release still work
after a deploy. Previews share it; a duplicated or imported project gets its own. Set
the variable (or `NEXT_ADAPTER_PATH`) yourself to use your own. Older Next.js versions
ignore the adapter and build as before.

`templates/hello-next` is an example with two instances and Valkey.

Templates: `templates/hello-hono`, `hello-next`, `static-site`, `queues-worker`.
