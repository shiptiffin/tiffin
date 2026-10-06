/**
 * `tiffin-sdk/next/cache-handler`: a Next.js 16 `cacheHandler` (ISR pages,
 * route handlers, `fetch` cache, `unstable_cache`) backed by Valkey, so every
 * instance of an app shares one cache and `revalidateTag` / `revalidatePath`
 * reach all of them.
 *
 * ```js
 * // next.config.mjs
 * export default {
 *   cacheHandler: fileURLToPath(new URL("./cache-handler.mjs", import.meta.url)),
 * };
 * // cache-handler.mjs
 * export { default } from "tiffin-sdk/next/cache-handler";
 * ```
 */
import { readBuildId, Store, warnOnce } from "./store.js";
const TAGS_HEADER = "x-next-cache-tags";
/**
 * An entry's tags: those it was stored with and Next.js passes (fetch), and
 * for pages and route handlers the x-next-cache-tags header, which holds their
 * explicit tags and the implicit _N_T_ path tags revalidatePath uses. That is
 * where Next.js's file-system cache reads them too.
 */
function entryTags(item, ctx) {
    const tags = [...item.meta.tags, ...(ctx.tags ?? []), ...(ctx.softTags ?? [])];
    const header = item.value?.headers?.[TAGS_HEADER];
    if (typeof header === "string" && header)
        tags.push(...header.split(","));
    return tags;
}
const builds = new Map();
/**
 * Next.js makes one handler per request; they share one Store (connection,
 * in-memory copy, tag state). Each request reads the tag counter once.
 */
export class TiffinCacheHandler {
    store;
    synced;
    constructor(nextOptions = {}, storeOptions = {}) {
        let o = storeOptions;
        const dist = nextOptions.serverDistDir;
        if (dist && o.buildId === undefined && !process.env.TIFFIN_DEPLOY && !process.env.NEXT_DEPLOYMENT_ID) {
            if (!builds.has(dist))
                builds.set(dist, readBuildId(`${dist}/..`));
            const id = builds.get(dist);
            if (id)
                o = { ...o, buildId: id };
        }
        this.store = Store.open(o);
    }
    async get(key, ctx = {}) {
        try {
            const s = this.store;
            // The tag counter and the entry in one round trip.
            let [, item] = await Promise.all([(this.synced ??= s.sync()), s.read("e", key)]);
            if (!item)
                return null;
            let tags = entryTags(item, ctx);
            if (s.expired(tags, item.meta.lastModified) || s.stale(tags, item.meta.lastModified)) {
                // Outdated copy: another instance may have rendered it anew already.
                item = await s.read("e", key, true);
                if (!item)
                    return null;
                tags = entryTags(item, ctx);
            }
            const { lastModified } = item.meta;
            const value = item.value;
            if (s.expired(tags, lastModified))
                return null;
            // A stale tag: Next.js sees it in its tag state and serves this entry
            // once while it regenerates. Without that state, regenerate now.
            if (!s.signalsStale && s.stale(tags, lastModified))
                return null;
            return { lastModified, value, tags: item.meta.tags };
        }
        catch (err) {
            warnOnce(`cache get failed, treating as a miss: ${String(err)}`);
            return null;
        }
    }
    async set(key, data, ctx = {}) {
        try {
            if (data == null) {
                await this.store.del("e", key);
                return;
            }
            const tags = ctx.tags ?? (Array.isArray(data.tags) ? data.tags : []);
            const lastModified = Date.now();
            const revalidate = ctx.cacheControl?.revalidate ?? ctx.revalidate ?? data.revalidate;
            const expire = ctx.cacheControl?.expire;
            const meta = { lastModified, tags };
            if (typeof revalidate === "number" && revalidate > 0)
                meta.fresh = lastModified + revalidate * 1000;
            await this.store.write("e", key, meta, data, typeof expire === "number" && expire > 0 ? expire : this.store.maxTtl);
        }
        catch (err) {
            warnOnce(`cache set failed (the response was still served): ${String(err)}`);
        }
    }
    async revalidateTag(tags, durations) {
        try {
            await this.store.updateTags(Array.isArray(tags) ? tags : [tags], durations);
        }
        catch (err) {
            warnOnce(`revalidating tags failed: ${String(err)}`);
        }
    }
    resetRequestCache() { }
}
export default TiffinCacheHandler;
