/**
 * `@shiptiffin/sdk/kv`: the project's key-value store (Valkey), with no setup.
 *
 * ```ts
 * import { kv } from "@shiptiffin/sdk/kv";
 *
 * const store = kv();                                  // REDIS_URL + VALKEY_PREFIX
 * await store.set("user:1", { name: "Ada" }, { ex: 3600 });
 * const user = await store.get<{ name: string }>("user:1");
 * await store.incr("visits");
 * await store.zadd("scores", { score: 42, member: "ada" });
 * await store.zrange("scores", 0, 9, { rev: true, withScores: true });
 *
 * const rl = await store.rateLimit(`login:${ip}`, { limit: 5, window: "1 m" });
 * if (!rl.allowed) return new Response("Slow down", { status: 429, headers: { "retry-after": String(rl.retryAfter) } });
 *
 * const posts = await store.cached("posts:latest", 60, () => db.query.posts.findMany());
 * ```
 *
 * - Keys: every key gets the project's prefix (VALKEY_PREFIX, "p_<project>:")
 *   and keys that come back (scan, keys) lose it, so code only sees its own
 *   names. `store.key("x")` is the full name, for your own clients.
 * - Values: like @upstash/redis, strings are stored as is and anything else as
 *   JSON; reads parse JSON back (`get<T>()`). A string that is valid JSON
 *   ("42", "true") reads back parsed; `kv({ json: false })` returns raw strings.
 * - Connection: one per process, opened on first use. Bun's built-in
 *   RedisClient on Bun, the SDK's own client on Node (no dependencies).
 *   Calls in the same tick share one round trip; a dropped connection is
 *   reopened (with backoff); a call fails after `timeoutMs` (5 s).
 * - Errors are KVError with the server's code ("NOPERM", "OOM", ...) and a
 *   message that says what to do (e.g. when the project is over its memory limit).
 * - Moving from @upstash/redis or @vercel/kv: the method names and options
 *   match. Differences: `zrange(..., { withScores: true })` returns
 *   `{ member, score }[]`; `set` returns a boolean; `rateLimit` is built in.
 */
import { KVError, type Conn, type RedisLike } from "./kv/conn.js";
import { type RateLimitOptions, type RateLimitResult } from "./kv/ratelimit.js";
export { KVError, type RedisLike, type RateLimitOptions, type RateLimitResult };
export type { Window } from "./kv/ratelimit.js";
export interface KVOptions {
    /** Connection URL (default REDIS_URL): redis://, rediss://, redis+unix://. */
    url?: string;
    /** Key prefix (default VALKEY_PREFIX). */
    prefix?: string;
    /** Store and parse JSON (default true). false: values are plain strings. */
    json?: boolean;
    /** A call fails after this long without a reply (default 5000). */
    timeoutMs?: number;
    /** Use this client instead of connecting: Bun.redis, or anything with send(command, args). */
    client?: RedisLike;
    /** Which connection to open: "bun" (Bun's RedisClient, the default on Bun) or "resp" (the SDK's own; the default elsewhere). */
    driver?: "bun" | "resp";
}
export interface SetOptions {
    /** Expire after this many seconds. */
    ex?: number;
    /** Expire after this many milliseconds. */
    px?: number;
    /** Expire at this Unix time (seconds). */
    exat?: number;
    /** Expire at this Unix time (milliseconds). */
    pxat?: number;
    /** Only set if the key does not exist. */
    nx?: boolean;
    /** Only set if the key exists. */
    xx?: boolean;
    /** Keep the key's current expiry. */
    keepTtl?: boolean;
}
export interface ScoreMember<T = unknown> {
    score: number;
    member: T;
}
export interface ZAddOptions {
    /** Only add new members. */
    nx?: boolean;
    /** Only update existing members. */
    xx?: boolean;
    /** Only update when the new score is greater. */
    gt?: boolean;
    /** Only update when the new score is less. */
    lt?: boolean;
    /** Return how many changed (added or updated), not only added. */
    ch?: boolean;
}
export interface ZRangeOptions {
    /** Highest score first. */
    rev?: boolean;
    /** start and stop are scores ("-inf", "+inf", "(5" for exclusive), not ranks. With rev, pass max then min. */
    byScore?: boolean;
    /** With byScore: skip offset, return at most count. */
    limit?: {
        offset: number;
        count: number;
    };
}
export interface ScanOptions {
    /** Keys per round trip, a hint (default 100). */
    count?: number;
    /** Only keys of this type: "string", "hash", "list", "set", "zset". */
    type?: string;
}
export interface CachedOptions {
    /**
     * Seconds past `ttl` that the old value is still served while one caller
     * refreshes it (default: ttl). 0: everyone waits for the fresh value.
     */
    stale?: number;
}
type Decode<T> = (r: unknown) => T;
/**
 * The data commands, shared by the store (runs them now) and pipelines
 * (queue them). Every method returns a promise of its decoded reply.
 */
export declare abstract class Commands {
    readonly prefix: string;
    /** Whether values are stored and read as JSON. */
    readonly json: boolean;
    constructor(prefix: string, 
    /** Whether values are stored and read as JSON. */
    json: boolean);
    protected abstract run<T>(args: string[], decode: Decode<T>): Promise<T>;
    /** The full key in Valkey: prefix + name (unchanged if it already has the prefix). */
    key(name: string): string;
    protected keyList(names: (string | string[])[]): string[];
    /** A value as stored: strings as is, anything else as JSON. */
    protected enc(v: unknown): string;
    /** A stored value back: JSON parsed (as @upstash/redis does), else the string. */
    protected dec: (v: unknown) => unknown;
    protected list: <T>(r: unknown) => T[];
    /** True when Valkey answers. */
    ping(): Promise<boolean>;
    /** The value, or null when the key does not exist. */
    get<T = unknown>(key: string): Promise<T | null>;
    /**
     * Set a value (objects as JSON). Returns false when `nx`/`xx` stopped it.
     * @example await store.set("session:abc", { userId: 1 }, { ex: 3600 })
     */
    set(key: string, value: unknown, opts?: SetOptions): Promise<boolean>;
    /** Get the value and delete the key. */
    getdel<T = unknown>(key: string): Promise<T | null>;
    /** Values of several keys, null for missing ones. Takes names or one array. */
    mget<T extends unknown[] = unknown[]>(...keys: (string | string[])[]): Promise<{
        [I in keyof T]: T[I] | null;
    }>;
    /** Set several keys at once. */
    mset(values: Record<string, unknown>): Promise<void>;
    /** Delete keys; returns how many existed. */
    del(...keys: (string | string[])[]): Promise<number>;
    /** How many of the keys exist. */
    exists(...keys: (string | string[])[]): Promise<number>;
    /** Expire the key after `seconds`. False when it does not exist. */
    expire(key: string, seconds: number): Promise<boolean>;
    /** Seconds left: -1 no expiry, -2 no such key. */
    ttl(key: string): Promise<number>;
    /** Remove the expiry. False when there was none (or no key). */
    persist(key: string): Promise<boolean>;
    /** Add 1; returns the new value (a missing key counts as 0). */
    incr(key: string): Promise<number>;
    incrby(key: string, by: number): Promise<number>;
    incrbyfloat(key: string, by: number): Promise<number>;
    decr(key: string): Promise<number>;
    decrby(key: string, by: number): Promise<number>;
    /**
     * Set fields; returns how many were new.
     * @example await store.hset("job:1", { status: "running", progress: 0.4 })
     */
    hset(key: string, fields: Record<string, unknown>): Promise<number>;
    hget<T = unknown>(key: string, field: string): Promise<T | null>;
    /** All fields, or null when the hash does not exist. */
    hgetall<T extends Record<string, unknown> = Record<string, unknown>>(key: string): Promise<T | null>;
    hdel(key: string, ...fields: string[]): Promise<number>;
    hincrby(key: string, field: string, by: number): Promise<number>;
    /** Add to the front; returns the new length. */
    lpush(key: string, ...values: unknown[]): Promise<number>;
    /** Add to the end; returns the new length. */
    rpush(key: string, ...values: unknown[]): Promise<number>;
    /** Items from start to stop, inclusive; -1 is the last. */
    lrange<T = unknown>(key: string, start: number, stop: number): Promise<T[]>;
    /** Keep only start..stop, e.g. ltrim(key, 0, 99) after lpush for the latest 100. */
    ltrim(key: string, start: number, stop: number): Promise<void>;
    llen(key: string): Promise<number>;
    /** Remove and return the first item (or the first `count`). */
    lpop<T = unknown>(key: string): Promise<T | null>;
    lpop<T = unknown>(key: string, count: number): Promise<T[] | null>;
    /** Remove and return the last item (or the last `count`). */
    rpop<T = unknown>(key: string): Promise<T | null>;
    rpop<T = unknown>(key: string, count: number): Promise<T[] | null>;
    private pop;
    sadd(key: string, ...members: unknown[]): Promise<number>;
    srem(key: string, ...members: unknown[]): Promise<number>;
    smembers<T = unknown>(key: string): Promise<T[]>;
    sismember(key: string, member: unknown): Promise<boolean>;
    /**
     * Add members with scores; returns how many were added.
     * @example await store.zadd("leaderboard", { score: 120, member: "ada" }, { score: 80, member: "bob" })
     */
    zadd<T = unknown>(key: string, ...items: (ScoreMember<T> | ZAddOptions)[]): Promise<number>;
    /**
     * Members from start to stop (ranks, or scores with byScore).
     * @example await store.zrange("leaderboard", 0, 9, { rev: true, withScores: true }) // top 10
     */
    zrange<T = unknown>(key: string, start: number | string, stop: number | string, opts: ZRangeOptions & {
        withScores: true;
    }): Promise<ScoreMember<T>[]>;
    zrange<T = unknown>(key: string, start: number | string, stop: number | string, opts?: ZRangeOptions & {
        withScores?: false;
    }): Promise<T[]>;
    /** A member's rank (0 = lowest score; with rev, 0 = highest), or null. */
    zrank(key: string, member: unknown, opts?: {
        rev?: boolean;
    }): Promise<number | null>;
    zscore(key: string, member: unknown): Promise<number | null>;
    /** Add `by` to a member's score (adding the member at `by`); returns the new score. */
    zincrby(key: string, by: number, member: unknown): Promise<number>;
    zrem(key: string, ...members: unknown[]): Promise<number>;
    zcard(key: string): Promise<number>;
    /** Publish to a channel (prefixed like keys); returns how many subscribers got it. */
    publish(channel: string, message: unknown): Promise<number>;
}
/**
 * Commands sent together in one round trip by exec(); with multi(), as one
 * transaction (all or nothing, nothing in between). Each call returns a
 * promise of its own result, settled by exec(), which also returns them all.
 *
 * ```ts
 * const p = store.pipeline();
 * const views = p.incr("views");
 * p.expire("views", 3600);
 * const [n] = await p.exec<[number, boolean]>();   // or: await views
 * ```
 */
export declare class Pipeline extends Commands {
    private readonly conn;
    private readonly tx;
    private queue;
    constructor(conn: Conn, prefix: string, json: boolean, tx: boolean);
    protected run<T>(args: string[], decode: Decode<T>): Promise<T>;
    /** How many commands are queued. */
    get length(): number;
    /** Send the queued commands; the results in order. Throws the first error (after settling every promise). */
    exec<T extends unknown[] = unknown[]>(): Promise<T>;
}
/** Key-value store bound to one prefix. Get the project's with `kv()`. */
export declare class KV extends Commands {
    private readonly conn;
    private restScan;
    constructor(conn: Conn, prefix?: string, json?: boolean);
    protected run<T>(args: string[], decode: Decode<T>): Promise<T>;
    /** The client underneath: Bun's RedisClient, the SDK's RESP client, or the one you passed. */
    get client(): unknown;
    /**
     * Any command, as is: no prefix added, reply not decoded. Use `key()` for key names.
     * @example await store.command("HLEN", store.key("job:1"))
     */
    command<T = unknown>(cmd: string, ...args: (string | number)[]): Promise<T>;
    /** Queue commands and send them in one round trip with exec(). */
    pipeline(): Pipeline;
    /** Like pipeline(), but exec() runs them as one transaction (MULTI/EXEC). */
    multi(): Pipeline;
    /** The same store without JSON: values are plain strings. */
    raw(): KV;
    /**
     * The project's keys matching a glob pattern ("user:*"), prefix removed,
     * a page at a time. On a Tiffin box apps may not run SCAN themselves (it
     * would name other projects' keys), so it goes through the box's KV
     * endpoint, which lists only this project's keys.
     * @example for await (const key of store.scan("session:*")) await store.del(key)
     */
    scan(match?: string, opts?: ScanOptions): AsyncGenerator<string>;
    /** Every key matching the pattern (a scan collected into an array). */
    keys(match?: string): Promise<string[]>;
    private scanPage;
    /**
     * Allow at most `limit` calls per `window` for `key` (say a user id or IP).
     * Atomic in Valkey (one Lua script), so parallel requests can't slip past,
     * and app instances share it. Refused calls don't count.
     * @example
     * const rl = await store.rateLimit(`api:${userId}`, { limit: 100, window: "1 m" });
     * if (!rl.allowed) return new Response("Too many requests", { status: 429, headers: { "retry-after": String(rl.retryAfter) } });
     */
    rateLimit(key: string, opts: RateLimitOptions): Promise<RateLimitResult>;
    /**
     * The value of `fn()`, kept for `ttl` seconds. When it expires, one caller
     * refreshes it (a short lock) while the others get the old value, for up to
     * `stale` more seconds; on a cold start the others wait for that one
     * instead of all running `fn`. Values must be JSON.
     * @example const stats = await store.cached("stats", 300, () => computeStats())
     */
    cached<T>(key: string, ttl: number, fn: () => Promise<T> | T, opts?: CachedOptions): Promise<T>;
    /** Close the connection (scripts don't need to: an idle connection lets the process exit). */
    close(): void;
}
/**
 * The project's KV store. Without options: REDIS_URL and VALKEY_PREFIX from
 * the environment (the box sets both for apps with `services.valkey`), one
 * shared connection per process. With options: a new store.
 *
 * @example
 * import { kv } from "@shiptiffin/sdk/kv";
 * await kv().set("hello", "world", { ex: 60 });
 */
export declare function kv(opts?: KVOptions): KV;
