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
| Astro, static | First-class | Built to files and served by the edge. |
| Astro with `@astrojs/node` | Detected | Runs `dist/server/entry.mjs` as a server. |
| Vite + React (SPA), plain HTML | First-class | Static site; client-side routers get an `index.html` fallback. |
| Any other Bun or Node.js server (Express, Elysia, Fastify...) | Detected | Must listen on `$PORT`. |
| Flask, Django, Litestar and other Python servers | Detected (`python`) | Started by your `command`, or Railpack's guess. |
| Go, Rust, anything else | Dockerfile only | |
| SvelteKit, Nuxt, React Router (framework mode), Remix, SolidStart, TanStack Start for Solid | Not yet | Planned. |
| Astro with the Vercel, Netlify or Cloudflare adapter | Not yet | Switch to `@astrojs/node`, or build it static. |

**Bun by default.** JavaScript apps build and run on Bun. Some things only work on Node.js;
set `runtime: "node"` for them:

- native modules built for Node.js, and libraries that lean on Node internals;
- React Router's own server (`react-router-serve`, on Express) is slow on Bun: if you run
  React Router from a Dockerfile, run it on Node.js.

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

Not yet: serving pages from the edge while the app sleeps, or Early Hints during a wake.

## Email

- **Sending** goes through a mail provider you bring (Resend, Postmark, SES, SendGrid,
  Mailgun, Brevo or any SMTP relay). Without one, mail goes to the dev inbox.
- **Port 25 is blocked for apps:** their mail goes through the box (`SMTP_URL`).
- **Hetzner blocks outbound port 465** on new servers (and 25). Use a relay on port 587
  with STARTTLS; Cloudflare Email Service's SMTP is on 465, so it doesn't work there.
- **No inbound mail yet:** the box can't receive email for your domains.
- Each project may send 300 messages an hour by default.

## Sign-in

- **Your apps' sign-in providers:** Google is tested end to end. GitHub, Apple,
  Microsoft, Discord, Facebook, X, LinkedIn, GitLab, Slack, Twitch and generic OpenID
  Connect are wired up but not tested yet.
- **Dashboard sign-in** with a provider supports only Google and GitHub, and only with the
  box-wide keys, not a project's own.
- People are matched by email, not by the provider's account ID: if someone changes the
  email on their Google or GitHub account, change it on the box too.
- A provider sign-in in progress fails if the box restarts (its signing key is kept in
  memory). Start it again.
- Settings doesn't show which provider a person signed in with before.
- **Previews share real users.** A preview signs in against the project's own accounts:
  anyone who signs up on a preview is a user of the app, and a preview's emails reach
  real people. Treat a preview of someone else's branch as you would deploying it.

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

## Limits per project and per box

| | Default | Change it |
|---|---|---|
| Project memory and CPU | Shares the box; protected up to a fair share | `resources` (`memoryMB`, `cpus`, `maxSharePercent`) |
| Postgres query time | 5 minutes; 30 s in a project with a limit | `services.postgres.statementTimeoutSeconds` |
| Postgres connections | 80 per project; a limited project gets its share of 100 | |
| Idle in transaction | closed after 60 s | |
| KV memory (Valkey) | 64 MB, held while the project has a limit | `maxMemoryMB` |
| Storage (databases + files) | no limit; the disk guard warns at 85% and makes the fastest-growing project read-only at 95% | `tiffin storage quota set` |
| Request time | 15 minutes, up to 24 hours | `timeoutSeconds` |
| Queue job attempt | 60 s without a response or heartbeat (5 to 3600); heartbeats extend it up to 24 hours | `leaseSeconds` |
| Cron call | 60 s (5 to 3600) | `timeoutSeconds` on the cron |
| Calls to web addresses (`url` crons and queues) | 600 a minute per project | `TIFFIN_QUEUE_URL_RATE` on the box |
| Live progress streams | 200 open per project | |
| Email | 300 messages an hour | `tiffin email rate-limit set` |
| Rollbacks | the last 3 production deploys; previews keep none | |
| Previews | deleted after 7 days with no request or deploy | |
| Build cache | 15% of the data disk (4 to 20 GiB) | |

Everything runs on **one machine**: if the box is down, your apps are down. Backups stay on
the box unless you [copy them off it](data.md#copies-off-the-box).

## Usage and observability

- Usage trends cover only the last hour.
- Per-project image sizes count layers that images share once per project, so the
  projects' totals can add up to more than the image store.
- A token without box-wide read access can't see the disk breakdown.
- Database snapshots of deleted projects are kept for 7 days.

## Caching and images

- No edge response cache (ISR, `s-maxage`, `stale-while-revalidate`) for frameworks other
  than Next.js yet.
- No shared image optimiser at the edge yet: each app optimises its own images (Next.js
  with sharp).
- Prerendered pages of non-Next frameworks are served by the app, not the edge.

All three are planned.

## Analytics

Not yet: funnels and retention, goals, share links, excluding your own visits, and
automatic events from sign-ups and deploys.
