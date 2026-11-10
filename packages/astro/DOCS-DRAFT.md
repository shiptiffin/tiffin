<!--
Draft for docs/guide/apps.md: a subsection to go under the "## Astro" section that another branch
is adding. Not edited in place, because that branch is editing docs/guide. Delete this file once the
text is merged.
-->

### The `@shiptiffin/astro` integration

Add one line to `astro.config.mjs` and a site with server routes runs well on the box:

```sh
npm i @shiptiffin/astro @astrojs/node @shiptiffin/sdk
```

```js
// astro.config.mjs
import { defineConfig } from "astro/config";
import tiffin from "@shiptiffin/astro";

export default defineConfig({ integrations: [tiffin()] });
```

During a build it does the following. It leaves every setting your config already makes alone:

- **Adapter.** It adds `@astrojs/node` (standalone) when a page has `export const prerender = false`,
  `output` is `"server"`, or the site uses server islands or Actions. A site where every page is
  prerendered stays static.
- **Proxy trust.** It adds the app's hosts (`TIFFIN_HOSTS`, else the host of `TIFFIN_URL`) to
  `security.allowedDomains`. `Astro.url`, redirects and the origin check on form posts then see
  the address the visitor used. Without this, Astro ignores the edge's `X-Forwarded-Host`. Your
  own entries are kept first.
- **`site`.** It sets `site` from `TIFFIN_URL` when the config has none.
- **Sessions.** It stores Astro sessions in the project's Valkey
  (`VALKEY_PREFIX` + `sess:<id>`). Each read or write renews a session, which expires after 30
  days without use, or after your `session.ttl`. `@astrojs/node`'s default stores sessions on the
  container's disk, so a deploy loses them and two instances don't share them.
- **Route cache.** When the site uses `Astro.cache` or `routeRules`, it stores cached pages in
  Valkey. Every instance shares them, and they outlive deploys. `invalidate({ tags })` and
  `invalidate({ path })` reach every instance. Previews have their own entries. A Valkey error
  counts as a miss.
- **Health route.** It adds `/_health` (`/_health/` with `trailingSlash: "always"`), which returns
  200 JSON from the Astro server. Set `healthcheck: "/_health"` for the app. The box's default
  `/` is usually a prerendered page, which the box serves as a file without the server.
  `health: { database: true }` also checks that Postgres answers.
- **Graceful shutdown.** It wraps `dist/server/entry.mjs`. When SIGTERM arrives, open requests
  finish (for up to 25 seconds) instead of being cut off.
- **`ASTRO_KEY`.** It warns at build time when the site uses server islands and `ASTRO_KEY` is
  not set.

Each feature has an option (`tiffin({ cache: false, health: { path: "/healthz" } })`) and logs one
line saying what it did. Away from the box (`astro dev`, a build on your laptop), the box's
variables are missing, so the features that need them do nothing, and the build is the same as
without the integration. `allowedDomains` and `site` are fixed at build time. Builds on the box
see the app's environment, so after you add a domain, redeploy. For sign-in, use
`authMiddleware` from `@shiptiffin/sdk/astro` in `src/middleware.ts`. The integration's `auth`
option is reserved for a later version.

All options: [packages/astro/README.md](../../packages/astro/README.md).
