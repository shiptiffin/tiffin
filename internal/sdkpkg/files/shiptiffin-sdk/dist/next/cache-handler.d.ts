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
/** An optimized image as Next.js's image optimizer stores it (kind IMAGE). */
interface ImageValue {
    kind: "IMAGE";
    etag: string;
    buffer: Buffer;
    extension: string;
    upstreamEtag: string;
    revalidate?: number;
}
/**
 * Optimized images (kind IMAGE) on disk, never in Valkey: they are large,
 * depend only on the source and its parameters (not the deploy), and the box
 * mounts .next/cache/images per app environment, shared by its instances and
 * kept across deploys. Next.js 16.2+ hands images to a cacheHandler only when
 * the app sets `images.customCacheHandler`; by default its own disk cache
 * keeps them in the same directory. Entries use Next.js's own layout,
 * `<key>/<maxAge>.<expireAt>.<etag>.<upstreamEtag>.<extension>`, so either
 * cache reads what the other wrote, and the directory is bounded by
 * `images.maximumDiskCacheSize` (least recently used out first), as Next.js
 * bounds it. Every instance of the app writes to the directory, so a write
 * reads it afresh when the last look is older than `rescanMs`: the cap then
 * counts the other instances' files too (it holds to within what they
 * write in that time).
 */
export declare class ImageDiskCache {
    readonly dir: string;
    /** Byte cap; undefined: half the free disk, as Next.js does; 0: nothing is kept. */
    readonly maxBytes: number | undefined;
    /** How old the view of the directory may get before a write reads it again. */
    readonly rescanMs: number;
    private lru;
    private scanned;
    private bytes;
    constructor(dir: string, 
    /** Byte cap; undefined: half the free disk, as Next.js does; 0: nothing is kept. */
    maxBytes: number | undefined, 
    /** How old the view of the directory may get before a write reads it again. */
    rescanMs?: number);
    private entries;
    private cap;
    get(key: string): Promise<Entry | null>;
    set(key: string, value: ImageValue | null, revalidate: number): Promise<void>;
}
/**
 * Next.js makes one handler per request; they share one Store (connection,
 * in-memory copy, tag state). Each request reads the tag counter once.
 */
export declare class TiffinCacheHandler {
    readonly store: Store;
    private synced;
    private readonly files;
    private readonly images;
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
