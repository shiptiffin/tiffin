/**
 * `tiffin-sdk/next/use-cache`: a Next.js 16 `cacheHandlers` handler for the
 * `"use cache"` directive, backed by Valkey and sharing tag revalidation
 * with the `cacheHandler` in `tiffin-sdk/next/cache-handler`.
 *
 * ```js
 * // next.config.mjs
 * export default {
 *   cacheComponents: true,
 *   cacheHandlers: {
 *     default: fileURLToPath(new URL("./use-cache-handler.mjs", import.meta.url)),
 *     remote: fileURLToPath(new URL("./use-cache-handler.mjs", import.meta.url)),
 *   },
 * };
 * // use-cache-handler.mjs
 * export { default } from "tiffin-sdk/next/use-cache";
 * ```
 */
import { Store, warnOnce } from "./store.js";
/** Creates a handler; the default export is one with default options. */
export function createUseCacheHandler(o = {}) {
    let store;
    const s = () => (store ??= new Store(o));
    const pending = new Map();
    return {
        async get(cacheKey, softTags) {
            await pending.get(cacheKey);
            try {
                const e = await s().getJSON(s().entryKey("u", cacheKey));
                if (!e)
                    return undefined;
                if (Date.now() > e.timestamp + e.revalidate * 1000)
                    return undefined;
                const tags = [...e.tags, ...softTags];
                if (tags.length > 0 && (await s().tagsExpiredAt(tags)) > e.timestamp)
                    return undefined;
                const bytes = e.value;
                return {
                    value: new ReadableStream({
                        start(c) {
                            c.enqueue(new Uint8Array(bytes));
                            c.close();
                        },
                    }),
                    tags: e.tags,
                    stale: e.stale,
                    timestamp: e.timestamp,
                    expire: e.expire,
                    revalidate: e.revalidate,
                };
            }
            catch (err) {
                warnOnce(`"use cache" get failed, treating as a miss: ${String(err)}`);
                return undefined;
            }
        },
        async set(cacheKey, pendingEntry) {
            let done;
            pending.set(cacheKey, new Promise((r) => (done = r)));
            try {
                const entry = await pendingEntry;
                const chunks = [];
                const reader = entry.value.getReader();
                for (;;) {
                    const { done: end, value } = await reader.read();
                    if (end)
                        break;
                    chunks.push(value);
                }
                const value = Buffer.concat(chunks.map((c) => Buffer.from(c)));
                const ttl = Number.isFinite(entry.expire) && entry.expire > 0 ? entry.expire : s().maxTtl;
                await s().setJSON(s().entryKey("u", cacheKey), { value, tags: entry.tags, stale: entry.stale, timestamp: entry.timestamp, expire: entry.expire, revalidate: entry.revalidate }, ttl);
            }
            catch (err) {
                // A failed render or store just means a miss next time.
                warnOnce(`"use cache" set failed: ${String(err)}`);
            }
            finally {
                pending.delete(cacheKey);
                done();
            }
        },
        async refreshTags() {
            try {
                await s().refreshTags();
            }
            catch (err) {
                warnOnce(`refreshing tags failed: ${String(err)}`);
            }
        },
        async getExpiration(tags) {
            let max = 0;
            for (const t of tags)
                max = Math.max(max, s().tagTimes.get(t) ?? 0);
            return max;
        },
        async updateTags(tags) {
            try {
                await s().revalidate(tags);
            }
            catch (err) {
                warnOnce(`revalidating tags failed: ${String(err)}`);
            }
        },
    };
}
const handler = createUseCacheHandler();
export default handler;
