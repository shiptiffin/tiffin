/**
 * The storage behind `tiffin-sdk/next`'s cache handlers: entries and tag
 * revalidation timestamps in Valkey, shared by every instance of the app.
 *
 * Keys (all under VALKEY_PREFIX, the project's ACL prefix):
 *   <prefix>next:<app>:<build>:e:<key>   incremental cache entries (ISR, route handlers, fetch, images)
 *   <prefix>next:<app>:<build>:u:<key>   "use cache" entries
 *   <prefix>next:<app>:tags              hash: tag → last revalidation (ms since epoch)
 *
 * Entries are namespaced by the Next.js build ID, so a new deploy never
 * serves pages rendered against another build's assets, and a rollback finds
 * its own build's cache again. Tags are shared across builds.
 */
/** The subset of a Redis client the handlers use: Bun's RedisClient as is. */
export interface RedisLike {
    send(command: string, args: string[]): Promise<unknown>;
}
/** Wraps an ioredis client (`call`) or a node-redis v4+ client (`sendCommand`). */
export declare function toRedisLike(client: unknown): RedisLike;
/**
 * An in-memory stand-in supporting exactly the commands the handlers send.
 * Used when no REDIS_URL is set (local dev, `next build`): caching then
 * works per process, like Next.js's default.
 */
export declare function memoryRedis(): RedisLike;
/**
 * The client the handlers use by default: Bun's built-in RedisClient on
 * REDIS_URL (Tiffin sets it when the project has `services.valkey`), or an
 * in-memory store when REDIS_URL is unset or the process is not Bun.
 */
export declare function defaultClient(): RedisLike;
export declare function warnOnce(msg: string): void;
/** Settings shared by both handlers. */
export interface StoreOptions {
    /** Defaults to defaultClient(). */
    client?: RedisLike;
    /** Key prefix. Default: VALKEY_PREFIX + "next:" + TIFFIN_APP + ":". */
    prefix?: string;
    /** Build ID used to namespace entries. Default: .next/BUILD_ID, else TIFFIN_DEPLOY. */
    buildId?: string;
    /** Upper bound for entry TTLs in seconds. Default 30 days. */
    maxTtlSeconds?: number;
}
/** Configure both handlers, e.g. to pass your own client. Call before Next.js starts serving. */
export declare function configure(o: StoreOptions): void;
export declare class Store {
    readonly client: RedisLike;
    readonly prefix: string;
    readonly buildId: string;
    readonly maxTtl: number;
    /** Local copy of tag timestamps, refreshed from Valkey. */
    readonly tagTimes: Map<string, number>;
    constructor(o?: StoreOptions);
    entryKey(kind: "e" | "u", key: string): string;
    get tagsKey(): string;
    getJSON<T>(key: string): Promise<T | undefined>;
    setJSON(key: string, value: unknown, ttlSeconds: number): Promise<void>;
    del(key: string): Promise<void>;
    /** Latest revalidation time (ms) across tags, from Valkey. */
    tagsExpiredAt(tags: string[]): Promise<number>;
    /** Marks tags revalidated now (every instance sees it). */
    revalidate(tags: string[], at?: number): Promise<void>;
    /** Pulls every tag timestamp into the local map. */
    refreshTags(): Promise<void>;
}
/** Reads Next.js's BUILD_ID from a .next directory (default: ./.next). */
export declare function readBuildId(distDir?: string): string | undefined;
