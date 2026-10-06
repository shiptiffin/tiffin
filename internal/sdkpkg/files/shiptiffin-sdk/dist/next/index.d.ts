/**
 * `@shiptiffin/sdk/next`: Next.js on Tiffin.
 *
 * - `cache-handler` — Next.js `cacheHandler` (ISR, route handlers, fetch) in Valkey
 * - `use-cache`     — Next.js `cacheHandlers` for the "use cache" directive, in Valkey
 * - `configure()`   — pass your own Redis client (ioredis / node-redis) or prefix
 *
 * Both handlers share tag revalidation: `revalidateTag("posts")` or
 * `revalidatePath("/blog")` on any instance invalidates the entry everywhere.
 * See README.md next to this file for setup.
 */
export { TiffinCacheHandler } from "./cache-handler.js";
export { createUseCacheHandler, type CacheEntry, type UseCacheHandler } from "./use-cache.js";
export { configure, defaultClient, memoryRedis, toRedisLike, readBuildId, Store, type RedisLike, type StoreOptions, type TagState } from "./store.js";
export { RespClient } from "../resp.js";
