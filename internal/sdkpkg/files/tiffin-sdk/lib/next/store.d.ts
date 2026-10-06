/**
 * The storage behind `tiffin-sdk/next`'s cache handlers: entries and tag
 * revalidations in Valkey, shared by every instance of the app, with a
 * bounded in-process copy in front.
 *
 * Keys (all under VALKEY_PREFIX, the project's ACL prefix):
 *   <prefix>next:<app>:<env>:<deploy>:e:<key>   incremental cache entries (ISR, route handlers, fetch)
 *   <prefix>next:<app>:<env>:<deploy>:u:<key>   "use cache" entries
 *   <prefix>next:<app>:<env>:tags               hash: s:<tag> / x:<tag> → when the tag went stale / expires (ms)
 *   <prefix>next:<app>:<env>:tagv               counter, bumped on every revalidation
 *
 * <env> is "prod" or "pr-<preview>", <deploy> the deploy (TIFFIN_DEPLOY). Next.js
 * gives every build the same BUILD_ID once deploymentId is set, so entries are
 * keyed by deploy: a new release or a preview never serves pages another one
 * rendered, and a rollback finds its own entries again. Tags are shared by all
 * deploys of an environment, so a revalidation outlives a redeploy.
 *
 * Entries are stored as bytes (a JSON header plus raw buffers), not base64 JSON.
 * Each request reads the tag counter (one small GET, pipelined with the entry
 * read); the tag hash is fetched only when the counter moved.
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
 * The client the handlers use by default: a connection to REDIS_URL (Tiffin
 * sets it when the project has `services.valkey`) on Bun or Node, or an
 * in-memory store when REDIS_URL is unset and during `next build`.
 */
export declare function defaultClient(): RedisLike;
export declare function warnOnce(msg: string): void;
/** When a tag went stale and when it expires (ms since epoch), as Next.js keeps them. */
export interface TagState {
    stale?: number;
    expired?: number;
}
/** Settings shared by both handlers. */
export interface StoreOptions {
    /** Defaults to defaultClient(). */
    client?: RedisLike;
    /** Key prefix. Default: VALKEY_PREFIX + "next:" + TIFFIN_APP + ":". */
    prefix?: string;
    /** Environment the tags and entries belong to. Default: "pr-" + TIFFIN_PREVIEW, else "prod". */
    environment?: string;
    /** Deploy that entries are keyed by. Default: TIFFIN_DEPLOY, else NEXT_DEPLOYMENT_ID, else .next/BUILD_ID. */
    buildId?: string;
    /** Upper bound for entry TTLs in seconds. Default 30 days. */
    maxTtlSeconds?: number;
    /** Size of the in-process copy of entries, in bytes. Default 32 MB; 0 turns it off. */
    memoryBytes?: number;
    /** Next.js's in-process tag state (found automatically from the app's `next`). */
    tagsManifest?: Map<string, TagState> | null;
}
/** Configure both handlers, e.g. to pass your own client. Call before Next.js starts serving. */
export declare function configure(o: StoreOptions): void;
/** An entry as kept in memory: its header (`meta`) and value. */
export interface Item<M = Record<string, unknown>> {
    meta: M & {
        fresh?: number;
    };
    value: unknown;
    size: number;
    /** Until when the in-memory copy may be served without asking Valkey. */
    until: number;
}
export declare class Store {
    readonly client: RedisLike;
    readonly prefix: string;
    readonly environment: string;
    readonly buildId: string;
    readonly maxTtl: number;
    /** Tag states of this environment, as of `version`. */
    readonly tagTimes: Map<string, TagState>;
    /** The tag counter value the map reflects (undefined: re-read the hash). */
    private version;
    private applied;
    private syncing;
    /** This process's revalidations: a sequence number, the last one Valkey confirmed, and per tag the latest. */
    private seq;
    private acked;
    private readonly own;
    private readonly lru;
    private readonly reads;
    private readonly writes;
    private readonly manifest;
    /** The store for these options, shared by every handler in the process. */
    static open(o?: StoreOptions): Store;
    constructor(o?: StoreOptions);
    /** Whether Next.js itself can be told a tag is stale (otherwise stale counts as expired). */
    get signalsStale(): boolean;
    entryKey(kind: "e" | "u", key: string): string;
    get tagsKey(): string;
    get versionKey(): string;
    private get binary();
    private send;
    private pipeline;
    /**
     * Reads an entry: the in-memory copy, else Valkey (one read per key at a
     * time). `fromValkey` skips the in-memory copy, for when it turned out to be
     * outdated by a revalidation (another instance may have rendered anew).
     */
    read<M>(kind: "e" | "u", key: string, fromValkey?: boolean): Promise<Item<M> | undefined>;
    /**
     * Stores an entry in Valkey. `meta.fresh` (ms) is when it goes stale; until then a local copy needs no check.
     * `ifAbsent` leaves an entry already there alone (SET NX).
     */
    write(kind: "e" | "u", key: string, meta: Record<string, unknown>, value: unknown, ttlSeconds: number, ifAbsent?: boolean): Promise<void>;
    del(kind: "e" | "u", key: string): Promise<void>;
    /** Mirrors Next.js's areTagsExpired: a tag expired (by now) after the entry was made. */
    expired(tags: Iterable<string>, at: number): boolean;
    /** Mirrors Next.js's areTagsStale: a tag was marked stale after the entry was made. */
    stale(tags: Iterable<string>, at: number): boolean;
    /**
     * Revalidates tags on every instance, the way Next.js's own handlers do:
     * without durations they expire now; with durations they are stale now
     * (served once more while regenerating) and expire after `expire` seconds.
     */
    updateTags(tags: string[], durations?: {
        expire?: number;
    }): Promise<void>;
    /**
     * Brings the tag map up to date: one GET of the counter, the hash only when
     * it moved. Calls in the same tick share the GET (it has not gone out yet);
     * later ones send their own, so a request sees every revalidation that
     * finished before it began.
     */
    sync(): Promise<void>;
    private doSync;
    private setTag;
    private text;
    private bytes;
}
/** Reads Next.js's BUILD_ID from a .next directory (default: ./.next). */
export declare function readBuildId(distDir?: string): string | undefined;
export declare function encode(meta: Record<string, unknown>, value: unknown): Buffer;
export declare function decode(buf: Buffer): {
    meta: Record<string, unknown>;
    value: unknown;
};
