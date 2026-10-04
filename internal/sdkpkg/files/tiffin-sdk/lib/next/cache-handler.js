/**
 * `tiffin-sdk/next/cache-handler`: a Next.js 16 `cacheHandler` (ISR pages,
 * route handlers, `fetch` cache, optimized images) backed by Valkey, so every
 * instance of an app shares one cache and `revalidateTag` / `revalidatePath`
 * reach all of them.
 *
 * ```js
 * // next.config.mjs
 * export default {
 *   cacheHandler: fileURLToPath(new URL("./cache-handler.mjs", import.meta.url)),
 *   cacheMaxMemorySize: 0, // let Valkey hold the cache
 * };
 * // cache-handler.mjs
 * export { default } from "tiffin-sdk/next/cache-handler";
 * ```
 */
import { readBuildId, Store, warnOnce } from "./store.js";
export class TiffinCacheHandler {
    store;
    constructor(nextOptions = {}, storeOptions = {}) {
        const fromDist = nextOptions.serverDistDir ? readBuildId(`${nextOptions.serverDistDir}/..`) : undefined;
        this.store = new Store(fromDist ? { buildId: fromDist, ...storeOptions } : storeOptions);
    }
    async get(key, ctx = {}) {
        try {
            const entry = await this.store.getJSON(this.store.entryKey("e", key));
            if (!entry)
                return null;
            const tags = [...(entry.tags ?? []), ...(ctx.tags ?? []), ...(ctx.softTags ?? [])];
            if (tags.length > 0 && (await this.store.tagsExpiredAt(tags)) > entry.lastModified) {
                return null; // a tag (or path) was revalidated after this was stored
            }
            return entry;
        }
        catch (err) {
            warnOnce(`cache get failed, treating as a miss: ${String(err)}`);
            return null;
        }
    }
    async set(key, data, ctx = {}) {
        try {
            const k = this.store.entryKey("e", key);
            if (data == null) {
                await this.store.del(k);
                return;
            }
            const tags = ctx.tags ?? (Array.isArray(data.tags) ? data.tags : []);
            const entry = { lastModified: Date.now(), value: data, tags };
            await this.store.setJSON(k, entry, this.store.maxTtl);
        }
        catch (err) {
            warnOnce(`cache set failed (the response was still served): ${String(err)}`);
        }
    }
    async revalidateTag(tags, _durations) {
        // Entries tagged before now become misses on every instance.
        await this.store.revalidate(Array.isArray(tags) ? tags : [tags]);
    }
    resetRequestCache() { }
}
export default TiffinCacheHandler;
