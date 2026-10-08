# Apps and deploys

An app is one deployable unit in `tiffin.config.ts`:

```ts
apps: {
  web:    { framework: "next", path: "apps/web" },
  api:    { framework: "hono", path: "apps/api", routes: ["web/api"], instances: 2 },
  worker: { framework: "bun",  path: "apps/worker", role: "worker" },
  site:   { framework: "static", path: "site" },
  py:     { framework: "fastapi", path: "apps/py", release: "alembic upgrade head" },
}
```

`next`, `hono`, `fastapi` and any server listening on `$PORT` run as containers; `static`
sites are served straight from the edge. Python apps (`fastapi`, and `python` for Flask,
Django and others) build with Railpack's Python provider: see
[FastAPI and Python](#fastapi-and-python).

JavaScript apps build and run on Bun: `next build` and `next start`, or your `build` and `start`
scripts, run under `bun --bun`, so tools that ask for Node.js run on Bun too. It starts
faster and uses less memory. For an app that needs Node.js (a native module built for it, a
library that leans on Node internals), set `runtime: "node"` (or pick Node.js under the
app's Runtime in the dashboard): it then builds and runs on Node.js, from the next deploy. A
build or start that fails on Bun says so, and the version that was serving keeps serving.

The box pins both runtimes: Bun to the release it ships with, and Node.js to major 24
(Railpack's own default, "lts", would move to a new major on its own). An app that picks
its own version keeps it: `engines` or `packageManager` in `package.json`, `.nvmrc`,
`.node-version`, `.bun-version`, `mise.toml` or `.tool-versions`.

After a Next.js, SvelteKit, Nuxt or React Router app passes its health check, the box also
asks it for `/` and for a page that doesn't exist; a 5xx on either stops the deploy before
it takes traffic.

A static build whose `package.json` uses a client-side router (react-router, vue-router,
TanStack Router, wouter…) serves `index.html` for paths without a file, so a refresh on
`/about` works; `index_fallback: false` in a Staticfile turns that off.

SvelteKit, Nuxt and React Router (framework mode) are started by the box itself, with the
settings their servers need behind its proxy: see [SvelteKit](#sveltekit), [Nuxt](#nuxt)
and [React Router](#react-router). TanStack Start runs as a server on Bun: its `start` script (Nitro's
`node .output/server/index.mjs`), or Railpack's default when there is none. Astro with
`@astrojs/node` (standalone) runs its server the same way; without a `start` script the box
starts `dist/server/entry.mjs`. Which frameworks are first-class, which are only
detected and which aren't supported yet: [What works and what doesn't](limits.md#frameworks).
Any of them runs from its own Dockerfile (`builder: "dockerfile"`, see
[Build settings](#build-settings)).

The edge compresses text responses (zstd or gzip) for every app; a response the app
compressed itself is passed through. A static site's pages and files are revalidated on
every visit, except fingerprinted build assets (`/assets/index-B1x9Qa2c.js`,
`/_next/static/…`), which browsers keep for a year. A static site's text files are
compressed once, when it deploys (zstd and gzip at their highest levels), and those copies
are sent instead of compressing on the fly; `.br` files the site ships are sent too. Vite's
`.vite/` folder (its build manifest) is left out. A static site answers `/about` from
`about.html`, and a folder from its `index.html` (asked for without its slash, it redirects
there); a path with no file gets the site's `404.html` with status 404 when it has one.

A deploy of a static site keeps the previous release's hashed files (a Vite SPA's
`/assets/lazy-C2y8Rb3d.js`, Astro's `/_astro/…`) for a day after that release stopped
being live, so a tab opened before the deploy still loads its lazy chunks. Missing hashed
files are a 404, never `index.html`, so Vite's `vite:preloadError` event fires; a page can
reload on it:

```js
window.addEventListener("vite:preloadError", () => window.location.reload());
```

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

- **Rollback:** `tiffin rollback <app> [deploy]`, to one of the last 20 production deploys
  before the live one, each of which you can also open at its own address first (see
  [Every version's own address](#every-versions-own-address)). Older builds are cleaned
  up; their records stay listed.
- **Disk:** images nothing needs any more go at once or in the hourly sweep. BuildKit's
  cache may hold 15% of the data disk (at least 4 GiB, at most 20 GiB); the sweep trims
  it back to that even when nothing builds.
- **Build caches:** each app keeps its own. Railpack builds (server apps, and static
  sites built with npm, pnpm or yarn) keep the package manager's store and the
  framework's caches (`.next/cache`, Astro's, Vite's, `node_modules/.cache`) as BuildKit
  cache mounts. Static sites built with Bun keep Bun's package cache and
  `node_modules/.astro` (optimized images, content layer, fonts), `node_modules/.vite`,
  `node_modules/.cache` and `.next/cache` in a folder of the box's, started afresh once
  it passes 2 GiB. The build log says whether the cache was warm. Previews share their
  app's caches.
- **History:** `tiffin deploys list <project> <app>` for one app;
  `tiffin projects deploys <project>` for every app, previews included (filter with
  `--app`, `--env`, `--preview`, `--status`, `--branch`), each with its own address. Both
  return the newest 50 (`--limit`, at most 200); when there are more, the answer's
  `nextCursor` goes in `--cursor` for the next page, with the same filters. Pages are
  newest first by creation time, then ID, so a deploy made while you page never shows up
  twice or pushes one out.
- **Logs:** `tiffin logs <app> -f`.
- **Previews** sleep when idle and wake on the first request. Preview names may not start
  with `d-`, which is how [each version's own address](#every-versions-own-address)
  starts. Each preview gets its own
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
- **Client assets:** for Next.js, TanStack Start, SvelteKit, Nuxt, React Router and Astro
  (`@astrojs/node`) apps, the box
  copies the build's browser files (JS, CSS, images) out of the image and serves them
  itself: hashed files with a year-long immutable cache, others with revalidation. Hashed files of the
  previous releases stay served for a day, so a page loaded before a deploy keeps
  finding its chunks. The box serves these files without running the app's middleware.
  For another framework, name the directory: `assets: { dir: "dist/client", path: "/" }`
  (path defaults to `/`; files there are kept for old pages too, revalidated).
- **Prerendered pages:** for Astro (`@astrojs/node`), SvelteKit (`build/prerendered`),
  Nuxt, React Router and TanStack Start (`.output/public` or `dist/client`), the box also
  answers the pages the build prerendered, from the live release: `/about` from
  `about/index.html` or `about.html`, `/` from `index.html`, revalidated on every visit
  (ETag). Those frameworks' own servers answer these files before any app code runs, so
  nothing changes but speed: a page costs the app nothing and a sleeping app doesn't wake
  for it. Only exact files count; everything else goes to the app. Not Next.js: its
  `proxy.ts` runs before prerendered pages. See [the limits](limits.md#caching-and-images).
- **Shutdown:** a replaced release finishes the requests it has (each within the app's
  time limit, `timeoutSeconds`, so a long render survives a deploy), gets SIGTERM once
  they are done, then 30 seconds before it is killed, for work it does after responding.

### Every version's own address

Like Vercel's deployment URLs, every production deploy of a web app has an address of its
own, `d-<id>--<name>.<apps domain>`: the last 8 characters of its deploy ID, in lower
case, then the app's name under the apps domain as previews use it (`shop`, or
`<project>-<app>` for an app served on a path or its own domain), for example
`https://d-9j0kmnpq--shop.example.app`. It is the deploy's `url`; `appUrl` is the app's
own address. Workers have none.

- **The live version's address** is production: the same containers, nothing extra runs.
- **An earlier version** (one the box keeps to roll back to) runs only while someone
  visits it. Its first request starts one instance of its image and waits for it, as a
  sleeping preview does; it sleeps again after 5 minutes without requests. At most 2
  earlier versions of a project run at once: opening a third puts the one used longest
  ago to sleep. They run in the project's share of the box, so their memory counts
  against its limits.
- **Read-only.** An earlier version reads production's data but can't change it:
  `DATABASE_URL` (and `DIRECT_DATABASE_URL`, `PG*`) use the project's read-only login
  (`p_<project>__read`), so **writes to the database fail** (Postgres says it can't run
  them in a read-only transaction), and the KV credentials (`REDIS_URL` and the REST
  tokens) use the read-only KV user. `TIFFIN_READ_ONLY=1` tells the app. Its email goes to the dev inbox;
  no crons, queue deliveries, workflow steps or workers reach it; sign-in pages don't
  work there. Its disk folders are its own, made from its image, and go when the version
  is cleaned up. Everything else (env, secrets, files) is production's: see
  [the limits](limits.md#deploys-and-changes) for what is not read-only.
- **Who can open them.** By default only people signed in to this box's dashboard. A
  visitor without the box's cookie for that address goes to the dashboard (`/gate`),
  signs in if needed, and comes back. The dashboard's own session cookie belongs to the
  dashboard's host and never reaches an app's (the apps domain is often another domain
  altogether), so the dashboard instead asks the box for a link that works once, within a
  minute, for that one address; following it sets a cookie for that address alone
  (`__Host-`, HTTP-only, an hour), which the box strips before the request reaches the
  app. Anyone who can read the project may get such a link: agents and scripts with
  `tiffin deploys link <url>`, for a headless browser, say. To let anyone with the
  address in, set `deployAddresses: "public"` in `tiffin.config.ts` or **Settings ›
  General › Version addresses › Public**. Either way the addresses say `noindex` to
  search engines.
- **How many.** The last 20 production deploys before the live one stay, built and ready
  to roll back to or open (a sliding window: each new deploy pushes the oldest out). Once
  the data disk passes the disk guard's warning level (85% by default), apps keep only
  their last 3, and the guard cleans up the rest at once. An address whose version was
  cleaned up answers with a short page that links to the live site, as long as the box
  keeps its record (the last 50 per app); the record says `"retention": "cleaned"`.
- **What each one costs.** A kept version holds its image's own layers (once compressed,
  once unpacked) and its source archive; layers it shares with other versions (the base
  image, and the dependencies while the lockfile stays the same) are stored once.
  Measured for the Next.js starter (`next build`, without `.next/cache`, which stays in the
  build cache): about 6 MB of build output, 1.5 MB compressed, and a 10 KB source archive,
  so about 8 MB a version, or 160 MB for all 20. A version that changed dependencies
  also carries its own `node_modules` layer: about 320 MB unpacked and 82 MB compressed for
  that starter (10 MB unpacked for the Hono starter). An earlier version that was woken
  adds its container's writable layer and its own disk folders until it is cleaned up.
  These are estimates from a build of the starters, not a box's image store; past the
  disk guard's warning level the window shrinks to 3 by itself.
- **Certificates.** On a box with a wildcard certificate (a connected DNS provider; the
  hosted apps domain has one), the addresses are covered already. Without one, each
  address gets its own certificate on its first visit, as previews' do.

## Build settings

Detection picks how an app builds. When it guesses wrong, override it, in the app's
**Settings › Build and deploy** in the dashboard or in `tiffin.config.ts`. Every field
is optional and applies from the next deploy:

```ts
apps: {
  web: {
    framework: "next",
    git: { repo: "acme/mono", path: "apps/web" },   // root directory
    install: "pnpm install --frozen-lockfile",       // at the top of the workspace
    build: "pnpm --filter web build",                // in the app's folder
    command: "node .next/standalone/server.js",      // the start command
    healthcheck: "/api/health",
    release: "pnpm db:migrate",
    watch: ["apps/web/**", "packages/ui/**", "!**/*.md"],
  },
  api:  { builder: "dockerfile", dockerfile: "docker/api.Dockerfile", target: "runner" },
  docs: { framework: "static", build: "bun run docs:build", output: "site" },
}
```

| Field | Default | |
|---|---|---|
| `install` | `bun install`, or the lockfile's package manager | Wins over vercel.json's `installCommand` |
| `build` | package.json `build` | Wins over vercel.json's `buildCommand` |
| `command` | package.json `start` (Next.js: `next start`) | For a Dockerfile or prebuilt image, replaces its `CMD` |
| `output` | the first of `dist`, `build`, `out`, `public` with an `index.html` | Static sites and Next.js static exports |
| `builder` | `"auto"` | `"dockerfile"`, `"static"` (same as framework `static`) or `"prebuilt"` |
| `dockerfile`, `target` | `Dockerfile`, its last stage | Builder `"dockerfile"` only |
| `watch` | every push deploys | Patterns relative to the top of the repository |

- **Dockerfile.** `builder: "dockerfile"` builds the app's Dockerfile with BuildKit (the
  same build slot, memory and CPU limits as other builds), with the app's folder as the
  context (a workspace app: the workspace's top). The image runs like any other app:
  health checks, zero-downtime switches, logs, `release`, rollbacks and image cleanup
  are the same. It must listen on `$PORT`, which the box sets for each instance; its
  `EXPOSE` is not used. A folder with a `Dockerfile` and no `package.json` or Python
  project builds with it automatically (the build log says so). The app's plain env and
  its browser env (`NEXT_PUBLIC_*`, `VITE_*`, ...) reach the build as build args, for the
  `ARG`s the Dockerfile declares; secrets and service URLs only as BuildKit secrets
  (`RUN --mount=type=secret,id=DATABASE_URL,env=DATABASE_URL bun run build`), never in a
  layer. Builds can't mount anything outside the context, use the host's network
  (`RUN --network=host`) or run privileged steps. `install`, `build`, `runtime` and
  `packages` belong in the Dockerfile and are refused with it.
- **Prebuilt.** `builder: "prebuilt"` takes only `tiffin deploy --prebuilt image.tar`; a
  source deploy says so and fails, and it can't be combined with `git`.
- **Watch paths.** With `watch`, a GitHub push to the production branch or a pull request
  deploys the app only when it changes a file one of the patterns matches: `*` within a
  folder, `**` across folders, a pattern without a slash at any depth (`*.md`), a folder
  name for everything in it, and a leading `!` to exclude; the last pattern that matches
  a file decides. A new branch, or a comparison GitHub can't list fully (over 300 files),
  deploys. Redeploys, `tiffin deploy` and pushes to the box always build. The GitHub
  deliveries list says which apps a push skipped. A pull request is compared with its base:
  when an update undoes its change to the app, the app's preview is removed (its comment
  says so) rather than left serving the earlier commit.

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
at a time. Static apps have none. Deploys of an app go live in the order they were made:
one that finishes after a newer one went live is *skipped*, before its release runs.

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
  build log says "Next.js static export"; the deploy's framework is `next+export`. No
  image is made: BuildKit hands over `out/` alone, so nothing is compressed into layers
  and unpacked. On the 2-CPU box, exporting and unpacking the image took 33 s of the
  website's 92 s build; the same build without them took 28 s.
- **Monorepos.** An app in a JavaScript workspace goes up with the whole workspace, as on
  Vercel: the nearest folder above it with a `pnpm-workspace.yaml` or a `package.json`
  `workspaces` field (inside its repository), when that lists the app's folder among its
  packages or the app uses a workspace package (`"@acme/ui": "workspace:*"`). Dependencies
  install at the top with the workspace's package manager (by its lockfile), only for the
  app, the workspace packages it uses and the root, falling back to the whole workspace
  (see [Monorepos](limits.md#monorepos)); then the app builds and starts in its own folder
  (the deploy's `dir`, e.g. `apps/web`). `tiffin deploy`, git pushes and GitHub deploys all
  do this. Any other app goes up alone.
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
tiffin deploys template shop site --template astro
tiffin deploys git shop web --git-url https://github.com/owner/repo --ref main --path apps/web
```

Starters ship in the binary, grouped by what you make (`kind`), with a framework
(`preset`) inside each and one default per kind:

| Kind | Framework (id) | What it is |
|---|---|---|
| Web app (`web`) | **Next.js** (`nextjs`) | App Router on Bun; a server component reads Postgres, a server action writes it |
| | TanStack Start (`tanstack-start`) | A loader and server functions on Postgres, streamed stats, a prerendered `/about` |
| | SvelteKit (`sveltekit`) | SvelteKit 3 with adapter-bun: a server `load` on Postgres, a form action, streamed stats, a prerendered `/about` |
| | React Router (`react-router`) | React Router 8 framework mode: a loader and an action on Postgres, streamed stats, a prerendered `/about` |
| | Nuxt (`nuxt`) | Nuxt 4 on Bun: a page and server routes on Postgres, a form that works without JavaScript, a prerendered `/about` |
| Static site (`static`) | **Astro** (`astro`) | Plain HTML, the image service and a self-hosted font; no JavaScript unless a page asks |
| | Vite + React (`vite-react`) | A single-page app built to hashed, code-split files |
| API (`api`) | **Hono** (`hono`) | A JSON API with a Postgres table it creates on boot |

Two more aren't offered when starting a project but deploy by id: `static-site` (plain
HTML, no build) and `guestbook` (page + API + Postgres + Valkey + analytics in one Hono
app, a demo). Each starter lists the manifest fragment it needs: add that to the project (see
[Concepts](concepts.md#changes)), apply, then deploy the template. The `fastapi` starter
is an API in Python: see [FastAPI and Python](#fastapi-and-python).

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

**2. Import a repository.** New project › **Import from GitHub**: search your repositories,
pick one, then its production branch and folder (monorepos list
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

- the commit gets a status `tiffin/<project>/<app>` (a preview's: `tiffin/<project>/<app>/preview`):
  *pending* while it builds, then *success* (linking the live address) or *failure* (linking the
  build log);
- a GitHub deployment per deploy (`tiffin/<project>/<app>`, previews as transient
  environments `…/pr-12`);
- a pull request's preview lives at `pr-12--<app address>.<domain>` (`pr-12--shop` for the app at
  `shop`; `--` in a route's first label is kept for previews, so no app can take one); one comment says
  "Preview of `web`: https://pr-12--shop.example.com · built in 34 s · logs".
  Closing the pull request removes the preview, also one still building: it never goes live.

Rapid pushes to one branch coalesce: while one builds, only the newest waiting push is
built next (the ones in between show as *skipped*). A push is checked against its branch
on GitHub before it deploys: one that arrives after the branch moved on (a late or replayed
delivery) is skipped, so an older commit never replaces a newer one. A commit message with `[skip deploy]`
or `[skip ci]` deploys nothing. On the app's page: the connected repository and branch,
each version's commit (message, author, SHA), **Redeploy** and **Disconnect repo**.

**Security.** Deliveries must carry a valid `X-Hub-Signature-256` (HMAC-SHA256 with the
app's webhook secret, compared in constant time); a delivery is handled once (by its
signed content, so a replay under a new delivery id counts too; one that failed on the box's
side can be redelivered), and events older than an hour are refused. Each clone uses a fresh token that can only read
that one repository, handed to git through its environment (never a file or the command
line) and revoked as soon as the clone is done. **Pull requests from forks are not built**
unless the app says `previews: "forks"`: a fork's code would run on your box with the
project's env and secrets.

**From the terminal or an agent** (every step is an API operation, so also an MCP tool):

```bash
tiffin github status                       # connected? installed where? webhook and recent deliveries
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

The app's webhook must be active and point at `<box>/v1/github/webhook`; `tiffin github status`
(and Settings › Git) reads it from GitHub, says when it points somewhere else, and lists
GitHub's latest deliveries with the box's replies. A **public** app is installed by
other accounts too, so the box only acts for installations made from it: enable *Request
user authorization (OAuth) during installation* on the app with `<box>/v1/github/setup` as
a callback URL; after an install the box asks GitHub, with that person's sign-in, which of the
installation's repositories they can push to, and acts on those only (repositories added to the
installation later need another Install on repositories from the box). The same file takes `"apiUrl"` and `"webUrl"` for GitHub
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
them as using none). The stopped containers are kept, so a wake starts them again rather
than creating new ones. Their images, data, routes, env and secrets stay. In the dashboard,
the project's Settings › When nobody visits offers Never (the default), 24 hours, 7 days
or 14 days, and the project says "Asleep since …" with a Wake button.

- **Waking:** the next request is held while the app starts and passes its health check,
  then answered; if it cannot start, the visitor gets `503` with `Retry-After`. A job,
  cron tick or workflow turn for a sleeping app wakes it first and is then delivered, so
  no attempt is spent on it. Workers wake on deliveries only. A deploy starts the app as
  usual. `tiffin projects wake shop` (or Wake in the dashboard) starts them ahead of
  visitors.
- **Cold start:** measured on a 2-CPU box, from the request arriving to its first byte:
  about 0.45 s for the Hono starter, 0.87 s for the Next.js starter and 1.7 s for the FastAPI
  starter ([the numbers](limits.md#sleep-and-wake)). A larger app takes as long as it needs
  to start and pass its health check.
  `tiffin apps status <project> <app>` shows `lastWake` with its timing, `sleepingSince`
  and `lastActive`.
- **What counts as use:** every request to the app's addresses, including the files and
  prerendered pages the box serves for it (which never wake it), and every delivery. A request or a job still under way keeps the
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
not even with `SET default_transaction_read_only = off`, except through the app's own
`SECURITY DEFINER` functions that anyone may run) and `REDIS_URL` as a read-only
Valkey user, unless the app sets its own values. The values reach build steps as BuildKit
secrets: Tiffin puts them in no image layer, build plan or log, and only `NEXT_PUBLIC_*`
(and the other browser variables) are built into client code. Build code can still
leak one: a script that prints a secret puts it in the build log (only Tiffin's own
credentials are masked there), and one that writes it to a file can bake it into the image. The trade-offs:

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
- With the project's KV (every project has it): `cacheHandler` and
  `cacheHandlers` (`default`, `remote`) from `@shiptiffin/sdk/next`, and `cacheMaxMemorySize: 0`,
  so the instances of an app share one cache and `revalidatePath`, `revalidateTag` and
  `updateTag` reach all of them (`revalidateTag(tag, "max")` serves the old page once
  while it regenerates, as in Next.js). Cached pages belong to their deploy: a new
  release renders afresh and a rollback finds its old ones. Production and each preview
  have their own cache and revalidations. It takes effect on the next deploy after adding
  Valkey. Pages and route handlers `next build` prerendered are served from the build's
  files until the cache has a newer copy, as with Next.js's own cache: the first request
  after a deploy is not a render, and an ISR page's age counts from the build. A build
  file older than 30 days (the longest a cache entry lives, and so how long the box keeps
  a tag's revalidations) is rendered anew instead, so a revalidation from long ago can't
  bring it back.
- `compress: false`: the edge compresses.
- `poweredByHeader: false` (no `X-Powered-By`; add it with `headers()` if you want it).
- `supportsImmutableAssets: true` (Next.js 16.3+, Turbopack builds): chunks are served
  from `/_next/static/immutable/` without `?dpl=<deploy>`, so a chunk a deploy leaves
  unchanged stays in returning visitors' browser caches. Set it to `false` to opt out.
- `images.maximumDiskCacheSize`: 512 MB. Optimized images live in a directory per app
  environment, shared by its instances and kept across deploys (deleted with the
  preview or app). They stay on disk, never in Valkey, also when the app sets
  `images.customCacheHandler`. Each instance enforces the size from its own view of the
  directory, refreshed at most a minute old, so instances together can overshoot it by
  what they write in that minute.

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

## SvelteKit

SvelteKit 2 and 3 run as a server, picked by the adapter the app's config imports
(`vite.config` in SvelteKit 3, `svelte.config.js` in 2: the file that names an adapter
wins), or the one `package.json` lists:

| Adapter | What the box does |
|---|---|
| `@sveltejs/adapter-bun` (SvelteKit 3, Bun 1.4+) | `exec bun ./build/index.js`: one `Bun.serve` process. The default to use. |
| `@sveltejs/adapter-node` | `exec bun ./build/index.js` (`node` with `runtime: "node"`) |
| `@sveltejs/adapter-auto` | The build sets `GCP_BUILDPACKS`, so adapter-auto installs adapter-node and uses it; the deploy carries a warning pointing at adapter-bun |
| `@sveltejs/adapter-static` | Built to files and served by the edge; with a `fallback` page, paths without a file serve it (and `/` too when nothing is prerendered, so there is no `index.html`) |
| Any other (Vercel, Netlify, Cloudflare) | The build stops and says to switch |

The adapter's `out` folder is read from the config (default `build`). A `start` script that
only starts that build (`bun ./build`, `node build`) is replaced by the same command run
with `exec`; any other start script (a custom server) runs as written.

The image's env gets what the server reads behind the box's proxy (the app's own env wins):
`PROTOCOL_HEADER=x-forwarded-proto`, `HOST_HEADER=x-forwarded-host`,
`ADDRESS_HEADER=x-forwarded-for` and `XFF_DEPTH=1`, without which SvelteKit's origin check
refuses every form action with a 403; `BODY_SIZE_LIMIT=Infinity` (SvelteKit's own 512 KB
refuses ordinary uploads); `SHUTDOWN_TIMEOUT=25`; and `CONNECTION_IDLE_TIMEOUT=0`
(adapter-bun) or `KEEP_ALIVE_TIMEOUT=65` (adapter-node), so the switchboard's kept
connections are not closed under it. Files under `/_app/immutable/` are served by the box
for a year; `_app/version.json` is revalidated. Prerendered pages come from
`build/prerendered`.

Measured on the live box (2 vCPU x86, Hetzner cx23) with the `sveltekit` starter (a server
`load` with two Postgres queries, 32 connections for 20 s): 2,300 requests a second at
22 ms p95, 38 MB RSS idle and 75 MB after the load; a cold start to a healthy answer in
0.6 to 0.7 s; a build in 36 s; a rollback in 2 s. SvelteKit's adapter-node output on Node.js
used about three times the memory of Bun in the research run (249 against 84 MB after 2,000
requests).

## Nuxt

Nuxt 4 (and 3) builds with Nitro's `node-server` preset, which the box pins
(`NITRO_PRESET=node-server` at build; a `nitro.preset` in `nuxt.config` wins), and starts
`exec bun .output/server/index.mjs` (`node` with `runtime: "node"`). Never Nitro 2's `bun`
preset: it buffers request bodies and has no graceful shutdown. A `start` script that only
starts that output (`node .output/server/index.mjs`, `nuxt start`) is replaced by the same
command with `exec`.

- `NUXT_APP_SECRET` (sessions, `deriveSecret`) is made once per app and kept, sealed, the
  same in every build, instance and preview; set it yourself to use your own.
- `NITRO_SHUTDOWN_TIMEOUT=25000`, inside the box's 30 seconds.
- `/_nuxt/` and `/_fonts/` are served by the box for a year, except
  `/_nuxt/builds/latest.json`, which the app polls for new versions and is revalidated.
- A build that runs `nuxt generate` makes a static site (`.output/public`), served by the
  edge; with `ssr: false` in `nuxt.config`, paths without a file serve `200.html`. The build
  that counts is the one that runs: the app's Build command, else vercel.json's
  `buildCommand`, else the `build` script (`npm run generate` reads the `generate` script).

**Bun or Node.js:** measured on the live box with the `nuxt` starter (its home page renders
on the server and fetches its API route, two Postgres queries), 32 connections, three
30-second rounds back to back:

| Runtime | Requests/s | p95 | RSS idle | RSS after each round | Cold start |
|---|---|---|---|---|---|
| Bun 1.4.2 | 1,030 | 46 ms | 63 MB | 141, 140, 142 MB | 0.75 s |
| Node.js 24 | 610 | 82 ms | 83 MB | 187, 186, 187 MB | 0.98 s |

Bun was faster and leaner, and its memory did not grow across rounds, so Nuxt runs on Bun
unless the app sets `runtime: "node"`. A build of the starter takes about 80 s; a rollback
under 3 s.

## React Router

React Router 7 and 8 in framework mode (`@react-router/dev`) run as a server. On Bun the
box doesn't use `react-router-serve` (Express with compression, about 340 requests a second
on Bun): it writes its own server into the build (`.tiffin/react-router/serve.js`) and starts
`exec bun /app/.tiffin/react-router/serve.js ./build/server/index.js`, which is
`Bun.serve` with React Router's own request handler. It:

- serves the build's client files itself (`/assets/` for a year), and prerendered pages at
  `/about` and `/about/`;
- gives React Router the URL the browser used (`https`, from the edge's
  `X-Forwarded-Proto` and `X-Forwarded-Host`), which its action origin check compares
  with `Origin`;
- drains requests in flight on SIGTERM, for up to `SHUTDOWN_TIMEOUT` seconds (25).

A `start` script of `react-router-serve <build>` names the server build to start; any other
start script runs as written. With `runtime: "node"`, the box starts `react-router-serve` on
Node.js when the app depends on `@react-router/serve`. `buildDirectory` in
`react-router.config` is read (default `build`). With `ssr: false` the app is a static site:
`build/client` is served by the edge, and paths without a file serve `index.html`, or
`__spa-fallback.html` when the home page is prerendered. A deploy of `react-router` before
8.4.0 (7.18.4 on v7) carries a warning: those leak memory while streaming.

Measured on the live box with the `react-router` starter (a loader with two Postgres
queries, 32 connections for 20 s): 1,350 requests a second at 36 ms p95, 51 MB RSS idle
and 95 MB after the load; a cold start in 0.7 to 0.8 s; a build in 28 s; a rollback in 2 s.

## FastAPI and Python

```ts
apps: {
  api:   { framework: "fastapi", healthcheck: "/healthz", release: "alembic upgrade head" },
  admin: { framework: "python", path: "admin", command: "gunicorn --bind 0.0.0.0:$PORT shop.wsgi",
           release: "python manage.py migrate" },
}
```

`fastapi` is a FastAPI app; `python` is any other Python server that listens on `$PORT`
(Flask, Django, Litestar...). Both build with Railpack's Python provider, and importing a
repository finds them by `fastapi` (or Flask, Django...) in `pyproject.toml`,
`requirements*.txt`, `Pipfile` or `uv.lock`, in any folder of a monorepo.

- **Install.** By the lockfile: `uv.lock` (`uv sync --locked --no-dev`: a lockfile out of
  date with `pyproject.toml` fails the build, and dev groups are left out), `poetry.lock`,
  `pdm.lock` or `Pipfile`; a `requirements.txt` wins over all of them (pip). Debian
  programs go in `packages`, as for any app. `.venv`, `__pycache__` and tool caches are
  never uploaded.
- **Python version.** `.python-version` (what `uv python pin` writes), `.tool-versions`,
  `mise.toml` or `runtime.txt`; without one, when Railpack's default (3.13) does not meet
  `requires-python` in `pyproject.toml` (upper bounds and exclusions count), the newest of
  3.9 to 3.14 that does; `RAILPACK_PYTHON_VERSION`
  in the app's env wins. Python 3.14 is current, and FastAPI, Pydantic, uvloop, psycopg and
  asyncpg ship wheels for it. `runtime` is for JavaScript apps only.
- **Start.** The box starts a `fastapi` app as one Uvicorn process:

  ```bash
  uvicorn app.main:app --host 0.0.0.0 --port $PORT --proxy-headers --forwarded-allow-ips 127.0.0.1 \
    --timeout-keep-alive 75 --timeout-graceful-shutdown 25
  ```

  It finds the app as `fastapi run` does: `[tool.fastapi] entrypoint = "app.main:app"` in
  `pyproject.toml`, else `app = FastAPI()` in `main.py`, `app.py`, `api.py`, `app/main.py`,
  `app/app.py` or `app/api.py` (the build log says which). Uvicorn must be a dependency
  (`uvicorn[standard]`, or `fastapi[standard]`); the build log warns when it isn't.
  - One process, no `--workers`: add copies with `instances`. Each copy has its own health
    check and memory, and a deploy replaces them without dropping requests.
  - The edge connects from 127.0.0.1 and sets `X-Forwarded-For` and `X-Forwarded-Proto`,
    so `request.client.host` is the visitor and `request.url` and `url_for` are `https`.
  - Keep-alive 75 s: longer than the edge keeps an idle connection to the app (60 s), so
    the app never closes one the edge is about to reuse. With Uvicorn's default (5 s) a
    POST can get a `502` now and then.
  - Shutdown: `SIGTERM` comes once the requests in flight are done; background tasks get
    25 s, then the app's lifespan shutdown runs (close pools there), inside the box's 30 s.
  - `fastapi run` can set neither timeout, so the box does not use it. A `command` of your
    own replaces all of this (a factory needs one:
    `uvicorn app.main:create_app --factory --host 0.0.0.0 --port $PORT`); keep the
    keep-alive above 60 s.

  A `python` app starts with its `command`, or Railpack's guess: `gunicorn main:app` for
  Flask with gunicorn, `uvicorn main:app` for FastHTML, else `python main.py` (which must
  listen on `$PORT`). For Django, Railpack's guess runs `manage.py migrate` in every copy at
  every start; set `command` and put the migration in `release`, as above.
- **Logs.** Output is unbuffered. Lines that start with `ERROR:`, `WARNING:` or `CRITICAL:`
  (Uvicorn's format, and `logging`'s default) and exception lines (`ValueError: ...`,
  `psycopg.errors.UndefinedTable: ...`) are marked as errors or warnings; JSON lines with a
  `level` field are read as such. The root logger has no handler until the app adds one, so
  give the app's own logger a handler (the starter uses Uvicorn's formatter).
- **Traces.** FastAPI (0.142 and later) exports OpenTelemetry traces, metrics and logs by
  itself when `OTEL_EXPORTER_OTLP_ENDPOINT` is set, and the box sets it. With
  `fastapi[opentelemetry]` (or `fastapi[standard]`) installed, each request is a trace in
  Observability, under the edge's request ID. Without it, FastAPI prints one line at start
  saying so and carries on; `FastAPI(telemetry={"auto_configure": False})` turns it off.
- **Services** are env vars, so Python libraries take them as they are:
  - `DATABASE_URL` is a `postgresql://` URL through the pooler (PgBouncer in transaction
    mode, prepared statements tracked). For SQLAlchemy, change the scheme to
    `postgresql+psycopg://` (psycopg 3, SQLAlchemy 2.1's default driver) and size the pool
    with `DATABASE_POOL_MAX`. Migrations use `DIRECT_DATABASE_URL`. asyncpg also works, but
    SQLAlchemy hands the URL's `sslmode` to it as an argument it refuses: rename it to `ssl`.
  - `REDIS_URL` (`redis.asyncio.from_url`), `SMTP_URL` and `EMAIL_FROM`.
  - Buckets: `AWS_ENDPOINT_URL`, `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY`, which
    boto3 reads with no setup, plus `S3_BUCKET`.
  - Sign-in: see [Without the SDK](auth.md#without-the-sdk).
- **Memory.** The starter's one Uvicorn process settles around 100 to 115 MB RSS, idle or
  under load (Python 3.14 on a 2-CPU arm64 box; 700 requests a second of `GET /notes`).
  About 20 MB of that is OpenTelemetry export: without `fastapi[opentelemetry]` it is
  smaller, and requests stop showing as traces. `memoryMB: 256` leaves room.

**The starter** (`tiffin deploys template <project> api --template fastapi`) is a notes API:
FastAPI 0.142 on Python 3.14, typed Pydantic models, async SQLAlchemy 2.1 on psycopg 3, an
Alembic migration as its `release`, OpenAPI docs at `/docs`, a `/me` route that asks the
box's sign-in service who is signed in (with `services.auth`), and a pytest suite
(`uv run pytest`, with `DATABASE_URL` pointing at a scratch database). `tiffin pull` gives
you its source; `uv run uvicorn app.main:app --reload` runs it locally.

Templates: `templates/hello-hono`, `hello-next`, `static-site`, `queues-worker`.
