# @shiptiffin/sdk/next

Next.js on a Tiffin box: Railpack builds the app (`next build`), and it runs as a
long-lived server on Bun (`bun --bun next start`, Bun 1.4.2+; Bun 1.3 cannot run
Next 16.3 with `--bun`). With two or more instances, give them one shared cache so
`revalidateTag` and `revalidatePath` reach all of them. That is what these handlers do,
in Valkey.

## Setup

On a Tiffin box there is nothing to set up: add Valkey to the project so the box sets
`REDIS_URL` (and `VALKEY_PREFIX`), and its Next.js adapter (Next.js 16.2+) points
`cacheHandler` and `cacheHandlers` at these handlers on the next deploy, unless
next.config sets its own.

```ts
// tiffin.config.ts
export default defineConfig({
  project: "shop",
  apps: { web: { framework: "next", instances: 2 } },
  services: { valkey: {} },
});
```

Revalidate as usual: `revalidatePath("/blog")`, `revalidateTag("posts", "max")`,
`updateTag("posts")` in a Server Action.

Elsewhere (a prebuilt image, or your own next.config), wire them by hand:

```js
// next.config.mjs
import { fileURLToPath } from "node:url";
const here = (p) => fileURLToPath(new URL(p, import.meta.url));

export default {
  cacheHandler: here("./cache-handler.mjs"),   // ISR, route handlers, fetch, unstable_cache
  // With cacheComponents ("use cache"), also:
  // cacheHandlers: { default: here("./use-cache-handler.mjs"), remote: here("./use-cache-handler.mjs") },
};
```

```js
// cache-handler.mjs
export { default } from "@shiptiffin/sdk/next/cache-handler";
// use-cache-handler.mjs
export { default } from "@shiptiffin/sdk/next/use-cache";
```

## How it works

- Entries live under `<VALKEY_PREFIX>next:<app>:<env>:<deploy>:`, where `<env>` is `prod` or
  `pr-<preview>` and `<deploy>` is `TIFFIN_DEPLOY`. (With `deploymentId` set, Next.js gives
  every build the same BUILD_ID, so it cannot tell releases apart.) A new release or a
  preview never serves pages another one rendered, and a rollback finds its own entries again.
- Prerendered output survives the build: `next build` renders into an in-memory store,
  but writes every prerendered page and route handler to `.next/server` as well. On a miss
  the handler reads those files with Next.js's own file-system cache (the same files, PPR
  shells and segments, with the files' time as `lastModified`) and copies the entry into
  Valkey unless an instance already stored a newer one, so the first request after a
  deploy is served, not rendered, and ISR ages count from the build.
- Tag revalidations live in `<VALKEY_PREFIX>next:<app>:<env>:tags`, shared by every
  deploy and instance of the environment; a preview's revalidations never reach
  production. Fields older than the longest entry lifetime (30 days) are pruned.
- Pages and route handlers are checked against the tags in their `x-next-cache-tags`
  header, which includes the implicit path tags, so `revalidatePath` and `revalidateTag`
  reach ISR pages and cached GET route handlers, as with Next.js's own file-system cache.
- Revalidation works as in Next.js 16: `revalidatePath`, `updateTag` and
  `revalidateTag(tag, { expire: 0 })` expire entries now; `revalidateTag(tag, "max")` (or
  another profile) marks them stale, so the next request gets the old entry once while Next.js
  regenerates it, and they expire after the profile's `expire`. `updateTag` reads its own
  write: the Server Action's response and every later request see fresh data.
- Each process keeps a bounded copy of recent entries (32 MB; `configure({ memoryBytes })`),
  checked against the tags on every use and read again from Valkey once past `revalidate`
  (or after 10 s), so instances converge on one rendering. A request costs one small read
  of a revalidation counter, sent together with the entry read on a miss; the tag hash is
  read only when the counter moved. Page bodies are stored as bytes, not base64 JSON.
- The handlers talk to Valkey with a small built-in client, on Bun or Node (`next start`
  under plain Node keeps the shared cache). Without `REDIS_URL` (local `next dev`,
  `next build`, or a project without Valkey) they use an in-memory store: correct for one
  process, not shared.
- Valkey errors never break a page: a failed read is a miss, a failed write is skipped. A
  Valkey that does not answer within 2 s counts as failed.
- Your own client: `configure({ client: new Redis(process.env.REDIS_URL) })` (ioredis,
  node-redis or Bun's RedisClient) in the handler file before re-exporting. Entries are then
  stored base64-encoded, since those clients return text.
