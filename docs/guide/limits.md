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
  context can be mounted. BuildKit's own Dockerfile frontend (BuildKit 0.33) always builds
  it: a `# syntax=` line is ignored, so `docker/dockerfile:1-labs` features don't work.
  Env names starting `BUILDKIT_` don't reach the build as build args.
- **Build caches** (`RUN --mount=type=cache`, Railpack's install caches) belong to one
  app: another app, or another project, never shares them, whatever cache id it names.
- **Build resources:** all builds share one memory cap and 4,096 processes and threads;
  a static site's build gets the same task cap. An app's project may use a quarter of the
  box's task limit, all apps together half.
- **Prebuilt images:** one image per tarball (`docker save` / `nerdctl save`).
- **Git imports:** public https repositories only, no submodules, 512 MB and 3 minutes
  at most, checked out (files at their full size) as well as downloaded, and 200,000
  files. For private code, push to the box or connect GitHub.
- **Uploads:** 4 GB of files, 200,000 files and 300,000 entries in all (files, links and
  folders) per source.
- **Files the box reads itself** (`package.json`, framework configs, lock files,
  `vercel.json`, `.gitignore`/`.tiffinignore`, workspace files) must be plain files of at
  most 16 MB; a bigger one, or a link in a fresh clone, is treated as missing. Like git,
  the box never follows an ignore file that is a link.
- **Static sites** may hold only folders, plain files and links inside the site (no
  FIFOs or devices). Text files over 32 MB, and anything past 512 MB in all, are served
  without a precompressed copy.
- **Railpack plans on the box itself:** it reads your repository's files (and runs mise,
  in its safe mode, to resolve versions) as root on the host, outside a container. Your
  env never reaches its environment, but a bug in Railpack or mise parsing a repository
  is a bug on the host. Planning in a container is planned.

## Deploys and changes

- The box converges projects one at a time. An app whose new settings keep failing their
  health check holds up other projects' changes for up to about 2 minutes per attempt;
  its retries back off from 30 seconds to 30 minutes.
- A workflow run that starts at the moment a new release takes over can find its release
  already stopped. It then runs on the new release from its first step, and its timeline
  says so.
- A static preview's requests are counted from the edge's access log. While the box
  cannot read that log, a static preview expires 7 days after its last deploy, used or not.
- **Earlier versions at their own addresses** read the database and KV read-only, but not
  everything is: the project's files (buckets) take uploads and deletes as from
  production (there are no read-only S3 keys yet), jobs and workflow runs an earlier
  version starts run on production's workers, and an app's own credentials (a
  `DATABASE_URL` it set itself, an outside service's API key) are left as they are. Open
  an old version to look, not to work in it.
- An earlier version's disk folders start from its image, not from production's data.
  Sign-in doesn't work at version addresses (the auth engine is not routed there).
- A version address's gate cookie lasts its hour: signing out of the dashboard doesn't end
  it, and anyone who can read the project can mint a link (`tiffin deploys link`).
- An earlier version with a request under way is not put to sleep to make room, so a
  third can run for as long as that request does.
- An earlier version's first request waits for a new container and, for the frameworks
  whose client files the box serves, a copy of those out of its image: at least the
  1.4 s a fresh Next.js starter container takes (see [Sleep and wake](#sleep-and-wake)),
  more for a bigger app. Later wakes reuse the container.
- A new production deploy's address goes on the edge when the deploy is queued: one edge
  config reload per deploy, as for a new preview (open WebSockets survive it), never at
  the switch itself.
- **Without a wildcard certificate** (no DNS provider connected), each version address
  gets its own certificate from Let's Encrypt on its first visit, as previews do. Let's
  Encrypt allows 50 new certificates per registered domain a week, shared by previews,
  version addresses and custom subdomains; past that a visit fails until the week rolls
  over. Connect a DNS provider for a wildcard certificate instead.
- A cleaned-up version's address answers its "cleaned up" page while the box keeps its
  record (the last 50 per app), then "nothing here".

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

## GitHub

- **Webhooks are handled as they arrive**, not from a durable inbox. GitHub does not resend
  a failed delivery by itself: when the box answered one with an error (a 5xx), redeliver it
  from the app's settings on GitHub (Advanced › Recent deliveries).
- **Only production pushes are checked against their branch.** A pull request event that
  arrives late can rebuild the preview of an older head; the next push to the pull request
  fixes it.
- **Final reports to GitHub** (status, deployment, comment) are retried for a day, then
  dropped.
- **A shared GitHub App** acts only on the repositories the installer could push to when
  installing from the box; repositories added to the installation later need another
  Install on repositories.

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
bigger app takes as long as it needs to boot and pass its health check. A wake while the
box is busy (builds running) is slower: a sweep deploying three starters at once measured
1.7 to 2.1 s for the Next.js starter. The Next.js starter's health check is its home page,
which a cheaper route would not speed up: the visitor's request then pays the first render
instead. A deploy, a
rollback, or a change to the app's env or settings while it sleeps makes the next start
a fresh container.

Prerendered pages of Astro, SvelteKit, Nuxt, React Router and TanStack Start are
answered by the box while the app sleeps, without waking it (see Caching below). Not yet:
Next.js pages, pages rendered on request, and Early Hints during a wake.

## Isolation between apps

Apps run in containers on the host's network, so they share its loopback ports (see
[Security](security.md)). The box checks who answers every connection it opens to an app,
and app containers run without raw sockets or ports below 1024. Not covered yet (the fix
is a network namespace per app, with box services on an address of their own):

- **Box services while they restart.** Postgres, PgBouncer, Valkey, the sign-in engine,
  storage and the error and analytics collectors listen on loopback ports. While one of
  them restarts (an update), an app could listen on its port and answer apps in its place,
  getting what they send it (a Valkey password, a query). The dashboard and API are not
  affected: the edge reaches them on a Unix socket.
- **Builds** (`RUN` steps of a Dockerfile, Railpack, static site builds) run on the host
  network, and BuildKit's steps keep raw sockets. The box's requests never go to a build,
  but a build could read loopback traffic while it runs.
- **Apps of one project** are not checked against each other: the project is the
  boundary. An app of the project can take a port another of its apps left free.
- **An app that listens with `SO_REUSEPORT`** lets another app running as the same user
  (root, in most images) join its port; the box sends nothing to the intruder, so its
  share of requests fails (502) instead.
- **A local box** serves HTTPS on 8443 and HTTP on 8080, above 1024, so an app running as
  root could join those sockets and get a share of the connections (TLS it cannot
  decrypt, and the HTTP port's redirects). A server's 80 and 443 are out of apps' reach.

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
  page existed show no browser or method. The list goes back 30 days, and sessions that
  ended or expired before that are deleted at the next sign-in (unless they made an API
  key, so removing the person still revokes it).
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
- **An invite lives as long as its sender's access**, not its session: it stops working if
  the person who sent it is removed or no longer an owner or admin, or an owner ends the
  session that sent it, but not when that session just signs out or expires. To stop one
  sooner, change or clear the person's email (that cancels their unspent links) or
  remove them.
- **Restarting Tiffin** (not the edge) makes a new edge key: for the moment until the
  edge has its new configuration, dashboard requests count as coming from `127.0.0.1`
  for rate limits and the audit log.
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
- **An upgraded edge serves its saved configuration only if the new build can load it.**
  When a new build drops a module the old configuration names, the edge serves nothing
  until Tiffin sends it a fresh configuration: about a second when Tiffin is running
  (it is, during `up` and self-updates), longer if Tiffin itself is down. Builds keep
  retired modules registered so this does not happen.
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
- **Lists page:** lists that grow (changes, jobs, workflow runs, mail, auth users and
  organizations, error issues, traces, alert history, the audit log) answer 50 rows by
  default and at most 200, with a `nextCursor` for the next page. A cursor holds its
  list's order and filters' position, not a snapshot: rows added while you page show on a
  new first page, and an error issue seen again moves to the top (it can show twice, never
  not at all). Traces keep their 3-day window; there is no total count, except where a
  page says one (Auth's overview).
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
| KV Lua script | 1 s, then killed; one that already wrote can't be, so Valkey restarts and every project's KV drops for a few seconds (the box can't tell which project's script it was, so one that keeps doing it keeps restarting it). No functions (`FUNCTION`, `FCALL`) | |
| KV REST request | 16 MB and 10,000 commands in; about 16 MB of replies out | |
| Storage (databases + files) | no limit; the disk guard warns at 85% and makes the fastest-growing project read-only at 95% | `tiffin storage quota set` |
| A read-only hold on a database | transactions default to read-only, which an app can override; one whose databases still grow by more than 64 MiB is locked out of them, reads included, until the hold lifts (checked every 30 s, so a determined app writes for up to that long) | |
| Restoring a database snapshot | runs as the project's own role, never the superuser: it needs one of the project's connections, and its index builds must fit the role's temporary-file limit | |
| SQL console results (`tiffin sql`, the data browser) | values cut at 100,000 characters; at most 32 MiB of rows per request (more are counted, not returned); a single row over 64 MiB fails | `limit`, or select fewer columns |
| Image transforms (`files.<domain>?w=`) | 3840 px wide and 40 megapixels out, 50 MiB in, 2 GiB of memory, 30 s; half the CPUs (at least 2) at once, a project half of those; a failed transform answers 422 for 10 minutes without running again (a new version of the file is tried at once) | |
| Request time | 15 minutes, up to 24 hours | `timeoutSeconds` |
| Queue job attempt | 60 s without a response or heartbeat (5 to 3600); heartbeats extend it up to 24 hours | `leaseSeconds` |
| Cron call | 60 s (5 to 3600) | `timeoutSeconds` on the cron |
| Calls to web addresses (`url` crons and queues) | 600 a minute per project | `TIFFIN_QUEUE_URL_RATE` on the box |
| Queue deliveries at once (jobs, cron calls and workflow turns together) | 50 per project, of 200 on the box; the rest wait their turn | `TIFFIN_QUEUE_PROJECT_CONCURRENCY` on the box |
| A delivery the SDK reads | 2 MB for a job or cron call; 64 MB for a workflow turn, which carries the run's whole history (each step's result up to 1 MB); more answers 413 and retries | `maxBytes` on `defineHandler`, `workflow.handler()` or `verifyRequest` |
| `sendTx` outbox rows | payload 1 MB, options 16 KB; a larger row goes to the dead-letter queue without its payload | |
| Live progress streams | 200 open per project | |
| Email | 300 messages an hour | `tiffin email rate-limit set` |
| Rollbacks and version addresses | the last 20 production deploys (3 while the data disk is past the disk guard's warning level); previews keep none | |
| Earlier versions awake at their own addresses | 2 per project; each sleeps after 5 idle minutes | |
| Previews | deleted after 7 days with no request or deploy | |
| Build cache | 15% of the data disk (4 to 20 GiB), less while under 15% of the disk is free; no BuildKit build history is kept | |

Everything runs on **one machine**: if the box is down, your apps are down. Backups stay on
the box unless you [copy them off it](data.md#copies-off-the-box).

## Database clients

- **postgres.js 3.4.9 needs `prepare: false` on `DATABASE_URL`.** When a prepared query
  fails because the pooler dropped its statement or a migration changed a table, it
  retries with its parameters encoded twice: jsonb stored as a string, `true` as `false`
  ([porsager/postgres#1197](https://github.com/porsager/postgres/issues/1197)). On a pooler, a commit can also silently become a
  rollback ([#1212](https://github.com/porsager/postgres/issues/1212)). `prepare: false` avoids both, and the starters set it. The
  fix for #1197 is merged but not released: upgrade when 3.4.10 ships.
- **Bun.sql isn't recommended yet** (Bun 1.4.2): wrong `text[]` binding, unparsed `uuid[]`,
  a connection leak, `sql.listen()` not told when its connection drops, and no COPY or
  cursors. See [Connecting from your app](data.md#connecting-from-your-app).

## Databases and KV from outside the box

- **Postgres and KV are reachable only from inside the box.** Apps on the box use them
  directly; from your computer, `tiffin db tunnel` and `tiffin kv tunnel` open a private SSH
  tunnel. There is no public address, so something running elsewhere (a frontend on Vercel,
  a hosted BI tool, a database app that can't use SSH) can't connect. Workaround: run a small
  API app on the box in the same project and call that instead. Planned: a per-project
  "Allow connections from outside" switch (confirmed, because it opens data to the internet)
  giving an address on the box's domain, e.g. `db.<project>.<apps domain>:5432`, TLS required,
  every project on one shared port (Postgres direct-TLS with SNI), with an optional
  read-only login and allowed-IP list.

## Deleting all data

- Delete all data keeps one delete per part: deleting again within 7 days replaces the
  earlier delete's saved data (the database's earlier snapshots stay in
  `tiffin snapshots list` until their 7 days are up).
- A restore puts back the data in place of what the part holds by then: KV keys written
  since are deleted for good (a database is snapshotted first, a bucket's files go to the
  trash).
- Deleting a database's data drops its preview branches too; a preview gets a new, empty
  branch on its next deploy. pg_cron jobs (kept in the box's own database) stay.
- Saving KV keys holds each key's value in memory while it is written to disk; a project
  with very large keys needs that memory free for a moment.
- The plan measures what goes within a fifth of a second: a very large KV or bucket can
  leave the count out, and the confirm then asks for the project's name anyway.
- Auth isn't always there like Database, KV, Files, Email and Analytics: it answers
  `/api/auth` on every app address, which would take that path from apps with their own
  sign-in. Add it with `services: { auth: {} }`.

## Backups

- **Point-in-time restore is for Postgres only.** Valkey (KV), files (buckets, mail,
  analytics, app disk folders) and the platform state keep no log between backups, so a
  restore to a moment puts them back to the newest backup set at or before it: up to 6
  hours earlier with the default schedule (more often: `tiffin backups schedule
  --incremental-every-hours 1`).
- **It restores the whole cluster, not one project.** Every project's database shares one
  Postgres cluster, and WAL replay can't pick out one database, so a time restore takes
  every project back. There is no per-project point-in-time restore: a project's own
  database snapshots (`tiffin snapshots`) go back to when each was taken.
- **From this box's copy only.** A restore from the bucket (`--from offsite`) restores a
  whole set, not a moment.
- Moments between a restore and the next backup, and the minute or so while a backup
  starts, can't be reached; the refusal says which times to pick instead.
- The moment is to the second in the API and CLI, to the minute on the dashboard.

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
- **What is kept, and for how long:** the audit log (`tiffin audit list`) a year; alert
  history the newest 1,000 transitions; a project's dev inbox its newest 1,000 messages,
  and relayed mail's log 30 days; done and cancelled jobs 7 days, failed jobs and finished
  workflow runs 30 days. The change log (Activity) is kept for good: Undo and
  History read it. Error issues stay until their project is deleted (each keeps its newest
  events); resolve or ignore old ones to keep the open list short.

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
  files in them that production builds read. Deleting the app or destroying the project
  removes them.
- **BuildKit's cache is shared, not per project:** destroying a project removes its
  images and static build caches at once, but its BuildKit cache mounts (package caches
  keyed by project and app) stay until they are a week unused or the build cache passes
  its cap.

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

## Managed boxes (ShipTiffin)

See [managed boxes](managed.md). What is not done yet, or done the simple way:

- **shiptiffin.app is not on the Public Suffix List yet.** Until it is, browsers treat
  every `<name>.shiptiffin.app` as one site with `shiptiffin.app` (cookies set on the
  parent domain would be shared between customers' boxes; the dashboard's own cookies are
  host-only), and Let's Encrypt's limit of 50 new certificates a week per registered domain
  is shared by every managed box and its apps. The owner submits `shiptiffin.app` to the
  PSL (github.com/publicsuffix/list, private section, with the `_psl` TXT record);
  acceptance takes weeks.
- **One certificate per name, over HTTP-01.** The box holds no DNS token, so it cannot get
  a wildcard: each new app or preview gets its certificate on its first visit (a few
  seconds), counted against the limit above.
- **Support access has no button yet.** Support never logs in by default. A customer who
  wants help on the server writes to hello@shiptiffin.com and we arrange it by email: they
  add a temporary SSH key and firewall rule by hand, and remove both afterwards. A
  dashboard switch that does both, and undoes them, is planned.
- **Certificates can be slow.** A box is *ready* only once its dashboard answers over
  HTTPS with a valid certificate; until then it shows *certificate pending* and the worker
  checks every minute (the ready email goes then). A box stuck there for days (the shared
  rate limit above) has no automatic escalation beyond the admin page.
- **The first sign-in is a link that works once, for 24 hours.** The box makes it (at
  setup, again once the dashboard is ready if that took over an hour, and whenever the
  customer asks until their first sign-in) and enforces both; the control plane keeps the
  newest until the box reports the owner signed in. A customer who signs in and adds no
  passkey (and has no mail service on the box for email links) gets back in through the
  Hetzner console: in the server's root console, `sudo tiffin login --home
  /var/lib/tiffin/platform` prints a one-time path (`/login#…`) to open on the dashboard's
  address. Until the owner first signs in, the website's account (and its database) can
  get an owner link: whoever controls the customer's ShipTiffin account, or can read and
  write that database, before the first sign-in can sign in as the box's owner. A box
  waiting for its first sign-in checks in every 2 to 10 minutes instead of every six
  hours. "Signed in" means the box saw a sign-in link of the owner's redeemed.
- **Resize changes the server type only.** Growing the data volume is still `tiffin up
  --volume-size` from a computer with SSH access, or the Hetzner console plus
  `xfs_growfs`. A type change keeps the architecture (cx↔cx, cax↔cax): Hetzner can't move a
  server between ARM and x86.
- **Automatic updates are gated, the releases are not.** An unpaid managed box stops
  installing updates by itself; the signed releases stay where every box reads them, so an
  owner can still update by hand. Gating is a courtesy switch on a server the customer
  fully controls, not a lock.
- **Monitoring is one place.** The checks run from ShipTiffin's own box every five minutes;
  if that box is down, nobody is told. A box that misses its check-ins (every six hours)
  for 36 hours gets one email.
- **A box gone quiet loses its address after 72 hours.** A server deleted in the Hetzner
  console frees its IP for someone else, and the `shiptiffin.app` name must not follow it.
  So a box without a check-in that counts for 72 hours has its address parked (the owner
  is emailed), however its IP answers HTTPS. A check-in counts only with the licence of
  the box's current setup, sent from the box's own address (its IPv4, or its IPv6 /64);
  the next one puts the address back. A box whose IP changed (a new primary IP) is parked
  for good: write to support. The address check needs shiptiffin.com served directly
  (DNS only, not through a proxy), as it is.
- **A failed setup cleans up at once, until Tiffin is installed.** Before the install it
  records the address before publishing it (`dns_state` *pending*), removes it on failure
  whatever was recorded, and deletes what it made in the customer's project (only
  resources labelled with its box id) only once the address is gone. If the worker stops
  mid-setup, a clean-up job does the same while the customer's key lasts (two hours);
  after that, what's left stays, labelled `shiptiffin-box=<id>`, until the next try (which
  cleans up first) or the customer deletes it, and the sweep keeps removing the address.
  From the install on (`installed_at`, written right after it, retried), nothing deletes
  the server or volume: one database statement decides every clean-up (never installed,
  never ready, same setup), a later failure sets *needs attention* (account, /admin,
  email) and leaves the box *certificate pending* so it becomes ready by itself, and a
  "ready" whose answer was lost is read back. The one gap: a worker that loses the
  database exactly between a finished install and recording it, then stops, is cleaned up
  as a failed setup (that box had no owner sign-in yet, so no data). A setup is never
  resumed half way.
- **One worker, a few jobs at once.** Jobs run three at a time, one per box, oldest first;
  more wait their turn. A worker that can't renew its lease (5 minutes) stops its job
  within half of it (each renewal is abandoned at that deadline, even a database call
  that hangs), and the sweep then retries it (resize, delete, DNS) or fails it and
  cleans up (setup). An interrupted resize always powers the server back on, with its
  own key (two hours) or the kept one, whatever the subscription; without either, or
  when the sweep gives up on it, the box gets *needs attention* and a `server_off` email. A delete, clean-up or address change that fails is tried again by
  itself, five times at most, backing off from a minute, with the customer's key while it
  lasts (two hours); a delete always removes the address first, so what's left after
  that is only the server, which the customer can delete in the console. A new setup of a
  box whose clean-up gave up removes the old address before it deletes anything the
  earlier attempt left, and stops (keeping it all) while the address can't be removed.
- **No key rotation tool.** `CLOUD_SEAL_KEY` opens stored Hetzner keys; changing it makes
  the stored ones unreadable (customers paste their key again). The sealed format carries
  a version prefix (`v2.`) for a rotation later. `CLOUD_LICENCE_KEY` signs licences;
  changing it means every box needs a new licence (a re-setup).
- **The website can still queue jobs.** It holds no secret of the worker's, but it
  writes the job table: a compromised website could queue a resize with a stored key, or
  undo the kill switch (an admin action). It can't open a Hetzner key, sign a licence,
  or point an address anywhere the worker didn't record for that box (the worker's MAC
  over the addresses).
- **The worker holds the website's database password.** Projects can't share a database
  role, so the worker reaches the `cloud_*` tables with the website's `DATABASE_URL`
  (`CONTROL_DATABASE_URL`). The worker is the more trusted side; a scoped role would need
  the box's superuser and is planned with per-project grants.
- **Release downloads need a reachable release source.** The worker installs the newest
  `stable` release from `release.DefaultSource` (or `CLOUD_RELEASE_SOURCE`); while the
  repository's releases are private, set that to a URL the worker can read.
- **The address goes only after a delivered warning.** The grace removal waits for the
  "goes soon" email to be accepted by our mail server (SMTP accepted it: a later bounce
  isn't seen) and for 7 days after that; a warning that failed all its tries is sent again
  a day later, and the address stays until one gets through. Parking (no check-ins) and
  the kill switch don't wait for an email.
- **Checkout requests are saved before they are sent.** The idempotency key and exact
  parameters go to the database first, so a retry replays the same request. A saved
  request Stripe refused as invalid (it never ran there) is replaced by a fresh one; one
  that is no longer useful (its session would expire within two minutes) too. A new
  request asks for a session of 35 minutes (Stripe's minimum is 30), fixed when the
  request is saved, so a slow commit or a retry within five minutes still goes through.
- **Stripe cancellations and refunds are never given up on.** The cancel and refund of a
  duplicate subscription (and the money-back one) is retried until Stripe takes it, at
  most an hour apart; one still not done an hour after it was queued is emailed to the
  admin (`CLOUD_ABUSE_NOTIFY`, else `EARLY_ACCESS_NOTIFY`) once. Emails still stop after
  ten tries.
- **Billing is cards only.** Checkout offers cards (and Link with `STRIPE_CHECKOUT_LINK=1`),
  so a box is set up only after its first payment went through; bank debits and other
  methods that confirm days later are off until the setup can wait for them.
- **Refunds outside the guarantee are manual.** The admin page's *Refund and cancel* is
  the 14-day money-back (the first payment, in full). Other refunds are made in Stripe;
  a full refund of the first payment there ends the subscription too.
- **Founding offer counter.** The first 100 paid boxes get the coupon. Our own count is
  checked when Checkout opens, so two people at the 100th can both be offered it; the
  coupon's own limit in Stripe (100 redemptions) is the hard stop.
