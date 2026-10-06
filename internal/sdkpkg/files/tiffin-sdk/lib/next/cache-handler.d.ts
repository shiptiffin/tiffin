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
import { Store, type StoreOptions } from "./store.js";
/** What Next.js passes to get(). */
export interface GetContext {
    kind?: string;
    tags?: string[];
    softTags?: string[];
    [k: string]: unknown;
}
/** What Next.js passes to set(). */
export interface SetContext {
    tags?: string[];
    revalidate?: number | false;
    cacheControl?: {
        revalidate?: number | false;
        expire?: number;
    };
    [k: string]: unknown;
}
/** One stored entry, as Next.js expects get() to return it. */
export interface Entry {
    lastModified: number;
    value: Record<string, unknown> | null;
    tags?: string[];
}
/** Options Next.js passes to the constructor (the parts we use). */
export interface HandlerOptions {
    serverDistDir?: string;
    [k: string]: unknown;
}
/**
 * Next.js makes one handler per request; they share one Store (connection,
 * in-memory copy, tag state). Each request reads the tag counter once.
 */
export declare class TiffinCacheHandler {
    readonly store: Store;
    private synced;
    constructor(nextOptions?: HandlerOptions, storeOptions?: StoreOptions);
    get(key: string, ctx?: GetContext): Promise<Entry | null>;
    set(key: string, data: Record<string, unknown> | null, ctx?: SetContext): Promise<void>;
    revalidateTag(tags: string | string[], durations?: {
        expire?: number;
    }): Promise<void>;
    resetRequestCache(): void;
}
export default TiffinCacheHandler;
