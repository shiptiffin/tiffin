/**
 * `@shiptiffin/sdk/kv`: small helpers over the project's Valkey namespace, using
 * Bun's built-in Redis client.
 *
 * ```ts
 * import { kv } from "@shiptiffin/sdk/kv";
 *
 * const store = kv();
 * await store.set("greeting", "hello", { ttl: 60 });   // seconds
 * await store.get("greeting");                         // "hello"
 * await store.setJSON("user:1", { name: "Ada" });
 * await store.getJSON<{ name: string }>("user:1");
 * await store.incr("visits");
 * const rl = await store.rateLimit(`login:${ip}`, { limit: 5, windowSec: 60 });
 * if (!rl.allowed) return new Response("slow down", { status: 429, headers: { "retry-after": String(rl.retryAfterSec) } });
 * ```
 *
 * Every key is prefixed with VALKEY_PREFIX ("p_<project>:"): the box gives
 * each project an ACL user that can only touch its own prefix, so these
 * helpers add it for you. With your own client, prefix keys yourself.
 * The connection defaults to `new Bun.RedisClient(process.env.REDIS_URL)`.
 */
/** The subset of Bun's RedisClient these helpers use. */
export interface RedisLike {
    send(command: string, args: string[]): Promise<unknown>;
}
export interface SetOptions {
    /** Expire after this many seconds. */
    ttl?: number;
    /** Only set when the key does not exist (returns false when it did). */
    nx?: boolean;
}
export interface RateLimitResult {
    allowed: boolean;
    /** Requests left in the current window. */
    remaining: number;
    limit: number;
    /** Seconds until the window resets. */
    retryAfterSec: number;
}
/** Key/value helpers bound to one prefix. */
export declare class KV {
    readonly client: RedisLike;
    readonly prefix: string;
    constructor(client: RedisLike, prefix: string);
    /** The full key in Valkey: prefix + key. */
    key(k: string): string;
    get(key: string): Promise<string | null>;
    /** Set a string. Returns false when nx was set and the key existed. */
    set(key: string, value: string, opts?: SetOptions): Promise<boolean>;
    getJSON<T>(key: string): Promise<T | null>;
    setJSON(key: string, value: unknown, opts?: SetOptions): Promise<boolean>;
    /** Delete keys. Returns how many existed. */
    del(...keys: string[]): Promise<number>;
    /** Add `by` (default 1) and return the new value. */
    incr(key: string, by?: number): Promise<number>;
    /** Set a key's expiry in seconds. Returns false when the key does not exist. */
    expire(key: string, ttl: number): Promise<boolean>;
    /** Seconds until the key expires: -1 never, -2 missing. */
    ttl(key: string): Promise<number>;
    /**
     * Fixed-window rate limit: at most `limit` hits per `windowSec` for `key`.
     * Atomic (one Lua script), so concurrent requests can't slip past.
     */
    rateLimit(key: string, opts: {
        limit: number;
        windowSec: number;
    }): Promise<RateLimitResult>;
    /** Remember the result of fn for ttl seconds (JSON-serialisable values). */
    cached<T>(key: string, ttl: number, fn: () => Promise<T>): Promise<T>;
}
/**
 * The project's KV namespace. Without arguments it connects with
 * `new Bun.RedisClient(process.env.REDIS_URL)` once and uses VALKEY_PREFIX.
 */
export declare function kv(client?: RedisLike, prefix?: string): KV;
