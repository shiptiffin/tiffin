# tiffin-sdk/next

Next.js on a Tiffin box: Railpack builds the app (`next build`), and it runs as a
long-lived server on Bun (`bun --bun next start`, Bun 1.4.2+; Bun 1.3 cannot run
Next 16.3 with `--bun`). With two or more instances, give them one shared cache so
`revalidateTag` and `revalidatePath` reach all of them. That is what these handlers do,
in Valkey.

## Setup

1. Add Valkey to the project so the box sets `REDIS_URL` (and `VALKEY_PREFIX`) for your apps:

   ```ts
   // tiffin.config.ts
   export default defineConfig({
     project: "shop",
     apps: { web: { framework: "next", instances: 2 } },
     services: { valkey: {} },
   });
   ```

2. Point Next.js at the handlers:

   ```js
   // next.config.mjs
   import { fileURLToPath } from "node:url";
   const here = (p) => fileURLToPath(new URL(p, import.meta.url));

   export default {
     cacheHandler: here("./cache-handler.mjs"),   // ISR, route handlers, fetch, unstable_cache, images
     cacheMaxMemorySize: 0,                       // Valkey holds the cache
     // With cacheComponents ("use cache"), also:
     // cacheHandlers: { default: here("./use-cache-handler.mjs"), remote: here("./use-cache-handler.mjs") },
   };
   ```

   ```js
   // cache-handler.mjs
   export { default } from "tiffin-sdk/next/cache-handler";
   // use-cache-handler.mjs
   export { default } from "tiffin-sdk/next/use-cache";
   ```

   Until `tiffin-sdk` is published on npm, templates/hello-next carries a bundled copy
   (`bun run sync-cache-handler` regenerates it from this package).

3. Revalidate as usual: `revalidateTag("posts", { expire: 0 })`, `revalidatePath("/blog")`.

## How it works

- Entries live under `<VALKEY_PREFIX>next:<app>:<BUILD_ID>:`, so a new deploy never serves
  HTML rendered against another build's assets, and a rollback finds its own cache again.
- Tag revalidation times live in one hash, `<VALKEY_PREFIX>next:<app>:tags`, shared by all
  builds and instances. A cached entry older than any of its tags (or its path's soft tags)
  is a miss.
- Without `REDIS_URL` (local `next dev`, `next build`, or a project without Valkey) the
  handlers fall back to an in-memory store: correct for one process, not shared.
- Valkey errors never break a page: a failed read is a miss, a failed write is skipped.
- Not on Bun? Pass a client: `configure({ client: new Redis(process.env.REDIS_URL) })`
  (ioredis or node-redis) in the handler file before re-exporting.
- `revalidateTag(tag, "max")` (stale-while-revalidate) is treated as an immediate expiry.
