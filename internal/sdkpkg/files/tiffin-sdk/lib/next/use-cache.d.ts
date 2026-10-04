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
import { type StoreOptions } from "./store.js";
/** Next.js's CacheEntry. */
export interface CacheEntry {
    value: ReadableStream<Uint8Array>;
    tags: string[];
    stale: number;
    timestamp: number;
    expire: number;
    revalidate: number;
}
export interface UseCacheHandler {
    get(cacheKey: string, softTags: string[]): Promise<CacheEntry | undefined>;
    set(cacheKey: string, pendingEntry: Promise<CacheEntry>): Promise<void>;
    refreshTags(): Promise<void>;
    getExpiration(tags: string[]): Promise<number>;
    updateTags(tags: string[], durations?: {
        expire?: number;
    }): Promise<void>;
}
/** Creates a handler; the default export is one with default options. */
export declare function createUseCacheHandler(o?: StoreOptions): UseCacheHandler;
declare const handler: UseCacheHandler;
export default handler;
