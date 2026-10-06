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
the originals are sent instead of compressing on the fly. A static site answers `/about` from
`about.html`, and a folder from its `index.html` (asked for without its slash, it redirects
there); a path with no file gets the site's `404.html` with status 404 when it has one.

The edge adds `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy:
strict-origin-when-cross-origin` and `Content-Security-Policy: frame-ancestors 'none'` to
responses that do not set them; an app's own headers (or its vercel.json's) win.

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
- **Previews** sleep when idle and wake on the first request. Each preview gets its own
  copy of the project's database (see below); it shares the cache, buckets and secrets
  with production. Email goes to the dev inbox. A preview keeps only its latest build (no rollback), and one nobody
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
- **Shutdown:** a replaced release finishes the requests it has (each within the app's
  time limit, `timeoutSeconds`, so a long render survives a deploy), gets SIGTERM once
  they are done, then 30 seconds before it is killed, for work it does after responding.

## Migrations and preview databases

```ts
apps: { web: { framework: "next", release: "bunx drizzle-kit migrate" } }
```

`release` runs once per deploy, after the build and before the new version takes
traffic: one container of the new image with the app's env (secrets too; `DATABASE_URL`
goes straight to Postgres here, not through the pooler, so migration locks work), memory
cap and disk folders, in the app's folder. Its
output is in the deploy log (`tiffin deploys build-log`). If it exits non-zero, or runs
over 10 minutes, the deploy fails and the running version keeps serving. Any command
works (`bun run db:migrate`, `bunx prisma migrate deploy`); releases of one app run one
at a time. Static apps have none.

- **Old and new side by side.** The old version keeps serving while the release runs
  and while the new one starts, and a rollback does not run it again (nor undo it). Write
  migrations that the running version survives: add a column or table in one deploy,
  start using it, and drop what the old code needs only in a later deploy
  (expand, then contract).
- **Partial runs.** A migration that stops part-way may have applied some steps. Keep
  each one in a transaction (drizzle-kit and Prisma do) or safe to run again.
- **Migrating on start instead** (the starters do it): fine for `create table if not
  exists`, but every instance runs it and a failure only shows up as a failed health
  check. `release` runs it once and says why it failed.

**Preview databases.** With `services.postgres`, each preview gets its own branch of the
database (`pv-<preview>`, e.g. `pv-pr-12`): a copy-on-write copy of production's made on
the preview's first deploy (milliseconds, whatever the size), deleted with the preview
(pull request closed, `tiffin previews delete`, or 7 days unused). The preview's
`DATABASE_URL`, `DIRECT_DATABASE_URL` and `PG*` point at it, and its `release` migrates
it, so a preview can change its schema and data without touching production's. Apps of
the project that have a preview of the same name share it. While the copy is made,
queries through the pooler wait (usually well under a second, once per preview) and
direct connections close (pools reconnect); a transaction still running after 5 seconds
is ended. `tiffin sql <project> --branch pv-pr-12`
reads it.

```ts
services: { postgres: { previews: "shared" } } // previews use the production database
```

With `"shared"`, previews read and write production's data and skip `release`; the plan
warns about it. A value the app sets itself (`DATABASE_URL` in env or a secret) is never
replaced.

## Programs, folders and long requests

```ts
apps: {
  web: {
    packages: ["ffmpeg", "chromium"],             // Debian packages in the app's image
    disk: { data: "5GB", ".renders": "20GB" },    // folders kept across deploys, with sizes
    timeoutSeconds: 3600,                         // one request may take an hour (default 15 min)
  },
}
```

- **Packages:** `packages` installs Debian (apt) packages in the image the app runs, so
  it can call `ffmpeg`, `ffprobe` or a headless `chromium`. Names are Debian's (`ffmpeg`,
  `chromium`, `imagemagick`, `poppler-utils`). They apply from the next deploy (the build
  log says what it installs). A prebuilt image (`--prebuilt`) brings its own; static apps
  have none.
- **Disk folders:** each folder in `disk` is relative to the app's working directory
  (`/app`) and keeps what the app writes across deploys, restarts and rollbacks. All
  production instances share it (one disk, so SQLite works across them). The first time
  the app starts with a folder, the folder gets what the image has at that path (a SQLite
  file the repository ships, say); after that it is the app's and a deploy never touches
  it. Each preview gets its own folders, of the same sizes, made the same way from the
  preview's build and deleted with the preview, so a preview never writes to production's.
  Folders travel in `projects export`, `duplicate` and `move`, and are in box backups.
  Deleting the app, or the project, moves its folders to
  `/var/lib/tiffin/runtime/disks-trash` for 7 days. Taking a path out of `disk` unmounts
  it but keeps its data (counted) until the app is deleted. Files the app writes to
  `public/` at run time are not served by Next.js (it only serves what the build had):
  put user files in a bucket (`services.storage`) instead.
- **Folder sizes:** a folder holds no more than its size, like a volume: `disk: ["data"]`
  makes each folder 1GB; `disk: { data: "5GB", renders: "500MB" }` sets them (MB, GB or
  TB; 1GB = 1024MB). A write past the size fails as a full disk does (`ENOSPC`, "No space
  left on device"), so the app sees the error; nothing else on the box is affected.
  Growing a folder applies when the change is applied, without a deploy or restart.
  Shrinking one below what it holds is refused at plan time; delete files first. The
  sizes set this way must fit together in the project's storage limit, which its database
  and buckets share (a plan over it is refused; folders listed without sizes are not
  counted against it). What the folders hold counts as files in the project's usage;
  `tiffin projects usage` lists each folder's use against its size (`disk.folders`), and
  box health names folders at 90% of their size or more. When the project reaches its
  storage limit, or the disk guard stops it because the data disk is nearly full, its
  folders stop growing too, with its database and buckets. Sizes need the data disk
  mounted with XFS project quotas: boxes set up now are; on a server set up earlier,
  `tiffin up` adds them and they turn on at the server's next restart (`sudo reboot`).
  Until then folders are not limited, and box health says so. A folder that already held
  more than 1GB when sizes came in may hold what it held plus 1GB until you give it a size.
- **Long requests:** one request may take up to the app's time limit, `timeoutSeconds`:
  15 minutes by default (a Vercel function's `maxDuration`, at most 800 s, fits), up to
  86400 (24 hours). The limit counts from the moment the request reaches the box to the response's last
  byte, upload included. Past it the box answers `504 Gateway Timeout`, saying which limit
  ran out, or, when the response has begun, ends it there (a stream is cut). Under it a
  response may take as long as it needs, streamed or all at once, and a stream may pause
  for as long as it likes. A client that stops sending its body, or stops reading, for 5
  minutes is cut (see [Protection](protection.md)). Queue jobs and workflow steps are
  not requests: they have their own limits. A new limit applies to the next request, with
  no deploy. A release replaced by a deploy finishes the requests it has, each within its
  limit. Browsers and proxies in front of the box may have limits of their own. A preview
  with a request under way does not fall asleep.
  Bun's own server (`export default { fetch }`, Hono, Elysia) has two limits of its own:
  it closes a request that sends nothing for 10 seconds and refuses bodies over 128 MB.
  Lift them in the app: `export default { port, fetch, idleTimeout: 0,
  maxRequestBodySize: 4 * 1024 ** 3 }` (or `server.timeout(req, 0)` for one route).
  Next.js has neither.
- **Uploads:** no size limit on request bodies; they stream to the app as they arrive.
  With the WAF on (see [Protection](protection.md)), it inspects the first 12.5 MB of a body and passes
  the rest through, and its rules refuse some content types (a raw
  `application/octet-stream` body, for one): send files as `multipart/form-data`.
  For very large files, an upload straight to a bucket (a presigned URL) spares the app.

## Coming from Vercel

The box reads what an app already has, so an app that deploys on Vercel deploys here
unchanged.

- **Next.js static export.** A Next.js app whose `next.config` (`.js`, `.mjs`, `.ts`) sets
  `output: "export"` builds with its own `next build` and package manager (Railpack), then
  the edge serves `out/` as a static site, with no container, whatever its `framework`. The
  build log says "Next.js static export"; the deploy's framework is `next+export`.
- **Monorepos.** An app in a JavaScript workspace goes up with the whole workspace, as on
  Vercel: the nearest folder above it with a `pnpm-workspace.yaml` or a `package.json`
  `workspaces` field (inside its repository), when that lists the app's folder among its
  packages or the app uses a workspace package (`"@acme/ui": "workspace:*"`). Dependencies
  install at the top with the workspace's package manager (by its lockfile), then the app
  builds and starts in its own folder (the deploy's `dir`, e.g. `apps/web`). `tiffin
  deploy`, git pushes and GitHub deploys all do this. Any other app goes up alone.
- **vercel.json** in the app's folder is read at every deploy. The build log lists what was
  taken and what was not used (`tiffin plan` says the same on a terminal), and the deploy
  record keeps it (`vercel`):

| vercel.json | On the box |
|---|---|
| `buildCommand`, `installCommand` | Replace the build's own: build in the app's folder, install at the top of its workspace |
| `outputDirectory` | The folder a static site or export serves |
| `crons` | Crons that call the app with `GET` and its `CRON_SECRET`, as Vercel does ([queues](queues.md#crons)) |
| `headers` | Set at the edge on matching responses; they win over the app's own and the edge's defaults (CSP, `X-Frame-Options`) |
| `redirects` | Answered at the edge, query string kept; `permanent: false` is 307, `statusCode` is kept |
| `rewrites` | Static sites: another path of the site answers when no file matches |
| `cleanUrls`, `trailingSlash` | Static sites: `/page.html` redirects to `/page`; paths get (or lose) their trailing slash |

Sources use Vercel's patterns (`/blog/:slug`, `/docs/:path*`, `/(.*)`, `/post/:id(\d+)`), and
destinations `:slug` or `$1`. Not used, and listed as such: rules with `has` or `missing`,
rewrites to another site, and every other key (`functions`, `regions`, `framework`...). For a
server app, rewrites, `cleanUrls` and `trailingSlash` stay with the app: Next.js's own
`next.config` redirects, rewrites and headers run inside it as before. A static site's build
runs `bun install` and `bun run build`; when vercel.json's commands use npm, pnpm or yarn, or
its workspace does, it builds with Railpack instead. A broken vercel.json fails the deploy, saying why; the live
version keeps serving.

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

## Letting apps sleep

Production apps never sleep unless you say so. A side project that gets a few visits a
week can give its memory back to the box between them:

```ts
sleepAfter: "7d", // at the top of tiffin.config.ts: hours or days, "1h" to "30d"
```

After that long with no requests and no job, cron or workflow deliveries, the project's
production apps sleep: their containers stop, freeing their memory and CPU (usage counts
them as using none). Their images, data, routes, env and secrets stay. In the dashboard,
the project's Settings › When nobody visits offers Never (the default), 24 hours, 7 days
or 14 days, and the project says "Asleep since …" with a Wake button.

- **Waking:** the next request is held while the app starts and passes its health check,
  then answered; if it cannot start, the visitor gets `503` with `Retry-After`. A job,
  cron tick or workflow turn for a sleeping app wakes it first and is then delivered, so
  no attempt is spent on it. Workers wake on deliveries only. A deploy starts the app as
  usual. `tiffin projects wake shop` (or Wake in the dashboard) starts them ahead of
  visitors.
- **Cold start:** well under a second for small apps: about 0.3 s for a small Bun app and
  0.5 s for a Hello World Next.js app, from the request arriving to its first byte (a
  2-CPU box). A larger app takes as long as it needs to start and pass its health check.
  `tiffin apps status <project> <app>` shows `lastWake` with its timing, `sleepingSince`
  and `lastActive`.
- **What counts as use:** every request to the app's addresses, including the files the
  box serves for it, and every delivery. A request or a job still under way keeps the
  app awake, however long it runs. The clock is kept across restarts of the box.
- **What stops:** anything the app does on its own between requests (timers,
  `setInterval`, in-memory caches) stops while it sleeps. Put recurring work in a cron:
  it wakes the app, so a cron that runs every hour keeps an app with `sleepAfter: "24h"`
  awake.
- Previews sleep after 15 idle minutes whatever this says.

## What your app gets

`PORT`, `NODE_ENV`, `TIFFIN_URL` (its public URL), plus each service's variables:
`DATABASE_URL`, `REDIS_URL`, `S3_*`, `SMTP_URL`, `TIFFIN_AUTH_URL`, `SENTRY_DSN`,
`OTEL_*`, `TIFFIN_QUEUE_*` and your secrets (`tiffin secrets set`). Changing env or
secrets restarts the app with the new values.

**At build time** the app gets the same env as its instances, as on Vercel, so
`generateStaticParams`, prerendered pages and build scripts can query the database: a
preview's build reads its own branch. Two differences: `DATABASE_URL` (and `PG*`) connect
as the project's read-only role (`p_<project>__read`: reads every table, writes nothing,
not even with `SET default_transaction_read_only = off`) and `REDIS_URL` as a read-only
Valkey user, unless the app sets its own values. The values reach build steps as BuildKit
secrets: they are in no image layer, no build plan and no log, and only `NEXT_PUBLIC_*`
(and the other browser variables) are built into client code. The trade-offs:

- A build reads live data. A page prerendered from it shows the data of build time until
  it revalidates, and a build fails if its queries fail.
- The build runs before `release`, so it sees the schema before this deploy's
  migrations: on a first deploy there are no tables yet. Prerender code that a new
  migration feeds should cope with that (fall back, or render the page on demand).
- Static sites built with Bun (no `package-lock`, `pnpm-lock` or `yarn.lock`) get the
  plain env and browser variables only.

Variables that frameworks build into browser code (`NEXT_PUBLIC_*`, `VITE_*`,
`PUBLIC_*`) are public by definition: builds get them from env and secrets alike, and
changing one rebuilds the app from its live version's source instead of restarting it
(the plan says so; the deploy's `trigger` is `env`). A version deployed with
`--prebuilt` has no source to rebuild: it restarts and its browser code keeps the old
value until the next deploy. Next.js apps also get `NEXT_PUBLIC_TIFFIN_URL` (the app's
URL, a preview's own) and `NEXT_PUBLIC_SENTRY_DSN` (the box's error ingest for browsers),
unless they set them. Setting, copying or deleting a secret is a
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
  so the instances of an app share one cache and `revalidatePath`, `revalidateTag` and
  `updateTag` reach all of them (`revalidateTag(tag, "max")` serves the old page once
  while it regenerates, as in Next.js). Cached pages belong to their deploy: a new
  release renders afresh and a rollback finds its old ones. Production and each preview
  have their own cache and revalidations. It takes effect on the next deploy after adding
  Valkey. Pages and route handlers `next build` prerendered are served from the build's
  files until the cache has a newer copy, as with Next.js's own cache: the first request
  after a deploy is not a render, and an ISR page's age counts from the build.
- `compress: false`: the edge compresses.
- `poweredByHeader: false` (no `X-Powered-By`; add it with `headers()` if you want it).
- `images.maximumDiskCacheSize`: 512 MB. Optimized images live in a directory per app
  environment, shared by its instances and kept across deploys (deleted with the
  preview or app).

Without the adapter's help:

- **next/image and buckets.** `/_next/image` requests for files in the project's own
  buckets (`TIFFIN_FILES_URL/...`, public or signed) are answered by the box's image
  transforms (WebP when the browser takes it), not by the app: Next.js could not fetch
  them (they resolve to the box itself, an address it refuses) and the app keeps the
  memory sharp would use. Images in `public/` and from elsewhere go to Next.js as usual.
- **Social images.** The box sets `VERCEL_PROJECT_PRODUCTION_URL` to the app's host (a
  preview also gets `VERCEL_ENV=preview` and its own host in `VERCEL_BRANCH_URL`), which
  is what Next.js resolves `opengraph-image`, `twitter-image` and relative image metadata
  against when the app sets no `metadataBase`; without it they point at
  `http://localhost:<port>`. `VERCEL` and `VERCEL_URL` stay unset, since libraries take
  them to mean the app runs on Vercel. Values the app sets win.
- **Start command.** With no start script, or one that only runs `next start` (any of
  `next start`, `bun --bun next start`, `bunx next start`, with `-p $PORT` and such), the
  box starts Next.js on Bun itself, as one process
  (`exec bun --bun ./node_modules/next/dist/bin/next start`; `bun next` or `bun run start`
  would put a Bun process in front of it), so `SIGTERM` reaches Next.js: it finishes
  requests and `after()` work before it exits. A start command of your own (`command`)
  is started with `exec` too when it is a plain command.
- **Memory.** An instance of a small Next.js app on Bun settles around 270 MB RSS under
  load (`memoryMB: 512` leaves room). Bun ignores `NODE_OPTIONS`' heap size; its own knobs
  (`--smol`, `BUN_JSC_forceRAMSize`) made no measurable difference, so the box sets none.
- **Client files** under `/_next/static` are served by the box from disk, compressed
  ahead of time (zstd and gzip, best levels), and count toward a separate per-IP limit
  ten times the app's ([Protection](protection.md)).

The app also gets `NEXT_SERVER_ACTIONS_ENCRYPTION_KEY`, made once per app and used at
build and run time, so Server Actions in a page from the previous release still work
after a deploy. Previews share it; a duplicated or imported project gets its own. Set
the variable (or `NEXT_ADAPTER_PATH`) yourself to use your own. Older Next.js versions
ignore the adapter and build as before.

Apps that use Vercel's Workflow DevKit (`workflow`) run unchanged on the project's
Postgres: see [Already using Vercel Workflow?](queues.md#already-using-vercel-workflow).

`templates/hello-next` is an example with two instances and Valkey.

Templates: `templates/hello-hono`, `hello-next`, `static-site`, `queues-worker`.
