# @shiptiffin/astro

An Astro integration for sites on a [ShipTiffin](https://shiptiffin.com) box. A site with server
routes then runs on the box without any further config.

```sh
npm i @shiptiffin/astro @astrojs/node @shiptiffin/sdk
```

```js
// astro.config.mjs
import { defineConfig } from "astro/config";
import tiffin from "@shiptiffin/astro";

export default defineConfig({ integrations: [tiffin()] });
```

Requires Astro 7.2 or later. `@astrojs/node` is needed only for sites with server routes, and
`@shiptiffin/sdk` only for sessions and the route cache in Valkey.

## What it does

Each feature is on by default and has its own option. A feature turns itself off and logs one
line when what it needs is missing, so `astro dev` and builds on your own computer work as before.
Settings in your own config always win.

| Option | Default | What it does | Needs |
|---|---|---|---|
| `adapter` | `true` | Adds `@astrojs/node` in standalone mode when you have no adapter and some route renders on demand. Leaves your adapter alone; warns once if it isn't `@astrojs/node`. | `@astrojs/node` installed |
| `proxy` | `true` | Adds the app's hosts to `security.allowedDomains`, so `Astro.url`, `checkOrigin` and redirects use the address the visitor used, not the container's. | `TIFFIN_HOSTS` or `TIFFIN_URL` at build |
| `site` | `true` | Sets `site` from `TIFFIN_URL` when you haven't set it. | `TIFFIN_URL` at build |
| `sessions` | `true` | Stores Astro sessions in the box's Valkey when you haven't chosen a session driver. Replaces `@astrojs/node`'s filesystem default, which loses sessions on every deploy. | `REDIS_URL` at build and run time |
| `cache` | `"auto"` | Stores route caching (`Astro.cache`, `routeRules`) in Valkey, shared by every instance of the app. | `REDIS_URL` at build and run time |
| `health` | `true` | Adds a health route at `/_health` (`/_health/` with `trailingSlash: "always"`). | an adapter |
| `shutdown` | `true` | Graceful shutdown for the `@astrojs/node` standalone server. | `@astrojs/node` |
| `astroKey` | `true` | Warns at build time when the site uses server islands and `ASTRO_KEY` is not set. | |
| `auth` | — | Reserved, see [Auth](#auth). | |

Pass `false` to turn a feature off, for example `tiffin({ cache: false, health: false })`.

### adapter

The adapter has to be picked before Astro has listed the routes, so the integration reads your
`src/` files as text. It adds `@astrojs/node` when it finds any of these:

- a page or endpoint with `export const prerender = false`
- `output: "server"`
- `server:defer` (server islands)
- `src/actions/` (Astro Actions)

A computed `prerender` value doesn't count. When that is the only server route, Astro stops the
build with its own "no adapter" error. Add `adapter: node({ mode: "standalone" })` yourself to fix
it. A site where every page is prerendered gets no adapter, and the box serves it as files.

### proxy

The box's edge forwards `X-Forwarded-Host` and `X-Forwarded-Proto`, and Astro ignores both unless
`security.allowedDomains` names the host. The hosts come from `TIFFIN_HOSTS`, a comma- or
space-separated list such as `example.com, www.example.com`. Entries may be URLs or wildcards
(`*.example.com`). When `TIFFIN_HOSTS` is not set, the integration uses the host of `TIFFIN_URL`.
Hosts without a scheme get the scheme of `TIFFIN_URL`. Nothing else is trusted, and your own
`allowedDomains` entries stay first and unchanged.

`allowedDomains` and `site` are fixed when the site is built. On the box, builds get the same
environment as the running app, so they match. Add a domain and redeploy so that the build sees
the new host.

### sessions

Astro calls the session driver `@shiptiffin/astro/session-driver`. It stores each session at
`VALKEY_PREFIX + "sess:" + <id>`, through the SDK's KV client: one connection per process, shared
with your own `kv()` calls. Each read or write renews the session's expiry, so a session expires
after a period without use. That period is your `session.ttl`, or 30 days. Set it with
`tiffin({ sessions: { ttl: 7 * 86400 } })`.

The connection settings (`REDIS_URL`, `VALKEY_PREFIX`) are read when the server runs and are never
written into the build. If `REDIS_URL` is missing when the server runs, sessions are kept in memory
and a line is logged. `session: false` or a driver you set yourself is left alone.

### cache

`"auto"` turns the cache on when your code calls `Astro.cache.set({ maxAge | swr | tags })` or
`cache.invalidate({ tags | path })`, or when `routeRules` sets `maxAge`, `swr` or `tags`. Use
`cache: true` to turn it on in every case, or pass an options object:

```js
tiffin({ cache: { maxTtl: 86400, maxBodyBytes: 512 * 1024, timeoutMs: 500, query: { exclude: ["utm_*"] } } })
```

It works like Astro's `memoryCache()`, and responses carry `X-Astro-Cache: HIT | STALE | MISS`.
The differences:

- **Shared store.** Entries live in Valkey, so every instance serves them. They outlive
  restarts and deploys, so after a deploy a page can show what the previous release rendered
  until its `maxAge` runs out. To refresh it sooner, call `invalidate()`.
- **Invalidation reaches every instance.** `invalidate({ tags })` and `invalidate({ path })`
  record the time of the invalidation, and an entry rendered before that time is never served again.
  A render that started before the invalidation and finished after it counts as old.
  `path` matches exactly, as in Astro: the path plus the normalised query, on every host.
- **Previews are separate.** Keys include `TIFFIN_APP` and `TIFFIN_PREVIEW`, so previews and
  production never share entries.
- **Limits.** An entry or invalidation is kept for at most `maxTtl` seconds (default 7 days).
  Bodies over `maxBodyBytes` (default 1 MB) are served but not stored. Entries count toward the
  project's Valkey memory limit and, because they all expire, they are the first keys Valkey removes.
- **Failures.** A Valkey error, or a read slower than `timeoutMs` (default 1 s), counts as a miss:
  the page renders, a warning is logged, and Valkey is skipped for 5 seconds. Pages are stored
  after the response is sent. `invalidate()` throws when Valkey can't record the invalidation, so
  your code knows the content is still cached.
- **No caching at build time.** Pages prerendered during the build don't read or write the cache.

While the cache is on, every GET to a server route reads Valkey once (twice when there is an
entry), whether or not that route caches anything. With `"auto"`, sites that don't cache never
pay for this.

### health

`/_health` answers `200 {"ok":true, "app", "deploy", "instance", "uptime"}` with
`Cache-Control: no-store`. The answer comes from the Astro server, unlike a prerendered `/`,
which the box serves as a file. Point the box at it:

```ts
// tiffin.config.ts
apps: { web: { framework: "astro", healthcheck: "/_health" } }
```

The path is `/_health` because the box keeps `/_tiffin/*` for itself (`/_tiffin/stripe`,
`/_tiffin/vitals`, `/_tiffin/runs`, `/_tiffin/opt-out` and more), and its request metrics skip that prefix.

- `health: { database: true }` also checks that Postgres at `DATABASE_URL` answers, and returns 503
  when it doesn't. It sends a Postgres SSL request on a plain socket: no database driver and no
  credentials.
- `health: { path: "/healthz" }` moves the route.
- `health: "/api/health/"` means you already have a health route: nothing is added.

### shutdown

`@astrojs/node`'s standalone server has no SIGTERM handler and keeps its server object to itself.
Its entry does export `startServer()`, and it skips its own start when `ASTRO_NODE_AUTOSTART` is
`"disabled"`, which is what `astro preview` uses. The integration relies on both:

1. Astro writes the adapter's entry as `dist/server/astro-entry.mjs` (`build.serverEntry`).
2. The integration writes `dist/server/entry.mjs`. It imports the adapter's entry with
   autostart off, starts the server, and re-exports `handler`, `startServer` and `options`.
3. On SIGTERM or SIGINT it stops accepting connections, closes idle keep-alive connections, lets
   in-flight requests finish, and exits with code 0.
4. After `timeout` seconds (default 25) it closes the remaining connections. The box sends SIGKILL
   30 seconds after SIGTERM.

The command you run stays `node dist/server/entry.mjs` or `bun dist/server/entry.mjs`. If you set
`build.serverEntry` yourself, the integration leaves the entry alone.

The box already stops sending traffic to an old release and waits for its requests before it sends
SIGTERM. The wrapper covers what is left: a request still open at SIGTERM, a long response, a
`docker stop` or a Ctrl-C. Without the wrapper, Node exits on SIGTERM straight away and cuts those
requests off.

### astroKey

Server islands encrypt their props with `ASTRO_KEY`. Without it, each build makes its own key, so a
page from one deploy can't load islands from the next. The box sets `ASTRO_KEY` for you. To build
somewhere else, run `astro create-key` and use the same key for every build.

### Auth

`auth: { protect: [...], signIn: "/sign-in/" }` is reserved for a later version and does nothing
yet (it logs a warning). For now, use the middleware from `@shiptiffin/sdk/astro`:

```ts
// src/middleware.ts
import { authMiddleware } from "@shiptiffin/sdk/astro";
export const onRequest = authMiddleware({ protect: ["/app/:path*"], signIn: "/sign-in/" });
```

## Environment

| Variable | When | Used for |
|---|---|---|
| `TIFFIN_HOSTS` | build | the app's hosts, for `allowedDomains` |
| `TIFFIN_URL` | build | `site`, and the fallback host and scheme for `allowedDomains` |
| `REDIS_URL`, `VALKEY_PREFIX` | build (on or off) and run time (connection) | sessions, cache |
| `TIFFIN_APP`, `TIFFIN_PREVIEW` | run time | cache key scope |
| `TIFFIN_DEPLOY`, `TIFFIN_INSTANCE` | run time | shown by `/_health` |
| `DATABASE_URL` | run time | `health: { database: true }` |
| `ASTRO_KEY` | build | the server-islands warning (Astro itself reads it) |
| `PORT`, `HOST` | run time | read by `@astrojs/node` (the box sets `HOST=0.0.0.0`) |

## Without the integration

The runtime modules work on their own:

```js
export default defineConfig({
  adapter: node({ mode: "standalone" }),
  session: { driver: { entrypoint: "@shiptiffin/astro/session-driver", config: { ttl: 86400 } } },
  cache: { provider: { entrypoint: "@shiptiffin/astro/cache-provider", config: { maxTtl: 86400 } } },
});
```

## Licence

Apache-2.0
