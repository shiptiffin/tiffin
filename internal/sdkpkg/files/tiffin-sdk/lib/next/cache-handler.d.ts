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
    /** Next.js's file-system interface, which its own file-system cache reads build output with. */
    fs?: unknown;
    [k: string]: unknown;
}
/**
 * Next.js makes one handler per request; they share one Store (connection,
 * in-memory copy, tag state). Each request reads the tag counter once.
 */
export declare class TiffinCacheHandler {
    readonly store: Store;
    private synced;
    private readonly files;
    constructor(nextOptions?: HandlerOptions, storeOptions?: StoreOptions);
    get(key: string, ctx?: GetContext): Promise<Entry | null>;
    /**
     * What `next build` prerendered for key, when Valkey has nothing yet. It is
     * copied into Valkey with the build's time (unless an instance stored a
     * rendering meanwhile), so later requests and the other instances read it there.
     */
    private prerendered;
    set(key: string, data: Record<string, unknown> | null, ctx?: SetContext): Promise<void>;
    revalidateTag(tags: string | string[], durations?: {
        expire?: number;
    }): Promise<void>;
    resetRequestCache(): void;
}
export default TiffinCacheHandler;
