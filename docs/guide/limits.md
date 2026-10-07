# What works and what doesn't

This page is the one list of what Tiffin supports, what it supports only partly and what
it doesn't do yet. Other pages link here instead of repeating it. If something you need
is missing, it is missing on purpose for now, not by accident.

## Frameworks

Three levels:

- **First-class:** a starter in the dashboard (New project), and builds and start
  commands tuned for it.
- **Detected:** importing a repository recognises it and it runs as a server on `$PORT`
  (or as files). It works, but nothing is tuned for it beyond that.
- **Not yet:** importing one refuses it and says so. It can still run from your own
  Dockerfile (`builder: "dockerfile"`, see [Build settings](apps.md#build-settings)).

| Framework | Level | Notes |
|---|---|---|
| Next.js | First-class | 16.2 or later gets the box's adapter (shared cache, client assets served by the edge). Older versions build and run without it. |
| Hono (Bun) | First-class | |
| FastAPI | First-class | One Uvicorn process per instance. See [FastAPI and Python](apps.md#fastapi-and-python). |
| TanStack Start (React) | First-class | Runs its Nitro server on Bun; the edge serves its client assets. |
| SvelteKit 2 and 3 | First-class | adapter-bun, adapter-node or adapter-auto run as a server on Bun; adapter-static is built to files. See [SvelteKit](apps.md#sveltekit). |
| Nuxt 3 and 4 | First-class | Nitro's `node-server` output, on Bun; `nuxt generate` is built to files. See [Nuxt](apps.md#nuxt). |
| React Router 7 and 8 (framework mode) | First-class | The box's own Bun server; `ssr: false` is built to files. See [React Router](apps.md#react-router). |
| Astro, static | First-class | Built to files and served by the edge. |
| Astro with `@astrojs/node` | Detected | Runs `dist/server/entry.mjs` as a server. |
| Vite + React (SPA), plain HTML | First-class | Static site; client-side routers get an `index.html` fallback. |
| Any other Bun or Node.js server (Express, Elysia, Fastify...) | Detected | Must listen on `$PORT`. |
| Flask, Django, Litestar and other Python servers | Detected (`python`) | Started by your `command`, or Railpack's guess. |
| Go, Rust, anything else | Dockerfile only | |
| Remix 2, SolidStart, TanStack Start for Solid | Not yet | Planned. |
| Astro with the Vercel, Netlify or Cloudflare adapter | Not yet | Switch to `@astrojs/node`, or build it static. |

**Bun by default.** JavaScript apps build and run on Bun. Some things only work on Node.js;
set `runtime: "node"` for them:

- native modules built for Node.js, and libraries that lean on Node internals;
- React Router's own server (`react-router-serve`, on Express) is slow on Bun (about 340
  requests a second): the box starts its own Bun server instead, but a custom server or a
  Dockerfile that runs `react-router-serve` should run on Node.js.

**SvelteKit, Nuxt and React Router** (what the box sets up is in [Apps](apps.md#sveltekit)):

- SvelteKit with `adapter-auto` installs `adapter-node` during every build (it knows no
  box). Use `@sveltejs/adapter-bun` (SvelteKit 3) for the leaner server. `adapter-bun` needs
  Bun, so `runtime: "node"` with it stops the build. The Vercel, Netlify and Cloudflare
  adapters aren't supported.
- The adapter, `out` folder, `ssr: false`, `buildDirectory` and an adapter-static `fallback`
  are read from the config files as written: a value computed in code (an adapter picked by
  an env var, say) isn't seen. Set the app's start command and output folder then.
- Nuxt `routeRules`: `swr` and `cache` keep their pages in each instance's memory (not shared,
  lost on deploy), and `isr` does nothing on the `node-server` preset. No edge cache yet.
- Nuxt with a `nitro.preset` in `nuxt.config` builds that preset; `bun` (Nitro 2) is not
  recommended (no graceful shutdown, buffered request bodies).
- React Router's RSC framework mode (unstable) isn't tested.
- Hashed asset folders (`/assets/` of React Router and TanStack Start, `/_app/immutable/`,
  `/_nuxt/`, `/_astro/`) are cached for a year, except what the app's own `public/` (SvelteKit
  `static/`) puts there, which is revalidated. A Vite `publicDir` other than `public/` isn't
  read: its files under `/assets/` would be cached as hashed. Keep such files outside
  `assets/`, or rename them on change.

## Builds

- **Dockerfile:** supported, with limits. Your plain env reaches the build as build args;
  secrets and service URLs only as BuildKit secrets (`RUN --mount=type=secret`), never as
  build args. There is no SSH forwarding (`RUN --mount=type=ssh`), no named or extra build
  contexts, no `RUN --network=host` and no privileged steps. Nothing outside the build
  context can be mounted.
- **Prebuilt images:** one image per tarball (`docker save` / `nerdctl save`).
- **Git imports:** public https repositories only, no submodules, 512 MB and 3 minutes
  at most. For private code, push to the box or connect GitHub.

## Monorepos

- **JavaScript workspaces** (pnpm, Bun, npm, Yarn 2 or later): an app in a workspace
  installs only itself, the workspace packages it depends on, and the workspace root's
  own dependencies:

  | Package manager | Install |
  |---|---|
  | pnpm | `pnpm install --filter {./apps/web}...` |
  | Bun | `bun install --filter ./ --filter ./apps/web` |
  | npm | `npm install --workspace apps/web --include-workspace-root` |
  | Yarn 2+ | `yarn workspaces focus <app> <root>` |

  If that install fails, the box installs the whole workspace instead, and the build log
  says so. An `install` command of your own (or vercel.json's `installCommand`) replaces
  both, and so does a folder or package name with spaces or quotes in it. Measured on a
  2-CPU box: importing `honojs/starter`'s `templates/bun` (a pnpm workspace of 13
  starters, 579 packages in all) took 327 s with the whole workspace and 105 s now.
- **Yarn 1** workspaces install whole: Yarn 1 can't install one package.
- **A build that needs another workspace package's dev tools** that the app doesn't list
  itself fails after the filtered install (the fallback only covers a failed install).
  Add the tool to the app's own `package.json`, or set `install` to a full install.
- **Python:** each app installs on its own. uv workspace members (`[tool.uv.workspace]`)
  aren't built from the workspace yet: give the member its own `uv.lock`, or use a
  Dockerfile.

## Python

FastAPI is first-class; other Python servers run as generic `python` apps (see the table
above). Installs follow the lockfile (`uv.lock`, `poetry.lock`, `pdm.lock`, `Pipfile`,
`requirements.txt`). uv workspaces: see Monorepos.

Without a pinned version (`.python-version` and the like), the box picks a Python from
3.9 to 3.14 that meets `requires-python`; one that needs anything else (3.8, 3.15, a
pre-release) needs a pin.

## Sleep and wake

Production apps sleep only when the project sets `sleepAfter`; previews sleep after 15
idle minutes. A sleeping app's containers are stopped, not removed: its memory and CPU
are freed, and the next request starts the same container again. Measured on the live
box (2 vCPU x86, Hetzner cx23), the median of 5 wakes, from the request arriving to its
first byte (runs vary by about 0.1 s):

| App | A new container (before) | The kept container (now) |
|---|---|---|
| Hono on Bun (starter) | 0.73 s | 0.45 s |
| Next.js 16 on Bun (starter) | 1.36 s | 0.87 s |
| FastAPI (starter) | 1.95 s | 1.7 s |

Starting a container costs about 0.35 s of that (creating one cost about 0.65 s); the
rest is the app's own boot (FastAPI's imports alone take about 1.1 s on this box). A
bigger app takes as long as it needs to boot and pass its health check. A deploy, a
rollback, or a change to the app's env or settings while it sleeps makes the next start
a fresh container.

Prerendered pages of Astro, SvelteKit, Nuxt, React Router and TanStack Start are
answered by the box while the app sleeps, without waking it (see Caching below). Not yet:
Next.js pages, pages rendered on request, and Early Hints during a wake.

## Email

- **Sending** goes through a mail provider you bring (Resend, Postmark, SES, SendGrid,
  Mailgun, Brevo or any SMTP relay). Without one, mail goes to the dev inbox.
- **Port 25 is blocked for apps:** their mail goes through the box (`SMTP_URL`).
- **Hetzner blocks outbound port 465** on new servers (and 25). Use a relay on port 587
  with STARTTLS; Cloudflare Email Service's SMTP is on 465, so it doesn't work there.
- **No inbound mail yet:** the box can't receive email for your domains.
- Each project may send 300 messages an hour by default.
- **Relayed mail comes only from the project's own senders:** `<project>@<box domain>`
  or its one verified sending domain. A project can't send from several domains.
- **SMTP submission is bounded:** messages up to 25 MiB; at most 4 arriving at once per
  project and 16 for the box (more get a "try again" 451); one message may take up to 10
  minutes to arrive; at most 256 open connections.
- **Reading mail needs full access.** With read-only access to a project you see who each
  message is from and to, its size and delivery, never its subject, text, links or
  attachments: mail carries reset links and sign-in codes.

## Sign-in

- **Your apps' sign-in providers:** Google is tested end to end. GitHub, Apple,
  Microsoft, Discord, Facebook, X, LinkedIn, GitLab, Slack, Twitch and generic OpenID
  Connect are wired up but not tested yet.
- **Dashboard sign-in** with a provider supports only Google and GitHub, and only with the
  box-wide keys, not a project's own.
- **Dashboard sign-in matches by email once.** The first sign-in with Google or GitHub
  matches a person by an address the provider vouches for, then links that provider
  account to them: later sign-ins go by the account, and no other account of that
  provider signs them in. That first match still trusts the provider: GitHub keeps an
  address "verified" after its domain changes hands, so invite people by an address they
  hold now. Google counts only Gmail and Google Workspace addresses (others: sign in with
  an email link). To link a different account, remove the person and invite them again.
- **Apps: Google sign-up with a non-Gmail, non-Workspace address** makes an unconfirmed
  account (Google doesn't vouch for who holds that address now); it confirms by email and
  never joins an existing account with that address on its own (`account_not_linked`).
  The same goes for any provider that doesn't report the address as verified.
- A provider sign-in in progress fails if the box restarts (its signing key is kept in
  memory). Start it again.
- **Sign-ins** (Settings › Sign-ins) show the address and country a session signed in
  from, not where it is used now; last active is to the minute. Sessions from before this
  page existed show no browser or method. The list goes back 30 days.
- **Confirming it's you** (for a long-lived or full-access API key, a new passkey or a
  changed email address) takes one of your passkeys, or signing in again with a passkey,
  Google, GitHub or an emailed link (the owner: or their own `tiffin login`). On a box
  with no Google or GitHub keys and no mail relay, people other than the owner, signed in
  with an invite or an admin's link, can't add their first passkey or change their email
  in the dashboard, and can make only read-only keys for a day there: connect a relay
  (Settings › Email) or the Google or GitHub keys first, or the owner adds it for them
  (`tiffin tokens create`, `tiffin people email`). Box-wide limits on how long keys may
  live don't exist yet.
- **Email addresses can't be changed with an API key**, even an admin one: only in the
  dashboard or with the owner token.
- **Removing a passkey** doesn't ask you to confirm it's you (it is emailed and audited),
  and neither do inviting people or making sign-in links for them.
- Admins can see the owner's sessions but not end them. There is no "sign everyone out"
  for the whole box; end each person's sessions in turn.
- Browsers the box knows can't be forgotten one at a time, and the box doesn't name
  browsers beyond "Chrome on macOS".
- **Previews share real users.** A preview signs in against the project's own accounts:
  anyone who signs up on a preview is a user of the app, and a preview's emails reach
  real people. Treat a preview of someone else's branch as you would deploying it.
- **Auth tables live in the app's database**, so the app, and anyone with read-only SQL on
  the project, can read them: users' addresses, password hashes (scrypt), sessions'
  addresses and browsers. Nothing there works as a credential (reset and magic-link
  tokens hashed, one-time codes and provider tokens encrypted, session cookies signed with
  a key outside the database), but there is no separate database role that hides them.
- **Rate limits trust the address apps send.** Server-side sign-ins pass the visitor's
  address (`X-Forwarded-For`) so limits count per visitor. Each project has its own
  counters, but an app on the box calling the engine directly can name another app's host
  and a made-up address, and so spend that app's counters for that address.

## Agents

An agent is an app you write ([Run an always-on agent on your box](always-on-agents.md)).
The box adds nothing for LLMs:

- **No model settings or spending records.** Your app calls the provider with your key. The
  box doesn't count tokens or cost; cap them in your code and with the provider's own
  limits.
- **No approval step for API keys.** A full key applies changes with nobody asked. Give an
  unattended agent a read-only key for one project. Workflow approvals (`ctx.approval`)
  cover your app's own actions, not the box's.
- **Apps get no Tiffin API key or address.** An agent that reads the box needs its own key
  and the dashboard's address as secrets.
- **Outbound connections are open,** except port 25. The box doesn't limit which hosts an app
  or its tools reach.
- **Long-lived connections** (a Discord gateway) work from a worker, with caveats: during a
  deploy the old and new releases overlap for a moment, so both can receive the same events;
  a sleeping project (`sleepAfter`) drops the connection, and a worker wakes only for queue
  deliveries. Not tested end to end yet.
- **No inbound email** (see Email above), so inbox agents need a mail provider's inbound
  webhook to an app route.

## Servers

- **SSH host keys are trusted on first use.** A new Hetzner server, or an SSH server that
  is not in your `~/.ssh/known_hosts`, is trusted the first time Tiffin connects; only
  after that is a different key refused. Tiffin cannot check the fingerprint through
  another channel. For an SSH server, connect once with `ssh` and check the fingerprint
  first: Tiffin then uses the key you accepted.
- **No moving data onto a new data disk.** `up --data-disk` or `--data-dir` on a box whose
  data is on the root disk is refused rather than hiding the data; move it by hand
  (stop `tiffin`, `tiffin-edge.socket` and `tiffin-edge.service`, copy `/var/lib/tiffin`,
  then run `up` with the option). A data directory that is already mounted is kept as it
  is, even if the option names another disk.
- **`down` on an SSH server stops Tiffin, its edge and its app containers only.** Postgres,
  Valkey and the other system services stay installed and running (reachable only from the
  server), and the firewall and hardening stay on.
- **`tiffin.config.ts` sees no environment** on your computer or the box (`process.env` is
  empty) and imports only files of its repository. Values that differ per environment
  belong in secrets or `env`.

## API

- **Request bodies:** a JSON body holds at most 200,000 values (array items and object
  members); bigger batches go in several requests. An error lists the first 50 field
  problems.
- **Raw uploads** (deploy tarballs, box and project imports, storage parts) must keep moving:
  at least 64 KiB every 30 seconds, or the box ends the upload. There is no limit on how long
  a steady upload takes.
- **Connections:** idle keep-alive connections close after 2 minutes; request headers are at
  most 64 KiB.
- **Idempotency-Key:** the answer is kept for 24 hours when it is at most 1 MiB (larger
  answers are sent but not kept). Whether a request is still running is known only to the
  running box: after a restart, a request that was running reads `none`, and its work may
  be partly done.

## Limits per project and per box

| | Default | Change it |
|---|---|---|
| Project memory and CPU | Shares the box; protected up to a fair share | `resources` (`memoryMB`, `cpus`, `maxSharePercent`) |
| Postgres query time | 5 minutes; 30 s in a project with a limit | `services.postgres.statementTimeoutSeconds` |
| Postgres connections | 80 per project; a limited project gets its share of 100 | |
| Idle in transaction | closed after 60 s | |
| KV memory (Valkey) | 64 MB, held while the project has a limit | `maxMemoryMB` |
| Storage (databases + files) | no limit; the disk guard warns at 85% and makes the fastest-growing project read-only at 95% | `tiffin storage quota set` |
| A read-only hold on a database | transactions default to read-only, which an app can override; one whose databases still grow by more than 64 MiB is locked out of them, reads included, until the hold lifts (checked every 30 s, so a determined app writes for up to that long) | |
| Restoring a database snapshot | runs as the project's own role, never the superuser: it needs one of the project's connections, and its index builds must fit the role's temporary-file limit | |
| SQL console results (`tiffin sql`, the data browser) | values cut at 100,000 characters; at most 32 MiB of rows per request (more are counted, not returned); a single row over 64 MiB fails | `limit`, or select fewer columns |
| Request time | 15 minutes, up to 24 hours | `timeoutSeconds` |
| Queue job attempt | 60 s without a response or heartbeat (5 to 3600); heartbeats extend it up to 24 hours | `leaseSeconds` |
| Cron call | 60 s (5 to 3600) | `timeoutSeconds` on the cron |
| Calls to web addresses (`url` crons and queues) | 600 a minute per project | `TIFFIN_QUEUE_URL_RATE` on the box |
| Queue deliveries at once (jobs, cron calls and workflow turns together) | 50 per project, of 200 on the box; the rest wait their turn | `TIFFIN_QUEUE_PROJECT_CONCURRENCY` on the box |
| A delivery the SDK reads | 2 MB for a job or cron call; 64 MB for a workflow turn, which carries the run's whole history (each step's result up to 1 MB); more answers 413 and retries | `maxBytes` on `defineHandler`, `workflow.handler()` or `verifyRequest` |
| `sendTx` outbox rows | payload 1 MB, options 16 KB; a larger row goes to the dead-letter queue without its payload | |
| Live progress streams | 200 open per project | |
| Email | 300 messages an hour | `tiffin email rate-limit set` |
| Rollbacks | the last 3 production deploys; previews keep none | |
| Previews | deleted after 7 days with no request or deploy | |
| Build cache | 15% of the data disk (4 to 20 GiB) | |

Everything runs on **one machine**: if the box is down, your apps are down. Backups stay on
the box unless you [copy them off it](data.md#copies-off-the-box).

## Backups

- Each off-box copy reads every backed-up file again (only changed chunks are sent), so
  on a box with many gigabytes of files each copy spends a while reading the disk.
- A backup set's file list must fit in 1 GiB (about five million files) to be copied off
  the box; a bigger set fails to copy and says so. A restore from the bucket holds that
  list in memory.
- A bucket that stops sending data for 2 minutes fails the copy, restore or prune that
  was reading from it (copies try again after 15 minutes). Objects bigger than they can
  be are refused, not read.

## Usage and observability

- Usage trends cover only the last hour.
- Per-project image sizes count layers that images share once per project, so the
  projects' totals can add up to more than the image store.
- A key limited to some projects can't see box-wide reports: the disk breakdown, the
  box's resources, backups and restore drills. In observe it sees the machine's CPU,
  memory, disks and services, but only its own projects' containers, alerts and
  error-spike rules.
- Database snapshots of deleted projects are kept for 7 days.

## The dashboard

- **Build logs:** the viewer keeps the newest 50,000 lines (8 MB of text) and cuts any line
  at 16 KB, saying so; **Download** always fetches the whole log from the box. The launch
  page shows the newest 1,000 lines.
- **Logs page, live:** reads up to 2,000 new lines every 2 s. A faster burst shows a
  "came in too fast" marker that opens that time range; the live view keeps the newest
  3,000 lines.
- **Routes:** a route folded from several addresses (`/orders/:id`) shows the slowest
  address's p50/p95 as an upper bound (≤), not an exact percentile across the route.
- **Live app logs:** after a dropped connection the stream resumes from the newest line it
  showed. A line from another instance still in flight at that moment can be missed;
  reloading shows it.
- **Table editor:** arrays are edited as Postgres array literals (`{a,"b c",NULL}`), not one
  item per line. Timestamps with microseconds, `infinity` or BC dates are edited as text.
  After a change, the rows reload so filters and sort stay true; an edited row that no
  longer matches shows until they do.

## Caching and images

- No edge response cache (ISR, `s-maxage`, `stale-while-revalidate`) for frameworks other
  than Next.js yet.
- No shared image optimiser at the edge yet: each app optimises its own images (Next.js
  with sharp).

Both are planned.

- **Next.js cache:** a page `next build` prerendered more than 30 days ago is rendered
  anew on its first request rather than served from the build, since the box keeps a tag's
  revalidations for 30 days only.
- **Next.js image cache:** `images.maximumDiskCacheSize` is enforced by each instance from
  its view of the shared directory, refreshed at most a minute old; instances together may
  overshoot it by what they write in that minute.

**Prerendered pages at the edge** (Astro with `@astrojs/node`, SvelteKit, Nuxt, React
Router, TanStack Start) cover the files the build wrote, as they are:

- Next.js prerendered pages still go to the app (its `proxy.ts` runs before them).
- Headers a framework adds to prerendered pages are not added: Astro's `_headers.json`
  (CSP with `staticHeaders`) and Nuxt `routeRules` headers. A site that needs them on
  prerendered pages should render those pages on request.
- SPA shells (TanStack Start's `_shell.html`, React Router's `__spa-fallback.html`,
  Nuxt's `200.html`) and `404.html` are not used as fallbacks for a server app: other
  paths go to it. A build that is only files (React Router `ssr: false`, `nuxt generate`
  with `ssr: false`, an adapter-static `fallback`) does serve its shell for them.
- A trailing slash is answered as the framework writes the file: `/about/` from
  `about/index.html` (and `/about` too), `/about` only from `about.html`. The box never
  redirects; the app does, for a path it leaves to it.
- React Router's lazy route discovery (`/__manifest`) still reaches the app on client
  navigation, so a sleeping `ssr: true` app wakes then; `routeDiscovery: { mode: "initial" }`
  avoids it for mostly-static sites.

**Static sites:**

- The previous release's hashed files are kept for a day after it stopped being live,
  counted from deploy to deploy: a file goes at the first deploy after its day is up. Only
  hashed names are kept (a name with a content hash, or under `/_astro/`, `/_app/immutable/`
  or `/_next/static/`); other files are the live release's only.
- Static sites built with Bun keep their build caches in a folder per app, started afresh
  past 2 GiB. A preview's build uses its app's caches, so a preview build could leave
  files in them that production builds read.

## Analytics

Not yet: funnels and retention, goals, share links, excluding your own visits, and
automatic events from sign-ups and deploys.

- A `from`..`to` range covers at most 3,653 days (ten years, the longest retention);
  hourly points go up to 92 days.
- "Right now" counts at most 5,000 visitors and 500 pages and sources per app and
  minute; past that it undercounts visitors and shows the rest of the pages as
  `(other)`. Daily stats are not affected.
- When the analytics store falls behind, the collector holds up to 100,000 events
  (64 MiB) and then drops new ones, counted as lost in `tiffin status`. There is no
  per-project share yet: one app flooding the collector can crowd out other projects'
  events while the store catches up.
