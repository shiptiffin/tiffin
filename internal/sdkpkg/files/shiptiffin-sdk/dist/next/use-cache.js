/**
 * `@shiptiffin/sdk/next/use-cache`: a Next.js 16 `cacheHandlers` handler for the
 * `"use cache"` directive, backed by Valkey and sharing tag revalidation
 * with the `cacheHandler` in `@shiptiffin/sdk/next/cache-handler`.
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
 * export { default } from "@shiptiffin/sdk/next/use-cache";
 * ```
 */
import { Store, warnOnce } from "./store.js";
/** Creates a handler; the default export is one with default options. */
export function createUseCacheHandler(o = {}) {
    let store;
    const s = () => (store ??= Store.open(o));
    const pending = new Map();
    return {
        // Like Next.js's own handlers, with stale-while-revalidate kept: an entry
        // past `revalidate` (or with a tag marked stale, revalidate -1) is served
        // while Next.js regenerates it in the background; past `expire`, or with
        // an expired tag (its own or the page's implicit ones), it is a miss.
        async get(cacheKey, softTags) {
            await pending.get(cacheKey);
            try {
                // refreshTags() ran for this request, so the tag state is current.
                let item = await s().read("u", cacheKey);
                if (!item)
                    return undefined;
                const tagsOf = (m) => (softTags.length > 0 ? [...m.tags, ...softTags] : m.tags);
                if (s().expired(tagsOf(item.meta), item.meta.timestamp) || s().stale(tagsOf(item.meta), item.meta.timestamp)) {
                    // Outdated copy: another instance may have regenerated it already.
                    item = await s().read("u", cacheKey, true);
                    if (!item)
                        return undefined;
                }
                const m = item.meta;
                const tags = tagsOf(m);
                if (Date.now() > m.timestamp + m.expire * 1000)
                    return undefined;
                if (s().expired(tags, m.timestamp))
                    return undefined;
                const bytes = item.value;
                return {
                    value: new ReadableStream({
                        start(c) {
                            c.enqueue(new Uint8Array(bytes));
                            c.close();
                        },
                    }),
                    tags: m.tags,
                    stale: m.stale,
                    timestamp: m.timestamp,
                    expire: m.expire,
                    revalidate: s().stale(tags, m.timestamp) ? -1 : m.revalidate,
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
                // expire 0 is dynamic: Next.js regenerates it on every read anyway.
                if (entry.expire === 0)
                    return;
                const value = Buffer.concat(chunks);
                const meta = {
                    tags: entry.tags,
                    stale: entry.stale,
                    timestamp: entry.timestamp,
                    expire: entry.expire,
                    revalidate: entry.revalidate,
                    fresh: entry.timestamp + entry.revalidate * 1000,
                };
                const ttl = Number.isFinite(entry.expire) && entry.expire > 0 ? entry.expire : s().maxTtl;
                await s().write("u", cacheKey, meta, value, ttl);
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
                await s().sync();
            }
            catch (err) {
                warnOnce(`refreshing tags failed: ${String(err)}`);
            }
        },
        // Infinity tells Next.js that get() checks the implicit (path) tags itself.
        async getExpiration() {
            return Infinity;
        },
        async updateTags(tags, durations) {
            try {
                await s().updateTags(tags, durations);
            }
            catch (err) {
                warnOnce(`revalidating tags failed: ${String(err)}`);
            }
        },
    };
}
const handler = createUseCacheHandler();
export default handler;
